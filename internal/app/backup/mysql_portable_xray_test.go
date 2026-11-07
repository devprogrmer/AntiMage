package backup

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func restoreTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func restoreTestStartXray(t *testing.T, binary string, config map[string]any, port int) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "xray.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", file)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = log.Close()
		if t.Failed() {
			diagnostic, _ := os.ReadFile(log.Name())
			t.Logf("Xray fixture log: %s", diagnostic)
		}
	})
	for range 100 {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return fmt.Sprintf("127.0.0.1:%d", port)
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("Xray did not listen on %d", port)
	return ""
}

// Keep a single TCP connection open across the restore and send fresh data
// through it afterwards. An already buffered response cannot pass this check.
func restoreTestVLESSStream(t *testing.T, binary, uuid string, inbound map[string]any) func() {
	t.Helper()
	origin, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = origin.Close() })
	go func() {
		for {
			conn, err := origin.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	port, proxyPort := restoreTestPort(t), restoreTestPort(t)
	raw, err := json.Marshal(inbound)
	if err != nil {
		t.Fatal(err)
	}
	var serverInbound map[string]any
	if err := json.Unmarshal(raw, &serverInbound); err != nil {
		t.Fatal(err)
	}
	serverInbound["listen"], serverInbound["port"] = "127.0.0.1", port
	serverInbound["settings"].(map[string]any)["clients"] = []any{map[string]any{"id": uuid}}
	physical := map[string]any{
		"log":      map[string]any{"loglevel": "debug"},
		"inbounds": []any{serverInbound},
		"outbounds": []any{map[string]any{
			"protocol": "freedom",
			"settings": map[string]any{
				"finalRules": []any{map[string]any{
					"action": "allow", "ip": []string{"127.0.0.1/32"}, "port": origin.Addr().(*net.TCPAddr).Port,
				}},
			},
		}},
	}
	restoreTestStartXray(t, binary, physical, port)
	clientConfig := map[string]any{
		"log": map[string]any{"loglevel": "debug"},
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": proxyPort, "protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1", "port": origin.Addr().(*net.TCPAddr).Port, "network": "tcp"},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": port,
				"users": []any{map[string]any{"id": uuid, "encryption": "none"}},
			}}},
		}},
	}
	address := restoreTestStartXray(t, binary, clientConfig, proxyPort)
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	verify := func(marker string) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, marker); err != nil {
			t.Fatalf("send VPN marker: %v", err)
		}
		reply := make([]byte, len(marker))
		if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != marker {
			t.Fatalf("VPN echo: %q, %v", reply, err)
		}
	}
	verify("before")
	return func() { verify("after") }
}
