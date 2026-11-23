package inspect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFreshPublicationWithdrawsOnlyThisAttempt(t *testing.T) {
	target := t.TempDir()
	work := t.TempDir()
	for _, name := range []string{".env", "app", "docker-compose.yml"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("new file"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(target, "app")
	if err := os.WriteFile(path, []byte("existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishFreshEntries(work, target); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected no-replace conflict, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new .env should have been withdrawn: %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "existing file" {
		t.Fatalf("existing file was changed: %v", err)
	}
}
