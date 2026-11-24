package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The container provides facts using its existing settings and fingerprint code.
// Only hashes leave the container. Go compares them with the rendered Compose configuration.
const botAcceptanceProbe = `import hashlib, json, os
from tgcloud import __version__
from tgcloud.config import Settings
from tgcloud.deployment.fingerprints import code_fingerprint
Settings.from_env(create_directories=False)
print(json.dumps({"version": __version__, "code": code_fingerprint(), "environment": {
    key: hashlib.sha256(os.fsencode(value)).hexdigest() for key, value in os.environ.items()}}))`

const acceptanceContainerFields = `{"container":` + containerFields + `,"image":{{json .Image}}}`

var envKey = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func AcceptBot(directory string, checkGateway bool, progress io.Writer) error {
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	if err := checkUpgradeFiles(directory); err != nil {
		return err
	}
	native, err := DockerPreflight(false)
	if err != nil {
		return err
	}
	// CloudDrive2 network repair can recreate the Bot after the first health check.
	if waitBotHealth(RunDocker) != "healthy" {
		return errors.New("Bot 未通过最终健康检查")
	}
	docker := func(args ...string) ([]byte, error) {
		return runBotAcceptanceDocker(context.Background(), 20*time.Second, nil, args...)
	}
	return acceptBot(directory, checkGateway, native.Architecture, progress, docker)
}

func RunBotAcceptanceDocker(ctx context.Context, timeout time.Duration, environment []string, args ...string) ([]byte, error) {
	return runBotAcceptanceDocker(ctx, timeout, environment, args...)
}

func VerifyBotRuntime(directory, id string, marker SourceMarker, progress io.Writer, docker DockerRunner) error {
	return verifyBotRuntime(directory, id, marker, progress, docker)
}

func runBotAcceptanceDocker(ctx context.Context, timeout time.Duration, environment []string, args ...string) ([]byte, error) {
	// This probe imports Python settings and hashes the installed code in the container.
	if len(args) > 1 && args[0] == "exec" && args[len(args)-1] == botAcceptanceProbe {
		timeout = 60 * time.Second
	}
	return runDockerCommandContext(ctx, true, nil, timeout, environment, args...)
}

func acceptBot(directory string, checkGateway bool, architecture string, progress io.Writer, docker DockerRunner) error {
	marker, err := verifyPendingSource(directory)
	if err != nil {
		return err
	}
	bot, err := acceptanceContainer(botName, architecture, docker)
	if err != nil {
		return err
	}
	if bot.Health != "healthy" || bot.Project == "" || bot.Directory != directory || bot.Service != botName ||
		bot.Mode == "compose" || !hasMounts(bot, directory, map[string]string{
		"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs",
	}) {
		return errors.New("待验收的 Bot 未通过健康或一键安装归属检查")
	}
	if err := verifyBotRuntime(directory, bot.ID, marker, progress, docker); err != nil {
		return err
	}
	if checkGateway {
		if _, err := acceptanceContainer(gatewayName, architecture, docker); err != nil {
			return err
		}
	}
	// Inspect the same named container immediately before activation in case it was recreated.
	current, err := acceptanceContainer(botName, architecture, docker)
	if err != nil || current.ID != bot.ID || current.Health != "healthy" {
		return errors.New("Bot 已更换或健康状态发生变化，未激活安装")
	}
	if err := activateSource(directory, marker); err != nil {
		return err
	}
	fmt.Fprintf(progress, "TGCLOUD_CODE=OK\nTGCLOUD_VERSION=%s\nTGCLOUD_CONFIG=OK\n", marker.Version)
	return nil
}

func acceptanceContainer(name, architecture string, docker DockerRunner) (Container, error) {
	output, err := docker("container", "inspect", "--format", acceptanceContainerFields, name)
	var result struct {
		Container Container
		Image     string
	}
	if err != nil || json.Unmarshal(output, &result) != nil || !containerID.MatchString(result.Container.ID) ||
		result.Container.Name != "/"+name || result.Container.State != "running" || result.Container.Health == "unhealthy" ||
		!imageID.MatchString(result.Image) || !imageIsNative(result.Image, architecture, docker) {
		return Container{}, fmt.Errorf("%s 的运行状态或原生镜像无法核实", name)
	}
	return result.Container, nil
}

func verifyBotRuntime(directory, id string, marker sourceMarker, progress io.Writer, docker DockerRunner) error {
	var desired struct {
		Services map[string]struct{ Environment map[string]*string }
	}
	config, err := docker(standaloneComposeArgs(directory, "config", "--format", "json")...)
	if err != nil || json.Unmarshal(config, &desired) != nil || len(desired.Services[botName].Environment) == 0 {
		return errors.New("无法核对正式 Bot 的 Compose 配置")
	}
	var probe struct {
		Version, Code string
		Environment   map[string]string
	}
	output, err := docker("exec", id, "python", "-B", "-c", botAcceptanceProbe)
	if err != nil {
		fmt.Fprintln(progress, "TGCLOUD_CONFIG=UNKNOWN")
		return fmt.Errorf("无法读取正式 Bot 配置验收结果：%w", err)
	}
	if json.Unmarshal(output, &probe) != nil || len(probe.Environment) == 0 {
		fmt.Fprintln(progress, "TGCLOUD_CONFIG=FAILED")
		return errors.New("正式 Bot 返回的配置验收数据无效（未输出配置值）")
	}
	var changed []string
	for key, value := range desired.Services[botName].Environment {
		if !envKey.MatchString(key) {
			return errors.New("正式 Bot 的配置键格式无效")
		}
		if value == nil || probe.Environment[key] != sha256Text([]byte(*value)) {
			changed = append(changed, key)
		}
	}
	if len(changed) != 0 {
		sort.Strings(changed)
		fmt.Fprintln(progress, "TGCLOUD_CONFIG=MISMATCH_KEYS:"+strings.Join(changed, ","))
		return errors.New("运行中的 Bot 配置与预期不一致")
	}
	if probe.Code != marker.codeFingerprint() || probe.Version != marker.Version {
		fmt.Fprintln(progress, "TGCLOUD_CODE=MISMATCH")
		return errors.New("运行中的 Bot 代码指纹或版本与预期不一致")
	}
	return nil
}
