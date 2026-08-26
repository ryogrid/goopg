package sqlparser

import "testing"

// TestTransactionStatementParity — the transaction statement forms the
// upstream isolation specs use.
//
// `TRANSACTION` used to be a tx_mode rather than gram.y's `opt_transaction`
// prefix, so `BEGIN TRANSACTION ISOLATION LEVEL ...` consumed TRANSACTION as
// the first mode and then demanded a comma before ISOLATION — 22 spec steps.
// COMMIT/ROLLBACK PREPARED (two-phase commit) and ROLLBACK TO [SAVEPOINT] had
// no productions at all, and both lead keywords are routed.
//
// `ROLLBACK TO SAVEPOINT x` is the grammar's one deliberate ambiguity:
// SAVEPOINT is unreserved and therefore also a valid ColId, so `ROLLBACK TO
// SAVEPOINT` could name a savepoint "savepoint". goyacc shifts — keyword form
// wins — which is what PostgreSQL does. It is the pinned SAVEPOINT S/R.
func TestTransactionStatementParity(t *testing.T) {
	for _, q := range []string{
		"BEGIN", "BEGIN WORK", "BEGIN TRANSACTION",
		"BEGIN ISOLATION LEVEL SERIALIZABLE",
		"BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE, READ ONLY",
		"BEGIN READ ONLY",
		"START TRANSACTION",
		"START TRANSACTION ISOLATION LEVEL READ COMMITTED",
		"COMMIT", "COMMIT WORK", "COMMIT TRANSACTION", "END",
		"ROLLBACK", "ROLLBACK WORK", "ROLLBACK TRANSACTION", "ABORT",
		// two-phase commit
		"COMMIT PREPARED 's1'",
		"ROLLBACK PREPARED 's1'",
		// savepoint rollback
		"ROLLBACK TO f",
		"ROLLBACK TO SAVEPOINT f",
		// (legacy itself rejects ROLLBACK TRANSACTION TO — a legacy limit, not a gap)
	} {
		assertParity(t, q)
	}
}

// TestSetValueSurfaceParity — GUC values are NOT expressions. Legacy's
// parseSetValueAtoms accepts any keyword or literal, so `SET
// debug_parallel_query = on` is valid even though ON is reserved and can never
// be an a_expr. GUC names may also be dotted (`SET spec.session = 1`).
//
// `SET x = DEFAULT` and `SET x = 'default'` differ only by token KIND, which
// the grammar cannot see — hence one permissive value list plus
// setValueIsDefault() inspecting the token, rather than two alternatives that
// would reduce/reduce.
func TestSetValueSurfaceParity(t *testing.T) {
	for _, q := range []string{
		"SET debug_parallel_query = on",
		"SET debug_parallel_query = off",
		"SET enable_seqscan = true",
		"SET enable_seqscan = false",
		"SET spec.session = 1",
		"SET x = DEFAULT",
		"SET x = 'default'",
		"SET x = 1",
		"SET x TO 'v'",
		"SET SESSION x = 1",
		"SET LOCAL x = off",
		"SET work_mem = '64MB'",
		"SET search_path TO public, pg_catalog",
		"SET seq_page_cost = 0.1",
		"SET default_transaction_isolation = 'read committed'",
		"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE",
		"SET LOCAL TRANSACTION ISOLATION LEVEL SERIALIZABLE",
		"SHOW x", "SHOW ALL", "RESET x", "RESET ALL",
	} {
		assertParity(t, q)
	}
}
