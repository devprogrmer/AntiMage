//go:build !linux

package nodeagent

import (
	"context"
	"fmt"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func acceptNativeDestructiveFence(context.Context, *nodev1.DestructiveFence) error {
	return fmt.Errorf("persistent destructive boundaries require Linux")
}

func nativeFencingAvailable() bool { return false }

func readNativeCommandEvidence(context.Context, *nodev1.HealthRequest, *nodev1.RuntimeState) error {
	return fmt.Errorf("persistent command evidence requires Linux")
}

func runNativeDestructiveBoundary(context.Context, *nodev1.DestructiveFence, func(context.Context) (*nodev1.RuntimeActionResponse, error)) (*nodev1.RuntimeActionResponse, error) {
	return nil, fmt.Errorf("persistent destructive boundaries require Linux")
}
