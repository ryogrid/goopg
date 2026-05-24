package executor

// operators_setop_test.go — end-to-end multiset semantics for the SQL set
// operations UNION [ALL] / INTERSECT [ALL] / EXCEPT [ALL], plus a trailing
// positional ORDER BY (the shape copyselect uses). M0097-0024.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// setOpFixture creates two single-column int tables with deliberately
// asymmetric multiplicities so that DISTINCT vs ALL and the three operations
// all produce distinct row counts:
//
//	a: 1,1,2,3,3,3   (counts: 1→2, 2→1, 3→3)
//	b: 1,1,1,3,4     (counts: 1→3, 3→1, 4→1)
func setOpFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{"CREATE TABLE a (x int)", "CREATE TABLE b (x int)"} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}
	load := func(tbl string, vals []int64) {
		tb, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: tbl})
		rel := ctx.Catalog.RelFileNode(tb)
		for _, v := range vals {
			if err := writeHeapRow(ctx, rel, tb.Columns, Row{{Kind: KindInt, Int: v}}); err != nil {
				cleanup()
				t.Fatalf("load %s=%d: %v", tbl, v, err)
			}
		}
	}
	load("a", []int64{1, 1, 2, 3, 3, 3})
	load("b", []int64{1, 1, 1, 3, 4})
	return ctx, cleanup
}

// multiset turns single-column int rows into a value→count map.
func multiset(t *testing.T, rows []Row) map[int64]int {
	t.Helper()
	m := make(map[int64]int)
	for _, r := range rows {
		if len(r) != 1 || r[0].Kind != KindInt {
			t.Fatalf("unexpected row shape %+v", r)
		}
		m[r[0].Int]++
	}
	return m
}

func eqMultiset(a, b map[int64]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestSetOpMultisetSemantics(t *testing.T) {
	ctx, cleanup := setOpFixture(t)
	defer cleanup()

	cases := []struct {
		name string
		sql  string
		want map[int64]int
	}{
		{"union_all", "SELECT x FROM a UNION ALL SELECT x FROM b",
			map[int64]int{1: 5, 2: 1, 3: 4, 4: 1}}, // every row from both
		{"union_distinct", "SELECT x FROM a UNION SELECT x FROM b",
			map[int64]int{1: 1, 2: 1, 3: 1, 4: 1}},
		{"intersect_distinct", "SELECT x FROM a INTERSECT SELECT x FROM b",
			map[int64]int{1: 1, 3: 1}},
		{"intersect_all", "SELECT x FROM a INTERSECT ALL SELECT x FROM b",
			map[int64]int{1: 2, 3: 1}}, // min(2,3)=2, min(3,1)=1
		{"except_distinct", "SELECT x FROM a EXCEPT SELECT x FROM b",
			map[int64]int{2: 1}},
		{"except_all", "SELECT x FROM a EXCEPT ALL SELECT x FROM b",
			map[int64]int{2: 1, 3: 2}}, // max(2-3,0)=0, max(1-0,0)=1, max(3-1,0)=2
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := multiset(t, runQuery(t, ctx, c.sql))
			if !eqMultiset(got, c.want) {
				t.Errorf("%s\n got=%v\nwant=%v", c.sql, got, c.want)
			}
		})
	}
}

// TestSetOpOrderByPosition pins the copyselect shape: a positional ORDER BY
// applied to the combined UNION result.
func TestSetOpOrderByPosition(t *testing.T) {
	ctx, cleanup := setOpFixture(t)
	defer cleanup()

	rows := runQuery(t, ctx, "SELECT x FROM a UNION SELECT x FROM b ORDER BY 1")
	want := []int64{1, 2, 3, 4}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i][0].Kind != KindInt || rows[i][0].Int != w {
			t.Errorf("row %d = %+v, want %d", i, rows[i][0], w)
		}
	}
}
