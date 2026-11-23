package backupops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
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
	// Unknown file must fail closed.
	if err := os.WriteFile(filepath.Join(dir, "stranger.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListInstanceBackups(dir); err == nil {
		t.Fatal("expected unknown file rejection")
	}
}
