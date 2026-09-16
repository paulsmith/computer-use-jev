//go:build unix

package computeruse

import "syscall"

func newProcessGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func signalProcessGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != syscall.ESRCH {
		return err
	}
	if err := syscall.Kill(pid, sig); err != syscall.ESRCH {
		return err
	}
	return nil
}
