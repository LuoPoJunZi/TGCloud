package inspect

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const UpgradeBackupDir = "/opt/tgcloud-backups"
const upgradeBackupDir = UpgradeBackupDir
const backupTimeout = 2400 * time.Second

var (
	configBackup   = regexp.MustCompile(`^config-[A-Za-z0-9]{8}\.tar\.gz\.partial$`)
	databaseBackup = regexp.MustCompile(`^database-[A-Za-z0-9]{8}\.db\.partial$`)
	snapshotName   = regexp.MustCompile(`^\.upgrade-backup-[0-9]+\.db$`)
)

func checkUpgradeBackupPath(directory, destination string, name *regexp.Regexp) error {
	if !strings.HasPrefix(directory, "/opt/") || filepath.Clean(directory) != directory ||
		filepath.Clean(destination) != destination || filepath.Dir(destination) != upgradeBackupDir ||
		!name.MatchString(filepath.Base(destination)) {
		return errors.New("升级备份路径无效")
	}
	for _, dir := range []string{directory, upgradeBackupDir} {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil || resolved != dir {
			return errors.New("升级备份目录无法核实")
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || !privateOwner(info, 0) {
			return errors.New("升级备份目录归属或权限不安全")
		}
	}
	return nil
}

// The installer reserves a private file before starting either backup.
func openUpgradeBackup(directory, destination string, name *regexp.Regexp) (*os.File, error) {
	if err := checkUpgradeBackupPath(directory, destination, name); err != nil {
		return nil, err
	}
	target, err := openPrivate(destination, syscall.O_WRONLY, 0)
	if err != nil {
		return nil, errors.New("无法打开升级备份暂存文件")
	}
	if info, err := target.Stat(); err != nil || info.Size() != 0 {
		target.Close()
		return nil, errors.New("升级备份暂存文件必须为空")
	}
	return target, nil
}

func CommitUpgradeBackup(directory, pending string) error {
	switch name := filepath.Base(pending); {
	case configBackup.MatchString(name):
		if err := BackupUpgradeConfig(directory, pending); err != nil {
			return err
		}
	case databaseBackup.MatchString(name):
		if err := checkUpgradeBackupPath(directory, pending, databaseBackup); err != nil {
			return err
		}
		if err := validateUpgradeSnapshot(pending); err != nil {
			return err
		}
	default:
		return errors.New("升级备份暂存文件名无效")
	}
	if err := renameNoReplace(pending, strings.TrimSuffix(pending, ".partial")); err != nil {
		return errors.New("无法发布已校验的升级备份，现有备份保留")
	}
	return nil
}

func validateUpgradeSnapshot(path string) error {
	invalid := errors.New("SQLite 备份完整性检查未通过")
	file, err := openPrivate(path, syscall.O_RDONLY, 0)
	if err != nil {
		return invalid
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.Size() == 0 {
		return invalid
	}
	// Use the same read-only SQLite check as the Shell fallback.
	const check = `import sqlite3, sys
from contextlib import closing
from pathlib import Path
with closing(sqlite3.connect(Path(sys.argv[1]).as_uri() + "?mode=ro&immutable=1", uri=True)) as db:
    sys.exit(db.execute("PRAGMA quick_check").fetchall() != [("ok",)])`
	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-I", "-S", "-B", "-c", check, path)
	isolateInstallCommand(command, true)
	if err := command.Run(); err != nil {
		return invalid
	}
	return nil
}

func BackupUpgradeConfig(directory, destination string) error {
	target, err := openUpgradeBackup(directory, destination, configBackup)
	if err != nil {
		return err
	}
	defer target.Close()
	return archiveUpgradeConfig(directory, destination, target)
}

func archiveUpgradeConfig(directory, destination string, target *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	archive := exec.CommandContext(ctx, "tar", "-czf", "-", "-C", directory,
		"--exclude=./data", "--exclude=./downloads", "--exclude=./logs", "--exclude=./clouddrive", ".")
	isolateInstallCommand(archive, true)
	archive.Stdout = target
	if err := archive.Run(); err != nil {
		return errors.New("配置备份写入失败，请检查磁盘空间和权限")
	}
	if err := target.Sync(); err != nil {
		return errors.New("配置备份无法保存到磁盘")
	}
	verify := exec.CommandContext(ctx, "tar", "-tzf", destination)
	isolateInstallCommand(verify, true)
	if err := verify.Run(); err != nil {
		return errors.New("配置备份无法读取")
	}
	return nil
}

func SnapshotUpgradeDatabase(directory, tag, name, destination string) error {
	if !candidateTag.MatchString(tag) || !snapshotName.MatchString(name) {
		return errors.New("数据库快照镜像或暂存文件名无效")
	}
	target, err := openUpgradeBackup(directory, destination, databaseBackup)
	if err != nil {
		return err
	}
	defer target.Close()
	data := filepath.Join(directory, "data")
	if resolved, err := filepath.EvalSymlinks(data); err != nil || resolved != data {
		return errors.New("数据库目录无法核实")
	}
	if _, err := DockerPreflight(false); err != nil {
		return err
	}
	// Reuse SQLite's online backup and integrity check in the native candidate
	// image. Copying the live database file alone can miss committed WAL data.
	if _, err := runDockerWithTimeout(true, nil, backupTimeout, "run", "--rm", "--label", candidateLabel+"="+tag, "--read-only",
		"--network", "none", "--tmpfs", "/tmp", "--user", "10001:10001", "--volume", data+":/data",
		"--entrypoint", "python", tag, "-m", "app.backup_database", "/data/tgcloud.db", "/data/"+name); err != nil {
		return errors.New("SQLite 一致性快照生成失败")
	}
	snapshot, err := openPrivate(filepath.Join(data, name), syscall.O_RDONLY, 10001)
	if err != nil {
		return errors.New("无法读取候选 SQLite 快照")
	}
	defer snapshot.Close()
	if size, err := io.Copy(target, snapshot); err != nil || size == 0 {
		return errors.New("SQLite 快照复制失败")
	}
	if err := target.Sync(); err != nil {
		return errors.New("SQLite 快照无法保存到磁盘")
	}
	return nil
}
