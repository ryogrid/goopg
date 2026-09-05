package optimizer

// C-02a gate: PG initsplan.c case table for the per-link delay test.
// Bit convention: leaf 0 = left input, leaf 1 = right input.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func delaySJI(jt parser.JoinType) *SpecialJoinInfo {
	return &SpecialJoinInfo{
		SynLefthand:  0b001,
		SynRighthand: 0b010,
		MinLefthand:  0b001,
		MinRighthand: 0b010,
		Jointype:     jt,
	}
}

func TestDelayedAboveOJ(t *testing.T) {
	tests := []struct {
		name  string
		jt    parser.JoinType
		qual  RelSet
		delay bool
	}{
		// LEFT: nullable side is right.
		{"left/preserved-only places", parser.JoinLeft, 0b001, false},
		{"left/nullable delays", parser.JoinLeft, 0b010, true},
		{"left/spanning delays", parser.JoinLeft, 0b011, true},
		{"left/constant places", parser.JoinLeft, 0, false},
		{"left/outside places", parser.JoinLeft, 0b100, false},
		// Strictness does NOT exempt: `nullable.x = 1` is strict and still
		// delays (demotion already ran; this link survived it).
		{"left/strict-on-nullable delays", parser.JoinLeft, 0b010, true},
		// RIGHT: mirror image.
		{"right/preserved-only places", parser.JoinRight, 0b010, false},
		{"right/nullable delays", parser.JoinRight, 0b001, true},
		{"right/spanning delays", parser.JoinRight, 0b011, true},
		{"right/constant places", parser.JoinRight, 0, false},
		// FULL: both sides nullable.
		{"full/left delays", parser.JoinFull, 0b001, true},
		{"full/right delays", parser.JoinFull, 0b010, true},
		{"full/constant places", parser.JoinFull, 0, false},
		{"full/outside places", parser.JoinFull, 0b100, false},
		// INNER/CROSS never delay.
		{"inner/spanning places", parser.JoinInner, 0b011, false},
		{"cross/spanning places", parser.JoinCross, 0b011, false},
		// SEMI/ANTI fail closed (no caller descends into them).
		{"semi delays", parser.JoinSemi, 0b001, true},
		{"anti delays", parser.JoinAnti, 0b010, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := delayedAboveOJ(tc.qual, delaySJI(tc.jt)); got != tc.delay {
				t.Errorf("delayedAboveOJ(%03b, %v) = %v, want %v",
					tc.qual, tc.jt, got, tc.delay)
			}
		})
	}
}

func TestDelayedAboveOJNilDelays(t *testing.T) {
	if !delayedAboveOJ(0, nil) {
		t.Error("nil sj must delay (fail-closed), even for an empty qual")
	}
	if !delayedAboveOJ(0b001, nil) {
		t.Error("nil sj must delay (fail-closed)")
	}
}
