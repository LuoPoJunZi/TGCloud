package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

func TestRuntimeStatusAndCheck(t *testing.T) {
	const (
		botName     = "tgcloud-clouddrive2-bot"
		gatewayName = "tgcloud-clouddrive2"
		version     = "0.0.6"
	)
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	marker := `{"schema":1,"owner":"tgcloud.local-source","project":"tgcloud","mode":"shell","edition":"clouddrive2","state":"ready","version":"` + version + `"}`
	if err := os.WriteFile(filepath.Join(directory, "tgcloud-source.json"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	botID := strings.Repeat("a", 64)
	gatewayID := strings.Repeat("b", 64)

	for _, tc := range []struct {
		name, action, state, mode string
		stale, probe              bool
		exit                      int
		want                      string
	}{
		{name: "运行状态", action: "status", state: "running", want: "Bot：运行中"},
		{name: "运行就绪", action: "check", state: "running", probe: true, want: "业务就绪：基础条件通过"},
		{name: "停止状态", action: "status", state: "exited", want: "Bot：已停止"},
		{name: "已停止时不调用探针", action: "check", state: "exited", exit: 1, want: "业务就绪：服务尚未全部运行"},
		{name: "报告过期", action: "check", state: "running", stale: true, probe: true, exit: 1, want: "Telegram 连接：未核实"},
		{name: "独立 Compose 实例", action: "check", state: "running", mode: "compose", exit: 2, want: "独立 Docker Compose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var metadata bytes.Buffer
			for _, service := range []struct {
				name, id, state, label string
				directories            map[string]string
			}{
				{botName, botID, tc.state, botName, map[string]string{
					"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs",
				}},
				{gatewayName, gatewayID, "running", "clouddrive2", map[string]string{
					"/Config": "clouddrive/config", "/CloudNAS": "clouddrive/mounts",
				}},
			} {
				var mounts []map[string]string
				for destination, relative := range service.directories {
					mounts = append(mounts, map[string]string{
						"Type": "bind", "Source": filepath.Join(directory, filepath.FromSlash(relative)), "Destination": destination,
					})
				}
				if err := json.NewEncoder(&metadata).Encode(map[string]any{
					"id": service.id, "name": "/" + service.name, "state": service.state, "health": "healthy",
					"project": "tgcloud", "directory": directory, "service": service.label, "mode": tc.mode, "mounts": mounts,
				}); err != nil {
					t.Fatal(err)
				}
			}

			probeCalled := false
			docker := func(args ...string) ([]byte, error) {
				switch {
				case slices.Equal(args, []string{"container", "ls", "--all", "--filter", "name=" + gatewayName, "--format", "{{.Names}}"}):
					return []byte(botName + "\n" + gatewayName + "\n"), nil
				case len(args) == 6 && args[0] == "container" && args[1] == "inspect" && args[4] == botName && args[5] == gatewayName:
					return metadata.Bytes(), nil
				case slices.Equal(args, []string{"exec", botID, "python", "-B", "-m", "tgcloud.monitoring.readiness"}):
					probeCalled = true
					checks := map[string]string{"heartbeat": "ok", "telegram": "ok", "destination": "ok", "queue": "ok", "capacity": "ok"}
					if tc.stale {
						// Expired reports preserve the heartbeat and mark the other observations unknown.
						for _, key := range []string{"telegram", "destination", "queue", "capacity"} {
							checks[key] = "unknown"
						}
					}
					return json.Marshal(map[string]any{"schema": 1, "version": version, "ready": !tc.stale, "checks": checks})
				default:
					t.Fatalf("unexpected Docker arguments: %q", args)
					return nil, nil
				}
			}

			var output bytes.Buffer
			exit := run([]string{tc.action}, &output, &output, directory, docker)
			if exit != tc.exit || !strings.Contains(output.String(), tc.want) || probeCalled != tc.probe {
				t.Fatalf("exit=%d, probe=%t, output=%q", exit, probeCalled, output.String())
			}
		})
	}
}

func TestUnconfirmedServiceControlDoesNotContactDocker(t *testing.T) {
	docker := func(args ...string) ([]byte, error) {
		t.Fatalf("unexpected Docker arguments: %q", args)
		return nil, nil
	}
	var output bytes.Buffer
	if exit := run([]string{"stop", "bot"}, &output, &output, t.TempDir(), docker); exit != 2 {
		t.Fatalf("exit=%d, output=%q", exit, output.String())
	}
}

func TestStatusAndCheckJSONWhenNoInstance(t *testing.T) {
	docker := func(args ...string) ([]byte, error) {
		return nil, nil
	}
	directory := t.TempDir()
	for _, tc := range []struct {
		action string
		exit   int
	}{
		{action: "status", exit: 0},
		{action: "check", exit: 1},
	} {
		var out, errOut bytes.Buffer
		code := run([]string{tc.action, "--json"}, &out, &errOut, directory, docker)
		if code != tc.exit {
			t.Fatalf("%s --json exit=%d, want %d, stderr=%q", tc.action, code, tc.exit, errOut.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
			t.Fatalf("%s --json did not emit JSON: %v, raw=%q", tc.action, err, out.String())
		}
		if present, ok := payload["present"].(bool); !ok || present {
			t.Fatalf("%s --json unexpected payload: %v", tc.action, payload)
		}
	}
}

func TestSupportedReflectsPlatform(t *testing.T) {
	want := 1
	if inspect.LinuxSupported() {
		want = 0
	}
	docker := func(args ...string) ([]byte, error) {
		t.Fatalf("unexpected Docker arguments: %q", args)
		return nil, nil
	}
	for _, cmd := range []string{"install-transaction", "stage-upgrade", "fresh-install", "install-layout", "prepare-dependencies", "uninstall", "backups", "webdav-entry", "diagnostics", "menu"} {
		var out, errOut bytes.Buffer
		if got := run([]string{cmd, "--supported"}, &out, &errOut, t.TempDir(), docker); got != want {
			t.Fatalf("%s --supported exit=%d, want %d", cmd, got, want)
		}
	}
}
