package backupops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

func createSQLiteFixture(t *testing.T, path string, wal bool, activeCount int) {
	t.Helper()
	script := `import os, sqlite3, sys
p, wal, active = sys.argv[1], sys.argv[2] == "1", int(sys.argv[3])
c = sqlite3.connect(p)
if wal:
    c.execute("PRAGMA journal_mode=WAL")
    c.execute("PRAGMA wal_autocheckpoint=0")
c.execute("CREATE TABLE IF NOT EXISTS tasks (id INTEGER PRIMARY KEY, state TEXT)")
c.execute("DELETE FROM tasks")
for i in range(active):
    c.execute("INSERT INTO tasks(state) VALUES ('downloading')")
c.execute("INSERT INTO tasks(state) VALUES ('completed')")
c.commit()
if wal:
    os._exit(0)
c.close()
`
	if out, err := exec.Command(snapshotPyBin, "-I", "-S", "-B", "-c", script, path, map[bool]string{true: "1", false: "0"}[wal], map[int]string{0: "0", 1: "1"}[activeCount]).CombinedOutput(); err != nil {
		t.Fatalf("create sqlite fixture %s failed: %v (%s)", path, err, string(out))
	}
}

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

func TestInstanceSnapshotCreatePreflightAndGuards(t *testing.T) {
	prevUID := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = prevUID })

	prevLock := lockInstanceFunc
	lockBusy := false
	lockInstanceFunc = func() (io.Closer, error) {
		if lockBusy {
			return nil, errors.New("lock busy")
		}
		return noopCloser{}, nil
	}
	t.Cleanup(func() { lockInstanceFunc = prevLock })

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(root, "instance")
	destDir := filepath.Join(root, "snapshots")
	for _, rel := range []string{"data", "config", "downloads", "logs", "clouddrive/config", "clouddrive/mounts"} {
		if err := os.MkdirAll(filepath.Join(installDir, rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range map[string][]byte{
		".env":                 []byte("ALLOWED_USER_ID=123\n"),
		"docker-compose.yml":   []byte("services:{}\n"),
		"tgcloud-release.json": []byte(`{"version":"0.0.9"}`),
		"tgcloud-source.json":  []byte(`{"schema":1,"owner":"tgcloud.local-source","project":"tgcloud","mode":"shell","edition":"clouddrive2","state":"ready","version":"0.0.9"}`),
		"config/settings.json": []byte("{}\n"),
	} {
		if err := os.WriteFile(filepath.Join(installDir, filepath.FromSlash(rel)), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	createSQLiteFixture(t, filepath.Join(installDir, "data/tgcloud.db"), true, 0)
	createSQLiteFixture(t, filepath.Join(installDir, "data/bot.session"), false, 0)
	createSQLiteFixture(t, filepath.Join(installDir, "clouddrive/config/c.db"), false, 0)

	botID := strings.Repeat("a", 64)
	gwID := strings.Repeat("b", 64)
	botRunning, gwRunning := true, true
	var ops []string
	makeContainer := func(name, service, id string, running bool, paths map[string]string) inspect.Container {
		state := "exited"
		if running {
			state = "running"
		}
		c := inspect.Container{ID: id, Name: "/" + name, Project: "tgcloud", Directory: installDir, Service: service, State: state, Health: "healthy"}
		for dst, rel := range paths {
			c.Mounts = append(c.Mounts, inspect.Mount{Type: "bind", Source: filepath.Join(installDir, filepath.FromSlash(rel)), Destination: dst})
		}
		return c
	}
	docker := func(args ...string) ([]byte, error) {
		switch args[0] {
		case "container":
			if args[1] == "ls" {
				return []byte(inspect.BotName + "\n" + inspect.GatewayName), nil
			}
			bot := makeContainer(inspect.BotName, inspect.BotName, botID, botRunning, map[string]string{
				"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs",
			})
			gw := makeContainer(inspect.GatewayName, inspect.GatewayService, gwID, gwRunning, map[string]string{
				"/CloudNAS": "clouddrive/mounts", "/Config": "clouddrive/config",
			})
			b1, _ := json.Marshal(bot)
			b2, _ := json.Marshal(gw)
			return append(append(b1, '\n'), b2...), nil
		case "inspect":
			name := args[len(args)-1]
			if name == inspect.BotName {
				return json.Marshal(containerState{ID: botID, Running: botRunning})
			}
			return json.Marshal(containerState{ID: gwID, Running: gwRunning})
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

	// Lock contention must block CreateInstanceSnapshot immediately.
	lockBusy = true
	if _, err := CreateInstanceSnapshot(installDir, destDir, "0.0.9", docker); err == nil {
		t.Fatal("expected lock contention error")
	}
	lockBusy = false

	// Normal WAL-enabled SQLite snapshot succeeds, folds WAL cleanly, and restores Gateway before Bot.
	archive, err := CreateInstanceSnapshot(installDir, destDir, "0.0.9", docker)
	if err != nil {
		t.Fatalf("CreateInstanceSnapshot failed: %v", err)
	}
	if !botRunning || !gwRunning {
		t.Fatalf("services were not restored after snapshot: bot=%v gw=%v", botRunning, gwRunning)
	}
	wantOps := []string{"stop:aaaa", "stop:bbbb", "start:bbbb", "start:aaaa"}
	if strings.Join(ops, ",") != strings.Join(wantOps, ",") {
		t.Fatalf("unexpected stop/start order: got %v, want %v", ops, wantOps)
	}
	count, err := PreflightInstanceSnapshot(archive, destDir, "0.0.9")
	if err != nil || count != 8 {
		t.Fatalf("PreflightInstanceSnapshot failed: count=%d err=%v", count, err)
	}
	if _, err := PreflightInstanceSnapshot(archive, destDir, "0.0.8"); err == nil {
		t.Fatal("expected version compatibility rejection")
	}

	// Active task appearing right before stop must cancel snapshot and restore services.
	prevActive := activeTasksFunc
	calls := 0
	activeTasksFunc = func(string) (int, error) {
		calls++
		if calls == 2 {
			return 1, nil
		}
		return 0, nil
	}
	ops = nil
	if _, err := CreateInstanceSnapshot(installDir, destDir, "0.0.9", docker); err == nil {
		t.Fatal("expected post-stop active task cancellation")
	}
	activeTasksFunc = prevActive
	if !botRunning || !gwRunning || strings.Join(ops, ",") != strings.Join(wantOps, ",") {
		t.Fatalf("services not restored properly on post-stop cancel: bot=%v gw=%v ops=%v", botRunning, gwRunning, ops)
	}

	// Ambiguous extra database must be rejected.
	extraDB := filepath.Join(installDir, "data/tasks.db")
	createSQLiteFixture(t, extraDB, false, 0)
	if _, err := CreateInstanceSnapshot(installDir, destDir, "0.0.9", docker); err == nil {
		t.Fatal("expected ambiguous extra database rejection")
	}
	_ = os.Remove(extraDB)

	// Version mismatch against lock-time discovered inst.Version must be rejected.
	if _, err := CreateInstanceSnapshot(installDir, destDir, "0.0.8", docker); err == nil {
		t.Fatal("expected discovered version mismatch rejection")
	}

	// Corrupt SQLite file (valid header, invalid B-tree) must be rejected by PRAGMA quick_check,
	// leave zero parent-owned staging dirs behind, and merge restore errors if restart also fails.
	corrupt := append([]byte("SQLite format 3\x00"), make([]byte, 256)...)
	if err := os.WriteFile(filepath.Join(installDir, "data/bot.session"), corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	failStartDocker := func(args ...string) ([]byte, error) {
		if args[0] == "start" {
			return nil, errors.New("start failed fixture")
		}
		return docker(args...)
	}
	_, err = CreateInstanceSnapshot(installDir, destDir, "0.0.9", failStartDocker)
	if err == nil || !strings.Contains(err.Error(), "实例快照一致性备份或校验失败") || !strings.Contains(err.Error(), "原服务恢复未确认") {
		t.Fatalf("expected merged pack + restore error, got: %v", err)
	}
	entries, _ := os.ReadDir(destDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "snapshot-") || strings.HasPrefix(e.Name(), "tgcloud-restore-check-") {
			t.Fatalf("leftover temp dir after failed snapshot: %s", e.Name())
		}
	}
}

func TestPreflightAndRecoveryDrillCleanTempOnCancel(t *testing.T) {
	prevUID := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = prevUID })

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	snapArchive := filepath.Join(root, "instance-clouddrive2-0.0.9-20260101T000000Z-abcdef123456.tar.gz")
	if err := os.WriteFile(snapArchive, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	proxyArchive := filepath.Join(root, "proxy-20260101T000000Z-"+strings.Repeat("a", 32)+".tar.gz")
	if err := os.WriteFile(proxyArchive, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := preflightInstanceSnapshotCtx(ctx, snapArchive, root, "0.0.9"); err == nil || !strings.Contains(err.Error(), "中断") {
		t.Fatalf("expected interrupted preflight error, got %v", err)
	}
	if _, err := recoveryDrillCtx(ctx, proxyArchive, root); err == nil || !strings.Contains(err.Error(), "中断") {
		t.Fatalf("expected interrupted recovery drill error, got %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tgcloud-restore-check-") || strings.HasPrefix(e.Name(), "recovery-drill-") {
			t.Fatalf("leftover temp dir after cancellation: %s", e.Name())
		}
	}
}
