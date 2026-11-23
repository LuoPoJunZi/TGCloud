package inspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeReplacementFailureKeepsDataAvailableForRollback(t *testing.T) {
	root := t.TempDir()
	directory, incoming, previous := filepath.Join(root, "installed"), filepath.Join(root, "incoming"), filepath.Join(root, "previous")
	write := func(dir, name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name, expected string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != expected {
			t.Fatalf("unexpected content in %s: %v", name, err)
		}
	}
	write(directory, "app/main.py", "old source")
	write(directory, ".env", "existing configuration\r\n")
	write(directory, "data/tgcloud.db", "existing state")
	write(directory, "blocked", "unusable directory")
	write(incoming, "app/main.py", "new source")
	write(incoming, "blocked/entry", "replacement")
	write(previous, "app/main.py", "old source")
	if err := replaceUpgradeFiles(incoming, directory, []string{"app", "blocked/entry"}); err == nil {
		t.Fatal("expected replacement to fail after replacing app")
	}
	check("app/main.py", "new source")
	if err := replaceUpgradeFiles(previous, directory, []string{"app"}); err != nil {
		t.Fatal(err)
	}
	check("app/main.py", "old source")
	check(".env", "existing configuration\r\n")
	check("data/tgcloud.db", "existing state")
}

func TestRollbackDoesNotStopAnUnrelatedBotContainer(t *testing.T) {
	id := strings.Repeat("a", 64)
	docker := func(args ...string) ([]byte, error) {
		switch args[0] + " " + args[1] {
		case "container ls":
			return []byte(id), nil
		case "container inspect":
			return []byte(`{"id":"` + id + `","name":"/` + botName +
				`","project":"other","directory":"/opt/other","service":"` + botName + `"}`), nil
		default:
			t.Fatalf("issued an operation on an unrelated container: %q", args)
			return nil, nil
		}
	}
	if err := removeUpgradeBot("/opt/tgcloud", docker); err != errInstanceOwner {
		t.Fatalf("expected container ownership error, got %v", err)
	}
}
