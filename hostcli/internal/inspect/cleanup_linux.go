package inspect

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

func CleanupInstall(temps InstallTemps) error {
	if err := validateInstallTemps(temps); err != nil {
		return err
	}
	privateDirectory := checkUpgradeDirectory(temps.Directory) == nil
	// Fresh candidates can exist before the installation directory is created.
	if !privateDirectory && (checkInstallLayoutPath(temps.Directory) != nil ||
		temps.ConfigPartial != "" || temps.DatabasePartial != "" || temps.RollbackDatabase != "" || temps.SnapshotName != "") {
		return errors.New("无法核实安装目录中的升级暂存资源")
	}
	var result error
	if temps.Candidate != "" {
		result = cleanupCandidate(temps.Candidate, RunDocker)
	}
	for _, pending := range []string{temps.ConfigPartial, temps.DatabasePartial} {
		if pending != "" {
			result = errors.Join(result, removeBackupPartial(pending))
		}
	}
	if temps.RollbackDatabase != "" {
		result = errors.Join(result, removeTemporaryFile(temps.RollbackDatabase, 0, 10001))
	}
	if temps.SnapshotName != "" {
		result = errors.Join(result, removeUpgradeSnapshot(filepath.Join(temps.Directory, "data"), temps.SnapshotName))
	}
	if temps.Stage != "" {
		if privateDirectory {
			result = errors.Join(result, removeInstallStage(upgradeWorkDirectory(temps.Directory, temps.Stage)))
		}
		result = errors.Join(result, removeInstallStage(temps.Stage))
	}
	return result
}

func validateInstallTemps(temps InstallTemps) error {
	if temps.Stage != "" && (!candidateStage.MatchString(temps.Stage) || temps.Directory == temps.Stage ||
		strings.HasPrefix(temps.Directory, temps.Stage+"/")) {
		return errors.New("安装暂存目录路径无效")
	}
	if temps.Candidate != "" && !candidateTag.MatchString(temps.Candidate) {
		return errors.New("候选镜像标签无效")
	}
	for _, item := range []struct {
		path, directory string
		pattern         *regexp.Regexp
	}{
		{temps.ConfigPartial, upgradeBackupDir, configBackup},
		{temps.DatabasePartial, upgradeBackupDir, databaseBackup},
		{temps.RollbackDatabase, filepath.Join(temps.Directory, "data"), rollbackDB},
	} {
		if item.path != "" && (filepath.Clean(item.path) != item.path || filepath.Dir(item.path) != item.directory ||
			!item.pattern.MatchString(filepath.Base(item.path))) {
			return errors.New("备份暂存文件路径无效")
		}
	}
	if temps.SnapshotName != "" {
		separator := strings.LastIndexByte(temps.Candidate, '-')
		if !snapshotName.MatchString(temps.SnapshotName) || separator < 0 ||
			temps.SnapshotName != ".upgrade-backup-"+temps.Candidate[separator+1:]+".db" {
			return errors.New("SQLite 暂存文件与本次安装不匹配")
		}
	}
	return nil
}

func cleanupCandidate(tag string, docker DockerRunner) error {
	list := []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=" + candidateLabel + "=" + tag}
	output, err := docker(list...)
	if err != nil {
		return errors.New("无法列出本次安装的候选容器")
	}
	ids := strings.Fields(string(output))
	for _, id := range ids {
		if !containerID.MatchString(id) {
			return errors.New("无法核实候选容器标识")
		}
	}
	if len(ids) != 0 {
		if _, err := docker(append([]string{"container", "rm", "--force"}, ids...)...); err != nil {
			// --rm containers may have stopped and removed themselves since listing.
			remaining, err := docker(list...)
			if err != nil || len(strings.Fields(string(remaining))) > 0 {
				return errors.New("无法清理本次安装的候选容器")
			}
		}
	}
	output, err = docker("image", "ls", "--filter", "reference="+tag, "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return errors.New("无法核实本次安装的候选镜像")
	}
	if strings.TrimSpace(string(output)) == tag {
		if _, err := docker("image", "rm", tag); err != nil {
			return errors.New("无法清理本次安装的候选镜像")
		}
	}
	return nil
}

func removeBackupPartial(path string) error {
	if err := checkUpgradeDirectory(filepath.Dir(path)); err != nil {
		return errors.New("无法核实备份暂存目录")
	}
	return removeTemporaryFile(path, 0)
}

func removeTemporaryFile(path string, owners ...uint32) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	resolved, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || parentErr != nil || resolved != filepath.Dir(path) || !info.Mode().IsRegular() {
		return errors.New("无法核实待清理的暂存文件")
	}
	for _, owner := range owners {
		if info.Sys().(*syscall.Stat_t).Uid == owner {
			if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return errors.New("无法移除暂存文件")
		}
	}
	return errors.New("暂存文件归属不符")
}

func removeUpgradeSnapshot(directory, name string) error {
	resolved, err := filepath.EvalSymlinks(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || resolved != directory {
		return errors.New("无法核实 SQLite 暂存目录")
	}
	// The SQLite helper writes .<name>.tmp-<container-pid> before publishing
	// the snapshot. An interrupted writer can also leave SQLite sidecars.
	temporary := regexp.MustCompile(`^\.` + regexp.QuoteMeta(name) + `\.tmp-[0-9]+(?:-(?:journal|wal|shm))?$`)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return errors.New("无法读取 SQLite 暂存目录")
	}
	var result error
	for _, entry := range entries {
		filename := entry.Name()
		if filename == name || filename == name+"-journal" || filename == name+"-wal" || filename == name+"-shm" || temporary.MatchString(filename) {
			result = errors.Join(result, removeTemporaryFile(filepath.Join(directory, filename), 0, 10001))
		}
	}
	return result
}

func removeInstallStage(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := checkUpgradeDirectory(path); err != nil {
		return errors.New("无法核实待清理的安装暂存目录")
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return errors.New("无法核实暂存目录中的挂载")
	}
	escapes := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			point := escapes.Replace(fields[4])
			if point == path || strings.HasPrefix(point, path+"/") {
				return errors.New("暂存目录包含挂载，已保留该目录")
			}
		}
	}
	if err := os.RemoveAll(path); err != nil {
		return errors.New("无法移除安装暂存目录")
	}
	return nil
}
