package executor

import "testing"

// C-04b (P3-04) — RIGHT admission into the join search, end to end and on
// VALUES. The mirror of leftjoin_search_admission_test.go, and every case is
// a shape the search could not reach before C-04b: a RIGHT link in second or
// later position (the first is flipped to LEFT by reduceOuterJoins' S9.4
// arm) used to pin, be peeled, and be planned syntactically.
//
// The planner-side fixtures (internal/optimizer/joinsearch_rightlink_test.go)
// pin where each qual is PLACED and which side the reduced LEFT join
// preserves; this one pins what the rows are. A RIGHT link null-extends its
// whole left prefix, so every placement bug here shows up as a preserved row
// that is absent, or present with the wrong NULLs — which is why the rows are
// compared column by column and never counted.
func TestRightJoinSearchAdmissionValues(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE rj_t (id int, v int)",
		"CREATE TABLE rj_s (id int, z int)",
		"CREATE TABLE rj_p (id int, y int)",
		"CREATE TABLE rj_q (id int, w text)",
		"INSERT INTO rj_t VALUES (1,10),(2,20),(3,30)",
		"INSERT INTO rj_s VALUES (1,100),(2,200),(3,300)",
		// rj_p row 4 has no rj_t/rj_s match: it is the row every RIGHT case
		// below must keep, null-extended.
		"INSERT INTO rj_p VALUES (1,5),(2,7),(4,9)",
		"INSERT INTO rj_q VALUES (1,'a'),(4,'d')",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	// Nine inner self-joins of rj_t below a RIGHT link: the chain exceeds
	// join_collapse_limit (8), so the prefix becomes a sub-problem item and
	// the link goes through the per-problem SJI remap — where C-04a lost
	// Q72's jointype.
	longPrefix := "rj_t t1"
	for i := 2; i <= 9; i++ {
		longPrefix += " JOIN rj_t t" + string(rune('0'+i)) + " ON t1.id = t" + string(rune('0'+i)) + ".id"
	}

	cases := []struct {
		name string
		q    string
		want []string
	}{
		{
			// The base case: an inner link then a RIGHT link, one 3-relation
			// problem. Row 4 of rj_p survives null-extended.
			name: "right join keeps the unmatched preserved row",
			q: "SELECT rj_t.id, rj_p.y FROM rj_t JOIN rj_s ON rj_t.id = rj_s.id " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id ORDER BY rj_p.id",
			want: []string{"1|5", "2|7", "NULL|9"},
		},
		{
			// A WHERE on the NULLABLE prefix is a test on null-extended rows
			// and is delayed above the join: IS NULL sees the extension.
			name: "nullable-side IS NULL sees the null extension",
			q: "SELECT rj_p.id, rj_p.y FROM rj_t JOIN rj_s ON rj_t.id = rj_s.id " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id WHERE rj_t.id IS NULL ORDER BY rj_p.id",
			want: []string{"4|9"},
		},
		{
			// A non-strict WHERE on the nullable side (an OR keeps the link
			// an outer join through reduceOuterJoins): pushed below the link
			// it would read rj_t's own rows and lose row 4's null extension.
			name: "non-strict nullable-side WHERE is delayed above the join",
			q: "SELECT rj_t.id, rj_p.y FROM rj_t JOIN rj_s ON rj_t.id = rj_s.id " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id WHERE rj_t.v IS NULL OR rj_t.v < 15 ORDER BY rj_p.id",
			want: []string{"1|5", "NULL|9"},
		},
		{
			// A WHERE on the PRESERVED side distributes to its leaf; the
			// answer is the same either way, and pinned so a delay that
			// over-holds cannot be told from one that under-holds.
			name: "preserved-side WHERE",
			q: "SELECT rj_t.id, rj_p.y FROM rj_t JOIN rj_s ON rj_t.id = rj_s.id " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id WHERE rj_p.y > 6 ORDER BY rj_p.id",
			want: []string{"2|7", "NULL|9"},
		},
		{
			// An ON conjunct on the NULLABLE side is a filter on the nullable
			// input (`(sigma v>15) t … RIGHT JOIN p`): row 1 of rj_p is
			// null-extended rather than dropped.
			name: "nullable-side ON qual filters the inner, keeping every preserved row",
			q: "SELECT rj_t.id, rj_p.y FROM rj_t JOIN rj_s ON rj_t.id = rj_s.id " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id AND rj_t.v > 15 ORDER BY rj_p.id",
			want: []string{"NULL|5", "2|7", "NULL|9"},
		},
		{
			// A PRESERVED-side-only ON conjunct has no destination in a
			// searched tree (`outerOnQualsOK` declines, syntactic fallback).
			// Pinned on VALUES because the decline is what keeps this right:
			// pushed into rj_p's scan it would drop row 1 instead of
			// null-extending it.
			name: "preserved-side ON qual null-extends rather than filtering",
			q: "SELECT rj_t.id, rj_p.y FROM rj_t JOIN rj_s ON rj_t.id = rj_s.id " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id AND rj_p.y > 6 ORDER BY rj_p.id",
			want: []string{"NULL|5", "2|7", "NULL|9"},
		},
		{
			// An inner ON qual under the nullable prefix that the search
			// cannot consume (an OR-of-ANDs: the clause list takes the
			// shared equality, the OR itself would fall into the residual
			// ABOVE the outer join). `innerOnQualsBelowNullableOK` declines
			// the statement; evaluated above the join, the OR would test
			// null-extended rows and drop rows 1 and 4.
			name: "unconsumed inner ON qual stays below the right link",
			q: "SELECT rj_t.id, rj_p.y FROM rj_t JOIN rj_s ON " +
				"(rj_t.id = rj_s.id AND rj_s.z > 150) OR (rj_t.id = rj_s.id AND rj_t.v > 25) " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id ORDER BY rj_p.id",
			want: []string{"NULL|5", "2|7", "NULL|9"},
		},
		{
			// RIGHT under LEFT: both spine links admitted, both survive as
			// outer joins. rj_p row 2 has no rj_q match (LEFT null-extends
			// it), row 4 has no rj_t match (RIGHT null-extends it).
			name: "right link under a left link",
			q: "SELECT rj_t.id, rj_p.id, rj_q.w FROM rj_t JOIN rj_s ON rj_t.id = rj_s.id " +
				"RIGHT JOIN rj_p ON rj_t.id = rj_p.id LEFT JOIN rj_q ON rj_p.id = rj_q.id ORDER BY rj_p.id",
			want: []string{"1|1|a", "2|2|NULL", "NULL|4|d"},
		},
		{
			// The collapse-split pin on VALUES: the RIGHT link's jointype
			// must survive the sub-problem split (C-04a's Q72 failure mode).
			name: "right link survives the collapse split",
			q: "SELECT t1.id, rj_p.y FROM " + longPrefix +
				" RIGHT JOIN rj_p ON t9.id = rj_p.id ORDER BY rj_p.id",
			want: []string{"1|5", "2|7", "NULL|9"},
		},
		{
			// …and its nullable-side WHERE must stay delayed across it.
			name: "nullable-side IS NULL survives the collapse split",
			q: "SELECT rj_p.id, rj_p.y FROM " + longPrefix +
				" RIGHT JOIN rj_p ON t9.id = rj_p.id WHERE t1.id IS NULL ORDER BY rj_p.id",
			want: []string{"4|9"},
		},
		{
			// An outer link on a RIGHT link's nullable side is declined by
			// the walk (C-04c's shape); pinned so the decline is a correct
			// answer. rj_t row 3 has no rj_p match under the LEFT; the RIGHT
			// then keeps every rj_q row.
			name: "left link under a right link's nullable side",
			q: "SELECT rj_t.id, rj_p.y, rj_q.w FROM rj_t LEFT JOIN rj_p ON rj_t.id = rj_p.id " +
				"RIGHT JOIN rj_q ON rj_t.id = rj_q.id ORDER BY rj_q.id",
			want: []string{"1|5|a", "NULL|NULL|d"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := runQuery(t, ctx, c.q)
			if len(rows) != len(c.want) {
				t.Fatalf("got %d rows, want %d:\n%v", len(rows), len(c.want), rows)
			}
			for i, want := range c.want {
				got := ""
				for j, d := range rows[i] {
					if j > 0 {
						got += "|"
					}
					got += datumTestString(d)
				}
				if got != want {
					t.Errorf("row %d = %q, want %q", i, got, want)
				}
			}
		})
	}
}
