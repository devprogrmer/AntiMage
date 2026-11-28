package networkintelligence

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkTCPQualityBoundedACK(b *testing.B) {
	for _, capacity := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprint(capacity), func(b *testing.B) {
			q, _ := NewTCPQuality(capacity)
			at := time.Now()
			// Exercise a full outstanding window and cumulative ACK handling.
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := uint32(i) * uint32(capacity)
				for j := 0; j < capacity; j++ {
					at = at.Add(time.Microsecond)
					q.Observe(tcpPacket(start+uint32(j), 0, 1, 0), 0, at)
				}
				at = at.Add(time.Microsecond)
				q.Observe(tcpPacket(0, start+uint32(capacity), 0, 0x10), 1, at)
			}
		})
	}
}
