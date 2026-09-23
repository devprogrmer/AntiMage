package nodeagent

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultIKEv2PoolCIDR = "10.70.0.0/16"

type ikev2RuntimeInbound struct {
	Tag        string             `json:"tag"`
	TunnelTag  string             `json:"tunnel_tag"`
	Port       int                `json:"port"`
	TunnelPort int                `json:"tunnel_port"`
	Settings   map[string]any     `json:"settings"`
	Users      []ikev2RuntimeUser `json:"users"`
}

type ikev2RuntimeUser struct {
	UserID             int64  `json:"user_id"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	IPv4Address        string `json:"ipv4_address"`
	Status             string `json:"status"`
	UsedTraffic        int64  `json:"used_traffic"`
	DataLimit          *int64 `json:"data_limit,omitempty"`
	Expire             *int64 `json:"expire,omitempty"`
	DeviceLimit        int64  `json:"device_limit,omitempty"`
	UploadSpeedLimit   int64  `json:"upload_speed_limit"`
	DownloadSpeedLimit int64  `json:"download_speed_limit"`
}

type ikev2RuntimeFiles struct {
	IPSecConfig string
	IPSecSecret string
	CACert      string
	ServerCert  string
	ServerKey   string
	CertName    string
	KeyName     string
	CAName      string
}

type preparedIKEv2Runtime struct {
	Tag     string
	Inbound ikev2RuntimeInbound
	Files   ikev2RuntimeFiles
	TProxy  ikev2TProxySpec
	NAT     openVPNNATSpec
}

func (s *Server) prepareIKEv2Inbound(
	inbound ikev2RuntimeInbound,
) (ikev2RuntimeFiles, error) {
	tag := strings.TrimSpace(inbound.Tag)
	if tag == "" {
		return ikev2RuntimeFiles{}, fmt.Errorf("ikev2 inbound tag is required")
	}
	if inbound.Port != 500 {
		return ikev2RuntimeFiles{}, fmt.Errorf(
			"ikev2 %q: public port must be 500",
			tag,
		)
	}

	prefix, err := ikev2PoolPrefix(inbound)
	if err != nil {
		return ikev2RuntimeFiles{}, err
	}

	authMode := strings.ToLower(strings.TrimSpace(
		openVPNStringSetting(inbound.Settings, "auth_mode", "password"),
	))
	switch authMode {
	case "password", "certificate", "password+certificate":
	default:
		return ikev2RuntimeFiles{}, fmt.Errorf(
			"ikev2 %q: unsupported auth mode %q",
			tag,
			authMode,
		)
	}

	root := filepath.Join(
		s.cfg.DataDir,
		"ikev2",
		ikev2RuntimeDirName(tag),
	)
	if err := os.MkdirAll(root, 0700); err != nil {
		return ikev2RuntimeFiles{}, fmt.Errorf(
			"ikev2 %q: create runtime dir: %w",
			tag,
			err,
		)
	}

	files := ikev2RuntimeFiles{
		IPSecConfig: filepath.Join(root, "ipsec.conf"),
		IPSecSecret: filepath.Join(root, "ipsec.secrets"),
		CACert:      filepath.Join(root, "ca.pem"),
		ServerCert:  filepath.Join(root, "server.pem"),
		ServerKey:   filepath.Join(root, "server.key"),
	}

	stem := "antimage-ikev2-" + ikev2RuntimeDirName(tag)
	files.CAName = stem + "-ca.pem"
	files.CertName = stem + "-server.pem"
	files.KeyName = stem + "-server.key"

	identity := strings.TrimSpace(
		openVPNStringSetting(inbound.Settings, "server_identity", "auto"),
	)
	if identity == "" || strings.EqualFold(identity, "auto") {
		identity = ikev2NormalizeServerIdentity(
			openVPNStringSetting(
				inbound.Settings,
				"runtime_server_identity",
				"",
			),
		)
		if identity == "" {
			host, _ := os.Hostname()
			identity = strings.TrimSpace(host)
		}
	}
	if identity == "" {
		return ikev2RuntimeFiles{}, fmt.Errorf(
			"ikev2 %q: server identity could not be resolved",
			tag,
		)
	}

	certMode := strings.ToLower(strings.TrimSpace(
		openVPNStringSetting(inbound.Settings, "certificate_mode", "auto"),
	))
	if certMode != "manual" && certMode != "custom" {
		certMode = "auto"
	}

	if certMode == "auto" {
		if err := ensureIKEv2AutoCertificate(
			root,
			identity,
			files.CACert,
			files.ServerCert,
			files.ServerKey,
		); err != nil {
			return ikev2RuntimeFiles{}, fmt.Errorf(
				"ikev2 %q: automatic certificate: %w",
				tag,
				err,
			)
		}
	} else {
		ca := strings.TrimSpace(
			openVPNStringSetting(inbound.Settings, "ca_certificate", ""),
		)
		cert := strings.TrimSpace(
			openVPNStringSetting(inbound.Settings, "server_certificate", ""),
		)
		key := strings.TrimSpace(
			openVPNStringSetting(inbound.Settings, "server_key", ""),
		)
		if ca == "" || cert == "" || key == "" {
			return ikev2RuntimeFiles{}, fmt.Errorf(
				"ikev2 %q: manual certificate mode requires CA, server certificate and server key",
				tag,
			)
		}

		if err := os.WriteFile(files.CACert, []byte(ca+"\n"), 0644); err != nil {
			return ikev2RuntimeFiles{}, err
		}
		if err := os.WriteFile(files.ServerCert, []byte(cert+"\n"), 0644); err != nil {
			return ikev2RuntimeFiles{}, err
		}
		if err := os.WriteFile(files.ServerKey, []byte(key+"\n"), 0600); err != nil {
			return ikev2RuntimeFiles{}, err
		}
	}

	config, err := renderIKEv2IPSecConfig(
		inbound,
		prefix,
		identity,
		files,
	)
	if err != nil {
		return ikev2RuntimeFiles{}, err
	}
	secrets, err := renderIKEv2IPSecSecrets(
		inbound,
		files,
	)
	if err != nil {
		return ikev2RuntimeFiles{}, err
	}

	if err := os.WriteFile(files.IPSecConfig, []byte(config), 0600); err != nil {
		return ikev2RuntimeFiles{}, err
	}
	if err := os.WriteFile(files.IPSecSecret, []byte(secrets), 0600); err != nil {
		return ikev2RuntimeFiles{}, err
	}

	return files, nil
}

func ikev2PoolPrefix(
	inbound ikev2RuntimeInbound,
) (netip.Prefix, error) {
	raw := strings.TrimSpace(openVPNStringSetting(
		inbound.Settings,
		"ipv4_pool_cidr",
		defaultIKEv2PoolCIDR,
	))

	prefix, err := netip.ParsePrefix(raw)
	if err != nil || !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf(
			"ikev2 %q: invalid IPv4 pool %q",
			inbound.Tag,
			raw,
		)
	}

	prefix = prefix.Masked()
	if prefix.Bits() < 16 || prefix.Bits() > 30 {
		return netip.Prefix{}, fmt.Errorf(
			"ikev2 %q: IPv4 pool prefix must be between /16 and /30",
			inbound.Tag,
		)
	}

	return prefix, nil
}

func renderIKEv2IPSecConfig(
	inbound ikev2RuntimeInbound,
	prefix netip.Prefix,
	identity string,
	files ikev2RuntimeFiles,
) (string, error) {
	authMode := strings.ToLower(strings.TrimSpace(
		openVPNStringSetting(inbound.Settings, "auth_mode", "password"),
	))

	ike := openVPNStringSetting(
		inbound.Settings,
		"ike_proposals",
		"aes256-sha256-modp2048,aes256-sha384-modp3072,aes256gcm16-prfsha384-ecp384",
	)
	esp := openVPNStringSetting(
		inbound.Settings,
		"esp_proposals",
		"aes256-sha256,aes256gcm16-ecp384",
	)

	fragmentation := openVPNStringSetting(
		inbound.Settings,
		"fragmentation",
		"yes",
	)

	ikeLifetime := openVPNIntSetting(inbound.Settings, "ike_lifetime")
	if ikeLifetime <= 0 {
		ikeLifetime = 10800
	}
	childLifetime := openVPNIntSetting(inbound.Settings, "child_lifetime")
	if childLifetime <= 0 {
		childLifetime = 3600
	}
	rekeyTime := openVPNIntSetting(inbound.Settings, "rekey_time")
	if rekeyTime < 0 {
		rekeyTime = 3000
	}
	dpdDelay := openVPNIntSetting(inbound.Settings, "dpd_delay")
	if dpdDelay <= 0 {
		dpdDelay = 30
	}

	dns := openVPNStringListSetting(inbound.Settings, "dns_servers")
	if len(dns) == 0 {
		dns = []string{"1.1.1.1", "8.8.8.8"}
	}

	var b strings.Builder

	fmt.Fprintf(&b, "conn %s\n", ikev2ConnectionName(inbound.Tag))
	b.WriteString("    auto=add\n")
	b.WriteString("    keyexchange=ikev2\n")
	b.WriteString("    type=tunnel\n")
	b.WriteString("    left=%any\n")
	b.WriteString("    leftauth=pubkey\n")
	fmt.Fprintf(&b, "    leftid=%s\n", ikev2StrongSwanIdentity(identity))
	fmt.Fprintf(&b, "    leftcert=%s\n", files.CertName)
	b.WriteString("    leftsubnet=0.0.0.0/0\n")

	if openVPNBoolSetting(inbound.Settings, "send_cert", true) {
		b.WriteString("    leftsendcert=always\n")
	} else {
		b.WriteString("    leftsendcert=never\n")
	}

	b.WriteString("    right=%any\n")
	b.WriteString("    rightsendcert=never\n")
	fmt.Fprintf(&b, "    rightsourceip=%s\n", prefix.String())
	fmt.Fprintf(&b, "    rightdns=%s\n", strings.Join(dns, ","))

	switch authMode {
	case "password":
		b.WriteString("    rightauth=eap-mschapv2\n")
		b.WriteString("    eap_identity=%any\n")

	case "certificate":
		b.WriteString("    rightauth=pubkey\n")

	case "password+certificate":
		b.WriteString("    rightauth=pubkey\n")
		b.WriteString("    rightauth2=eap-mschapv2\n")
		b.WriteString("    eap_identity=%any\n")

	default:
		return "", fmt.Errorf(
			"ikev2 %q: unsupported auth mode %q",
			inbound.Tag,
			authMode,
		)
	}

	fmt.Fprintf(&b, "    ike=%s\n", ike)
	fmt.Fprintf(&b, "    esp=%s\n", esp)
	fmt.Fprintf(&b, "    fragmentation=%s\n", fragmentation)

	if openVPNBoolSetting(inbound.Settings, "mobike", true) {
		b.WriteString("    mobike=yes\n")
	} else {
		b.WriteString("    mobike=no\n")
	}

	if openVPNBoolSetting(inbound.Settings, "reauth", false) {
		b.WriteString("    reauth=yes\n")
	} else {
		b.WriteString("    reauth=no\n")
	}

	fmt.Fprintf(&b, "    ikelifetime=%ds\n", ikeLifetime)
	fmt.Fprintf(&b, "    lifetime=%ds\n", childLifetime)
	fmt.Fprintf(&b, "    rekeytime=%ds\n", rekeyTime)
	fmt.Fprintf(&b, "    dpddelay=%ds\n", dpdDelay)
	b.WriteString("    dpdaction=clear\n")
	b.WriteString("    closeaction=clear\n")

	return b.String(), nil
}

func renderIKEv2IPSecSecrets(
	inbound ikev2RuntimeInbound,
	files ikev2RuntimeFiles,
) (string, error) {
	authMode := strings.ToLower(strings.TrimSpace(
		openVPNStringSetting(inbound.Settings, "auth_mode", "password"),
	))

	var b strings.Builder

	fmt.Fprintf(&b, ": RSA %s\n", files.KeyName)

	if authMode == "certificate" {
		return b.String(), nil
	}

	seen := make(map[string]struct{})

	for _, user := range inbound.Users {
		if !ikev2UserAllowed(user) {
			continue
		}

		username := strings.TrimSpace(user.Username)
		password := strings.TrimSpace(user.Password)

		if username == "" || password == "" {
			continue
		}
		if _, exists := seen[username]; exists {
			return "", fmt.Errorf(
				"ikev2 %q: duplicate username %q",
				inbound.Tag,
				username,
			)
		}
		seen[username] = struct{}{}

		fmt.Fprintf(
			&b,
			"%s : EAP %s\n",
			ikev2SecretQuote(username),
			ikev2SecretQuote(password),
		)
	}

	return b.String(), nil
}

func ikev2UserAllowed(user ikev2RuntimeUser) bool {
	status := strings.ToLower(strings.TrimSpace(user.Status))
	return status == "" || status == "active" || status == "on_hold"
}

func ikev2SecretQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func ikev2NormalizeServerIdentity(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimRight(value, "/")

	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}

	return strings.Trim(strings.TrimSpace(value), "[]")
}

func ikev2StrongSwanIdentity(identity string) string {
	identity = strings.TrimSpace(identity)

	if net.ParseIP(identity) != nil {
		return identity
	}
	if strings.HasPrefix(identity, "@") ||
		strings.Contains(identity, "=") {
		return identity
	}

	return "@" + identity
}

func ikev2ConnectionName(tag string) string {
	return "antimage-ikev2-" + ikev2RuntimeDirName(tag)
}

func ikev2RuntimeDirName(tag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tag)))
	return fmt.Sprintf("%x", sum[:8])
}

func ensureIKEv2AutoCertificate(
	root string,
	identity string,
	caPath string,
	certPath string,
	keyPath string,
) error {
	identityFile := filepath.Join(root, "server-identity")

	oldIdentityRaw, _ := os.ReadFile(identityFile)
	oldIdentity := strings.TrimSpace(string(oldIdentityRaw))

	if oldIdentity == identity &&
		fileExistsIKEv2(caPath) &&
		fileExistsIKEv2(certPath) &&
		fileExistsIKEv2(keyPath) {
		return nil
	}

	caKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}

	now := time.Now().UTC()

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"AntiMage"},
			CommonName:   "AntiMage IKEv2 CA",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign |
			x509.KeyUsageCRLSign,
	}

	caDER, err := x509.CreateCertificate(
		rand.Reader,
		caTemplate,
		caTemplate,
		&caKey.PublicKey,
		caKey,
	)
	if err != nil {
		return err
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}

	serverKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano() + 1),
		Subject: pkix.Name{
			Organization: []string{"AntiMage"},
			CommonName:   identity,
		},
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.AddDate(3, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature |
			x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
	}

	if ip := net.ParseIP(identity); ip != nil {
		serverTemplate.IPAddresses = []net.IP{ip}
		// Windows compatibility when connecting by IP.
		serverTemplate.DNSNames = []string{identity}
	} else {
		serverTemplate.DNSNames = []string{identity}
	}

	serverDER, err := x509.CreateCertificate(
		rand.Reader,
		serverTemplate,
		caCert,
		&serverKey.PublicKey,
		caKey,
	)
	if err != nil {
		return err
	}

	caPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caDER,
	})
	serverPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: serverDER,
	})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(serverKey),
	})

	if err := os.WriteFile(caPath, caPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, serverPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(
		identityFile,
		[]byte(identity+"\n"),
		0600,
	); err != nil {
		return err
	}

	return nil
}

func fileExistsIKEv2(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func ikev2IntString(value int) string {
	return strconv.Itoa(value)
}
