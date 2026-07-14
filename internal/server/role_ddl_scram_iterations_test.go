package server

// TestCreateAlterRolePasswordHonorsScramIterationsGUC guards the
// scram_iterations wiring (postgres/src/backend/commands/user.c
// encrypt_password / postgres/src/common/scram-common.c
// scram_build_secret both read the live GUC, not a hardcoded default):
// CREATE/ALTER ROLE ... PASSWORD must derive the SCRAM-SHA-256 verifier
// using the CALLING SESSION's current scram_iterations value, so `SET
// scram_iterations = N` changes the PBKDF2 cost of newly-set passwords.

import (
	"testing"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/catalog"
)

func TestCreateAlterRolePasswordHonorsScramIterationsGUC(t *testing.T) {
	s := newTestRoleServer()
	im := s.cfg.Catalog.(*catalog.InMemory)

	// No resolver (e.g. an internal/bootstrap caller) falls back to
	// upstream's SCRAM_SHA_256_DEFAULT_ITERATIONS (4096).
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE alice LOGIN PASSWORD 'hunter2'", "postgres", nil); !handled || err != nil {
		t.Fatalf("CREATE ROLE alice: handled=%v err=%v", handled, err)
	}
	aliceAttrs, ok := im.LookupRoleAttrs("alice")
	if !ok {
		t.Fatal("alice: expected a recorded RoleAttrs sidecar entry")
	}
	aliceSecret, err := auth.ParseSCRAMSecret(aliceAttrs.Secret)
	if err != nil {
		t.Fatalf("alice: ParseSCRAMSecret: %v", err)
	}
	if aliceSecret.Iterations != 4096 {
		t.Errorf("alice: Iterations = %d, want 4096 (default, nil resolver)", aliceSecret.Iterations)
	}

	// A live session reporting a non-default scram_iterations must be
	// honored both by CREATE ROLE and by ALTER ROLE ... PASSWORD.
	live := map[string]string{"scram_iterations": "1024"}
	resolver := currentGUCResolver(func(name string) (string, bool) {
		v, ok := live[name]
		return v, ok
	})
	if handled, err := s.tryHandleRoleDDL("CREATE ROLE bob LOGIN PASSWORD 'hunter2'", "postgres", resolver); !handled || err != nil {
		t.Fatalf("CREATE ROLE bob: handled=%v err=%v", handled, err)
	}
	bobAttrs, ok := im.LookupRoleAttrs("bob")
	if !ok {
		t.Fatal("bob: expected a recorded RoleAttrs sidecar entry")
	}
	bobSecret, err := auth.ParseSCRAMSecret(bobAttrs.Secret)
	if err != nil {
		t.Fatalf("bob: ParseSCRAMSecret: %v", err)
	}
	if bobSecret.Iterations != 1024 {
		t.Errorf("bob: Iterations = %d, want 1024 (from live scram_iterations GUC)", bobSecret.Iterations)
	}

	live["scram_iterations"] = "42"
	if handled, err := s.tryHandleRoleDDL("ALTER ROLE alice PASSWORD 'newpass'", "postgres", resolver); !handled || err != nil {
		t.Fatalf("ALTER ROLE alice PASSWORD: handled=%v err=%v", handled, err)
	}
	aliceAttrs, ok = im.LookupRoleAttrs("alice")
	if !ok {
		t.Fatal("alice: expected a recorded RoleAttrs sidecar entry after ALTER")
	}
	aliceSecret, err = auth.ParseSCRAMSecret(aliceAttrs.Secret)
	if err != nil {
		t.Fatalf("alice: ParseSCRAMSecret after ALTER: %v", err)
	}
	if aliceSecret.Iterations != 42 {
		t.Errorf("alice: Iterations after ALTER = %d, want 42 (live scram_iterations GUC)", aliceSecret.Iterations)
	}
}
