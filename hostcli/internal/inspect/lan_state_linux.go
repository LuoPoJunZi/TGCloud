package inspect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	lanEdition = "clouddrive2"
	lanName    = "tgcloud-lan-cd2"
	lanPort    = "19798"
	lanTmpfs   = "rw,noexec,nosuid,mode=1777,size=64m"
)

type lanEntry struct {
	root, httpsState, directory, image string
}

// Keep the field order and whitespace compatible with the Python state reader.
type lanState struct {
	BindIP     string `json:"bind_ip"`
	Edition    string `json:"edition"`
	InstallDir string `json:"install_dir"`
	Mode       string `json:"mode"`
	ProxyImage string `json:"proxy_image"`
	Schema     int    `json:"schema"`
}

func (entry lanEntry) configDir() string {
	return filepath.Join(entry.root, lanEdition)
}

func (entry lanEntry) stateFor(address string) lanState {
	return lanState{address, lanEdition, entry.directory, "lan", entry.image, 1}
}

func (entry lanEntry) files(address string) map[string][]byte {
	state := entry.stateFor(address)
	// Quote the values as JSON before placing them in the Compose document.
	quote := func(value string) string {
		raw, _ := json.Marshal(value)
		return string(raw)
	}
	return map[string][]byte{
		"state.json": fmt.Appendf(nil, "{\"bind_ip\": %s, \"edition\": %s, \"install_dir\": %s, \"mode\": %s, \"proxy_image\": %s, \"schema\": %d}\n",
			quote(state.BindIP), quote(state.Edition), quote(state.InstallDir), quote(state.Mode), quote(state.ProxyImage), state.Schema),
		"compose.json": fmt.Appendf(nil, lanCompose, quote(entry.image), lanName, lanTmpfs, lanEdition, quote(filepath.Join(entry.configDir(), "nginx.conf"))),
		"nginx.conf":   fmt.Appendf(nil, lanNginx, address, lanPort, address, lanEdition, address, lanPort),
	}
}

func (entry lanEntry) rejectHTTPS() error {
	if _, err := os.Lstat(entry.httpsState); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !managerSafePath(entry.httpsState, true) {
		return errors.New("既有 HTTPS 状态无法核对，不改变访问模式")
	}
	body, err := readInstallConfig(entry.httpsState)
	var state, domains map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &state) != nil ||
		json.Unmarshal(state["domains"], &domains) != nil || domains == nil {
		return errors.New("既有 HTTPS 状态无法核对，不改变访问模式")
	}
	if _, exists := domains[lanEdition]; exists {
		return errors.New("该实例已配置 HTTPS；内网脚本不转换、不移除已有域名路由")
	}
	return nil
}

func lanDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if !managerSafePath(path, false) {
			return false, errors.New("内网状态目录经过链接或权限不安全")
		}
		return false, nil
	}
	if err != nil || !info.IsDir() || !privateOwner(info, managedOwnerUID) || !managerSafePath(path, true) {
		return false, errors.New("内网状态目录必须归 root 所有且权限为 700，不能经过链接")
	}
	return true, nil
}

func (entry lanEntry) load() (*lanState, error) {
	if exists, err := lanDirectory(entry.root); err != nil || !exists {
		return nil, err
	}
	directory := entry.configDir()
	if exists, err := lanDirectory(directory); err != nil || !exists {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 3 {
		return nil, errors.New("内网入口文件不完整或含额外文件；停止，不自动清理")
	}
	raw, err := readInstallConfig(filepath.Join(directory, "state.json"))
	var state lanState
	if err != nil || json.Unmarshal(raw, &state) != nil || state != entry.stateFor(state.BindIP) {
		return nil, errors.New("内网模式记录无效；不接管其他实例或改变安装模式")
	}
	if _, err := parseLanIP(state.BindIP); err != nil {
		return nil, err
	}
	for name, expected := range entry.files(state.BindIP) {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != managedOwnerUID || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("内网入口文件归属、权限或类型不安全")
		}
		body, err := readSourceFile(path, installConfigLimit)
		if err != nil || !bytes.Equal([]byte(strings.ReplaceAll(strings.ReplaceAll(string(body), "\r\n", "\n"), "\r", "\n")), expected) {
			return nil, errors.New("内网入口配置缺失或被修改；停止，不覆盖手改配置")
		}
	}
	return &state, nil
}

func (entry lanEntry) save(address string) error {
	if err := entry.rejectHTTPS(); err != nil {
		return err
	}
	if _, err := parseLanIP(address); err != nil {
		return err
	}
	saved, err := entry.load()
	if err != nil {
		return err
	}
	if saved != nil {
		if saved.BindIP != address {
			return errors.New("不隐式切换内网地址")
		}
		return nil
	}
	if err := os.Mkdir(entry.root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("无法创建内网状态目录")
	}
	if _, err := lanDirectory(entry.root); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(entry.root, ".stage-"+lanEdition+"-")
	if err != nil {
		return errors.New("无法暂存内网入口配置")
	}
	defer os.RemoveAll(staged)
	for name, body := range entry.files(address) {
		mode := os.FileMode(0600)
		if name == "nginx.conf" {
			mode = 0644 // The unprivileged Nginx process reads this bind mount.
		}
		file, err := os.OpenFile(filepath.Join(staged, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil || finishManagerFile(file, body, mode) != nil {
			return errors.New("无法写入内网入口配置")
		}
	}
	if err := renameNoReplace(staged, entry.configDir()); err != nil {
		return errors.New("无法发布内网入口配置，已有状态保留")
	}
	return nil
}

const lanCompose = `{
  "services": {
    "lan": {
      "image": %s,
      "container_name": "%s",
      "network_mode": "host",
      "user": "101:101",
      "entrypoint": [
        "nginx"
      ],
      "command": [
        "-g",
        "daemon off;"
      ],
      "restart": "unless-stopped",
      "read_only": true,
      "cap_drop": [
        "ALL"
      ],
      "security_opt": [
        "no-new-privileges:true"
      ],
      "tmpfs": [
        "/tmp:%s"
      ],
      "labels": {
        "com.tgcloud.managed": "true",
        "com.tgcloud.role": "lan-management",
        "com.tgcloud.edition": "%s"
      },
      "volumes": [
        {
          "type": "bind",
          "source": %s,
          "target": "/etc/nginx/nginx.conf",
          "read_only": true
        }
      ]
    }
  }
}
`

const lanNginx = `worker_processes auto;
pid /tmp/nginx.pid;
error_log /dev/stderr warn;
events { worker_connections 1024; }
http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    access_log off;
    server_tokens off;
    client_max_body_size 16m;
    client_body_temp_path /tmp/client_body;
    proxy_temp_path /tmp/proxy;
    fastcgi_temp_path /tmp/fastcgi;
    uwsgi_temp_path /tmp/uwsgi;
    scgi_temp_path /tmp/scgi;
    map $http_upgrade $connection_upgrade { default upgrade; '' close; }
    server {
        listen %s:%s;
        server_name %s;
        add_header X-TGCloud-LAN %s always;
        if ($host != '%s') { return 403; }
        location ~* ^/dav(?:/|$) { return 403; }
        location / {
            proxy_pass http://127.0.0.1:%s;
            proxy_http_version 1.1;
            proxy_set_header Host $http_host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $remote_addr;
            proxy_set_header X-Forwarded-Proto http;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_read_timeout 300s;
        }
    }
}
`
