//go:build !windows

package studio

import "os/exec"

// The child owns no subprocesses. The parent's sole write end is closed by the
// kernel on exit. EOF cancels all child HTTP contexts and wakes the ACK waits.
func chatChildGuard(_ *exec.Cmd) (func() error, func(), error) {
	return func() error { return nil }, func() {}, nil
}
