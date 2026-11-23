package inspect

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

const lanContainerFields = `{"id":{{json .Id}},"name":{{json .Name}},"reference":{{json .Config.Image}},` +
	`"image":{{json .Image}},"running":{{json .State.Running}},"labels":{{json .Config.Labels}},` +
	`"mounts":{{json .Mounts}},"network":{{json .HostConfig.NetworkMode}},"readonly":{{json .HostConfig.ReadonlyRootfs}},` +
	`"privileged":{{json .HostConfig.Privileged}},"user":{{json .Config.User}},"cap_add":{{json .HostConfig.CapAdd}},` +
	`"cap_drop":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"tmpfs":{{json .HostConfig.Tmpfs}},` +
	`"entrypoint":{{json .Config.Entrypoint}},"command":{{json .Config.Cmd}}}`

var (
	lanImage           = regexp.MustCompile(`^nginx:[0-9]+\.[0-9]+\.[0-9]+-alpine$`)
	errLanReachability = errors.New("内网管理页或 /dav 阻断未通过")
)

type lanObservation struct {
	Saved   *lanState `json:"saved"`
	Running bool      `json:"running"`
	Healthy bool      `json:"healthy"`
}

type lanContainer struct {
	ID, Name, Reference, Image, Network, User string
	Running, Readonly                         bool
	Privileged                                *bool
	Labels, Tmpfs                             map[string]string
	CapAdd                                    []string `json:"cap_add"`
	CapDrop                                   []string `json:"cap_drop"`
	Security, Entrypoint, Command             []string
	Mounts                                    []lanMount
}

type lanMount struct {
	Type, Source, Destination string
	RW                        *bool
}

func LANEntrySupported() bool { return true }

func LANEntry(args []string) (any, error) {
	if len(args) < 3 || len(args) > 5 {
		return nil, errors.New("内网管理入口参数无效")
	}
	entry := lanEntry{"/opt/tgcloud-lan", "/opt/tgcloud-proxy/state/domains.json", args[1], args[2]}
	if !lanImage.MatchString(entry.image) || entry.directory == entry.root ||
		strings.HasPrefix(entry.directory, entry.root+"/") {
		return nil, errors.New("内网入口镜像或安装目录无效")
	}
	if err := checkPlanningDirectory(entry.directory); err != nil {
		return nil, err
	}
	switch {
	case args[0] == "state" && len(args) == 3:
		if err := entry.rejectHTTPS(); err != nil {
			return nil, err
		}
		saved, err := entry.load()
		return lanObservation{Saved: saved}, err
	case args[0] == "save" && len(args) == 4:
		return struct{}{}, entry.save(args[3])
	case args[0] == "check" && len(args) == 5 && (args[4] == "fresh" || args[4] == "existing"):
		return entry.observe(context.Background(), args[3], args[4] == "existing", false)
	case args[0] == "activate" && len(args) == 4:
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		_, err := entry.observe(ctx, args[3], true, true)
		return struct{}{}, err
	default:
		return nil, errors.New("内网管理入口参数无效")
	}
}

func parseLanIP(value string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(value)
	if err != nil || !ip.Is4() {
		return netip.Addr{}, errors.New("请输入标准内网 IPv4 地址，不接受域名、URL 或端口")
	}
	if ip.String() != value || !ip.IsPrivate() {
		return netip.Addr{}, errors.New("只接受 RFC1918 内网 IPv4，不接受公网、回环或通配地址")
	}
	return ip, nil
}

func validateLanInterface(ctx context.Context, address string) error {
	ip, err := parseLanIP(address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ip", "-j", "-4", "address", "show")
	command.Env = append(os.Environ(), "LC_ALL=C")
	var output dockerOutput
	command.Stdout = &output
	if command.Run() != nil {
		return errors.New("无法读取本机网卡；需要 iproute2，不会猜测内网地址")
	}
	return checkLanInterface(ip, output.buffer.Bytes())
}

func checkLanInterface(ip netip.Addr, data []byte) error {
	var interfaces []struct {
		Name      string `json:"ifname"`
		Flags     []string
		Addresses []struct {
			Family, Local string
			PrefixLen     *int
		} `json:"addr_info"`
	}
	if json.Unmarshal(data, &interfaces) != nil {
		return errors.New("无法核对本机网卡地址")
	}
	for _, iface := range interfaces {
		if iface.Name == "lo" || strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "br-") ||
			strings.HasPrefix(iface.Name, "veth") || !slices.Contains(iface.Flags, "UP") {
			continue
		}
		for _, address := range iface.Addresses {
			if address.Family != "inet" || address.Local != ip.String() || address.PrefixLen == nil ||
				*address.PrefixLen < 1 || *address.PrefixLen > 32 {
				continue
			}
			octets := ip.As4()
			hostMask := uint32(1)<<(32-*address.PrefixLen) - 1
			host := binary.BigEndian.Uint32(octets[:]) & hostMask
			if *address.PrefixLen >= 31 || (host != 0 && host != hostMask) {
				return nil
			}
		}
	}
	return errors.New("该地址不属于本机已启用的内网网卡；虚拟机请填虚拟机地址，不填 NAS 宿主机地址")
}

func checkLanBind(address string) error {
	listener, err := net.Listen("tcp4", net.JoinHostPort(address, lanPort))
	if err != nil {
		return errors.New("指定内网地址/固定管理端口不可绑定；请核对占用，不会停止外部服务或更换端口")
	}
	return listener.Close()
}

func (entry lanEntry) inspect(docker DockerRunner) (*lanContainer, error) {
	output, err := docker("container", "ls", "--all", "--no-trunc", "--filter", "name=^/"+lanName+"$", "--format", "{{.ID}}")
	if err != nil {
		return nil, errors.New("无法核对内网入口容器，不接管同名容器")
	}
	id := strings.TrimSpace(string(output))
	if id == "" {
		return nil, nil
	}
	if !containerID.MatchString(id) {
		return nil, errors.New("无法核对内网入口容器身份")
	}
	output, err = docker("container", "inspect", "--format", lanContainerFields, id)
	var item lanContainer
	if err != nil || json.Unmarshal(output, &item) != nil || item.ID != id || item.Name != "/"+lanName || !imageID.MatchString(item.Image) {
		return nil, errors.New("无法核对内网入口容器身份")
	}
	directory := entry.configDir()
	for key, expected := range map[string]string{
		"com.tgcloud.managed": "true", "com.tgcloud.role": "lan-management", "com.tgcloud.edition": lanEdition,
		"com.docker.compose.project": lanName, "com.docker.compose.service": "lan",
		"com.docker.compose.project.working_dir": directory, "com.docker.compose.project.config_files": filepath.Join(directory, "compose.json"),
	} {
		if item.Labels[key] != expected {
			return nil, errors.New("同名内网入口归属不符，停止，不接管")
		}
	}
	mounts := slices.DeleteFunc(item.Mounts, func(mount lanMount) bool { return mount.Destination == "/tmp" })
	if item.Reference != entry.image || item.Network != "host" || !item.Readonly || item.Privileged == nil || *item.Privileged ||
		item.User != "101:101" || len(item.CapAdd) != 0 || !slices.Equal(item.CapDrop, []string{"ALL"}) ||
		(!slices.Equal(item.Security, []string{"no-new-privileges:true"}) && !slices.Equal(item.Security, []string{"no-new-privileges"})) ||
		len(item.Tmpfs) != 1 || item.Tmpfs["/tmp"] != lanTmpfs ||
		!slices.Equal(item.Entrypoint, []string{"nginx"}) || !slices.Equal(item.Command, []string{"-g", "daemon off;"}) ||
		len(mounts) != 1 || mounts[0].Type != "bind" || mounts[0].Source != filepath.Join(directory, "nginx.conf") ||
		mounts[0].Destination != "/etc/nginx/nginx.conf" || mounts[0].RW == nil || *mounts[0].RW {
		return nil, errors.New("同名内网入口运行配置不符，停止，不接管")
	}
	return &item, nil
}

type lanBinding struct{ HostIP, HostPort string }

func checkLanBindings(id string, docker DockerRunner) error {
	output, err := docker("inspect", "--format", "{{json .HostConfig.PortBindings}}", id)
	var bindings map[string][]lanBinding
	if err != nil || json.Unmarshal(output, &bindings) != nil {
		return errors.New("无法核对网关回环监听")
	}
	expected := []lanBinding{{"127.0.0.1", lanPort}}
	if !slices.Equal(bindings[lanPort+"/tcp"], expected) {
		return errors.New("网关不再仅发布预期回环端口；停止，不改变访问模式")
	}
	for port, values := range bindings {
		if port != lanPort+"/tcp" && len(values) > 0 {
			return errors.New("网关发布了额外端口；停止，不改变访问模式")
		}
	}
	return nil
}

func (entry lanEntry) checkBackend(ctx context.Context, base *Instance, docker DockerRunner) error {
	if err := checkLanBindings(base.Gateway.ID, docker); err != nil {
		return err
	}
	healthCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if (base.Bot.State != "running" || base.Bot.Health != "healthy") &&
		waitContainerHealthContext(healthCtx, base.Bot.ID, docker) != "healthy" {
		return errors.New("Bot 基础健康检查未通过；不自动重装")
	}
	marker, err := verifyDeploymentSource(entry.directory, "ready")
	if err != nil {
		return errors.New("无法核对 Bot 已安装程序的来源")
	}
	return verifyBotRuntime(entry.directory, base.Bot.ID, marker, io.Discard, docker)
}

func (entry lanEntry) observe(ctx context.Context, address string, existing, activating bool) (lanObservation, error) {
	var result lanObservation
	if err := entry.rejectHTTPS(); err != nil {
		return result, err
	}
	saved, err := entry.load()
	if err != nil {
		return result, err
	}
	result.Saved = saved
	if existing && saved == nil {
		return result, errors.New("缺少受管内网状态；不转换公网 HTTPS 或接管其他部署")
	}
	if saved != nil && saved.BindIP != address {
		return result, errors.New("不隐式切换内网地址")
	}
	if err := validateLanInterface(ctx, address); err != nil {
		return result, err
	}
	native, err := installerDocker("lan")
	if err != nil {
		return result, err
	}
	if native == nil {
		if existing {
			return result, errors.New("Docker 不可用，无法检查已有内网实例")
		}
		if err := checkLanBind(address); err != nil {
			return result, err
		}
		return result, checkLanBind("127.0.0.1")
	}
	docker := func(args ...string) ([]byte, error) {
		timeout := 20 * time.Second
		var environment []string
		if len(args) > 2 && args[0] == "compose" && args[2] == entry.configDir() {
			timeout = 180 * time.Second
			environment = []string{"DOCKER_DEFAULT_PLATFORM=" + native.Platform}
		}
		return runBotAcceptanceDocker(ctx, timeout, environment, args...)
	}
	return entry.inspectAndCheck(ctx, result, address, existing, activating, native.Architecture, docker, checkLanBind, checkLanHTTP)
}

func (entry lanEntry) inspectAndCheck(ctx context.Context, result lanObservation, address string, existing, activating bool,
	architecture string, docker DockerRunner, bind, probe func(string) error,
) (lanObservation, error) {
	item, err := entry.inspect(docker)
	if err != nil {
		return result, err
	}
	if item != nil && (result.Saved == nil || !existing) {
		return result, errors.New("内网入口缺少受管记录或完整 Bot/网关；不接管、不重装")
	}
	var base *Instance
	var gateway *gatewayContainer
	if existing {
		base, err = Discover(entry.directory, docker)
		if err != nil || base == nil {
			return result, errors.New("无法核对 Bot 和 CloudDrive2 网关归属")
		}
		gateway = &gatewayContainer{Container: base.Gateway}
	}
	if _, err := checkGatewayPort(gateway, docker); err != nil {
		return result, err
	}
	result.Running = item != nil && item.Running
	if !result.Running {
		if err := bind(address); err != nil {
			return result, err
		}
	}
	if base == nil {
		return result, bind("127.0.0.1")
	}
	if err := entry.checkBackend(ctx, base, docker); err != nil {
		return result, err
	}
	result.Healthy = result.Running && imageIsNative(item.Image, architecture, docker) && probe(address) == nil
	if !activating || result.Healthy {
		return result, nil
	}
	if base.Gateway.State != "running" {
		return result, errors.New("CloudDrive2 网关未运行；请先检查基础服务，状态和数据保留")
	}
	return result, entry.start(ctx, address, result.Running, architecture, docker, probe)
}

func (entry lanEntry) compose() []string {
	directory := entry.configDir()
	return []string{"compose", "--project-directory", directory, "--env-file", "/dev/null", "-p", lanName,
		"-f", filepath.Join(directory, "compose.json")}
}

func (entry lanEntry) start(ctx context.Context, address string, recreate bool, architecture string, docker DockerRunner,
	probe func(string) error,
) error {
	fail := func(stage string) error {
		return fmt.Errorf("内网入口%s失败；基础服务和内网设置保留，可重跑内网向导单独修复入口", stage)
	}
	compose := entry.compose()
	if _, err := docker(append(compose, "config", "--quiet")...); err != nil {
		return fail("配置检查")
	}
	if _, err := docker(append(compose, "pull", "lan")...); err != nil || !imageIsNative(entry.image, architecture, docker) {
		return fail("镜像核验")
	}
	if _, err := docker(append(compose, "run", "--rm", "--no-deps", "lan", "-t")...); err != nil {
		return fail(" Nginx 配置检查")
	}
	up := []string{"up", "-d", "--no-deps", "--pull", "never"}
	if recreate {
		up = append(up, "--force-recreate")
	}
	if _, err := docker(append(compose, append(up, "lan")...)...); err != nil {
		return fail("启动")
	}
	item, err := entry.inspect(docker)
	if err != nil || item == nil || !item.Running || !imageIsNative(item.Image, architecture, docker) {
		return fail("启动后的容器核验")
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		if ctx.Err() != nil {
			return fail("检查")
		}
		if probe(address) == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fail("管理页或 /dav 阻断检查")
		}
		select {
		case <-ctx.Done():
			return fail("检查")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func checkLanHTTP(address string) error {
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return probeLanHTTP(client, "http://"+net.JoinHostPort(address, lanPort))
}

func probeLanHTTP(client *http.Client, url string) error {
	for _, request := range [][2]string{{"GET", "/"}, {"GET", "/dav"}, {"PROPFIND", "/dav/"}, {"PROPFIND", "/DAV/"}} {
		req, err := http.NewRequest(request[0], url+request[1], nil)
		if err != nil {
			return errLanReachability
		}
		response, err := client.Do(req)
		if err != nil {
			return errLanReachability
		}
		response.Body.Close()
		status := response.StatusCode
		if response.Header.Get("X-TGCloud-LAN") != lanEdition ||
			(request[1] == "/" && (status < 200 || status >= 400)) ||
			(request[1] != "/" && status != http.StatusForbidden) {
			return errLanReachability
		}
	}
	return nil
}
