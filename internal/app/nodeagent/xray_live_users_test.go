package nodeagent

import (
	"context"
	"fmt"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodeUserOperationsDoNotAcknowledgeNoOp(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	_, add := s.AddUser(context.Background(), &nodev1.InboundUserRequest{})
	_, update := s.UpdateUser(context.Background(), &nodev1.InboundUserRequest{})
	_, remove := s.RemoveUser(context.Background(), &nodev1.RemoveInboundUserRequest{})
	for name, err := range map[string]error{"add": add, "update": update, "remove": remove} {
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("%s silently accepted a no-op: %v", name, err)
		}
	}
}

func TestXrayLiveUserAdditionsPlan(t *testing.T) {
	config := func(clients string) string {
		return fmt.Sprintf(`{"log":{"loglevel":"warning"},"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"decryption":"none","clients":%s}}]}`, clients)
	}
	old := config(`[{"email":"one","id":"11111111-1111-4111-8111-111111111111"}]`)
	next := config(`[{"email":"one","id":"11111111-1111-4111-8111-111111111111"},{"email":"two","id":"22222222-2222-4222-8222-222222222222","level":0}]`)
	adds, eligible, err := xrayLiveUserAdditions(old, next)
	if err != nil || !eligible || len(adds) != 1 || adds[0].tag != "vless" || adds[0].user.Email != "two" {
		t.Fatalf("plan: %+v eligible=%v err=%v", adds, eligible, err)
	}
	adds, eligible, err = xrayLiveUserAdditions(old, old)
	if err != nil || !eligible || len(adds) != 0 {
		t.Fatal("identical runtime should not restart")
	}
}

func TestXrayLiveUserAdditionsRefusesStructuralOrRevocationChanges(t *testing.T) {
	old := `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"}],"decryption":"none"}}]}`
	for name, next := range map[string]string{
		"remove":                `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[],"decryption":"none"}}]}`,
		"credential-update":     `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"changed"}],"decryption":"none"}}]}`,
		"port-change":           `{"inbounds":[{"tag":"vless","port":8443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"}],"decryption":"none"}}]}`,
		"unknown-account-field": `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"},{"email":"two","id":"22222222-2222-4222-8222-222222222222","reverse":{"tag":"reverse"}}],"decryption":"none"}}]}`,
		"duplicate-email":       `{"inbounds":[{"tag":"vless","port":443,"protocol":"vless","settings":{"clients":[{"email":"one","id":"11111111-1111-4111-8111-111111111111"},{"email":"one","id":"22222222-2222-4222-8222-222222222222"}],"decryption":"none"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, eligible, err := xrayLiveUserAdditions(old, next)
			if err != nil || eligible {
				t.Fatalf("unsafe live plan: eligible=%v err=%v", eligible, err)
			}
		})
	}
}
