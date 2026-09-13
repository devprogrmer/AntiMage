package api

import "testing"

func TestAttachLiveInboundSpeedsAddsMatchingTagsOnly(t *testing.T) {
	inbounds := []map[string]any{
		{"tag": "vless-main", "uplink": int64(10), "downlink": int64(20)},
		{"tag": "wg-edge", "uplink": int64(30), "downlink": int64(40)},
	}
	attachLiveInboundSpeeds(inbounds, map[string]liveInboundSpeed{
		"vless-main": {UploadSpeed: 1000, DownloadSpeed: 2000},
		"missing":    {UploadSpeed: 9, DownloadSpeed: 9},
	})

	if inbounds[0]["upload_speed"] != uint64(1000) || inbounds[0]["download_speed"] != uint64(2000) {
		t.Fatalf("expected live speeds on matching inbound, got %#v", inbounds[0])
	}
	if _, ok := inbounds[1]["upload_speed"]; ok {
		t.Fatalf("unexpected live speed on unmatched inbound: %#v", inbounds[1])
	}
}
