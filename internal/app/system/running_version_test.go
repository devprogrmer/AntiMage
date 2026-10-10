package system

import "testing"

func TestSystemServiceUsesInjectedRunningBuildVersion(t *testing.T) {
	previous := BuildVersion
	BuildVersion = "v2.0.0-runtime-fixture"
	t.Cleanup(func() { BuildVersion = previous })
	if service := NewService(nil, "sqlite", ""); service.version != BuildVersion {
		t.Fatalf("running build hidden by fallback: %s", service.version)
	}
	if service := NewServiceWithProvider(nil, "sqlite", "explicit-test-version", nil); service.version != "explicit-test-version" {
		t.Fatal("explicit provider version changed")
	}
}
