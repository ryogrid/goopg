package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCatalogCreateLookupDrop pins the round-trip: a created table is
// found by both LookupTable and LookupColumn (case-insensitive), and
// can be dropped.
func TestCatalogCreateLookupDrop(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []Column{
		{Name: "aid", Type: Type{Name: "int4"}, NotNull: true},
		{Name: "abalance", Type: Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tbl.OID < FirstUserOID {
		t.Errorf("OID=%d want >= %d", tbl.OID, FirstUserOID)
	}
	if tbl.Columns[0].Ordinal != 0 || tbl.Columns[1].Ordinal != 1 {
		t.Errorf("ordinals not assigned")
	}

	got, ok := c.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if !ok || got.OID != tbl.OID {
		t.Fatalf("LookupTable round-trip failed: ok=%v", ok)
	}
	col, ok := c.LookupColumn(got, "ABALANCE")
	if !ok || col.Ordinal != 1 {
		t.Fatalf("LookupColumn case-insensitive failed: ok=%v col=%+v", ok, col)
	}

	rfn := c.RelFileNode(got)
	if rfn.RelOid != tbl.OID || rfn.DBOid != DefaultDBOid {
		t.Errorf("RelFileNode=%+v", rfn)
	}

	if err := c.DropTable(parser.ObjectName{Name: "pgbench_accounts"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupTable(parser.ObjectName{Name: "pgbench_accounts"}); ok {
		t.Errorf("table should be gone after DropTable")
	}
}

// TestCatalogDuplicateAndMissing locks down the error paths.
func TestCatalogDuplicateAndMissing(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, nil); err == nil {
		t.Error("duplicate CreateTable should fail")
	}
	if err := c.DropTable(parser.ObjectName{Name: "missing"}); err == nil {
		t.Error("DropTable of missing should fail")
	}
}

func TestCatalogIndexLifecycle(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	if idx.OID <= tbl.OID {
		t.Fatalf("index oid=%d should be greater than table oid=%d", idx.OID, tbl.OID)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "items_id_idx"}); !ok {
		t.Fatal("LookupIndex failed")
	}
	idxs := c.IndexesOnTable(tbl)
	if len(idxs) != 1 || idxs[0].Name != "items_id_idx" {
		t.Fatalf("IndexesOnTable=%+v", idxs)
	}
	rfn := c.IndexRelFileNode(idx)
	if rfn.RelOid != idx.OID || rfn.DBOid != DefaultDBOid {
		t.Fatalf("IndexRelFileNode=%+v", rfn)
	}
	if err := c.DropIndex(parser.ObjectName{Name: "items_id_idx"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "items_id_idx"}); ok {
		t.Fatal("index should be gone after DropIndex")
	}
}

func TestCatalogDropTableAlsoDropsIndexes(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if !c.HasPrimaryKey(tbl) {
		t.Fatal("expected HasPrimaryKey to be true")
	}
	if err := c.DropTable(parser.ObjectName{Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "t_pkey"}); ok {
		t.Fatal("index metadata should be removed when table is dropped")
	}
}

func TestCatalogAddColumn(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	col, err := c.AddColumn(tbl, Column{Name: "label", Type: Type{Name: "text"}})
	if err != nil {
		t.Fatal(err)
	}
	if col.Ordinal != 1 {
		t.Fatalf("new column ordinal=%d want=1", col.Ordinal)
	}
	if _, err := c.AddColumn(tbl, Column{Name: "LABEL", Type: Type{Name: "text"}}); err == nil {
		t.Fatal("duplicate AddColumn should fail")
	}
}

// TestPgCatalogBootstrapViews pins the pg_database / pg_roles /
// pg_tables virtual views HammerDB queries during bootstrap +
// checkschema. Each is exposed under pg_catalog and resolves
// unqualified via the search_path fallback.
func TestPgCatalogBootstrapViews(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"pg_database", "pg_roles", "pg_tables"} {
		v, ok := c.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("LookupTable(%s) failed — search_path fallback not honored", name)
		}
		if !v.Virtual || v.VirtualRows == nil {
			t.Fatalf("%s is not a virtual view", name)
		}
		rows := v.VirtualRows()
		if len(rows) == 0 {
			t.Errorf("%s: empty rows", name)
		}
	}

	tables, _ := c.LookupTable(parser.ObjectName{Name: "pg_tables"})
	got := tables.VirtualRows()
	if len(got) != 1 || got[0][1] != "items" {
		t.Errorf("pg_tables rows=%v want one (public, items, postgres)", got)
	}
}


// TestPgClassExposesRelNatts pins the M0103-0008 rung-14 surface:
// PG's CREATE SUBSCRIPTION column-list probe runs
//
//	… WHEN (array_length(gpt.attrs,1) = c.relnatts) … FROM pg_class c
//
// against the publisher. Before rung 14 goopg's pg_class virtual
// view omitted `relnatts`, so the probe failed with SQLSTATE 42703
// ("column \"relnatts\" does not exist") and CREATE SUBSCRIPTION
// registered zero relations in pg_subscription_rel — every change
// then silently skipped on the subscriber.
//
// Pin: relnatts is present at ordinal 8, typed int4, and populated
// with the table's user-column count (no system columns — goopg
// has no rowid/ctid in its catalog).
func TestPgClassExposesRelNatts(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
		{Name: "v", Type: Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}

	pgClass, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok {
		t.Fatal("pg_catalog.pg_class missing")
	}

	var natts *Column
	for i := range pgClass.Columns {
		if pgClass.Columns[i].Name == "relnatts" {
			natts = &pgClass.Columns[i]
			break
		}
	}
	if natts == nil {
		t.Fatal("pg_class.relnatts column not declared")
	}
	if natts.Type.Name != "int4" {
		t.Errorf("pg_class.relnatts type=%q want int4", natts.Type.Name)
	}

	rows := pgClass.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("pg_class rows=%d want 1 (the user 't' table)", len(rows))
	}
	row := rows[0]
	if len(row) != len(pgClass.Columns) {
		t.Fatalf("pg_class row width=%d want %d (one cell per column)", len(row), len(pgClass.Columns))
	}
	if row[natts.Ordinal] != "2" {
		t.Errorf("pg_class.t.relnatts=%q want %q (user column count)", row[natts.Ordinal], "2")
	}
}

// TestPgIndexesView pins the pg_catalog.pg_indexes virtual
// view that HammerDB's checkschema queries. Each index on a
// non-virtual table should produce one row with
// (schemaname, tablename, indexname, ...). Unqualified lookup
// resolves via the implicit pg_catalog search_path.
func TestPgIndexesView(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
		{Name: "label", Type: Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	view, ok := c.LookupTable(parser.ObjectName{Name: "pg_indexes"})
	if !ok {
		t.Fatal("LookupTable(pg_indexes) failed — search_path fallback to pg_catalog not honored")
	}
	if !view.Virtual || view.VirtualRows == nil {
		t.Fatalf("pg_indexes is not a virtual view: virtual=%v rows=nil(%t)", view.Virtual, view.VirtualRows == nil)
	}
	rows := view.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if got, want := rows[0][1], "items"; got != want {
		t.Errorf("tablename=%q want %q", got, want)
	}
	if got, want := rows[0][2], "items_id_idx"; got != want {
		t.Errorf("indexname=%q want %q", got, want)
	}
}

// TestSystemCatalogOIDConstants verifies the fixed OIDs match upstream's
// values so ODBC/JDBC metadata probes that look up by numeric OID see the
// expected numbers.
func TestSystemCatalogOIDConstants(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"TypeRelationId (pg_type)", TypeRelationId, 1247},
		{"AttributeRelationId (pg_attribute)", AttributeRelationId, 1249},
		{"RelationRelationId (pg_class)", RelationRelationId, 1259},
		{"FirstUserOID", FirstUserOID, 16384},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestIsSystemRelation checks the OID range boundary.
func TestIsSystemRelation(t *testing.T) {
	cases := []struct {
		oid  uint32
		want bool
	}{
		{TypeRelationId, true},
		{AttributeRelationId, true},
		{RelationRelationId, true},
		{FirstUserOID - 1, true},
		{FirstUserOID, false},
		{FirstUserOID + 1, false},
		{0xFFFFFFFF, false},
	}
	for _, tc := range cases {
		if got := IsSystemRelation(tc.oid); got != tc.want {
			t.Errorf("IsSystemRelation(%d) = %v, want %v", tc.oid, got, tc.want)
		}
	}
}

// TestSystemRelationOIDsBelowFirstUserOID is a cross-check: all three
// fixed catalog OIDs must be recognised as system relations.
func TestSystemRelationOIDsBelowFirstUserOID(t *testing.T) {
	for _, oid := range []uint32{TypeRelationId, AttributeRelationId, RelationRelationId} {
		if !IsSystemRelation(oid) {
			t.Errorf("OID %d should be a system relation (< FirstUserOID %d)", oid, FirstUserOID)
		}
	}
}
