//go:build !windows

package videoexport

import "os/exec"

// Only the Windows controlled executor has been implemented and verified.
func runControlled(*exec.Cmd) error { return ErrUnavailable }
