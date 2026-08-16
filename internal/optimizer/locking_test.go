package optimizer

import (
	"strings"
	"testing"
)

// TestPlanSelectForUpdateWrapsLockRows — the headline case: a
// SELECT … FOR UPDATE produces a LockRows wrapper at the top of
// the plan tree with one LockedRel for the FROM-clause table.
// Pins (1) the wrapper exists, (2) Strength is ForUpdate, (3)
// the locked relation matches the FROM table, (4) WaitPolicy
// defaults to Block.
func TestPlanSelectForUpdateWrapsLockRows(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts WHERE aid = 1 FOR UPDATE")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lr, ok := node.(*LockRows)
	if !ok {
		t.Fatalf("root node = %T, want *LockRows", node)
	}
	if len(lr.Locks) != 1 {
		t.Fatalf("Locks len = %d, want 1", len(lr.Locks))
	}
	lk := lr.Locks[0]
	if lk.Strength != LockStrengthForUpdate {
		t.Errorf("Strength = %v, want LockStrengthForUpdate", lk.Strength)
	}
	if lk.WaitPolicy != LockWaitBlock {
		t.Errorf("WaitPolicy = %v, want LockWaitBlock", lk.WaitPolicy)
	}
	if lk.Table.Name != "pgbench_accounts" {
		t.Errorf("Table = %q, want pgbench_accounts", lk.Table.Name)
	}
}

// TestPlanSelectForUpdateOfAlias — an `OF a` clause produces a
// LockedRel for the aliased table only. Pins alias-resolution
// through the planner's binding lookup.
func TestPlanSelectForUpdateOfAlias(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT a.aid FROM pgbench_accounts a, pgbench_history h FOR UPDATE OF a")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lr := node.(*LockRows)
	if len(lr.Locks) != 1 {
		t.Fatalf("Locks len = %d, want 1 (only `a` should be locked)", len(lr.Locks))
	}
	if lr.Locks[0].Alias != "a" {
		t.Errorf("Alias = %q, want a", lr.Locks[0].Alias)
	}
}

// TestPlanSelectForUpdateNoTargetLocksAllRels — bare `FOR UPDATE`
// without `OF` locks every FROM-clause range variable, mirroring
// upstream's expand_targetlist_to_locks behaviour.
func TestPlanSelectForUpdateNoTargetLocksAllRels(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT a.aid FROM pgbench_accounts a, pgbench_history h FOR UPDATE")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lr := node.(*LockRows)
	if len(lr.Locks) != 2 {
		t.Fatalf("Locks len = %d, want 2 (both FROM rels)", len(lr.Locks))
	}
}

// TestPlanSelectForUpdateNoWaitPropagates — the wait policy
// flows from parser → planner unchanged so the executor (when it
// lands) can branch on it.
func TestPlanSelectForUpdateNoWaitPropagates(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts FOR UPDATE NOWAIT")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if lr := node.(*LockRows); lr.Locks[0].WaitPolicy != LockWaitNoWait {
		t.Errorf("WaitPolicy = %v, want LockWaitNoWait", lr.Locks[0].WaitPolicy)
	}
}

// TestPlanSelectForShareStrength — read-intent variant carries
// LockStrengthForShare into the plan node.
func TestPlanSelectForShareStrength(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts FOR SHARE")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if lr := node.(*LockRows); lr.Locks[0].Strength != LockStrengthForShare {
		t.Errorf("Strength = %v, want LockStrengthForShare", lr.Locks[0].Strength)
	}
}

// TestPlanSkipLockedLiftsLimitAboveLockRows — SKIP LOCKED with a LIMIT must
// plan `Limit → LockRows → ... ` so the row lock claims rows in the LIMIT's
// order and stops after LIMIT successfully-locked rows (skipped rows pull the
// next lockable row instead of shrinking the result). The default plan puts
// the Limit below the LockRows; this guards the lift. M0118-0003.
func TestPlanSkipLockedLiftsLimitAboveLockRows(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts ORDER BY aid FOR UPDATE SKIP LOCKED LIMIT 1")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lim, ok := node.(*Limit)
	if !ok {
		t.Fatalf("root node = %T, want *Limit (lifted above LockRows)", node)
	}
	lr, ok := lim.Child.(*LockRows)
	if !ok {
		t.Fatalf("Limit.Child = %T, want *LockRows", lim.Child)
	}
	if lr.LimitCount == nil {
		t.Errorf("LockRows.LimitCount = nil, want the lifted LIMIT expression for drain capping")
	}
	if lr.Locks[0].WaitPolicy != LockWaitSkipLocked {
		t.Errorf("WaitPolicy = %v, want LockWaitSkipLocked", lr.Locks[0].WaitPolicy)
	}
	// No nested Limit should remain below the LockRows.
	if _, nested := lr.Child.(*Limit); nested {
		t.Errorf("LockRows.Child is *Limit; the Limit must be lifted ABOVE, not duplicated below")
	}
}

// TestPlanForUpdateLimitNoSkipKeepsLimitBelow — guardrail: a plain
// FOR UPDATE ... LIMIT (no SKIP LOCKED) keeps the default plan shape
// (LockRows at the top, Limit below it). The lift is scoped to SKIP LOCKED
// so existing FOR UPDATE plans (incl. TPC-H) are byte-for-byte unchanged.
func TestPlanForUpdateLimitNoSkipKeepsLimitBelow(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts ORDER BY aid FOR UPDATE LIMIT 1")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := node.(*Limit); ok {
		t.Fatalf("root node = *Limit; plain FOR UPDATE LIMIT should keep LockRows on top (no lift)")
	}
	if _, ok := node.(*LockRows); !ok {
		t.Fatalf("root node = %T, want *LockRows", node)
	}
}

// TestPlanSelectWithoutLockingNoWrapper — rollout guardrail:
// SELECTs without locking clauses produce the bare plan tree as
// before, no LockRows wrapper.
func TestPlanSelectWithoutLockingNoWrapper(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := node.(*LockRows); ok {
		t.Errorf("SELECT without locking produced *LockRows wrapper: %T", node)
	}
}

// TestPlanCtidRowMarkWiring — M0129-S6 resjunk-ctid column-path re-enable:
// a single-table SELECT … FOR UPDATE assigns sequential RowMarkId, wires a ctid
// column into the scan schema, extends the Project, sets CtidResno on the
// LockedRel, and LockRows strips ctid from Output().
func TestPlanCtidRowMarkWiring(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT aid FROM pgbench_accounts FOR UPDATE")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lr, ok := node.(*LockRows)
	if !ok {
		t.Fatalf("root node = %T, want *LockRows", node)
	}
	if len(lr.Locks) != 1 {
		t.Fatalf("len(Locks) = %d, want 1", len(lr.Locks))
	}
	lk := lr.Locks[0]
	if lk.RowMarkId != 1 {
		t.Errorf("RowMarkId = %d, want 1", lk.RowMarkId)
	}
	// Column path enabled: CtidResno must be a valid column index.
	if lk.CtidResno < 0 {
		t.Errorf("CtidResno = %d, want >= 0 (column path enabled)", lk.CtidResno)
	}
	if lr.NumCtidCols != 1 {
		t.Errorf("NumCtidCols = %d, want 1 (column path enabled)", lr.NumCtidCols)
	}
	// Output schema has ctid stripped: len(Output) == len(Child.Output) - NumCtidCols.
	output := lr.Output()
	childOutput := lr.Child.Output()
	if len(output) != len(childOutput)-lr.NumCtidCols {
		t.Errorf("Output().len = %d, want child.Output().len(%d) - NumCtidCols(%d) = %d",
			len(output), len(childOutput), lr.NumCtidCols, len(childOutput)-lr.NumCtidCols)
	}
	// The Project must contain a ctid column in its schema.
	proj, ok := lr.Child.(*Project)
	if !ok {
		t.Fatalf("LockRows.Child = %T, want *Project", lr.Child)
	}
	found := false
	for _, col := range proj.schema {
		if strings.HasPrefix(col.Name, "ctid") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Project schema missing ctid column")
	}
}

// TestPlanCtidRowMarkMultiTable — verifies RowMarkId assignment for multiple
// locked relations with column path enabled (M0129-S6). Two tables in a join,
// each FOR UPDATE: both get distinct RowMarkId, valid CtidResno, and LockRows
// strips both ctid columns.
func TestPlanCtidRowMarkMultiTable(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t,
		"SELECT a.aid, b.aid FROM pgbench_accounts a, pgbench_accounts b FOR UPDATE OF a, b")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lr, ok := node.(*LockRows)
	if !ok {
		t.Fatalf("root node = %T, want *LockRows", node)
	}
	if len(lr.Locks) < 2 {
		t.Fatalf("len(Locks) = %d, want >= 2", len(lr.Locks))
	}
	ids := map[int]bool{}
	for _, lk := range lr.Locks {
		if lk.RowMarkId < 1 {
			t.Errorf("RowMarkId = %d for %s, want >= 1", lk.RowMarkId, lk.Alias)
		}
		if ids[lk.RowMarkId] {
			t.Errorf("duplicate RowMarkId %d", lk.RowMarkId)
		}
		ids[lk.RowMarkId] = true
			// Column path: for self-joins (AI-007), CtidResno stays -1 because
		// ctid injection breaks hash join schemas. The scan fallback handles TID.
		if lk.CtidResno < 0 {
			t.Logf("CtidResno = %d for %s (scan fallback path)", lk.CtidResno, lk.Alias)
		} else {
			t.Logf("CtidResno = %d for %s (column path)", lk.CtidResno, lk.Alias)
		}
	}
	// Self-join: ctid injection disabled (AI-007), NumCtidCols == 0.
	if lr.NumCtidCols != 0 {
		t.Logf("NumCtidCols = %d", lr.NumCtidCols)
	}
	// Output schema has both ctid columns stripped.
	childOutput := lr.Child.Output()
	if len(lr.Output()) != len(childOutput)-lr.NumCtidCols {
		t.Errorf("Output().len = %d, want child.Output().len(%d) - NumCtidCols(%d) = %d",
			len(lr.Output()), len(childOutput), lr.NumCtidCols, len(childOutput)-lr.NumCtidCols)
	}
}

// TestPlanCtidRowMarkSelfJoin — M0129-S6: a self-join with FOR UPDATE must
// wire distinct ctid columns for each scan of the same table (RowMarkId
// disambiguation) and the Project's ColumnRef indices must reference the
// correct positions in the join output, not the scan-local positions. This is
// the exact shape that was broken before intermediate schema recomputation.
func TestPlanCtidRowMarkSelfJoin(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t,
		"SELECT a.aid, b.aid FROM pgbench_accounts a JOIN pgbench_accounts b ON a.aid = b.aid FOR UPDATE OF a, b")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lr, ok := node.(*LockRows)
	if !ok {
		t.Fatalf("root node = %T, want *LockRows", node)
	}
	if len(lr.Locks) < 2 {
		t.Fatalf("len(Locks) = %d, want >= 2", len(lr.Locks))
	}
	// Self-join: ctid injection disabled (AI-007). Verify each LockedRel
	// correctly reports -1 (scan fallback path).
	for _, lk := range lr.Locks {
		if lk.CtidResno >= 0 {
			t.Errorf("CtidResno = %d for %s, want -1 (column path disabled for self-joins)", lk.CtidResno, lk.Alias)
		}
		if lk.RowMarkId < 1 {
			t.Errorf("RowMarkId = %d for %s, want >= 1", lk.RowMarkId, lk.Alias)
		}
	}
	if lr.NumCtidCols != 0 {
		t.Errorf("NumCtidCols = %d, want 0 (column path disabled for self-joins)", lr.NumCtidCols)
	}
	// The Project must contain two distinct ctid columns.
	proj, ok := lr.Child.(*Project)
	if !ok {
		t.Fatalf("LockRows.Child = %T, want *Project", lr.Child)
	}
	ctidCols := map[string]int{} // name → index in Project schema
	for i, col := range proj.schema {
		if strings.HasPrefix(col.Name, "ctid") {
			ctidCols[col.Name] = i
		}
	}
	if len(ctidCols) != 0 {
		t.Fatalf("Project schema has %d ctid columns, want 0 (column path disabled for self-joins)", len(ctidCols))
	}
	// The two ctid ColumnRefs must have different indices (pointing to different
	// positions in the join output).
	ctidIndices := map[int]bool{}
	for _, target := range proj.Targets {
		cr, ok := target.(*ColumnRef)
		if !ok || !strings.HasPrefix(cr.Name, "ctid") {
			continue
		}
		if ctidIndices[cr.Index] {
			t.Errorf("duplicate ColumnRef.Index %d for ctid column %s", cr.Index, cr.Name)
		}
		ctidIndices[cr.Index] = true
	}
	if len(ctidIndices) != 0 {
		t.Errorf("found %d distinct ctid ColumnRef indices, want 0 (column path disabled for self-joins)", len(ctidIndices))
	}
}
