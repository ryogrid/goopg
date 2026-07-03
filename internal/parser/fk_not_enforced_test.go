package parser

import "testing"

// TestParseFKNotEnforced covers DU-002 slice 431: a `NOT ENFORCED` trailer
// (PG18) on an `ALTER TABLE ADD CONSTRAINT ... FOREIGN KEY` must be captured,
// not rejected as a parse error — mirroring TestParseCheckNotEnforced's
// CHECK-form precedent (slice 430). It must also compose with NOT VALID and
// DEFERRABLE in any order, and a bare `ENFORCED`/no trailer at all must leave
// the flag false.
func TestParseFKNotEnforced(t *testing.T) {
	t.Run("PlainNotEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id) NOT ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.Kind != AlterTableAddForeignKey {
			t.Fatalf("kind=%v want AddForeignKey", act.Kind)
		}
		if !act.FKNotEnforced {
			t.Errorf("expected FKNotEnforced=true")
		}
		if act.NotValid {
			t.Errorf("expected NotValid=false when only NOT ENFORCED is given")
		}
	})
	t.Run("NotValidThenNotEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id) NOT VALID NOT ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.FKNotEnforced || !act.NotValid {
			t.Errorf("expected both NotValid and FKNotEnforced true, got NotValid=%v FKNotEnforced=%v", act.NotValid, act.FKNotEnforced)
		}
	})
	t.Run("DeferrableThenNotEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id) DEFERRABLE NOT ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.Deferrable {
			t.Errorf("expected Deferrable=true")
		}
		if !act.FKNotEnforced {
			t.Errorf("expected FKNotEnforced=true")
		}
	})
	t.Run("PlainNotValid", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id) NOT VALID")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.FKNotEnforced {
			t.Errorf("expected FKNotEnforced=false for a plain NOT VALID constraint")
		}
		if !act.NotValid {
			t.Errorf("expected NotValid=true")
		}
	})
	t.Run("BareEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id) ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.FKNotEnforced {
			t.Errorf("expected FKNotEnforced=false for a bare ENFORCED constraint")
		}
	})
	t.Run("NoTrailer", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES p(id)")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.FKNotEnforced {
			t.Errorf("expected FKNotEnforced=false with no trailer at all")
		}
	})
}
