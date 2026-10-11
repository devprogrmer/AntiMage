package nodeagent

import (
	"context"
	"testing"
)

func TestAmneziaWGEmptyCollectorDoesNotPinCombinedWrapper(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	for i := 0; i < 2; i++ {
		batch, err := s.collectAmneziaWGUserUsage(context.Background(), nil)
		if err != nil || batch.GetBatchId() != "" || s.amneziaWGUsagePending != nil {
			t.Fatalf("empty collector created pending=%+v batch=%v err=%v", s.amneziaWGUsagePending, batch, err)
		}
	}
}
