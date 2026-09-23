package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// CREATE INDEX publishes the heap's size as PG's index_update_stats does
// (index.c:2809): over a loaded heap, reltuples = the build scan's live-tuple
// count and the table stops looking never-vacuumed (no 10-page floor); over an
// EMPTY never-measured heap it leaves the "unknown" state alone (CREATE TABLE
// … PRIMARY KEY must not look vacuumed); and a table whose autovacuum_enabled
// reloption is false keeps its statistics untouched.
func TestCreateIndexUpdatesHeapStats(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	stats := func(name string) (int64, bool) {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if tbl.Stats == nil {
			return 0, false
		}
		return tbl.Stats.RowCount, tbl.Stats.Analyzed
	}
	for _, sql := range []string{
		"CREATE TABLE loaded (a int, b int)",
		"INSERT INTO loaded VALUES (1, 1), (2, 2), (3, 3), (4, 4)",
		"DELETE FROM loaded WHERE a = 4",
		"CREATE INDEX loaded_a ON loaded (a)",
		"CREATE TABLE empty_pk (a int PRIMARY KEY)",
		"CREATE TABLE noav (a int) WITH (autovacuum_enabled = false)",
		"INSERT INTO noav VALUES (1), (2)",
		"CREATE INDEX noav_a ON noav (a)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if rows, analyzed := stats("loaded"); !analyzed || rows != 3 {
		t.Fatalf("loaded: stats (rows=%d, measured=%v), want 3 live tuples, measured", rows, analyzed)
	}
	if _, analyzed := stats("empty_pk"); analyzed {
		t.Fatal("empty_pk: an index built over an empty never-measured heap must leave it unmeasured")
	}
	if _, analyzed := stats("noav"); analyzed {
		t.Fatal("noav: autovacuum_enabled = false must keep CREATE INDEX from writing table statistics")
	}
}

// withAutovacuumOff makes ctx's session report `autovacuum = off`, under
// which PG's index_update_stats leaves the heap's statistics alone
// (AutoVacuumingActive() false). Tests that build a tiny table, index it after
// loading and never ANALYZE it use this to keep planning against the
// never-measured size they were written for: with the size recorded, PG itself
// plans a Seq Scan for them, and the executor index-scan behaviour they exist
// to exercise would not run.
func withAutovacuumOff(ctx *Context) {
	prev := ctx.GetSetting
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "autovacuum" {
			return "off", true
		}
		if prev != nil {
			return prev(name)
		}
		return "", false
	}
}
