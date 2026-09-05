package executor

// packedtuple_test.go — D-03 (MD-03) gates for the packed format itself.
// 06-verification.md §3 MD-03 and 03-byte-format-fidelity.md TD-2.

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

func testCol(name, typ string) catalog.Column {
	return catalog.Column{Name: name, Type: catalog.Type{Name: typ}}
}

func testDesc(t *testing.T, cols ...catalog.Column) *TupleDesc {
	t.Helper()
	return NewTupleDescFromColumns(cols)
}

func mustForm(t *testing.T, d *TupleDesc, row Row) PackedTuple {
	t.Helper()
	pt, err := FormPackedTuple(d, row, nil)
	if err != nil {
		t.Fatalf("FormPackedTuple: %v", err)
	}
	return pt
}

// TestPackedTupleHeaderIsMinimalTupleShaped pins the layout 03 §7.1 (TD-3)
// specifies against the constants 01 §2 transcribes from
// postgres/src/include/access/htup_details.h:674-704. The two details that are
// easy to get wrong are called out at htup_details.h:668 and are asserted
// separately below: t_hoff INCLUDES the MINIMAL_TUPLE_OFFSET distance, t_len
// does not.
func TestPackedTupleHeaderIsMinimalTupleShaped(t *testing.T) {
	if minimalTupleOffset != 8 || minimalTuplePadding != 6 ||
		minimalTupleDataOffset != 10 || sizeofMinimalTupleHeader != 15 {
		t.Fatalf("MinimalTuple constants drifted: offset=%d padding=%d dataOffset=%d header=%d",
			minimalTupleOffset, minimalTuplePadding, minimalTupleDataOffset, sizeofMinimalTupleHeader)
	}

	d := testDesc(t, testCol("a", "int4"), testCol("b", "int4"))
	pt := mustForm(t, d, Row{NewIntDatum(1), NewIntDatum(2)})

	// No NULLs → no bitmap → hoff = MAXALIGN(15) = 16, stored as 16 + 8.
	if got := int(pt.Hoff()); got != 16+minimalTupleOffset {
		t.Errorf("t_hoff = %d, want %d (MAXALIGN(SizeofMinimalTupleHeader) + MINIMAL_TUPLE_OFFSET)",
			got, 16+minimalTupleOffset)
	}
	if got := pt.dataOffset(); got != 16 {
		t.Errorf("data offset = %d, want 16 — the hoff-relative accessor must undo the PG scale", got)
	}
	if pt.Len() != len(pt.TupleBytes()) {
		t.Errorf("t_len = %d but the tuple is %d bytes; t_len must NOT include MINIMAL_TUPLE_OFFSET",
			pt.Len(), len(pt.TupleBytes()))
	}
	if pt.Natts() != 2 {
		t.Errorf("natts = %d, want 2", pt.Natts())
	}
	if pt.hasNulls() {
		t.Error("HEAP_HASNULL set on a row with no NULLs")
	}
	if pt.bitmap() != nil {
		t.Error("a NULL-free tuple must carry no bitmap")
	}
	// The 6 pad bytes are kept even though Go does not need them (03 §7.1), so
	// that every downstream offset equals PG's.
	for i := 4; i < 10; i++ {
		if pt.TupleBytes()[i] != 0 {
			t.Errorf("mt_padding[%d] = %d, want 0", i-4, pt.TupleBytes()[i])
		}
	}

	// With a NULL: bitmap of BITMAPLEN(3) = 1 byte at offset 15, hoff still
	// MAXALIGN(16) = 16 here, and HEAP_HASNULL set.
	d3 := testDesc(t, testCol("a", "int4"), testCol("b", "text"), testCol("c", "int4"))
	pn := mustForm(t, d3, Row{NewIntDatum(1), NullDatum, NewIntDatum(3)})
	if !pn.hasNulls() {
		t.Fatal("HEAP_HASNULL not set on a row with a NULL")
	}
	bm := pn.bitmap()
	if len(bm) != 1 {
		t.Fatalf("bitmap len = %d, want BITMAPLEN(3) = 1", len(bm))
	}
	// PG convention (NullBitmapPG): bit SET means NOT null.
	if bm[0]&0b001 == 0 || bm[0]&0b010 != 0 || bm[0]&0b100 == 0 {
		t.Errorf("bitmap = %08b, want bits 0 and 2 set and bit 1 clear", bm[0])
	}
	if pn.Infomask()&storage.HeapHasVarWidth == 0 {
		// b is text but NULL, so no varlena is actually stored. PG's
		// heap_fill_tuple sets HEAP_HASVARWIDTH per STORED value, so this
		// tuple must NOT claim it.
		t.Log("HEAP_HASVARWIDTH correctly absent: the only varlena column is NULL")
	} else {
		t.Error("HEAP_HASVARWIDTH set although every stored value is fixed-width")
	}
}

// TestPackedTupleHashPrefixMatchesSpillFraming pins 03 §7.1's hash-value
// prefix. The framing is deliberately spill.go's WriteRowHashed (spill.go:104,
// citing nodeHashjoin.c:1414): four little-endian bytes, immediately ahead of
// the payload, in the same write. A resident and a spilled tuple must differ
// in the payload encoding only.
func TestPackedTupleHashPrefixMatchesSpillFraming(t *testing.T) {
	d := testDesc(t, testCol("a", "int4"), testCol("b", "text"))
	row := Row{NewIntDatum(42), NewStringDatum("hello")}

	bare := mustForm(t, d, row)
	hashed, err := FormPackedTupleHashed(d, row, 0xDEADBEEF, nil)
	if err != nil {
		t.Fatalf("FormPackedTupleHashed: %v", err)
	}

	if _, ok := bare.HashValue(); ok {
		t.Error("a bare PackedTuple reported a hash prefix")
	}
	h, ok := hashed.HashValue()
	if !ok || h != 0xDEADBEEF {
		t.Fatalf("hash = %#x present=%v, want 0xDEADBEEF present", h, ok)
	}
	// Same allocation, prefix first — the probe rejects on the hash without
	// touching the tuple (01 §6).
	if got := binary.LittleEndian.Uint32(hashed.Bytes()[:4]); got != 0xDEADBEEF {
		t.Errorf("prefix bytes = %#x, want the little-endian hash", got)
	}
	if len(hashed.Bytes()) != packedHashPrefixLen+len(bare.TupleBytes()) {
		t.Errorf("hashed allocation = %d bytes, want prefix(%d) + tuple(%d) in ONE allocation",
			len(hashed.Bytes()), packedHashPrefixLen, len(bare.TupleBytes()))
	}
	if string(hashed.TupleBytes()) != string(bare.TupleBytes()) {
		t.Error("the prefix changed the tuple bytes; the framing must be additive")
	}
	// The accessors are prefix-agnostic: this is what "hoff-relative, no
	// negative-offset trick" buys.
	if hashed.Len() != bare.Len() || hashed.Hoff() != bare.Hoff() || hashed.Natts() != bare.Natts() {
		t.Error("header accessors disagree across the prefix")
	}
}

// TestTupleDescAttCacheOffFollowsPGRule pins the descriptor half of 04 §6
// (D-4). PG's rule (execTuples.c:1053-1082): an offset is cacheable only while
// every preceding attribute is fixed-width, and the cache dies at the first
// attlen <= 0 and never revives. The per-tuple half — a NULL also kills it —
// is answered by the tuple, which is why fixedOffset takes hasNulls.
//
// Consumption is deliberately deferred; see fixedOffset's doc comment.
func TestTupleDescAttCacheOffFollowsPGRule(t *testing.T) {
	d := testDesc(t,
		testCol("a", "int4"), // attlen 4, align 4 → offset 0
		testCol("b", "int8"), // attlen 8, align 8 → offset 8 (padded)
		testCol("c", "bool"), // attlen 1, align 1 → offset 16
		testCol("d", "text"), // varlena → cache dies here
		testCol("e", "int4"), // still dead
	)
	want := []struct {
		off int
		ok  bool
	}{{0, true}, {8, true}, {16, true}, {0, false}, {0, false}}
	for i, w := range want {
		off, ok := d.fixedOffset(i, false)
		if off != w.off || ok != w.ok {
			t.Errorf("fixedOffset(%d) = (%d,%v), want (%d,%v)", i, off, ok, w.off, w.ok)
		}
	}
	// A NULL anywhere in the tuple makes every position value-dependent.
	for i := range want {
		if _, ok := d.fixedOffset(i, true); ok {
			t.Errorf("fixedOffset(%d, hasNulls=true) was usable; PG's cache dies at the first NULL", i)
		}
	}
	if !d.hasVarWidth {
		t.Error("descriptor with a text column did not record hasVarWidth")
	}
	if !d.mayBeToasted {
		t.Error("descriptor with a text column ('x' storage) did not record mayBeToasted")
	}
	fixed := testDesc(t, testCol("a", "int4"), testCol("b", "float8"))
	if fixed.hasVarWidth || fixed.mayBeToasted {
		t.Error("an all-fixed-width descriptor claimed varlena or TOAST capability")
	}
}

// TestPackedTupleRejectsAToastPointerInAPlainColumn is the attstorage half of
// the D-01 payload put to work. A 'p' column can never hold an out-of-line
// value, and a TOAST pointer written into one would decode as whatever the
// column's type says — a silent retyping of the exact shape 04 §3.1 warns
// about, so it is refused on the ENCODE side where the error can be reported.
func TestPackedTupleRejectsAToastPointerInAPlainColumn(t *testing.T) {
	plain := testDesc(t, testCol("a", "int4"))
	ptr := Datum{Kind: KindToastPointer, Buf: make([]byte, 12)}
	if _, err := FormPackedTuple(plain, Row{ptr}, nil); err == nil {
		t.Fatal("a TOAST pointer packed into an int4 column was accepted")
	}
	ext := testDesc(t, testCol("a", "text"))
	if _, err := FormPackedTuple(ext, Row{ptr}, nil); err != nil {
		t.Fatalf("a TOAST pointer in a text column must be accepted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TD-2 — the exhaustiveness pattern, carried onto the PG-format codec.
// ---------------------------------------------------------------------------

// The spill codec is, today, "the one place in the repo where the Datum kind
// space is enforced as a closed set" (03 §6): TestSpillDatumRoundTripCovers
// EveryKind and …EveryTimeSubtype walk datumKindCount and timeSubtypeCount, so
// a kind added without a codec arm fails a test instead of silently degrading a
// value in a query that happens to spill. Three production bugs are on the
// record behind that guard, all of the same shape — the VALUE survived and only
// the TYPE was forgotten, so a values-comparing round-trip test stayed green
// (02 §1, TPC-DS Q72's spilled DATEs).
//
// 03 TD-2: that pattern moves onto the PG-format codec BEFORE anything depends
// on it, because the migration's end state retires the private spill payload
// (03 §7.3) and "the migration deletes the only thing standing between this
// codebase and the Q72-class bug it already paid for once" otherwise. The spill
// tests stay where they are until MD-last re-points them (06 §3 MD-last); these
// are their PG-format counterparts, not their replacements.
//
// One structural difference, and it is the reason a counterpart is needed at
// all rather than a copy: a spill frame is read back as a bare Row with no
// types in reach, so every scrap of type state has to travel INSIDE the frame.
// A packed tuple is typed by its DESCRIPTOR (03 §6), so the question this suite
// asks is different: for each DatumKind, is there a column type that carries a
// value of that kind through EncodeRowPG → decodeRowRangeInfo with its kind and
// its meaning intact? A kind for which the answer is no is a named, ledgered
// gap below — never a silent pass.

// packedKindCase is one kind's representative value, the column type it is
// packed as, and what must survive.
type packedKindCase struct {
	typeName string
	in       Datum
	check    func(t *testing.T, got Datum)
	// gap names a ledger row when the PG format cannot carry this kind today.
	// A non-empty gap turns the case into a pinned expectation of FAILURE, so
	// the closed set stays closed and the gap stays visible.
	gap string
}

func packedKindCases() map[DatumKind]packedKindCase {
	when := time.Date(1998, 3, 15, 12, 34, 56, 0, time.UTC)
	return map[DatumKind]packedKindCase{
		KindNull: {"int4", NullDatum, func(t *testing.T, got Datum) {
			if !got.IsNull() {
				t.Errorf("null did not survive: kind %v", got.Kind)
			}
		}, ""},
		KindBool: {"bool", NewBoolDatum(true), func(t *testing.T, got Datum) {
			if !got.BoolValue() {
				t.Error("bool value flipped")
			}
		}, ""},
		KindInt: {"int8", Datum{Kind: KindInt, Int: -42}, func(t *testing.T, got Datum) {
			if got.Int != -42 {
				t.Errorf("int = %d, want -42", got.Int)
			}
		}, ""},
		KindString: {"text", NewStringDatum("héllo"), func(t *testing.T, got Datum) {
			if got.StringValue() != "héllo" {
				t.Errorf("string = %q", got.StringValue())
			}
		}, ""},
		KindBytes: {"bytea", NewBytesDatum([]byte{0, 1, 255}), func(t *testing.T, got Datum) {
			if string(got.BytesValue()) != string([]byte{0, 1, 255}) {
				t.Errorf("bytes = %v", got.BytesValue())
			}
		}, ""},
		// A DATE column stores DAYS, so the representative must be midnight —
		// unlike the spill codec's case, which stores nanoseconds verbatim and
		// can therefore carry 12:34:56 in a "date". That difference is the
		// point of a counterpart rather than a copy: the PG format is typed by
		// the descriptor, so the value must be one the declared type can hold.
		KindTime: {"date", NewDateDatum(when.Truncate(24 * time.Hour)), func(t *testing.T, got Datum) {
			if !got.IsDate() {
				t.Error("date forgot it was a date — the Q72 failure shape (02 §1)")
			}
			if !got.TimeValue().Equal(when.Truncate(24 * time.Hour)) {
				t.Errorf("time = %v, want %v", got.TimeValue(), when.Truncate(24*time.Hour))
			}
		}, ""},
		KindInterval: {"interval", NewIntervalDatumFull(14, 3, 456789), func(t *testing.T, got Datum) {
			if got.IntervalMonthsValue() != 14 || got.IntervalDaysValue() != 3 ||
				got.IntervalMicrosValue() != 456789 {
				t.Errorf("interval = %d/%d/%d, want 14/3/456789",
					got.IntervalMonthsValue(), got.IntervalDaysValue(), got.IntervalMicrosValue())
			}
		}, ""},
		KindNumeric: {"numeric", Datum{Kind: KindNumeric, Int: -12345, Scale: 3}, func(t *testing.T, got Datum) {
			if got.Format() != "-12.345" {
				t.Errorf("numeric = %q, want -12.345", got.Format())
			}
		}, ""},
		KindToastPointer: {"text",
			Datum{Kind: KindToastPointer, Buf: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}},
			func(t *testing.T, got Datum) {
				if len(got.BytesValue()) != 12 || got.BytesValue()[11] != 12 {
					t.Errorf("toast pointer = %v", got.BytesValue())
				}
			}, ""},
		KindEnum: {"mood",
			Datum{Kind: KindEnum, Int: 7, Buf: []byte("shipped")},
			func(t *testing.T, got Datum) {
				if got.Int != 7 {
					t.Errorf("enum sort order = %d, want 7", got.Int)
				}
				if string(got.Buf) != "shipped" {
					t.Errorf("enum label = %q", got.Buf)
				}
			},
			// Ledgered by D-02 (TODO_ALL D-02 close entry): `take3-D-02-enum-
			// encode` — "enum values cannot be encoded at all". Confirmed
			// here: FormPackedTuple fails with `kind 9 cannot encode as
			// mood`. This is the SAFE half of D-02's finding — a loud refusal
			// rather than a silent retyping — and encode-side validation is
			// precisely what R-2 (04 §9.3) relies on to make deform total.
			// Pinned as a KNOWN gap rather than skipped: MD-04 onward may not
			// retain an enum column in packed form until this is closed, and
			// the day it IS closed this case fails and the `gap` is deleted.
			"take3-D-02-enum-encode"},
	}
}

// packedRoundTrip forms a one-column tuple and deforms it back.
func packedRoundTrip(t *testing.T, typeName string, in Datum) (Datum, error) {
	t.Helper()
	d := testDesc(t, testCol("c", typeName))
	pt, err := FormPackedTuple(d, Row{in}, nil)
	if err != nil {
		return Datum{}, err
	}
	out := make(Row, 1)
	_, err = decodeRowRangeInfo(out, d.cols, d.info, pt.data(), pt.bitmap(), pt.natts(),
		nil, array.DefaultOutputStyle(), 0, 1, 0)
	return out[0], err
}

// TestPackedTupleRoundTripCoversEveryKind is TD-2's counterpart of
// TestSpillDatumRoundTripCoversEveryKind for the PG format.
func TestPackedTupleRoundTripCoversEveryKind(t *testing.T) {
	cases := packedKindCases()
	for k := DatumKind(0); k < datumKindCount; k++ {
		tc, ok := cases[k]
		if !ok {
			t.Fatalf("DatumKind %d has no PG-format round-trip case. A new kind must be "+
				"given an arm in encodeValuePGCtx AND decodePhysicalPGValueLowered "+
				"(codec.go) and a case here; without one it round-trips through the "+
				"codec's outer default as KindString, silently (04 §3.1) — which is "+
				"the same shape as the DATE bug TPC-DS Q72 found (02 §1).", k)
		}
		got, err := packedRoundTrip(t, tc.typeName, tc.in)
		if tc.gap != "" {
			if err == nil && got.Kind == tc.in.Kind {
				t.Errorf("kind %d now round-trips through the PG format; ledger row %q "+
					"is closed — delete the `gap` from this case and let the checker run", k, tc.gap)
			}
			continue
		}
		if err != nil {
			t.Errorf("kind %d: %v", k, err)
			continue
		}
		if got.Kind != tc.in.Kind {
			t.Errorf("kind %d round-tripped as kind %d — the value may still compare "+
				"correctly, which is exactly why this is dangerous", k, got.Kind)
			continue
		}
		tc.check(t, got)
	}
}

// TestPackedTupleRoundTripCoversEveryTimeSubtype is TD-2's counterpart of
// TestSpillDatumRoundTripCoversEveryTimeSubtype. The subtype is what tells a
// `date` from a `timestamp` from a `timetz` on the shared KindTime carrier;
// losing it is silent by construction, because Int is unchanged and only the
// type is gone.
//
// In the PG format the subtype is not carried in the value at all — it is
// re-derived from the COLUMN TYPE (03 §6: "TimeSub must come from the column
// type"). So the assertion is that each declared subtype has a column type
// that reconstructs it, which is the property a packed retention site depends
// on.
func TestPackedTupleRoundTripCoversEveryTimeSubtype(t *testing.T) {
	when := time.Date(1998, 3, 15, 12, 34, 56, 0, time.UTC)
	type subCase struct {
		typeName string
		// gap names the ledger row when the PG format cannot reconstruct this
		// subtype today. A pinned failure, never a skip.
		gap string
	}
	typeFor := map[TimeSubtype]subCase{
		TimeSubTimestamp:   {"timestamp", ""},
		TimeSubDate:        {"date", ""},
		TimeSubTimestampTZ: {"timestamptz", ""},
		// FOUND BY THIS TEST, and it is exactly what TD-2 exists to surface.
		// `TimeSubTime` is declared but has NO producer anywhere in the
		// executor (datum.go:112-114 records the deferral against
		// M0127-P5.9-u; grep confirms the constant is written in datum.go and
		// nowhere else), so a `time without time zone` column deforms to
		// TimeSubTimestamp — the subtype is not lost by the packed format, it
		// was never set. The spill counterpart passes because it round-trips
		// a subtype the TEST supplied; the PG format re-derives it from the
		// column type and therefore cannot invent one the codec never emits.
		//
		// Consequence for the bundle: nothing regresses (no producer, so no
		// query can observe it), but any later slice that starts populating
		// TimeSubTime must close this at the same time or a retained `time`
		// column silently widens to `timestamp` — the TPC-DS Q72 shape.
		TimeSubTime:   {"time", "M0127-P5.9-u (TimeSubTime has no producer)"},
		TimeSubTimeTZ: {"timetz", ""},
	}
	for sub := TimeSubtype(0); sub < timeSubtypeCount; sub++ {
		sc, ok := typeFor[sub]
		if !ok {
			t.Fatalf("TimeSubtype %d has no column type in this table. Every declared "+
				"subtype must name the PG type that reconstructs it on deform, or a "+
				"packed retention site silently widens it to a bare timestamp — the "+
				"TPC-DS Q72 failure (02 §1).", sub)
		}
		in := NewTimeDatum(when)
		in.TimeSub = sub
		switch sub {
		case TimeSubTimeTZ:
			in = NewTimeTZDatum(when, -25200)
		case TimeSubDate:
			in = NewDateDatum(when.Truncate(24 * time.Hour))
		}
		got, err := packedRoundTrip(t, sc.typeName, in)
		if sc.gap != "" {
			if err == nil && got.Kind == KindTime && got.TimeSub == sub {
				t.Errorf("subtype %d (%s) now round-trips; ledger row %q is closed — "+
					"delete the `gap` from this case", sub, sc.typeName, sc.gap)
			}
			continue
		}
		if err != nil {
			t.Errorf("subtype %d (%s): %v", sub, sc.typeName, err)
			continue
		}
		if got.Kind != KindTime {
			t.Errorf("subtype %d (%s): round-tripped as kind %d, want KindTime", sub, sc.typeName, got.Kind)
			continue
		}
		if got.TimeSub != sub {
			t.Errorf("subtype %d (%s) came back as %d — the column type must reconstruct "+
				"the discriminator the packed form does not carry", sub, sc.typeName, got.TimeSub)
		}
	}
}

// TestPackedTupleCarriesTheTimeTZOffset is the PG-format counterpart of
// TestSpillPreservesTheTimeTZOffset: a timetz's offset east of UTC rides in
// Datum.Scale and compareDatum normalises through it, so a retention format
// that drops it re-sorts by LOCAL time and renders in the wrong zone — a wrong
// answer that raises no error.
func TestPackedTupleCarriesTheTimeTZOffset(t *testing.T) {
	local := time.Date(1970, 1, 1, 12, 0, 0, 0, time.UTC)
	in := NewTimeTZDatum(local, -25200)
	got, err := packedRoundTrip(t, "timetz", in)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if got.TimeTZOffsetSecs() != -25200 {
		t.Fatalf("timetz offset = %d, want -25200", got.TimeTZOffsetSecs())
	}
	other := NewTimeTZDatum(time.Date(1970, 1, 1, 13, 0, 0, 0, time.UTC), 0)
	cmp, err := compareDatum(got, other, 0)
	if err != nil {
		t.Fatalf("compareDatum: %v", err)
	}
	if cmp <= 0 {
		t.Errorf("compare(12:00-07, 13:00+00) = %d, want > 0 (19:00Z > 13:00Z)", cmp)
	}
}

// TestNewTupleDescFromSchemaMatchesColumns pins R-7's premise: a descriptor
// built from a plan's Schema — the intermediate-row case, where the retention
// lives (02 §9) — is equivalent to one built from a column list, and holds no
// catalog state that could go stale behind it.
func TestNewTupleDescFromSchemaMatchesColumns(t *testing.T) {
	schema := optimizer.Schema{
		{Name: "a", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		{Name: "b", Type: catalog.Type{Name: "text"}, SourceTableIdx: 2},
	}
	fromSchema := NewTupleDesc(schema)
	fromCols := testDesc(t, testCol("a", "int4"), testCol("b", "text"))
	if fromSchema.Width() != fromCols.Width() {
		t.Fatalf("width %d vs %d", fromSchema.Width(), fromCols.Width())
	}
	for i := range fromSchema.cols {
		if fromSchema.info[i] != fromCols.info[i] {
			t.Errorf("column %d descriptor differs between the Schema and Column forms", i)
		}
		if fromSchema.attCacheOff[i] != fromCols.attCacheOff[i] {
			t.Errorf("column %d attcacheoff differs between the Schema and Column forms", i)
		}
		if fromSchema.cols[i].MissingValue != nil {
			t.Errorf("column %d carries a MissingValue; an intermediate row has no "+
				"ALTER TABLE history (04 §3)", i)
		}
	}
	row := Row{NewIntDatum(7), NewStringDatum("x")}
	a := mustForm(t, fromSchema, row)
	b := mustForm(t, fromCols, row)
	if string(a.Bytes()) != string(b.Bytes()) {
		t.Error("the two descriptor forms produced different bytes for the same row")
	}
}

// TestPackedTupleRejectsAToastPointerPerColumn covers the review finding that
// the plain-column TOAST check was descriptor-WIDE while its error message
// asserted a per-column fact. `mayBeToasted` is true if ANY column can be
// toasted, so on a {int4, text} descriptor — every realistic intermediate row
// — a TOAST pointer in the int4 column was accepted. The original test missed
// it because both fixtures were single-column, which is the one shape where
// the two readings coincide.
func TestPackedTupleRejectsAToastPointerPerColumn(t *testing.T) {
	// Mixed descriptor: col 0 is plain ('p'), col 1 is extended ('x').
	desc := NewTupleDescFromColumns([]catalog.Column{
		testCol("n", "int4"), testCol("s", "text"),
	})
	if desc.info[0].attStorage != 'p' {
		t.Fatalf("fixture: col 0 attstorage = %q, want 'p'", desc.info[0].attStorage)
	}
	if desc.info[1].attStorage == 'p' {
		t.Fatalf("fixture: col 1 must be toastable for this test to mean anything")
	}

	ptr := Datum{Kind: KindToastPointer, Buf: make([]byte, 18)}
	_, err := FormPackedTuple(desc, Row{ptr, NewStringDatum("ok")}, nil)
	if err == nil {
		t.Fatal("a TOAST pointer in the PLAIN column was accepted; the check is " +
			"reading the descriptor-wide mayBeToasted instead of this column's " +
			"attstorage, and R-2's decode-totality argument depends on it being exact")
	}
}
