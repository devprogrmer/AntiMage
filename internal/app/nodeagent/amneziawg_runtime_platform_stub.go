//go:build !linux

package nodeagent

import (
	"context"
	"fmt"
)

func amneziaWGPlatformInterfaceIdentity(string) (string, error) { return "", nil }

func amneziaWGDurableWrite(path string, raw []byte) error {
	return atomicWriteFile(path, raw, 0600)
}

func amneziaWGPlatformSnapshotAll(context.Context, []string) (map[string][]wireGuardPeerCounters, error) {
	return nil, fmt.Errorf("amneziawg native runtime is supported only on linux")
}

func amneziaWGPlatformPreflight() error {
	return fmt.Errorf("amneziawg native runtime is supported only on linux")
}

func amneziaWGPlatformApply(prepared preparedAmneziaWGRuntime) error {
	return fmt.Errorf("amneziawg native runtime is supported only on linux")
}

func amneziaWGPlatformRemove(interfaceName string) error { return nil }
func amneziaWGPlatformSnapshot(interfaceName string) ([]wireGuardPeerCounters, error) {
	return nil, fmt.Errorf("amneziawg native runtime is supported only on linux")
}
func amneziaWGPlatformRemovePeer(interfaceName, publicKey string) error {
	return fmt.Errorf("amneziawg native runtime is supported only on linux")
}
