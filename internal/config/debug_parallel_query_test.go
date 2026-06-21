package config

import "testing"

// TestDebugParallelQueryGUC asserts the developer GUC is registered with PG's
// boot value (off) as a user-settable enum accepting off/on/regress, and that a
// bogus value is rejected. goopg has no parallel executor so the GUC is a no-op,
// but SET must succeed so upstream isolation specs (serializable-parallel*) that
// flip it during session setup don't fail with "unrecognized configuration
// parameter". M0118-0001.
func TestDebugParallelQueryGUC(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())

	v, val, ok := s.Get("debug_parallel_query")
	if !ok {
		t.Fatal("debug_parallel_query not registered")
	}
	if val != "off" {
		t.Errorf("boot value = %q, want off", val)
	}
	if v.Type != TypeEnum {
		t.Errorf("type = %v, want TypeEnum", v.Type)
	}
	if v.Context != ContextUserset {
		t.Errorf("context = %v, want ContextUserset", v.Context)
	}

	// The spec uses "on"; the full enum should also accept "regress" and be
	// case-insensitive, and reset back to "off".
	for _, want := range []string{"on", "regress", "off"} {
		if err := s.Set("debug_parallel_query", want, false); err != nil {
			t.Fatalf("Set %q: %v", want, err)
		}
		if _, got, _ := s.Get("debug_parallel_query"); got != want {
			t.Errorf("after Set %q: value = %q", want, got)
		}
	}
	if err := s.Set("debug_parallel_query", "ON", false); err != nil {
		t.Fatalf("Set ON (case-insensitive): %v", err)
	}
	if _, got, _ := s.Get("debug_parallel_query"); got != "on" {
		t.Errorf("after Set ON: value = %q, want on", got)
	}

	// A value outside the enum must be rejected.
	if err := s.Set("debug_parallel_query", "maybe", false); err == nil {
		t.Error("Set maybe: expected error for invalid enum value, got nil")
	}
}
