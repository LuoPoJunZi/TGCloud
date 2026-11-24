package tui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/acceptance"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/backupops"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/entry"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/uninstall"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/webdav"
)

type HostBackend struct {
	Directory string
	Docker    inspect.DockerRunner
	TTYIn     *os.File
	TTYOut    *os.File
}

func isTerminalFile(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}

func RunMenu(directory string, _ io.Reader, errOut io.Writer) int {
	if directory == "" {
		directory = inspect.InstallDirectory
	}
	ttyIn, errIn := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	ttyOut, errOutOpen := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if errIn != nil || errOutOpen != nil || !isTerminalFile(ttyIn) || !isTerminalFile(ttyOut) {
		if ttyIn != nil {
			ttyIn.Close()
		}
		if ttyOut != nil {
			ttyOut.Close()
		}
		fmt.Fprintln(errOut, "需要交互终端；不从管道/历史记录读取或显示凭据")
		return 1
	}
	defer ttyIn.Close()
	defer ttyOut.Close()
	backend := &HostBackend{Directory: directory, Docker: inspect.RunDocker, TTYIn: ttyIn, TTYOut: ttyOut}
	app := NewApp(backend, ttyIn, ttyOut, "hostcli")
	app.PrivateOut = ttyOut
	return app.Run()
}

func RunDiagnosticsCommand(directory, saveDir string, out io.Writer) error {
	if directory == "" {
		directory = inspect.InstallDirectory
	}
	backend := &HostBackend{Directory: directory, Docker: inspect.RunDocker}
	instance, err := backend.Discover()
	if err != nil {
		return err
	}
	if instance == nil {
		return errors.New("未检测到已安装的 TGCloud 实例")
	}
	report, err := backend.CollectDiagnostics(instance)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, report)
	if saveDir != "" {
		saved, err := backend.SaveDiagnostics(report, saveDir)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "报告已保存："+saved)
	}
	return nil
}

func (b *HostBackend) InstallDir() string { return b.Directory }

func (b *HostBackend) Discover() (*inspect.Instance, error) {
	if _, err := inspect.DockerPreflight(false); err != nil {
		return nil, err
	}
	return inspect.Discover(b.Directory, b.Docker)
}

func (b *HostBackend) ShowStatus(instance *inspect.Instance, out io.Writer) error {
	fmt.Fprintf(out, "安装目录：%s\n已安装版本：%s\nBot：%s（健康：%s）\nCloudDrive2 网关：%s\n",
		b.Directory, instance.Version, instance.Bot.State, fallbackText(instance.Bot.Health, "未配置"), instance.Gateway.State)
	return nil
}

func (b *HostBackend) ShowLogs(instance *inspect.Instance, out io.Writer) error {
	clean, err := inspect.LogRedactor(b.Directory)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "TGCloud 运行日志（最近 30 分钟，每个服务最多 100 行）")
	for _, svc := range []struct{ label, id string }{
		{"Bot", instance.Bot.ID},
		{"CloudDrive2 网关", instance.Gateway.ID},
	} {
		fmt.Fprintln(out, "\n── "+svc.label+" 日志 ──")
		raw, err := b.Docker("logs", "--since", "30m", "--tail", "100", svc.id)
		if err != nil {
			fmt.Fprintln(out, svc.label+" 日志读取失败："+err.Error())
			continue
		}
		text := clean(string(raw))
		if text == "" {
			text = "最近 30 分钟暂无日志。"
		}
		fmt.Fprintln(out, text)
	}
	return nil
}

func (b *HostBackend) ControlService(instance *inspect.Instance, verb, target string, _ io.Writer) error {
	lock, err := inspect.LockInstance()
	if err != nil {
		return err
	}
	defer lock.Close()
	id := instance.Bot.ID
	if target == "gateway" {
		id = instance.Gateway.ID
	}
	_, err = b.Docker(verb, id)
	return err
}

func (b *HostBackend) ShowReadiness(instance *inspect.Instance, out io.Writer) error {
	if instance.Bot.State != "running" || instance.Gateway.State != "running" {
		fmt.Fprintln(out, "业务就绪：服务尚未全部运行")
		return nil
	}
	report, err := instance.Readiness(b.Docker)
	if err != nil {
		return err
	}
	for _, c := range report.Checks {
		fmt.Fprintf(out, "%s：%s\n", c.Label, c.Status)
	}
	if report.Ready {
		fmt.Fprintln(out, "业务就绪：基础条件通过（最近观察，不代表写入）")
	} else {
		fmt.Fprintln(out, "业务就绪：尚未全部确认")
	}
	return nil
}

func (b *HostBackend) VerifyWebDAV(instance *inspect.Instance, out io.Writer) error {
	_, err := acceptance.Run([]string{"webdav", b.Directory, instance.Version, "--yes"}, nil, out)
	return err
}

func (b *HostBackend) CollectDiagnostics(instance *inspect.Instance) (string, error) {
	dockerVer, _ := b.Docker("version", "--format", "{{.Server.Version}}")
	composeVer, _ := b.Docker("compose", "version", "--short")
	var snapJSON string
	if instance.Bot.State == "running" {
		if raw, err := b.Docker("exec", instance.Bot.ID, "python", "-B", "-m", "tgcloud.monitoring.diagnostics"); err == nil && len(raw) <= 16384 {
			snapJSON = string(raw)
		}
	}
	logRot := map[string]string{
		"Bot":      acceptance.InspectContainerLogRotation(instance.Bot.ID, b.Directory, inspect.BotName, instance.Bot.Project, "", b.Docker),
		"网关":       acceptance.InspectContainerLogRotation(instance.Gateway.ID, b.Directory, "clouddrive2", instance.Gateway.Project, "", b.Docker),
		"共享 Nginx": acceptance.InspectContainerLogRotation(entry.HTTPSNginx, entry.HTTPSRoot, "nginx", entry.HTTPSProject, "reverse-proxy", b.Docker),
		"Certbot":  acceptance.InspectContainerLogRotation(entry.HTTPSCertbot, entry.HTTPSRoot, "certbot", entry.HTTPSProject, "certificate-manager", b.Docker),
	}
	entrySummary := "未核实"
	if info, err := b.EntryInfo(instance); err == nil {
		if info.IsLAN {
			entrySummary = "内网 HTTP 管理入口已登记"
		} else if info.Domain != "" {
			entrySummary = "共享 HTTPS 代理路由已登记"
		}
	}
	return RenderDiagnostics(instance, string(dockerVer), string(composeVer), snapJSON, logRot, entrySummary), nil
}

func (b *HostBackend) SaveDiagnostics(report, dir string) (string, error) {
	if !filepath.IsAbs(dir) || dir == "/" || len(report) > 32768 {
		return "", errors.New("报告目录或大小无效")
	}
	if !hostutil.SafePath(dir, false) {
		return "", errors.New("报告目录经过链接或权限不安全")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", errors.New("无法创建私密报告目录")
	}
	if !hostutil.SafePath(dir, true) {
		return "", errors.New("报告目录必须私有（0700）")
	}
	var rb [8]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return "", err
	}
	target := filepath.Join(dir, "diagnostic-"+hex.EncodeToString(rb[:])+".md")
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("无法写入诊断报告文件")
	}
	if err := hostutil.FinishFile(f, []byte(report), 0600); err != nil {
		return "", errors.New("保存诊断报告失败")
	}
	return target, nil
}

func (b *HostBackend) ReadEnv(_ *inspect.Instance) (map[string]string, error) {
	envPath := filepath.Join(b.Directory, ".env")
	file, err := hostutil.OpenPrivate(envPath, syscall.O_RDONLY, hostutil.ManagedOwnerUID)
	if err != nil {
		return nil, errors.New("无法安全读取实例 .env 私有配置")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 128*1024+1))
	if err != nil || len(raw) > 128*1024 {
		return nil, errors.New("实例 .env 配置读取失败或过大")
	}
	return hostutil.ParseEnvLines(raw, errors.New("实例 .env 配置格式无效"))
}

func (b *HostBackend) EntryInfo(_ *inspect.Instance) (EntryInfo, error) {
	if raw, err := os.ReadFile("/opt/tgcloud-lan/clouddrive2/state.json"); err == nil {
		var rec struct {
			BindIP string `json:"bind_ip"`
		}
		if json.Unmarshal(raw, &rec) == nil && rec.BindIP != "" {
			return EntryInfo{IsLAN: true, BindIP: rec.BindIP}, nil
		}
	}
	raw, err := os.ReadFile(filepath.Join(entry.HTTPSRoot, "state/domains.json"))
	if err != nil {
		return EntryInfo{}, nil
	}
	var doc struct {
		Domains map[string]struct {
			Domain string `json:"domain"`
			Email  string `json:"email"`
		} `json:"domains"`
	}
	if json.Unmarshal(raw, &doc) == nil {
		route := doc.Domains["clouddrive2"]
		return EntryInfo{Domain: route.Domain, Email: route.Email}, nil
	}
	return EntryInfo{}, nil
}

func (b *HostBackend) ShowEntryStatus(instance *inspect.Instance, out io.Writer) error {
	info, err := b.EntryInfo(instance)
	if err != nil {
		return err
	}
	if info.IsLAN {
		fmt.Fprintf(out, "访问模式：内网 HTTP\n管理地址：http://%s:19798\n", info.BindIP)
		return nil
	}
	if info.Domain == "" {
		fmt.Fprintln(out, "访问模式：HTTPS（未配置域名路由）")
		return nil
	}
	fmt.Fprintln(out, "访问模式：HTTPS\n管理地址：https://"+info.Domain)
	res, err := entry.HTTPSEntry([]string{"status", b.Directory, entry.DefaultNginxImage, entry.DefaultCertbotImage, info.Domain}, out)
	if err != nil {
		return err
	}
	if markers, ok := res.(map[string]string); ok {
		failed := []string{}
		for _, k := range []string{"CORE", "PORTS", "NGINX", "CERTBOT", "RENEWAL", "NGINX_TEST", "HTTPS", "REDIRECT", "DAV_BLOCK", "BACKEND_LOCAL", "CERT"} {
			if markers["PROXY_"+k] != "OK" {
				failed = append(failed, k)
			}
		}
		if len(failed) == 0 {
			fmt.Fprintln(out, "入口检查：本机通过")
		} else {
			fmt.Fprintln(out, "入口检查：未通过（项目："+strings.Join(failed, ", ")+"）")
		}
		if exp := markers["PROXY_CERT_END"]; exp != "" {
			fmt.Fprintln(out, "证书到期："+exp)
		}
		if resText := markers["PROXY_RENEW_RESULT"]; resText != "" {
			fmt.Fprintln(out, "续期检查："+resText)
		}
	}
	return nil
}

func (b *HostBackend) ConfigureHTTPS(_ *inspect.Instance, domain, email, confirmedNAT, expectedDomain string, out io.Writer) error {
	lock, err := inspect.LockInstance()
	if err != nil {
		return err
	}
	defer lock.Close()
	_, err = entry.HTTPSEntry([]string{
		"configure", b.Directory, entry.DefaultNginxImage, entry.DefaultCertbotImage,
		domain, email, confirmedNAT, expectedDomain,
	}, out)
	return err
}

func (b *HostBackend) RenewHTTPSDryRun(_ *inspect.Instance, domain string, out io.Writer) error {
	lock, err := inspect.LockInstance()
	if err != nil {
		return err
	}
	defer lock.Close()
	err = entry.RenewDryRun(domain, out)
	if !errors.Is(err, entry.ErrExternalProxyMode) {
		return err
	}
	const cliCmd = "/usr/local/bin/tgcloud"
	if !hostutil.SafePath(cliCmd, false) {
		return err
	}
	cmd := exec.Command(cliCmd, "proxy", "maintain", "--dry-run", "--yes", "--authorize-proxy")
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func (b *HostBackend) ShowExternalWebDAV(_ *inspect.Instance, out io.Writer) error {
	status, err := webdav.Status("/opt/tgcloud-webdav", "/opt/tgcloud-proxy/external-auth", "clouddrive2")
	if err != nil {
		return err
	}
	if !status.Present {
		fmt.Fprintln(out, "外部 WebDAV 尚未登记。")
		return nil
	}
	fmt.Fprintf(out, "外部域名：%s\n主用户名：%s\n共享目录：%s\n状态：%s\n认证账号数：%d\n",
		status.Domain, status.Username, status.TargetDir, status.Phase, status.AuthUserCount)
	return nil
}

func (b *HostBackend) ManageExternalWebDAV(_ *inspect.Instance, out io.Writer) error {
	const webdavCmd = "/usr/local/bin/tgcloud-webdav"
	const mainCmd = "/usr/local/bin/tgcloud"
	var cmd *exec.Cmd
	if _, err := os.Lstat(webdavCmd); err == nil {
		if !hostutil.SafePath(webdavCmd, true) {
			return errors.New("现有 tgcloud-webdav 入口路径不安全，拒绝执行")
		}
		cmd = exec.Command(webdavCmd, "--edition", "clouddrive2", "--install-dir", b.Directory)
	} else {
		if !hostutil.SafePath(mainCmd, false) {
			return errors.New("未找到受管外部 WebDAV 入口；请在完整源码目录运行 tgcloud webdav")
		}
		cmd = exec.Command(mainCmd, "webdav", "--edition", "clouddrive2", "--install-dir", b.Directory)
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TGCLOUD_SOURCE_ROOT=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "TGCLOUD_MANAGER_MENU=1")
	if b.TTYIn != nil && b.TTYOut != nil {
		cmd.Stdin = b.TTYIn
		cmd.Stdout = b.TTYOut
		cmd.Stderr = b.TTYOut
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = out
		cmd.Stderr = out
	}
	return cmd.Run()
}

func (b *HostBackend) dockerExecJSON(containerID, module string, reqPayload map[string]any) ([]byte, error) {
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "--host=unix:///var/run/docker.sock", "exec", "-i", containerID, "python", "-B", "-m", module)
	cmd.Stdin = bytes.NewReader(body)
	var out hostutil.BoundedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, errors.New("容器内业务协议调用失败，请确认 Bot 已升级并正常运行")
	}
	return out.Bytes(), nil
}

func (b *HostBackend) BotUsersRPC(instance *inspect.Instance, adminID int64, payload map[string]any) (UsersRecord, error) {
	req := map[string]any{"admin_id": adminID}
	for k, v := range payload {
		req[k] = v
	}
	raw, err := b.dockerExecJSON(instance.Bot.ID, "tgcloud.persistence.users", req)
	if err != nil {
		return UsersRecord{}, err
	}
	var resp struct {
		API    int         `json:"api"`
		Record UsersRecord `json:"record"`
	}
	if json.Unmarshal(raw, &resp) != nil || resp.API != 1 || resp.Record.Schema != 1 || resp.Record.AdminID != adminID {
		return UsersRecord{}, errors.New("Bot 返回的家庭授权协议无法确认")
	}
	return resp.Record, nil
}

func (b *HostBackend) BotControlRPC(instance *inspect.Instance, adminID int64, payload map[string]any) (map[string]any, error) {
	req := map[string]any{"admin_id": adminID}
	for k, v := range payload {
		req[k] = v
	}
	raw, err := b.dockerExecJSON(instance.Bot.ID, "tgcloud.persistence.control", req)
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	if json.Unmarshal(raw, &resp) != nil || resp["api"] != float64(1) {
		return nil, errors.New("Bot 返回的可选功能控制协议无法确认")
	}
	return resp, nil
}

func (b *HostBackend) RunListenAccount(instance *inspect.Instance, action string, out io.Writer) error {
	args := []string{"--host=unix:///var/run/docker.sock", "exec"}
	if action == "login" {
		args = append(args, "-it")
	}
	args = append(args, instance.Bot.ID, "python", "-B", "-m", "tgcloud.listening.account", action)
	cmd := exec.Command("docker", args...)
	if b.TTYIn != nil && b.TTYOut != nil {
		cmd.Stdin = b.TTYIn
		cmd.Stdout = b.TTYOut
		cmd.Stderr = b.TTYOut
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = out
		cmd.Stderr = out
	}
	return cmd.Run()
}

func (b *HostBackend) ListBackups(out io.Writer) error {
	items, err := backupops.ListInstanceBackups(inspect.UpgradeBackupDir)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(out, "没有受管实例历史备份。")
		return nil
	}
	for _, item := range items {
		fmt.Fprintf(out, "%s（%d 字节，%s）\n", item.Name, item.SizeBytes, item.ModTime)
	}
	return nil
}

func (b *HostBackend) CreateInstanceSnapshot(instance *inspect.Instance, destDir string) (string, error) {
	return backupops.CreateInstanceSnapshot(b.Directory, destDir, instance.Version, b.Docker)
}

func (b *HostBackend) PreflightInstanceSnapshot(instance *inspect.Instance, archivePath, scratchDir string) (int, error) {
	return backupops.PreflightInstanceSnapshot(archivePath, scratchDir, instance.Version)
}

func (b *HostBackend) ProxyBackupAction(action string, keep int, planID string, out io.Writer) (string, error) {
	const proxyRoot = "/opt/tgcloud-proxy"
	const backupRoot = "/opt/tgcloud-proxy-backups"
	switch action {
	case "list":
		items, err := backupops.ListProxyBackups(backupRoot)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "暂无共享代理备份。")
			return "", nil
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s（%d 字节，完整性 %s）\n", item.Name, item.SizeBytes, item.Integrity)
		}
		return "", nil
	case "create":
		archive, count, err := backupops.CreateProxyBackup(proxyRoot, backupRoot)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(out, "已创建并校验共享代理备份：%s（%d 项）\n", archive, count)
		return "", nil
	case "verify":
		items, err := backupops.ListProxyBackups(backupRoot)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "", errors.New("没有可校验的共享代理备份")
		}
		latest := filepath.Join(backupRoot, items[0].Name)
		count, err := backupops.RecoveryDrill(latest, backupRoot)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(out, "共享代理备份隔离恢复演练通过：%s（%d 项）\n", latest, count)
		return "", nil
	case "prune-preview":
		plan, err := backupops.PlanProxyPrune(backupRoot, keep)
		if err != nil {
			return "", err
		}
		if len(plan.Remove) == 0 {
			fmt.Fprintln(out, "没有需要清理的代理备份。")
			return "", nil
		}
		for _, item := range plan.Remove {
			fmt.Fprintln(out, "将删除："+item.Name)
		}
		return plan.PlanID, nil
	case "prune":
		if _, err := backupops.ExecuteProxyPrune(backupRoot, keep, planID); err != nil {
			return "", err
		}
		fmt.Fprintln(out, "已按复核后的计划清理旧代理备份。")
		return "", nil
	default:
		return "", errors.New("未知代理备份操作")
	}
}

func (b *HostBackend) UninstallPreview(purgeData, purgeBackups bool, out io.Writer) (string, error) {
	plan, err := uninstall.BuildPlan(uninstall.DefaultPaths(), uninstall.Options{
		PurgeData: purgeData, PurgeBackups: purgeBackups,
	}, b.Docker, hostutil.Mountpoints)
	if err != nil {
		return "", err
	}
	uninstall.WritePreview(plan, out)
	return plan.Summary.PlanDigest, nil
}

func (b *HostBackend) UninstallExecute(purgeData, purgeBackups bool, planDigest string, out io.Writer) error {
	plan, err := uninstall.BuildPlan(uninstall.DefaultPaths(), uninstall.Options{
		PurgeData: purgeData, PurgeBackups: purgeBackups, PlanDigest: planDigest,
	}, b.Docker, hostutil.Mountpoints)
	if err != nil {
		return err
	}
	if planDigest != "" && plan.Summary.PlanDigest != planDigest {
		return errors.New("确认期间卸载计划发生变化；请重新预检")
	}
	return uninstall.Execute(plan, b.Docker, hostutil.Mountpoints, out)
}
