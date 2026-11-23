package inspect

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type installPhase int

const (
	stagingInstall installPhase = iota
	quiescingUpgrade
	publishingUpgrade
	acceptedInstall
)

type installTransaction struct {
	InstallPlan
	temps              InstallTemps
	phase              installPhase
	executable         string
	output             io.Writer
	archive, database  string
	oldImage, oldTag   string
	originalSource     string
	snapshotInProgress bool
}

func RunInstallTransaction(plan InstallPlan, output io.Writer) error {
	syscall.Umask(0077)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	executable, err := os.Executable()
	if err != nil {
		return errors.New("无法找到受管 Go 安装工具")
	}
	tx := installTransaction{InstallPlan: plan, temps: InstallTemps{Directory: plan.Directory}, executable: executable, output: output}
	return tx.finish(ctx, tx.execute(ctx))
}

func (tx *installTransaction) finish(ctx context.Context, err error) error {
	if err != nil && tx.phase != acceptedInstall {
		if ctx.Err() != nil {
			err = errors.New("安装被中断")
		}
		err = tx.recover(err)
	}
	if tx.temps.Stage != "" || tx.temps.Candidate != "" {
		if tx.cleanup(tx.temps) != nil {
			fmt.Fprintln(tx.output, "[TGCloud] 警告：部分安装暂存资源未能清理，请核对上方日志")
		}
	}
	if err == nil {
		fmt.Fprintf(tx.output, "TGCLOUD_INSTALL_DIR=%s\nTGCLOUD_BOT_HEALTH=healthy\nTGCLOUD_RESULT=SUCCESS\n", tx.Directory)
	}
	return err
}

func (tx *installTransaction) checkPlan() error {
	if err := checkPlanningDirectory(tx.Directory); err != nil {
		return err
	}
	if tx.Action != "install" && tx.Action != "upgrade" {
		return errors.New("一键部署操作类型无效")
	}
	if !filepath.IsAbs(tx.Source) || filepath.Clean(tx.Source) != tx.Source ||
		tx.Source == tx.Directory || strings.HasPrefix(tx.Source, tx.Directory+"/") || strings.HasPrefix(tx.Directory, tx.Source+"/") {
		return errors.New("部署源目录和安装目录无效或重叠")
	}
	if err := tx.checkSource(tx.Source); err != nil {
		return err
	}
	if tx.Action == "upgrade" {
		marker, err := verifyDeploymentSource(tx.Directory, "ready")
		if err != nil {
			return errors.New("无法核实现有一键部署的来源，停止升级")
		}
		tx.originalSource = marker.SourceSHA256
		return tx.checkOriginalConfig()
	}
	if entries, err := os.ReadDir(tx.Directory); !errors.Is(err, os.ErrNotExist) && (err != nil || len(entries) != 0) {
		return errors.New("新装目标目录已有文件，请核对现有内容")
	}
	return nil
}

func (tx *installTransaction) checkSource(directory string) error {
	marker, err := verifyPendingSource(directory)
	if err != nil || marker.SourceSHA256 != tx.SourceDigest || marker.Version != tx.Version {
		return errors.New("部署文件来源、版本或摘要与本次计划不一致")
	}
	return nil
}

func (tx *installTransaction) checkOriginalConfig() error {
	data, err := readInstallConfig(filepath.Join(tx.Directory, ".env"))
	if err != nil || sha256Text(data) != tx.OriginalConfigDigest {
		return errors.New("原 .env 内容与本次升级计划不一致")
	}
	return nil
}

func (tx *installTransaction) execute(ctx context.Context) error {
	if err := tx.checkPlan(); err != nil {
		return err
	}
	if _, err := DockerPreflight(true); err != nil {
		return err
	}
	if err := tx.step(ctx, "安装前资源检查未通过", "check-install-resources", tx.Directory); err != nil {
		return err
	}
	mode, err := tx.capture(ctx, "无法准备部署配置", "prepare-install-config", tx.Directory, tx.Config)
	if err != nil {
		return err
	}
	if tx.Action == "upgrade" && mode == "preserved" {
		fmt.Fprintln(tx.output, "TGCLOUD_CONFIG=PRESERVED")
	} else if tx.Action != "install" || mode != "fresh" {
		return errors.New("部署配置或实例状态与已确认的计划不一致")
	}
	if err := tx.cancellable(ctx, "宿主依赖准备失败", "prepare-dependencies", tx.Action); err != nil {
		return err
	}
	if err := tx.step(ctx, "宿主复核未通过", "check-install-host", tx.Directory, tx.Config); err != nil {
		return err
	}
	if err := tx.stage(ctx); err != nil {
		return err
	}
	if tx.Action == "upgrade" {
		if err := tx.backUp(ctx); err != nil {
			return err
		}
	}
	return tx.publish(ctx)
}

func (tx *installTransaction) stage(ctx context.Context) error {
	fmt.Fprintln(tx.output, "[TGCloud] 准备持久化目录和候选配置")
	var err error
	if tx.Action == "upgrade" {
		tx.temps.Stage, err = tx.capture(ctx, "升级部署包暂存失败", "stage-upgrade", tx.Directory, tx.Source)
	} else {
		tx.temps.Stage, err = tx.capture(ctx, "新装部署包暂存失败", "fresh-install", "stage", tx.Directory, tx.Source, tx.Config)
	}
	if err != nil {
		return err
	}
	if !separateInstallStage(tx.Directory, tx.temps.Stage) {
		return errors.New("无法核实候选部署目录")
	}
	if err := tx.checkSource(tx.temps.Stage); err != nil {
		return err
	}
	if tx.Action == "upgrade" {
		if err := tx.step(ctx, "无法准备持久化目录和候选配置", "install-layout", "prepare", tx.Directory, tx.temps.Stage, tx.Config); err != nil {
			return err
		}
	}
	tx.temps.Candidate = fmt.Sprintf("tgcloud-clouddrive2-candidate:%d-%d", time.Now().Unix(), os.Getpid())
	fmt.Fprintln(tx.output, "[TGCloud] 在隔离目录预检新程序和配置")
	if err := tx.cancellable(ctx, "候选 Compose 或镜像预检失败，尚未停止原 Bot 或替换程序文件", "preflight-candidate", tx.temps.Stage); err != nil {
		return err
	}
	return tx.cancellable(ctx, "候选 Bot 构建或校验失败", "build-candidate", tx.temps.Stage, tx.temps.Candidate)
}

func (tx *installTransaction) backUp(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.checkOriginalConfig(); err != nil {
		return err
	}
	if marker, err := verifyDeploymentSource(tx.Directory, "ready"); err != nil || marker.SourceSHA256 != tx.originalSource {
		return errors.New("候选预检期间已安装程序或来源发生变化，尚未停止原 Bot")
	}
	if err := checkUpgradeDirectory(upgradeBackupDir); err != nil {
		return err
	}
	partial, err := openInstallTemporary(upgradeBackupDir, "config-", ".tar.gz.partial", &tx.temps.ConfigPartial)
	if err != nil {
		return err
	}
	if err := partial.Close(); err != nil {
		return err
	}
	tx.archive = strings.TrimSuffix(tx.temps.ConfigPartial, ".partial")
	fmt.Fprintln(tx.output, "[TGCloud] 备份现有程序配置到", tx.archive)
	if err := tx.cancellable(ctx, "配置备份失败，尚未替换程序文件", "commit-upgrade-backup", tx.Directory, tx.temps.ConfigPartial); err != nil {
		return err
	}
	tx.phase = quiescingUpgrade
	image, err := tx.capture(ctx, "升级前旧 Bot 停机准备失败", "quiesce-upgrade", tx.Directory, tx.temps.Stage)
	if err != nil {
		return err
	}
	var found bool
	tx.oldImage, tx.oldTag, found = strings.Cut(image, "|")
	if !found {
		return errors.New("无法核实旧 Bot 的镜像")
	}
	if err := tx.backUpDatabase(ctx); err != nil {
		fmt.Fprintln(tx.output, "TGCLOUD_DATABASE_BACKUP=FAILED")
		return fmt.Errorf("SQLite 一致性快照未通过：%w", err)
	}
	return nil
}

func (tx *installTransaction) backUpDatabase(ctx context.Context) error {
	// Keep the shared SQLite file/sidecar identity policy in the existing helper.
	data := filepath.Join(tx.Directory, "data")
	check := exec.CommandContext(ctx, "python3", "-I", "-S", "-B", filepath.Join(tx.temps.Stage, "app/tgcloud/persistence/paths.py"), data)
	isolateInstallCommand(check, true)
	if result, err := check.Output(); err != nil || string(result) != "tgcloud.db\n" {
		return errors.New("无法核实业务数据库身份")
	}
	if _, err := os.Lstat(filepath.Join(data, "tgcloud.db")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("无法读取业务数据库状态")
	}
	partial, err := openInstallTemporary(upgradeBackupDir, "database-", ".db.partial", &tx.temps.DatabasePartial)
	if err != nil {
		return err
	}
	if err := partial.Close(); err != nil {
		return err
	}
	tx.temps.SnapshotName = fmt.Sprintf(".upgrade-backup-%d.db", os.Getpid())
	fmt.Fprintln(tx.output, "[TGCloud] 创建并校验升级前数据库一致性快照")
	tx.snapshotInProgress = true
	if err := tx.cancellable(ctx, "SQLite 快照生成失败", "snapshot-database", tx.Directory, tx.temps.Candidate,
		tx.temps.SnapshotName, tx.temps.DatabasePartial); err != nil {
		return err
	}
	tx.snapshotInProgress = false
	if err := tx.cancellable(ctx, "SQLite 备份发布失败", "commit-upgrade-backup", tx.Directory, tx.temps.DatabasePartial); err != nil {
		return err
	}
	tx.database = strings.TrimSuffix(tx.temps.DatabasePartial, ".partial")
	fmt.Fprintln(tx.output, "TGCLOUD_DATABASE_BACKUP=OK")
	return nil
}

func (tx *installTransaction) publish(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(tx.output, "[TGCloud] 提交已预检的程序和配置")
	if tx.Action == "upgrade" {
		tx.phase = publishingUpgrade
		if err := tx.step(context.WithoutCancel(ctx), "升级程序文件替换失败", "apply-upgrade", tx.Directory, tx.temps.Stage, "false"); err != nil {
			return err
		}
		if err := tx.step(ctx, "无法设置持久化目录权限", "install-layout", "finalize", tx.Directory); err != nil {
			return err
		}
	} else if err := tx.step(ctx, "新装程序发布失败", "fresh-install", "publish", tx.Directory, tx.temps.Stage); err != nil {
		return err
	}
	gateway, err := tx.capture(ctx, "正式 Compose 配置检查失败", "check-install-compose", tx.Directory)
	if err != nil {
		return err
	}
	if gateway == "true" {
		if err := tx.step(ctx, "CloudDrive2 容器启动或复用失败", "start-gateway", tx.Directory); err != nil {
			return err
		}
	} else if gateway != "false" {
		return errors.New("无法核实正式部署的 CloudDrive2 配置")
	}
	if err := tx.run(ctx, gracefulInstallCommand, tx.output, "正式 Bot 部署失败", "deploy-bot", tx.Directory, tx.temps.Candidate); err != nil {
		return err
	}
	if gateway == "true" {
		fmt.Fprintln(tx.output, "[TGCloud] 迁移或修复 CloudDrive2 Docker 网络并执行硬性连通检查")
		if err := tx.step(ctx, "CloudDrive2 网络修复或连通检查失败", "repair-gateway-network", tx.Directory, tx.temps.Stage); err != nil {
			return err
		}
	}
	fmt.Fprintln(tx.output, "[TGCloud] 核对运行中的环境配置、代码指纹和基础健康状态")
	if err := tx.checkSource(tx.Directory); err != nil {
		return err
	}
	if tx.Action == "upgrade" {
		if err := tx.checkOriginalConfig(); err != nil {
			return err
		}
	}
	// The existing outer Python acceptance requires both containers even when
	// the configured gateway is reused instead of started by this deployment.
	if err := tx.step(ctx, "Bot 验收或安装激活失败", "accept-bot", tx.Directory, "--check-gateway"); err != nil {
		return err
	}
	tx.phase = acceptedInstall
	if err := tx.step(context.Background(), "安装已通过验收，但无法生成部署摘要", "report-install", tx.Directory); err != nil {
		return err
	}
	return nil
}

func (tx *installTransaction) recover(cause error) error {
	ctx := context.Background()
	if tx.snapshotInProgress && tx.cleanup(InstallTemps{Directory: tx.Directory, Candidate: tx.temps.Candidate}) != nil {
		cause = errors.Join(cause, errors.New("SQLite 暂存容器清理未完成，请核对 Docker"))
	}
	if tx.temps.Stage != "" {
		if _, err := os.Lstat(filepath.Join(tx.temps.Stage, gatewayNetworkJournal)); err == nil {
			cause = errors.Join(cause, tx.step(ctx, "CloudDrive2 网络恢复未完成", "restore-gateway-network", tx.Directory, tx.temps.Stage))
		}
	}
	switch tx.phase {
	case quiescingUpgrade:
		return errors.Join(cause, tx.step(ctx, "原 Bot 未恢复健康，请检查容器日志", "resume-upgrade", tx.Directory, tx.temps.Stage))
	case publishingUpgrade:
		fmt.Fprintln(tx.output, "[TGCloud] 新版本未通过验收，开始恢复升级前的程序、配置和数据库")
		if err := tx.rollback(ctx); err != nil {
			return errors.Join(cause, err, errors.New("自动恢复未完成，请保留备份并人工处理"))
		}
		return fmt.Errorf("%w。已自动恢复升级前版本", cause)
	}
	return cause
}

func (tx *installTransaction) rollback(ctx context.Context) error {
	if tx.database != "" {
		if err := prepareRollbackCopy(tx.Directory, tx.database, &tx.temps.RollbackDatabase); err != nil {
			return err
		}
	}
	return tx.step(ctx, "升级回滚失败", "rollback-upgrade", tx.Directory, tx.archive, tx.temps.RollbackDatabase, tx.oldImage, tx.oldTag)
}

func (tx *installTransaction) cleanup(temps InstallTemps) error {
	return tx.step(context.Background(), "安装暂存资源清理失败", "cleanup-install", temps.Directory, temps.Stage, temps.Candidate,
		temps.ConfigPartial, temps.DatabasePartial, temps.SnapshotName, temps.RollbackDatabase)
}

func openInstallTemporary(directory, prefix, suffix string, result *string) (*os.File, error) {
	invalid := errors.New("无法创建私有备份暂存文件")
	if resolved, err := filepath.EvalSymlinks(directory); err != nil || resolved != directory {
		return nil, invalid
	}
	var token [4]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, invalid
	}
	path := filepath.Join(directory, fmt.Sprintf("%s%x%s", prefix, token, suffix))
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, invalid
	}
	*result = path
	return file, nil
}

func prepareRollbackCopy(directory, database string, path *string) error {
	invalid := errors.New("无法校验并准备 SQLite 回滚副本")
	if err := validateUpgradeSnapshot(database); err != nil {
		return invalid
	}
	source, err := openPrivate(database, syscall.O_RDONLY, 0)
	if err != nil {
		return invalid
	}
	defer source.Close()
	file, err := openInstallTemporary(filepath.Join(directory, "data"), ".rollback-", ".db", path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, source); err != nil || file.Sync() != nil || validateUpgradeSnapshot(*path) != nil || file.Chown(10001, 10001) != nil {
		return invalid
	}
	return nil
}

type installCommandMode int

const (
	protectedInstallCommand installCommandMode = iota
	cancellableInstallCommand
	gracefulInstallCommand
)

func (tx *installTransaction) step(ctx context.Context, message string, args ...string) error {
	return tx.run(ctx, protectedInstallCommand, tx.output, message, args...)
}

func (tx *installTransaction) cancellable(ctx context.Context, message string, args ...string) error {
	return tx.run(ctx, cancellableInstallCommand, tx.output, message, args...)
}

func (tx *installTransaction) capture(ctx context.Context, message string, args ...string) (string, error) {
	var output bytes.Buffer
	err := tx.run(ctx, protectedInstallCommand, &output, message, args...)
	return strings.TrimSpace(output.String()), err
}

func (tx *installTransaction) run(ctx context.Context, mode installCommandMode, output io.Writer, message string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var command *exec.Cmd
	if mode == protectedInstallCommand {
		command = exec.Command(tx.executable, args...)
	} else {
		command = exec.CommandContext(ctx, tx.executable, args...)
	}
	isolateInstallCommand(command, mode == cancellableInstallCommand)
	if len(args) > 0 && args[0] == "prepare-dependencies" {
		command.Cancel = func() error {
			err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
	}
	if mode == gracefulInstallCommand {
		// The formal deployment worker waits for live Docker changes to finish.
		command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
		command.WaitDelay = 0
	}
	command.Stdout, command.Stderr = output, tx.output
	if err := command.Run(); err != nil {
		return errors.New(message)
	}
	return nil
}
