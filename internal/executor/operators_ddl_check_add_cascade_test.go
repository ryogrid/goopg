package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0134-0005as: AlterTableAddCheck's cascade loop only walked
// im3.PartitionChildren(tbl.OID) — ONE level of partition children — never
// plain-INHERITS children and never grandchildren of either kind. PG's
// ATAddCheckNNConstraint (postgres/src/backend/commands/tablecmds.c:9911-
// 10049) unifies plain inheritance and partitions via find_inheritance_children
// (pg_inherits covers both) and recurses DFS into every child
// (:10028-10046). This file pins the fix: cascadeCheckToChildren /
// cascadeCheckToChildrenAt (operators_ddl.go), the ADD-direction CHECK twin
// of cascadeNotNullToChildren.

// TestCheckAddCascadeThreeLevelPlainInherits ports acceptance criterion 1: a
// CHECK added via ALTER TABLE on a 3-level plain-INHERITS chain (parent ->
// child -> grandchild, all pre-existing) must propagate to BOTH descendants,
// each with IsLocal=false, InhCount=1, and the literal NotValid/NotEnforced
// flags. FAIL-pre: before this brief's fix, only PartitionChildren was
// walked, so a plain-INHERITS child never received the constraint at all.
func TestCheckAddCascadeThreeLevelPlainInherits(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cac3_parent (a int, b int)`,
		`CREATE TABLE cac3_child (a int, b int) INHERITS (cac3_parent)`,
		`CREATE TABLE cac3_grand () INHERITS (cac3_child)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `ALTER TABLE cac3_parent ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`); err != nil {
		t.Fatalf("ALTER TABLE cac3_parent ADD CONSTRAINT b_le_20: %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "cac3_child"})
	if !ok {
		t.Fatal("cac3_child not found")
	}
	idx := findNamedCheck(t, child, "b_le_20")
	if child.NamedChecks[idx].IsLocal {
		t.Errorf("cac3_child's b_le_20.IsLocal = true, want false")
	}
	if child.NamedChecks[idx].InhCount != 1 {
		t.Errorf("cac3_child's b_le_20.InhCount = %d, want 1", child.NamedChecks[idx].InhCount)
	}
	if !child.NamedChecks[idx].NotValid {
		t.Errorf("cac3_child's b_le_20.NotValid = false, want true (literal ALTER-time flag)")
	}

	grand, ok := cat.LookupTable(parser.ObjectName{Name: "cac3_grand"})
	if !ok {
		t.Fatal("cac3_grand not found")
	}
	idxG := findNamedCheck(t, grand, "b_le_20")
	if grand.NamedChecks[idxG].IsLocal {
		t.Errorf("cac3_grand's b_le_20.IsLocal = true, want false")
	}
	if grand.NamedChecks[idxG].InhCount != 1 {
		t.Errorf("cac3_grand's b_le_20.InhCount = %d, want 1", grand.NamedChecks[idxG].InhCount)
	}
	if !grand.NamedChecks[idxG].NotValid {
		t.Errorf("cac3_grand's b_le_20.NotValid = false, want true (literal ALTER-time flag, transitively)")
	}
}

// TestCheckAddCascadeMixedTree covers acceptance criterion 2: a mixed tree —
// a partitioned parent whose partition is itself the target of a plain
// INHERITS child, and (separately) a plain-INHERITS child that is itself
// partitioned. Every descendant, intermediate nodes included, must receive
// the constraint.
func TestCheckAddCascadeMixedTree(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		// partitioned parent -> partition -> INHERITS child of the partition.
		`CREATE TABLE cacm_ptop (a int, b int) PARTITION BY LIST (a)`,
		`CREATE TABLE cacm_part1 PARTITION OF cacm_ptop FOR VALUES IN (1,2)`,
		`CREATE TABLE cacm_part1_child () INHERITS (cacm_part1)`,
		// INHERITS child that is itself partitioned. PG's parser rejects
		// PARTITION BY combined with INHERITS in ONE CREATE TABLE statement
		// (parse_utilcmd.c:260-265 "cannot create partitioned table as
		// inheritance child"), so build this with the same two-step
		// sequence PG itself requires: create the table partitioned and
		// standalone, then attach the plain-inheritance edge afterward via
		// ALTER TABLE ... INHERIT (a real, supported PG DDL path distinct
		// from the CREATE-time inhRelations clause the parser guards).
		`CREATE TABLE cacm_itop (a int, b int)`,
		`CREATE TABLE cacm_isub (a int, b int) PARTITION BY LIST (a)`,
		`ALTER TABLE cacm_isub INHERIT cacm_itop`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	// cacm_isub is partitioned so it needs its own partition to hold rows,
	// but it is a node in the walk regardless.
	if err := runDDL(t, ctx, `CREATE TABLE cacm_isub_p PARTITION OF cacm_isub FOR VALUES IN (1,2)`); err != nil {
		t.Fatalf("setup cacm_isub_p: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE cacm_ptop ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`); err != nil {
		t.Fatalf("ALTER TABLE cacm_ptop ADD CONSTRAINT b_le_20: %v", err)
	}
	for _, name := range []string{"cacm_part1", "cacm_part1_child"} {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("%s not found", name)
		}
		idx := findNamedCheck(t, tbl, "b_le_20")
		if tbl.NamedChecks[idx].IsLocal {
			t.Errorf("%s's b_le_20.IsLocal = true, want false", name)
		}
		if tbl.NamedChecks[idx].InhCount != 1 {
			t.Errorf("%s's b_le_20.InhCount = %d, want 1", name, tbl.NamedChecks[idx].InhCount)
		}
	}

	if err := runDDL(t, ctx, `ALTER TABLE cacm_itop ADD CONSTRAINT b_le_30 CHECK (b <= 30) NOT VALID`); err != nil {
		t.Fatalf("ALTER TABLE cacm_itop ADD CONSTRAINT b_le_30: %v", err)
	}
	for _, name := range []string{"cacm_isub", "cacm_isub_p"} {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("%s not found", name)
		}
		idx := findNamedCheck(t, tbl, "b_le_30")
		if tbl.NamedChecks[idx].IsLocal {
			t.Errorf("%s's b_le_30.IsLocal = true, want false", name)
		}
		if tbl.NamedChecks[idx].InhCount != 1 {
			t.Errorf("%s's b_le_30.InhCount = %d, want 1", name, tbl.NamedChecks[idx].InhCount)
		}
	}
}

// TestCheckAddCascadeNoInheritDoesNotPropagate covers acceptance criterion
// 3: `ADD CONSTRAINT ... CHECK (...) NO INHERIT` on a plain table WITH
// INHERITS children must propagate to NOTHING — the guard mirrors
// tablecmds.c:10004's early return BEFORE enumerating any children. This is
// a no-regression pin (today's inline code is accidentally correct since
// PartitionChildren is empty for a plain-INHERITS tree), not a FAIL-pre
// proof, but it protects the new walk against ever touching a NO INHERIT
// constraint's children.
func TestCheckAddCascadeNoInheritDoesNotPropagate(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cacni_parent (a int, b int)`,
		`CREATE TABLE cacni_child (a int, b int) INHERITS (cacni_parent)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `ALTER TABLE cacni_parent ADD CONSTRAINT b_le_20 CHECK (b <= 20) NO INHERIT`); err != nil {
		t.Fatalf("ALTER TABLE cacni_parent ADD CONSTRAINT b_le_20 NO INHERIT: %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "cacni_child"})
	if !ok {
		t.Fatal("cacni_child not found")
	}
	for _, nc := range child.NamedChecks {
		if nc.Name == "b_le_20" {
			t.Errorf("cacni_child unexpectedly received NO INHERIT constraint b_le_20")
		}
	}
}

// TestCheckAddCascadeAnonymousNameResolved covers acceptance criterion 4: an
// anonymous `ALTER TABLE p ADD CHECK (...)` must propagate a NON-EMPTY,
// resolved constraint name to children — PG fills newConstraint->conname
// once at the top of ATAddCheckNNConstraint and reuses that SAME node for
// every child. FAIL-pre: before this brief's fix, the inline cascade passed
// act.ConstraintName (empty for an anonymous ADD CHECK) straight through.
func TestCheckAddCascadeAnonymousNameResolved(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cacan_parent (a int, b int)`,
		`CREATE TABLE cacan_child (a int, b int) INHERITS (cacan_parent)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `ALTER TABLE cacan_parent ADD CHECK (b <= 20)`); err != nil {
		t.Fatalf("ALTER TABLE cacan_parent ADD CHECK (b <= 20): %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "cacan_child"})
	if !ok {
		t.Fatal("cacan_child not found")
	}
	if len(child.NamedChecks) != 1 {
		t.Fatalf("cacan_child.NamedChecks has %d entries, want 1", len(child.NamedChecks))
	}
	if child.NamedChecks[0].Name == "" {
		t.Errorf("cacan_child's anonymous CHECK cascaded with an EMPTY name, want the resolved auto-generated name")
	}
	if child.NamedChecks[0].Name != "cacan_parent_b_check" {
		t.Errorf("cacan_child's cascaded CHECK name = %q, want %q (PG's <table>_<col>_check auto-naming for a single-column CHECK, resolved on the PARENT)",
			child.NamedChecks[0].Name, "cacan_parent_b_check")
	}
}

// TestCheckAddCascadeDiamondTerminates covers acceptance criterion 5: a
// diamond-inherited tree (grand inherits from both parent and child, and
// child inherits from parent) must terminate — no infinite recursion, and
// the edge-keyed visited guard means the diamond node is visited once per
// EDGE, not deduped away entirely.
func TestCheckAddCascadeDiamondTerminates(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cacd_parent (a int, b int)`,
		`CREATE TABLE cacd_child (a int, b int) INHERITS (cacd_parent)`,
		`CREATE TABLE cacd_grand () INHERITS (cacd_parent, cacd_child)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// The depth-bounded, edge-keyed visited guard (maxNotNullCascadeDepth,
	// shared with cascadeCheckToChildrenAt) is what stops this from
	// recursing forever on the diamond edge cacd_parent->cacd_grand reached
	// via both cacd_parent directly and via cacd_child; a synchronous call
	// returning at all (rather than the test process hanging) is the
	// termination proof.
	if err := runDDL(t, ctx, `ALTER TABLE cacd_parent ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`); err != nil {
		t.Fatalf("ALTER TABLE cacd_parent ADD CONSTRAINT b_le_20 (diamond tree): %v", err)
	}

	grand, ok := cat.LookupTable(parser.ObjectName{Name: "cacd_grand"})
	if !ok {
		t.Fatal("cacd_grand not found")
	}
	idx := findNamedCheck(t, grand, "b_le_20")
	if grand.NamedChecks[idx].InhCount < 1 {
		t.Errorf("cacd_grand's b_le_20.InhCount = %d, want >= 1", grand.NamedChecks[idx].InhCount)
	}
}
