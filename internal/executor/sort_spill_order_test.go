package executor

// E-01 (D) — the ordering gate over spilling sorts.
//
// 06 §3 names one thing for the sort work: the comparator warning
// (operators.go:1010-1015) — a chunk sorted by one comparator and merged by
// another emits out-of-order rows with NO error. A row-set assertion passes
// on wrong output, so every test here asserts the SEQUENCE, drained through
// a merge that actually ran, against an INDEPENDENT oracle comparator
// written from the SortKey list — never by calling o.lessKeyVals or
// o.sortKeyVals. Membership is asserted too (multiset equality), because an
// ordering-only assertion passes on a truncated stream.
//
// D-06 extends this file with the packed arm (same drains, switch ON);
// the drain helpers take the built *sortOp so the extension is mechanical.

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// spillOrderSource is a minimal child that rewinds on Open, so Close+Open
// rescan tests (TestSortCloseOpenRescanOrderedTwice) actually re-drain.
// fakeBorrowSource does NOT reset on Open and cannot serve that test.
type spillOrderSource struct {
	rows   []Row
	ctids  []sortCTID
	schema optimizer.Schema
	idx    int
}

func (o *spillOrderSource) Open(*Context) error      { o.idx = 0; return nil }
func (o *spillOrderSource) Schema() optimizer.Schema { return o.schema }
func (o *spillOrderSource) Close() error             { return nil }
func (o *spillOrderSource) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	ms := SlotFromRow(nil, cloneRow(o.rows[o.idx]))
	if o.idx < len(o.ctids) {
		c := o.ctids[o.idx]
		ms.hasCTID = c.has
		ms.ctidBlock = c.block
		ms.ctidOff = c.off
	}
	o.idx++
	return ms, nil
}

// spillOrderFixture returns n two-column rows over small key domains with
// NULLs in the leading key: ties are guaranteed to span run boundaries once
// the chunk limit forces several flushes.
func spillOrderFixture(n int, seed int64) []Row {
	rng := rand.New(rand.NewSource(seed))
	rows := make([]Row, 0, n)
	for i := 0; i < n; i++ {
		var a Datum
		if rng.Intn(10) == 0 {
			a = NullDatum
		} else {
			a = NewIntDatum(int64(rng.Intn(8)))
		}
		var b Datum
		if rng.Intn(10) == 0 {
			b = NullDatum
		} else {
			b = NewIntDatum(int64(rng.Intn(20)))
		}
		rows = append(rows, Row{a, b})
	}
	return rows
}

func spillRowSig(r Row) string {
	s := ""
	for _, d := range r {
		if d.IsNull() {
			s += "N;"
		} else {
			s += fmt.Sprintf("I%d;", d.Int)
		}
	}
	return s
}

// spillOracleLess is the independent ordering oracle: transcribed from the
// SortKey list (Desc/NullsFirst per key), int-domain only. It deliberately
// shares no code with sortKeyVals/lessKeyVals — agreement between the two
// is what the test measures.
func spillOracleLess(a, b Row, keys []optimizer.SortKey) bool {
	for _, k := range keys {
		col := k.Expr.(*optimizer.ColumnRef).Index
		av, bv := a[col], b[col]
		an, bn := av.IsNull(), bv.IsNull()
		if an && bn {
			continue
		}
		if an {
			return k.NullsFirst
		}
		if bn {
			return !k.NullsFirst
		}
		if av.Int == bv.Int {
			continue
		}
		if k.Desc {
			return av.Int > bv.Int
		}
		return av.Int < bv.Int
	}
	return false
}

// drainSortOrdered drains s fully and asserts, in order: (1) every adjacent
// pair satisfies !oracle(cur, prev) — the SEQUENCE, not the set; (2) the
// emitted multiset equals the input multiset — no duplication, no loss
// (the §2.1(a) single-buffer hazard duplicates one row and drops another
// while keeping every adjacent pair ordered, so (1) alone cannot catch
// it). Returns the emitted rows for cross-arm comparison.
func drainSortOrdered(t *testing.T, s *sortOp, keys []optimizer.SortKey, want []Row) []Row {
	t.Helper()
	var prev Row
	var emitted []Row
	wantCount := map[string]int{}
	for _, r := range want {
		wantCount[spillRowSig(r)]++
	}
	gotCount := map[string]int{}
	for i := 0; ; i++ {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next row %d: %v", i, err)
		}
		row := cloneRow(slot.Row())
		if i > 0 && spillOracleLess(row, prev, keys) {
			t.Fatalf("order violated at row %d: %q sorts before %q", i, spillRowSig(row), spillRowSig(prev))
		}
		prev = row
		emitted = append(emitted, row)
		gotCount[spillRowSig(row)]++
	}
	if len(emitted) != len(want) {
		t.Fatalf("emitted %d rows, want %d", len(emitted), len(want))
	}
	for sig, n := range wantCount {
		if gotCount[sig] != n {
			t.Fatalf("multiset mismatch for %q: got %d, want %d", sig, gotCount[sig], n)
		}
	}
	return emitted
}

func spillOrderKeys() []struct {
	name string
	keys []optimizer.SortKey
} {
	col := func(i int) *optimizer.ColumnRef { return &optimizer.ColumnRef{Index: i} }
	return []struct {
		name string
		keys []optimizer.SortKey
	}{
		{"asc-nulls-last", []optimizer.SortKey{{Expr: col(0)}}},
		{"asc-nulls-first", []optimizer.SortKey{{Expr: col(0), NullsFirst: true}}},
		{"desc-nulls-first", []optimizer.SortKey{{Expr: col(0), Desc: true, NullsFirst: true}}},
		{"desc-nulls-last", []optimizer.SortKey{{Expr: col(0), Desc: true}}},
		{"mixed-multikey", []optimizer.SortKey{
			{Expr: col(0), Desc: true},
			{Expr: col(1), NullsFirst: true},
		}},
	}
}

// TestSortSpillOrderingMatrix runs every key shape (§5.1-5.2) twice:
// fully in-memory and spilling (tiny chunk limit, ≥ 8 runs), so the
// tail-vs-merge comparator agreement (§5.3) is exercised on every shape.
func TestSortSpillOrderingMatrix(t *testing.T) {
	const N = 2048
	for _, kc := range spillOrderKeys() {
		for _, spilling := range []bool{false, true} {
			name := kc.name
			if spilling {
				name += "-spilling"
			} else {
				name += "-inmem"
			}
			t.Run(name, func(t *testing.T) {
				rows := spillOrderFixture(N, 0x50DD)
				s := &sortOp{
					child: &spillOrderSource{rows: rows},
					keys:  kc.keys,
				}
				if spilling {
					s.chunkLimitBytes = 1024
				}
				if err := s.Open(&Context{}); err != nil {
					t.Fatalf("Open: %v", err)
				}
				if spilling && len(s.spillFiles) < 8 {
					t.Fatalf("spill arm produced %d runs, want ≥ 8", len(s.spillFiles))
				}
				if !spilling && len(s.spillFiles) != 0 {
					t.Fatalf("in-mem arm spilled: %d files", len(s.spillFiles))
				}
				drainSortOrdered(t, s, kc.keys, rows)
				if err := s.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			})
		}
	}
}

// TestSortCTIDFollowsOwnRow (§5.5): ORDER BY ... FOR UPDATE in-memory —
// each emitted row's TID must be its OWN row's TID after the sort (the
// applySortPerm lockstep across rows/keyvals/ctids). Keys are unique so
// the row→TID mapping is exact.
func TestSortCTIDFollowsOwnRow(t *testing.T) {
	const N = 64
	rng := rand.New(rand.NewSource(0xC71D))
	perm := rng.Perm(N)
	rows := make([]Row, 0, N)
	ctids := make([]sortCTID, 0, N)
	wantTID := map[int64]sortCTID{}
	for _, p := range perm {
		rows = append(rows, Row{NewIntDatum(int64(p))})
		c := sortCTID{block: 0, off: uint16(p), has: true}
		ctids = append(ctids, c)
		wantTID[int64(p)] = c
	}
	s := &sortOp{
		child:     &spillOrderSource{rows: rows, ctids: ctids},
		keys:      []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: 0}}},
		wantCTIDs: true,
	}
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.spillFiles) != 0 {
		t.Fatalf("ctid arm spilled: %d files", len(s.spillFiles))
	}
	for i := 0; i < N; i++ {
		slot, err := s.Next()
		if err != nil {
			t.Fatalf("Next row %d: %v", i, err)
		}
		row := slot.Row()
		if got, want := row[0].Int, int64(i); got != want {
			t.Fatalf("row %d: key %d, want %d", i, got, want)
		}
		ms := slot.(*MaterializedSlot)
		if !ms.hasCTID {
			t.Fatalf("row %d (key %d): no TID re-attached", i, row[0].Int)
		}
		want := wantTID[row[0].Int]
		if ms.ctidBlock != want.block || ms.ctidOff != want.off {
			t.Fatalf("row %d (key %d): TID (%d,%d), want (%d,%d)",
				i, row[0].Int, ms.ctidBlock, ms.ctidOff, want.block, want.off)
		}
	}
	if _, err := s.Next(); err != EOF {
		t.Fatalf("trailing Next: %v, want EOF", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSortChunkLimitSourcesWorkMem (§6.3): the spill threshold is the
// number the planner prices — ctx.WorkMem (session work_mem in bytes)
// when set, today's constant when unset, explicit override first always.
func TestSortChunkLimitSourcesWorkMem(t *testing.T) {
	s := &sortOp{}
	if got := s.chunkLimit(); got != sortChunkBytes {
		t.Fatalf("unset: chunkLimit = %d, want sortChunkBytes %d", got, sortChunkBytes)
	}
	s.ctx = &Context{WorkMem: 64 << 20}
	if got := s.chunkLimit(); got != 64<<20 {
		t.Fatalf("WorkMem set: chunkLimit = %d, want %d", got, 64<<20)
	}
	s.chunkLimitBytes = 1024
	if got := s.chunkLimit(); got != 1024 {
		t.Fatalf("override: chunkLimit = %d, want 1024", got)
	}
}

// — Close, re-Open, and drain the IDENTICAL ordered sequence a second time,
// in-memory and spilling. Pre-(E) the spilling arm fails: mergeReady
// survives Close while heap is nil, so the second Open skips initMerge and
// popMerge dereferences a nil heap (and the surviving keyvals would
// mis-key the tail merge even past that).
func TestSortCloseOpenRescanOrderedTwice(t *testing.T) {
	const N = 512
	keys := []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: 0}}, {Expr: &optimizer.ColumnRef{Index: 1}}}
	for _, spilling := range []bool{false, true} {
		name := "inmem"
		if spilling {
			name = "spilling"
		}
		t.Run(name, func(t *testing.T) {
			rows := spillOrderFixture(N, 0xE5CA)
			s := &sortOp{
				child: &spillOrderSource{rows: rows},
				keys:  keys,
			}
			if spilling {
				s.chunkLimitBytes = 1024
			}
			var first []Row
			for pass := 0; pass < 2; pass++ {
				if err := s.Open(&Context{}); err != nil {
					t.Fatalf("pass %d Open: %v", pass, err)
				}
				got := drainSortOrdered(t, s, keys, rows)
				if pass == 0 {
					first = got
				} else {
					if len(got) != len(first) {
						t.Fatalf("second drain emitted %d rows, first %d", len(got), len(first))
					}
					for i := range got {
						if spillRowSig(got[i]) != spillRowSig(first[i]) {
							t.Fatalf("second drain row %d: %q, first drain %q",
								i, spillRowSig(got[i]), spillRowSig(first[i]))
						}
					}
				}
				if err := s.Close(); err != nil {
					t.Fatalf("pass %d Close: %v", pass, err)
				}
			}
		})
	}
}

// spillOrderIntSchema is the two-int descriptor the D-06 packed arm
// needs: FormPackedTuple encodes through NewTupleDesc(o.Schema()), so a
// nil schema would fail every row LOUDLY (by design — never silently).
func spillOrderIntSchema() optimizer.Schema {
	return optimizer.Schema{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}
}

// TestSortPackedOrderingMatrix (D-06 §5.4) runs the §5 matrix through the
// packed arm and asserts arm equivalence: the same input through the
// switch OFF and ON produces the identical SEQUENCE. A permutation applied
// to packed but not to keyvals (or vice versa) fails here and nowhere else.
func TestSortPackedOrderingMatrix(t *testing.T) {
	const N = 2048
	defer SetSortPackedEnabled(false)
	for _, kc := range spillOrderKeys() {
		for _, spilling := range []bool{false, true} {
			name := kc.name
			if spilling {
				name += "-spilling"
			} else {
				name += "-inmem"
			}
			t.Run(name, func(t *testing.T) {
				var seqs [][]string
				for _, packed := range []bool{false, true} {
					SetSortPackedEnabled(packed)
					rows := spillOrderFixture(N, 0x50DD)
					s := &sortOp{
						child: &spillOrderSource{rows: rows, schema: spillOrderIntSchema()},
						keys:  kc.keys,
					}
					if spilling {
						s.chunkLimitBytes = 1024
					}
					if err := s.Open(&Context{}); err != nil {
						t.Fatalf("packed=%v Open: %v", packed, err)
					}
					if !packed {
						if spilling && len(s.spillFiles) < 8 {
							t.Fatalf("spill arm produced %d runs, want ≥ 8", len(s.spillFiles))
						}
					} else if len(s.packed) == 0 && len(s.spillFiles) == 0 {
						t.Fatalf("packed arm retained nothing (in-mem) and spilled nothing")
					}
					emitted := drainSortOrdered(t, s, kc.keys, rows)
					sigs := make([]string, len(emitted))
					for i, r := range emitted {
						sigs[i] = spillRowSig(r)
					}
					seqs = append(seqs, sigs)
					if err := s.Close(); err != nil {
						t.Fatalf("packed=%v Close: %v", packed, err)
					}
				}
				if len(seqs[0]) != len(seqs[1]) {
					t.Fatalf("arm length mismatch: off=%d on=%d", len(seqs[0]), len(seqs[1]))
				}
				for i := range seqs[0] {
					if seqs[0][i] != seqs[1][i] {
						t.Fatalf("arm sequence mismatch at row %d: off=%q on=%q", i, seqs[0][i], seqs[1][i])
					}
				}
			})
		}
	}
}

// TestSortPackedCTIDFollowsOwnRow (D-06 §5.5, packed side): the TID
// side-channel rides LoadWithTID through the packed sort and each emitted
// row carries its own TID.
func TestSortPackedCTIDFollowsOwnRow(t *testing.T) {
	const N = 64
	defer SetSortPackedEnabled(false)
	SetSortPackedEnabled(true)
	rng := rand.New(rand.NewSource(0xC71D))
	perm := rng.Perm(N)
	rows := make([]Row, 0, N)
	ctids := make([]sortCTID, 0, N)
	wantTID := map[int64]sortCTID{}
	for _, p := range perm {
		rows = append(rows, Row{NewIntDatum(int64(p))})
		c := sortCTID{block: 0, off: uint16(p), has: true}
		ctids = append(ctids, c)
		wantTID[int64(p)] = c
	}
	// Single-column schema for the single-column rows above.
	schema := optimizer.Schema{{Name: "a", Type: catalog.Type{Name: "int4"}}}
	s := &sortOp{
		child:     &spillOrderSource{rows: rows, ctids: ctids, schema: schema},
		keys:      []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: 0}}},
		wantCTIDs: true,
	}
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < N; i++ {
		slot, err := s.Next()
		if err != nil {
			t.Fatalf("Next row %d: %v", i, err)
		}
		row := slot.Row()
		if got, want := row[0].Int, int64(i); got != want {
			t.Fatalf("row %d: key %d, want %d", i, got, want)
		}
		ps, ok := slot.(*PackedSlot)
		if !ok {
			t.Fatalf("row %d: packed arm returned %T, want *PackedSlot", i, slot)
		}
		blk, off, has := ps.TID()
		want := wantTID[row[0].Int]
		if !has || blk != want.block || off != want.off {
			t.Fatalf("row %d (key %d): TID (%d,%d,%v), want (%d,%d,true)",
				i, row[0].Int, blk, off, has, want.block, want.off)
		}
	}
	if _, err := s.Next(); err != EOF {
		t.Fatalf("trailing Next: %v, want EOF", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
