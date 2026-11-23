package inspect

import (
	"errors"
	"strings"
	"syscall"
)

func hostUnameMachine() (string, error) {
	var utsname syscall.Utsname
	if err := syscall.Uname(&utsname); err != nil {
		return "", errors.New("无法核实宿主 CPU 架构")
	}
	var architecture strings.Builder
	for _, character := range utsname.Machine {
		if character == 0 {
			break
		}
		architecture.WriteByte(byte(character))
	}
	return architecture.String(), nil
}
