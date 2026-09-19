package user

import (
	"fmt"
	"strings"
)

type AWGProfileRequest struct {
	Username, Endpoint, ServerPublicKey string
	DNS                                 []string
	MTU, PersistentKeepalive            int
	Jc, Jmin, Jmax, S1, S2              int
	H1, H2, H3, H4                      string
	Devices                             []AWGDevice
}

type AWGProfile struct {
	DeviceIndex int    `json:"device_index"`
	Filename    string `json:"filename"`
	Body        string `json:"body"`
}

func RenderAmneziaWGProfiles(req AWGProfileRequest) ([]AWGProfile, error) {
	if strings.TrimSpace(req.Endpoint) == "" || strings.TrimSpace(req.ServerPublicKey) == "" {
		return nil, fmt.Errorf("AmneziaWG endpoint and server public key are required")
	}
	profiles := make([]AWGProfile, 0, len(req.Devices))
	for _, device := range req.Devices {
		if device.PrivateKey == "" || device.Address == "" {
			return nil, fmt.Errorf("AmneziaWG device %d is incomplete", device.DeviceIndex)
		}
		var body strings.Builder
		fmt.Fprintf(&body, "[Interface]\nPrivateKey = %s\nAddress = %s/32\n", device.PrivateKey, device.Address)
		if len(req.DNS) > 0 {
			fmt.Fprintf(&body, "DNS = %s\n", strings.Join(req.DNS, ", "))
		}
		if req.MTU > 0 {
			fmt.Fprintf(&body, "MTU = %d\n", req.MTU)
		}
		fmt.Fprintf(&body, "Jc = %d\nJmin = %d\nJmax = %d\nS1 = %d\nS2 = %d\nH1 = %s\nH2 = %s\nH3 = %s\nH4 = %s\n", req.Jc, req.Jmin, req.Jmax, req.S1, req.S2, req.H1, req.H2, req.H3, req.H4)
		fmt.Fprintf(&body, "\n[Peer]\nPublicKey = %s\n", req.ServerPublicKey)
		if device.PresharedKey != "" {
			fmt.Fprintf(&body, "PresharedKey = %s\n", device.PresharedKey)
		}
		fmt.Fprintf(&body, "Endpoint = %s\nAllowedIPs = 0.0.0.0/0\n", req.Endpoint)
		if req.PersistentKeepalive > 0 {
			fmt.Fprintf(&body, "PersistentKeepalive = %d\n", req.PersistentKeepalive)
		}
		profiles = append(profiles, AWGProfile{DeviceIndex: device.DeviceIndex, Filename: fmt.Sprintf("%s-amneziawg-device-%d.conf", WGSafePathComponent(req.Username), device.DeviceIndex+1), Body: body.String()})
	}
	return profiles, nil
}
