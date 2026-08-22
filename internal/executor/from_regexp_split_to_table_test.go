package executor

import (
	"strings"
	"testing"
)

// TestFromRegexpSplitToTable pins the FROM-clause SRF form of
// regexp_split_to_table(string, pattern[, flags]): FROM
// regexp_split_to_table(...) [AS alias(col)] [WITH ORDINALITY]. Unlike
// regexp_matches, output is a single plain text column (one row per
// substring), N matches always produce N+1 rows, an explicit 'g' flag raises
// 22023, and an invalid pattern raises 2201B (does not silently return zero
// rows). Mirrors TestFromRegexpMatches. M0134-0070 Round D.
func TestFromRegexpSplitToTable(t *testing.T) {
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

	eq("splits on pattern: N matches -> N+1 rows",
		collect(`SELECT * FROM regexp_split_to_table('hello world foo', '\s+')`),
		[]string{"hello", "world", "foo"})

	eq("no match: whole string as one row",
		collect(`SELECT * FROM regexp_split_to_table('nomatch', 'xyz')`),
		[]string{"nomatch"})

	// Column alias.
	rows := runQuery(t, ctx, `SELECT m FROM regexp_split_to_table('a,b,c', ',') AS t(m)`)
	if len(rows) != 3 || rows[0][0].StringValue() != "a" || rows[1][0].StringValue() != "b" || rows[2][0].StringValue() != "c" {
		t.Fatalf("aliased column: got %v", rows)
	}

	// Explicit 'g' flag: rejected with 22023.
	_, err := runQueryErr(t, ctx, `SELECT * FROM regexp_split_to_table('a1b2c3', '\d', 'g')`)
	if err == nil {
		t.Fatal("expected error for 'g' flag, got none")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "22023" {
		t.Fatalf("SQLSTATE=%s want 22023 (err=%v)", ee.Code, err)
	}
	if !strings.Contains(ee.Message, `regexp_split_to_table() does not support the "global" option`) {
		t.Fatalf("message=%q, want it to mention regexp_split_to_table()", ee.Message)
	}

	// Invalid pattern: raises 2201B, not a silent zero rows.
	_, err = runQueryErr(t, ctx, `SELECT * FROM regexp_split_to_table('a[b', '[')`)
	if err == nil {
		t.Fatal("expected error for invalid pattern, got none")
	}
	ee, ok = err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T, want *ExecError (err=%v)", err, err)
	}
	if ee.Code != "2201B" {
		t.Fatalf("SQLSTATE=%s want 2201B (err=%v)", ee.Code, err)
	}

	// WITH ORDINALITY appends a 1-based bigint ordinal.
	rows = runQuery(t, ctx, `SELECT * FROM regexp_split_to_table('a,b,c', ',') WITH ORDINALITY AS t(m, n)`)
	if len(rows) != 3 {
		t.Fatalf("WITH ORDINALITY: got %d rows, want 3 (rows=%v)", len(rows), rows)
	}
	for i, want := range []struct {
		m string
		n int64
	}{{"a", 1}, {"b", 2}, {"c", 3}} {
		n, _ := datumInt64(rows[i][1])
		if rows[i][0].StringValue() != want.m || n != want.n {
			t.Fatalf("WITH ORDINALITY row %d = (m=%q, n=%d), want (m=%q, n=%d)", i, rows[i][0].StringValue(), n, want.m, want.n)
		}
	}
}
