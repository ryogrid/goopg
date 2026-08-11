package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0130-S11.6 — `pg_class.relhasindex` for a USER table.
//
// The flag is what gates every index-aware code path in a real PG 18.3 attached
// to a goopg cluster: `get_relation_info` (plancat.c) will not call
// RelationGetIndexList without it, and `ExecOpenIndices` will not maintain a
// result relation's indexes without it. It was hardcoded false until the
// nbtree on-disk format became PG's (S11.1..S11.5), so PG silently planned
// seq scans and silently skipped index maintenance for its own INSERTs
// (AI-20260810-011258-003 blocker #10).
//
// The value is deliberately NOT `len(indexes) > 0`: goopg's key format is a
// per-INDEX property (descriptor-bearing ⇒ real PG datums, refused ⇒ goopg's
// order-preserving blob) while relhasindex is per-RELATION, so a single
// undescribable index has to take the whole table back to false. These tests
// pin both directions plus the mixed case, since a regression to the naive
// form would hand PG a tree it orders with the wrong comparator — wrong rows
// and mis-filed inserts, not an error.

// relhasindexColIdx locates the column by NAME so the assertions survive a
// reordering of pgClassColumnsPG18.
func relhasindexColIdx(t *testing.T) int {
	t.Helper()
	for i, c := range pgClassColumnsPG18() {
		if c.Name == "relhasindex" {
			return i
		}
	}
	t.Fatal("relhasindex column not found in pgClassColumnsPG18")
	return -1
}

func newRelhasindexCatalog(t *testing.T, tblName string, cols []catalog.Column) (*catalog.InMemory, *catalog.Table) {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Schema: "public", Name: tblName}, cols)
	if err != nil {
		t.Fatalf("CreateTable(%s): %v", tblName, err)
	}
	return cat, tbl
}

func TestPGClassRelhasindexTracksDescribableIndexes(t *testing.T) {
	col := relhasindexColIdx(t)

	intCols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
		{Name: "amount", Type: catalog.Type{Name: "numeric"}, Ordinal: 1},
		{Name: "span", Type: catalog.Type{Name: "interval"}, Ordinal: 2},
	}

	t.Run("no indexes", func(t *testing.T) {
		cat, tbl := newRelhasindexCatalog(t, "rhi_none", intCols)
		if pgClassRelhasindex(cat, tbl) {
			t.Error("pgClassRelhasindex = true with no index")
		}
		if got := buildUserPGClassRow(cat, tbl)[col].BoolValue(); got {
			t.Errorf("relhasindex = %v, want false", got)
		}
	})

	t.Run("describable index", func(t *testing.T) {
		cat, tbl := newRelhasindexCatalog(t, "rhi_int", intCols)
		if _, err := cat.CreateIndex(parser.ObjectName{Schema: "public", Name: "rhi_int_pk"},
			tbl, []string{"id"}, true, "btree", true); err != nil {
			t.Fatalf("CreateIndex: %v", err)
		}
		// Sanity: this is exactly the property the flag stands for — the
		// resolver must accept the index, else the test proves nothing.
		if _, err := buildPGIndexKeyDesc(cat.IndexesOnTable(tbl)[0]); err != nil {
			t.Fatalf("buildPGIndexKeyDesc refused an int4 PK: %v", err)
		}
		if !pgClassRelhasindex(cat, tbl) {
			t.Error("pgClassRelhasindex = false for a describable int4 index")
		}
		if got := buildUserPGClassRow(cat, tbl)[col].BoolValue(); !got {
			t.Errorf("relhasindex = %v, want true", got)
		}
	})

	t.Run("undescribable index alone", func(t *testing.T) {
		// `interval` has no 3b-2a comparator (pgIndexComparatorForOID returns
		// nil for it), so the tree keeps the blob key format. This subtest used
		// `numeric` until M0119-0006 made goopg's numeric image PostgreSQL's
		// own, which moved numeric onto the tuple format and took the premise
		// with it.
		cat, tbl := newRelhasindexCatalog(t, "rhi_span", intCols)
		if _, err := cat.CreateIndex(parser.ObjectName{Schema: "public", Name: "rhi_span_idx"},
			tbl, []string{"span"}, false, "btree", false); err != nil {
			t.Fatalf("CreateIndex: %v", err)
		}
		if _, err := buildPGIndexKeyDesc(cat.IndexesOnTable(tbl)[0]); err == nil {
			t.Fatal("buildPGIndexKeyDesc accepted an interval key — test premise gone")
		}
		if pgClassRelhasindex(cat, tbl) {
			t.Error("pgClassRelhasindex = true for a blob-format index")
		}
	})

	t.Run("mixed takes the table back down", func(t *testing.T) {
		cat, tbl := newRelhasindexCatalog(t, "rhi_mixed", intCols)
		if _, err := cat.CreateIndex(parser.ObjectName{Schema: "public", Name: "rhi_mixed_pk"},
			tbl, []string{"id"}, true, "btree", true); err != nil {
			t.Fatalf("CreateIndex(pk): %v", err)
		}
		if !pgClassRelhasindex(cat, tbl) {
			t.Fatal("premise: describable-only table should be true")
		}
		if _, err := cat.CreateIndex(parser.ObjectName{Schema: "public", Name: "rhi_mixed_span"},
			tbl, []string{"span"}, false, "btree", false); err != nil {
			t.Fatalf("CreateIndex(interval): %v", err)
		}
		// relhasindex is per-RELATION and RelationGetIndexList reads every
		// pg_index row once it is set — there is no way to expose only the
		// describable one, so the whole table has to go back to false.
		if pgClassRelhasindex(cat, tbl) {
			t.Error("pgClassRelhasindex = true with one undescribable index present")
		}
	})

	t.Run("gate off keeps the pre-slice value", func(t *testing.T) {
		cat, tbl := newRelhasindexCatalog(t, "rhi_gateoff", intCols)
		if _, err := cat.CreateIndex(parser.ObjectName{Schema: "public", Name: "rhi_gateoff_pk"},
			tbl, []string{"id"}, true, "btree", true); err != nil {
			t.Fatalf("CreateIndex: %v", err)
		}
		defer func(prev bool) { pgIndexTupleKeys = prev }(pgIndexTupleKeys)
		pgIndexTupleKeys = false
		// With the S11.4 flip off every tree is blob format, so no table may
		// advertise an index to PG — the behaviour that stood before S11.6.
		if pgClassRelhasindex(cat, tbl) {
			t.Error("pgClassRelhasindex = true with pgIndexTupleKeys off")
		}
	})

	t.Run("nil catalog", func(t *testing.T) {
		if pgClassRelhasindex(nil, &catalog.Table{Name: "x", OID: 1}) {
			t.Error("pgClassRelhasindex = true with a nil catalog")
		}
	})
}

// TestPGClassRelhasindexForIndexRelationStaysFalse pins that an INDEX relation
// keeps relhasindex=false — an index never has indexes of its own, and
// buildUserPGClassRowForIndex must not pick up the base table's value.
func TestPGClassRelhasindexForIndexRelationStaysFalse(t *testing.T) {
	col := relhasindexColIdx(t)
	cat, tbl := newRelhasindexCatalog(t, "rhi_idxrel", []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true, Ordinal: 0},
	})
	idx, err := cat.CreateIndex(parser.ObjectName{Schema: "public", Name: "rhi_idxrel_pk"},
		tbl, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	if got := buildUserPGClassRowForIndex(cat, idx)[col].BoolValue(); got {
		t.Errorf("index relation relhasindex = %v, want false", got)
	}
	if got := buildUserPGClassRow(cat, tbl)[col].BoolValue(); !got {
		t.Errorf("base table relhasindex = %v, want true", got)
	}
}
