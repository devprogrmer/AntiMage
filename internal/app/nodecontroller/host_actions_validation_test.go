package nodecontroller

import (
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestMaintenanceResponseMustBeAccepted(t *testing.T) {
	for _, response := range []*nodev1.RuntimeActionResponse{nil, {}, {Accepted: false}} {
		if err := requireAcceptedMaintenanceResponse(response, "update runtime"); err == nil {
			t.Fatal("missing or rejected runtime response was accepted")
		}
	}
	if err := requireAcceptedMaintenanceResponse(&nodev1.RuntimeActionResponse{Accepted: true}, "update runtime"); err != nil {
		t.Fatal(err)
	}
}
