package hostutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

var ManagedOwnerUID uint32 = 0

func HostUnameMachine() (string, error) {
	var utsname syscall.Utsname
	if err := syscall.Uname(&utsname); err != nil {
		return "", errors.New("无法核实宿主 CPU 架构")
	}
	var architecture strings.Builder
	for _, character := range utsname.Machine {
		if character == 0 {
			break
		}
		architecture.WriteByte(byte(character))
	}
	return architecture.String(), nil
}

func PrivateOwner(info os.FileInfo, owner uint32) bool {
	return info.Sys().(*syscall.Stat_t).Uid == owner && info.Mode().Perm()&0077 == 0
}

func OpenPrivate(path string, flags int, owner uint32) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !PrivateOwner(info, owner) {
		file.Close()
		return nil, errors.New("文件归属或权限不符合要求")
	}
	return file, nil
}

func SafePath(path string, private bool) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			owner := info.Sys().(*syscall.Stat_t).Uid
			stickyAncestor := current != path && info.IsDir() && owner == 0 && info.Mode()&os.ModeSticky != 0
			if info.Mode()&os.ModeSymlink != 0 || (owner != 0 && owner != uint32(os.Geteuid())) ||
				(info.Mode().Perm()&0022 != 0 && !stickyAncestor) ||
				(current == path && private && !PrivateOwner(info, uint32(os.Geteuid()))) {
				return false
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if filepath.Dir(current) == current {
			return true
		}
	}
}

func FinishFile(file *os.File, body []byte, mode os.FileMode) error {
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func AtomicWrite(path string, body []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tgcloud-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := FinishFile(temp, body, mode); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func Mountpoints() ([]string, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, errors.New("无法读取挂载点清单；不删除数据")
	}
	defer file.Close()
	const limit = 4 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("挂载点信息过大或不可读；不删除数据")
	}
	return ParseMountinfo(string(raw))
}

func ParseMountinfo(body string) ([]string, error) {
	var points []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 7 || !slices.Contains(fields, "-") {
			return nil, errors.New("无法完整核对挂载点；不删除数据")
		}
		points = append(points, filepath.Clean(UnescapeMountPath(fields[4])))
	}
	if len(points) == 0 {
		return nil, errors.New("挂载点清单为空；不删除数据")
	}
	return points, nil
}
