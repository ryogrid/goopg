package executor

import "testing"

// C-04c (P3-04) — below-inner and non-first-comma outer links, end to end and
// on VALUES.
//
// Every case is a shape the search could not reach before C-04c: an outer link
// below an INNER one, or on a non-first comma FROM item, made the walk return
// the link as one opaque leaf, the leaf count disagreed with the binding count,
// and the whole statement fell back to the syntactic tree.
//
// The rows are compared column by column and never counted, and that is the
// lesson of C-04a's Q72: the failure there had the RIGHT row count on TPC-H and
// the wrong one on TPC-DS, because losing an outer join's jointype moves rows
// across the null-extension boundary rather than adding or removing them
// uniformly. A count-only assertion would have passed on the broken build.
func TestNonSpineOuterAdmissionValues(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE nsj_t (id int, v int)",
		"CREATE TABLE nsj_p (id int, y int)",
		"CREATE TABLE nsj_s (id int, z int)",
		"CREATE TABLE nsj_q (id int, w text)",
		// nsj_t row 3 has no nsj_p match: it is the row every LEFT case must
		// keep, null-extended.
		"INSERT INTO nsj_t VALUES (1,10),(2,20),(3,30)",
		"INSERT INTO nsj_p VALUES (1,5),(2,7)",
		"INSERT INTO nsj_s VALUES (1,100),(2,200),(3,300)",
		"INSERT INTO nsj_q VALUES (1,'a'),(2,'b'),(3,'c')",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	// Eight further inner links above the LEFT one: the chain exceeds
	// join_collapse_limit (8), so the joinlist nests a sub-problem and the
	// LEFT link's SpecialJoinInfo goes through the per-problem remap
	// (`sjInfosInItemSpace`) — where C-04a lost Q72's jointype. Every inner
	// qual is anchored on nsj_t, the LEFT link's PRESERVED side.
	longTail := ""
	for i := 1; i <= 8; i++ {
		a := string(rune('0' + i))
		longTail += " JOIN nsj_s s" + a + " ON nsj_t.id = s" + a + ".id"
	}

	cases := []struct {
		name string
		q    string
		want []string
	}{
		{
			// C-04c's first subject. The LEFT link sits on the INNER link's
			// left input; nsj_t row 3 survives null-extended.
			name: "left link below an inner link",
			q: "SELECT nsj_t.id, nsj_p.y, nsj_s.z FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"JOIN nsj_s ON nsj_t.id = nsj_s.id ORDER BY nsj_t.id",
			want: []string{"1|5|100", "2|7|200", "3|NULL|300"},
		},
		{
			// The same with two inner links above the LEFT one.
			name: "left link below two inner links",
			q: "SELECT nsj_t.id, nsj_p.y, nsj_q.w FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"JOIN nsj_s ON nsj_t.id = nsj_s.id JOIN nsj_q ON nsj_t.id = nsj_q.id ORDER BY nsj_t.id",
			want: []string{"1|5|a", "2|7|b", "3|NULL|c"},
		},
		{
			// OVER join_collapse_limit: the jointype must survive the
			// sub-problem split too (C-04a's Q72 failure mode, on C-04c's
			// position for the link).
			name: "left link below inner links, over the collapse limit",
			q: "SELECT nsj_t.id, nsj_p.y FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id" +
				longTail + " ORDER BY nsj_t.id",
			want: []string{"1|5", "2|7", "3|NULL"},
		},
		{
			// A WHERE reading the NULLABLE side is held above the searched
			// tree: pushed onto nsj_p's leaf it would read nsj_p's own rows
			// and lose the null extension nsj_t row 3 depends on.
			name: "nullable-side IS NULL below an inner link",
			q: "SELECT nsj_t.id, nsj_p.y FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"JOIN nsj_s ON nsj_t.id = nsj_s.id WHERE nsj_p.y IS NULL ORDER BY nsj_t.id",
			want: []string{"3|NULL"},
		},
		{
			// A non-strict nullable-side WHERE, which `reduceOuterJoins`
			// cannot demote: the delay proof has to hold it above.
			name: "non-strict nullable-side WHERE below an inner link",
			q: "SELECT nsj_t.id, nsj_p.y FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"JOIN nsj_s ON nsj_t.id = nsj_s.id WHERE nsj_p.y IS NULL OR nsj_p.y > 6 ORDER BY nsj_t.id",
			want: []string{"2|7", "3|NULL"},
		},
		{
			// The DECLINE, pinned on values, and this is the wrong-answer case
			// the decline exists for. The INNER link's ON qual reads the LEFT
			// link's nullable side from ABOVE it, so it must be evaluated on
			// the null-extended rows: only nsj_t row 3 (no nsj_p match) has a
			// NULL `y`, and it joins nsj_s row 3. Pushed onto nsj_p's leaf
			// instead — which is what `partitionConjunctsForJoinPlanning`
			// would do with a single-relation conjunct, and why the seam
			// declines the shape — nsj_p would be EMPTY (it has no NULL `y`
			// rows at all), every nsj_t row would be null-extended, and the
			// answer would be three rows instead of one.
			name: "inner ON qual reading the nullable side declines and still answers",
			q: "SELECT nsj_t.id, nsj_p.y FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"JOIN nsj_s ON nsj_t.id = nsj_s.id AND nsj_p.y IS NULL ORDER BY nsj_t.id",
			want: []string{"3|NULL"},
		},
		{
			// C-04c's second subject: a LEFT link on the SECOND comma FROM
			// item, whose ON qual is written in that item's own coordinates
			// and has to be re-based. An un-re-based shift would equate
			// nsj_s.id with nsj_t.id here and return different rows.
			name: "left link on a non-first comma item",
			q: "SELECT nsj_s.id, nsj_t.id, nsj_p.y FROM nsj_s, nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"WHERE nsj_s.id = 1 ORDER BY nsj_t.id",
			want: []string{"1|1|5", "1|2|7", "1|3|NULL"},
		},
		{
			// The same, with the WHERE reading the nullable side of the
			// non-first item's link.
			name: "non-first comma item, nullable-side WHERE",
			q: "SELECT nsj_s.id, nsj_t.id, nsj_p.y FROM nsj_s, nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"WHERE nsj_s.id = 1 AND nsj_p.y IS NULL ORDER BY nsj_t.id",
			want: []string{"1|3|NULL"},
		},
		{
			// The INNER half of the same decline: `base != 0` was one check
			// for both link types and lifting it admits this too.
			name: "inner link on a non-first comma item",
			q: "SELECT nsj_s.id, nsj_t.id, nsj_q.w FROM nsj_s, nsj_t JOIN nsj_q ON nsj_t.id = nsj_q.id " +
				"WHERE nsj_s.id = 2 ORDER BY nsj_t.id",
			want: []string{"2|1|a", "2|2|b", "2|3|c"},
		},
		{
			// An outer link on a RIGHT link's NULLABLE side. C-04c admitted
			// it, MEASURED it, and put the decline back: with the shape
			// searched, `buildJoinRelRestrictList` re-applies the LOWER
			// link's own `ON` clause (`t.id = p.id`) at the upper join as an
			// outer-join filter clause, the nsj_t row with no nsj_p match
			// carries a NULL through it, and its nsj_q row came back
			// null-extended — "NULL|NULL|c" instead of "3|NULL|c". These two
			// cases pin the CORRECT rows, which today come from the syntactic
			// fallback; they are the reproducer for ledger
			// `c04c-nested-outer-refilters-lower-on-qual` and they will keep
			// standing when that fix admits the shape.
			name: "left link under a right link's nullable side",
			q: "SELECT nsj_t.id, nsj_p.y, nsj_q.w FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"RIGHT JOIN nsj_q ON nsj_t.id = nsj_q.id ORDER BY nsj_q.id",
			want: []string{"1|5|a", "2|7|b", "3|NULL|c"},
		},
		{
			// …and with an unmatched preserved row on the RIGHT side, so the
			// nested null extension is visible in both directions at once —
			// which is what distinguishes a correct answer here from the
			// re-filtered one above, whose third row is null-extended for the
			// WRONG reason.
			name: "left link under a right link, unmatched preserved row",
			q: "SELECT nsj_t.id, nsj_p.y, nsj_q.w FROM nsj_t LEFT JOIN nsj_p ON nsj_t.id = nsj_p.id " +
				"RIGHT JOIN nsj_q ON nsj_t.id = nsj_q.id AND nsj_t.id < 3 ORDER BY nsj_q.id",
			want: []string{"1|5|a", "2|7|b", "NULL|NULL|c"},
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
