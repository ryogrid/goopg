package config

import "testing"

// TestPgDumpConnectionSetupGUCs guards the GUCs pg_dump's setup_connection()
// issues before any catalog query: it runs `SET synchronize_seqscans TO off`,
// `SET transaction_timeout = 0` (PG 17+), and `SET row_security = off`. goopg
// does not enforce any of them, but the SET must succeed — otherwise pg_dump
// aborts with `unrecognized configuration parameter`. (M0110-0001.)
func TestPgDumpConnectionSetupGUCs(t *testing.T) {
	r := BuildDefaultRegistry()
	cases := []struct {
		name    string
		bootVal string
		typ     Type
		set     string
	}{
		{"synchronize_seqscans", "on", TypeBool, "off"},
		{"transaction_timeout", "0", TypeInt, "0"},
		{"row_security", "on", TypeBool, "off"},
	}
	for _, tc := range cases {
		v, ok := r.Get(tc.name)
		if !ok {
			t.Errorf("%s not registered", tc.name)
			continue
		}
		if v.Type != tc.typ {
			t.Errorf("%s: Type=%v want %v", tc.name, v.Type, tc.typ)
		}
		if v.Value != tc.bootVal {
			t.Errorf("%s: boot Value=%q want %q", tc.name, v.Value, tc.bootVal)
		}
		if err := v.Set(tc.set, SourceSession); err != nil {
			t.Errorf("%s: SET %s: %v", tc.name, tc.set, err)
		}
	}
}
