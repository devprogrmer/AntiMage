package networkintelligence

import (
	"bytes"
	_ "embed"
	"time"
)

//go:embed application_catalog.json
var applicationCatalogue []byte

// LoadDefaultApplicationDetector uses the same bounded/versioned loader as
// administrator catalogues. Expired built-ins fail closed to protocol-only data.
func LoadDefaultApplicationDetector(now time.Time) (*ApplicationDetector, error) {
	return LoadApplicationDetector(bytes.NewReader(applicationCatalogue), now)
}
