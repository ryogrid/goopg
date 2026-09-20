package optimizer

// M0145-0002 — the GOOPG_JOINTREE_PIPELINE dispatch
// (docs/design/0100-0149/m0145-0002-dual-pipeline-harness.md, AGENT.md
// §"Plan-parity harness" G8). Two pins:
//
//  1. the resolver is fail-closed — only the literal "1" arms the new
//     pipeline, so an unset/misspelled environment can never silently
//     route planning off the value-gated path;
//  2. while the jointree entry is a delegating stub (until M0145-0003
//     lands the IR), the knob arm is byte-for-byte the legacy plan — the
//     property the A/A knob-arm capture measures end-to-end.

import (
	"reflect"
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
	if !reflect.DeepEqual(off, on) {
		t.Errorf("delegating stub: knob-on plan differs from knob-off plan")
	}
}
