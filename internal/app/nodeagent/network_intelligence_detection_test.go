package nodeagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkIntelligenceSignatureLoadingFailsClosed(t *testing.T) {
	if detector, err := loadNetworkApplicationDetector(""); detector != nil || err != nil {
		t.Fatal(detector, err)
	}
	path := filepath.Join(t.TempDir(), "signatures.json")
	if detector, err := loadNetworkApplicationDetector(path); detector != nil || err == nil {
		t.Fatal(detector, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	if detector, err := loadNetworkApplicationDetector(path); detector != nil || err == nil {
		t.Fatal(detector, err)
	}
}
