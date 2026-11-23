//go:build !linux

package tui

import (
	"fmt"
	"io"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

func RunMenu(directory string, in io.Reader, out io.Writer) int {
	fmt.Fprintln(out, hostutil.ErrLinuxRequired)
	return 1
}

func RunDiagnosticsCommand(directory, saveDir string, out io.Writer) error {
	return hostutil.ErrLinuxRequired
}
