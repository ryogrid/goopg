package executor

// M0146 bpchar hash-parity pins: every hashing/dedup surface must key on
// the bcTruelen image of a bpchar-typed value, exactly as PG's hashbpchar
// and the *chareq/bpcharlt comparators ignore trailing blanks. A byte-exact
// key splits one PG group into per-padding-width fragments — the class that
// made `char(20) 'x'` and `char(5) 'x'` never meet in GROUP BY, DISTINCT,
// SetOp dedup, hashed IN, window partitions and correlated-subquery maps.
//
// Every witness below mixes padding widths through UNION arms of different
// declared typmods; PG's answers were captured live on 18.3.

import (
	"context"
	"testing"
)

// bpcharMixedValues runs sql and joins formatted cells row-major with a
// stable ordering so the assertion is exact.
func bpcharMixedValues(t *testing.T, ctx *Context, sql string) string {
	t.Helper()
	return bpcharValues(t, ctx, sql)
}

// TestBpcharGroupByMixedWidths: GROUP BY over a UNION ALL mixing char(20)
// and char(5) arms. PG (hashagg over bpchar) merges all 'x' paddings into
// one group; a byte-exact key splits them.
func TestBpcharGroupByMixedWidths(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	got := bpcharMixedValues(t, ctx,
		`SELECT count(*) FROM (
			SELECT v FROM (SELECT 'x'::char(20) v UNION ALL SELECT 'x'::char(5)
				UNION ALL SELECT 'y'::char(10)) s
			GROUP BY v) g`)
	if got != "2" {
		t.Fatalf("GROUP BY mixed-width bpchar group count: got %q, want \"2\" (x, y)", got)
	}

	// Explicit counts per group, keyed by a trimmed projection so the
	// padding-width representative doesn't matter.
	got = bpcharMixedValues(t, ctx,
		`SELECT btrim(v), count(*) FROM (
			SELECT 'x'::char(20) v UNION ALL SELECT 'x'::char(5)
			UNION ALL SELECT 'y'::char(10)) s
		GROUP BY v ORDER BY 1`)
	if got != "x|2,y|1" {
		t.Fatalf("GROUP BY mixed-width bpchar: got %q, want \"x|2,y|1\"", got)
	}
}

// TestBpcharDistinctMixedWidths covers the hash-distinct path
// (SELECT DISTINCT over a union of different-width arms) plus
// COUNT(DISTINCT) — both must see one 'x'.
func TestBpcharDistinctMixedWidths(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, tc := range []struct{ name, sql, want string }{
		{"plain-distinct",
			`SELECT count(*) FROM (SELECT DISTINCT v FROM (
				SELECT 'x'::char(20) v UNION ALL SELECT 'x'::char(5)) s) q`,
			"1"},
		{"count-distinct",
			`SELECT count(DISTINCT v) FROM (
				SELECT 'x'::char(20) v UNION ALL SELECT 'x'::char(5)) s`,
			"1"},
		{"distinct-on",
			`SELECT count(*) FROM (SELECT DISTINCT ON (v) v FROM (
				SELECT 'x'::char(20) v UNION ALL SELECT 'x'::char(5)) s
				ORDER BY v) q`,
			"1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bpcharMixedValues(t, ctx, tc.sql); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBpcharSetOpMixedWidths: UNION/INTERSECT/EXCEPT dedup under bpchar
// semantics — 'x'::char(20) and 'x'::char(5) are the same row.
func TestBpcharSetOpMixedWidths(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, tc := range []struct{ name, sql, want string }{
		{"union",
			`SELECT count(*) FROM (SELECT 'x'::char(20) v UNION SELECT 'x'::char(5)) s`,
			"1"},
		{"intersect",
			`SELECT count(*) FROM (SELECT 'x'::char(20) v INTERSECT SELECT 'x'::char(5)) s`,
			"1"},
		{"except",
			`SELECT count(*) FROM (SELECT 'x'::char(20) v EXCEPT SELECT 'x'::char(5)) s`,
			"0"},
		{"except-distinct-value",
			`SELECT count(*) FROM (SELECT 'z'::char(20) v EXCEPT SELECT 'x'::char(5)) s`,
			"1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bpcharMixedValues(t, ctx, tc.sql); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBpcharRecursiveCteDedup: WITH RECURSIVE ... UNION (not ALL) dedups the
// fixpoint rows under the output column type — bpchar dedup trims.
func TestBpcharRecursiveCteDedup(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.Ctx = context.Background() // recursiveUnionOp polls ctx.Ctx.Err() per iteration

	got := bpcharMixedValues(t, ctx,
		`SELECT count(*) FROM (
			WITH RECURSIVE t(v) AS (SELECT 'x'::char(20) UNION SELECT 'x'::char(5))
			SELECT * FROM t) q`)
	if got != "1" {
		t.Fatalf("recursive CTE UNION dedup: got %q, want \"1\"", got)
	}
}

// TestBpcharWindowPartition: PARTITION BY a bpchar expression merges
// padding-width variants into one partition, so row_number never hands out
// a second 1.
func TestBpcharWindowPartition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	got := bpcharMixedValues(t, ctx,
		`SELECT count(*) FROM (
			SELECT v, row_number() OVER (PARTITION BY v ORDER BY v) rn
			FROM (SELECT 'x'::char(20) v UNION ALL SELECT 'x'::char(5)) s) q
		WHERE rn = 1`)
	if got != "1" {
		t.Fatalf("window PARTITION BY bpchar: got %q, want \"1\"", got)
	}
}

// TestBpcharOrderByBlankInsensitive: bpcharlt compares the bcTruelen image.
// 'a ' and 'a!' pad to 'a  '/'a! ' under char(3); byte order would place
// 'a!' first ('!'< ' '), trimmed order places 'a' first — PG emits 'a','a!'.
func TestBpcharOrderByBlankInsensitive(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	got := bpcharMixedValues(t, ctx,
		`SELECT btrim(v) FROM (SELECT 'a '::char(3) v UNION ALL SELECT 'a!'::char(3)) s
		ORDER BY v`)
	if got != "a,a!" {
		t.Fatalf("ORDER BY bpchar: got %q, want \"a,a!\"", got)
	}
}

// TestBpcharRowInSubplan: multi-column IN against a bpchar inner — both the
// hashed tuple path and the linear fallback must agree the padded operand
// matches.
func TestBpcharRowInSubplan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	got := bpcharMixedValues(t, ctx,
		`SELECT count(*) FROM (
			SELECT 'x'::char(20) a, 'y'::char(20) b
			UNION ALL SELECT 'z'::char(20), 'w'::char(20)) s
		WHERE (a, b) IN (SELECT 'x'::char(5), 'y'::char(5))`)
	if got != "1" {
		t.Fatalf("row IN over bpchar: got %q, want \"1\"", got)
	}
}

// TestBpcharCorrScalarHash: the correlated-scalar hash path — inner join
// column bpchar, outer bpchar — must trim both sides. Probed live during
// M0146 as the Q41 shape; pinned here so the hash-map arm cannot regress
// independently of the linear one.
func TestBpcharCorrScalarHash(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	stmts := []string{
		"CREATE TABLE cor_a (m char(20), v int)",
		"CREATE TABLE cor_b (m char(5), v int)",
		"INSERT INTO cor_a VALUES ('m', 1), ('n', 2)",
		"INSERT INTO cor_b VALUES ('m', 10), ('m', 11), ('n', 20)",
	}
	for _, s := range stmts {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("fixture %q: %v", s, err)
		}
	}
	// 'm' matches twice under bpchareq regardless of the 20-vs-5 padding.
	got := bpcharMixedValues(t, ctx,
		`SELECT a.v, (SELECT count(*) FROM cor_b b WHERE b.m = a.m)
		FROM cor_a a ORDER BY a.v`)
	if got != "1|2,2|1" {
		t.Fatalf("correlated bpchar scalar subquery: got %q, want \"1|2,2|1\"", got)
	}
}

// TestBpcharTextArmKeepsSpaces is the negative control: a text-typed UNION
// arm's trailing spaces are data, never padding — 'x  '::text and
// 'x'::text dedup to two rows under UNION DISTINCT and stay unequal under
// `=`.
func TestBpcharTextArmKeepsSpaces(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, tc := range []struct{ name, sql, want string }{
		{"union-distinct-text",
			`SELECT count(*) FROM (SELECT 'x  '::text v UNION SELECT 'x'::text) s`,
			"2"},
		// PG resolves char(5) UNION ALL text to unbounded bpchar (measured
		// on 18.3: pg_typeof = character), so both arms compare under
		// bpchareq and 'x  ' meets 'x'.
		{"text-vs-bpchar",
			`SELECT count(*) FROM (SELECT 'x'::char(5) v UNION ALL SELECT 'x  '::text) s
			WHERE v = 'x'::text`,
			"2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bpcharMixedValues(t, ctx, tc.sql); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
