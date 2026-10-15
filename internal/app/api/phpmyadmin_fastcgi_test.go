package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFastCGIRequestStopsWhenRequestContextIsCanceled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err = fastCGIRequestContextLimited(ctx, "tcp", listener.Addr().String(), nil, nil, 128)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request error = %v, want context deadline", err)
	}
	<-serverDone
}

func TestFastCGIRequestRejectsOutputAboveConfiguredLimit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		for {
			typeID, body, err := readFastCGIRecord(conn)
			if err != nil {
				serverDone <- err
				return
			}
			if typeID == fcgiStdin && len(body) == 0 {
				break
			}
		}
		if err := writeFastCGIRecord(conn, fcgiStdout, []byte("12345")); err != nil {
			serverDone <- err
			return
		}
		serverDone <- writeFastCGIRecord(conn, fcgiEnd, nil)
	}()

	_, _, err = fastCGIRequestContextLimited(context.Background(), "tcp", listener.Addr().String(), nil, nil, 4)
	if err == nil || !strings.Contains(err.Error(), "configured limit") {
		t.Fatalf("request error = %v, want output limit error", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake FastCGI server: %v", err)
	}
}

func TestRewritePHPMyAdminBodyRemovesFrameProtection(t *testing.T) {
	body := []byte(`<!doctype html>
<html>
<head>
<style id="cfs-style">html{display: none;}</style>
<script data-cfasync="false" type="text/javascript" src="js/dist/cross_framing_protection.js?v=5.2.1deb3"></script>
<link href="/phpmyadmin/themes/pmahomme/css/theme.css">
</head>
<body><a href="/phpmyadmin/server_databases.php">antimage</a></body>
</html>`)
	status := phpMyAdminResponse{Path: "/phpmyadmin/", Port: 8080}

	rewritten := string(rewritePHPMyAdminBody(body, status, phpMyAdminEmbedPath))

	if strings.Contains(rewritten, "cfs-style") {
		t.Fatalf("expected cfs-style to be removed, got %s", rewritten)
	}
	if strings.Contains(rewritten, "cross_framing_protection") {
		t.Fatalf("expected cross_framing_protection script to be removed, got %s", rewritten)
	}
	if !strings.Contains(rewritten, phpMyAdminEmbedPath+"themes/pmahomme/css/theme.css") {
		t.Fatalf("expected phpMyAdmin paths to be rewritten, got %s", rewritten)
	}
}

func TestPHPMyAdminFastCGIResponseSetsEmbedSecurityHeaders(t *testing.T) {
	response := httptest.NewRecorder()
	status := phpMyAdminResponse{Path: "/phpmyadmin/", Port: 8080}
	if err := writePHPMyAdminFastCGIResponse(response, []byte("Content-Type: text/html\r\n\r\n<html></html>"), status); err != nil {
		t.Fatal(err)
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Fatalf("missing sandbox CSP: %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("missing nosniff header: %q", got)
	}
}

func TestPHPMyAdminEnvValueReadsAntiMageEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("MYSQL_ROOT_PASSWORD = \"root-pass\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_ENV_FILE", path)
	t.Setenv("MYSQL_ROOT_PASSWORD", "")

	if got := phpMyAdminEnvValue("MYSQL_ROOT_PASSWORD"); got != "root-pass" {
		t.Fatalf("expected root password from env file, got %q", got)
	}
}

func TestPHPMyAdminRecoverySessionCarriesProxyState(t *testing.T) {
	now := time.Unix(1_750_000_000, 0)
	want := phpMyAdminResponse{Path: "/db-tools/", Port: 9090}
	token := signPHPMyAdminEmbedSession("admin", want, now.Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, phpMyAdminEmbedPath+"index.php", nil)
	req.AddCookie(&http.Cookie{Name: phpMyAdminEmbedCookie, Value: token})

	got, ok := phpMyAdminProxySession(req, now)
	if !ok {
		t.Fatal("expected recovery session to be accepted without database access")
	}
	if got.Path != want.Path || got.Port != want.Port || !got.Enabled {
		t.Fatalf("unexpected proxy state: %+v", got)
	}
	if _, ok := phpMyAdminProxySession(req, now.Add(2*time.Hour)); ok {
		t.Fatal("expected expired recovery session to be rejected")
	}
}

func TestPHPMyAdminPHPDirectivesBoundMemoryAndExecution(t *testing.T) {
	directives := strings.Join(phpMyAdminUploadPHPDirectives(), "\n")
	for _, directive := range []string{
		"upload_max_filesize=1024M",
		"post_max_size=1024M",
		"memory_limit=512M",
		"max_execution_time=600",
		"max_input_time=600",
	} {
		if !strings.Contains(directives, directive) {
			t.Fatalf("missing directive %q in %q", directive, directives)
		}
	}
	if strings.Contains(directives, "memory_limit=4096M") || strings.Contains(directives, "max_execution_time=0") || strings.Contains(directives, "max_input_time=0") {
		t.Fatalf("phpMyAdmin resource limits are still unbounded: %q", directives)
	}
}
