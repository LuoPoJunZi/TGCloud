package inspect

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayRepairRestoresOnlyItsNewConnection(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(directory, gatewayNetworkJournal)
	gatewayID, botID, networkID := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	bot := gatewayContainer{
		Container: Container{ID: botID, Name: "/" + botName, State: "running", Project: "project", Directory: directory, Service: botName},
		Image:     "sha256:" + botID,
		Networks:  map[string]gatewayEndpoint{gatewayNetwork: {NetworkID: networkID}},
	}
	for _, name := range []string{"config", "downloads", "data", "logs"} {
		bot.Container.Mounts = append(bot.Container.Mounts, mount{Type: "bind", Source: filepath.Join(directory, name), Destination: "/" + name})
	}
	gateway := gatewayContainer{
		Container: Container{ID: gatewayID, Name: "/" + gatewayName, State: "running", Project: "project", Directory: directory, Service: gatewayService,
			Mounts: []mount{{Type: "bind", Source: filepath.Join(directory, "clouddrive/config"), Destination: "/Config"},
				{Type: "bind", Source: filepath.Join(directory, "clouddrive/mounts"), Destination: "/CloudNAS"}}},
		Image:    "sha256:" + gatewayID,
		Networks: map[string]gatewayEndpoint{"external": {IPAddress: "172.19.0.2"}},
	}
	var changes []string
	docker := func(args ...string) ([]byte, error) {
		switch args[0] + "/" + args[1] {
		case "container/ls":
			if args[5] == "name=^/"+botName+"$" {
				return []byte(botID), nil
			}
			return []byte(gatewayID), nil
		case "container/inspect":
			if args[4] == botID {
				return json.Marshal(bot)
			}
			return json.Marshal(gateway)
		case "network/inspect":
			return json.Marshal(map[string]string{"Name": gatewayNetwork, "ID": networkID, "Driver": "bridge"})
		case "network/connect":
			changes = append(changes, args[1])
			gateway.Networks[gatewayNetwork] = gatewayEndpoint{NetworkID: networkID, IPAddress: "172.18.0.2"}
		case "network/disconnect":
			if args[2] != networkID || args[3] != gatewayID {
				t.Fatalf("unexpected disconnect: %v", args)
			}
			changes = append(changes, args[1])
			delete(gateway.Networks, gatewayNetwork)
		case "ps/--format":
			return []byte(gatewayName + "|127.0.0.1:19798->19798/tcp"), nil
		default:
			t.Fatalf("unexpected Docker command: %v", args)
		}
		return nil, nil
	}
	probe := func(id, host string, expected ...string) (net.IP, error) {
		if id != botID || host != gatewayName || expected[0] != "172.18.0.2" {
			t.Fatalf("unexpected probe: %v %v %v", id, host, expected)
		}
		return nil, errors.New("fixture connectivity failure")
	}
	if err := repairGatewayNetwork(directory, journal, io.Discard, docker, probe); err == nil {
		t.Fatal("repair accepted an unreachable gateway")
	}
	if err := restoreGatewayNetwork(directory, journal, docker); err != nil {
		t.Fatal(err)
	}
	if strings.Join(changes, ",") != "connect,disconnect" || len(gateway.Networks) != 1 || gateway.Networks["external"].IPAddress != "172.19.0.2" {
		t.Fatalf("unrelated network changed: %v %v", changes, gateway.Networks)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	gateway.Networks[gatewayNetwork] = gatewayEndpoint{NetworkID: networkID, IPAddress: "172.18.0.2"}
	if err := repairGatewayNetwork(directory, journal, io.Discard, docker, func(string, string, ...string) (net.IP, error) {
		return net.ParseIP("172.18.0.2"), nil
	}); err != nil || len(changes) != 2 {
		t.Fatalf("existing connection was not reused: %v, %v", err, changes)
	}
}
