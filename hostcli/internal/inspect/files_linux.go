package inspect

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const instanceLock = "/opt/tgcloud-cli-locks/clouddrive2.lock"

var managedOwnerUID uint32 = 0

func LinuxSupported() bool { return true }

func LockInstance() (io.Closer, error) {
	directory := filepath.Dir(instanceLock)
	invalidLock := errors.New("无法安全访问 TGCloud 管理锁")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, invalidLock
	}
	if resolved, err := filepath.EvalSymlinks(directory); err != nil || resolved != directory {
		return nil, invalidLock
	}
	info, err := os.Stat(directory)
	owner := uint32(os.Getuid())
	if err != nil || !info.IsDir() || !privateOwner(info, owner) {
		return nil, invalidLock
	}
	file, err := openInstanceLock(owner)
	if err != nil {
		return nil, invalidLock
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("另一个 TGCloud 管理操作正在使用此实例")
		}
		return nil, invalidLock
	}
	return file, nil
}

func openInstanceLock(owner uint32) (*os.File, error) {
	fdText, inherited := os.LookupEnv("TGCLOUD_HOST_LOCK_FD")
	if !inherited {
		return openPrivate(instanceLock, syscall.O_CREAT|syscall.O_RDWR, owner)
	}
	fd, err := strconv.Atoi(fdText)
	if err != nil || fd < 3 {
		return nil, errors.New("无效的继承锁描述符")
	}
	syscall.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), instanceLock)
	info, err := file.Stat()
	target, targetErr := os.Lstat(instanceLock)
	if err != nil || targetErr != nil || !info.Mode().IsRegular() ||
		!privateOwner(info, owner) || !os.SameFile(info, target) {
		file.Close()
		return nil, errors.New("继承锁与受管锁文件不一致")
	}
	return file, nil
}

func openPrivate(path string, flags int, owner uint32) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !privateOwner(info, owner) {
		file.Close()
		return nil, errors.New("文件归属或权限不符合要求")
	}
	return file, nil
}

func privateOwner(info os.FileInfo, owner uint32) bool {
	return info.Sys().(*syscall.Stat_t).Uid == owner && info.Mode().Perm()&0077 == 0
}

func verifyCandidateStage(directory string) error {
	invalid := errors.New("候选 Bot 暂存目录或配置权限不安全")
	if resolved, err := filepath.EvalSymlinks(directory); err != nil || resolved != directory {
		return invalid
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() || !privateOwner(info, 0) {
		return invalid
	}
	file, err := openPrivate(filepath.Join(directory, ".env"), syscall.O_RDONLY, 0)
	if err != nil {
		return invalid
	}
	return file.Close()
}

func readLogConfig(directory string) ([]byte, error) {
	file, err := openPrivate(filepath.Join(directory, ".env"), syscall.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, logConfigLimit+1))
}
