package executor

import (
	"testing"
	"time"
)

// The spill codec is goopg's only Datum-level serializer: everywhere else a
// Datum either stays in memory (a 48-byte copy, nothing to lose) or is written
// through the storage/wire codecs, which carry the column's declared type
// alongside the bytes and can therefore re-derive what the value IS.
// `encodeDatum`/`decodeDatum` cannot — a spill frame is read back as a bare
// `Row` with no types in reach — so every scrap of per-value type state has to
// travel inside the frame.
//
// That contract was never written down, and it had been broken three times over
// by the time M0127-P5.9-u audited it:
//
//   - the DATE discriminator was dropped (found in production by TPC-DS Q72,
//     M0127-P5.9-s: `d_date + 5` raising "operator + requires integer
//     operands" only at the work_mem where the join spilled);
//   - a `timetz`'s UTC offset, which rides in Datum.Scale, was dropped — so
//     spilled timetz values re-sorted by local time and rendered in the wrong
//     zone;
//   - KindEnum and KindToastPointer had no arm at all.
//
// The first two are the dangerous shape: the VALUE survives, only the type is
// forgotten, so the result is wrong rather than absent. TestSpillRoundTrip was
// green throughout — it compared values.
//
// These tests replace "someone remembers to extend the codec" with a failure.
// They enumerate the kind and subtype spaces from the declared constants
// (datumKindCount, timeSubtypeCount), so adding a DatumKind or a TimeSubtype
// without teaching the codec about it fails here instead of silently degrading
// a value in a query that happens to spill.

// TestSpillDatumRoundTripCoversEveryKind walks every declared DatumKind. The
// per-kind checker asserts the semantics that kind is responsible for, not
// struct equality — a decoded Datum legitimately differs in representation
// (arena-backed strings come back Buf-backed; numerics are rebuilt through
// newNumeric).
func TestSpillDatumRoundTripCoversEveryKind(t *testing.T) {
	when := time.Date(1998, 3, 15, 12, 34, 56, 0, time.UTC)

	// One representative Datum per kind, plus what must survive the trip.
	cases := map[DatumKind]struct {
		in    Datum
		check func(t *testing.T, got Datum)
	}{
		KindNull: {NullDatum, func(t *testing.T, got Datum) {
			if !got.IsNull() {
				t.Errorf("null did not survive: %v", got.Kind)
			}
		}},
		KindBool: {NewBoolDatum(true), func(t *testing.T, got Datum) {
			if !got.BoolValue() {
				t.Error("bool value flipped")
			}
		}},
		KindInt: {Datum{Kind: KindInt, Int: -42}, func(t *testing.T, got Datum) {
			if got.Int != -42 {
				t.Errorf("int = %d, want -42", got.Int)
			}
		}},
		KindString: {NewStringDatum("héllo"), func(t *testing.T, got Datum) {
			if got.StringValue() != "héllo" {
				t.Errorf("string = %q", got.StringValue())
			}
		}},
		KindBytes: {NewBytesDatum([]byte{0, 1, 255}), func(t *testing.T, got Datum) {
			if string(got.BytesValue()) != string([]byte{0, 1, 255}) {
				t.Errorf("bytes = %v", got.BytesValue())
			}
		}},
		KindTime: {NewDateDatum(when), func(t *testing.T, got Datum) {
			if !got.IsDate() {
				t.Error("date forgot it was a date")
			}
			if !got.TimeValue().Equal(when) {
				t.Errorf("time = %v, want %v", got.TimeValue(), when)
			}
		}},
		KindInterval: {NewIntervalDatumFull(14, 3, 456789), func(t *testing.T, got Datum) {
			if got.IntervalMonthsValue() != 14 || got.IntervalDaysValue() != 3 ||
				got.IntervalMicrosValue() != 456789 {
				t.Errorf("interval = %d/%d/%d, want 14/3/456789",
					got.IntervalMonthsValue(), got.IntervalDaysValue(), got.IntervalMicrosValue())
			}
		}},
		KindNumeric: {Datum{Kind: KindNumeric, Int: -12345, Scale: 3}, func(t *testing.T, got Datum) {
			if got.Format() != "-12.345" {
				t.Errorf("numeric = %q, want -12.345", got.Format())
			}
		}},
		// M0127-P5.9-u: previously unencodable — the frame decoded as
		// "unknown datum kind 8".
		KindToastPointer: {
			Datum{Kind: KindToastPointer, Buf: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}},
			func(t *testing.T, got Datum) {
				if len(got.BytesValue()) != 12 || got.BytesValue()[11] != 12 {
					t.Errorf("toast pointer = %v", got.BytesValue())
				}
			},
		},
		// M0127-P5.9-u: likewise. Both halves matter — Int is what
		// compareDatum orders enums by, Buf is what Format() prints.
		KindEnum: {
			Datum{Kind: KindEnum, Int: 7, Buf: []byte("shipped")},
			func(t *testing.T, got Datum) {
				if got.Int != 7 {
					t.Errorf("enum sort order = %d, want 7 — an enum that loses it sorts wrong", got.Int)
				}
				if string(got.Buf) != "shipped" {
					t.Errorf("enum label = %q, want \"shipped\"", got.Buf)
				}
			},
		},
	}

	for k := DatumKind(0); k < datumKindCount; k++ {
		tc, ok := cases[k]
		if !ok {
			t.Fatalf("DatumKind %d has no spill round-trip case. A new kind must be "+
				"given an arm in encodeDatum AND decodeDatum (spill.go) and a case "+
				"here; without one, any query that spills a column of this kind "+
				"either fails with \"unknown datum kind\" or, worse, comes back "+
				"stripped of type state that still compares correctly.", k)
		}
		got, n, err := decodeDatum(encodeDatum(tc.in, nil))
		if err != nil {
			t.Errorf("kind %d: decode: %v", k, err)
			continue
		}
		if n == 0 {
			t.Errorf("kind %d: decode consumed no bytes", k)
			continue
		}
		if got.Kind != tc.in.Kind {
			t.Errorf("kind %d: round trip changed kind to %d", k, got.Kind)
			continue
		}
		tc.check(t, got)
	}
}

// TestSpillDatumRoundTripCoversEveryTimeSubtype pins the discriminator half.
// The subtype is what tells a `date` from a `timestamp` from a `timetz` on the
// shared KindTime carrier, and losing it is silent by construction: Int is
// unchanged, so the value still compares correctly and only its type is gone.
func TestSpillDatumRoundTripCoversEveryTimeSubtype(t *testing.T) {
	when := time.Date(1998, 3, 15, 12, 34, 56, 0, time.UTC)
	for sub := TimeSubtype(0); sub < timeSubtypeCount; sub++ {
		in := NewTimeDatum(when)
		in.TimeSub = sub
		got, _, err := decodeDatum(encodeDatum(in, nil))
		if err != nil {
			t.Errorf("subtype %d: decode: %v", sub, err)
			continue
		}
		if got.TimeSub != sub {
			t.Errorf("subtype %d came back as %d — the spill codec must carry every "+
				"declared TimeSubtype (spill.go's KindTime arm)", sub, got.TimeSub)
		}
	}
}

// TestSpillPreservesTheTimeTZOffset is the regression guard for the second
// casualty M0127-P5.9-u's audit found. A `timetz` keeps its offset east of UTC
// in Datum.Scale (minutes, NewTimeTZDatum), and compareDatum normalises to UTC
// through it — "PostgreSQL compares by UTC time (local_nanos - offset_nanos),
// then by offset as tiebreaker", matching upstream timetz_cmp
// (postgres/src/backend/utils/adt/date.c). The KindTime arm wrote 8 bytes of
// nanoseconds and nothing else, so a spilled timetz came back with Scale == 0:
// it then sorted by LOCAL time against unspilled peers and printed in the wrong
// zone. Both are wrong answers, and neither raises an error.
func TestSpillPreservesTheTimeTZOffset(t *testing.T) {
	// 12:00:00-07 (PDT). Offset is -25200s = -420 minutes.
	local := time.Date(1970, 1, 1, 12, 0, 0, 0, time.UTC)
	in := NewTimeTZDatum(local, -25200)
	if in.TimeTZOffsetSecs() != -25200 {
		t.Fatalf("fixture is wrong: offset = %d", in.TimeTZOffsetSecs())
	}

	got, _, err := decodeDatum(encodeDatum(in, nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TimeTZOffsetSecs() != -25200 {
		t.Errorf("timetz offset = %d, want -25200 — a spilled timetz that loses its "+
			"offset compares by local time instead of UTC", got.TimeTZOffsetSecs())
	}
	if got.TimeSub != TimeSubTimeTZ {
		t.Errorf("timetz subtype = %d, want TimeSubTimeTZ", got.TimeSub)
	}

	// The consequence, stated as behaviour: 12:00-07 is 19:00Z and must sort
	// AFTER 13:00+00, even though its local clock reads earlier.
	other := NewTimeTZDatum(time.Date(1970, 1, 1, 13, 0, 0, 0, time.UTC), 0)
	cmp, err := compareDatum(got, other, 0)
	if err != nil {
		t.Fatalf("compareDatum: %v", err)
	}
	if cmp <= 0 {
		t.Errorf("compare(12:00-07, 13:00+00) = %d, want > 0 (19:00Z > 13:00Z)", cmp)
	}
}

// TestSpillRejectsAnUnknownTimeSubtype pins the fail-closed direction. A frame
// written by a future encoder that knows a subtype this reader does not must
// error, not decode as TimeSubTimestamp — quietly widening an unknown type into
// "bare timestamp" is precisely the failure mode this whole contract exists to
// close.
func TestSpillRejectsAnUnknownTimeSubtype(t *testing.T) {
	buf := encodeDatum(NewDateDatum(time.Now()), nil)
	// Layout: [kind][8 bytes nanos][subtype][2 bytes scale].
	buf[9] = byte(timeSubtypeCount)
	if _, _, err := decodeDatum(buf); err == nil {
		t.Fatal("decodeDatum accepted an unknown time subtype; it must fail loudly " +
			"rather than silently degrade the value to a bare timestamp")
	}
}
