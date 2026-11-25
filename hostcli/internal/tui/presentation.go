package tui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

var friendlyStageTitles = map[string]string{
	"GATEWAY":       "启动 CloudDrive2 网关",
	"BOT":           "构建并启动 TGCloud Bot",
	"HEALTH":        "检查 Bot 基础健康",
	"HTTPS_ENV":     "检查 HTTPS 部署环境",
	"ACME":          "启动证书签发验证",
	"CERTIFICATE":   "申请或复用 HTTPS 证书",
	"HTTPS_VERIFY":  "检查 HTTPS 管理入口",
	"RENEW_DRY_RUN": "执行证书续期演练",
	"PROXY_BACKUP":  "处理共享代理备份",
}

var friendlyMarkerLabels = map[string]string{
	"CODE":             "运行代码校验",
	"CONFIG":           "配置检查",
	"CONFIGURATION":    "配置检查",
	"DATABASE_CHECK":   "数据库检查",
	"DATABASE_BACKUP":  "数据库备份",
	"BOT_HEALTH":       "Bot 基础健康",
	"RESULT":           "基础部署",
	"REPAIR":           "CloudDrive2 网络检查",
	"STATUS":           "服务状态",
	"UPDATE":           "程序更新",
	"APPLY_CONFIG":     "配置应用",
	"BACKUPS":          "备份检查",
	"BACKUP_PRUNE":     "备份清理",
	"BACKUP_PREFLIGHT": "备份隔离预检",
	"DESTINATION":      "WebDAV 写入验收",
	"CLI_CHECK":        "安装只读预检",
	"LAN_CHECK":        "内网只读预检",
	"HTTPS":            "HTTPS 入口配置",
	"LAN_ENTRY":        "内网入口检查",
	"WEBDAV_AUTH":      "WebDAV 账号认证",
	"WEBDAV_LIST":      "WebDAV 目标目录访问",
	"WEBDAV":           "WebDAV 账号与目录检查",
	"WEBDAV_WRITE":     "WebDAV 测试文件写入",
	"WEBDAV_SIZE":      "WebDAV 临时文件大小复验",
	"UPLOAD":           "文件上传与大小复验",
	"WEBDAV_MOVE":      "WebDAV 改名与最终文件复验",
	"RENAME":           "文件改名检查",
	"WEBDAV_DELETE":    "WebDAV 测试文件删除检查",
	"DELETE":           "临时文件清理",
}

var friendlyMarkerStates = map[string]string{
	"OK":              "通过",
	"SUCCESS":         "通过",
	"healthy":         "通过",
	"FAILED":          "未通过",
	"unhealthy":       "未通过",
	"MISMATCH":        "未通过（与预期不一致）",
	"MISMATCH_KEYS":   "未通过（配置字段不一致）",
	"starting":        "启动检查中",
	"exited":          "已停止",
	"STOPPED":         "已停止",
	"missing":         "未找到",
	"UNKNOWN":         "未核实",
	"unknown":         "未核实",
	"PRESERVED":       "已保留原配置",
	"APPLY_REQUESTED": "已请求应用新配置",
}

func isMarkerKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

func splitFirstDelimiter(s string) (head, sep, tail string) {
	bestIdx := -1
	bestSep := ""
	for _, candidate := range []string{"；", ":", "："} {
		if idx := strings.Index(s, candidate); idx >= 0 && (bestIdx < 0 || idx < bestIdx) {
			bestIdx = idx
			bestSep = candidate
		}
	}
	if bestIdx < 0 {
		return s, "", ""
	}
	return s[:bestIdx], bestSep, s[bestIdx+len(bestSep):]
}

// FormatFriendlyMarker translates TGCLOUD_* protocol markers into concise Chinese lines for interactive menus.
func FormatFriendlyMarker(line string) string {
	if !strings.HasPrefix(line, "TGCLOUD_") {
		return line
	}
	rest := strings.TrimPrefix(line, "TGCLOUD_")
	key, value, hasEq := strings.Cut(rest, "=")
	if !hasEq || !isMarkerKey(key) {
		return "[待核对] 收到未识别的检查结果，请查看诊断报告。"
	}
	switch key {
	case "STAGE":
		if title, ok := friendlyStageTitles[value]; ok {
			return "\n── " + title + " ──"
		}
		return "[待核对] 维护阶段未识别，请查看诊断报告。"
	case "VERSION":
		return "程序版本：v" + value
	case "INSTALL_DIR":
		return "安装目录：" + value
	case "CLEANUP_WARNING":
		return "[待核对] 临时测试文件清理未确认，请在授权目标目录检查残留。"
	case "STORAGE_GATEWAY":
		if value == "clouddrive2" {
			return "存储网关：CloudDrive2"
		}
		return "存储网关：未核实"
	case "TEST_PATH":
		return "随机测试文件验收流程结束。"
	case "PROXY_TASK_CLEANUP":
		switch value {
		case "OK":
			return "一次性演练容器清理：通过"
		case "UNKNOWN":
			return "[错误] 一次性演练容器清理：未确认"
		case "REFUSED":
			return "[错误] 一次性演练容器清理：拒绝清理（容器归属不符）"
		case "FAILED":
			return "[错误] 一次性演练容器清理：未通过"
		}
	case "PROXY_CHECK":
		checkTitle, checkVal, _ := strings.Cut(value, ":")
		if checkTitle != "" {
			if checkVal == "" {
				checkVal = "未核实"
			}
			prefix := ""
			if checkVal != "通过" {
				prefix = "[错误] "
			}
			return fmt.Sprintf("%sHTTPS 入口检查（%s）：%s", prefix, checkTitle, checkVal)
		}
	case "UNINSTALL_CHECK":
		return "卸载预检：通过（仅预检，尚未执行卸载）"
	case "UNINSTALL_RESULT":
		status, detail, _ := strings.Cut(value, "；")
		detail = strings.TrimSpace(detail)
		headers := map[string]string{
			"OK":          "卸载结果：已完成",
			"CANCELLED":   "卸载结果：已取消",
			"PARTIAL":     "[待核对] 卸载结果：部分完成",
			"INTERRUPTED": "[待核对] 卸载结果：操作中断",
			"FAILED":      "[错误] 卸载结果：未通过",
		}
		if header, ok := headers[status]; ok {
			if detail != "" {
				return header + "（" + detail + "）"
			}
			return header
		}
	}

	label, okLabel := friendlyMarkerLabels[key]
	status, _, after := splitFirstDelimiter(value)
	state, okState := friendlyMarkerStates[status]
	if !okLabel || !okState {
		return "[待核对] 收到未识别的检查结果，请查看诊断报告。"
	}
	prefix := ""
	if status == "FAILED" || status == "MISMATCH" || status == "MISMATCH_KEYS" || status == "unhealthy" {
		prefix = "[错误] "
	}
	suffix := ""
	if key == "CLI_CHECK" || key == "LAN_CHECK" {
		suffix = "（不代表部署或转存验收）"
	} else if key == "DESTINATION" && status == "FAILED" && strings.TrimSpace(after) != "" {
		suffix = "；" + strings.TrimSpace(after)
	}
	return prefix + label + "：" + state + suffix
}

// FriendlyWriter translates line-delimited TGCLOUD_* markers before writing to a human terminal.
type FriendlyWriter struct {
	out io.Writer
	buf bytes.Buffer
}

func FriendlyProgressWriter(out io.Writer) *FriendlyWriter {
	return &FriendlyWriter{out: out}
}

func (w *FriendlyWriter) Write(p []byte) (int, error) {
	if w.out == nil {
		return len(p), nil
	}
	w.buf.Write(p)
	for {
		data := w.buf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSuffix(string(data[:idx]), "\r")
		w.buf.Next(idx + 1)
		if _, err := fmt.Fprintln(w.out, FormatFriendlyMarker(line)); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

func (w *FriendlyWriter) Flush() error {
	if w.out == nil || w.buf.Len() == 0 {
		return nil
	}
	line := strings.TrimSuffix(w.buf.String(), "\r")
	w.buf.Reset()
	if line == "" {
		return nil
	}
	_, err := fmt.Fprintln(w.out, FormatFriendlyMarker(line))
	return err
}

func FormatContainerState(state string) string {
	switch state {
	case "running":
		return "运行中"
	case "exited", "dead":
		return "已停止"
	case "restarting":
		return "重启中"
	case "created":
		return "尚未启动"
	case "paused":
		return "已暂停"
	case "removing":
		return "正在移除"
	default:
		return "未核实"
	}
}

func FormatContainerHealth(state, health string) string {
	if state != "running" {
		return "未检查（容器未运行）"
	}
	switch health {
	case "healthy":
		return "通过（容器检查）"
	case "unhealthy":
		return "未通过"
	case "starting":
		return "检查中"
	case "", "none":
		return "未配置容器检查"
	default:
		return "未核实"
	}
}

func FormatProxyCheckName(key string) string {
	names := map[string]string{
		"CORE":          "基础容器运行",
		"PORTS":         "80/443 端口监听",
		"NGINX":         "Nginx 容器状态",
		"CERTBOT":       "Certbot 容器状态",
		"RENEWAL":       "自动续期配置",
		"NGINX_TEST":    "Nginx 配置语法",
		"HTTPS":         "HTTPS 管理页访问",
		"REDIRECT":      "HTTP 跳转 HTTPS",
		"DAV_BLOCK":     "内部 WebDAV 路径隔离",
		"BACKEND_LOCAL": "网关本机回环监听",
		"CERT":          "证书域名与有效期",
	}
	if label, ok := names[key]; ok {
		return label
	}
	return key
}

func FormatCheckResult(val string) string {
	switch strings.TrimSpace(val) {
	case "OK":
		return "通过"
	case "FAILED":
		return "未通过"
	default:
		return "未核实"
	}
}

func FormatWebDAVPhase(phase string) string {
	switch strings.TrimSpace(phase) {
	case "pending":
		return "待完成配置"
	case "enabled":
		return "已启用"
	case "disabled":
		return "已停用"
	case "needs_repair":
		return "需要修复"
	default:
		return "未核实"
	}
}

func FormatReadinessState(state string) string {
	states := map[string]string{
		"ok":       "通过",
		"failed":   "未通过",
		"unknown":  "未核实／记录过期",
		"partial":  "仅账号根目录可读，目标目录未确认",
		"paused":   "手动暂停",
		"cooldown": "冷却中，不追加请求",
	}
	if label, ok := states[strings.TrimSpace(state)]; ok {
		return label
	}
	return "未核实"
}

func FormatFailureKind(kind string) string {
	failures := map[string]string{
		"authentication": "认证失败",
		"permission":     "权限不足",
		"rate_limited":   "限流",
		"server":         "服务端异常",
		"remote_conflict": "远端待复核",
		"local_missing":  "本地文件不可读",
		"network":        "网络或校验异常",
		"source_access":  "监听来源或附件不可读",
		"other":          "其他",
		"unchecked":      "未检查",
	}
	if label, ok := failures[strings.TrimSpace(kind)]; ok {
		return label
	}
	return "其他"
}

// FormatTerminalReport strips Markdown syntax for terminal viewing without mutating saved reports.
func FormatTerminalReport(markdown string) string {
	lines := strings.Split(strings.TrimSuffix(markdown, "\n"), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
		lines = lines[1:]
	}
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "### "):
			lines[i] = "── " + strings.TrimPrefix(line, "### ") + " ──"
		case strings.HasPrefix(line, "## "):
			lines[i] = "── " + strings.TrimPrefix(line, "## ") + " ──"
		case strings.HasPrefix(line, "# "):
			lines[i] = "── " + strings.TrimPrefix(line, "# ") + " ──"
		}
	}
	return strings.TrimLeft(strings.Join(lines, "\n"), "\n")
}
