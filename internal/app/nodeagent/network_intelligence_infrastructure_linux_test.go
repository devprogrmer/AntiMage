//go:build linux

package nodeagent

import (
	"strings"
	"testing"
)

func TestNetworkInfrastructureReadsKernelLoopbackState(t *testing.T) {
	_, interfaces, err := readNetworkInfrastructure()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range interfaces {
		if item.Name == "lo" {
			// Linux commonly reports "unknown" for loopback.
			if item.MTU == 0 || item.OperState == "" {
				t.Fatalf("missing kernel loopback metadata: %+v", item)
			}
			return
		}
	}
	t.Fatal("loopback interface absent")
}

func TestNetworkInfrastructureParsers(t *testing.T) {
	got, err := parseInfrastructureSNMP(strings.NewReader("Ip: Forwarding ReasmTimeout ReasmReqds ReasmOKs ReasmFails FragOKs FragFails FragCreates\nIp: 1 30 20 10 3 8 2 12\n"))
	if err != nil || got["ip.ReasmFails"] != 3 || got["ip.FragCreates"] != 12 {
		t.Fatal(got, err)
	}
	if _, ok := got["ip.ReasmTimeout"]; ok {
		t.Fatal("configuration gauge treated as counter")
	}
	got, err = parseInfrastructureSoftnet(strings.NewReader("000000ff 00000002 00000003\n00000001 00000004 00000005\n"))
	if err != nil || got["softnet.dropped"] != 6 || got["softnet.time_squeeze"] != 8 {
		t.Fatal(got, err)
	}
	for _, raw := range []string{"", "Ip: ReasmFails\nIp: invalid\n", "Ip: ReasmFails\n"} {
		_, err := parseInfrastructureSNMP(strings.NewReader(raw))
		if raw != "" && err == nil {
			t.Fatal("accepted malformed row")
		}
	}
	if _, err := parseInfrastructureSoftnet(strings.NewReader("ff\n")); err == nil {
		t.Fatal("accepted short row")
	}
}
