package studio

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

// The driver must wait for an explicit stdin start signal before launching
// descendants. Call afterStart before sending that signal or waiting on cmd.
func longReplyProcessGuard(cmd *exec.Cmd) (afterStart func() error, closeGuard func() error, err error) {
	attach, stop, err := newLongReplyProcessGuard(cmd)
	if err != nil {
		return nil, nil, err
	}
	var mu sync.Mutex
	var closed bool
	var stopErr error
	afterStart = func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return os.ErrClosed
		}
		if cmd.Process == nil {
			return errors.New("long reply driver has not started")
		}
		return attach()
	}
	closeGuard = func() error {
		mu.Lock()
		defer mu.Unlock()
		if !closed {
			closed = true
			stopErr = stop()
		}
		return stopErr
	}
	if cancelDriver := cmd.Cancel; cancelDriver != nil {
		cmd.Cancel = func() error {
			// Cancellation can race attachment, while the driver is still
			// waiting for start. In that case also stop the driver itself.
			err := closeGuard()
			driverErr := cancelDriver()
			if errors.Is(driverErr, os.ErrProcessDone) {
				driverErr = nil
			}
			return errors.Join(err, driverErr)
		}
	}
	return afterStart, closeGuard, nil
}
