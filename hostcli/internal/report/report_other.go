//go:build !linux

package report

import (
	"io"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func Install(directory string, progress io.Writer) error {
	return hostutil.ErrLinuxRequired
}
