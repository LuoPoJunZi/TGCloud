package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

var (
	installConfigKey = hostutil.EnvKeyPattern
	dockerfileBase   = regexp.MustCompile(`(?m)^FROM ([^\s]+)\s*$`)
)

func PrepareInstallConfig(directory, config string, result io.Writer) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(config); err != nil || config != resolved ||
		!filepath.IsAbs(config) || strings.HasPrefix(config, directory+"/") {
		return errors.New("输入配置必须是安装目录之外的私有文件")
	}
	data, err := normalizedInstallConfig(config)
	if err != nil {
		return err
	}
	mode := "fresh"
	if compose, err := os.Lstat(filepath.Join(directory, "docker-compose.yml")); !errors.Is(err, os.ErrNotExist) {
		if err != nil || !compose.Mode().IsRegular() {
			return errors.New("无法核实现有安装的 Compose 文件")
		}
		if _, err := standaloneVersion(directory, "ready"); err != nil {
			return err
		}
		if slices.Contains(strings.Split(string(data), "\n"), "TGCLOUD_REDEPLOY_APPLY_CONFIG=true") {
			mode = "apply"
		} else {
			data, err = readInstallConfig(filepath.Join(directory, ".env"))
			if err != nil {
				return errors.New("无法读取现有私有配置以供候选预检")
			}
			mode = "preserved"
		}
	}
	if err := replaceInstallInput(config, data); err != nil {
		return err
	}
	_, err = fmt.Fprintln(result, mode)
	return err
}

func replaceInstallInput(path string, data []byte) error {
	invalid := errors.New("无法准备私有部署输入配置")
	file, err := os.CreateTemp(filepath.Dir(path), ".tgcloud-config-")
	if err != nil {
		return invalid
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	if closeErr := file.Close(); writeErr != nil || syncErr != nil || closeErr != nil {
		return invalid
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return invalid
	}
	return nil
}

func checkDeploymentConfig(directory string) (bool, error) {
	data, err := readInstallConfig(filepath.Join(directory, ".env"))
	if err != nil {
		return false, err
	}
	values, err := parseDeploymentConfig(data)
	if err != nil {
		return false, err
	}
	for _, key := range forbiddenDockerEnv {
		if values[key] != "" {
			return false, errors.New("部署配置包含 Docker/Compose 平台或构建器覆盖")
		}
	}
	switch values["DEPLOY_CLOUDDRIVE2"] {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("DEPLOY_CLOUDDRIVE2 必须为 true 或 false")
	}
}

func parseDeploymentConfig(data []byte) (map[string]string, error) {
	values, err := hostutil.ParseEnvLines(data, errors.New("部署配置存在无效或重复项目"))
	if err != nil {
		return nil, err
	}
	if value, exists := values["TGCLOUD_REDEPLOY_APPLY_CONFIG"]; exists && value != "true" && value != "false" {
		return nil, errors.New("配置覆盖标记无效")
	}
	return values, nil
}

func PreflightCandidate(stage string) error {
	if !candidateStage.MatchString(stage) {
		return errors.New("候选部署目录无效")
	}
	if err := verifyCandidateStage(stage); err != nil {
		return err
	}
	if _, err := checkDeploymentConfig(stage); err != nil {
		return err
	}
	native, err := DockerPreflight(false)
	if err != nil {
		return err
	}
	config, err := runDockerWithTimeout(true, nil, 30*time.Second, standaloneComposeArgs(stage, "config", "--format", "json")...)
	if err != nil {
		return errors.New("候选 Compose 配置检查失败")
	}
	dockerfile, err := readSourceFile(filepath.Join(stage, "Dockerfile"), 2*1024*1024)
	if err != nil {
		return errors.New("无法读取候选 Dockerfile")
	}
	images, err := candidateImages(config, dockerfile, native.Platform)
	if err != nil {
		return err
	}
	for _, reference := range images {
		if err := ensureNativeImage(reference, *native, RunDocker); err != nil {
			return err
		}
	}
	return nil
}

func candidateImages(config, dockerfile []byte, platform string) ([]string, error) {
	invalid := errors.New("候选 Compose/Dockerfile 架构或镜像引用无法核实")
	var compose struct {
		Services map[string]*struct {
			Image    string
			Platform json.RawMessage
		}
	}
	base := dockerfileBase.FindAllSubmatch(dockerfile, -1)
	if len(base) != 1 || json.Unmarshal(config, &compose) != nil || compose.Services[gatewayService] == nil {
		return nil, invalid
	}
	for _, service := range compose.Services {
		if service == nil {
			return nil, invalid
		}
		var actual string
		if len(service.Platform) != 0 && (json.Unmarshal(service.Platform, &actual) != nil || actual != platform) {
			return nil, invalid
		}
	}
	images := []string{string(base[0][1]), compose.Services[gatewayService].Image}
	for _, reference := range images {
		if !imageReference.MatchString(reference) || strings.Contains(reference, "://") {
			return nil, invalid
		}
	}
	return images, nil
}

func CheckInstallCompose(directory string, result io.Writer) error {
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	if err := checkUpgradeFiles(directory); err != nil {
		return err
	}
	if _, err := standaloneVersion(directory, "pending"); err != nil {
		return err
	}
	gateway, err := checkDeploymentConfig(directory)
	if err != nil {
		return err
	}
	if _, err := DockerPreflight(false); err != nil {
		return err
	}
	if _, err := runDockerWithTimeout(true, nil, 30*time.Second, standaloneComposeArgs(directory, "config", "--quiet")...); err != nil {
		return errors.New("正式 Compose 配置检查失败")
	}
	_, err = fmt.Fprintln(result, gateway)
	return err
}
