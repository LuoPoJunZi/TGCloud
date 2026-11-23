package webdav

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func TestWriteAuthStatusAndVerify(t *testing.T) {
	prevUID := hostutil.ManagedOwnerUID
	hostutil.ManagedOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { hostutil.ManagedOwnerUID = prevUID })

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	webdavRoot := filepath.Join(root, "webdav")
	authRoot := filepath.Join(root, "external-auth")
	if err := os.MkdirAll(filepath.Join(webdavRoot, "clouddrive2"), 0700); err != nil {
		t.Fatal(err)
	}
	record := `{"schema":1,"owner":"tgcloud.external_webdav","edition":"clouddrive2","instance_dir":"/opt/tgcloud","domain":"dav.example.com","username":"tgcloud-webdav","target_dir":"/Media","phase":"enabled","scope_verification":"manual-native-readonly","binding":"` + strings.Repeat("a", 64) + `","revision":"` + strings.Repeat("b", 32) + `"}`
	if err := os.WriteFile(filepath.Join(webdavRoot, "clouddrive2/config.json"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}

	validLine := "tgcloud-webdav:$6$rounds=200000$" + strings.Repeat("a", 16) + "$" + strings.Repeat("b", 86) + "\n"
	digest, err := WriteAuth(authRoot, "clouddrive2", 101, []byte(validLine))
	if err != nil || len(digest) != 64 {
		t.Fatalf("WriteAuth failed: digest=%q err=%v", digest, err)
	}
	status, err := Status(webdavRoot, authRoot, "clouddrive2")
	if err != nil || !status.Present || !status.AuthPresent || status.AuthUserCount != 1 || status.AuthSHA256 != digest {
		t.Fatalf("unexpected status: %+v, err=%v", status, err)
	}

	docker := func(args ...string) ([]byte, error) {
		return []byte(digest + "  /etc/nginx/external-auth/clouddrive2.htpasswd\n"), nil
	}
	got, err := VerifyMountedAuth(authRoot, "clouddrive2", docker)
	if err != nil || got != digest {
		t.Fatalf("VerifyMountedAuth failed: got=%q err=%v", got, err)
	}

	// Revocation writes disabled:!
	revDigest, err := WriteAuth(authRoot, "clouddrive2", 101, []byte("disabled:!\n"))
	if err != nil || revDigest == digest {
		t.Fatalf("revoke WriteAuth failed: digest=%q err=%v", revDigest, err)
	}
	status, err = Status(webdavRoot, authRoot, "clouddrive2")
	if err != nil || status.AuthUserCount != 0 {
		t.Fatalf("expected 0 active users after revoke: %+v, err=%v", status, err)
	}

	// Weak or malformed htpasswd must fail closed.
	if _, err := WriteAuth(authRoot, "clouddrive2", 101, []byte("user:$apr1$weak$hash\n")); err == nil {
		t.Fatal("expected weak hash rejection")
	}
}
