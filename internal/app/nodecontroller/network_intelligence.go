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
	return json.RawMessage(raw), nil
}
