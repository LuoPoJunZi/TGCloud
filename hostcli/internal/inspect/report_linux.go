package inspect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ReportInstall(directory string, progress io.Writer) error {
	if err := checkDockerHost(); err != nil {
		return err
	}
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	if _, err := installedVersion(directory); err != nil {
		return err
	}
	showBackupSummary(upgradeBackupDir, progress)
	fmt.Fprintln(progress, "[TGCloud] 部署完成")
	if _, err := runDocker(true, progress, standaloneComposeArgs(directory, "ps")...); err != nil {
		return errors.New("安装已通过验收，但无法显示容器状态，请运行 manage.sh status 核对")
	}
	return nil
}

func showBackupSummary(directory string, output io.Writer) {
	count, size, err := backupInventory(directory)
	if err != nil {
		fmt.Fprintln(output, "[TGCloud] 警告：无法统计历史备份；部署已经通过验收，请稍后运行 manage.sh backups 检查")
		return
	}
	fmt.Fprintf(output, "[TGCloud] 当前保留 %d 份升级／配置备份，占用约 %dMB\n", count, size/(1024*1024))
	if size > 5*1024*1024*1024 {
		fmt.Fprintln(output, "[TGCloud] 警告：历史备份已超过 5GB；请先运行 manage.sh backups 核对，再按需手动 prune-backups")
	}
}

func backupInventory(directory string) (count int, size uint64, err error) {
	if resolved, err := filepath.EvalSymlinks(directory); err != nil || !filepath.IsAbs(directory) || resolved != directory {
		return 0, 0, errors.New("备份目录不是安全的真实绝对目录")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !(strings.HasPrefix(name, "config-") && strings.HasSuffix(name, ".tar.gz") ||
			strings.HasPrefix(name, "database-") && strings.HasSuffix(name, ".db") ||
			strings.HasPrefix(name, "env-") && strings.HasSuffix(name, ".env")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, 0, err
		}
		if info.Mode().IsRegular() {
			count++
			size += uint64(info.Size())
		}
	}
	return count, size, nil
}
