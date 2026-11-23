package inspect

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// A signal sent to the entry process must not interrupt recovery subprocesses.
func isolateInstallCommand(command *exec.Cmd, killGroup bool) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if killGroup {
		command.Cancel = func() error {
			err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
	}
	command.WaitDelay = 15 * time.Second
}
