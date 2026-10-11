package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func nativePanelEffectiveUsage(t *testing.T, stateDir string) uint64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, "native-panel-receipt.json"))
	if err != nil {
		t.Fatalf("read native panel usage receipt: %v", err)
	}
	var receipt struct {
		EffectiveTotal uint64 `json:"effective_total"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatalf("decode native panel usage receipt: %v", err)
	}
	return receipt.EffectiveTotal
}
