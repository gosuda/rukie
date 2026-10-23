//go:build linux

package rukproc

import "syscall"

func setDeathSig(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGTERM
}
