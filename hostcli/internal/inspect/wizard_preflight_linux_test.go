package inspect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWizardConfigIsPrivateAndUpgradeOnlyWritesPreserveFlag(t *testing.T) {
	for _, action := range []string{"install", "upgrade"} {
		t.Run(action, func(t *testing.T) {
			private, err := os.MkdirTemp(t.TempDir(), "tgcloud-cli-payload-")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(private, "config.env")
			input := "[[\"TZ\",\"Asia/Shanghai\"],[\"WEBDAV_TARGET_PATH_B64\",\"YWJjZA==\"]]"
			want := "TZ=Asia/Shanghai\nWEBDAV_TARGET_PATH_B64=YWJjZA==\n"
			if action == "upgrade" {
				want = "TGCLOUD_REDEPLOY_APPLY_CONFIG=false\n"
			}
			if err := writeWizardConfig(path, action, strings.NewReader(input)); err != nil {
				t.Fatal(err)
			}
			if err := writeWizardConfig(path, action, strings.NewReader("[]")); err == nil {
				t.Fatal("the candidate was allowed to overwrite a file")
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != want {
				t.Fatalf("candidate bytes differ: %q, %v", body, err)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("candidate is not private: %v", err)
			}
		})
	}
}
