package user

import (
	"context"
	"strings"
	"testing"
)

func TestSubscriptionPlaceholderRemarkOnlyReplacesInactiveUsers(t *testing.T) {
	settings := SubscriptionSettings{
		SubscriptionPlaceholderEnabled:        true,
		SubscriptionPlaceholderExpiredRemark:  "Expired: {USERNAME} ({STATUS_TEXT})",
		SubscriptionPlaceholderLimitedRemark:  "Limited: {USERNAME} ({DATA_USAGE}/{DATA_LIMIT})",
		SubscriptionPlaceholderDisabledRemark: "Disabled: {USERNAME}",
	}
	limit := int64(10)
	user := UserDetail{Username: "alice", Status: "limited", UsedTraffic: 9, DataLimit: &limit}
	if got, want := subscriptionPlaceholderRemark(user, settings), "Limited: alice (9 B/10 B)"; got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}
	user.Status = "active"
	if got := subscriptionPlaceholderRemark(user, settings); got != "" {
		t.Fatalf("active user got placeholder %q", got)
	}
}

func TestSubscriptionPlaceholderRemarkDisabledByDefault(t *testing.T) {
	if got := subscriptionPlaceholderRemark(UserDetail{Username: "alice", Status: "disabled"}, SubscriptionSettings{}); got != "" {
		t.Fatalf("disabled-by-default setting returned %q", got)
	}
}

func TestRenderSubscriptionUsesFallbackInsteadOfRealConnections(t *testing.T) {
	service, key := newSubscriptionClientTestService(t)
	ctx := context.Background()
	for _, statement := range []string{
		`ALTER TABLE subscription_settings ADD COLUMN subscription_placeholder_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE subscription_settings ADD COLUMN subscription_placeholder_expired_remark TEXT NOT NULL DEFAULT 'Subscription expired'`,
		`ALTER TABLE subscription_settings ADD COLUMN subscription_placeholder_limited_remark TEXT NOT NULL DEFAULT 'Traffic limit reached'`,
		`ALTER TABLE subscription_settings ADD COLUMN subscription_placeholder_disabled_remark TEXT NOT NULL DEFAULT 'Subscription disabled'`,
		`UPDATE subscription_settings SET subscription_placeholder_enabled = 1, subscription_placeholder_limited_remark = 'Limit reached for {USERNAME}'`,
		`UPDATE users SET status = 'limited' WHERE username = 'alice'`,
	} {
		if _, err := service.repo.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	response, err := service.RenderSubscription(ctx, SubscriptionRenderRequest{Identifier: key, ClientType: "v2ray"})
	if err != nil {
		t.Fatal(err)
	}
	body := decodeSubscriptionTestBody(string(response.Body))
	if !strings.Contains(body, "vmess://") || strings.Contains(body, "edge.example.com") {
		t.Fatalf("inactive subscription leaked real connection details: %s", body)
	}
	html, err := service.RenderSubscription(ctx, SubscriptionRenderRequest{Identifier: key, Accept: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html.Body), "Limit reached for alice") || strings.Contains(string(html.Body), "edge.example.com") {
		t.Fatalf("inactive subscription page leaked connection details or omitted its message: %s", html.Body)
	}
}
