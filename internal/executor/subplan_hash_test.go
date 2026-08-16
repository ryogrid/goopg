package executor

// Stage 11 (S3, design bundle D4.3): hashed execution for uncorrelated
// IN / NOT IN sublinks. Every test here runs its query through BOTH the
// hashed probe and the linear reference path (SetHashedSubPlanEnabled)
// and asserts identical results — the linear path is the correctness
// oracle. The unnest pass is suppressed around each query so the
// sublink actually survives as a SubPlan (with unnesting on, an
// uncorrelated NOT IN becomes a NullAware anti join and the probe never
// runs); the matrix (subquery_semantics_test.go) already covers the
// unnested twins.

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

func hashFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE ht1 (a int, b int)",
		"CREATE TABLE ht2 (b int)",
		"CREATE TABLE ht3 (s text)",
		// ht1: operands 10 (match), 99 (miss), NULL (operand-NULL row).
		"INSERT INTO ht1 VALUES (1, 10), (2, 99), (3, NULL)",
		// ht2: one NULL in the inner set — the three-valued case.
		"INSERT INTO ht2 VALUES (10), (20), (NULL)",
		"INSERT INTO ht3 VALUES ('10'), ('20')",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	return ctx, cleanup
}

// runBothHashPaths runs sql with the hashed probe on and off (unnesting
// suppressed both times) and asserts the row sets are identical.
// Returns the hashed-path rows.
func runBothHashPaths(t *testing.T, ctx *Context, sql string) []Row {
	t.Helper()
	optimizer.SetSubqueryUnnestEnabled(false)
	defer optimizer.SetSubqueryUnnestEnabled(true)

	SetHashedSubPlanEnabled(true)
	defer SetHashedSubPlanEnabled(true) // package default
	hashed := runQuery(t, ctx, sql)

	SetHashedSubPlanEnabled(false)
	linear := runQuery(t, ctx, sql)

	if len(hashed) != len(linear) {
		t.Fatalf("%s: hashed path returned %d rows, linear %d", sql, len(hashed), len(linear))
	}
	for i := range hashed {
		for j := range hashed[i] {
			hk, lk := datumKey(hashed[i][j]), datumKey(linear[i][j])
			if hk != lk {
				t.Fatalf("%s: row %d col %d differs: hashed %s linear %s", sql, i, j, hk, lk)
			}
		}
	}
	return hashed
}

// TestHashedInTruthTable pins the probe's truth table against the
// linear oracle: match → TRUE; miss with an inner NULL → NULL (row
// filtered, not FALSE); miss without NULLs → FALSE; NOT IN inverts
// TRUE/FALSE and leaves NULL alone; empty inner stays vacuous
// (Stage-5 semantics, served by the linear path — the probe declines
// empty sets).
func TestHashedInTruthTable(t *testing.T) {
	ctx, cleanup := hashFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want int
	}{
		// inner {10,20,NULL}: a=1(b=10) matches → kept; a=2(b=99) miss
		// but inner has NULL → NULL → filtered; a=3(b NULL) non-empty
		// inner → NULL → filtered.
		{"SELECT a FROM ht1 WHERE b IN (SELECT b FROM ht2) ORDER BY a", 1},
		// NOT IN over a NULL-containing inner can never be TRUE.
		{"SELECT a FROM ht1 WHERE b NOT IN (SELECT b FROM ht2) ORDER BY a", 0},
		// NULL-free inner {10,20}: miss → FALSE, so NOT IN keeps a=2;
		// operand-NULL row stays NULL → filtered.
		{"SELECT a FROM ht1 WHERE b IN (SELECT b FROM ht2 WHERE b IS NOT NULL) ORDER BY a", 1},
		{"SELECT a FROM ht1 WHERE b NOT IN (SELECT b FROM ht2 WHERE b IS NOT NULL) ORDER BY a", 1},
		// Empty inner: IN → no rows; NOT IN → every row, including the
		// NULL operand (vacuous quantification — Stage 5).
		{"SELECT a FROM ht1 WHERE b IN (SELECT b FROM ht2 WHERE b > 1000) ORDER BY a", 0},
		{"SELECT a FROM ht1 WHERE b NOT IN (SELECT b FROM ht2 WHERE b > 1000) ORDER BY a", 3},
	}
	for _, c := range cases {
		got := runBothHashPaths(t, ctx, c.sql)
		if len(got) != c.want {
			t.Errorf("%s: got %d rows, want %d", c.sql, len(got), c.want)
		}
	}
}

// TestHashedInProbeActuallyFires asserts the hash entry lands in the
// scoped store alongside the value slice — i.e. the truth table above
// really exercised the probe rather than silently falling back.
func TestHashedInProbeActuallyFires(t *testing.T) {
	ctx, cleanup := hashFixture(t)
	defer cleanup()

	optimizer.SetSubqueryUnnestEnabled(false)
	defer optimizer.SetSubqueryUnnestEnabled(true)
	SetHashedSubPlanEnabled(true)

	runQuery(t, ctx, "SELECT a FROM ht1 WHERE b IN (SELECT b FROM ht2) ORDER BY a")
	// Two entries under the constant key stem: the []Datum slice and
	// the derived subPlanHash.
	if n := ctx.subqCacheScoped.Len(); n < 2 {
		t.Fatalf("scoped store holds %d entries; want >= 2 (values + hash) — probe never built its table", n)
	}
}

// TestHashedInAnyAllFormsUnaffected: non-equality quantified forms
// cannot hash and must stay on (correct) linear evaluation.
func TestHashedInAnyAllFormsUnaffected(t *testing.T) {
	ctx, cleanup := hashFixture(t)
	defer cleanup()
	// <> ALL over the NULL-free inner: a=2 (99 differs from all) kept.
	got := runBothHashPaths(t, ctx,
		"SELECT a FROM ht1 WHERE b <> ALL (SELECT b FROM ht2 WHERE b IS NOT NULL) ORDER BY a")
	if len(got) != 1 || datumKey(got[0][0]) != datumKey(NewIntDatum(2)) {
		t.Fatalf("<> ALL: got %d rows, want exactly a=2", len(got))
	}
}

// TestHashedInMixedKindFallsBack: an int operand probed against a text
// inner set must take the linear path, where compareEq's int↔string
// coercion applies (`10 = '10'` is TRUE). If the hash answered, the
// family mismatch would wrongly report a miss — the probe must decline
// instead.
func TestHashedInMixedKindFallsBack(t *testing.T) {
	ctx, cleanup := hashFixture(t)
	defer cleanup()
	got := runBothHashPaths(t, ctx,
		"SELECT a FROM ht1 WHERE b IN (SELECT s FROM ht3) ORDER BY a")
	// b=10 coerces equal to '10' → row a=1 kept; 99 misses (no NULLs
	// in ht3) → dropped; NULL operand over non-empty inner → NULL.
	if len(got) != 1 || datumKey(got[0][0]) != datumKey(NewIntDatum(1)) {
		t.Fatalf("int-vs-text IN: got %d rows, want exactly a=1 (coercion via linear path)", len(got))
	}
}

// TestHashedInBudgetPressure: a tiny WorkMem keeps the hash (and even
// the value slice) from being retained; results must be identical.
func TestHashedInBudgetPressure(t *testing.T) {
	ctx, cleanup := hashFixture(t)
	defer cleanup()
	ctx.WorkMem = 64 // budget = 16 bytes: nothing fits
	got := runBothHashPaths(t, ctx,
		"SELECT a FROM ht1 WHERE b IN (SELECT b FROM ht2) ORDER BY a")
	if len(got) != 1 {
		t.Fatalf("budget-squeezed IN: got %d rows, want 1", len(got))
	}
	if used := ctx.subqBudget.Used(); used > 16 {
		t.Fatalf("budget overrun: used=%d > 16", used)
	}
}

// TestScopedCacheDepthConsistency pins the Stage-11 depth fix: cache
// Get and Put must both run at the HOST OuterRows depth. Before the
// fix, collectInValues Put ran inside its own pushed scope, so the
// scoped store's clear-on-depth-change guard wiped the entry before
// the next host-depth Get — every scoped non-correlated sublink
// re-executed once per outer row (the M0058-0001 pathology returning
// through the Stage-10 store split).
func TestScopedCacheDepthConsistency(t *testing.T) {
	ctx, cleanup := hashFixture(t)
	defer cleanup()

	optimizer.SetSubqueryUnnestEnabled(false)
	defer optimizer.SetSubqueryUnnestEnabled(true)

	// One statement, TWO scoped non-correlated sublinks (IN + EXISTS):
	// they share the scoped store and previously ping-pong-cleared it.
	runQuery(t, ctx, "SELECT a FROM ht1 WHERE b IN (SELECT b FROM ht2)"+
		" AND EXISTS (SELECT 1 FROM ht3) ORDER BY a")

	var inStat, existsStat *SubPlanSiteStats
	for e, s := range ctx.SubPlanStats {
		switch e.(type) {
		case *optimizer.InExpr:
			inStat = s
		case *optimizer.ExistsExpr:
			existsStat = s
		}
	}
	if inStat == nil || existsStat == nil {
		t.Fatalf("expected stats for both sublinks; got IN=%v EXISTS=%v", inStat, existsStat)
	}
	// ht1 has 3 rows: the sublink executes ONCE, every later row hits.
	if inStat.CacheMisses != 1 {
		t.Errorf("IN sublink: CacheMisses = %d, want 1 (one execution per statement)", inStat.CacheMisses)
	}
	if existsStat.CacheMisses != 1 {
		t.Errorf("EXISTS sublink: CacheMisses = %d, want 1 (one execution per statement)", existsStat.CacheMisses)
	}
	if inStat.CacheHits == 0 || existsStat.CacheHits == 0 {
		t.Errorf("expected cache hits on later rows; IN hits=%d EXISTS hits=%d",
			inStat.CacheHits, existsStat.CacheHits)
	}
}

// TestBuildSubPlanHashClassification unit-tests the family rules the
// probe's correctness rests on.
func TestBuildSubPlanHashClassification(t *testing.T) {
	// Int + scale-normalised numeric share the numeric family and one
	// canonical key (1 == 1.0).
	h := buildSubPlanHash([]Datum{NewIntDatum(1), NewNumericInt64Datum(10, 1)})
	if h.family != hashFamNumeric {
		t.Fatalf("int+numeric: family = %v, want hashFamNumeric", h.family)
	}
	if len(h.set) != 1 {
		t.Fatalf("1 and 1.0 should canonicalise to one key; set has %d", len(h.set))
	}
	// Mixed families → sentinel.
	if h := buildSubPlanHash([]Datum{NewIntDatum(1), NewStringDatum("x")}); h.family != hashFamNone {
		t.Fatalf("mixed int+string: family = %v, want hashFamNone", h.family)
	}
	// NULLs tracked, not stored.
	h = buildSubPlanHash([]Datum{NewIntDatum(1), NullDatum})
	if !h.hasNull || len(h.set) != 1 {
		t.Fatalf("null tracking: hasNull=%v set=%d", h.hasNull, len(h.set))
	}
	// All-NULL set → sentinel (linear path handles).
	if h := buildSubPlanHash([]Datum{NullDatum, NullDatum}); h.family != hashFamNone {
		t.Fatalf("all-NULL: family = %v, want hashFamNone", h.family)
	}
	// Kill-switch / probe declines are covered end-to-end above.
}
