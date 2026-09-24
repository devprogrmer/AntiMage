package nodecontroller

import "testing"

func TestNormalizeRuntimeSessionCallbackBase(t *testing.T) {
	got, err := normalizeRuntimeSessionCallbackBase(
		"https://panel.example.com:8000/internal/node/session-event",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://panel.example.com:8000" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeRuntimeSessionCallbackRejectsMarkdownURL(t *testing.T) {
	_, err := normalizeRuntimeSessionCallbackBase(
		"[https://panel.example.com](https://panel.example.com)",
	)
	if err == nil {
		t.Fatal("expected malformed markdown URL to be rejected")
	}
}

func TestNormalizeRuntimeSessionCallbackRejectsQuery(t *testing.T) {
	_, err := normalizeRuntimeSessionCallbackBase(
		"https://panel.example.com:8000?bad=1",
	)
	if err == nil {
		t.Fatal("expected query string to be rejected")
	}
}
