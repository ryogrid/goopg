package postmaster

// review/260831-2 X-8 — a session with a scan-method toggle off plans
// DIFFERENTLY, and the cross-session plan cache keys only on (dbOid,
// normalized SQL). Once the toggles became real planner input, a `SET
// enable_indexscan = off` session would have published its Seq Scan plan to
// every other connection running the same query text (and read theirs). Both
// directions are closed by keeping such a session off the shared cache, which
// is what plannerScanTogglesActive gates.

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

func TestPlannerScanTogglesActiveDetectsEveryToggle(t *testing.T) {
	if plannerScanTogglesActive(nil) {
		t.Error("nil session must not report toggles active")
	}

	fresh := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if plannerScanTogglesActive(fresh) {
		t.Error("a fresh session (every toggle at its `on` default) must use the shared plan cache")
	}

	for _, name := range []string{"enable_seqscan", "enable_indexscan",
		"enable_bitmapscan", "enable_indexonlyscan"} {
		sess := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
		if err := sess.Set(name, "off", false); err != nil {
			t.Fatalf("SET %s = off: %v", name, err)
		}
		if !plannerScanTogglesActive(sess) {
			t.Errorf("SET %s = off must take the session off the shared plan cache", name)
		}
		if err := sess.Reset(name); err != nil {
			t.Fatalf("RESET %s: %v", name, err)
		}
		if plannerScanTogglesActive(sess) {
			t.Errorf("RESET %s must put the session back on the shared plan cache", name)
		}
	}
}
