//go:build !linux

package networkintelligence

import (
	"context"
	"errors"
)

func RunCapture(ctx context.Context, cfg CaptureConfig, consume func(CapturedPacket)) error {
	return runCapture(ctx, cfg, consume, nil)
}

func runCapture(ctx context.Context, cfg CaptureConfig, consume func(CapturedPacket), ready func()) error {
	return errors.New("packet metadata capture requires Linux")
}
