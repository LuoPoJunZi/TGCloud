package inspect

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResumeUpgradeBotPreservesOriginalContainerAndState(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name                string
		wasRunning, running bool
		replaced, wantStart bool
	}{
		{name: "previously stopped"},
		{name: "still running", wasRunning: true, running: true},
		{name: "stopped during upgrade", wasRunning: true, wantStart: true},
		{name: "name reused", wasRunning: true, replaced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := upgradeBot{Directory: t.TempDir(), ID: id, Image: "sha256:" + id, Tag: "tgcloud:old", Running: tc.wasRunning}
			current := gatewayContainer{
				Container: Container{ID: id, Name: "/" + botName, Directory: old.Directory, Project: "tgcloud", Service: botName},
				Image:     old.Image,
			}
			if tc.replaced {
				current.Container.ID = strings.Repeat("b", 64)
			}
			for _, dir := range []string{"data", "config", "downloads", "logs"} {
				current.Container.Mounts = append(current.Container.Mounts, mount{
					Type: "bind", Source: filepath.Join(old.Directory, dir), Destination: "/" + dir,
				})
			}
			started := false
			docker := func(args ...string) ([]byte, error) {
				if !tc.wasRunning {
					t.Fatalf("Docker called for an originally stopped Bot: %v", args)
				}
				switch args[0] + " " + args[1] {
				case "container ls":
					return []byte(current.Container.ID), nil
				case "container inspect":
					return json.Marshal(current)
				case "inspect --format":
					if strings.Contains(args[2], ".Config.Image") {
						return []byte(old.Image + "|" + old.Tag + "|" + strconv.FormatBool(tc.running)), nil
					}
					return []byte("healthy"), nil
				case "start " + id:
					started = true
					return nil, nil
				default:
					t.Fatalf("unexpected Docker command: %v", args)
					return nil, nil
				}
			}
			err := resumeUpgradeBot(&old, io.Discard, docker)
			if (err != nil) != tc.replaced || started != tc.wantStart {
				t.Fatalf("old container recovery: started=%v, err=%v", started, err)
			}
		})
	}
}
