package inspect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type DockerRunner func(args ...string) ([]byte, error)

const localDockerHost = "unix:///var/run/docker.sock"

var errOutputLimit = errors.New("Docker 输出超过 8 MiB")

type dockerOutput struct {
	buffer bytes.Buffer
}

func (output *dockerOutput) Write(chunk []byte) (int, error) {
	if len(chunk) > 8*1024*1024-output.buffer.Len() {
		return 0, errOutputLimit
	}
	return output.buffer.Write(chunk)
}

func RunDocker(args ...string) ([]byte, error) {
	return runDocker(true, nil, args...)
}

func RunDockerWithProgress(forceLocal bool, progress io.Writer, args ...string) ([]byte, error) {
	return runDocker(forceLocal, progress, args...)
}

func RunDockerContext(parent context.Context, forceLocal bool, progress io.Writer, timeout time.Duration, environment []string, args ...string) ([]byte, error) {
	return runDockerCommandContext(parent, forceLocal, progress, timeout, environment, args...)
}

func runDocker(forceLocal bool, progress io.Writer, args ...string) ([]byte, error) {
	timeout := 20 * time.Second
	if len(args) > 0 {
		switch args[0] {
		case "logs":
			timeout = 30 * time.Second
		case "start", "stop", "restart":
			// The installed Bot allows up to 45 seconds for graceful shutdown.
			timeout = 90 * time.Second
		case "pull":
			timeout = 600 * time.Second
		case "build":
			timeout = 2400 * time.Second
		case "run":
			timeout = 120 * time.Second
		}
	}
	return runDockerWithTimeout(forceLocal, progress, timeout, args...)
}

func runDockerWithTimeout(forceLocal bool, progress io.Writer, timeout time.Duration, args ...string) ([]byte, error) {
	return runDockerCommand(forceLocal, progress, timeout, nil, args...)
}

func runDockerCommand(forceLocal bool, progress io.Writer, timeout time.Duration, environment []string, args ...string) ([]byte, error) {
	return runDockerCommandContext(context.Background(), forceLocal, progress, timeout, environment, args...)
}

func runDockerCommandContext(parent context.Context, forceLocal bool, progress io.Writer, timeout time.Duration, environment []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	commandArgs := args
	if forceLocal {
		commandArgs = append([]string{"--host=" + localDockerHost}, args...)
	}
	cmd := exec.CommandContext(ctx, "docker", commandArgs...)
	if parent.Done() != nil {
		isolateInstallCommand(cmd, true)
	}
	if forceLocal {
		// Match host paths to the local daemon even if the shell selects a remote context or Compose override.
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if name == "DOCKER_TLS" || name == "TGCLOUD_HOST_LOCK_FD" || slices.Contains(forbiddenDockerEnv[:], name) {
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
	}
	if len(environment) > 0 {
		cmd.Env = append(cmd.Environ(), environment...)
	}
	var output dockerOutput
	cmd.Stdout = &output
	if len(args) > 0 && args[0] == "logs" {
		cmd.Stderr = &output
	}
	if progress != nil {
		cmd.Stdout, cmd.Stderr = progress, progress
	}
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return nil, errors.New("本机 Docker 操作超时，请用 status 核对运行状态")
	case errors.Is(err, exec.ErrNotFound):
		return nil, errors.New("找不到 Docker 命令")
	case errors.Is(err, errOutputLimit):
		return nil, err
	case err != nil:
		return nil, errors.New("本机 Docker 命令失败，请核对 Docker 服务和访问权限")
	}
	return output.buffer.Bytes(), nil
}
