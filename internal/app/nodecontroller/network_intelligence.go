package nodecontroller

import (
	"context"
	"encoding/json"
	"errors"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// NetworkIntelligence reads optional metadata without changing node runtime.
// Older agents return an empty field and remain compatible.
func (c Controller) NetworkIntelligence(ctx context.Context, nodeID int64) (json.RawMessage, error) {
	client, _, err := c.dial(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	res, err := client.Runtime().Metrics(ctx, &nodev1.MetricsRequest{IncludeNetworkIntelligence: true})
	if err != nil {
		return nil, err
	}
	raw := res.GetNetworkIntelligenceJson()
	if raw == "" {
		return json.RawMessage(`{"available":false}`), nil
	}
	if len(raw) > 512*1024 || !json.Valid([]byte(raw)) {
		return nil, errors.New("invalid network intelligence snapshot")
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || snapshot == nil {
		return nil, errors.New("network intelligence snapshot must be an object")
	}
	if system := res.GetSystem(); system.GetMemoryTotal() > 0 {
		data, err := json.Marshal(struct {
			MemoryUsed  uint64 `json:"memory_used_bytes"`
			MemoryTotal uint64 `json:"memory_total_bytes"`
			SampledAt   int64  `json:"sampled_at_unix"`
		}{system.GetMemoryUsed(), system.GetMemoryTotal(), res.GetSampledAtUnix()})
		if err == nil {
			snapshot["node_resources"] = data
		}
	}
	return json.Marshal(snapshot)
}

// ManageNetworkOptimization invokes only the additive, optional Node Agent
// operation. Older agents return UNIMPLEMENTED; callers must fail closed.
func (c Controller) ManageNetworkOptimization(ctx context.Context, request *nodev1.NetworkOptimizationRequest) (*nodev1.NetworkOptimizationResponse, error) {
	if request == nil || request.GetNodeId() <= 0 {
		return nil, errors.New("valid optimization node is required")
	}
	client, _, err := c.dial(ctx, request.GetNodeId())
	if err != nil {
		return nil, err
	}
	response, err := client.Runtime().ManageNetworkOptimization(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.GetOperationId() != request.GetOperationId() {
		return nil, errors.New("node returned an invalid optimization operation identity")
	}
	return response, nil
}
