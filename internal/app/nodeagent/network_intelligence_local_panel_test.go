package nodeagent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
	"github.com/antimage/antimage/internal/app/migrations"
	ni "github.com/antimage/antimage/internal/app/networkintelligence"
	nodeapp "github.com/antimage/antimage/internal/app/node"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "modernc.org/sqlite"
)

// This portable fixture observes actual application socket datagrams. IP/TCP
// headers are test adapters, not raw capture: no synthetic TCP ACK timing is
// supplied. The separate Linux fixture remains required for WireGuard/capture.
func TestNetworkIntelligenceLocalTrafficToAuthenticatedPanel(t *testing.T) {
	dashboard := os.Getenv("ANTIMAGE_NI_LOCAL_DASHBOARD")
	if dashboard == "" {
		t.Skip("opt-in local browser integration; requires built dashboard")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.sqlite")
	agent := New(Config{NetworkIntelligenceEnabled: true, DataDir: filepath.Join(dir, "agent")})
	defer agent.stopNetworkIntelligence()
	udpServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpServer.Close()
	for i, owner := range []int64{42, 42, 43} {
		addr := net.IPv4(127, 0, 0, byte(i+1))
		binding := ni.Binding{NodeID: 7, UserID: owner, Interface: []string{"lab-session-a", "lab-session-b", "lab-session-c"}[i], Address: netip.MustParseAddr(addr.String()), Session: "local-private-session", Generation: 1, Since: time.Now().UTC().Add(-time.Second)}
		if err := agent.networkIntelligenceTracker.Bind(binding); err != nil {
			t.Fatal(err)
		}
		capture, err := ni.NewSessionCapture(agent.networkIntelligenceTracker, []ni.Binding{binding}, 16)
		if err != nil {
			t.Fatal(err)
		}
		defer capture.Stop()
		localPanelUDP(t, capture, addr, i, udpServer)
		if i == 1 {
			localPanelTCP(t, capture, addr)
		}
	}
	cert, key, err := nodeapp.GenerateCertificate("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(cert)) {
		t.Fatal("fixture CA")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	rpc := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots})))
	defer rpc.Stop()
	nodev1.RegisterNodeControlServiceServer(rpc, agent)
	nodev1.RegisterNodeRuntimeServiceServer(rpc, agent)
	go func() { _ = rpc.Serve(listener) }()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.RunMigrations(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO tls (certificate,`key`) VALUES (?,?)", cert, key); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := db.Exec(`INSERT INTO nodes (id,name,address,port,api_port,status,certificate,certificate_key,xray_config_mode,usage_coefficient) VALUES (7,'local-traffic-node','127.0.0.1',?,?,'connected',?,?,'custom',1)`, port, port, cert, key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id,username) VALUES (42,'local-multi-session'),(43,'local-other-user'); INSERT INTO user_online_ips (node_id,user_id,protocol,ip,last_seen_at) VALUES (7,42,'wireguard','127.0.0.1',CURRENT_TIMESTAMP),(7,42,'wireguard','127.0.0.2',CURRENT_TIMESTAMP),(7,43,'wireguard','127.0.0.3',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	hash, err := adminapp.HashPassword("local-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	permissions, _ := json.Marshal(adminapp.RoleDefaultPermissions(adminapp.RoleFullAccess))
	if _, err := db.Exec(`INSERT INTO admins (username,hashed_password,role,permissions,status,subscription_settings,users_usage,lifetime_usage,created_traffic,deleted_users_usage) VALUES ('local-admin',?,'full_access',?,'active','{}',0,0,0,0)`, hash, string(permissions)); err != nil {
		t.Fatal(err)
	}
	restricted, _ := json.Marshal(adminapp.RoleDefaultPermissions(adminapp.RoleSudo))
	if _, err := db.Exec(`INSERT INTO admins (username,hashed_password,role,permissions,status,subscription_settings,users_usage,lifetime_usage,created_traffic,deleted_users_usage) VALUES ('local-restricted',?,'sudo',?,'active','{}',0,0,0,0)`, hash, string(restricted)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "go", "test", "./internal/app/api", "-run", "^TestNetworkLiveBrowserAgainstLocalNode$", "-count=1", "-v")
	cmd.Dir = filepath.Join("..", "..", "..")
	cmd.Env = append(os.Environ(), "ANTIMAGE_NI_LOCAL_DB="+path, "ANTIMAGE_NI_LOCAL_DASHBOARD="+dashboard)
	output, err := cmd.CombinedOutput()
	t.Logf("local panel/browser: %s", output)
	if err != nil {
		t.Fatalf("local full-stack browser failed: %v", err)
	}
}

func localPanelUDP(t *testing.T, c *ni.SessionCapture, source net.IP, index int, server *net.UDPConn) {
	t.Helper()
	client, err := net.DialUDP("udp4", &net.UDPAddr{IP: source}, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server.SetDeadline(time.Now().Add(5 * time.Second))
	client.SetDeadline(time.Now().Add(5 * time.Second))
	for sample := 0; sample < 2; sample++ {
		request := make([]byte, 20)
		binary.BigEndian.PutUint16(request, 1)
		binary.BigEndian.PutUint32(request[4:], 0x2112a442)
		request[8] = byte(sample + 1)
		if index == 2 {
			request = []byte("unclassified-private-body")
		}
		if _, err := client.Write(request); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 128)
		n, peer, err := server.ReadFromUDP(buf)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC()
		localPanelPacket(t, c, buf[:n], peer.AddrPort(), server.LocalAddr().(*net.UDPAddr).AddrPort(), false, at, false)
		delay := 20 * time.Millisecond
		if index == 0 {
			delay = time.Duration(250+sample*100) * time.Millisecond
		}
		time.Sleep(delay)
		reply := append([]byte(nil), request...)
		if index != 2 {
			binary.BigEndian.PutUint16(reply, 0x101)
		}
		if _, err := server.WriteToUDP(reply, peer); err != nil {
			t.Fatal(err)
		}
		n, err = client.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		localPanelPacket(t, c, buf[:n], server.LocalAddr().(*net.UDPAddr).AddrPort(), client.LocalAddr().(*net.UDPAddr).AddrPort(), true, time.Now().UTC(), false)
	}
}

func localPanelTCP(t *testing.T, c *ni.SessionCapture, source net.IP) {
	t.Helper()
	server, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.DialTCP("tcp4", &net.TCPAddr{IP: source}, server.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	peer, err := server.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Write([]byte("private-tcp-body")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := peer.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	localPanelPacket(t, c, buf[:n], client.LocalAddr().(*net.TCPAddr).AddrPort(), server.Addr().(*net.TCPAddr).AddrPort(), false, time.Now().UTC(), true)
}

func localPanelPacket(t *testing.T, c *ni.SessionCapture, body []byte, source, destination netip.AddrPort, outgoing bool, at time.Time, tcp bool) {
	t.Helper()
	header := 28
	protocol := byte(17)
	if tcp {
		header = 40
		protocol = 6
	}
	packet := make([]byte, header+len(body))
	packet[0] = 0x45
	packet[9] = protocol
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	a, b := source.Addr().As4(), destination.Addr().As4()
	copy(packet[12:16], a[:])
	copy(packet[16:20], b[:])
	binary.BigEndian.PutUint16(packet[20:], source.Port())
	binary.BigEndian.PutUint16(packet[22:], destination.Port())
	if tcp {
		packet[32] = 0x50
		packet[33] = 8
		binary.BigEndian.PutUint32(packet[24:], 100)
	} else {
		binary.BigEndian.PutUint16(packet[24:], uint16(8+len(body)))
	}
	copy(packet[header:], body)
	metadata, err := ni.DecodePacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Consume(ni.CapturedPacket{Metadata: metadata, At: at, Outgoing: outgoing}) {
		t.Fatal("local socket observation rejected")
	}
}
