package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	InstallDirectory = "/opt/tgcloud"
	BotName          = "tgcloud-clouddrive2-bot"
	GatewayName      = "tgcloud-clouddrive2"
	botName          = BotName
	gatewayName      = GatewayName
)

// The format intentionally excludes the container environment and health logs.
const ContainerFields = `{"id":{{json .ID}},"name":{{json .Name}},"state":{{json .State.Status}},` +
	`"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},` +
	`"project":{{json (index .Config.Labels "com.docker.compose.project")}},` +
	`"directory":{{json (index .Config.Labels "com.docker.compose.project.working_dir")}},` +
	`"service":{{json (index .Config.Labels "com.docker.compose.service")}},` +
	`"mode":{{json (index .Config.Labels "com.tgcloud.install-mode")}},"mounts":{{json .Mounts}}}`

const containerFields = ContainerFields

var (
	VersionPattern     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	ContainerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionPattern     = VersionPattern
	containerID        = ContainerIDPattern
	errInstanceOwner   = errors.New("容器归属与 /opt/tgcloud 一键安装实例不一致")
)

type Container struct {
	ID, Name, State, Health, Project, Directory, Service, Mode string
	Mounts                                                     []Mount
}

type Mount struct {
	Type, Source, Destination string
}

type mount = Mount

type Instance struct {
	Version      string
	Bot, Gateway Container
}

func Discover(directory string, docker DockerRunner) (*Instance, error) {
	output, err := docker("container", "ls", "--all", "--filter", "name="+gatewayName, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	var botFound, gatewayFound bool
	for _, name := range strings.Fields(string(output)) {
		botFound = botFound || name == botName
		gatewayFound = gatewayFound || name == gatewayName
	}
	if !botFound && !gatewayFound {
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
			return nil, nil
		}
		return nil, errors.New("未找到服务容器，安装目录需要核对")
	}
	if !botFound || !gatewayFound {
		return nil, errors.New("仅找到部分服务容器，请核对实例")
	}

	output, err = docker("container", "inspect", "--format", containerFields, botName, gatewayName)
	if err != nil {
		return nil, err
	}
	containers := make(map[string]Container, 2)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		var container Container
		if err := json.Unmarshal([]byte(line), &container); err != nil {
			return nil, errors.New("Docker 返回的容器状态无法解析")
		}
		containers[container.Name] = container
	}
	bot, gateway := containers["/"+botName], containers["/"+gatewayName]
	if bot.Mode == "compose" || gateway.Mode == "compose" {
		return nil, errors.New("检测到独立 Docker Compose 部署，请在其部署目录运行 bash manage.sh")
	}
	if !containerID.MatchString(bot.ID) || !containerID.MatchString(gateway.ID) ||
		bot.Project == "" || bot.Project != gateway.Project ||
		bot.Directory != directory || gateway.Directory != directory ||
		bot.Service != botName || gateway.Service != "clouddrive2" ||
		!hasMounts(bot, directory, map[string]string{"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs"}) ||
		!hasMounts(gateway, directory, map[string]string{"/Config": "clouddrive/config", "/CloudNAS": "clouddrive/mounts"}) {
		return nil, errInstanceOwner
	}

	version, err := installedVersion(directory)
	if err != nil {
		return nil, err
	}
	return &Instance{Version: version, Bot: bot, Gateway: gateway}, nil
}

func hasMounts(container Container, directory string, required map[string]string) bool {
	for destination, relative := range required {
		found := false
		for _, mount := range container.Mounts {
			if mount.Type == "bind" && mount.Destination == destination && mount.Source == filepath.Join(directory, filepath.FromSlash(relative)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func InstalledVersion(directory string) (string, error) {
	return installedVersion(directory)
}

func installedVersion(directory string) (string, error) {
	return standaloneVersion(directory, "ready")
}

func standaloneVersion(directory, state string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(directory); err != nil || resolved != directory {
		return "", errors.New("无法确认安装目录")
	}
	path := filepath.Join(directory, "tgcloud-source.json")
	invalidMarker := fmt.Errorf("%s 的 TGCloud 安装标记缺失或无效", directory)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256*1024 {
		return "", invalidMarker
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", invalidMarker
	}
	var marker struct {
		Schema                                        int
		Owner, Project, Mode, Edition, State, Version string
	}
	if err := json.Unmarshal(payload, &marker); err != nil || marker.Schema != 1 ||
		marker.Owner != "tgcloud.local-source" || marker.Project != "tgcloud" || marker.Mode != "shell" ||
		marker.Edition != "clouddrive2" || marker.State != state || !versionPattern.MatchString(marker.Version) {
		return "", invalidMarker
	}
	return marker.Version, nil
}
