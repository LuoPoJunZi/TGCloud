package backupops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

func TestProxyBackupCreateVerifyDrillAndPrune(t *testing.T) {
	prevUID := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = prevUID })

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxyRoot := filepath.Join(root, "proxy")
	backupRoot := filepath.Join(root, "backups")
	for _, rel := range []string{"state", "nginx/conf.d", "certbot/letsencrypt/archive/ex.test", "certbot/letsencrypt/live/ex.test"} {
		if err := os.MkdirAll(filepath.Join(proxyRoot, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proxyRoot, "state/domains.json"), []byte(`{"version":1,"project":"tgcloud","domains":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyRoot, "docker-compose.yml"), []byte("services:{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyRoot, "nginx/nginx.conf"), []byte("events {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyRoot, "certbot/letsencrypt/archive/ex.test/fullchain1.pem"), []byte("CERT"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../archive/ex.test/fullchain1.pem", filepath.Join(proxyRoot, "certbot/letsencrypt/live/ex.test/fullchain.pem")); err != nil {
		t.Fatal(err)
	}

	archive1, count1, err := CreateProxyBackup(proxyRoot, backupRoot)
	if err != nil || count1 < 5 {
		t.Fatalf("CreateProxyBackup 1 failed: archive=%q count=%d err=%v", archive1, count1, err)
	}
	if drilled, err := RecoveryDrill(archive1, backupRoot); err != nil || drilled != count1 {
		t.Fatalf("RecoveryDrill failed: drilled=%d err=%v", drilled, err)
	}
	archive2, _, err := CreateProxyBackup(proxyRoot, backupRoot)
	if err != nil {
		t.Fatalf("CreateProxyBackup 2 failed: %v", err)
	}
	items, err := ListProxyBackups(backupRoot)
	if err != nil || len(items) != 2 || items[0].Integrity != "OK" || items[1].Integrity != "OK" {
		t.Fatalf("ListProxyBackups unexpected: %+v, err=%v", items, err)
	}
	plan, err := PlanProxyPrune(backupRoot, 1)
	if err != nil || len(plan.Retain) != 1 || len(plan.Remove) != 1 {
		t.Fatalf("PlanProxyPrune unexpected: %+v, err=%v", plan, err)
	}
	if _, err := ExecuteProxyPrune(backupRoot, 1, plan.PlanID); err != nil {
		t.Fatalf("ExecuteProxyPrune failed: %v", err)
	}
	remaining, err := ListProxyBackups(backupRoot)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("expected 1 remaining backup after prune (archive2=%q), got: %+v, err=%v", archive2, remaining, err)
	}
}

func TestListInstanceBackups(t *testing.T) {
	prevUID := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = prevUID })

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config-abcd1234.tar.gz"), []byte("tar"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := ListInstanceBackups(dir)
	if err != nil || len(items) != 1 || items[0].Name != "config-abcd1234.tar.gz" {
		t.Fatalf("ListInstanceBackups unexpected: %+v, err=%v", items, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stranger.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListInstanceBackups(dir); err == nil {
		t.Fatal("expected unknown file rejection")
	}
}

func TestInstanceSnapshotCreateAndPreflight(t *testing.T) {
	prevUID := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = prevUID })

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(root, "instance")
	destDir := filepath.Join(root, "snapshots")
	for _, rel := range []string{"data", "config", "clouddrive/config"} {
		if err := os.MkdirAll(filepath.Join(installDir, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	sqliteStub := append([]byte("SQLite format 3\x00"), make([]byte, 64)...)
	for rel, body := range map[string][]byte{
		".env":                   []byte("ALLOWED_USER_ID=123\n"),
		"docker-compose.yml":     []byte("services:{}\n"),
		"data/tgcloud.db":        sqliteStub,
		"data/bot.session":       sqliteStub,
		"config/settings.json":   []byte("{}\n"),
		"clouddrive/config/c.db": sqliteStub,
	} {
		if err := os.WriteFile(filepath.Join(installDir, filepath.FromSlash(rel)), body, 0600); err != nil {
			t.Fatal(err)
		}
	}

	botID := strings.Repeat("a", 64)
	gwID := strings.Repeat("b", 64)
	botRunning, gwRunning := true, true
	var ops []string
	docker := func(args ...string) ([]byte, error) {
		switch args[0] {
		case "inspect":
			name := args[len(args)-1]
			if name == inspect.BotName {
				return json.Marshal(containerState{ID: botID, Running: botRunning})
			}
			return json.Marshal(containerState{ID: gwID, Running: gwRunning})
		case "exec":
			return []byte("0\n"), nil
		case "stop":
			id := args[len(args)-1]
			ops = append(ops, "stop:"+id[:4])
			if id == botID {
				botRunning = false
			} else {
				gwRunning = false
			}
			return nil, nil
		case "start":
			id := args[len(args)-1]
			ops = append(ops, "start:"+id[:4])
			if id == botID {
				botRunning = true
			} else {
				gwRunning = true
			}
			return nil, nil
		default:
			t.Fatalf("unexpected docker call: %v", args)
			return nil, nil
		}
	}

	archive, err := CreateInstanceSnapshot(installDir, destDir, "0.0.9", docker)
	if err != nil {
		t.Fatalf("CreateInstanceSnapshot failed: %v", err)
	}
	if !botRunning || !gwRunning {
		t.Fatalf("services were not restored after snapshot: bot=%v gw=%v", botRunning, gwRunning)
	}
	// Gateway must start before Bot on restore.
	wantOps := []string{"stop:aaaa", "stop:bbbb", "start:bbbb", "start:aaaa"}
	if strings.Join(ops, ",") != strings.Join(wantOps, ",") {
		t.Fatalf("unexpected stop/start order: got %v, want %v", ops, wantOps)
	}
	count, err := PreflightInstanceSnapshot(archive, destDir, "0.0.9")
	if err != nil || count != 6 {
		t.Fatalf("PreflightInstanceSnapshot failed: count=%d err=%v", count, err)
	}
	// Incompatible target version (older than snapshot) must fail.
	if _, err := PreflightInstanceSnapshot(archive, destDir, "0.0.8"); err == nil {
		t.Fatal("expected version compatibility rejection")
	}
	// Non-empty WAL sidecar must be rejected.
	if err := os.WriteFile(filepath.Join(installDir, "data/tgcloud.db-wal"), []byte("wal"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateInstanceSnapshot(installDir, destDir, "0.0.9", docker); err == nil {
		t.Fatal("expected live WAL sidecar rejection")
	}
}
