package config

import "testing"

// TestAllowInPlaceTablespacesGUC asserts the developer GUC is registered with
// PG's boot value (off) and superuser-set context, and is settable per session.
// M0095-0003.
func TestAllowInPlaceTablespacesGUC(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())

	v, val, ok := s.Get("allow_in_place_tablespaces")
	if !ok {
		t.Fatal("allow_in_place_tablespaces not registered")
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

	if err := s.Set("allow_in_place_tablespaces", "on", false); err != nil {
		t.Fatalf("Set on: %v", err)
	}
	if _, val, _ := s.Get("allow_in_place_tablespaces"); val != "on" {
		t.Errorf("after Set: value = %q, want on", val)
	}
}
