package nodeagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkNativeHelperQuota1000Users(b *testing.B) {
	for _, protocol := range []string{"openvpn", "ppp"} {
		b.Run(protocol, func(b *testing.B) {
			s := New(Config{DataDir: b.TempDir()})
			protocols := []string{protocol}
			if protocol == "ppp" {
				protocols = []string{"l2tp", "pptp"}
				previous := pppOfflineQuery
				pppOfflineQuery = func(context.Context) ([]byte, error) { return nil, nil }
				b.Cleanup(func() { pppOfflineQuery = previous })
			}
			for _, kind := range protocols {
				root := filepath.Join(s.cfg.DataDir, kind, "benchmark")
				users, usage := map[string]int64{}, map[string]int64{}
				policies := map[string]nativeSessionUserPolicy{}
				for i := 0; i < 1000; i++ {
					name := fmt.Sprintf("user-%04d", i)
					users[name] = int64(i + 1)
					key := name
					if kind != "openvpn" {
						key = fmt.Sprintf("10.66.%d.%d", i/250, i%250+1)
					}
					usage[key] = int64(i + 1)
					policies[name] = nativeSessionUserPolicy{Status: "active", DataLimit: 1000000000, UsedTraffic: 12345}
				}
				if err := offlineDurableJSON(filepath.Join(root, "usage-helper.json"), map[string]any{"inbound_tag": kind, "users": usage}); err != nil {
					b.Fatal(err)
				}
				if err := offlineDurableJSON(filepath.Join(root, "session-helper.json"), nativeSessionHelperConfig{InboundTag: kind, Users: users, Policies: policies}); err != nil {
					b.Fatal(err)
				}
				if kind == "openvpn" {
					if err := os.WriteFile(filepath.Join(root, "status.tsv"), []byte("HEADER\tCLIENT_LIST\tUsername\tClient ID\tConnected Since (time_t)\tBytes Received\tBytes Sent\n"), 0600); err != nil {
						b.Fatal(err)
					}
				}
			}
			quota := s.quotaCheckOpenVPNOffline
			if protocol == "ppp" {
				quota = s.quotaCheckPPPOffline
			}
			if err := quota(context.Background()); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := quota(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
