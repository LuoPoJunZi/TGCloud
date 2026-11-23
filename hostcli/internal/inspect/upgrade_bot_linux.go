package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const upgradeBotJournal = ".upgrade-bot"

type upgradeBot struct {
	Directory string
	ID        string
	Image     string
	Tag       string
	Running   bool
}

func checkUpgradeBotStage(directory, stage string) error {
	if !separateInstallStage(directory, stage) {
		return errors.New("旧 Bot 状态暂存目录无效")
	}
	if err := checkUpgradeDirectory(directory); err != nil {
		return err
	}
	return checkUpgradeDirectory(stage)
}

func inspectUpgradeBot(directory string, docker DockerRunner) (*upgradeBot, error) {
	current, err := findGatewayContainer(directory, botName, docker)
	if err != nil {
		return nil, errors.New("无法核实现有 Bot 的容器归属")
	}
	if current == nil {
		return nil, nil
	}
	id := current.Container.ID
	result, err := docker("inspect", "--format", "{{.Image}}|{{.Config.Image}}|{{.State.Running}}", id)
	fields := strings.Split(strings.TrimSpace(string(result)), "|")
	if err != nil || len(fields) != 3 || fields[0] != current.Image || !imageReference.MatchString(fields[1]) ||
		(fields[2] != "true" && fields[2] != "false") {
		return nil, errors.New("无法核实旧 Bot 的镜像或运行状态")
	}
	return &upgradeBot{directory, id, fields[0], fields[1], fields[2] == "true"}, nil
}

func writeUpgradeBot(stage string, old *upgradeBot) error {
	invalid := errors.New("无法保存旧 Bot 的运行状态")
	file, err := os.CreateTemp(stage, upgradeBotJournal+"-*")
	if err != nil {
		return invalid
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(old)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return invalid
	}
	if err := renameNoReplace(file.Name(), filepath.Join(stage, upgradeBotJournal)); err != nil {
		return invalid
	}
	return nil
}

func QuiesceUpgradeBot(directory, stage string, output io.Writer) error {
	if err := checkUpgradeBotStage(directory, stage); err != nil {
		return err
	}
	old, err := inspectUpgradeBot(directory, RunDocker)
	if err != nil {
		return err
	}
	if old == nil {
		_, err = fmt.Fprintln(output, "|")
		return err
	}
	// Recovery must know the previous state even if Docker stops the container
	// and then reports an error or the installer receives a signal.
	if err := writeUpgradeBot(stage, old); err != nil {
		return err
	}
	if old.Running {
		if _, err := RunDocker("stop", "--time", "30", old.ID); err != nil {
			return errors.New("停止旧 Bot 失败，已停止升级")
		}
		state, err := RunDocker("inspect", "--format", "{{.State.Running}}", old.ID)
		if err != nil || strings.TrimSpace(string(state)) != "false" {
			return errors.New("无法确认旧 Bot 已停止，已停止升级")
		}
	}
	_, err = fmt.Fprintf(output, "%s|%s\n", old.Image, old.Tag)
	return err
}

func ResumeUpgradeBot(directory, stage string, progress io.Writer) error {
	if err := checkUpgradeBotStage(directory, stage); err != nil {
		return err
	}
	file, err := openPrivate(filepath.Join(stage, upgradeBotJournal), syscall.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	invalid := errors.New("无法读取旧 Bot 的运行状态，请核对容器")
	if err != nil {
		return invalid
	}
	defer file.Close()
	var old upgradeBot
	if err := json.NewDecoder(io.LimitReader(file, 4096)).Decode(&old); err != nil || old.Directory != directory ||
		!containerID.MatchString(old.ID) || !imageID.MatchString(old.Image) || !imageReference.MatchString(old.Tag) {
		return invalid
	}
	return resumeUpgradeBot(&old, progress, RunDocker)
}

func resumeUpgradeBot(old *upgradeBot, progress io.Writer, docker DockerRunner) error {
	if !old.Running {
		return nil
	}
	current, err := inspectUpgradeBot(old.Directory, docker)
	if err != nil || current == nil || current.ID != old.ID || current.Image != old.Image || current.Tag != old.Tag {
		return errors.New("无法核实待恢复的是原 Bot 容器")
	}
	if !current.Running {
		if _, err := docker("start", old.ID); err != nil {
			return errors.New("原 Bot 容器启动失败")
		}
	}
	if waitContainerHealth(old.ID, docker) != "healthy" {
		return errors.New("原 Bot 未恢复健康，请检查容器日志")
	}
	if current.Running {
		fmt.Fprintln(progress, "[TGCloud] 原 Bot 仍在运行，现有程序、配置和数据库保留。")
	} else {
		fmt.Fprintln(progress, "[TGCloud] 已恢复原 Bot，现有程序、配置和数据库保留。")
	}
	return nil
}
