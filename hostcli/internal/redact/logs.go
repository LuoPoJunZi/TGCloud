package redact

import (
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

const LogConfigLimit = 128 * 1024

var (
	ansiCSI    = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	botToken   = regexp.MustCompile(`[0-9]{6,12}:[A-Za-z0-9_-]{20,}`)
	urlLogin   = regexp.MustCompile(`(?i)(https?://)[^\s/:@]+:[^\s/@]+@`)
	secretLine = regexp.MustCompile(`(?im)^.*(?:["']?(?:authorization|(?:set[-_])?cookie|password|passwd|` +
		`(?:access[_-]|refresh[_-]|bot[_-]|oauth[_-])?token|api[_-]?(?:hash|key)|secret)["']?\s*[:=]|密码\s*[:：]).*$`)
	ErrLogConfig = errors.New("无法核实日志脱敏配置，日志未读取")
)

func NewLogRedactor(config []byte) (func(string) string, error) {
	if len(config) > LogConfigLimit {
		return nil, ErrLogConfig
	}
	values, err := hostutil.ParseEnvLines(config, ErrLogConfig)
	if err != nil {
		return nil, ErrLogConfig
	}
	var secrets []string
	for key, value := range values {
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
	sort.Slice(secrets, func(i, j int) bool {
		if len(secrets[i]) != len(secrets[j]) {
			return len(secrets[i]) > len(secrets[j])
		}
		return secrets[i] < secrets[j]
	})
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
