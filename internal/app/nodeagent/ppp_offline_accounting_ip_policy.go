package nodeagent

import (
	"net/netip"
	"sort"
)

type nativeOfflineIPObservation struct {
	SessionID string
	UserID    int64
	RealIP    string
}

// Session authentication and source IP are observable; physical device identity
// is not. Missing/malformed real addresses disable IP enforcement for that user.
func nativeOfflineIPLimitDenied(observations []nativeOfflineIPObservation, limits map[int64]int64) map[string]bool {
	byUser := map[int64][]nativeOfflineIPObservation{}
	unreliable := map[int64]bool{}
	for _, observation := range observations {
		if limits[observation.UserID] <= 0 {
			continue
		}
		address, err := netip.ParseAddr(observation.RealIP)
		if err != nil || address.IsUnspecified() || observation.SessionID == "" {
			unreliable[observation.UserID] = true
			continue
		}
		observation.RealIP = address.Unmap().String()
		byUser[observation.UserID] = append(byUser[observation.UserID], observation)
	}
	denied := map[string]bool{}
	for uid, sessions := range byUser {
		if unreliable[uid] {
			continue
		}
		sort.Slice(sessions, func(i, j int) bool {
			if sessions[i].RealIP == sessions[j].RealIP {
				return sessions[i].SessionID < sessions[j].SessionID
			}
			return sessions[i].RealIP < sessions[j].RealIP
		})
		allowedIPs := map[string]bool{}
		for _, session := range sessions {
			if !allowedIPs[session.RealIP] {
				if int64(len(allowedIPs)) >= limits[uid] {
					denied[session.SessionID] = true
					continue
				}
				allowedIPs[session.RealIP] = true
			}
		}
	}
	return denied
}

func openVPNOfflineIPDenied(cfg nativeSessionHelperConfig, clients []openVPNStatusClient) map[string]bool {
	limits := map[int64]int64{}
	observations := make([]nativeOfflineIPObservation, 0, len(clients))
	for _, client := range clients {
		username := client.Username
		uid := cfg.Users[username]
		if uid <= 0 {
			username = client.CommonName
			uid = cfg.Users[username]
		}
		if uid <= 0 {
			continue
		}
		policy := cfg.Policies[username]
		limits[uid] = policy.IPLimit
		realIP, _ := openVPNStatusRealAddressParts(client.RealAddress)
		observations = append(observations, nativeOfflineIPObservation{client.ClientID, uid, realIP})
	}
	return nativeOfflineIPLimitDenied(observations, limits)
}
