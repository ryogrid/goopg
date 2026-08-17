package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// newAmprocUpdateFixture wires a Context with the storage/txn handles
// updateOp.Open requires (Pool/Catalog non-nil) even though the pg_amproc
// path itself never touches the heap — mirrors newDatFrozenXIDFixture.
func newAmprocUpdateFixture(t *testing.T) *Context {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	cat := catalog.NewInMemory()
	mgrMVCC := transam.NewManager()
	tx, err := mgrMVCC.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := mgrMVCC.SnapshotFor(tx)
	if err != nil {
		t.Fatal(err)
	}
	ctx := NewContext()
	ctx.Pool = pool
	ctx.Catalog = cat
	ctx.TxnMgr = mgrMVCC
	ctx.Tx = tx
	ctx.Snap = snap
	t.Cleanup(func() {
		_ = mgrMVCC.Rollback(tx)
		_ = pool.Close()
		_ = mgr.Close()
	})
	return ctx
}

// TestUpdatePgAmprocRewritesAmprocColumn pins nextVirtualPgAmproc: pg_amproc
// is Virtual (no physical heap), so `UPDATE pg_amproc SET amproc = ...`
// needs its own read/match/write path, mirroring nextVirtualPgDatabase. This
// is the exact statement shape pg_amcheck's upstream 005_opclass_damage.pl
// uses to inject corruption (`UPDATE pg_catalog.pg_amproc SET amproc =
// '<other fn>'::regproc WHERE amproc = '<fn>'::regproc`). M0119-0006.
func TestUpdatePgAmprocRewritesAmprocColumn(t *testing.T) {
	ctx := newAmprocUpdateFixture(t)
	if err := runDDL(t, ctx, `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING btree`); err != nil {
		t.Fatalf("CREATE OPERATOR FAMILY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class FOR TYPE int4 USING btree FAMILY public.op_family AS
		OPERATOR 1 ~=~ (int4, int4),
		FUNCTION 1 int4eq(int4, int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR CLASS: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	before := pgAmprocVirtualRows(t, im)
	if len(before) != 1 {
		t.Fatalf("pg_amproc VirtualRows = %d rows, want 1: %v", len(before), before)
	}
	if before[0][5] != "65" { // int4eq's curated builtin OID
		t.Fatalf("amproc = %q, want 65 (int4eq)", before[0][5])
	}

	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc SET amproc = 'btint4cmp'::regproc WHERE amproc = 'int4eq'::regproc`); err != nil {
		t.Fatalf("UPDATE pg_amproc: %v", err)
	}

	after := pgAmprocVirtualRows(t, im)
	if len(after) != 1 {
		t.Fatalf("post-UPDATE pg_amproc VirtualRows = %d rows, want 1: %v", len(after), after)
	}
	if after[0][5] != "351" { // btint4cmp's curated builtin OID
		t.Errorf("amproc = %q after UPDATE, want 351 (btint4cmp)", after[0][5])
	}
	if after[0][0] != before[0][0] {
		t.Errorf("oid changed across UPDATE: %q -> %q, want unchanged (in-place rewrite)", before[0][0], after[0][0])
	}

	// A non-matching WHERE leaves the row untouched.
	if err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc SET amproc = 'int4eq'::regproc WHERE amproc = 'eqsel'::regproc`); err != nil {
		t.Fatalf("no-op UPDATE pg_amproc: %v", err)
	}
	unchanged := pgAmprocVirtualRows(t, im)
	if unchanged[0][5] != "351" {
		t.Errorf("amproc = %q after no-op UPDATE, want unchanged 351", unchanged[0][5])
	}
}

// TestUpdatePgAmprocRejectsOtherColumns confirms the Virtual-UPDATE path
// only accepts writes to the amproc column, matching nextVirtualPgDatabase's
// "refuse rather than silently discard" precedent for any other column.
func TestUpdatePgAmprocRejectsOtherColumns(t *testing.T) {
	ctx := newAmprocUpdateFixture(t)
	if err := runDDL(t, ctx, `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR FAMILY public.op_family USING btree`); err != nil {
		t.Fatalf("CREATE OPERATOR FAMILY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR CLASS public.op_class FOR TYPE int4 USING btree FAMILY public.op_family AS
		OPERATOR 1 ~=~ (int4, int4),
		FUNCTION 1 int4eq(int4, int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR CLASS: %v", err)
	}

	err := runDDL(t, ctx, `UPDATE pg_catalog.pg_amproc SET amprocnum = 2 WHERE amproc = 'int4eq'::regproc`)
	if err == nil {
		t.Fatal("expected UPDATE pg_amproc SET amprocnum to error, got nil")
	}
	execErr, ok := err.(*ExecError)
	if !ok || execErr.Code != "0A000" {
		t.Fatalf("err = %v, want *ExecError{Code: 0A000}", err)
	}
}
