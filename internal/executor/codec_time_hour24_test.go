package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"

	"github.com/goopg/goopg/internal/access/btree"
)

// M0119-0006 (50th slice). `24:00:00` is a real PG TimeADT value: time_in
// accepts it and AdjustTimeForTypmod's range check admits exactly USECS_PER_DAY
// (postgres/src/backend/utils/adt/date.c). goopg's parsers carry it as
// 1970-01-02 00:00:00 — time.Date normalises hour 24 into the next day — so the
// Hour/Minute/Second extraction in pgTimeMicros used to report 0 for it and the
// value was silently rewritten to `00:00:00` on the way INTO the heap.
//
// These tests fail against the pre-fix extraction. They cover the three
// consumers of pgTimeMicros that must agree (Hard-won Rule #2): the heap
// encode/decode pair, the btree scalar key, and the COPY renderer.

// timeHour24Carrier is how parseTimeString renders '24:00:00'.
func timeHour24Carrier(t *testing.T) Datum {
	t.Helper()
	ts, err := parseTimeString("24:00:00")
	if err != nil {
		t.Fatalf("parseTimeString(24:00:00): %v", err)
	}
	if ts.UTC().Day() != 2 {
		t.Fatalf("carrier = %s, want next-day midnight (the convention this test pins)",
			ts.UTC().Format("2006-01-02 15:04:05"))
	}
	return NewTimeDatum(ts)
}

func TestPGTimeMicrosHour24IsUsecsPerDay(t *testing.T) {
	d := timeHour24Carrier(t)
	if got := pgTimeMicros(d.TimeValue()); got != usecsPerDay {
		t.Fatalf("pgTimeMicros(24:00:00) = %d, want %d (USECS_PER_DAY)", got, usecsPerDay)
	}
	// The neighbouring genuine midnight must stay 0, or the probe is too broad.
	mid, err := parseTimeString("00:00:00")
	if err != nil {
		t.Fatalf("parseTimeString(00:00:00): %v", err)
	}
	if got := pgTimeMicros(mid); got != 0 {
		t.Fatalf("pgTimeMicros(00:00:00) = %d, want 0", got)
	}
}

func TestEncodeDecodeValuePGTimeHour24RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   catalog.Type
		width int
	}{
		{"time", catalog.Type{Name: "time"}, 8},
		{"timetz", catalog.Type{Name: "timetz"}, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := encodeValuePG(tc.typ, NewStringDatum("24:00:00"))
			if err != nil {
				t.Fatalf("encodeValuePG(%s): %v", tc.name, err)
			}
			got, n, err := decodePhysicalPGValueMctx(tc.typ, enc, nil)
			if err != nil {
				t.Fatalf("decodePhysicalPGValueMctx(%s): %v", tc.name, err)
			}
			if n != tc.width {
				t.Fatalf("decode consumed %d bytes, want %d", n, tc.width)
			}
			// The decoded carrier must be the SAME next-day midnight, not
			// 1970-01-01: that is what makes the stored value render as
			// `24:00:00` again rather than collapsing to `00:00:00`.
			u := got.TimeValue().UTC()
			if u.Year() != 1970 || u.Month() != 1 || u.Day() != 2 || u.Hour() != 0 {
				t.Fatalf("decoded carrier = %s, want 1970-01-02 00:00:00",
					u.Format("2006-01-02 15:04:05"))
			}
			if micros := pgTimeMicros(u); micros != usecsPerDay {
				t.Fatalf("round-tripped micros = %d, want %d", micros, usecsPerDay)
			}
		})
	}
}

func TestScalarBTreeKeyTimeHour24SortsAboveMidnight(t *testing.T) {
	// A btree key that collapsed 24:00:00 to 0 sorted it BELOW 00:00:01,
	// which is an index that disagrees with its own heap.
	col := catalog.Column{Name: "t", Type: catalog.Type{Name: "time"}}
	keyOf := func(lit string) []byte {
		ts, err := parseTimeString(lit)
		if err != nil {
			t.Fatalf("parseTimeString(%s): %v", lit, err)
		}
		// encErr must NOT reuse `err` above: it is a *ExecError, and assigning
		// a nil one into the existing `error` variable yields a non-nil
		// interface holding a nil pointer.
		k, handled, encErr := encodeScalarBTreeKey(NewTimeDatum(ts), &col, 0)
		if encErr != nil || !handled {
			t.Fatalf("encodeScalarBTreeKey(%s): handled=%v err=%v", lit, handled, encErr)
		}
		return k
	}
	midnight, oneSec, hour24 := keyOf("00:00:00"), keyOf("00:00:01"), keyOf("24:00:00")
	if !(btree.CompareKeys(midnight, oneSec) < 0 && btree.CompareKeys(oneSec, hour24) < 0) {
		t.Fatalf("key order broken: 00:00:00 vs 00:00:01 = %d, 00:00:01 vs 24:00:00 = %d",
			btree.CompareKeys(midnight, oneSec), btree.CompareKeys(oneSec, hour24))
	}
}

func TestDatumToCopyTextTimeHour24(t *testing.T) {
	d := timeHour24Carrier(t)
	for _, tc := range []struct {
		typ  catalog.Type
		want string
	}{
		{catalog.Type{Name: "time"}, "24:00:00"},
		// Declared precision is applied at OUTPUT (no AdjustTimeForTypmod port
		// yet); truncating USECS_PER_DAY at any scale leaves it unchanged.
		{catalog.Type{Name: "time", Args: []int64{2}}, "24:00:00"},
	} {
		got, err := datumToCopyText(tc.typ, d, "ISO", "MDY", "")
		if err != nil {
			t.Fatalf("datumToCopyText(%v): %v", tc.typ, err)
		}
		if got != tc.want {
			t.Fatalf("datumToCopyText(%v) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}
