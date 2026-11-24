package uninstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

func Supported() bool { return true }

func Run(args []string, progress io.Writer) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("卸载子命令参数无效")
	}
	action := args[0]
	opts := Options{PurgeData: true, PurgeBackups: true}
	jsonOutput := false
	confirmed := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--keep-data":
			opts.PurgeData = false
			opts.PurgeBackups = false
		case "--purge-data":
			opts.PurgeData = true
			opts.PurgeBackups = false
		case "--purge-all":
			opts.PurgeData = true
			opts.PurgeBackups = true
		case "--json":
			jsonOutput = true
		case "--yes":
			confirmed = true
		case "--plan":
			if i+1 >= len(args) {
				return nil, errors.New("缺少 --plan 摘要参数")
			}
			i++
			opts.PlanDigest = args[i]
		default:
			return nil, errors.New("未知卸载参数：" + args[i])
		}
	}
	if _, err := inspect.DockerPreflight(false); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, errors.New("卸载 TGCloud 实例需要 Linux root 权限")
	}
	paths := DefaultPaths()
	switch action {
	case "check":
		plan, err := BuildPlan(paths, opts, inspect.RunDocker, hostutil.Mountpoints)
		if err != nil {
			return nil, err
		}
		if jsonOutput {
			return plan.Summary, nil
		}
		WritePreview(plan, progress)
		fmt.Fprintln(progress, "TGCLOUD_UNINSTALL_CHECK=OK；只是预检，不是已经卸载")
		return nil, nil
	case "run":
		if !confirmed {
			return nil, errors.New("执行卸载必须显式传入 --yes 确认")
		}
		plan, err := BuildPlan(paths, opts, inspect.RunDocker, hostutil.Mountpoints)
		if err != nil {
			return nil, err
		}
		if opts.PlanDigest != "" && opts.PlanDigest != plan.Summary.PlanDigest {
			return nil, errors.New("确认期间卸载计划发生变化；请重新预检")
		}
		if err := Execute(plan, inspect.RunDocker, hostutil.Mountpoints, progress); err != nil {
			return nil, err
		}
		if jsonOutput {
			return map[string]any{"result": "ok", "plan_sha256": plan.Summary.PlanDigest}, nil
		}
		fmt.Fprintln(progress, "TGCLOUD_UNINSTALL_RESULT=OK；所选受管运行资源及路由已移除，系统应用未卸载。")
		return nil, nil
	default:
		return nil, errors.New("卸载子命令参数无效")
	}
}

func Execute(plan *Plan, docker inspect.DockerRunner, mountFn func() ([]string, error), progress io.Writer) error {
	editionLock, err := inspect.LockInstance()
	if err != nil {
		return err
	}
	defer editionLock.Close()

	proxyLock, err := lockProxy(plan.Paths.ProxyRoot)
	if err != nil {
		return err
	}
	if proxyLock != nil {
		defer proxyLock.Close()
	}

	rechecked, err := BuildPlan(plan.Paths, Options{PurgeData: plan.PurgeData, PurgeBackups: plan.PurgeBackups}, docker, mountFn)
	if err != nil {
		return err
	}
	if rechecked.Summary.PlanDigest != plan.Summary.PlanDigest {
		return errors.New("确认期间实例/网络/HTTPS 状态发生变化；停止，请重新预检")
	}

	if plan.Summary.HasHTTPSRoute && !plan.RemoveProxy {
		return errors.New("共享代理仍有其他活动路由或外部接入，请先通过 tgcloud 管理菜单移除对应路由后再卸载")
	}

	order := []string{lanContainer, inspect.BotName, inspect.GatewayName}
	for _, name := range plan.Summary.Containers {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	for _, name := range order {
		item, ok := plan.Containers[name]
		if !ok {
			continue
		}
		if err := removeContainer(docker, name, item, progress); err != nil {
			return err
		}
	}

	for _, name := range plan.Summary.Networks {
		item := plan.Networks[name]
		if err := removeNetwork(docker, name, item, progress); err != nil {
			return err
		}
	}

	afterContainers, afterNetworks, err := dockerSnapshot(docker)
	if err != nil {
		return err
	}
	for name := range plan.Containers {
		if _, still := afterContainers[name]; still {
			return errors.New("卸载后容器复检未通过；不输出成功标记")
		}
	}
	for name := range plan.Networks {
		if _, still := afterNetworks[name]; still {
			return errors.New("卸载后网络复检未通过；不输出成功标记")
		}
	}

	lanDir := filepath.Join(plan.Paths.LANRoot, editionKey)
	if pathExists(lanDir) {
		if err := safeDeleteTree(lanDir, []string{lanDir}, mountFn); err != nil {
			return err
		}
		logLine(progress, "已移除内网入口配置："+lanDir)
	}

	if plan.PurgeData {
		points, err := mountFn()
		if err != nil {
			return err
		}
		if hasMountUnder(points, plan.Paths.InstallDir) {
			return errors.New("删除前发现挂载点；停止，不触碰云盘文件")
		}
		if pathExists(plan.Paths.InstallDir) {
			if err := safeDeleteTree(plan.Paths.InstallDir, []string{plan.Paths.InstallDir}, mountFn); err != nil {
				return err
			}
			logLine(progress, "已删除本地实例目录："+plan.Paths.InstallDir)
		}
		if plan.RemoveProxy && pathExists(plan.Paths.ProxyRoot) {
			var allowed []string
			for _, child := range proxyChildren {
				allowed = append(allowed, filepath.Join(plan.Paths.ProxyRoot, child))
			}
			for _, target := range allowed {
				if pathExists(target) {
					if err := safeDeleteTree(target, allowed, mountFn); err != nil {
						return err
					}
				}
			}
			logLine(progress, "已删除不再使用的共享代理配置/证书；事务锁文件保留。")
		}
		for _, dir := range plan.Summary.BackupDirs {
			pattern := instanceBackupRegex
			if dir == plan.Paths.ProxyBackupDir {
				pattern = proxyBackupRegex
			}
			entries, present, err := inspectBackupDir(dir, pattern)
			if err != nil || !present || !slices.Equal(entries, plan.BackupEntries[dir]) {
				return errors.New("备份在删除前变化；停止，不删除未经确认的新备份")
			}
			if err := safeDeleteTree(dir, plan.Summary.BackupDirs, mountFn); err != nil {
				return err
			}
			logLine(progress, "已删除历史备份目录："+dir)
		}
	}

	if plan.PurgeBackups {
		cleanupManagerIfUnused(plan.Paths, afterContainers, afterNetworks, mountFn, progress)
	}
	return nil
}

func cleanupManagerIfUnused(paths Paths, containers map[string]DockerContainer, networks map[string]DockerNetwork, mountFn func() ([]string, error), progress io.Writer) {
	if !pathExists(paths.CLIRoot) && !pathExists(paths.CLICommand) {
		return
	}
	for name, item := range containers {
		if strings.HasPrefix(name, "tgcloud-") || item.Labels[managedLabel] == "true" || strings.HasPrefix(item.Labels[composeLabel+"project"], "tgcloud-") {
			logLine(progress, "仍有受管容器存在；保留 tgcloud 管理命令及其代码。")
			return
		}
	}
	for name, item := range networks {
		if strings.HasPrefix(name, "tgcloud-") || item.Labels[managedLabel] == "true" || strings.HasPrefix(item.Labels[composeLabel+"project"], "tgcloud-") {
			logLine(progress, "仍有受管网络存在；保留 tgcloud 管理命令及其代码。")
			return
		}
	}
	for _, check := range []string{paths.InstallDir, paths.BackupDir, filepath.Join(paths.LANRoot, editionKey), paths.ProxyBackupDir, filepath.Join(paths.WebDAVRoot, editionKey, "config.json")} {
		if pathExists(check) {
			logLine(progress, "仍有实例、入口或保留数据/备份；保留 tgcloud 管理命令及其代码。")
			return
		}
	}
	points, err := mountFn()
	if err != nil || hasMountUnder(points, paths.CLIRoot) || hasMountUnder(points, paths.CLICommand) {
		logLine(progress, "警告：无法确认管理目录挂载状态；保留 tgcloud 管理命令及其代码。")
		return
	}
	if pathExists(paths.CLICommand) {
		if err := os.Remove(paths.CLICommand); err != nil {
			logLine(progress, "警告：无法移除 tgcloud 快捷命令，请人工核对。")
			return
		}
	}
	if pathExists(paths.CLIRoot) {
		if err := safeDeleteTree(paths.CLIRoot, []string{paths.CLIRoot}, mountFn); err != nil {
			logLine(progress, "警告：无法移除受管管理代码目录，请人工核对。")
			return
		}
	}
	logLine(progress, "最后一个实例已完整卸载：tgcloud 快捷命令及受管管理代码已移除。")
}

func lockProxy(proxyRoot string) (*os.File, error) {
	if !pathExists(proxyRoot) {
		return nil, nil
	}
	if err := checkedPath(proxyRoot); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(proxyRoot, "transaction.lock")
	file, err := hostutil.OpenPrivate(lockPath, syscall.O_CREAT|syscall.O_RDWR, hostutil.ManagedOwnerUID)
	if err != nil {
		return nil, errors.New("无法安全获取共享代理锁")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("另一个操作正在使用共享代理锁；未执行卸载")
	}
	return file, nil
}

func removeContainer(docker inspect.DockerRunner, name string, item DockerContainer, progress io.Writer) error {
	if !hex64Pattern.MatchString(item.ID) {
		return errors.New("容器 ID 无效")
	}
	if _, err := docker("container", "stop", "--time", "60", item.ID); err != nil {
		return errors.New("容器停止失败；没有显示原始输出，停止后续删除")
	}
	if _, err := docker("container", "rm", item.ID); err != nil {
		return errors.New("容器移除失败；停止，请重新预检核对部分完成状态")
	}
	logLine(progress, "已移除容器："+name)
	return nil
}

func removeNetwork(docker inspect.DockerRunner, name string, item DockerNetwork, progress io.Writer) error {
	const tmpl = `{"id":{{json .Id}},"containers":{{json .Containers}},"labels":{{json .Labels}}}`
	out, err := docker("network", "inspect", "--format", tmpl, item.ID)
	if err != nil {
		return errors.New("网络删除前复检失败；停止")
	}
	var current DockerNetwork
	if json.Unmarshal(out, &current) != nil || current.ID != item.ID || len(current.Containers) != 0 {
		return errors.New("网络归属或连接已变化；不强制断开任何容器")
	}
	if _, err := docker("network", "rm", item.ID); err != nil {
		return errors.New("网络移除失败；停止，请核对部分完成状态")
	}
	logLine(progress, "已移除网络："+name)
	return nil
}

func safeDeleteTree(target string, allowed []string, mountFn func() ([]string, error)) error {
	if !slices.Contains(allowed, target) {
		return errors.New("删除目标不在本次已确认的精确目录白名单中")
	}
	if err := checkedPath(target); err != nil {
		return err
	}
	points, err := mountFn()
	if err != nil {
		return err
	}
	if hasMountUnder(points, target) {
		return errors.New("删除前发现挂载点；停止，不触碰云盘文件")
	}
	if err := os.RemoveAll(target); err != nil {
		return errors.New("删除目标失败：" + target)
	}
	if pathExists(target) {
		return errors.New("删除后复检失败；停止，不输出成功")
	}
	return nil
}

func logLine(out io.Writer, msg string) {
	if out != nil {
		fmt.Fprintln(out, msg)
	}
}
