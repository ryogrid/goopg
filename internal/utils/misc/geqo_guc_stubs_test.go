package misc

import "testing"

// TestGeqoAndPlannerTuningGUCStubs asserts the GEQO GUC family and the three
// remaining QUERY_TUNING_OTHER GUCs (constraint_exclusion,
// cursor_tuple_fraction, recursive_worktable_factor) are registered with PG's
// boot values, contexts, and bounds. goopg's planner is rule/cost-based and
// never runs GEQO, so these are pure compatibility stubs — but tuning scripts,
// ORMs, and psql tab-completion issue `SET geqo_threshold = ...` etc., which
// must succeed instead of failing "unrecognized configuration parameter".
// Names/defaults/ranges mirror postgres/src/backend/utils/misc/guc_tables.c
// (numeric bounds from optimizer/geqo.h, cost.h, planmain.h). M0122-0007.
func TestGeqoAndPlannerTuningGUCStubs(t *testing.T) {
	cases := []struct {
		name    string
		bootVal string
		typ     Type
	}{
		{"geqo", "on", TypeBool},
		{"geqo_threshold", "12", TypeInt},
		{"geqo_effort", "5", TypeInt},
		{"geqo_pool_size", "0", TypeInt},
		{"geqo_generations", "0", TypeInt},
		{"geqo_selection_bias", "2", TypeReal},
		{"geqo_seed", "0", TypeReal},
		{"constraint_exclusion", "partition", TypeEnum},
		{"cursor_tuple_fraction", "0.1", TypeReal},
		{"recursive_worktable_factor", "10", TypeReal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessionRegistry(BuildDefaultRegistry())

			v, val, ok := s.Get(tc.name)
			if !ok {
				t.Fatalf("%s not registered", tc.name)
			}
			if val != tc.bootVal {
				t.Errorf("boot value = %q, want %q", val, tc.bootVal)
			}
			if v.Type != tc.typ {
				t.Errorf("type = %v, want %v", v.Type, tc.typ)
			}
			// All ten are PGC_USERSET in upstream, so a plain client SET
			// must succeed at the boot value.
			if v.Context != ContextUserset {
				t.Errorf("context = %v, want ContextUserset", v.Context)
			}
			if err := s.Set(tc.name, tc.bootVal, false); err != nil {
				t.Errorf("Set(%s, %q): unexpected error: %v", tc.name, tc.bootVal, err)
			}
		})
	}
}

// TestGeqoTuningGUCBoundsEnforced confirms the numeric/enum bounds from
// guc_tables.c are honoured so an out-of-range SET is rejected exactly as
// upstream would reject it (not silently clamped or accepted).
func TestGeqoTuningGUCBoundsEnforced(t *testing.T) {
	reject := []struct {
		name string
		val  string
	}{
		{"geqo_effort", "0"},                 // MIN_GEQO_EFFORT = 1
		{"geqo_effort", "11"},                // MAX_GEQO_EFFORT = 10
		{"geqo_threshold", "1"},              // min 2
		{"geqo_selection_bias", "1.4"},       // MIN_GEQO_SELECTION_BIAS = 1.5
		{"geqo_selection_bias", "2.1"},       // MAX_GEQO_SELECTION_BIAS = 2.0
		{"geqo_seed", "1.1"},                 // max 1.0
		{"cursor_tuple_fraction", "1.5"},     // max 1.0
		{"recursive_worktable_factor", "0"},  // min 0.001
		{"constraint_exclusion", "sometime"}, // not in {partition,on,off}
	}
	for _, tc := range reject {
		s := NewSessionRegistry(BuildDefaultRegistry())
		if err := s.Set(tc.name, tc.val, false); err == nil {
			t.Errorf("Set(%s, %q): expected out-of-range/invalid error, got nil", tc.name, tc.val)
		}
	}

	// Representative in-range values must be accepted.
	accept := []struct {
		name string
		val  string
	}{
		{"geqo_effort", "10"},
		{"geqo_selection_bias", "1.5"},
		{"cursor_tuple_fraction", "0.5"},
		{"recursive_worktable_factor", "1000000"},
		{"constraint_exclusion", "off"},
		{"constraint_exclusion", "on"},
	}
	for _, tc := range accept {
		s := NewSessionRegistry(BuildDefaultRegistry())
		if err := s.Set(tc.name, tc.val, false); err != nil {
			t.Errorf("Set(%s, %q): unexpected error: %v", tc.name, tc.val, err)
		}
	}
}
