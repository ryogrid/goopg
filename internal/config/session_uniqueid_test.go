package config

import "testing"

// TestSessionRegistryUniqueID checks that every SessionRegistry receives a
// stable, non-zero, process-unique identity. This token is the owning-session
// marker for temporary relations (design 0118-0036): it must be fixed for the
// session's lifetime and must differ between two concurrent sessions so that
// inheritance expansion can tell one backend's temp children from another's.
func TestSessionRegistryUniqueID(t *testing.T) {
	g := NewRegistry()
	s1 := NewSessionRegistry(g)
	s2 := NewSessionRegistry(g)

	if s1.UniqueID() == 0 || s2.UniqueID() == 0 {
		t.Fatalf("UniqueID must be non-zero: s1=%d s2=%d", s1.UniqueID(), s2.UniqueID())
	}
	if s1.UniqueID() == s2.UniqueID() {
		t.Fatalf("two sessions share an ID: %d", s1.UniqueID())
	}
	// Stable across repeated calls.
	if got := s1.UniqueID(); got != s1.UniqueID() {
		t.Fatalf("UniqueID not stable: %d vs %d", got, s1.UniqueID())
	}

	// A nil receiver yields 0 ("no session").
	var nilReg *SessionRegistry
	if got := nilReg.UniqueID(); got != 0 {
		t.Fatalf("nil session UniqueID = %d, want 0", got)
	}
}
