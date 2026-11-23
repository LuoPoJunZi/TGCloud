package inspect

import (
	"context"
	"errors"
	"io"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func HTTPSEntrySupported() bool { return true }

// HTTPSEntry is used only by the public one-command installer for its managed
// proxy. The external Nginx adapter and the Compose owner have separate writers.
func HTTPSEntry(args []string, progress io.Writer) (any, error) {
	if len(args) < 5 || len(args) > 8 {
		return nil, errors.New("公网 HTTPS 入口参数无效")
	}
	entry := httpsEntry{root: httpsRoot, directory: args[1], nginxImage: args[2], certbotImage: args[3]}
	action, domain := args[0], args[4]
	if err := checkPlanningDirectory(entry.directory); err != nil {
		return nil, err
	}
	if !validHTTPSDomain(domain) || !httpsNginxImage.MatchString(entry.nginxImage) || !httpsCertbotImage.MatchString(entry.certbotImage) {
		return nil, errors.New("公网 HTTPS 域名或镜像参数无效")
	}
	if action == "configure" && len(args) == 8 {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		return nil, entry.configureHost(ctx, domain, args[5], args[6], args[7], progress)
	}
	if len(args) != 5 || (action != "probe" && action != "status") {
		return nil, errors.New("公网 HTTPS 入口参数无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	native, err := DockerPreflight(true)
	if err != nil {
		return nil, err
	}
	var docker DockerRunner
	if native != nil {
		docker = entry.dockerRunner(ctx, *native)
	}
	state, exists, err := entry.readState()
	if err != nil {
		return nil, err
	}
	if err := entry.sharedStateAvailable(); err != nil {
		return nil, err
	}
	containers, err := entry.ownedContainers(docker, exists)
	if err != nil {
		return nil, err
	}
	if action == "probe" {
		return entry.probe(ctx, domain, containers, docker)
	}
	if domain != state.domain() {
		return nil, errors.New("共享 HTTPS 域名状态已变化，请重新检查")
	}
	return entry.status(ctx, domain, containers, docker), nil
}

func (entry httpsEntry) dockerRunner(ctx context.Context, native PreflightResult) DockerRunner {
	return func(args ...string) ([]byte, error) {
		timeout := 20 * time.Second
		var environment []string
		switch {
		case args[0] == "compose" && len(args) > 9 && args[2] == entry.root:
			switch args[9] {
			case "run":
				timeout = 320 * time.Second
			case "up", "stop":
				timeout = 180 * time.Second
			}
			environment = []string{"DOCKER_DEFAULT_PLATFORM=" + native.Platform}
		case args[0] == "pull":
			timeout = 600 * time.Second
		case args[0] == "run":
			timeout = 90 * time.Second
		}
		return runDockerCommandContext(ctx, true, nil, timeout, environment, args...)
	}
}

func (entry httpsEntry) configureHost(ctx context.Context, domain, email, confirmed, expected string, progress io.Writer) error {
	if email != strings.TrimSpace(email) || len(email) > 254 || (email != "" && !httpsEmail.MatchString(email)) {
		return errors.New("Let's Encrypt 邮箱格式不正确")
	}
	native, err := DockerPreflight(false)
	if err != nil {
		return err
	}
	lock, err := entry.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	previous, exists, err := entry.readState()
	if err != nil {
		return err
	}
	if previous.domain() != expected || (expected != "" && expected != domain) {
		return errors.New("确认后共享 HTTPS 域名发生变化；请重新预检")
	}
	if err := entry.sharedStateAvailable(); err != nil {
		return err
	}
	if err := entry.checkConfigFiles(); err != nil {
		return err
	}

	docker := entry.dockerRunner(ctx, *native)
	containers, err := entry.ownedContainers(docker, exists)
	if err != nil {
		return err
	}
	// An entry recovered by another operation before the lock can be reused.
	if expected == domain && healthyHTTPS(entry.status(ctx, domain, containers, docker)) {
		io.WriteString(progress, "TGCLOUD_HTTPS=OK\n")
		return nil
	}
	io.WriteString(progress, "TGCLOUD_STAGE=HTTPS_ENV\n")
	environment, err := entry.probe(ctx, domain, containers, docker)
	if err != nil {
		return err
	}
	if environment.Core != "OK" || !environment.backendLocal || environment.Docker != "OK" {
		return errors.New("基础服务、网关回环监听或 Docker Compose 未通过 HTTPS 前置检查")
	}
	if err := environment.safe(confirmed); err != nil {
		return err
	}
	for _, image := range []string{entry.nginxImage, entry.certbotImage} {
		if err := ensureNativeImage(image, *native, docker); err != nil {
			return err
		}
	}
	if !exists {
		if err := entry.commit(previous); err != nil {
			return err
		}
	}
	// The foreground session sends TERM and waits for recovery. The rollback
	// context stays live even after the original work was cancelled.
	var rollbackContext context.Context
	var cancelRollback context.CancelFunc
	defer func() {
		if cancelRollback != nil {
			cancelRollback()
		}
	}()
	rollback := func(args ...string) ([]byte, error) {
		if rollbackContext == nil {
			rollbackContext, cancelRollback = context.WithTimeout(context.Background(), 7*time.Minute)
		}
		return entry.dockerRunner(rollbackContext, *native)(args...)
	}
	return entry.configure(ctx, previous, domain, email, native.Architecture, docker, rollback, entry.status, progress)
}
