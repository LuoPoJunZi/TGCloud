package inspect

import (
	"strings"
	"testing"
)

func TestInstallActionKeepsVersionAndSourceRules(t *testing.T) {
	oldDigest, newDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, scenario := range []struct {
		current, target, digest, want string
	}{
		{"", "0.0.6", oldDigest, "install"},
		{"0.0.6", "0.0.6", oldDigest, "current"},
		{"0.0.6", "0.0.6", newDigest, "upgrade"},
		{"0.0.9", "0.0.10", newDigest, "upgrade"},
		{"0.10.0", "0.9.100", newDigest, "newer"},
		{"1.0.0", "0.9.100", newDigest, "newer"},
		{"0.9.100", "1.0.0", newDigest, ""},
	} {
		instance := InstallInstance{Present: scenario.current != "", Version: scenario.current, SourceDigest: oldDigest}
		got, err := chooseInstallAction(instance, scenario.target, scenario.digest)
		if got != scenario.want || (err != nil) != (scenario.want == "") {
			t.Errorf("%q to %q yielded %q, %v; want %q", scenario.current, scenario.target, got, err, scenario.want)
		}
	}
}
