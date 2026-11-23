package inspect

import (
	"encoding/json"
	"errors"
)

var readinessFields = []struct{ Key, Label string }{
	{"heartbeat", "运行心跳"},
	{"telegram", "Telegram 连接"},
	{"destination", "WebDAV 最近目录探测"},
	{"queue", "队列调度"},
	{"capacity", "下载资源保护"},
}

var readinessStates = map[string]string{
	"ok":       "通过",
	"failed":   "未通过",
	"unknown":  "未核实／记录过期",
	"partial":  "仅账号根目录可读，目标目录未确认",
	"paused":   "手动暂停",
	"cooldown": "冷却中",
}

type Check struct {
	Label, Status string
}

type Report struct {
	Ready   bool
	Checks  []Check
	States  map[string]string
	Payload json.RawMessage
}

func (instance Instance) UnreadyReport() map[string]any {
	checks := make(map[string]string, len(readinessFields))
	for _, field := range readinessFields {
		checks[field.Key] = "unknown"
	}
	return map[string]any{"schema": 1, "version": instance.Version, "ready": false, "checks": checks}
}

func (instance Instance) Readiness(docker DockerRunner) (Report, error) {
	output, err := docker("exec", instance.Bot.ID, "python", "-B", "-m", "tgcloud.monitoring.readiness")
	if err != nil {
		return Report{}, err
	}
	var result struct {
		Schema           int
		Version, Edition string
		Ready            *bool
		Checks           map[string]string
	}
	invalidReport := errors.New("Bot 就绪报告无效，请核对运行版本与日志")
	if len(output) > 16*1024 || json.Unmarshal(output, &result) != nil || result.Schema != 1 ||
		result.Version != instance.Version || (result.Edition != "" && result.Edition != "clouddrive2") ||
		result.Ready == nil || len(result.Checks) != len(readinessFields) {
		return Report{}, invalidReport
	}

	report := Report{Ready: true, Payload: output, States: result.Checks}
	for _, field := range readinessFields {
		value := result.Checks[field.Key]
		status, ok := readinessStates[value]
		if !ok {
			return Report{}, invalidReport
		}
		report.Ready = report.Ready && value == "ok"
		report.Checks = append(report.Checks, Check{Label: field.Label, Status: status})
	}
	if report.Ready != *result.Ready {
		return Report{}, invalidReport
	}
	return report, nil
}
