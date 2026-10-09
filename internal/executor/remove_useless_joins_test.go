package executor

import (
	"strings"
	"testing"
)

// TestRemoveUselessLeftJoins pins M0146-0005dl — PG's remove_useless_joins for
// a LEFT JOIN: a nullable side that is unique for the ON equalities and read
// nowhere else is dropped from the plan, with the same rows as the join. Every
// case PG keeps the join for keeps it here too (verified on PG 18.3).
func TestRemoveUselessLeftJoins(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE rj_a (id int, k1 int, k2 int, v int)")
	runSQL(t, ctx, "CREATE TABLE rj_b (k1 int, k2 int, w int, PRIMARY KEY (k1, k2))")
	runSQL(t, ctx, "CREATE TABLE rj_c (k1 int, w int)")
	runSQL(t, ctx, "CREATE TABLE rj_d (dk int PRIMARY KEY, dv int)")
	runSQL(t, ctx, "INSERT INTO rj_a SELECT g, g % 50, g % 7, g FROM generate_series(1, 2000) g")
	runSQL(t, ctx, "INSERT INTO rj_b SELECT g % 50, g / 50, g FROM generate_series(1, 300) g")
	runSQL(t, ctx, "INSERT INTO rj_c SELECT g % 50, g FROM generate_series(1, 300) g")
	runSQL(t, ctx, "INSERT INTO rj_d SELECT g, g FROM generate_series(1, 300) g")

	for _, c := range []struct {
		name    string
		sql     string
		removed bool
	}{
		{"pk covered, unread", "SELECT count(*), sum(a.v) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2", true},
		{"extra ON restriction", "SELECT count(*), sum(a.v) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2 AND b.w > 5", true},
		{"unqualified columns", "SELECT count(*), sum(v) FROM rj_a a LEFT JOIN rj_d ON dk = a.v", true},
		{"two removable in a chain", "SELECT count(*), sum(a.v) FROM rj_a a LEFT JOIN rj_d d1 ON d1.dk = a.v LEFT JOIN rj_d d2 ON d2.dk = a.id", true},
		{"a later join reads b, then both go", "SELECT count(*) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2 LEFT JOIN rj_d d ON d.dk = b.w", true},
		{"key only partly covered", "SELECT count(*), sum(a.v) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1", false},
		{"no unique key", "SELECT count(*), sum(a.v) FROM rj_a a LEFT JOIN rj_c c ON c.k1 = a.k1", false},
		{"b read in the target list", "SELECT count(*), sum(b.w) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2", false},
		{"b read in WHERE", "SELECT count(*) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2 WHERE b.w IS NULL", false},
		{"b read by a later ON, kept", "SELECT count(*), sum(d.dv) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2 LEFT JOIN rj_d d ON d.dk = b.w", false},
		{"b read by a correlated sublink", "SELECT count(*) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2 WHERE EXISTS (SELECT 1 FROM rj_c c WHERE c.w = b.w)", false},
		{"whole-row reference", "SELECT count(b) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2", false},
		{"inner join is not removed", "SELECT count(*) FROM rj_a a JOIN rj_d d ON d.dk = a.v", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := explainText(t, ctx, c.sql)
			hasJoin := strings.Contains(plan, "Join") || strings.Contains(plan, "Nested Loop")
			if hasJoin == c.removed {
				t.Fatalf("join removed=%v, want %v:\n%s", !hasJoin, c.removed, plan)
			}
		})
	}
	// The removal must not change a row: the same query with an ON clause
	// the proof cannot see through (a.k2 + 0) keeps the join.
	got := renderRows(runSQL(t, ctx, "SELECT count(*), sum(a.v) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2"))
	want := renderRows(runSQL(t, ctx, "SELECT count(*), sum(a.v) FROM rj_a a LEFT JOIN rj_b b ON b.k1 = a.k1 AND b.k2 = a.k2 + 0"))
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("removed join returned %v, kept join %v", got, want)
	}
}
