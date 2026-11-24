package tui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

type WebDAVCredentials struct {
	URL        string
	Username   string
	Password   string
	TargetPath string
}

func DecodeWebDAVCredentials(env map[string]string) (WebDAVCredentials, error) {
	decodeField := func(primary, legacy string) (string, error) {
		raw := env[primary]
		if raw == "" {
			raw = env[legacy]
		}
		if raw == "" {
			return "", nil
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return "", errors.New("实例的 WebDAV 配置无法安全解析")
		}
		return string(decoded), nil
	}
	url, err := decodeField("WEBDAV_URL_B64", "CD2_WEBDAV_URL_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	user, err := decodeField("WEBDAV_USERNAME_B64", "CD2_WEBDAV_USERNAME_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	pass, err := decodeField("WEBDAV_PASSWORD_B64", "CD2_WEBDAV_PASSWORD_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	target, err := decodeField("WEBDAV_TARGET_PATH_B64", "CD2_TARGET_PATH_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	return WebDAVCredentials{URL: url, Username: user, Password: pass, TargetPath: target}, nil
}

func RenderDiagnostics(instance *inspect.Instance, dockerVer, composeVer, botSnapshotJSON string, logRotation map[string]string, entrySummary string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# TGCloud 故障诊断报告")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "仅只读检查；不包含原始日志、文件名、私人路径或凭据。")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "生成时间（UTC）："+time.Now().UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintln(&b, "实例：CloudDrive2")
	fmt.Fprintln(&b, "Docker："+fallbackText(dockerVer, "未检查"))
	fmt.Fprintln(&b, "Compose："+fallbackText(composeVer, "未检查"))
	fmt.Fprintf(&b, "Bot 容器：%s / %s\n", fallbackText(instance.Bot.State, "未检查"), fallbackText(instance.Bot.Health, "none"))
	fmt.Fprintf(&b, "网关容器：%s / %s\n", fallbackText(instance.Gateway.State, "未检查"), fallbackText(instance.Gateway.Health, "none"))

	var snap struct {
		Schema        int               `json:"schema"`
		Edition       string            `json:"edition"`
		Version       string            `json:"version"`
		Python        string            `json:"python"`
		Configuration string            `json:"configuration"`
		Checks        map[string]string `json:"checks"`
		Failures      map[string]int64  `json:"failures"`
	}
	if botSnapshotJSON != "" && json.Unmarshal([]byte(botSnapshotJSON), &snap) == nil &&
		snap.Schema == 1 && snap.Edition == "clouddrive2" {
		fmt.Fprintln(&b, "实际 Bot 版本："+snap.Version)
		fmt.Fprintln(&b, "Bot Python："+snap.Python)
		cfgLabel := "不完整"
		if snap.Configuration == "present" {
			cfgLabel = "存在（不代表凭据有效）"
		}
		fmt.Fprintln(&b, "配置必填项："+cfgLabel)
		for _, item := range []struct{ key, label string }{
			{"heartbeat", "Bot 心跳"}, {"telegram", "Telegram 连接"},
			{"destination", "WebDAV 目标"}, {"queue", "队列调度"}, {"capacity", "下载容量"},
		} {
			fmt.Fprintf(&b, "%s：%s\n", item.label, fallbackText(snap.Checks[item.key], "未核实"))
		}
		fmt.Fprintln(&b, "\n## 最近7天当前失败任务分类（非原始日志）")
		if len(snap.Failures) == 0 {
			fmt.Fprintln(&b, "当前无此范围内的失败任务；不代表从未发生错误。")
		} else {
			keys := make([]string, 0, len(snap.Failures))
			for k := range snap.Failures {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s：%d\n", k, snap.Failures[k])
			}
		}
	} else {
		fmt.Fprintln(&b, "Bot 诊断快照：未检查（未运行、旧版本或响应不受支持）")
	}
	fmt.Fprintln(&b, "\n## 容器日志轮转（实际配置）")
	logStateLabels := map[string]string{
		"ok":      "已生效（每份不超过10m，最多3份）",
		"pending": "未采用项目轮转限额（旧容器需确认更新）",
		"custom":  "已有限额，但与项目设置不同；请核对",
		"other":   "使用其他日志驱动；需核对其保留策略",
		"unknown": "未核实（未使用、不可读或归属未确认）",
	}
	for _, label := range []string{"Bot", "网关", "共享 Nginx", "Certbot"} {
		st := logStateLabels[logRotation[label]]
		if st == "" {
			st = logStateLabels["unknown"]
		}
		fmt.Fprintf(&b, "%s：%s\n", label, st)
	}
	fmt.Fprintln(&b, "\n管理入口："+fallbackText(entrySummary, "未核实"))
	fmt.Fprintln(&b, "未执行 WebDAV 写入、服务重启或软件安装。")
	return b.String()
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
