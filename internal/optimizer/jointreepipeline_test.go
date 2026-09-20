package optimizer

// M0145-0002 — the GOOPG_JOINTREE_PIPELINE dispatch
// (docs/design/0100-0149/m0145-0002-dual-pipeline-harness.md, AGENT.md
// §"Plan-parity harness" G8). Two pins:
//
//  1. the resolver is fail-closed — only the literal "1" arms the new
//     pipeline, so an unset/misspelled environment can never silently
//     route planning off the value-gated path;
//  2. the dispatch reaches the arm it names: on a scope the arms plan
//     differently (a single-table statement, since M0145-0005 slice 4
//     lifted the isSimpleSingle bypass on the jointree arm), the
//     knob-on plan carries the searched-subtree tag and the knob-off
//     plan does not.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestJointreePipelineKnobPolarity(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{
		{"", false},     // unset — the default: the legacy pipeline is the arm
		{"0", false},    // explicit off
		{"1", true},     // the only on value
		{"on", false},   // unrecognised: fail closed, stay legacy
		{"true", false}, // unrecognised: fail closed, stay legacy
		{"yes", false},  // unrecognised: fail closed, stay legacy
	} {
		if got := jointreePipelineFromEnv(tc.env); got != tc.want {
			t.Errorf("jointreePipelineFromEnv(%q) = %v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestJointreePipelineDispatchDelegates(t *testing.T) {
	cat := oneRelRoutedCatalog(t)
	stmts, err := parser.Parse("select a from t where a > 3 order by a")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	defer func(v bool) { jointreePipeline = v }(jointreePipeline)
	jointreePipeline = false
	off, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("knob-off plan: %v", err)
	}
	jointreePipeline = true
	on, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("knob-on plan: %v", err)
	}
	// M0145-0005 slice 4: the arms legitimately diverge on this
	// single-table statement — the jointree arm lifts the isSimpleSingle
	// bypass so the scope is searched, the legacy arm keeps the
	// rule-chooser path. The delegation pin is therefore the routing
	// itself: knob-on carries the searched-subtree tag, knob-off does not.
	if !treeHasSearched(on) {
		t.Errorf("knob-on plan is not a search product — the dispatch did not reach the jointree arm")
	}
	if treeHasSearched(off) {
		t.Errorf("knob-off plan carries the search tag — the lift leaked onto the legacy arm")
	}
}
