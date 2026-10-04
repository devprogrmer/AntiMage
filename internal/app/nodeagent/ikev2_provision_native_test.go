package nodeagent

import (
	"os"
	"path/filepath"
	"testing"
)

// Run only inside the harness's private mount/network/PID namespaces.
func TestIKEv2NativeProvision(t *testing.T) {
	root := os.Getenv("ANTIMAGE_IKEV2_PROVISION_ROOT")
	if root == "" {
		t.Skip("requires isolated native provisioning worker")
	}
	marker, err := os.ReadFile("/run/antimage-ikev2-isolated")
	if err != nil || string(marker) != root {
		t.Fatal("refusing provisioning outside isolated mount namespace")
	}
	s := New(Config{DataDir: filepath.Join(root, "provision-state")})
	settings := map[string]any{"auth_mode": "certificate", "certificate_mode": "manual", "server_identity": "server", "ipv4_pool_cidr": "10.82.0.0/24", "ike_proposals": "aes256-sha256-modp2048!", "esp_proposals": "aes256-sha256!"}
	for key, file := range map[string]string{"ca_certificate": "ca.pem", "server_certificate": "server.pem", "server_key": "server.key"} {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		settings[key] = string(raw)
	}
	inbound := ikev2RuntimeInbound{Tag: "native", Port: 500, Settings: settings, Users: []ikev2RuntimeUser{{UserID: 7, Username: "client", Status: "active", UsageCoefficient: 1.5, InboundCoefficient: 2}}}
	files, err := s.prepareIKEv2Inbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	prepared := []preparedIKEv2Runtime{{Tag: inbound.Tag, Inbound: inbound, Files: files}}
	if err := preflightIKEv2Runtimes(prepared); err != nil {
		t.Fatal(err)
	}
	if err := s.applyIKEv2Runtimes(prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := s.offlineIKEv2RuntimesForCheckpoint(true); err != nil {
		t.Fatal(err)
	}
	if s.ikev2Runtimes["native"] == nil {
		t.Fatal("runtime not installed")
	}

	t.Log("production prepare/preflight/applyIKEv2Runtimes completed")
}
