package executor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestDecodeDateOutOfMicrosRange pins review/260831-2 EC-5: the physical date
// decoder multiplied days-since-2000 into microseconds with no range check,
// so a date PG can hold but int64 micros cannot (anything past ~294247 AD)
// wrapped silently into a plausible-looking near-past date instead of being
// reported. goopg's own date input already refuses those values, so the path
// only opens on a heap real PG wrote — where a wrong answer is worse than an
// error.
func TestDecodeDateOutOfMicrosRange(t *testing.T) {
	dateType := catalog.Type{Name: "date"}
	enc := func(days int32) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(days))
		return b
	}

	// Well past the micros range (PG's own max date, 5874897 AD, is ~1.43e9
	// days from 2000-01-01) — must error, not wrap.
	for _, days := range []int32{1_430_000_000, -1_430_000_000} {
		if _, _, err := decodePhysicalPGValueLowered(dateType, "date", enc(days), nil, array.OutputStyle{}); err == nil {
			t.Errorf("decode of %d days succeeded, want an out-of-range error", days)
		}
	}

	// The ±infinity sentinels keep their dedicated meaning.
	for _, tc := range []struct {
		days int32
		want string
	}{{math.MaxInt32, "infinity"}, {math.MinInt32, "-infinity"}} {
		d, _, err := decodePhysicalPGValueLowered(dateType, "date", enc(tc.days), nil, array.OutputStyle{})
		if err != nil {
			t.Fatalf("decode of the %s sentinel: %v", tc.want, err)
		}
		if got := d.Format(); got != tc.want {
			t.Errorf("sentinel %d decoded to %q, want %q", tc.days, got, tc.want)
		}
	}

	// An ordinary date still round-trips.
	d, _, err := decodePhysicalPGValueLowered(dateType, "date", enc(7500), nil, array.OutputStyle{})
	if err != nil {
		t.Fatalf("decode of an ordinary date: %v", err)
	}
	// Datum.Format renders a date in goopg's default display style (MM-DD-YYYY).
	if got := d.Format(); got != "07-14-2020" {
		t.Errorf("7500 days after 2000-01-01 = %q, want 07-14-2020", got)
	}
}
