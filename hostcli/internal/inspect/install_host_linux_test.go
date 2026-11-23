package inspect

import (
	"io"
	"strings"
	"testing"
)

func TestInstallPortRejectsDockerRangeAndHostListener(t *testing.T) {
	gateway := &gatewayContainer{Container: Container{State: "running"}}
	for _, test := range []struct {
		name, ports, listeners, want string
	}{
		{"owned gateway", "tgcloud-clouddrive2|127.0.0.1:19798->19798/tcp", "owned listener", ""},
		{"additional Docker range", "tgcloud-clouddrive2|127.0.0.1:19798->19798/tcp\nother|[::]:19797-19799->80-82/tcp", "", "其他容器"},
		{"foreign host process", "other|19798/tcp", "LISTEN 0 128 127.0.0.1:19798", "其他进程"},
	} {
		t.Run(test.name, func(t *testing.T) {
			docker := func(...string) ([]byte, error) { return []byte(test.ports), nil }
			err := checkInstallPort(gateway, docker, func() (string, error) { return test.listeners, nil })
			if (test.want == "" && err != nil) || (test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want))) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}

func TestInstallDiskRecheckUsesActualDeviceAndInstallMode(t *testing.T) {
	for _, test := range []struct {
		name, config, want      string
		installFree, dockerFree uint64
		separate, installed     bool
	}{
		{"shared filesystem", "", "", 8_000_000, 8_000_000, false, false},
		{"install disk low on upgrade", "", "安装文件系统可用空间不足", 7_999_999, 8_000_000, true, true},
		{"Docker disk low on new install", "", "Docker 数据文件系统空间不足", 8_000_000, 5_999_999, true, false},
		{"same Docker disk supports upgrade", "", "", 8_000_000, 5_999_999, true, true},
		{"upgrade without gateway", "DEPLOY_CLOUDDRIVE2=false\r\n", "", 8_000_000, 2_000_000, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			install := diskResources{device: 1, available: test.installFree * 1024}
			docker := diskResources{device: 1, available: test.dockerFree * 1024}
			if test.separate {
				docker.device = 2
			}
			err := checkInstallDisk(install, docker, test.installed, func() ([]byte, error) {
				return []byte(test.config), nil
			}, io.Discard)
			if (test.want == "" && err != nil) || (test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want))) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
		})
	}
}
