package executor

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestToastRelationAutoExposed verifies M0118-0008 TOAST-exposure slice 1
// (design 0118-0084): a user table with at least one toastable (varlena) column
// auto-acquires a TOAST relation in the pg_class virtual view — its
// reltoastrelid points at a synthesized relkind='t' `pg_toast_<oid>` row — even
// when the table carries no explicit `toast.*` storage parameters. This mirrors
// PostgreSQL's needs_toast_table (src/backend/catalog/toasting.c), which creates
// a TOAST relation for any ordinary table with a toastable column. A table whose
// columns are all fixed-width (e.g. only integers) gets neither.
func TestToastRelationAutoExposed(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// wide: has a text column → needs a TOAST relation.
	if err := runDDL(t, ctx, `CREATE TABLE wide (id integer PRIMARY KEY, data text)`); err != nil {
		t.Fatalf("CREATE TABLE wide: %v", err)
	}
	// narrow: only fixed-width columns → no TOAST relation.
	if err := runDDL(t, ctx, `CREATE TABLE narrow (id integer PRIMARY KEY, n bigint)`); err != nil {
		t.Fatalf("CREATE TABLE narrow: %v", err)
	}

	wideTbl, ok := cat.LookupTable(parser.ObjectName{Name: "wide"})
	if !ok {
		t.Fatal("wide table not found")
	}
	narrowTbl, ok := cat.LookupTable(parser.ObjectName{Name: "narrow"})
	if !ok {
		t.Fatal("narrow table not found")
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}

	// pg_class column indices: 0=oid, 1=relname, 13=reltoastrelid, 17=relkind.
	const offset = 100_000_000
	var wideReltoast, narrowReltoast string
	wideToastName := "pg_toast_" + strconv.Itoa(int(wideTbl.OID))
	narrowToastName := "pg_toast_" + strconv.Itoa(int(narrowTbl.OID))
	sawWideToastRow, sawNarrowToastRow := false, false
	for _, r := range pgClass.VirtualRows() {
		if len(r) <= 17 {
			continue
		}
		switch r[1] {
		case "wide":
			wideReltoast = r[13]
		case "narrow":
			narrowReltoast = r[13]
		case wideToastName:
			sawWideToastRow = true
			if r[17] != "t" {
				t.Errorf("%s relkind = %q, want \"t\"", wideToastName, r[17])
			}
			if r[0] != strconv.Itoa(int(wideTbl.OID)+offset) {
				t.Errorf("%s oid = %q, want %d", wideToastName, r[0], int(wideTbl.OID)+offset)
			}
		case narrowToastName:
			sawNarrowToastRow = true
		}
	}

	// wide: reltoastrelid set to OID+offset, and a pg_toast row exists.
	wantWideReltoast := strconv.Itoa(int(wideTbl.OID) + offset)
	if wideReltoast != wantWideReltoast {
		t.Errorf("wide.reltoastrelid = %q, want %q", wideReltoast, wantWideReltoast)
	}
	if !sawWideToastRow {
		t.Errorf("no pg_toast row (%s) synthesized for the wide table", wideToastName)
	}

	// narrow: no TOAST relation at all.
	if narrowReltoast != "0" {
		t.Errorf("narrow.reltoastrelid = %q, want \"0\" (no TOAST relation)", narrowReltoast)
	}
	if sawNarrowToastRow {
		t.Errorf("unexpected pg_toast row (%s) for an all-fixed-width table", narrowToastName)
	}
}
