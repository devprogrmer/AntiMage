package nodeagent

import (
	"testing"
)

// The healthy health-reporting path runs once per user per inbound per tick, so
// it must not allocate or mutate state when nothing is degraded. Once a domain
// has recorded a failure, a later success must still clear it and keep tracking
// subsequent failures, and the domains must stay independent.
func TestRecordLocalDomainHealthHealthyPathIsNoop(t *testing.T) {
	s := New(Config{})
	s.recordLocalDomainHealth("policy", "xray", nil)
	if s.localPolicyFailures != nil {
		t.Fatalf("healthy report created failure state: %v", s.localPolicyFailures)
	}
}

func TestRecordLocalDomainHealthClearsOnlyMatchingDomain(t *testing.T) {
	s := New(Config{})
	s.recordLocalDomainHealth("policy", "xray", errTestHealth("policy down"))
	s.recordLocalDomainHealth("accounting", "xray", errTestHealth("accounting down"))
	if len(s.localPolicyFailures) != 1 || len(s.localAccountingFailures) != 1 {
		t.Fatalf("failures not recorded: policy=%v accounting=%v", s.localPolicyFailures, s.localAccountingFailures)
	}

	s.recordLocalDomainHealth("policy", "xray", nil)
	if len(s.localPolicyFailures) != 0 {
		t.Fatalf("policy failure not cleared: %v", s.localPolicyFailures)
	}
	if len(s.localAccountingFailures) != 1 {
		t.Fatalf("unrelated accounting failure was cleared: %v", s.localAccountingFailures)
	}

	// A later failure in the cleared domain must be recorded again.
	s.recordLocalDomainHealth("policy", "xray", errTestHealth("policy down again"))
	if s.localPolicyFailures["policy:xray"] != "policy down again" {
		t.Fatalf("policy failure not re-recorded: %v", s.localPolicyFailures)
	}
}

type errTestHealth string

func (e errTestHealth) Error() string { return string(e) }
