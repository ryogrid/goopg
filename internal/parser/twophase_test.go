package parser

import "testing"

// TestParseTwoPhaseCommit verifies the PREPARE TRANSACTION / COMMIT PREPARED /
// ROLLBACK PREPARED grammar added for M0118-0009 (prepared-transactions), and
// that the prepared-statement PREPARE and plain COMMIT/ROLLBACK still parse to
// their original nodes (the keywords are shared / adjacent).
func TestParseTwoPhaseCommit(t *testing.T) {
	t.Run("PREPARE TRANSACTION", func(t *testing.T) {
		stmt := parseOne(t, "PREPARE TRANSACTION 's1'")
		pt, ok := stmt.(*PrepareTransactionStmt)
		if !ok {
			t.Fatalf("got %T, want *PrepareTransactionStmt", stmt)
		}
		if pt.Gid != "s1" {
			t.Fatalf("gid = %q, want s1", pt.Gid)
		}
	})

	t.Run("COMMIT PREPARED", func(t *testing.T) {
		stmt := parseOne(t, "COMMIT PREPARED 'a'")
		cp, ok := stmt.(*CommitPreparedStmt)
		if !ok {
			t.Fatalf("got %T, want *CommitPreparedStmt", stmt)
		}
		if cp.Gid != "a" {
			t.Fatalf("gid = %q, want a", cp.Gid)
		}
	})

	t.Run("ROLLBACK PREPARED", func(t *testing.T) {
		stmt := parseOne(t, "ROLLBACK PREPARED 'a'")
		rp, ok := stmt.(*RollbackPreparedStmt)
		if !ok {
			t.Fatalf("got %T, want *RollbackPreparedStmt", stmt)
		}
		if rp.Gid != "a" {
			t.Fatalf("gid = %q, want a", rp.Gid)
		}
	})

	// Regression: the shared/adjacent keywords must not divert the ordinary
	// statements into the 2PC nodes.
	t.Run("prepared statement PREPARE still works", func(t *testing.T) {
		stmt := parseOne(t, "PREPARE p1 AS SELECT 1")
		if _, ok := stmt.(*PrepareStmt); !ok {
			t.Fatalf("got %T, want *PrepareStmt", stmt)
		}
	})

	t.Run("plain COMMIT still works", func(t *testing.T) {
		if _, ok := parseOne(t, "COMMIT").(*CommitStmt); !ok {
			t.Fatalf("COMMIT did not parse to *CommitStmt")
		}
		if _, ok := parseOne(t, "COMMIT TRANSACTION").(*CommitStmt); !ok {
			t.Fatalf("COMMIT TRANSACTION did not parse to *CommitStmt")
		}
	})

	t.Run("plain ROLLBACK still works", func(t *testing.T) {
		if _, ok := parseOne(t, "ROLLBACK").(*RollbackStmt); !ok {
			t.Fatalf("ROLLBACK did not parse to *RollbackStmt")
		}
		if _, ok := parseOne(t, "ROLLBACK TO SAVEPOINT sp").(*RollbackToSavepointStmt); !ok {
			t.Fatalf("ROLLBACK TO SAVEPOINT did not parse to *RollbackToSavepointStmt")
		}
	})
}
