package backupops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return nil, errors.New("无法读取代理备份目录")
	}
	var names []string
	for _, e := range entries {
		if proxyArchiveRegex.MatchString(e.Name()) && !e.IsDir() {
			p := filepath.Join(backupRoot, e.Name())
			st, err := os.Lstat(p)
			if err == nil && st.Mode().IsRegular() {
				names = append(names, e.Name())
			}
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	if len(names) > 500 {
		return nil, errors.New("代理备份数量超过上限")
	}
	items := make([]ProxyBackupItem, 0, len(names))
	for _, name := range names {
		archive := filepath.Join(backupRoot, name)
		st, err := os.Lstat(archive)
		if err != nil {
			continue
		}
		valid := st.Size() <= maxBackupBytes*2
		if valid {
			if _, err := VerifyProxyBackup(archive); err != nil {
				valid = false
			}
		}
		integrity := "FAILED"
		if valid {
			integrity = "OK"
		}
		created := ""
		if m := proxyArchiveRegex.FindStringSubmatch(name); len(m) == 2 {
			if t, err := time.Parse("20060102T150405Z", m[1]); err == nil {
				created = t.UTC().Format("2006-01-02 15:04:05 UTC")
			}
		}
		items = append(items, ProxyBackupItem{
			Name:       name,
			CreatedUTC: created,
			SizeBytes:  st.Size(),
			Integrity:  integrity,
		})
	}
	return items, nil
}

func CreateProxyBackup(proxyRoot, backupRoot string) (string, int, error) {
	if err := ensurePrivateDir(backupRoot); err != nil {
		return "", 0, err
	}
	before, err := snapshotProxy(proxyRoot)
	if err != nil {
		return "", 0, err
	}
	var randBytes [16]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		return "", 0, errors.New("无法生成代理备份随机标识")
	}
	name := fmt.Sprintf("proxy-%s-%s.tar.gz", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(randBytes[:]))
	archivePath := filepath.Join(backupRoot, name)
	checksumPath := archivePath + ".sha256"

	cleanup := func() {
		_ = os.Remove(archivePath)
		_ = os.Remove(checksumPath)
	}
	if err := writeProxyArchive(proxyRoot, archivePath, before); err != nil {
		cleanup()
		return "", 0, err
	}
	after, err := snapshotProxy(proxyRoot)
	if err != nil || !reflect.DeepEqual(before, after) {
		cleanup()
		return "", 0, errors.New("备份期间代理文件发生变化；请在续期完成后重试")
	}
	digest, err := fileSHA256(archivePath)
	if err != nil {
		cleanup()
		return "", 0, err
	}
	csFile, err := os.OpenFile(checksumPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		cleanup()
		return "", 0, errors.New("无法写入代理备份校验文件")
	}
	if err := hostutil.FinishFile(csFile, []byte(digest+"\n"), 0600); err != nil {
		cleanup()
		return "", 0, errors.New("无法保存代理备份校验文件")
	}
	verified, err := VerifyProxyBackup(archivePath)
	if err != nil {
		cleanup()
		return "", 0, err
	}
	return archivePath, len(verified), nil
}

func VerifyProxyBackup(archivePath string) (map[string]ManifestEntry, error) {
	base := filepath.Base(archivePath)
	info, err := os.Lstat(archivePath)
	if err != nil || !info.Mode().IsRegular() || !proxyArchiveRegex.MatchString(base) {
		return nil, errors.New("代理备份归档无效")
	}
	checksumPath := archivePath + ".sha256"
	csInfo, err := os.Lstat(checksumPath)
	if err != nil || !csInfo.Mode().IsRegular() || csInfo.Size() > 128 {
		return nil, errors.New("代理备份校验文件缺失或无效")
	}
	csRaw, err := os.ReadFile(checksumPath)
	expectedHash := strings.TrimSpace(string(csRaw))
	if err != nil || !sha256Regex.MatchString(expectedHash) {
		return nil, errors.New("代理备份校验文件格式无效")
	}
	actualHash, err := fileSHA256(archivePath)
	if err != nil || actualHash != expectedHash {
		return nil, errors.New("代理备份归档 SHA256 校验不匹配")
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return nil, errors.New("无法打开代理备份归档")
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, errors.New("代理备份 gzip 格式损坏")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	records := make(map[string]ManifestEntry)
	var manifest *ProxyManifest
	var stateRaw []byte
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("代理备份 tar 内容损坏")
		}
		name := hdr.Name
		if !validArchivePath(name) || len(records) >= maxBackupEntries {
			return nil, errors.New("代理备份含不安全或超限条目")
		}
		if _, dup := records[name]; dup {
			return nil, errors.New("代理备份含重复条目")
		}
		mode := hdr.Mode & 0o7777
		switch hdr.Typeflag {
		case tar.TypeReg:
			total += hdr.Size
			if hdr.Size < 0 || total > maxBackupBytes {
				return nil, errors.New("代理备份解压容量超出安全上限")
			}
			if name == "proxy/manifest.json" {
				if manifest != nil || hdr.Size > 8*1024*1024 {
					return nil, errors.New("代理备份 manifest 无效")
				}
				raw, err := io.ReadAll(io.LimitReader(tr, 8*1024*1024+1))
				if err != nil {
					return nil, errors.New("无法读取代理备份 manifest")
				}
				var m ProxyManifest
				if json.Unmarshal(raw, &m) != nil {
					return nil, errors.New("代理备份 manifest 格式无效")
				}
				manifest = &m
				continue
			}
			if name == "proxy/state/domains.json" {
				if hdr.Size > 65536 {
					return nil, errors.New("代理状态过大")
				}
				raw, err := io.ReadAll(io.LimitReader(tr, 65536+1))
				if err != nil {
					return nil, errors.New("无法读取代理状态")
				}
				stateRaw = raw
				sum := sha256.Sum256(raw)
				sz := hdr.Size
				records[name] = ManifestEntry{Kind: "file", Mode: mode, Size: &sz, SHA256: hex.EncodeToString(sum[:])}
			} else {
				h := sha256.New()
				if _, err := io.Copy(h, tr); err != nil {
					return nil, errors.New("无法校验归档文件内容")
				}
				sz := hdr.Size
				records[name] = ManifestEntry{Kind: "file", Mode: mode, Size: &sz, SHA256: hex.EncodeToString(h.Sum(nil))}
			}
		case tar.TypeDir:
			records[name] = ManifestEntry{Kind: "directory", Mode: mode}
		case tar.TypeSymlink:
			if !safeLink(name, hdr.Linkname) {
				return nil, errors.New("代理备份包含不安全符号链接")
			}
			records[name] = ManifestEntry{Kind: "symlink", Mode: mode, Target: hdr.Linkname}
		default:
			return nil, errors.New("代理备份包含不受支持的条目类型")
		}
	}
	if manifest == nil || manifest.Version != 1 || !reflect.DeepEqual(manifest.Files, records) {
		return nil, errors.New("代理备份 manifest 校验不一致")
	}
	for name := range records {
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			ancestor := strings.Join(parts[:i], "/")
			if entry, ok := records[ancestor]; ok && entry.Kind == "symlink" {
				return nil, errors.New("代理备份条目经过符号链接")
			}
		}
	}
	for _, req := range []string{"proxy/state/domains.json", "proxy/docker-compose.yml", "proxy/nginx/nginx.conf"} {
		if entry, ok := records[req]; !ok || entry.Kind != "file" {
			return nil, errors.New("代理备份缺少必需文件")
		}
	}
	if err := validateProxyState(stateRaw, records); err != nil {
		return nil, err
	}
	return records, nil
}

func RecoveryDrill(archivePath, backupRoot string) (int, error) {
	records, err := VerifyProxyBackup(archivePath)
	if err != nil {
		return 0, err
	}
	if err := ensurePrivateDir(backupRoot); err != nil {
		return 0, err
	}
	dest, err := os.MkdirTemp(backupRoot, "recovery-drill-")
	if err != nil {
		return 0, errors.New("无法创建隔离恢复演练目录")
	}
	defer os.RemoveAll(dest)

	file, err := os.Open(archivePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		if hdr.Name == "proxy/manifest.json" {
			continue
		}
		rec, ok := records[hdr.Name]
		if !ok {
			return 0, errors.New("演练解包发现未登记条目")
		}
		target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return 0, err
		}
		switch rec.Kind {
		case "directory":
			if err := os.MkdirAll(target, 0700); err != nil {
				return 0, err
			}
		case "file":
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return 0, err
			}
			h := sha256.New()
			_, copyErr := io.Copy(io.MultiWriter(out, h), tr)
			closeErr := out.Close()
			if copyErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != rec.SHA256 {
				return 0, errors.New("隔离恢复演练文件校验不匹配")
			}
		}
	}
	for name, rec := range records {
		if rec.Kind == "symlink" {
			target := filepath.Join(dest, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return 0, err
			}
			if err := os.Symlink(rec.Target, target); err != nil {
				return 0, err
			}
		}
	}
	return len(records), nil
}

func PlanProxyPrune(backupRoot string, keep int) (ProxyPrunePlan, error) {
	if keep < 1 || keep > 50 {
		return ProxyPrunePlan{}, errors.New("保留数量必须在 1 到 50 之间")
	}
	items, err := ListProxyBackups(backupRoot)
	if err != nil {
		return ProxyPrunePlan{}, err
	}
	for _, item := range items {
		if item.Integrity != "OK" {
			return ProxyPrunePlan{}, errors.New("存在完整性未通过的代理备份，清理前请先人工核对")
		}
	}
	type digestItem struct {
		Item   ProxyBackupItem `json:"item"`
		SHA256 string          `json:"sha256"`
	}
	ident := make([]digestItem, 0, len(items))
	for _, item := range items {
		d, err := fileSHA256(filepath.Join(backupRoot, item.Name))
		if err != nil {
			return ProxyPrunePlan{}, err
		}
		ident = append(ident, digestItem{Item: item, SHA256: d})
	}
	raw, _ := json.Marshal(struct {
		Keep     int          `json:"keep"`
		Archives []digestItem `json:"archives"`
	}{Keep: keep, Archives: ident})
	sum := sha256.Sum256(raw)

	retainCount := keep
	if retainCount > len(items) {
		retainCount = len(items)
	}
	retain := make([]string, 0, retainCount)
	for i := 0; i < retainCount; i++ {
		retain = append(retain, items[i].Name)
	}
	remove := make([]ProxyBackupItem, 0)
	if len(items) > retainCount {
		remove = append(remove, items[retainCount:]...)
	}
	return ProxyPrunePlan{
		Keep:   keep,
		Retain: retain,
		Remove: remove,
		PlanID: hex.EncodeToString(sum[:]),
	}, nil
}

func ExecuteProxyPrune(backupRoot string, keep int, expectedPlan string) (ProxyPrunePlan, error) {
	if !sha256Regex.MatchString(expectedPlan) {
		return ProxyPrunePlan{}, errors.New("缺少已确认的代理备份清理计划 ID")
	}
	plan, err := PlanProxyPrune(backupRoot, keep)
	if err != nil {
		return ProxyPrunePlan{}, err
	}
	if plan.PlanID != expectedPlan {
		return ProxyPrunePlan{}, errors.New("代理备份清单已变化，请重新预览")
	}
	for _, item := range plan.Remove {
		archive := filepath.Join(backupRoot, item.Name)
		checksum := archive + ".sha256"
		if err := os.Remove(archive); err != nil {
			return ProxyPrunePlan{}, errors.New("移除代理备份失败：" + item.Name)
		}
		if err := os.Remove(checksum); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ProxyPrunePlan{}, errors.New("移除代理备份校验文件失败：" + item.Name)
		}
	}
	return plan, nil
}

func snapshotProxy(root string) (map[string]ManifestEntry, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("无效的共享代理目录")
	}
	if _, err := os.Lstat(filepath.Join(root, composeTransaction)); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("存在未完成的 Compose HTTPS 事务，暂停备份")
	}
	records := make(map[string]ManifestEntry)
	var total int64
	for _, rel := range proxySelection {
		start := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Lstat(start); errors.Is(err, os.ErrNotExist) {
			if rel == "certbot/status" || rel == "external-auth" || rel == composeRegistry {
				continue
			}
			return nil, errors.New("缺少必需的共享代理资源：" + rel)
		}
		walkErr := filepath.WalkDir(start, func(curr string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relPath, err := filepath.Rel(root, curr)
			if err != nil {
				return err
			}
			arcName := "proxy/" + filepath.ToSlash(relPath)
			st, err := os.Lstat(curr)
			if err != nil {
				return err
			}
			mode := int64(st.Mode().Perm())
			switch {
			case st.Mode()&os.ModeSymlink != 0:
				target, err := os.Readlink(curr)
				if err != nil || !safeLink(arcName, target) {
					return errors.New("检测到不安全的证书符号链接")
				}
				records[arcName] = ManifestEntry{Kind: "symlink", Mode: mode, Target: target}
			case st.Mode().IsRegular():
				total += st.Size()
				if total > maxBackupBytes {
					return errors.New("代理备份容量超出上限")
				}
				digest, err := fileSHA256(curr)
				if err != nil {
					return err
				}
				sz := st.Size()
				records[arcName] = ManifestEntry{Kind: "file", Mode: mode, Size: &sz, SHA256: digest}
			case st.IsDir():
				records[arcName] = ManifestEntry{Kind: "directory", Mode: mode}
			default:
				return errors.New("代理目录包含不受支持的文件类型")
			}
			if len(records) > maxBackupEntries {
				return errors.New("代理备份条目过多")
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	stateRaw, err := os.ReadFile(filepath.Join(root, "state/domains.json"))
	if err != nil || len(stateRaw) > 65536 {
		return nil, errors.New("共享代理状态不可读或过大")
	}
	if err := validateProxyState(stateRaw, records); err != nil {
		return nil, err
	}
	return records, nil
}

func writeProxyArchive(root, archivePath string, records map[string]ManifestEntry) error {
	file, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("无法创建代理备份归档")
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, arcName := range names {
		rec := records[arcName]
		srcPath := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(arcName, "proxy/")))
		hdr := &tar.Header{
			Name:    arcName,
			Mode:    rec.Mode,
			ModTime: time.Now().UTC(),
		}
		switch rec.Kind {
		case "directory":
			hdr.Typeflag = tar.TypeDir
			if err := tw.WriteHeader(hdr); err != nil {
				tw.Close()
				gz.Close()
				file.Close()
				return err
			}
		case "symlink":
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = rec.Target
			if err := tw.WriteHeader(hdr); err != nil {
				tw.Close()
				gz.Close()
				file.Close()
				return err
			}
		case "file":
			raw, err := os.ReadFile(srcPath)
			if err != nil {
				tw.Close()
				gz.Close()
				file.Close()
				return err
			}
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(raw))
			if err := tw.WriteHeader(hdr); err != nil {
				tw.Close()
				gz.Close()
				file.Close()
				return err
			}
			if _, err := tw.Write(raw); err != nil {
				tw.Close()
				gz.Close()
				file.Close()
				return err
			}
		}
	}
	manifestBytes, err := json.Marshal(ProxyManifest{Version: 1, Files: records})
	if err != nil {
		tw.Close()
		gz.Close()
		file.Close()
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "proxy/manifest.json",
		Mode:     0600,
		Size:     int64(len(manifestBytes)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now().UTC(),
	}); err != nil {
		tw.Close()
		gz.Close()
		file.Close()
		return err
	}
	if _, err := io.Copy(tw, bytes.NewReader(manifestBytes)); err != nil {
		tw.Close()
		gz.Close()
		file.Close()
		return err
	}
	return errors.Join(tw.Close(), gz.Close(), file.Sync(), file.Close())
}

func validateProxyState(raw []byte, records map[string]ManifestEntry) error {
	var state struct {
		Version        int                        `json:"version"`
		Project        string                     `json:"project"`
		Domains        map[string]json.RawMessage `json:"domains"`
		ExternalWebDAV map[string]struct {
			Owner    string `json:"owner"`
			AuthFile string `json:"auth_file"`
		} `json:"external_webdav"`
	}
	if json.Unmarshal(raw, &state) != nil || (state.Version != 1 && state.Version != 2) ||
		state.Project != projectID || state.Domains == nil {
		return errors.New("无法识别的代理状态格式")
	}
	for edition := range state.Domains {
		if edition != "clouddrive2" {
			return errors.New("代理状态包含未知实例")
		}
	}
	if state.Version == 2 {
		for edition, route := range state.ExternalWebDAV {
			if edition != "clouddrive2" || route.Owner != "tgcloud.external_webdav" ||
				route.AuthFile != "external-auth/"+edition+".htpasswd" ||
				records["proxy/external-auth/"+edition+".htpasswd"].Kind != "file" {
				return errors.New("备份缺少必需的外部 WebDAV 认证文件")
			}
		}
	}
	return nil
}

func validArchivePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, ":") {
		return false
	}
	cleaned := path.Clean(name)
	if cleaned != name {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) == 0 || parts[0] != "proxy" {
		return false
	}
	return !slices.Contains(parts, "..")
}

func safeLink(name, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, "\\") || strings.Contains(target, ":") {
		return false
	}
	parts := strings.Split(path.Dir(name), "/")
	for _, segment := range strings.Split(target, "/") {
		switch segment {
		case "", ".":
		case "..":
			if len(parts) <= 1 {
				return false
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, segment)
		}
	}
	return len(parts) > 0 && parts[0] == "proxy"
}
