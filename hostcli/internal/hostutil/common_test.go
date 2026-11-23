package hostutil

import (
	"errors"
	"strings"
	"testing"
)

func TestParseEnvLinesRejectsDuplicatesAndInvalidKeys(t *testing.T) {
	invalid := errors.New("invalid")
	values, err := ParseEnvLines([]byte("# comment\r\nFOO=bar\r\nBAZ=1=2\n"), invalid)
	if err != nil || values["FOO"] != "bar" || values["BAZ"] != "1=2" {
		t.Fatalf("unexpected parse result: %v, %v", values, err)
	}
	if _, err := ParseEnvLines([]byte("FOO=1\nFOO=2\n"), invalid); !errors.Is(err, invalid) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if _, err := ParseEnvLines([]byte("bad_key=1\n"), invalid); !errors.Is(err, invalid) {
		t.Fatalf("expected invalid key error, got %v", err)
	}
}

func TestParseHostPortRange(t *testing.T) {
	first, last, ok := ParseHostPortRange("0.0.0.0:80-81->80-81/tcp", true)
	if !ok || first != 80 || last != 81 {
		t.Fatalf("expected 80-81 tcp range, got %d-%d ok=%v", first, last, ok)
	}
	if _, _, ok := ParseHostPortRange("0.0.0.0:443->443/udp", true); ok {
		t.Fatalf("udp mapping should be ignored when tcpOnly is true")
	}
	if first, last, ok := ParseHostPortRange("127.0.0.1:19798->19798/udp", false); !ok || first != 19798 || last != 19798 {
		t.Fatalf("expected non-tcpOnly match, got %d-%d ok=%v", first, last, ok)
	}
}

func TestParseOSRelease(t *testing.T) {
	values, ok := ParseOSRelease("ID=ubuntu\nUBUNTU_CODENAME=\"noble\"\n", nil, "ID", "UBUNTU_CODENAME")
	if !ok || values["ID"] != "ubuntu" || values["UBUNTU_CODENAME"] != "noble" {
		t.Fatalf("unexpected lenient os-release parse: %v ok=%v", values, ok)
	}
	strictWord := func(value string) bool { return value != "" && !strings.ContainsAny(value, "\"' \t") }
	if _, ok := ParseOSRelease("ID=ubuntu\nPRETTY_NAME=\"bad\n", strictWord, "ID", "PRETTY_NAME"); ok {
		t.Fatalf("strict mode must reject unterminated quotes")
	}
	if _, ok := ParseOSRelease("ID=ubuntu\nPRETTY_NAME=\"bad\\q\"\n", strictWord, "ID", "PRETTY_NAME"); ok {
		t.Fatalf("strict mode must reject invalid double-quoted escapes")
	}
}
