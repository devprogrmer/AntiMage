package nodeagent

import "testing"

func mockWireGuardGenerationIdentity(t *testing.T) {
	t.Helper()
	original := wireGuardInterfaceIdentity
	wireGuardInterfaceIdentity = func(name string) (string, error) { return "fixture-boot:" + name, nil }
	t.Cleanup(func() { wireGuardInterfaceIdentity = original })
}
