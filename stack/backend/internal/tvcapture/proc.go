package tvcapture

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// utcEnv returns the current environment with TZ pinned to UTC so ffmpeg's
// strftime segment names are UTC and round-trip through parseSegmentTime.
func utcEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TZ=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TZ=UTC")
}

// signalTerm sends SIGTERM to a started process; a nil or unstarted command is a
// no-op. Reaping is the caller's Wait, never here (a command's Wait runs once).
func signalTerm(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
}

// killProcess force-kills a started process; a nil or unstarted command is a
// no-op.
func killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
