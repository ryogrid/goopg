package executor

// M0146-0063b: every heap writer clears the page's visibility-map bits.
//
// PG clears the bits wherever it modifies an all-visible page: heap_insert,
// heap_delete and heap_update (HOT or not) clear both bits, and
// heap_lock_tuple clears ALL_FROZEN alone (heapam.c). goopg cleared them on its
// main INSERT/DELETE/UPDATE paths only. INSERT … ON CONFLICT DO UPDATE, REFRESH
// MATERIALIZED VIEW, HOT updates, TOAST inserts, logical-apply and catalog
// deletes, and row locks left the bits set.
//
// Measured on a server before this fix: VACUUM, then two upserts that rewrite
// indexed values. `count(*)` by Index Only Scan returned 61 where the heap held
// 49, because the deleted versions' index entries still sat on pages the map
// called all-visible.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

func TestVMClearedByEveryHeapWriter(t *testing.T) {
	// vacuumed builds table `name` from ddl, commits, and VACUUMs it,
	// returning its heap relfilenode with page 0 verified all-visible. With
	// freeze set, page 0 is also marked all-frozen directly: the in-process
	// VACUUM FREEZE does not reach the freeze cutoff, and these cases test
	// the clear, not the freeze.
	vacuumed := func(t *testing.T, ctx *Context, name string, freeze bool, ddl ...string) storage.RelFileNode {
		t.Helper()
		for _, q := range ddl {
			if err := runDDL(t, ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		commitTx(t, ctx)
		beginTx(t, ctx)
		vac := "VACUUM " + name
		if err := runDDL(t, ctx, vac); err != nil {
			t.Fatalf("%s: %v", vac, err)
		}
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %s missing", name)
		}
		rel := ctx.Catalog.RelFileNode(tbl)
		if !ctx.VM.AllVisible(rel, 0) {
			t.Fatalf("precondition: VACUUM left %s page 0 not all-visible", name)
		}
		if freeze {
			ctx.VM.SetAllFrozen(rel, 0)
		}
		return rel
	}

	t.Run("upsert-do-update", func(t *testing.T) {
		ctx, cleanup := newVMFixture(t)
		defer cleanup()
		rel := vacuumed(t, ctx, "up", false,
			"CREATE TABLE up (k int PRIMARY KEY, v int)",
			"CREATE INDEX up_v ON up (v)",
			// Several full pages: the new version of k = 5 lands on a later
			// page, so only the delete half can clear page 0.
			"INSERT INTO up SELECT g, g FROM generate_series(1, 2000) g")
		if err := runDDL(t, ctx, "INSERT INTO up VALUES (5, 0) ON CONFLICT (k) DO UPDATE SET v = excluded.v + 1000"); err != nil {
			t.Fatal(err)
		}
		if ctx.VM.AllVisible(rel, 0) {
			t.Fatal("ON CONFLICT DO UPDATE deleted a version on an all-visible page and left the bit set")
		}
	})

	t.Run("hot-update", func(t *testing.T) {
		ctx, cleanup := newVMFixture(t)
		defer cleanup()
		rel := vacuumed(t, ctx, "hu", true,
			"CREATE TABLE hu (k int PRIMARY KEY, v int)",
			"INSERT INTO hu SELECT g, g FROM generate_series(1, 50) g")
		if err := runDDL(t, ctx, "UPDATE hu SET v = v + 1 WHERE k = 7"); err != nil {
			t.Fatal(err)
		}
		if ctx.VM.AllVisible(rel, 0) || ctx.VM.AllFrozen(rel, 0) {
			t.Fatal("a HOT update left page 0's visibility-map bits set")
		}
	})

	t.Run("refresh-matview", func(t *testing.T) {
		ctx, cleanup := newVMFixture(t)
		defer cleanup()
		for _, q := range []string{
			"CREATE TABLE src (a int)",
			// Several full pages, so the refresh's re-inserts cannot clear
			// page 0 for the deleting stamp.
			"INSERT INTO src SELECT g FROM generate_series(1, 2000) g",
		} {
			if err := runDDL(t, ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		rel := vacuumed(t, ctx, "mv", false, "CREATE MATERIALIZED VIEW mv AS SELECT a FROM src")
		if err := runDDL(t, ctx, "REFRESH MATERIALIZED VIEW mv"); err != nil {
			t.Fatal(err)
		}
		if ctx.VM.AllVisible(rel, 0) {
			t.Fatal("REFRESH MATERIALIZED VIEW deleted the old rows and left page 0 all-visible")
		}
	})

	t.Run("row-lock-clears-only-frozen", func(t *testing.T) {
		ctx, cleanup := newVMFixture(t)
		defer cleanup()
		rel := vacuumed(t, ctx, "lk", true,
			"CREATE TABLE lk (k int PRIMARY KEY, v int)",
			"INSERT INTO lk SELECT g, g FROM generate_series(1, 50) g")
		runQuery(t, ctx, "SELECT k FROM lk WHERE k = 3 FOR UPDATE")
		if ctx.VM.AllFrozen(rel, 0) {
			t.Fatal("a row lock left page 0 all-frozen; VACUUM's all-frozen skip would pass over the locker xmax")
		}
		if !ctx.VM.AllVisible(rel, 0) {
			t.Fatal("a row lock cleared ALL_VISIBLE; PG's heap_lock_tuple clears ALL_FROZEN alone")
		}
	})
}
