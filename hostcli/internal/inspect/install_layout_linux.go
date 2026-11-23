package inspect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const installConfigLimit = 128 * 1024

var (
	installPathPattern = regexp.MustCompile(`^/opt/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	botDataDirs        = []string{"data", "downloads", "logs", "config"}
	gatewayDataDirs    = []string{"clouddrive", "clouddrive/config", "clouddrive/mounts"}
)

func PrepareInstallLayout(directory, stage, config string) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	if !separateInstallStage(directory, stage) || checkUpgradeDirectory(stage) != nil {
		return errors.New("候选配置暂存目录无法核实")
	}
	data, err := normalizedInstallConfig(config)
	if err != nil {
		return err
	}
	if err := prepareInstallDirectories(directory); err != nil {
		return err
	}
	return writeInstallConfig(stage, data)
}

func prepareInstallDirectories(directory string) error {
	for _, path := range []string{directory, upgradeBackupDir} {
		if err := ensureInstallRoot(path); err != nil {
			return err
		}
		if err := os.Chmod(path, 0700); err != nil {
			return errors.New("无法设置私有安装或备份目录权限")
		}
	}
	for _, name := range installDataDirs() {
		if err := ensureInstallDirectory(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return nil
}

func normalizedInstallConfig(config string) ([]byte, error) {
	data, err := readInstallConfig(config)
	if err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\r")), nil
}

func FinalizeInstallLayout(directory, stage string) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	for _, path := range []string{directory, upgradeBackupDir} {
		if err := checkUpgradeDirectory(path); err != nil {
			return err
		}
	}
	paths := []string{directory, upgradeBackupDir}
	for _, name := range installDataDirs() {
		path := filepath.Join(directory, name)
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			return errors.New("持久化目录缺失或经过链接：" + name)
		}
		// The existing installer only sets rclone ownership, not its mode.
		if name != "config/rclone" {
			paths = append(paths, path)
		}
	}
	for _, path := range paths {
		if err := os.Chmod(path, 0700); err != nil {
			return errors.New("无法设置持久化目录权限：" + filepath.Base(path))
		}
	}
	args := []string{"-hRP", "10001:10001", "--"}
	for _, name := range botDataDirs {
		args = append(args, filepath.Join(directory, name))
	}
	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "chown", args...).Run(); err != nil {
		return errors.New("无法设置 Bot 数据目录归属")
	}
	for _, name := range []string{"remote_install.sh", "manage.sh", "repair_clouddrive_network.sh"} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || os.Chmod(path, info.Mode().Perm()|0100) != nil {
			return errors.New("无法设置管理脚本执行权限：" + name)
		}
	}
	if stage == "" {
		return nil
	}
	return publishInitialConfig(directory, stage)
}

func installDataDirs() []string {
	names := append([]string{}, botDataDirs...)
	names = append(names, "config/rclone")
	return append(names, gatewayDataDirs...)
}

func checkInstallLayoutPath(path string) error {
	if os.Geteuid() != 0 || !installPathPattern.MatchString(path) || filepath.Clean(path) != path {
		return errors.New("宿主安装目录无效")
	}
	for _, protected := range []string{upgradeBackupDir, "/opt/tgcloud-cli", "/opt/tgcloud-cli-locks", "/opt/tgcloud-webdav", "/opt/tgcloud-proxy"} {
		if path == protected || strings.HasPrefix(path, protected+"/") {
			return errors.New("安装目录与 TGCloud 受保护目录重叠")
		}
	}
	return nil
}

func separateInstallStage(directory, stage string) bool {
	return candidateStage.MatchString(stage) && directory != stage && !strings.HasPrefix(directory, stage+"/")
}

func ensureInstallRoot(path string) error {
	current := "/opt"
	for _, name := range append([]string{""}, strings.Split(strings.TrimPrefix(path, "/opt/"), "/")...) {
		current = filepath.Join(current, name)
		if err := ensureInstallDirectory(current); err != nil {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 ||
			(info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return errors.New("安装目录或上层目录的归属或权限不安全")
		}
	}
	return nil
}

func ensureInstallDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("无法创建持久化目录：" + filepath.Base(path))
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		return errors.New("持久化目录不是实际目录：" + filepath.Base(path))
	}
	return nil
}

func readInstallConfig(path string) ([]byte, error) {
	invalid := errors.New("无法读取私有安装配置")
	file, err := openPrivate(path, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, installConfigLimit+1))
	if err != nil || len(data) > installConfigLimit {
		return nil, invalid
	}
	return data, nil
}

func writeInstallConfig(directory string, data []byte) error {
	return writePrivateConfigFile(filepath.Join(directory, ".env"), data)
}

func writePrivateConfigFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("无法新建私有安装配置，现有配置保留")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("无法写入私有安装配置")
	}
	return nil
}

func publishInitialConfig(directory, stage string) error {
	if !separateInstallStage(directory, stage) || verifyCandidateStage(stage) != nil {
		return errors.New("无法核实初始配置暂存目录")
	}
	data, err := readInstallConfig(filepath.Join(stage, ".env"))
	if err != nil {
		return err
	}
	// Use the installation filesystem, including when /opt and the instance
	// are separate mounts. The installer already cleans this work directory.
	return publishInstallConfig(directory, upgradeWorkDirectory(directory, stage), data)
}

func publishInstallConfig(directory, work string, data []byte) error {
	if err := os.Mkdir(work, 0700); err != nil {
		return errors.New("无法创建初始配置暂存目录")
	}
	defer os.RemoveAll(work)
	if err := writeInstallConfig(work, data); err != nil {
		return err
	}
	if err := os.Link(filepath.Join(work, ".env"), filepath.Join(directory, ".env")); err != nil {
		return errors.New("无法发布初始配置，现有配置保留")
	}
	return nil
}
