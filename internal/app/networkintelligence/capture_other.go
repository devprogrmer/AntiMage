//go:build !linux

package networkintelligence

import (
	"context"
	"errors"
)

func RunCapture(ctx context.Context, cfg CaptureConfig, consume func(CapturedPacket)) error {
	return errors.New("packet metadata capture requires Linux")
}
