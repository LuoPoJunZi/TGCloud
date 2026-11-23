package inspect

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLogRedactorMasksConfiguredValuesAndCredentials(t *testing.T) {
	const password = "example-log-password"
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	clean, err := newLogRedactor([]byte("CD2_WEBDAV_PASSWORD_B64=" + encoded + "\nTELEGRAM_API_HASH=Author\n"))
	if err != nil {
		t.Fatal(err)
	}
	const token = "123456789:synthetic_Telegram_bot_token"
	input := "unrelated line\nvalues " + password + " " + encoded + "\n" +
		"\x1b[1mAuthorization\x1b[0m: Bearer synthetic-credential\n" +
		"connection https://fixture:other-password@example.test/dav token is " + token
	output := clean(input)
	for _, secret := range []string{password, encoded, "synthetic-credential", "other-password", token, "\x1b"} {
		if strings.Contains(output, secret) {
			t.Fatalf("log still contains %q", secret)
		}
	}
	if !strings.Contains(output, "unrelated line") || !strings.Contains(output, "example.test/dav") {
		t.Fatalf("ordinary log content was lost: %q", output)
	}
}
