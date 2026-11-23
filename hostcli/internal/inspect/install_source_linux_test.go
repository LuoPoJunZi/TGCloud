package inspect

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallSourcePreservesPythonDigestAndDeploysExactFiles(t *testing.T) {
	root, private := t.TempDir(), t.TempDir()
	source, payload := filepath.Join(private, "source"), filepath.Join(private, "payload")
	mapping := map[string]string{
		"app/main.py":            "program/main.py",
		"app/tgcloud/version.py": "program/version.py",
		"app/tgcloud/代码&😀.py":    "program/代码&😀.py",
		"Dockerfile":             "Dockerfile",
		"docker-compose.yml":     "docker-compose.yml",
		"manage.sh":              "manage.sh",
	}
	var selected sourceSelection
	for _, original := range mapping {
		selected.Files = append(selected.Files, original)
		file := filepath.Join(root, original)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(original+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(args ...string) (any, error) {
		t.Helper()
		input, err := json.Marshal(selected)
		if err != nil {
			t.Fatal(err)
		}
		return InstallSource(args, bytes.NewReader(input))
	}
	// Produced by Python local_source.digest using the same file bytes and names.
	const expected = "ace6c6fafcca98c300cb18c67922fb3c0060a02f178a4b04f85438f7d614cb65"
	if result, err := request("snapshot", root, source); err != nil || result != expected {
		t.Fatalf("snapshot = %v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(source, ".env")); !os.IsNotExist(err) {
		t.Fatalf("private input appeared in snapshot: %v", err)
	}
	selected.Payload = mapping
	if _, err := request("payload", source, payload, expected); err != nil {
		t.Fatal(err)
	}
	for target, name := range mapping {
		path := filepath.Join(payload, target)
		if body, err := os.ReadFile(path); err != nil || string(body) != name+"\n" {
			t.Fatalf("payload %s = %q, %v", target, body, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0644 {
			t.Fatalf("payload %s is not readable by the Bot: %v", target, err)
		}
	}
	if info, err := os.Stat(filepath.Join(payload, "app/tgcloud")); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("payload subdirectory is not traversable by the Bot: %v", err)
	}

	// The public wizard can change just the Compose network before marking.
	compose := []byte("updated network\n")
	if err := os.WriteFile(filepath.Join(payload, "docker-compose.yml"), compose, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSource([]string{"mark", payload, "0.0.6", expected}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	marker, err := verifyPendingSource(payload)
	if err != nil || marker.SourceSHA256 != expected || marker.Files["docker-compose.yml"] != sha256Text(compose) {
		t.Fatalf("pending marker incompatible with deployment verification: %v", err)
	}

	if err := os.WriteFile(filepath.Join(source, "program/main.py"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := request("verify", source, expected); err == nil {
		t.Fatal("modified snapshot passed verification")
	}
	rejected := filepath.Join(private, "rejected")
	if _, err := request("payload", source, rejected, expected); err == nil {
		t.Fatal("payload was produced from modified source")
	}
	if _, err := os.Stat(rejected); !os.IsNotExist(err) {
		t.Fatalf("failed payload left a destination: %v", err)
	}
}
