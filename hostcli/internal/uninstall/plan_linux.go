package uninstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/entry"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

const (
	editionKey      = "clouddrive2"
	dockerNetwork   = "tgcloud-clouddrive2-net"
	lanContainer    = "tgcloud-lan-cd2"
	proxyNetwork    = "tgcloud-proxy_default"
	composeLabel    = "com.docker.compose."
	managedLabel    = "com.tgcloud.managed"
	roleLabel       = "com.tgcloud.role"
	installModeKey  = "com.tgcloud.install-mode"
	defaultLANRoot  = "/opt/tgcloud-lan"
	defaultLockRoot = "/opt/tgcloud-cli-locks"
	defaultCLIRoot  = "/opt/tgcloud-cli"
	defaultCLICmd   = "/usr/local/bin/tgcloud"
)

var (
	hex64Pattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	projectPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	instanceBackupRegex = regexp.MustCompile(`^(config-[A-Za-z0-9]{8}\.tar\.gz|database-[A-Za-z0-9]{8}\.db|env-[A-Za-z0-9]{8}\.env)$`)
	proxyBackupRegex    = regexp.MustCompile(`^proxy-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{32}\.tar\.gz(\.sha256)?$`)
	proxyChildren       = []string{"state", "nginx", "certbot", "docker-compose.yml", "external-auth"}
)

type Paths struct {
	InstallDir     string
	BackupDir      string
	ProxyRoot      string
	ProxyBackupDir string
	LANRoot        string
	WebDAVRoot     string
	LockRoot       string
	CLIRoot        string
	CLICommand     string
}

func DefaultPaths() Paths {
	return Paths{
		InstallDir:     inspect.InstallDirectory,
		BackupDir:      inspect.UpgradeBackupDir,
		ProxyRoot:      entry.HTTPSRoot,
		ProxyBackupDir: "/opt/tgcloud-proxy-backups",
		LANRoot:        defaultLANRoot,
		WebDAVRoot:     "/opt/tgcloud-webdav",
		LockRoot:       defaultLockRoot,
		CLIRoot:        defaultCLIRoot,
		CLICommand:     defaultCLICmd,
	}
}

type Options struct {
	PurgeData    bool
	PurgeBackups bool
	PlanDigest   string
}

type DockerContainer struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Running bool              `json:"running"`
	Labels  map[string]string `json:"labels"`
	Mounts  []DockerMount     `json:"mounts"`
	Network string            `json:"network"`
}

type DockerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

type DockerNetwork struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Driver     string                 `json:"driver"`
	Labels     map[string]string      `json:"labels"`
	Containers map[string]interface{} `json:"containers"`
}

type PlanSummary struct {
	Edition        string   `json:"edition"`
	InstallDir     string   `json:"install_dir"`
	Containers     []string `json:"containers"`
	Networks       []string `json:"networks"`
	RemoveProxy    bool     `json:"remove_proxy"`
	PurgeData      bool     `json:"purge_data"`
	PurgeBackups   bool     `json:"purge_backups"`
	BackupDirs     []string `json:"backup_dirs"`
	HasHTTPSRoute  bool     `json:"has_https_route"`
	ProxyStateVer  int      `json:"proxy_state_version"`
	PlanDigest     string   `json:"plan_sha256"`
	ManagerCleanup bool     `json:"manager_cleanup_eligible"`
}

type Plan struct {
	Summary       PlanSummary
	Paths         Paths
	Containers    map[string]DockerContainer
	Networks      map[string]DockerNetwork
	ProxyState    proxyState
	BackupEntries map[string][]string
	PurgeData     bool
	PurgeBackups  bool
	RemoveProxy   bool
}

type proxyState struct {
	Version        int                        `json:"version"`
	Project        string                     `json:"project"`
	Domains        map[string]json.RawMessage `json:"domains"`
	ExternalWebDAV map[string]json.RawMessage `json:"external_webdav,omitempty"`
}

func WritePreview(plan *Plan, out io.Writer) {
	if out == nil {
		return
	}
	fmt.Fprintln(out, "TGCloud 卸载计划（确认前不停止容器、不修改路由）")
	fmt.Fprintln(out, "实例：CloudDrive2")
	containers := "无"
	if len(plan.Summary.Containers) > 0 {
		containers = strings.Join(plan.Summary.Containers, ", ")
	}
	networks := "无"
	if len(plan.Summary.Networks) > 0 {
		networks = strings.Join(plan.Summary.Networks, ", ")
	}
	fmt.Fprintln(out, "容器："+containers)
	fmt.Fprintln(out, "网络："+networks)
	fmt.Fprintln(out, "移除所选实例的 HTTPS 路由和独立 LAN 管理入口；其他项目资源保留。")
	if plan.RemoveProxy {
		fmt.Fprintln(out, "共享 Nginx/Certbot：没有剩余 HTTPS 路由，移除受管容器")
	} else {
		fmt.Fprintln(out, "共享 Nginx/Certbot：保留，其他部署仍在使用")
	}
	if plan.PurgeData {
		fmt.Fprintln(out, "删除本地数据："+plan.Paths.InstallDir)
	} else {
		fmt.Fprintln(out, "保留本地配置/数据："+plan.Paths.InstallDir)
	}
	if plan.PurgeBackups {
		fmt.Fprintln(out, "删除历史备份："+plan.Paths.BackupDir)
	} else {
		fmt.Fprintln(out, "历史备份保留："+plan.Paths.BackupDir)
	}
	if slices.Contains(plan.Summary.BackupDirs, plan.Paths.ProxyBackupDir) {
		fmt.Fprintln(out, "删除不再使用的共享代理备份："+plan.Paths.ProxyBackupDir)
	} else {
		fmt.Fprintln(out, "共享代理历史备份保留；所选范围之外的数据不会清理。")
	}
	fmt.Fprintln(out, "镜像缓存、并发锁元数据和系统软件保留。")
	fmt.Fprintln(out, "请先结束进行中的传输并自行备份；停止容器会中断任务。")
}

func BuildPlan(paths Paths, opts Options, docker inspect.DockerRunner, mountFn func() ([]string, error)) (*Plan, error) {
	purgeData := opts.PurgeData || opts.PurgeBackups
	purgeBackups := opts.PurgeBackups
	if err := checkComposeTransaction(paths.ProxyRoot); err != nil {
		return nil, err
	}
	composeInstances, err := readComposeInstances(paths.ProxyRoot)
	if err != nil {
		return nil, err
	}
	if _, owned := composeInstances[editionKey]; owned {
		return nil, errors.New("所选实例已由独立 Compose HTTPS 登记；请使用部署包 HTTPS 工具移除路由，再按 Compose 指南维护；此卸载器不接管")
	}
	containers, networks, err := dockerSnapshot(docker)
	if err != nil {
		return nil, err
	}
	state, stateExists, err := readProxyState(paths.ProxyRoot)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(paths.ProxyRoot, "state/external-transaction.json")); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("外部入口事务尚未恢复；先用 tgcloud proxy recover 核对，不删除配置或恢复材料。")
	}
	if _, hasExt := state.ExternalWebDAV[editionKey]; hasExt {
		return nil, errors.New("所选网关仍被外部 WebDAV 依赖；请先在 tgcloud → 2. 访问入口管理 → 3. 外部 WebDAV 管理 → 7. 卸载外部服务 中处理，不静默中断外部入口。")
	}
	if _, err := os.Lstat(filepath.Join(paths.WebDAVRoot, editionKey, "config.json")); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("所选网关仍被外部 WebDAV 依赖；请先在 tgcloud → 2. 访问入口管理 → 3. 外部 WebDAV 管理 → 7. 卸载外部服务 中处理，不静默中断外部入口。")
	}
	if err := checkedPath(paths.InstallDir); err != nil {
		return nil, err
	}

	selected := make(map[string]DockerContainer)
	selectedNetworks := make(map[string]DockerNetwork)
	_, botExists := containers[inspect.BotName]
	_, gwExists := containers[inspect.GatewayName]
	_, lanExists := containers[lanContainer]
	_, netExists := networks[dockerNetwork]
	_, hasRoute := state.Domains[editionKey]
	_, dirStatErr := os.Lstat(paths.InstallDir)
	dirExists := !errors.Is(dirStatErr, os.ErrNotExist)
	if dirExists || botExists || gwExists || lanExists || netExists || hasRoute {
		if _, err := inspect.InstalledVersion(paths.InstallDir); err != nil {
			return nil, errors.New("实例缺少 TGCloud 有效来源标记；停止处理未知资源。")
		}
	}

	projects := make(map[string]bool)
	for _, name := range []string{inspect.BotName, inspect.GatewayName} {
		item, ok := containers[name]
		if !ok {
			continue
		}
		project, err := verifyCoreIdentity(paths.InstallDir, name, item)
		if err != nil {
			return nil, err
		}
		projects[project] = true
		selected[name] = item
	}
	if len(projects) > 1 {
		return nil, errors.New("Bot 与网关不属于同一个 Compose 项目")
	}
	for name, item := range containers {
		if name == inspect.BotName || name == inspect.GatewayName {
			continue
		}
		if projects[item.Labels[composeLabel+"project"]] || item.Labels[composeLabel+"project.working_dir"] == paths.InstallDir {
			return nil, errors.New("安装目录关联了其他容器；停止，不使用 remove-orphans")
		}
	}
	lanDir := filepath.Join(paths.LANRoot, editionKey)
	if item, ok := containers[lanContainer]; ok {
		if err := verifyLANIdentity(lanDir, item); err != nil {
			return nil, err
		}
		selected[lanContainer] = item
	}
	if err := checkedPath(lanDir); err != nil {
		return nil, err
	}
	if err := verifyLANDir(lanDir, paths.InstallDir); err != nil {
		return nil, err
	}
	if network, ok := networks[dockerNetwork]; ok {
		project := network.Labels[composeLabel+"project"]
		if network.Driver != "bridge" || network.Labels[composeLabel+"network"] != dockerNetwork ||
			project == "" || (len(projects) > 0 && !projects[project]) {
			return nil, errors.New("同名网络缺少预期 Compose 归属；停止")
		}
		if len(projects) == 0 && project != filepath.Base(paths.InstallDir) {
			return nil, errors.New("残留网络缺少可验证的标准项目身份")
		}
		if hasExternalEndpoints(network, selected) {
			return nil, errors.New("项目网络有外部容器连接；停止，不强制断开")
		}
		selectedNetworks[dockerNetwork] = network
	}

	remainingDomains := 0
	for k := range state.Domains {
		if k != editionKey {
			remainingDomains++
		}
	}
	removeProxy := remainingDomains == 0 && len(state.ExternalWebDAV) == 0 && state.Version != 3 && len(composeInstances) == 0
	for name, item := range containers {
		if item.Labels[roleLabel] == "maintenance-dry-run" {
			return nil, errors.New("检测到代理证书维护辅助容器；先完成/核对维护，不在卸载时接管")
		}
		if name == entry.HTTPSNginx || name == entry.HTTPSCertbot || item.Labels[composeLabel+"project"] == entry.HTTPSProject {
			if err := verifyProxyIdentity(paths.ProxyRoot, name, item); err != nil {
				return nil, err
			}
			if removeProxy {
				selected[name] = item
			}
		}
	}
	if !stateExists {
		if _, ok := containers[entry.HTTPSNginx]; ok {
			return nil, errors.New("共享代理容器存在但状态缺失；停止")
		}
		if _, ok := containers[entry.HTTPSCertbot]; ok {
			return nil, errors.New("共享代理容器存在但状态缺失；停止")
		}
	}
	if removeProxy {
		if network, ok := networks[proxyNetwork]; ok {
			if network.Driver != "bridge" || network.Labels[composeLabel+"project"] != entry.HTTPSProject ||
				network.Labels[composeLabel+"network"] != "default" || hasExternalEndpoints(network, selected) {
				return nil, errors.New("共享代理网络有外部连接或类型不符；不强制删除")
			}
			selectedNetworks[proxyNetwork] = network
		}
	}

	points, err := mountFn()
	if err != nil {
		return nil, err
	}
	if pathExists(lanDir) {
		if hasMountUnder(points, lanDir) {
			return nil, errors.New("内网入口目录下存在挂载点；不递归清理")
		}
		if otherContainerOverlaps(containers, selected, lanDir) {
			return nil, errors.New("其他容器正在使用内网入口文件；停止")
		}
	}

	backupEntries := make(map[string][]string)
	var backupDirs []string
	if purgeData {
		if dirExists && !botExists && !gwExists {
			if err := verifyRetainedCompose(paths.InstallDir); err != nil {
				return nil, err
			}
		}
		purgeRoots := []string{paths.InstallDir}
		if removeProxy {
			purgeRoots = append(purgeRoots, paths.ProxyRoot)
		}
		if purgeBackups {
			entries, present, err := inspectBackupDir(paths.BackupDir, instanceBackupRegex)
			if err != nil {
				return nil, err
			}
			if present {
				backupEntries[paths.BackupDir] = entries
				backupDirs = append(backupDirs, paths.BackupDir)
				purgeRoots = append(purgeRoots, paths.BackupDir)
			}
			if removeProxy {
				pEntries, pPresent, err := inspectBackupDir(paths.ProxyBackupDir, proxyBackupRegex)
				if err != nil {
					return nil, err
				}
				if pPresent {
					backupEntries[paths.ProxyBackupDir] = pEntries
					backupDirs = append(backupDirs, paths.ProxyBackupDir)
					purgeRoots = append(purgeRoots, paths.ProxyBackupDir)
				}
			}
		}
		for _, root := range purgeRoots {
			if err := checkedPath(root); err != nil {
				return nil, err
			}
			if hasMountUnder(points, root) {
				return nil, errors.New("数据目录下仍有挂载点（可能是云盘/FUSE）；不递归删除、不强制卸载，请处理后重新预检")
			}
			if otherContainerOverlaps(containers, selected, root) {
				return nil, errors.New("其他容器正在使用待删除的数据；停止")
			}
		}
		if removeProxy && pathExists(paths.ProxyRoot) {
			entries, err := os.ReadDir(paths.ProxyRoot)
			if err != nil {
				return nil, errors.New("无法读取共享代理目录；不递归删除")
			}
			for _, item := range entries {
				name := item.Name()
				if name != "transaction.lock" && !slices.Contains(proxyChildren, name) {
					return nil, errors.New("共享代理目录有未知文件；不递归删除")
				}
			}
		}
	}

	containerNames := make([]string, 0, len(selected))
	for name := range selected {
		containerNames = append(containerNames, name)
	}
	slices.Sort(containerNames)
	networkNames := make([]string, 0, len(selectedNetworks))
	for name := range selectedNetworks {
		networkNames = append(networkNames, name)
	}
	slices.Sort(networkNames)
	slices.Sort(backupDirs)

	summary := PlanSummary{
		Edition:        editionKey,
		InstallDir:     paths.InstallDir,
		Containers:     containerNames,
		Networks:       networkNames,
		RemoveProxy:    removeProxy,
		PurgeData:      purgeData,
		PurgeBackups:   purgeBackups,
		BackupDirs:     backupDirs,
		HasHTTPSRoute:  hasRoute,
		ProxyStateVer:  state.Version,
		ManagerCleanup: purgeBackups,
	}
	summary.PlanDigest = computePlanDigest(summary, selected, selectedNetworks, backupEntries)

	return &Plan{
		Summary:       summary,
		Paths:         paths,
		Containers:    selected,
		Networks:      selectedNetworks,
		ProxyState:    state,
		BackupEntries: backupEntries,
		PurgeData:     purgeData,
		PurgeBackups:  purgeBackups,
		RemoveProxy:   removeProxy,
	}, nil
}

func dockerSnapshot(docker inspect.DockerRunner) (map[string]DockerContainer, map[string]DockerNetwork, error) {
	containers := make(map[string]DockerContainer)
	out, err := docker("ps", "-a", "--no-trunc", "--format", "{{.ID}}")
	if err != nil {
		return nil, nil, errors.New("Docker 容器清单不可读；停止，不把错误当作空清单")
	}
	var ids []string
	for _, line := range strings.Fields(string(out)) {
		if !hex64Pattern.MatchString(line) {
			return nil, nil, errors.New("Docker 容器清单不可读；停止，不把错误当作空清单")
		}
		ids = append(ids, line)
	}
	if len(ids) > 0 {
		const tmpl = `{"id":{{json .Id}},"name":{{json .Name}},"running":{{json .State.Running}},"labels":{{json .Config.Labels}},"mounts":{{json .Mounts}},"network":{{json .HostConfig.NetworkMode}}}`
		args := append([]string{"container", "inspect", "--format", tmpl}, ids...)
		raw, err := docker(args...)
		if err != nil {
			return nil, nil, errors.New("容器检查失败；没有显示原始 Docker 输出")
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var item DockerContainer
			if json.Unmarshal([]byte(line), &item) != nil || !slices.Contains(ids, item.ID) || item.Name == "" {
				return nil, nil, errors.New("容器身份格式异常")
			}
			name := strings.TrimPrefix(item.Name, "/")
			if _, dup := containers[name]; dup {
				return nil, nil, errors.New("容器身份重复")
			}
			containers[name] = item
		}
		if len(containers) != len(ids) {
			return nil, nil, errors.New("容器清单在读取期间变化；请重新预检")
		}
	}

	networks := make(map[string]DockerNetwork)
	netOut, err := docker("network", "ls", "--no-trunc", "--format", "{{.ID}}")
	if err != nil {
		return nil, nil, errors.New("Docker 网络清单不可读")
	}
	var netIDs []string
	for _, line := range strings.Fields(string(netOut)) {
		if !hex64Pattern.MatchString(line) {
			return nil, nil, errors.New("Docker 网络清单不可读")
		}
		netIDs = append(netIDs, line)
	}
	if len(netIDs) > 0 {
		const tmpl = `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Labels}},"containers":{{json .Containers}},"driver":{{json .Driver}}}`
		args := append([]string{"network", "inspect", "--format", tmpl}, netIDs...)
		raw, err := docker(args...)
		if err != nil {
			return nil, nil, errors.New("网络检查失败；没有显示原始 Docker 输出")
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var item DockerNetwork
			if json.Unmarshal([]byte(line), &item) != nil || !slices.Contains(netIDs, item.ID) || item.Name == "" {
				return nil, nil, errors.New("网络身份格式异常")
			}
			networks[item.Name] = item
		}
		if len(networks) != len(netIDs) {
			return nil, nil, errors.New("网络清单在读取期间变化；请重新预检")
		}
	}
	return containers, networks, nil
}

func checkComposeTransaction(proxyRoot string) error {
	journal := filepath.Join(proxyRoot, "state/compose-transaction.json")
	if _, err := os.Lstat(journal); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Compose HTTPS 事务尚未完成；请先用部署包 HTTPS 工具核对，不删除入口、证书或恢复材料")
	}
	return nil
}

func readComposeInstances(proxyRoot string) (map[string]json.RawMessage, error) {
	path := filepath.Join(proxyRoot, "state/compose-instances.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 ||
		!hostutil.PrivateOwner(info, hostutil.ManagedOwnerUID) || info.Size() > 65536 {
		return nil, errors.New("Compose HTTPS 归属记录无效或不安全；停止，不删除共享入口或证书")
	}
	raw, err := os.ReadFile(path)
	var doc struct {
		Format    int                        `json:"format"`
		Owner     string                     `json:"owner"`
		Project   string                     `json:"project"`
		Instances map[string]json.RawMessage `json:"instances"`
	}
	if err != nil || json.Unmarshal(raw, &doc) != nil || doc.Format != 1 ||
		doc.Owner != "tgcloud.compose.https" || doc.Project != "tgcloud" || doc.Instances == nil {
		return nil, errors.New("Compose HTTPS 归属记录无效或不安全；停止，不删除共享入口或证书")
	}
	return doc.Instances, nil
}

func readProxyState(proxyRoot string) (proxyState, bool, error) {
	empty := proxyState{Version: 1, Project: "tgcloud", Domains: map[string]json.RawMessage{}}
	if err := checkedPath(proxyRoot); err != nil {
		return empty, false, err
	}
	source := filepath.Join(proxyRoot, "state/domains.json")
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		if entries, err := os.ReadDir(proxyRoot); err == nil {
			for _, e := range entries {
				if e.Name() != "transaction.lock" {
					return empty, false, errors.New("共享代理有文件但缺少状态；停止，不能猜测或覆盖未知路由")
				}
			}
		}
		return empty, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || !hostutil.PrivateOwner(info, hostutil.ManagedOwnerUID) || info.Size() > 65536 {
		return empty, false, errors.New("共享代理状态文件无效或不安全；停止")
	}
	raw, err := os.ReadFile(source)
	var state proxyState
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Project != "tgcloud" || state.Domains == nil {
		return empty, false, errors.New("共享代理状态格式无效；停止")
	}
	return state, true, nil
}

func verifyCoreIdentity(installDir, name string, item DockerContainer) (string, error) {
	if item.Labels[installModeKey] == "compose" {
		return "", errors.New("独立 Compose 实例请按 Compose 指南移除；此卸载器不接管")
	}
	service := inspect.BotName
	expectedMounts := map[string]string{
		"/data":      filepath.Join(installDir, "data"),
		"/downloads": filepath.Join(installDir, "downloads"),
		"/logs":      filepath.Join(installDir, "logs"),
		"/config":    filepath.Join(installDir, "config"),
	}
	if name == inspect.GatewayName {
		service = editionKey
		expectedMounts = map[string]string{
			"/Config":   filepath.Join(installDir, "clouddrive/config"),
			"/CloudNAS": filepath.Join(installDir, "clouddrive/mounts"),
		}
	}
	if item.Labels[composeLabel+"service"] != service ||
		item.Labels[composeLabel+"project.working_dir"] != installDir ||
		item.Labels[composeLabel+"project.config_files"] != filepath.Join(installDir, "docker-compose.yml") {
		return "", errors.New("容器归属标签与标准安装目录不符；停止，不接管自定义部署")
	}
	project := item.Labels[composeLabel+"project"]
	if !projectPattern.MatchString(project) {
		return "", errors.New("容器缺少有效 Compose 项目身份")
	}
	if !bindMountsEqual(item.Mounts, expectedMounts) {
		return "", errors.New("容器挂载不属于标准安装目录；停止，不接管自定义部署")
	}
	return project, nil
}

func verifyLANIdentity(lanDir string, item DockerContainer) error {
	if item.Labels[managedLabel] != "true" || item.Labels[roleLabel] != "lan-management" ||
		item.Labels["com.tgcloud.edition"] != editionKey ||
		item.Labels[composeLabel+"project"] != lanContainer ||
		item.Labels[composeLabel+"service"] != "lan" ||
		item.Labels[composeLabel+"project.working_dir"] != lanDir ||
		item.Labels[composeLabel+"project.config_files"] != filepath.Join(lanDir, "compose.json") ||
		item.Network != "host" ||
		!bindMountsEqual(item.Mounts, map[string]string{"/etc/nginx/nginx.conf": filepath.Join(lanDir, "nginx.conf")}) {
		return errors.New("内网入口身份或挂载不符")
	}
	return nil
}

func verifyLANDir(lanDir, installDir string) error {
	if !pathExists(lanDir) {
		return nil
	}
	entries, err := os.ReadDir(lanDir)
	if err != nil || len(entries) != 3 {
		return errors.New("内网入口目录有未知文件或不完整；停止，不递归清理")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"compose.json", "nginx.conf", "state.json"}) {
		return errors.New("内网入口目录有未知文件或不完整；停止，不递归清理")
	}
	raw, err := os.ReadFile(filepath.Join(lanDir, "state.json"))
	var rec struct {
		Schema     int    `json:"schema"`
		Mode       string `json:"mode"`
		Edition    string `json:"edition"`
		InstallDir string `json:"install_dir"`
	}
	if err != nil || json.Unmarshal(raw, &rec) != nil || rec.Schema != 1 || rec.Mode != "lan" ||
		rec.Edition != editionKey || rec.InstallDir != installDir {
		return errors.New("内网入口模式记录不符")
	}
	return nil
}

func verifyProxyIdentity(proxyRoot, name string, item DockerContainer) error {
	service, role := "nginx", "reverse-proxy"
	if name == entry.HTTPSCertbot {
		service, role = "certbot", "certificate-manager"
	} else if name != entry.HTTPSNginx {
		return errors.New("共享代理项目存在未知容器；停止")
	}
	if item.Labels[managedLabel] != "true" || item.Labels[roleLabel] != role ||
		item.Labels[composeLabel+"project"] != entry.HTTPSProject ||
		item.Labels[composeLabel+"service"] != service ||
		item.Labels[composeLabel+"project.working_dir"] != proxyRoot ||
		item.Labels[composeLabel+"project.config_files"] != filepath.Join(proxyRoot, "docker-compose.yml") {
		return errors.New("共享代理容器归属不符；停止，不接管")
	}
	return nil
}

func verifyRetainedCompose(installDir string) error {
	path := filepath.Join(installDir, "docker-compose.yml")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
		return errors.New("残留目录缺少可核对的标准 Compose；不删除")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.New("残留目录缺少可核对的标准 Compose；不删除")
	}
	text := string(raw)
	if !strings.Contains(text, "container_name: "+inspect.BotName) || !strings.Contains(text, "container_name: "+inspect.GatewayName) {
		return errors.New("残留目录不是所选实例的标准 payload；不删除")
	}
	return nil
}

func inspectBackupDir(dir string, pattern *regexp.Regexp) ([]string, bool, error) {
	if err := checkedPath(dir); err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.IsDir() || !hostutil.PrivateOwner(info, hostutil.ManagedOwnerUID) {
		return nil, false, errors.New("备份目录所有者/权限不符合受管私密目录；停止")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, errors.New("无法读取备份目录；停止")
	}
	var names []string
	for _, item := range entries {
		childPath := filepath.Join(dir, item.Name())
		childInfo, err := os.Lstat(childPath)
		if err != nil || !pattern.MatchString(item.Name()) || !childInfo.Mode().IsRegular() ||
			!hostutil.PrivateOwner(childInfo, hostutil.ManagedOwnerUID) {
			return nil, false, errors.New("备份目录含未知文件、链接或不安全归属；停止，不按目录名猜测删除")
		}
		names = append(names, fmt.Sprintf("%s:%d:%d", item.Name(), childInfo.Size(), childInfo.ModTime().UnixNano()))
	}
	slices.Sort(names)
	return names, true, nil
}

func checkedPath(target string) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("目标路径缺失绝对位置或经过链接；停止")
	}
	for current := target; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || (current != target && !info.IsDir()) {
				return errors.New("目录路径经过链接或不是目录；停止")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法核对目录路径；停止")
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func bindMountsEqual(mounts []DockerMount, expected map[string]string) bool {
	actual := make(map[string]string)
	for _, m := range mounts {
		if m.Type == "tmpfs" {
			continue
		}
		if m.Type != "bind" {
			return false
		}
		actual[m.Destination] = m.Source
	}
	if len(actual) != len(expected) {
		return false
	}
	for k, v := range expected {
		if actual[k] != v {
			return false
		}
	}
	return true
}

func hasExternalEndpoints(network DockerNetwork, selected map[string]DockerContainer) bool {
	selectedIDs := make(map[string]bool, len(selected))
	for _, item := range selected {
		selectedIDs[item.ID] = true
	}
	for id := range network.Containers {
		if !selectedIDs[id] {
			return true
		}
	}
	return false
}

func hasMountUnder(points []string, root string) bool {
	prefix := root + "/"
	for _, point := range points {
		if point == root || strings.HasPrefix(point, prefix) {
			return true
		}
	}
	return false
}

func otherContainerOverlaps(containers, selected map[string]DockerContainer, root string) bool {
	prefix := root + "/"
	for name, item := range containers {
		if _, ok := selected[name]; ok {
			continue
		}
		for _, m := range item.Mounts {
			if m.Type == "tmpfs" || m.Source == "" {
				continue
			}
			src := filepath.Clean(m.Source)
			if src == root || strings.HasPrefix(src, prefix) || strings.HasPrefix(root, src+"/") {
				return true
			}
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func computePlanDigest(summary PlanSummary, containers map[string]DockerContainer, networks map[string]DockerNetwork, backups map[string][]string) string {
	payload := struct {
		Summary    PlanSummary                `json:"summary"`
		Containers map[string]DockerContainer `json:"containers"`
		Networks   map[string]DockerNetwork   `json:"networks"`
		Backups    map[string][]string        `json:"backups"`
	}{Summary: summary, Containers: containers, Networks: networks, Backups: backups}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
