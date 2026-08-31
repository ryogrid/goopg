package executor

import (
	"fmt"
	"strings"
	"testing"
)

// TestRangeScanExclusiveBoundaryAndNull is the M0134-0001 S4 (class 8) row-parity
// guard: after making the btree range scan stop at an EXCLUSIVE bound for a
// strict op (`WHERE c2 < 100` → `Index Cond: (c2 < 100)` with no Filter), the
// scan must return IDENTICAL rows to the pre-change inclusive-scan + Filter
// behavior: the boundary value (`c2 = 100`) and the NULL row (`c2 IS NULL`) are
// both excluded, and every row 1..99 is present.
//
// The covering query `SELECT c2 FROM t WHERE c2 < 100` IS promoted to an
// IndexOnlyScan. It briefly was not: indexOnlyScanOp scanned inclusively and
// could not express the strict bound, so promotion would have leaked c2=100,
// and Option B (2026-08-15) had tryPromoteIndexOnlyScan refuse the promotion.
// The operator now threads LowOp/HighOp into its btree range scan (Option A),
// so promotion is back on and the IOS renders `Index Cond: (c2 < 100)` while
// still excluding the boundary row.
func TestRangeScanExclusiveBoundaryAndNull(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (c1 int, c2 int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_c2 ON t(c2)"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 100; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO t VALUES (%d, %d)", i, i)); err != nil {
			t.Fatal(err)
		}
	}
	// NULL row: not indexed (NULLs don't participate), so the exclusive scan
	// must not emit it and the old Filter would not have kept it either.
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1000, NULL)"); err != nil {
		t.Fatal(err)
	}

	// Covering single-conjunct strict range: promoted to an IndexOnlyScan that
	// carries the exclusive bound itself (Option A).
	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT c2 FROM t WHERE c2 < 100")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Index Only Scan") {
		t.Errorf("expected Index Only Scan (exclusive bound is IOS-expressible); got:\n%s", joined)
	}
	if !strings.Contains(joined, "Index Cond: (c2 < 100)") {
		t.Errorf("expected exclusive `Index Cond: (c2 < 100)`; got:\n%s", joined)
	}
	if strings.Contains(joined, "Filter:") {
		t.Errorf("redundant Filter should be dropped for a single range conjunct; got:\n%s", joined)
	}

	rows := runQuery(t, ctx, "SELECT c2 FROM t WHERE c2 < 100")
	if len(rows) != 99 {
		t.Fatalf("got %d rows, want 99 (c2 1..99; boundary c2=100 and NULL excluded)", len(rows))
	}
	seen := make(map[int64]bool, len(rows))
	for _, r := range rows {
		if len(r) < 1 {
			t.Fatalf("row has no columns: %v", r)
		}
		if r[0].IsNull() {
			t.Fatal("NULL row emitted by exclusive scan")
		}
		v := r[0].Int
		if v == 100 {
			t.Fatal("boundary c2=100 emitted by exclusive scan")
		}
		if v < 1 || v > 99 {
			t.Fatalf("unexpected c2=%d in result", v)
		}
		seen[v] = true
	}
	if len(seen) != 99 {
		t.Fatalf("got %d distinct c2 values, want 99", len(seen))
	}
}

// TestRangeScanExclusiveUpperBoundTwin is the exclusive-LO twin of
// TestRangeScanExclusiveBoundaryAndNull (reviewer nit, M0134-0001 S4): a single
// `WHERE c2 > 100` on a single-column index must drop the Filter, render
// `Index Cond: (c2 > 100)`, and return exactly the rows above the boundary —
// c2=100 and the NULL row excluded, 101..110 present.
func TestRangeScanExclusiveUpperBoundTwin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (c1 int, c2 int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_c2 ON t(c2)"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 110; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO t VALUES (%d, %d)", i, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1000, NULL)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT c2 FROM t WHERE c2 > 100")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Index Only Scan") {
		t.Errorf("expected Index Only Scan; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Index Cond: (c2 > 100)") {
		t.Errorf("expected exclusive `Index Cond: (c2 > 100)`; got:\n%s", joined)
	}
	if strings.Contains(joined, "Filter:") {
		t.Errorf("redundant Filter should be dropped for a single range conjunct; got:\n%s", joined)
	}

	rows := runQuery(t, ctx, "SELECT c2 FROM t WHERE c2 > 100")
	if len(rows) != 10 {
		t.Fatalf("got %d rows, want 10 (c2 101..110; boundary c2=100 and NULL excluded)", len(rows))
	}
	seen := make(map[int64]bool, len(rows))
	for _, r := range rows {
		if len(r) < 1 {
			t.Fatalf("row has no columns: %v", r)
		}
		if r[0].IsNull() {
			t.Fatal("NULL row emitted by exclusive scan")
		}
		v := r[0].Int
		if v == 100 {
			t.Fatal("boundary c2=100 emitted by exclusive scan")
		}
		if v < 101 || v > 110 {
			t.Fatalf("unexpected c2=%d in result", v)
		}
		seen[v] = true
	}
	if len(seen) != 10 {
		t.Fatalf("got %d distinct c2 values, want 10", len(seen))
	}
}

// TestRangeScanCompositeIndexKeepsFilter verifies the reviewer finding 2 guard
// (M0134-0001 S4): a single-conjunct strict range over a COMPOSITE index
// (e.g. btg_y_x_w on (y, x, w)) must KEEP its `Filter:` line — only
// single-column indexes drop it, because a composite index's trailing columns
// can leak on an exclusive-lo blob-padded bound. Row parity still holds: y<0
// rows returned, boundary y=0 and NULL excluded (the exclusive scan is the
// primary guard, the retained Filter the second).
func TestRangeScanCompositeIndexKeepsFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE btg (y int, x int, w int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX btg_y_x_w ON btg(y, x, w)"); err != nil {
		t.Fatal(err)
	}
	for _, y := range []int{-2, -1, 0, 1, 2} {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO btg VALUES (%d, %d, %d)", y, y*10, y*100)); err != nil {
			t.Fatal(err)
		}
	}
	if err := runDDL(t, ctx, "INSERT INTO btg VALUES (NULL, 7, 8)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT x FROM btg WHERE y < 0")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Index Cond: (y < 0)") {
		t.Errorf("expected `Index Cond: (y < 0)`; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Filter:") {
		t.Errorf("composite index must KEEP its Filter (trailing-column leak guard); got:\n%s", joined)
	}

	rows := runQuery(t, ctx, "SELECT y FROM btg WHERE y < 0")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (y=-2,-1; boundary y=0 and NULL excluded)", len(rows))
	}
	for _, r := range rows {
		if len(r) < 1 {
			t.Fatalf("row has no columns: %v", r)
		}
		if r[0].IsNull() {
			t.Fatal("NULL row emitted")
		}
		v := r[0].Int
		if v >= 0 {
			t.Fatalf("boundary/non-negative y=%d emitted", v)
		}
	}
}

// TestRangeScanVolatileBoundKeepsFilter verifies the reviewer finding 1 guard
// (M0134-0001 S4): a strict range bound that is a function call (`random()`) is
// VOLATILE — the range scan evaluates its bound ONCE, so dropping the Filter
// would stop re-evaluating random() per row (pre-S4 behavior). The Filter must
// be KEPT. goopg has no contain_volatile_functions walker, so the gate is
// conservative: only plain literals/params drop the Filter; a FuncCall bound
// keeps it.
func TestRangeScanVolatileBoundKeepsFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (c1 int, c2 int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_c2 ON t(c2)"); err != nil {
		t.Fatal(err)
	}
	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT c1 FROM t WHERE c2 < random()")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Filter:") {
		t.Errorf("volatile bound `random()` must keep its per-row Filter; got:\n%s", joined)
	}
}
