package entry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

// RenewDryRun executes a bounded, isolated Certbot ACME renewal dry-run without
// modifying active certificates and guarantees cleanup of the temporary container.
func RenewDryRun(domain string, progress io.Writer) error {
	if !validHTTPSDomain(domain) {
		return errors.New("公网 HTTPS 域名格式无效")
	}
	if _, err := inspect.DockerPreflight(false); err != nil {
		return err
	}
	var rb [16]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return errors.New("无法生成续期演练任务标识")
	}
	taskID := hex.EncodeToString(rb[:])
	containerName := "tgcloud-proxy-dry-run-" + taskID

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := inspect.RunDockerContext(cleanupCtx, true, nil, 10*time.Second, nil,
			"container", "inspect", "--format",
			`{{index .Config.Labels "com.tgcloud.task"}}|{{index .Config.Labels "com.tgcloud.role"}}`,
			containerName)
		if err == nil && strings.TrimSpace(string(out)) == taskID+"|maintenance-dry-run" {
			_, _ = inspect.RunDockerContext(cleanupCtx, true, nil, 10*time.Second, nil, "rm", "-f", containerName)
		}
	}()

	if progress != nil {
		fmt.Fprintln(progress, "TGCLOUD_STAGE=RENEW_DRY_RUN")
	}
	composeFile := filepath.Join(HTTPSRoot, "docker-compose.yml")
	_, err := inspect.RunDockerContext(ctx, true, progress, 540*time.Second, nil,
		"compose", "--project-directory", HTTPSRoot, "--env-file", "/dev/null",
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
	if err != nil {
		return errors.New("续期 dry-run 未通过；请检查 ACME 公网连通与 Certbot 日志")
	}
	return nil
}

// ValidHTTPSDomain exposes strict FQDN validation for callers such as the Go TUI.
func ValidHTTPSDomain(domain string) bool {
	return validHTTPSDomain(domain)
}
