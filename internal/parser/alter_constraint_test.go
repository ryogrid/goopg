package parser

import "testing"

// TestParseAlterTableAlterConstraint covers DU-002 slice 433: `ALTER TABLE t
// ALTER CONSTRAINT name ConstraintAttributeSpec` (PG18) — re-declaring an
// EXISTING constraint's deferrability and/or enforceability, distinct from
// the `ADD CONSTRAINT` forms slices 431/432 covered. It must be distinguished
// from the pre-existing `ALTER COLUMN` grammar (both start with the bare
// ALTER keyword) by the CONSTRAINT keyword immediately following ALTER, and
// each attribute the statement doesn't mention must leave its corresponding
// Has* flag false.
func TestParseAlterTableAlterConstraint(t *testing.T) {
	t.Run("PlainNotEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ALTER CONSTRAINT fk NOT ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.Kind != AlterTableAlterConstraint {
			t.Fatalf("kind=%v want AlterTableAlterConstraint", act.Kind)
		}
		if act.ConstraintName != "fk" {
			t.Errorf("ConstraintName=%q want fk", act.ConstraintName)
		}
		if !act.AlterConstraintHasEnforceability {
			t.Fatalf("expected AlterConstraintHasEnforceability=true")
		}
		if act.AlterConstraintEnforced {
			t.Errorf("expected AlterConstraintEnforced=false for NOT ENFORCED")
		}
		if act.AlterConstraintHasDeferrability {
			t.Errorf("expected AlterConstraintHasDeferrability=false when only NOT ENFORCED is given")
		}
	})
	t.Run("BareEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ALTER CONSTRAINT fk ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.AlterConstraintHasEnforceability {
			t.Fatalf("expected AlterConstraintHasEnforceability=true")
		}
		if !act.AlterConstraintEnforced {
			t.Errorf("expected AlterConstraintEnforced=true for bare ENFORCED")
		}
	})
	t.Run("DeferrableInitiallyDeferred", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ALTER CONSTRAINT fk DEFERRABLE INITIALLY DEFERRED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.AlterConstraintHasDeferrability {
			t.Fatalf("expected AlterConstraintHasDeferrability=true")
		}
		if !act.AlterConstraintDeferrable || !act.AlterConstraintInitiallyDeferred {
			t.Errorf("expected Deferrable=true InitiallyDeferred=true, got Deferrable=%v InitiallyDeferred=%v",
				act.AlterConstraintDeferrable, act.AlterConstraintInitiallyDeferred)
		}
		if act.AlterConstraintHasEnforceability {
			t.Errorf("expected AlterConstraintHasEnforceability=false when only DEFERRABLE is given")
		}
	})
	t.Run("NotDeferrableEnforcedComposed", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ALTER CONSTRAINT fk NOT DEFERRABLE ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.AlterConstraintHasDeferrability || act.AlterConstraintDeferrable {
			t.Errorf("expected HasDeferrability=true Deferrable=false, got has=%v deferrable=%v",
				act.AlterConstraintHasDeferrability, act.AlterConstraintDeferrable)
		}
		if !act.AlterConstraintHasEnforceability || !act.AlterConstraintEnforced {
			t.Errorf("expected HasEnforceability=true Enforced=true, got has=%v enforced=%v",
				act.AlterConstraintHasEnforceability, act.AlterConstraintEnforced)
		}
	})
	t.Run("NotDistinguishedFromAlterColumnNamedConstraint", func(t *testing.T) {
		// A column named "constraint" would be unusual, but ALTER COLUMN's own
		// grammar requires the COLUMN keyword or a column identifier — this
		// case exists purely to confirm ALTER CONSTRAINT's dispatch doesn't
		// mis-fire on an ordinary `ALTER COLUMN col TYPE ...`.
		stmts, err := Parse("ALTER TABLE t ALTER COLUMN a TYPE bigint")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.Kind != AlterTableAlterColumnType {
			t.Fatalf("kind=%v want AlterTableAlterColumnType", act.Kind)
		}
	})
}
