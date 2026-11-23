package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

const (
	gatewayService = "clouddrive2"
	gatewayNetwork = "tgcloud-clouddrive2-net"
	gatewayFields  = `{"container":` + containerFields + `,"image":{{json .Image}},` +
		`"network_mode":{{json .HostConfig.NetworkMode}},"networks":{{json .NetworkSettings.Networks}}}`
)

type gatewayEndpoint struct {
	NetworkID, IPAddress, GlobalIPv6Address string
}

type gatewayContainer struct {
	Container   Container
	Image       string
	NetworkMode string `json:"network_mode"`
	Networks    map[string]gatewayEndpoint
}

func checkGatewayDeployment(directory string) error {
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	if err := checkUpgradeFiles(directory); err != nil {
		return err
	}
	if _, err := standaloneVersion(directory, "pending"); err != nil {
		return errors.New("CloudDrive2 部署操作要求待激活的一键安装目录")
	}
	return nil
}

func StartGateway(directory string, progress io.Writer) error {
	if err := checkGatewayDeployment(directory); err != nil {
		return err
	}
	native, err := DockerPreflight(false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "modprobe", "fuse").Run()
	if info, err := os.Stat("/dev/fuse"); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("宿主没有提供 /dev/fuse，请启用 FUSE 后重试")
	}
	return startGateway(directory, native.Architecture, progress, func(args ...string) ([]byte, error) {
		if args[0] == "compose" {
			return runDockerWithTimeout(true, progress, 120*time.Second, args...)
		}
		return RunDocker(args...)
	})
}

func startGateway(directory, architecture string, progress io.Writer, docker DockerRunner) error {
	gateway, err := findGatewayContainer(directory, gatewayName, docker)
	if err != nil {
		return err
	}
	if _, err := checkGatewayPort(gateway, docker); err != nil {
		return err
	}
	if gateway != nil && !imageIsNative(gateway.Image, architecture, docker) {
		return errors.New("已有 CloudDrive2 镜像未通过原生架构核验")
	}
	if gateway != nil && gateway.Container.State == "running" {
		fmt.Fprintln(progress, "[TGCloud] 发现现有 CloudDrive2 容器 tgcloud-clouddrive2；保留登录和挂载数据")
		return nil
	}
	fmt.Fprintln(progress, "TGCLOUD_STAGE=GATEWAY")
	if gateway != nil {
		fmt.Fprintln(progress, "[TGCloud] 启动现有 CloudDrive2 容器，保留挂载和配置")
		_, err = docker("start", gateway.Container.ID)
	} else {
		fmt.Fprintln(progress, "[TGCloud] 使用已预检的镜像启动 CloudDrive2")
		_, err = docker(standaloneComposeArgs(directory, "up", "-d", "--no-deps", "--pull", "never", gatewayService)...)
	}
	if err != nil {
		return errors.New("CloudDrive2 容器启动失败")
	}
	current, err := findGatewayContainer(directory, gatewayName, docker)
	if err != nil || current == nil || current.Container.State != "running" ||
		(gateway != nil && current.Container.ID != gateway.Container.ID) || !imageIsNative(current.Image, architecture, docker) {
		return errors.New("无法核实启动后的 CloudDrive2 容器")
	}
	return nil
}

func findGatewayContainer(directory, name string, docker DockerRunner) (*gatewayContainer, error) {
	output, err := docker("container", "ls", "--all", "--no-trunc", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	if err != nil {
		return nil, errors.New("无法核实 CloudDrive2 部署的容器列表")
	}
	id := strings.TrimSpace(string(output))
	if id == "" {
		return nil, nil
	}
	if !containerID.MatchString(id) {
		return nil, errInstanceOwner
	}
	output, err = docker("container", "inspect", "--format", gatewayFields, id)
	var result gatewayContainer
	if err != nil || json.Unmarshal(output, &result) != nil || !imageID.MatchString(result.Image) {
		return nil, errors.New("无法读取 CloudDrive2 部署的容器身份")
	}
	item := result.Container
	service := gatewayService
	mounts := map[string]string{"/Config": "clouddrive/config", "/CloudNAS": "clouddrive/mounts"}
	if name == botName {
		service = botName
		mounts = map[string]string{"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs"}
	}
	if item.ID != id || item.Name != "/"+name || item.Directory != directory || item.Project == "" ||
		item.Service != service || item.Mode == "compose" || !hasMounts(item, directory, mounts) {
		return nil, errInstanceOwner
	}
	return &result, nil
}

func checkGatewayPort(gateway *gatewayContainer, docker DockerRunner) (bool, error) {
	output, err := docker("ps", "--format", "{{.Names}}|{{.Ports}}")
	if err != nil {
		return false, errors.New("无法核实 Docker 固定端口 19798")
	}
	published := false
	for _, line := range strings.Split(string(output), "\n") {
		name, ports, _ := strings.Cut(line, "|")
		if !publishesGatewayPort(ports) {
			continue
		}
		if name != gatewayName || gateway == nil || gateway.Container.State != "running" {
			return false, errors.New("固定端口 19798 已被其他容器占用，未修改现有容器")
		}
		published = true
	}
	return published, nil
}

func publishesGatewayPort(ports string) bool {
	const target = 19798
	for _, port := range strings.Split(ports, ",") {
		first, last, ok := hostutil.ParseHostPortRange(port, false)
		if ok && first <= target && target <= last {
			return true
		}
	}
	return false
}
