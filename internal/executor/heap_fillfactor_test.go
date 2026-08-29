package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// tablesampleFixtureLayout creates `name` with the given storage-parameter
// clause, inserts the ten fixture rows, and reports how many tuples landed on
// each heap block.
func tablesampleFixtureLayout(t *testing.T, name, with string) []int {
	t.Helper()
	ctx, cat, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, fmt.Sprintf("CREATE TABLE %s (id int, name text)%s", name, with)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		// repeat(i::text, 200) for single-digit i: a 200-character value, so
		// the marshalled tuple is 24 (t_hoff) + 4 (int4) + 4+200 (varlena) =
		// 232 bytes, already maxaligned.
		val := strings.Repeat(fmt.Sprintf("%d", i), 200)
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO %s VALUES (%d, '%s')", name, i, val)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
	if !ok {
		t.Fatalf("table %q not in catalog", name)
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	n, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	layout := make([]int, 0, n)
	for blk := storage.BlockNumber(0); blk < n; blk++ {
		page := readPageViaPool(t, ctx.Pool, rel, blk)
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			t.Fatalf("PageLinePointerCount(blk=%d): %v", blk, err)
		}
		layout = append(layout, count)
	}
	return layout
}

// TestFillfactorReservesSpaceAtInsert is the M0134-0175a guard: `fillfactor`
// must actually govern how densely INSERT packs heap pages, not merely be
// parsed, persisted to pg_class.reloptions and read by the cost model.
//
// The fixture is upstream's own, chosen by the PostgreSQL authors precisely
// because it produces multiple pages from very little data
// (postgres/src/test/regress/sql/tablesample.sql:1 — "use fillfactor so we
// don't have to load too much data to get multiple pages"). PG 18.3 lands the
// ten rows on FOUR blocks as 3/3/3/1, which is what makes
// `TABLESAMPLE SYSTEM (50) REPEATABLE (0)` return ids 3..8 — blocks 1 and 2.
// Before this fix goopg packed all ten into a single block, so every
// block-addressed sample diverged from the oracle even though the sampler
// arithmetic was exact.
func TestFillfactorReservesSpaceAtInsert(t *testing.T) {
	got := tablesampleFixtureLayout(t, "test_tablesample", " WITH (fillfactor=10)")
	want := []int{3, 3, 3, 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("fillfactor=10 block layout = %v, want %v (PG 18.3 expected/tablesample.out)", got, want)
	}
}

// TestDefaultFillfactorPacksTightly is the control half of the guard above:
// with no fillfactor reloption the reserve is zero, so the same ten rows must
// still share one block. This is what proves the change is inert for every
// table that does not ask for a reserve — the TPC-H/TPC-DS schemas, the
// catalog heaps, and pgbench's tables all take this path, so their on-disk
// density is unchanged.
func TestDefaultFillfactorPacksTightly(t *testing.T) {
	got := tablesampleFixtureLayout(t, "packed_tightly", "")
	want := []int{10}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("default-fillfactor block layout = %v, want %v", got, want)
	}
}

// TestHeapFillfactorMemoTracksAlter checks the per-session memo behaves like
// PG's relcache entry: resolved once, and dropped when ALTER TABLE changes the
// reloption so a later insert re-reads it.
func TestHeapFillfactorMemoTracksAlter(t *testing.T) {
	ctx, cat, cleanup := newHOTFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE ff (id int, name text)"); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "ff"})
	if !ok {
		t.Fatal("table ff not in catalog")
	}
	rel := ctx.Catalog.RelFileNode(tbl)

	if got := ctx.heapFillfactor(rel); got != storage.HeapDefaultFillfactor {
		t.Fatalf("unset fillfactor resolved to %d, want %d", got, storage.HeapDefaultFillfactor)
	}
	if _, memoised := ctx.heapFillfactorCache[rel.RelOid]; !memoised {
		t.Fatal("resolution did not populate the memo")
	}

	if err := runDDL(t, ctx, "ALTER TABLE ff SET (fillfactor=40)"); err != nil {
		t.Fatal(err)
	}
	if _, memoised := ctx.heapFillfactorCache[rel.RelOid]; memoised {
		t.Fatal("ALTER TABLE SET (fillfactor) did not drop the memo entry")
	}
	if got := ctx.heapFillfactor(rel); got != 40 {
		t.Fatalf("after SET (fillfactor=40) resolved to %d, want 40", got)
	}

	if err := runDDL(t, ctx, "ALTER TABLE ff RESET (fillfactor)"); err != nil {
		t.Fatal(err)
	}
	if got := ctx.heapFillfactor(rel); got != storage.HeapDefaultFillfactor {
		t.Fatalf("after RESET (fillfactor) resolved to %d, want %d", got, storage.HeapDefaultFillfactor)
	}
}
