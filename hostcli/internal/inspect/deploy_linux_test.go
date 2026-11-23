package inspect

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestDeployBotStopsAtFailedStep(t *testing.T) {
	for _, test := range []struct {
		name, failure, message string
		steps                  []string
	}{
		{"healthy", "", "", []string{"build", "validate", "up", "health"}},
		{"build failed", "build", "镜像构建失败", []string{"build"}},
		{"validation failed", "validate", "配置校验失败", []string{"build", "validate"}},
		{"start failed", "up", "容器启动失败", []string{"build", "validate", "up"}},
		{"unhealthy", "health", "未通过基础健康检查", []string{"build", "validate", "up", "health"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var steps []string
			record := func(step string) error {
				steps = append(steps, step)
				if step == test.failure {
					return errors.New("fixture failure")
				}
				return nil
			}
			result := deployBot(io.Discard,
				func(args ...string) error { return record(args[0]) },
				func() error { return record("validate") },
				func() string {
					if record("health") != nil {
						return "unhealthy"
					}
					return "healthy"
				})
			if !slices.Equal(steps, test.steps) {
				t.Fatalf("called %v, want %v", steps, test.steps)
			}
			if test.failure == "" {
				if result != nil {
					t.Fatal(result)
				}
			} else if result == nil || !strings.Contains(result.Error(), test.message) {
				t.Fatalf("wrong failure message: %v", result)
			}
		})
	}
}
