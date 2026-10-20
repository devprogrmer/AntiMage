package nodeagent

import (
	"context"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"testing"
)

func TestIKEv2MutationEnvelopeAndDurableIdempotency(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	desired := &ikev2RuntimeUser{UserID: 7, Username: "alice", Password: "secret", Status: "active"}
	req := &nodev1.InboundUser{Protocol: "ikev2", Fields: map[string]string{ikev2MutationUserIDField: "7", ikev2MutationOperation: "update", ikev2MutationRevision: "9", ikev2MutationUserJSON: `{"user_id":7,"username":"alice","password":"secret","status":"active"}`}}
	id, rev, op, got, err := decodeIKEv2MutationUser(req, "update")
	if err != nil || id != 7 || rev != 9 || op != "update" || got.Username != desired.Username {
		t.Fatalf("decode id=%d rev=%d op=%q user=%+v err=%v", id, rev, op, got, err)
	}
	digest := ikev2MutationDigest("ike", id, op, got)
	if err := s.recordIKEv2MutationCompleted("op-1", digest); err != nil {
		t.Fatal(err)
	}
	done, err := s.ikev2MutationAlreadyCompleted("op-1", digest)
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if _, err := s.ikev2MutationAlreadyCompleted("op-1", ikev2MutationDigest("ike", id, "update", &ikev2RuntimeUser{UserID: 7, Username: "alice", Password: "changed"})); err == nil {
		t.Fatal("conflicting operation id accepted")
	}
	remove := &nodev1.InboundUser{Protocol: "ikev2", Fields: map[string]string{ikev2MutationUserIDField: "7", ikev2MutationOperation: "remove", ikev2MutationRevision: "10"}}
	if _, _, op, got, err := decodeIKEv2MutationUser(remove, "remove"); err != nil || op != "remove" || got != nil {
		t.Fatalf("remove decode op=%q got=%v err=%v", op, got, err)
	}
	_ = context.Background()
}
