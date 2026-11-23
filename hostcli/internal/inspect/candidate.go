package inspect

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
)

var (
	candidateStage = regexp.MustCompile(`^/opt/tgcloud-release-[A-Za-z0-9]{8}$`)
	candidateTag   = regexp.MustCompile(`^tgcloud-clouddrive2-candidate:[0-9]+-[0-9]+$`)
)

func BuildCandidate(stage, tag string, progress io.Writer) error {
	if !candidateStage.MatchString(stage) || !candidateTag.MatchString(tag) {
		return errors.New("候选 Bot 暂存目录或临时镜像标签无效")
	}
	if err := verifyCandidateStage(stage); err != nil {
		return err
	}
	native, err := DockerPreflight(false)
	if err != nil {
		return err
	}
	return buildCandidate(stage, tag, *native, func(args ...string) ([]byte, error) {
		if args[0] == "build" {
			return runDocker(true, progress, args...)
		}
		return RunDocker(args...)
	})
}

func buildCandidate(stage, tag string, native PreflightResult, docker DockerRunner) error {
	if _, err := docker("build", "--platform", native.Platform, "--tag", tag, stage); err != nil {
		return fmt.Errorf("候选 Bot 镜像构建失败：%w", err)
	}
	if !imageIsNative(tag, native.Architecture, docker) {
		return errors.New("候选 Bot 镜像未通过原生平台核验")
	}
	if _, err := docker("run", "--rm", "--label", candidateLabel+"="+tag, "--read-only", "--network", "none", "--tmpfs", "/tmp",
		"--entrypoint", "python", "--env-file", filepath.Join(stage, ".env"), tag,
		"-m", "app.deployment_check", "--validate-only"); err != nil {
		return errors.New("候选 Bot 配置校验失败，请核对配置格式、必填项和 Docker 运行环境")
	}
	return nil
}
