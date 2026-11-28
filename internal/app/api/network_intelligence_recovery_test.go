package api

import (
	"context"
	"encoding/json"
	backupapp "github.com/antimage/antimage/internal/app/backup"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Every phase is a fresh process, as required by the process-scoped DB pool.
func TestNetworkIntelligencePanelRestartBackupAndFlagRollback(t *testing.T) {
	if dir := os.Getenv("ANTIMAGE_NI_RECOVERY_PANEL_DIR"); dir != "" {
		runPanelRecoveryPhase(t, dir, os.Getenv("ANTIMAGE_NI_RECOVERY_PHASE"))
		return
	}
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"prepare", "restore", "disabled", "reenable"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, executable, "-test.run", "^TestNetworkIntelligencePanelRestartBackupAndFlagRollback$", "-test.v")
		cmd.Env = append(os.Environ(), "ANTIMAGE_NI_RECOVERY_PANEL_DIR="+dir, "ANTIMAGE_NI_RECOVERY_PHASE="+phase)
		output, err := cmd.CombinedOutput()
		cancel()
		t.Logf("phase %s: %s", phase, output)
		if err != nil {
			t.Fatalf("panel process %s failed: %v", phase, err)
		}
	}
	t.Log("PASS: process exits/restarts; backup/restore, flags-off rollback, re-enable, accounting and scoped history preserved")
}

func runPanelRecoveryPhase(t *testing.T, dir, phase string) {
	t.Helper()
	t.Setenv("ANTIMAGE_DATA_DIR", filepath.Join(dir, "data"))
	path := filepath.Join(dir, "panel.sqlite")
	url := "sqlite:///" + filepath.ToSlash(path)
	configuration := filepath.Join(dir, "panel.env")
	archivePath := filepath.Join(dir, "rollback.backup")
	const original = "ANTIMAGE_NETWORK_INTELLIGENCE_ENABLED=false\nANTIMAGE_NETWORK_HISTORY_PERSISTENCE=false\n"
	s, err := New(Config{Database: url, CertificateBase: filepath.Join(dir, "certificates"), NetworkIntelligenceEnabled: phase != "disabled", NetworkHistoryPersistence: phase != "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	service := backupapp.NewService(s.db, "sqlite", url, backupapp.WithFileRoots([]backupapp.FileRoot{{ArchiveName: "antimage_env", Path: configuration}}), backupapp.WithRestoreJournalDir(filepath.Join(dir, "journal")))
	switch phase {
	case "prepare":
		if err := os.WriteFile(configuration, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO users (id,username,used_traffic) VALUES (42,'recovery-user',123456),(43,'other-user',5678)`); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		value := 42.5
		repo := &ni.HistoryRepository{DB: s.db}
		for _, user := range []int64{42, 43} {
			if err := repo.Add(ctx, ni.DurableQualityPoint{NodeID: 7, UserID: user, ObservedAt: now, ACKRTTMilliseconds: &value}, now); err != nil {
				t.Fatal(err)
			}
		}
		archive, err := service.Export(ctx, backupapp.ScopeFull)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(archive.Path)
		raw, err := os.ReadFile(archive.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`DELETE FROM network_quality_history; UPDATE users SET used_traffic=999999 WHERE id=42`); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configuration, []byte("ANTIMAGE_NETWORK_INTELLIGENCE_ENABLED=true\n"), 0600); err != nil {
			t.Fatal(err)
		}
	case "restore":
		if _, err := service.Import(ctx, archivePath); err != nil {
			t.Fatal(err)
		}
	case "disabled":
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/network-intelligence/history?node_id=7&user_id=42", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("disabled rollback exposed module: %d", response.Code)
		}
		var usage int64
		if err := s.db.QueryRow("SELECT used_traffic FROM users WHERE id=42").Scan(&usage); err != nil || usage != 123456 {
			t.Fatalf("accounting not restored: %d %v", usage, err)
		}
		content, err := os.ReadFile(configuration)
		if err != nil || string(content) != original {
			t.Fatalf("flags not restored: %q %v", content, err)
		}
	case "reenable":
		request := httptest.NewRequest(http.MethodGet, "/api/network-intelligence/history?node_id=7&user_id=42", nil)
		request = request.WithContext(context.WithValue(request.Context(), adminContextKey, adminPrincipal{Role: "full_access"}))
		response := httptest.NewRecorder()
		s.handleNetworkIntelligenceHistory(response, request)
		var history networkHistoryResponse
		if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil || response.Code != 200 || history.Storage != "durable" || len(history.Points) != 1 {
			t.Fatalf("history lost: %d %s %v", response.Code, response.Body, err)
		}
		var point struct {
			UserID int64    `json:"user_id"`
			RTT    *float64 `json:"ack_rtt_ms"`
		}
		if err := json.Unmarshal(history.Points[0], &point); err != nil || point.UserID != 42 || point.RTT == nil || *point.RTT != 42.5 {
			t.Fatal("cross-user or changed restored fixture history", point, err)
		}
	default:
		t.Fatal("invalid recovery phase")
	}
}
