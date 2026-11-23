package inspect

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeConfigArchiveKeepsConfigurationAndExcludesRuntimeData(t *testing.T) {
	directory := t.TempDir()
	for _, excluded := range []string{"data", "downloads", "logs", "clouddrive"} {
		if err := os.Mkdir(filepath.Join(directory, excluded), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, excluded, "payload"), []byte("runtime data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte("KEY=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := os.CreateTemp(t.TempDir(), "config-*.tar.gz.partial")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := archiveUpgradeConfig(directory, target.Name(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	compressed, err := gzip.NewReader(target)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	files := tar.NewReader(compressed)
	var gotConfig bool
	for {
		entry, err := files.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch entry.Name {
		case "./":
		case "./.env":
			content, err := io.ReadAll(files)
			if err != nil || string(content) != "KEY=fixture\n" || entry.Mode != 0600 {
				t.Fatalf("configuration not preserved: %v", err)
			}
			gotConfig = true
		default:
			t.Fatalf("unexpected file in configuration archive: %q", entry.Name)
		}
	}
	if !gotConfig {
		t.Fatal("configuration missing from the archive")
	}
}
