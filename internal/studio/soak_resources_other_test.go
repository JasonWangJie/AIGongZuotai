//go:build !windows

package studio

import "errors"

func soakResourceSample256(*childAcceptanceServer) (soakResourcePair256, error) {
	return soakResourcePair256{}, errors.New("V17 process resource sampling requires Windows")
}
