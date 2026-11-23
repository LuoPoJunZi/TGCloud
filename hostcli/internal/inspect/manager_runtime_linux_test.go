package inspect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerSafePathRejectsStickyTargetFileAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tgcloud")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if !managerSafePath(target, false) {
		t.Fatalf("expected private regular file %s to be safe", target)
	}
	if err := os.Chmod(target, os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	if managerSafePath(target, false) {
		t.Fatalf("managerSafePath accepted a world-writable sticky regular file: %s", target)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if managerSafePath(link, false) {
		t.Fatalf("managerSafePath accepted a symlink: %s", link)
	}
}

func TestVerifyManagerBundleRejectsDirectoryHelper(t *testing.T) {
	bundle := t.TempDir()
	files := map[string]string{
		"scripts/run_source.py":              "# run\n",
		"src/tgcloud/__init__.py":            "",
		"src/tgcloud/version.py":             "VERSION = '0.0.6'\n",
		"src/tgcloud/config.py":              "",
		"src/tgcloud/cli/management.py":      "",
		"src/tgcloud/deployment/runtime.py":  "",
	}
	hashes := make(map[string]string, len(files))
	for name, body := range files {
		path := filepath.Join(bundle, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		hashes[name] = sha256Text([]byte(body))
	}
	manifest := managerManifest{
		Schema:   1,
		Owner:    "tgcloud.cli",
		Version:  "0.0.6",
		Origin:   map[string]string{"kind": "local-source", "project": "tgcloud", "source_sha256": strings.Repeat("a", 64)},
		Files:    hashes,
		ToolHash: strings.Repeat("b", 64),
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, managerMarker), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(bundle, managerHelper), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyManagerBundle(bundle); err == nil {
		t.Fatal("verifyManagerBundle accepted a directory at tgcloud-host")
	}
}
