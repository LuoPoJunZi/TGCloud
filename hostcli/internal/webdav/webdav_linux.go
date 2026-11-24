package webdav

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/entry"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

const (
	defaultWebDAVRoot = "/opt/tgcloud-webdav"
	defaultAuthRoot   = "/opt/tgcloud-proxy/external-auth"
	expectedOwner     = "tgcloud.external_webdav"
	maxAuthSize       = 16384
	maxMembers        = 32
)

var (
	editionRegex  = regexp.MustCompile(`^clouddrive2$`)
	usernameRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{2,47}$`)
	hashRegex     = regexp.MustCompile(`^\$6\$rounds=200000\$[A-Za-z0-9./]{16}\$[A-Za-z0-9./]{86}$`)
	revRegex      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	bindRegex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type StatusReport struct {
	Edition       string `json:"edition"`
	Present       bool   `json:"present"`
	Domain        string `json:"domain,omitempty"`
	Username      string `json:"username,omitempty"`
	TargetDir     string `json:"target_dir,omitempty"`
	Phase         string `json:"phase,omitempty"`
	InstanceDir   string `json:"instance_dir,omitempty"`
	MemberCount   int    `json:"member_count"`
	AuthPresent   bool   `json:"auth_present"`
	AuthSHA256    string `json:"auth_sha256,omitempty"`
	AuthUserCount int    `json:"auth_user_count"`
}

func Supported() bool { return true }

func Run(args []string, input io.Reader) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("外部 WebDAV 子命令参数无效")
	}
	switch args[0] {
	case "status":
		edition := "clouddrive2"
		if len(args) >= 2 && !strings.HasPrefix(args[1], "--") {
			edition = args[1]
		}
		return Status(defaultWebDAVRoot, defaultAuthRoot, edition)
	case "write-auth":
		if len(args) < 3 || len(args) > 4 {
			return nil, errors.New("用法：webdav-entry write-auth <edition> <gid> [--revoke]")
		}
		edition := args[1]
		gid, err := strconv.Atoi(args[2])
		if err != nil || gid <= 0 || gid >= 65536 {
			return nil, errors.New("无效的 Nginx worker GID")
		}
		revoke := len(args) == 4 && args[3] == "--revoke"
		if len(args) == 4 && !revoke {
			return nil, errors.New("未知 write-auth 参数")
		}
		var body []byte
		if revoke {
			body = []byte("disabled:!\n")
		} else {
			body, err = io.ReadAll(io.LimitReader(input, maxAuthSize+1))
			if err != nil || len(body) > maxAuthSize {
				return nil, errors.New("外部认证输入无效或超出大小限制")
			}
		}
		digest, err := WriteAuth(defaultAuthRoot, edition, gid, body)
		if err != nil {
			return nil, err
		}
		return map[string]any{"edition": edition, "sha256": digest, "revoked": revoke}, nil
	case "verify-auth":
		if len(args) != 2 {
			return nil, errors.New("用法：webdav-entry verify-auth <edition>")
		}
		digest, err := VerifyMountedAuth(defaultAuthRoot, args[1], inspect.RunDocker)
		if err != nil {
			return nil, err
		}
		return map[string]any{"edition": args[1], "sha256": digest, "matched": true}, nil
	default:
		return nil, errors.New("外部 WebDAV 子命令参数无效")
	}
}

func Status(webdavRoot, authRoot, edition string) (StatusReport, error) {
	if !editionRegex.MatchString(edition) {
		return StatusReport{}, errors.New("仅支持 clouddrive2 外部 WebDAV 实例")
	}
	report := StatusReport{Edition: edition}
	recordPath := filepath.Join(webdavRoot, edition, "config.json")
	if !hostutil.SafePath(recordPath, false) {
		return report, errors.New("外部 WebDAV 路径经过链接或权限不安全")
	}
	if info, err := os.Lstat(recordPath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 32768 || !hostutil.PrivateOwner(info, hostutil.ManagedOwnerUID) {
			return report, errors.New("外部 WebDAV 记录权限或大小不安全")
		}
		raw, err := os.ReadFile(recordPath)
		if err != nil {
			return report, errors.New("无法读取外部 WebDAV 记录")
		}
		var rec struct {
			Schema            int               `json:"schema"`
			Owner             string            `json:"owner"`
			Edition           string            `json:"edition"`
			InstanceDir       string            `json:"instance_dir"`
			Domain            string            `json:"domain"`
			Username          string            `json:"username"`
			TargetDir         string            `json:"target_dir"`
			Phase             string            `json:"phase"`
			ScopeVerification string            `json:"scope_verification"`
			Binding           string            `json:"binding"`
			Revision          string            `json:"revision"`
			Members           []json.RawMessage `json:"members"`
		}
		if json.Unmarshal(raw, &rec) != nil || (rec.Schema != 1 && rec.Schema != 2) ||
			rec.Owner != expectedOwner || rec.Edition != edition ||
			rec.ScopeVerification != "manual-native-readonly" ||
			!revRegex.MatchString(rec.Revision) || !bindRegex.MatchString(rec.Binding) {
			return report, errors.New("外部 WebDAV 记录格式无效或归属不符")
		}
		report.Present = true
		report.Domain = rec.Domain
		report.Username = rec.Username
		report.TargetDir = rec.TargetDir
		report.Phase = rec.Phase
		report.InstanceDir = rec.InstanceDir
		report.MemberCount = len(rec.Members)
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, errors.New("无法检查外部 WebDAV 记录")
	}

	authPath := filepath.Join(authRoot, edition+".htpasswd")
	if hostutil.SafePath(authPath, false) {
		if info, err := os.Lstat(authPath); err == nil {
			if !info.Mode().IsRegular() || info.Size() > maxAuthSize || info.Mode().Perm()&0007 != 0 {
				return report, errors.New("外部认证文件权限或大小不安全")
			}
			raw, err := os.ReadFile(authPath)
			if err != nil {
				return report, errors.New("无法读取外部认证文件")
			}
			users, err := ParseAuthEntries(string(raw))
			if err != nil {
				return report, err
			}
			sum := sha256.Sum256(raw)
			report.AuthPresent = true
			report.AuthSHA256 = hex.EncodeToString(sum[:])
			report.AuthUserCount = len(users)
		}
	}
	return report, nil
}

func ParseAuthEntries(content string) (map[string]string, error) {
	if content == "disabled:!\n" {
		return map[string]string{}, nil
	}
	if len(content) == 0 || len(content) > maxAuthSize || !strings.HasSuffix(content, "\n") {
		return nil, errors.New("外部认证文件大小或格式不受支持")
	}
	result := make(map[string]string)
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		user, hashed, ok := strings.Cut(line, ":")
		if !ok || !usernameRegex.MatchString(user) || !hashRegex.MatchString(hashed) {
			return nil, errors.New("外部认证文件必须使用唯一专用账号和强哈希")
		}
		lower := strings.ToLower(user)
		if seen[lower] {
			return nil, errors.New("外部认证文件存在重复账号")
		}
		seen[lower] = true
		result[user] = hashed
		if len(result) > maxMembers+1 {
			return nil, errors.New("外部账号数量超过家庭共享限制")
		}
	}
	return result, nil
}

func WriteAuth(authRoot, edition string, gid int, content []byte) (string, error) {
	if !editionRegex.MatchString(edition) {
		return "", errors.New("仅支持 clouddrive2 外部 WebDAV 实例")
	}
	if _, err := ParseAuthEntries(string(content)); err != nil {
		return "", err
	}
	if !hostutil.SafePath(authRoot, false) {
		return "", errors.New("外部认证目录路径不安全")
	}
	if err := os.MkdirAll(authRoot, 0750); err != nil {
		return "", errors.New("无法创建外部认证目录")
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(authRoot, 0, gid); err != nil {
			return "", errors.New("无法设置外部认证目录属组")
		}
	}
	if err := os.Chmod(authRoot, 0750); err != nil {
		return "", errors.New("无法设置外部认证目录权限")
	}
	target := filepath.Join(authRoot, edition+".htpasswd")
	if !hostutil.SafePath(target, false) {
		return "", errors.New("外部认证文件路径不安全")
	}
	temp, err := os.CreateTemp(authRoot, ".htpasswd-")
	if err != nil {
		return "", errors.New("无法创建外部认证暂存文件")
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return "", errors.New("无法写入外部认证暂存文件")
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", errors.New("无法同步外部认证暂存文件")
	}
	if err := temp.Close(); err != nil {
		return "", errors.New("无法关闭外部认证暂存文件")
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(temp.Name(), 0, gid); err != nil {
			return "", errors.New("无法设置外部认证文件属组")
		}
	}
	if err := os.Chmod(temp.Name(), 0640); err != nil {
		return "", errors.New("无法设置外部认证文件权限")
	}
	info, err := os.Lstat(temp.Name())
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return "", errors.New("外部认证暂存文件归属异常")
	}
	if err := os.Rename(temp.Name(), target); err != nil {
		return "", errors.New("无法原子发布外部认证文件")
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func VerifyMountedAuth(authRoot, edition string, docker inspect.DockerRunner) (string, error) {
	if !editionRegex.MatchString(edition) {
		return "", errors.New("仅支持 clouddrive2 外部 WebDAV 实例")
	}
	authPath := filepath.Join(authRoot, edition+".htpasswd")
	if !hostutil.SafePath(authPath, false) {
		return "", errors.New("外部认证文件路径不安全")
	}
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return "", errors.New("无法读取宿主机外部认证文件")
	}
	sum := sha256.Sum256(raw)
	expected := hex.EncodeToString(sum[:])
	out, err := docker("exec", entry.HTTPSNginx, "sha256sum", "/etc/nginx/external-auth/"+edition+".htpasswd")
	if err != nil {
		return "", errors.New("容器没有读取到当前认证文件，拒绝宣称密码轮换成功")
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || fields[0] != expected {
		return "", errors.New("容器没有读取到当前认证文件，拒绝宣称密码轮换成功")
	}
	return expected, nil
}
