package inspect

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestBuildCandidateValidatesOnlyANativeBuild(t *testing.T) {
	const stage = "/opt/tgcloud-release-Abcd0123"
	const tag = "tgcloud-clouddrive2-candidate:123-456"
	native := PreflightResult{Architecture: "amd64", Platform: "linux/amd64"}
	commands := [][]string{
		{"build", "--platform", native.Platform, "--tag", tag, stage},
		{"image", "inspect", "--format", imagePlatform, tag},
		{"run", "--rm", "--label", candidateLabel + "=" + tag, "--read-only", "--network", "none", "--tmpfs", "/tmp",
			"--entrypoint", "python", "--env-file", filepath.Join(stage, ".env"), tag,
			"-m", "app.deployment_check", "--validate-only"},
	}
	for _, test := range []struct {
		name      string
		failAfter int
	}{
		{"success", 0},
		{"build fails", 1},
		{"non-native image", 2},
		{"validation fails", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			docker := func(args ...string) ([]byte, error) {
				if calls >= len(commands) || !slices.Equal(args, commands[calls]) {
					t.Fatalf("unexpected Docker command: %q", args)
				}
				calls++
				if calls == test.failAfter {
					if calls == 2 {
						return []byte(`{"os":"linux","arch":"arm64"}`), nil
					}
					return nil, errors.New("Docker fixture failure")
				}
				return []byte(`{"os":"linux","arch":"amd64"}`), nil
			}
			err := buildCandidate(stage, tag, native, docker)
			wantCalls := len(commands)
			if test.failAfter != 0 {
				wantCalls = test.failAfter
			}
			if calls != wantCalls || (err != nil) != (test.failAfter != 0) {
				t.Fatalf("Docker calls=%d, want=%d; error=%v", calls, wantCalls, err)
			}
		})
	}
}
