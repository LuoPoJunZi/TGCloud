//go:build !linux

package deps

import (
	"io"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func PrepareDependencies(upgrade bool, progress io.Writer) error {
	return hostutil.ErrLinuxRequired
}
