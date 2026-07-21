package executor

// Stage 10 (D4.4/D4.5): the kvcache-backed sublink result stores and
// their shared WorkMem/4 budget. See subq_cache.go and the Context
// field comment for the two key families (scope-safe lowered keys vs
// scoped legacy keys).

import (
	"testing"
)

// budgetFixture: t1 rows all fail the left OR arm so the sublink runs
// once per outer row; t2.a is unique so the bare-column scalar shape
// (the CorrSubqHashMaps candidate) never errors with 21000.
func budgetFixture(t *testing.T, workMem int64) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE t1 (a int, b int)",
		"CREATE TABLE t2 (a int, b int)",
		"INSERT INTO t1 VALUES (1, 10), (2, 20), (3, 30)",
		"INSERT INTO t2 VALUES (1, 201), (3, 300)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	ctx.WorkMem = workMem
	return ctx, cleanup
}

const budgetProbe = "SELECT * FROM t1 WHERE t1.a = -999 OR t1.b < (SELECT sum(t2.b) FROM t2 WHERE t2.a = t1.a)"

// TestSubqCacheBudgetEvictsAndStaysCorrect: a small WorkMem forces the
// result store to evict under budget pressure; the query's rows must be
// identical to the unlimited run — the budget bounds memory, never
// correctness.
func TestSubqCacheBudgetEvictsAndStaysCorrect(t *testing.T) {
	// Unlimited reference run.
	refCtx, refCleanup := budgetFixture(t, 0)
	defer refCleanup()
	want := runQuery(t, refCtx, budgetProbe)

	// WorkMem 1000 → cache budget 250 bytes: roughly one result entry,
	// so three distinct correlation keys must evict.
	ctx, cleanup := budgetFixture(t, 1000)
	defer cleanup()
	got := runQuery(t, ctx, budgetProbe)

	if len(got) != len(want) {
		t.Fatalf("budgeted run returned %d rows, unlimited returned %d", len(got), len(want))
	}
	if ctx.subqBudget == nil {
		t.Fatal("budget never initialised — cache path not exercised")
	}
	evictions := ctx.subqCacheSafe.Evictions() + ctx.subqCacheScoped.Evictions()
	stored := ctx.subqCacheSafe.Len() + ctx.subqCacheScoped.Len()
	// Under a ~250-byte budget three ~170-byte entries cannot all be
	// resident: either eviction fired or entries were refused outright.
	if evictions == 0 && stored >= 3 {
		t.Fatalf("expected budget pressure (evictions or refused entries); evictions=0 stored=%d bytes=%d",
			stored, ctx.subqBudget.Used())
	}
	if lim := ctx.subqBudget.Limit(); lim != 250 {
		t.Fatalf("budget limit = %d, want WorkMem/4 = 250", lim)
	}
	if used := ctx.subqBudget.Used(); used > 250 {
		t.Fatalf("budget overrun: used=%d > limit=250", used)
	}
}

// TestSubqCacheUnlimitedWorkMemZero: WorkMem == 0 means unlimited
// (ch.06 D6.4 — explicitly not a silent fallback constant): nothing is
// evicted, and within one statement two outer rows sharing a
// correlation value are served from the cache via the projected key.
// (Cross-statement reuse is out of scope by design: keys embed the
// plan's expr pointer and a fresh statement plans fresh exprs — and a
// real statement gets a fresh Context anyway.)
func TestSubqCacheUnlimitedWorkMemZero(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE t1 (a int, b int)",
		"CREATE TABLE t2 (a int, b int)",
		// Two t1 rows share correlation value a=1 (different b, so a
		// full-row key provably could not hit — only the projected
		// param key can).
		"INSERT INTO t1 VALUES (1, 10), (1, 15), (3, 30)",
		"INSERT INTO t2 VALUES (1, 201), (3, 300)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	ctx.WorkMem = 0

	rows := runQuery(t, ctx, budgetProbe)
	if len(rows) != 3 { // 10<201, 15<201, 30<300
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if ctx.subqCacheSafe.Evictions()+ctx.subqCacheScoped.Evictions() != 0 {
		t.Fatal("unlimited budget must never evict")
	}
	var hits int64
	for _, s := range ctx.SubPlanStats {
		hits += s.CacheHits
	}
	if hits < 1 {
		t.Fatalf("shared correlation value should hit the projected-key cache, hits=%d", hits)
	}
	if !ctx.subqBudget.Unlimited() {
		t.Fatal("WorkMem==0 must map to an unlimited budget")
	}
}

// TestSubqScopedStoreClearsOnDepthChange pins the Stage-10 split of the
// historical SubqueryCacheScope guard: entries in the scoped store
// (unlowered/legacy key families) are dropped when the OuterRows depth
// changes — the guard that masks stale hits for keys that do not
// describe their full scope — while scope-safe (lowered-key) entries
// survive, because their keys embed every value they depend on.
func TestSubqScopedStoreClearsOnDepthChange(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	vals := []Datum{NewIntDatum(42)}
	ctx.subqCachePut("legacy-key", true, vals)
	ctx.subqCachePut("lowered-key", false, vals)

	if _, ok := ctx.subqCacheGet("legacy-key", true); !ok {
		t.Fatal("scoped entry should hit at the depth it was stored")
	}

	// Enter a nested subquery scope.
	ctx.OuterRows = append(ctx.OuterRows, Row{NewIntDatum(1)})
	if _, ok := ctx.subqCacheGet("legacy-key", true); ok {
		t.Fatal("scoped entry must be cleared on OuterRows depth change")
	}
	if _, ok := ctx.subqCacheGet("lowered-key", false); !ok {
		t.Fatal("scope-safe entry must survive OuterRows depth change")
	}

	// Returning to the original depth does not resurrect the entry.
	ctx.OuterRows = ctx.OuterRows[:0]
	if _, ok := ctx.subqCacheGet("legacy-key", true); ok {
		t.Fatal("cleared scoped entry must stay gone after depth returns")
	}
}

// TestCorrSubqHashMapBudget: the whole-inner-table hash map draws on
// the shared budget. Over budget → the map is not retained and the
// statement falls back to per-row execution (correct, uncached); with
// an unlimited budget the map builds and serves later rows.
func TestCorrSubqHashMapBudget(t *testing.T) {
	// Bare-column correlated scalar with NO index on t2: the
	// Project(Filter(SeqScan, col = param)) hash-map shape.
	probe := "SELECT * FROM t1 WHERE t1.a = -999 OR t1.b < (SELECT t2.b FROM t2 WHERE t2.a = t1.a)"

	// Unlimited: the map is built once and answers later rows.
	ctxU, cleanupU := budgetFixture(t, 0)
	defer cleanupU()
	wantRows := runQuery(t, ctxU, probe)
	if len(ctxU.CorrSubqHashMaps) != 1 {
		t.Fatalf("unlimited: hash map not built (len=%d)", len(ctxU.CorrSubqHashMaps))
	}

	// WorkMem 4 → budget 1 byte: the pre-build reservation fails, the
	// map is never retained, rows still match.
	ctxB, cleanupB := budgetFixture(t, 4)
	defer cleanupB()
	gotRows := runQuery(t, ctxB, probe)
	if len(ctxB.CorrSubqHashMaps) != 0 {
		t.Fatalf("budgeted: hash map should have been skipped (len=%d)", len(ctxB.CorrSubqHashMaps))
	}
	if len(gotRows) != len(wantRows) {
		t.Fatalf("budgeted fallback changed results: %d rows vs %d", len(gotRows), len(wantRows))
	}
}
