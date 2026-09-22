package nodecontroller

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func normalizeRuntimeSessionCallbackBase(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", nil
	}

	base = strings.TrimRight(base, "/")
	const suffix = "/internal/node/session-event"
	if strings.HasSuffix(base, suffix) {
		base = strings.TrimRight(strings.TrimSuffix(base, suffix), "/")
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid node session callback URL %q: %w", base, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf(
			"invalid node session callback URL %q: http or https scheme is required",
			base,
		)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf(
			"invalid node session callback URL %q: host is required",
			base,
		)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf(
			"invalid node session callback URL %q: query or fragment is not allowed",
			base,
		)
	}

	return base, nil
}

func runtimeSessionCallbackFallbackBase() (string, error) {
	certPath := strings.TrimSpace(os.Getenv("UVICORN_SSL_CERTFILE"))
	if certPath == "" {
		return "", nil
	}

	raw, err := os.ReadFile(certPath)
	if err != nil {
		return "", fmt.Errorf(
			"read panel TLS certificate for node session callback: %w",
			err,
		)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf(
			"panel TLS certificate %q is not valid PEM",
			certPath,
		)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf(
			"parse panel TLS certificate for node session callback: %w",
			err,
		)
	}

	host := ""
	for _, candidate := range cert.DNSNames {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && !strings.Contains(candidate, "*") {
			host = candidate
			break
		}
	}

	if host == "" {
		candidate := strings.TrimSpace(cert.Subject.CommonName)
		if candidate != "" && !strings.Contains(candidate, "*") {
			host = candidate
		}
	}

	if host == "" {
		return "", fmt.Errorf(
			"panel TLS certificate has no usable DNS name for node session callback",
		)
	}

	portText := strings.TrimSpace(os.Getenv("UVICORN_PORT"))
	if portText == "" {
		portText = "8000"
	}

	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf(
			"invalid UVICORN_PORT %q for node session callback",
			portText,
		)
	}

	if port == 443 {
		return "https://" + host, nil
	}

	return fmt.Sprintf("https://%s:%d", host, port), nil
}
