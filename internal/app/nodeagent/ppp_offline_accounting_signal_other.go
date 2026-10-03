//go:build !linux

package nodeagent

import "fmt"

func pppOfflineSignalSession(string) error {
	return fmt.Errorf("PPP offline enforcement requires Linux process handles")
}
