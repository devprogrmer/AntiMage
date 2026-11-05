package nodeagent

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// nodeControlTLS authenticates both directions of the existing install bundle.
// Current panels present the node-specific bundle certificate as their client
// identity. A separate master identity (or overlapping rotation pins) can be
// supplied explicitly; unrecognized clients are never enrolled automatically.
func nodeControlTLS(cfg Config, identity tls.Certificate) (*tls.Config, error) {
	trustFile := strings.TrimSpace(cfg.MasterCertFile)
	if trustFile == "" {
		trustFile = cfg.CertFile
	}
	raw, err := os.ReadFile(trustFile)
	if err != nil {
		return nil, fmt.Errorf("read trusted master certificates: %w", err)
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("trusted master certificate file exceeds 1 MiB")
	}
	roots := x509.NewCertPool()
	pins := make(map[[sha256.Size]byte]struct{})
	for len(bytes.TrimSpace(raw)) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("trusted master file must contain PEM certificates only")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse trusted master certificate: %w", err)
		}
		roots.AddCert(cert)
		pins[sha256.Sum256(cert.Raw)] = struct{}{}
		raw = rest
	}
	if len(pins) == 0 {
		return nil, fmt.Errorf("trusted master certificate file is empty")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{identity},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    roots,
		MinVersion:   tls.VersionTLS12,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("master client certificate is required")
			}
			if _, ok := pins[sha256.Sum256(state.PeerCertificates[0].Raw)]; !ok {
				return fmt.Errorf("master client certificate does not match a configured pin")
			}
			return nil
		},
	}, nil
}
