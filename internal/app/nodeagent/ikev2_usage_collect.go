package nodeagent

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

type ikev2RawChildSA struct {
	Name     string
	UniqueID string
	State    string
	BytesIn  uint64
	BytesOut uint64
}

type ikev2RawSA struct {
	ConnectionName string
	UniqueID       string
	State          string
	RemoteID       string
	RemoteEAPID    string
	RemoteHost     string
	RemoteVIPs     []string
	InitiatorSPI   string
	ResponderSPI   string
	Children       []ikev2RawChildSA
}

type ikev2UsageSample struct {
	UserID     int64    `json:"user_id"`
	InboundTag string   `json:"inbound_tag"`
	Value      uint64   `json:"value"`
	Online     bool     `json:"online"`
	IPs        []string `json:"ips,omitempty"`
}

type ikev2UsagePendingBatch struct {
	BatchID      string             `json:"batch_id"`
	Samples      []ikev2UsageSample `json:"samples"`
	NextBaseline map[string]uint64  `json:"next_baseline"`
}

type ikev2RawBlock struct {
	Name string
	Body string
}

func ikev2RawIsSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func ikev2RawMatchingBrace(
	raw string,
	open int,
) int {
	if open < 0 ||
		open >= len(raw) ||
		raw[open] != '{' {
		return -1
	}

	depth := 0

	for i := open; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

func ikev2RawNamedBlocks(
	raw string,
) []ikev2RawBlock {
	result := []ikev2RawBlock{}

	for i := 0; i < len(raw); {
		for i < len(raw) &&
			ikev2RawIsSpace(raw[i]) {
			i++
		}

		if i >= len(raw) {
			break
		}

		nameStart := i

		for i < len(raw) &&
			!ikev2RawIsSpace(raw[i]) &&
			raw[i] != '{' &&
			raw[i] != '}' {
			i++
		}

		name := strings.TrimSpace(
			raw[nameStart:i],
		)

		for i < len(raw) &&
			ikev2RawIsSpace(raw[i]) {
			i++
		}

		if name == "" ||
			i >= len(raw) ||
			raw[i] != '{' {
			for i < len(raw) &&
				!ikev2RawIsSpace(raw[i]) {
				i++
			}
			continue
		}

		end := ikev2RawMatchingBrace(raw, i)
		if end < 0 {
			break
		}

		result = append(
			result,
			ikev2RawBlock{
				Name: name,
				Body: raw[i+1 : end],
			},
		)

		i = end + 1
	}

	return result
}

func ikev2RawScalar(
	raw string,
	key string,
) string {
	needle := key + "="
	index := strings.Index(raw, needle)

	if index < 0 {
		return ""
	}

	start := index + len(needle)
	end := start

	for end < len(raw) {
		switch raw[end] {
		case ' ', '\t', '\n', '\r',
			'{', '}', '[', ']':
			return strings.TrimSpace(
				raw[start:end],
			)
		default:
			end++
		}
	}

	return strings.TrimSpace(raw[start:end])
}

func ikev2RawList(
	raw string,
	key string,
) []string {
	needle := key + "=["
	index := strings.Index(raw, needle)

	if index < 0 {
		return nil
	}

	start := index + len(needle)
	relativeEnd := strings.Index(
		raw[start:],
		"]",
	)
	if relativeEnd < 0 {
		return nil
	}

	body := raw[start : start+relativeEnd]

	values := strings.FieldsFunc(
		body,
		func(r rune) bool {
			switch r {
			case ' ', '\t', '\n', '\r', ',':
				return true
			default:
				return false
			}
		},
	)

	result := make([]string, 0, len(values))

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}

	return result
}

func ikev2RawUint(
	raw string,
	key string,
) uint64 {
	value := ikev2RawScalar(raw, key)
	if value == "" {
		return 0
	}

	parsed, _ := strconv.ParseUint(
		value,
		10,
		64,
	)

	return parsed
}

func parseIKEv2SwanctlRaw(
	raw string,
) []ikev2RawSA {
	const marker = "list-sa event {"

	result := []ikev2RawSA{}

	for offset := 0; offset < len(raw); {
		relative := strings.Index(
			raw[offset:],
			marker,
		)

		if relative < 0 {
			break
		}

		eventStart := offset + relative
		open := eventStart +
			strings.Index(raw[eventStart:], "{")

		if open < eventStart {
			break
		}

		end := ikev2RawMatchingBrace(raw, open)
		if end < 0 {
			break
		}

		eventBody := raw[open+1 : end]

		for _, block := range ikev2RawNamedBlocks(
			eventBody,
		) {
			header := block.Body

			childIndex := strings.Index(
				header,
				"child-sas {",
			)

			childBody := ""

			if childIndex >= 0 {
				childOpen := childIndex +
					strings.Index(
						header[childIndex:],
						"{",
					)

				childEnd := ikev2RawMatchingBrace(
					header,
					childOpen,
				)

				if childEnd >= 0 {
					childBody = header[childOpen+1 : childEnd]

					header = header[:childIndex]
				}
			}

			sa := ikev2RawSA{
				ConnectionName: block.Name,
				UniqueID: ikev2RawScalar(
					header,
					"uniqueid",
				),
				State: ikev2RawScalar(
					header,
					"state",
				),
				RemoteID: ikev2RawScalar(
					header,
					"remote-id",
				),
				RemoteEAPID: ikev2RawScalar(
					header,
					"remote-eap-id",
				),
				RemoteHost: ikev2RawScalar(
					header,
					"remote-host",
				),
				RemoteVIPs: ikev2RawList(
					header,
					"remote-vips",
				),
				InitiatorSPI: ikev2RawScalar(
					header,
					"initiator-spi",
				),
				ResponderSPI: ikev2RawScalar(
					header,
					"responder-spi",
				),
			}

			for _, child := range ikev2RawNamedBlocks(
				childBody,
			) {
				sa.Children = append(
					sa.Children,
					ikev2RawChildSA{
						Name: child.Name,
						UniqueID: ikev2RawScalar(
							child.Body,
							"uniqueid",
						),
						State: ikev2RawScalar(
							child.Body,
							"state",
						),
						BytesIn: ikev2RawUint(
							child.Body,
							"bytes-in",
						),
						BytesOut: ikev2RawUint(
							child.Body,
							"bytes-out",
						),
					},
				)
			}

			result = append(result, sa)
		}

		offset = end + 1
	}

	return result
}

func ikev2UsageDelta(
	current uint64,
	baseline uint64,
	exists bool,
) uint64 {
	if !exists {
		return current
	}

	if current < baseline {
		return current
	}

	return current - baseline
}

func ikev2SafeAdd(
	left uint64,
	right uint64,
) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func ikev2UsageBaselineKey(
	inboundTag string,
	sa ikev2RawSA,
	child ikev2RawChildSA,
) string {
	ikeID := strings.TrimSpace(
		sa.InitiatorSPI,
	) + ":" + strings.TrimSpace(
		sa.ResponderSPI,
	)

	if strings.Trim(ikeID, ":") == "" {
		ikeID = strings.TrimSpace(sa.UniqueID)
	}

	childID := strings.TrimSpace(child.UniqueID)
	if childID == "" {
		childID = strings.TrimSpace(child.Name)
	}

	return strings.TrimSpace(inboundTag) +
		"|" + ikeID +
		"|" + childID
}

func ikev2AppendUniqueIP(
	values []string,
	value string,
) []string {
	value = strings.TrimSpace(value)

	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() {
		return values
	}

	value = addr.String()

	for _, existing := range values {
		if existing == value {
			return values
		}
	}

	return append(values, value)
}

func (s *Server) activeIKEv2RuntimeInbounds() map[string]ikev2RuntimeInbound {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make(
		map[string]ikev2RuntimeInbound,
		len(s.ikev2Runtimes),
	)

	for _, runtime := range s.ikev2Runtimes {
		if runtime == nil {
			continue
		}

		inbound := runtime.inbound
		inbound.Users = append(
			[]ikev2RuntimeUser(nil),
			runtime.inbound.Users...,
		)

		result[ikev2ConnectionName(runtime.tag)] = inbound
	}

	return result
}

func (s *Server) collectIKEv2UserUsage(
	ctx context.Context,
	_ *nodev1.CollectUsageRequest,
) (*nodev1.UserUsageBatch, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	runtimes := s.activeIKEv2RuntimeInbounds()
	if len(runtimes) == 0 {
		return &nodev1.UserUsageBatch{}, nil
	}

	s.ikev2UsageMu.Lock()
	defer s.ikev2UsageMu.Unlock()

	if err := s.ensureIKEv2UsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	if s.ikev2UsagePending != nil {
		return ikev2UsageBatchProto(
			s.ikev2UsagePending,
		), nil
	}

	swanctlPath, err := exec.LookPath("swanctl")
	if err != nil {
		return nil, fmt.Errorf(
			"ikev2 usage: swanctl is not installed",
		)
	}

	output, err := exec.CommandContext(
		ctx,
		swanctlPath,
		"--list-sas",
		"--raw",
	).CombinedOutput()

	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}

		return nil, fmt.Errorf(
			"ikev2 usage: swanctl --list-sas --raw: %s",
			detail,
		)
	}

	sas := parseIKEv2SwanctlRaw(string(output))

	terminatedSAs, policyErr :=
		s.enforceIKEv2Policies(
			ctx,
			runtimes,
			sas,
		)

	if policyErr != nil {
		s.appendLog(
			"ikev2 policy enforcement failed: " +
				policyErr.Error(),
		)
	}

	nextBaseline := make(
		map[string]uint64,
		len(s.ikev2UsageBaseline),
	)

	for key, value := range s.ikev2UsageBaseline {
		nextBaseline[key] = value
	}

	type aggregateKey struct {
		UserID     int64
		InboundTag string
	}

	aggregated := make(
		map[aggregateKey]ikev2UsageSample,
	)

	for _, sa := range sas {
		if !strings.EqualFold(
			strings.TrimSpace(sa.State),
			"ESTABLISHED",
		) {
			continue
		}

		inbound, exists := runtimes[sa.ConnectionName]
		if !exists {
			continue
		}

		identity := strings.TrimSpace(sa.RemoteEAPID)
		if identity == "" {
			identity = strings.TrimSpace(sa.RemoteID)
		}
		if identity == "" {
			continue
		}

		var user ikev2RuntimeUser
		found := false

		for _, candidate := range inbound.Users {
			if strings.TrimSpace(candidate.Username) ==
				identity {
				user = candidate
				found = true
				break
			}
		}

		if !found || user.UserID <= 0 {
			continue
		}

		key := aggregateKey{
			UserID:     user.UserID,
			InboundTag: strings.TrimSpace(inbound.Tag),
		}

		sample := aggregated[key]
		sample.UserID = user.UserID
		sample.InboundTag = key.InboundTag
		if _, terminated :=
			terminatedSAs[strings.TrimSpace(
				sa.UniqueID,
			)]; !terminated {
			sample.Online = true
		}

		for _, vip := range sa.RemoteVIPs {
			sample.IPs = ikev2AppendUniqueIP(
				sample.IPs,
				vip,
			)
		}

		for _, child := range sa.Children {
			if !strings.EqualFold(
				strings.TrimSpace(child.State),
				"INSTALLED",
			) {
				continue
			}

			total := ikev2SafeAdd(
				child.BytesIn,
				child.BytesOut,
			)

			baselineKey := ikev2UsageBaselineKey(
				inbound.Tag,
				sa,
				child,
			)

			baseline, baselineExists :=
				s.ikev2UsageBaseline[baselineKey]

			delta := ikev2UsageDelta(
				total,
				baseline,
				baselineExists,
			)

			nextBaseline[baselineKey] = total
			sample.Value = ikev2SafeAdd(
				sample.Value,
				delta,
			)
		}

		aggregated[key] = sample
	}

	if len(aggregated) == 0 {
		return &nodev1.UserUsageBatch{}, nil
	}

	keys := make(
		[]aggregateKey,
		0,
		len(aggregated),
	)

	for key := range aggregated {
		keys = append(keys, key)
	}

	sort.Slice(
		keys,
		func(i int, j int) bool {
			if keys[i].UserID ==
				keys[j].UserID {
				return keys[i].InboundTag <
					keys[j].InboundTag
			}

			return keys[i].UserID <
				keys[j].UserID
		},
	)

	samples := make(
		[]ikev2UsageSample,
		0,
		len(keys),
	)

	for _, key := range keys {
		sample := aggregated[key]
		sort.Strings(sample.IPs)
		samples = append(samples, sample)
	}

	pending := &ikev2UsagePendingBatch{
		BatchID: fmt.Sprintf(
			"ikev2-%d",
			time.Now().UTC().UnixNano(),
		),
		Samples:      samples,
		NextBaseline: nextBaseline,
	}

	s.ikev2UsagePending = pending

	if err := s.persistIKEv2UsageStateLocked(); err != nil {
		s.ikev2UsagePending = nil
		return nil, err
	}

	return ikev2UsageBatchProto(pending), nil
}

func (s *Server) ackIKEv2UserUsage(
	_ context.Context,
	req *nodev1.AckUsageRequest,
) (*nodev1.AckUsageResponse, error) {
	batchID := strings.TrimSpace(req.GetBatchId())
	if batchID == "" {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	s.ikev2UsageMu.Lock()
	defer s.ikev2UsageMu.Unlock()

	if err := s.ensureIKEv2UsageStateLoadedLocked(); err != nil {
		return nil, err
	}

	if s.ikev2UsageLastAckedBatchID == batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: true,
		}, nil
	}

	pending := s.ikev2UsagePending
	if pending == nil ||
		pending.BatchID != batchID {
		return &nodev1.AckUsageResponse{
			Acknowledged: false,
		}, nil
	}

	previousBaseline := s.ikev2UsageBaseline
	previousLastAcked :=
		s.ikev2UsageLastAckedBatchID

	s.ikev2UsageBaseline =
		pending.NextBaseline
	s.ikev2UsagePending = nil
	s.ikev2UsageLastAckedBatchID =
		batchID

	if err := s.persistIKEv2UsageStateLocked(); err != nil {
		s.ikev2UsageBaseline = previousBaseline
		s.ikev2UsagePending = pending
		s.ikev2UsageLastAckedBatchID =
			previousLastAcked

		return nil, err
	}

	return &nodev1.AckUsageResponse{
		Acknowledged: true,
	}, nil
}

func ikev2UsageBatchProto(
	pending *ikev2UsagePendingBatch,
) *nodev1.UserUsageBatch {
	if pending == nil {
		return &nodev1.UserUsageBatch{}
	}

	stats := make(
		[]*nodev1.UserUsageSample,
		0,
		len(pending.Samples),
	)

	onlineIPs := make(
		[]*nodev1.OnlineUserIP,
		0,
		len(pending.Samples),
	)

	now := time.Now().UTC().Unix()

	for _, sample := range pending.Samples {
		uid := "ikev2:" +
			strconv.FormatInt(
				sample.UserID,
				10,
			)

		statUID := uid

		if sample.Value == 0 &&
			sample.Online {
			statUID = "online:" + uid
		}

		stats = append(
			stats,
			&nodev1.UserUsageSample{
				Uid:        statUID,
				Value:      sample.Value,
				InboundTag: sample.InboundTag,
			},
		)

		if !sample.Online ||
			len(sample.IPs) == 0 {
			continue
		}

		ips := make(
			[]*nodev1.OnlineIP,
			0,
			len(sample.IPs),
		)

		for _, ip := range sample.IPs {
			ips = append(
				ips,
				&nodev1.OnlineIP{
					Ip:           ip,
					LastSeenUnix: now,
				},
			)
		}

		onlineIPs = append(
			onlineIPs,
			&nodev1.OnlineUserIP{
				Uid: uid,
				Ips: ips,
			},
		)
	}

	return &nodev1.UserUsageBatch{
		BatchId:   pending.BatchID,
		Stats:     stats,
		OnlineIps: onlineIPs,
	}
}
