package misc

import "testing"

// TestSetRealAcceptsScientificNotation is the review/260831-2 UT-2 guard.
// canonicalizeFrom's TypeReal arm scanned the numeric part with a hand-rolled
// sign/digit/dot loop, so an exponent ended up in the "unit suffix": "1e-2"
// split into number "1" and suffix "e-2", and because a unitless GUC ignores
// its suffix the stored value became 1 — a 100x error, silently accepted.
// PG parses reals with strtod (parse_real, guc.c):
//
//	postgres=# set seq_page_cost = 1e-2; show seq_page_cost;  ->  0.01
func TestSetRealAcceptsScientificNotation(t *testing.T) {
	cases := []struct {
		name, set, want string
	}{
		{"seq_page_cost", "1e-2", "0.01"},
		{"seq_page_cost", "1E2", "100"},
		{"seq_page_cost", "2.5e1", "25"},
		{"cursor_tuple_fraction", "5e-1", "0.5"},
	}
	for _, tc := range cases {
		s := NewSessionRegistry(BuildDefaultRegistry())
		if err := s.Set(tc.name, tc.set, false); err != nil {
			t.Fatalf("SET %s = %s: %v", tc.name, tc.set, err)
		}
		_, got, ok := s.Get(tc.name)
		if !ok {
			t.Fatalf("%s not registered", tc.name)
		}
		if got != tc.want {
			t.Errorf("SET %s = %s -> %q, want %q", tc.name, tc.set, got, tc.want)
		}
	}
}
