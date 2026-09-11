package api

import (
	"testing"
	"time"

	"github.com/antimage/antimage/internal/app/online"
)

func TestParseNodeUsageCollectionInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{
			name:  "default",
			value: "",
			want:  defaultNodeUsageCollectionInterval,
		},
		{
			name:  "five seconds",
			value: "5s",
			want:  5 * time.Second,
		},
		{
			name:  "thirty seconds",
			value: "30s",
			want:  30 * time.Second,
		},
		{
			name:  "forty five seconds capped",
			value: "45s",
			want:  maxNodeUsageCollectionInterval,
		},
		{
			name:  "one minute capped",
			value: "1m",
			want:  maxNodeUsageCollectionInterval,
		},
		{
			name:  "disabled zero",
			value: "0",
			want:  0,
		},
		{
			name:  "disabled off",
			value: "off",
			want:  0,
		},
		{
			name:  "disabled false",
			value: "false",
			want:  0,
		},
		{
			name:  "invalid uses default",
			value: "not-a-duration",
			want:  defaultNodeUsageCollectionInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNodeUsageCollectionInterval(tt.value)
			if got != tt.want {
				t.Fatalf(
					"parseNodeUsageCollectionInterval(%q) = %s; want %s",
					tt.value,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestOnlineActiveWindowCoversMaximumCollectionCadence(t *testing.T) {
	minimum := 2 * maxNodeUsageCollectionInterval

	if online.ActiveWindow <= minimum {
		t.Fatalf(
			"ActiveWindow=%s must be greater than two maximum collection intervals=%s",
			online.ActiveWindow,
			minimum,
		)
	}
}

func TestOnlineActiveWindowHasJitterBudget(t *testing.T) {
	jitter := online.ActiveWindow - (2 * maxNodeUsageCollectionInterval)

	if jitter < 10*time.Second {
		t.Fatalf(
			"online presence jitter budget=%s; want at least 10s",
			jitter,
		)
	}
}
