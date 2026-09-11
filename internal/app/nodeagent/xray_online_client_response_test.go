package nodeagent

import "testing"

func TestParseXrayOnlineCountResponseV26711(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		email   string
		want    int32
		wantErr bool
	}{
		{
			name:   "online numeric value",
			output: `{"stat":{"name":"user>>>42.alice>>>online","value":2}}`,
			email:  "42.alice",
			want:   2,
		},
		{
			name:   "online string value",
			output: `{"stat":{"name":"user>>>42.alice>>>online","value":"3"}}`,
			email:  "42.alice",
			want:   3,
		},
		{
			name:   "zero value may be omitted",
			output: `{"stat":{"name":"user>>>42.alice>>>online"}}`,
			email:  "42.alice",
			want:   0,
		},
		{
			name:    "legacy wrong count shape is rejected",
			output:  `{"count":2}`,
			email:   "42.alice",
			wantErr: true,
		},
		{
			name:    "wrong stat identity",
			output:  `{"stat":{"name":"user>>>77.bob>>>online","value":1}}`,
			email:   "42.alice",
			wantErr: true,
		},
		{
			name:    "negative value",
			output:  `{"stat":{"name":"user>>>42.alice>>>online","value":-1}}`,
			email:   "42.alice",
			wantErr: true,
		},
		{
			name:    "malformed JSON",
			output:  `{"stat":`,
			email:   "42.alice",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseXrayOnlineCountResponse(
				[]byte(tt.output),
				tt.email,
			)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("count=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseXrayBulkOnlineResponseV26711(t *testing.T) {
	output := []byte(`{
"users": [
{
"email": "42.alice",
"ips": [
{"ip":"127.0.0.1","lastSeen":100},
{"ip":"127.0.0.2","lastSeen":101},
{"ip":"127.0.0.1","lastSeen":103},
{"ip":"   ","lastSeen":104}
]
},
{
"email": "77.bob",
"ips": [
{"ip":"127.0.0.3","lastSeen":102}
]
}
]
}`)

	got, err := parseXrayBulkOnlineResponse(output)
	if err != nil {
		t.Fatalf("parse bulk online response: %v", err)
	}

	alice := got["42.alice"]
	if alice.Count != 2 {
		t.Fatalf("alice count=%d, want 2", alice.Count)
	}
	if len(alice.IPs) != 2 {
		t.Fatalf("alice IPs=%d, want 2", len(alice.IPs))
	}
	if alice.IPs[0].IP != "127.0.0.1" || alice.IPs[0].LastSeen != 103 {
		t.Fatalf("alice first IP=%+v, want 127.0.0.1 lastSeen=103", alice.IPs[0])
	}
	if alice.IPs[1].IP != "127.0.0.2" || alice.IPs[1].LastSeen != 101 {
		t.Fatalf("alice second IP=%+v, want 127.0.0.2 lastSeen=101", alice.IPs[1])
	}

	bob := got["77.bob"]
	if bob.Count != 1 {
		t.Fatalf("bob count=%d, want 1", bob.Count)
	}
	if len(bob.IPs) != 1 || bob.IPs[0].IP != "127.0.0.3" {
		t.Fatalf("bob IPs=%+v, want 127.0.0.3", bob.IPs)
	}
}

func TestParseXrayBulkOnlineResponseEmpty(t *testing.T) {
	got, err := parseXrayBulkOnlineResponse([]byte(`{}`))
	if err != nil {
		t.Fatalf("parse empty bulk response: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("online users=%d, want 0", len(got))
	}
}

func TestParseXrayBulkOnlineResponseRejectsOldMapShape(t *testing.T) {
	_, err := parseXrayBulkOnlineResponse(
		[]byte(`{"users":{"42.alice":1}}`),
	)
	if err == nil {
		t.Fatal("expected old map-shaped response to be rejected")
	}
}
