package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// seedItems fills the items table with three rows so DML tests have a
// known starting state.
func seedItems(t *testing.T, ctx *Context, tbl *catalog.Table) {
	t.Helper()
	in := &planner.Insert{
		Table: tbl,
		Source: &planner.Values{
			Rows: [][]planner.Expr{
				{&planner.IntegerConst{Value: 1}, &planner.StringConst{Value: "alpha"}},
				{&planner.IntegerConst{Value: 2}, &planner.StringConst{Value: "beta"}},
				{&planner.IntegerConst{Value: 3}, &planner.StringConst{Value: "gamma"}},
			},
		},
		ColumnIndex: []int{0, 1},
	}
	op, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("seed insert: %v", err)
	}
	_ = op.Close()
}

// TestUpdateRewritesMatchingRows pins the v0 update protocol: matching
// tuples have their xmax stamped (so they vanish from a subsequent
// scan via TupleVisible's xmax-equals-current-xact branch), and a
// fresh tuple carrying the new row is appended.
func TestUpdateRewritesMatchingRows(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// UPDATE items SET label = 'updated' WHERE id = 2
	upd := &planner.Update{
		Table: tbl,
		Child: &planner.Filter{
			Child: &planner.SeqScan{Table: tbl},
			Predicate: &planner.BinaryOp{
				Op: parser.OpEq,
				Left:  &planner.ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}},
				Right: &planner.IntegerConst{Value: 2},
			},
		},
		Set: []planner.Expr{nil, &planner.StringConst{Value: "updated"}},
	}
	op, err := Build(upd)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Update.Next: %v", err)
	}
	if uo := op.(*updateOp); uo.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1", uo.RowsAffected())
	}
	_ = op.Close()

	// Scan back: same xact should see id=1 alpha, id=3 gamma, and the
	// new id=2 updated. The old id=2 beta is invisible because its
	// xmax = ctx.Tx.XID (TupleVisible's "deleted by current xact"
	// branch returns false).
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, r := range rows {
		got[r[0].Int] = r[1].StringValue()
	}
	want := map[int64]string{1: "alpha", 2: "updated", 3: "gamma"}
	if len(got) != len(want) {
		t.Fatalf("rows=%v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%d]=%q want %q", k, got[k], v)
		}
	}
}

// TestDeleteStampsXmax: matching tuples become invisible after delete
// without removing the page bytes.
func TestDeleteStampsXmax(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// DELETE FROM items WHERE id = 2
	del := &planner.Delete{
		Table: tbl,
		Child: &planner.Filter{
			Child: &planner.SeqScan{Table: tbl},
			Predicate: &planner.BinaryOp{
				Op: parser.OpEq,
				Left:  &planner.ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}},
				Right: &planner.IntegerConst{Value: 2},
			},
		},
	}
	op, err := Build(del)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = op.Next()
	if d := op.(*deleteOp); d.RowsAffected() != 1 {
		t.Errorf("RowsAffected=%d want 1", d.RowsAffected())
	}
	_ = op.Close()

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	for _, r := range rows {
		if r[0].Int == 2 {
			t.Errorf("deleted row still visible: %+v", r)
		}
	}
}

// TestDeleteAllRowsWithoutPredicate: no Filter wrapping the SeqScan
// in the child plan means every row matches.
func TestDeleteAllRowsWithoutPredicate(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	del := &planner.Delete{
		Table: tbl,
		Child: &planner.SeqScan{Table: tbl},
	}
	op, _ := Build(del)
	_ = op.Open(ctx)
	_, _ = op.Next()
	if d := op.(*deleteOp); d.RowsAffected() != 3 {
		t.Errorf("RowsAffected=%d want 3", d.RowsAffected())
	}
	_ = op.Close()

	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, _ := drainScan(scan)
	if len(rows) != 0 {
		t.Errorf("rows=%d want 0", len(rows))
	}
}

// TestUpdateViaIndexScanPath — M0021 step 2d. Once the items
// table has a unique index on id, the planner picks IndexScan
// (or Filter(IndexScan) when an extra predicate is added) for
// `UPDATE … WHERE id = N`. Verifies that the executor's
// extractScanAndPredicate handles both shapes correctly,
// synthesising the index key as a `=` predicate so scanMatching
// still filters to the right rows; the rewrite produces the
// same observable outcome as the SeqScan-based path.
func TestUpdateViaIndexScanPath(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE items (id int, label text)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	// Index forces planUpdate to pick the IndexScan branch.
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX items_pk_2d ON items (id)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "UPDATE items SET label = 'updated' WHERE id = 2"); err != nil {
		t.Fatalf("UPDATE WHERE indexed: %v", err)
	}
	scan := newSeqScanOp(&planner.SeqScan{Table: tbl})
	_ = scan.Open(ctx)
	defer scan.Close()
	rows, err := drainScan(scan)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, r := range rows {
		got[r[0].Int] = r[1].StringValue()
	}
	want := map[int64]string{1: "alpha", 2: "updated", 3: "gamma"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%d]=%q want %q", k, got[k], v)
		}
	}
}

// TestDMLRequiresTablePrivilege pins dmlPrivilegePermitted (M0097-0040): a
// non-superuser role with no GRANT and no ownership on the target table gets
// 42501 on INSERT/UPDATE/DELETE, gaining access once granted the matching
// privilege; the table owner and the bootstrap superuser always pass without
// any GRANT. Mirrors the TRUNCATE privilege check (M0118-0008
// truncate-conflict) that the same internal/catalog tableACLs store already
// backs for pg_dump round-tripping but was never consulted for plain DML.
func TestDMLRequiresTablePrivilege(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	insertStmt := func() *planner.Insert {
		return &planner.Insert{
			Table:       tbl,
			Source:      &planner.Values{Rows: [][]planner.Expr{{&planner.IntegerConst{Value: 4}, &planner.StringConst{Value: "delta"}}}},
			ColumnIndex: []int{0, 1},
		}
	}
	updateStmt := func() *planner.Update {
		return &planner.Update{
			Table: tbl,
			Child: &planner.Filter{
				Child:     &planner.SeqScan{Table: tbl},
				Predicate: &planner.BinaryOp{Op: parser.OpEq, Left: &planner.ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}}, Right: &planner.IntegerConst{Value: 1}},
			},
			Set: []planner.Expr{nil, &planner.StringConst{Value: "updated"}},
		}
	}
	deleteStmt := func() *planner.Delete {
		return &planner.Delete{
			Table: tbl,
			Child: &planner.Filter{
				Child:     &planner.SeqScan{Table: tbl},
				Predicate: &planner.BinaryOp{Op: parser.OpEq, Left: &planner.ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}}, Right: &planner.IntegerConst{Value: 1}},
			},
		}
	}

	assertDenied := func(t *testing.T, plan planner.Node, priv string) {
		t.Helper()
		op, err := Build(plan)
		if err != nil {
			t.Fatal(err)
		}
		err = op.Open(ctx)
		execErr, ok := err.(*ExecError)
		if !ok || execErr.Code != "42501" {
			t.Fatalf("%s: expected 42501 permission-denied, got %#v", priv, err)
		}
	}
	assertAllowed := func(t *testing.T, plan planner.Node, priv string) {
		t.Helper()
		op, err := Build(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatalf("%s: expected success, got %v", priv, err)
		}
		if _, err := op.Next(); err != EOF {
			t.Fatalf("%s: Next: %v", priv, err)
		}
		_ = op.Close()
	}

	// Unprivileged non-superuser role: every DML op is denied.
	ctx.NonSuperuserRole = "alice"
	assertDenied(t, insertStmt(), "INSERT")
	assertDenied(t, updateStmt(), "UPDATE")
	assertDenied(t, deleteStmt(), "DELETE")

	// GRANT the matching privilege one at a time: each op starts passing.
	im.GrantTablePrivilege(tbl.OID, "alice", "INSERT")
	assertAllowed(t, insertStmt(), "INSERT")
	assertDenied(t, updateStmt(), "UPDATE")
	im.GrantTablePrivilege(tbl.OID, "alice", "UPDATE")
	assertAllowed(t, updateStmt(), "UPDATE")
	assertDenied(t, deleteStmt(), "DELETE")
	im.GrantTablePrivilege(tbl.OID, "alice", "DELETE")
	assertAllowed(t, deleteStmt(), "DELETE")

	// The table owner passes without any GRANT.
	ctx.NonSuperuserRole = "bob"
	tbl.Owner = "bob"
	assertAllowed(t, deleteStmt(), "DELETE (owner)")
	tbl.Owner = ""

	// The bootstrap superuser (no SET ROLE) always passes.
	ctx.NonSuperuserRole = ""
	assertAllowed(t, deleteStmt(), "DELETE (superuser)")
}

// TestSeqScanRequiresSelectPrivilege extends M0097-0040 to SELECT: a
// non-superuser role with no GRANT and no ownership gets 42501 reading a
// user table via SeqScan, gaining access once granted SELECT; the owner
// and the bootstrap superuser always pass. Sibling coverage for
// IndexScan/IndexOnlyScan lives in TestIndexScansRequireSelectPrivilege
// below — a naive fix to only seqScanOp would leave an index-scan-chosen
// plan able to bypass the gate entirely.
func TestSeqScanRequiresSelectPrivilege(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	scanStmt := func() *planner.SeqScan { return &planner.SeqScan{Table: tbl} }

	assertDenied := func(t *testing.T) {
		t.Helper()
		op, err := Build(scanStmt())
		if err != nil {
			t.Fatal(err)
		}
		err = op.Open(ctx)
		execErr, ok := err.(*ExecError)
		if !ok || execErr.Code != "42501" {
			t.Fatalf("expected 42501 permission-denied, got %#v", err)
		}
	}
	assertAllowed := func(t *testing.T) {
		t.Helper()
		op, err := Build(scanStmt())
		if err != nil {
			t.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		_ = op.Close()
	}

	ctx.NonSuperuserRole = "alice"
	assertDenied(t)

	im.GrantTablePrivilege(tbl.OID, "alice", "SELECT")
	assertAllowed(t)

	// The table owner passes without any GRANT.
	ctx.NonSuperuserRole = "bob"
	tbl.Owner = "bob"
	assertAllowed(t)
	tbl.Owner = ""

	// The bootstrap superuser (no SET ROLE) always passes.
	ctx.NonSuperuserRole = ""
	assertAllowed(t)
}

// TestIndexScansRequireSelectPrivilege pins the IndexScan/IndexOnlyScan
// sibling of TestSeqScanRequiresSelectPrivilege's SeqScan gate directly at
// the dmlPrivilegePermitted layer both operators call — a regression that
// only re-checks SeqScan would miss an index-scan-chosen plan silently
// reading a table the role has no SELECT grant on.
func TestIndexScansRequireSelectPrivilege(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})

	ctx.NonSuperuserRole = "alice"
	if dmlPrivilegePermitted(ctx, tbl, "SELECT") {
		t.Fatal("expected SELECT denied with no grant")
	}
	idxOp := &indexScanOp{plan: &planner.IndexScan{Table: tbl}}
	if err := idxOp.openPrep(ctx); err == nil {
		t.Fatal("indexScanOp.openPrep: expected 42501, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("indexScanOp.openPrep: expected 42501, got %#v", err)
	}
	ionOp := &indexOnlyScanOp{plan: &planner.IndexOnlyScan{Table: tbl}}
	if err := ionOp.Open(ctx); err == nil {
		t.Fatal("indexOnlyScanOp.Open: expected 42501, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("indexOnlyScanOp.Open: expected 42501, got %#v", err)
	}
}

// TestScanOperatorsUseViewOwnerPrivilegeOverride pins the view-owner
// privilege fix (M0122-0008): a scan tagged by the planner's
// tagViewOwnerScans (PrivilegeCheckRoleSet) must check the SELECT grant of
// the *tagged* role, not the querying session's own ctx.NonSuperuserRole —
// this is how `GRANT SELECT ON view TO role` alone (no base-table grant)
// still lets role query through an inlined view (PostgreSQL's default,
// non-security_invoker view semantics: reads run as the view owner).
// Covers all three SELECT-gated scan operators (M0097-0040's sibling
// trio) since each extracts/reads the plan's PrivilegeCheckRole
// independently.
func TestScanOperatorsUseViewOwnerPrivilegeOverride(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	tbl.Owner = "bob"

	// The querying session is "alice", who holds no grant on items at all —
	// direct access would be denied. tagViewOwnerScans has stamped the scan
	// with "bob" (the view's owner and items' owner), so it must succeed.
	ctx.NonSuperuserRole = "alice"

	seqOp := &seqScanOp{tbl: tbl, cols: tbl.Columns, privilegeCheckRole: "bob", privilegeCheckRoleSet: true}
	if err := seqOp.Open(ctx); err != nil {
		t.Fatalf("seqScanOp.Open: expected success via owner override, got %v", err)
	}
	_ = seqOp.Close()

	// indexScanOp/indexOnlyScanOp check the same override before touching
	// any index/lock state (mirrors TestIndexScansRequireSelectPrivilege,
	// which likewise only drives these two through the denial path — a
	// bare *planner.IndexScan{Table: tbl} with no real Index/btree can't
	// proceed past the privilege gate). A tagged role that is neither the
	// owner nor a grantee must be denied regardless of the querying
	// session's own privileges — proves checkRole REPLACES the querying
	// role rather than merely supplementing it.
	idxOpDenied := &indexScanOp{plan: &planner.IndexScan{Table: tbl, PrivilegeCheckRole: "carol", PrivilegeCheckRoleSet: true}}
	if err := idxOpDenied.openPrep(ctx); err == nil {
		t.Fatal("indexScanOp.openPrep: expected 42501 for untagged non-owner role, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("indexScanOp.openPrep: expected 42501, got %#v", err)
	}
	ionOpDenied := &indexOnlyScanOp{plan: &planner.IndexOnlyScan{Table: tbl, PrivilegeCheckRole: "carol", PrivilegeCheckRoleSet: true}}
	if err := ionOpDenied.Open(ctx); err == nil {
		t.Fatal("indexOnlyScanOp.Open: expected 42501 for untagged non-owner role, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("indexOnlyScanOp.Open: expected 42501, got %#v", err)
	}

	seqOpDenied := &seqScanOp{tbl: tbl, cols: tbl.Columns, privilegeCheckRole: "carol", privilegeCheckRoleSet: true}
	if err := seqOpDenied.Open(ctx); err == nil {
		t.Fatal("seqScanOp.Open: expected 42501 for untagged non-owner role, got nil")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42501" {
		t.Fatalf("seqScanOp.Open: expected 42501, got %#v", err)
	}

	// A GRANT to the tagged role (rather than ownership) is honored too.
	im.GrantTablePrivilege(tbl.OID, "carol", "SELECT")
	seqOpGranted := &seqScanOp{tbl: tbl, cols: tbl.Columns, privilegeCheckRole: "carol", privilegeCheckRoleSet: true}
	if err := seqOpGranted.Open(ctx); err != nil {
		t.Fatalf("seqScanOp.Open: expected success via tagged-role grant, got %v", err)
	}
	_ = seqOpGranted.Close()
}

// TestSystemCatalogSelectAlwaysPermitted pins the dmlPrivilegePermitted
// carve-out that keeps pg_catalog/information_schema readable by any role
// even though goopg has no per-catalog default-ACL (pg_init_privs)
// seeding — without it, gating SELECT would 42501 every psql `\d`,
// pg_dump, and information_schema query issued by a non-superuser role.
func TestSystemCatalogSelectAlwaysPermitted(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	_ = cat
	ctx.NonSuperuserRole = "alice"

	sysTbl := &catalog.Table{OID: 1259, Name: "pg_class", Owner: "postgres"} // < FirstUserOID (16384)
	if !dmlPrivilegePermitted(ctx, sysTbl, "SELECT") {
		t.Fatal("expected system catalog SELECT to always be permitted")
	}
	// INSERT/UPDATE/DELETE on a system catalog are unaffected by the
	// SELECT carve-out — a non-superuser role still needs a real grant.
	if dmlPrivilegePermitted(ctx, sysTbl, "INSERT") {
		t.Fatal("expected system catalog INSERT to still require a grant")
	}
}
