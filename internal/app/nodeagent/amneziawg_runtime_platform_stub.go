//go:build !linux

package nodeagent

import "fmt"

func amneziaWGPlatformPreflight() error {
	return fmt.Errorf("amneziawg native runtime is supported only on linux")
}

func amneziaWGPlatformApply(prepared preparedAmneziaWGRuntime) error {
	return fmt.Errorf("amneziawg native runtime is supported only on linux")
}

func amneziaWGPlatformRemove(interfaceName string) error { return nil }
