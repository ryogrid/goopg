package executor

import (
	"testing"
)

// TestDDLPathsFlagRelcacheInvalPending pins M0106-0011 follow-up: DROP TABLE,
// ALTER TABLE ADD COLUMN, and DROP INDEX must call
// TxnMgr.SetRelcacheInvalPending() so the commit-time xact-marker hook
// (internal/initdb/open.go) emits RecordKindXactCommitInval and refreshes
// `pg_internal.init`. Without the flag, a PG18 standby reconnecting after
// the DDL keeps stale relcache entries (dropped relation still resolved,
// ADD COLUMN attribute count off-by-one).
//
// CREATE TABLE / CREATE INDEX already flag inside their catalogHeapSync
// branch (`syncTableToCatalogHeap`, `syncIndexToCatalogHeap`); those paths
// are exercised in end-to-end fixtures that bootstrap pg_catalog as
// physical relations, so they're not covered here.
func TestDDLPathsFlagRelcacheInvalPending(t *testing.T) {
	t.Run("DropTable", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()
		if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
			t.Fatalf("CREATE TABLE: %v", err)
		}
		// Drain anything set during fixture setup; we only care about
		// the DROP that follows.
		_ = ctx.TxnMgr.TakeRelcacheInvalPending()
		if err := runDDL(t, ctx, "DROP TABLE items"); err != nil {
			t.Fatalf("DROP TABLE: %v", err)
		}
		if !ctx.TxnMgr.TakeRelcacheInvalPending() {
			t.Fatalf("DROP TABLE did not flag relcache-inval pending")
		}
	})

	t.Run("AlterTableAddColumn", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()
		if err := runDDL(t, ctx, "CREATE TABLE items (id int)"); err != nil {
			t.Fatalf("CREATE TABLE: %v", err)
		}
		_ = ctx.TxnMgr.TakeRelcacheInvalPending()
		if err := runDDL(t, ctx, "ALTER TABLE items ADD COLUMN extra int"); err != nil {
			t.Fatalf("ALTER TABLE ADD COLUMN: %v", err)
		}
		if !ctx.TxnMgr.TakeRelcacheInvalPending() {
			t.Fatalf("ALTER TABLE ADD COLUMN did not flag relcache-inval pending")
		}
	})

	t.Run("DropIndex", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()
		if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
			t.Fatalf("CREATE TABLE: %v", err)
		}
		if err := runDDL(t, ctx, "CREATE INDEX items_id_idx ON items (id)"); err != nil {
			t.Fatalf("CREATE INDEX: %v", err)
		}
		_ = ctx.TxnMgr.TakeRelcacheInvalPending()
		if err := runDDL(t, ctx, "DROP INDEX items_id_idx"); err != nil {
			t.Fatalf("DROP INDEX: %v", err)
		}
		if !ctx.TxnMgr.TakeRelcacheInvalPending() {
			t.Fatalf("DROP INDEX did not flag relcache-inval pending")
		}
	})

	t.Run("DropIndexIfExistsMiss", func(t *testing.T) {
		// IF EXISTS on a non-existent index: no catalog mutation, must NOT
		// flag inval (would emit spurious XactCommitInval records).
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()
		_ = ctx.TxnMgr.TakeRelcacheInvalPending()
		if err := runDDL(t, ctx, "DROP INDEX IF EXISTS no_such_idx"); err != nil {
			t.Fatalf("DROP INDEX IF EXISTS: %v", err)
		}
		if ctx.TxnMgr.TakeRelcacheInvalPending() {
			t.Fatalf("DROP INDEX IF EXISTS (miss) flagged relcache-inval — should be no-op")
		}
	})
}
