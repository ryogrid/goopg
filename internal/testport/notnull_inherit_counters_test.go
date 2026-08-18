package testport

// TestPort_NotNullDiamondConinhcount and its siblings guard M0134-0005p:
// the `coninhcount`/`conislocal`/`convalidated` counter ARITHMETIC on a
// pg_constraint contype='n' row, distinct from 0005i's mere presence/absence
// of the cascade. Four defects fixed here (report.md §2, brief §Scope):
//   - A: `ALTER TABLE ... SET NOT NULL` on a column that already carries a
//     not-null constraint must merge in place (conislocal / validate) and
//     NOT recurse to children a second time
//     (postgres/src/backend/commands/tablecmds.c:7913 ATExecSetNotNull,
//     existing-constraint branch :7950-8010).
//   - B1: a cascaded child constraint must inherit the SOURCE constraint's
//     NotValid instead of always convalidated=true
//     (postgres/src/backend/catalog/heap.c:2385 AddRelationNewConstraints).
//   - B2: a diamond-inherited descendant must get coninhcount incremented
//     once PER DISTINCT PARENT EDGE, not once total
//     (postgres/src/backend/catalog/pg_constraint.c:742
//     AdjustNotNullInheritance).
//   - C: a freshly created `PARTITION OF` child always cooks
//     convalidated=true regardless of the parent's own NOT VALID state.

import (
	"strings"
	"testing"

	"github.com/lib/pq"
)

// TestPort_NotNullDiamondConinhcount is acceptance criterion 1: a 3-level
// INHERITS diamond (`grand INHERITS (parent, child)`, `child INHERITS
// (parent)`) — SET NOT NULL on parent must leave grand's coninhcount at 2
// (one increment per distinct parent edge: parent->grand and child->grand),
// not 1.
func TestPort_NotNullDiamondConinhcount(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-diamond-coninhcount")

	for _, stmt := range []string{
		"CREATE TABLE dparent (a int, b int)",
		"CREATE TABLE dchild (a int, b int) INHERITS (dparent)",
		"CREATE TABLE dgrand () INHERITS (dparent, dchild)",
		"ALTER TABLE dparent ALTER COLUMN a SET NOT NULL",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE contype = 'n' AND conrelid = 'dgrand'::regclass"); got != "2" {
		t.Fatalf("dgrand's NOT NULL coninhcount = %q, want 2 (diamond: one increment per distinct parent edge)", got)
	}
}

// TestPort_NotNullSetNotNullOnExistingDoesNotDoubleCascade is acceptance
// criterion 2: when a child already has the constraint (from an earlier
// cascade) and the parent's SET NOT NULL runs again, the child's
// coninhcount must NOT be bumped a second time, and an already-local NOT
// VALID constraint flips to validated (convalidated=t) instead of being
// left alone.
func TestPort_NotNullSetNotNullOnExistingDoesNotDoubleCascade(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-setnn-no-doublecascade")

	for _, stmt := range []string{
		"CREATE TABLE ep (a int, b int)",
		"CREATE TABLE ec () INHERITS (ep)",
		"ALTER TABLE ep ALTER COLUMN a SET NOT NULL",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE contype = 'n' AND conrelid = 'ec'::regclass"); got != "1" {
		t.Fatalf("ec's NOT NULL coninhcount after first SET NOT NULL = %q, want 1", got)
	}

	// Re-running SET NOT NULL on the parent (idempotent, merges into the
	// already-local constraint) must not re-cascade to the child.
	if err := runSQLSimple(t, c, "ALTER TABLE ep ALTER COLUMN a SET NOT NULL"); err != nil {
		t.Fatalf("second SET NOT NULL on ep: %v", err)
	}
	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE contype = 'n' AND conrelid = 'ec'::regclass"); got != "1" {
		t.Fatalf("ec's NOT NULL coninhcount after SECOND parent SET NOT NULL = %q, want still 1 (must not double-cascade)", got)
	}

	// Now the local-NOT VALID -> validate branch: an already-local NOT VALID
	// constraint on ep itself flips convalidated true on a repeat SET NOT
	// NULL, without touching ec.
	for _, stmt := range []string{
		"CREATE TABLE nv (x int)",
		"ALTER TABLE nv ADD CONSTRAINT nv_x_nn NOT NULL x NOT VALID",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE conname = 'nv_x_nn' AND conrelid = 'nv'::regclass"); got != "false" {
		t.Fatalf("nv_x_nn convalidated before SET NOT NULL = %q, want false", got)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE nv ALTER COLUMN x SET NOT NULL"); err != nil {
		t.Fatalf("ALTER TABLE nv ALTER COLUMN x SET NOT NULL (validate existing local NOT VALID): %v", err)
	}
	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE conname = 'nv_x_nn' AND conrelid = 'nv'::regclass"); got != "true" {
		t.Fatalf("nv_x_nn convalidated after SET NOT NULL = %q, want true (validate-in-place branch)", got)
	}
}

// TestPort_NotNullAddConstraintNotValidCascadesNotValid is acceptance
// criterion 3: `ADD CONSTRAINT ... NOT NULL ... NOT VALID` on a parent with
// an existing child propagates convalidated=f to the CHILD's newly created
// row too (not hardcoded true).
func TestPort_NotNullAddConstraintNotValidCascadesNotValid(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-addconstraint-notvalid-cascade")

	for _, stmt := range []string{
		"CREATE TABLE vp (a int, b int)",
		"CREATE TABLE vc () INHERITS (vp)",
		"ALTER TABLE vp ADD CONSTRAINT vp_a_nn NOT NULL a NOT VALID",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE contype = 'n' AND conrelid = 'vc'::regclass"); got != "false" {
		t.Fatalf("vc's cascaded NOT NULL convalidated = %q, want false (NOT VALID must propagate to the child row)", got)
	}
}

// TestPort_NotNullPartitionOfAlwaysValidated is acceptance criterion 4: a
// freshly created `PARTITION OF` child always gets convalidated=t on the
// inherited-column not-null row, even when the parent's own constraint is
// NOT VALID (a new empty partition has no rows to violate it).
func TestPort_NotNullPartitionOfAlwaysValidated(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-partitionof-validated")

	for _, stmt := range []string{
		"CREATE TABLE pp (a int, b int) PARTITION BY LIST (a)",
		"ALTER TABLE pp ADD CONSTRAINT pp_b_nn NOT NULL b NOT VALID",
		"CREATE TABLE pp1 PARTITION OF pp FOR VALUES IN (1)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE contype = 'n' AND conrelid = 'pp1'::regclass AND conname = 'pp_b_nn'"); got != "true" {
		t.Fatalf("pp1's inherited NOT NULL (from parent's NOT VALID constraint) convalidated = %q, want true (a fresh empty partition is always cooked validated)", got)
	}
}

// TestPort_NotNullAttachPartitionAbsorbs guards M0134-0005q: ALTER TABLE …
// ATTACH PARTITION must absorb the child's own NOT NULL constraint into the
// parent's, mirroring PG's CreateInheritance → MergeConstraintsIntoExisting
// (tablecmds.c:17638-17817). Mirrors constraints.sql's notnull_tbl1_2 case
// (constraints.sql:924-925): the child declares its OWN NOT NULL constraint
// "nn2"; after ATTACH, PG flips conislocal to false and bumps coninhcount to
// 1 while the constraint KEEPS ITS OWN NAME. Reads through a real server
// (pg_constraint), so it is not blind to the heap-resync gap. Guard read
// through pg_constraint/pg_attribute over the wire — real-server evidence,
// not the in-memory catalog.
func TestPort_NotNullAttachPartitionAbsorbs(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-attach-absorb")

	for _, stmt := range []string{
		"CREATE TABLE notnull_tbl1 (a int, b int) PARTITION BY LIST (a)",
		"ALTER TABLE notnull_tbl1 ADD CONSTRAINT notnull_con NOT NULL a NOT VALID",
		"CREATE TABLE notnull_tbl1_2(a int, CONSTRAINT nn2 NOT NULL a, b int)",
		"ALTER TABLE notnull_tbl1 ATTACH PARTITION notnull_tbl1_2 FOR VALUES IN (3,4)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT conislocal FROM pg_constraint WHERE conname = 'nn2' AND conrelid = 'notnull_tbl1_2'::regclass"); got != "false" {
		t.Fatalf("notnull_tbl1_2's nn2 conislocal after ATTACH = %q, want false (absorbed)", got)
	}
	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE conname = 'nn2' AND conrelid = 'notnull_tbl1_2'::regclass"); got != "1" {
		t.Fatalf("notnull_tbl1_2's nn2 coninhcount after ATTACH = %q, want 1", got)
	}
}

// TestPort_NotNullAttachPartitionNotValidConflict guards M0134-0005q's
// missing ATTACH-time compatibility ERROR: mirrors constraints.sql's pp_nn
// fixture (constraints.sql:943-947) — the parent's NOT NULL constraint is
// VALID, the child's matching constraint is NOT VALID; PG raises 42P17
// "constraint \"nn1\" conflicts with NOT VALID constraint on child table
// \"pp_nn_1\"" from ATExecAttachPartition itself. goopg previously let the
// ATTACH silently succeed.
func TestPort_NotNullAttachPartitionNotValidConflict(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-attach-notvalid-conflict")

	for _, stmt := range []string{
		"CREATE TABLE pp_nn (a int, b int, NOT NULL a) PARTITION BY LIST (a)",
		"CREATE TABLE pp_nn_1(a int, b int)",
		"ALTER TABLE pp_nn_1 ADD CONSTRAINT nn1 NOT NULL a NOT VALID",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runSQLSimple(t, c, "ALTER TABLE pp_nn ATTACH PARTITION pp_nn_1 FOR VALUES IN (NULL,5)")
	if err == nil {
		t.Fatalf("ATTACH PARTITION with parent-valid/child-NOT-VALID nn1 succeeded, want 42P17")
	}
	if pe, ok := err.(*pq.Error); ok {
		if pe.Code != "42P17" {
			t.Fatalf("ATTACH error SQLSTATE = %q, want 42P17", pe.Code)
		}
		want := `constraint "nn1" conflicts with NOT VALID constraint on child table "pp_nn_1"`
		if pe.Message != want {
			t.Fatalf("ATTACH error message = %q, want %q", pe.Message, want)
		}
	} else if !strings.Contains(err.Error(), `constraint "nn1" conflicts with NOT VALID constraint on child table "pp_nn_1"`) {
		t.Fatalf("ATTACH error = %v, want the 42P17 NOT VALID conflict message", err)
	}

	// After VALIDATE CONSTRAINT, the same ATTACH must succeed (PG's own
	// follow-on in the fixture) — proves the guard isn't blocking ATTACH
	// unconditionally.
	if err := runSQLSimple(t, c, "ALTER TABLE pp_nn_1 VALIDATE CONSTRAINT nn1"); err != nil {
		t.Fatalf("VALIDATE CONSTRAINT nn1: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE pp_nn ATTACH PARTITION pp_nn_1 FOR VALUES IN (NULL,5)"); err != nil {
		t.Fatalf("ATTACH PARTITION after VALIDATE CONSTRAINT: %v", err)
	}
}

// TestPort_NotNullAttachPartitionMissingChildConstraint guards the 42804
// branch of M0134-0005q: when the child has NO matching NOT NULL constraint
// on the parent's not-inherited column at all, ATTACH must fail with 42804
// "column \"%s\" in child table \"%s\" must be marked NOT NULL"
// (tablecmds.c:17797-17812).
func TestPort_NotNullAttachPartitionMissingChildConstraint(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-attach-missing-child-nn")

	for _, stmt := range []string{
		"CREATE TABLE mc_p (a int, b int, NOT NULL a) PARTITION BY LIST (a)",
		"CREATE TABLE mc_c (a int, b int)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runSQLSimple(t, c, "ALTER TABLE mc_p ATTACH PARTITION mc_c FOR VALUES IN (3,4)")
	if err == nil {
		t.Fatalf("ATTACH PARTITION with no matching child NOT NULL succeeded, want 42804")
	}
	if pe, ok := err.(*pq.Error); ok {
		if pe.Code != "42804" {
			t.Fatalf("ATTACH error SQLSTATE = %q, want 42804", pe.Code)
		}
		want := `column "a" in child table "mc_c" must be marked NOT NULL`
		if pe.Message != want {
			t.Fatalf("ATTACH error message = %q, want %q", pe.Message, want)
		}
	} else if !strings.Contains(err.Error(), `column "a" in child table "mc_c" must be marked NOT NULL`) {
		t.Fatalf("ATTACH error = %v, want the 42804 missing-child-NOT-NULL message", err)
	}
}

// TestPort_NotNullDetachPartitionUnabsorbs is the DETACH twin (Rule #2) of
// TestPort_NotNullAttachPartitionAbsorbs: mirrors PG's RemoveInheritance
// (tablecmds.c:17950-18144) — after DETACH, the child's NOT NULL constraint
// must go back to conislocal=true, coninhcount=0.
func TestPort_NotNullDetachPartitionUnabsorbs(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-detach-unabsorb")

	for _, stmt := range []string{
		"CREATE TABLE dt_p (a int, b int) PARTITION BY LIST (a)",
		"ALTER TABLE dt_p ADD CONSTRAINT dt_con NOT NULL a NOT VALID",
		"CREATE TABLE dt_c(a int, CONSTRAINT dt_nn NOT NULL a, b int)",
		"ALTER TABLE dt_p ATTACH PARTITION dt_c FOR VALUES IN (3,4)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	if got := queryScalar(t, c,
		"SELECT conislocal FROM pg_constraint WHERE conname = 'dt_nn' AND conrelid = 'dt_c'::regclass"); got != "false" {
		t.Fatalf("dt_c's dt_nn conislocal after ATTACH = %q, want false (precondition)", got)
	}

	if err := runSQLSimple(t, c, "ALTER TABLE dt_p DETACH PARTITION dt_c"); err != nil {
		t.Fatalf("DETACH PARTITION dt_c: %v", err)
	}

	if got := queryScalar(t, c,
		"SELECT conislocal FROM pg_constraint WHERE conname = 'dt_nn' AND conrelid = 'dt_c'::regclass"); got != "true" {
		t.Fatalf("dt_c's dt_nn conislocal after DETACH = %q, want true (un-absorbed)", got)
	}
	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE conname = 'dt_nn' AND conrelid = 'dt_c'::regclass"); got != "0" {
		t.Fatalf("dt_c's dt_nn coninhcount after DETACH = %q, want 0 (un-absorbed)", got)
	}
}
