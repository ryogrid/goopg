package catalog

import (
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDBOidParameterRoutesToDistinctNamespace exercises the optional trailing
// `dbOid ...uint32` parameter added to InMemory's table/index entry points
// (M0122-0007 slice 4b-ii, docs/design/0122-0018-per-database-catalog-namespace.md).
// No caller passes a real per-connection dbOid yet (that's 4c/4d) — this test
// is the only thing today that exercises ns()'s lazy-create branch with a
// dbOid other than DefaultDBOid, proving the plumbing actually isolates two
// namespaces instead of silently aliasing them.
func TestDBOidParameterRoutesToDistinctNamespace(t *testing.T) {
	c := NewInMemory()
	const otherDBOid = 999

	name := parser.ObjectName{Schema: "public", Name: "widgets"}
	cols := []Column{{Name: "id", Type: Type{Name: "int4"}}}

	// CreateTable under otherDBOid must not collide with, or become visible
	// under, DefaultDBOid.
	tbl, err := c.CreateTable(name, cols, otherDBOid)
	if err != nil {
		t.Fatalf("CreateTable(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupTable(name); ok {
		t.Fatalf("LookupTable(default dbOid) unexpectedly found a table created under dbOid=%d", otherDBOid)
	}
	got, ok := c.LookupTable(name, otherDBOid)
	if !ok || got != tbl {
		t.Fatalf("LookupTable(dbOid=%d) = (%v, %v), want the table created above", otherDBOid, got, ok)
	}

	// A same-named table under DefaultDBOid is a wholly separate object.
	defTbl, err := c.CreateTable(name, cols)
	if err != nil {
		t.Fatalf("CreateTable(default dbOid) failed: %v", err)
	}
	if defTbl == tbl {
		t.Fatal("CreateTable(default dbOid) returned the same *Table as the dbOid=999 create")
	}

	// AllTables/TablesInSchema only see their own namespace.
	if all := c.AllTables(otherDBOid); len(all) != 1 || all[0].OID != tbl.OID {
		t.Fatalf("AllTables(dbOid=%d) = %v, want exactly the one table created there", otherDBOid, all)
	}
	if inSchema := c.TablesInSchema("public", otherDBOid); len(inSchema) != 1 {
		t.Fatalf("TablesInSchema(dbOid=%d) = %v, want exactly one entry", otherDBOid, inSchema)
	}

	// LookupTableByOID / InheritanceChildren / PartitionChildren resolve OIDs
	// within the given namespace only.
	if _, ok := c.LookupTableByOID(tbl.OID); ok {
		t.Fatalf("LookupTableByOID(default dbOid) unexpectedly found OID %d, which only exists under dbOid=%d", tbl.OID, otherDBOid)
	}
	if got, ok := c.LookupTableByOID(tbl.OID, otherDBOid); !ok || got != tbl {
		t.Fatalf("LookupTableByOID(dbOid=%d) = (%v, %v), want the table created above", otherDBOid, got, ok)
	}

	// CreateIndex/LookupIndex/AllIndexes/DropIndex under otherDBOid.
	idxName := parser.ObjectName{Schema: "public", Name: "widgets_id_idx"}
	idx, err := c.CreateIndex(idxName, tbl, []string{"id"}, true, "btree", true, otherDBOid)
	if err != nil {
		t.Fatalf("CreateIndex(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupIndex(idxName); ok {
		t.Fatalf("LookupIndex(default dbOid) unexpectedly found an index created under dbOid=%d", otherDBOid)
	}
	if got, ok := c.LookupIndex(idxName, otherDBOid); !ok || got != idx {
		t.Fatalf("LookupIndex(dbOid=%d) = (%v, %v), want the index created above", otherDBOid, got, ok)
	}
	if all := c.AllIndexes(otherDBOid); len(all) != 1 || all[0].OID != idx.OID {
		t.Fatalf("AllIndexes(dbOid=%d) = %v, want exactly the one index created there", otherDBOid, all)
	}
	if _, ok := c.LookupIndexByOID(idx.OID); ok {
		t.Fatal("LookupIndexByOID(default dbOid) unexpectedly found an index that only exists under dbOid=999")
	}
	if got, ok := c.LookupIndexByOID(idx.OID, otherDBOid); !ok || got != idx {
		t.Fatalf("LookupIndexByOID(dbOid=%d) = (%v, %v), want the index created above", otherDBOid, got, ok)
	}

	// RenameTable/RenameIndex stay within their own namespace.
	renamed := parser.ObjectName{Schema: "public", Name: "widgets2"}
	if err := c.RenameTable(name, renamed, otherDBOid); err != nil {
		t.Fatalf("RenameTable(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupTable(renamed, otherDBOid); !ok {
		t.Fatal("renamed table not found under dbOid=999 after RenameTable")
	}
	if _, ok := c.LookupTable(name); !ok {
		t.Fatal("RenameTable(dbOid=999) unexpectedly renamed the default-namespace table of the same original name")
	}

	// DropIndex/DropTable only remove from the targeted namespace.
	if err := c.DropIndex(idxName, otherDBOid); err != nil {
		t.Fatalf("DropIndex(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if err := c.DropTable(renamed, otherDBOid); err != nil {
		t.Fatalf("DropTable(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupTable(name); !ok {
		t.Fatal("DropTable(dbOid=999) unexpectedly dropped the default-namespace table")
	}
}

// TestSearchPathCatalogDBOidRoutesReads exercises M0122-0007 slice 4c: wiring
// a connection's real database oid into SearchPathCatalog.DBOid so
// LookupTable/LookupIndex key off it instead of always DefaultDBOid. Covers
// the two special cases NamespaceDBOid must translate back to DefaultDBOid
// (zero and PostgresDBOid — the "postgres"/DefaultDBOid dual-mirror, since
// write paths don't route by dbOid until slice 4d) alongside the genuinely
// distinct case (a real, separate dbOid gets its own isolated namespace).
func TestSearchPathCatalogDBOidRoutesReads(t *testing.T) {
	c := NewInMemory()
	const otherDBOid = 777

	name := parser.ObjectName{Schema: "public", Name: "gadgets"}
	cols := []Column{{Name: "id", Type: Type{Name: "int4"}}}

	defTbl, err := c.CreateTable(name, cols) // lands under DefaultDBOid
	if err != nil {
		t.Fatalf("CreateTable(default dbOid) failed: %v", err)
	}
	otherTbl, err := c.CreateTable(name, cols, otherDBOid)
	if err != nil {
		t.Fatalf("CreateTable(dbOid=%d) failed: %v", otherDBOid, err)
	}

	idxName := parser.ObjectName{Schema: "public", Name: "gadgets_id_idx"}
	defIdx, err := c.CreateIndex(idxName, defTbl, []string{"id"}, true, "btree", true)
	if err != nil {
		t.Fatalf("CreateIndex(default dbOid) failed: %v", err)
	}
	otherIdx, err := c.CreateIndex(idxName, otherTbl, []string{"id"}, true, "btree", true, otherDBOid)
	if err != nil {
		t.Fatalf("CreateIndex(dbOid=%d) failed: %v", otherDBOid, err)
	}

	cases := []struct {
		name  string
		dbOid uint32
		want  *Table
	}{
		{"zero (no live connection)", 0, defTbl},
		{"PostgresDBOid (postgres/DefaultDBOid dual-mirror)", PostgresDBOid, defTbl},
		{"DefaultDBOid explicitly", DefaultDBOid, defTbl},
		{"a genuinely distinct dbOid", otherDBOid, otherTbl},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := WithSearchPath(c, func() []string { return nil })
			wrapped.DBOid = tc.dbOid

			gotTbl, ok := wrapped.LookupTable(name)
			if !ok || gotTbl != tc.want {
				t.Fatalf("LookupTable() with SearchPathCatalog.DBOid=%d = (%v, %v), want (%v, true)", tc.dbOid, gotTbl, ok, tc.want)
			}

			wantIdx := defIdx
			if tc.want == otherTbl {
				wantIdx = otherIdx
			}
			gotIdx, ok := wrapped.LookupIndex(idxName)
			if !ok || gotIdx != wantIdx {
				t.Fatalf("LookupIndex() with SearchPathCatalog.DBOid=%d = (%v, %v), want (%v, true)", tc.dbOid, gotIdx, ok, wantIdx)
			}

			// An explicit dbOid argument still wins over the wrapper's DBOid
			// field (unchanged pre-4c contract: a caller that already knows
			// exactly which database it means is never overridden).
			if tc.want != otherTbl {
				if explicitTbl, ok := wrapped.LookupTable(name, otherDBOid); !ok || explicitTbl != otherTbl {
					t.Fatalf("LookupTable(name, otherDBOid) = (%v, %v), want the explicit-dbOid table regardless of wrapper.DBOid=%d", explicitTbl, ok, tc.dbOid)
				}
			}
		})
	}

	// A dbOid nobody ever created a namespace under reads as empty, not a
	// panic or a data race — this is the read-safety fix ns() needed once 4c
	// makes a never-seeded dbOid a normal read instead of a test-only edge
	// case reached under a write lock. M0122-0007 slice 4c.
	t.Run("never-seeded dbOid reads empty", func(t *testing.T) {
		wrapped := WithSearchPath(c, func() []string { return nil })
		wrapped.DBOid = 424242
		if _, ok := wrapped.LookupTable(name); ok {
			t.Fatal("LookupTable() under a never-seeded dbOid unexpectedly found a table")
		}
		if _, ok := wrapped.LookupIndex(idxName); ok {
			t.Fatal("LookupIndex() under a never-seeded dbOid unexpectedly found an index")
		}
	})
}

// TestNsReadOnlyUnderConcurrentUnseededDBOids proves ns()'s read-only fix:
// many goroutines racing LookupTable against distinct dbOids that have no
// namespace yet must never touch the shared c.namespaces map — before this
// fix, ns() lazily inserted into c.namespaces on every miss, which under
// concurrent RLock holders is a concurrent map write (`go test -race` would
// report it, or worse, a "fatal error: concurrent map writes" crash). Slice
// 4c is what makes an unseeded dbOid a normal read instead of a test-only
// edge case reached under a write lock, so this is now a real production
// hazard, not a hypothetical one. M0122-0007 slice 4c.
func TestNsReadOnlyUnderConcurrentUnseededDBOids(t *testing.T) {
	c := NewInMemory()
	name := parser.ObjectName{Schema: "public", Name: "widgets"}

	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// Every goroutine targets its own never-seeded dbOid so each one
			// would hit ns()'s (pre-fix) lazy-create branch concurrently.
			dbOid := uint32(100000 + g)
			for i := 0; i < 100; i++ {
				if _, ok := c.LookupTable(name, dbOid); ok {
					t.Errorf("LookupTable(dbOid=%d) unexpectedly found a table", dbOid)
					return
				}
				if _, ok := c.LookupIndex(name, dbOid); ok {
					t.Errorf("LookupIndex(dbOid=%d) unexpectedly found an index", dbOid)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
