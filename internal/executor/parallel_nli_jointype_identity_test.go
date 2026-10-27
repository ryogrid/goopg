package executor

// nliJointypeFixture is shared by the parallel lateral-probe identity pins
// (parallel_lateral_{left,semi,anti}_probe_identity_test.go).
//
// This file used to hold TestParallelNLIJointypeIdentity, the FUSED
// nested-loop-index-join parallel identity pin (M0145-0010). Its last case —
// `o LEFT JOIN i ON i.k = o.k AND o.id > 0` — reached the fused
// `*optimizer.NestedLoopIndexJoin` only because the preserved-side ON
// conjunct kept the statement out of the PG-shaped search, so the legacy
// post-pass fused it. Since M0147-0053 the search builds that statement, and
// it builds PG 18.3's own plan (scratch-PG verified): the decomposed lateral
// `Nested Loop Left Join` over the parameterised index probe, `Join Filter:
// (o.id > 0)`. That family's parallel identity is pinned by
// parallel_lateral_left_probe_identity_test.go; the fused node now appears
// only for memoized probes and the legacy post-pass M0147-0051 retires.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// nliJointypeFixture gives the probe an INDEXED correlation, which is what
// makes the planner fuse the join into a parameterized index probe instead of
// electing an ordinary nested loop or a hash join.
func nliJointypeFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE nlij_outer (id int, k int)",
		"CREATE TABLE nlij_inner (k int, v int)",
		"CREATE INDEX nlij_inner_k ON nlij_inner (k)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	for i := 0; i < 100; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO nlij_outer VALUES (%d, %d)", i, i)); err != nil {
			cleanup()
			t.Fatalf("insert outer: %v", err)
		}
	}
	// Only the first 50 outer keys have a match, so SEMI/ANTI/LEFT each give a
	// different, easily-checked row count and an N-copy regression cannot hide
	// behind a coincidence.
	for i := 0; i < 50; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO nlij_inner VALUES (%d, %d)", i, i*2)); err != nil {
			cleanup()
			t.Fatalf("insert inner: %v", err)
		}
	}
	// 100,000 inner rows that match no outer key: the probe has to earn
	// its election as it does in PG. On 400 outer x 50 inner rows PG 18.3
	// elects Hash Semi/Anti Join and Merge Left Join; with 100 outer rows
	// over this inner it elects `Nested Loop Semi Join` and `Nested Loop
	// Left Join` over an index probe (M0145-0030). For NOT EXISTS PG
	// elects Merge Right Anti Join, which goopg lacks (no JOIN_RIGHT_ANTI,
	// ledgered); the NL anti probe is goopg's election there.
	if err := runDDL(t, ctx, "INSERT INTO nlij_inner SELECT 1000 + g, g FROM generate_series(1, 100000) g"); err != nil {
		cleanup()
		t.Fatalf("insert inner filler: %v", err)
	}
	// The in-process ANALYZE is a no-op, so seed what ANALYZE would record.
	// Without it the outer is sized by the never-analysed 10-page floor
	// (2260 rows, as PG's estimate_rel_size also does) and hash wins.
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "nlij_outer"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 100, Columns: []catalog.ColumnStats{
			{NDistinct: 100}, {NDistinct: 100},
		}}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "nlij_inner"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 100050, Columns: []catalog.ColumnStats{
			{NDistinct: 100050}, {NDistinct: 100050},
		}}
	}
	return ctx, cleanup
}
