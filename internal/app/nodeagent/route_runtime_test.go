package nodeagent

import (
	"path/filepath"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRouteRejectsMissingConfig(t *testing.T) {
	s := New(Config{})

	_, err := s.TestRoute(t.Context(), &nodev1.RouteTestRequest{
		TestUrl: "https://example.com/",
	})
	if err == nil {
		t.Fatal("expected missing config to fail")
	}

	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status=%v error=%v", status.Code(err), err)
	}
}

func TestRouteRejectsMissingURL(t *testing.T) {
	s := New(Config{})

	_, err := s.TestRoute(t.Context(), &nodev1.RouteTestRequest{
		ConfigJson: `{
"outbounds":[
{"tag":"direct","protocol":"freedom"}
]
}`,
	})
	if err == nil {
		t.Fatal("expected missing test URL to fail")
	}

	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status=%v error=%v", status.Code(err), err)
	}
}

func TestRouteRejectsInvalidURL(t *testing.T) {
	s := New(Config{})

	_, err := s.TestRoute(t.Context(), &nodev1.RouteTestRequest{
		ConfigJson: `{
"outbounds":[
{"tag":"direct","protocol":"freedom"}
]
}`,
		TestUrl: "ftp://example.com/file",
	})
	if err == nil {
		t.Fatal("expected invalid URL to fail")
	}

	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status=%v error=%v", status.Code(err), err)
	}
}

func TestRouteReportsUnavailableXray(t *testing.T) {
	dataDir := t.TempDir()

	s := New(Config{
		DataDir:  dataDir,
		XrayPath: filepath.Join(dataDir, "missing-xray"),
	})

	res, err := s.TestRoute(t.Context(), &nodev1.RouteTestRequest{
		InboundTag: "customer-in",
		ConfigJson: `{
"outbounds":[
{"tag":"direct","protocol":"freedom"}
],
"routing":{
"rules":[
{
"type":"field",
"inboundTag":["customer-in"],
"domain":["domain:example.com"],
"outboundTag":"direct"
}
]
}
}`,
		TestUrl: "https://example.com/",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}

	if res.GetSuccess() {
		t.Fatal("expected route test to fail without Xray")
	}

	if !strings.Contains(
		res.GetError(),
		"Xray executable is unavailable",
	) {
		t.Fatalf("unexpected error=%q", res.GetError())
	}
}
