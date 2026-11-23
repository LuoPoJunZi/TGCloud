package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

type installEvidence struct {
	Action, Mode, Target           string
	Completed, Failed, Interrupted bool
	Checks                         map[string]string
}

type installSummary struct {
	Schema        int               `json:"schema"`
	Outcome       string            `json:"outcome"`
	Checks        map[string]string `json:"checks"`
	ActualVersion string            `json:"actual_version"`
	Readiness     map[string]string `json:"readiness"`
	LogRotation   map[string]string `json:"log_rotation"`
}

var installChecks = []string{"base", "config", "backup", "code", "health", "entry", "webdav"}

func Supported() bool { return true }

func Run(args []string, input io.Reader, progress io.Writer) (any, error) {
	if len(args) < 2 || len(args) > 4 {
		return nil, errors.New("安装后验收参数无效")
	}
	directory := args[1]
	if err := inspect.CheckPlanningDirectory(directory); err != nil {
		return nil, err
	}
	switch {
	case args[0] == "summary" && len(args) == 2:
		var observed installEvidence
		if err := inspect.ReadPreflightInput(input, &observed); err != nil {
			return nil, err
		}
		return summarizeInstallation(directory, observed)
	case args[0] == "webdav" && len(args) == 4 && args[3] == "--yes" && inspect.VersionPattern.MatchString(args[2]):
		return nil, verifyInstalledDestination(directory, args[2], progress)
	default:
		return nil, errors.New("安装后验收参数无效；WebDAV 写入需要确认")
	}
}

func summarizeInstallation(directory string, observed installEvidence) (installSummary, error) {
	if !inspect.VersionPattern.MatchString(observed.Target) ||
		(observed.Mode != "https" && observed.Mode != "lan") {
		return installSummary{}, errors.New("安装汇总的版本或模式无效")
	}
	switch observed.Action {
	case "install", "upgrade", "current", "newer":
	default:
		return installSummary{}, errors.New("安装汇总的操作类型无效")
	}
	summary := summarizeInstallEvidence(observed)
	// Preserve already verified stages on failure. Never probe a service
	// during rollback or re-run a WebDAV write to obtain a summary.
	if observed.Interrupted || summary.Checks["entry"] != "ok" {
		return summary, nil
	}
	if _, err := inspect.DockerPreflight(false); err != nil {
		summary.ActualVersion = ""
		summary.Checks["health"] = "unknown"
		return summary, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	docker := func(args ...string) ([]byte, error) {
		return inspect.RunDockerContext(ctx, true, nil, 10*time.Second, nil, args...)
	}
	summary.observe(directory, observed, docker)
	return summary, nil
}

func summarizeInstallEvidence(observed installEvidence) installSummary {
	summary := installSummary{Schema: 1, Outcome: "incomplete", Checks: make(map[string]string, len(installChecks))}
	for _, key := range installChecks {
		result := observed.Checks[key]
		switch result {
		case "ok", "failed", "na", "not_run":
			summary.Checks[key] = result
		default:
			summary.Checks[key] = "unknown"
		}
	}
	baseAction := observed.Action == "install" || observed.Action == "upgrade"
	switch {
	case observed.Interrupted:
		summary.Outcome = "interrupted"
	case observed.Failed || summary.Checks["base"] == "failed" || summary.Checks["entry"] == "failed" || summary.Checks["webdav"] == "failed":
		summary.Outcome = "failed"
	case observed.Completed && summary.Checks["entry"] == "ok" && (!baseAction || summary.Checks["base"] == "ok"):
		summary.Outcome = "completed"
	}
	if summary.Checks["code"] == "ok" && (!baseAction || summary.Checks["base"] == "ok") {
		summary.ActualVersion = observed.Target
	}
	return summary
}

func (summary *installSummary) observe(directory string, observed installEvidence, docker inspect.DockerRunner) {
	instance, err := inspect.Discover(directory, docker)
	if err != nil || instance == nil {
		summary.ActualVersion = ""
		summary.Checks["health"] = "unknown"
		return
	}
	summary.LogRotation = map[string]string{
		"Bot":      InspectContainerLogRotation(instance.Bot.ID, directory, inspect.BotName, instance.Bot.Project, "", docker),
		"网关":       InspectContainerLogRotation(instance.Gateway.ID, directory, "clouddrive2", instance.Gateway.Project, "", docker),
		"共享 Nginx": "unknown",
		"Certbot":  "unknown",
	}
	if observed.Mode == "https" {
		summary.LogRotation["共享 Nginx"] = InspectContainerLogRotation(inspect.HTTPSNginx, inspect.HTTPSRoot, "nginx", inspect.HTTPSProject, "reverse-proxy", docker)
		summary.LogRotation["Certbot"] = InspectContainerLogRotation(inspect.HTTPSCertbot, inspect.HTTPSRoot, "certbot", inspect.HTTPSProject, "certificate-manager", docker)
	}

	switch {
	case instance.Bot.State == "running" && instance.Bot.Health == "healthy":
		summary.Checks["health"] = "ok"
	case instance.Bot.State == "exited" || instance.Bot.State == "dead" || instance.Bot.Health == "unhealthy":
		summary.Checks["health"] = "failed"
	default:
		summary.Checks["health"] = "unknown"
	}
	if instance.Version != observed.Target || instance.Bot.State != "running" {
		summary.ActualVersion = ""
		summary.Checks["code"] = "unknown"
		return
	}
	if instance.Gateway.State != "running" {
		return
	}
	if report, err := instance.Readiness(docker); err == nil {
		summary.ActualVersion = instance.Version
		summary.Readiness = report.States
	}
}

const installLogFields = `{"driver":{{json .HostConfig.LogConfig.Type}},` +
	`"size":{{json (index .HostConfig.LogConfig.Config "max-size")}},"files":{{json (index .HostConfig.LogConfig.Config "max-file")}},` +
	`"directory":{{json (index .Config.Labels "com.docker.compose.project.working_dir")}},` +
	`"service":{{json (index .Config.Labels "com.docker.compose.service")}},` +
	`"project":{{json (index .Config.Labels "com.docker.compose.project")}},` +
	`"managed":{{json (index .Config.Labels "com.tgcloud.managed")}},"role":{{json (index .Config.Labels "com.tgcloud.role")}}}`

var (
	installLogSize  = regexp.MustCompile(`^([1-9][0-9]{0,12})([kKmMgG]?)[bB]?$`)
	installLogFiles = regexp.MustCompile(`^[1-9][0-9]{0,6}$`)
)

func InspectContainerLogRotation(id, directory, service, project, role string, docker inspect.DockerRunner) string {
	raw, err := docker("container", "inspect", "--format", installLogFields, id)
	var limits struct {
		Driver                                                  *string
		Size, Files, Directory, Service, Project, Managed, Role string
	}
	if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &limits) != nil ||
		limits.Directory != directory || limits.Service != service || limits.Project != project ||
		(role != "" && (limits.Managed != "true" || limits.Role != role)) || limits.Driver == nil {
		return "unknown"
	}
	if *limits.Driver != "json-file" {
		return "other"
	}
	size := installLogSize.FindStringSubmatch(limits.Size)
	if size == nil || !installLogFiles.MatchString(limits.Files) {
		return "pending"
	}
	scale := map[string]uint64{"": 1, "k": 1024, "m": 1024 * 1024, "g": 1024 * 1024 * 1024}
	bytes, _ := strconv.ParseUint(size[1], 10, 64)
	files, _ := strconv.Atoi(limits.Files)
	if bytes <= (10*1024*1024)/scale[strings.ToLower(size[2])] && files <= 3 {
		return "ok"
	}
	return "custom"
}

func verifyInstalledDestination(directory, target string, progress io.Writer) error {
	if _, err := inspect.DockerPreflight(false); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	discover := func(args ...string) ([]byte, error) {
		return inspect.RunDockerContext(ctx, true, nil, 15*time.Second, nil, args...)
	}
	writeProbe := func(args ...string) ([]byte, error) {
		// Capture both streams. Only the standard verifier's bounded stage
		// markers are echoed; remote paths and storage error bodies stay private.
		var output hostutil.BoundedBuffer
		_, err := inspect.RunDockerContext(ctx, true, &output, 600*time.Second, nil, args...)
		return output.Bytes(), err
	}
	return verifyInstallWebDAV(directory, target, discover, writeProbe, progress)
}

var installWebDAVStage = regexp.MustCompile(`^TGCLOUD_(WEBDAV_(AUTH|LIST|WRITE|SIZE|MOVE|DELETE)|WEBDAV|UPLOAD|RENAME|DELETE)=(OK|FAILED)$`)

func verifyInstallWebDAV(directory, target string, discover, writeProbe inspect.DockerRunner, progress io.Writer) error {
	instance, err := inspect.Discover(directory, discover)
	if err != nil || instance == nil || instance.Version != target ||
		instance.Bot.State != "running" || instance.Gateway.State != "running" {
		return errors.New("无法核对 Bot、网关和目标版本；未执行 WebDAV 写入验收")
	}
	output, err := writeProbe("exec", instance.Bot.ID, "python", "-m", "app.verify_destination")
	accepted, failed := false, false
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "TGCLOUD_DESTINATION=OK":
			accepted = true
		case strings.HasPrefix(line, "TGCLOUD_DESTINATION=FAILED"):
			failed = true
		case installWebDAVStage.MatchString(line):
			fmt.Fprintln(progress, line)
			failed = failed || strings.HasSuffix(line, "=FAILED")
		case strings.HasPrefix(line, "TGCLOUD_CLEANUP_WARNING="):
			fmt.Fprintln(progress, "TGCLOUD_CLEANUP_WARNING=UNCONFIRMED")
			failed = true
		}
	}
	if err != nil || failed || !accepted {
		fmt.Fprintln(progress, "TGCLOUD_DESTINATION=FAILED")
		return errors.New("WebDAV 写入验收未通过；已完成的基础部署保留")
	}
	fmt.Fprintln(progress, "TGCLOUD_DESTINATION=OK")
	return nil
}
