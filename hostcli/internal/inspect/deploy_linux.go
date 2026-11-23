package inspect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const deployTimeout = 2400 * time.Second

func StandaloneComposeArgs(directory string, args ...string) []string {
	return standaloneComposeArgs(directory, args...)
}

func standaloneComposeArgs(directory string, args ...string) []string {
	command := []string{"compose", "--project-directory", directory, "--env-file", filepath.Join(directory, ".env"),
		"-f", filepath.Join(directory, "docker-compose.yml")}
	return append(command, args...)
}

func DeployBot(directory string, progress io.Writer) error {
	return deployInstalledBot(context.Background(), directory, "", progress)
}

func DeployBotForInstall(directory, candidate string, progress io.Writer) error {
	if !candidateTag.MatchString(candidate) {
		return errors.New("安装候选镜像标签无效")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	return deployInstalledBot(ctx, directory, candidate, progress)
}

func deployInstalledBot(ctx context.Context, directory, candidate string, progress io.Writer) error {
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	if err := checkUpgradeFiles(directory); err != nil {
		return err
	}
	if _, err := standaloneVersion(directory, "pending"); err != nil {
		return errors.New("正式 Bot 部署要求待激活的一键安装程序")
	}
	if _, err := DockerPreflight(false); err != nil {
		return err
	}
	compose := func(args ...string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var environment []string
		if args[0] == "build" {
			environment = []string{"BUILDX_BUILDER=default"}
		}
		commandContext := ctx
		if args[0] == "up" {
			// Wait for Docker to finish publishing the container before rollback.
			commandContext = context.WithoutCancel(ctx)
		}
		_, err := runDockerCommandContext(commandContext, true, progress, deployTimeout, environment, standaloneComposeArgs(directory, args...)...)
		return err
	}
	validate := func() error {
		args := []string{"run", "--rm", "--no-deps"}
		if candidate != "" {
			args = append(args, "--label", candidateLabel+"="+candidate)
		}
		return compose(append(args, "--entrypoint", "python", botName, "-m", "app.deployment_check", "--validate-only")...)
	}
	health := func() string {
		status := waitContainerHealthContext(ctx, botName, RunDocker)
		if status != "healthy" && ctx.Err() == nil {
			fmt.Fprintln(progress, "[TGCloud] Bot 当前状态："+status)
			showDeployDiagnostics(directory, progress)
		}
		return status
	}
	return deployBot(progress, compose, validate, health)
}

func deployBot(progress io.Writer, compose func(...string) error, validate func() error, health func() string) error {
	fmt.Fprintln(progress, "TGCLOUD_STAGE=BOT\n[TGCloud] 构建 TGCloud Bot 容器")
	if err := compose("build", botName); err != nil {
		return errors.New("正式 Bot 镜像构建失败")
	}
	if err := validate(); err != nil {
		return errors.New("正式 Bot 配置校验失败")
	}
	if err := compose("up", "-d", "--no-deps", botName); err != nil {
		return errors.New("正式 Bot 容器启动失败")
	}
	fmt.Fprintln(progress, "TGCLOUD_STAGE=HEALTH\n[TGCloud] 等待 Bot 基础健康检查")
	if health() != "healthy" {
		return errors.New("Bot 未通过基础健康检查，请根据上面的日志修正配置")
	}
	return nil
}

func waitBotHealth(docker DockerRunner) string {
	return waitContainerHealth(botName, docker)
}

func waitContainerHealth(container string, docker DockerRunner) string {
	return waitContainerHealthContext(context.Background(), container, docker)
}

func waitContainerHealthContext(ctx context.Context, container string, docker DockerRunner) string {
	format := "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}"
	status := "unknown"
	for range 40 {
		if ctx.Err() != nil {
			break
		}
		result, err := docker("inspect", "--format", format, container)
		if err == nil {
			switch state := strings.TrimSpace(string(result)); state {
			case "healthy", "unhealthy", "exited", "dead":
				return state
			case "starting", "created", "running", "restarting", "paused", "removing":
				status = state
			}
		}
		select {
		case <-ctx.Done():
			return status
		case <-time.After(3 * time.Second):
		}
	}
	return status
}

func showDeployDiagnostics(directory string, output io.Writer) {
	clean, err := LogRedactor(directory)
	if err != nil {
		return
	}
	for _, args := range [][]string{{"ps"}, {"logs", "--tail=120", botName}} {
		data, err := runDockerWithTimeout(true, nil, 20*time.Second, standaloneComposeArgs(directory, args...)...)
		if err == nil && len(data) > 0 {
			fmt.Fprintln(output, clean(string(data)))
		}
	}
}
