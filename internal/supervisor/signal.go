package supervisor

import (
	"os"
	"syscall"
)

func terminateSignal() os.Signal { return syscall.SIGTERM }

func killSignal() os.Signal { return syscall.SIGKILL }
