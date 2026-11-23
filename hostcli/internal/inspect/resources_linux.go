package inspect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var resourceDirectory = optPathRegex

const minimumInstallFreeKiB = 8_000_000

func CheckInstallResources(directory string, progress io.Writer) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	memory, err := hostMemory()
	if err != nil {
		return err
	}
	disk, err := filesystem(directory)
	if err != nil {
		return errors.New("无法读取安装目录磁盘容量")
	}
	return checkInstallResourceLimits(onlineCPU(), memory.total, disk, progress)
}

func checkInstallResourceLimits(cpu int, memoryKiB uint64, disk diskResources, progress io.Writer) error {
	memoryMiB := memoryKiB / 1024
	fmt.Fprintf(progress, "[TGCloud] 检测到 %d 核 CPU、约 %dMB 内存、安装文件系统可用约 %dMB\n", cpu, memoryMiB, disk.available/(1024*1024))
	if memoryMiB < minimumInstallMiB {
		return fmt.Errorf("内存不足 2GB，当前约 %dMB", memoryMiB)
	}
	if disk.total/1024 < 45_000_000 {
		fmt.Fprintln(progress, "[TGCloud] 警告：安装文件系统总容量低于推荐的 50GB")
	}
	if disk.available/1024 < minimumInstallFreeKiB {
		return errors.New("安装文件系统可用空间不足 8GB，无法安全安装")
	}
	return nil
}

// ResourceProbe emits the existing installer probe format for the shared Python parser.
func ResourceProbe(install, backup string) (string, error) {
	for _, path := range []string{install, backup} {
		if !resourceDirectory.MatchString(path) || filepath.Clean(path) != path {
			return "", errors.New("安装与备份目录必须是 /opt 下的绝对路径")
		}
	}
	architecture, err := hostUnameMachine()
	if err != nil {
		return "", err
	}
	memory, err := hostMemory()
	if err != nil {
		return "", err
	}

	dockerRoot := "/var/lib/docker"
	output, err := RunDocker("info", "--format", "{{.DockerRootDir}}")
	reported := strings.TrimSuffix(string(output), "\n")
	detected := err == nil && filepath.IsAbs(reported) && !strings.ContainsAny(reported, "\r\n\x00")
	if detected {
		dockerRoot = reported
	}
	installDisk, err := filesystem(install)
	if err != nil {
		return "", errors.New("无法读取安装目录磁盘容量")
	}
	dockerDisk, err := filesystem(dockerRoot)
	if err != nil {
		return "", errors.New("无法读取 Docker 数据目录磁盘容量")
	}
	fsType, err := resourceCommand("stat", "-Lf", "-c", "%T", "--", installDisk.path)
	if err != nil {
		return "", errors.New("无法读取安装目录文件系统类型")
	}

	usage := [3]uint64{}
	for i, path := range []string{install, filepath.Join(install, "downloads"), backup} {
		usage[i], err = directoryKiB(path)
		if err != nil {
			return "", errors.New("无法完整核实安装、下载或备份目录的占用")
		}
	}
	installed, err := isDirectory(install)
	if err != nil {
		return "", errors.New("无法核实安装目录")
	}
	fuse, err := os.Stat("/dev/fuse")
	hasFuse := err == nil && fuse.Mode()&os.ModeCharDevice != 0
	var result strings.Builder
	fmt.Fprintln(&result, "TGCLOUD_VPS_PROBE_BEGIN\nPROBE_VERSION=2")
	fmt.Fprintf(&result, "ARCHITECTURE=%s\nCPU_CORES=%d\n", architecture, onlineCPU())
	fmt.Fprintf(&result, "MEMORY_TOTAL_KIB=%d\nMEMORY_AVAILABLE_KIB=%d\nSWAP_TOTAL_KIB=%d\n", memory.total, memory.available, memory.swap)
	fmt.Fprintf(&result, "FILESYSTEM_TYPE=%s\nSTORAGE_TOTAL_BYTES=%d\nSTORAGE_AVAILABLE_BYTES=%d\n", fsType, installDisk.total, installDisk.available)
	fmt.Fprintf(&result, "INODES_TOTAL=%d\nINODES_AVAILABLE=%d\n", installDisk.inodes, installDisk.freeInodes)
	fmt.Fprintf(&result, "INSTALL_PRESENT=%d\nINSTALL_USED_KIB=%d\nDOWNLOADS_USED_KIB=%d\nBACKUPS_USED_KIB=%d\n",
		flag(installed), usage[0], usage[1], usage[2])
	fmt.Fprintf(&result, "DOCKER_SAME_FILESYSTEM=%d\nDOCKER_ROOT_DETECTED=%d\n", flag(installDisk.device == dockerDisk.device), flag(detected))
	fmt.Fprintf(&result, "DOCKER_STORAGE_TOTAL_BYTES=%d\nDOCKER_STORAGE_AVAILABLE_BYTES=%d\n", dockerDisk.total, dockerDisk.available)
	fmt.Fprintf(&result, "DOCKER_INODES_TOTAL=%d\nDOCKER_INODES_AVAILABLE=%d\n", dockerDisk.inodes, dockerDisk.freeInodes)
	fmt.Fprintf(&result, "FUSE_AVAILABLE=%d\nTGCLOUD_VPS_PROBE_END\n", flag(hasFuse))
	return result.String(), nil
}

type resourceMemory struct{ total, available, swap uint64 }

func onlineCPU() int {
	if number, err := resourceCommand("getconf", "_NPROCESSORS_ONLN"); err == nil {
		if online, err := strconv.Atoi(number); err == nil && online > 0 {
			return online
		}
	}
	return runtime.NumCPU()
}

func hostMemory() (resourceMemory, error) {
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return resourceMemory{}, errors.New("无法读取宿主内存")
	}
	return parseMemory(string(meminfo))
}

func parseMemory(meminfo string) (resourceMemory, error) {
	values := make(map[string]uint64)
	for _, line := range strings.Split(meminfo, "\n") {
		key, value, _ := strings.Cut(line, ":")
		switch key {
		case "MemTotal", "MemAvailable", "MemFree", "SwapTotal":
			fields := strings.Fields(value)
			if len(fields) == 2 && fields[1] == "kB" {
				if size, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
					values[key] = size
				}
			}
		}
	}
	available, ok := values["MemAvailable"]
	if !ok {
		available, ok = values["MemFree"]
	}
	if !ok || values["MemTotal"] == 0 || available > values["MemTotal"] {
		return resourceMemory{}, errors.New("无法核实宿主内存")
	}
	return resourceMemory{values["MemTotal"], available, values["SwapTotal"]}, nil
}

type diskResources struct {
	path                                         string
	device, total, available, inodes, freeInodes uint64
}

func filesystem(path string) (diskResources, error) {
	path = filepath.Clean(path)
	var info os.FileInfo
	for {
		var err error
		info, err = os.Stat(path)
		if err == nil {
			break
		}
		parent := filepath.Dir(path)
		if !errors.Is(err, os.ErrNotExist) || parent == path {
			return diskResources{}, err
		}
		path = parent
	}
	var usage syscall.Statfs_t
	if err := syscall.Statfs(path, &usage); err != nil {
		return diskResources{}, err
	}
	size := usage.Frsize
	if size <= 0 {
		size = usage.Bsize
	}
	if size <= 0 || usage.Blocks == 0 || usage.Bavail > usage.Blocks || usage.Blocks > math.MaxUint64/uint64(size) {
		return diskResources{}, errors.New("文件系统容量无效")
	}
	return diskResources{
		path: path, device: uint64(info.Sys().(*syscall.Stat_t).Dev),
		total: usage.Blocks * uint64(size), available: usage.Bavail * uint64(size),
		inodes: usage.Files, freeInodes: usage.Ffree,
	}, nil
}

func directoryKiB(path string) (uint64, error) {
	isDir, err := isDirectory(path)
	if err != nil || !isDir {
		return 0, err
	}
	data, err := resourceCommand("du", "-skx", "--", path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(data)
	if len(fields) < 2 {
		return 0, errors.New("目录占用数据不完整")
	}
	return strconv.ParseUint(fields[0], 10, 64)
}

func isDirectory(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil && info.IsDir(), err
}

func resourceCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	output, err := cmd.Output()
	return strings.TrimSpace(string(output)), err
}

func flag(value bool) int {
	if value {
		return 1
	}
	return 0
}
