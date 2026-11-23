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
	instance       *inspect.Instance
	env            map[string]string
	controlled     []string
	usersRPCs      []map[string]any
	controlRPCs    []map[string]any
	uninstallCalls int
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
		`{"schema":1,"edition":"clouddrive2","version":"0.0.8","python":"3.12.10","configuration":"present","checks":{"heartbeat":"ok","telegram":"ok","destination":"ok","queue":"ok","capacity":"ok"},"failures":{}}`,
		map[string]string{"Bot": "ok", "网关": "ok"}, "共享 HTTPS 代理路由已登记"), nil
}
func (f *fakeBackend) SaveDiagnostics(_, dir string) (string, error) { return dir + "/diag.md", nil }
func (f *fakeBackend) ReadEnv(_ *inspect.Instance) (map[string]string, error) {
	return f.env, nil
}
func (f *fakeBackend) ShowEntryStatus(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "访问模式：HTTPS")
	return nil
}
func (f *fakeBackend) ShowExternalWebDAV(_ *inspect.Instance, out io.Writer) error {
	fmt.Fprintln(out, "外部 WebDAV 尚未登记")
	return nil
}
func (f *fakeBackend) BotUsersRPC(_ *inspect.Instance, adminID int64, payload map[string]any) (UsersRecord, error) {
	f.usersRPCs = append(f.usersRPCs, payload)
	return UsersRecord{Schema: 1, AdminID: adminID, Revision: "initial"}, nil
}
func (f *fakeBackend) BotControlRPC(_ *inspect.Instance, _ int64, payload map[string]any) (map[string]any, error) {
	f.controlRPCs = append(f.controlRPCs, payload)
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

func TestMenuFiveGroupsAndNavigation(t *testing.T) {
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
			Version: "0.0.8",
			Bot:     inspect.Container{ID: strings.Repeat("a", 64), State: "running", Health: "healthy"},
			Gateway: inspect.Container{ID: strings.Repeat("b", 64), State: "running", Health: "healthy"},
		},
		env: map[string]string{
			"ALLOWED_USER_ID":        "123456",
			"WEBDAV_URL_B64":         b64("http://tgcloud-clouddrive2:19798/dav"),
			"WEBDAV_USERNAME_B64":    b64("botuser"),
			"WEBDAV_PASSWORD_B64":    b64("supersecret"),
			"WEBDAV_TARGET_PATH_B64": b64("/Uploads"),
		},
	}

	// Flow:
	// 1 (服务运行管理) -> 3 (启停) -> 3 (重启 Bot) -> n (取消!) -> 0 (返回主菜单)
	// 2 (访问入口管理) -> 2 (WebDAV凭据) -> n (不显示密码) -> 0 (返回上级) -> 0 (返回主菜单)
	// 4 (健康检查诊断) -> 3 (故障诊断报告) -> n (不保存) -> 0 (返回上级) -> 0 (返回主菜单)
	// 5 (实例维护备份) -> 3 (卸载) -> y -> WRONG TOKEN (取消!) -> 0 (返回主菜单)
	// 0 (退出)
	input := strings.Join([]string{
		"1", "3", "3", "n", "0",
		"2", "2", "n", "0", "0",
		"4", "3", "n", "0", "0",
		"5", "3", "y", "WRONG", "0",
		"0",
	}, "\n") + "\n"
	var out bytes.Buffer
	app := NewApp(fb, strings.NewReader(input), &out, "0.0.8")
	if code := app.Run(); code != 0 {
		t.Fatalf("App.Run() = %d, output=%s", code, out.String())
	}
	text := out.String()
	if len(fb.controlled) != 0 {
		t.Fatalf("declined restart should not mutate containers: %v", fb.controlled)
	}
	if fb.uninstallCalls != 0 {
		t.Fatalf("mismatched uninstall token should not execute uninstall")
	}
	if strings.Contains(text, "supersecret") {
		t.Fatal("password leaked when confirmation was declined")
	}
	for _, want := range []string{"服务运行管理", "访问入口管理", "业务功能设置", "健康检查诊断", "实例维护备份", "0. 返回上级", "TGCloud 故障诊断报告", "密码：已隐藏"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
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
