package inspect

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallAcceptanceSummarizesOwnedRuntimeWithoutWriting(t *testing.T) {
	directory, docker := installAcceptanceFixture(t)
	for _, sample := range []struct {
		action, mode, target, webdav, outcome string
		failed                                bool
	}{
		{"install", "https", "0.0.6", "ok", "completed", false},
		{"upgrade", "lan", "0.0.6", "not_run", "completed", false},
		{"upgrade", "https", "0.0.6", "failed", "failed", true},
		{"upgrade", "lan", "0.0.7", "not_run", "completed", false},
	} {
		t.Run(sample.action+"/"+sample.mode+"/"+sample.target+"/"+sample.webdav, func(t *testing.T) {
			evidence := installEvidence{Action: sample.action, Mode: sample.mode, Target: sample.target,
				Completed: !sample.failed, Failed: sample.failed,
				Checks: map[string]string{"base": "ok", "entry": "ok", "code": "ok", "webdav": sample.webdav}}
			summary := summarizeInstallEvidence(evidence)
			summary.observe(directory, evidence, docker)
			if summary.Outcome != sample.outcome || summary.Checks["base"] != "ok" ||
				summary.Checks["entry"] != "ok" || summary.Checks["webdav"] != sample.webdav {
				t.Fatalf("unexpected stage summary: %+v", summary)
			}
			if sample.target == "0.0.6" {
				if summary.ActualVersion != sample.target || summary.Readiness["destination"] != "partial" ||
					summary.LogRotation["Bot"] != "ok" {
					t.Fatalf("read-only observations missing: %+v", summary)
				}
			} else if summary.ActualVersion != "" || summary.Readiness != nil || summary.Checks["code"] != "unknown" {
				t.Fatalf("a prior Bot was reported as the target version: %+v", summary)
			}
		})
	}
}

func TestInstallAcceptanceWebDAVRequiresExitSuccessAndUnambiguousResult(t *testing.T) {
	directory, inspect := installAcceptanceFixture(t)
	for _, sample := range []struct {
		output string
		err    error
		pass   bool
	}{
		{"TGCLOUD_WEBDAV_SIZE=OK\nTGCLOUD_DESTINATION=OK\n", nil, true},
		{"TGCLOUD_DESTINATION=OK\n", errors.New("failed"), false},
		{"TGCLOUD_DESTINATION=OK\nTGCLOUD_DESTINATION=FAILED: PRIVATE_FIXTURE\n", nil, false},
		{"TGCLOUD_WEBDAV_SIZE=OK\n", nil, false},
	} {
		var report bytes.Buffer
		calls := 0
		run := func(args ...string) ([]byte, error) {
			calls++
			want := []string{"exec", strings.Repeat("a", 64), "python", "-m", "app.verify_destination"}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("wrong WebDAV command: %v", args)
			}
			return []byte(sample.output), sample.err
		}
		err := verifyInstallWebDAV(directory, "0.0.6", inspect, run, &report)
		if (err == nil) != sample.pass || calls != 1 || strings.Contains(report.String(), "PRIVATE_FIXTURE") ||
			strings.Contains(report.String(), "TGCLOUD_DESTINATION=OK") != sample.pass {
			t.Fatalf("incorrect WebDAV result: %v, %d, %s", err, calls, report.String())
		}
	}
}

func installAcceptanceFixture(t *testing.T) (string, DockerRunner) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	marker := `{"schema":1,"owner":"tgcloud.local-source","project":"tgcloud","mode":"shell","edition":"clouddrive2","state":"ready","version":"0.0.6"}`
	if err := os.WriteFile(filepath.Join(directory, "tgcloud-source.json"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	container := func(name, service, id string, files map[string]string) Container {
		result := Container{ID: strings.Repeat(id, 64), Name: "/" + name, Directory: directory,
			Project: "tgcloud", Service: service, State: "running", Health: "healthy"}
		for dest, file := range files {
			result.Mounts = append(result.Mounts, mount{"bind", filepath.Join(directory, file), dest})
		}
		return result
	}
	bot := container(botName, botName, "a", map[string]string{"/data": "data", "/config": "config", "/downloads": "downloads", "/logs": "logs"})
	gateway := container(gatewayName, "clouddrive2", "b", map[string]string{"/Config": "clouddrive/config", "/CloudNAS": "clouddrive/mounts"})
	return directory, func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "container" && args[1] == "ls":
			return []byte(botName + "\n" + gatewayName), nil
		case args[0] == "container" && args[3] == containerFields:
			a, _ := json.Marshal(bot)
			b, _ := json.Marshal(gateway)
			return append(append(a, '\n'), b...), nil
		case args[0] == "container" && args[3] == installLogFields:
			item := bot
			switch args[4] {
			case gateway.ID:
				item = gateway
			case bot.ID:
			default:
				return nil, errors.New("fixture proxy does not exist")
			}
			return json.Marshal(map[string]string{"driver": "json-file", "size": "10m", "files": "3",
				"directory": item.Directory, "service": item.Service, "project": item.Project})
		case args[0] == "exec" && args[1] == bot.ID && args[len(args)-1] == "tgcloud.monitoring.readiness":
			return []byte(`{"schema":1,"version":"0.0.6","edition":"clouddrive2","ready":false,"checks":{"heartbeat":"ok","telegram":"ok","destination":"partial","queue":"ok","capacity":"ok"}}`), nil
		default:
			t.Fatalf("unexpected or mutating Docker operation: %v", args)
			return nil, nil
		}
	}
}
