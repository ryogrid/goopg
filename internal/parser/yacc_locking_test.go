package parser

import "testing"

// TestLockingClauseParity — `FOR UPDATE / FOR SHARE / FOR NO KEY UPDATE /
// FOR KEY SHARE [OF rel, ...] [NOWAIT | SKIP LOCKED]`.
//
// These were absent from the grammar ENTIRELY. SelectStmt.Locking and
// LockingClause have existed on the AST since M0021, and TODO's P1.5
// entry claimed "order/limit/FOR UPDATE" landed, but no production ever built
// one — `Locking` had zero occurrences in grammar/pg_grammar.y. SELECT is
// routed, so every row-locking SELECT was a hard 42601: 23 upstream isolation
// specs, and any application using SELECT ... FOR UPDATE.
//
// Both tail orders are covered because upstream's skip-locked specs use the
// second one: gram.y's select_no_parens allows the limit either before or
// after the locking clause.
func TestLockingClauseParity(t *testing.T) {
	for _, q := range []string{
		// strengths
		"SELECT * FROM t FOR UPDATE",
		"SELECT * FROM t FOR SHARE",
		"SELECT * FROM t FOR NO KEY UPDATE",
		"SELECT * FROM t FOR KEY SHARE",
		// OF list
		"SELECT * FROM t FOR UPDATE OF t",
		"SELECT * FROM t, u FOR UPDATE OF t, u",
		// wait policy
		"SELECT * FROM t FOR UPDATE NOWAIT",
		"SELECT * FROM t FOR UPDATE SKIP LOCKED",
		"SELECT * FROM t FOR SHARE NOWAIT",
		// combined with the rest of the tail, in both orders
		"SELECT * FROM t WHERE a = 1 FOR UPDATE",
		"SELECT * FROM t ORDER BY a FOR UPDATE",
		"SELECT * FROM t ORDER BY a LIMIT 1 FOR UPDATE",
		"SELECT * FROM t ORDER BY a FOR UPDATE SKIP LOCKED LIMIT 1",
		"SELECT * FROM t FOR UPDATE LIMIT 1",
		"SELECT * FROM t FOR SHARE SKIP LOCKED LIMIT 1",
		"SELECT * FROM t FOR UPDATE OF t NOWAIT LIMIT 1",
		// multiple clauses (upstream allows differing OF lists / policies)
		"SELECT * FROM t FOR UPDATE FOR SHARE",
		"SELECT * FROM t, u FOR UPDATE OF t FOR SHARE OF u",
		// the no-locking tail must stay identical
		"SELECT * FROM t LIMIT 1",
		"SELECT * FROM t ORDER BY a",
		"SELECT * FROM t OFFSET 2 LIMIT 1",
		"SELECT * FROM t ORDER BY a LIMIT 1 OFFSET 2",
	} {
		assertParity(t, q)
	}
}
