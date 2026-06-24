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

// TestReltoastrelidRegclassRendersToastName verifies M0118-0008 TOAST-exposure
// slice 2 (design 0118-0084): `reltoastrelid::regclass` for a table that owns an
// auto-exposed TOAST relation renders the schema-qualified `pg_toast.pg_toast_<oid>`
// name PG's regclassout emits (the pg_toast namespace is never in search_path,
// so the name is always schema-qualified). The synthetic TOAST pg_class row
// lives only in the virtual builder output — not in c.tables — so the regclass
// cast resolves the OID via InMemory.ToastRelName rather than tableByOID. This
// is exactly the value the reindex-concurrently-toast spec's setup feeds into
// `EXECUTE 'ALTER TABLE ' || r.table_name || ' RENAME TO …'`.
func TestReltoastrelidRegclassRendersToastName(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE reind_con_wide (id int primary key, data text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	wideTbl, ok := cat.LookupTable(parser.ObjectName{Name: "reind_con_wide"})
	if !ok {
		t.Fatal("reind_con_wide not found")
	}

	want := "pg_toast.pg_toast_" + strconv.Itoa(int(wideTbl.OID))
	rows := runQuery(t, ctx,
		`SELECT reltoastrelid::regclass::text FROM pg_class WHERE oid = 'reind_con_wide'::regclass`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got := rows[0][0].StringValue(); got != want {
		t.Errorf("reltoastrelid::regclass::text = %q, want %q", got, want)
	}
}

// TestToastRelationIndexExposed verifies M0118-0008 TOAST-exposure slice 3: a
// table that owns an auto-exposed TOAST relation also exposes that relation's
// unique btree index pg_toast_<oid>_index in BOTH pg_class (relkind='i') and
// pg_index (indrelid=toast-rel OID). This is what the reindex-concurrently-toast
// spec's setup walks:
//
//	SELECT indexrelid::regclass::text FROM pg_index
//	  WHERE indrelid = (SELECT oid FROM pg_class WHERE relname = <toast rel>)
//
// resolving the index name it then RENAMEs. The toast relation's pg_class row
// also flips relhasindex='t'. Mirrors PG, which auto-creates one toast index per
// TOAST relation.
func TestToastRelationIndexExposed(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE reind_con_wide (id int primary key, data text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	wideTbl, ok := cat.LookupTable(parser.ObjectName{Name: "reind_con_wide"})
	if !ok {
		t.Fatal("reind_con_wide not found")
	}
	const (
		relOffset = 100_000_000
		idxOffset = 200_000_000
	)
	toastRelName := "pg_toast_" + strconv.Itoa(int(wideTbl.OID))
	toastIdxName := toastRelName + "_index"
	wantIdxOID := strconv.Itoa(int(wideTbl.OID) + idxOffset)
	wantRelOID := strconv.Itoa(int(wideTbl.OID) + relOffset)

	// pg_class: relkind='i' row for the toast index + the toast relation row now
	// reports relhasindex='t'.
	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	sawIdxRow, sawToastRelHasIndex := false, false
	for _, r := range pgClass.VirtualRows() {
		if len(r) <= 17 {
			continue
		}
		switch r[1] {
		case toastIdxName:
			sawIdxRow = true
			if r[0] != wantIdxOID {
				t.Errorf("%s oid = %q, want %s", toastIdxName, r[0], wantIdxOID)
			}
			if r[17] != "i" {
				t.Errorf("%s relkind = %q, want \"i\"", toastIdxName, r[17])
			}
			if r[6] != "403" {
				t.Errorf("%s relam = %q, want \"403\" (btree)", toastIdxName, r[6])
			}
		case toastRelName:
			// pg_class column 14 = relhasindex.
			if r[14] != "t" {
				t.Errorf("%s relhasindex = %q, want \"t\"", toastRelName, r[14])
			}
			sawToastRelHasIndex = true
		}
	}
	if !sawIdxRow {
		t.Errorf("no relkind='i' pg_class row (%s) synthesized for the toast relation", toastIdxName)
	}
	if !sawToastRelHasIndex {
		t.Errorf("toast relation row (%s) not found in pg_class", toastRelName)
	}

	// pg_index: a row with indexrelid=toast-idx OID, indrelid=toast-rel OID.
	pgIndex, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_index"})
	if !ok || pgIndex.VirtualRows == nil {
		t.Fatal("pg_index virtual table not found")
	}
	sawIndexRow := false
	for _, r := range pgIndex.VirtualRows() {
		// pg_index column 0 = indexrelid, 1 = indrelid.
		if len(r) >= 2 && r[0] == wantIdxOID {
			sawIndexRow = true
			if r[1] != wantRelOID {
				t.Errorf("toast pg_index.indrelid = %q, want %s", r[1], wantRelOID)
			}
		}
	}
	if !sawIndexRow {
		t.Errorf("no pg_index row with indexrelid=%s synthesized for the toast index", wantIdxOID)
	}

	// End-to-end: the exact join the spec uses to discover the index name,
	// resolving indexrelid::regclass to the schema-qualified toast index name.
	wantName := "pg_toast." + toastIdxName
	rows := runQuery(t, ctx,
		`SELECT indexrelid::regclass::text FROM pg_index WHERE indrelid = (SELECT oid FROM pg_class WHERE relname = '`+toastRelName+`')`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from the spec join, got %d", len(rows))
	}
	if got := rows[0][0].StringValue(); got != wantName {
		t.Errorf("indexrelid::regclass::text = %q, want %q", got, wantName)
	}
}
