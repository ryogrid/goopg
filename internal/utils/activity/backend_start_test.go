package activity

import (
	"testing"
	"time"
)

// TestRegisterKeepsBackendStart is the review/260831-2 UT-1 guard.
// coldFromBackend copied every other immutable field but silently dropped
// Backend.BackendStart (an RFC3339Nano string on the way in, unix nanos in
// the slot), so pg_stat_activity.backend_start came back EMPTY for every
// client backend even though postmaster/server.go stamps it at connection
// setup. PG's backend_start is never null.
func TestRegisterKeepsBackendStart(t *testing.T) {
	r := NewActivityRegistry(4)
	start := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Microsecond)
	r.Register(&Backend{
		PID:          "4242",
		BackendStart: start.Format(time.RFC3339Nano),
		State:        "active",
		BackendType:  "client_backend",
	})

	var got Backend
	for _, b := range r.Snapshot() {
		if b.PID == "4242" {
			got = b
		}
	}
	if got.PID == "" {
		t.Fatal("registered backend not in Snapshot")
	}
	if got.BackendStart == "" {
		t.Fatal("backend_start is empty")
	}
	parsed, err := time.Parse(time.RFC3339Nano, got.BackendStart)
	if err != nil {
		t.Fatalf("backend_start %q not RFC3339Nano: %v", got.BackendStart, err)
	}
	if !parsed.Equal(start) {
		t.Errorf("backend_start = %v, want %v", parsed, start)
	}
}
