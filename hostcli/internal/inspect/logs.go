package inspect

import "github.com/LuoPoJunZi/TGCloud/hostcli/internal/redact"

const logConfigLimit = redact.LogConfigLimit

func LogRedactor(directory string) (func(string) string, error) {
	config, err := readLogConfig(directory)
	if err != nil {
		return nil, redact.ErrLogConfig
	}
	return redact.NewLogRedactor(config)
}
