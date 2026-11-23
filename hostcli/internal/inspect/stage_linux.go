package inspect

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func StageUpgrade(directory, source string, result io.Writer) error {
	if err := checkInstallLayoutPath(directory); err != nil {
		return err
	}
	if _, err := standaloneVersion(directory, "ready"); err != nil {
		return errors.New("升级暂存要求已激活的一键安装目录")
	}
	_, err := stageInstallProgram(directory, source, result)
	return err
}

func stageInstallProgram(directory, source string, result io.Writer) (_ string, err error) {
	var token [4]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", errors.New("无法生成候选部署目录名称")
	}
	stage := fmt.Sprintf("/opt/tgcloud-release-%x", token)
	if !separateInstallStage(directory, stage) || os.Mkdir(stage, 0700) != nil {
		return "", errors.New("无法创建私有候选部署目录")
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeInstallStage(stage))
		}
	}()
	// Let the Shell exit handler track the directory while the copy is in progress.
	if _, err := fmt.Fprintln(result, stage); err != nil {
		return "", errors.New("无法返回候选部署目录")
	}
	if err := copyInstallProgram(source, stage); err != nil {
		return "", err
	}
	return stage, nil
}

func copyInstallProgram(source, destination string) error {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return errors.New("部署包路径无效")
	}
	marker, err := verifyPendingSource(source)
	if err != nil {
		return errors.New("部署包文件清单或内容无法核实")
	}
	for name, expected := range marker.Files {
		data, err := readSourceFile(filepath.Join(source, name), 2*1024*1024)
		if err != nil || sha256Text(data) != expected {
			return errors.New("部署包文件在暂存期间发生变化")
		}
		target := filepath.Join(destination, name)
		if os.MkdirAll(filepath.Dir(target), 0755) != nil || os.WriteFile(target, data, 0600) != nil || os.Chmod(target, 0644) != nil {
			return errors.New("无法复制部署包文件到暂存目录")
		}
	}
	if err := filepath.WalkDir(filepath.Join(destination, "app"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			return os.Chmod(path, 0755)
		}
		return err
	}); err != nil {
		return errors.New("无法设置候选程序目录权限")
	}
	data, err := readSourceFile(filepath.Join(source, sourceMarkerName), 256*1024)
	if err != nil || os.WriteFile(filepath.Join(destination, sourceMarkerName), data, 0600) != nil {
		return errors.New("无法暂存部署来源记录")
	}
	if _, err := verifyPendingSource(destination); err != nil {
		return errors.New("复制后的部署包未通过来源核验")
	}
	return nil
}
