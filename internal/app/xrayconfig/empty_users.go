package xrayconfig

import "strings"

// EmptyUsersAllowServerAuth identifies protocols whose empty user list can
// enable a shared server credential instead of denying all connections.
// Panel-managed listeners must be absent when no users are authorized.
func EmptyUsersAllowServerAuth(protocol, method string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "hysteria":
		return true
	case "shadowsocks":
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "2022-")
	default:
		return false
	}
}
