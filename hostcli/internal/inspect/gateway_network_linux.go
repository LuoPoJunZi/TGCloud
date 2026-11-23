package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const gatewayNetworkJournal = ".gateway-network"

type gatewayNetworkChange struct {
	GatewayID, NetworkID string
}

func gatewayJournalPath(directory, stage string) (string, error) {
	if stage == directory || !candidateStage.MatchString(stage) || checkUpgradeDirectory(stage) != nil {
		return "", errors.New("CloudDrive2 网络记录的暂存目录无效")
	}
	return filepath.Join(stage, gatewayNetworkJournal), nil
}

func RepairGatewayNetwork(directory, stage string, progress io.Writer) error {
	if err := checkGatewayDeployment(directory); err != nil {
		return err
	}
	journal, err := gatewayJournalPath(directory, stage)
	if err != nil {
		return err
	}
	if _, err := DockerPreflight(false); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return repairGatewayNetwork(directory, journal, progress, RunDocker, func(bot, host string, expected ...string) (net.IP, error) {
		return waitGateway(ctx, client, bot, host, expected, RunDocker)
	})
}

func repairGatewayNetwork(directory, journal string, progress io.Writer, docker DockerRunner,
	probe func(bot, host string, expected ...string) (net.IP, error)) error {
	gateway, err := findGatewayContainer(directory, gatewayName, docker)
	if err != nil {
		return err
	}
	bot, err := findGatewayContainer(directory, botName, docker)
	if err != nil {
		return err
	}
	if gateway == nil || bot == nil || gateway.Container.State != "running" || bot.Container.State != "running" ||
		gateway.Container.Project != bot.Container.Project {
		return errors.New("CloudDrive2 和 Bot 未同时运行在当前 TGCloud 项目中")
	}
	if _, err := checkGatewayPort(gateway, docker); err != nil {
		return err
	}

	host, expected := "127.0.0.1", []string{"127.0.0.1"}
	if gateway.NetworkMode == "host" || strings.HasPrefix(gateway.NetworkMode, "container:") {
		if gateway.NetworkMode != bot.NetworkMode && bot.NetworkMode != "container:"+gateway.Container.ID {
			return errors.New("CloudDrive2 与 Bot 的网络命名空间不一致，请核对现有网络配置")
		}
	} else {
		endpoint, err := connectGatewayBridge(directory, journal, bot, gateway, progress, docker)
		if err != nil {
			return err
		}
		host, expected = gatewayName, []string{endpoint.IPAddress, endpoint.GlobalIPv6Address}
	}

	fmt.Fprintln(progress, "[TGCloud-REPAIR] 等待 CloudDrive2 管理端口和 Bot 内部 DNS/TCP 连通")
	ip, err := probe(bot.Container.ID, host, expected...)
	if err != nil {
		return err
	}
	fmt.Fprintf(progress, "[TGCloud-REPAIR] Bot 已解析并连通 CloudDrive2：%s\n", net.JoinHostPort(ip.String(), "19798"))
	fmt.Fprintf(progress, "TGCLOUD_REPAIR_CONTAINER=%s\nTGCLOUD_REPAIR=SUCCESS\n", gatewayName)
	return nil
}

func connectGatewayBridge(directory, journal string, bot, gateway *gatewayContainer, progress io.Writer,
	docker DockerRunner) (gatewayEndpoint, error) {
	networkID := bot.Networks[gatewayNetwork].NetworkID
	if !containerID.MatchString(networkID) {
		return gatewayEndpoint{}, errors.New("Bot 未加入受管 CloudDrive2 网桥")
	}
	const networkFields = `{"name":{{json .Name}},"id":{{json .ID}},"driver":{{json .Driver}},"labels":{{json .Labels}}}`
	output, err := docker("network", "inspect", "--format", networkFields, networkID)
	var network struct {
		Name, ID, Driver string
		Labels           map[string]string
	}
	if err != nil || json.Unmarshal(output, &network) != nil || network.ID != networkID || network.Name != gatewayNetwork ||
		network.Driver != "bridge" || (network.Labels["com.docker.compose.project"] != "" &&
		network.Labels["com.docker.compose.project"] != bot.Container.Project) {
		return gatewayEndpoint{}, errors.New("无法核实 CloudDrive2 受管网桥")
	}
	if endpoint, exists := gateway.Networks[gatewayNetwork]; exists {
		if endpoint.NetworkID != networkID {
			return gatewayEndpoint{}, errors.New("CloudDrive2 接入的网桥与 Bot 不一致")
		}
		return endpoint, nil
	}
	change := gatewayNetworkChange{GatewayID: gateway.Container.ID, NetworkID: networkID}
	if err := writeGatewayChange(journal, change); err != nil {
		return gatewayEndpoint{}, err
	}
	fmt.Fprintln(progress, "[TGCloud-REPAIR] 为 CloudDrive2 补接 TGCloud 受管网桥，保留原网络连接")
	if _, err := docker("network", "connect", "--alias", gatewayName, networkID, change.GatewayID); err != nil {
		return gatewayEndpoint{}, errors.New("CloudDrive2 无法加入 TGCloud 受管网桥")
	}
	current, err := findGatewayContainer(directory, gatewayName, docker)
	if err != nil || current == nil || current.Container.ID != change.GatewayID || current.Container.State != "running" ||
		current.Networks[gatewayNetwork].NetworkID != networkID {
		return gatewayEndpoint{}, errors.New("无法核实 CloudDrive2 新增的网桥连接")
	}
	return current.Networks[gatewayNetwork], nil
}

func writeGatewayChange(journal string, change gatewayNetworkChange) error {
	file, err := os.OpenFile(journal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("无法记录 CloudDrive2 网络变更")
	}
	err = json.NewEncoder(file).Encode(change)
	if closeErr := file.Close(); err != nil || closeErr != nil {
		// No Docker call has been issued yet.
		_ = os.Remove(journal)
		return errors.New("无法保存 CloudDrive2 网络变更记录")
	}
	return nil
}

func RestoreGatewayNetwork(directory, stage string) error {
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	journal, err := gatewayJournalPath(directory, stage)
	if err != nil {
		return err
	}
	if _, err := DockerPreflight(false); err != nil {
		return err
	}
	return restoreGatewayNetwork(directory, journal, RunDocker)
}

func restoreGatewayNetwork(directory, journal string, docker DockerRunner) error {
	data, err := readSourceFile(journal, 1024)
	var change gatewayNetworkChange
	if err != nil || json.Unmarshal(data, &change) != nil || !containerID.MatchString(change.GatewayID) || !containerID.MatchString(change.NetworkID) {
		return errors.New("无法读取 CloudDrive2 网络变更记录")
	}
	gateway, err := findGatewayContainer(directory, gatewayName, docker)
	if err != nil || gateway == nil || gateway.Container.ID != change.GatewayID {
		return errors.New("CloudDrive2 容器身份已变化，未撤销网络连接")
	}
	endpoint, exists := gateway.Networks[gatewayNetwork]
	if !exists {
		return nil
	}
	if endpoint.NetworkID != change.NetworkID {
		return errors.New("CloudDrive2 网桥身份已变化，未撤销网络连接")
	}
	if _, err := docker("network", "disconnect", change.NetworkID, change.GatewayID); err != nil {
		return errors.New("无法撤销本次新增的 CloudDrive2 网桥连接")
	}
	return nil
}

const gatewaySocketProbe = `import socket, sys
with socket.create_connection((sys.argv[1], 19798), timeout=5) as connection:
    print(connection.getpeername()[0])`

func waitGateway(ctx context.Context, client *http.Client, bot, host string, expected []string, docker DockerRunner) (net.IP, error) {
	for ctx.Err() == nil {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:19798/", nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode < 400 {
				output, err := docker("exec", bot, "python", "-B", "-c", gatewaySocketProbe, host)
				if err == nil {
					ip := net.ParseIP(strings.TrimSpace(string(output)))
					for _, address := range expected {
						if ip != nil && ip.Equal(net.ParseIP(address)) {
							return ip, nil
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("CloudDrive2 管理端口或 Bot 到 %s:19798 的连通检查未通过", host)
}
