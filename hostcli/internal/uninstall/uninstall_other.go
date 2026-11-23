//go:build !linux

package uninstall

import (
	"io"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func Supported() bool { return false }

func Run(args []string, progress io.Writer) (any, error) {
	return nil, hostutil.ErrLinuxRequired
}
