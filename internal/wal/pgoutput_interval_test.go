package wal

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// pgoDecodePhysicalValue is the SECOND decoder of goopg's heap layout (the
// executor's decodePhysicalPGValueMctx is the first), and the two must agree —
// .ralph/PROMPT.md hard-won rule #2. When interval columns moved from varlena
// text to PG's native 16-byte {time int64, day int32, month int32} struct, an
// unrouted interval here would have read those bytes through the varlena
// fall-through: a 1-byte header taken from the microsecond field, so a
// replicated interval would reach the subscriber as garbage of an arbitrary
// length rather than failing.
//
// The expected texts are PostgreSQL 18.3 output (interval_out under the default
// 'postgres' IntervalStyle), captured from the reference cluster on port 65432.
func TestPgoDecodeIntervalMatchesPGNativeLayout(t *testing.T) {
	enc := func(months, days int32, micros int64) []byte {
		b := make([]byte, 16)
		binary.LittleEndian.PutUint64(b[:8], uint64(micros))
		binary.LittleEndian.PutUint32(b[8:12], uint32(days))
		binary.LittleEndian.PutUint32(b[12:16], uint32(months))
		return b
	}
	cases := []struct {
		name         string
		months, days int32
		micros       int64
		want         string
	}{
		{"month", 1, 0, 0, "1 mon"},
		{"sub-day", 0, 0, 2 * 3600 * 1_000_000, "02:00:00"},
		{"negative mixed", 0, -1, 2 * 3600 * 1_000_000, "-1 days +02:00:00"},
		{"full", 14, 3, 14706500000, "1 year 2 mons 3 days 04:05:06.5"},
		{"infinity", math.MaxInt32, math.MaxInt32, math.MaxInt64, "infinity"},
	}
	typ := catalog.Type{Name: "interval"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n, err := pgoDecodePhysicalValue(typ, enc(tc.months, tc.days, tc.micros), nil)
			if err != nil {
				t.Fatalf("pgoDecodePhysicalValue(interval): %v", err)
			}
			if n != 16 {
				t.Fatalf("consumed %d bytes, want 16 (pg_type OID 1186 typlen)", n)
			}
			if string(got) != tc.want {
				t.Errorf("decoded %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPgoPhysicalAlignInterval pins typalign 'd'. A 4-byte answer here decodes
// the interval correctly and shifts every following column of the replicated
// row, so it cannot be caught by the value test above.
func TestPgoPhysicalAlignInterval(t *testing.T) {
	typ := catalog.Type{Name: "interval"}
	if got := pgoPhysicalAlign(4, typ); got != 8 {
		t.Errorf("pgoPhysicalAlign(4, interval) = %d, want 8", got)
	}
	if got := pgoPhysicalAlign(16, typ); got != 16 {
		t.Errorf("pgoPhysicalAlign(16, interval) = %d, want 16 (already aligned)", got)
	}
}
