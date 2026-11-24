package backupops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

const (
	snapshotMaxBytes = 2 * 1024 * 1024 * 1024
	snapshotMaxFiles = 20000
	snapshotMinFree  = 256 * 1024 * 1024
	snapshotDBName   = "tgcloud.db"
)

var (
	snapshotOptional = []string{"tgcloud-release.json", "tgcloud-source.json", "tgcloud-compose.json", "compose-bundle.json", "compose-access.json"}
	lockInstanceFunc = inspect.LockInstance
	activeTasksFunc  = queryActiveTasks
	snapshotPyBin    = "python3"
)

type containerState struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
}

const activeTasksScript = `import sqlite3, sys
from contextlib import closing
from pathlib import Path
db = Path(sys.argv[1]).resolve()
with closing(sqlite3.connect(db.as_uri() + "?mode=ro", uri=True, timeout=5)) as c:
    n = c.execute("SELECT count(*) FROM tasks WHERE state IN ('reserved','downloading','streaming','uploading','verifying','finalizing','cleanup_pending')").fetchone()[0]
print(int(n))`

const snapshotBundleScript = `import hashlib, io, json, os, re, secrets, shutil, sqlite3, sys, tarfile
from contextlib import closing
from pathlib import Path, PurePosixPath

MAX_BYTES, MAX_FILES, MIN_FREE = 2 * 1024**3, 20000, 256 * 1024**2
DB_NAME = "tgcloud.db"
SIDECARS = ("-wal", "-shm", "-journal")
OPTIONAL = {"tgcloud-release.json", "tgcloud-source.json", "tgcloud-compose.json", "compose-bundle.json", "compose-access.json", "data/listener/personal.session"}

def sha256_file(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        while chunk := f.read(1024 * 1024):
            h.update(chunk)
    return h.hexdigest()

def quick_check(path):
    with closing(sqlite3.connect(path.resolve().as_uri() + "?mode=ro&immutable=1", uri=True)) as c:
        if c.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
            raise ValueError("database integrity failed")

def backup_db(src, dst):
    dst.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    tmp = dst.with_name(f".{dst.name}.tmp-{os.getpid()}-{secrets.token_hex(6)}")
    fd = os.open(tmp, os.O_CREAT | os.O_EXCL | os.O_RDWR | getattr(os, "O_NOFOLLOW", 0), 0o600)
    os.close(fd)
    try:
        with closing(sqlite3.connect(src)) as orig, closing(sqlite3.connect(tmp)) as copy:
            orig.backup(copy)
            if copy.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
                raise ValueError("sqlite backup quick_check failed")
        os.chmod(tmp, 0o600)
        with tmp.open("r+b") as h:
            os.fsync(h.fileno())
        os.replace(tmp, dst)
    finally:
        tmp.unlink(missing_ok=True)

def valid_name(name):
    p = PurePosixPath(name)
    if not name or p.is_absolute() or ".." in p.parts or "\\" in name or ":" in name or str(p) != name or any(ord(c) < 32 for c in name):
        raise ValueError("unsafe archive path")

def parse_ver(v):
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", v):
        raise ValueError("invalid version")
    return tuple(map(int, v.split(".")))

def check_preflight(archive, scratch, extract_root, target_ver):
    target = parse_ver(target_ver)
    if archive.is_symlink() or scratch.is_symlink() or not scratch.is_dir() or extract_root.is_symlink() or not extract_root.is_dir():
        raise ValueError("invalid archive/scratch")
    if extract_root.resolve().parent != scratch.resolve() or any(extract_root.iterdir()):
        raise ValueError("invalid preflight directory")
    with tarfile.open(archive, "r:gz") as tar:
        first = tar.next()
        if first is None or first.name != "manifest.json" or not first.isfile() or first.size > 8 * 1024**2:
            raise ValueError("missing manifest")
        manifest = json.load(tar.extractfile(first))
        src_ver = parse_ver(manifest["version"])
        if manifest.get("format") != 1 or manifest.get("edition") != "clouddrive2" or src_ver > target or src_ver[0] != target[0]:
            raise ValueError("edition/version incompatible")
        records = manifest.get("files")
        if not isinstance(records, dict) or not records or len(records) > MAX_FILES:
            raise ValueError("incomplete manifest")
        req = {".env", "docker-compose.yml", "data/" + DB_NAME, "data/bot.session"}
        if not req.issubset(records):
            raise ValueError("incomplete manifest")
        for k in records:
            p = PurePosixPath(k)
            if p.parent.as_posix() == "data" and p.suffix in {".db", ".sqlite", ".sqlite3"} and p.name != DB_NAME:
                raise ValueError("ambiguous database entry")
        if not any(k.startswith("config/") for k in records) or not any(k.startswith("clouddrive/config/") for k in records):
            raise ValueError("configuration/gateway coverage incomplete")
        total = 0
        for name, rec in records.items():
            valid_name(name)
            if name not in (req | OPTIONAL) and not name.startswith(("config/", "clouddrive/config/")):
                raise ValueError("entry outside snapshot scope")
            if name.endswith(SIDECARS):
                raise ValueError("live sidecar in archive")
            if type(rec.get("size")) is not int or rec["size"] < 0:
                raise ValueError("invalid size")
            total += rec["size"]
        if total > MAX_BYTES or shutil.disk_usage(scratch).free < total + MIN_FREE:
            raise ValueError("restore capacity limit")
        root = extract_root.resolve()
        root.chmod(0o700)
        seen = set()
        for item in iter(tar.next, None):
            valid_name(item.name)
            if item.name not in records or item.name in seen or not item.isfile():
                raise ValueError("unexpected/duplicate/link entry")
            rec = records[item.name]
            if item.size != rec["size"]:
                raise ValueError("size mismatch")
            dest = root / item.name
            dest.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with tar.extractfile(item) as src, dest.open("xb") as out:
                dest.chmod(0o600)
                shutil.copyfileobj(src, out, length=1024 * 1024)
            if sha256_file(dest) != rec["sha256"]:
                raise ValueError("checksum mismatch")
            if dest.suffix in {".db", ".session"}:
                quick_check(dest)
            seen.add(item.name)
        if seen != set(records):
            raise ValueError("missing entries")
        return len(records)

mode = sys.argv[1]
if mode == "preflight":
    print(check_preflight(Path(sys.argv[2]), Path(sys.argv[3]), Path(sys.argv[4]), sys.argv[5]))
elif mode == "pack":
    spec = json.loads(sys.stdin.read())
    dest_dir, archive, ver = Path(spec["dest_dir"]), Path(spec["archive"]), spec["version"]
    snap, preflight_dir = Path(spec["stage_dir"]).resolve(), Path(spec["preflight_dir"]).resolve()
    if snap.is_symlink() or not snap.is_dir() or snap.parent != dest_dir.resolve() or any(snap.iterdir()):
        raise ValueError("invalid stage directory")
    snap.chmod(0o700)
    files = spec["files"]
    records = {}
    total = 0
    for rel in sorted(files):
        src = Path(files[rel])
        if src.resolve() != src or src.is_symlink() or not src.is_file():
            raise ValueError("source changed")
        dst = snap / rel
        dst.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
        with src.open("rb") as r:
            is_sqlite = r.read(16) == b"SQLite format 3\x00"
        if is_sqlite:
            backup_db(src, dst)
        else:
            if rel.endswith((".db", ".session")):
                raise ValueError("invalid sqlite file")
            with src.open("rb") as r, dst.open("xb") as w:
                dst.chmod(0o600)
                shutil.copyfileobj(r, w, 1024 * 1024)
        st = dst.stat()
        total += st.st_size
        if total > MAX_BYTES or len(records) >= MAX_FILES:
            raise ValueError("snapshot limit exceeded")
        records[rel] = {"size": st.st_size, "sha256": sha256_file(dst)}
    if not any(k.startswith("config/") for k in records) or not any(k.startswith("clouddrive/config/") for k in records):
        raise ValueError("configuration/gateway coverage incomplete")
    manifest = {"format": 1, "edition": "clouddrive2", "version": ver, "files": records,
                "excluded": ["downloads", "logs", "shared-https"], "source": "operator-offline-copy"}
    fd = os.open(archive, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as out, tarfile.open(fileobj=out, mode="w:gz") as tar:
            mb = json.dumps(manifest, ensure_ascii=False).encode()
            ti = tarfile.TarInfo("manifest.json")
            ti.size, ti.mode = len(mb), 0o600
            tar.addfile(ti, io.BytesIO(mb))
            for rel, rec in records.items():
                p = snap / rel
                with p.open("rb") as h:
                    if sha256_file(p) != rec["sha256"]:
                        raise ValueError("snapshot changed")
                    ti = tarfile.TarInfo(rel)
                    ti.size, ti.mode = rec["size"], 0o600
                    tar.addfile(ti, h)
        print(check_preflight(archive, archive.parent, preflight_dir, ver))
    except BaseException:
        archive.unlink(missing_ok=True)
        raise
else:
    raise ValueError("unknown mode")
`

func createOwnedTempDir(parent, prefix string) (string, error) {
	dir, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", errors.New("无法创建私密暂存目录")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", errors.New("无法设置私密暂存目录权限")
	}
	return dir, nil
}

func removeOwnedChild(parent, child string) {
	if child == "" || filepath.Dir(child) != filepath.Clean(parent) {
		return
	}
	info, err := os.Lstat(child)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (st.Uid != uint32(os.Geteuid()) && st.Uid != hostutil.ManagedOwnerUID) {
		return
	}
	_ = os.RemoveAll(child)
}

func runSnapshotPython(ctx context.Context, script string, stdin []byte, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"-I", "-S", "-B", "-c", script}, args...)
	cmd := exec.CommandContext(ctx, snapshotPyBin, cmdArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C.UTF-8",
		"PYTHONUTF8=1",
	}
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	return cmd.Output()
}

func queryActiveTasks(installDir string) (int, error) {
	dbPath := filepath.Join(installDir, "data", snapshotDBName)
	info, err := os.Lstat(dbPath)
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("业务数据库缺失或经过链接，无法确认备份条件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := runSnapshotPython(ctx, activeTasksScript, nil, dbPath)
	if err != nil {
		return 0, errors.New("无法核对 Bot 活动传输任务数；请稍后重试")
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n < 0 {
		return 0, errors.New("无法解析 Bot 活动传输任务数")
	}
	return n, nil
}

func validateSnapshotDest(destDir, installDir string) error {
	if !filepath.IsAbs(destDir) || !hostutil.SafePath(destDir, true) {
		return errors.New("备份目录必须是安全的私密绝对路径")
	}
	cleanDest := filepath.Clean(destDir)
	cleanInstall := filepath.Clean(installDir)
	if cleanDest == cleanInstall || strings.HasPrefix(cleanDest, cleanInstall+"/") || strings.HasPrefix(cleanInstall, cleanDest+"/") {
		return errors.New("备份位置不能位于实例或其父目录内")
	}
	if strings.HasPrefix(cleanDest, "/opt/") {
		top := strings.SplitN(strings.TrimPrefix(cleanDest, "/opt/"), "/", 2)[0]
		if strings.HasPrefix(top, "tgcloud-") && top != "tgcloud-snapshots" {
			return errors.New("备份位置不能复用其他 TGCloud 运行目录")
		}
	}
	return nil
}

func inspectSnapshotContainer(docker inspect.DockerRunner, name string) (containerState, error) {
	const tmpl = `{"id":{{json .Id}},"running":{{json .State.Running}}}`
	out, err := docker("inspect", "--format", tmpl, name)
	var state containerState
	if err != nil || json.Unmarshal(out, &state) != nil || !inspect.ContainerIDPattern.MatchString(state.ID) {
		return containerState{}, errors.New("无法核对备份容器身份／运行状态")
	}
	return state, nil
}

func checkPendingJournals(destDir string) error {
	matches, err := filepath.Glob(filepath.Join(destDir, "operation-*.jsonl"))
	if err != nil {
		return err
	}
	for _, p := range matches {
		if !hostutil.SafePath(p, false) {
			return errors.New("备份操作记录路径不安全，先人工核对")
		}
		info, err := os.Lstat(p)
		if err != nil || info.Size() > 1024*1024 {
			return errors.New("备份操作记录过大或不可读，先人工核对")
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return errors.New("无法读取备份操作记录")
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		var last struct {
			Schema int    `json:"schema"`
			Owner  string `json:"owner"`
			State  string `json:"state"`
		}
		if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &last) != nil ||
			last.Schema != 1 || last.Owner != "tgcloud.instance-snapshot" ||
			(last.State != "completed" && last.State != "failed-services-restored") {
			return errors.New("存在未完成的实例备份记录，先核对容器与备份，不自动重放")
		}
	}
	return nil
}

func checkDatabaseAndSidecars(installDir string) (int64, error) {
	dataDir := filepath.Join(installDir, "data")
	resolved, err := filepath.EvalSymlinks(dataDir)
	if err != nil || resolved != dataDir {
		return 0, errors.New("数据库目录经过链接；停止")
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, errors.New("无法读取业务数据目录")
	}
	for _, e := range entries {
		base := e.Name()
		for _, sfx := range []string{"-wal", "-shm", "-journal"} {
			base = strings.TrimSuffix(base, sfx)
		}
		if (strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".sqlite") || strings.HasSuffix(base, ".sqlite3")) && base != snapshotDBName {
			return 0, errors.New("发现其他数据库或旁文件；停止，不自动选择替代数据库")
		}
	}
	var sidecarBytes int64
	checkMainAndSidecars := func(rel string, required bool) error {
		full := filepath.Join(installDir, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		exists := err == nil
		if !exists && !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法检查数据库或 Session 文件")
		}
		if exists {
			st := info.Sys().(*syscall.Stat_t)
			if !info.Mode().IsRegular() || st.Nlink != 1 {
				return errors.New("数据库或 Session 不是安全的独立常规文件")
			}
		} else if required {
			return errors.New("业务数据库或主 Session 缺失，无法创建实例快照")
		}
		for _, sfx := range []string{"-wal", "-shm", "-journal"} {
			sc := full + sfx
			scInfo, scErr := os.Lstat(sc)
			if errors.Is(scErr, os.ErrNotExist) {
				continue
			}
			if scErr != nil || !scInfo.Mode().IsRegular() || scInfo.Sys().(*syscall.Stat_t).Nlink != 1 || !exists {
				return errors.New("数据库或 Session 旁文件归属不明确或路径不安全")
			}
			if sfx != "-shm" {
				sidecarBytes += scInfo.Size()
			}
		}
		return nil
	}
	if err := checkMainAndSidecars("data/"+snapshotDBName, true); err != nil {
		return 0, err
	}
	if err := checkMainAndSidecars("data/bot.session", true); err != nil {
		return 0, err
	}
	if err := checkMainAndSidecars("data/listener/personal.session", false); err != nil {
		return 0, err
	}
	return sidecarBytes, nil
}

func collectSnapshotFiles(installDir string, mounts []string) (map[string]string, int64, error) {
	resolved, err := filepath.EvalSymlinks(installDir)
	if err != nil || resolved != installDir {
		return nil, 0, errors.New("实例路径经过链接")
	}
	sidecarBytes, err := checkDatabaseAndSidecars(installDir)
	if err != nil {
		return nil, 0, err
	}
	names := []string{".env", "docker-compose.yml", "data/" + snapshotDBName, "data/bot.session", "config", "clouddrive/config"}
	if info, err := os.Lstat(filepath.Join(installDir, "data/listener/personal.session")); err == nil && info.Mode().IsRegular() {
		names = append(names, "data/listener/personal.session")
	}
	for _, opt := range snapshotOptional {
		if info, err := os.Lstat(filepath.Join(installDir, opt)); err == nil && info.Mode().IsRegular() {
			names = append(names, opt)
		}
	}

	mountSet := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		mountSet[m] = true
	}

	files := make(map[string]string)
	var total int64
	checked := 0
	for _, name := range names {
		top := filepath.Join(installDir, filepath.FromSlash(name))
		topInfo, err := os.Lstat(top)
		if err != nil || topInfo.Mode()&os.ModeSymlink != 0 {
			return nil, 0, errors.New("实例备份覆盖不完整，请先核对配置、Session 和网关状态")
		}
		topDev := topInfo.Sys().(*syscall.Stat_t).Dev
		walkErr := filepath.WalkDir(top, func(curr string, _ os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			checked++
			if checked > snapshotMaxFiles {
				return errors.New("实例备份扫描数量超过安全上限")
			}
			relPath, err := filepath.Rel(installDir, curr)
			if err != nil {
				return err
			}
			relPosix := filepath.ToSlash(relPath)
			if strings.Count(relPosix, "/") > 64 {
				return errors.New("实例备份目录深度超过安全上限")
			}
			info, err := os.Lstat(curr)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("备份范围包含符号链接；停止")
			}
			st := info.Sys().(*syscall.Stat_t)
			if st.Dev != topDev || (curr != top && mountSet[curr]) {
				return errors.New("备份范围包含嵌套挂载；停止，不扩展扫描范围")
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() || st.Nlink != 1 {
				return errors.New("备份范围包含特殊文件或硬链接")
			}
			if strings.HasSuffix(relPosix, "-wal") || strings.HasSuffix(relPosix, "-shm") || strings.HasSuffix(relPosix, "-journal") {
				if !strings.HasSuffix(relPosix, "-shm") {
					sidecarBytes += info.Size()
				}
				return nil
			}
			total += info.Size()
			if total+sidecarBytes > snapshotMaxBytes || len(files) >= snapshotMaxFiles {
				return errors.New("实例备份超过容量／文件数量上限")
			}
			files[relPosix] = curr
			return nil
		})
		if walkErr != nil {
			return nil, 0, walkErr
		}
	}
	return files, total + sidecarBytes, nil
}

func checkCustomDataDir(installDir string) error {
	raw, err := os.ReadFile(filepath.Join(installDir, ".env"))
	if err != nil {
		return errors.New("无法读取实例 .env 配置")
	}
	env, err := hostutil.ParseEnvLines(raw, errors.New("实例 .env 配置格式无效"))
	if err != nil {
		return err
	}
	if dataDir := strings.TrimSpace(env["DATA_DIR"]); dataDir != "" && dataDir != "/data" {
		return errors.New("自定义数据路径暂不支持自动实例备份")
	}
	return nil
}

// CreateInstanceSnapshot acquires the instance lock, verifies container, mount
// and version ownership via inspect.Discover, stops running services safely
// under signal protection, creates a SQLite-consistent offline snapshot archive
// using parent-owned private staging directories, and restores prior running
// state before returning (merging any restoration failure into the returned error).
func CreateInstanceSnapshot(installDir, destDir, version string, docker inspect.DockerRunner) (archive string, retErr error) {
	if !inspect.VersionPattern.MatchString(version) {
		return "", errors.New("实例版本号格式无效")
	}
	if err := validateSnapshotDest(destDir, installDir); err != nil {
		return "", err
	}
	lock, err := lockInstanceFunc()
	if err != nil {
		return "", err
	}
	defer lock.Close()

	inst, err := inspect.Discover(installDir, docker)
	if err != nil || inst == nil {
		return "", errors.New("无法核对实例安装目录与容器归属，拒绝执行实例快照")
	}
	if inst.Version != version {
		return "", errors.New("确认期间实例版本发生变化，请重新预检")
	}
	if err := checkCustomDataDir(installDir); err != nil {
		return "", err
	}
	mounts, err := hostutil.Mountpoints()
	if err != nil {
		return "", err
	}
	if _, _, err := collectSnapshotFiles(installDir, mounts); err != nil {
		return "", err
	}
	if n, err := activeTasksFunc(installDir); err != nil {
		return "", err
	} else if n > 0 {
		return "", errors.New("存在活动传输，先等待完成；暂停调度不会停止当前任务")
	}
	botState, err := inspectSnapshotContainer(docker, inspect.BotName)
	if err != nil || botState.ID != inst.Bot.ID {
		return "", errors.New("无法核对 Bot 容器归属／运行状态")
	}
	gwState, err := inspectSnapshotContainer(docker, inspect.GatewayName)
	if err != nil || gwState.ID != inst.Gateway.ID {
		return "", errors.New("无法核对网关容器归属／运行状态")
	}
	if botState.Running && !gwState.Running {
		return "", errors.New("Bot 与网关运行状态不一致，先人工核对")
	}
	if err := ensurePrivateDir(destDir); err != nil {
		return "", err
	}
	if err := checkPendingJournals(destDir); err != nil {
		return "", err
	}

	var rb [6]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return "", errors.New("无法生成快照随机标识")
	}
	key := time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(rb[:])
	archivePath := filepath.Join(destDir, fmt.Sprintf("instance-clouddrive2-%s-%s.tar.gz", version, key))
	journalPath := filepath.Join(destDir, fmt.Sprintf("operation-%s.jsonl", key))

	jFile, err := os.OpenFile(journalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("无法创建实例快照操作日志")
	}
	defer jFile.Close()

	states := map[string]containerState{
		inspect.BotName:     botState,
		inspect.GatewayName: gwState,
	}
	writeEvent := func(st string) error {
		raw, err := json.Marshal(map[string]any{
			"schema": 1, "owner": "tgcloud.instance-snapshot", "edition": "clouddrive2",
			"state": st, "containers": states, "archive": filepath.Base(archivePath),
		})
		if err != nil {
			return errors.New("无法编码实例快照操作日志")
		}
		if _, err := jFile.Write(append(raw, '\n')); err != nil {
			return errors.New("无法写入实例快照操作日志")
		}
		if err := jFile.Sync(); err != nil {
			return errors.New("无法落盘实例快照操作日志")
		}
		return nil
	}
	if err := writeEvent("prepared"); err != nil {
		return "", err
	}

	ctx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSig()

	var stopped []string
	completed := false
	defer func() {
		var failures []string
		// Gateway before Bot; never start a container that was initially stopped.
		for i := len(stopped) - 1; i >= 0; i-- {
			name := stopped[i]
			prev := states[name]
			curr, err := inspectSnapshotContainer(docker, name)
			if err != nil || curr.ID != prev.ID {
				failures = append(failures, name)
				if name == inspect.GatewayName {
					break
				}
				continue
			}
			if !curr.Running {
				if _, err := docker("start", prev.ID); err != nil {
					failures = append(failures, name)
					if name == inspect.GatewayName {
						break
					}
					continue
				}
				after, err := inspectSnapshotContainer(docker, name)
				if err != nil || !after.Running {
					failures = append(failures, name)
					if name == inspect.GatewayName {
						break
					}
				}
			}
		}
		if len(failures) > 0 {
			jErr := writeEvent("needs-service-review")
			archive = ""
			restoreErr := errors.New("原服务恢复未确认；保留操作记录，请人工检查")
			if retErr == nil {
				retErr = errors.New("实例备份结束但原服务恢复未确认；保留归档和操作记录，请人工检查")
			} else {
				retErr = errors.Join(retErr, restoreErr)
			}
			if jErr != nil {
				retErr = errors.Join(retErr, jErr)
			}
			return
		}
		finalState := "failed-services-restored"
		if completed {
			finalState = "completed"
		}
		if jErr := writeEvent(finalState); jErr != nil {
			archive = ""
			if retErr == nil {
				retErr = jErr
			} else {
				retErr = errors.Join(retErr, jErr)
			}
		}
	}()

	for _, name := range []string{inspect.BotName, inspect.GatewayName} {
		if ctx.Err() != nil {
			return "", errors.New("实例备份被中断")
		}
		prev := states[name]
		curr, err := inspectSnapshotContainer(docker, name)
		if err != nil || curr != prev {
			return "", errors.New("备份前容器已变化，请重新预检")
		}
		if prev.Running {
			if err := writeEvent("stopping"); err != nil {
				return "", err
			}
			stopped = append(stopped, name)
			if _, err := docker("stop", "-t", "45", prev.ID); err != nil {
				return "", errors.New("无法确认容器已停止，未生成备份")
			}
			after, err := inspectSnapshotContainer(docker, name)
			if err != nil || after.ID != prev.ID || after.Running {
				return "", errors.New("无法确认容器已停止，未生成备份")
			}
		}
	}
	if err := writeEvent("stopped"); err != nil {
		return "", err
	}
	if n, err := activeTasksFunc(installDir); err != nil {
		return "", err
	} else if n > 0 {
		return "", errors.New("停止前出现活动传输，备份取消；将尝试恢复原服务")
	}

	files, total, err := collectSnapshotFiles(installDir, mounts)
	if err != nil {
		return "", err
	}
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(destDir, &statfs); err != nil {
		return "", errors.New("无法核对备份目录剩余空间")
	}
	if uint64(statfs.Bavail)*uint64(statfs.Bsize) < uint64(total)*3+snapshotMinFree {
		return "", errors.New("实例备份临时副本／归档／校验空间不足")
	}
	if ctx.Err() != nil {
		return "", errors.New("实例备份被中断")
	}

	// Parent Go process owns private staging and preflight extraction directories
	// so SIGKILL/timeout of the child process never leaves plaintext copies behind.
	stageDir, err := createOwnedTempDir(destDir, "snapshot-")
	if err != nil {
		return "", err
	}
	defer removeOwnedChild(destDir, stageDir)
	preflightDir, err := createOwnedTempDir(destDir, "tgcloud-restore-check-")
	if err != nil {
		return "", err
	}
	defer removeOwnedChild(destDir, preflightDir)

	specBytes, err := json.Marshal(map[string]any{
		"dest_dir": destDir, "archive": archivePath, "version": version,
		"stage_dir": stageDir, "preflight_dir": preflightDir, "files": files,
	})
	if err != nil {
		return "", err
	}
	packCtx, cancelPack := context.WithTimeout(ctx, 2400*time.Second)
	defer cancelPack()
	if _, err := runSnapshotPython(packCtx, snapshotBundleScript, specBytes, "pack"); err != nil {
		_ = os.Remove(archivePath)
		return "", errors.New("实例快照一致性备份或校验失败，已清理临时产物")
	}
	if err := writeEvent("packed"); err != nil {
		return "", err
	}
	completed = true
	return archivePath, nil
}

// PreflightInstanceSnapshot verifies an instance snapshot archive in an isolated
// parent-owned private directory using Python's read-only SQLite PRAGMA
// quick_check and full manifest/scope/coverage validation.
func PreflightInstanceSnapshot(archivePath, scratchDir, targetVersion string) (int, error) {
	sigCtx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSig()
	return preflightInstanceSnapshotCtx(sigCtx, archivePath, scratchDir, targetVersion)
}

func preflightInstanceSnapshotCtx(parentCtx context.Context, archivePath, scratchDir, targetVersion string) (int, error) {
	if !inspect.VersionPattern.MatchString(targetVersion) {
		return 0, errors.New("目标版本号格式无效")
	}
	if !hostutil.SafePath(archivePath, true) || !hostutil.SafePath(scratchDir, true) {
		return 0, errors.New("快照归档或隔离目录路径不安全")
	}
	if err := ensurePrivateDir(scratchDir); err != nil {
		return 0, err
	}
	extractDir, err := createOwnedTempDir(scratchDir, "tgcloud-restore-check-")
	if err != nil {
		return 0, err
	}
	defer removeOwnedChild(scratchDir, extractDir)

	ctx, cancel := context.WithTimeout(parentCtx, 600*time.Second)
	defer cancel()
	out, err := runSnapshotPython(ctx, snapshotBundleScript, nil, "preflight", archivePath, scratchDir, extractDir, targetVersion)
	if err != nil {
		if parentCtx.Err() != nil {
			return 0, errors.New("实例快照预检被中断，已清理隔离临时目录")
		}
		return 0, errors.New("实例快照结构／数据库预检未通过；请核对版本、覆盖范围与数据库完整性")
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || count <= 0 {
		return 0, errors.New("无法解析实例快照预检结果")
	}
	return count, nil
}
