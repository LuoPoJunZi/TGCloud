package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

const usage = "用法：tgcloud-host {status|check|logs}\n" +
	"      tgcloud-host {status|check} --json\n" +
	"      tgcloud-host {start|stop|restart} {bot|gateway} --yes\n" +
	"      tgcloud-host preflight [--allow-missing]\n" +
	"      tgcloud-host resources <install-dir> <backup-dir>\n" +
	"      tgcloud-host prepare-image <image@sha256:digest>\n" +
	"      tgcloud-host prepare-install-config <install-dir> <config-file>\n" +
	"      tgcloud-host preflight-candidate <stage-dir>\n" +
	"      tgcloud-host check-install-compose <install-dir>\n" +
	"      tgcloud-host discover-install [<install-dir>]\n" +
	"      tgcloud-host install-source {snapshot|verify|payload|mark} ...\n" +
	"      tgcloud-host install-acceptance {summary|webdav} ...\n" +
	"      tgcloud-host https-entry {probe|status|configure} ...\n" +
	"      tgcloud-host lan-entry {state|check|save|activate} ...\n" +
	"      tgcloud-host manager-runtime {check|publish} ...\n" +
	"      tgcloud-host installer-preflight {host|docker|storage|write-config} ...\n" +
	"      tgcloud-host plan-install <dir> <present> <version> <source-sha256> <config-sha256> <gateway-image> <target-source> <target-version> <target-sha256>\n" +
	"      tgcloud-host install-transaction <install-dir> <payload-dir> <config-file> <install|upgrade> <version> <source-sha256> <original-config-sha256>\n" +
	"      tgcloud-host build-candidate <stage-dir> <candidate-tag>\n" +
	"      tgcloud-host stage-upgrade <install-dir> <payload-dir>\n" +
	"      tgcloud-host backup-config <install-dir> <staged-archive>\n" +
	"      tgcloud-host snapshot-database <install-dir> <candidate-tag> <snapshot-name> <staged-backup>\n" +
	"      tgcloud-host commit-upgrade-backup <install-dir> <staged-backup>\n" +
	"      tgcloud-host {quiesce-upgrade|resume-upgrade} <install-dir> <stage-dir>\n" +
	"      tgcloud-host apply-upgrade <install-dir> <stage-dir> <apply-config>\n" +
	"      tgcloud-host rollback-upgrade <install-dir> <config-backup> <staged-db> <old-image-id> <old-image-tag>\n" +
	"      tgcloud-host deploy-bot <install-dir>\n" +
	"      tgcloud-host accept-bot <install-dir> [--check-gateway]\n" +
	"      tgcloud-host report-install <install-dir>\n" +
	"      tgcloud-host start-gateway <install-dir>\n" +
	"      tgcloud-host {repair-gateway-network|restore-gateway-network} <install-dir> <stage-dir>\n" +
	"      tgcloud-host cleanup-install <install-dir> <stage-dir> <candidate-tag> <config-partial> <database-partial> <snapshot-name> <rollback-db>\n" +
	"      tgcloud-host check-install-resources <install-dir>\n" +
	"      tgcloud-host prepare-dependencies {install|upgrade}\n" +
	"      tgcloud-host check-install-host <install-dir> <config-file>\n" +
	"      tgcloud-host fresh-install stage <install-dir> <payload-dir> <config-file>\n" +
	"      tgcloud-host fresh-install publish <install-dir> <stage-dir>\n" +
	"      tgcloud-host install-layout prepare <install-dir> <stage-dir> <config-file>\n" +
	"      tgcloud-host install-layout finalize <install-dir> [<fresh-stage-dir>]"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, inspect.InstallDirectory, inspect.RunDocker))
}

func supportedExit(supported bool) int {
	if supported {
		return 0
	}
	return 1
}

func installLayoutCommand(args []string, errOut io.Writer) int {
	var err error
	switch {
	case len(args) == 1 && args[0] == "--supported":
		return supportedExit(inspect.LinuxSupported())
	case len(args) == 4 && args[0] == "prepare":
		err = inspect.PrepareInstallLayout(args[1], args[2], args[3])
	case len(args) == 2 && args[0] == "finalize":
		err = inspect.FinalizeInstallLayout(args[1], "")
	case len(args) == 3 && args[0] == "finalize":
		err = inspect.FinalizeInstallLayout(args[1], args[2])
	default:
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func freshInstallCommand(args []string, out, errOut io.Writer) int {
	var err error
	switch {
	case len(args) == 1 && args[0] == "--supported":
		return supportedExit(inspect.LinuxSupported())
	case len(args) == 4 && args[0] == "stage":
		err = inspect.StageFreshInstall(args[1], args[2], args[3], out)
	case len(args) == 3 && args[0] == "publish":
		err = inspect.PublishFreshInstall(args[1], args[2])
	default:
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func run(args []string, out, errOut io.Writer, directory string, docker inspect.DockerRunner) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(out, usage)
		return 0
	}
	if len(args) > 0 && args[0] == "preflight" && (len(args) == 1 || (len(args) == 2 && args[1] == "--allow-missing")) {
		result, err := inspect.DockerPreflight(len(args) == 2)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return writeJSON(out, errOut, result, 0)
	}
	if len(args) == 3 && args[0] == "resources" {
		report, err := inspect.ResourceProbe(args[1], args[2])
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		if _, err := fmt.Fprint(out, report); err != nil {
			return 2
		}
		return 0
	}
	if len(args) == 2 && args[0] == "prepare-image" {
		if args[1] == "--supported" {
			return supportedExit(inspect.LinuxSupported())
		}
		if err := inspect.PrepareImage(args[1]); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	}
	if len(args) == 2 && args[0] == "build-candidate" && args[1] == "--supported" {
		return supportedExit(inspect.LinuxSupported())
	}
	if len(args) == 3 && args[0] == "build-candidate" {
		if err := inspect.BuildCandidate(args[1], args[2], out); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		fmt.Fprintln(out, "TGCLOUD_CONFIG=OK")
		return 0
	}
	if len(args) > 0 && args[0] == "install-transaction" {
		return installTransaction(args, out, errOut)
	}
	if len(args) >= 2 && len(args) <= 3 && args[0] == "deploy-bot" {
		if len(args) == 2 && args[1] == "--supported" {
			return supportedExit(inspect.LinuxSupported())
		}
		var err error
		if len(args) == 3 {
			err = inspect.DeployBotForInstall(args[1], args[2], out)
		} else {
			err = inspect.DeployBot(args[1], out)
		}
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	}
	if len(args) >= 2 && args[0] == "accept-bot" && (len(args) == 2 || (len(args) == 3 && args[2] == "--check-gateway")) {
		if len(args) == 2 && args[1] == "--supported" {
			return supportedExit(inspect.LinuxSupported())
		}
		if err := inspect.AcceptBot(args[1], len(args) == 3, out); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	}
	if len(args) == 2 && args[0] == "report-install" {
		if args[1] == "--supported" {
			return supportedExit(inspect.LinuxSupported())
		}
		if err := inspect.ReportInstall(args[1], out); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	}
	if len(args) > 0 {
		switch args[0] {
		case "install-acceptance":
			if len(args) == 2 && args[1] == "--supported" {
				if inspect.InstallAcceptanceSupported() {
					return 0
				}
				return 1
			}
			result, err := inspect.InstallAcceptance(args[1:], os.Stdin, out)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			if result == nil {
				return 0
			}
			return writeJSON(out, errOut, result, 0)
		case "https-entry":
			if len(args) == 2 && args[1] == "--supported" {
				if inspect.HTTPSEntrySupported() {
					return 0
				}
				return 1
			}
			result, err := inspect.HTTPSEntry(args[1:], out)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			if result == nil {
				return 0
			}
			return writeJSON(out, errOut, result, 0)
		case "lan-entry":
			if len(args) == 2 && args[1] == "--supported" {
				if inspect.LANEntrySupported() {
					return 0
				}
				return 1
			}
			result, err := inspect.LANEntry(args[1:])
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			return writeJSON(out, errOut, result, 0)
		case "installer-preflight":
			if len(args) == 2 && args[1] == "--supported" {
				if inspect.InstallerPreflightSupported() {
					return 0
				}
				return 1
			}
			result, err := inspect.InstallerPreflight(args[1:], os.Stdin)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			return writeJSON(out, errOut, result, 0)
		case "manager-runtime":
			if len(args) == 2 && args[1] == "--supported" {
				if inspect.ManagerRuntimeSupported() {
					return 0
				}
				return 1
			}
			result, err := inspect.ManagerRuntime(args[1:], os.Stdin)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			return writeJSON(out, errOut, result, 0)
		case "install-source":
			if len(args) == 2 && args[1] == "--supported" {
				if inspect.InstallSourceSupported() {
					return 0
				}
				return 1
			}
			result, err := inspect.InstallSource(args[1:], os.Stdin)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			return writeJSON(out, errOut, result, 0)
		case "discover-install", "plan-install":
			return installPlanCommand(args, out, errOut)
		case "fresh-install":
			return freshInstallCommand(args[1:], out, errOut)
		case "prepare-install-config", "preflight-candidate", "check-install-compose":
			return installPreflightCommand(args, out, errOut)
		case "check-install-resources":
			if len(args) == 2 && args[1] == "--supported" {
				return supportedExit(inspect.LinuxSupported())
			}
			if len(args) != 2 {
				fmt.Fprintln(errOut, usage)
				return 2
			}
			if err := inspect.CheckInstallResources(args[1], out); err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			return 0
		case "check-install-host":
			if len(args) == 2 && args[1] == "--supported" {
				return supportedExit(inspect.LinuxSupported())
			}
			if len(args) != 3 {
				fmt.Fprintln(errOut, usage)
				return 2
			}
			if err := inspect.CheckInstallHost(args[1], args[2], out); err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			return 0
		case "install-layout":
			return installLayoutCommand(args[1:], errOut)
		case "prepare-dependencies":
			if len(args) == 2 && args[1] == "--supported" {
				return supportedExit(inspect.LinuxSupported())
			}
			if len(args) != 2 || (args[1] != "install" && args[1] != "upgrade") {
				fmt.Fprintln(errOut, usage)
				return 2
			}
			if err := inspect.PrepareDependencies(args[1] == "upgrade", out); err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			return 0
		case "cleanup-install":
			return cleanupCommand(args, errOut)
		case "start-gateway", "repair-gateway-network", "restore-gateway-network":
			return gatewayCommand(args, out, errOut)
		case "stage-upgrade", "backup-config", "snapshot-database", "commit-upgrade-backup", "quiesce-upgrade", "resume-upgrade", "apply-upgrade", "rollback-upgrade":
			return upgrade(args, out, errOut)
		}
	}
	if len(args) == 2 && args[1] == "--supported" && (args[0] == "start" || args[0] == "stop" || args[0] == "restart") {
		return supportedExit(inspect.LinuxSupported())
	}
	jsonOutput := len(args) == 2 && args[1] == "--json" && (args[0] == "status" || args[0] == "check")
	if jsonOutput {
		args = args[:1]
	}
	action, target, ok := parseCommand(args)
	if !ok {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if target != "" {
		lock, err := inspect.LockInstance()
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		defer lock.Close()
	}

	instance, err := inspect.Discover(directory, docker)
	if err != nil {
		fmt.Fprintln(errOut, "无法核实 TGCloud 实例："+err.Error())
		return 2
	}
	if instance == nil {
		if jsonOutput {
			if action == "status" {
				return writeJSON(out, errOut, map[string]any{"present": false}, 0)
			}
			return writeJSON(out, errOut, map[string]any{"present": false, "ready": false}, 1)
		}
		fmt.Fprintln(out, "TGCloud：未检测到一键安装实例")
		if action == "status" {
			return 0
		}
		return 1
	}
	switch action {
	case "status":
		if jsonOutput {
			result := map[string]any{"version": instance.Version,
				"bot":     map[string]string{"state": instance.Bot.State, "health": instance.Bot.Health},
				"gateway": map[string]string{"state": instance.Gateway.State, "health": instance.Gateway.Health}}
			return writeJSON(out, errOut, result, 0)
		}
		fmt.Fprintf(out, "TGCloud 运行状态\n安装目录：%s\n已安装版本：%s\n", directory, instance.Version)
		showServices(out, instance)
		return 0
	case "logs":
		return showLogs(out, errOut, directory, instance, docker)
	case "check":
		return check(out, errOut, instance, docker, jsonOutput)
	}

	service := instance.Bot
	label := "Bot"
	if target == "gateway" {
		service, label = instance.Gateway, "CloudDrive2 网关"
	}
	if _, err := docker(action, service.ID); err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	fmt.Fprintf(out, "%s：容器操作已完成。可用 status 查看运行状态。\n", label)
	return 0
}

func cleanupCommand(args []string, errOut io.Writer) int {
	if len(args) == 2 && args[1] == "--supported" {
		return supportedExit(inspect.LinuxSupported())
	}
	if len(args) != 8 {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	temps := inspect.InstallTemps{Directory: args[1], Stage: args[2], Candidate: args[3], ConfigPartial: args[4],
		DatabasePartial: args[5], SnapshotName: args[6], RollbackDatabase: args[7]}
	if err := inspect.CleanupInstall(temps); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func gatewayCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 2 && args[1] == "--supported" {
		return supportedExit(inspect.LinuxSupported())
	}
	var err error
	switch {
	case args[0] == "start-gateway" && len(args) == 2:
		err = inspect.StartGateway(args[1], out)
	case args[0] == "repair-gateway-network" && len(args) == 3:
		err = inspect.RepairGatewayNetwork(args[1], args[2], out)
	case args[0] == "restore-gateway-network" && len(args) == 3:
		err = inspect.RestoreGatewayNetwork(args[1], args[2])
	default:
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func installPlanCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 2 && args[1] == "--supported" {
		return supportedExit(inspect.LinuxSupported())
	}
	var value any
	var err error
	switch {
	case args[0] == "discover-install" && len(args) <= 2:
		directory := ""
		if len(args) == 2 {
			directory = args[1]
		}
		value, err = inspect.DiscoverInstall(directory)
	case args[0] == "plan-install" && len(args) == 10 && (args[2] == "true" || args[2] == "false"):
		expected := inspect.InstallInstance{Directory: args[1], Present: args[2] == "true", Version: args[3],
			SourceDigest: args[4], ConfigDigest: args[5], GatewayImage: args[6]}
		value, err = inspect.VerifyInstallPlan(expected, args[7], args[8], args[9])
	default:
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return writeJSON(out, errOut, value, 0)
}

func installTransaction(args []string, out, errOut io.Writer) int {
	if len(args) == 2 && args[1] == "--supported" {
		return supportedExit(inspect.LinuxSupported())
	}
	if len(args) != 8 {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	plan := inspect.InstallPlan{Directory: args[1], Source: args[2], Config: args[3], Action: args[4],
		Version: args[5], SourceDigest: args[6], OriginalConfigDigest: args[7]}
	if err := inspect.RunInstallTransaction(plan, out); err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintln(errOut, "[TGCloud][ERROR] "+line)
		}
		fmt.Fprintln(errOut, "TGCLOUD_RESULT=FAILED")
		return 1
	}
	return 0
}

func installPreflightCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 2 && args[1] == "--supported" {
		return supportedExit(inspect.LinuxSupported())
	}
	var err error
	switch {
	case args[0] == "prepare-install-config" && len(args) == 3:
		err = inspect.PrepareInstallConfig(args[1], args[2], out)
	case args[0] == "preflight-candidate" && len(args) == 2:
		err = inspect.PreflightCandidate(args[1])
	case args[0] == "check-install-compose" && len(args) == 2:
		err = inspect.CheckInstallCompose(args[1], out)
	default:
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func upgrade(args []string, out, errOut io.Writer) int {
	if len(args) == 2 && args[1] == "--supported" {
		return supportedExit(inspect.LinuxSupported())
	}
	var err error
	switch {
	case args[0] == "stage-upgrade" && len(args) == 3:
		err = inspect.StageUpgrade(args[1], args[2], out)
	case args[0] == "backup-config" && len(args) == 3:
		err = inspect.BackupUpgradeConfig(args[1], args[2])
	case args[0] == "snapshot-database" && len(args) == 5:
		err = inspect.SnapshotUpgradeDatabase(args[1], args[2], args[3], args[4])
	case args[0] == "commit-upgrade-backup" && len(args) == 3:
		err = inspect.CommitUpgradeBackup(args[1], args[2])
	case args[0] == "quiesce-upgrade" && len(args) == 3:
		err = inspect.QuiesceUpgradeBot(args[1], args[2], out)
	case args[0] == "resume-upgrade" && len(args) == 3:
		err = inspect.ResumeUpgradeBot(args[1], args[2], out)
	case args[0] == "apply-upgrade" && len(args) == 4 && (args[3] == "true" || args[3] == "false"):
		err = inspect.ApplyUpgradeFiles(args[1], args[2], args[3] == "true")
	case args[0] == "rollback-upgrade" && len(args) == 6:
		err = inspect.RollbackUpgrade(args[1], args[2], args[3], args[4], args[5])
	default:
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func writeJSON(out, errOut io.Writer, value any, code int) int {
	if err := json.NewEncoder(out).Encode(value); err != nil {
		fmt.Fprintln(errOut, "无法输出检查结果")
		return 2
	}
	return code
}

func parseCommand(args []string) (action, target string, ok bool) {
	if len(args) == 1 {
		switch args[0] {
		case "status", "check", "logs":
			return args[0], "", true
		}
	}
	if len(args) == 3 && args[2] == "--yes" && (args[1] == "bot" || args[1] == "gateway") {
		switch args[0] {
		case "start", "stop", "restart":
			return args[0], args[1], true
		}
	}
	return "", "", false
}

func check(out, errOut io.Writer, instance *inspect.Instance, docker inspect.DockerRunner, jsonOutput bool) int {
	if !jsonOutput {
		fmt.Fprintln(out, "TGCloud 业务就绪检查")
	}
	if instance.Bot.State != "running" || instance.Gateway.State != "running" {
		if jsonOutput {
			return writeJSON(out, errOut, instance.UnreadyReport(), 1)
		}
		showServices(out, instance)
		fmt.Fprintln(out, "业务就绪：服务尚未全部运行")
		return 1
	}
	report, err := instance.Readiness(docker)
	if err != nil {
		fmt.Fprintln(errOut, "业务就绪未核实："+err.Error())
		return 2
	}
	if jsonOutput {
		code := 1
		if report.Ready {
			code = 0
		}
		return writeJSON(out, errOut, report.Payload, code)
	}
	for _, check := range report.Checks {
		fmt.Fprintf(out, "%s：%s\n", check.Label, check.Status)
	}
	if !report.Ready {
		fmt.Fprintln(out, "业务就绪：尚未全部确认")
		return 1
	}
	fmt.Fprintln(out, "业务就绪：基础条件通过（最近观察，不代表写入）")
	return 0
}

func showLogs(out, errOut io.Writer, directory string, instance *inspect.Instance, docker inspect.DockerRunner) int {
	clean, err := inspect.LogRedactor(directory)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	fmt.Fprintln(out, "TGCloud 运行日志（最近 30 分钟，每个服务最多 100 行）")
	exitCode := 0
	for _, service := range []struct{ label, id string }{
		{"Bot", instance.Bot.ID},
		{"CloudDrive2 网关", instance.Gateway.ID},
	} {
		fmt.Fprintln(out, "\n── "+service.label+" 日志 ──")
		logs, err := docker("logs", "--since", "30m", "--tail", "100", service.id)
		if err != nil {
			fmt.Fprintln(errOut, service.label+" 日志读取失败："+err.Error())
			exitCode = 2
			continue
		}
		text := clean(string(logs))
		if text == "" {
			text = "最近 30 分钟暂无日志。"
		}
		fmt.Fprintln(out, text)
	}
	return exitCode
}

func showServices(out io.Writer, instance *inspect.Instance) {
	fmt.Fprintln(out, "Bot："+serviceStatus(instance.Bot))
	fmt.Fprintln(out, "CloudDrive2 网关："+serviceStatus(instance.Gateway))
}

func serviceStatus(container inspect.Container) string {
	state := map[string]string{
		"created": "已创建", "running": "运行中", "exited": "已停止",
		"dead": "已停止", "paused": "已暂停", "restarting": "重启中", "removing": "移除中",
	}[container.State]
	if state == "" {
		return "状态未核实"
	}
	if container.State != "running" {
		return state
	}
	health := map[string]string{
		"healthy": "通过", "unhealthy": "未通过", "starting": "检查中", "": "未配置",
	}[container.Health]
	if health == "" {
		health = "未核实"
	}
	return state + "（健康检查：" + health + "）"
}
