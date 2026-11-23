package inspect

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureAPTKeyringDirRestoresModeUnderRestrictiveUmask(t *testing.T) {
	previousOwner := managedOwnerUID
	managedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { managedOwnerUID = previousOwner })

	oldUmask := syscall.Umask(0077)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	keyrings := filepath.Join(t.TempDir(), "etc", "apt", "keyrings")
	if err := ensureAPTKeyringDir(keyrings); err != nil {
		t.Fatalf("ensureAPTKeyringDir failed: %v", err)
	}
	info, err := os.Lstat(keyrings)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0755 {
		t.Fatalf("expected keyring directory mode 0755 under umask 0077, got %04o", perm)
	}
}

func TestPublishAPTFileModeAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tgcloud-docker.sources")
	content := []byte("Types: deb\nURIs: https://download.docker.com/linux/ubuntu\n")

	created, err := publishAPTFile(path, content)
	if err != nil || !created {
		t.Fatalf("publishAPTFile create failed: created=%v err=%v", created, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Fatalf("expected published APT file mode 0644, got %04o", perm)
	}

	created, err = publishAPTFile(path, content)
	if err != nil || created {
		t.Fatalf("idempotent publishAPTFile failed: created=%v err=%v", created, err)
	}
	if _, err := publishAPTFile(path, []byte("different")); err == nil {
		t.Fatal("publishAPTFile overwrote an existing differing file")
	}
}
