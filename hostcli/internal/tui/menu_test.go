package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

type fakeBackend struct {
	instance        *inspect.Instance
	env             map[string]string
	entryInfo       EntryInfo
	controlled      []string
	configuredHTTPS []string
	renewDryRuns    []string
	usersRPCs       []map[string]any
	controlRPCs     []map[string]any
	snapshots       []string
	preflights      []string
	webdavManaged   int
	uninstallCalls  int
}

func (f *fakeBackend) Discover() (*inspect.Instance, error) { return f.instance, nil }
func (f *fakeBackend) InstallDir() string                   { return inspect.InstallDirectory }
func (f *fakeBackend) ShowStatus(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "Bot：running")
	return nil
}
func (f *fakeBackend) ShowLogs(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "log-snapshot")
	return nil
}
func (f *fakeBackend) ControlService(_ *inspect.Instance, verb, target string, _ io.Writer) error {
	f.controlled = append(f.controlled, verb+":"+target)
	return nil
}
func (f *fakeBackend) ShowReadiness(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "业务就绪：基础条件通过")
	return nil
}
func (f *fakeBackend) VerifyWebDAV(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "TGCLOUD_DESTINATION=OK")
	return nil
}
func (f *fakeBackend) CollectDiagnostics(inst *inspect.Instance) (string, error) {
	return RenderDiagnostics(inst, "28.1.1", "2.35.0",
		`{"schema":1,"edition":"clouddrive2","version":"0.0.9","python":"3.12.10","configuration":"present","checks":{"heartbeat":"ok","telegram":"ok","destination":"ok","queue":"ok","capacity":"ok"},"failures":{}}`,
		map[string]string{"Bot": "ok", "网关": "ok"}, "共享 HTTPS 代理路由已登记"), nil
}
func (f *fakeBackend) SaveDiagnostics(_, dir string) (string, error) { return dir + "/diag.md", nil }
func (f *fakeBackend) ReadEnv(_ *inspect.Instance) (map[string]string, error) {
	return f.env, nil
}
func (f *fakeBackend) EntryInfo(_ *inspect.Instance) (EntryInfo, error) { return f.entryInfo, nil }
func (f *fakeBackend) ShowEntryStatus(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "访问模式：HTTPS")
	return nil
}
func (f *fakeBackend) ConfigureHTTPS(_ *inspect.Instance, domain, email, _, _ string, out io.Writer) error {
	f.configuredHTTPS = append(f.configuredHTTPS, domain+":"+email)
	fmt.Fprintln(out, "TGCLOUD_HTTPS=OK")
	return nil
}
func (f *fakeBackend) RenewHTTPSDryRun(_ *inspect.Instance, domain string, out io.Writer) error {
	f.renewDryRuns = append(f.renewDryRuns, domain)
	fmt.Fprintln(out, "TGCLOUD_STAGE=RENEW_DRY_RUN")
	return nil
}
func (f *fakeBackend) ShowExternalWebDAV(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "外部 WebDAV 尚未登记")
	return nil
}
func (f *fakeBackend) ManageExternalWebDAV(_ *inspect.Instance, out io.Writer) error {
	f.webdavManaged++
	fmt.Fprintln(out, "已转交外部 WebDAV 管理")
	return nil
}
func (f *fakeBackend) BotUsersRPC(_ *inspect.Instance, adminID int64, payload map[string]any) (UsersRecord, error) {
	f.usersRPCs = append(f.usersRPCs, payload)
	return UsersRecord{
		Schema:   1,
		AdminID:  adminID,
		Revision: "initial",
		Users:    []FamilyUser{{ID: 888001, Name: "家人A", Enabled: true}},
	}, nil
}
func (f *fakeBackend) BotControlRPC(_ *inspect.Instance, _ int64, payload map[string]any) (map[string]any, error) {
	f.controlRPCs = append(f.controlRPCs, payload)
	if payload["action"] == "quota-read" {
		return map[string]any{
			"api": float64(1),
			"usage": map[string]any{
				"daily_bytes":     float64(10 * 1024 * 1024 * 1024),
				"max_queued":      float64(5),
				"max_active":      float64(2),
				"completed_bytes": float64(1024 * 1024 * 1024),
				"reserved_bytes":  float64(0),
			},
		}, nil
	}
	return map[string]any{
		"api": float64(1),
		"settings": map[string]any{
			"classification":         "off",
			"notifications":          true,
			"recovery_notifications": false,
			"revision":               "initial",
		},
	}, nil
}
func (f *fakeBackend) RunListenAccount(_ *inspect.Instance, action string, out io.Writer) error {
	fmt.Fprintln(out, "listen:"+action)
	return nil
}
func (f *fakeBackend) ListBackups(out io.Writer) error {
	fmt.Fprintln(out, "config-abcd1234.tar.gz")
	return nil
}
func (f *fakeBackend) CreateInstanceSnapshot(_ *inspect.Instance, destDir string) (string, error) {
	f.snapshots = append(f.snapshots, destDir)
	return destDir + "/instance-clouddrive2-0.0.9.tar.gz", nil
}
func (f *fakeBackend) PreflightInstanceSnapshot(_ *inspect.Instance, archivePath, scratchDir string) (int, error) {
	f.preflights = append(f.preflights, archivePath+"@"+scratchDir)
	return 6, nil
}
func (f *fakeBackend) ProxyBackupAction(action string, _ int, _ string, out io.Writer) (string, error) {
	fmt.Fprintln(out, "proxy:"+action)
	return "", nil
}
func (f *fakeBackend) UninstallPreview(_, _ bool, out io.Writer) (string, error) {
	fmt.Fprintln(out, "TGCloud 卸载计划")
	return "deadbeef", nil
}
func (f *fakeBackend) UninstallExecute(_, _ bool, _ string, _ io.Writer) error {
	f.uninstallCalls++
	return nil
}

func TestMenuFiveGroupsAndAllClosures(t *testing.T) {
	if len(MainMenuItems) != 6 {
		t.Fatalf("expected 5 groups + exit, got %d", len(MainMenuItems))
	}
	for _, key := range []string{"1", "2", "3", "4", "5"} {
		if _, ok := SubmenuGroups[key]; !ok {
			t.Fatalf("missing submenu group %s", key)
		}
	}

	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	fb := &fakeBackend{
		instance: &inspect.Instance{
			Version: "0.0.9",
			Bot:     inspect.Container{ID: strings.Repeat("a", 64), State: "running", Health: "healthy"},
			Gateway: inspect.Container{ID: strings.Repeat("b", 64), State: "running", Health: "healthy"},
		},
		entryInfo: EntryInfo{Domain: "files.example.com", Email: "ops@example.com"},
		env: map[string]string{
			"ALLOWED_USER_ID":        "123456",
			"WEBDAV_URL_B64":         b64("http://tgcloud-clouddrive2:19798/dav"),
			"WEBDAV_USERNAME_B64":    b64("botuser"),
			"WEBDAV_PASSWORD_B64":    b64("supersecret"),
			"WEBDAV_TARGET_PATH_B64": b64("/Uploads"),
		},
	}

	// Flow covering all 5 groups + all interactive closures + TTY credential isolation + external WebDAV handoff:
	// 1 -> 3 (control) -> 3 (restart bot) -> n (cancel) -> 0
	// 2 -> 1 (entry) -> 3 (renew dry-run) -> y -> 0 (return)
	//   -> 2 (credentials) -> n (hide user) -> y (show pass on PrivateOut ONLY) -> 0
	//   -> 3 (external_webdav) -> 1 (open full manager) -> 0 -> 0
	// 3 -> 1 (users) -> 6 (quota-set) -> 1 (member 1) -> 20 (GB) -> 10 (queued) -> 3 (active) -> y -> 0 -> 0
	// 4 -> 3 (diagnostics) -> n (no save) -> 0 -> 0
	// 5 -> 2 (backups) -> 2 (snapshot create) -> "" (default dir) -> y -> 0 (return) -> 3 (uninstall) -> y -> WRONG -> 0
	// 0 (exit)
	input := strings.Join([]string{
		"1", "3", "3", "n", "0",
		"2", "1", "3", "y", "0", "2", "n", "y", "0", "3", "1", "0", "0",
		"3", "1", "6", "1", "20", "10", "3", "y", "0", "0",
		"4", "3", "n", "0", "0",
		"5", "2", "2", "", "y", "0", "3", "y", "WRONG", "0",
		"0",
	}, "\n") + "\n"
	var out, privOut bytes.Buffer
	app := NewApp(fb, strings.NewReader(input), &out, "0.0.9")
	app.PrivateOut = &privOut
	if code := app.Run(); code != 0 {
		t.Fatalf("App.Run() = %d, output=%s", code, out.String())
	}
	text := out.String()
	if len(fb.controlled) != 0 {
		t.Fatalf("declined restart should not mutate containers: %v", fb.controlled)
	}
	if len(fb.renewDryRuns) != 1 || fb.renewDryRuns[0] != "files.example.com" {
		t.Fatalf("expected renew dry-run for files.example.com, got: %v", fb.renewDryRuns)
	}
	if fb.webdavManaged != 1 {
		t.Fatalf("expected ManageExternalWebDAV handoff once, got %d", fb.webdavManaged)
	}
	if len(fb.snapshots) != 1 || fb.snapshots[0] != "/opt/tgcloud-snapshots/clouddrive2" {
		t.Fatalf("expected snapshot creation in default dir, got: %v", fb.snapshots)
	}
	hasQuotaWrite := false
	for _, rpc := range fb.controlRPCs {
		if rpc["action"] == "quota-write" {
			hasQuotaWrite = true
		}
	}
	if !hasQuotaWrite {
		t.Fatalf("expected quota-write RPC call, got: %v", fb.controlRPCs)
	}
	if fb.uninstallCalls != 0 {
		t.Fatalf("mismatched uninstall token should not execute uninstall")
	}
	// Password must NEVER appear on general stdout; it goes ONLY to PrivateOut.
	if strings.Contains(text, "supersecret") {
		t.Fatal("password leaked to non-private stdout")
	}
	if !strings.Contains(privOut.String(), "supersecret") || strings.Contains(privOut.String(), "botuser") {
		t.Fatalf("unexpected PrivateOut contents: %q", privOut.String())
	}
	for _, want := range []string{"服务运行管理", "访问入口管理", "业务功能设置", "健康检查诊断", "实例维护备份", "0. 返回上级", "配额已更新", "实例快照已核验", "WebDAV 用户名：已隐藏", "已转交外部 WebDAV 管理"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestCredentialsRejectsNonTTYOutput(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	fb := &fakeBackend{
		instance: &inspect.Instance{Version: "0.0.9", Bot: inspect.Container{State: "running"}, Gateway: inspect.Container{State: "running"}},
		env: map[string]string{
			"WEBDAV_URL_B64":      b64("http://gw:19798/dav"),
			"WEBDAV_USERNAME_B64": b64("u"),
			"WEBDAV_PASSWORD_B64": b64("secret-should-not-leak"),
		},
	}
	var out bytes.Buffer
	app := NewApp(fb, strings.NewReader("2\n2\n0\n0\n0\n"), &out, "0.0.9")
	// PrivateOut intentionally left nil (non-TTY).
	if code := app.Run(); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	if strings.Contains(out.String(), "secret-should-not-leak") || !strings.Contains(out.String(), "凭据只能在当前交互终端显示") {
		t.Fatalf("expected fail-closed non-TTY credential guard, got:\n%s", out.String())
	}
}

func TestDecodeWebDAVCredentialsLegacyFallback(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	creds, err := DecodeWebDAVCredentials(map[string]string{
		"CD2_WEBDAV_URL_B64":      b64("http://gw:19798/dav"),
		"CD2_WEBDAV_USERNAME_B64": b64("legacy-user"),
		"CD2_WEBDAV_PASSWORD_B64": b64("legacy-pass"),
		"CD2_TARGET_PATH_B64":     b64("/Media"),
	})
	if err != nil || creds.Username != "legacy-user" || creds.TargetPath != "/Media" {
		t.Fatalf("unexpected creds: %+v, err=%v", creds, err)
	}
}

func TestMainMenuShowsProcessVersionWhileSubmenuKeepsInstanceVersion(t *testing.T) {
	fb := &fakeBackend{
		instance: &inspect.Instance{
			Version: "0.0.9",
			Bot:     inspect.Container{State: "running"},
			Gateway: inspect.Container{State: "running"},
		},
	}
	var out bytes.Buffer
	app := NewApp(fb, strings.NewReader("1\n0\n0\n"), &out, "0.0.10")
	if code := app.Run(); code != 0 {
		t.Fatalf("App.Run() = %d, output=%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "管理工具（当前进程）：v0.0.10") {
		t.Fatalf("expected process version v0.0.10 in main menu, got:\n%s", text)
	}
	if !strings.Contains(text, "── 服务运行管理（CloudDrive2 · v0.0.9）──") {
		t.Fatalf("expected instance version v0.0.9 in submenu heading, got:\n%s", text)
	}
}
