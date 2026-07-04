package executor

import "testing"

// TestFromRegexpMatches pins the FROM-clause SRF form of
// regexp_matches(string, pattern[, flags]): FROM regexp_matches(...) [AS
// alias(col)] [WITH ORDINALITY]. Verified byte-for-byte against a real
// PostgreSQL 18.3 cluster: default column name is "regexp_matches"; 'g' flag
// yields one row per match; no match yields zero rows; WITH ORDINALITY
// appends a 1-based bigint ordinal column. M0122-0002 follow-up (the
// FROM-clause form deferred by the earlier SELECT-list-only loop).
func TestFromRegexpMatches(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	collect := func(sql string) []string {
		t.Helper()
		rows := runQuery(t, ctx, sql)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r[0].StringValue())
		}
		return out
	}

	eq := func(name string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %d rows %v, want %d rows %v", name, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: row %d = %q, want %q (got=%v)", name, i, got[i], want[i], got)
			}
		}
	}

	eq("no flag: at most the first match",
		collect(`SELECT * FROM regexp_matches('foobarbequebaz', '(bar)(beque)')`),
		[]string{"{bar,beque}"})

	eq("'g' flag: one row per match",
		collect(`SELECT * FROM regexp_matches('foobarbequebazbarbeque', '(bar)(beque)', 'g')`),
		[]string{"{bar,beque}", "{bar,beque}"})

	eq("no match: zero rows",
		collect(`SELECT * FROM regexp_matches('nomatch', 'xyz')`),
		nil)

	// Column alias.
	rows := runQuery(t, ctx, `SELECT m FROM regexp_matches('foobarbequebaz', '(bar)(beque)') AS t(m)`)
	if len(rows) != 1 || rows[0][0].StringValue() != "{bar,beque}" {
		t.Fatalf("aliased column: got %v", rows)
	}

	// WITH ORDINALITY appends a 1-based bigint ordinal (`SELECT *` form; naming
	// the ordinality column explicitly in the SELECT list hits a pre-existing,
	// unnest-shared column-resolution gap — see ledger M0122-0002-FROM-SRF row).
	rows = runQuery(t, ctx, `SELECT * FROM regexp_matches('foobarbequebazbarbeque', '(bar)(beque)', 'g') WITH ORDINALITY AS t(m, n)`)
	if len(rows) != 2 {
		t.Fatalf("WITH ORDINALITY: got %d rows, want 2 (rows=%v)", len(rows), rows)
	}
	for i, want := range []int64{1, 2} {
		n, _ := datumInt64(rows[i][1])
		if rows[i][0].StringValue() != "{bar,beque}" || n != want {
			t.Fatalf("WITH ORDINALITY row %d = (m=%q, n=%d), want (m={bar,beque}, n=%d)", i, rows[i][0].StringValue(), n, want)
		}
	}
}
