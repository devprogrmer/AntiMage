package nodeagent

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"
)

const openVPNNATChain = "ANTIMAGE_OV_NAT"

var (
	openVPNNATGOOS = runtime.GOOS
)

type openVPNNATSpec struct {
	Enabled    bool
	SourceCIDR string
}

func buildOpenVPNNATSpec(inbound openVPNRuntimeInbound) (openVPNNATSpec, error) {
	source := strings.TrimSpace(openVPNStringSetting(inbound.Settings, "ipv4_pool_cidr", "10.66.0.0/16"))
	if source == "" {
		source = "10.66.0.0/16"
	}
	if _, _, err := openVPNPoolNetwork(source); err != nil {
		return openVPNNATSpec{}, fmt.Errorf("openvpn %q NAT: %w", inbound.Tag, err)
	}
	return openVPNNATSpec{Enabled: true, SourceCIDR: source}, nil
}

func (s *Server) applyOpenVPNNAT(tag string, spec openVPNNATSpec) error {
	if !spec.Enabled || openVPNNATGOOS != "linux" {
		return nil
	}
	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return fmt.Errorf("openvpn NAT: iptables command not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := ensureOpenVPNNATChain(ctx, iptablesPath); err != nil {
		return err
	}
	check := []string{"-w", "5", "-t", "nat", "-C", openVPNNATChain, "-s", spec.SourceCIDR, "-j", "MASQUERADE"}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, check...); err != nil {
		if err := runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "nat", "-A", openVPNNATChain, "-s", spec.SourceCIDR, "-j", "MASQUERADE"); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.openVPNNATSpecs[tag] = spec
	s.mu.Unlock()
	return nil
}

func ensureOpenVPNNATChain(ctx context.Context, iptablesPath string) error {
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-S", openVPNNATChain); err != nil {
		if err := runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "nat", "-N", openVPNNATChain); err != nil {
			return err
		}
	}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-C", "POSTROUTING", "-j", openVPNNATChain); err == nil {
		return nil
	}
	return runOpenVPNNetworkRequired(ctx, iptablesPath, "-w", "5", "-t", "nat", "-A", "POSTROUTING", "-j", openVPNNATChain)
}

func (s *Server) removeOpenVPNNATForTag(tag string) error {
	s.mu.Lock()
	delete(s.openVPNNATSpecs, tag)
	unused := len(s.openVPNNATSpecs) == 0
	s.mu.Unlock()
	if unused {
		return s.cleanupOpenVPNNAT()
	}
	return nil
}

func (s *Server) stopRemovedOpenVPNNATSpecs(desired map[string]struct{}) {
	s.mu.Lock()
	for tag := range s.openVPNNATSpecs {
		if _, ok := desired[tag]; !ok {
			delete(s.openVPNNATSpecs, tag)
		}
	}
	unused := len(s.openVPNNATSpecs) == 0
	s.mu.Unlock()
	if unused {
		if err := s.cleanupOpenVPNNAT(); err != nil {
			s.appendLog("cleanup openvpn NAT failed: " + err.Error())
		}
	}
}

func (s *Server) stopAllOpenVPNNATSpecs() {
	s.mu.Lock()
	s.openVPNNATSpecs = make(map[string]openVPNNATSpec)
	s.mu.Unlock()
	if err := s.cleanupOpenVPNNAT(); err != nil {
		s.appendLog("cleanup openvpn NAT failed: " + err.Error())
	}
}

func (s *Server) cleanupOpenVPNNAT() error {
	if openVPNNATGOOS != "linux" {
		return nil
	}
	iptablesPath, err := openVPNNetworkLookPath("iptables")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-C", "POSTROUTING", "-j", openVPNNATChain); err == nil {
		if err := runOpenVPNNetworkAlreadyCleanOK(ctx, iptablesPath, "-w", "5", "-t", "nat", "-D", "POSTROUTING", "-j", openVPNNATChain); err != nil {
			return err
		}
	}
	if _, err := openVPNNetworkRun(ctx, iptablesPath, "-w", "5", "-t", "nat", "-S", openVPNNATChain); err != nil {
		return nil
	}
	if err := runOpenVPNNetworkAlreadyCleanOK(ctx, iptablesPath, "-w", "5", "-t", "nat", "-F", openVPNNATChain); err != nil {
		return err
	}
	return runOpenVPNNetworkAlreadyCleanOK(ctx, iptablesPath, "-w", "5", "-t", "nat", "-X", openVPNNATChain)
}
