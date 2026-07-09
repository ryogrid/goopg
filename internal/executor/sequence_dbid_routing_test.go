package executor

import "testing"

// TestSerialSequenceDoesNotAliasAcrossDistinctDBOid covers M0122-0007 slice
// 4e's sequence-ownership item: seqRegistry (internal/executor/
// operators_sequence.go) used to be a process-global sync.Map keyed only by
// the (optionally schema-qualified) sequence name, with zero dbOid concept
// anywhere — so two same-named tables in distinct databases, each with an
// implicit SERIAL-column sequence (both named "items_id_seq"), silently
// shared one counter. A second CREATE TABLE items(...) on a distinct-dbOid
// connection would either collide with the first database's already-live
// sequence or, worse, its rows would advance the *other* database's counter.
// This proves the two are now genuinely independent.
func TestSerialSequenceDoesNotAliasAcrossDistinctDBOid(t *testing.T) {
	const otherDBOid = 7101
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// Default-dbOid database: create items, insert two rows.
	if err := runDDL(t, ctx, "CREATE TABLE items (id serial PRIMARY KEY, name text)"); err != nil {
		t.Fatalf("CREATE TABLE items (default dbOid): %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO items (name) VALUES ('a')"); err != nil {
		t.Fatalf("INSERT items (default dbOid) 1: %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO items (name) VALUES ('b')"); err != nil {
		t.Fatalf("INSERT items (default dbOid) 2: %v", err)
	}
	rows := runQueryUnderDBOid(t, ctx, "SELECT id FROM items ORDER BY id")
	if len(rows) != 2 || rows[0][0].Int != 1 || rows[1][0].Int != 2 {
		t.Fatalf("default-dbOid items.id = %v, want [1 2]", rows)
	}

	// Distinct-dbOid database: same table/column/sequence name. If seqRegistry
	// keyed the two databases' "items_id_seq" onto the same entry, this
	// CREATE TABLE's RegisterSequence call would silently reset the DEFAULT
	// database's already-live counter back to fresh (start-increment) as a
	// cross-database side effect — the collision symptom this test detects.
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE items (id serial PRIMARY KEY, name text)"); err != nil {
		t.Fatalf("CREATE TABLE items (distinct dbOid): %v", err)
	}

	// The default database's counter must NOT have been reset by the distinct
	// database's CREATE TABLE: the next INSERT must continue at 3.
	ctx.CurrentDatabaseOid = 0
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO items (name) VALUES ('c')"); err != nil {
		t.Fatalf("INSERT items (default dbOid) 3: %v", err)
	}
	rowsAfter := runQueryUnderDBOid(t, ctx, "SELECT id FROM items ORDER BY id")
	if len(rowsAfter) != 3 || rowsAfter[0][0].Int != 1 || rowsAfter[1][0].Int != 2 || rowsAfter[2][0].Int != 3 {
		t.Fatalf("default-dbOid items.id after distinct-dbOid CREATE TABLE = %v, want [1 2 3] (counter reset by a cross-database sequence-key collision)", rowsAfter)
	}

	// The distinct database's own sequence must start fresh at 1, not
	// continue the default database's counter.
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO items (name) VALUES ('x')"); err != nil {
		t.Fatalf("INSERT items (distinct dbOid): %v", err)
	}
	otherRows := runQueryUnderDBOid(t, ctx, "SELECT id FROM items")
	if len(otherRows) != 1 || otherRows[0][0].Int != 1 {
		t.Fatalf("distinct-dbOid items.id = %v, want [1] (sequence aliased onto the default database's counter)", otherRows)
	}
}

// TestDropTableDoesNotCascadeSequenceAcrossDistinctDBOid covers the other
// half of the same gap: DropSequencesOwnedByTable (called from DROP TABLE's
// cascade) used to scan every sequence in the process-global registry by bare
// owned-by name only, so DROP TABLE items in one database could delete a
// same-named unrelated table's owned sequence in a different database.
func TestDropTableDoesNotCascadeSequenceAcrossDistinctDBOid(t *testing.T) {
	const otherDBOid = 7102
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE items (id serial PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE items (default dbOid): %v", err)
	}

	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, "CREATE TABLE items (id serial PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE items (distinct dbOid): %v", err)
	}
	if err := runDMLUnderDBOid(t, ctx, "DROP TABLE items"); err != nil {
		t.Fatalf("DROP TABLE items (distinct dbOid): %v", err)
	}

	// The default database's items table and its owned sequence must survive
	// the distinct database's DROP TABLE.
	ctx.CurrentDatabaseOid = 0
	if err := runDMLUnderDBOid(t, ctx, "INSERT INTO items DEFAULT VALUES"); err != nil {
		t.Fatalf("INSERT INTO items (default dbOid) after distinct-dbOid DROP TABLE: %v", err)
	}
	rows := runQueryUnderDBOid(t, ctx, "SELECT id FROM items")
	if len(rows) != 1 || rows[0][0].Int != 1 {
		t.Fatalf("default-dbOid items.id after distinct-dbOid DROP TABLE = %v, want [1] (its owned sequence was cross-database cascade-dropped)", rows)
	}
}
