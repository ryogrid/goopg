package executor

// right_join_spine_rows_test.go — M0127-P5.9-t. The end-to-end half of "a RIGHT
// JOIN's nullable arm may be REORDERED but its `WHERE` may not be PUSHED".
//
// The planner-side guards (internal/planner/joinsearchspine_test.go) assert the
// seam's decisions on a synthetic tree. This file asserts the only thing that
// ultimately matters: WHICH ROWS COME BACK. It is here rather than in the
// planner package because the planner cannot execute, and a join-order change
// that is provably legal on paper is still the project's most expensive failure
// mode when it is not (Hard-won Rule #1).
//
// Until M0127-P6.3 both enumerators ran the same statement here and had to
// agree; the A/B closed with the old subset-bitmask DP (08 §4), whose deletion
// also took the cross-package `SetPGShapedJoinSearch` pin. The expected row set
// is still spelled out independently of the enumerator — an assertion that only
// compared two arms would pass if both were wrong the same way.

import (
	"reflect"
	"strings"
	"testing"
)

// rightSpineFixture builds `a ⋈ b RIGHT JOIN c`: a two-relation INNER prefix on
// the NULLABLE side of a RIGHT JOIN, which is the shape P5.9-t opened to the
// search. `c` has one row that matches nothing, so the statement below has
// null-extended rows to be right or wrong about.
func rightSpineFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE rj_a (id int, aval int)",
		"CREATE TABLE rj_b (id int, aid int, cid int)",
		"CREATE TABLE rj_c (id int, cval int)",
		"INSERT INTO rj_a VALUES (1, 10), (2, 20)",
		"INSERT INTO rj_b VALUES (11, 1, 100), (12, 2, 101)",
		"INSERT INTO rj_c VALUES (100, 1000), (101, 1010), (102, 1020)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	return ctx, cleanup
}

// The three `c` rows join as: 100→(b 11, a 1), 101→(b 12, a 2), 102→nothing.
// So exactly one row of the join is null-extended, and it is the only row that
// satisfies `WHERE a.id IS NULL`.
const rightSpineSQL = `SELECT rj_c.id, rj_a.id, rj_b.id
	FROM rj_a JOIN rj_b ON rj_a.id = rj_b.aid
	RIGHT JOIN rj_c ON rj_b.cid = rj_c.id
	WHERE rj_a.id IS NULL`

// TestRightJoinSpineKeepsNullExtendedRows is the decisive one. `rj_a.id IS NULL`
// is TRUE only on the row the RIGHT JOIN null-extends; pushed down to `rj_a` it
// becomes a test on that table's own `id` column, which is never NULL, and the
// statement answers 0 rows instead of 1.
//
// That is not a hypothetical: it is exactly what the seam does to a LEFT JOIN's
// prefix, legally, and what it would have done here had P5.9-t widened
// `spineLinkSearchable` without also suppressing the push (`prefixNullable`,
// joinsearchseam.go). Upstream's rule is `check_outerjoin_delay` (initsplan.c).
func TestRightJoinSpineKeepsNullExtendedRows(t *testing.T) {
	ctx, cleanup := rightSpineFixture(t)
	defer cleanup()

	want := []string{"102,,"} // c.id=102, both nullable-side columns NULL
	if got := formatRows(runQueryRows(t, ctx, rightSpineSQL)); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v — a WHERE qual on a RIGHT JOIN's nullable arm was "+
			"evaluated below the join that produces the NULLs", got, want)
	}
}

// TestRightJoinSpineSearchedRows: the searched arm may pick a different order
// for `rj_a ⋈ rj_b` than the syntactic one, and a reordering that changes the
// answer is the failure this project pays most for. Until M0127-P6.3 this was
// an A/B against the legacy arm (`SetPGShapedJoinSearch`); the old DP's
// deletion took the pin, so the expected rows are now asserted directly —
// which is the stronger half of the comparison anyway.
func TestRightJoinSpineSearchedRows(t *testing.T) {
	ctx, cleanup := rightSpineFixture(t)
	defer cleanup()

	// The unrestricted join, so the assertion covers the MATCHED rows
	// too — a lost prefix `ON` qual is a cross product, and `WHERE
	// a.id IS NULL` alone would not see it.
	all := formatRows(runQueryRows(t, ctx, strings.TrimSuffix(
		rightSpineSQL, "\n\tWHERE rj_a.id IS NULL")+" ORDER BY rj_c.id"))
	want := []string{"100,1,11", "101,2,12", "102,,"}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("rows = %v, want %v", all, want)
	}
	if got := formatRows(runQueryRows(t, ctx, rightSpineSQL)); !reflect.DeepEqual(
		got, []string{"102,,"}) {
		t.Fatalf("restricted rows = %v, want [102,,]", got)
	}
}
