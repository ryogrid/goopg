package xlog

import (
	"testing"
	"time"
)

// BenchmarkPgoPhysEpoch measures the epoch accessor used per decoded timestamp
// column (review/260831 XL-25): it used to rebuild the time.Time each call.
func BenchmarkPgoPhysEpoch(b *testing.B) {
	var sink time.Time
	b.ReportAllocs()
	for b.Loop() {
		sink = pgoPhysEpoch()
	}
	if sink.IsZero() {
		b.Fatal("zero epoch")
	}
}
