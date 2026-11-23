package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func TestLANEntryReusesAndRepairsOnlyProxy(t *testing.T) {
	previousOwner := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = previousOwner })
	root := t.TempDir()
	entry := lanEntry{filepath.Join(root, "lan"), filepath.Join(root, "https.json"), filepath.Join(root, "tgcloud"), "nginx:1.30.5-alpine"}
	const address = "192.168.26.5"
	id := strings.Repeat("a", 64)
	gatewayID := strings.Repeat("b", 64)
	proxyID := strings.Repeat("c", 64)
	var running, reachable, failStart bool
	var writes []string
	var bound []string
	bind := func(address string) error {
		bound = append(bound, address)
		return nil
	}
	probe := func(string) error {
		if !reachable {
			return errLanReachability
		}
		return nil
	}
	marker := sourceMarker{Schema: 1, Owner: "tgcloud.local-source", Project: "tgcloud", Mode: "shell",
		Edition: lanEdition, State: "ready", Version: "0.0.6", SourceSHA256: strings.Repeat("f", 64), Files: map[string]string{}}
	const value = "synthetic configuration"
	makeContainer := func(name, service, id string, paths map[string]string) Container {
		item := Container{ID: id, Name: "/" + name, Project: "tgcloud", Directory: entry.directory,
			Service: service, State: "running", Health: "healthy"}
		for destination, relative := range paths {
			item.Mounts = append(item.Mounts, mount{"bind", filepath.Join(entry.directory, relative), destination})
		}
		return item
	}
	bot := makeContainer(botName, botName, id, map[string]string{
		"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs",
	})
	gateway := makeContainer(gatewayName, gatewayService, gatewayID, map[string]string{
		"/CloudNAS": "clouddrive/mounts", "/Config": "clouddrive/config",
	})
	docker := func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "ps":
			return nil, nil
		case args[0] == "container" && args[1] == "ls":
			if strings.Contains(strings.Join(args, " "), lanName) {
				if !running {
					return nil, nil
				}
				return []byte(proxyID), nil
			}
			return []byte(botName + "\n" + gatewayName), nil
		case args[0] == "container" && args[len(args)-1] == proxyID:
			readonly := false
			return json.Marshal(lanContainer{ID: proxyID, Name: "/" + lanName, Image: "sha256:" + id, Reference: entry.image,
				Running: running, Network: "host", Readonly: true, Privileged: &readonly, User: "101:101",
				Labels: map[string]string{"com.tgcloud.managed": "true", "com.tgcloud.role": "lan-management",
					"com.tgcloud.edition": lanEdition, "com.docker.compose.project": lanName, "com.docker.compose.service": "lan",
					"com.docker.compose.project.working_dir":  entry.configDir(),
					"com.docker.compose.project.config_files": filepath.Join(entry.configDir(), "compose.json")},
				CapDrop: []string{"ALL"}, Security: []string{"no-new-privileges:true"}, Tmpfs: map[string]string{"/tmp": lanTmpfs},
				Entrypoint: []string{"nginx"}, Command: []string{"-g", "daemon off;"},
				Mounts: []lanMount{{"bind", filepath.Join(entry.configDir(), "nginx.conf"), "/etc/nginx/nginx.conf", &readonly}}})
		case args[0] == "container":
			first, _ := json.Marshal(bot)
			second, _ := json.Marshal(gateway)
			return append(append(first, '\n'), second...), nil
		case args[0] == "inspect" && args[len(args)-1] == gatewayID:
			return []byte(`{"19798/tcp":[{"HostIp":"127.0.0.1","HostPort":"19798"}]}`), nil
		case args[0] == "exec" && args[1] == id:
			return json.Marshal(map[string]any{"version": marker.Version, "code": marker.codeFingerprint(),
				"environment": map[string]string{"BOT_TOKEN_B64": sha256Text([]byte(value))}})
		case args[0] == "image":
			return []byte(`{"os":"linux","arch":"amd64"}`), nil
		case args[0] == "compose" && args[2] == entry.directory:
			return json.Marshal(map[string]any{"services": map[string]any{botName: map[string]any{
				"environment": map[string]string{"BOT_TOKEN_B64": value},
			}}})
		case args[0] == "compose" && args[2] == entry.configDir():
			action := args[len(entry.compose()):]
			writes = append(writes, strings.Join(action, " "))
			if action[0] == "up" {
				if failStart {
					return nil, errors.New("injected entry start failure")
				}
				running, reachable = true, true
			}
			return nil, nil
		default:
			t.Fatalf("unexpected Docker operation: %v", args)
			return nil, nil
		}
	}
	check := func(existing, activate bool) (lanObservation, error) {
		saved, err := entry.load()
		if err != nil {
			t.Fatal(err)
		}
		return entry.inspectAndCheck(context.Background(), lanObservation{Saved: saved}, address, existing, activate, "amd64", docker, bind, probe)
	}
	if result, err := check(false, false); err != nil || result.Saved != nil ||
		!reflect.DeepEqual(bound, []string{address, "127.0.0.1"}) || len(writes) != 0 {
		t.Fatalf("fresh read-only preflight: %+v, %v, bound=%v, writes=%v", result, err, bound, writes)
	}
	if err := entry.save(address); err != nil {
		t.Fatal(err)
	}
	if err := entry.save(address); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		path := filepath.Join(entry.directory, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range requiredSourceFiles {
		write(name, []byte(name))
		marker.Files[name] = sha256Text([]byte(name))
	}
	raw, _ := json.Marshal(marker)
	write(sourceMarkerName, raw)
	write(".env", []byte(value))
	write("data/tasks.db", []byte("existing task data"))
	failStart = true
	if _, err := check(true, true); err == nil || !strings.Contains(err.Error(), "单独修复入口") {
		t.Fatalf("entry startup error lost: %v", err)
	}
	failStart = false
	writes = nil
	if _, err := check(true, true); err != nil || !running || !reachable {
		t.Fatalf("repair on the existing base failed: %v", err)
	}
	expected := []string{"config --quiet", "pull lan", "run --rm --no-deps lan -t", "up -d --no-deps --pull never lan"}
	if !reflect.DeepEqual(writes, expected) {
		t.Fatalf("unexpected operations during repair: %v", writes)
	}
	writes = nil
	if result, err := check(true, true); err != nil || !result.Healthy || len(writes) != 0 {
		t.Fatalf("healthy entry was not reused: %+v, %v, writes=%v", result, err, writes)
	}
	reachable = false
	if _, err := check(true, true); err != nil || !reflect.DeepEqual(writes, append(expected[:3], "up -d --no-deps --pull never --force-recreate lan")) {
		t.Fatalf("unresponsive proxy was not repaired independently: %v, writes=%v", err, writes)
	}
	for name, expected := range map[string][]byte{".env": []byte(value), "data/tasks.db": []byte("existing task data"), sourceMarkerName: raw} {
		body, err := os.ReadFile(filepath.Join(entry.directory, name))
		if err != nil || !bytes.Equal(body, expected) {
			t.Fatalf("base file %s changed: %v", name, err)
		}
	}
}

func TestLANHTTPProbeChecksManagementAndDAV(t *testing.T) {
	var allowUppercaseDAV atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-TGCloud-LAN", lanEdition)
		switch {
		case r.URL.Path == "/" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/DAV/" && allowUppercaseDAV.Load():
			w.WriteHeader(http.StatusOK)
		case (r.URL.Path == "/dav" && r.Method == http.MethodGet) ||
			((r.URL.Path == "/dav/" || r.URL.Path == "/DAV/") && r.Method == "PROPFIND"):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	if err := probeLanHTTP(server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	allowUppercaseDAV.Store(true)
	if probeLanHTTP(server.Client(), server.URL) == nil {
		t.Fatal("an accessible uppercase DAV endpoint passed")
	}
}
