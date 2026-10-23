//go:build !linux

package rukproc

import "syscall"

func setDeathSig(_ *syscall.SysProcAttr) {}
