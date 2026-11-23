package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

const (
	HTTPSRoot        = "/opt/tgcloud-proxy"
	HTTPSProject     = "tgcloud-proxy"
	HTTPSNginx       = "tgcloud-proxy-nginx"
	HTTPSCertbot     = "tgcloud-proxy-certbot"
	httpsRoot        = HTTPSRoot
	httpsProject     = HTTPSProject
	httpsNginx       = HTTPSNginx
	httpsCertbot     = HTTPSCertbot
	httpsEdition     = "clouddrive2"
	httpsBackendPort = 19798
)

type httpsEntry struct {
	root, directory, nginxImage, certbotImage string
}

type httpsRoute struct {
	Domain string `json:"domain"`
	Port   int    `json:"backend_port"`
}

type httpsState struct {
	Domains map[string]httpsRoute `json:"domains"`
	Project string                `json:"project"`
	Version int                   `json:"version"`
}

var (
	errHTTPSState     = errors.New("共享代理状态或目录无法核对；停止，不覆盖 HTTPS 配置")
	errHTTPSForeign   = errors.New("共享代理容器归属不符；停止，不接管其他服务")
	httpsDomainLabel  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	httpsEmail        = regexp.MustCompile("^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+$")
	httpsNginxImage   = regexp.MustCompile(`^nginx:[0-9]+\.[0-9]+\.[0-9]+-alpine$`)
	httpsCertbotImage = regexp.MustCompile(`^certbot/certbot:v[0-9]+\.[0-9]+\.[0-9]+$`)
)

func validHTTPSDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") || netLiteral(domain) {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if !httpsDomainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func newHTTPSState() httpsState {
	return httpsState{map[string]httpsRoute{}, "tgcloud", 1}
}

func (state httpsState) domain() string {
	return state.Domains[httpsEdition].Domain
}

func configuredHTTPSState(domain string) httpsState {
	state := newHTTPSState()
	state.Domains[httpsEdition] = httpsRoute{Domain: domain, Port: httpsBackendPort}
	return state
}

func (entry httpsEntry) statePath() string {
	return filepath.Join(entry.root, "state/domains.json")
}

func (entry httpsEntry) readState() (httpsState, bool, error) {
	state := newHTTPSState()
	path := entry.statePath()
	if !managerSafePath(path, false) {
		return state, false, errHTTPSState
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	raw, err := readHTTPSPrivate(path, installConfigLimit)
	if err != nil {
		return state, false, errHTTPSState
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 ||
		fields["domains"] == nil || fields["version"] == nil || fields["project"] == nil {
		return state, false, errHTTPSState
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF ||
		state.Version != 1 || state.Project != "tgcloud" || state.Domains == nil || len(state.Domains) > 1 {
		return state, false, errHTTPSState
	}
	for key, route := range state.Domains {
		if key != httpsEdition || route.Port != httpsBackendPort || !validHTTPSDomain(route.Domain) {
			return state, false, errHTTPSState
		}
	}
	return state, true, nil
}

func readHTTPSPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !privateOwner(info, hostutil.ManagedOwnerUID) {
		return nil, errHTTPSState
	}
	return readSourceFile(path, limit)
}

func (entry httpsEntry) sharedStateAvailable() error {
	path := filepath.Join(entry.root, "state")
	for _, name := range []string{"compose-transaction.json", "external-transaction.json"} {
		if _, err := os.Lstat(filepath.Join(path, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("共享代理还有待处理的事务；停止，保留现有入口")
		}
	}
	registry := filepath.Join(path, "compose-instances.json")
	if _, err := os.Lstat(registry); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	raw, err := readHTTPSPrivate(registry, 65536)
	var ownership struct {
		Format         int
		Owner, Project string
		Instances      map[string]json.RawMessage
	}
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &ownership) != nil || json.Unmarshal(raw, &fields) != nil ||
		len(fields) != 4 || fields["format"] == nil || fields["owner"] == nil || fields["project"] == nil ||
		fields["instances"] == nil || ownership.Format != 1 || ownership.Owner != "tgcloud.compose.https" ||
		ownership.Project != "tgcloud" || ownership.Instances == nil || len(ownership.Instances) != 0 {
		return errors.New("共享代理归属无法核对或仍承载独立 Compose，未修改入口")
	}
	return nil
}

func requireHTTPSDir(path string, mode os.FileMode) error {
	if !managerSafePath(path, false) {
		return errHTTPSState
	}
	if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return errHTTPSState
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != hostutil.ManagedOwnerUID || info.Mode().Perm() & ^mode != 0 {
		return errHTTPSState
	}
	// os.Mkdir is subject to the installer's umask. Nginx workers need
	// search permission on the mounted public ACME webroot.
	if mode == 0755 && info.Mode().Perm() != mode {
		if err := os.Chmod(path, mode); err != nil {
			return errHTTPSState
		}
	}
	return nil
}

func (entry httpsEntry) lock() (*os.File, error) {
	if err := requireHTTPSDir(entry.root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(entry.root, "transaction.lock")
	file, err := openPrivate(path, syscall.O_CREAT|syscall.O_RDWR, hostutil.ManagedOwnerUID)
	if err != nil {
		return nil, errHTTPSState
	}
	info, err := file.Stat()
	if err != nil || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		file.Close()
		return nil, errHTTPSState
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("另一个操作正在使用共享代理锁；未修改 HTTPS")
	}
	return file, nil
}

func (entry httpsEntry) commit(state httpsState) error {
	if err := requireHTTPSDir(filepath.Dir(entry.statePath()), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errHTTPSState
	}
	return writeHTTPSFile(entry.statePath(), append(raw, '\n'), 0600)
}

// Nginx binds the active conf.d directory. Keep its inode and replace only
// the individual files, after testing the complete configuration in staging.
func writeHTTPSFile(path string, body []byte, mode os.FileMode) error {
	if !managerSafePath(path, false) {
		return errHTTPSState
	}
	if info, err := os.Lstat(path); err == nil &&
		(!info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1) {
		return errHTTPSState
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".tgcloud-https-")
	if err != nil {
		return errHTTPSState
	}
	defer os.Remove(file.Name())
	if err := finishManagerFile(file, body, mode); err != nil {
		return errHTTPSState
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errHTTPSState
	}
	return nil
}

func (entry httpsEntry) runtimeFilesExist() (bool, error) {
	if _, err := os.Lstat(filepath.Join(entry.root, "docker-compose.yml")); !errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	entries, err := os.ReadDir(filepath.Join(entry.root, "nginx/conf.d"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, errHTTPSState
	}
	for _, config := range entries {
		if strings.HasSuffix(config.Name(), ".conf") {
			return true, nil
		}
	}
	return false, nil
}

func (entry httpsEntry) checkConfigFiles() error {
	for _, name := range []string{"docker-compose.yml", "nginx/nginx.conf", "nginx/conf.d"} {
		if !managerSafePath(filepath.Join(entry.root, name), false) {
			return errHTTPSState
		}
	}
	directory := filepath.Join(entry.root, "nginx/conf.d")
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errHTTPSState
	}
	for _, item := range entries {
		switch item.Name() {
		case "00-default.conf", "10-acme-bootstrap.conf", "20-clouddrive2.conf":
			if _, err := readSourceFile(filepath.Join(directory, item.Name()), installConfigLimit); err != nil ||
				!managerSafePath(filepath.Join(directory, item.Name()), false) {
				return errHTTPSState
			}
		default:
			if strings.HasSuffix(item.Name(), ".conf") {
				return fmt.Errorf("共享代理含未知 Nginx 路由；保留现有站点")
			}
		}
	}
	return nil
}
