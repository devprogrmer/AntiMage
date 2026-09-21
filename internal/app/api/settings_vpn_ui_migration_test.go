package api

import (
	"strings"
	"testing"
	"time"

	vpnuimigration "github.com/antimage/antimage/internal/app/vpnuimigration"
)

func TestVPNUIRenamedUsernameIsDeterministicAndBounded(t *testing.T) {
	got := vpnUIRenamedUsername(strings.Repeat("a", 34), 42)
	if got != strings.Repeat("a", 28)+"-vpn42" {
		t.Fatalf("renamed username = %q", got)
	}
	if len(got) > 34 {
		t.Fatalf("renamed username length = %d, want at most 34", len(got))
	}
}

func TestVPNUIImportedStatus(t *testing.T) {
	now := time.Now().Unix()
	limit := int64(100)
	expired := now - 1

	tests := []struct {
		name    string
		account vpnuimigration.Account
		want    string
	}{
		{name: "active", account: vpnuimigration.Account{Enabled: true}, want: "active"},
		{name: "disabled", account: vpnuimigration.Account{Enabled: false}, want: "disabled"},
		{name: "expired", account: vpnuimigration.Account{Enabled: true, Expire: &expired}, want: "expired"},
		{name: "limited", account: vpnuimigration.Account{Enabled: true, DataLimit: &limit, UsedBytes: limit}, want: "limited"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vpnUIImportedStatus(tt.account, now); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}
