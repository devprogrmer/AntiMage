package gateway

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDashboardFilesServePrecompressedAssets(t *testing.T) {
	const asset = "statics/vendor.abcdef12.js"
	raw := []byte(strings.Repeat("const example = 123;\n", 100))
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	dashboard := &dashboardFiles{root: "/dashboard", fs: fstest.MapFS{
		asset:         {Data: raw},
		asset + ".gz": {Data: compressed.Bytes()},
	}}
	for _, test := range []struct {
		name, accept, byteRange string
		gzip                    bool
	}{
		{name: "browser", accept: "br, gzip, deflate", gzip: true},
		{name: "weighted", accept: "gzip;q=0.5", gzip: true},
		{name: "identity"},
		{name: "forbidden", accept: "gzip;q=0, *;q=1"},
		{name: "range", accept: "gzip", byteRange: "bytes=0-4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/"+asset, nil)
			req.Header.Set("Accept-Encoding", test.accept)
			req.Header.Set("Range", test.byteRange)
			resp := httptest.NewRecorder()
			dashboard.serve(resp, req)
			header := resp.Header()
			if header.Get("Vary") != "Accept-Encoding" || !strings.Contains(header.Get("Content-Type"), "javascript") {
				t.Fatalf("unexpected headers: %v", header)
			}
			if (header.Get("Content-Encoding") == "gzip") != test.gzip {
				t.Fatalf("unexpected Content-Encoding: %q", header.Get("Content-Encoding"))
			}
			want := raw
			var body io.Reader = resp.Body
			if test.gzip {
				reader, err := gzip.NewReader(body)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				body = reader
			}
			if test.byteRange != "" {
				want = raw[:5]
				if resp.Code != http.StatusPartialContent {
					t.Fatalf("range status = %d", resp.Code)
				}
			}
			got, err := io.ReadAll(body)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("body differs from original asset: %v", err)
			}
		})
	}
}

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
