package networkintelligence

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type lifecycleSession struct {
	started, stopped bool
	startErr         error
}

func (s *lifecycleSession) Start(context.Context, CaptureConfig) error {
	s.started = true
	return s.startErr
}
func (s *lifecycleSession) Stop() error { s.stopped = true; return errors.New("collector failed") }

func TestLifecycleRevokesBeforeMutationAndRequiresRepublish(t *testing.T) {
	tracker, _ := New(8, time.Minute)
	l, _ := NewLifecycle(tracker, 1)
	b := Binding{NodeID: 1, Interface: "wg0", Address: netip.MustParseAddr("10.0.0.2"), UserID: 42, Session: "peer", Generation: 1, Since: time.Now()}
	s := &lifecycleSession{}
	if l.Publish(context.Background(), CaptureConfig{Interface: "wg0"}, []Binding{b}, func() (CaptureSession, error) { return s, nil }) == nil {
		t.Fatal("default enabled")
	}
	l.SetEnabled(true)
	if err := l.Publish(context.Background(), CaptureConfig{Interface: "wg0"}, []Binding{b}, func() (CaptureSession, error) { return s, nil }); err != nil {
		t.Fatal(err)
	}
	want := errors.New("runtime mutation")
	if err := l.Mutate("wg0", func() error {
		if !s.stopped || tracker.bindings[key(b)].Session != "" {
			t.Fatal("capture survived mutation barrier")
		}
		return want
	}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if len(l.sessions) != 0 {
		t.Fatal("automatic restart")
	}
	b.Generation++
	s2 := &lifecycleSession{}
	if err := l.Publish(context.Background(), CaptureConfig{Interface: "wg0"}, []Binding{b}, func() (CaptureSession, error) { return s2, nil }); err != nil {
		t.Fatal(err)
	}
	l.SetEnabled(false)
	if !s2.stopped || l.Enabled() || len(l.sessions) != 0 {
		t.Fatal("kill switch did not close capture")
	}
}

func TestLifecycleFailedStartRevokesBindings(t *testing.T) {
	tracker, _ := New(8, time.Minute)
	l, _ := NewLifecycle(tracker, 1)
	l.SetEnabled(true)
	b := Binding{NodeID: 1, Interface: "wg0", Address: netip.MustParseAddr("10.0.0.2"), UserID: 42, Session: "peer", Generation: 1, Since: time.Now()}
	s := &lifecycleSession{startErr: errors.New("socket unavailable")}
	if l.Publish(context.Background(), CaptureConfig{Interface: "wg0"}, []Binding{b}, func() (CaptureSession, error) { return s, nil }) == nil || !s.stopped || tracker.bindings[key(b)].Session != "" {
		t.Fatal("failed capture kept binding")
	}
}
