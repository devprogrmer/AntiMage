package nodecontroller

import (
	"bufio"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var runtimeSessionCallbackEnvKeys = []string{
	"ANTIMAGE_NODE_SESSION_CALLBACK_URL",
	"ANTIMAGE_PUBLIC_URL",
	"PUBLIC_URL",
	"UVICORN_SSL_CERTFILE",
	"UVICORN_PORT",
}

func runtimeSessionCallbackEnvironment() map[string]string {
	values := map[string]string{}
	paths := []string{}
	if explicit := strings.TrimSpace(os.Getenv("ANTIMAGE_ENV_FILE")); explicit != "" {
		paths = append(paths, explicit)
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".env"))
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		paths = append(paths, filepath.Join(dir, ".env"), filepath.Join(filepath.Dir(dir), ".env"))
	}
	paths = append(paths, "/opt/antimage/.env")
	for _, path := range paths {
		mergeRuntimeSessionEnvFile(values, path)
	}
	for _, key := range runtimeSessionCallbackEnvKeys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values[key] = value
		}
	}
	return values
}

func mergeRuntimeSessionEnvFile(values map[string]string, path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	allowed := map[string]bool{}
	for _, key := range runtimeSessionCallbackEnvKeys {
		allowed[key] = true
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !allowed[key] || values[key] != "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
}

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

func runtimeSessionCallbackFallbackBase(env map[string]string) (string, error) {
	certPath := strings.TrimSpace(env["UVICORN_SSL_CERTFILE"])
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

	portText := strings.TrimSpace(env["UVICORN_PORT"])
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
