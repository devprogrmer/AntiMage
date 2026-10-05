//go:build !linux

package nodeagent

import (
	"context"
	"fmt"
)

func pppOfflineSignalSession(string) error {
	return fmt.Errorf("PPP offline enforcement requires Linux process handles")
}

func pppOfflineTerminateSession(context.Context, string) error {
	return fmt.Errorf("PPP offline enforcement requires Linux process handles")
}
