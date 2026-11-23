package inspect

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const previousInstallDirectory = "/opt/tgcloud-clouddrive2"
const planningContainerFields = `{"container":` + containerFields + `,"image":{{json .Image}},"reference":{{json .Config.Image}},"running":{{json .State.Running}}}`
const runningProgramProbe = `from app import __version__; from app.deployment_check import code_fingerprint; import json; print(json.dumps({"version":__version__,"fingerprint":code_fingerprint()}))`

var (
	planVersion      = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	declaredVersion  = regexp.MustCompile(`(?m)^VERSION[ \t]*=[ \t]*(?:"([0-9.]+)"|'([0-9.]+)')[ \t]*(?:#[^\r\n]*)?\r?$`)
	foreignGateway   = regexp.MustCompile(`(^|/)clouddrive2([:@]|$)`)
	composeImageLine = regexp.MustCompile(`(?m)^    image: ([^\r\n]+)`)
)

type planningContainer struct {
	Container Container
	Image     string
	Reference string
	Running   *bool
}

type installInventory struct {
	bot, gateway, foreign bool
}

func DiscoverInstall(directory string) (InstallInstance, error) {
	instance, _, err := inspectInstall(directory)
	return instance, err
}

func inspectInstall(requested string) (InstallInstance, installInventory, error) {
	native, err := DockerPreflight(true)
	if err != nil {
		return InstallInstance{}, installInventory{}, err
	}
	return discoverInstall(requested, native, RunDocker)
}

func discoverInstall(requested string, native *PreflightResult, docker DockerRunner) (InstallInstance, installInventory, error) {
	defaultDir := requested
	if defaultDir == "" {
		defaultDir = InstallDirectory
	}
	var inventory installInventory
	if err := checkPlanningDirectory(defaultDir); err != nil {
		return InstallInstance{}, inventory, err
	}
	if native != nil {
		data, err := docker("ps", "--all", "--format", "{{.Names}} {{.Image}}")
		if err != nil {
			return InstallInstance{}, inventory, errors.New("Docker 容器列表无法检查，不把它当作未安装")
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			name, image, _ := strings.Cut(line, " ")
			switch name {
			case botName:
				inventory.bot = true
			case gatewayName:
				inventory.gateway = true
			default:
				inventory.foreign = inventory.foreign || strings.Contains(strings.ToLower(name), "clouddrive2") || foreignGateway.MatchString(strings.ToLower(image))
			}
		}
	}
	if !inventory.bot && !inventory.gateway {
		if defaultDir == InstallDirectory {
			if err := checkPlanningDirectory(previousInstallDirectory); err != nil || !emptyInstallDirectory(previousInstallDirectory) {
				return InstallInstance{}, inventory, errors.New("发现其他目录中的 TGCloud 数据，停止新建实例，请核对原目录后显式指定 --install-dir")
			}
		}
		if !emptyInstallDirectory(defaultDir) {
			return InstallInstance{}, inventory, errors.New("安装目录有残留但缺少容器，不能当作首次安装覆盖")
		}
		return InstallInstance{Directory: defaultDir}, inventory, nil
	}
	if !inventory.bot || !inventory.gateway {
		return InstallInstance{}, inventory, errors.New("仅检测到部分容器，请先核对实例")
	}
	instance, err := describeInstalledPair(requested, native.Architecture, docker)
	return instance, inventory, err
}

func checkPlanningDirectory(directory string) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	for path := directory; ; path = filepath.Dir(path) {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return errors.New("安装目录或上层路径经过链接或无法核实")
		}
		return nil
	}
}

func emptyInstallDirectory(path string) bool {
	entries, err := os.ReadDir(path)
	return errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0)
}

func describeInstalledPair(requested, architecture string, docker DockerRunner) (InstallInstance, error) {
	data, err := docker("container", "inspect", "--format", planningContainerFields, botName, gatewayName)
	if err != nil {
		return InstallInstance{}, errors.New("容器检查失败，不把不可读的容器当作未安装")
	}
	containers := make(map[string]planningContainer, 2)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var item planningContainer
		if json.Unmarshal([]byte(line), &item) != nil || item.Running == nil || !containerID.MatchString(item.Container.ID) || !imageID.MatchString(item.Image) {
			return InstallInstance{}, errors.New("容器身份或镜像无法核实")
		}
		containers[item.Container.Name] = item
	}
	bot, gateway := containers["/"+botName], containers["/"+gatewayName]
	if bot.Container.Mode == "compose" || gateway.Container.Mode == "compose" {
		return InstallInstance{}, errors.New("检测到独立 Docker Compose 部署，请在其部署目录维护")
	}
	directory := bot.Container.Directory
	if err := checkPlanningDirectory(directory); err != nil {
		return InstallInstance{}, err
	}
	if requested != "" && directory != requested {
		return InstallInstance{}, errors.New("指定目录与容器实际目录不一致")
	}
	for _, marker := range []string{"compose-bundle.json", "tgcloud-compose.json"} {
		if _, err := os.Lstat(filepath.Join(directory, marker)); !errors.Is(err, os.ErrNotExist) {
			return InstallInstance{}, errors.New("无法确认实例目录的一键安装归属，请核对独立 Compose 标记")
		}
	}
	if bot.Container.Project == "" || bot.Container.Project != gateway.Container.Project || directory != gateway.Container.Directory ||
		bot.Container.Service != botName || gateway.Container.Service != gatewayService ||
		!hasMounts(bot.Container, directory, map[string]string{"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs"}) ||
		!hasMounts(gateway.Container, directory, map[string]string{"/Config": "clouddrive/config", "/CloudNAS": "clouddrive/mounts"}) {
		return InstallInstance{}, errInstanceOwner
	}
	return readInstalledPlanInstance(directory, architecture, bot, gateway, docker)
}

func readInstalledPlanInstance(directory, architecture string, bot, gateway planningContainer, docker DockerRunner) (InstallInstance, error) {
	marker, err := verifyDeploymentSource(directory, "ready")
	if err != nil {
		return InstallInstance{}, errors.New("已安装程序或来源记录已变化，停止并重新预检")
	}
	if !matchesPlanVersion(filepath.Join(directory, "app/tgcloud/version.py"), marker.Version) {
		return InstallInstance{}, errors.New("实例版本与源码标记不一致")
	}
	for _, name := range []string{"app", "data", "config", "downloads", "logs", "clouddrive/config", "clouddrive/mounts"} {
		path := filepath.Join(directory, name)
		resolved, err := filepath.EvalSymlinks(path)
		info, statErr := os.Stat(path)
		if err != nil || resolved != path || statErr != nil || !info.IsDir() {
			return InstallInstance{}, errors.New("持久化或程序目录缺失或经过链接")
		}
	}
	raw, err := readInstallConfig(filepath.Join(directory, ".env"))
	if err != nil || bytes.ContainsRune(raw, '\r') {
		return InstallInstance{}, errors.New("无法核实现有私有 .env，或配置含回车，已停止")
	}
	config, err := parseDeploymentConfig(raw)
	if err != nil {
		return InstallInstance{}, err
	}
	if deploy, exists := config["DEPLOY_CLOUDDRIVE2"]; config["TGCLOUD_STORAGE_BACKEND"] != gatewayService || (exists && deploy != "true") {
		return InstallInstance{}, errors.New("实例不是一键安装的受管 CloudDrive2 网关部署")
	}
	var compose struct {
		Services map[string]struct {
			ContainerName string `json:"container_name"`
		}
	}
	output, err := docker(standaloneComposeArgs(directory, "config", "--format", "json")...)
	if err != nil || json.Unmarshal(output, &compose) != nil || compose.Services[botName].ContainerName != botName ||
		compose.Services[gatewayService].ContainerName != gatewayName {
		return InstallInstance{}, errors.New("现有 Compose 配置无法核实，未显示配置内容")
	}
	if !imageIsNative(bot.Image, architecture, docker) || !imageIsNative(gateway.Image, architecture, docker) || !imageReference.MatchString(gateway.Reference) {
		return InstallInstance{}, errors.New("现有容器镜像或原生平台无法核实")
	}
	running := *bot.Running
	if running {
		var observed struct{ Version, Fingerprint string }
		output, err := docker("exec", bot.Container.ID, "python", "-c", runningProgramProbe)
		if err != nil || json.Unmarshal(output, &observed) != nil || observed.Version != marker.Version || observed.Fingerprint != marker.codeFingerprint() {
			return InstallInstance{}, errors.New("无法核实运行中的 Bot 版本或代码，已停止")
		}
	}
	return InstallInstance{Directory: directory, Present: true, Version: marker.Version, Running: running, GatewayImage: gateway.Reference,
		ConfigDigest: sha256Text(raw), SourceDigest: marker.SourceSHA256}, nil
}

func VerifyInstallPlan(expected InstallInstance, source, targetVersion, targetDigest string) (string, error) {
	actual, inventory, err := inspectInstall(expected.Directory)
	if err != nil {
		return "", err
	}
	if actual.Present != expected.Present || actual.Version != expected.Version || actual.ConfigDigest != expected.ConfigDigest ||
		actual.SourceDigest != expected.SourceDigest || actual.GatewayImage != expected.GatewayImage {
		return "", errors.New("确认期间实例配置或来源已变更，停止并重新预检")
	}
	action, err := chooseInstallAction(actual, targetVersion, targetDigest)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(source) || filepath.Clean(source) != source ||
		!matchesPlanVersion(filepath.Join(source, "src/tgcloud/version.py"), targetVersion) {
		return "", errors.New("目标源码版本与本次计划不一致，停止并重新预检")
	}
	if (action == "install" || action == "upgrade") && inventory.foreign {
		return "", errors.New("存在其他 CloudDrive2 容器，请人工核对归属")
	}
	if action == "upgrade" {
		data, err := readSourceFile(filepath.Join(source, "packaging/compose/docker-compose.clouddrive2.yml"), 2*1024*1024)
		images := composeImageLine.FindAllSubmatch(data, -1)
		if err != nil || len(images) != 1 || !pinnedImage.Match(images[0][1]) {
			return "", errors.New("无法核实目标源码的 CloudDrive2 固定镜像")
		}
		if string(images[0][1]) != actual.GatewayImage {
			return "", errors.New("云网关镜像变化需要人工迁移验收，已停止自动升级")
		}
	}
	return action, nil
}

func matchesPlanVersion(path, version string) bool {
	data, err := readSourceFile(path, 2*1024*1024)
	if err != nil || !planVersion.MatchString(version) {
		return false
	}
	declared := declaredVersion.FindAllSubmatch(data, -1)
	return len(declared) == 1 && string(declared[0][1])+string(declared[0][2]) == version
}

func chooseInstallAction(instance InstallInstance, target, digest string) (string, error) {
	if !planVersion.MatchString(target) || !sourceHash.MatchString(digest) {
		return "", errors.New("目标版本或源码摘要无效")
	}
	if !instance.Present {
		return "install", nil
	}
	if !planVersion.MatchString(instance.Version) {
		return "", errors.New("已安装版本号无效")
	}
	if instance.Version == target {
		if instance.SourceDigest == digest {
			return "current", nil
		}
		return "upgrade", nil
	}
	currentParts, targetParts := strings.Split(instance.Version, "."), strings.Split(target, ".")
	for index, current := range currentParts {
		next := targetParts[index]
		if current == next {
			continue
		}
		if len(current) > len(next) || (len(current) == len(next) && current > next) {
			return "newer", nil
		}
		break
	}
	if currentParts[0] != targetParts[0] {
		return "", errors.New("跨主版本升级需要人工核对迁移说明")
	}
	return "upgrade", nil
}
