package executor

// R3-1: PG-faithful LEFT-join semantics for the index-driven NLI operator.
//
// A NestedLoopIndexJoin's `Predicate` is the JOIN **ON** residual (the
// conjuncts the index probe did not consume — see nl_index_join.go's
// leftover retention), NOT a post-join WHERE filter. PostgreSQL therefore
// requires, for `a LEFT JOIN b ON (equi AND residual)`:
//
//   - an outer row whose probe candidates ALL fail the residual still
//     emits null-padded (the residual is part of the join condition, so
//     "no candidate satisfies it" means "no match", not "drop the row");
//   - the null-padded row itself is emitted unconditionally — the residual
//     is never evaluated against the padding.
//
// Both were violated before R3-1: the operator marked the outer row as
// matched on the first probe-produced row (before evaluating the
// predicate, and without LEFT resetting it on failure), and it gated the
// null-pad fallback on evaluating the predicate against the padded row.
// The hash join already had exactly this fix (M0119-0004, pinned by
// leftjoin_hash_residual_dropped_row_test.go); the NLI operator never
// received it.
//
// The fixture is the canonical Q13 tripwire shape at doll-house scale — a
// preserved left side joined to an indexed inner with a residual on the
// ON clause — built in its CROSS-RELATION form so the residual lands on
// `nli.Predicate` rather than being pushed into a Filter{inner} wrapper
// (the inner-only form is exactly what keeps real Q13 on the hash path).
//
//	cust(c_key, c_bal)                     — preserved side
//	ordr(o_key, o_total) + btree on o_key  — indexed inner
//
// Per c_key, with ON (c_key = o_key AND o_total > c_bal):
//	1 → (1, 50)            residual fails  → null-padded (defect 1)
//	2 → (2, 500)           residual passes → joined
//	3 → no probe candidate                 → null-padded (defect 2)
//	4 → (4, 10), (4, 20)   both fail       → null-padded (defect 1, multi)

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func newNLILeftResidualFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE cust (c_key int, c_bal int)",
		"CREATE TABLE ordr (o_key int, o_total int)",
		"CREATE INDEX ordr_key_idx ON ordr (o_key)",
		"INSERT INTO cust VALUES (1, 100)",
		"INSERT INTO cust VALUES (2, 100)",
		"INSERT INTO cust VALUES (3, 100)",
		"INSERT INTO cust VALUES (4, 100)",
		"INSERT INTO ordr VALUES (1, 50)",
		"INSERT INTO ordr VALUES (2, 500)",
		"INSERT INTO ordr VALUES (4, 10)",
		"INSERT INTO ordr VALUES (4, 20)",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	// Stats so the NLI cost gate accepts the shape (the in-process
	// fixture's ANALYZE is a no-op) — same technique as
	// newNLIResidualFixture.
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "cust"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 4, Columns: []catalog.ColumnStats{
			{NDistinct: 4}, {NDistinct: 1},
		}}
	}
	if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "ordr"}); ok {
		tbl.Stats = &catalog.TableStats{RowCount: 4, Columns: []catalog.ColumnStats{
			{NDistinct: 3}, {NDistinct: 4},
		}}
	}
	return ctx, cleanup
}

// nliLeftQuery is the cross-relation ON-residual LEFT join. The residual
// `o_total > c_bal` references BOTH sides, so the LEFT ON-split classifies
// it sideMixed and keeps it on the join predicate — where the NLI leftover
// retention picks it up as `nli.Predicate`.
const nliLeftQuery = "SELECT c_key, o_total FROM cust LEFT JOIN ordr " +
	"ON c_key = o_key AND o_total > c_bal ORDER BY c_key"

// requireNLILeftPlan fails unless the query is actually served by a
// NestedLoopIndexJoin carrying a residual Predicate — without this the row
// assertions could pass on the hash path and prove nothing about the
// operator under test.
//
// The NLI renders PG's label `Nested Loop Left Join`, which a plain nested
// loop shares, so the discriminator is the inner `Index Cond` binding an
// OUTER column (`o_key = c_key`): only the NLI binds outer values into a
// per-outer index probe. The `Filter:` line is the residual Predicate this
// test exists to exercise.
func requireNLILeftPlan(t *testing.T, ctx *Context) {
	t.Helper()
	plan := nliResidualExplain(t, ctx, nliLeftQuery)
	for _, want := range []string{"Nested Loop Left Join", "Index Cond: (o_key = c_key)", "Filter:"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("expected an index-driven LEFT NLI with a residual (missing %q); plan:\n%s", want, plan)
		}
	}
}

// TestNLILeftResidualPreservesOuterRows pins both defects at once: rows 1
// and 4 have probe candidates that all fail the residual (defect 1), row 3
// has no candidate at all and must not have the residual evaluated against
// its padding (defect 2). PG returns one row per left row, four in total.
func TestNLILeftResidualPreservesOuterRows(t *testing.T) {
	ctx, cleanup := newNLILeftResidualFixture(t)
	defer cleanup()
	requireNLILeftPlan(t, ctx)

	rows, err := runQueryWithErr(ctx, nliLeftQuery)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, datumTestString(r[0])+"|"+datumTestString(r[1]))
	}
	want := []string{"1|NULL", "2|500", "3|NULL", "4|NULL"}
	if len(got) != len(want) {
		t.Fatalf("LEFT join dropped preserved rows: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestNLILeftResidualMatchesHashPath cross-checks the operator against the
// hash join, which already implements the PG semantics (M0119-0004). Any
// divergence is by definition a bug in whichever path differs from PG, and
// the two agreeing is the strongest available in-process oracle.
func TestNLILeftResidualMatchesHashPath(t *testing.T) {
	ctx, cleanup := newNLILeftResidualFixture(t)
	defer cleanup()

	nliRows, err := runQueryWithErr(ctx, nliLeftQuery)
	if err != nil {
		t.Fatalf("NLI query: %v", err)
	}
	// Dropping the index removes the probe, forcing the hash path with
	// the identical predicate.
	if err := runDDL(t, ctx, "DROP INDEX ordr_key_idx"); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	hashRows, err := runQueryWithErr(ctx, nliLeftQuery)
	if err != nil {
		t.Fatalf("hash query: %v", err)
	}
	nli, hash := renderRows(nliRows), renderRows(hashRows)
	if len(nli) != len(hash) {
		t.Fatalf("NLI and hash paths disagree on row count: NLI %v vs hash %v", nli, hash)
	}
	for i := range nli {
		if nli[i] != hash[i] {
			t.Fatalf("row %d: NLI %q vs hash %q", i, nli[i], hash[i])
		}
	}
}
