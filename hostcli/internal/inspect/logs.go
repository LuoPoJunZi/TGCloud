package inspect

import (
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const logConfigLimit = 128 * 1024

var (
	configKey  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	ansiCSI    = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	botToken   = regexp.MustCompile(`[0-9]{6,12}:[A-Za-z0-9_-]{20,}`)
	urlLogin   = regexp.MustCompile(`(?i)(https?://)[^\s/:@]+:[^\s/@]+@`)
	secretLine = regexp.MustCompile(`(?im)^.*(?:["']?(?:authorization|(?:set[-_])?cookie|password|passwd|` +
		`(?:access[_-]|refresh[_-]|bot[_-]|oauth[_-])?token|api[_-]?(?:hash|key)|secret)["']?\s*[:=]|密码\s*[:：]).*$`)
	errLogConfig = errors.New("无法核实日志脱敏配置，日志未读取")
)

func LogRedactor(directory string) (func(string) string, error) {
	config, err := readLogConfig(directory)
	if err != nil {
		return nil, errLogConfig
	}
	return newLogRedactor(config)
}

func newLogRedactor(config []byte) (func(string) string, error) {
	if len(config) > logConfigLimit || !utf8.Valid(config) {
		return nil, errLogConfig
	}
	var secrets []string
	keys := make(map[string]bool)
	for _, line := range strings.Split(string(config), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !configKey.MatchString(key) || keys[key] {
			return nil, errLogConfig
		}
		keys[key] = true
		if value == "" || !isSecretKey(key) {
			continue
		}
		secrets = append(secrets, value)
		if strings.HasSuffix(key, "_B64") {
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err == nil && len(decoded) != 0 && utf8.Valid(decoded) {
				secrets = append(secrets, string(decoded))
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	var pairs []string
	for _, secret := range secrets {
		pairs = append(pairs, secret, "[REDACTED]")
	}
	replaceSecrets := strings.NewReplacer(pairs...)
	return func(text string) string {
		text = ansiCSI.ReplaceAllString(text, "")
		text = strings.Map(func(char rune) rune {
			if unicode.IsControl(char) && char != '\t' && char != '\n' {
				return -1
			}
			return char
		}, text)
		text = secretLine.ReplaceAllString(text, "[REDACTED]")
		text = botToken.ReplaceAllString(text, "[REDACTED_BOT_TOKEN]")
		text = urlLogin.ReplaceAllString(text, "$1[REDACTED]@")
		text = replaceSecrets.Replace(text)
		return strings.TrimSpace(text)
	}, nil
}

func isSecretKey(key string) bool {
	return strings.Contains(key, "PASSWORD") || strings.Contains(key, "TOKEN") ||
		strings.Contains(key, "HASH") || strings.Contains(key, "SECRET")
}
