package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestCompatWindowRowNumberPartitionOrder(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 7}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, row_number() OVER (PARTITION BY grp ORDER BY val) AS rn FROM t ORDER BY grp, val, rn")
	want := []struct{ grp, val, rn int64 }{
		{grp: 1, val: 10, rn: 1},
		{grp: 1, val: 10, rn: 2},
		{grp: 1, val: 20, rn: 3},
		{grp: 2, val: 5, rn: 1},
		{grp: 2, val: 7, rn: 2},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w.grp {
			t.Fatalf("row[%d] grp=%+v want %d", i, rows[i][0], w.grp)
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w.val {
			t.Fatalf("row[%d] val=%+v want %d", i, rows[i][1], w.val)
		}
		if rows[i][2].Kind != KindInt || rows[i][2].Int != w.rn {
			t.Fatalf("row[%d] rn=%+v want %d", i, rows[i][2], w.rn)
		}
	}
}

// TestCompatWindowNamedWindowClause pins the M0020 named-window slice
// end to end: a trailing `WINDOW w AS (...)` clause plus a bare
// `OVER w` reference on two different functions must produce exactly
// the same rows as writing the same PARTITION BY/ORDER BY spec out
// twice inline — the analyzer's resolveNamedWindowRefs copies the
// definition into each reference before the planner ever groups
// window functions by spec.
func TestCompatWindowNamedWindowClause(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 7}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	named := runQuery(t, ctx,
		"SELECT grp, val, row_number() OVER w AS rn, rank() OVER w AS rk FROM t "+
			"WINDOW w AS (PARTITION BY grp ORDER BY val) ORDER BY grp, val, rn")
	inline := runQuery(t, ctx,
		"SELECT grp, val, row_number() OVER (PARTITION BY grp ORDER BY val) AS rn, "+
			"rank() OVER (PARTITION BY grp ORDER BY val) AS rk FROM t ORDER BY grp, val, rn")

	if len(named) != len(inline) {
		t.Fatalf("named rows=%d inline rows=%d", len(named), len(inline))
	}
	for i := range inline {
		for col := range inline[i] {
			if named[i][col].Format() != inline[i][col].Format() {
				t.Fatalf("row[%d] col[%d] = %+v, want %+v (inline)", i, col, named[i][col], inline[i][col])
			}
		}
	}
}

func TestCompatWindowRankPeerGroups(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (grp int, val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 10}},
		{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 20}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
		{{Kind: KindInt, Int: 2}, {Kind: KindInt, Int: 5}},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT grp, val, rank() OVER (PARTITION BY grp ORDER BY val) AS rk FROM t ORDER BY grp, val, rk")
	want := []struct{ grp, val, rk int64 }{
		{grp: 1, val: 10, rk: 1},
		{grp: 1, val: 10, rk: 1},
		{grp: 1, val: 20, rk: 3},
		{grp: 2, val: 5, rk: 1},
		{grp: 2, val: 5, rk: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows=%d want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w.grp {
			t.Fatalf("row[%d] grp=%+v want %d", i, rows[i][0], w.grp)
		}
		if rows[i][1].Kind != KindInt || rows[i][1].Int != w.val {
			t.Fatalf("row[%d] val=%+v want %d", i, rows[i][1], w.val)
		}
		if rows[i][2].Kind != KindInt || rows[i][2].Int != w.rk {
			t.Fatalf("row[%d] rk=%+v want %d", i, rows[i][2], w.rk)
		}
	}
}

func TestCompatWindowRankNullPeersAsc(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (val int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	seed := []Row{
		{{Kind: KindInt, Int: 10}},
		{NullDatum},
		{NullDatum},
	}
	for _, r := range seed {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	// PostgreSQL ORDER BY val ASC (default) puts NULLs LAST.
	// So: 10 gets rank=1, NULLs (peers) get rank=2.
	// Outer ORDER BY rk, val: (10, 1) first, then (NULL, 2), (NULL, 2).
	rows := runQuery(t, ctx,
		"SELECT val, rank() OVER (ORDER BY val) AS rk FROM t ORDER BY rk, val")
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	if rows[0][0].Kind != KindInt || rows[0][0].Int != 10 || rows[0][1].Kind != KindInt || rows[0][1].Int != 1 {
		t.Fatalf("row0=%+v want 10 rank=1", rows[0])
	}
	if !rows[1][0].IsNull() || rows[1][1].Kind != KindInt || rows[1][1].Int != 2 {
		t.Fatalf("row1=%+v want NULL rank=2", rows[1])
	}
	if !rows[2][0].IsNull() || rows[2][1].Kind != KindInt || rows[2][1].Int != 2 {
		t.Fatalf("row2=%+v want NULL rank=2", rows[2])
	}
}
