package protocols

import "strings"

// Family identifies the runtime family used by a protocol adapter.
type Family string

const (
	FamilyXray   Family = "xray"
	FamilyDaemon Family = "daemon"
	FamilyKernel Family = "kernel"
)

// Definition is the stable capability metadata shared by the control plane,
// dashboard and subscription services.
type Definition struct {
	ID                string
	DisplayName       string
	Family            Family
	Inbound           bool
	Outbound          bool
	Subscription      bool
	TrafficAccounting bool
	Source            string
}

var definitions = []Definition{
	{ID: "vmess", DisplayName: "VMess", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "vless", DisplayName: "VLESS", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "trojan", DisplayName: "Trojan", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "shadowsocks", DisplayName: "Shadowsocks", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "http", DisplayName: "HTTP", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "socks", DisplayName: "SOCKS", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "mixed", DisplayName: "Mixed", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "hysteria", DisplayName: "Hysteria", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "hysteria2", DisplayName: "Hysteria 2", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "tunnel", DisplayName: "Tunnel", Family: FamilyXray, Inbound: true, Outbound: false, Subscription: false, TrafficAccounting: true, Source: "xray"},
	{ID: "openvpn", DisplayName: "OpenVPN", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "wireguard", DisplayName: "WireGuard", Family: FamilyKernel, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "wg-c", DisplayName: "WireGuard C", Family: FamilyKernel, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "amneziawg", DisplayName: "AmneziaWG", Family: FamilyKernel, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "awg", DisplayName: "AmneziaWG", Family: FamilyKernel, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "ikev2", DisplayName: "IKEv2", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "l2tp", DisplayName: "L2TP/IPsec", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "pptp", DisplayName: "PPTP", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "openconnect", DisplayName: "OpenConnect", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "anyconnect", DisplayName: "AnyConnect", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "sstp", DisplayName: "SSTP", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "gre", DisplayName: "GRE", Family: FamilyKernel, Inbound: true, Outbound: true, Subscription: false, TrafficAccounting: true, Source: "system"},
	{ID: "mtproto", DisplayName: "MTProto", Family: FamilyDaemon, Inbound: true, Outbound: false, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "ssh", DisplayName: "SSH", Family: FamilyDaemon, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "system"},
	{ID: "anytls", DisplayName: "AnyTLS", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "tuic", DisplayName: "TUIC", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
	{ID: "naiveproxy", DisplayName: "NaiveProxy", Family: FamilyXray, Inbound: true, Outbound: true, Subscription: true, TrafficAccounting: true, Source: "xray"},
}

// All returns a copy so callers cannot mutate the global capability contract.
func All() []Definition {
	result := make([]Definition, len(definitions))
	copy(result, definitions)
	return result
}

var aliases = map[string]string{
	"naive":     "naiveproxy",
	"awg":       "amneziawg",
	"hysteria2": "hysteria2",
}

// CanonicalID normalizes identifiers used by either upstream panel.
func CanonicalID(id string) string {
	normalized := strings.ToLower(strings.TrimSpace(id))
	if canonical, ok := aliases[normalized]; ok {
		return canonical
	}
	return normalized
}

// Find returns a protocol definition by its stable identifier or upstream alias.
func Find(id string) (Definition, bool) {
	id = CanonicalID(id)
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return Definition{}, false
}
