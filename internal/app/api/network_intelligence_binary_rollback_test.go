package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

// Executes actual gateway binaries on a disposable database. This validates
// binary rollback compatibility, not Linux service-manager/deployment scripts.
func TestNetworkIntelligencePanelBinaryVersionRollback(t *testing.T) {
	baseline, current := os.Getenv("ANTIMAGE_NI_BASELINE_PANEL"), os.Getenv("ANTIMAGE_NI_CURRENT_PANEL")
	if baseline == "" || current == "" {
		t.Skip("requires current and archived baseline panel binaries")
	}
	dir := t.TempDir()
	t.Setenv("ANTIMAGE_DATA_DIR", filepath.Join(dir, "data"))
	url := "sqlite:///" + filepath.ToSlash(filepath.Join(dir, "panel.sqlite"))
	s, err := New(Config{Database: url, CertificateBase: filepath.Join(dir, "certificates")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO users (id,username,used_traffic) VALUES (42,'binary-rollback-user',123456)`); err != nil {
		t.Fatal(err)
	}
	hash, err := adminapp.HashPassword("rollback-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	permissions, _ := json.Marshal(adminapp.RoleDefaultPermissions(adminapp.RoleFullAccess))
	if _, err := s.db.Exec(`INSERT INTO admins (username,hashed_password,role,permissions,status,subscription_settings,users_usage,lifetime_usage,created_traffic,deleted_users_usage) VALUES ('rollback-admin',?,'full_access',?,'active','{}',0,0,0,0)`, hash, string(permissions)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	value := 42.5
	if err := (&ni.HistoryRepository{DB: s.db}).Add(context.Background(), ni.DurableQualityPoint{NodeID: 7, UserID: 42, ObservedAt: now, ACKRTTMilliseconds: &value}, now); err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + reservation.Addr().String()
	address := reservation.Addr().String()
	reservation.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	var active *exec.Cmd
	var log bytes.Buffer
	stop := func() {
		if active != nil {
			_ = active.Process.Kill()
			_ = active.Wait()
			active = nil
		}
	}
	defer stop()
	for _, phase := range []struct {
		name, binary string
		enabled      bool
	}{{"current", current, true}, {"baseline-flags-off", baseline, false}, {"baseline-history", baseline, true}, {"current-reupgrade", current, true}} {
		stop()
		log.Reset()
		active = exec.Command(phase.binary)
		active.Dir = dir
		// An explicit environment prevents inherited production endpoints, hooks
		// or credentials from being used by background workers in this fixture.
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "TEMP", "TMP", "PATH"} {
			if value := os.Getenv(key); value != "" {
				active.Env = append(active.Env, key+"="+value)
			}
		}
		active.Env = append(active.Env, "ANTIMAGE_ENV_FILE="+filepath.Join(dir, "empty.env"), "ANTIMAGE_DATA_DIR="+filepath.Join(dir, "data"), "ANTIMAGE_CERT_BASE="+filepath.Join(dir, "certificates"), "ANTIMAGE_EXTERNAL_APPS_BASE="+filepath.Join(dir, "external-apps"), "SQLALCHEMY_DATABASE_URL="+url, "ANTIMAGE_GATEWAY_ADDR="+address, "ANTIMAGE_NETWORK_INTELLIGENCE_ENABLED="+strconv.FormatBool(phase.enabled), "ANTIMAGE_NETWORK_HISTORY_PERSISTENCE="+strconv.FormatBool(phase.enabled), "ANTIMAGE_NETWORK_OPTIMIZATION_ENABLED=false")
		active.Stdout = &log
		active.Stderr = &log
		if err := active.Start(); err != nil {
			t.Fatal(err)
		}
		ready := false
		for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
			response, err := client.Get(origin + "/api/auth/session")
			if err == nil {
				response.Body.Close()
				ready = true
				break
			}
			time.Sleep(40 * time.Millisecond)
		}
		if !ready {
			stop()
			t.Fatalf("%s failed startup: %s", phase.name, log.String())
		}
		if !phase.enabled {
			response, err := client.Get(origin + "/api/network-intelligence/history?node_id=7&user_id=42")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 404 {
				t.Fatalf("flags-off route exposed: %d", response.StatusCode)
			}
		} else {
			response, err := client.Post(origin+"/api/admin/token", "application/json", strings.NewReader(`{"username":"rollback-admin","password":"rollback-fixture-password"}`))
			if err != nil {
				t.Fatal(err)
			}
			var token struct {
				Token string `json:"access_token"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&token)
			response.Body.Close()
			if response.StatusCode != 200 || decodeErr != nil || token.Token == "" {
				t.Fatalf("%s login failed: %d %v", phase.name, response.StatusCode, decodeErr)
			}
			request, _ := http.NewRequest(http.MethodGet, origin+"/api/network-intelligence/history?node_id=7&user_id=42", nil)
			request.Header.Set("Authorization", "Bearer "+token.Token)
			response, err = client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var history networkHistoryResponse
			decodeErr = json.NewDecoder(response.Body).Decode(&history)
			response.Body.Close()
			if response.StatusCode != 200 || decodeErr != nil || history.Storage != "durable" || len(history.Points) != 1 {
				t.Fatalf("%s lost compatible history: %d %+v %v", phase.name, response.StatusCode, history, decodeErr)
			}
			var point struct {
				Value *float64 `json:"ack_rtt_ms"`
			}
			if err := json.Unmarshal(history.Points[0], &point); err != nil || point.Value == nil || *point.Value != value {
				t.Fatal("stored fixture changed across binary rollback", err)
			}
		}
		t.Logf("phase %s passed", phase.name)
	}
	stop()
	var usage int64
	if err := s.db.QueryRow("SELECT used_traffic FROM users WHERE id=42").Scan(&usage); err != nil || usage != 123456 {
		t.Fatalf("binary rollback changed accounting: %d %v", usage, err)
	}
	t.Log("PASS: actual current panel → baseline (flags off) → baseline (durable history on) → current; migrated DB/history/accounting preserved")
}
