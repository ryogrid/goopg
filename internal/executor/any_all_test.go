package executor

import "testing"

// TestAnySomeAllOrderingOperators pins the M0122-0004 executor-level
// evaluation of `expr op ANY|SOME|ALL (array | subquery)` for the ordering
// comparisons (< > <= >=), which previously only worked for `=`/`!=`/regex
// operators. Verified against real PostgreSQL 18.3 semantics: ANY/SOME is
// true iff the comparison holds for at least one element, ALL iff it holds
// for every element.
func TestAnySomeAllOrderingOperators(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 5)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	scalarBool := func(sql string) bool {
		t.Helper()
		rows := runQuery(t, ctx, sql)
		if len(rows) != 1 {
			t.Fatalf("%s: got %d rows, want 1", sql, len(rows))
		}
		return rows[0][0].BoolValue()
	}

	cases := []struct {
		sql  string
		want bool
	}{
		// v=5; ARRAY[1,5,10]: some element equals 5, some are <5, some >5.
		{"SELECT v < ANY (ARRAY[1,5,10]) FROM t", true},   // 5 < 10
		{"SELECT v < SOME (ARRAY[1,5,10]) FROM t", true},  // synonym
		{"SELECT v < ALL (ARRAY[1,5,10]) FROM t", false},  // 5 < 1 is false
		{"SELECT v < ALL (ARRAY[10,20]) FROM t", true},    // 5 < 10 and 5 < 20
		{"SELECT v > ANY (ARRAY[1,5,10]) FROM t", true},   // 5 > 1
		{"SELECT v > ALL (ARRAY[1,5,10]) FROM t", false},  // 5 > 10 is false
		{"SELECT v >= ALL (ARRAY[1,5]) FROM t", true},     // 5>=1 and 5>=5
		{"SELECT v <= ALL (ARRAY[5,10]) FROM t", true},    // 5<=5 and 5<=10
		{"SELECT v != ALL (ARRAY[1,2,3]) FROM t", true},   // 5 differs from all
		{"SELECT v != ALL (ARRAY[1,5,3]) FROM t", false},  // 5 == 5 present
		{"SELECT v <> ALL (ARRAY[1,2,3]) FROM t", true},
		// Subquery form: previously unsupported for any operator.
		{"SELECT v > ANY (SELECT x FROM (VALUES (1),(2)) AS s(x)) FROM t", true},
		{"SELECT v > ALL (SELECT x FROM (VALUES (1),(2)) AS s(x)) FROM t", true},
		{"SELECT v > ALL (SELECT x FROM (VALUES (1),(10)) AS s(x)) FROM t", false},
		{"SELECT v > SOME (SELECT x FROM (VALUES (10),(20)) AS s(x)) FROM t", false},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			if got := scalarBool(c.sql); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
