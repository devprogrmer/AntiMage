package nodecontroller

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAccountingReflectionPageUsesNodeScopedCommittedTotals(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `
CREATE TABLE users (id INTEGER PRIMARY KEY, used_traffic BIGINT);
CREATE TABLE node_wireguard_usage_reflection (
    node_id INTEGER NOT NULL, user_id INTEGER NOT NULL, batch_id TEXT NOT NULL,
    PRIMARY KEY (node_id, user_id)
);
WITH RECURSIVE ids(id) AS (SELECT 1 UNION ALL SELECT id + 1 FROM ids WHERE id < 502)
INSERT INTO users SELECT id, id * 100 FROM ids;
INSERT INTO node_wireguard_usage_reflection
SELECT 11, id, 'xray-' || id FROM users WHERE id <= 501;
INSERT INTO node_wireguard_usage_reflection VALUES
    (12, 1, 'wrong-node'), (12, 502, 'other-node'), (11, 999, 'deleted-user');
`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db, "sqlite")
	page, err := repo.accountingReflectionPage(ctx, 11, 0)
	if err != nil || len(page) != 500 {
		t.Fatalf("first page size=%d, error=%v", len(page), err)
	}
	for index, proof := range page {
		userID := int64(index + 1)
		if proof.GetUserId() != userID || proof.GetBatchId() != fmt.Sprintf("xray-%d", userID) || proof.GetUsedTraffic() != userID*100 {
			t.Fatalf("incorrect proof for user %d: %v", userID, proof)
		}
	}
	tail, err := repo.accountingReflectionPage(ctx, 11, page[len(page)-1].GetUserId())
	if err != nil || len(tail) != 1 || tail[0].GetUserId() != 501 ||
		tail[0].GetUsedTraffic() != 50100 || tail[0].GetBatchId() != "xray-501" {
		t.Fatalf("incorrect final page: %v, error=%v", tail, err)
	}
	client := &accountingReflectionRetryClient{}
	controller := Controller{repo: repo}
	batch, err := controller.collectUserUsageWithReflection(ctx, client, 11, &nodev1.CollectUsageRequest{
		CollectorId: "collector-test", Reset_: true,
	})
	if err != nil || batch.GetBatchId() != "xray-recovered" || len(client.requests) != 2 {
		t.Fatalf("retry failed: batch=%v calls=%d error=%v", batch, len(client.requests), err)
	}
	retry := client.requests[1]
	if len(client.requests[0].GetAccountingReflections()) != 0 ||
		len(retry.GetAccountingReflections()) != 501 ||
		retry.GetCollectorId() != "collector-test" || !retry.GetReset_() ||
		retry.GetAccountingReflections()[500].GetBatchId() != "xray-501" {
		t.Fatal("recovery request lost its collection identity or node-scoped proofs")
	}
}

type accountingReflectionRetryClient struct {
	nodev1.NodeUsageServiceClient
	requests []*nodev1.CollectUsageRequest
}

func (c *accountingReflectionRetryClient) CollectUserUsage(_ context.Context, req *nodev1.CollectUsageRequest, _ ...grpc.CallOption) (*nodev1.UserUsageBatch, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return nil, status.Error(codes.ResourceExhausted, "unreflected accounting delivery capacity exceeded")
	}
	return &nodev1.UserUsageBatch{BatchId: "xray-recovered"}, nil
}
