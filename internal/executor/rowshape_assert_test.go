package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRowShapeAssertionCatchesPlannerExecutorDisagreement pins the assertion
// added for take2's P4-A review. The invariant it guards spans the planner and
// the executor — "a node's Output() equals the row its operator emits" — which
// is why no plan-time tripwire caught P4-01b: the PLAN was self-consistent.
func TestRowShapeAssertionCatchesPlannerExecutorDisagreement(t *testing.T) {
	prev := assertRowShapeEnabled
	assertRowShapeEnabled = true
	defer func() { assertRowShapeEnabled = prev }()

	schema := optimizer.Schema{{Name: "a"}, {Name: "b"}}

	// A 3-column row under a 2-column schema is P4-01b in miniature.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("a 3-column row under a 2-column schema did not trip the assertion")
			}
			msg, _ := r.(string)
			for _, want := range []string{"row-shape assertion", "3-column", "2-column", "seqScanOp"} {
				if !strings.Contains(msg, want) {
					t.Errorf("panic message missing %q; got: %s", want, msg)
				}
			}
		}()
		assertRowShapeInline("seqScanOp", schema, 3)
	}()

	// It must not fire on a well-formed row, or it would be useless.
	assertRowShapeInline("seqScanOp", schema, 2)
}

// TestRowShapeAssertionIsOffByDefault: production pays nothing unless the env
// var asks for the check.
func TestRowShapeAssertionIsOffByDefault(t *testing.T) {
	prev := assertRowShapeEnabled
	assertRowShapeEnabled = false
	defer func() { assertRowShapeEnabled = prev }()
	// A mismatch must be ignored entirely when disabled.
	assertRowShapeInline("seqScanOp", optimizer.Schema{{Name: "a"}}, 99)
}
