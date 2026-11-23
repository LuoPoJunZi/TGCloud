package storage

import (
	"errors"
	"fmt"
	"math"
)

const (
	GiB               = 1024 * 1024 * 1024
	MinimumInstallMiB = 1800
	storageTolerance  = 1e-9
)

// Resources is the existing resource observation, shared with the Python fallback.
type Resources struct {
	Architecture     string `json:"architecture"`
	CPU              int    `json:"cpu_cores"`
	MemoryTotal      uint64 `json:"memory_total_bytes"`
	MemoryAvailable  uint64 `json:"memory_available_bytes"`
	Total            uint64 `json:"storage_total_bytes"`
	Available        uint64 `json:"storage_available_bytes"`
	Inodes           uint64 `json:"inodes_total"`
	FreeInodes       uint64 `json:"inodes_available"`
	Installed        bool   `json:"install_present"`
	Downloads        uint64 `json:"downloads_used_bytes"`
	DockerShared     bool   `json:"docker_same_filesystem"`
	DockerAvailable  uint64 `json:"docker_storage_available_bytes"`
	DockerTotal      uint64 `json:"docker_storage_total_bytes"`
	DockerInodes     uint64 `json:"docker_inodes_total"`
	DockerFreeInodes uint64 `json:"docker_inodes_available"`
	Fuse             bool   `json:"fuse_available"`
}

type Request struct {
	Resources    Resources `json:"resources"`
	RequiresFuse bool      `json:"requires_fuse"`
	Budget       float64   `json:"budget_gb"`
	Reserve      float64   `json:"reserve_gb"`
}

type Assessment struct {
	Safe     bool    `json:"safe"`
	Required float64 `json:"required_available_gb"`
	Reason   string  `json:"reason"`
}

type Plan struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Budget  uint64 `json:"budget_gb"`
	Reserve uint64 `json:"reserve_gb"`
	Assessment
}

type Advice struct {
	Profile     string  `json:"performance_profile"`
	Preferred   *string `json:"preferred_key"`
	Balanced    Plan    `json:"balanced"`
	StreamFirst Plan    `json:"stream_first"`
}

func Decide(operation string, request Request) (any, error) {
	r := request.Resources
	if r.CPU < 1 || r.CPU > 4096 || r.MemoryTotal == 0 || r.MemoryAvailable > r.MemoryTotal ||
		r.Total == 0 || r.Available > r.Total || r.DockerTotal == 0 || r.DockerAvailable > r.DockerTotal {
		return nil, errors.New("VPS 资源探测结果不合理")
	}
	overhead := 6.0
	if r.Installed {
		overhead = 3
	}
	available, occupied := float64(r.Available)/GiB, float64(r.Downloads)/GiB
	blocker := storageBlocker(r, request.RequiresFuse, overhead)
	switch operation {
	case "recommend":
		return recommendInstallStorage(r, overhead, available, occupied, blocker), nil
	case "assess":
		if !validStorageGB(request.Budget) || !validStorageGB(request.Reserve) {
			return nil, errors.New("请输入有限的正数 GB")
		}
		required := max(8, request.Reserve+overhead+max(0, request.Budget-occupied))
		if blocker != "" {
			return Assessment{false, required, blocker}, nil
		}
		if available+storageTolerance < required {
			return Assessment{false, required, fmt.Sprintf("当前配置至少需要 %.1fGB 可用空间，VPS 只有 %.1fGB", required, available)}, nil
		}
		return Assessment{true, required, "当前配置通过部署前容量校验"}, nil
	default:
		return nil, errors.New("未知的资源预算操作")
	}
}

func validStorageGB(value float64) bool {
	return value > 0 && value <= 100000
}

func storageBlocker(r Resources, requiresFuse bool, dockerHeadroom float64) string {
	switch r.Architecture {
	case "x86_64", "amd64", "aarch64", "arm64":
	default:
		return "当前 CPU 架构暂不支持：" + r.Architecture
	}
	if r.MemoryTotal < MinimumInstallMiB*1024*1024 {
		return fmt.Sprintf("VPS 总内存不足 %.1fGB 安装门槛", float64(MinimumInstallMiB)/1024)
	}
	if requiresFuse && !r.Fuse {
		return "VPS 未提供 /dev/fuse，无法运行受管 CloudDrive2"
	}
	if lowInodes(r.Inodes, r.FreeInodes) {
		return "目标文件系统可用 inode 低于安全线"
	}
	if !r.DockerShared {
		available := float64(r.DockerAvailable) / GiB
		if available+storageTolerance < dockerHeadroom {
			return fmt.Sprintf("Docker 数据文件系统至少需要 %.0fGB 可用空间，当前只有 %.1fGB", dockerHeadroom, available)
		}
		if lowInodes(r.DockerInodes, r.DockerFreeInodes) {
			return "Docker 数据文件系统可用 inode 低于安全线"
		}
	}
	return ""
}

func lowInodes(total, available uint64) bool {
	return total != 0 && float64(available) < max(1000, math.Ceil(float64(total)*0.01))
}

func storagePerformance(r Resources) string {
	memory := float64(r.MemoryTotal) / GiB
	if float64(r.MemoryAvailable)/GiB < min(2, memory*0.25) {
		return "保守"
	}
	if r.CPU >= 4 && memory >= 7.5 {
		return "批量"
	}
	if r.CPU >= 2 && memory >= 3.5 {
		return "标准"
	}
	return "保守"
}

func recommendInstallStorage(r Resources, overhead, available, occupied float64, blocker string) Advice {
	total := float64(r.Total) / GiB
	reserve := max(8, math.Ceil(total*0.20))
	pool := max(0, available-reserve-overhead)
	makePlan := func(key, label string, portion float64) Plan {
		budget := max(math.Ceil(occupied), math.Floor(pool*portion))
		required := reserve + overhead + max(0, budget-occupied)
		plan := Plan{Key: key, Label: label, Budget: uint64(budget), Reserve: uint64(reserve),
			Assessment: Assessment{Required: required}}
		switch {
		case blocker != "":
			plan.Reason = blocker
		case budget < 2:
			plan.Reason = "扣除磁盘保留线和安装余量后不足 2GB 本地任务预算"
		case available+storageTolerance < required:
			plan.Reason = fmt.Sprintf("至少需要 %.1fGB 可用空间", required)
		default:
			plan.Safe, plan.Reason = true, "满足任务预算、磁盘保留线和安装余量"
		}
		return plan
	}
	result := Advice{Profile: storagePerformance(r),
		StreamFirst: makePlan("stream_first", "流式优先", .25), Balanced: makePlan("balanced", "均衡模式", .60)}
	switch {
	case result.StreamFirst.Safe && (result.Profile == "保守" || total < 50 || available < 40 || result.Balanced.Budget < 12):
		result.Preferred = &result.StreamFirst.Key
	case result.Balanced.Safe:
		result.Preferred = &result.Balanced.Key
	case result.StreamFirst.Safe:
		result.Preferred = &result.StreamFirst.Key
	}
	return result
}
