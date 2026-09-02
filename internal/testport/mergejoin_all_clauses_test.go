package testport

// mergejoin_all_clauses_test.go — a merge join must evaluate EVERY equi-clause,
// whether or not it uses each one as a merge key.
//
// Two instances of the same bug were fixed in take2. `generateMergeJoinPaths`
// trims its merge-clause list to the groups the outer path's ordering actually
// serves (`findMergeClausesForOuterPathkeys` returns a prefix of the groups),
// and the clauses in the UNMATCHED groups were dropped from the merge keys
// without being demoted into the residual — so nothing evaluated them at all.
//
//  1. the first candidate (joinpathsmergeouter.go:178) passed the original
//     `residual` through untouched;
//  2. the truncation search's two candidates then re-derived their residual
//     from that same stale list, so demoting a clause cut from the ALREADY
//     trimmed list could not re-add a clause that was never in it.
//
// Both are silent WRONG ANSWERS, not missed optimisations, and both return the
// CORRECT ROW COUNT for the shape they produce — TPC-H Q9 returned its correct
// 175 rows while summing 4.02x the right answer. A row-count gate cannot see
// this class; only a value comparison can.
//
// The guard here is an invariant rather than a pinned constant: the same query
// must produce the same answer whichever join method the planner picks. That
// makes it independent of the data and of which method wins today, and it fails
// for exactly the reason the bug exists.

import (
	"database/sql"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestPort_MergeJoinEvaluatesEveryEquiClause(t *testing.T) {
	c := newCluster(t, "mergejoin_all_clauses")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	db, err := sql.Open("postgres", buildDSN(t, c))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Two equi-clauses. The index on (k) alone gives the outer an ordering that
	// serves the k clause and NOT the s clause, which is the shape that trims
	// the merge-clause list — TPC-H Q9's `lineitem x partsupp` in miniature.
	for _, stmt := range []string{
		`CREATE TABLE mjq_a (k int, s int, v int)`,
		`CREATE TABLE mjq_b (k int, s int)`,
		`INSERT INTO mjq_a SELECT i%100, i%7, i FROM generate_series(1,5000) i`,
		`INSERT INTO mjq_b SELECT i%100, i%7 FROM generate_series(1,5000) i`,
		`CREATE INDEX ON mjq_a (k)`,
		`CREATE INDEX ON mjq_b (k)`,
		`ANALYZE mjq_a`,
		`ANALYZE mjq_b`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}

	const q = `SELECT count(*), sum(a.v) FROM mjq_a a, mjq_b b WHERE a.k = b.k AND a.s = b.s`

	var hashCount, hashSum int64
	if err := db.QueryRow(q).Scan(&hashCount, &hashSum); err != nil {
		t.Fatal(err)
	}
	if hashCount == 0 {
		t.Fatal("fixture produced no rows; the comparison below would be vacuous")
	}

	// enable_hashjoin is a per-session GUC, so the merge arm needs its own
	// pinned connection. (That GUC was itself inert until take2 P2-05 — this
	// test could not have been written before it was wired.)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(t.Context(), `SET enable_hashjoin = off`); err != nil {
		t.Fatal(err)
	}

	var mergeCount, mergeSum int64
	if err := conn.QueryRowContext(t.Context(), q).Scan(&mergeCount, &mergeSum); err != nil {
		t.Fatal(err)
	}

	if mergeCount != hashCount || mergeSum != hashSum {
		t.Errorf("the join method changed the ANSWER: hash gave (count=%d, sum=%d), "+
			"merge gave (count=%d, sum=%d) — a merge join that does not key on "+
			"every equi-clause must still evaluate the rest as a filter",
			hashCount, hashSum, mergeCount, mergeSum)
	}
}
