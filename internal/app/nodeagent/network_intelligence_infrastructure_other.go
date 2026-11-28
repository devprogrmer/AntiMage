//go:build !linux

package nodeagent

import "errors"

func readNetworkInfrastructure() (map[string]uint64, []networkInterfaceInfrastructure, error) {
	return nil, nil, errors.New("network infrastructure telemetry requires Linux")
}
