//go:build !linux

package backupops

import "github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"

func Supported() bool { return false }

func Run(args []string) (any, error) {
	return nil, hostutil.ErrLinuxRequired
}
