package executor

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAnalyzeSeedDefaultUnsetIsWallClock pins the production default: with
// GOOPG_ANALYZE_SEED unset, analyzeSeedDefault draws a fresh seed per call, so
// ANALYZE stays sampled exactly as upstream's acquire_sample_rows is
// (postgres/src/backend/commands/analyze.c). A pinned default here would
// silently make every production ANALYZE draw the same sample.
func TestAnalyzeSeedDefaultUnsetIsWallClock(t *testing.T) {
	if analyzeSeedEnv != 0 {
		t.Skip("GOOPG_ANALYZE_SEED is set in this environment")
	}
	tbl := &catalog.Table{OID: 16384}
	a := analyzeSeedFor(tbl)
	var differs bool
	for i := 0; i < 1000 && !differs; i++ {
		if analyzeSeedFor(tbl) != a {
			differs = true
		}
	}
	if !differs {
		t.Fatalf("analyzeSeedFor returned the same seed %d on 1001 calls; "+
			"the unset default must not be pinned", a)
	}
}

// TestAnalyzeSeedEnvPinsSampler runs this package's own binary in a
// subprocess with GOOPG_ANALYZE_SEED set, because analyzeSeedEnv is read once
// at package init — the value cannot be changed in-process without making the
// production path re-read the environment per ANALYZE.
func TestAnalyzeSeedEnvPinsSampler(t *testing.T) {
	if os.Getenv("GOOPG_ANALYZE_SEED_CHILD") == "1" {
		// Child: the env seed must win over the wall clock, on every call.
		if analyzeSeedEnv != 4242 {
			t.Fatalf("analyzeSeedEnv = %d, want 4242", analyzeSeedEnv)
		}
		// The env seed wins over the wall clock on every call, and the
		// per-relation mix is deterministic: same table, same seed;
		// different tables, different seeds (decorrelated reservoirs).
		a := &catalog.Table{OID: 16384}
		b := &catalog.Table{OID: 16385}
		wantA := int64(4242) ^ int64(a.OID)
		for i := 0; i < 100; i++ {
			if got := analyzeSeedFor(a); got != wantA {
				t.Fatalf("analyzeSeedFor(a) = %d on call %d, want %d", got, i, wantA)
			}
		}
		if analyzeSeedFor(b) == analyzeSeedFor(a) {
			t.Fatalf("two relations share seed %d; reservoirs would replay "+
				"the identical stream", analyzeSeedFor(a))
		}
		if got := analyzeSeedFor(nil); got != 4242 {
			t.Fatalf("analyzeSeedFor(nil) = %d, want the bare pinned seed 4242", got)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(),
		"GOOPG_ANALYZE_SEED_CHILD=1",
		"GOOPG_ANALYZE_SEED=4242",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	// "--- PASS: <name>", not a bare "PASS": a renamed test would match zero
	// cases, print "no tests to run" AND "PASS", exit 0, and this parent
	// would pass with no assertion ever executed.
	if !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("child did not run %s:\n%s", t.Name(), out)
	}
}

// TestAnalyzeSeedEnvRejectsGarbage pins the fail-open parse: an unparsable
// value behaves as unset (wall clock) rather than seeding with zero, which
// would pin every sample to one draw without anyone asking for it.
func TestAnalyzeSeedEnvRejectsGarbage(t *testing.T) {
	if os.Getenv("GOOPG_ANALYZE_SEED_CHILD") == "2" {
		if analyzeSeedEnv != 0 {
			t.Fatalf("analyzeSeedEnv = %d for a non-numeric value, want 0", analyzeSeedEnv)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(),
		"GOOPG_ANALYZE_SEED_CHILD=2",
		"GOOPG_ANALYZE_SEED=not-a-number",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	// "--- PASS: <name>", not a bare "PASS": a renamed test would match zero
	// cases, print "no tests to run" AND "PASS", exit 0, and this parent
	// would pass with no assertion ever executed.
	if !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("child did not run %s:\n%s", t.Name(), out)
	}
}
