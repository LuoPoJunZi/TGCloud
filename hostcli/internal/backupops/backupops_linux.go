package backupops

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

const (
	defaultProxyRoot       = "/opt/tgcloud-proxy"
	defaultProxyBackupRoot = "/opt/tgcloud-proxy-backups"
)

var (
	instanceBackupRegex = regexp.MustCompile(`^(config-[A-Za-z0-9]{8}\.tar\.gz|database-[A-Za-z0-9]{8}\.db|env-[A-Za-z0-9]{8}\.env)$`)
	proxyArchiveRegex   = regexp.MustCompile(`^proxy-([0-9]{8}T[0-9]{6}Z)-[0-9a-f]{32}\.tar\.gz$`)
	sha256Regex         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type InstanceBackupItem struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	ModTime   string `json:"mtime_utc"`
}

func Supported() bool { return true }

func Run(args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("备份子命令参数无效")
	}
	switch args[0] {
	case "list":
		dir := inspect.UpgradeBackupDir
		if len(args) >= 2 && !strings.HasPrefix(args[1], "--") {
			dir = args[1]
		}
		items, err := ListInstanceBackups(dir)
		if err != nil {
			return nil, err
		}
		return map[string]any{"directory": dir, "items": items}, nil
	case "snapshot-create":
		if len(args) != 5 || args[4] != "--yes" {
			return nil, errors.New("用法：backups snapshot-create <install-dir> <dest-dir> <version> --yes")
		}
		archive, err := CreateInstanceSnapshot(args[1], args[2], args[3], inspect.RunDocker)
		if err != nil {
			return nil, err
		}
		return map[string]any{"archive": archive}, nil
	case "snapshot-preflight":
		if len(args) != 4 {
			return nil, errors.New("用法：backups snapshot-preflight <archive-path> <scratch-dir> <target-version>")
		}
		count, err := PreflightInstanceSnapshot(args[1], args[2], args[3])
		if err != nil {
			return nil, err
		}
		return map[string]any{"archive": args[1], "files": count}, nil
	case "proxy-list":
		dir := defaultProxyBackupRoot
		if len(args) >= 2 && !strings.HasPrefix(args[1], "--") {
			dir = args[1]
		}
		items, err := ListProxyBackups(dir)
		if err != nil {
			return nil, err
		}
		return map[string]any{"directory": dir, "items": items}, nil
	case "proxy-create":
		proxyRoot, backupRoot := defaultProxyRoot, defaultProxyBackupRoot
		if len(args) >= 2 && !strings.HasPrefix(args[1], "--") {
			proxyRoot = args[1]
		}
		if len(args) >= 3 && !strings.HasPrefix(args[2], "--") {
			backupRoot = args[2]
		}
		archive, count, err := CreateProxyBackup(proxyRoot, backupRoot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"archive": archive, "entries": count}, nil
	case "proxy-verify":
		if len(args) < 2 {
			return nil, errors.New("用法：backups proxy-verify <archive> [--drill <backup-root>]")
		}
		archive := args[1]
		drillRoot := ""
		for i := 2; i < len(args); i++ {
			if args[i] == "--drill" && i+1 < len(args) {
				i++
				drillRoot = args[i]
			}
		}
		records, err := VerifyProxyBackup(archive)
		if err != nil {
			return nil, err
		}
		if drillRoot != "" {
			if _, err := RecoveryDrill(archive, drillRoot); err != nil {
				return nil, err
			}
		}
		return map[string]any{"archive": archive, "entries": len(records), "drilled": drillRoot != ""}, nil
	case "proxy-prune-preview", "proxy-prune":
		keep := 5
		dir := defaultProxyBackupRoot
		planID := ""
		confirmed := false
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--keep":
				if i+1 >= len(args) {
					return nil, errors.New("缺少 --keep 参数")
				}
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil {
					return nil, errors.New("--keep 必须为整数")
				}
				keep = n
			case "--dir":
				if i+1 >= len(args) {
					return nil, errors.New("缺少 --dir 参数")
				}
				i++
				dir = args[i]
			case "--plan":
				if i+1 >= len(args) {
					return nil, errors.New("缺少 --plan 参数")
				}
				i++
				planID = args[i]
			case "--yes":
				confirmed = true
			case "--json":
			default:
				return nil, errors.New("未知代理备份清理参数：" + args[i])
			}
		}
		if args[0] == "proxy-prune-preview" {
			plan, err := PlanProxyPrune(dir, keep)
			if err != nil {
				return nil, err
			}
			return map[string]any{"plan": plan}, nil
		}
		if !confirmed {
			return nil, errors.New("清理代理备份必须显式传入 --yes 确认")
		}
		plan, err := ExecuteProxyPrune(dir, keep, planID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"plan": plan}, nil
	default:
		return nil, errors.New("备份子命令参数无效")
	}
}

func ListInstanceBackups(dir string) ([]InstanceBackupItem, error) {
	if !hostutil.SafePath(dir, false) {
		return nil, errors.New("备份目录经过链接或路径不安全")
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []InstanceBackupItem{}, nil
	}
	if err != nil || !info.IsDir() || !hostutil.PrivateOwner(info, hostutil.ManagedOwnerUID) {
		return nil, errors.New("备份目录所有者/权限不符合受管私密目录")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.New("无法读取实例备份目录")
	}
	items := make([]InstanceBackupItem, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		childPath := filepath.Join(dir, name)
		childInfo, err := os.Lstat(childPath)
		if err != nil || !instanceBackupRegex.MatchString(name) || !childInfo.Mode().IsRegular() ||
			!hostutil.PrivateOwner(childInfo, hostutil.ManagedOwnerUID) {
			return nil, errors.New("备份目录含未知文件、链接或不安全归属")
		}
		items = append(items, InstanceBackupItem{
			Name:      name,
			SizeBytes: childInfo.Size(),
			ModTime:   childInfo.ModTime().UTC().Format("2006-01-02 15:04:05 UTC"),
		})
	}
	slices.SortFunc(items, func(a, b InstanceBackupItem) int {
		return strings.Compare(a.Name, b.Name)
	})
	return items, nil
}

func ensurePrivateDir(dir string) error {
	if !hostutil.SafePath(dir, false) {
		return errors.New("目录路径经过链接或不安全")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("无法创建私密目录")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !hostutil.PrivateOwner(info, hostutil.ManagedOwnerUID) {
		if err == nil && info.IsDir() {
			if chmodErr := os.Chmod(dir, 0700); chmodErr == nil {
				info, err = os.Lstat(dir)
			}
		}
		if err != nil || !info.IsDir() || !hostutil.PrivateOwner(info, hostutil.ManagedOwnerUID) {
			return errors.New("私密目录权限或归属不符合要求")
		}
	}
	return nil
}
