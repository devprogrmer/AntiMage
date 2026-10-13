package nodeagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// canonicalRuntimeConfigDigest ignores JSON object key ordering and whitespace
// while preserving array order and JSON number spelling. This prevents
// formatting-only Xray config updates from restarting a healthy core.
func canonicalRuntimeConfigDigest(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("multiple JSON values")
		}
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}
