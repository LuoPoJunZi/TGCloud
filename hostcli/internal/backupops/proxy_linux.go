package backupops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

type ProxyBackupItem struct {
	Name       string `json:"name"`
	CreatedUTC string `json:"created_utc"`
	SizeBytes  int64  `json:"size_bytes"`
	Integrity  string `json:"integrity"`
}

type ProxyPrunePlan struct {
	Keep   int               `json:"keep"`
	Retain []string          `json:"retain"`
	Remove []ProxyBackupItem `json:"remove"`
	PlanID string            `json:"plan_id"`
}

type ManifestEntry struct {
	Kind   string `json:"kind"`
	Mode   int64  `json:"mode"`
	Size   *int64 `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}

type ProxyManifest struct {
	Version int                      `json:"version"`
	Files   map[string]ManifestEntry `json:"files"`
}

const proxyArchiveScript = `import hashlib, io, json, os, re, shutil, stat, sys, tarfile, tempfile, time, uuid
from pathlib import Path, PurePosixPath

COMPOSE_REG, COMPOSE_TX = "state/compose-instances.json", "state/compose-transaction.json"
SELECTION = ("state/domains.json", COMPOSE_REG, "docker-compose.yml", "nginx", "certbot/letsencrypt", "certbot/status", "external-auth")
ARCH_RE = re.compile(r"^proxy-([0-9]{8}T[0-9]{6}Z)-[0-9a-f]{32}\.tar\.gz$")
HEX64_RE = re.compile(r"^[0-9a-f]{64}$")
MAX_BYTES, MAX_ENTRIES = 512 * 1024 * 1024, 20000

def sha256_handle(h):
    d = hashlib.sha256()
    while chunk := h.read(1024 * 1024):
        d.update(chunk)
    return d.hexdigest()

def sha256_path(p):
    with p.open("rb") as h:
        return sha256_handle(h)

def safe_link(name, target):
    if not target or target.startswith("/") or "\\" in target or ":" in target:
        return False
    parts = list(PurePosixPath(name).parent.parts)
    for part in PurePosixPath(target).parts:
        if part == "..":
            if len(parts) <= 1:
                return False
            parts.pop()
        elif part != ".":
            parts.append(part)
    return bool(parts) and parts[0] == "proxy"

def validate_auth(state, records):
    if not isinstance(state, dict) or state.get("version") not in {1, 2} or state.get("project") != "tgcloud" or not isinstance(state.get("domains"), dict) or set(state["domains"]) - {"clouddrive2"}:
        raise ValueError("unrecognized proxy state")
    if state["version"] == 2:
        entries = state.get("external_webdav")
        if not isinstance(entries, dict) or set(entries) - {"clouddrive2"}:
            raise ValueError("invalid addon routes")
        for ed, rt in entries.items():
            if not isinstance(rt, dict) or rt.get("owner") != "tgcloud.external_webdav" or rt.get("auth_file") != f"external-auth/{ed}.htpasswd" or records.get(f"proxy/external-auth/{ed}.htpasswd", {}).get("kind") != "file":
                raise ValueError("missing addon auth")

def snapshot_proxy(root):
    if root.is_symlink() or not root.is_dir():
        raise ValueError("invalid proxy root")
    if (root / COMPOSE_TX).exists() or (root / COMPOSE_TX).is_symlink():
        raise ValueError("unfinished compose transaction")
    records, total = {}, 0
    for rel in SELECTION:
        start = root / rel
        if not start.exists() and not start.is_symlink():
            if rel in {"certbot/status", "external-auth", COMPOSE_REG}:
                continue
            raise ValueError("required proxy resource missing")
        for p in (start, *sorted(start.rglob("*"))):
            for par in p.parents:
                if par == root:
                    break
                if par.is_symlink():
                    raise ValueError("source traverses symlink")
            name = "proxy/" + p.relative_to(root).as_posix()
            info = p.lstat()
            rec = {"mode": stat.S_IMODE(info.st_mode)}
            if p.is_symlink():
                tgt = os.readlink(p)
                if not safe_link(name, tgt):
                    raise ValueError("unsafe symlink")
                rec.update(kind="symlink", target=tgt)
            elif p.is_file():
                total += info.st_size
                if total > MAX_BYTES:
                    raise ValueError("backup too large")
                rec.update(kind="file", size=info.st_size, sha256=sha256_path(p))
            elif p.is_dir():
                rec.update(kind="directory")
            else:
                raise ValueError("unsupported file")
            records[name] = rec
            if len(records) > MAX_ENTRIES:
                raise ValueError("too many entries")
    st_path = root / "state/domains.json"
    if st_path.stat().st_size > 65536:
        raise ValueError("state too large")
    validate_auth(json.loads(st_path.read_text(encoding="utf-8")), records)
    return records

def verify_archive(archive):
    if archive.is_symlink() or not archive.is_file() or not ARCH_RE.fullmatch(archive.name):
        raise ValueError("invalid archive")
    cs = archive.with_name(archive.name + ".sha256")
    if cs.is_symlink() or not cs.is_file():
        raise ValueError("missing checksum")
    expected = cs.read_text(encoding="ascii").strip()
    if not HEX64_RE.fullmatch(expected) or sha256_path(archive) != expected:
        raise ValueError("checksum mismatch")
    records, manifest, state, total = {}, None, None, 0
    with tarfile.open(archive, "r:gz") as tar:
        for m in tar:
            name = m.name
            parts = PurePosixPath(name).parts
            if not parts or parts[0] != "proxy" or ".." in parts or "\\" in name or ":" in name or str(PurePosixPath(name)) != name or name.startswith("/") or name in records or len(records) >= MAX_ENTRIES:
                raise ValueError("unsafe entry")
            rec = {"mode": m.mode & 0o7777}
            if m.isfile():
                total += m.size
                if m.size < 0 or total > MAX_BYTES:
                    raise ValueError("size limit")
                with tar.extractfile(m) as h:
                    if name == "proxy/manifest.json":
                        if manifest is not None or m.size > 8 * 1024 * 1024:
                            raise ValueError("invalid manifest")
                        manifest = json.load(h)
                        continue
                    if name == "proxy/state/domains.json":
                        if m.size > 65536:
                            raise ValueError("state too large")
                        raw = h.read()
                        state = json.loads(raw)
                        dig = hashlib.sha256(raw).hexdigest()
                    else:
                        dig = sha256_handle(h)
                rec.update(kind="file", size=m.size, sha256=dig)
            elif m.isdir():
                rec.update(kind="directory")
            elif m.issym() and safe_link(name, m.linkname):
                rec.update(kind="symlink", target=m.linkname)
            else:
                raise ValueError("unsupported entry")
            records[name] = rec
    if not isinstance(manifest, dict) or manifest.get("version") != 1 or manifest.get("files") != records:
        raise ValueError("manifest mismatch")
    for req in ("proxy/state/domains.json", "proxy/docker-compose.yml", "proxy/nginx/nginx.conf"):
        if records.get(req, {}).get("kind") != "file":
            raise ValueError("required missing")
    validate_auth(state, records)
    return records

def list_items(backup_root):
    if not backup_root.exists():
        return []
    archives = sorted((p for p in backup_root.iterdir() if ARCH_RE.fullmatch(p.name) and not p.is_symlink() and p.is_file()), reverse=True)
    if len(archives) > 500:
        raise ValueError("too many archives")
    items = []
    for a in archives:
        sz = a.stat().st_size
        ok = False
        try:
            if sz <= MAX_BYTES * 2:
                verify_archive(a)
                ok = True
        except Exception:
            pass
        ts = time.strftime("%Y-%m-%d %H:%M:%S UTC", time.strptime(a.name[6:22], "%Y%m%dT%H%M%SZ"))
        items.append({"name": a.name, "created_utc": ts, "size_bytes": sz, "integrity": "OK" if ok else "FAILED"})
    return items

def make_plan(backup_root, keep):
    if not 1 <= keep <= 50:
        raise ValueError("keep out of range")
    items = list_items(backup_root)
    if any(i["integrity"] != "OK" for i in items):
        raise ValueError("verify failed archive before pruning")
    ident = [[i, sha256_path(backup_root / i["name"])] for i in items]
    pid = hashlib.sha256(json.dumps({"keep": keep, "archives": ident}, sort_keys=True).encode()).hexdigest()
    return {"keep": keep, "retain": [i["name"] for i in items[:keep]], "remove": items[keep:], "plan_id": pid}

act = sys.argv[1]
if act == "list":
    print(json.dumps(list_items(Path(sys.argv[2]))))
elif act == "create":
    root, broot = Path(sys.argv[2]), Path(sys.argv[3])
    before = snapshot_proxy(root)
    name = f"proxy-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{uuid.uuid4().hex}.tar.gz"
    arch, cs = broot / name, broot / (name + ".sha256")
    try:
        fd = os.open(arch, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as out, tarfile.open(fileobj=out, mode="w:gz", dereference=False) as tar:
            for e in sorted(before):
                tar.add(root / e.removeprefix("proxy/"), arcname=e, recursive=False)
            mb = json.dumps({"version": 1, "files": before}, sort_keys=True).encode()
            ti = tarfile.TarInfo("proxy/manifest.json")
            ti.size, ti.mode = len(mb), 0o600
            tar.addfile(ti, io.BytesIO(mb))
        if snapshot_proxy(root) != before:
            raise ValueError("source changed")
        fd = os.open(cs, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="ascii") as h:
            h.write(sha256_path(arch) + "\n")
        recs = verify_archive(arch)
        print(json.dumps({"archive": str(arch), "entries": len(recs)}))
    except BaseException:
        arch.unlink(missing_ok=True)
        cs.unlink(missing_ok=True)
        raise
elif act == "verify":
    print(json.dumps(verify_archive(Path(sys.argv[2]))))
elif act == "drill":
    arch, broot, dst = Path(sys.argv[2]), Path(sys.argv[3]), Path(sys.argv[4])
    if dst.is_symlink() or not dst.is_dir() or dst.resolve().parent != broot.resolve() or any(dst.iterdir()):
        raise ValueError("invalid drill directory")
    recs = verify_archive(arch)
    with tarfile.open(arch, "r:gz") as tar:
        for name, rec in recs.items():
            t = dst.joinpath(*PurePosixPath(name).parts)
            t.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            if rec["kind"] == "directory":
                t.mkdir(mode=0o700, exist_ok=True)
            elif rec["kind"] == "file":
                fd = os.open(t, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
                with tar.extractfile(name) as s, os.fdopen(fd, "wb") as w:
                    shutil.copyfileobj(s, w)
                if sha256_path(t) != rec["sha256"]:
                    raise ValueError("checksum mismatch")
        for name, rec in recs.items():
            if rec["kind"] == "symlink":
                dst.joinpath(*PurePosixPath(name).parts).symlink_to(rec["target"])
    validate_auth(json.loads((dst / "proxy/state/domains.json").read_text(encoding="utf-8")), recs)
    print(len(recs))
elif act == "prune-preview":
    print(json.dumps(make_plan(Path(sys.argv[2]), int(sys.argv[3]))))
elif act == "prune":
    broot, keep, expected = Path(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
    if not HEX64_RE.fullmatch(expected):
        raise ValueError("missing plan")
    plan = make_plan(broot, keep)
    if plan["plan_id"] != expected:
        raise ValueError("plan changed")
    for item in plan["remove"]:
        a = broot / item["name"]
        if a.is_symlink() or not a.is_file() or not ARCH_RE.fullmatch(a.name):
            raise ValueError("unsafe target")
        a.unlink()
        (broot / (item["name"] + ".sha256")).unlink()
    print(json.dumps(plan))
else:
    raise ValueError("unknown action")
`

func runProxyHelperCtx(parentCtx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 180*time.Second)
	defer cancel()
	return runSnapshotPython(ctx, proxyArchiveScript, nil, args...)
}

func runProxyHelper(args ...string) ([]byte, error) {
	return runProxyHelperCtx(context.Background(), args...)
}

func ListProxyBackups(backupRoot string) ([]ProxyBackupItem, error) {
	if !hostutil.SafePath(backupRoot, false) {
		return nil, errors.New("代理备份目录路径不安全")
	}
	info, err := os.Lstat(backupRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []ProxyBackupItem{}, nil
	}
	if err != nil || !info.IsDir() {
		return nil, errors.New("代理备份目录不可读")
	}
	raw, err := runProxyHelper("list", backupRoot)
	if err != nil {
		return nil, errors.New("无法读取代理备份目录")
	}
	var items []ProxyBackupItem
	if json.Unmarshal(raw, &items) != nil {
		return nil, errors.New("代理备份清单解析失败")
	}
	return items, nil
}

func CreateProxyBackup(proxyRoot, backupRoot string) (string, int, error) {
	if !hostutil.SafePath(proxyRoot, false) {
		return "", 0, errors.New("代理目录路径不安全")
	}
	if err := ensurePrivateDir(backupRoot); err != nil {
		return "", 0, err
	}
	raw, err := runProxyHelper("create", proxyRoot, backupRoot)
	if err != nil {
		return "", 0, errors.New("创建或校验共享代理备份失败；请核对代理状态、权限与磁盘空间")
	}
	var res struct {
		Archive string `json:"archive"`
		Entries int    `json:"entries"`
	}
	if json.Unmarshal(raw, &res) != nil || res.Archive == "" || res.Entries <= 0 {
		return "", 0, errors.New("代理备份结果无法解析")
	}
	return res.Archive, res.Entries, nil
}

func VerifyProxyBackup(archivePath string) (map[string]ManifestEntry, error) {
	if !hostutil.SafePath(archivePath, false) || !proxyArchiveRegex.MatchString(filepath.Base(archivePath)) {
		return nil, errors.New("代理备份归档无效")
	}
	raw, err := runProxyHelper("verify", archivePath)
	if err != nil {
		return nil, errors.New("代理备份归档或摘要校验未通过")
	}
	var records map[string]ManifestEntry
	if json.Unmarshal(raw, &records) != nil {
		return nil, errors.New("代理备份清单解析失败")
	}
	return records, nil
}

func RecoveryDrill(archivePath, backupRoot string) (int, error) {
	sigCtx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSig()
	return recoveryDrillCtx(sigCtx, archivePath, backupRoot)
}

func recoveryDrillCtx(parentCtx context.Context, archivePath, backupRoot string) (int, error) {
	if !hostutil.SafePath(archivePath, false) || !proxyArchiveRegex.MatchString(filepath.Base(archivePath)) {
		return 0, errors.New("代理备份归档无效")
	}
	if err := ensurePrivateDir(backupRoot); err != nil {
		return 0, err
	}
	drillDir, err := createOwnedTempDir(backupRoot, "recovery-drill-")
	if err != nil {
		return 0, err
	}
	defer removeOwnedChild(backupRoot, drillDir)
	raw, err := runProxyHelperCtx(parentCtx, "drill", archivePath, backupRoot, drillDir)
	if err != nil {
		if parentCtx.Err() != nil {
			return 0, errors.New("共享代理备份隔离恢复演练被中断，已清理临时目录")
		}
		return 0, errors.New("共享代理备份隔离恢复演练未通过")
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || count <= 0 {
		return 0, errors.New("无法解析隔离恢复演练结果")
	}
	return count, nil
}

func PlanProxyPrune(backupRoot string, keep int) (ProxyPrunePlan, error) {
	if keep < 1 || keep > 50 {
		return ProxyPrunePlan{}, errors.New("保留份数必须在 1～50 之间")
	}
	if !hostutil.SafePath(backupRoot, false) {
		return ProxyPrunePlan{}, errors.New("代理备份目录路径不安全")
	}
	raw, err := runProxyHelper("prune-preview", backupRoot, strconv.Itoa(keep))
	if err != nil {
		return ProxyPrunePlan{}, errors.New("生成代理备份清理计划失败；请先核对已有备份完整性")
	}
	var plan ProxyPrunePlan
	if json.Unmarshal(raw, &plan) != nil {
		return ProxyPrunePlan{}, errors.New("代理备份清理计划解析失败")
	}
	return plan, nil
}

func ExecuteProxyPrune(backupRoot string, keep int, expectedPlanID string) (ProxyPrunePlan, error) {
	if !sha256Regex.MatchString(expectedPlanID) {
		return ProxyPrunePlan{}, errors.New("缺少已确认的清理计划编号")
	}
	if keep < 1 || keep > 50 {
		return ProxyPrunePlan{}, errors.New("保留份数必须在 1～50 之间")
	}
	if !hostutil.SafePath(backupRoot, false) {
		return ProxyPrunePlan{}, errors.New("代理备份目录路径不安全")
	}
	raw, err := runProxyHelper("prune", backupRoot, strconv.Itoa(keep), expectedPlanID)
	if err != nil {
		return ProxyPrunePlan{}, errors.New("备份清单已变化或清理未完成，请重新预览确认")
	}
	var plan ProxyPrunePlan
	if json.Unmarshal(raw, &plan) != nil {
		return ProxyPrunePlan{}, errors.New("代理备份清理结果解析失败")
	}
	return plan, nil
}
