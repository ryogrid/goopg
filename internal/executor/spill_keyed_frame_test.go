package executor

// E-14 — the keyed INNER spill frame's round trip.
//
// spill.go's header states the rule this file enforces: appendRowPayload /
// encodeDatum and decodeRowPayload / decodeDatum are a sibling pair, and the
// test for a frame change is the ROUND TRIP, in both directions, not one
// side inspected in isolation. The keyed frame adds a canonical hash-table
// key between the routing hash and the payload; every case below writes with
// the real writer and reads with the real reader.
//
// Reuse (checked before writing): newSpillWriterInDir / newSpillReader /
// spillIntKey / spillStrKey / canonicalNumericKey / datumKey are all
// existing package symbols; no helper is redefined here.

import (
	"io"
	"math"
	"testing"
)

// skfRoundTrip writes the given (hash, key, row) triples with WriteRowKeyed
// and reads them back with ReadRowKeyedInto, returning what came out.
func skfRoundTrip(t *testing.T, hs []uint32, ks []spillRowKey, rows []Row) ([]uint32, []spillRowKey, []Row) {
	t.Helper()
	w, err := newSpillWriterInDir(t.TempDir())
	if err != nil {
		t.Fatalf("newSpillWriterInDir: %v", err)
	}
	for i := range rows {
		if err := w.WriteRowKeyed(hs[i], ks[i], rows[i]); err != nil {
			t.Fatalf("WriteRowKeyed[%d]: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}
	r, err := newSpillReader(w.Path())
	if err != nil {
		t.Fatalf("newSpillReader: %v", err)
	}
	defer r.Close()

	var gotH []uint32
	var gotK []spillRowKey
	var gotR []Row
	for {
		// A fresh dst per row: the reader's contract is that the returned
		// Row is invalidated by the next read, and this test keeps them.
		h, k, row, err := r.ReadRowKeyedInto(nil)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadRowKeyedInto: %v", err)
		}
		gotH = append(gotH, h)
		gotK = append(gotK, k)
		gotR = append(gotR, append(Row(nil), row...))
	}
	return gotH, gotK, gotR
}

// TestSpillKeyedFrameRoundTrip covers both key lanes, the boundary int64
// values, a binary (non-UTF8) canonical key, an empty string key and the
// ZERO-WIDTH row — the shape E-14's narrowed retention produces and the one
// a length-prefixed format is most likely to get wrong.
func TestSpillKeyedFrameRoundTrip(t *testing.T) {
	hs := []uint32{0, 1, 0xFFFFFFFF, 42, 7, 7}
	ks := []spillRowKey{
		spillIntKey(0),
		spillIntKey(math.MaxInt64),
		spillIntKey(math.MinInt64),
		spillStrKey(canonicalNumericKey(-12345, 0)),
		spillStrKey(""),
		spillStrKey(string([]byte{0x00, 0xff, 0x80, 0x00})),
	}
	rows := []Row{
		{NewIntDatum(1), NewStringDatum("one")},
		{},                   // zero-width: E-14's narrowed retention shape
		{NewIntDatum(-3)},    // single column
		{NewStringDatum("")}, // empty string payload
		{NewIntDatum(9), NewIntDatum(10), NewIntDatum(11)},
		{NewIntDatum(12)},
	}

	gotH, gotK, gotR := skfRoundTrip(t, hs, ks, rows)
	if len(gotR) != len(rows) {
		t.Fatalf("read %d frames, wrote %d", len(gotR), len(rows))
	}
	for i := range rows {
		if gotH[i] != hs[i] {
			t.Errorf("frame %d: hash %d, want %d", i, gotH[i], hs[i])
		}
		if gotK[i] != ks[i] {
			t.Errorf("frame %d: key %+v, want %+v", i, gotK[i], ks[i])
		}
		if len(gotR[i]) != len(rows[i]) {
			t.Fatalf("frame %d: width %d, want %d", i, len(gotR[i]), len(rows[i]))
		}
		for c := range rows[i] {
			if gotR[i][c].Kind != rows[i][c].Kind {
				t.Errorf("frame %d col %d: kind %v, want %v", i, c, gotR[i][c].Kind, rows[i][c].Kind)
			}
		}
	}
}

// TestSpillKeyedFrameKeyIsDetached pins that a decoded string key does not
// alias the reader's reusable payload buffer. A map key that aliased
// dataBuf would be silently rewritten by the NEXT read — the class the
// reader's own "valid until the next call" contract warns about, and the
// reason ReadRowKeyedInto copies the key out.
func TestSpillKeyedFrameKeyIsDetached(t *testing.T) {
	w, err := newSpillWriterInDir(t.TempDir())
	if err != nil {
		t.Fatalf("newSpillWriterInDir: %v", err)
	}
	first := string([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	second := string([]byte{9, 9, 9, 9, 9, 9, 9, 9})
	if err := w.WriteRowKeyed(1, spillStrKey(first), Row{NewIntDatum(1)}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRowKeyed(2, spillStrKey(second), Row{NewIntDatum(2)}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := newSpillReader(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var buf Row
	_, k1, row, err := r.ReadRowKeyedInto(buf)
	if err != nil {
		t.Fatal(err)
	}
	buf = row
	if _, _, _, err := r.ReadRowKeyedInto(buf); err != nil {
		t.Fatal(err)
	}
	if k1.s != first {
		t.Fatalf("first key became %q after the second read; the key aliases the read buffer", k1.s)
	}
}

// TestSpillKeyedFrameRejectsCorruptKey pins the decoder's refusals. Each is
// an error, never a silently mis-keyed row: a row filed under a wrong key
// is a lost-rows bug, which is what join_batch.go's header calls the failure
// mode the whole file exists to prevent.
func TestSpillKeyedFrameRejectsCorruptKey(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"unknown tag", []byte{9}},
		{"truncated int", []byte{spillKeyInt, 1, 2, 3}},
		{"truncated string body", []byte{spillKeyStr, 8, 1, 2}},
	}
	for _, c := range cases {
		if _, _, err := decodeSpillRowKey(c.data); err == nil {
			t.Errorf("%s: decodeSpillRowKey accepted a corrupt key", c.name)
		}
	}
	// A NULL-key marker is legal and decodes to the defensive tag.
	k, n, err := decodeSpillRowKey([]byte{spillKeyNone})
	if err != nil || n != 1 || k.tag != spillKeyNone {
		t.Fatalf("null key marker: %+v n=%d err=%v", k, n, err)
	}
}

// TestSpillKeyOfDatumMatchesInsertLane is the agreement pin between the two
// halves that must never disagree: spillKeyOfDatum chooses the lane a spill
// frame records, lazyHashInsertDatum chooses the lane an in-memory insert
// uses, and lazyHashInsertKeyed re-files a reloaded row. A row that spills
// and reloads must land in the bucket the direct insert would have used.
func TestSpillKeyOfDatumMatchesInsertLane(t *testing.T) {
	for _, intLane := range []bool{true, false} {
		direct := &joinOp{lazyHashIsInt: intLane}
		viaSpill := &joinOp{lazyHashIsInt: intLane}
		keys := []Datum{NewIntDatum(1), NewIntDatum(-7), NewIntDatum(0)}
		for i, kd := range keys {
			direct.lazyHashInsertDatum(kd, Row{NewIntDatum(int64(i))})
			viaSpill.lazyHashInsertKeyed(viaSpill.spillKeyOfDatum(kd), Row{NewIntDatum(int64(i))})
		}
		if direct.lazyHashIsInt != viaSpill.lazyHashIsInt {
			t.Fatalf("intLane=%v: lane diverged (%v vs %v)", intLane,
				direct.lazyHashIsInt, viaSpill.lazyHashIsInt)
		}
		if len(direct.lazyIntHash) != len(viaSpill.lazyIntHash) {
			t.Fatalf("intLane=%v: int buckets %d vs %d", intLane,
				len(direct.lazyIntHash), len(viaSpill.lazyIntHash))
		}
		for k, rows := range direct.lazyIntHash {
			if len(viaSpill.lazyIntHash[k]) != len(rows) {
				t.Fatalf("intLane=%v: int bucket %d holds %d rows, want %d", intLane,
					k, len(viaSpill.lazyIntHash[k]), len(rows))
			}
		}
		if len(direct.lazyHash) != len(viaSpill.lazyHash) {
			t.Fatalf("intLane=%v: string buckets %d vs %d", intLane,
				len(direct.lazyHash), len(viaSpill.lazyHash))
		}
		for k, rows := range direct.lazyHash {
			if len(viaSpill.lazyHash[k]) != len(rows) {
				t.Fatalf("intLane=%v: string bucket %q holds %d rows, want %d", intLane,
					k, len(viaSpill.lazyHash[k]), len(rows))
			}
		}
	}
}

// TestLazyHashInsertKeyedAcrossDemotion pins the one lane-crossing case that
// really occurs: a build spills int-lane keys, then demotes (demoteIntHash)
// because a datum was not int64-representable, and the reload arrives at a
// STRING table holding int keys. The conversion must be the same one
// demoteIntHash performs, or reloaded rows land in buckets no probe visits.
func TestLazyHashInsertKeyedAcrossDemotion(t *testing.T) {
	o := &joinOp{lazyHashIsInt: true}
	o.lazyHashInsertDatum(NewIntDatum(5), Row{NewIntDatum(1)})
	o.demoteIntHash()
	if o.lazyHashIsInt {
		t.Fatal("demoteIntHash left the int lane on")
	}
	// A frame written before the demotion carries the int-lane key.
	o.lazyHashInsertKeyed(spillIntKey(5), Row{NewIntDatum(2)})
	sk := canonicalNumericKey(5, 0)
	if got := len(o.lazyHash[sk]); got != 2 {
		t.Fatalf("bucket %q holds %d rows, want 2 (the reloaded row missed the demoted bucket)", sk, got)
	}
	// And a string-key frame arriving at an int table demotes rather than
	// dropping the row.
	o2 := &joinOp{lazyHashIsInt: true}
	o2.lazyHashInsertDatum(NewIntDatum(5), Row{NewIntDatum(1)})
	o2.lazyHashInsertKeyed(spillStrKey(sk), Row{NewIntDatum(2)})
	if o2.lazyHashIsInt {
		t.Fatal("a string-keyed reload left the int lane on")
	}
	if got := len(o2.lazyHash[sk]); got != 2 {
		t.Fatalf("after demotion bucket %q holds %d rows, want 2", sk, got)
	}
}
