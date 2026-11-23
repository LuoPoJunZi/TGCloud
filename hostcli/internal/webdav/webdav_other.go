//go:build !linux

package webdav

import (
	"io"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func Supported() bool { return false }

func Run(args []string, input io.Reader) (any, error) {
	return nil, hostutil.ErrLinuxRequired
}
