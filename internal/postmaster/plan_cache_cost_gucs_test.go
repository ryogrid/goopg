package postmaster

// take2 P2-04, B-18 commit 1 (cache-key half) — the same hazard as the scan
// toggles (see plan_cache_scan_toggles_test.go).
//
// The plan cache is server-level and cross-session. Until P2-01 the cost GUCs
// were inert — defaultCostParams() was hard-wired — so every session planned
// identically and the cache was safe by accident. The moment P2-02 fills PlannerSettings from
// the session, a plan costed under one connection's `random_page_cost` becomes
// servable to every other connection running the same SQL — and that
// connection's plan becomes servable back.
//
// This is why P2-04 is a PREREQUISITE of P2-02 rather than a follow-up: landing
// P2-02 first would open the leak, and nothing in the suite would notice.

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/misc"
)

// take2 P2-04, B-18 commit 1 (cache-key half).
//
// The plan cache is server-level and cross-session. Keying on
// (dbOid, normalized SQL) alone would serve a plan costed under one
// connection's `random_page_cost` to every other connection, so every GUC that
// feeds PlannerSettings — plus the four scan-method toggles, which travel
// through the catalog wrapper rather than through PlannerSettings — rides in
// the cache key (sessionPlannerFingerprint). A session with its own planner
// inputs reads and writes its own entry instead of bypassing the cache.
//
// costGUCValues maps every planner-input GUC to a value that differs from its
// boot default. Kept here rather than exported from the fingerprint so a GUC
// added to plannerSettingsFrom and forgotten in plannerCacheFingerprint fails
// a test instead of silently going uncovered: SET-ing it would leave the
// fingerprint unchanged.
var costGUCValues = map[string]string{
	"seq_page_cost": "1.5", "random_page_cost": "1.1",
	"cpu_tuple_cost": "0.02", "cpu_index_tuple_cost": "0.01",
	"cpu_operator_cost":   "0.005",
	"parallel_setup_cost": "500", "parallel_tuple_cost": "0.05",
	"effective_cache_size": "1GB", "work_mem": "4MB",
	"hash_mem_multiplier": "1.0",
	"enable_hashjoin":     "off", "enable_mergejoin": "off",
	"enable_nestloop": "off", "enable_memoize": "off",
	"enable_nestloop_index": "off",
	"enable_hashagg":        "off", "enable_presorted_aggregate": "off",
	"geqo": "off", "geqo_threshold": "20",
	"geqo_effort": "10", "geqo_pool_size": "100",
	"geqo_generations": "50", "geqo_selection_bias": "1.8",
	"geqo_seed": "0.5",
}

func TestSessionPlannerFingerprintDetectsEveryCostGUC(t *testing.T) {
	fresh := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	base := sessionPlannerFingerprint(fresh)

	for name, val := range costGUCValues {
		sess := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
		if _, _, ok := sess.Get(name); !ok {
			t.Fatalf("%s is not a registered GUC", name)
		}
		// SET to a non-default value. The question is "does this session plan
		// under different inputs", not "did it touch the GUC at all": SET-ing
		// a GUC back to its default value fingerprints identically, which is
		// correct — the plan would be identical too.
		if err := sess.Set(name, val, false); err != nil {
			t.Fatalf("SET %s = %s: %v", name, val, err)
		}
		if got := sessionPlannerFingerprint(sess); got == base {
			t.Errorf("SET %s = %s must change the plan-cache fingerprint", name, val)
		}
		if err := sess.Reset(name); err != nil {
			t.Fatalf("RESET %s: %v", name, err)
		}
		if got := sessionPlannerFingerprint(sess); got != base {
			t.Errorf("RESET %s must restore the plan-cache fingerprint", name)
		}
	}
}

// TestPlanCacheKeySeparatesPlannerContexts pins that the single cache key the
// guard sites use subsumes both planner-input families. Same SQL under
// different planner inputs must key apart; same inputs must key together.
func TestPlanCacheKeySeparatesPlannerContexts(t *testing.T) {
	const sql = "SELECT * FROM t WHERE a = 1"
	fresh := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	base := planCacheKey(sql, 7, sessionPlannerFingerprint(fresh))

	scan := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if err := scan.Set("enable_indexscan", "off", false); err != nil {
		t.Fatal(err)
	}
	if got := planCacheKey(sql, 7, sessionPlannerFingerprint(scan)); got == base {
		t.Error("scan-toggle session must key apart from a fresh session")
	}
	cost := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if err := cost.Set("random_page_cost", "1.1", false); err != nil {
		t.Fatal(err)
	}
	if got := planCacheKey(sql, 7, sessionPlannerFingerprint(cost)); got == base {
		t.Error("cost-GUC session must key apart from a fresh session")
	}
	// dbOid still separates: same fingerprint, different namespace, no share.
	if got := planCacheKey(sql, 8, sessionPlannerFingerprint(fresh)); got == base {
		t.Error("different dbOid must key apart under the same fingerprint")
	}
}

// TestSessionPlannerSettingsRoundTripsUnits pins take2 P2-02's unit
// conversions, which are the part of that item that fails SILENTLY.
//
// Both memory GUCs are registered UnitKB and read back as plain KB integers
// (work_mem "524288", effective_cache_size "4194304"), while the planner wants
// BYTES for one and BLOCKS for the other. A wrong conversion does not error —
// the plan simply comes out costed for a machine that does not exist.
//
// The assertion is against the PLANNER's own defaults rather than against
// hand-written numbers, so the two cannot drift.
func TestSessionPlannerSettingsRoundTripsUnits(t *testing.T) {
	fresh := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	got := sessionPlannerSettings(fresh)
	want := optimizer.DefaultPlannerSettings()

	if got.WorkMem != want.WorkMem {
		t.Errorf("work_mem: session round-trip gave %d bytes, planner default is %d "+
			"— the KB->bytes conversion is wrong", got.WorkMem, want.WorkMem)
	}
	if got.EffectiveCacheSize != want.EffectiveCacheSize {
		t.Errorf("effective_cache_size: session round-trip gave %.0f blocks, planner "+
			"default is %.0f — the KB->blocks conversion is wrong",
			got.EffectiveCacheSize, want.EffectiveCacheSize)
	}
	if got != want {
		t.Errorf("a fresh session must reproduce the planner defaults exactly:\n got %+v\nwant %+v", got, want)
	}
}

// TestSessionPlannerSettingsHonoursOverrides is the point of P2-02: a SET must
// reach the struct the planner costs with.
func TestSessionPlannerSettingsHonoursOverrides(t *testing.T) {
	sess := misc.NewSessionRegistry(misc.BuildDefaultRegistry())
	if err := sess.Set("random_page_cost", "1.1", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("work_mem", "4MB", false); err != nil {
		t.Fatal(err)
	}
	ps := sessionPlannerSettings(sess)
	if ps.RandomPageCost != 1.1 {
		t.Errorf("random_page_cost = %v, want 1.1", ps.RandomPageCost)
	}
	if want := int64(4 << 20); ps.WorkMem != want {
		t.Errorf("work_mem = %d bytes, want %d (4MB)", ps.WorkMem, want)
	}
	// And such a session must key apart in the shared cache, or the plan it
	// produces under these settings would be served to every other connection.
	if sessionPlannerFingerprint(sess) == sessionPlannerFingerprint(misc.NewSessionRegistry(misc.BuildDefaultRegistry())) {
		t.Error("a session with cost-GUC overrides must key apart in the shared plan cache")
	}
}

// TestSessionPlannerSettingsDegradesToDefaults guards the failure mode: a
// malformed or absent value must fall back to the planner default, never to a
// zero cost.
func TestSessionPlannerSettingsDegradesToDefaults(t *testing.T) {
	if got, want := sessionPlannerSettings(nil), optimizer.DefaultPlannerSettings(); got != want {
		t.Errorf("nil session gave %+v, want the planner defaults %+v", got, want)
	}
}

// TestPlanCacheInvalidatingStmt pins take2 P1-03b's second half. ANALYZE and
// VACUUM are planned as *optimizer.Utility, not *optimizer.DDL, so before this
// a session could ANALYZE a relation, re-run a cached query, and still get the
// plan chosen from the OLD statistics — the one case where the user has
// explicitly asked the planner to reconsider.
//
// Upstream reaches the same place by a different route: the pg_statistic and
// relstats writes emit relcache invalidation messages that plancache.c's
// ResetPlanCache picks up. goopg has no such bus, so the trigger is the
// statement kind, and that makes it worth pinning.
func TestPlanCacheInvalidatingStmt(t *testing.T) {
	for _, tc := range []struct {
		name string
		node optimizer.Node
		want bool
	}{
		{"DDL", &optimizer.DDL{}, true},
		{"ANALYZE", &optimizer.Utility{Stmt: &parser.AnalyzeStmt{}}, true},
		{"VACUUM", &optimizer.Utility{Stmt: &parser.VacuumStmt{}}, true},
		{"SET", &optimizer.Utility{Stmt: &parser.SetStmt{}}, false},
		{"SHOW", &optimizer.Utility{Stmt: &parser.ShowStmt{}}, false},
		{"PREPARE", &optimizer.Utility{Stmt: &parser.PrepareStmt{}}, false},
	} {
		if got := planCacheInvalidatingStmt(tc.node); got != tc.want {
			t.Errorf("%s: planCacheInvalidatingStmt = %v, want %v", tc.name, got, tc.want)
		}
	}
	if planCacheInvalidatingStmt(nil) {
		t.Error("a nil node must not invalidate")
	}
}
