package executor

import "testing"

// C-04a (P3-04) — LEFT admission into the join search, end to end and on
// VALUES.
//
// The planner-side fixtures (internal/optimizer/joinsearchspine_test.go) pin
// where each qual is PLACED; this one pins what the rows are. Both are needed
// and neither substitutes for the other: outer joins move rows across the
// null-extension boundary, so a placement bug shows up as a row that is
// present-but-NULL or absent-entirely, and a row COUNT alone would miss the
// first of those (21 of 21 TPC-H result sets once stayed byte-identical
// through a 43x plan regression, and a hash join once returned the right count
// with a NULL payload in every column).
//
// Every case below is a shape that was UNREACHABLE for the search before
// C-04a — the LEFT link was peeled off and planned syntactically — so "an
// unwinnable path is an untested path" applies to all of them.
func TestLeftJoinSearchAdmissionValues(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE lj_t (id int, v int)",
		"CREATE TABLE lj_p (id int, y int)",
		"CREATE TABLE lj_s (id int, z int)",
		// lj_t row 3 has no lj_p match: it is the row every LEFT case below
		// must keep, null-extended.
		"INSERT INTO lj_t VALUES (1,10),(2,20),(3,30)",
		"INSERT INTO lj_p VALUES (1,5),(2,7)",
		"INSERT INTO lj_s VALUES (1,100),(2,200),(3,300)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	cases := []struct {
		name string
		q    string
		want []string
	}{
		{
			// The base case: the whole statement is now one search problem.
			name: "left join keeps the unmatched row",
			q:    "SELECT lj_t.id, lj_p.y FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id ORDER BY lj_t.id",
			want: []string{"1|5", "2|7", "3|NULL"},
		},
		{
			// Q72's shape in miniature: a comma item beside a LEFT link, one
			// 3-relation problem whose join ORDER the search now chooses.
			name: "left join plus a comma item",
			q: "SELECT lj_t.id, lj_p.y, lj_s.z FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id, lj_s " +
				"WHERE lj_s.id = lj_t.id ORDER BY lj_t.id",
			want: []string{"1|5|100", "2|7|200", "3|NULL|300"},
		},
		{
			// DESIGN §3.5, the finding-1 shape. A single-relation WHERE
			// conjunct on the NULLABLE side must be delayed above the join.
			// Pushed to the leaf it would read as an ON qual and keep row 3.
			name: "nullable-side WHERE is delayed above the join",
			q:    "SELECT lj_t.id, lj_p.y FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id WHERE lj_p.y > 6 ORDER BY lj_t.id",
			want: []string{"2|7"},
		},
		{
			// The same rule read from the other side: IS NULL on the nullable
			// side is a test ON the null extension, so pushing it below would
			// return nothing at all.
			name: "nullable-side IS NULL sees the null extension",
			q:    "SELECT lj_t.id FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id WHERE lj_p.y IS NULL ORDER BY lj_t.id",
			want: []string{"3"},
		},
		{
			// A multi-relation WHERE conjunct reaching the nullable side. It
			// is not a leaf local, so the single-relation guard cannot catch
			// it: applied at the join it would become an ON condition and
			// drop rows 1 and 3 instead of null-extending them.
			name: "spanning WHERE reaching the nullable side is delayed",
			q: "SELECT lj_t.id, lj_p.y FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id " +
				"WHERE lj_t.v = lj_p.y * 2 ORDER BY lj_t.id",
			want: []string{"1|5"},
		},
		{
			// The converse: an ON qual on the nullable side IS a leaf filter
			// (`t LEFT JOIN (sigma y>6) p`), so row 1 is null-extended rather
			// than dropped. Same relids as the WHERE case above, opposite
			// answer — which is why the two cannot share a rule.
			name: "nullable-side ON qual filters the inner, keeping every left row",
			q: "SELECT lj_t.id, lj_p.y FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id AND lj_p.y > 6 " +
				"ORDER BY lj_t.id",
			want: []string{"1|NULL", "2|7", "3|NULL"},
		},
		{
			// A PRESERVED-side-only ON qual has no destination in a searched
			// tree (`outerOnQualsOK` declines the statement, which falls back
			// to the syntactic shape). Pinned on VALUES because the decline is
			// what keeps this answer right: pushed into lj_t's scan it would
			// drop rows 1 and 3 entirely instead of null-extending them.
			name: "preserved-side ON qual null-extends rather than filtering",
			q: "SELECT lj_t.id, lj_p.y FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id AND lj_t.v > 15 " +
				"ORDER BY lj_t.id",
			want: []string{"1|NULL", "2|7", "3|NULL"},
		},
		{
			// A LEFT link BELOW an inner one is C-04c's scope and is declined
			// here (the walk's on-spine rule). Pinned so the decline is a
			// correct answer rather than an untested one.
			name: "left link below an inner link",
			q: "SELECT lj_t.id, lj_p.y FROM lj_t JOIN lj_s ON lj_t.id = lj_s.id LEFT JOIN lj_p ON lj_t.id = lj_p.id " +
				"ORDER BY lj_t.id",
			want: []string{"1|5", "2|7", "3|NULL"},
		},
		{
			// Two stacked LEFT links: both must survive as LEFT joins. One
			// planned as an inner join loses row 3.
			name: "two stacked left links",
			q: "SELECT lj_t.id, lj_s.z FROM lj_t LEFT JOIN lj_p ON lj_t.id = lj_p.id " +
				"LEFT JOIN lj_s ON lj_p.id = lj_s.id ORDER BY lj_t.id",
			want: []string{"1|100", "2|200", "3|NULL"},
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
