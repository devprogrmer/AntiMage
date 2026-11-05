package user

import "testing"

func TestServiceCreatePreservesTelegramOwner(t *testing.T) {
	id := "123456789"
	payload := UserServiceCreate{Username: "shop-account", ServiceID: 1, TelegramID: &id}
	created := payload.ToUserCreate(ServiceInfo{ID: 1})
	if created.TelegramID == nil || *created.TelegramID != id {
		t.Fatalf("telegram owner lost during service creation: %+v", created.TelegramID)
	}
	if err := ValidateUserServiceCreate(&payload, MutationContext{}); err != nil {
		t.Fatalf("valid numeric telegram owner rejected: %v", err)
	}
	bad := "not:an:id"
	payload.TelegramID = &bad
	if err := ValidateUserServiceCreate(&payload, MutationContext{}); err == nil {
		t.Fatal("invalid telegram owner accepted")
	}
}
