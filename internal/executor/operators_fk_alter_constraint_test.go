package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFKAlterConstraint covers DU-002 slice 433: `ALTER TABLE t ALTER
// CONSTRAINT name ConstraintAttributeSpec` (PG18) — re-declaring an EXISTING
// foreign key constraint's deferrability and/or enforceability, distinct
// from the `ADD CONSTRAINT ... NOT ENFORCED` forms slices 431/432 covered
// (which declare a NEW constraint's state at creation time). Verifies
// catalog.ForeignKey mutation, pg_constraint conenforced/convalidated,
// pg_get_constraintdef rendering, the runtime skip toggling live, and the
// non-FK/undefined-constraint error paths, all verified against real
// PostgreSQL 18.3's own behavior and error messages.
func TestFKAlterConstraint(t *testing.T) {
	t.Run("NotEnforcedThenEnforced", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acfk_parent (id integer PRIMARY KEY)`); err != nil {
			t.Fatalf("CREATE TABLE acfk_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE acfk_child (id integer, pid integer)`); err != nil {
			t.Fatalf("CREATE TABLE acfk_child: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE acfk_child ADD CONSTRAINT acfk_fk FOREIGN KEY (pid) REFERENCES acfk_parent(id)`); err != nil {
			t.Fatalf("ADD CONSTRAINT: %v", err)
		}

		// Control: dangling reference rejected while the FK is still enforced.
		err := runDDL(t, ctx, `INSERT INTO acfk_child VALUES (1, 999)`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "23503" {
			t.Fatalf("expected 23503 on the enforced control, got: %v", err)
		}

		if err := runDDL(t, ctx, `ALTER TABLE acfk_child ALTER CONSTRAINT acfk_fk NOT ENFORCED`); err != nil {
			t.Fatalf("ALTER CONSTRAINT ... NOT ENFORCED: %v", err)
		}

		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "acfk_child"})
		if !ok {
			t.Fatal("acfk_child table not found")
		}
		if len(tbl.ForeignKeys) != 1 || !tbl.ForeignKeys[0].NotEnforced {
			t.Fatalf("expected NotEnforced=true after ALTER CONSTRAINT, got %+v", tbl.ForeignKeys)
		}

		pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
		if !ok || pgcon.VirtualRows == nil {
			t.Fatal("pg_constraint virtual table not found")
		}
		var row []string
		for _, r := range pgcon.VirtualRows() {
			if r[3] == "f" && r[1] == "acfk_fk" {
				row = r
			}
		}
		if row == nil {
			t.Fatal("no pg_constraint row for acfk_fk")
		}
		if row[6] != "f" {
			t.Errorf("convalidated = %q, want f after NOT ENFORCED", row[6])
		}
		if row[25] != "f" {
			t.Errorf("conenforced = %q, want f", row[25])
		}

		rows := runQuery(t, ctx, `SELECT pg_get_constraintdef(`+row[0]+`::oid)`)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row from pg_get_constraintdef, got %d", len(rows))
		}
		def := rows[0][0].StringValue()
		if !strings.Contains(def, "NOT ENFORCED") {
			t.Errorf("pg_get_constraintdef = %q, want it to contain NOT ENFORCED", def)
		}

		// Runtime skip now applies: a dangling reference is allowed.
		if err := runDDL(t, ctx, `INSERT INTO acfk_child VALUES (2, 999)`); err != nil {
			t.Fatalf("INSERT with dangling FK reference under NOT ENFORCED should succeed, got: %v", err)
		}

		// Flip back to ENFORCED: pg_get_constraintdef must drop the trailer
		// entirely (real PG also re-validates convalidated='t' — this
		// simplification mirrors VALIDATE CONSTRAINT's own flag-only flip).
		if err := runDDL(t, ctx, `ALTER TABLE acfk_child ALTER CONSTRAINT acfk_fk ENFORCED`); err != nil {
			t.Fatalf("ALTER CONSTRAINT ... ENFORCED: %v", err)
		}
		if tbl.ForeignKeys[0].NotEnforced || tbl.ForeignKeys[0].NotValid {
			t.Fatalf("expected NotEnforced=false NotValid=false after re-ENFORCED, got %+v", tbl.ForeignKeys[0])
		}
		rows = runQuery(t, ctx, `SELECT pg_get_constraintdef(`+row[0]+`::oid)`)
		def = rows[0][0].StringValue()
		if strings.Contains(def, "ENFORCED") || strings.Contains(def, "NOT VALID") {
			t.Errorf("pg_get_constraintdef = %q, want no NOT ENFORCED/NOT VALID trailer once re-enforced", def)
		}
	})

	t.Run("DeferrableInitiallyDeferred", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acfkdef_parent (id integer PRIMARY KEY)`); err != nil {
			t.Fatalf("CREATE TABLE acfkdef_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE acfkdef_child (id integer, pid integer)`); err != nil {
			t.Fatalf("CREATE TABLE acfkdef_child: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE acfkdef_child ADD CONSTRAINT acfkdef_fk FOREIGN KEY (pid) REFERENCES acfkdef_parent(id)`); err != nil {
			t.Fatalf("ADD CONSTRAINT: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE acfkdef_child ALTER CONSTRAINT acfkdef_fk DEFERRABLE INITIALLY DEFERRED`); err != nil {
			t.Fatalf("ALTER CONSTRAINT ... DEFERRABLE INITIALLY DEFERRED: %v", err)
		}

		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "acfkdef_child"})
		if !ok {
			t.Fatal("acfkdef_child table not found")
		}
		if !tbl.ForeignKeys[0].Deferrable || !tbl.ForeignKeys[0].InitiallyDeferred {
			t.Fatalf("expected Deferrable=true InitiallyDeferred=true, got %+v", tbl.ForeignKeys[0])
		}
		// Enforceability must be untouched by a DEFERRABLE-only clause.
		if tbl.ForeignKeys[0].NotEnforced {
			t.Errorf("expected NotEnforced unchanged (false), got true")
		}
	})

	t.Run("NonFKConstraintRejected", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acfkchk_t (a integer CHECK (a > 0))`); err != nil {
			t.Fatalf("CREATE TABLE acfkchk_t: %v", err)
		}
		err := runDDL(t, ctx, `ALTER TABLE acfkchk_t ALTER CONSTRAINT acfkchk_t_a_check NOT ENFORCED`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42809" {
			t.Fatalf("expected 42809 wrong_object_type on a CHECK constraint, got: %v", err)
		}
		if !strings.Contains(ee.Message, "cannot alter enforceability of constraint") {
			t.Errorf("message = %q, want it to match real PG's enforceability-specific wording", ee.Message)
		}
	})

	t.Run("UndefinedConstraintRejected", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE acfkund_t (a integer)`); err != nil {
			t.Fatalf("CREATE TABLE acfkund_t: %v", err)
		}
		err := runDDL(t, ctx, `ALTER TABLE acfkund_t ALTER CONSTRAINT nosuchconstraint NOT ENFORCED`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "42704" {
			t.Fatalf("expected 42704 undefined_object, got: %v", err)
		}
	})
}
