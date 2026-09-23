package executor

import (
	"strings"
	"testing"
)

// TestReindexTableNoIndexNotice pins ReindexTable's NOTICE (indexcmds.c) as
// PG 18.3 emits it, captured live: a table with nothing to rebuild says so;
// the TOAST table's index counts (reindex_relation processes TOAST), so a
// table with a text column is silent; CONCURRENTLY has its own wording; a
// partitioned parent (ReindexPartitions) is silent; the name is printed
// without its schema. Found by the 2026-09-23 command-tag sweep.
func TestReindexTableNoIndexNotice(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE ri_int (a int)",
		"CREATE TABLE ri_text (a text)",
		"CREATE TABLE ri_idx (a int)",
		"CREATE INDEX ri_idx_a ON ri_idx (a)",
		"CREATE TABLE ri_part (a int) PARTITION BY RANGE (a)",
		"CREATE TABLE ri_part_1 PARTITION OF ri_part FOR VALUES FROM (0) TO (10)",
		"CREATE SCHEMA ris",
		"CREATE TABLE ris.q (a int)",
	} {
		runSQL(t, ctx, ddl)
	}
	for _, tc := range []struct{ sql, want string }{
		{"REINDEX TABLE ri_int", `table "ri_int" has no indexes to reindex`},
		{"REINDEX TABLE ri_text", ""},
		{"REINDEX TABLE ri_idx", ""},
		{"REINDEX TABLE CONCURRENTLY ri_int", `table "ri_int" has no indexes that can be reindexed concurrently`},
		{"REINDEX TABLE ri_part", ""},
		{"REINDEX TABLE ris.q", `table "q" has no indexes to reindex`},
		// A system catalog always has indexes in PG (upstream reindex_catalog).
		{"REINDEX TABLE pg_class", ""},
	} {
		ctx.Notices = nil
		runSQL(t, ctx, tc.sql)
		got := strings.Join(ctx.Notices, "|")
		if got != tc.want {
			t.Errorf("%s: notices %q, want %q", tc.sql, got, tc.want)
		}
	}
}
