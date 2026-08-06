package executor

// M0127-P2.2 — executor composite hash keys (design leftdeep-joins/05 §5,
// stage E4).
//
// Two properties need pinning, and only one of them is a row count.
//
//   - THE DEGENERACY WITNESS (the Q78 class). When the first key column is
//     pinned to a constant on both inputs, the pre-P2.2 build put every row in
//     ONE bucket and each probe walked the whole build side — quadratic work
//     behind a plan that looked PG-identical, which is exactly why it survived
//     so long. A row-count test cannot see this: the results were always
//     right. So the test looks at the built table's bucket count directly.
//   - THE ENCODING IS INJECTIVE. A composite key is a concatenation, and a
//     concatenation that is not self-delimiting silently merges distinct keys
//     — ("a\x00b", "c") and ("a", "b\x00c") would collide under the NUL-joined
//     form used elsewhere in this package, and the join would over-emit.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// twoKeyJoinPlan builds an INNER hash join whose ON clause is two int
// equalities, with HashKeys already populated as fillJoinHashKeys would.
// leftWidth columns come from the left input, so the right key indices are
// leftWidth+0 and leftWidth+1.
func twoKeyJoinPlan(leftWidth int) *planner.Join {
	col := func(idx int) *planner.ColumnRef {
		return &planner.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
	}
	return &planner.Join{
		Type:     planner.JoinTypeInner,
		Algo:     planner.JoinAlgoHash,
		LeftKey:  col(0),
		RightKey: col(leftWidth),
		HashKeys: []planner.JoinKeyPair{
			{Left: col(0), Right: col(leftWidth)},
			{Left: col(1), Right: col(leftWidth + 1)},
		},
	}
}

// TestCompositeKeySpreadsBucketsWhenLeadColumnIsPinned is the degeneracy
// regression. Every build row shares the lead key value (the `ws_sold_year =
// 1998` shape qual placement produces on both inputs of Q78's top spine); only
// the second column discriminates. Keying on the pair must give one bucket per
// row, where keying on the lead column alone gave one bucket total.
func TestCompositeKeySpreadsBucketsWhenLeadColumnIsPinned(t *testing.T) {
	const leftWidth = 2
	const nRows = 64

	rows := make([]Row, 0, nRows)
	for i := 0; i < nRows; i++ {
		// col 0 is the pinned year; col 1 discriminates.
		rows = append(rows, Row{NewIntDatum(1998), NewIntDatum(int64(i))})
	}
	child := &bufferReuseOp{rows: rows}
	o := &joinOp{plan: twoKeyJoinPlan(leftWidth), right: child, lazyRW: 2}
	if err := child.Open(nil); err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := o.buildLoopRight(nil, leftWidth); err != nil {
		t.Fatalf("buildLoopRight: %v", err)
	}
	if !o.multiKey() {
		t.Fatalf("join did not adopt both key pairs: execKeys=%d", len(o.execKeys))
	}
	if got := len(o.lazyHash); got != nRows {
		t.Fatalf("build side landed in %d bucket(s) for %d distinct composite keys — "+
			"the pinned lead column is still collapsing the key space (Q78 degeneracy)",
			got, nRows)
	}
	if o.lazyIntHash != nil {
		t.Fatalf("multi-column key built the single-key int map (%d entries)", len(o.lazyIntHash))
	}
	// Both equalities are enforced by the key, so there is nothing left for
	// the per-match interpreted residual to do.
	if o.execResidual != nil {
		t.Errorf("residual survived a fully-equijoin two-key join: %v", o.execResidual)
	}
}

// TestCompositeKeyEncodingIsInjective drives the encoder directly over datums
// whose textual parts concatenate ambiguously. Length prefixes are what make
// this pass; a separator-joined encoding merges the two keys.
func TestCompositeKeyEncodingIsInjective(t *testing.T) {
	o := &joinOp{
		buildKeyExprs: []planner.Expr{
			&planner.ColumnRef{Index: 0},
			&planner.ColumnRef{Index: 1},
		},
	}
	// M0127-PS6.1: the encoder reads compiled slab indices, so a hand-built
	// joinOp (no plan, hence no Open-time initExecKeys) must compile its own.
	o.compileExecExprs()
	encode := func(a, b string) string {
		t.Helper()
		slot := SlotFromRow(nil, Row{NewStringDatum(a), NewStringDatum(b)})
		ok, packMiss, err := o.encodeCompositeKey(o.buildKeyNodes, slot)
		if err != nil || !ok || packMiss {
			t.Fatalf("encode(%q,%q): ok=%v packMiss=%v err=%v", a, b, ok, packMiss, err)
		}
		return string(o.execKeyBuf)
	}
	if k1, k2 := encode("a\x00b", "c"), encode("a", "b\x00c"); k1 == k2 {
		t.Fatalf("ambiguous split collided on key %q — the encoding is not self-delimiting", k1)
	}
	if k1, k2 := encode("ab", "c"), encode("ab", "c"); k1 != k2 {
		t.Fatalf("same key encoded two ways: %q vs %q", k1, k2)
	}
}

// TestCompositeKeyNullColumnMatchesNothing pins the componentwise NULL rule: a
// NULL in ANY key column means the row can match nothing, because `=` is not
// true for a NULL operand. Encoding it as an ordinary value would make NULLs
// join each other.
func TestCompositeKeyNullColumnMatchesNothing(t *testing.T) {
	o := &joinOp{
		buildKeyExprs: []planner.Expr{
			&planner.ColumnRef{Index: 0},
			&planner.ColumnRef{Index: 1},
		},
	}
	o.compileExecExprs()
	slot := SlotFromRow(nil, Row{NewIntDatum(1), NullDatum})
	ok, _, err := o.encodeCompositeKey(o.buildKeyNodes, slot)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if ok {
		t.Fatal("a NULL in the trailing key column produced a usable key")
	}
}

// TestCompositeIntPackDemotionKeepsEveryRow is demoteIntHash's composite twin.
// The plan promises machine ints; a datum that does not fit int64 breaks that
// promise, and the build must re-key what it already filed rather than drop it
// — rows filed before and after the demotion have to land under one key.
func TestCompositeIntPackDemotionKeepsEveryRow(t *testing.T) {
	o := &joinOp{
		execKeys: make([]planner.JoinKeyPair, 2),
		buildKeyExprs: []planner.Expr{
			&planner.ColumnRef{Index: 0},
			&planner.ColumnRef{Index: 1},
		},
		execKeyPackInt: true,
	}
	file := func(a, b Datum, payload string) {
		t.Helper()
		slot := SlotFromRow(nil, Row{a, b})
		ok, err := o.encodeBuildCompositeKey(slot)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !ok {
			t.Fatalf("key rejected for payload %q", payload)
		}
		o.fileCompositeBuildRow(Row{a, b, NewStringDatum(payload)})
	}

	file(NewIntDatum(1998), NewIntDatum(7), "a")
	file(NewIntDatum(1998), NewIntDatum(8), "b")
	if !o.execKeyPackInt {
		t.Fatal("pack lane abandoned before any non-int key")
	}
	// A key the packed lane cannot hold forces the demotion...
	file(NewIntDatum(1998), NewStringDatum("s"), "c")
	if o.execKeyPackInt {
		t.Fatal("pack lane survived a non-int64 key")
	}
	// ...and a row filed afterwards under a previously-packed key must join
	// the rows that were re-keyed out of it.
	file(NewIntDatum(1998), NewIntDatum(7), "d")

	if len(o.lazyHash) != 3 {
		t.Fatalf("string map has %d keys after the demotion, want 3", len(o.lazyHash))
	}
	var seven []Row
	for _, rows := range o.lazyHash {
		if len(rows) == 2 {
			seven = rows
		}
	}
	if seven == nil {
		t.Fatalf("no key holds both (1998,7) rows — the demotion re-keyed them apart: %v", o.lazyHash)
	}
	payloads := map[string]bool{}
	for _, r := range seven {
		payloads[r[2].StringValue()] = true
	}
	if !payloads["a"] || !payloads["d"] {
		t.Errorf("key (1998,7) holds payloads %v, want both \"a\" and \"d\"", payloads)
	}
}

// newTwoKeyJoinFixture builds the end-to-end shape: two tables whose join is
// two int equalities, with the LEAD column pinned to one value on every row
// (the Q78 shape) and NULLs present in both key columns.
func newTwoKeyJoinFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE ck_a (y int, c int, tag text)",
		"CREATE TABLE ck_b (y int, c int, tag text)",
		"INSERT INTO ck_a VALUES (1998, 1, 'a1'), (1998, 2, 'a2'), (1998, 3, 'a3')",
		"INSERT INTO ck_a VALUES (1998, NULL, 'anull'), (NULL, 1, 'aynull')",
		"INSERT INTO ck_b VALUES (1998, 2, 'b2'), (1998, 3, 'b3'), (1998, 4, 'b4')",
		"INSERT INTO ck_b VALUES (1998, NULL, 'bnull'), (NULL, 1, 'bynull')",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

// TestTwoKeyJoinEndToEnd is the results half of the degeneracy fix: keying on
// both columns must not change a single row. NULLs are the part worth spelling
// out — `=` is not true for a NULL operand, so a NULL in EITHER key column
// means the row matches nothing, which is what the encoder's componentwise
// rejection has to reproduce.
func TestTwoKeyJoinEndToEnd(t *testing.T) {
	ctx, cleanup := newTwoKeyJoinFixture(t)
	defer cleanup()

	rows, err := runQueryWithErr(ctx,
		"SELECT ck_a.tag, ck_b.tag FROM ck_a JOIN ck_b ON ck_a.y = ck_b.y AND ck_a.c = ck_b.c ORDER BY ck_a.tag")
	if err != nil {
		t.Fatalf("two-key join: %v", err)
	}
	got := renderRows(rows)
	want := []string{"a2|b2", "a3|b3"}
	if len(got) != len(want) {
		t.Fatalf("two-key join returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q want %q (all: %v)", i, got[i], want[i], got)
		}
	}

	// LEFT JOIN keeps the unmatched outer rows, NULL keys included — the
	// branch that null-pads when the composite lookup reports no match.
	rows, err = runQueryWithErr(ctx,
		"SELECT ck_a.tag, ck_b.tag FROM ck_a LEFT JOIN ck_b ON ck_a.y = ck_b.y AND ck_a.c = ck_b.c ORDER BY ck_a.tag")
	if err != nil {
		t.Fatalf("two-key left join: %v", err)
	}
	if got, want := len(renderRows(rows)), 5; got != want {
		t.Fatalf("LEFT JOIN emitted %d rows, want %d (one per left row): %v",
			got, want, renderRows(rows))
	}
}

// TestTwoKeyJoinUsesBothColumnsInHashCond pins that the query above really
// exercises the composite path rather than passing because the residual
// rescued a single-key hash. EXPLAIN renders the executor-visible key list
// (M0127-P2.1's `Hash Cond:`).
func TestTwoKeyJoinUsesBothColumnsInHashCond(t *testing.T) {
	ctx, cleanup := newTwoKeyJoinFixture(t)
	defer cleanup()

	rows, err := runQueryWithErr(ctx,
		"EXPLAIN SELECT ck_a.tag, ck_b.tag FROM ck_a JOIN ck_b ON ck_a.y = ck_b.y AND ck_a.c = ck_b.c")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var cond string
	for _, line := range renderRows(rows) {
		if strings.Contains(line, "Hash Cond:") {
			cond = line
		}
	}
	if cond == "" {
		t.Skip("plan is not a hash join here; the composite path is covered by the unit tests")
	}
	if !strings.Contains(cond, "AND") {
		t.Fatalf("Hash Cond names one column only (%q) — the two-key join is not keying on both", cond)
	}
}
