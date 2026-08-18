package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAlterConstraintInheritabilityToggle covers M0134-0005 S05 Part B:
// `ALTER TABLE t ALTER CONSTRAINT name [NO] INHERIT` toggles a NOT NULL
// constraint's connoinherit and propagates one level to inheritance children
// (tablecmds.c ATExecAlterConstrInheritability, PG18+), matching the
// constraints.sql "ALTER .. NO INHERIT works for invalid constraints" case
// (notnull_tbl1 / notnull_tbl1_chld, live-verified against PostgreSQL 18.3).
func TestAlterConstraintInheritabilityToggle(t *testing.T) {
	t.Run("RoundTripWithChildPropagation", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		// The NOT NULL constraint must exist on the parent BEFORE the child is
		// created: goopg's `CREATE TABLE ... INHERITS` copies the parent's
		// contype='n' constraint (with InhCount incremented) onto the child at
		// creation time (M0097-0023) — adding a NOT NULL constraint to a
		// parent that already has children does NOT retroactively cascade to
		// them (a separate, out-of-scope gap; see report.md).
		if err := runDDL(t, ctx, `CREATE TABLE acinh_tbl1 (a int)`); err != nil {
			t.Fatalf("CREATE TABLE acinh_tbl1: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE acinh_tbl1 ADD CONSTRAINT acinh_nn NOT NULL a NOT VALID`); err != nil {
			t.Fatalf("ADD CONSTRAINT ... NOT NULL a NOT VALID: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE acinh_tbl1_chld () INHERITS (acinh_tbl1)`); err != nil {
			t.Fatalf("CREATE TABLE acinh_tbl1_chld: %v", err)
		}

		parent, ok := cat.LookupTable(parser.ObjectName{Name: "acinh_tbl1"})
		if !ok {
			t.Fatal("acinh_tbl1 table not found")
		}
		child, ok := cat.LookupTable(parser.ObjectName{Name: "acinh_tbl1_chld"})
		if !ok {
			t.Fatal("acinh_tbl1_chld table not found")
		}
		// The child copy was created with InhCount=1 at CREATE TABLE ... INHERITS
		// time (M0097-0023's inheritance-copy path).
		var childBefore *int
		for i := range child.NotNullConstraints {
			if child.NotNullConstraints[i].ColName == "a" {
				v := child.NotNullConstraints[i].InhCount
				childBefore = &v
			}
		}
		if childBefore == nil || *childBefore != 1 {
			t.Fatalf("expected child acinh_tbl1_chld.a NOT NULL InhCount=1 before ALTER CONSTRAINT, got %v", childBefore)
		}

		// NO INHERIT: parent connoinherit flips true, child's inherited copy
		// loses one level of coninhcount and becomes locally-owned.
		if err := runDDL(t, ctx, `ALTER TABLE acinh_tbl1 ALTER CONSTRAINT acinh_nn NO INHERIT`); err != nil {
			t.Fatalf("ALTER CONSTRAINT ... NO INHERIT: %v", err)
		}
		var parentNC, childNC = -1, -1
		for i := range parent.NotNullConstraints {
			if parent.NotNullConstraints[i].Name == "acinh_nn" {
				parentNC = i
			}
		}
		for i := range child.NotNullConstraints {
			if child.NotNullConstraints[i].ColName == "a" {
				childNC = i
			}
		}
		if parentNC < 0 {
			t.Fatal("acinh_nn not found on parent after NO INHERIT")
		}
		if !parent.NotNullConstraints[parentNC].NoInherit {
			t.Errorf("expected parent NoInherit=true after NO INHERIT")
		}
		if childNC < 0 {
			t.Fatal("child not-null constraint on column a not found after NO INHERIT")
		}
		if child.NotNullConstraints[childNC].InhCount != 0 {
			t.Errorf("expected child InhCount=0 after NO INHERIT, got %d", child.NotNullConstraints[childNC].InhCount)
		}
		if !child.NotNullConstraints[childNC].IsLocal {
			t.Errorf("expected child IsLocal=true after NO INHERIT")
		}

		// INHERIT: flips back, child's coninhcount is restored.
		if err := runDDL(t, ctx, `ALTER TABLE acinh_tbl1 ALTER CONSTRAINT acinh_nn INHERIT`); err != nil {
			t.Fatalf("ALTER CONSTRAINT ... INHERIT: %v", err)
		}
		if parent.NotNullConstraints[parentNC].NoInherit {
			t.Errorf("expected parent NoInherit=false after INHERIT")
		}
		if child.NotNullConstraints[childNC].InhCount != 1 {
			t.Errorf("expected child InhCount=1 after re-INHERIT, got %d", child.NotNullConstraints[childNC].InhCount)
		}
	})

	t.Run("NoOpWhenAlreadyInRequestedState", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acinhnp_tbl1 (a int)`); err != nil {
			t.Fatalf("CREATE TABLE acinhnp_tbl1: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE acinhnp_tbl1 ADD CONSTRAINT acinhnp_nn NOT NULL a NOT VALID`); err != nil {
			t.Fatalf("ADD CONSTRAINT: %v", err)
		}
		// Already INHERIT (the default) — re-asserting INHERIT must be a silent
		// no-op (ATExecAlterConstrInheritability, tablecmds.c:12635-12636).
		if err := runDDL(t, ctx, `ALTER TABLE acinhnp_tbl1 ALTER CONSTRAINT acinhnp_nn INHERIT`); err != nil {
			t.Fatalf("ALTER CONSTRAINT ... INHERIT (already inherit): %v", err)
		}
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "acinhnp_tbl1"})
		if !ok {
			t.Fatal("acinhnp_tbl1 table not found")
		}
		if tbl.NotNullConstraints[0].NoInherit {
			t.Fatalf("expected NoInherit unchanged (false)")
		}
	})

	t.Run("NonNotNullConstraintRejected", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acinhchk_t (a integer CHECK (a > 0))`); err != nil {
			t.Fatalf("CREATE TABLE acinhchk_t: %v", err)
		}
		err := runDDL(t, ctx, `ALTER TABLE acinhchk_t ALTER CONSTRAINT acinhchk_t_a_check NO INHERIT`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42809" {
			t.Fatalf("expected 42809 on a CHECK constraint, got: %v", err)
		}
		if ee.Message != `constraint "acinhchk_t_a_check" of relation "acinhchk_t" is not a not-null constraint` {
			t.Errorf("message = %q, want PG's not-null-specific wording", ee.Message)
		}
	})

	t.Run("FKConstraintRejected", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acinhfk_parent (id integer PRIMARY KEY)`); err != nil {
			t.Fatalf("CREATE TABLE acinhfk_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE acinhfk_child (id integer, pid integer)`); err != nil {
			t.Fatalf("CREATE TABLE acinhfk_child: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE acinhfk_child ADD CONSTRAINT acinhfk_fk FOREIGN KEY (pid) REFERENCES acinhfk_parent(id)`); err != nil {
			t.Fatalf("ADD CONSTRAINT: %v", err)
		}
		err := runDDL(t, ctx, `ALTER TABLE acinhfk_child ALTER CONSTRAINT acinhfk_fk NO INHERIT`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42809" {
			t.Fatalf("expected 42809 on an FK constraint, got: %v", err)
		}
		if ee.Message != `constraint "acinhfk_fk" of relation "acinhfk_child" is not a not-null constraint` {
			t.Errorf("message = %q, want PG's not-null-specific wording", ee.Message)
		}
	})

	t.Run("UndefinedConstraintRejected", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acinhund_t (a integer)`); err != nil {
			t.Fatalf("CREATE TABLE acinhund_t: %v", err)
		}
		err := runDDL(t, ctx, `ALTER TABLE acinhund_t ALTER CONSTRAINT nosuchconstraint NO INHERIT`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42704" {
			t.Fatalf("expected 42704 undefined_object, got: %v", err)
		}
	})
}
