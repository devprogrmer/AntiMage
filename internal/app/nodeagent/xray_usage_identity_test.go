package nodeagent

import (
	"encoding/base64"
	"testing"
)

func TestParseXrayUserEmail(t *testing.T) {
	tests := []struct {
		name         string
		email        string
		wantUserID   int64
		wantUsername string
		wantTag      string
		wantErr      bool
	}{
		{
			name:         "tagged format with simple tag",
			email:        "42.rb1_" + base64.RawURLEncoding.EncodeToString([]byte("vless-in")) + ".alice",
			wantUserID:   42,
			wantUsername: "alice",
			wantTag:      "vless-in",
			wantErr:      false,
		},
		{
			name:         "tagged format with Persian tag",
			email:        "123.rb1_" + base64.RawURLEncoding.EncodeToString([]byte("تهران.vless")) + ".user.name",
			wantUserID:   123,
			wantUsername: "user.name",
			wantTag:      "تهران.vless",
			wantErr:      false,
		},
		{
			name:         "legacy format without tag",
			email:        "99.alice.name",
			wantUserID:   99,
			wantUsername: "alice.name",
			wantTag:      "",
			wantErr:      false,
		},
		{
			name:         "legacy format simple",
			email:        "1.bob",
			wantUserID:   1,
			wantUsername: "bob",
			wantTag:      "",
			wantErr:      false,
		},
		{
			name:    "empty email",
			email:   "",
			wantErr: true,
		},
		{
			name:    "no dots",
			email:   "42",
			wantErr: true,
		},
		{
			name:    "invalid user ID",
			email:   "abc.username",
			wantErr: true,
		},
		{
			name:    "negative user ID",
			email:   "-1.username",
			wantErr: true,
		},
		{
			name:    "zero user ID",
			email:   "0.username",
			wantErr: true,
		},
		{
			name:    "malformed tag encoding",
			email:   "42.rb1_!!!invalid!!!.username",
			wantErr: true,
		},
		{
			name:    "tagged format with empty decoded tag",
			email:   "42.rb1_" + base64.RawURLEncoding.EncodeToString([]byte("")) + ".username",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity, err := parseXrayUserEmail(tt.email)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseXrayUserEmail(%q) error = %v, wantErr %v", tt.email, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if identity.UserID != tt.wantUserID {
				t.Errorf("UserID = %d, want %d", identity.UserID, tt.wantUserID)
			}
			if identity.Username != tt.wantUsername {
				t.Errorf("Username = %q, want %q", identity.Username, tt.wantUsername)
			}
			if identity.InboundTag != tt.wantTag {
				t.Errorf("InboundTag = %q, want %q", identity.InboundTag, tt.wantTag)
			}
		})
	}
}

func TestParseXrayStatName(t *testing.T) {
	tests := []struct {
		name          string
		statName      string
		wantType      string
		wantEmail     string
		wantTag       string
		wantDirection string
		wantOK        bool
	}{
		{
			name:          "user uplink",
			statName:      "user>>>42.alice>>>traffic>>>uplink",
			wantType:      "user",
			wantEmail:     "42.alice",
			wantDirection: "uplink",
			wantOK:        true,
		},
		{
			name:          "user downlink",
			statName:      "user>>>99.bob.name>>>traffic>>>downlink",
			wantType:      "user",
			wantEmail:     "99.bob.name",
			wantDirection: "downlink",
			wantOK:        true,
		},
		{
			name:          "outbound uplink",
			statName:      "outbound>>>direct>>>traffic>>>uplink",
			wantType:      "outbound",
			wantTag:       "direct",
			wantDirection: "uplink",
			wantOK:        true,
		},
		{
			name:          "outbound downlink",
			statName:      "outbound>>>proxy-out>>>traffic>>>downlink",
			wantType:      "outbound",
			wantTag:       "proxy-out",
			wantDirection: "downlink",
			wantOK:        true,
		},
		{
			name:          "inbound uplink",
			statName:      "inbound>>>vless-in>>>traffic>>>uplink",
			wantType:      "inbound",
			wantTag:       "vless-in",
			wantDirection: "uplink",
			wantOK:        true,
		},
		{
			name:          "inbound downlink",
			statName:      "inbound>>>vmess-in>>>traffic>>>downlink",
			wantType:      "inbound",
			wantTag:       "vmess-in",
			wantDirection: "downlink",
			wantOK:        true,
		},
		{
			name:     "wrong number of parts",
			statName: "user>>>email>>>traffic",
			wantOK:   false,
		},
		{
			name:     "not traffic",
			statName: "user>>>email>>>bandwidth>>>uplink",
			wantOK:   false,
		},
		{
			name:     "invalid direction",
			statName: "user>>>email>>>traffic>>>sideways",
			wantOK:   false,
		},
		{
			name:     "empty email",
			statName: "user>>>>>>traffic>>>uplink",
			wantOK:   false,
		},
		{
			name:     "empty tag",
			statName: "outbound>>>>>>traffic>>>uplink",
			wantOK:   false,
		},
		{
			name:     "unknown type",
			statName: "unknown>>>tag>>>traffic>>>uplink",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stat, ok := parseXrayStatName(tt.statName)
			if ok != tt.wantOK {
				t.Errorf("parseXrayStatName(%q) ok = %v, want %v", tt.statName, ok, tt.wantOK)
				return
			}
			if !tt.wantOK {
				return
			}
			if stat.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", stat.Type, tt.wantType)
			}
			if stat.Email != tt.wantEmail {
				t.Errorf("Email = %q, want %q", stat.Email, tt.wantEmail)
			}
			if stat.Tag != tt.wantTag {
				t.Errorf("Tag = %q, want %q", stat.Tag, tt.wantTag)
			}
			if stat.Direction != tt.wantDirection {
				t.Errorf("Direction = %q, want %q", stat.Direction, tt.wantDirection)
			}
		})
	}
}

func TestParseXrayUserEmailDoesNotMixUsers(t *testing.T) {
	// Ensure that malformed input cannot accidentally attribute one user's identity to another
	emails := []string{
		"42.alice",
		"43.bob",
		"42.rb1_" + base64.RawURLEncoding.EncodeToString([]byte("tag1")) + ".alice",
		"43.rb1_" + base64.RawURLEncoding.EncodeToString([]byte("tag2")) + ".bob",
	}

	seen := make(map[int64]bool)
	for _, email := range emails {
		identity, err := parseXrayUserEmail(email)
		if err != nil {
			t.Fatalf("parseXrayUserEmail(%q) unexpected error: %v", email, err)
		}
		if seen[identity.UserID] {
			// If we see the same user ID twice, ensure the usernames actually match
			// This test ensures we're not accidentally parsing one user as another
			for _, otherEmail := range emails {
				otherIdentity, _ := parseXrayUserEmail(otherEmail)
				if otherIdentity.UserID == identity.UserID && otherIdentity.Username != identity.Username {
					t.Errorf("User ID %d mapped to different usernames: %q and %q", identity.UserID, identity.Username, otherIdentity.Username)
				}
			}
		}
		seen[identity.UserID] = true
	}
}
