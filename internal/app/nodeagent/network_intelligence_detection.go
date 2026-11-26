package nodeagent

import (
	"os"
	"strings"
	"time"

	ni "github.com/antimage/antimage/internal/app/networkintelligence"
)

// Signatures are administrator-supplied metadata and are validated before use.
// No destination ranges or application labels are inferred from common ports.
func loadNetworkApplicationDetector(path string) (*ni.ApplicationDetector, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return ni.LoadApplicationDetector(file, time.Now().UTC())
}
