package postmaster

// B-18 commit 1 (take2 P2-04 cache-key half) — a session with a scan-method
// toggle off plans DIFFERENTLY, and the cross-session plan cache must serve it
// its own plan. Before, such a session bypassed the shared cache entirely
// (plannerScanTogglesActive); now the four toggles ride in the cache key, so
// the session reads and writes its own entry instead of opting out.

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

func TestSessionPlannerFingerprintDetectsEveryScanToggle(t *testing.T) {
	fresh := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	base := sessionPlannerFingerprint(fresh)

	for _, name := range []string{"enable_seqscan", "enable_indexscan",
		"enable_bitmapscan", "enable_indexonlyscan"} {
		sess := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
		if err := sess.Set(name, "off", false); err != nil {
			t.Fatalf("SET %s = off: %v", name, err)
		}
		if got := sessionPlannerFingerprint(sess); got == base {
			t.Errorf("SET %s = off must change the plan-cache fingerprint", name)
		}
		if err := sess.Reset(name); err != nil {
			t.Fatalf("RESET %s: %v", name, err)
		}
		if got := sessionPlannerFingerprint(sess); got != base {
			t.Errorf("RESET %s must restore the plan-cache fingerprint", name)
		}
	}
}

func TestSessionPlannerFingerprintNilSessionIsDefault(t *testing.T) {
	fresh := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if got, want := sessionPlannerFingerprint(nil), sessionPlannerFingerprint(fresh); got != want {
		t.Error("nil session must fingerprint exactly like a fresh session")
	}
}
