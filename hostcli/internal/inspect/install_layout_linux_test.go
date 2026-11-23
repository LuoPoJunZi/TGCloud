package inspect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishInstallConfigKeepsExistingConfig(t *testing.T) {
	directory := t.TempDir()
	work := filepath.Join(directory, ".install-config")
	initial := []byte("SETTING=initial\n")
	if err := publishInstallConfig(directory, work, initial); err != nil {
		t.Fatal(err)
	}
	if err := publishInstallConfig(directory, work, []byte("SETTING=replace\n")); err == nil {
		t.Fatal("initial configuration replaced an existing file")
	}
	path := filepath.Join(directory, ".env")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected private .env, got %v", info.Mode())
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != string(initial) {
		t.Fatalf("existing .env changed: %v", err)
	}
}
