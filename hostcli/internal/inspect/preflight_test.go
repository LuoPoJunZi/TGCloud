package inspect

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestDockerPreflightDaemonPolicy(t *testing.T) {
	for _, scenario := range []struct {
		name, endpoint, architecture string
		security                     []string
		allowed                      bool
	}{
		{"本机原生", localDockerHost, "x86_64", nil, true},
		{"远端 context", "tcp://engine.example.test:2375", "x86_64", nil, false},
		{"rootless", localDockerHost, "x86_64", []string{"name=rootless"}, false},
		{"架构不匹配", localDockerHost, "aarch64", nil, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			docker := func(args ...string) ([]byte, error) {
				switch {
				case slices.Equal(args, []string{"context", "inspect", "--format", contextEndpoint}):
					return json.Marshal(scenario.endpoint)
				case scenario.endpoint == localDockerHost && slices.Equal(args, []string{"info", "--format", preflightInfo}):
					return json.Marshal(map[string]any{"os": "linux", "arch": scenario.architecture,
						"version": "28.5.2", "security": scenario.security})
				default:
					t.Fatalf("unexpected Docker arguments: %q", args)
					return nil, nil
				}
			}
			info, err := checkDockerDaemon("amd64", docker)
			if scenario.allowed {
				if err != nil || info == nil || info.Platform != "linux/amd64" || info.Version != "28.5.2" {
					t.Fatalf("result=%+v, error=%v", info, err)
				}
			} else if err == nil {
				t.Fatalf("daemon should have been rejected: %+v", info)
			}
		})
	}
}
