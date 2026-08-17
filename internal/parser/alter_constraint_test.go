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

// TestAlterConstraintNotValidRejected covers M0134-0005 S05 Part A:
// `ALTER TABLE t ALTER CONSTRAINT c NOT VALID` must fail at parse time with
// PG's own grammar-level error (gram.y:2672-2676, the CAS_NOT_VALID special
// case inside the `ALTER CONSTRAINT name ConstraintAttributeSpec` production)
// — SQLSTATE 0A000 (feature_not_supported), not the generic
// "expected ';' or end of input" trailing-token fallthrough goopg produced
// before this fix.
func TestAlterConstraintNotValidRejected(t *testing.T) {
	_, err := Parse("ALTER TABLE t ALTER CONSTRAINT c NOT VALID")
	if err == nil {
		t.Fatal("expected a parse error for ALTER CONSTRAINT ... NOT VALID")
	}
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("expected *SyntaxError, got %T: %v", err, err)
	}
	if se.Code != "0A000" {
		t.Errorf("Code = %q, want 0A000", se.Code)
	}
	if se.Message != "constraints cannot be altered to be NOT VALID" {
		t.Errorf("Message = %q, want PG's exact wording", se.Message)
	}
}

// TestParseAlterConstraintInherit covers M0134-0005 S05 Part B parsing:
// `ALTER CONSTRAINT name INHERIT` (its own grammar production, gram.y:2686-
// 2699) and `ALTER CONSTRAINT name NO INHERIT` (part of ConstraintAttributeSpec,
// gram.y:6249 CAS_NO_INHERIT) must both parse and set
// AlterConstraintHasInheritability, with AlterConstraintNoInherit recording
// which spelling was used — the round-trip of the parsed fields back to the
// input spelling that acceptance criterion 3 calls for (goopg has no
// AlterTableAction.String() method to exercise; the fields ARE the AST here).
func TestParseAlterConstraintInherit(t *testing.T) {
	t.Run("BareInherit", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ALTER CONSTRAINT nn INHERIT")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.Kind != AlterTableAlterConstraint {
			t.Fatalf("kind=%v want AlterTableAlterConstraint", act.Kind)
		}
		if act.ConstraintName != "nn" {
			t.Errorf("ConstraintName=%q want nn", act.ConstraintName)
		}
		if !act.AlterConstraintHasInheritability {
			t.Fatalf("expected AlterConstraintHasInheritability=true")
		}
		if act.AlterConstraintNoInherit {
			t.Errorf("expected AlterConstraintNoInherit=false for bare INHERIT")
		}
		if act.AlterConstraintHasDeferrability || act.AlterConstraintHasEnforceability {
			t.Errorf("bare INHERIT must not touch deferrability/enforceability, got has=%v/%v",
				act.AlterConstraintHasDeferrability, act.AlterConstraintHasEnforceability)
		}
	})
	t.Run("NoInherit", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ALTER CONSTRAINT nn NO INHERIT")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.AlterConstraintHasInheritability {
			t.Fatalf("expected AlterConstraintHasInheritability=true")
		}
		if !act.AlterConstraintNoInherit {
			t.Errorf("expected AlterConstraintNoInherit=true for NO INHERIT")
		}
	})
	t.Run("NoInheritComposedWithNotDeferrable", func(t *testing.T) {
		// NO INHERIT is part of ConstraintAttributeSpec (unlike bare INHERIT),
		// so it can combine with other attribute elements in one statement.
		stmts, err := Parse("ALTER TABLE t ALTER CONSTRAINT nn NOT DEFERRABLE NO INHERIT")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.AlterConstraintHasDeferrability || act.AlterConstraintDeferrable {
			t.Errorf("expected HasDeferrability=true Deferrable=false, got has=%v deferrable=%v",
				act.AlterConstraintHasDeferrability, act.AlterConstraintDeferrable)
		}
		if !act.AlterConstraintHasInheritability || !act.AlterConstraintNoInherit {
			t.Errorf("expected HasInheritability=true NoInherit=true, got has=%v noInherit=%v",
				act.AlterConstraintHasInheritability, act.AlterConstraintNoInherit)
		}
	})
}
