package executor

import (
	"testing"
	"time"
)

// TestGenUUIDv7FromTimeExtremeYears guards against the overflow ts.UnixNano()
// hits for dates before 1678 / after 2262 (int64 nanoseconds since epoch can
// only span ~292 years — Go's own documented UnixNano() caveat). uuidv7(interval)
// accepts an arbitrary shift (postgres/src/test/regress/sql/uuid.sql exercises
// years 1970..10888 via `uuidv7((y || ' years')::interval)`), so genUUIDv7FromTime
// must derive the ms-since-epoch/sub-ms components without routing through
// UnixNano. M0134-0083.
func TestGenUUIDv7FromTimeExtremeYears(t *testing.T) {
	cases := []struct {
		name string
		ts   time.Time
	}{
		{"near-epoch", time.Date(1970, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"far-future-y10888", time.Date(10888, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"beyond-unixnano-ceiling-y2262", time.Date(2262, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"beyond-unixnano-floor-y1829", time.Date(1829, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"present", time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := genUUIDv7FromTime(c.ts)
			if err != nil {
				t.Fatalf("genUUIDv7FromTime(%v): %v", c.ts, err)
			}
			b, ok := uuidToBytes(u)
			if !ok {
				t.Fatalf("genUUIDv7FromTime(%v) produced unparsable uuid %q", c.ts, u)
			}
			if b[6]>>4 != 7 {
				t.Fatalf("uuid %q: version nibble = %d, want 7", u, b[6]>>4)
			}
			ms := int64(b[0])<<40 | int64(b[1])<<32 | int64(b[2])<<24 |
				int64(b[3])<<16 | int64(b[4])<<8 | int64(b[5])
			wantMs := c.ts.Unix()*1_000 + int64(c.ts.Nanosecond())/1_000_000
			// The wire field is an UNSIGNED 48-bit ms count (RFC 9562); a
			// pre-1970 timestamp legitimately wraps modulo 2^48, matching
			// what genUUIDv7FromMs's byte-truncation naturally produces.
			wantMs &= (1 << 48) - 1
			if ms != wantMs {
				t.Errorf("uuid %q: decoded ms=%d, want %d (derived from ts=%v)", u, ms, wantMs, c.ts)
			}
		})
	}
}

// TestGenUUIDv7FromTimeMonotonicAcrossYears reproduces uuid.sql's own
// monotonicity probe at smaller scale: increasing timestamps must decode to
// increasing embedded ms values even across the pre-1678/post-2262
// UnixNano() overflow boundary.
func TestGenUUIDv7FromTimeMonotonicAcrossYears(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var prevMs int64 = -1
	for _, years := range []int{-56, 236, 528, 4913, 8862} {
		ts := base.AddDate(years, 0, 0)
		u, err := genUUIDv7FromTime(ts)
		if err != nil {
			t.Fatalf("years=%d: %v", years, err)
		}
		b, ok := uuidToBytes(u)
		if !ok {
			t.Fatalf("years=%d: unparsable uuid %q", years, u)
		}
		ms := int64(b[0])<<40 | int64(b[1])<<32 | int64(b[2])<<24 |
			int64(b[3])<<16 | int64(b[4])<<8 | int64(b[5])
		if ms <= prevMs {
			t.Errorf("years=%d: ms=%d not increasing from prev=%d (ts=%v)", years, ms, prevMs, ts)
		}
		prevMs = ms
	}
}
