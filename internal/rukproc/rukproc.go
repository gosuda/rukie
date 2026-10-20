package rukproc

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ResolveBinary finds the ruk binary. An explicit path wins, then RUK_BIN,
// then PATH. Missing binaries and directories fail closed.
func ResolveBinary(explicit string) (string, error) {
	bin := cmp.Or(explicit, os.Getenv("RUK_BIN"), "ruk")
	if strings.Contains(bin, "/") {
		if info, err := os.Stat(bin); err != nil || info.IsDir() {
			return "", fmt.Errorf("rukproc: ruk binary %q not found (set RUK_BIN)", bin)
		}
		return bin, nil
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("rukproc: ruk binary %q not found (set RUK_BIN)", bin)
	}
	return resolved, nil
}

// Child owns one running ruk serve process.
type Child struct {
	cmd *exec.Cmd
}

// StartServe starts ruk serve --config with fixed argv and no shell. env
// fully determines the child environment. stdout and stderr receive the
// daemon logs; pass buffers to keep failure diagnostics.
func StartServe(bin, configPath string, env []string, stdout, stderr io.Writer) (*Child, error) {
	cmd := exec.Command(bin, "serve", "--config", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	setDeathSig(cmd.SysProcAttr)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("rukproc: start ruk serve: %w", err)
	}
	return &Child{cmd: cmd}, nil
}

// Wait blocks until the child exits and reports its status.
func (c *Child) Wait() error {
	if err := c.cmd.Wait(); err != nil {
		return fmt.Errorf("rukproc: wait ruk serve: %w", err)
	}
	return nil
}

// Signal delivers sig to the child.
func (c *Child) Signal(sig os.Signal) error {
	if err := c.cmd.Process.Signal(sig); err != nil {
		return fmt.Errorf("rukproc: signal ruk serve: %w", err)
	}
	return nil
}

// Kill terminates the child with SIGKILL, reaps it, and reports how it died.
func (c *Child) Kill() error {
	if err := c.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return c.Wait()
}

// Run executes one short ruk invocation and returns its stdout. env fully
// determines the child environment. Failures carry the trimmed stderr.
func Run(ctx context.Context, bin string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rukproc: ruk %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
