package catalog

import (
	"encoding/json"
	"reflect"
	"strings"
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

// TestSnapshotPreservesSmallDimension pins that planner-only catalog
// hints survive restart. M0077 Slice A gates pre-MHJ leaf-local
// filter attachment on Table.SmallDimension (region / nation); if the
// flag is lost across Snapshot/Restore, live bench servers fall back
// to the historical 6-table Q5 MHJ even though unit tests still pass.
func TestSnapshotPreservesSmallDimension(t *testing.T) {
	src := NewInMemory()
	tbl, err := src.CreateTable(parser.ObjectName{Name: "region"}, []Column{
		{Name: "r_regionkey", Type: Type{Name: "int4"}},
		{Name: "r_name", Type: Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tbl.SmallDimension = true

	raw, err := json.Marshal(src.Snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dst := NewInMemory()
	if err := dst.Restore(decoded); err != nil {
		t.Fatal(err)
	}
	got, ok := dst.LookupTable(parser.ObjectName{Name: "region"})
	if !ok {
		t.Fatal("region lost across round-trip")
	}
	if !got.SmallDimension {
		t.Fatal("SmallDimension lost across snapshot/restore")
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

// TestSnapshotPreservesTableStats pins the M0006 / 0006-0002
// contract: a populated TableStats with MCV + histogram payloads
// survives Snapshot → JSON-encode → JSON-decode → Restore deep-
// equal. Without this, ANALYZE has to re-run after every clean
// stop / start.
func TestSnapshotPreservesTableStats(t *testing.T) {
	src := NewInMemory()
	tbl, err := src.CreateTable(parser.ObjectName{Name: "items"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}, NotNull: true},
		{Name: "label", Type: Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantStats := &TableStats{
		RowCount: 1000,
		Pages:    7,
		AvgWidth: 24.5,
		Columns: []ColumnStats{
			{NDistinct: 1000, NullFrac: 0.0},
			{
				NDistinct: 3,
				NullFrac:  0.01,
				MCV: []MCVEntry{
					{Value: "F", Frequency: 0.8},
					{Value: "O", Frequency: 0.15},
				},
				Histogram: []string{"P", "Q", "R"},
			},
		},
	}
	src.SetTableStats(tbl, wantStats)

	// Round-trip via JSON to exercise the actual on-disk path,
	// not just the in-memory deep-copy.
	raw, err := json.Marshal(src.Snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dst := NewInMemory()
	if err := dst.Restore(decoded); err != nil {
		t.Fatal(err)
	}
	got, ok := dst.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("items lost across round-trip")
	}
	if got.Stats == nil {
		t.Fatal("Stats lost across round-trip")
	}
	if !reflect.DeepEqual(got.Stats, wantStats) {
		t.Errorf("Stats=%+v want %+v", got.Stats, wantStats)
	}

	// Independent ownership: mutating the source after Snapshot
	// must not show up on the restored copy.
	wantStats.Columns[1].MCV[0].Value = "MUTATED"
	if got.Stats.Columns[1].MCV[0].Value != "F" {
		t.Errorf("restored MCV aliased source: got %q", got.Stats.Columns[1].MCV[0].Value)
	}
}

// TestSnapshotOmitsStatsWhenNil pins the forward-compat
// contract: a table with no Stats serialises without a `stats`
// key. Old snapshots saved before M0006 / 0006-0002 also have no
// such key, so the JSON shape stays interchangeable.
func TestSnapshotOmitsStatsWhenNil(t *testing.T) {
	src := NewInMemory()
	if _, err := src.CreateTable(parser.ObjectName{Name: "items"}, []Column{
		{Name: "id", Type: Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(src.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"stats"`) {
		t.Errorf("snapshot for unanalysed table included `stats` key:\n%s", raw)
	}
}

// TestRestoreAcceptsLegacySnapshotWithoutStats: a JSON snapshot
// from before M0006 (no `stats` field on tables) must round-trip
// cleanly into Stats==nil and be queryable as an unanalysed
// relation.
func TestRestoreAcceptsLegacySnapshotWithoutStats(t *testing.T) {
	legacy := []byte(`{
        "next_oid": 16385,
        "tables": [
            {
                "oid": 16384,
                "name": "items",
                "columns": [
                    {"name": "id", "type": {"name": "int4"}, "ordinal": 0}
                ]
            }
        ]
    }`)
	var snap Snapshot
	if err := json.Unmarshal(legacy, &snap); err != nil {
		t.Fatalf("unmarshal legacy snapshot: %v", err)
	}
	dst := NewInMemory()
	if err := dst.Restore(snap); err != nil {
		t.Fatalf("restore legacy snapshot: %v", err)
	}
	got, ok := dst.LookupTable(parser.ObjectName{Name: "items"})
	if !ok {
		t.Fatal("items lost from legacy snapshot")
	}
	if got.Stats != nil {
		t.Errorf("Stats=%+v want nil for unanalysed legacy table", got.Stats)
	}
}

// TestRestoreLegacySnapshotReappliesSmallDimension pins the migration
// path for snapshots written before TableEntry.small_dimension
// existed: restore must still recover the create-time hint for the
// canonical tiny TPC-H tables so planner gates remain live after
// restart.
func TestRestoreLegacySnapshotReappliesSmallDimension(t *testing.T) {
	legacy := []byte(`{
        "next_oid": 16385,
        "tables": [
            {
                "oid": 16384,
                "name": "region",
                "columns": [
                    {"name": "r_regionkey", "type": {"name": "int4"}, "ordinal": 0},
                    {"name": "r_name", "type": {"name": "text"}, "ordinal": 1}
                ]
            }
        ]
    }`)
	var snap Snapshot
	if err := json.Unmarshal(legacy, &snap); err != nil {
		t.Fatalf("unmarshal legacy snapshot: %v", err)
	}
	dst := NewInMemory()
	if err := dst.Restore(snap); err != nil {
		t.Fatalf("restore legacy snapshot: %v", err)
	}
	got, ok := dst.LookupTable(parser.ObjectName{Name: "region"})
	if !ok {
		t.Fatal("region lost from legacy snapshot")
	}
	if !got.SmallDimension {
		t.Fatal("legacy snapshot restore should reapply SmallDimension for region")
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
