package catalog

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestSnapshotRestoreRoundTrip pins the contract that saving and
// reloading a populated catalog reconstructs it byte-for-byte
// (post-canonicalisation). The on-disk snapshot is what carries
// the schema across goopg restarts.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	src := NewInMemory()
	tbl, err := src.CreateTable(parser.ObjectName{Name: "items"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateTable(parser.ObjectName{Name: "events"}, []Column{
		{Name: "ts", Type: Type{Name: "timestamp"}},
		{Name: "msg", Type: Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	snap := src.Snapshot()

	dst := NewInMemory()
	if err := dst.Restore(snap); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"items", "events"} {
		got, ok := dst.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Errorf("table %q lost across round-trip", name)
			continue
		}
		want, _ := src.LookupTable(parser.ObjectName{Name: name})
		if got.OID != want.OID {
			t.Errorf("table %q OID=%d want %d", name, got.OID, want.OID)
		}
		if !reflect.DeepEqual(got.Columns, want.Columns) {
			t.Errorf("table %q columns=%+v want %+v", name, got.Columns, want.Columns)
		}
	}
	gotIdx, ok := dst.LookupIndex(parser.ObjectName{Name: "items_id_idx"})
	if !ok {
		t.Fatal("items_id_idx lost")
	}
	if gotIdx.Table == nil || gotIdx.Table.Name != "items" {
		t.Errorf("idx.Table=%+v want items", gotIdx.Table)
	}
	if !gotIdx.Unique || !gotIdx.Primary || gotIdx.Method != "btree" {
		t.Errorf("idx flags lost: %+v", gotIdx)
	}

	// IndexesOnTable should resolve through the rebuilt by-table
	// map, not just the by-name one.
	tblAfter, _ := dst.LookupTable(parser.ObjectName{Name: "items"})
	if got := dst.IndexesOnTable(tblAfter); len(got) != 1 || got[0].OID != gotIdx.OID {
		t.Errorf("IndexesOnTable=%v", got)
	}

	// nextOID survives so a follow-up CreateTable doesn't collide
	// with a restored OID.
	tbl2, err := dst.CreateTable(parser.ObjectName{Name: "fresh"}, []Column{{Name: "x", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tbl2.OID <= gotIdx.OID {
		t.Errorf("post-restore CreateTable OID=%d collides with restored OID=%d", tbl2.OID, gotIdx.OID)
	}
}

// TestRestoreRejectsDanglingIndex: an index whose TableOID doesn't
// resolve in the snapshot is corruption — we want to fail loudly
// rather than silently install a broken Index pointer.
func TestRestoreRejectsDanglingIndex(t *testing.T) {
	snap := Snapshot{
		NextOID: FirstUserOID + 2,
		Tables: []TableEntry{
			{OID: FirstUserOID, Name: "items", Columns: []Column{{Name: "id", Type: Type{Name: "int4"}}}},
		},
		Indexes: []IndexEntry{
			{OID: FirstUserOID + 1, Name: "ghost_idx", TableOID: 99999, Columns: []string{"id"}, Method: "btree"},
		},
	}
	dst := NewInMemory()
	if err := dst.Restore(snap); err == nil {
		t.Fatal("expected error for dangling index")
	}
}

// TestSnapshotIsDeterministic: two snapshots of the same catalog
// in the same state must serialize identically — needed so the
// atomic write doesn't churn the file with no-op rewrites.
func TestSnapshotIsDeterministic(t *testing.T) {
	c := NewInMemory()
	for _, name := range []string{"a", "b", "c"} {
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, []Column{{Name: "x", Type: Type{Name: "int4"}}}); err != nil {
			t.Fatal(err)
		}
	}
	a := c.Snapshot()
	b := c.Snapshot()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("snapshots differ:\n a=%+v\n b=%+v", a, b)
	}
}
