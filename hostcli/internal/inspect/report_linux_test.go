package inspect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallBackupSummary(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config-previous.tar.gz", "database-previous.db", "env-previous.env", "database-unfinished.db.partial"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("backup"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("database-previous.db", filepath.Join(directory, "database-alias.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "config-not-a-file.tar.gz"), 0700); err != nil {
		t.Fatal(err)
	}
	if count, size, err := backupInventory(directory); err != nil || count != 3 || size != 18 {
		t.Fatalf("inventory = %d files, %d bytes, %v", count, size, err)
	}
	large := filepath.Join(directory, "database-previous.db")
	if err := os.Truncate(large, 5*1024*1024*1024); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	showBackupSummary(directory, &output)
	if !strings.Contains(output.String(), "当前保留 3 份") || !strings.Contains(output.String(), "历史备份已超过 5GB") {
		t.Fatalf("unexpected summary output %q", output.String())
	}
	output.Reset()
	showBackupSummary(filepath.Join(directory, "missing"), &output)
	if !strings.Contains(output.String(), "无法统计历史备份") {
		t.Fatalf("missing inventory warning: %q", output.String())
	}
}
