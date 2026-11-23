package inspect

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const contextEndpoint = "{{json .Endpoints.docker.Host}}"
const preflightInfo = `{"os":{{json .OSType}},"arch":{{json .Architecture}},"version":{{json .ServerVersion}},"security":{{json .SecurityOptions}}}`

var forbiddenDockerEnv = [...]string{
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
	"DOCKER_DEFAULT_PLATFORM", "BUILDX_BUILDER", "BUILDKIT_HOST", "COMPOSE_BAKE",
	"COMPOSE_FILE", "COMPOSE_PROJECT_NAME", "COMPOSE_PROFILES", "COMPOSE_ENV_FILES",
}

type PreflightResult struct {
	Architecture string `json:"architecture"`
	Platform     string `json:"platform"`
	Version      string `json:"version"`
}

func DockerPreflight(allowMissing bool) (*PreflightResult, error) {
	if err := checkDockerHost(); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("docker"); err != nil {
		if allowMissing && errors.Is(err, exec.ErrNotFound) {
			return nil, nil
		}
		return nil, errors.New("Docker 不可用；不自动重启或接管其他 daemon")
	}
	return checkDockerDaemon(runtime.GOARCH, func(args ...string) ([]byte, error) {
		// Read the configured context before querying the fixed local socket.
		return runDocker(args[0] != "context", nil, args...)
	})
}

func checkDockerHost() error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("需要 Linux AMD64 或 ARM64 宿主机")
	}
	for _, key := range forbiddenDockerEnv {
		if os.Getenv(key) != "" {
			return errors.New("只允许本机默认 Docker/Compose；拒绝远端 daemon、平台与构建器覆盖")
		}
	}
	return nil
}

func checkDockerDaemon(expected string, docker DockerRunner) (*PreflightResult, error) {
	output, err := docker("context", "inspect", "--format", contextEndpoint)
	var endpoint string
	if err != nil || json.Unmarshal(output, &endpoint) != nil || endpoint != localDockerHost {
		return nil, errors.New("Docker 不可用或不是本机默认 daemon；拒绝远端/自定义 context")
	}
	output, err = docker("info", "--format", preflightInfo)
	var info struct {
		OS, Arch string
		Version  *string
		Security []*string
	}
	invalid := errors.New("Docker 平台/权限信息无效；需要本机 Linux rootful daemon")
	if err != nil || json.Unmarshal(output, &info) != nil || info.OS != "linux" || info.Version == nil {
		return nil, invalid
	}
	for _, option := range info.Security {
		if option == nil || strings.Contains(strings.ToLower(*option), "rootless") {
			return nil, invalid
		}
	}
	architecture := nativeArchitecture(info.Arch)
	if architecture == "" {
		return nil, invalid
	}
	if architecture != expected {
		return nil, errors.New("Docker daemon 与宿主 CPU 架构不一致；禁止模拟或跨架构部署")
	}
	return &PreflightResult{Architecture: architecture, Platform: "linux/" + architecture, Version: *info.Version}, nil
}

func nativeArchitecture(value string) string {
	return map[string]string{
		"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64",
	}[strings.ToLower(strings.TrimSpace(value))]
}
