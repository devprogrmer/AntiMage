package nodeagent

import (
	"context"
	"errors"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	"net/netip"
	"testing"
	"time"
)

type monitoringBarrierSession struct{ stopped bool }

func (*monitoringBarrierSession) Start(context.Context, ni.CaptureConfig) error { return nil }
func (s *monitoringBarrierSession) Stop() error {
	s.stopped = true
	return errors.New("collector failure")
}

func TestNetworkIntelligenceMutationPreservesRuntimeAndJoinsCapture(t *testing.T) {
	runtimeErr := errors.New("runtime failure")
	s := &Server{}
	if got := s.withNetworkIntelligenceMutation("wg0", func() error { return runtimeErr }); got != runtimeErr {
		t.Fatal(got)
	}
	tracker, _ := ni.New(8, time.Minute)
	lifecycle, _ := ni.NewLifecycle(tracker, 2)
	lifecycle.SetEnabled(true)
	s.networkIntelligenceLifecycle = lifecycle
	session := &monitoringBarrierSession{}
	b := ni.Binding{NodeID: 1, Interface: "wg0", Address: netip.MustParseAddr("10.0.0.2"), UserID: 42, Session: "peer", Generation: 1, Since: time.Now()}
	if err := lifecycle.Publish(context.Background(), ni.CaptureConfig{Interface: "wg0"}, []ni.Binding{b}, func() (ni.CaptureSession, error) { return session, nil }); err != nil {
		t.Fatal(err)
	}
	if got := s.withNetworkIntelligenceMutation("wg0", func() error {
		if !session.stopped {
			t.Fatal("capture active during mutation")
		}
		return runtimeErr
	}); got != runtimeErr {
		t.Fatal(got)
	}
}
