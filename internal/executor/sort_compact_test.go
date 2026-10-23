package executor

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// TestCompactSortMatchesLiveSort pins M0122-0012 slice 83: a live sort whose
// only reader is a Gather Merge (compactOut) stores compact rows — its live
// cells alone — and emits each through one reused full-width row. Read the
// way that reader reads (each row's cells copied at once), it must return
// exactly what the plain live sort returns: the same order, the same live
// cells, every dead cell NULL. Both in memory, where the rows stay compact,
// and with a chunk limit small enough to spill, where the rows are expanded
// back before the first spill and the merge reads full-width rows.
func TestCompactSortMatchesLiveSort(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })
	rng := rand.New(rand.NewSource(83))
	var rows []Row
	for i := range 600 {
		rows = append(rows, Row{
			NewIntDatum(int64(i)),                                // dead
			NewStringDatum(fmt.Sprintf("s%03d", rng.Intn(1000))), // live
			NewIntDatum(rng.Int63n(50)),                          // live, the key
			NewStringDatum("dead tail"),                          // dead
		})
	}
	live := []bool{false, true, true}
	keys := []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: 2}}, {Expr: &optimizer.ColumnRef{Index: 1}}}
	drain := func(compactOut bool, limit int64) ([]Row, *sortOp) {
		s := &sortOp{child: &fakeBorrowSource{rows: rows}, keys: keys, live: live, compactOut: compactOut, chunkLimitBytes: limit}
		if err := s.Open(&Context{}); err != nil {
			t.Fatalf("Open: %v", err)
		}
		var out []Row
		for {
			slot, err := s.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			out = append(out, append(Row(nil), slot.Row()...)) // the reader copies at once
		}
		return out, s
	}
	for _, limit := range []int64{0, 4096} {
		want, plain := drain(false, limit)
		got, s := drain(true, limit)
		if limit > 0 && (len(s.spillFiles) == 0 || s.compact) {
			t.Fatalf("limit %d: spills=%d compact=%v, want a spill and compact mode left", limit, len(s.spillFiles), s.compact)
		}
		if limit == 0 && (!s.compact || len(s.spillFiles) != 0) {
			t.Fatalf("in-memory: compact=%v spills=%d, want compact rows and no spill", s.compact, len(s.spillFiles))
		}
		if len(got) != len(want) || len(got) != len(rows) {
			t.Fatalf("limit %d: %d rows, want %d", limit, len(got), len(want))
		}
		for i := range want {
			for c := range 4 {
				if !sameResult(got[i][c], nil, want[i][c], nil) {
					t.Fatalf("limit %d row %d col %d: %v, want %v", limit, i, c, got[i][c], want[i][c])
				}
			}
			if !got[i][0].IsNull() || !got[i][3].IsNull() {
				t.Fatalf("limit %d row %d: dead cells %v / %v, want NULL", limit, i, got[i][0], got[i][3])
			}
		}
		_ = plain.Close()
		_ = s.Close()
	}
}

// arenaPageSource is a scan whose string cells live in a per-"page" arena
// reset before every row, as a seq scan resets its page arena: a value that
// aliases the arena past the next row reads someone else's bytes.
type arenaPageSource struct {
	vals  []string
	idx   int
	arena *mmgr.Context
}

func (o *arenaPageSource) Open(*Context) error {
	o.idx = 0
	if o.arena == nil {
		o.arena = mmgr.Acquire(nil, mmgr.KindStmt)
	}
	return nil
}
func (o *arenaPageSource) Schema() optimizer.Schema { return nil }
func (o *arenaPageSource) Close() error             { return nil }
func (o *arenaPageSource) Next() (TupleSlot, error) {
	if o.idx >= len(o.vals) {
		return nil, EOF
	}
	o.arena.Reset()
	off, ln := o.arena.AllocString(o.vals[o.idx])
	row := Row{newStringArenaDatum(o.arena, off, ln), NewIntDatum(int64(o.idx))}
	o.idx++
	return &MaterializedSlot{row: row}, nil
}

// TestCompactSortKeysSurviveTheArena pins the bug slice 83's first cut had:
// keys evaluated on the incoming slot. substr over an arena-backed cell
// returns a string aliasing the arena with no ArenaID, so detaching the key
// did nothing and the next row's reset rewrote it — TPC-H Q22 sorted every
// row on "  ". The keys must be evaluated on the detached cells: the compact
// sort must order exactly as the plain live sort does, by substr(s, 1, 2).
func TestCompactSortKeysSurviveTheArena(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })
	rng := rand.New(rand.NewSource(22))
	var vals []string
	for range 300 {
		vals = append(vals, fmt.Sprintf("%02d-%03d-%04d  ", 10+rng.Intn(25), rng.Intn(1000), rng.Intn(10000)))
	}
	key := &optimizer.FuncCall{Name: "substr", Args: []optimizer.Expr{
		&optimizer.ColumnRef{Index: 0, Type: catalog.Type{Name: "text"}}, &optimizer.IntegerConst{Value: 1}, &optimizer.IntegerConst{Value: 2},
	}}
	keys := []optimizer.SortKey{{Expr: key}, {Expr: &optimizer.ColumnRef{Index: 1}}}
	drain := func(compactOut bool) []string {
		s := &sortOp{child: &arenaPageSource{vals: vals}, keys: keys, live: []bool{true, true}, compactOut: compactOut}
		if err := s.Open(&Context{}); err != nil {
			t.Fatalf("Open: %v", err)
		}
		var out []string
		for {
			slot, err := s.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			out = append(out, slot.Row()[0].StringValue())
		}
		return out
	}
	want, got := drain(false), drain(true)
	if len(got) != len(vals) || len(want) != len(vals) {
		t.Fatalf("%d / %d rows, want %d", len(got), len(want), len(vals))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: compact %q, live %q", i, got[i], want[i])
		}
		if i > 0 && want[i][:2] < want[i-1][:2] {
			t.Fatalf("row %d: %q after %q — not ordered by substr(s, 1, 2)", i, want[i], want[i-1])
		}
	}
}
