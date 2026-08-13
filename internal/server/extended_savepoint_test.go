package server

import (
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// M0132-S10 — SAVEPOINT / RELEASE / ROLLBACK TO over the extended protocol.
//
// applyTransactionVerb (txn_verb.go) returns Handled=false for these three
// verbs, and the extended caller (dispatch_extended.go) falls through to
// executor.Build(node) → transactionOp → execSavepoint/execRelease/
// execRollbackTo — exactly the route the simple path takes (M0097-0023).
// This file is the proof obligation D5 names: the verbs must be handled
// explicitly (they are — via the executor), never a bare tag, and the
// sub-transaction semantics must hold across extended Executes.

// TestM0132S10_ExtendedSavepointRollbackTo drives a full block over the
// extended protocol with an intervening ROLLBACK TO: the second INSERT must be
// discarded while the first survives the COMMIT.
func TestM0132S10_ExtendedSavepointRollbackTo(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	steps := []struct{ name, sql string }{
		{"sp_begin", "BEGIN"},
		{"sp_i1", "INSERT INTO items VALUES (1, 'a')"},
		{"sp_sv", "SAVEPOINT f"},
		{"sp_i2", "INSERT INTO items VALUES (2, 'b')"},
		{"sp_rb", "ROLLBACK TO f"},
		{"sp_commit", "COMMIT"},
	}
	for _, s := range steps {
		if f := extendedStmt(t, conn, r, s.name, s.sql); hasError(f) {
			t.Fatalf("extended %q errored: %+v", s.sql, f)
		}
	}
	if got := countItems(t, conn, r); got != 1 {
		t.Errorf("after extended BEGIN/INSERT/SAVEPOINT/INSERT/ROLLBACK TO/COMMIT: %d rows, want 1", got)
	}
}

// TestM0132S10_ExtendedSavepointRelease covers the positive half: a RELEASE
// commits the savepoint's work into the outer transaction.
func TestM0132S10_ExtendedSavepointRelease(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	steps := []struct{ name, sql string }{
		{"rl_begin", "BEGIN"},
		{"rl_sv", "SAVEPOINT f"},
		{"rl_i1", "INSERT INTO items VALUES (1, 'a')"},
		{"rl_rel", "RELEASE f"},
		{"rl_commit", "COMMIT"},
	}
	for _, s := range steps {
		if f := extendedStmt(t, conn, r, s.name, s.sql); hasError(f) {
			t.Fatalf("extended %q errored: %+v", s.sql, f)
		}
	}
	if got := countItems(t, conn, r); got != 1 {
		t.Errorf("after extended BEGIN/SAVEPOINT/INSERT/RELEASE/COMMIT: %d rows, want 1", got)
	}
}

// TestM0132S10_ExtendedSavepointSelfVisibility pins the sub-transaction
// visibility that matters inside a live block: after ROLLBACK TO f, the
// rolled-back INSERT must be invisible to a later statement in the SAME block
// (not just after COMMIT).
func TestM0132S10_ExtendedSavepointSelfVisibility(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	for _, s := range []struct{ name, sql string }{
		{"v_begin", "BEGIN"},
		{"v_i1", "INSERT INTO items VALUES (1, 'a')"},
		{"v_sv", "SAVEPOINT f"},
		{"v_i2", "INSERT INTO items VALUES (2, 'b')"},
		{"v_rb", "ROLLBACK TO f"},
	} {
		if f := extendedStmt(t, conn, r, s.name, s.sql); hasError(f) {
			t.Fatalf("extended %q errored: %+v", s.sql, f)
		}
	}
	// SELECT inside the still-open block: the rolled-back row must be absent.
	frames := extendedStmt(t, conn, r, "v_sel", "SELECT * FROM items")
	if hasError(frames) {
		t.Fatalf("in-block extended SELECT errored: %+v", frames)
	}
	n := 0
	for _, f := range frames {
		if f.Type == protocol.MsgDataRow {
			n++
		}
	}
	if n != 1 {
		t.Errorf("in-block SELECT after ROLLBACK TO f returned %d rows, want 1", n)
	}
	if f := extendedStmt(t, conn, r, "v_rollback", "ROLLBACK"); hasError(f) {
		t.Fatalf("extended ROLLBACK errored: %+v", f)
	}
}

// TestM0132S10_ExtendedSavepointOutsideBlock pins the 25P01 error for a
// SAVEPOINT issued with no explicit block open.
func TestM0132S10_ExtendedSavepointOutsideBlock(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	frames := extendedStmt(t, conn, r, "o_sv", "SAVEPOINT f")
	if !errorContains(frames, "SAVEPOINT can only be used in transaction blocks") {
		t.Errorf("SAVEPOINT outside a block: want 25P01 message, got frames %+v", frames)
	}
	if !errorContains(frames, "25P01") {
		t.Errorf("SAVEPOINT outside a block: want SQLSTATE 25P01, got frames %+v", frames)
	}
}

// TestM0132S10_MixedSavepoint is the driver shape: simple BEGIN, extended
// SAVEPOINT/INSERT/ROLLBACK TO, simple COMMIT. pgx/lib/pq send argument-less
// verbs down the simple path and parameterised DML down the extended one.
func TestM0132S10_MixedSavepoint(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	r := extendedReader(t, conn)

	if f := simpleStmt(t, conn, r, "BEGIN"); hasError(f) {
		t.Fatalf("simple BEGIN errored: %+v", f)
	}
	if f := extendedStmt(t, conn, r, "m_sv", "SAVEPOINT f"); hasError(f) {
		t.Fatalf("extended SAVEPOINT errored: %+v", f)
	}
	if f := extendedStmt(t, conn, r, "m_i1", "INSERT INTO items VALUES (1, 'a')"); hasError(f) {
		t.Fatalf("extended INSERT errored: %+v", f)
	}
	if f := extendedStmt(t, conn, r, "m_rb", "ROLLBACK TO f"); hasError(f) {
		t.Fatalf("extended ROLLBACK TO errored: %+v", f)
	}
	if f := simpleStmt(t, conn, r, "COMMIT"); hasError(f) {
		t.Fatalf("simple COMMIT errored: %+v", f)
	}
	if got := countItems(t, conn, r); got != 0 {
		t.Errorf("after mixed BEGIN/SAVEPOINT/INSERT/ROLLBACK TO/COMMIT: %d rows, want 0", got)
	}
}
