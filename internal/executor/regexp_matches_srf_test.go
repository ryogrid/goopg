package executor

import "testing"

// TestRegexpMatchesSRF pins the SELECT-list SRF expansion of
// regexp_matches(string, pattern[, flags]): with the 'g' flag it yields one
// row per match (PG's setup_regexp_matches semantics); without 'g' it still
// yields at most one row, matching regexp_match's own non-SRF behavior; and a
// non-match yields ZERO rows (unlike the scalar-position fallback used
// elsewhere, which returns SQL NULL). M0122-0002 (regexp_matches-srf).
func TestRegexpMatchesSRF(t *testing.T) {
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

	eq("'g' flag yields one row per match",
		collect(`SELECT regexp_matches('foo bar baz', '\w+', 'g')`),
		[]string{"{foo}", "{bar}", "{baz}"})

	eq("no 'g' flag yields at most the first match",
		collect(`SELECT regexp_matches('foo bar baz', '\w+')`),
		[]string{"{foo}"})

	eq("no match yields zero rows",
		collect(`SELECT regexp_matches('---', '[0-9]+', 'g')`),
		nil)

	eq("capture groups per match with 'g'",
		collect(`SELECT regexp_matches('2026-07-04 2027-01-02', '([0-9]+)-([0-9]+)-([0-9]+)', 'g')`),
		[]string{"{2026,07,04}", "{2027,01,02}"})
}

// TestRegexpMatchesSRFPerRow exercises the per-child-row case (regexp_matches
// in the SELECT list over a FROM table), zipped against a passthrough column
// exactly like generate_series/unnest already are in this position.
func TestRegexpMatchesSRFPerRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE rm_src (id int4, s text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO rm_src VALUES (1, 'a1 a2'), (2, 'b1'), (3, 'nomatch')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows := runQuery(t, ctx, `SELECT id, regexp_matches(s, '[a-z][0-9]', 'g') FROM rm_src ORDER BY id`)
	type got struct {
		id  int64
		arr string
	}
	want := []got{
		{1, "{a1}"}, {1, "{a2}"},
		{2, "{b1}"},
		// id=3 contributes zero rows (no match).
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d (rows=%v)", len(rows), len(want), rows)
	}
	for i, w := range want {
		id, _ := datumInt64(rows[i][0])
		if id != w.id || rows[i][1].StringValue() != w.arr {
			t.Fatalf("row %d = (id=%d, arr=%q), want (id=%d, arr=%q)", i, id, rows[i][1].StringValue(), w.id, w.arr)
		}
	}
}
