package bot

import (
	"context"
	"testing"

	"github.com/antimage/antimage/internal/app/support"
)

type fakeSupport struct {
	owner   int64
	created int
	replied int
}

func (f *fakeSupport) Create(_ context.Context, owner int64, content, reference string) (support.Ticket, error) {
	f.owner = owner
	f.created++
	return support.Ticket{ID: "11111111-2222-3333-4444-555555555555", TelegramID: owner}, nil
}
func (f *fakeSupport) List(_ context.Context, owner int64, _ bool) ([]support.Ticket, error) {
	f.owner = owner
	return nil, nil
}
func (f *fakeSupport) Get(_ context.Context, _ string, owner int64, _ bool) (support.Ticket, error) {
	f.owner = owner
	return support.Ticket{ID: "11111111-2222-3333-4444-555555555555"}, nil
}
func (f *fakeSupport) Reply(_ context.Context, _, _, _ string) (support.Ticket, error) {
	f.replied++
	return support.Ticket{ID: "11111111-2222-3333-4444-555555555555", TelegramID: 321}, nil
}

func TestSupportCommandsRequirePrivateOwnerAndAdminReply(t *testing.T) {
	b, _, settings := newTestBot(t, newFakeUsers())
	f := &fakeSupport{}
	b.support = f
	for _, msg := range []Message{
		{MessageID: 1, From: &User{ID: 321}, Chat: Chat{ID: -100, Type: "supergroup"}, Text: "/support hello"},
		{MessageID: 2, From: &User{ID: 321}, Chat: Chat{ID: 999, Type: "private"}, Text: "/support hello"},
		{MessageID: 3, From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/reply 11111111-2222-3333-4444-555555555555 hello"},
	} {
		b.handleMessage(context.Background(), settings, &msg)
	}
	if f.created != 0 || f.replied != 0 {
		t.Fatalf("unauthorized support action: %+v", f)
	}
	b.handleMessage(context.Background(), settings, &Message{MessageID: 4, From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/support cannot connect"})
	if f.created != 1 || f.owner != 321 {
		t.Fatalf("customer ticket not created: %+v", f)
	}
	b.handleMessage(context.Background(), settings, &Message{MessageID: 5, From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/reply 11111111-2222-3333-4444-555555555555 check node"})
	if f.replied != 1 {
		t.Fatalf("admin reply failed: %+v", f)
	}
}
