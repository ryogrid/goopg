package executor

// EX1-03 contract tests — bound-narrowed detoast + per-attribute resolver.
//
// Gate (docs/design/executor-ex1-03-detoast/DESIGN.md section 4): TOAST
// contract tests per pointer-producing type (values + pin). The cost model
// is honest only when measured, so the narrow-query tests assert
// DetoastValue-call counts (poison OFF — armed poison OVERWRITES tail slots
// with an Int sentinel, masking pointers rather than detecting reads).
//
// Barrier audit lives here as TESTS with no lazy movement: the silent-sink
// tests assert end-to-end correctness over toasted inputs (a walk miss at a
// silent sink is wrong answers, so correct answers prove no pointer reached
// the sink); the loud-sink tests assert the errors are kept.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// toastNarrowBigValue builds a deterministic >2000 B value. The xorshift
// stream has no 3+ byte back-references for PGLZ to match, so the value is
// always stored raw out-of-line (2 chunks at 3000 B) rather than
// compressed — chunk-count and call-count assertions stay independent of
// the compression path.
func toastNarrowBigValue(n int) string {
	return incompressibleString(n)
}

// toastNarrowFixtureTable creates w(id int, narrow text, wide text) with
// wide values >2000 B (hence toasted) and returns the table's heap
// relation. narrowVal/wideVals[i] drive the rows 1..N.
func toastNarrowFixtureTable(t *testing.T, ctx *Context, name, narrowVal string, wideVals []string) {
	t.Helper()
	if err := runDDL(t, ctx, "CREATE TABLE "+name+" (id int, narrow text, wide text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
	rel := ctx.Catalog.RelFileNode(tbl)
	for i, wv := range wideVals {
		row := Row{
			{Kind: KindInt, Int: int64(i + 1)},
			NewStringDatum(narrowVal),
			NewStringDatum(wv),
		}
		if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
			t.Fatalf("INSERT row %d: %v", i+1, err)
		}
	}
}

// TestNarrowDetoastRoundTripPerType pins the per-type contract: a >2000 B
// value in every isToastableType column type round-trips with value AND
// kind restored. json/jsonb need syntactically valid content (the SQL read
// path serves them as text); xml needs a well-formed fragment. unknown has
// no DDL spelling (pseudo-type) and is covered unit-level in
// TestNarrowUnknownTypeUnitLevel.
func TestNarrowDetoastRoundTripPerType(t *testing.T) {
	big := toastNarrowBigValue(3000)
	jsonBig := `{"k": "` + strings.Repeat("x", 3000) + `"}`
	xmlBig := `<a>` + strings.Repeat("y", 3000) + `</a>`
	bigBytes := []byte(toastNarrowBigValue(3000))
	cases := []struct {
		name    string
		ddlType string
		val     string
		isBytes bool
	}{
		{"text", "text", big, false},
		{"varchar", "varchar", big, false},
		{"char", "char", big, false},
		{"bpchar", "bpchar", big, false},
		{"bytea", "bytea", string(bigBytes), true},
		{"json", "json", jsonBig, false},
		{"jsonb", "jsonb", jsonBig, false},
		{"xml", "xml", xmlBig, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := newToastFixture(t)
			defer cleanup()
			tblName := "narrow_rt_" + tc.name
			if err := runDDL(t, ctx, "CREATE TABLE "+tblName+" (id int, v "+tc.ddlType+")"); err != nil {
				t.Fatal(err)
			}
			tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: tblName})
			rel := ctx.Catalog.RelFileNode(tbl)
			var d Datum
			if tc.isBytes {
				d = NewBytesDatum([]byte(tc.val))
			} else {
				d = NewStringDatum(tc.val)
			}
			if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: 1}, d}); err != nil {
				t.Fatalf("INSERT: %v", err)
			}
			// The value must actually be stored out-of-line (a pointer was
			// produced); otherwise the round-trip below is vacuous.
			if nBlocks, err := ctx.Pool.NBlocks(ToastRelFor(rel)); err != nil || nBlocks == 0 {
				t.Fatalf("expected TOAST chunks for >2000 B %s value (blocks=%d err=%v)", tc.name, nBlocks, err)
			}
			rows := runQuery(t, ctx, "SELECT v FROM "+tblName+" WHERE id = 1")
			if len(rows) != 1 {
				t.Fatalf("expected 1 row, got %d", len(rows))
			}
			got := rows[0][0]
			if tc.isBytes {
				if got.Kind != KindBytes {
					t.Errorf("kind: want KindBytes (%d), got %d", KindBytes, got.Kind)
				}
				if string(got.BytesValue()) != tc.val {
					t.Errorf("value mismatch: got %d bytes, want %d", len(got.BytesValue()), len(tc.val))
				}
				return
			}
			if got.Kind != KindString {
				t.Errorf("kind: want KindString (%d), got %d", KindString, got.Kind)
			}
			if got.StringValue() != tc.val {
				t.Errorf("value mismatch: got %d bytes, want %d", len(got.StringValue()), len(tc.val))
			}
		})
	}
}

// TestNarrowUnknownTypeUnitLevel covers the isToastableType "unknown"
// entry, which has no DDL spelling (CREATE TABLE rejects the pseudo-type):
// at the storage layer an unknown-typed column still toasts oversized
// string payloads and DetoastAttr restores them as KindString.
func TestNarrowUnknownTypeUnitLevel(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()
	// The SQL write path materialises the writer XID for us; the
	// low-level ToastLargeColumnsIfNeeded call below does not, so do it
	// explicitly or the chunks are invisible to our own snapshot.
	if err := ctx.MaterializeWriterXID(); err != nil {
		t.Fatalf("MaterializeWriterXID: %v", err)
	}
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "v", Type: catalog.Type{Name: "unknown"}, Ordinal: 1},
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 910001, Fork: storage.MainFork}
	big := toastNarrowBigValue(3000)
	row := Row{{Kind: KindInt, Int: 1}, NewStringDatum(big)}
	toasted, err := ToastLargeColumnsIfNeeded(ctx, rel, cols, row)
	if err != nil {
		t.Fatalf("ToastLargeColumnsIfNeeded: %v", err)
	}
	if toasted[1].Kind != KindToastPointer {
		t.Fatalf("expected unknown column toasted, got kind %d", toasted[1].Kind)
	}
	got, err := DetoastAttr(ctx, rel, cols[1], toasted[1])
	if err != nil {
		t.Fatalf("DetoastAttr: %v", err)
	}
	if got.Kind != KindString || got.StringValue() != big {
		t.Errorf("unknown round-trip mismatch: kind=%d len=%d want len=%d", got.Kind, len(got.StringValue()), len(big))
	}
}

// TestDetoastAttrLeavesSiblingsUntouched pins the EX1-03b single-column
// guarantee: resolving one attribute performs exactly one DetoastValue
// call and never touches its siblings' pointers.
func TestDetoastAttrLeavesSiblingsUntouched(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()
	// The SQL write path materialises the writer XID for us; the
	// low-level ToastLargeColumnsIfNeeded call below does not, so do it
	// explicitly or the chunks are invisible to our own snapshot.
	if err := ctx.MaterializeWriterXID(); err != nil {
		t.Fatalf("MaterializeWriterXID: %v", err)
	}
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "a", Type: catalog.Type{Name: "text"}, Ordinal: 1},
		{Name: "b", Type: catalog.Type{Name: "text"}, Ordinal: 2},
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 910002, Fork: storage.MainFork}
	valA := toastNarrowBigValue(3000)
	valB := toastNarrowBigValue(4000)
	row := Row{{Kind: KindInt, Int: 1}, NewStringDatum(valA), NewStringDatum(valB)}
	toasted, err := ToastLargeColumnsIfNeeded(ctx, rel, cols, row)
	if err != nil {
		t.Fatalf("ToastLargeColumnsIfNeeded: %v", err)
	}
	if toasted[1].Kind != KindToastPointer || toasted[2].Kind != KindToastPointer {
		t.Fatalf("expected both columns toasted, got kinds %d/%d", toasted[1].Kind, toasted[2].Kind)
	}
	ResetDetoastValueCalls()
	got, err := DetoastAttr(ctx, rel, cols[1], toasted[1])
	if err != nil {
		t.Fatalf("DetoastAttr: %v", err)
	}
	if got.Kind != KindString || got.StringValue() != valA {
		t.Errorf("resolved value mismatch: kind=%d len=%d", got.Kind, len(got.StringValue()))
	}
	if toasted[2].Kind != KindToastPointer {
		t.Errorf("sibling touched: want KindToastPointer, got kind %d", toasted[2].Kind)
	}
	if n := DetoastValueCalls(); n != 1 {
		t.Errorf("DetoastValue calls = %d, want exactly 1", n)
	}
	// A non-pointer datum passes through untouched with zero calls.
	ResetDetoastValueCalls()
	plain := NewStringDatum("small")
	back, err := DetoastAttr(ctx, rel, cols[1], plain)
	if err != nil || back.StringValue() != "small" {
		t.Errorf("non-pointer passthrough: %v %q", err, back.StringValue())
	}
	if n := DetoastValueCalls(); n != 0 {
		t.Errorf("DetoastValue calls for passthrough = %d, want 0", n)
	}
}

// TestDetoastRowBoundResolvesPrefixOnly pins the EX1-03a pairing: only
// i < bound is resolved, the tail is never even inspected (so a stale tail
// pointer can never false-positive the skip-undetoastable path), and a
// full-width bound behaves exactly as DetoastRow.
func TestDetoastRowBoundResolvesPrefixOnly(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()
	// The SQL write path materialises the writer XID for us; the
	// low-level ToastLargeColumnsIfNeeded call below does not, so do it
	// explicitly or the chunks are invisible to our own snapshot.
	if err := ctx.MaterializeWriterXID(); err != nil {
		t.Fatalf("MaterializeWriterXID: %v", err)
	}
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "text"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "text"}, Ordinal: 1},
		{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 2},
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 910003, Fork: storage.MainFork}
	valA := toastNarrowBigValue(3000)
	valC := toastNarrowBigValue(3000)
	row := Row{NewStringDatum(valA), NewStringDatum("inline"), NewStringDatum(valC)}
	toasted, err := ToastLargeColumnsIfNeeded(ctx, rel, cols, row)
	if err != nil {
		t.Fatalf("ToastLargeColumnsIfNeeded: %v", err)
	}
	if toasted[0].Kind != KindToastPointer || toasted[2].Kind != KindToastPointer {
		t.Fatalf("fixture: want pointers at 0 and 2, got %d/%d", toasted[0].Kind, toasted[2].Kind)
	}

	// Bound 1: only position 0 resolves; the tail pointer at 2 is
	// invisible (needsDetoastPrefix false past bound, zero calls when the
	// prefix itself holds no pointer — covered below).
	ResetDetoastValueCalls()
	got, err := DetoastRowBound(ctx, rel, cols, toasted, 1)
	if err != nil {
		t.Fatalf("DetoastRowBound(bound=1): %v", err)
	}
	if got[0].Kind != KindString || got[0].StringValue() != valA {
		t.Errorf("prefix col unresolved: kind=%d", got[0].Kind)
	}
	if got[2].Kind != KindToastPointer {
		t.Errorf("tail touched at bound=1: got kind %d, want KindToastPointer", got[2].Kind)
	}
	if n := DetoastValueCalls(); n != 1 {
		t.Errorf("DetoastValue calls at bound=1 = %d, want 1", n)
	}

	// Stale-tail pairing: a row whose ONLY pointer sits past the bound
	// must come back untouched with zero resolutions.
	stale := Row{NewStringDatum("inline-a"), NewStringDatum("inline-b"), toasted[2]}
	ResetDetoastValueCalls()
	if !needsDetoast(stale) {
		t.Fatal("fixture: stale row must contain a pointer for the pairing to mean anything")
	}
	if needsDetoastPrefix(stale, 2) {
		t.Fatal("prefix scan must not see the stale tail pointer")
	}
	back, err := DetoastRowBound(ctx, rel, cols, stale, 2)
	if err != nil {
		t.Fatalf("DetoastRowBound stale tail: %v", err)
	}
	if back[2].Kind != KindToastPointer {
		t.Errorf("stale tail resolved: got kind %d", back[2].Kind)
	}
	if n := DetoastValueCalls(); n != 0 {
		t.Errorf("DetoastValue calls for stale-tail row = %d, want 0", n)
	}

	// Full-width bound resolves everything, exactly as DetoastRow.
	ResetDetoastValueCalls()
	full, err := DetoastRowBound(ctx, rel, cols, toasted, len(toasted))
	if err != nil {
		t.Fatalf("DetoastRowBound full: %v", err)
	}
	if full[0].StringValue() != valA || full[2].StringValue() != valC {
		t.Errorf("full-width bound left pointers unresolved")
	}
	if n := DetoastValueCalls(); n != 2 {
		t.Errorf("DetoastValue calls full-width = %d, want 2", n)
	}
}

// TestNarrowOnlyQueryPerformsZeroDetoastCalls is the witness in miniature:
// over w(id, narrow, wide) with toasted wide values, a query referencing
// only narrow columns performs ZERO DetoastValue calls, while resolving
// wide costs one call per row — detoast proportional to referenced cols.
func TestNarrowOnlyQueryPerformsZeroDetoastCalls(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()
	wideA := toastNarrowBigValue(3000)
	wideB := toastNarrowBigValue(3500)
	wideC := toastNarrowBigValue(4000)
	toastNarrowFixtureTable(t, ctx, "narrow_counts", "n", []string{wideA, wideB, wideC})

	ResetDetoastValueCalls()
	rows := runQuery(t, ctx, "SELECT narrow FROM narrow_counts ORDER BY id")
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if n := DetoastValueCalls(); n != 0 {
		t.Errorf("narrow-only SELECT: DetoastValue calls = %d, want 0", n)
	}

	ResetDetoastValueCalls()
	rows = runQuery(t, ctx, "SELECT id FROM narrow_counts ORDER BY id")
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if n := DetoastValueCalls(); n != 0 {
		t.Errorf("id-only SELECT: DetoastValue calls = %d, want 0", n)
	}

	ResetDetoastValueCalls()
	rows = runQuery(t, ctx, "SELECT wide FROM narrow_counts ORDER BY id")
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0][0].StringValue() != wideA || rows[1][0].StringValue() != wideB || rows[2][0].StringValue() != wideC {
		t.Fatalf("wide values mismatch")
	}
	if n := DetoastValueCalls(); n != 3 {
		t.Errorf("wide SELECT: DetoastValue calls = %d, want 3 (one per row)", n)
	}
}

// TestUpdateSetAttrLazyLeavesSiblingPointer pins the EX1-03b adoption: an
// UPDATE whose SET-clause reads only one toasted column resolves exactly
// that column in the SET step; the sibling keeps its pointer and its
// value. The DetoastValue counter is statement-wide (the WHERE-predicate
// and RETURNING paths stay whole-row by design), so the adoption is pinned
// two ways: a direct detoastUpdateEvalRow unit test with exact counts, and
// an end-to-end comparison showing the attributed shape costs exactly one
// fewer resolution than the declined (CASE) shape on identical tables.
func TestUpdateSetAttrLazyLeavesSiblingPointer(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "narrow", Type: catalog.Type{Name: "text"}, Ordinal: 1},
		{Name: "wide", Type: catalog.Type{Name: "text"}, Ordinal: 2},
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 910005, Fork: storage.MainFork}
	if err := ctx.MaterializeWriterXID(); err != nil {
		t.Fatalf("MaterializeWriterXID: %v", err)
	}
	narrow := toastNarrowBigValue(3000)
	wide := toastNarrowBigValue(4000)
	toasted, err := ToastLargeColumnsIfNeeded(ctx, rel, cols, Row{
		{Kind: KindInt, Int: 1}, NewStringDatum(narrow), NewStringDatum(wide),
	})
	if err != nil {
		t.Fatalf("ToastLargeColumnsIfNeeded: %v", err)
	}

	// Attributed refs (narrow only): exactly one resolution, sibling intact.
	ResetDetoastValueCalls()
	evalRow, err := detoastUpdateEvalRow(ctx, rel, cols, toasted, []int{1}, false)
	if err != nil {
		t.Fatalf("detoastUpdateEvalRow attributed: %v", err)
	}
	if evalRow[1].Kind != KindString || evalRow[1].StringValue() != narrow {
		t.Errorf("attributed col unresolved: kind=%d", evalRow[1].Kind)
	}
	if evalRow[2].Kind != KindToastPointer {
		t.Errorf("sibling touched: got kind %d, want KindToastPointer", evalRow[2].Kind)
	}
	if n := DetoastValueCalls(); n != 1 {
		t.Errorf("attributed eval row: DetoastValue calls = %d, want 1", n)
	}

	// Declined shape: whole-row fallback at whole-row cost, same answers.
	ResetDetoastValueCalls()
	fullRow, err := detoastUpdateEvalRow(ctx, rel, cols, toasted, nil, true)
	if err != nil {
		t.Fatalf("detoastUpdateEvalRow fallback: %v", err)
	}
	if fullRow[1].StringValue() != narrow || fullRow[2].StringValue() != wide {
		t.Errorf("fallback eval row mismatch")
	}
	if n := DetoastValueCalls(); n != 2 {
		t.Errorf("fallback eval row: DetoastValue calls = %d, want 2", n)
	}

	// No attributed reads at all: the row passes through untouched.
	ResetDetoastValueCalls()
	untouched, err := detoastUpdateEvalRow(ctx, rel, cols, toasted, []int{}, false)
	if err != nil {
		t.Fatalf("detoastUpdateEvalRow no-refs: %v", err)
	}
	if untouched[1].Kind != KindToastPointer || untouched[2].Kind != KindToastPointer {
		t.Errorf("no-refs eval row resolved pointers: kinds %d/%d", untouched[1].Kind, untouched[2].Kind)
	}
	if n := DetoastValueCalls(); n != 0 {
		t.Errorf("no-refs eval row: DetoastValue calls = %d, want 0", n)
	}
}

// TestUpdateSetAttrLazyEndToEnd exercises the adopted site through SQL:
// both SET shapes produce identical answers with the sibling preserved,
// and the attributed shape costs exactly one fewer DetoastValue call than
// the declined shape (isolating the SET step; predicate and RETURNING
// paths are whole-row in both).
func TestUpdateSetAttrLazyEndToEnd(t *testing.T) {
	runShape := func(t *testing.T, tbl, setExpr string) (int64, string) {
		t.Helper()
		ctx, cleanup := newToastFixture(t)
		defer cleanup()
		narrow := toastNarrowBigValue(3000)
		wide := toastNarrowBigValue(4000)
		toastNarrowFixtureTable(t, ctx, tbl, narrow, []string{wide})
		ResetDetoastValueCalls()
		ret := runQuery(t, ctx, "UPDATE "+tbl+" SET narrow = "+setExpr+" WHERE id = 1 RETURNING narrow")
		if len(ret) != 1 {
			t.Fatalf("%s: expected 1 RETURNING row, got %d", tbl, len(ret))
		}
		calls := DetoastValueCalls()
		rows := runQuery(t, ctx, "SELECT wide FROM "+tbl+" WHERE id = 1")
		if len(rows) != 1 || rows[0][0].StringValue() != wide {
			t.Fatalf("%s: sibling wide corrupted by UPDATE", tbl)
		}
		return calls, ret[0][0].StringValue()
	}
	narrow := toastNarrowBigValue(3000)
	attrCalls, attrVal := runShape(t, "narrow_upd", "narrow || '-s'")
	if want := narrow + "-s"; attrVal != want {
		t.Errorf("attributed UPDATE: got %d bytes, want %d", len(attrVal), len(want))
	}
	fallbackCalls, fallbackVal := runShape(t, "narrow_upd2", "CASE WHEN id > 0 THEN narrow ELSE narrow END")
	if fallbackVal != narrow {
		t.Errorf("fallback UPDATE: got %d bytes, want %d", len(fallbackVal), len(narrow))
	}
	if diff := fallbackCalls - attrCalls; diff != 1 {
		t.Errorf("SET-step detoast gap = %d calls, want 1 (attributed=%d fallback=%d)", diff, attrCalls, fallbackCalls)
	}
}

// TestSilentSinksNeverObservePointers is the barrier audit with no lazy
// movement: every silent sink in the design table is driven with toasted
// inputs and must produce exactly the detoasted answers. A pointer
// reaching any of them is wrong answers, not an error — so exact
// value/count assertions ARE the audit. W1/W2/W3 are pairwise distinct;
// the insert order (W3, W1, W2) deliberately differs from lexicographic
// order so a pointer-OID comparison cannot accidentally agree.
func TestSilentSinksNeverObservePointers(t *testing.T) {
	ctx, cleanup := newToastFixture(t)
	defer cleanup()
	w1 := "A" + toastNarrowBigValue(3000)
	w2 := "M" + toastNarrowBigValue(3000)
	w3 := "Z" + toastNarrowBigValue(3000)
	// id order: 1->W3, 2->W1, 3->W2, 4->W1 (W1 duplicated for grouping).
	toastNarrowFixtureTable(t, ctx, "narrow_sink", "n", []string{w3, w1, w2, w1})

	// Hash/group keys via datumKey: GROUP BY wide must group the two W1
	// rows together (raw pointers are per-row distinct and would not).
	rows := runQuery(t, ctx, "SELECT count(*), min(id) FROM narrow_sink GROUP BY wide ORDER BY min(id)")
	if len(rows) != 3 {
		t.Fatalf("GROUP BY wide: want 3 groups, got %d", len(rows))
	}
	// Groups ordered by min(id): {W3:1x id1}, {W1:2x ids2,4}, {W2:1x id3}.
	if rows[0][0].Int != 1 || rows[0][1].Int != 1 {
		t.Errorf("GROUP BY group 1: got count=%d minid=%d, want 1/1", rows[0][0].Int, rows[0][1].Int)
	}
	if rows[1][0].Int != 2 || rows[1][1].Int != 2 {
		t.Errorf("GROUP BY group 2: got count=%d minid=%d, want 2/2", rows[1][0].Int, rows[1][1].Int)
	}
	if rows[2][0].Int != 1 || rows[2][1].Int != 3 {
		t.Errorf("GROUP BY group 3: got count=%d minid=%d, want 1/3", rows[2][0].Int, rows[2][1].Int)
	}

	// DISTINCT rowKey + DISTINCT sort comparator: 3 distinct values.
	rows = runQuery(t, ctx, "SELECT DISTINCT wide FROM narrow_sink")
	if len(rows) != 3 {
		t.Fatalf("DISTINCT wide: want 3 rows, got %d", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r[0].StringValue()] = true
	}
	if !seen[w1] || !seen[w2] || !seen[w3] {
		t.Errorf("DISTINCT wide missed a value (got %d distinct)", len(seen))
	}

	// ORDER BY wide (sort comparator): lexicographic, not OID order.
	rows = runQuery(t, ctx, "SELECT id FROM narrow_sink ORDER BY wide, id")
	if len(rows) != 4 {
		t.Fatalf("ORDER BY wide: want 4 rows, got %d", len(rows))
	}
	wantOrder := []int64{2, 4, 3, 1} // W1(ids2,4), W2(id3), W3(id1)
	for i, want := range wantOrder {
		if rows[i][0].Int != want {
			t.Errorf("ORDER BY wide row %d: got id %d, want %d", i, rows[i][0].Int, want)
		}
	}

	// Hash join on wide: the W1 pair matches (2 rows); pointer keys would
	// match nothing (every pointer OID is distinct).
	rows = runQuery(t, ctx, "SELECT a.id, b.id FROM narrow_sink a JOIN narrow_sink b ON a.wide = b.wide AND a.id < b.id ORDER BY a.id, b.id")
	if len(rows) != 1 || rows[0][0].Int != 2 || rows[0][1].Int != 4 {
		t.Errorf("self-join on wide: got %+v, want [{2 4}]", rows)
	}

	// Aggregate transfn inputs via StringValue/Format: min/max over the
	// real values (raw pointer bytes would sort by OID, not content).
	rows = runQuery(t, ctx, "SELECT min(wide), max(wide), count(wide) FROM narrow_sink")
	if len(rows) != 1 {
		t.Fatalf("agg over wide: want 1 row, got %d", len(rows))
	}
	if rows[0][0].StringValue() != w1 || rows[0][1].StringValue() != w3 || rows[0][2].Int != 4 {
		t.Errorf("agg over wide mismatch: min=%dB max=%dB count=%d", len(rows[0][0].StringValue()), len(rows[0][1].StringValue()), rows[0][2].Int)
	}

	// Window partition key + peer compare: PARTITION BY wide groups the W1
	// pair (row_numbers 1,2); rank() OVER (ORDER BY wide) exercises
	// samePeer/compareDatum — a same-kind pointer compare would ERROR
	// (42883), so correct ranks prove detoasted comparison.
	rows = runQuery(t, ctx, "SELECT id, row_number() OVER (PARTITION BY wide ORDER BY id) FROM narrow_sink ORDER BY id")
	if len(rows) != 4 {
		t.Fatalf("window partition: want 4 rows, got %d", len(rows))
	}
	wantRN := []int64{1, 1, 1, 2} // id1(W3):1, id2(W1):1, id3(W2):1, id4(W1):2
	for i, want := range wantRN {
		if rows[i][1].Int != want {
			t.Errorf("row_number id=%d: got %d, want %d", rows[i][0].Int, rows[i][1].Int, want)
		}
	}
	rows = runQuery(t, ctx, "SELECT id, rank() OVER (ORDER BY wide) FROM narrow_sink ORDER BY id")
	if len(rows) != 4 {
		t.Fatalf("window rank: want 4 rows, got %d", len(rows))
	}
	wantRank := []int64{4, 1, 3, 1} // W3->4, W1->1,1, W2->3
	for i, want := range wantRank {
		if rows[i][1].Int != want {
			t.Errorf("rank id=%d: got %d, want %d", rows[i][0].Int, rows[i][1].Int, want)
		}
	}

	// Recursive-UNION / UNION dedup via rowKey: 3 distinct wide values.
	rows = runQuery(t, ctx, "SELECT wide FROM narrow_sink WHERE id <= 2 UNION SELECT wide FROM narrow_sink WHERE id >= 3")
	if len(rows) != 3 {
		t.Fatalf("UNION over wide: want 3 rows, got %d", len(rows))
	}

	// Correlated EXISTS (memoize probe key when the planner memoizes):
	// ids sharing their wide value with another row.
	rows = runQuery(t, ctx, "SELECT id FROM narrow_sink o WHERE EXISTS (SELECT 1 FROM narrow_sink i WHERE i.wide = o.wide AND i.id <> o.id) ORDER BY id")
	if len(rows) != 2 || rows[0][0].Int != 2 || rows[1][0].Int != 4 {
		t.Errorf("correlated EXISTS over wide: got %+v, want ids [2 4]", rows)
	}

	// WHERE on wide (predicate over detoasted values; the reference value
	// is fetched by subquery so no random bytes enter the SQL text).
	rows = runQuery(t, ctx, "SELECT id FROM narrow_sink WHERE wide = (SELECT wide FROM narrow_sink WHERE id = 3)")
	if len(rows) != 1 || rows[0][0].Int != 3 {
		t.Errorf("WHERE wide = w2: got %+v, want [3]", rows)
	}
}

// TestLoudSinksKeepErroring pins the loud half of the barrier table: a
// pointer reaching COPY TO, pgIndexTupleKey, or a same-kind compareDatum
// must ERROR, never silently render or compare.
func TestLoudSinksKeepErroring(t *testing.T) {
	ptr := NewToastPointerDatum(make([]byte, 12))

	// COPY TO renders through datumToCopyText: a pointer has no text form.
	if _, err := datumToCopyText(catalog.Type{Name: "text"}, ptr, "ISO", "MDY", "UTC", "hex", nil, false); err == nil {
		t.Errorf("COPY TO accepted an unresolved TOAST pointer; must error")
	}

	// Index build keys refuse pointers (PG detoasts before index_form_tuple).
	tbl := keyDescTable(col("a", "text"))
	desc, keyCols, _ := tupleKeyIndex(t, tbl, "a")
	if _, _, err := pgIndexTupleKey(desc, keyCols, []Datum{ptr}, tupleKeyHeapTID); err == nil {
		t.Errorf("pgIndexTupleKey accepted an unresolved TOAST pointer; must error")
	}

	// Same-kind pointer-vs-pointer comparison has no ordering (42883).
	if _, err := compareDatum(ptr, NewToastPointerDatum(make([]byte, 12)), 0); err == nil {
		t.Errorf("compareDatum(pointer, pointer) succeeded; want 42883")
	} else if !strings.Contains(err.Error(), "42883") {
		t.Errorf("compareDatum(pointer, pointer) error = %v, want 42883", err)
	}
}

// TestNumericNeverToasts is the negative test: fixed-width/numeric types
// are outside isToastableType, so no pointer is ever produced for them and
// full selects cost zero resolutions.
func TestNumericNeverToasts(t *testing.T) {
	if isToastableType("numeric") || isToastableType("int4") || isToastableType("float8") {
		t.Fatalf("numeric/fixed-width types must not be toastable")
	}
	ctx, cleanup := newToastFixture(t)
	defer cleanup()
	// The SQL write path materialises the writer XID for us; the
	// low-level ToastLargeColumnsIfNeeded call below does not, so do it
	// explicitly or the chunks are invisible to our own snapshot.
	if err := ctx.MaterializeWriterXID(); err != nil {
		t.Fatalf("MaterializeWriterXID: %v", err)
	}
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "n", Type: catalog.Type{Name: "numeric"}, Ordinal: 1},
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 910004, Fork: storage.MainFork}
	row := Row{{Kind: KindInt, Int: 1}, numericFromInt(12345)}
	out, err := ToastLargeColumnsIfNeeded(ctx, rel, cols, row)
	if err != nil {
		t.Fatalf("ToastLargeColumnsIfNeeded: %v", err)
	}
	if out[1].Kind == KindToastPointer {
		t.Fatalf("numeric value was toasted; fixed-width types must stay inline")
	}

	if err := runDDL(t, ctx, "CREATE TABLE narrow_num (id int, n numeric)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO narrow_num VALUES (1, 123456789.123456789)"); err != nil {
		t.Fatal(err)
	}
	ResetDetoastValueCalls()
	rows := runQuery(t, ctx, "SELECT id, n FROM narrow_num WHERE id = 1")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if n := DetoastValueCalls(); n != 0 {
		t.Errorf("numeric SELECT: DetoastValue calls = %d, want 0", n)
	}
}

