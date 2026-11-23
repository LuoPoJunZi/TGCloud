package inspect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func CheckInstallHost(directory, config string, progress io.Writer) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	if _, err := DockerPreflight(false); err != nil {
		return err
	}
	compose, err := os.Lstat(filepath.Join(directory, "docker-compose.yml"))
	installed := err == nil && compose.Mode().IsRegular()
	if !installed && !errors.Is(err, os.ErrNotExist) {
		return errors.New("无法核实安装目录中的 Compose 文件")
	}
	gateway, err := checkInstallContainers(directory, installed, RunDocker)
	if err != nil {
		return err
	}
	if err := checkInstallPort(gateway, RunDocker, func() (string, error) {
		return resourceCommand("ss", "-H", "-ltn", "( sport = :19798 )")
	}); err != nil {
		return err
	}
	output, err := RunDocker("info", "--format", "{{.DockerRootDir}}")
	dockerRoot := strings.TrimSuffix(string(output), "\n")
	if err != nil || !filepath.IsAbs(dockerRoot) || strings.ContainsAny(dockerRoot, "\r\n\x00") {
		return errors.New("无法核实 Docker 数据目录")
	}
	installDisk, err := filesystem(directory)
	if err != nil {
		return errors.New("无法复核安装文件系统可用空间")
	}
	dockerDisk, err := filesystem(dockerRoot)
	if err != nil {
		return errors.New("无法复核 Docker 数据文件系统可用空间")
	}
	return checkInstallDisk(installDisk, dockerDisk, installed, func() ([]byte, error) {
		return readInstallConfig(config)
	}, progress)
}

func checkInstallContainers(directory string, installed bool, docker DockerRunner) (*gatewayContainer, error) {
	bot, err := findGatewayContainer(directory, botName, docker)
	if err != nil || (bot != nil && !installed) {
		return nil, fmt.Errorf("目标容器名 %s 的归属无法核实，请检查已有部署", botName)
	}
	gateway, err := findGatewayContainer(directory, gatewayName, docker)
	if err != nil || (gateway != nil && !installed) ||
		(bot != nil && gateway != nil && bot.Container.Project != gateway.Container.Project) {
		return nil, fmt.Errorf("目标容器名 %s 的归属无法核实，请检查已有部署", gatewayName)
	}
	return gateway, nil
}

func checkInstallPort(gateway *gatewayContainer, docker DockerRunner, listeners func() (string, error)) error {
	published, err := checkGatewayPort(gateway, docker)
	if err != nil || published {
		return err
	}
	output, err := listeners()
	if err != nil {
		return errors.New("无法核实宿主固定端口 19798 的监听状态")
	}
	if output != "" {
		return errors.New("固定端口 19798 已被其他进程占用，请核对宿主监听")
	}
	return nil
}

func checkInstallDisk(install, docker diskResources, installed bool, readConfig func() ([]byte, error), progress io.Writer) error {
	if install.available/1024 < minimumInstallFreeKiB {
		return errors.New("安装 Docker 后安装文件系统可用空间不足 8GB，停止部署")
	}
	if install.device == docker.device {
		return nil
	}
	config, err := readConfig()
	if err != nil {
		return err
	}
	minimum, withoutGateway := uint64(6_000_000), uint64(4_000_000)
	if installed {
		minimum, withoutGateway = 3_000_000, 2_000_000
	}
	for _, line := range strings.Split(string(config), "\n") {
		key, value, _ := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if key == "DEPLOY_CLOUDDRIVE2" {
			if value != "" && value != "true" {
				minimum = withoutGateway
			}
			break
		}
	}
	available := docker.available / 1024
	fmt.Fprintf(progress, "[TGCloud] Docker 数据目录位于另一文件系统，可用约 %dMB\n", available/1024)
	if available < minimum {
		return errors.New("Docker 数据文件系统空间不足，无法安全构建和启动容器")
	}
	return nil
}
