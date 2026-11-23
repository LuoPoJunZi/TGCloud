package uninstall

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

func TestBuildPlanAndRejectExternalWebDAVOrMounts(t *testing.T) {
	prevUID := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = prevUID })

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := Paths{
		InstallDir:     filepath.Join(root, "tgcloud"),
		BackupDir:      filepath.Join(root, "tgcloud-backups"),
		ProxyRoot:      filepath.Join(root, "tgcloud-proxy"),
		ProxyBackupDir: filepath.Join(root, "tgcloud-proxy-backups"),
		LANRoot:        filepath.Join(root, "tgcloud-lan"),
		WebDAVRoot:     filepath.Join(root, "tgcloud-webdav"),
		LockRoot:       filepath.Join(root, "tgcloud-cli-locks"),
		CLIRoot:        filepath.Join(root, "tgcloud-cli"),
		CLICommand:     filepath.Join(root, "tgcloud-bin"),
	}
	if err := os.MkdirAll(paths.InstallDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := `{"schema":1,"owner":"tgcloud.local-source","project":"tgcloud","mode":"shell","edition":"clouddrive2","state":"ready","version":"0.0.8"}`
	if err := os.WriteFile(filepath.Join(paths.InstallDir, "tgcloud-source.json"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.BackupDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.BackupDir, "config-abcd1234.tar.gz"), []byte("bak"), 0600); err != nil {
		t.Fatal(err)
	}

	botID := strings.Repeat("a", 64)
	gwID := strings.Repeat("b", 64)
	netID := strings.Repeat("c", 64)

	bot := DockerContainer{
		ID: botID, Name: "/" + inspect.BotName, Running: true,
		Labels: map[string]string{
			composeLabel + "project":              "tgcloud",
			composeLabel + "service":              inspect.BotName,
			composeLabel + "project.working_dir":  paths.InstallDir,
			composeLabel + "project.config_files": filepath.Join(paths.InstallDir, "docker-compose.yml"),
		},
		Mounts: []DockerMount{
			{Type: "bind", Source: filepath.Join(paths.InstallDir, "data"), Destination: "/data"},
			{Type: "bind", Source: filepath.Join(paths.InstallDir, "downloads"), Destination: "/downloads"},
			{Type: "bind", Source: filepath.Join(paths.InstallDir, "logs"), Destination: "/logs"},
			{Type: "bind", Source: filepath.Join(paths.InstallDir, "config"), Destination: "/config"},
		},
	}
	gw := DockerContainer{
		ID: gwID, Name: "/" + inspect.GatewayName, Running: true,
		Labels: map[string]string{
			composeLabel + "project":              "tgcloud",
			composeLabel + "service":              "clouddrive2",
			composeLabel + "project.working_dir":  paths.InstallDir,
			composeLabel + "project.config_files": filepath.Join(paths.InstallDir, "docker-compose.yml"),
		},
		Mounts: []DockerMount{
			{Type: "bind", Source: filepath.Join(paths.InstallDir, "clouddrive/config"), Destination: "/Config"},
			{Type: "bind", Source: filepath.Join(paths.InstallDir, "clouddrive/mounts"), Destination: "/CloudNAS"},
		},
	}
	net := DockerNetwork{
		ID: netID, Name: dockerNetwork, Driver: "bridge",
		Labels: map[string]string{
			composeLabel + "project": "tgcloud",
			composeLabel + "network": dockerNetwork,
		},
		Containers: map[string]interface{}{botID: map[string]any{}, gwID: map[string]any{}},
	}
	docker := func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "ps":
			return []byte(botID + "\n" + gwID + "\n"), nil
		case args[0] == "container" && args[1] == "inspect":
			a, _ := json.Marshal(bot)
			b, _ := json.Marshal(gw)
			return append(append(a, '\n'), b...), nil
		case args[0] == "network" && args[1] == "ls":
			return []byte(netID + "\n"), nil
		case args[0] == "network" && args[1] == "inspect":
			return json.Marshal(net)
		default:
			t.Fatalf("unexpected docker call: %v", args)
			return nil, nil
		}
	}
	noMounts := func() ([]string, error) { return []string{"/"}, nil }

	plan, err := BuildPlan(paths, Options{PurgeData: true, PurgeBackups: true}, docker, noMounts)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}
	if !slices.Equal(plan.Summary.Containers, []string{inspect.GatewayName, inspect.BotName}) ||
		!slices.Equal(plan.Summary.Networks, []string{dockerNetwork}) ||
		!slices.Equal(plan.Summary.BackupDirs, []string{paths.BackupDir}) ||
		plan.Summary.PlanDigest == "" {
		t.Fatalf("unexpected plan summary: %+v", plan.Summary)
	}
	var preview bytes.Buffer
	WritePreview(plan, &preview)
	if !strings.Contains(preview.String(), "TGCloud 卸载计划") || !strings.Contains(preview.String(), paths.BackupDir) {
		t.Fatalf("unexpected preview: %q", preview.String())
	}

	// Active mountpoint under install dir must be rejected.
	fuseMounts := func() ([]string, error) { return []string{"/", filepath.Join(paths.InstallDir, "clouddrive/mounts/cloud")}, nil }
	if _, err := BuildPlan(paths, Options{PurgeData: true, PurgeBackups: true}, docker, fuseMounts); err == nil {
		t.Fatal("expected BuildPlan to reject active FUSE mountpoint under install dir")
	}

	// External WebDAV config must block uninstall.
	if err := os.MkdirAll(filepath.Join(paths.WebDAVRoot, "clouddrive2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.WebDAVRoot, "clouddrive2/config.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(paths, Options{PurgeData: true, PurgeBackups: true}, docker, noMounts); err == nil || !strings.Contains(err.Error(), "外部 WebDAV") {
		t.Fatalf("expected external webdav block, got: %v", err)
	}
}
