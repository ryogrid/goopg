package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPgCollationForFolds verifies pg_collation_for(expr) is folded at plan
// time (mirroring pg_typeof's fold, see TestPgTypeofFoldsToCompileTimeType)
// to the collation PostgreSQL's own pg_collation_for would report, per
// postgres/src/backend/utils/adt/misc.c and
// postgres/src/test/regress/expected/collate.out. M0122-0005.
func TestPgCollationForFolds(t *testing.T) {
	cat := pgbenchCatalog(t)
	if _, err := cat.(*catalog.InMemory).RegisterDomain("text_domain", catalog.Type{Name: "text"}, false); err != nil {
		t.Fatal(err)
	}
	// A table with explicit column-level COLLATE clauses so pg_collation_for(col)
	// can report the column's declared collation rather than the type default.
	if _, err := cat.(*catalog.InMemory).CreateTable(parser.ObjectName{Name: "collated_tbl"}, []catalog.Column{
		{Name: "c_plain", Type: catalog.Type{Name: "text"}},
		{Name: "c_ub", Type: catalog.Type{Name: "text"}, Collation: "ucs_basic"},
		{Name: "c_c", Type: catalog.Type{Name: "text"}, Collation: "C"},
		{Name: "n", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		sql       string
		wantValue string // expected StringConst value; ignored if wantNull or wantErrCode set
		wantNull  bool
		wantErr   string // expected PlanError.Code
		// wantDeclined asserts the WITHIN GROUP fold declined and control fell
		// through unchanged: Plan succeeds with no error, and the argument is
		// NOT folded to a *StringConst (i.e. it stays whatever the pre-S20
		// resolver would have produced). Used for the "must not merge"/decline
		// cases where asserting a specific folded value would be asserting a
		// merge that must NOT happen. M0134-0001 S20.
		wantDeclined bool
	}{
		{name: "explicit cast to text", sql: `SELECT pg_collation_for('x'::text)`, wantValue: "default"},
		{name: "bare untyped literal has no collation", sql: `SELECT pg_collation_for('x')`, wantNull: true},
		{name: "explicit COLLATE POSIX is quoted", sql: `SELECT pg_collation_for('x' COLLATE "POSIX")`, wantValue: `"POSIX"`},
		{name: "explicit COLLATE simple lowercase name is bare", sql: `SELECT pg_collation_for('x' COLLATE "ucs_basic")`, wantValue: "ucs_basic"},
		{name: "domain over text is collatable like its base", sql: `SELECT pg_collation_for('x'::text_domain)`, wantValue: "default"},
		{name: "non-collatable type errors 42804", sql: `SELECT pg_collation_for(aid) FROM pgbench_accounts`, wantErr: "42804"},
		{name: "array of text is collatable like its element", sql: `SELECT pg_collation_for('{a,b}'::text[])`, wantValue: "default"},
		{name: "array of name follows name's C collation", sql: `SELECT pg_collation_for(ARRAY['a','b']::name[])`, wantValue: "C"},
		{name: "array of non-collatable element type errors 42804", sql: `SELECT pg_collation_for('{1,2}'::int4[])`, wantErr: "42804"},
		// Column-level explicit COLLATE clauses are reflected verbatim (quoted
		// per generate_collation_name) instead of the text default.
		{name: "column with explicit lowercase COLLATE is bare", sql: `SELECT pg_collation_for(c_ub) FROM collated_tbl`, wantValue: "ucs_basic"},
		{name: "column with explicit COLLATE C is quoted", sql: `SELECT pg_collation_for(c_c) FROM collated_tbl`, wantValue: `"C"`},
		{name: "collatable column without explicit COLLATE is default", sql: `SELECT pg_collation_for(c_plain) FROM collated_tbl`, wantValue: "default"},
		{name: "qualified column with explicit COLLATE", sql: `SELECT pg_collation_for(collated_tbl.c_ub) FROM collated_tbl`, wantValue: "ucs_basic"},
		// Ordered-set aggregate (WITHIN GROUP) collation propagation. M0134-0001
		// S20. See docs/design/0134-0001-p8-ordered-set-agg-collation.md — PG's
		// merge_sort_collations condition (parse_collate.c:926-927) merges a
		// single WITHIN GROUP sort key's collation into the aggregate's own
		// result. percentile_disc(...) WITHIN GROUP (...) is itself an
		// aggregate call, so pg_collation_for(...) wrapping it routes target-
		// list resolution through resolveExprAfterAggregate, not resolveExpr
		// (planner.go:4241-4247) — this is the site round 1 of this slice
		// found unreachable and round 2 fixed.
		{name: "ordered-set agg with single explicit-collate WITHIN GROUP key merges (was default pre-S20)",
			sql:       `SELECT pg_collation_for(percentile_disc(1) within group (order by x collate "POSIX")) from (values ('fred'),('jim')) v(x)`,
			wantValue: `"POSIX"`},
		{name: "ordered-set agg with single WITHIN GROUP key, no explicit collate, folds to type default",
			sql:       `SELECT pg_collation_for(percentile_disc(1) within group (order by x)) from (values ('fred'),('jim')) v(x)`,
			wantValue: "default"},
		{name: "ordered-set agg with two differing-collate WITHIN GROUP keys does NOT merge (declines, no error)",
			sql:          `SELECT pg_collation_for(mode() within group (order by c_ub collate "POSIX", c_c collate "C")) FROM collated_tbl`,
			wantDeclined: true},
		{name: "non-FuncCall raw arg in a post-aggregate-context query declines the WITHIN GROUP fold unaffected",
			sql:          `SELECT pg_collation_for(c_ub), count(*) FROM collated_tbl GROUP BY c_ub`,
			wantDeclined: true},
		// Characterisation only — NOT fixed by S20 (out of scope; see brief and
		// design doc's "Scope boundary"). PG returns "POSIX" here via
		// assign_aggregate_collations (parse_collate.c:880-899, unconditional
		// merge for plain AGGKIND_NORMAL aggregates); goopg's actual answer is
		// recorded below, whatever it is, as a deferral-ledger candidate rather
		// than asserted as correct. max(...) is itself the aggregate (no
		// WITHIN GROUP), so foldPgCollationForWithinGroup declines (len==0 not
		// ==1) and this also exercises the post-aggregate resolver.
		{name: "CHARACTERISATION ONLY: plain aggregate max(x COLLATE) — PG returns \"POSIX\", not fixed by S20",
			sql:          `SELECT pg_collation_for(max(x collate "POSIX")) from (values ('fred'),('jim')) v(x)`,
			wantDeclined: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stmt := parseOne(t, tc.sql)
			plan, err := Plan(stmt, cat)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Plan(%q): want error %s, got none", tc.sql, tc.wantErr)
				}
				pe, ok := err.(*PlanError)
				if !ok || pe.Code != tc.wantErr {
					t.Fatalf("Plan(%q) error = %#v, want PlanError{Code: %s}", tc.sql, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Plan(%q): %v", tc.sql, err)
			}
			proj, ok := plan.(*Project)
			if !ok {
				t.Fatalf("plan=%T want *Project", plan)
			}
			fc, ok := proj.Targets[0].(*FuncCall)
			if !ok || fc.Name != "pg_collation_for" || len(fc.Args) != 1 {
				t.Fatalf("target=%#v want pg_collation_for(single arg)", proj.Targets[0])
			}
			if tc.wantNull {
				if _, ok := fc.Args[0].(*NullConst); !ok {
					t.Fatalf("pg_collation_for arg=%T want *NullConst", fc.Args[0])
				}
				return
			}
			if tc.wantDeclined {
				// The fold must NOT have merged a collation: Plan succeeded
				// (checked above) with no error, and the argument must not be
				// a *StringConst produced by the WITHIN GROUP merge fold.
				if _, ok := fc.Args[0].(*StringConst); ok {
					t.Fatalf("pg_collation_for arg unexpectedly folded to *StringConst %q, want decline (unmerged)", fc.Args[0].(*StringConst).Value)
				}
				return
			}
			sc, ok := fc.Args[0].(*StringConst)
			if !ok {
				t.Fatalf("pg_collation_for arg=%T want *StringConst (folded)", fc.Args[0])
			}
			if sc.Value != tc.wantValue {
				t.Errorf("pg_collation_for = %q, want %q", sc.Value, tc.wantValue)
			}
		})
	}
}
