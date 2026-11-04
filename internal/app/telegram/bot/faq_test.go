package bot

import (
	"context"
	"strings"
	"testing"
)

func TestFAQMatchesImplementedPaymentFlowInEveryLanguage(t *testing.T) {
	for _, language := range []string{"fa", "en", "ru"} {
		ctx := context.WithValue(context.Background(), languageContextKey{}, language)
		content := faqText(ctx)
		for _, command := range []string{"/shop", "/orders", "/topup", "/receipt", "/my", "/support", "/tickets"} {
			if !strings.Contains(content, command) {
				t.Fatalf("%s FAQ missing %s", language, command)
			}
		}
		if strings.Contains(strings.ToLower(content), "automatic payment gateway") {
			t.Fatalf("%s FAQ promises an unsupported gateway", language)
		}
	}
}

func TestCustomerMenuOffersFAQ(t *testing.T) {
	for _, language := range []string{"fa", "en", "ru"} {
		menu := menuForLanguage(false, language, "")
		found := false
		for _, row := range menu.InlineKeyboard {
			for _, button := range row {
				found = found || button.CallbackData == "menu:faq"
			}
		}
		if !found {
			t.Fatalf("%s menu missing FAQ", language)
		}
	}
}
