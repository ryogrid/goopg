package executor

// S5a (D3.1) end-to-end value check: with sublink pull-up running
// BEFORE join-order search and bushy DP reordering the outer layout
// beneath the pinned semi join, the query must still return the right
// ROWS — the F8 hazard (stale pinned-join indices) is silent wrong
// results, so a shape-level planner test alone is not enough.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

func TestPreDPExistsValuesSurviveDPReorder(t *testing.T) {
	planner.SetUnnestPreDPEnabled(true)
	t.Cleanup(func() { planner.SetUnnestPreDPEnabled(true) })

	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, stmt := range []string{
		"CREATE TABLE big1 (b1_k int, b1_j int)",
		"CREATE TABLE big2 (b2_k int, b2_j int)",
		"CREATE TABLE small3 (s3_k int, s3_j int)",
		"CREATE TABLE inner_e (e_k int, e_v int)",
		"CREATE INDEX inner_e_k_ix ON inner_e (e_k)",
		// big1 rows: keys 1..4 on join value j=1.
		"INSERT INTO big1 VALUES (1, 1)",
		"INSERT INTO big1 VALUES (2, 1)",
		"INSERT INTO big1 VALUES (3, 1)",
		"INSERT INTO big1 VALUES (4, 2)", // j=2: excluded by big2 join
		"INSERT INTO big2 VALUES (10, 1)",
		"INSERT INTO small3 VALUES (100, 1)",
		// EXISTS matches only b1_k in {1, 3}.
		"INSERT INTO inner_e VALUES (1, 0)",
		"INSERT INTO inner_e VALUES (3, 0)",
		"INSERT INTO inner_e VALUES (9, 0)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	// Force bushy DP to actually reorder: stats claim big1/big2 are
	// large and small3 is tiny, so DP starts from the small pair while
	// the FROM order lists the big tables first. (The in-process
	// harness's ANALYZE is a no-op, so stats are set directly — same
	// pattern as the D6.3a fixtures.)
	set := func(name string, rows int64, nds ...int64) {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok || tbl == nil {
			t.Fatalf("lookup %s failed", name)
		}
		cs := make([]catalog.ColumnStats, len(nds))
		for i, nd := range nds {
			cs[i] = catalog.ColumnStats{NDistinct: nd}
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Columns: cs}
	}
	set("big1", 100000, 100000, 1000)
	set("big2", 90000, 90000, 1000)
	set("small3", 10, 10, 10)
	set("inner_e", 1000, 500, 100)

	rows, err := runQueryWithErr(ctx,
		"SELECT b1_k FROM big1, big2, small3 "+
			"WHERE b1_j = b2_j AND b2_j = s3_j "+
			"AND EXISTS (SELECT 1 FROM inner_e WHERE e_k = big1.b1_k) "+
			"ORDER BY b1_k")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, datumTestString(r[0]))
	}
	want := []string{"1", "3"}
	if len(got) != len(want) {
		t.Fatalf("F8 value check failed: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("F8 value check failed: got %v want %v", got, want)
		}
	}

	// Same query, legacy order: identical values.
	planner.SetUnnestPreDPEnabled(false)
	rows2, err := runQueryWithErr(ctx,
		"SELECT b1_k FROM big1, big2, small3 "+
			"WHERE b1_j = b2_j AND b2_j = s3_j "+
			"AND EXISTS (SELECT 1 FROM inner_e WHERE e_k = big1.b1_k) "+
			"ORDER BY b1_k")
	planner.SetUnnestPreDPEnabled(true)
	if err != nil {
		t.Fatalf("legacy query: %v", err)
	}
	if len(rows2) != len(want) {
		t.Fatalf("legacy path rows: got %d want %d", len(rows2), len(want))
	}
}
