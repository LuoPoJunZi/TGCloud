package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const httpsContainerFields = `{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Image}},` +
	`"running":{{json .State.Running}},"labels":{{json .Config.Labels}},"mounts":{{json .Mounts}},` +
	`"network":{{json .HostConfig.NetworkMode}}}`

var httpsListenerPID = regexp.MustCompile(`\bpid=([0-9]+)\b`)

type httpsContainer struct {
	ID, Name, Image, Network string
	Running                  bool
	Labels                   map[string]string
	Mounts                   []lanMount
	missingStatus            bool
}

type httpsProxy struct {
	nginx, certbot *httpsContainer
}

type httpsEnvironment struct {
	backendLocal bool
	Core         string   `json:"core"`
	Docker       string   `json:"docker"`
	Port80       string   `json:"port_80"`
	Port443      string   `json:"port_443"`
	DNSA         []string `json:"dns_a"`
	DNSAAAA      []string `json:"dns_aaaa"`
	Local        []string `json:"local_addresses"`
}

func netLiteral(value string) bool {
	_, err := netip.ParseAddr(value)
	return err == nil
}

func (entry httpsEntry) ownedContainers(docker DockerRunner, stateExists bool) (httpsProxy, error) {
	var proxy httpsProxy
	if docker == nil {
		if !stateExists {
			exists, err := entry.runtimeFilesExist()
			if exists || err != nil {
				return proxy, errHTTPSState
			}
		}
		return proxy, nil
	}
	raw, err := docker("container", "ls", "--all", "--format", "{{.Names}}")
	if err != nil {
		return proxy, errors.New("无法核对共享 HTTPS 容器列表")
	}
	names := strings.Fields(string(raw))
	if !stateExists {
		files, err := entry.runtimeFilesExist()
		if err != nil || files || slices.Contains(names, httpsNginx) || slices.Contains(names, httpsCertbot) {
			return proxy, errors.New("共享代理已有运行文件或容器但缺少 domains.json；请先核对现有入口")
		}
	}
	project, err := docker("container", "ls", "--all", "--filter", "label=com.docker.compose.project="+httpsProject, "--format", "{{.Names}}|{{.State}}")
	if err != nil {
		return proxy, errors.New("无法核对共享代理 Compose 项目")
	}
	for _, line := range strings.Fields(string(project)) {
		name, status, _ := strings.Cut(line, "|")
		if name == httpsNginx || name == httpsCertbot {
			continue
		}
		if strings.HasPrefix(name, httpsCertbot+"-run-") && (status == "exited" || status == "dead") {
			continue // A stopped ACME one-off is never modified by this workflow.
		}
		return proxy, errHTTPSForeign
	}
	for _, service := range []string{"nginx", "certbot"} {
		name := httpsProject + "-" + service
		if !slices.Contains(names, name) {
			continue
		}
		container, err := entry.inspectContainer(docker, service)
		if err != nil {
			return proxy, err
		}
		if service == "nginx" {
			proxy.nginx = container
		} else {
			proxy.certbot = container
		}
	}
	return proxy, nil
}

func (entry httpsEntry) inspectContainer(docker DockerRunner, service string) (*httpsContainer, error) {
	name := httpsProject + "-" + service
	raw, err := docker("container", "inspect", "--format", httpsContainerFields, name)
	var item httpsContainer
	if err != nil || json.Unmarshal(raw, &item) != nil || item.Name != "/"+name ||
		!containerID.MatchString(item.ID) || !imageID.MatchString(item.Image) {
		return nil, errHTTPSForeign
	}
	role, network := "reverse-proxy", "host"
	mounts := map[string]string{"/var/www/certbot": "certbot/www", "/etc/letsencrypt": "certbot/letsencrypt"}
	if service == "nginx" {
		mounts["/etc/nginx/nginx.conf"] = "nginx/nginx.conf"
		mounts["/etc/nginx/conf.d"] = "nginx/conf.d"
	} else {
		role, network = "certificate-manager", httpsProject+"_default"
		mounts["/var/lib/letsencrypt"] = "certbot/lib"
		mounts["/var/log/letsencrypt"] = "certbot/log"
		mounts["/status"] = "certbot/status"
	}
	labels := map[string]string{
		"com.tgcloud.managed": "true", "com.tgcloud.role": role,
		"com.docker.compose.project": httpsProject, "com.docker.compose.service": service,
		"com.docker.compose.project.working_dir":  entry.root,
		"com.docker.compose.project.config_files": filepath.Join(entry.root, "docker-compose.yml"),
	}
	if item.Network != network {
		return nil, errHTTPSForeign
	}
	for label, value := range labels {
		if item.Labels[label] != value {
			return nil, errHTTPSForeign
		}
	}
	for _, mount := range item.Mounts {
		if mount.Type == "tmpfs" {
			continue
		}
		path, exists := mounts[mount.Destination]
		if !exists || mount.Type != "bind" || mount.Source != filepath.Join(entry.root, path) ||
			mount.RW == nil || *mount.RW != (service == "certbot") {
			return nil, errHTTPSForeign
		}
		delete(mounts, mount.Destination)
	}
	item.missingStatus = service == "certbot" && len(mounts) == 1 && mounts["/status"] != ""
	if len(mounts) != 0 && !item.missingStatus {
		return nil, errHTTPSForeign
	}
	return &item, nil
}

func httpsHostCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var output dockerOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return output.buffer.Bytes(), nil
}

func publishedHTTPSPorts(docker DockerRunner) (map[int]bool, error) {
	published := map[int]bool{}
	if docker == nil {
		return published, nil
	}
	raw, err := docker("ps", "--format", "{{.Ports}}")
	if err != nil {
		return nil, errors.New("无法核对 Docker 发布的 80/443 端口")
	}
	for _, mapping := range strings.FieldsFunc(string(raw), func(separator rune) bool {
		return separator == ',' || unicode.IsSpace(separator)
	}) {
		minPort, maxPort, ok := parseHostPortRange(mapping, true)
		if !ok {
			continue
		}
		for _, port := range []int{80, 443} {
			if minPort <= port && port <= maxPort {
				published[port] = true
			}
		}
	}
	return published, nil
}

func (entry httpsEntry) portOwners(ctx context.Context, proxy httpsProxy, docker DockerRunner) (string, string, error) {
	published, err := publishedHTTPSPorts(docker)
	if err != nil {
		return "", "", err
	}
	pids := map[string]bool{}
	if proxy.nginx != nil && proxy.nginx.Running {
		if output, err := docker("top", proxy.nginx.ID, "-eo", "pid"); err == nil {
			for _, pid := range strings.Fields(string(output)) {
				if value, err := strconv.Atoi(pid); err == nil && value > 0 {
					pids[pid] = true
				}
			}
		}
	}
	owner := func(port int) (string, error) {
		if published[port] {
			return "FOREIGN", nil
		}
		output, err := httpsHostCommand(ctx, "ss", "-H", "-ltnp", "( sport = :"+strconv.Itoa(port)+" )")
		if err != nil {
			return "", errors.New("无法读取 80/443 监听归属；请确认 iproute2（ss）可用")
		}
		return classifyHTTPSListener(string(output), proxy.nginx != nil && proxy.nginx.Running, pids), nil
	}
	p80, err := owner(80)
	if err != nil {
		return "", "", err
	}
	p443, err := owner(443)
	return p80, p443, err
}

func classifyHTTPSListener(output string, running bool, pids map[string]bool) string {
	text := strings.TrimSpace(output)
	if text == "" {
		return "FREE"
	}
	if !running {
		return "FOREIGN"
	}
	if len(pids) == 0 {
		return "UNKNOWN"
	}
	for _, line := range strings.Split(text, "\n") {
		matches := httpsListenerPID.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			return "UNKNOWN"
		}
		for _, match := range matches {
			if !pids[match[1]] {
				return "FOREIGN"
			}
		}
	}
	return "OWN"
}

func resolveHTTPS(ctx context.Context, domain string) ([]string, []string, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resolver := net.Resolver{StrictErrors: true}
	var resolved [][]string
	for _, family := range []string{"ip4", "ip6"} {
		ips, err := resolver.LookupIP(ctx, family, domain+".")
		if err != nil {
			var dnsErr *net.DNSError
			if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
				return nil, nil, nil, errors.New("VPS 无法完成域名 A/AAAA 查询；请核对本机 DNS")
			}
		}
		addresses := make([]string, 0, len(ips))
		for _, ip := range ips {
			address, ok := netip.AddrFromSlice(ip)
			if ok && (family != "ip6" || address.Is6() && !address.Is4In6()) {
				addresses = append(addresses, address.Unmap().String())
			}
		}
		slices.Sort(addresses)
		resolved = append(resolved, slices.Compact(addresses))
	}
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		return nil, nil, nil, errors.New("无法核对本机网卡的 IPv4/IPv6 地址")
	}
	local := []string{}
	for _, iface := range interfaces {
		address, err := netip.ParsePrefix(iface.String())
		if err == nil && address.Addr().IsGlobalUnicast() {
			local = append(local, address.Addr().Unmap().String())
		}
	}
	slices.Sort(local)
	return resolved[0], resolved[1], slices.Compact(local), nil
}

var httpsReservedIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
}

func httpsPublicIPv4(ip netip.Addr) bool {
	if !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, prefix := range httpsReservedIPv4 {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func (environment httpsEnvironment) safe(confirmed string) error {
	for _, port := range []struct{ number, owner string }{{"80", environment.Port80}, {"443", environment.Port443}} {
		switch port.owner {
		case "FREE", "OWN":
		case "FOREIGN":
			return errors.New("公网端口 " + port.number + " 由其他服务占用；保留现有服务")
		default:
			return errors.New("无法确认公网端口 " + port.number + " 的归属")
		}
	}
	if len(environment.DNSA)+len(environment.DNSAAAA) == 0 {
		return errors.New("VPS 当前没有解析到该域名的 A/AAAA 记录")
	}
	if confirmed != "" {
		ip, err := netip.ParseAddr(confirmed)
		private := false
		for _, address := range environment.Local {
			local, _ := netip.ParseAddr(address)
			if httpsPublicIPv4(local) {
				return errors.New("本机公网地址已变化，请重新确认 DNS")
			}
			private = private || (local.Is4() && local.IsPrivate())
		}
		if err != nil || !httpsPublicIPv4(ip) || !private || !slices.Equal(environment.DNSA, []string{confirmed}) {
			return errors.New("已确认的公网 NAT 地址与当前 DNS 或网卡不一致")
		}
	}
	for _, target := range slices.Concat(environment.DNSA, environment.DNSAAAA) {
		ip, err := netip.ParseAddr(target)
		if err != nil || !ip.IsGlobalUnicast() || (!slices.Contains(environment.Local, target) && target != confirmed) {
			return errors.New("域名 A/AAAA 未全部匹配本机或已确认的 NAT 地址；请重新核对 DNS")
		}
	}
	return nil
}

func (entry httpsEntry) probe(ctx context.Context, domain string, proxy httpsProxy, docker DockerRunner) (httpsEnvironment, error) {
	result := httpsEnvironment{Core: "MISSING", Docker: "MISSING"}
	var err error
	result.Port80, result.Port443, err = entry.portOwners(ctx, proxy, docker)
	if err != nil {
		return result, err
	}
	result.DNSA, result.DNSAAAA, result.Local, err = resolveHTTPS(ctx, domain)
	if err != nil {
		return result, err
	}
	if docker != nil {
		if _, err := docker("compose", "version"); err == nil {
			result.Docker = "OK"
		}
		core, local := entry.backend(ctx, docker)
		if core {
			result.Core = "OK"
		}
		result.backendLocal = local
	}
	return result, nil
}

func (entry httpsEntry) backend(ctx context.Context, docker DockerRunner) (core, local bool) {
	base, err := Discover(entry.directory, docker)
	if err != nil || base == nil || base.Gateway.State != "running" || checkLanBindings(base.Gateway.ID, docker) != nil {
		return false, false
	}
	client := loopbackHTTPSClient()
	defer client.CloseIdleConnections()
	backend := "http://127.0.0.1:" + strconv.Itoa(httpsBackendPort)
	status, _ := httpsProbe(ctx, client, backend)
	txt, err := httpsHostCommand(ctx, "ss", "-H", "-ltn", "( sport = :"+strconv.Itoa(httpsBackendPort)+" )")
	lines := strings.Split(strings.TrimSpace(string(txt)), "\n")
	local = err == nil
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 4 || (!strings.HasPrefix(f[3], "127.0.0.1:") && !strings.HasPrefix(f[3], "[::1]:")) {
			local = false
			break
		}
	}
	return status >= 200 && status < 400, local
}

func loopbackHTTPSClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
			},
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func httpsProbe(ctx context.Context, client *http.Client, url string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, ""
	}
	result, err := client.Do(req)
	if err != nil {
		return 0, ""
	}
	result.Body.Close()
	return result.StatusCode, result.Header.Get("Location")
}
