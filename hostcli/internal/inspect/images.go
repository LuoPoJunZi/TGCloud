package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const imagePlatform = `{"os":{{json .Os}},"arch":{{json .Architecture}}}`

var pinnedImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./:-]*@sha256:[a-f0-9]{64}$`)

func PrepareImage(reference string) error {
	if len(reference) > 300 || !pinnedImage.MatchString(reference) {
		return errors.New("候选镜像必须使用固定 SHA256 摘要")
	}
	docker, err := DockerPreflight(false)
	if err != nil {
		return err
	}
	return ensureNativeImage(reference, *docker, RunDocker)
}

func ensureNativeImage(reference string, native PreflightResult, docker DockerRunner) error {
	if imageIsNative(reference, native.Architecture, docker) {
		return nil
	}
	if _, err := docker("pull", "--platform", native.Platform, reference); err != nil {
		return fmt.Errorf("无法取得原生 %s 候选镜像", native.Platform)
	}
	if !imageIsNative(reference, native.Architecture, docker) {
		return errors.New("候选镜像拉取后未通过原生平台核验")
	}
	return nil
}

func imageIsNative(reference, architecture string, docker DockerRunner) bool {
	output, err := docker("image", "inspect", "--format", imagePlatform, reference)
	var info struct{ OS, Arch string }
	return err == nil && json.Unmarshal(output, &info) == nil &&
		info.OS == "linux" && nativeArchitecture(info.Arch) == architecture
}
