package entry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

type renewStateDoc struct {
	Version int                   `json:"version"`
	Project string                `json:"project"`
	Domains map[string]httpsRoute `json:"domains"`
}

func readRenewState(root string) (renewStateDoc, error) {
	path := filepath.Join(root, "state/domains.json")
	if !hostutil.SafePath(path, false) {
		return renewStateDoc{}, errHTTPSState
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return renewStateDoc{}, errors.New("当前实例尚未配置域名，不能演练续期")
	}
	raw, err := readHTTPSPrivate(path, inspect.InstallConfigLimit)
	if err != nil {
		return renewStateDoc{}, errHTTPSState
	}
	var doc renewStateDoc
	if json.Unmarshal(raw, &doc) != nil || doc.Project != "tgcloud" {
		return renewStateDoc{}, errHTTPSState
	}
	if doc.Version == 3 {
		return doc, ErrExternalProxyMode
	}
	if doc.Version != 1 && doc.Version != 2 {
		return renewStateDoc{}, errHTTPSState
	}
	return doc, nil
}

// RenewDryRun executes a bounded, locked Certbot ACME renewal dry-run for the
// managed proxy, verifying registered domain, proxy ownership, and confirmed
// temporary container cleanup before reporting success.
func RenewDryRun(domain string, progress io.Writer) error {
	if _, err := inspect.DockerPreflight(false); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	docker := func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "compose" {
			return inspect.RunDockerContext(ctx, true, progress, 540*time.Second, nil, args...)
		}
		// Cleanup and ownership probes run on an uncancellable short context so
		// SIGINT/SIGTERM never skips container verification or removal.
		probeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return inspect.RunDockerContext(probeCtx, true, nil, 10*time.Second, nil, args...)
	}
	return renewDryRunWith(HTTPSRoot, domain, docker, progress)
}

func renewDryRunWith(root, requestedDomain string, docker inspect.DockerRunner, progress io.Writer) (retErr error) {
	if requestedDomain != "" && !validHTTPSDomain(requestedDomain) {
		return errors.New("公网 HTTPS 域名格式无效")
	}
	e := httpsEntry{root: root}
	lock, err := e.lock()
	if err != nil {
		return err
	}
	defer lock.Close()

	state, err := readRenewState(root)
	if err != nil {
		return err
	}
	route, ok := state.Domains[httpsEdition]
	if !ok || !validHTTPSDomain(route.Domain) || route.Port != httpsBackendPort {
		return errors.New("当前实例尚未配置域名，不能演练续期")
	}
	if requestedDomain != "" && requestedDomain != route.Domain {
		return errors.New("共享 HTTPS 域名状态已变化，请重新检查")
	}
	domain := route.Domain
	if err := e.sharedStateAvailable(); err != nil {
		return err
	}
	if err := e.checkConfigFiles(); err != nil {
		return err
	}
	composeFile := filepath.Join(root, "docker-compose.yml")
	if _, err := readHTTPSPrivate(composeFile, inspect.InstallConfigLimit); err != nil {
		return errHTTPSState
	}
	if _, err := e.ownedContainers(docker, true); err != nil {
		return err
	}

	var rb [16]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return errors.New("无法生成续期演练任务标识")
	}
	taskID := hex.EncodeToString(rb[:])
	containerName := "tgcloud-proxy-dry-run-" + taskID

	cleanupConfirmed := false
	cleanupErrMsg := errors.New("维护任务清理未确认，请检查本次一次性续期容器；不要重复演练")
	verifyCleanup := func() error {
		out, inspectErr := docker("container", "inspect", "--format",
			`{{index .Config.Labels "com.tgcloud.task"}}|{{index .Config.Labels "com.tgcloud.role"}}`,
			containerName)
		if inspectErr == nil {
			if strings.TrimSpace(string(out)) != taskID+"|maintenance-dry-run" {
				if progress != nil {
					fmt.Fprintln(progress, "TGCLOUD_PROXY_TASK_CLEANUP=REFUSED")
				}
				return cleanupErrMsg
			}
			if _, rmErr := docker("rm", "-f", containerName); rmErr != nil {
				if progress != nil {
					fmt.Fprintln(progress, "TGCLOUD_PROXY_TASK_CLEANUP=FAILED")
				}
				return cleanupErrMsg
			}
		} else {
			if _, infoErr := docker("info"); infoErr != nil {
				if progress != nil {
					fmt.Fprintln(progress, "TGCLOUD_PROXY_TASK_CLEANUP=UNKNOWN")
				}
				return cleanupErrMsg
			}
		}
		if progress != nil {
			fmt.Fprintln(progress, "TGCLOUD_PROXY_TASK_CLEANUP=OK")
		}
		cleanupConfirmed = true
		return nil
	}

	defer func() {
		if !cleanupConfirmed {
			if cErr := verifyCleanup(); cErr != nil && retErr == nil {
				retErr = cErr
			}
		}
	}()

	if progress != nil {
		fmt.Fprintln(progress, "TGCLOUD_STAGE=RENEW_DRY_RUN")
	}
	_, runErr := docker(
		"compose", "--project-directory", root, "--env-file", "/dev/null",
		"-p", HTTPSProject, "-f", composeFile,
		"run", "--rm", "--no-deps",
		"--name", containerName,
		"--label", "com.tgcloud.task="+taskID,
		"--label", "com.tgcloud.role=maintenance-dry-run",
		"--entrypoint", "certbot", "certbot",
		"renew", "--dry-run", "--cert-name", domain,
		"--webroot", "-w", "/var/www/certbot",
		"--non-interactive", "--no-random-sleep-on-renew", "--no-directory-hooks",
	)
	if cErr := verifyCleanup(); cErr != nil {
		return cErr
	}
	if runErr != nil {
		return errors.New("续期 dry-run 未通过；请检查 ACME 公网连通与 Certbot 日志")
	}
	return nil
}

// ValidHTTPSDomain exposes strict FQDN validation for callers such as the Go TUI.
func ValidHTTPSDomain(domain string) bool {
	return validHTTPSDomain(domain)
}
