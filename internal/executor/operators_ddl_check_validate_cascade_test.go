package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0134-0005an: `ALTER TABLE ... VALIDATE CONSTRAINT <check>` flipped ONLY
// the named table's own NamedChecks[i].NotValid, leaving every
// inheritance/partition descendant's copy at convalidated='f' where PG
// cascades the validation down the whole descendant tree
// (QueueCheckConstraintValidation, postgres/src/backend/commands/
// tablecmds.c:13109-13210). Separately, goopg's existing-row scan
// (validateCheckConstraintRows -> forEachLiveRow) always walked the whole
// descendant tree even for a NO INHERIT constraint, where PG's
// find_all_inheritors walk is gated on `!con->connoinherit`
// (tablecmds.c:13140) so a NO INHERIT constraint's Phase-3 scan never
// touches descendant storage at all. These tests pin both halves for both
// VALIDATE CONSTRAINT and its ADD CHECK twin, mirroring
// alter_table.sql:397-428 (attmp3/attmp6/attmp7,
// parent_noinh_convalid/child_noinh_convalid).

// TestAlterTableValidateCheckCascadesToDescendants covers the 3-level plain
// inheritance case: a CHECK added NOT VALID on the parent and inherited down
// to child and grandchild; VALIDATE CONSTRAINT on the parent must flip
// NotValid=false on the parent AND both descendants.
func TestAlterTableValidateCheckCascadesToDescendants(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// The CHECK is added BEFORE the children exist so goopg's
	// CREATE-TABLE-INHERITS propagation path (operators_ddl.go:5165, distinct
	// from AlterTableAddCheck's own children loop) picks it up on both
	// descendants — AlterTableAddCheck itself only propagates a freshly-added
	// CHECK to PartitionChildren, not pre-existing inheritance children (a
	// separate, already-ledgered gap; see report.md).
	for _, stmt := range []string{
		`CREATE TABLE vchk_parent (a int, b int)`,
		`ALTER TABLE vchk_parent ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`,
		`CREATE TABLE vchk_child (a int, b int) INHERITS (vchk_parent)`,
		`CREATE TABLE vchk_grand () INHERITS (vchk_parent, vchk_child)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "vchk_child"})
	if !ok {
		t.Fatal("vchk_child not found")
	}
	grand, ok := cat.LookupTable(parser.ObjectName{Name: "vchk_grand"})
	if !ok {
		t.Fatal("vchk_grand not found")
	}
	// AddCheckInherited (operators_ddl.go:305, used by the
	// CREATE-TABLE-INHERITS propagation loop above) always stamps the
	// propagated copy convalidated='t' regardless of the parent's own NOT
	// VALID — a distinct, already-ledgered gap (constraint-inheritance
	// stamping, not this brief's VALIDATE CONSTRAINT cascade). Set the
	// precondition directly so this test isolates
	// cascadeCheckValidateToDescendants itself.
	child.NamedChecks[findNamedCheck(t, child, "b_le_20")].NotValid = true
	grand.NamedChecks[findNamedCheck(t, grand, "b_le_20")].NotValid = true
	if idx := findNamedCheck(t, child, "b_le_20"); !child.NamedChecks[idx].NotValid {
		t.Fatalf("precondition: vchk_child's b_le_20 already valid before VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, grand, "b_le_20"); !grand.NamedChecks[idx].NotValid {
		t.Fatalf("precondition: vchk_grand's b_le_20 already valid before VALIDATE CONSTRAINT")
	}

	if err := runDDL(t, ctx, `ALTER TABLE vchk_parent VALIDATE CONSTRAINT b_le_20`); err != nil {
		t.Fatalf("ALTER TABLE vchk_parent VALIDATE CONSTRAINT b_le_20: %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "vchk_parent"})
	if !ok {
		t.Fatal("vchk_parent not found")
	}
	if idx := findNamedCheck(t, parent, "b_le_20"); parent.NamedChecks[idx].NotValid {
		t.Errorf("vchk_parent's b_le_20.NotValid = true after VALIDATE CONSTRAINT, want false")
	}

	child, ok = cat.LookupTable(parser.ObjectName{Name: "vchk_child"})
	if !ok {
		t.Fatal("vchk_child not found after VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, child, "b_le_20"); child.NamedChecks[idx].NotValid {
		t.Errorf("vchk_child's b_le_20.NotValid = true after parent VALIDATE CONSTRAINT, want false (cascade)")
	}

	grand, ok = cat.LookupTable(parser.ObjectName{Name: "vchk_grand"})
	if !ok {
		t.Fatal("vchk_grand not found after VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, grand, "b_le_20"); grand.NamedChecks[idx].NotValid {
		t.Errorf("vchk_grand's b_le_20.NotValid = true after parent VALIDATE CONSTRAINT, want false (2-level cascade)")
	}
}

// TestAlterTableValidateCheckCascadesToPartitions covers the same cascade
// against a partitioned tree.
func TestAlterTableValidateCheckCascadesToPartitions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE vchk_ptbl (a int, b int) PARTITION BY LIST (a)`,
		`ALTER TABLE vchk_ptbl ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`,
		`CREATE TABLE vchk_ptbl_1 PARTITION OF vchk_ptbl FOR VALUES IN (1,2)`,
		`CREATE TABLE vchk_ptbl_2 PARTITION OF vchk_ptbl FOR VALUES IN (3,4)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	p1, ok := cat.LookupTable(parser.ObjectName{Name: "vchk_ptbl_1"})
	if !ok {
		t.Fatal("vchk_ptbl_1 not found")
	}
	p2, ok := cat.LookupTable(parser.ObjectName{Name: "vchk_ptbl_2"})
	if !ok {
		t.Fatal("vchk_ptbl_2 not found")
	}
	// AlterTableAddCheck's PartitionChildren propagation loop
	// (operators_ddl.go:8765, AddCheckInherited) always stamps the
	// propagated copy convalidated='t' regardless of the parent's own
	// NOT VALID — a distinct, already-ledgered gap (the ADD CHECK
	// propagation code path, not this brief's VALIDATE CONSTRAINT cascade).
	// Set the precondition directly so this test isolates
	// cascadeCheckValidateToDescendants itself rather than depending on a
	// fix to that separate bug.
	p1.NamedChecks[findNamedCheck(t, p1, "b_le_20")].NotValid = true
	p2.NamedChecks[findNamedCheck(t, p2, "b_le_20")].NotValid = true

	if err := runDDL(t, ctx, `ALTER TABLE vchk_ptbl VALIDATE CONSTRAINT b_le_20`); err != nil {
		t.Fatalf("ALTER TABLE vchk_ptbl VALIDATE CONSTRAINT b_le_20: %v", err)
	}

	p1, ok = cat.LookupTable(parser.ObjectName{Name: "vchk_ptbl_1"})
	if !ok {
		t.Fatal("vchk_ptbl_1 not found after VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, p1, "b_le_20"); p1.NamedChecks[idx].NotValid {
		t.Errorf("vchk_ptbl_1's b_le_20.NotValid = true after parent VALIDATE CONSTRAINT, want false (partition cascade)")
	}

	p2, ok = cat.LookupTable(parser.ObjectName{Name: "vchk_ptbl_2"})
	if !ok {
		t.Fatal("vchk_ptbl_2 not found after VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, p2, "b_le_20"); p2.NamedChecks[idx].NotValid {
		t.Errorf("vchk_ptbl_2's b_le_20.NotValid = true after parent VALIDATE CONSTRAINT, want false (partition cascade)")
	}
}

// TestAlterTableValidateCheckNoInheritDoesNotCascadeFlag pins the first half
// of the second acceptance goal: a NO INHERIT CHECK on a parent that also
// has an inheritance child carrying a same-named CHECK (the child's own,
// independently-added copy — NO INHERIT constraints are never propagated to
// children in the first place, so the child's copy must be added directly).
// Validating the parent must flip ONLY the parent; the child's NotValid
// stays untouched (QueueCheckConstraintValidation's `!con->connoinherit`
// gate, tablecmds.c:13140 — a NO INHERIT constraint is never looked for in
// children at all).
func TestAlterTableValidateCheckNoInheritDoesNotCascadeFlag(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE nichk_parent (a int)`,
		`CREATE TABLE nichk_child () INHERITS (nichk_parent)`,
		`ALTER TABLE nichk_parent ADD CONSTRAINT a_is_2 CHECK (a = 2) NO INHERIT NOT VALID`,
		`ALTER TABLE nichk_child ADD CONSTRAINT a_is_2 CHECK (a = 2) NOT VALID`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "nichk_child"})
	if !ok {
		t.Fatal("nichk_child not found")
	}
	if idx := findNamedCheck(t, child, "a_is_2"); !child.NamedChecks[idx].NotValid {
		t.Fatalf("precondition: nichk_child's a_is_2 already valid before VALIDATE CONSTRAINT")
	}

	if err := runDDL(t, ctx, `ALTER TABLE nichk_parent VALIDATE CONSTRAINT a_is_2`); err != nil {
		t.Fatalf("ALTER TABLE nichk_parent VALIDATE CONSTRAINT a_is_2: %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "nichk_parent"})
	if !ok {
		t.Fatal("nichk_parent not found after VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, parent, "a_is_2"); parent.NamedChecks[idx].NotValid {
		t.Errorf("nichk_parent's a_is_2.NotValid = true after VALIDATE CONSTRAINT, want false")
	}

	child, ok = cat.LookupTable(parser.ObjectName{Name: "nichk_child"})
	if !ok {
		t.Fatal("nichk_child not found after VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, child, "a_is_2"); !child.NamedChecks[idx].NotValid {
		t.Errorf("nichk_child's a_is_2.NotValid = false after parent's NO INHERIT VALIDATE CONSTRAINT, want true (no cascade)")
	}
}

// TestAlterTableValidateCheckNoInheritScanDoesNotDescend covers the same
// scenario as alter_table.sql:412-425 (parent_noinh_convalid/
// child_noinh_convalid) — a NO INHERIT CHECK's existing-row scan must be
// confined to the named relation — but as two independent fixtures rather
// than one shared table exercised via `DELETE FROM ONLY`: goopg's `DELETE
// FROM ONLY` does not honor the ONLY qualifier (it deletes descendant rows
// too, confirmed via a throwaway probe against forEachLiveRow — a separate,
// pre-existing DML-scoping gap unrelated to this brief's CHECK-scan gating;
// worth a deferral row, see report.md), so reusing alter_table.sql's exact
// shared-table-plus-DELETE-FROM-ONLY sequence would silently make BOTH
// halves pass regardless of whether the scan itself respects NoInherit.
// Splitting into "parent's own row violates -> fails" and "only the
// child's row violates -> succeeds" pins the identical scan-scoping
// behavior without depending on ONLY-DELETE.
func TestAlterTableValidateCheckNoInheritScanDoesNotDescend(t *testing.T) {
	t.Run("parent's own row violates", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()
		for _, stmt := range []string{
			`CREATE TABLE noinh_fail_parent (a int)`,
			`CREATE TABLE noinh_fail_child () INHERITS (noinh_fail_parent)`,
			`INSERT INTO noinh_fail_parent VALUES (1)`,
			`ALTER TABLE noinh_fail_parent ADD CONSTRAINT check_a_is_2 CHECK (a = 2) NO INHERIT NOT VALID`,
		} {
			if err := runDDL(t, ctx, stmt); err != nil {
				t.Fatalf("setup %q: %v", stmt, err)
			}
		}
		err := runDDL(t, ctx, `ALTER TABLE noinh_fail_parent VALIDATE CONSTRAINT check_a_is_2`)
		requireExecError(t, err, "23514", `check constraint "check_a_is_2" of relation "noinh_fail_parent" is violated by some row`)
		_ = cat
	})

	t.Run("only the child's row violates", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()
		for _, stmt := range []string{
			`CREATE TABLE noinh_ok_parent (a int)`,
			`CREATE TABLE noinh_ok_child () INHERITS (noinh_ok_parent)`,
			`INSERT INTO noinh_ok_parent VALUES (2)`,
			`INSERT INTO noinh_ok_child VALUES (1)`,
			`ALTER TABLE noinh_ok_parent ADD CONSTRAINT check_a_is_2 CHECK (a = 2) NO INHERIT NOT VALID`,
		} {
			if err := runDDL(t, ctx, stmt); err != nil {
				t.Fatalf("setup %q: %v", stmt, err)
			}
		}
		// The child row (a=1) violates the constraint, but the NO INHERIT
		// scan must not descend into it.
		if err := runDDL(t, ctx, `ALTER TABLE noinh_ok_parent VALIDATE CONSTRAINT check_a_is_2`); err != nil {
			t.Fatalf("ALTER TABLE noinh_ok_parent VALIDATE CONSTRAINT check_a_is_2 (only child row violates): %v", err)
		}
		parent, ok := cat.LookupTable(parser.ObjectName{Name: "noinh_ok_parent"})
		if !ok {
			t.Fatal("noinh_ok_parent not found")
		}
		if idx := findNamedCheck(t, parent, "check_a_is_2"); parent.NamedChecks[idx].NotValid {
			t.Errorf("noinh_ok_parent's check_a_is_2.NotValid = true after VALIDATE CONSTRAINT succeeded, want false")
		}
	})
}

// TestAlterTableValidateCheckInheritableScanStillDescends ports
// alter_table.sql:399-406 (attmp3/attmp6/attmp7): an inheritable (non-NO
// INHERIT) CHECK's row scan must still cover descendant storage. A
// violating row lives in a child only; a parent-level VALIDATE CONSTRAINT
// fails until it is deleted. This is a no-regression pin for the
// M0134-0005ac whole-tree scan (forEachLiveRow) — it must PASS both before
// and after this brief's edit, since the row-scan-over-the-tree half
// already existed; only the catalog-flag cascade is new. Confirmed in
// report.md.
func TestAlterTableValidateCheckInheritableScanStillDescends(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE inh_attmp3 (a int, b int)`,
		`CREATE TABLE inh_attmp6 () INHERITS (inh_attmp3)`,
		`CREATE TABLE inh_attmp7 () INHERITS (inh_attmp3)`,
		`INSERT INTO inh_attmp6 VALUES (6, 30), (7, 16)`,
		`ALTER TABLE inh_attmp3 ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE inh_attmp3 VALIDATE CONSTRAINT b_le_20`)
	requireExecError(t, err, "23514", `check constraint "b_le_20"`)

	if err := runDDL(t, ctx, `DELETE FROM inh_attmp6 WHERE b > 20`); err != nil {
		t.Fatalf("DELETE FROM inh_attmp6 WHERE b > 20: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE inh_attmp3 VALIDATE CONSTRAINT b_le_20`); err != nil {
		t.Fatalf("ALTER TABLE inh_attmp3 VALIDATE CONSTRAINT b_le_20 (post-delete): %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "inh_attmp3"})
	if !ok {
		t.Fatal("inh_attmp3 not found")
	}
	if idx := findNamedCheck(t, parent, "b_le_20"); parent.NamedChecks[idx].NotValid {
		t.Errorf("inh_attmp3's b_le_20.NotValid = true after VALIDATE CONSTRAINT succeeded, want false")
	}
}

// TestAlterTableValidateCheckAlreadyValidDescendantNoDrift pins the
// already-validated-descendant no-op (acceptance criterion 6): a
// descendant whose CHECK is already valid keeps its IsLocal/InhCount
// unchanged, and a still-invalid grandchild BEHIND it is still flipped —
// PG's CHECK path has no already-convalidated skip (confirmed absent in
// QueueCheckConstraintValidation, unlike the NOT NULL twin's
// :13277-13278), so the cascade must recurse unconditionally past an
// already-valid intermediate node.
func TestAlterTableValidateCheckAlreadyValidDescendantNoDrift(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// CHECK added BEFORE children exist — see the comment on
	// TestAlterTableValidateCheckCascadesToDescendants for why.
	for _, stmt := range []string{
		`CREATE TABLE nd_parent (a int)`,
		`ALTER TABLE nd_parent ADD CONSTRAINT a_is_2 CHECK (a = 2) NOT VALID`,
		`CREATE TABLE nd_child (a int) INHERITS (nd_parent)`,
		`CREATE TABLE nd_grand () INHERITS (nd_parent, nd_child)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Pre-validate the child's own copy directly, so it enters the cascade
	// already convalidated=t, and record its InhCount/IsLocal for the
	// no-drift assertion.
	if err := runDDL(t, ctx, `ALTER TABLE nd_child VALIDATE CONSTRAINT a_is_2`); err != nil {
		t.Fatalf("ALTER TABLE nd_child VALIDATE CONSTRAINT a_is_2 (pre-validate): %v", err)
	}
	child, ok := cat.LookupTable(parser.ObjectName{Name: "nd_child"})
	if !ok {
		t.Fatal("nd_child not found")
	}
	idxC := findNamedCheck(t, child, "a_is_2")
	if child.NamedChecks[idxC].NotValid {
		t.Fatalf("precondition: nd_child's a_is_2 still NotValid after direct VALIDATE CONSTRAINT")
	}
	wantInhCount := child.NamedChecks[idxC].InhCount
	wantIsLocal := child.NamedChecks[idxC].IsLocal

	grand, ok := cat.LookupTable(parser.ObjectName{Name: "nd_grand"})
	if !ok {
		t.Fatal("nd_grand not found")
	}
	// Same AddCheckInherited stamping gap as
	// TestAlterTableValidateCheckCascadesToDescendants — set the
	// precondition directly.
	grand.NamedChecks[findNamedCheck(t, grand, "a_is_2")].NotValid = true
	if idx := findNamedCheck(t, grand, "a_is_2"); !grand.NamedChecks[idx].NotValid {
		t.Fatalf("precondition: nd_grand's a_is_2 already valid before parent VALIDATE CONSTRAINT")
	}

	if err := runDDL(t, ctx, `ALTER TABLE nd_parent VALIDATE CONSTRAINT a_is_2`); err != nil {
		t.Fatalf("ALTER TABLE nd_parent VALIDATE CONSTRAINT a_is_2: %v", err)
	}

	child, ok = cat.LookupTable(parser.ObjectName{Name: "nd_child"})
	if !ok {
		t.Fatal("nd_child not found after VALIDATE CONSTRAINT")
	}
	idxC = findNamedCheck(t, child, "a_is_2")
	if child.NamedChecks[idxC].InhCount != wantInhCount {
		t.Errorf("nd_child's a_is_2.InhCount drifted from %d to %d for an already-convalidated descendant",
			wantInhCount, child.NamedChecks[idxC].InhCount)
	}
	if child.NamedChecks[idxC].IsLocal != wantIsLocal {
		t.Errorf("nd_child's a_is_2.IsLocal drifted from %v to %v for an already-convalidated descendant",
			wantIsLocal, child.NamedChecks[idxC].IsLocal)
	}

	grand, ok = cat.LookupTable(parser.ObjectName{Name: "nd_grand"})
	if !ok {
		t.Fatal("nd_grand not found after VALIDATE CONSTRAINT")
	}
	if idx := findNamedCheck(t, grand, "a_is_2"); grand.NamedChecks[idx].NotValid {
		t.Errorf("nd_grand's a_is_2.NotValid = true after parent VALIDATE CONSTRAINT, want false (cascade must recurse past an already-valid intermediate node)")
	}
}

// TestAlterTableAddCheckNoInheritScanDoesNotDescend is the ADD CHECK twin of
// TestAlterTableValidateCheckNoInheritScanDoesNotDescend, pinning the same
// scope gate on validateCheckConstraintRows's other caller
// (AlterTableAddCheck, operators_ddl.go:8738). `ADD CONSTRAINT ... CHECK
// (...) NO INHERIT` must succeed when only a child row violates (scan does
// not descend), and must still fail 23514 when the parent's own row
// violates.
func TestAlterTableAddCheckNoInheritScanDoesNotDescend(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// b=3 on the parent's own row will violate a later `b_is_2` check;
	// a=1 on the child's row will violate a later `a_is_2` check. Both rows
	// are inserted BEFORE either constraint exists — a CHECK is enforced on
	// subsequent DML once added, so setting up "the parent's own row already
	// violates" has to precede the ADD CHECK statement that's supposed to
	// catch it (mirrors PG's own ATAddCheckNNConstraint ordering: the scan
	// only ever looks at rows that predate the constraint).
	for _, stmt := range []string{
		`CREATE TABLE aichk_parent (a int, b int)`,
		`CREATE TABLE aichk_child (a int, b int) INHERITS (aichk_parent)`,
		`INSERT INTO aichk_parent VALUES (2, 3)`,
		`INSERT INTO aichk_child VALUES (1, 2)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Only the child row (a=1) violates a=2 — the parent's own row (a=2)
	// satisfies it. A NO INHERIT scan confined to the parent must succeed.
	if err := runDDL(t, ctx, `ALTER TABLE aichk_parent ADD CONSTRAINT a_is_2 CHECK (a = 2) NO INHERIT`); err != nil {
		t.Fatalf("ALTER TABLE aichk_parent ADD CONSTRAINT a_is_2 CHECK (a = 2) NO INHERIT (child-only violation): %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "aichk_parent"})
	if !ok {
		t.Fatal("aichk_parent not found")
	}
	idx := findNamedCheck(t, parent, "a_is_2")
	if parent.NamedChecks[idx].NotValid {
		t.Errorf("aichk_parent's a_is_2.NotValid = true after successful ADD CHECK, want false")
	}

	// b=3 on the parent's own row violates b=2 — the scan must still catch
	// it, even though the parent's own scan is what's under test (not a
	// descendant).
	err := runDDL(t, ctx, `ALTER TABLE aichk_parent ADD CONSTRAINT b_is_2 CHECK (b = 2) NO INHERIT`)
	requireExecError(t, err, "23514", `check constraint "b_is_2" of relation "aichk_parent" is violated by some row`)
}
