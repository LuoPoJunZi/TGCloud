package inspect

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/storage"
)

var dockerEngineVersion = regexp.MustCompile(`^([0-9]+)\.[0-9]+\.[0-9]+(?:[-+].*)?$`)

func InstallerPreflightSupported() bool { return true }

func InstallerPreflight(args []string, input io.Reader) (any, error) {
	if len(args) != 2 && len(args) != 3 {
		return nil, errors.New("一键安装预检参数无效")
	}
	switch {
	case len(args) == 2 && args[0] == "host" && (args[1] == "public" || args[1] == "lan"):
		return installerHost(args[1] == "lan")
	case len(args) == 2 && args[0] == "docker":
		info, err := installerDocker(args[1])
		return map[string]any{"docker": info}, err
	case len(args) == 2 && args[0] == "storage":
		var request storage.Request
		if err := readPreflightInput(input, &request); err != nil {
			return nil, err
		}
		return storage.Decide(args[1], request)
	case len(args) == 3 && args[0] == "write-config":
		return map[string]bool{"written": true}, writeWizardConfig(args[1], args[2], input)
	default:
		return nil, errors.New("一键安装预检参数无效")
	}
}

func ReadPreflightInput(input io.Reader, value any) error {
	return readPreflightInput(input, value)
}

func readPreflightInput(input io.Reader, value any) error {
	raw, err := io.ReadAll(io.LimitReader(input, 4*installConfigLimit+1))
	if err != nil || len(raw) > 4*installConfigLimit || !utf8.Valid(raw) || json.Unmarshal(raw, value) != nil {
		return errors.New("一键安装预检输入无效或超过大小限制")
	}
	return nil
}

func installerHost(lan bool) (map[string]string, error) {
	if err := checkDockerHost(); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, errors.New("请在 Linux VPS 以 root 运行；Windows 用户可通过 SSH 登录后运行脚本")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		return nil, errors.New("缺少 Bash")
	}
	machine, err := hostutil.HostUnameMachine()
	if err != nil {
		return nil, err
	}
	release, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return nil, errors.New("无法读取 Linux 系统信息")
	}
	name, err := installerOSName(string(release))
	if err != nil {
		return nil, err
	}
	dpkg, err := resourceCommand("dpkg", "--print-architecture")
	if err != nil {
		return nil, errors.New("无法核对 dpkg 用户态架构；未执行安装")
	}
	if dpkg != "amd64" && dpkg != "arm64" {
		return nil, errors.New("需要 Debian/Ubuntu 的 amd64 或 arm64 用户态")
	}
	if nativeArchitecture(machine) != dpkg || runtime.GOARCH != dpkg {
		return nil, errors.New("内核与用户态架构不一致；不自动使用 QEMU 或跨架构部署")
	}
	if lan {
		if _, err := exec.LookPath("ip"); err != nil {
			return nil, errors.New("缺少 iproute2；请先人工安装，不在信息收集前安装软件")
		}
	}
	return map[string]string{"architecture": dpkg, "machine": machine, "name": name}, nil
}

func installerOSName(release string) (string, error) {
	values, ok := hostutil.ParseOSRelease(release, shellWord.MatchString, "ID", "PRETTY_NAME")
	if !ok {
		return "", errors.New("Linux 系统信息格式异常")
	}
	id, pretty := values["ID"], values["PRETTY_NAME"]
	if id != "debian" && id != "ubuntu" {
		return "", errors.New("仅支持 Debian/Ubuntu；不猜测其他发行版的安装方式")
	}
	if pretty == "" {
		pretty = id
	}
	return pretty, nil
}

func installerDocker(mode string) (*PreflightResult, error) {
	if mode != "base" && mode != "lan" && mode != "upgrade" {
		return nil, errors.New("未知的 Docker 预检模式")
	}
	info, err := DockerPreflight(mode != "upgrade")
	if err != nil || info == nil {
		return info, err
	}
	if mode == "lan" {
		match := dockerEngineVersion.FindStringSubmatch(info.Version)
		if len(match) == 0 || !lanDockerVersion(match[1]) {
			return nil, errors.New("内网模式要求 Docker Engine 28+，避免旧版回环端口被同网段直接访问；请人工升级，不重启其他容器")
		}
	}
	if mode == "upgrade" {
		if _, err := RunDocker("compose", "version"); err != nil {
			return nil, errors.New("升级要求现有 Docker Compose 可用；不在升级中自动安装依赖")
		}
	}
	return info, nil
}

func lanDockerVersion(major string) bool {
	digits := strings.TrimLeft(major, "0")
	return len(digits) > 2 || (len(digits) == 2 && digits >= "28")
}

func writeWizardConfig(path, action string, input io.Reader) error {
	if (action != "install" && action != "upgrade") || filepath.Base(path) != "config.env" ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(path)), "tgcloud-cli-payload-") ||
		!sourceDirectory(filepath.Dir(path), true) || !managerSafePath(path, false) || filepath.Clean(path) != path {
		return errors.New("候选配置路径或操作无效")
	}
	if action == "upgrade" {
		return writePrivateConfigFile(path, []byte("TGCLOUD_REDEPLOY_APPLY_CONFIG=false\n"))
	}

	var entries [][]string
	if err := readPreflightInput(input, &entries); err != nil {
		return err
	}
	var data strings.Builder
	for _, entry := range entries {
		if len(entry) != 2 || !installConfigKey.MatchString(entry[0]) ||
			strings.ContainsAny(entry[1], "\r\n\x00") {
			return errors.New("候选配置包含无效项目")
		}
		data.WriteString(entry[0] + "=" + entry[1] + "\n")
	}
	if data.Len() > installConfigLimit {
		return errors.New("候选配置超过大小限制")
	}
	if _, err := parseDeploymentConfig([]byte(data.String())); err != nil {
		return err
	}
	return writePrivateConfigFile(path, []byte(data.String()))
}
