package inspect

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func (entry httpsEntry) certFile(domain, path string) string {
	root := filepath.Join(entry.root, "certbot/letsencrypt")
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, path))
	if err != nil || !managerSafePath(resolved, false) {
		return ""
	}
	// Certbot's live links are normally relative links into its archive.
	for _, directory := range []string{"archive", "live", "renewal"} {
		if (directory != "renewal" && strings.HasPrefix(resolved, filepath.Join(root, directory, domain)+"/")) ||
			(directory == "renewal" && resolved == filepath.Join(root, directory, domain+".conf")) {
			info, err := os.Lstat(resolved)
			if err == nil && info.Mode().IsRegular() && info.Sys().(*syscall.Stat_t).Uid == hostutil.ManagedOwnerUID &&
				info.Size() > 0 && info.Size() <= 256*1024 {
				return resolved
			}
		}
	}
	return ""
}

func (entry httpsEntry) certificate(domain string) (time.Time, bool, bool) {
	chain := entry.certFile(domain, "live/"+domain+"/fullchain.pem")
	key := entry.certFile(domain, "live/"+domain+"/privkey.pem")
	renewal := entry.certFile(domain, "renewal/"+domain+".conf") != ""
	present := chain != "" && key != ""
	if !present || !renewal {
		return time.Time{}, present, renewal
	}
	body, err := readSourceFile(chain, 256*1024)
	block, _ := pem.Decode(body)
	if err != nil || block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, present, renewal
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !slices.Contains(cert.DNSNames, domain) || !cert.NotAfter.After(time.Now().AddDate(0, 0, 7)) {
		return time.Time{}, present, renewal
	}
	return cert.NotAfter.UTC(), present, renewal
}

var renewTimestamp = regexp.MustCompile(`^[0-9]{1,12}$`)

func renewalStatuses(root, domain string, markers map[string]string) {
	state := filepath.Join(root, "certbot/status", domain)
	if raw, err := readHTTPSPrivate(state+".check", 4096); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			key, value, _ := strings.Cut(line, "=")
			switch {
			case (key == "ATTEMPT" || key == "NEXT") && renewTimestamp.MatchString(value):
				markers["PROXY_RENEW_"+key] = value
			case key == "RESULT" && (value == "OK" || value == "FAILED"):
				markers["PROXY_RENEW_RESULT"] = value
			}
		}
	}
	for _, key := range []string{"success", "renewed"} {
		if raw, err := readHTTPSPrivate(state+"."+key, 4096); err == nil {
			value := strings.TrimSpace(string(raw))
			if renewTimestamp.MatchString(value) {
				markers["PROXY_RENEW_"+strings.ToUpper(key)] = value
			}
		}
	}
}

func (entry httpsEntry) status(ctx context.Context, domain string, containers httpsProxy, docker DockerRunner) map[string]string {
	markers := map[string]string{"PROXY_STATUS": "OK"}
	set := func(key string, checked bool) {
		markers["PROXY_"+key] = "FAILED"
		if checked {
			markers["PROXY_"+key] = "OK"
		}
	}
	core, backend, ports, syntax := false, false, false, false
	if docker != nil {
		core, backend = entry.backend(ctx, docker)
		p80, p443, err := entry.portOwners(ctx, containers, docker)
		ports = err == nil && p80 == "OWN" && p443 == "OWN"
	}
	nginx := containers.nginx != nil && containers.nginx.Running
	certbot := containers.certbot != nil && containers.certbot.Running && !containers.certbot.missingStatus
	if nginx {
		_, err := docker("exec", containers.nginx.ID, "nginx", "-t")
		syntax = err == nil
	}
	if certbot {
		// Only this non-secret environment value is read from the owned
		// container. It tells us if the 12-hour renewal loop includes the route.
		raw, err := docker("exec", containers.certbot.ID, "/bin/sh", "-c", `printf '%s' "$TGCLOUD_CERT_NAMES"`)
		certbot = err == nil && slices.Contains(strings.Fields(string(raw)), domain)
	}
	okPage, okRedirect, blocked := false, false, false
	if nginx && ports {
		client := loopbackHTTPSClient()
		defer client.CloseIdleConnections()
		status, _ := httpsProbe(ctx, client, "https://"+domain+"/")
		okPage = status >= 100 && status < 400
		redirect, location := httpsProbe(ctx, client, "http://"+domain+"/")
		okRedirect = (redirect == 301 || redirect == 308) && strings.HasPrefix(location, "https://"+domain+"/")
		dav, _ := httpsProbe(ctx, client, "https://"+domain+"/dav")
		davSlash, _ := httpsProbe(ctx, client, "https://"+domain+"/dav/")
		blocked = dav == 403 && davSlash == 403
	}
	expires, _, renewal := entry.certificate(domain)
	set("CORE", core)
	set("PORTS", ports)
	set("NGINX", nginx)
	set("CERTBOT", certbot)
	set("RENEWAL", renewal)
	set("NGINX_TEST", syntax)
	set("HTTPS", okPage)
	set("REDIRECT", okRedirect)
	set("DAV_BLOCK", blocked)
	set("BACKEND_LOCAL", backend)
	set("CERT", !expires.IsZero())
	if !expires.IsZero() {
		markers["PROXY_CERT_END"] = expires.Format(time.RFC3339)
	}
	renewalStatuses(entry.root, domain, markers)
	return markers
}

func healthyHTTPS(markers map[string]string) bool {
	for _, key := range []string{"CORE", "PORTS", "NGINX", "CERTBOT", "RENEWAL", "NGINX_TEST", "HTTPS", "REDIRECT", "DAV_BLOCK", "BACKEND_LOCAL", "CERT"} {
		if markers["PROXY_"+key] != "OK" {
			return false
		}
	}
	return true
}

func (entry httpsEntry) compose(args ...string) []string {
	return append([]string{"compose", "--project-directory", entry.root, "--env-file", "/dev/null",
		"-p", httpsProject, "-f", filepath.Join(entry.root, "docker-compose.yml")}, args...)
}

func (entry httpsEntry) configDirectories() error {
	for _, name := range []string{"nginx", "nginx/conf.d", "certbot", "certbot/letsencrypt", "certbot/lib", "certbot/log", "certbot/status", "certbot/www"} {
		mode := os.FileMode(0700)
		if name == "certbot/www" {
			mode = 0755 // Nginx workers only see the public challenge webroot.
		}
		if err := requireHTTPSDir(filepath.Join(entry.root, name), mode); err != nil {
			return err
		}
	}
	return entry.checkConfigFiles()
}

// Validate a complete candidate before touching the active bind mounts.
// A private task-specific directory avoids reusing old .next files.
func (entry httpsEntry) publish(state httpsState, candidate string, docker DockerRunner, dirty *bool) (bool, error) {
	if err := entry.configDirectories(); err != nil {
		return false, err
	}
	stage, err := os.MkdirTemp(entry.root, ".https-stage-")
	if err != nil {
		return false, errHTTPSState
	}
	defer os.RemoveAll(stage)
	stagedSites := filepath.Join(stage, "conf.d")
	if err := os.Mkdir(stagedSites, 0700); err != nil {
		return false, errHTTPSState
	}
	files := entry.render(state, candidate)
	for name, body := range files {
		if name == "docker-compose.yml" {
			continue
		}
		if err := os.WriteFile(filepath.Join(stage, strings.TrimPrefix(name, "nginx/")), []byte(body), 0644); err != nil {
			return false, errHTTPSState
		}
	}
	if _, err := docker("run", "--rm", "--pull", "never", "--network", "host", "--read-only",
		"--tmpfs", "/var/cache/nginx", "--tmpfs", "/var/run", "--tmpfs", "/tmp",
		"-v", filepath.Join(stage, "nginx.conf")+":/etc/nginx/nginx.conf:ro",
		"-v", stagedSites+":/etc/nginx/conf.d:ro",
		"-v", filepath.Join(entry.root, "certbot/www")+":/var/www/certbot:ro",
		"-v", filepath.Join(entry.root, "certbot/letsencrypt")+":/etc/letsencrypt:ro",
		entry.nginxImage, "nginx", "-t"); err != nil {
		return false, errors.New("候选 Nginx 配置自检失败；尚未发布本次配置")
	}
	mainChanged := false
	for _, name := range []string{"nginx/conf.d/00-default.conf", "nginx/conf.d/10-acme-bootstrap.conf",
		"nginx/conf.d/20-clouddrive2.conf", "nginx/nginx.conf", "docker-compose.yml"} {
		target := filepath.Join(entry.root, name)
		body, keep := files[name]
		existing, err := readSourceFile(target, installConfigLimit)
		if err != nil {
			if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
				return mainChanged, errHTTPSState
			}
		} else if keep && string(existing) == body {
			continue
		}
		if !keep && err != nil {
			continue
		}
		*dirty = true
		if !keep {
			if err := os.Remove(target); err != nil {
				return mainChanged, errHTTPSState
			}
			continue
		}
		mode := os.FileMode(0644)
		if name == "docker-compose.yml" {
			mode = 0600
		}
		if err := writeHTTPSFile(target, []byte(body), mode); err != nil {
			return mainChanged, err
		}
		mainChanged = mainChanged || name == "nginx/nginx.conf"
	}
	return mainChanged, nil
}

func (entry httpsEntry) activate(state httpsState, candidate, architecture string, docker DockerRunner, dirty *bool) error {
	mainChanged, err := entry.publish(state, candidate, docker, dirty)
	if err != nil {
		return err
	}
	if _, err := docker(entry.compose("config", "--quiet")...); err != nil {
		return errors.New("共享代理 Compose 配置校验失败")
	}
	*dirty = true
	up := []string{"up", "-d", "--no-deps", "--pull", "never"}
	if mainChanged {
		up = append(up, "--force-recreate")
	}
	if _, err := docker(entry.compose(append(up, "nginx")...)...); err != nil {
		return errors.New("共享 Nginx 容器启动失败")
	}
	item, err := entry.inspectContainer(docker, "nginx")
	if err != nil || !item.Running || !imageIsNative(item.Image, architecture, docker) {
		return errors.New("无法核对启动后的共享 Nginx 容器")
	}
	if _, err := docker("exec", item.ID, "nginx", "-t"); err != nil {
		return errors.New("共享 Nginx 配置检查失败")
	}
	if err := reloadHTTPSNginx(docker, item.ID); err != nil {
		return errors.New("共享 Nginx 平滑重载失败")
	}
	return nil
}

func reloadHTTPSNginx(docker DockerRunner, containerID string) error {
	var err error
	for attempt := range 10 {
		if _, err = docker("exec", containerID, "nginx", "-s", "reload"); err == nil {
			return nil
		}
		if attempt < 9 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return err
}

func (entry httpsEntry) startRenewal(architecture string, docker DockerRunner) error {
	up := []string{"up", "-d", "--no-deps", "--pull", "never"}
	try := func(options []string) (*httpsContainer, error) {
		if _, err := docker(entry.compose(append(options, "certbot")...)...); err != nil {
			return nil, errors.New("证书续期容器启动失败")
		}
		item, err := entry.inspectContainer(docker, "certbot")
		if err != nil || !item.Running || !imageIsNative(item.Image, architecture, docker) {
			return nil, errors.New("无法核对启动后的证书续期容器")
		}
		return item, nil
	}
	item, err := try(up)
	if err != nil {
		return err
	}
	if item.missingStatus {
		item, err = try(append(up, "--force-recreate"))
		if err != nil || item.missingStatus {
			return errors.New("证书续期容器缺少状态挂载；未确认部署成功")
		}
	}
	return nil
}

func (entry httpsEntry) obtain(domain, email string, present bool, docker DockerRunner) error {
	args := []string{"run", "--rm", "--no-deps", "--entrypoint", "certbot", "certbot", "certonly",
		"--webroot", "-w", "/var/www/certbot", "--non-interactive", "--agree-tos", "--no-eff-email"}
	if email == "" {
		args = append(args, "--register-unsafely-without-email")
	} else {
		args = append(args, "--email", email)
	}
	if present {
		args = append(args, "--force-renewal")
	}
	args = append(args, "--cert-name", domain, "-d", domain)
	if _, err := docker(entry.compose(args...)...); err != nil {
		return errors.New("Let's Encrypt 证书签发失败；请核对 Certbot 日志，可单独运行 HTTPS 修复")
	}
	if expires, _, _ := entry.certificate(domain); expires.IsZero() {
		return errors.New("签发后证书或续期配置未通过核验")
	}
	return nil
}

func (entry httpsEntry) restore(previous httpsState, architecture string, docker DockerRunner) error {
	dirty := false
	if domain := previous.domain(); domain != "" {
		restore, candidate := previous, ""
		if expires, _, _ := entry.certificate(domain); expires.IsZero() {
			restore, candidate = newHTTPSState(), domain
		}
		if err := entry.activate(restore, candidate, architecture, docker, &dirty); err != nil {
			return err
		}
		return entry.startRenewal(architecture, docker)
	}
	if _, err := entry.publish(previous, "", docker, &dirty); err != nil {
		return err
	}
	_, err := docker(entry.compose("stop", "nginx", "certbot")...)
	return err
}

type httpsStatusCheck func(context.Context, string, httpsProxy, DockerRunner) map[string]string

func (entry httpsEntry) configure(ctx context.Context, previous httpsState, domain, email, arch string,
	docker, rollback DockerRunner, status httpsStatusCheck, progress io.Writer,
) (err error) {
	dirty, committed := false, false
	defer func() {
		if dirty && !committed {
			if restoreErr := entry.restore(previous, arch, rollback); restoreErr != nil {
				fmt.Fprintln(progress, "警告：共享 HTTPS 自动回退未完全通过；请单独核对入口状态")
			} else {
				fmt.Fprintln(progress, "共享 HTTPS 已回退；原域名记录和基础服务保留")
			}
		}
	}()
	target := configuredHTTPSState(domain)
	expires, present, _ := entry.certificate(domain)
	if expires.IsZero() {
		fmt.Fprintln(progress, "TGCLOUD_STAGE=ACME")
		// With one managed route the bootstrap contains only the ACME path;
		// it never proxies HTTP management traffic during certificate repair.
		if err := entry.activate(newHTTPSState(), domain, arch, docker, &dirty); err != nil {
			return err
		}
		fmt.Fprintln(progress, "TGCLOUD_STAGE=CERTIFICATE")
		if err := entry.obtain(domain, email, present, docker); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(progress, "已有证书和续期配置有效，将直接复用证书")
	}
	if err := entry.activate(target, "", arch, docker, &dirty); err != nil {
		return err
	}
	if err := entry.startRenewal(arch, docker); err != nil {
		return err
	}
	fmt.Fprintln(progress, "TGCLOUD_STAGE=HTTPS_VERIFY")
	containers, err := entry.ownedContainers(docker, true)
	if err != nil {
		return err
	}
	if markers := status(ctx, domain, containers, docker); !healthyHTTPS(markers) {
		return errors.New("HTTPS 管理页、端口归属或证书最终检查未通过；原域名记录保留")
	}
	if ctx.Err() != nil {
		return errors.New("HTTPS 操作已中断，正在恢复共享入口")
	}
	if err := entry.commit(target); err != nil {
		return err
	}
	committed = true
	fmt.Fprintln(progress, "TGCLOUD_HTTPS=OK")
	return nil
}
