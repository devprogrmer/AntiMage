package nodeagent

import (
	"net/netip"
	"strings"
	"testing"
)

func TestIKEv2CertificateAuthRequiresClientCertificate(t *testing.T) {
	for _, mode := range []string{"certificate", "password+certificate"} {
		t.Run(mode, func(t *testing.T) {
			raw, err := renderIKEv2IPSecConfig(ikev2RuntimeInbound{Tag: "native", Settings: map[string]any{"auth_mode": mode}}, netip.MustParsePrefix("10.82.0.0/24"), "server", ikev2RuntimeFiles{CertName: "server.pem"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(raw, "rightauth=pubkey\n") {
				t.Fatal("certificate authentication must require a client certificate")
			}
			if strings.Contains(raw, "rightsendcert=never\n") {
				t.Fatal("certificate authentication must allow the client certificate to be requested")
			}
		})
	}
}
