package executor

// Subquery semantics matrix — gate V1 of the correlated-subquery-planning
// design bundle (docs/design/correlated-subquery-planning/07-verification-and-measurement.md).
//
// Every row pins a PostgreSQL 18.3-mandated behaviour of EXISTS / IN / NOT IN /
// ANY / ALL / scalar sublinks. The expectations below are PG's, not goopg's:
// where goopg currently disagrees, the exact wrong answer is pinned in a `known`
// entry together with the reason and the stage expected to fix it, so the test
// fails loudly — rather than silently going green — the moment that behaviour
// changes.
//
// Re-verify any expectation against the real oracle with:
//
//	scripts/pg-oracle-diff.sh --auto-start internal/executor/testdata/subquery_semantics.sql
//
// The fixture mirrors the 2026-07-20 review probes
// (docs/design/correlated-subquery-planning/evidence/review-probes-20260720.md)
// so the rows here are directly comparable with the archived measurements:
//
//	t1(a, b) = (1,10) (2,20) (3,30) (4,NULL)
//	t2(a, b) = (1,10) (1,11) (3,NULL)
//
// Every case runs TWICE — once with the sublink pull-up pass enabled (the
// default) and once with `planner.SetSubqueryUnnestEnabled(false)` — because
// the count-bug and NULL rows are precisely where the decorrelated plan and the
// SubPlan path can silently diverge. The two paths are pinned separately, and
// which one is wrong is itself diagnostic: a divergence on the unnested path
// only is a bad pull-up rewrite (planner), while a divergence on both is an
// evaluation bug (executor).
//
// This suite lives in `internal/executor` rather than `internal/testport` (which
// the design doc suggested) because the repo's existing "run SQL, compare rows"
// harness here is in-process (newDDLFixture + runDDL + runQueryWithErr): it needs
// no server, runs inside the mandatory units gate, and therefore actually guards
// every stage. The testport alternative needs a live server plus psql and is
// build-tagged out of the default suite.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// known pins a divergence that exists at HEAD: the rows goopg actually returns
// instead of the PG-mandated `want`, plus why. Pinning the wrong answer exactly
// means the test fails loudly — rather than silently going green — the moment
// the behaviour changes, so a fix cannot land unnoticed and a regression cannot
// masquerade as the known bug.
type known struct {
	rows []string
	why  string
	// fixedIn names the stage expected to make this path match PG.
	fixedIn string
}

// semanticsCase is one row of the V1 matrix.
//
// Each case runs on both plan paths, and the two paths are pinned separately:
// a *planner* bug (a bad pull-up rewrite) shows up only on the unnested path,
// while an *executor* bug shows up on both. Which of the two is wrong is itself
// diagnostic, so the struct records them independently rather than collapsing
// them into one "expected failure" flag.
type semanticsCase struct {
	// id is the matrix row (M1..M19) this case belongs to.
	id string
	// desc names the specific behaviour being pinned.
	desc string
	// sql is the probe.
	sql string
	// want is the PG 18.3 result, one string per row, in result order.
	want []string
	// wantErrCode, when non-empty, means PG raises this SQLSTATE and goopg
	// must too; `want` is then ignored.
	wantErrCode string
	// badUnnested / badSubplan pin a HEAD divergence on that path. nil means
	// the path already matches PG.
	badUnnested *known
	badSubplan  *known
	// skip, when non-empty, is the reason the probe cannot be executed yet.
	skip string
}

// renderRows flattens result rows to comparable strings, rendering NULL
// explicitly so a NULL-vs-missing-row difference cannot hide.
func renderRows(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		parts := make([]string, len(r))
		for i, d := range r {
			if d.IsNull() {
				parts[i] = "NULL"
				continue
			}
			parts[i] = datumTestString(d)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	return out
}

// datumTestString renders a Datum using the executor's own value-text
// rendering, so comparisons match what a client would receive.
func datumTestString(d Datum) string {
	if d.IsNull() {
		return "NULL"
	}
	return string(d.AppendValueText(nil))
}

// newSemanticsFixture builds the t1/t2 fixture shared by every row.
func newSemanticsFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE t1 (a int, b int)",
		"CREATE TABLE t2 (a int, b int)",
		"INSERT INTO t1 VALUES (1, 10)",
		"INSERT INTO t1 VALUES (2, 20)",
		"INSERT INTO t1 VALUES (3, 30)",
		"INSERT INTO t1 VALUES (4, NULL)",
		"INSERT INTO t2 VALUES (1, 10)",
		"INSERT INTO t2 VALUES (1, 11)",
		"INSERT INTO t2 VALUES (3, NULL)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

// semanticsCases is the V1 matrix. Ordered by matrix row id.
func semanticsCases() []semanticsCase {
	return []semanticsCase{
		// ---- M1: IN NULL propagation -------------------------------------
		{
			id:   "M1",
			desc: "IN with a NULL in the inner set: no match yields NULL, not false",
			sql:  "SELECT a FROM t1 WHERE b IN (SELECT b FROM t2) ORDER BY a",
			want: []string{"1"},
		},
		{
			id:   "M1",
			desc: "IN over an empty inner set is FALSE (never NULL), even for a NULL operand",
			sql:  "SELECT a FROM t1 WHERE b IN (SELECT b FROM t2 WHERE t2.a = 99) ORDER BY a",
			want: nil,
		},
		{
			id:   "M1",
			desc: "IN with a NULL operand and a non-NULL inner set is NULL",
			sql:  "SELECT a FROM t1 WHERE b IN (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a",
			want: []string{"1"},
		},

		// ---- M2: NOT IN NULL propagation ---------------------------------
		{
			id:   "M2",
			desc: "NOT IN can never be TRUE once the inner set contains a NULL",
			sql:  "SELECT a FROM t1 WHERE b NOT IN (SELECT b FROM t2) ORDER BY a",
			want: nil,
		},
		{
			id:   "M2",
			desc: "NOT IN over a NULL-free inner set: NULL operand still yields NULL",
			sql:  "SELECT a FROM t1 WHERE b NOT IN (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a",
			want: []string{"2", "3"},
		},
		{
			id:   "M2",
			desc: "correlated NOT IN, NULL operand x empty inner: NULL NOT IN (empty) is TRUE (vacuous)",
			sql:  "SELECT a FROM t1 WHERE b NOT IN (SELECT b FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			// Fixed in Stage 5 (S1b): evalInExpr used to short-circuit to
			// NULL on a NULL operand before consulting the inner set,
			// making the vacuous-truth case (`NULL NOT IN (∅)` = TRUE)
			// unreachable — both paths returned {2}.
			want: []string{"2", "4"},
		},

		// ---- M3: EXISTS / NOT EXISTS with NULL correlation ---------------
		{
			id:   "M3",
			desc: "EXISTS on an equijoin correlation",
			sql:  "SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"1", "3"},
		},
		{
			id:   "M3",
			desc: "NOT EXISTS is the exact complement",
			sql:  "SELECT a FROM t1 WHERE NOT EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"2", "4"},
		},
		{
			id:   "M3",
			desc: "a NULL correlation value never matches, so EXISTS is FALSE (not NULL)",
			sql:  "SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.b = t1.b) ORDER BY a",
			want: []string{"1"},
		},

		// ---- M4: scalar subquery cardinality -----------------------------
		{
			id:   "M4",
			desc: "a scalar subquery returning zero rows is NULL, not an error",
			sql:  "SELECT a FROM t1 WHERE (SELECT b FROM t2 WHERE t2.a = 99) IS NULL ORDER BY a",
			want: []string{"1", "2", "3", "4"},
		},
		{
			id:   "M4",
			desc: "a scalar subquery returning exactly one row yields that value",
			sql:  "SELECT (SELECT a FROM t2 WHERE b = 11)",
			want: []string{"1"},
		},
		{
			id:          "M4",
			desc:        "a scalar subquery returning more than one row raises 21000",
			sql:         "SELECT (SELECT a FROM t2)",
			wantErrCode: "21000",
		},

		// ---- M5: the count bug -------------------------------------------
		{
			id:   "M5",
			desc: "count(*) over an empty correlated group is 0, so unmatched outer rows survive",
			sql:  "SELECT a FROM t1 WHERE t1.a > (SELECT count(*) FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"2", "3", "4"},
		},
		{
			id:   "M5",
			desc: "count(col) behaves identically to count(*) here — the Star gate must not be what saves us",
			sql:  "SELECT a FROM t1 WHERE t1.a > (SELECT count(b) FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"2", "3", "4"},
			// Was a planner-only bug: canUnnestSubquery excluded count(*) via
			// the Star check but let count(col) through, and the INNER-join
			// rewrite dropped outer rows whose correlated group is empty.
			// Fixed in Stage 4 by the NULL-on-empty aggregate whitelist
			// (nullOnEmptyAggregates), which keeps count() on the SubPlan path.
		},
		{
			id:   "M5",
			desc: "sum over an empty group is NULL, so the comparison filters the row out",
			sql:  "SELECT a FROM t1 WHERE t1.a > (SELECT sum(b) FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: nil,
		},
		{
			id:   "M5",
			desc: "COALESCE-wrapped aggregate restores the count-bug shape through a Project",
			sql:  "SELECT a FROM t1 WHERE t1.a > (SELECT COALESCE(sum(b), 0) FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"2", "3", "4"},
			// Fixed in Stage 4: nullPreservingScalarTarget rejects the Project
			// wrapper because COALESCE turns the aggregate's NULL into 0, which
			// would let the INNER-join rewrite drop unmatched outer rows.
		},

		// ---- M6: OR-position sublinks must NOT decorrelate ---------------
		{
			id:   "M6",
			desc: "EXISTS under OR keeps both arms alive",
			sql:  "SELECT a FROM t1 WHERE a = 2 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"1", "2", "3"},
		},
		{
			id:   "M6",
			desc: "scalar sublink under OR keeps the other arm alive",
			sql:  "SELECT a FROM t1 WHERE a = 2 OR b > (SELECT sum(x.b) FROM t2 x WHERE x.a = t1.a) ORDER BY a",
			want: []string{"2"},
			// Fixed in Stage 4: subqueryANDReachable refuses the pull-up when
			// the sublink sits under an OR, so the `a = 2` arm survives.
		},
		{
			id:   "M6",
			desc: "IN under OR keeps both arms alive",
			sql:  "SELECT a FROM t1 WHERE a = 2 OR b IN (SELECT b FROM t2) ORDER BY a",
			want: []string{"1", "2"},
			// Was the F1 planner infinite loop (evidence/review-probes-20260720.md
			// §1): the IN was found under the OR but never removed from the
			// predicate, so the driver loop wrapped a join per iteration
			// forever. Stage 4's inExprTopConjunct keeps it a SubPlan.
		},
		{
			id:   "M6",
			desc: "NOT-wrapped IN pulls up as an anti join, not as a semi join",
			sql:  "SELECT a FROM t1 WHERE NOT (b IN (SELECT b FROM t2 WHERE b IS NOT NULL)) ORDER BY a",
			want: []string{"2", "3"},
			// Also an F1 hang before Stage 4. `NOT (x IN …)` reaches the
			// planner as UnaryOp(NOT, InExpr) rather than InExpr{Negated},
			// so it is not itself a top-level conjunct. inExprTopConjunct
			// accepts the single NOT wrapper and flips the join to Anti
			// (NullAware), which is what `x NOT IN …` means.
		},

		// ---- M7: sublinks and outer joins --------------------------------
		{
			id:   "M7",
			desc: "a sublink in WHERE above a LEFT JOIN applies post-join and preserves join rows",
			sql: "SELECT t1.a FROM t1 LEFT JOIN t2 ON t1.a = t2.a " +
				"WHERE EXISTS (SELECT 1 FROM t2 x WHERE x.a = t1.a) ORDER BY t1.a",
			want: []string{"1", "1", "3"},
		},

		// ---- M8: multi-level correlation ---------------------------------
		{
			id:   "M8",
			desc: "a Level-2 outer reference resolves to the outermost query",
			sql: "SELECT a FROM t1 WHERE EXISTS (" +
				"SELECT 1 FROM t2 WHERE t2.a = t1.a AND EXISTS (" +
				"SELECT 1 FROM t2 y WHERE y.b = t1.b)) ORDER BY a",
			want: []string{"1"},
		},

		// ---- M9: correlated IN operand safety ----------------------------
		{
			id:   "M9",
			desc: "correlated IN: empty inner is FALSE even when the operand is NULL",
			sql:  "SELECT a FROM t1 WHERE t1.b IN (SELECT y.b FROM t2 y WHERE y.a = t1.a) ORDER BY a",
			want: []string{"1"},
		},

		// ---- M10: = ANY / <> ALL forms -----------------------------------
		{
			id:   "M10",
			desc: "= ANY (subquery) follows the IN NULL algebra",
			sql:  "SELECT a FROM t1 WHERE b = ANY (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a",
			want: []string{"1"},
		},
		{
			id:   "M10",
			desc: "<> ALL (subquery) follows the NOT IN NULL algebra",
			sql:  "SELECT a FROM t1 WHERE b <> ALL (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a",
			want: []string{"2", "3"},
			// Found in Stage 3, fixed in Stage 4: the pull-up turned `<> ALL`
			// into a SEMI join instead of an ANTI join, returning the exact
			// complement of the correct answer. inExprIsPlainEquality now
			// keeps every quantified non-equality form on the SubPlan path.
		},

		// ---- M11: non-correlated sublink caching -------------------------
		{
			id:   "M11",
			desc: "a non-correlated sublink yields one identical value for every outer row",
			sql:  "SELECT a FROM t1 WHERE a > (SELECT min(a) FROM t2) ORDER BY a",
			want: []string{"2", "3", "4"},
		},

		// ---- M12: EXISTS bodies ------------------------------------------
		{
			id:   "M12",
			desc: "LIMIT inside EXISTS does not change its truth value",
			sql:  "SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a LIMIT 1) ORDER BY a",
			want: []string{"1", "3"},
			// Found in Stage 3, fixed in Stage 4: the body's LIMIT survived the
			// pull-up and became a global LIMIT on the semi-join build side, so
			// only one correlation key could ever match. Following upstream's
			// simplify_EXISTS_query, a positive constant LIMIT is now stripped
			// before the rewrite (stripPositiveConstLimits) — the pull-up still
			// happens, which keeps the common EXISTS(... LIMIT 1) idiom fast.
		},
		{
			id:   "M12",
			desc: "DISTINCT inside EXISTS does not change its truth value",
			sql:  "SELECT a FROM t1 WHERE EXISTS (SELECT DISTINCT b FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"1", "3"},
		},
		{
			id:   "M12",
			desc: "EXISTS over an aggregate body is always TRUE — the aggregate always returns a row",
			sql:  "SELECT a FROM t1 WHERE EXISTS (SELECT count(*) FROM t2 WHERE t2.a = t1.a) ORDER BY a",
			want: []string{"1", "2", "3", "4"},
			// Found in Stage 3, fixed in Stage 4: an ungrouped aggregate body
			// always yields exactly one row, so EXISTS over it is a tautology,
			// but the pull-up built a semi join on the aggregate's output and
			// turned it into a selective filter. existsBodySafeForPullup now
			// refuses aggregate bodies, matching simplify_EXISTS_query.
		},

		// ---- M13: volatile / side-effecting bodies -----------------------
		{
			id:   "M13",
			desc: "a volatile function inside EXISTS does not change the result set",
			sql: "SELECT a FROM t1 WHERE EXISTS (" +
				"SELECT 1 FROM t2 WHERE t2.a = t1.a AND random() < 2) ORDER BY a",
			want: []string{"1", "3"},
		},

		// ---- M14: non-equi-only correlation ------------------------------
		{
			id:   "M14",
			desc: "EXISTS whose correlation is a range predicate only (zero equijoin pairs)",
			sql:  "SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 y WHERE y.b > t1.b) ORDER BY a",
			want: []string{"1"},
		},

		// ---- M15: nested sublink inside an EXISTS body -------------------
		{
			id:   "M15",
			desc: "IN nested inside an EXISTS body (the D3.3 shape) resolves both correlations",
			sql: "SELECT a FROM t1 WHERE EXISTS (" +
				"SELECT 1 FROM t2 z WHERE z.a = t1.a AND z.b IN (" +
				"SELECT y.b FROM t2 y WHERE y.a = t1.a)) ORDER BY a",
			want: []string{"1"},
		},

		// ---- M16: residual lifting x aggregate safety --------------------
		{
			id:   "M16",
			desc: "a non-equi outer conjunct alongside the equijoin correlation (residual-lifting shape)",
			sql: "SELECT a FROM t1 WHERE t1.b >= (" +
				"SELECT min(y.b) FROM t2 y WHERE y.a = t1.a AND y.b <= t1.b) ORDER BY a",
			want: []string{"1"},
		},

		// ---- M17: duplicate outer rows x residual scalar -----------------
		{
			id: "M17",
			desc: "fully-duplicate outer rows keep their multiplicity through the " +
				"aggregate-above-join residual rewrite",
			// UNION ALL doubles every t1 row, so the outer side feeds two
			// IDENTICAL (1,10) rows into the rewrite. Grouping the joined
			// rows by the outer columns alone would collapse them into one;
			// the per-row ordinal tag must keep both. Both paths must emit
			// the row twice.
			sql: "SELECT x.a FROM (SELECT a, b FROM t1 UNION ALL SELECT a, b FROM t1) x " +
				"WHERE x.b >= (SELECT min(y.b) FROM t2 y WHERE y.a = x.a AND y.b <= x.b) ORDER BY x.a",
			want: []string{"1", "1"},
		},

		// ---- M18: correlated NOT IN with residual (stays SubPlan) --------
		{
			id: "M18",
			desc: "correlated NOT IN with a non-equi residual keeps three-valued " +
				"NULL semantics (deliberately NOT lifted; planner pin: TestNotInResidualStaysSubPlan)",
			// Rows a=1..3 see a NULL in their inner set (t2 has (3,NULL)),
			// so `a NOT IN (...)` is UNKNOWN — filtered, not returned.
			// Only a=4, whose inner set is empty, passes. An anti join
			// produced by naive residual lifting would return 2,3,4.
			sql:  "SELECT a FROM t1 WHERE a NOT IN (SELECT y.b FROM t2 y WHERE y.a >= t1.a) ORDER BY a",
			want: []string{"4"},
		},

		// ---- M19: non-aggregate scalar with residual: 21000 agreement ----
		{
			id: "M19",
			desc: "a NON-aggregate correlated scalar with a residual raises 21000 on " +
				"both paths (residual lifting must not extend past the aggregate whitelist)",
			// For t1 row (1,10) the sublink yields two rows (10 and 11):
			// PG raises 21000 "more than one row returned by a subquery
			// used as an expression". The aggregate-above-join rewrite is
			// gated on the NULL-on-empty aggregate shape, so this shape
			// must stay a SubPlan and keep the runtime error on both paths.
			sql: "SELECT a FROM t1 WHERE t1.b = (" +
				"SELECT y.b FROM t2 y WHERE y.a = t1.a AND y.b >= t1.b) ORDER BY a",
			wantErrCode: "21000",
		},

		// ---- M20-M22: D3.3 nested-sublink tolerance (S4b) ----------------
		// M15 above pins the ESCAPING shape (its nested IN references t1,
		// a Level-2 ref seen from inside — it must stay a SubPlan). These
		// rows pin the complements the stage introduces.
		{
			id: "M20",
			desc: "nested sublink correlated only to the EXISTS body (Level 1) rides " +
				"into the semi-join build side as a SubPlan; results unchanged",
			// z=(1,10): nested set {10,11} contains 10 -> EXISTS true for
			// t1.a=1. z=(3,NULL): NULL IN {NULL} is NULL -> no. Others: no z.
			sql: "SELECT a FROM t1 WHERE EXISTS (" +
				"SELECT 1 FROM t2 z WHERE z.a = t1.a AND z.b IN (" +
				"SELECT y.b FROM t2 y WHERE y.a = z.a)) ORDER BY a",
			want: []string{"1"},
		},
		{
			id: "M21",
			desc: "correlation hiding inside a nested IN's OPERAND (host-scope position " +
				"the shallow walkers treat as a leaf) keeps the EXISTS a SubPlan and correct",
			// exists(z.a=t1.a) AND t1.b IN {10,11,NULL}: a=1 (b=10) yes;
			// a=3 (b=30 vs NULL-bearing set) NULL -> no; a=2,4: no z.
			sql: "SELECT a FROM t1 WHERE EXISTS (" +
				"SELECT 1 FROM t2 z WHERE z.a = t1.a AND t1.b IN (" +
				"SELECT y.b FROM t2 y)) ORDER BY a",
			want: []string{"1"},
		},
		{
			id: "M22",
			desc: "zero-equijoin EXISTS (NL semi) whose build side carries a nested " +
				"SubPlan evaluates the SubPlan correctly during the build drain",
			// Qualifying z rows need z.a IN {y.a : y.b = z.b}: z=(1,10) and
			// (1,11) qualify (their own a=1 is in the set); z=(3,NULL) has
			// an empty nested set -> false. Then EXISTS(z.b > t1.b) over
			// b in {10,11}: t1.a=1 (b=10 < 11) yes; a=2,3 no; a=4 (NULL) no.
			sql: "SELECT a FROM t1 WHERE EXISTS (" +
				"SELECT 1 FROM t2 z WHERE z.b > t1.b AND z.a IN (" +
				"SELECT y.a FROM t2 y WHERE y.b = z.b)) ORDER BY a",
			want: []string{"1"},
		},
	}
}

// TestSubquerySemanticsMatrix is gate V1. Each case runs on both plan paths.
func TestSubquerySemanticsMatrix(t *testing.T) {
	for _, tc := range semanticsCases() {
		tc := tc
		name := tc.id + "/" + strings.ReplaceAll(tc.desc, " ", "_")
		t.Run(name, func(t *testing.T) {
			if tc.skip != "" {
				t.Skip(tc.skip)
			}
			for _, unnest := range []bool{true, false} {
				path := "unnested"
				bad := tc.badUnnested
				if !unnest {
					path = "subplan"
					bad = tc.badSubplan
				}
				t.Run(path, func(t *testing.T) {
					ctx, cleanup := newSemanticsFixture(t)
					defer cleanup()

					planner.SetSubqueryUnnestEnabled(unnest)
					defer planner.SetSubqueryUnnestEnabled(true)

					rows, err := runQueryWithErr(ctx, tc.sql)
					if tc.wantErrCode != "" {
						if err == nil {
							t.Fatalf("%s: expected SQLSTATE %s, got %d rows", tc.sql, tc.wantErrCode, len(rows))
						}
						if !strings.Contains(err.Error(), tc.wantErrCode) {
							t.Fatalf("%s: expected SQLSTATE %s, got %v", tc.sql, tc.wantErrCode, err)
						}
						return
					}
					if err != nil {
						t.Fatalf("%s: %v", tc.sql, err)
					}

					got := renderRows(rows)
					if bad == nil {
						if !equalStrings(got, tc.want) {
							t.Fatalf("%s\nSQL:  %s\npath: %s\ngot   %v\nwant  %v (PG 18.3)",
								tc.desc, tc.sql, path, got, tc.want)
						}
						return
					}
					if !equalStrings(got, bad.rows) {
						t.Fatalf("%s\nSQL:  %s\npath: %s\n"+
							"This path has a PINNED KNOWN BUG and its behaviour just changed.\n"+
							"got    %v\npinned %v\nPG     %v\nbug:   %s\nexpected fix: %s\n"+
							"If the fix landed, re-pin this path as green (drop the `bad` entry).",
							tc.desc, tc.sql, path, got, bad.rows, tc.want, bad.why, bad.fixedIn)
					}
				})
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSubqueryUnnestKillSwitch pins the S1 rollback path: with the pass
// disabled the planner must leave sublinks alone, and re-enabling must restore
// the decorrelated shape. Uses an index-free fixture, where the pull-up does
// fire at HEAD (with an index on the inner correlation column the correlation is
// absorbed into IndexScan.Key and the collectors miss it — the D3.0 gap).
func TestSubqueryUnnestKillSwitch(t *testing.T) {
	ctx, cleanup := newSemanticsFixture(t)
	defer cleanup()

	const sql = "SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a"

	planner.SetSubqueryUnnestEnabled(true)
	defer planner.SetSubqueryUnnestEnabled(true)
	on := explainText(t, ctx, sql)

	planner.SetSubqueryUnnestEnabled(false)
	off := explainText(t, ctx, sql)

	if !strings.Contains(off, "SubPlan 1") {
		t.Errorf("with unnesting disabled the sublink must survive as a SubPlan; plan:\n%s", off)
	}
	if strings.Contains(on, "SubPlan 1") && !strings.Contains(off, "SubPlan 1") {
		t.Errorf("kill switch inverted")
	}
	if on == off {
		t.Errorf("kill switch had no effect on the plan:\n%s", on)
	}
}

// explainText renders EXPLAIN output for sql as a single string.
func explainText(t *testing.T, ctx *Context, sql string) string {
	t.Helper()
	rows, err := runQueryWithErr(ctx, "EXPLAIN "+sql)
	if err != nil {
		t.Fatalf("EXPLAIN %s: %v", sql, err)
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(datumTestString(r[0]))
		b.WriteString("\n")
	}
	return b.String()
}
