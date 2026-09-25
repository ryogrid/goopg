package executor

import (
	"strings"
	"testing"
)

// TestGroupByPruneAcrossJoinedRelations pins M0145-0008i: PG's
// remove_useless_groupby_columns (initsplan.c) runs for EVERY base relation of
// the FROM clause and unions the drops, so a GROUP BY listing two relations'
// primary keys plus columns they determine groups on the keys alone. Expected
// lines and rows are PG 18.3's, captured on a scratch cluster 2026-09-25:
//
//	HashAggregate
//	  Group Key: gc.ck, go.ok
//
// for both the inner join and the LEFT JOIN (PG applies no nullable-side
// guard: a NULL-extended key still determines its NULL-extended dependents).
func TestGroupByPruneAcrossJoinedRelations(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		"CREATE TABLE gc (ck int PRIMARY KEY, nm text)",
		"CREATE TABLE go (ok int PRIMARY KEY, ck int, tot int)",
		"INSERT INTO gc VALUES (1,'a'),(2,'b'),(3,'c')",
		"INSERT INTO go VALUES (10,1,5),(11,1,7),(12,2,9)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, join := range []string{"JOIN", "LEFT JOIN"} {
		q := "SELECT gc.nm, gc.ck, go.ok, go.tot, sum(go.tot) FROM gc " + join +
			" go ON gc.ck = go.ck GROUP BY gc.nm, gc.ck, go.ok, go.tot"
		plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+q), "\n")
		if !strings.Contains(plan, "Group Key: gc.ck, go.ok\n") && !strings.HasSuffix(plan, "Group Key: gc.ck, go.ok") {
			t.Errorf("%s: want PG's pruned `Group Key: gc.ck, go.ok`; plan:\n%s", join, plan)
		}
		rows, err := runQueryWithErr(ctx, q+" ORDER BY 2, 3")
		if err != nil {
			t.Fatalf("%s: %v", join, err)
		}
		var got []string
		for _, r := range rows {
			parts := make([]string, len(r))
			for i, d := range r {
				if d.IsNull() {
					parts[i] = "NULL"
				} else {
					parts[i] = d.Format()
				}
			}
			got = append(got, strings.Join(parts, "|"))
		}
		want := []string{"a|1|10|5|5", "a|1|11|7|7", "b|2|12|9|9"}
		if join == "LEFT JOIN" {
			want = append(want, "c|3|NULL|NULL|NULL")
		}
		if strings.Join(got, ";") != strings.Join(want, ";") {
			t.Errorf("%s rows:\n got %v\nwant %v", join, got, want)
		}
	}

	// A CTE named like a real table is not a plain relation: it must not
	// borrow gc's primary key, so both of its grouped columns stay.
	plan := strings.Join(runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) WITH gc AS MATERIALIZED (SELECT ck, nm FROM gc) "+
			"SELECT gc.ck, gc.nm, count(*) FROM gc JOIN go ON gc.ck = go.ck GROUP BY gc.ck, gc.nm"), "\n")
	// (Asserted by column count, not text: the alias EXPLAIN gives the CTE
	// scan is a separate rendering question.)
	var groupKey string
	for _, line := range strings.Split(plan, "\n") {
		if k := strings.TrimSpace(line); strings.HasPrefix(k, "Group Key: ") {
			groupKey = k
			break
		}
	}
	if n := len(strings.Split(strings.TrimPrefix(groupKey, "Group Key: "), ", ")); groupKey == "" || n != 2 {
		t.Errorf("a CTE borrowed a base table's key (group key %q); plan:\n%s", groupKey, plan)
	}
}
