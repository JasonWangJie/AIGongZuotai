//go:build !windows && !linux && !darwin

package studio

import (
	"errors"
	"os/exec"
)

func newLongReplyProcessGuard(*exec.Cmd) (func() error, func() error, error) {
	return nil, nil, errors.New("long reply process tree containment is unsupported on this platform")
}
