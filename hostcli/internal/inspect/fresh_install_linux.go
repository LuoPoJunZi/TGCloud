package inspect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

func StageFreshInstall(directory, source, config string, result io.Writer) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	data, err := normalizedInstallConfig(config)
	if err != nil {
		return err
	}
	stage, err := stageInstallProgram(directory, source, result)
	if err != nil {
		return err
	}
	if err := writeInstallConfig(stage, data); err != nil {
		return errors.Join(err, removeInstallStage(stage))
	}
	return nil
}

func PublishFreshInstall(directory, stage string) (err error) {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	if !separateInstallStage(directory, stage) || verifyCandidateStage(stage) != nil {
		return errors.New("无法核实新装候选目录及配置")
	}
	data, err := readInstallConfig(filepath.Join(stage, ".env"))
	if err != nil {
		return err
	}
	if err := ensureInstallRoot(directory); err != nil {
		return err
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		return errors.New("新装目标目录已有文件，请核对现有内容")
	}
	if os.Chmod(directory, 0700) != nil {
		return errors.New("无法设置新装目标目录权限")
	}
	work := upgradeWorkDirectory(directory, stage)
	if os.Mkdir(work, 0700) != nil {
		return errors.New("无法创建程序发布暂存目录")
	}
	defer func() { err = errors.Join(err, removeInstallStage(work)) }()
	if err := copyInstallProgram(stage, work); err != nil {
		return err
	}
	if err := prepareInstallDirectories(work); err != nil {
		return err
	}
	if err := writeInstallConfig(work, data); err != nil {
		return err
	}
	if err := FinalizeInstallLayout(work, ""); err != nil {
		return err
	}
	return publishFreshEntries(work, directory)
}

func publishFreshEntries(work, directory string) (err error) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return errors.New("无法读取待发布的新装文件")
	}
	type publishedEntry struct {
		name string
		info os.FileInfo
	}
	var published []publishedEntry
	defer func() {
		if err == nil {
			return
		}
		for i := len(published) - 1; i >= 0; i-- {
			entry := published[i]
			current := filepath.Join(directory, entry.name)
			info, inspectErr := os.Lstat(current)
			if inspectErr != nil || !os.SameFile(info, entry.info) || renameNoReplace(current, filepath.Join(work, entry.name)) != nil {
				err = errors.Join(err, errors.New("部分已发布文件无法撤回，请保留现场："+entry.name))
			}
		}
	}()
	for _, entry := range entries {
		from, to := filepath.Join(work, entry.Name()), filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return errors.New("无法核实待发布文件：" + entry.Name())
		}
		if err := renameNoReplace(from, to); err != nil {
			return fmt.Errorf("新装文件发布失败：%s：%w", entry.Name(), err)
		}
		published = append(published, publishedEntry{entry.Name(), info})
	}
	return nil
}

// renameat2 with RENAME_NOREPLACE atomically refuses an existing target.
// The standard syscall package omits SYS_RENAMEAT2 on Linux AMD64.
func renameNoReplace(from, to string) error {
	var renameat2 uintptr
	switch runtime.GOARCH {
	case "amd64":
		renameat2 = 316
	case "arm64":
		renameat2 = 276
	default:
		return syscall.ENOSYS
	}
	oldPath, err := syscall.BytePtrFromString(from)
	if err != nil {
		return err
	}
	newPath, err := syscall.BytePtrFromString(to)
	if err != nil {
		return err
	}
	atFDCWD := -100
	const renameNoReplace = 1
	_, _, errno := syscall.Syscall6(renameat2, uintptr(atFDCWD), uintptr(unsafe.Pointer(oldPath)),
		uintptr(atFDCWD), uintptr(unsafe.Pointer(newPath)), renameNoReplace, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
