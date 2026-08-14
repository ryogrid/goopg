package wal

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestBuildCatalogSnapshotFreezesShape pins the M0008
// snapshot-builder contract: a snapshot captures the catalog at
// a point in time, mutations to the underlying catalog after
// snapshot construction don't bleed through, and lookups by
// RelOid resolve back to the captured shape.
func TestBuildCatalogSnapshotFreezesShape(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	snap := BuildCatalogSnapshot(c, nil)
	if snap.Len() != 1 {
		t.Errorf("Len=%d want 1", snap.Len())
	}
	got, ok := snap.Lookup(c.RelFileNode(tbl))
	if !ok {
		t.Fatal("Lookup failed for newly captured table")
	}
	if got.Name != "items" || got.OID != tbl.OID {
		t.Errorf("got=%+v want items/%d", got, tbl.OID)
	}
	if len(got.Columns) != 2 {
		t.Fatalf("Columns len=%d want 2", len(got.Columns))
	}
	if got.Columns[0].Name != "id" || !got.Columns[0].NotNull {
		t.Errorf("col[0]=%+v want id NotNull", got.Columns[0])
	}

	// Mutating the catalog after snapshot must not leak into the
	// frozen view: drop items, then re-create with different
	// columns. The snapshot still resolves to the original.
	if err := c.DropTable(parser.ObjectName{Name: "items"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "newshape"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	got2, ok := snap.Lookup(c.RelFileNode(tbl))
	if !ok {
		t.Fatal("snapshot Lookup lost original after catalog mutation")
	}
	if got2.Name != "items" || len(got2.Columns) != 2 {
		t.Errorf("snapshot drifted: %+v", got2)
	}
}

// TestBuildCatalogSnapshotSkipsVirtualTables: virtual catalog
// views (pg_catalog.*) re-register on every startup and aren't
// part of the user-data slot decoder's scope. The snapshot must
// not include them.
func TestBuildCatalogSnapshotSkipsVirtualTables(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "real_table"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	snap := BuildCatalogSnapshot(c, nil)
	if snap.Len() != 1 {
		t.Errorf("Len=%d want 1 (virtual catalog views must be skipped)", snap.Len())
	}
}

// TestSnapshotLookupMissingRelation pins the v0 contract for
// "relation didn't exist when the slot started": Lookup returns
// (nil, false). A real plugin would skip the change; once schema
// changes are honoured this becomes an error path.
func TestSnapshotLookupMissingRelation(t *testing.T) {
	snap := BuildCatalogSnapshot(catalog.NewInMemory(), nil)
	if _, ok := snap.Lookup(storage.RelFileNode{DBOid: 1, RelOid: 99999}); ok {
		t.Errorf("Lookup of unknown rel returned ok=true")
	}
}

// TestNewSlotDecoderWithSnapshotAttachesIt pins the wiring: the
// returned decoder carries the SlotSnapshot the caller passed in,
// so a future OutputPlugin implementation can read it from the
// decoder without a separate plumbing path.
func TestNewSlotDecoderWithSnapshotAttachesIt(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, _ := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 16})
	defer w.Close()
	slots, _ := OpenSlots(dir)
	if _, err := slots.CreateLogical("ls1", "pgoutput", "appdb", 0); err != nil {
		t.Fatal(err)
	}

	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "captured"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	snap := SlotSnapshot{Catalog: BuildCatalogSnapshot(c, nil)}

	plugin := newCapturePlugin()
	dec, err := NewSlotDecoderWithSnapshot(slots, "ls1", w, walDir, 1<<16, plugin, snap)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	if dec.Snapshot.Catalog == nil || dec.Snapshot.Catalog.Len() != 1 {
		t.Errorf("decoder.Snapshot.Catalog=%+v, want one captured rel", dec.Snapshot.Catalog)
	}

	// Sanity: the wired decoder still runs the basic loop.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = dec.Run(ctx) // returns context.DeadlineExceeded; we just want no panic
}

// TestBuildCatalogSnapshotScopesToDBOid pins M0119-0006 (deferral row 1354
// claim 2): BuildCatalogSnapshot with a non-default dbOid scopes the snapshot
// to that database's namespace, so a DB-2 relation is Lookup-able while the
// DB-1 relation is not — and the empty-dbOid default keeps today's DB-1
// scoping. Upstream's logical slot lives in MyDatabaseId (slot.c:1760), so the
// snapshot must be built against the slot's database or the decoder skips
// every change at snap.Lookup.
func TestBuildCatalogSnapshotScopesToDBOid(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	db1Tbl, ok := c.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("DB-1 items not found")
	}

	db2Oid, err := c.CreateDatabase("db2", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "items"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}, db2Oid); err != nil {
		t.Fatal(err)
	}
	db2Tbl, ok := c.LookupTable(parser.ObjectName{Name: "items"}, db2Oid)
	if !ok {
		t.Fatal("DB-2 items not found")
	}

	// dbOid=2: snapshot scoped to DB 2.
	snap2 := BuildCatalogSnapshot(c, nil, db2Oid)
	if snap2.Len() != 1 {
		t.Fatalf("Len=%d want 1 (only the DB-2 relation in scope)", snap2.Len())
	}
	if _, ok := snap2.Lookup(c.RelFileNode(db2Tbl)); !ok {
		t.Errorf("DB-2 relation not Lookup-able in the DB-2-scoped snapshot")
	}
	if _, ok := snap2.Lookup(c.RelFileNode(db1Tbl)); ok {
		t.Errorf("DB-1 relation leaked into the DB-2-scoped snapshot")
	}

	// Empty dbOid: default DB-1 scoping unchanged.
	snap1 := BuildCatalogSnapshot(c, nil)
	if snap1.Len() != 1 {
		t.Fatalf("Len=%d want 1 (only the DB-1 relation in scope)", snap1.Len())
	}
	if _, ok := snap1.Lookup(c.RelFileNode(db1Tbl)); !ok {
		t.Errorf("DB-1 relation not Lookup-able in the default snapshot")
	}
	if _, ok := snap1.Lookup(c.RelFileNode(db2Tbl)); ok {
		t.Errorf("DB-2 relation leaked into the default (DB-1) snapshot")
	}
}
