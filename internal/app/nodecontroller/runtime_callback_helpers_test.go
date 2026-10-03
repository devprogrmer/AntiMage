package nodecontroller

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestRuntimeSessionCallbackEnvironmentReadsEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\n export ANTIMAGE_PUBLIC_URL = 'https://file.example.com' \nUVICORN_PORT=9443\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_ENV_FILE", path)
	t.Setenv("ANTIMAGE_NODE_SESSION_CALLBACK_URL", "")
	t.Setenv("ANTIMAGE_PUBLIC_URL", "")
	t.Setenv("PUBLIC_URL", "")

	env := runtimeSessionCallbackEnvironment()
	if got := env["ANTIMAGE_PUBLIC_URL"]; got != "https://file.example.com" {
		t.Fatalf("env file URL = %q", got)
	}
	if got := env["UVICORN_PORT"]; got != "9443" {
		t.Fatalf("env file port = %q", got)
	}

	t.Setenv("ANTIMAGE_PUBLIC_URL", "https://process.example.com")
	if got := runtimeSessionCallbackEnvironment()["ANTIMAGE_PUBLIC_URL"]; got != "https://process.example.com" {
		t.Fatalf("process env did not override file: %q", got)
	}
}
