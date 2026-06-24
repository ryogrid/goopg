package config

import "testing"

// TestAllowSystemTableModsGUC asserts the developer GUC is registered with PG's
// boot value (off) and superuser-set context, and is settable per session.
// goopg does not yet gate any catalog-structure modification on it; the GUC is
// recognised so regression/isolation setups that `SET allow_system_table_mods =
// on` succeed rather than failing with `unrecognized configuration parameter`.
// M0118-0008 (reindex-concurrently-toast enabler, design 0118-0065).
func TestAllowSystemTableModsGUC(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())

	v, val, ok := s.Get("allow_system_table_mods")
	if !ok {
		t.Fatal("allow_system_table_mods not registered")
	}
	if val != "off" {
		t.Errorf("boot value = %q, want off", val)
	}
	if v.Type != TypeBool {
		t.Errorf("type = %v, want TypeBool", v.Type)
	}
	if v.Context != ContextSuset {
		t.Errorf("context = %v, want ContextSuset", v.Context)
	}

	if err := s.Set("allow_system_table_mods", "on", false); err != nil {
		t.Fatalf("Set on: %v", err)
	}
	if _, val, _ := s.Get("allow_system_table_mods"); val != "on" {
		t.Errorf("after Set: value = %q, want on", val)
	}
}
