package nodeagent

import "github.com/antimage/antimage/internal/platform/usagereflection"

// The controller publishes one cumulative reflection receipt per (root family,
// user) and only rewrites it when that user's reflected usage advances. A
// decoded receipt is a pure function of the encoded string, so re-decoding an
// unchanged receipt on every 100-500ms quota tick is pure allocation churn: at
// AntiMage-s3 scale the durable reflection check was 92% of a quota tick's
// allocated bytes. The production profile identified localUsageReflectionV2Locked
// and usagereflection.Decode as the top two allocation sites for exactly this
// reason.
//
// The cache is keyed by the exact encoded receipt, so a served entry is always
// the decode of the bytes the caller passed. It is scoped to one ledger object
// and dropped whenever that object is replaced, which bounds the lifetime of any
// entry to the ledger revision that referenced it. Decoded watermarks are only
// ever read by the callers in this package, so sharing one value is safe.
const (
	localUsageReceiptCacheMaxEntries = 4096
	localUsageReceiptCacheMaxBytes   = 4 << 20
	localUsageReceiptCacheMaxReceipt = 64 << 10
)

type localUsageReceiptCache struct {
	ledger  *localUsageLedgerV2
	entries map[string]usagereflection.Watermark
	bytes   int
}

// decodeLocalUsageReceiptLocked returns the validated watermark for raw,
// reusing a previously decoded value when the same receipt string recurs. The
// caller must hold s.localUsageMu.
func (s *Server) decodeLocalUsageReceiptLocked(raw string) (usagereflection.Watermark, error) {
	cache := s.localUsageReceiptCache
	if cache == nil || cache.ledger != s.localUsageV2 {
		cache = &localUsageReceiptCache{ledger: s.localUsageV2}
		s.localUsageReceiptCache = cache
	}
	if w, ok := cache.entries[raw]; ok {
		return w, nil
	}
	w, err := usagereflection.Decode(raw)
	if err != nil {
		return w, err
	}
	// Oversized receipts, or a cache that is already at its bound, are decoded
	// without being retained. Correctness never depends on a cache hit.
	if len(raw) <= localUsageReceiptCacheMaxReceipt && len(cache.entries) < localUsageReceiptCacheMaxEntries {
		if cache.bytes+len(raw) > localUsageReceiptCacheMaxBytes {
			cache.entries = map[string]usagereflection.Watermark{}
			cache.bytes = 0
		}
		if cache.entries == nil {
			cache.entries = make(map[string]usagereflection.Watermark)
		}
		cache.entries[raw] = w
		cache.bytes += len(raw)
	}
	return w, nil
}
