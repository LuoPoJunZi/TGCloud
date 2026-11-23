package deps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

var basePackages = []string{"ca-certificates", "curl", "gnupg", "tar", "gzip", "fuse3", "kmod", "openssh-client", "iproute2", "python3"}

type dependencyRunner func(name string, args ...string) ([]byte, error)

func PrepareDependencies(upgrade bool, progress io.Writer) error {
	if err := inspect.CheckDockerHost(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("安装宿主依赖需要 root")
	}
	_, err := exec.LookPath("docker")
	dockerPresent := err == nil
	if !dockerPresent && !errors.Is(err, exec.ErrNotFound) {
		return errors.New("无法核实现有 Docker 命令")
	}
	run := func(name string, args ...string) ([]byte, error) {
		return runDependencyCommand(progress, name, args...)
	}
	if !dockerPresent && !upgrade {
		if _, err := exec.LookPath("dockerd"); !errors.Is(err, exec.ErrNotFound) {
			return errors.New("检测到现有 Docker daemon，请先恢复 Docker CLI")
		}
		if _, err := os.Lstat("/var/run/docker.sock"); !errors.Is(err, os.ErrNotExist) {
			return errors.New("检测到现有 Docker socket，请先恢复 Docker CLI")
		}
		packages := []string{"docker-ce", "docker-ce-cli", "docker.io"}
		missing, err := missingDependencyPackages(packages, run)
		if err != nil || len(missing) != len(packages) {
			return errors.New("现有 Docker 软件包状态无法核实，请先恢复 Docker CLI")
		}
	}
	return prepareDependencies(upgrade, dockerPresent, progress, run, func() error {
		_, err := inspect.DockerPreflight(false)
		return err
	}, func() error { return ensureDockerRepository(run) })
}

func prepareDependencies(upgrade, dockerPresent bool, progress io.Writer, run dependencyRunner,
	verifyDocker, prepareRepository func() error) error {
	if upgrade && !dockerPresent {
		return errors.New("升级要求现有 Docker 可用，请先检查宿主服务")
	}
	composeReady := false
	if dockerPresent {
		if err := verifyDocker(); err != nil {
			return err
		}
		var err error
		composeReady, err = checkComposeDependency(run)
		if err != nil {
			return err
		}
		if upgrade && !composeReady {
			return errors.New("升级要求现有 Docker Compose 可用，请先恢复 Compose")
		}
		if !composeReady {
			plugins := []string{"docker-compose-plugin", "docker-compose-v2"}
			missing, err := missingDependencyPackages(plugins, run)
			if err != nil || len(missing) != len(plugins) {
				return errors.New("已安装的 Compose 插件无法使用，请先核对现有插件")
			}
		}
	}
	missing, err := missingDependencyPackages(basePackages, run)
	if err != nil {
		return err
	}
	if len(missing) == 0 && composeReady {
		fmt.Fprintln(progress, "[TGCloud] 基础软件、Docker 与 Compose 已就绪")
		return nil
	}
	if len(missing) != 0 {
		fmt.Fprintln(progress, "[TGCloud] 安装缺少的基础软件")
		if err := refreshPackageLists(run); err != nil {
			return err
		}
		if err := installDependencyPackages(missing, run); err != nil {
			return err
		}
	}
	if !composeReady {
		if err := prepareRepository(); err != nil {
			return err
		}
		if dockerPresent {
			fmt.Fprintln(progress, "[TGCloud] 安装缺少的 Docker Compose 插件")
			if err := installDependencyPackages([]string{"docker-compose-plugin"}, run); err != nil {
				return err
			}
		} else {
			fmt.Fprintln(progress, "[TGCloud] 通过 Docker 官方 APT 仓库安装 Docker")
			if err := installDependencyPackages([]string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}, run); err != nil {
				return err
			}
			if _, err := run("systemctl", "enable", "--now", "docker"); err != nil {
				return errors.New("无法启用并启动新安装的 Docker 服务")
			}
		}
	}
	if err := verifyDocker(); err != nil {
		return err
	}
	if ready, err := checkComposeDependency(run); err != nil || !ready {
		return errors.New("Docker Compose 安装后未通过检查")
	}
	return nil
}

func checkComposeDependency(run dependencyRunner) (bool, error) {
	output, err := run("docker", "compose", "version")
	if err == nil {
		return true, nil
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "docker: unknown command: docker compose" || line == "docker: 'compose' is not a docker command." {
			return false, nil
		}
	}
	return false, errors.New("Docker Compose 已有异常，请核对插件，未执行安装")
}

func missingDependencyPackages(packages []string, run dependencyRunner) ([]string, error) {
	var missing []string
	for _, name := range packages {
		output, err := run("dpkg-query", "-W", "-f=${db:Status-Status}", name)
		status := strings.TrimSpace(string(output))
		switch {
		case err == nil && status == "installed":
		case err == nil && (status == "config-files" || status == "not-installed"),
			status == "dpkg-query: no packages found matching "+name:
			missing = append(missing, name)
		default:
			return nil, fmt.Errorf("无法核实软件包 %s 的安装状态", name)
		}
	}
	return missing, nil
}

const aptLockWait = "DPkg::Lock::Timeout=120"

func refreshPackageLists(run dependencyRunner) error {
	if _, err := run("apt-get", "-o", aptLockWait, "-o", "APT::Update::Error-Mode=any", "update"); err != nil {
		return errors.New("无法刷新 APT 软件包列表")
	}
	return nil
}

func installDependencyPackages(packages []string, run dependencyRunner) error {
	options := append([]string{"-o", aptLockWait, "--no-install-recommends", "--no-upgrade", "--no-remove", "install"}, packages...)
	plan, err := run("apt-get", append([]string{"-s"}, options...)...)
	if err != nil || !aptPlanOnlyAdds(string(plan)) {
		return errors.New("APT 计划无法核实或涉及修改现有软件包，已停止安装依赖")
	}
	if _, err := run("apt-get", append([]string{"-y"}, options...)...); err != nil {
		return errors.New("缺少的软件包安装失败")
	}
	return nil
}

func aptPlanOnlyAdds(plan string) bool {
	added := make(map[string]bool)
	for _, line := range strings.Split(plan, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "Remv", "Purg":
			return false
		case "Inst":
			if len(fields) < 3 || !strings.HasPrefix(fields[2], "(") {
				return false
			}
			added[fields[1]] = true
		case "Conf":
			if len(fields) < 2 || !added[fields[1]] {
				return false
			}
		}
	}
	return true
}

func runDependencyCommand(progress io.Writer, name string, args ...string) ([]byte, error) {
	timeout := 20 * time.Second
	if name == "apt-get" {
		timeout = 12 * time.Minute
	} else if name == "curl" || name == "systemctl" {
		timeout = 75 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l")
	if (name == "apt-get" && (slices.Contains(args, "-y") || slices.Contains(args, "update"))) || name == "systemctl" {
		command.Stdout, command.Stderr = progress, progress
		return nil, command.Run()
	}
	if name == "dpkg-query" || name == "dpkg" {
		var stdout, stderr hostutil.BoundedBuffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if err != nil && name == "dpkg-query" && strings.TrimSpace(stdout.String()) == "" {
			for _, line := range strings.Split(stderr.String(), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "dpkg-query: no packages found matching ") {
					return []byte(trimmed), err
				}
			}
		}
		return stdout.Bytes(), err
	}
	var output hostutil.BoundedBuffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	return output.Bytes(), err
}
