package inspect

import (
	"bytes"
	"strings"
	"testing"
)

func TestInstallResourceLimitsPreserveShellThresholds(t *testing.T) {
	for _, test := range []struct {
		name, failure      string
		memoryKiB, freeKiB uint64
	}{
		{"one CPU at both minimums", "", 1800 * 1024, 8_000_000},
		{"below minimum memory", "内存不足", 1800*1024 - 1, 8_000_000},
		{"below minimum free disk", "可用空间不足", 1800 * 1024, 7_999_999},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			disk := diskResources{total: 44_999_999 * 1024, available: test.freeKiB * 1024}
			err := checkInstallResourceLimits(1, test.memoryKiB, disk, &output)
			if test.failure == "" {
				if err != nil || !strings.Contains(output.String(), "低于推荐的 50GB") {
					t.Fatalf("small disk should warn at the available-space minimum: %v, %s", err, output.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), test.failure) {
				t.Fatalf("want %q, got %v", test.failure, err)
			}
		})
	}
}

func TestMemoryProbeUsesAvailableEvenWhenItIsZero(t *testing.T) {
	const base = "MemTotal: 4096 kB\nMemFree: 1024 kB\nSwapTotal: 512 kB\n"
	for _, scenario := range []struct {
		stats     string
		available uint64
	}{
		{base + "MemAvailable: 0 kB\n", 0},
		{base, 1024},
	} {
		info, err := parseMemory(scenario.stats)
		if err != nil || info != (resourceMemory{4096, scenario.available, 512}) {
			t.Fatalf("memory=%+v, error=%v", info, err)
		}
	}
}
