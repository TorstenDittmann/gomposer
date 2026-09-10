//go:build unix

package scripts

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group so a
// cancelled CommandContext (timeout / Ctrl-C) kills shell grandchildren
// such as `sleep` started by `sh -c`.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
