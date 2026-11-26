package networkintelligence

import (
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"time"
)

const maxSignatureBytes = 1 << 20
const maxSignatureRules = 4096

// SignatureDatabase is supplied explicitly by an administrator-controlled source.
// Prefix ownership and application exclusivity must be independently verified.
type SignatureDatabase struct {
	Version     int               `json:"version"`
	Revision    string            `json:"revision"`
	PublishedAt time.Time         `json:"published_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Rules       []ApplicationRule `json:"rules"`
}

type ApplicationRule struct {
	Application       string   `json:"application"`
	DestinationPrefix string   `json:"destination_prefix"`
	Protocol          string   `json:"protocol"`
	Ports             []uint16 `json:"ports"`
	Source            string   `json:"source"`
}

type compiledApplicationRule struct {
	rule   ApplicationRule
	prefix netip.Prefix
}

type ApplicationDetector struct {
	revision               string
	publishedAt, expiresAt time.Time
	rules                  []compiledApplicationRule
}

type ApplicationDetection struct {
	Application string   `json:"application"`
	Confidence  string   `json:"confidence"`
	Revision    string   `json:"revision"`
	Evidence    []string `json:"evidence"`
}

// LoadApplicationDetector validates an immutable, bounded signature snapshot.
func LoadApplicationDetector(reader io.Reader, now time.Time) (*ApplicationDetector, error) {
	if reader == nil || now.IsZero() {
		return nil, errors.New("signature reader and current time required")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxSignatureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSignatureBytes {
		return nil, errors.New("signature database exceeds byte limit")
	}
	var database SignatureDatabase
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&database); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing signature database content")
	}
	if database.Version != 1 || strings.TrimSpace(database.Revision) == "" || len(database.Revision) > 128 || database.PublishedAt.IsZero() || database.PublishedAt.After(now) || !database.ExpiresAt.After(now) || !database.ExpiresAt.After(database.PublishedAt) || len(database.Rules) == 0 || len(database.Rules) > maxSignatureRules {
		return nil, errors.New("invalid signature version, revision, validity or rule count")
	}
	detector := &ApplicationDetector{revision: database.Revision, publishedAt: database.PublishedAt, expiresAt: database.ExpiresAt}
	for _, rule := range database.Rules {
		prefix, err := netip.ParsePrefix(rule.DestinationPrefix)
		if err != nil || prefix != prefix.Masked() || prefix.Bits() == 0 || prefix.Addr().Is4In6() || !prefix.Addr().IsGlobalUnicast() || strings.TrimSpace(rule.Application) == "" || len(rule.Application) > 128 || strings.TrimSpace(rule.Source) == "" || len(rule.Source) > 2048 || (rule.Protocol != "tcp" && rule.Protocol != "udp") || len(rule.Ports) == 0 || len(rule.Ports) > 128 {
			return nil, errors.New("invalid application signature")
		}
		seen := make(map[uint16]bool, len(rule.Ports))
		for _, port := range rule.Ports {
			if port == 0 || seen[port] {
				return nil, errors.New("invalid or duplicate signature port")
			}
			seen[port] = true
		}
		detector.rules = append(detector.rules, compiledApplicationRule{rule, prefix})
	}
	return detector, nil
}

// Detect reports a probable application based on destination metadata. A match
// cannot establish the executable running on the client. Conflicting matches
// and expired databases return Unknown, including overlapping shared ranges.
func (d *ApplicationDetector) Detect(tuple Tuple, now time.Time) ApplicationDetection {
	result := ApplicationDetection{Application: "Unknown", Confidence: "unknown", Evidence: []string{}}
	if d == nil {
		return result
	}
	result.Revision = d.revision
	if now.IsZero() || now.Before(d.publishedAt) || !now.Before(d.expiresAt) {
		return result
	}
	application := ""
	for _, compiled := range d.rules {
		rule := compiled.rule
		if tuple.Protocol != rule.Protocol || !compiled.prefix.Contains(tuple.Destination.Addr()) {
			continue
		}
		for _, port := range rule.Ports {
			if tuple.Destination.Port() != port {
				continue
			}
			if application != "" && application != rule.Application {
				return ApplicationDetection{Application: "Unknown", Confidence: "unknown", Revision: d.revision, Evidence: []string{"conflicting destination signatures"}}
			}
			application = rule.Application
			result.Evidence = append(result.Evidence, rule.DestinationPrefix+" "+rule.Protocol+" destination port match; source "+rule.Source)
		}
	}
	if application != "" {
		result.Application, result.Confidence = application, "probable"
	}
	return result
}
