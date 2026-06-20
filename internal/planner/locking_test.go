package planner

import "testing"

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
