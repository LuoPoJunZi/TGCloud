package hostutil

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrLinuxRequired = errors.New("此操作需要 Linux 宿主机")
	ErrOutputLimit   = errors.New("Docker 输出超过 8 MiB")
	EnvKeyPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	OptPathRegex     = regexp.MustCompile(`^/opt/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`)
)

const MaxOutputBytes = 8 * 1024 * 1024

type BoundedBuffer struct {
	buf bytes.Buffer
}

func (b *BoundedBuffer) Write(chunk []byte) (int, error) {
	if len(chunk) > MaxOutputBytes-b.buf.Len() {
		return 0, ErrOutputLimit
	}
	return b.buf.Write(chunk)
}

func (b *BoundedBuffer) Bytes() []byte { return b.buf.Bytes() }

func (b *BoundedBuffer) String() string { return b.buf.String() }

func UnescapeMountPath(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+3 < len(raw) &&
			raw[i+1] >= '0' && raw[i+1] <= '7' &&
			raw[i+2] >= '0' && raw[i+2] <= '7' &&
			raw[i+3] >= '0' && raw[i+3] <= '7' {
			val := (raw[i+1]-'0')*64 + (raw[i+2]-'0')*8 + (raw[i+3] - '0')
			out.WriteByte(val)
			i += 3
			continue
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}

func ParseEnvLines(data []byte, invalid error) (map[string]string, error) {
	if !utf8.Valid(data) {
		return nil, invalid
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if _, duplicate := values[key]; !found || !EnvKeyPattern.MatchString(key) || duplicate {
			return nil, invalid
		}
		values[key] = value
	}
	return values, nil
}

func ParseHostPortRange(mapping string, tcpOnly bool) (int, int, bool) {
	host, container, published := strings.Cut(mapping, "->")
	colon := strings.LastIndexByte(host, ':')
	if !published || colon < 0 {
		return 0, 0, false
	}
	if tcpOnly && !strings.HasSuffix(container, "/tcp") {
		return 0, 0, false
	}
	left, right, ranged := strings.Cut(host[colon+1:], "-")
	first, err := strconv.Atoi(left)
	if err != nil {
		return 0, 0, false
	}
	last := first
	if ranged {
		last, err = strconv.Atoi(right)
		if err != nil {
			return 0, 0, false
		}
	}
	return first, last, true
}

func ParseOSRelease(release string, unquotedOK func(string) bool, keys ...string) (map[string]string, bool) {
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}
	values := make(map[string]string, len(keys))
	for _, line := range strings.Split(release, "\n") {
		key, raw, present := strings.Cut(line, "=")
		if !present || !wanted[key] {
			continue
		}
		value := strings.TrimSpace(raw)
		if unquotedOK != nil {
			switch {
			case len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"':
				var err error
				value, err = strconv.Unquote(value)
				if err != nil {
					return nil, false
				}
			case len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
				value = value[1 : len(value)-1]
			case !unquotedOK(value):
				return nil, false
			}
			if strings.ContainsFunc(value, func(r rune) bool { return r < 32 }) {
				return nil, false
			}
		} else if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[0] == value[len(value)-1] {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	return values, true
}
