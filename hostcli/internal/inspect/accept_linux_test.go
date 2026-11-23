package inspect

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBotAcceptanceActivatesOnlyVerifiedInstall(t *testing.T) {
	for _, scenario := range []struct {
		name, failure string
	}{
		{"accepted", ""},
		{"changed container config", "TGCLOUD_CONFIG=MISMATCH_KEYS:BOT_TOKEN_B64"},
		{"unavailable runtime probe", "TGCLOUD_CONFIG=UNKNOWN"},
		{"changed container code", "TGCLOUD_CODE=MISMATCH"},
		{"changed installed file", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// The installation path used by the inspector must have no symlink components.
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			marker := sourceMarker{Schema: 1, Owner: "tgcloud.local-source", Project: "tgcloud", Mode: "shell",
				Edition: "clouddrive2", State: "pending", Version: "0.0.6", SourceSHA256: strings.Repeat("f", 64),
				Files: map[string]string{}}
			for _, name := range []string{"app/main.py", "app/tgcloud/version.py", "Dockerfile", "docker-compose.yml", "manage.sh"} {
				path := filepath.Join(directory, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
				marker.Files[name] = sha256Text([]byte(name))
			}
			original, err := json.Marshal(marker)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, sourceMarkerName)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if scenario.name == "changed installed file" {
				if err := os.WriteFile(filepath.Join(directory, "manage.sh"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			const credential = "synthetic credential"
			id := strings.Repeat("a", 64)
			docker := func(args ...string) ([]byte, error) {
				switch args[0] {
				case "container":
					bot := Container{ID: id, Name: "/" + botName, State: "running", Health: "healthy",
						Project: "tgcloud", Directory: directory, Service: botName}
					for _, name := range []string{"data", "downloads", "logs", "config"} {
						bot.Mounts = append(bot.Mounts, mount{Type: "bind", Source: filepath.Join(directory, name), Destination: "/" + name})
					}
					return json.Marshal(map[string]any{"container": bot, "image": "sha256:" + id})
				case "image":
					return []byte(`{"os":"linux","arch":"amd64"}`), nil
				case "compose":
					return json.Marshal(map[string]any{"services": map[string]any{botName: map[string]any{
						"environment": map[string]string{"BOT_TOKEN_B64": credential},
					}}})
				case "exec":
					if scenario.name == "unavailable runtime probe" {
						return nil, errors.New("本机 Docker 操作超时")
					}
					code, value := marker.codeFingerprint(), credential
					if scenario.name == "changed container code" {
						code = "other code"
					}
					if scenario.name == "changed container config" {
						value = "other credential"
					}
					return json.Marshal(map[string]any{"version": marker.Version, "code": code,
						"environment": map[string]string{"BOT_TOKEN_B64": sha256Text([]byte(value))}})
				default:
					t.Fatalf("unexpected Docker command: %s", args[0])
					return nil, nil
				}
			}
			var output bytes.Buffer
			err = acceptBot(directory, false, "amd64", &output, docker)
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(output.String(), credential) {
				t.Fatal("printed a configuration value")
			}
			if scenario.name == "accepted" {
				if err != nil || !bytes.Contains(data, []byte(`"state":"ready"`)) ||
					!strings.Contains(output.String(), "TGCLOUD_CODE=OK") {
					t.Fatalf("acceptance failed: %v, output %q", err, output.String())
				}
			} else if err == nil || !bytes.Equal(data, original) || !strings.Contains(output.String(), scenario.failure) ||
				strings.Contains(output.String(), "TGCLOUD_CODE=OK") {
				t.Fatalf("failure did not leave installation pending: %v, output %q", err, output.String())
			}
		})
	}
}
