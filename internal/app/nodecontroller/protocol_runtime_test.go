package nodecontroller

import (
	"context"
	"strings"
	"testing"
)

func TestProtocolRuntimeRejectsUnsupportedProtocol(t *testing.T) {
	controller := Controller{}
	_, err := controller.ProtocolRuntime(context.Background(), 1, "not-a-vpn")
	if err == nil || !strings.Contains(err.Error(), `unsupported protocol runtime "not-a-vpn"`) {
		t.Fatalf("ProtocolRuntime() error = %v, want unsupported protocol error", err)
	}
}
