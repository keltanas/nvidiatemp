package gpu

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// commandTimeout bounds a single nvidia-smi / nvidia-settings call so a
// hung subprocess becomes a poll error instead of stalling the daemon.
const commandTimeout = 10 * time.Second

// Runner executes a command and returns its combined output. It is an
// injectable seam: unit tests substitute it so no real subprocess runs.
type Runner func(name string, args []string, env []string) (string, error)

// defaultRunner is the real subprocess implementation.
var defaultRunner Runner = func(name string, args []string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// displayEnv returns the process environment with DISPLAY forced to :0 —
// nvidia-settings needs the X display for both reads and writes.
func displayEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "DISPLAY=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "DISPLAY=:0")
}
