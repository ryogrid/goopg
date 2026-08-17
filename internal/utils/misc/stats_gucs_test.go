package misc

import "testing"

// TestStatsGUCs asserts the cumulative-statistics GUCs the isolation `stats`
// spec toggles are registered with PG 18.3 boot values, types, and enum
// options, and accept their valid values per session. goopg's cumulative
// statistics subsystem is being built incrementally (M0118-0009 stats enabler);
// the GUCs are recognised so the spec's setup/steps (e.g. SET track_functions =
// 'all', SET stats_fetch_consistency = 'snapshot') succeed rather than failing
// with `unrecognized configuration parameter`.
func TestStatsGUCs(t *testing.T) {
	cases := []struct {
		name    string
		boot    string
		typ     Type
		ctx     Context
		enum    []string
		accepts []string
	}{
		{"track_counts", "on", TypeBool, ContextSuset, nil, []string{"off", "on"}},
		{"track_functions", "none", TypeEnum, ContextSuset, []string{"none", "pl", "all"}, []string{"none", "pl", "all"}},
		{"stats_fetch_consistency", "cache", TypeEnum, ContextUserset, []string{"none", "cache", "snapshot"}, []string{"none", "cache", "snapshot"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessionRegistry(BuildDefaultRegistry())
			v, val, ok := s.Get(tc.name)
			if !ok {
				t.Fatalf("%s not registered", tc.name)
			}
			if val != tc.boot {
				t.Errorf("boot value = %q, want %q", val, tc.boot)
			}
			if v.Type != tc.typ {
				t.Errorf("type = %v, want %v", v.Type, tc.typ)
			}
			if v.Context != tc.ctx {
				t.Errorf("context = %v, want %v", v.Context, tc.ctx)
			}
			if tc.enum != nil {
				if len(v.EnumOptions) != len(tc.enum) {
					t.Fatalf("enum options = %v, want %v", v.EnumOptions, tc.enum)
				}
				for i, e := range tc.enum {
					if v.EnumOptions[i] != e {
						t.Errorf("enum option %d = %q, want %q", i, v.EnumOptions[i], e)
					}
				}
			}
			for _, valid := range tc.accepts {
				if err := s.Set(tc.name, valid, false); err != nil {
					t.Errorf("Set(%s, %q): %v", tc.name, valid, err)
				}
			}
			if tc.typ == TypeEnum {
				if err := s.Set(tc.name, "bogus", false); err == nil {
					t.Errorf("Set(%s, bogus) accepted; want error", tc.name)
				}
			}
		})
	}
}
