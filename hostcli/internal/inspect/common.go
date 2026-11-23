package inspect

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	envKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	optPathRegex  = regexp.MustCompile(`^/opt/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`)
)

func parseEnvLines(data []byte, invalid error) (map[string]string, error) {
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
		if _, duplicate := values[key]; !found || !envKeyPattern.MatchString(key) || duplicate {
			return nil, invalid
		}
		values[key] = value
	}
	return values, nil
}

func parseHostPortRange(mapping string, tcpOnly bool) (int, int, bool) {
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

func parseOSRelease(release string, unquotedOK func(string) bool, keys ...string) (map[string]string, bool) {
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
