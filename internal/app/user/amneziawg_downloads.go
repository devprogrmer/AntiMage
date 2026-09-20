package user

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
)

func (s Service) AWGDownloadProfiles(ctx context.Context, user UserDetail, subscriptionURL string) ([]AWGProfile, error) {
	if user.ServiceID == nil || *user.ServiceID <= 0 {
		return []AWGProfile{}, nil
	}
	inbounds, inboundOrder, err := s.repo.ResolvedInboundsByTag(ctx)
	if err != nil {
		return nil, err
	}
	hosts, err := s.repo.hosts(ctx)
	if err != nil {
		return nil, err
	}
	orders, err := s.repo.serviceHostOrders(ctx, []int64{*user.ServiceID})
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	for position, tag := range inboundOrder {
		index[tag] = position
	}
	selected := selectConfigHosts(hosts, user.ServiceID)
	orderMap := map[int64]int64{}
	for key, value := range orders[*user.ServiceID] {
		orderMap[key] = value
	}
	sortConfigHosts(selected, orderMap, index)
	serverIP := s.repo.configServerIP(ctx)
	limit := int(user.DeviceLimit)
	if limit < 1 {
		limit = 1
	}
	profiles := []AWGProfile{}
	for _, selectedHost := range selected {
		host := selectedHost.host
		inbound, ok := inbounds[host.InboundTag]
		if !ok || normalizeProxyProtocol(stringValue(inbound["protocol"])) != "amneziawg" {
			continue
		}
		settings := mapValue(inbound["settings"])
		pool := firstNonEmptyString(settings["address_pool"], settings["ipv4_pool_cidr"], "10.72.0.0/16")
		serverAddress := firstNonEmptyString(settings["server_address"], "10.72.0.1/16")
		devices, err := s.repo.ReconcileAmneziaWGDevices(ctx, stringValue(inbound["tag"]), user.ID, limit, pool, serverAddress, boolValue(settings["psk_enabled"]))
		if err != nil {
			return nil, err
		}
		serverPublicKey, err := wgServerPublicKey(settings)
		if err != nil {
			return nil, fmt.Errorf("%s", strings.Replace(err.Error(), "WireGuard", "AmneziaWG", 1))
		}
		address := firstNonEmptyString(host.Address, serverIP)
		port := intValue(inbound["port"])
		endpoint := net.JoinHostPort(strings.Trim(address, "[]"), fmt.Sprint(port))
		hostTag := WGSafePathComponent(firstNonEmptyString(host.Remark, host.InboundTag, "amneziawg"))
		rendered, err := RenderAmneziaWGProfiles(AWGProfileRequest{
			Username: user.Username, Endpoint: endpoint, ServerPublicKey: serverPublicKey,
			DNS: stringList(firstNonEmptyAny(settings["dns_servers"], settings["dnsServers"])),
			MTU: intValue(settings["mtu"]), PersistentKeepalive: intValue(firstNonEmptyAny(settings["persistent_keepalive"], settings["persistentKeepalive"])),
			Jc: intValue(settings["jc"]), Jmin: intValue(settings["jmin"]), Jmax: intValue(settings["jmax"]), S1: intValue(settings["s1"]), S2: intValue(settings["s2"]),
			H1: stringValue(settings["h1"]), H2: stringValue(settings["h2"]), H3: stringValue(settings["h3"]), H4: stringValue(settings["h4"]), Devices: devices,
		})
		if err != nil {
			return nil, err
		}
		for i := range rendered {
			rendered[i].HostTag = hostTag
			rendered[i].HostName = firstNonEmptyString(host.Remark, address)
			rendered[i].InboundTag = host.InboundTag
			rendered[i].Filename = fmt.Sprintf("%s-device-%d.conf", hostTag, rendered[i].DeviceIndex+1)
		}
		profiles = append(profiles, rendered...)
	}
	baseURL, parseErr := url.Parse(subscriptionURL)
	if parseErr == nil {
		basePath := strings.TrimRight(baseURL.Path, "/")
		if strings.HasSuffix(basePath, "/usage") || strings.HasSuffix(basePath, "/info") {
			basePath = path.Dir(basePath)
		}
		for i := range profiles {
			next := *baseURL
			next.RawQuery = ""
			next.Fragment = ""
			next.Path = fmt.Sprintf("%s/awg/%s-device-%d.conf", basePath, url.PathEscape(profiles[i].HostTag), profiles[i].DeviceIndex+1)
			profiles[i].DownloadURL = next.String()
		}
	}
	return profiles, nil
}

func (s Service) generateAWGProfile(ctx context.Context, user UserDetail, req SubscriptionRenderRequest) (SubscriptionHTTPResponse, error) {
	profiles, err := s.AWGDownloadProfiles(ctx, user, req.URL)
	if err != nil {
		return SubscriptionHTTPResponse{}, err
	}
	requested := strings.TrimSuffix(firstNonEmptyString(req.HostTag, req.InboundTag), ".conf")
	for _, profile := range profiles {
		candidate := fmt.Sprintf("%s-device-%d", profile.HostTag, profile.DeviceIndex+1)
		if requested == "" || WGSafePathComponent(requested) == WGSafePathComponent(candidate) {
			return SubscriptionHTTPResponse{Status: 200, MediaType: "application/x-amneziawg-profile", Headers: map[string]string{"content-disposition": `attachment; filename="` + profile.Filename + `"`}, Body: []byte(profile.Body)}, nil
		}
	}
	return SubscriptionHTTPResponse{}, clientError(404, "AmneziaWG profile not found")
}
