package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestAlterColumnResetOptionsClearsNamedEntriesOnly verifies the
// `ALTER TABLE ... ALTER COLUMN col RESET (opt, …)` gap named in
// unimplemented_feat.json (entry #87): before this fix the parser had no
// RESET counterpart to the existing SET (opt=value, …) per-column attribute
// option form, so RESET silently fell to the ALTER COLUMN dispatch's
// generic no-op consume — the option stayed on catalog.Column.Options and
// pg_dump kept re-emitting it forever. RESET must clear only the named
// option(s), leaving any other recorded option untouched (mirrors upstream
// ATExecResetOptions, which merges keys out of attoptions rather than
// wiping the whole array).
func TestAlterColumnResetOptionsClearsNamedEntriesOnly(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE arc_t (id integer, val integer)`); err != nil {
		t.Fatalf("CREATE TABLE arc_t: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE arc_t ALTER COLUMN val SET (n_distinct = 0.5, n_distinct_inherited = -0.1)`); err != nil {
		t.Fatalf("ALTER TABLE arc_t ALTER COLUMN val SET: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "arc_t"})
	if !ok {
		t.Fatal("arc_t table not found")
	}
	col := findColumn(t, tbl, "val")
	if len(col.Options) != 2 {
		t.Fatalf("after SET: Options = %v, want 2 entries", col.Options)
	}

	if err := runDDL(t, ctx, `ALTER TABLE arc_t ALTER COLUMN val RESET (n_distinct)`); err != nil {
		t.Fatalf("ALTER TABLE arc_t ALTER COLUMN val RESET: %v", err)
	}

	tbl, ok = cat.LookupTable(parser.ObjectName{Name: "arc_t"})
	if !ok {
		t.Fatal("arc_t table not found after RESET")
	}
	col = findColumn(t, tbl, "val")
	if len(col.Options) != 1 || col.Options[0] != "n_distinct_inherited=-0.1" {
		t.Fatalf("after RESET (n_distinct): Options = %v, want [n_distinct_inherited=-0.1]", col.Options)
	}

	// pg_attribute is heap-backed (populated at CREATE TABLE, not rendered
	// live from catalog.Table), so drive the same row-shape helper the
	// pg_dump-fidelity test for the SET side uses directly, rather than
	// re-deriving a full heap query harness here.
	const attoptionsIdx = 21
	row := buildUserPGAttributeRow(nil, tbl, col)
	if got := row[attoptionsIdx].StringValue(); got != "{n_distinct_inherited=-0.1}" {
		t.Errorf("attoptions after RESET (n_distinct) = %q, want {n_distinct_inherited=-0.1}", got)
	}
}

func findColumn(t *testing.T, tbl *catalog.Table, name string) catalog.Column {
	t.Helper()
	for _, c := range tbl.Columns {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("column %q not found on table %q", name, tbl.Name)
	return catalog.Column{}
}
