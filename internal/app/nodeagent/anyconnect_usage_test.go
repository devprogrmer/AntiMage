package nodeagent

import "testing"

func TestParseAnyConnectUsersJSON(t *testing.T) {
	raw := []byte(`{"users":[{"id":"17","username":"alice","ip_real":"198.51.100.7","ip_remote":"10.71.0.10","bytes_in":1200,"bytes_out":3400,"rx_per_sec":12,"tx_per_sec":34}]}`)
	sessions, err := parseAnyConnectUsersJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%d, want 1", len(sessions))
	}
	s := sessions[0]
	if s.Username != "alice" || s.ClientIP != "198.51.100.7" || s.AssignedIP != "10.71.0.10" {
		t.Fatalf("unexpected session: %+v", s)
	}
	if s.Received != 1200 || s.Sent != 3400 {
		t.Fatalf("unexpected counters: %+v", s)
	}
}

func TestParseAnyConnectUsersJSONAllowsTopLevelArrayAndStringCounters(t *testing.T) {
	sessions, err := parseAnyConnectUsersJSON([]byte(`[{"user":"bob","session-id":"x","remote-ip":"203.0.113.4","bytes-in":"9","bytes-out":"11"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Received != 9 || sessions[0].Sent != 11 {
		t.Fatalf("unexpected sessions: %+v", sessions)
	}
}

func TestParseAnyConnectUsersJSONMatchesOcctlFieldNames(t *testing.T) {
	sessions, err := parseAnyConnectUsersJSON([]byte(`[{"ID":17,"Username":"carol","Remote IP":"192.0.2.9","IPv4":"10.71.0.11","Full session":"abcdef","raw_rx":100,"raw_tx":200}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%d", len(sessions))
	}
	s := sessions[0]
	if s.Username != "carol" || s.ClientIP != "192.0.2.9" || s.AssignedIP != "10.71.0.11" || s.ID != "abcdef" || s.Received != 100 || s.Sent != 200 {
		t.Fatalf("unexpected occtl session: %+v", s)
	}
}

func TestAnyConnectUsageBatchDoesNotExposeSecrets(t *testing.T) {
	batch := anyConnectUsageBatchProto(&anyConnectUsagePendingBatch{BatchID: "anyconnect-1", Samples: []anyConnectUsageSample{{UserID: 7, InboundTag: "ac", Online: true, IPs: []string{"198.51.100.8"}}}})
	if len(batch.GetStats()) != 1 || batch.GetStats()[0].GetUid() != "online:anyconnect:7" {
		t.Fatalf("unexpected batch: %+v", batch)
	}
	if len(batch.GetOnlineIps()) != 1 || batch.GetOnlineIps()[0].GetIps()[0].GetIp() != "198.51.100.8" {
		t.Fatalf("missing endpoint IP: %+v", batch)
	}
}
