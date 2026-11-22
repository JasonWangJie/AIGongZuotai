//go:build linux || darwin

package studio

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

const longReplyGroupMarker = "long-reply-process-guard"

// The watchdog is the leader of an isolated group. Its control pipe has a
// single writer in the outer test. EOF works even if the test is forcibly
// killed and the Node driver is unresponsive. Group members must not detach.
func TestLongReplyGroupWatchdog(t *testing.T) {
	if args := os.Args; len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != longReplyGroupMarker {
		t.Skip("subprocess helper")
	}
	if syscall.Getpgrp() != os.Getpid() {
		t.Fatal("watchdog must lead its own process group")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	// The group is always our own; its numeric ID cannot have been reused.
	if err := syscall.Kill(0, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
}

func newLongReplyProcessGuard(cmd *exec.Cmd) (func() error, func() error, error) {
	if cmd.SysProcAttr != nil {
		return nil, nil, errors.New("driver already has process attributes")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	watch := exec.Command(executable, "-test.run=^TestLongReplyGroupWatchdog$", "--", longReplyGroupMarker)
	watch.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	control, err := watch.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := watch.Start(); err != nil {
		control.Close()
		return nil, nil, err
	}
	groupID := watch.Process.Pid
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: groupID}
	attach := func() error {
		group, err := syscall.Getpgid(cmd.Process.Pid)
		if err == nil && group != groupID {
			return errors.New("driver did not join the watchdog process group")
		}
		return err
	}
	stop := func() error {
		closeErr := control.Close()
		waitErr := watch.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signal() == syscall.SIGKILL {
				waitErr = nil
			}
		}
		return errors.Join(closeErr, waitErr)
	}
	return attach, stop, nil
}
