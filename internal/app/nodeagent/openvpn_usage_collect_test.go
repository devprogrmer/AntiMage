package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestOpenVPNUsageCollectAndAck(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	tag := "openvpn-main"

	root := filepath.Join(
		server.cfg.DataDir,
		"openvpn",
		openVPNRuntimeDirName(tag),
	)

	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}

	statusPath := filepath.Join(root, "status.tsv")

	usageConfigRaw, err := json.Marshal(
		openVPNUsageRuntimeConfig{
			InboundTag: tag,
			StatusFile: statusPath,
			Users: map[string]int64{
				"alice": 42,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(root, "usage-helper.json"),
		usageConfigRaw,
		0600,
	); err != nil {
		t.Fatal(err)
	}

	writeStatus := func(received, sent uint64) {
		t.Helper()

		raw := "HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\n" +
			"CLIENT_LIST\talice\t203.0.113.10:54321\t10.66.0.10\t\t" +
			fmtUint(received) + "\t" +
			fmtUint(sent) +
			"\tnow\t123\talice\t7\n"

		if err := os.WriteFile(
			statusPath,
			[]byte(raw),
			0600,
		); err != nil {
			t.Fatal(err)
		}
	}

	server.mu.Lock()
	server.openVPNRuntimes[tag] = &openVPNProcess{}
	server.mu.Unlock()

	writeStatus(1200, 3400)

	first, err := server.CollectUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{Reset_: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	if first.GetBatchId() == "" {
		t.Fatal("expected batch id")
	}

	if len(first.GetStats()) != 1 {
		t.Fatalf(
			"expected 1 usage sample, got %d",
			len(first.GetStats()),
		)
	}

	if got := first.GetStats()[0].GetUid(); got != "openvpn:42" {
		t.Fatalf("unexpected uid: %q", got)
	}

	if got := first.GetStats()[0].GetValue(); got != 4600 {
		t.Fatalf("unexpected first usage: %d", got)
	}

	if got := first.GetStats()[0].GetInboundTag(); got != tag {
		t.Fatalf("unexpected inbound tag: %q", got)
	}

	// Before ACK the exact same batch must be replayed.
	repeated, err := server.CollectUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{Reset_: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	if repeated.GetBatchId() != first.GetBatchId() {
		t.Fatalf(
			"pending batch changed: %q != %q",
			repeated.GetBatchId(),
			first.GetBatchId(),
		)
	}

	if repeated.GetStats()[0].GetValue() != 4600 {
		t.Fatalf(
			"pending usage changed: %d",
			repeated.GetStats()[0].GetValue(),
		)
	}

	ack, err := server.AckUserUsage(
		context.Background(),
		&nodev1.AckUsageRequest{
			BatchId: first.GetBatchId(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if !ack.GetAcknowledged() {
		t.Fatal("expected batch acknowledgment")
	}

	// Total changes from 4600 -> 5400, so only 800 is new.
	writeStatus(1500, 3900)

	second, err := server.CollectUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{Reset_: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(second.GetStats()) != 1 {
		t.Fatalf(
			"expected 1 second usage sample, got %d",
			len(second.GetStats()),
		)
	}

	if got := second.GetStats()[0].GetValue(); got != 800 {
		t.Fatalf("expected delta 800, got %d", got)
	}
}

func TestOpenVPNUsageZeroDeltaReportsOnline(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	tag := "online-test"

	root := filepath.Join(
		server.cfg.DataDir,
		"openvpn",
		openVPNRuntimeDirName(tag),
	)

	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}

	statusPath := filepath.Join(root, "status.tsv")

	rawConfig, err := json.Marshal(
		openVPNUsageRuntimeConfig{
			InboundTag: tag,
			StatusFile: statusPath,
			Users: map[string]int64{
				"alice": 42,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(root, "usage-helper.json"),
		rawConfig,
		0600,
	); err != nil {
		t.Fatal(err)
	}

	status := "" +
		"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\n" +
		"CLIENT_LIST\talice\t203.0.113.10:54321\t10.66.0.10\t\t100\t200\tnow\t123\talice\t7\n"

	if err := os.WriteFile(
		statusPath,
		[]byte(status),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	server.mu.Lock()
	server.openVPNRuntimes[tag] = &openVPNProcess{}
	server.mu.Unlock()

	first, err := server.CollectUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{Reset_: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := server.AckUserUsage(
		context.Background(),
		&nodev1.AckUsageRequest{
			BatchId: first.GetBatchId(),
		},
	)
	if err != nil || !ack.GetAcknowledged() {
		t.Fatalf("ack failed: %+v %v", ack, err)
	}

	second, err := server.CollectUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{Reset_: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(second.GetStats()) != 1 {
		t.Fatalf(
			"expected online sample, got %d",
			len(second.GetStats()),
		)
	}

	if got := second.GetStats()[0].GetUid(); got != "online:openvpn:42" {
		t.Fatalf("unexpected online uid: %q", got)
	}

	if got := second.GetStats()[0].GetValue(); got != 0 {
		t.Fatalf("expected zero online value, got %d", got)
	}
}

func fmtUint(value uint64) string {
	return strconv.FormatUint(value, 10)
}
