package deps

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

var (
	dockerKeyPath    = "/etc/apt/keyrings/tgcloud-docker.asc"
	dockerSourcePath = "/etc/apt/sources.list.d/tgcloud-docker.sources"
)

var releaseSuite = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

func ensureDockerRepository(run dependencyRunner) error {
	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return errors.New("无法识别宿主的 Debian/Ubuntu 软件源版本")
	}
	uri, suite, err := dockerRepositoryRelease(string(osRelease))
	if err != nil {
		return err
	}
	architecture, err := run("dpkg", "--print-architecture")
	if err != nil || strings.TrimSpace(string(architecture)) != runtime.GOARCH {
		return errors.New("dpkg 架构与 TGCloud 宿主管理工具不一致")
	}
	if err := refreshPackageLists(run); err != nil {
		return err
	}
	targets, err := run("apt-get", "indextargets", "--format", "$(URI)")
	if err != nil {
		return errors.New("无法核对已配置的 Docker 软件源")
	}
	index := uri + "/dists/" + suite + "/stable/binary-" + runtime.GOARCH + "/Packages"
	for _, target := range strings.Fields(string(targets)) {
		if target == index || strings.HasPrefix(target, index+".") {
			return nil
		}
	}
	key, err := run("curl", "-fsSL", "--proto", "=https", "--proto-redir", "=https", "--max-time", "60", "--max-filesize", "65536", uri+"/gpg")
	if err != nil || len(key) > 65536 || !bytes.HasPrefix(key, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		return errors.New("无法获取 Docker 官方软件源签名公钥")
	}
	if err := ensureAPTKeyringDir(filepath.Dir(dockerKeyPath)); err != nil {
		return err
	}
	if _, err := publishAPTFile(dockerKeyPath, key); err != nil {
		return err
	}
	source := fmt.Sprintf("Types: deb\nURIs: %s\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: %s\n",
		uri, suite, runtime.GOARCH, dockerKeyPath)
	created, err := publishAPTFile(dockerSourcePath, []byte(source))
	if err != nil {
		return err
	}
	if err := refreshPackageLists(run); err != nil {
		if created {
			_ = os.Remove(dockerSourcePath)
		}
		return err
	}
	return nil
}

func dockerRepositoryRelease(release string) (uri, suite string, err error) {
	values, _ := hostutil.ParseOSRelease(release, nil, "ID", "VERSION_CODENAME", "UBUNTU_CODENAME")
	id := values["ID"]
	suite = values["VERSION_CODENAME"]
	if id == "ubuntu" && values["UBUNTU_CODENAME"] != "" {
		suite = values["UBUNTU_CODENAME"]
	}
	if (id != "debian" && id != "ubuntu") || !releaseSuite.MatchString(suite) {
		return "", "", errors.New("无法核实受支持的 Debian/Ubuntu 软件源代号")
	}
	return "https://download.docker.com/linux/" + id, suite, nil
}

func ensureAPTKeyringDir(directory string) error {
	invalid := errors.New("无法准备 APT 签名公钥目录")
	if err := os.MkdirAll(directory, 0755); err != nil {
		return invalid
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != hostutil.ManagedOwnerUID {
		return invalid
	}
	// os.MkdirAll is subject to the installer's umask (0077), which would hide
	// the keyring from the unprivileged _apt sandbox user during apt-get update.
	if info.Mode().Perm()&0755 != 0755 {
		if err := os.Chmod(directory, 0755); err != nil {
			return invalid
		}
	}
	return nil
}

// A private file is published atomically and exclusively to keep an existing
// keyring or administrator-managed source intact.
func publishAPTFile(path string, content []byte) (bool, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode().IsRegular() && info.Size() == int64(len(content)) {
			if previous, err := os.ReadFile(path); err == nil && bytes.Equal(previous, content) {
				return false, nil
			}
		}
		return false, errors.New("TGCloud APT 路径已有不同配置，已保留现有文件")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tgcloud-apt-")
	if err != nil {
		return false, errors.New("无法创建 Docker APT 暂存文件")
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(content)
	modeErr := temp.Chmod(0644)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if writeErr != nil || modeErr != nil || syncErr != nil || closeErr != nil || os.Link(temp.Name(), path) != nil {
		return false, errors.New("无法保存 Docker APT 配置，已保留现有文件")
	}
	return true, nil
}
