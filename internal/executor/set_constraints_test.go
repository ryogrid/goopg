package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestFKConstraintDeferredPrecedence verifies the SET CONSTRAINTS deferral
// decision: a per-constraint override wins over an ALL setting, which wins over
// the constraint's declared INITIALLY {DEFERRED|IMMEDIATE} default. 0119-0004.
func TestFKConstraintDeferredPrecedence(t *testing.T) {
	// No SET CONSTRAINTS in effect: the declared default decides.
	s := NewBasicSession()
	if got := s.FKConstraintDeferred("fk1", true); !got {
		t.Errorf("default INITIALLY DEFERRED: got %v want true", got)
	}
	if got := s.FKConstraintDeferred("fk1", false); got {
		t.Errorf("default INITIALLY IMMEDIATE: got %v want false", got)
	}

	// SET CONSTRAINTS ALL DEFERRED overrides an IMMEDIATE default.
	s.SetConstraintsAll(true)
	if got := s.FKConstraintDeferred("fk1", false); !got {
		t.Errorf("ALL DEFERRED over IMMEDIATE default: got %v want true", got)
	}

	// SET CONSTRAINTS ALL IMMEDIATE overrides a DEFERRED default.
	s.SetConstraintsAll(false)
	if got := s.FKConstraintDeferred("fk1", true); got {
		t.Errorf("ALL IMMEDIATE over DEFERRED default: got %v want false", got)
	}

	// A per-name DEFERRED overrides ALL IMMEDIATE for just that constraint.
	s.SetConstraintsNamed([]string{"fk1"}, true)
	if got := s.FKConstraintDeferred("fk1", false); !got {
		t.Errorf("named DEFERRED over ALL IMMEDIATE: got %v want true", got)
	}
	if got := s.FKConstraintDeferred("fk2", false); got {
		t.Errorf("unnamed constraint still ALL IMMEDIATE: got %v want false", got)
	}

	// A subsequent ALL supersedes the per-name overrides.
	s.SetConstraintsAll(true)
	if got := s.FKConstraintDeferred("fk1", false); !got {
		t.Errorf("ALL DEFERRED supersedes named: got %v want true", got)
	}

	// EndExplicitTransaction resets to the declared defaults.
	s.EndExplicitTransaction()
	if got := s.FKConstraintDeferred("fk1", false); got {
		t.Errorf("after txn end: got %v want false (declared default)", got)
	}
}

// TestTakeDeferredFKChecksMatching verifies that SET CONSTRAINTS … IMMEDIATE
// removes the right subset of queued checks: ALL drains everything, a named
// form takes only the matching constraint and keeps the rest queued. 0119-0004.
func TestTakeDeferredFKChecksMatching(t *testing.T) {
	mk := func() *BasicSession {
		s := NewBasicSession()
		s.deferredFKChecks = []DeferredFKCheck{
			{ChildTableName: "c1", FK: catalog.ForeignKey{Name: "fk1"}},
			{ChildTableName: "c2", FK: catalog.ForeignKey{Name: "fk2"}},
			{ChildTableName: "c3", FK: catalog.ForeignKey{Name: "fk3"}},
		}
		return s
	}

	// ALL: takes everything, leaves the queue empty.
	s := mk()
	got := s.TakeDeferredFKChecksMatching(true, nil)
	if len(got) != 3 || len(s.deferredFKChecks) != 0 {
		t.Fatalf("ALL: took %d (want 3), %d left (want 0)", len(got), len(s.deferredFKChecks))
	}

	// Named: takes only fk2, keeps fk1 and fk3 queued.
	s = mk()
	got = s.TakeDeferredFKChecksMatching(false, []string{"fk2"})
	if len(got) != 1 || got[0].FK.Name != "fk2" {
		t.Fatalf("named: took %d %v, want [fk2]", len(got), got)
	}
	if len(s.deferredFKChecks) != 2 {
		t.Fatalf("named: %d left, want 2", len(s.deferredFKChecks))
	}
}
