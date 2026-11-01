package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestResolveDashboardFile(t *testing.T) {
	files := fstest.MapFS{
		"index.html":                {},
		"statics/guide.abcdef12.js": {},
	}

	tests := []struct {
		name     string
		expected string
		found    bool
	}{
		{name: "statics/guide.abcdef12.js", expected: "statics/guide.abcdef12.js", found: true},
		{name: "missing", expected: "", found: false},
	}

	for _, test := range tests {
		actual, found := resolveDashboardFile(files, test.name)
		if actual != test.expected || found != test.found {
			t.Fatalf("resolveDashboardFile(%q) = %q, %v; want %q, %v", test.name, actual, found, test.expected, test.found)
		}
	}
}

func TestDashboardFilesRedirectsLegacyTutorialContent(t *testing.T) {
	dashboard := &dashboardFiles{
		root: "/custom",
		fs: fstest.MapFS{
			"index.html": {Data: []byte("panel")},
		},
	}

	request := httptest.NewRequest(http.MethodGet, "/custom/tutorial-content/docs/", nil)
	response := httptest.NewRecorder()
	dashboard.serve(response, request)
	if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != "/custom/guide" {
		t.Fatalf("legacy guide status=%d location=%q", response.Code, response.Header().Get("Location"))
	}

	shell := httptest.NewRecorder()
	dashboard.serve(
		shell,
		httptest.NewRequest(http.MethodGet, "/custom/guide", nil),
	)
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "panel") {
		t.Fatalf("guide shell status=%d body=%q", shell.Code, shell.Body.String())
	}
}

func TestDashboardFilesCacheControl(t *testing.T) {
	dashboard := &dashboardFiles{
		root: "/dashboard",
		fs: fstest.MapFS{
			"index.html":                {Data: []byte("panel")},
			"statics/index.abcdef12.js": {Data: []byte("hashed")},
			"statics/locales/en.json":   {Data: []byte("locale")},
		},
	}

	tests := []struct {
		path     string
		expected string
	}{
		{path: "/dashboard/settings", expected: "no-store"},
		{path: "/statics/index.abcdef12.js", expected: "public, max-age=31536000, immutable"},
		{path: "/statics/locales/en.json", expected: "no-cache"},
	}

	for _, test := range tests {
		response := httptest.NewRecorder()
		dashboard.serve(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", test.path, response.Code)
		}
		if actual := response.Header().Get("Cache-Control"); actual != test.expected {
			t.Fatalf("%s cache control=%q; want %q", test.path, actual, test.expected)
		}
	}
}
