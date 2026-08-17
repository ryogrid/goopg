package misc

import "testing"

// TestJitGUCFamilyStubs asserts the remainder of upstream's jit_* GUC family
// (guc_tables.c) is registered with PG's boot values, contexts, and bounds,
// so SET/SHOW/pg_settings on a script written against real PostgreSQL never
// fails with "unrecognized configuration parameter" — goopg has no JIT
// compiler at all, so these exist purely as compatibility stubs, matching
// the pre-existing "jit"/"jit_above_cost"/"compute_query_id" stubs
// (M0097-0073). M0122-0007 follow-up.
func TestJitGUCFamilyStubs(t *testing.T) {
	cases := []struct {
		name        string
		bootVal     string
		typ         Type
		context     Context
		settableSQL bool // whether `SET name = ...` should succeed
	}{
		{"jit_debugging_support", "off", TypeBool, ContextSuBackend, false},
		{"jit_dump_bitcode", "off", TypeBool, ContextSuset, true},
		{"jit_expressions", "on", TypeBool, ContextUserset, true},
		{"jit_profiling_support", "off", TypeBool, ContextSuBackend, false},
		{"jit_tuple_deforming", "on", TypeBool, ContextUserset, true},
		{"jit_provider", "llvmjit", TypeString, ContextPostmaster, false},
		{"jit_optimize_above_cost", "500000", TypeReal, ContextUserset, true},
		{"jit_inline_above_cost", "500000", TypeReal, ContextUserset, true},
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
			if v.Context != tc.context {
				t.Errorf("context = %v, want %v", v.Context, tc.context)
			}

			err := s.Set(tc.name, tc.bootVal, false)
			if tc.settableSQL && err != nil {
				t.Errorf("Set(%s): unexpected error: %v", tc.name, err)
			}
			if !tc.settableSQL && err == nil {
				t.Errorf("Set(%s): expected error (context %v not SET-able), got nil", tc.name, tc.context)
			}
		})
	}
}

// TestJitCostGUCsAcceptNegativeOneSentinel mirrors jit_above_cost's existing
// -1-disables-JIT convention (guc_tables.c) for the two sibling cost GUCs.
func TestJitCostGUCsAcceptNegativeOneSentinel(t *testing.T) {
	for _, name := range []string{"jit_optimize_above_cost", "jit_inline_above_cost"} {
		s := NewSessionRegistry(BuildDefaultRegistry())
		if err := s.Set(name, "-1", false); err != nil {
			t.Errorf("Set(%s, -1): unexpected error: %v", name, err)
		}
		if err := s.Set(name, "-2", false); err == nil {
			t.Errorf("Set(%s, -2): expected out-of-range error, got nil", name)
		}
	}
}
