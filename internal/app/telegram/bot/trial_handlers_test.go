package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/antimage/antimage/internal/app/shop"
)

type fakeTrials struct {
	owner    int64
	claims   int
	config   shop.TrialConfig
	disables int
}

func (f *fakeTrials) ClaimAndProvision(_ context.Context, owner int64) (string, error) {
	f.owner = owner
	f.claims++
	return "tr123", nil
}
func (f *fakeTrials) Configure(_ context.Context, config shop.TrialConfig) error {
	f.config = config
	return nil
}
func (f *fakeTrials) Disable(context.Context) error { f.disables++; return nil }

func TestTrialOnlyOwnPrivateChatAndAdminConfiguration(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	f := &fakeTrials{}
	b.trials = f
	ctx := context.Background()
	for _, msg := range []Message{
		{From: &User{ID: 321}, Chat: Chat{ID: -100, Type: "supergroup"}, Text: "/trial"},
		{From: &User{ID: 321}, Chat: Chat{ID: 999, Type: "private"}, Text: "/trial"},
		{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/trial-config 42 2 7"},
		{From: &User{ID: 100}, Chat: Chat{ID: -100, Type: "supergroup"}, Text: "/trial-config 42 2 7"},
	} {
		b.handleMessage(ctx, settings, &msg)
	}
	if f.claims != 0 || f.config.ServiceID != 0 {
		t.Fatalf("unauthorized trial operation: %+v", f)
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/trial"})
	if f.claims != 1 || f.owner != 321 {
		t.Fatalf("customer claim: %+v", f)
	}
	if call, ok := lastCall(*calls, "sendMessage"); !ok || !strings.Contains(call.body["text"].(string), "tr123") {
		t.Fatalf("trial response: %+v", *calls)
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/trial-config 42 2 7"})
	if f.config.ServiceID != 42 || f.config.DataLimitBytes != 2<<30 || f.config.DurationDays != 7 {
		t.Fatalf("admin configuration: %+v", f.config)
	}
	b.handleMessage(ctx, settings, &Message{From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/trial-off"})
	if f.disables != 1 {
		t.Fatalf("trial disable calls: %d", f.disables)
	}
}
