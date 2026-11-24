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
	"slices"
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
)

var (
	sqliteMagic      = []byte("SQLite format 3\x00")
	snapshotDBNames  = []string{"tgcloud.db", "tasks.db"}
	snapshotOptional = []string{"tgcloud-release.json", "tgcloud-source.json", "tgcloud-compose.json", "compose-bundle.json", "compose-access.json"}
)

type SnapshotFileRecord struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type SnapshotManifest struct {
	Format   int                           `json:"format"`
	Edition  string                        `json:"edition"`
	Version  string                        `json:"version"`
	Files    map[string]SnapshotFileRecord `json:"files"`
	Excluded []string                      `json:"excluded"`
	Source   string                        `json:"source"`
}

type containerState struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
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
		rest := strings.TrimPrefix(cleanDest, "/opt/")
		top := strings.SplitN(rest, "/", 2)[0]
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

func resolveDatabaseRel(installDir string) (string, error) {
	for _, name := range snapshotDBNames {
		rel := "data/" + name
		full := filepath.Join(installDir, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err == nil && info.Mode().IsRegular() {
			return rel, nil
		}
	}
	return "", errors.New("业务数据库缺失，无法创建实例快照")
}

func collectSnapshotFiles(installDir string, mounts []string) (map[string]string, int64, error) {
	resolved, err := filepath.EvalSymlinks(installDir)
	if err != nil || resolved != installDir {
		return nil, 0, errors.New("实例路径经过链接")
	}
	dbRel, err := resolveDatabaseRel(installDir)
	if err != nil {
		return nil, 0, err
	}
	names := []string{".env", "docker-compose.yml", dbRel, "data/bot.session", "config", "clouddrive/config"}
	personal := filepath.Join(installDir, "data/listener/personal.session")
	if info, err := os.Lstat(personal); err == nil {
		if !info.Mode().IsRegular() {
			return nil, 0, errors.New("监听 Session 不是常规文件，停止备份")
		}
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
				if info.Size() > 0 {
					return errors.New("检测到非空 SQLite 活跃日志文件，请先确认服务已完全停止")
				}
				return nil
			}
			total += info.Size()
			if total > snapshotMaxBytes || len(files) >= snapshotMaxFiles {
				return errors.New("实例备份超过容量／文件数量上限")
			}
			files[relPosix] = curr
			return nil
		})
		if walkErr != nil {
			return nil, 0, walkErr
		}
	}
	return files, total, nil
}

func checkSQLiteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, buf); err != nil || !bytes.Equal(buf, sqliteMagic) {
		return errors.New("数据库／Session 格式不正确，未生成备份")
	}
	return nil
}

// CreateInstanceSnapshot creates a verified offline-compatible instance snapshot
// archive after stopping running Bot/Gateway containers, and restores their prior
// running state before returning.
func CreateInstanceSnapshot(installDir, destDir, version string, docker inspect.DockerRunner) (archive string, retErr error) {
	if !inspect.VersionPattern.MatchString(version) {
		return "", errors.New("实例版本号格式无效")
	}
	if err := validateSnapshotDest(destDir, installDir); err != nil {
		return "", err
	}
	mounts, err := hostutil.Mountpoints()
	if err != nil {
		return "", err
	}
	if _, _, err := collectSnapshotFiles(installDir, mounts); err != nil {
		return "", err
	}
	botState, err := inspectSnapshotContainer(docker, inspect.BotName)
	if err != nil {
		return "", err
	}
	gwState, err := inspectSnapshotContainer(docker, inspect.GatewayName)
	if err != nil {
		return "", err
	}
	if botState.Running && !gwState.Running {
		return "", errors.New("Bot 与网关运行状态不一致，先人工核对")
	}
	if botState.Running {
		activeOut, err := docker("exec", botState.ID, "python", "-B", "-c",
			"import sqlite3; from tgcloud.persistence.paths import database_path; "+
				"c=sqlite3.connect(database_path('/data', require_existing=True).as_uri()+'?mode=ro', uri=True, timeout=5); "+
				"print(c.execute(\"SELECT count(*) FROM tasks WHERE state IN ('reserved','downloading','streaming','uploading','verifying','finalizing','cleanup_pending')\").fetchone()[0])")
		if err != nil {
			return "", errors.New("无法核对 Bot 活动传输任务数；请稍后重试")
		}
		if strings.TrimSpace(string(activeOut)) != "0" {
			return "", errors.New("存在活动传输，先等待完成；暂停调度不会停止当前任务")
		}
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
	writeEvent := func(st string) {
		raw, _ := json.Marshal(map[string]any{
			"schema": 1, "owner": "tgcloud.instance-snapshot", "edition": "clouddrive2",
			"state": st, "containers": states, "archive": filepath.Base(archivePath),
		})
		_, _ = jFile.Write(append(raw, '\n'))
		_ = jFile.Sync()
	}
	writeEvent("prepared")

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
			writeEvent("needs-service-review")
			if retErr == nil {
				archive = ""
				retErr = errors.New("实例备份结束但原服务恢复未确认；保留归档和操作记录，请人工检查")
			}
			return
		}
		if completed {
			writeEvent("completed")
		} else {
			writeEvent("failed-services-restored")
		}
	}()

	for _, name := range []string{inspect.BotName, inspect.GatewayName} {
		prev := states[name]
		curr, err := inspectSnapshotContainer(docker, name)
		if err != nil || curr != prev {
			return "", errors.New("备份前容器已变化，请重新预检")
		}
		if prev.Running {
			stopped = append(stopped, name)
			writeEvent("stopping")
			if _, err := docker("stop", "-t", "45", prev.ID); err != nil {
				return "", errors.New("无法确认容器已停止，未生成备份")
			}
			after, err := inspectSnapshotContainer(docker, name)
			if err != nil || after.ID != prev.ID || after.Running {
				return "", errors.New("无法确认容器已停止，未生成备份")
			}
		}
	}
	writeEvent("stopped")

	files, total, err := collectSnapshotFiles(installDir, mounts)
	if err != nil {
		return "", err
	}
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(destDir, &statfs); err != nil {
		return "", errors.New("无法核对备份目录剩余空间")
	}
	freeBytes := uint64(statfs.Bavail) * uint64(statfs.Bsize)
	if freeBytes < uint64(total)*3+snapshotMinFree {
		return "", errors.New("实例备份临时副本／归档／校验空间不足")
	}

	if err := packInstanceSnapshot(files, archivePath, version); err != nil {
		return "", err
	}
	writeEvent("packed")
	if _, err := PreflightInstanceSnapshot(archivePath, destDir, version); err != nil {
		_ = os.Remove(archivePath)
		return "", err
	}
	completed = true
	return archivePath, nil
}

func packInstanceSnapshot(files map[string]string, archivePath, version string) error {
	records := make(map[string]SnapshotFileRecord, len(files))
	relNames := make([]string, 0, len(files))
	for rel, src := range files {
		if strings.HasSuffix(rel, ".db") || strings.HasSuffix(rel, ".session") {
			if err := checkSQLiteHeader(src); err != nil {
				return err
			}
		}
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("打包前备份对象发生变化")
		}
		sum, err := fileSHA256(src)
		if err != nil {
			return err
		}
		records[rel] = SnapshotFileRecord{Size: info.Size(), SHA256: sum}
		relNames = append(relNames, rel)
	}
	slices.Sort(relNames)

	manifest := SnapshotManifest{
		Format:   1,
		Edition:  "clouddrive2",
		Version:  version,
		Files:    records,
		Excluded: []string{"downloads", "logs", "shared-https"},
		Source:   "operator-offline-copy",
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("无法创建实例快照归档")
	}
	cleanup := func() { _ = os.Remove(archivePath) }
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{
		Name:     "manifest.json",
		Mode:     0600,
		Size:     int64(len(manifestBytes)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now().UTC(),
	}); err != nil {
		tw.Close()
		gz.Close()
		f.Close()
		cleanup()
		return err
	}
	if _, err := io.Copy(tw, bytes.NewReader(manifestBytes)); err != nil {
		tw.Close()
		gz.Close()
		f.Close()
		cleanup()
		return err
	}
	for _, rel := range relNames {
		src := files[rel]
		rec := records[rel]
		raw, err := os.ReadFile(src)
		if err != nil || int64(len(raw)) != rec.Size {
			tw.Close()
			gz.Close()
			f.Close()
			cleanup()
			return errors.New("打包期间实例快照内容发生变化")
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != rec.SHA256 {
			tw.Close()
			gz.Close()
			f.Close()
			cleanup()
			return errors.New("打包期间实例快照摘要发生变化")
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     rel,
			Mode:     0600,
			Size:     rec.Size,
			Typeflag: tar.TypeReg,
			ModTime:  time.Now().UTC(),
		}); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			cleanup()
			return err
		}
		if _, err := tw.Write(raw); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			cleanup()
			return err
		}
	}
	if err := errors.Join(tw.Close(), gz.Close(), f.Sync(), f.Close()); err != nil {
		cleanup()
		return err
	}
	return nil
}

func parseSemver(v string) ([3]int, error) {
	if !inspect.VersionPattern.MatchString(v) {
		return [3]int{}, errors.New("版本号格式无效")
	}
	parts := strings.Split(v, ".")
	var out [3]int
	for i := range 3 {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}, err
		}
		out[i] = n
	}
	return out, nil
}

// PreflightInstanceSnapshot verifies an instance snapshot archive in an isolated
// temporary directory without modifying live services or data.
func PreflightInstanceSnapshot(archivePath, scratchDir, targetVersion string) (int, error) {
	targetVer, err := parseSemver(targetVersion)
	if err != nil {
		return 0, err
	}
	if !hostutil.SafePath(archivePath, true) || !hostutil.SafePath(scratchDir, true) {
		return 0, errors.New("快照归档或隔离目录路径不安全")
	}
	if err := ensurePrivateDir(scratchDir); err != nil {
		return 0, err
	}
	tmp, err := os.MkdirTemp(scratchDir, "snapshot-preflight-")
	if err != nil {
		return 0, errors.New("无法创建隔离预检目录")
	}
	defer os.RemoveAll(tmp)

	f, err := os.Open(archivePath)
	if err != nil {
		return 0, errors.New("无法打开实例快照归档")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, errors.New("实例快照 gzip 格式无效")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	first, err := tr.Next()
	if err != nil || first.Name != "manifest.json" || first.Typeflag != tar.TypeReg || first.Size > 8*1024*1024 {
		return 0, errors.New("实例快照缺少有效 manifest.json")
	}
	mRaw, err := io.ReadAll(io.LimitReader(tr, 8*1024*1024+1))
	var manifest SnapshotManifest
	if err != nil || json.Unmarshal(mRaw, &manifest) != nil || manifest.Format != 1 || manifest.Edition != "clouddrive2" {
		return 0, errors.New("实例快照 manifest 版本或产品不匹配")
	}
	srcVer, err := parseSemver(manifest.Version)
	if err != nil || srcVer[0] != targetVer[0] ||
		(srcVer[0] == targetVer[0] && (srcVer[1] > targetVer[1] || (srcVer[1] == targetVer[1] && srcVer[2] > targetVer[2]))) {
		return 0, errors.New("实例快照版本高于目标版本或主版本不兼容")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > snapshotMaxFiles {
		return 0, errors.New("实例快照文件清单无效")
	}
	hasDB := false
	for _, dbName := range snapshotDBNames {
		if _, ok := manifest.Files["data/"+dbName]; ok {
			hasDB = true
		}
	}
	for _, req := range []string{".env", "docker-compose.yml", "data/bot.session"} {
		if _, ok := manifest.Files[req]; !ok {
			return 0, errors.New("实例快照缺少必需文件：" + req)
		}
	}
	if !hasDB {
		return 0, errors.New("实例快照缺少业务数据库")
	}

	seen := make(map[string]bool, len(manifest.Files))
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, errors.New("实例快照归档内容损坏")
		}
		name := hdr.Name
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, ":") ||
			path.Clean(name) != name || slices.Contains(strings.Split(name, "/"), "..") || hdr.Typeflag != tar.TypeReg {
			return 0, errors.New("实例快照包含不安全路径或非文件条目")
		}
		rec, ok := manifest.Files[name]
		if !ok || seen[name] || hdr.Size != rec.Size || hdr.Size < 0 {
			return 0, errors.New("实例快照条目与 manifest 不一致")
		}
		total += hdr.Size
		if total > snapshotMaxBytes {
			return 0, errors.New("实例快照解压容量超过上限")
		}
		dest := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return 0, err
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return 0, err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, h), tr)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n != rec.Size || hex.EncodeToString(h.Sum(nil)) != rec.SHA256 {
			return 0, errors.New("实例快照文件 SHA256 校验失败：" + name)
		}
		if strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".session") {
			if err := checkSQLiteHeader(dest); err != nil {
				return 0, err
			}
		}
		seen[name] = true
	}
	if len(seen) != len(manifest.Files) {
		return 0, errors.New("实例快照缺少 manifest 声明的文件")
	}
	return len(seen), nil
}
