package inspect

import (
	"slices"
	"strings"
	"testing"
)

func TestNativeImagePreparationReusesCacheAndChecksPullResult(t *testing.T) {
	reference := "registry.example.test/fixture@sha256:" + strings.Repeat("a", 64)
	native := PreflightResult{Architecture: "amd64", Platform: "linux/amd64"}
	for _, test := range []struct {
		name, before, after string
		wantPull, wantError bool
	}{
		{"native cache", "amd64", "", false, false},
		{"native pull", "arm64", "amd64", true, false},
		{"pull did not provide native image", "arm64", "arm64", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pulled := false
			docker := func(args ...string) ([]byte, error) {
				switch {
				case slices.Equal(args, []string{"image", "inspect", "--format", imagePlatform, reference}):
					arch := test.before
					if pulled {
						arch = test.after
					}
					return []byte(`{"os":"linux","arch":"` + arch + `"}`), nil
				case !pulled && slices.Equal(args, []string{"pull", "--platform", native.Platform, reference}):
					pulled = true
					return nil, nil
				default:
					t.Fatalf("unexpected Docker arguments: %q", args)
					return nil, nil
				}
			}
			err := ensureNativeImage(reference, native, docker)
			if pulled != test.wantPull || (err != nil) != test.wantError {
				t.Fatalf("pulled=%t error=%v", pulled, err)
			}
		})
	}
}
