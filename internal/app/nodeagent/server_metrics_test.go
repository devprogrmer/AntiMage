package nodeagent

import "testing"

func TestBytesPerSecond(t *testing.T) {
	if got := bytesPerSecond(100, 400, 3); got != 100 {
		t.Fatalf("bytesPerSecond = %d, want 100", got)
	}
	if got := bytesPerSecond(400, 100, 3); got != 0 {
		t.Fatalf("counter reset bytesPerSecond = %d, want 0", got)
	}
	if got := bytesPerSecond(100, 400, 0); got != 0 {
		t.Fatalf("zero elapsed bytesPerSecond = %d, want 0", got)
	}
}
