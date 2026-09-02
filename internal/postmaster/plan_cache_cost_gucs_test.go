package postmaster

// take2 P2-04 — the same hazard as the scan toggles (see
// plan_cache_scan_toggles_test.go), one step ahead of the change that arms it.
//
// The plan cache is server-level and cross-session, keyed on
// (dbOid, normalized SQL) with no GUC fingerprint. The nine cost GUCs were
// inert until P2-01 built the carrier, so every session planned identically and
// the cache was safe by accident. The moment P2-02 fills PlannerSettings from
// the session, a plan costed under one connection's `random_page_cost` becomes
// servable to every other connection running the same SQL — and that
// connection's plan becomes servable back.
//
// This is why P2-04 is a PREREQUISITE of P2-02 rather than a follow-up: landing
// P2-02 first would open the leak, and nothing in the suite would notice.

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

// costGUCs is the list PlannerSettings carries. Kept here rather than exported
// from the guard so a GUC added to one and forgotten in the other fails a test
// instead of silently going uncovered.
var costGUCs = []string{
	"seq_page_cost", "random_page_cost",
	"cpu_tuple_cost", "cpu_index_tuple_cost", "cpu_operator_cost",
	"parallel_setup_cost", "parallel_tuple_cost",
	"effective_cache_size", "work_mem",
}

func TestPlannerCostGUCsOverriddenDetectsEveryCostGUC(t *testing.T) {
	if plannerCostGUCsOverridden(nil) {
		t.Error("nil session must not report cost GUCs overridden")
	}

	fresh := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if plannerCostGUCsOverridden(fresh) {
		t.Error("a fresh session must use the shared plan cache")
	}

	for _, name := range costGUCs {
		sess := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
		v, cur, ok := sess.Get(name)
		if !ok || v == nil {
			t.Fatalf("%s is not a registered GUC", name)
		}
		// SET to the value it already has. The guard must still fire: the
		// question is "did this session take its own planner inputs", not
		// "does its value differ from the default". A value comparison would
		// wrongly declare this session cache-safe, and it is not — the next
		// SET in the same session would then publish a plan built under it.
		if err := sess.Set(name, cur, false); err != nil {
			t.Fatalf("SET %s = %s: %v", name, cur, err)
		}
		if !plannerCostGUCsOverridden(sess) {
			t.Errorf("SET %s must take the session off the shared plan cache", name)
		}
		if !plannerSessionInputsActive(sess) {
			t.Errorf("SET %s must be visible through plannerSessionInputsActive, "+
				"which is what the four cache guards actually call", name)
		}
		if err := sess.Reset(name); err != nil {
			t.Fatalf("RESET %s: %v", name, err)
		}
		if plannerCostGUCsOverridden(sess) {
			t.Errorf("RESET %s must put the session back on the shared plan cache", name)
		}
	}
}

// TestPlannerSessionInputsActiveCoversBothFamilies pins that the single
// predicate the guard sites call subsumes the older scan-toggle one. Adding a
// third family later must extend this function, not a fifth call site.
func TestPlannerSessionInputsActiveCoversBothFamilies(t *testing.T) {
	if plannerSessionInputsActive(nil) {
		t.Error("nil session must be cacheable")
	}
	scan := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if err := scan.Set("enable_indexscan", "off", false); err != nil {
		t.Fatal(err)
	}
	if !plannerSessionInputsActive(scan) {
		t.Error("scan-toggle family regressed out of the combined predicate")
	}
	cost := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if err := cost.Set("random_page_cost", "1.1", false); err != nil {
		t.Fatal(err)
	}
	if !plannerSessionInputsActive(cost) {
		t.Error("cost-GUC family not covered by the combined predicate")
	}
}
