package hostutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountpointsAndParseMountinfo(t *testing.T) {
	points, err := Mountpoints()
	if err != nil || len(points) == 0 {
		t.Fatalf("Mountpoints() = %v, %v", points, err)
	}
	parsed, err := ParseMountinfo("22 1 8:1 / /opt/tgcloud/my\\040dir rw,relatime - ext4 /dev/sda1 rw\n")
	if err != nil || len(parsed) != 1 || parsed[0] != "/opt/tgcloud/my dir" {
		t.Fatalf("ParseMountinfo() = %v, %v", parsed, err)
	}
	if _, err := ParseMountinfo("bad line\n"); err == nil {
		t.Fatal("expected error for malformed mountinfo line")
	}
}

func TestAtomicWriteAndSafePath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "secret.txt")
	if err := AtomicWrite(target, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if !SafePath(target, true) {
		t.Fatalf("expected SafePath(%q, true) = true", target)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if SafePath(link, false) {
		t.Fatalf("expected SafePath(%q, false) = false for symlink", link)
	}
	var buf BoundedBuffer
	if _, err := buf.Write([]byte("ok")); err != nil || buf.String() != "ok" {
		t.Fatalf("BoundedBuffer unexpected: %q, %v", buf.String(), err)
	}
	if _, err := buf.Write([]byte(strings.Repeat("a", MaxOutputBytes))); err == nil {
		t.Fatal("expected BoundedBuffer overflow error")
	}
}
