package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var managedUpgradeFiles = []string{
	"app", ".dockerignore", "backup_retention.sh", "Dockerfile", "docker-compose.yml",
	"manage.sh", "remote_install.sh", "preserve_runtime_config.sh", "repair_clouddrive_network.sh",
	"requirements.txt", "tgcloud-source.json", "LICENSE", "NOTICE", "README.md", "THIRD_PARTY_NOTICES.md",
}

var (
	rollbackArchive = regexp.MustCompile(`^config-[A-Za-z0-9]{8}\.tar\.gz$`)
	rollbackDB      = regexp.MustCompile(`^\.rollback-[A-Za-z0-9]{8}\.db$`)
	imageID         = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	imageReference  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./:@-]{0,299}$`)
)

func checkUpgradeDirectory(directory string) error {
	if !strings.HasPrefix(directory, "/opt/") || filepath.Clean(directory) != directory {
		return errors.New("升级目录路径无效")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return errors.New("升级目录无法核实")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() || !privateOwner(info, 0) {
		return errors.New("升级目录归属或权限不安全")
	}
	return nil
}

func ApplyUpgradeFiles(directory, stage string, applyConfig bool) error {
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	if !candidateStage.MatchString(stage) {
		return errors.New("升级程序暂存目录无效")
	}
	if err := verifyCandidateStage(stage); err != nil {
		return err
	}
	if err := checkUpgradeFiles(stage); err != nil {
		return err
	}
	scratch := upgradeWorkDirectory(directory, stage)
	if err := os.Mkdir(scratch, 0700); err != nil {
		return errors.New("无法建立升级程序暂存目录")
	}
	defer os.RemoveAll(scratch)

	names := append([]string{}, managedUpgradeFiles...)
	if applyConfig {
		names = append(names, ".env")
	}
	copyArgs := []string{"-a", "--"}
	for _, name := range names {
		path := filepath.Join(stage, name)
		if _, err := os.Lstat(path); err == nil {
			copyArgs = append(copyArgs, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法读取升级候选程序")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "cp", append(copyArgs, scratch)...).Run(); err != nil {
		return errors.New("无法暂存升级候选程序")
	}
	return replaceUpgradeFiles(scratch, directory, names)
}

func upgradeWorkDirectory(directory, stage string) string {
	return filepath.Join(directory, ".upgrade-work-"+strings.TrimPrefix(filepath.Base(stage), "tgcloud-release-"))
}

func checkUpgradeFiles(directory string) error {
	for _, name := range []string{"app", ".env", "docker-compose.yml", "tgcloud-source.json"} {
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil || (name == "app" && !info.IsDir()) || (name != "app" && !info.Mode().IsRegular()) {
			return errors.New("升级程序或配置文件不完整")
		}
	}
	return nil
}

// All moves stay on the installation filesystem. The verified upgrade backup
// remains available to the caller if a later move or the new Bot fails.
func replaceUpgradeFiles(source, directory string, names []string) error {
	retired, err := os.MkdirTemp(source, ".upgrade-retired-")
	if err != nil {
		return errors.New("无法建立旧程序暂存目录")
	}
	defer os.RemoveAll(retired)
	for _, name := range names {
		next := filepath.Join(source, name)
		_, err := os.Lstat(next)
		present := err == nil
		if !present && !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法读取待替换的程序文件")
		}
		current := filepath.Join(directory, name)
		if err := os.Rename(current, filepath.Join(retired, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法移开现有程序文件")
		}
		if present {
			if err := os.Rename(next, current); err != nil {
				return errors.New("程序文件替换失败")
			}
		}
	}
	return nil
}

func RollbackUpgrade(directory, archive, database, oldImage, oldTag string) error {
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	if filepath.Clean(archive) != archive || filepath.Dir(archive) != upgradeBackupDir ||
		!rollbackArchive.MatchString(filepath.Base(archive)) {
		return errors.New("配置回滚归档路径无效")
	}
	if err := checkUpgradeDirectory(upgradeBackupDir); err != nil {
		return err
	}
	config, err := openPrivate(archive, syscall.O_RDONLY, 0)
	if err != nil {
		return errors.New("无法读取受管配置备份")
	}
	defer config.Close()
	if err := checkRollbackDB(directory, database); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(directory, ".upgrade-rollback-")
	if err != nil {
		return errors.New("无法建立回滚暂存目录")
	}
	defer os.RemoveAll(scratch)

	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	extract := exec.CommandContext(ctx, "tar", "-xzf", "-", "-C", scratch)
	extract.Stdin = config
	if err := extract.Run(); err != nil {
		return errors.New("配置回滚归档无法解包")
	}
	if err := checkUpgradeFiles(scratch); err != nil {
		return err
	}
	if _, err := DockerPreflight(false); err != nil {
		return err
	}
	if err := restoreUpgradeImage(oldImage, oldTag, RunDocker); err != nil {
		return err
	}
	if err := removeUpgradeBot(directory, RunDocker); err != nil {
		return err
	}
	names := append(append([]string{}, managedUpgradeFiles...), ".env")
	if err := replaceUpgradeFiles(scratch, directory, names); err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Join(scratch, "config")); err == nil && info.IsDir() {
		// The config directory can be on another filesystem. Overlay the
		// archived files without moving the directory or its mount point.
		target := filepath.Join(directory, "config")
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil || resolved != target ||
			exec.CommandContext(ctx, "cp", "-a", "--", filepath.Join(scratch, "config")+"/.", target).Run() != nil {
			return errors.New("无法恢复升级前的存储配置")
		}
	}
	if database != "" {
		if err := restoreUpgradeDB(directory, database); err != nil {
			return err
		}
	}
	restart := []string{"up", "-d", "--no-deps", "--force-recreate"}
	if oldImage != "" {
		restart = append(restart, "--no-build", "--pull", "never")
	}
	if err := runUpgradeCompose(directory, append(restart, botName)...); err != nil {
		return errors.New("无法启动升级前的 Bot 容器")
	}
	if oldImage != "" {
		actual, err := RunDocker("inspect", "--format", "{{.Image}}", botName)
		if err != nil || strings.TrimSpace(string(actual)) != oldImage {
			_, _ = RunDocker("stop", "--time", "30", botName)
			return errors.New("恢复后的 Bot 镜像与升级前不一致")
		}
	}
	return waitUpgradeBot()
}

func removeUpgradeBot(directory string, docker DockerRunner) error {
	output, err := docker("container", "ls", "--all", "--no-trunc", "--filter", "name=^/"+botName+"$", "--format", "{{.ID}}")
	if err != nil {
		return errors.New("无法核实升级中的 Bot 容器")
	}
	id := strings.TrimSpace(string(output))
	if id == "" {
		return nil
	}
	if !containerID.MatchString(id) {
		return errors.New("无法核实升级中的 Bot 容器")
	}
	output, err = docker("container", "inspect", "--format", containerFields, id)
	var bot Container
	if err != nil || json.Unmarshal(output, &bot) != nil || bot.ID != id || bot.Name != "/"+botName ||
		bot.Project == "" || bot.Directory != directory || bot.Service != botName || bot.Mode == "compose" {
		return errInstanceOwner
	}
	if _, err := docker("stop", "--time", "30", id); err != nil {
		return errors.New("无法停止升级中的 Bot 容器")
	}
	if _, err := docker("rm", id); err != nil {
		return errors.New("无法移除升级中的 Bot 容器")
	}
	return nil
}

func restoreUpgradeImage(id, tag string, docker DockerRunner) error {
	if id == "" && tag == "" {
		return nil
	}
	if !imageID.MatchString(id) || !imageReference.MatchString(tag) {
		return errors.New("无法核实升级前的 Bot 镜像")
	}
	if strings.Contains(tag, "@") {
		actual, err := docker("image", "inspect", "--format", "{{.Id}}", tag)
		if err == nil && strings.TrimSpace(string(actual)) == id {
			return nil
		}
		return errors.New("升级前的固定摘要镜像与备份不一致")
	}
	if _, err := docker("tag", id, tag); err != nil {
		return errors.New("无法恢复升级前的 Bot 镜像标签")
	}
	return nil
}

func checkRollbackDB(directory, database string) error {
	if database == "" {
		return nil
	}
	data := filepath.Join(directory, "data")
	resolved, err := filepath.EvalSymlinks(data)
	if err != nil || resolved != data || filepath.Clean(database) != database ||
		filepath.Dir(database) != data || !rollbackDB.MatchString(filepath.Base(database)) {
		return errors.New("SQLite 回滚暂存路径无效")
	}
	snapshot, err := openPrivate(database, syscall.O_RDONLY, 10001)
	if err != nil {
		return errors.New("无法读取已校验的 SQLite 回滚副本")
	}
	defer snapshot.Close()
	if info, err := snapshot.Stat(); err != nil || info.Size() == 0 {
		return errors.New("SQLite 回滚副本为空")
	}
	return nil
}

func restoreUpgradeDB(directory, database string) error {
	target := filepath.Join(directory, "data", "tgcloud.db")
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := target + suffix
		info, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("SQLite 旁文件类型不安全，已停止数据库恢复")
		}
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法移除升级失败后残留的 SQLite 旁文件")
		}
	}
	if err := os.Rename(database, target); err != nil {
		return errors.New("无法恢复升级前的 SQLite 数据库")
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func runUpgradeCompose(directory string, args ...string) error {
	_, err := runDockerWithTimeout(true, nil, 120*time.Second, standaloneComposeArgs(directory, args...)...)
	return err
}

func waitUpgradeBot() error {
	switch waitBotHealth(RunDocker) {
	case "healthy":
		return nil
	case "unhealthy", "exited", "dead":
		return errors.New("升级前的 Bot 恢复后未通过健康检查")
	}
	return errors.New("等待升级前的 Bot 恢复健康超时")
}
