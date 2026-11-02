package bot

import (
	"context"
	"testing"

	"github.com/antimage/antimage/internal/app/shop"
)

type fakeTopups struct {
	owner        int64
	created      int
	submitted    int
	approved     int
	instructions string
}

func (f *fakeTopups) Get(context.Context, string) (shop.Topup, error) {
	fileID := "photo-1"
	return shop.Topup{ID: "11111111-2222-3333-4444-555555555555", TelegramID: 321, AmountToman: 80000, Status: "submitted", ReceiptFileID: &fileID}, nil
}
func (f *fakeTopups) Instructions(context.Context) (string, error) { return f.instructions, nil }
func (f *fakeTopups) SetInstructions(_ context.Context, text string) error {
	f.instructions = text
	return nil
}
func (f *fakeTopups) Create(_ context.Context, owner, amount int64, _ string) (shop.Topup, error) {
	f.owner, f.created = owner, f.created+1
	return shop.Topup{ID: "11111111-2222-3333-4444-555555555555", TelegramID: owner, AmountToman: amount}, nil
}
func (f *fakeTopups) SubmitReceipt(_ context.Context, owner int64, _, _ string) (shop.Topup, error) {
	f.owner, f.submitted = owner, f.submitted+1
	return shop.Topup{ID: "11111111-2222-3333-4444-555555555555", TelegramID: owner, AmountToman: 80000}, nil
}
func (f *fakeTopups) List(context.Context, int64, bool) ([]shop.Topup, error) { return nil, nil }
func (f *fakeTopups) Approve(_ context.Context, _ string, adminID int64) (shop.Topup, error) {
	f.approved++
	return shop.Topup{TelegramID: 321, Status: "approved", ReviewedBy: &adminID}, nil
}
func (f *fakeTopups) Reject(context.Context, string, int64) (shop.Topup, error) {
	return shop.Topup{TelegramID: 321, Status: "rejected"}, nil
}

func TestTopupReceiptAndApprovalRequirePrivateOwnerAndAdmin(t *testing.T) {
	b, calls, settings := newTestBot(t, newFakeUsers())
	f := &fakeTopups{instructions: "Bank 1234"}
	b.topups = f
	ctx := context.Background()
	b.handleMessage(ctx, settings, &Message{MessageID: 1, From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Text: "/topup 80000"})
	if f.created != 1 || f.owner != 321 {
		t.Fatalf("private topup failed: %+v", f)
	}
	b.handleMessage(ctx, settings, &Message{MessageID: 2, From: &User{ID: 321}, Chat: Chat{ID: -100, Type: "supergroup"}, Caption: "/receipt 11111111-2222-3333-4444-555555555555", Photo: []PhotoSize{{FileID: "photo-1"}}})
	b.handleMessage(ctx, settings, &Message{MessageID: 3, From: &User{ID: 321}, Chat: Chat{ID: 999, Type: "private"}, Caption: "/receipt 11111111-2222-3333-4444-555555555555", Photo: []PhotoSize{{FileID: "photo-1"}}})
	if f.submitted != 0 {
		t.Fatal("other chat submitted receipt")
	}
	b.handleMessage(ctx, settings, &Message{MessageID: 4, From: &User{ID: 321}, Chat: Chat{ID: 321, Type: "private"}, Caption: "/receipt 11111111-2222-3333-4444-555555555555", Photo: []PhotoSize{{FileID: "photo-1"}}})
	if f.submitted != 1 {
		t.Fatal("private receipt not saved")
	}
	if _, ok := lastCall(*calls, "sendPhoto"); !ok {
		t.Fatal("receipt photo not sent to admin")
	}
	b.handleMessage(ctx, settings, &Message{MessageID: 5, From: &User{ID: 100}, Chat: Chat{ID: 100, Type: "private"}, Text: "/topup-review 11111111-2222-3333-4444-555555555555"})
	if photo, ok := lastCall(*calls, "sendPhoto"); !ok || photo.body["chat_id"] != float64(100) {
		t.Fatalf("admin cannot reopen receipt: %+v", photo)
	}
	for _, chat := range []Chat{{ID: 321, Type: "private"}, {ID: -100, Type: "supergroup"}} {
		b.handleCallback(ctx, settings, &CallbackQuery{ID: "untrusted", From: &User{ID: 321}, Message: &Message{Chat: chat}, Data: "topup:approve-confirm:11111111-2222-3333-4444-555555555555"})
	}
	if f.approved != 0 {
		t.Fatal("untrusted actor approved wallet credit")
	}
	b.handleCallback(ctx, settings, &CallbackQuery{ID: "admin", From: &User{ID: 100}, Message: &Message{Chat: Chat{ID: 100, Type: "private"}}, Data: "topup:approve-confirm:11111111-2222-3333-4444-555555555555"})
	if f.approved != 1 {
		t.Fatal("allowlisted admin approval failed")
	}
}
