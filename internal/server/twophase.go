package server

// twophase.go — same-backend two-phase commit (2PC) for the isolation suite.
// M0118-0009 (prepared-transactions / prepared-transactions-cic / stats).
//
// PostgreSQL's two-phase commit splits COMMIT into a PREPARE phase (durably
// records the transaction's work under a global transaction identifier "gid"
// but does not finalise it) and a later COMMIT PREPARED / ROLLBACK PREPARED
// that finalises it — possibly from a different backend after a crash.
//
// goopg implements the *same-backend* subset the upstream isolation specs
// exercise: every spec PREPAREs and then COMMIT/ROLLBACK PREPAREs the gid from
// the SAME session, doing nothing on that connection in between. We therefore
// keep the prepared transaction OPEN as the connection's active transaction
// (its writes, heavyweight locks and SSI predicate-lock state all persist
// naturally) and route COMMIT PREPARED / ROLLBACK PREPARED through the
// canonical COMMIT / ROLLBACK code path. The commit-time SSI
// dangerous-structure check (which is what aborts one of the three overlapping
// SERIALIZABLE transactions in prepared-transactions.spec) thus fires at
// COMMIT PREPARED, exactly as in upstream.
//
// Deferred (not needed by any port spec): cross-backend COMMIT PREPARED, the
// pg_prepared_xacts catalog view, persistence of prepared state across a
// restart (pg_twophase), and detaching a prepared xact so the originating
// connection can run further transactions while it stays prepared.

import (
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// isTwoPhaseStmt reports whether stmt is a two-phase-commit statement handled
// at the server layer (the planner has no node for them, so they must be kept
// out of the plan-cache pre-plan path). M0118-0009.
func isTwoPhaseStmt(stmt parser.Stmt) bool {
	switch stmt.(type) {
	case *parser.PrepareTransactionStmt, *parser.CommitPreparedStmt, *parser.RollbackPreparedStmt:
		return true
	}
	return false
}

// execTwoPhaseStmt handles PREPARE TRANSACTION / COMMIT PREPARED / ROLLBACK
// PREPARED. It returns handled=true when stmt is one of those (err carries the
// protocol result); handled=false leaves stmt for the normal planning path.
func (s *Server) execTwoPhaseStmt(w *protocol.FrameWriter, ctx *executor.Context, stmt parser.Stmt, connTx *connTxState, autoCommitPtr *bool) (bool, error) {
	switch st := stmt.(type) {
	case *parser.PrepareTransactionStmt:
		return true, s.execPrepareTransaction(w, ctx, st, connTx, autoCommitPtr)
	case *parser.CommitPreparedStmt:
		return true, s.execFinalizePrepared(w, ctx, st.Gid, connTx, autoCommitPtr, &parser.CommitStmt{})
	case *parser.RollbackPreparedStmt:
		return true, s.execFinalizePrepared(w, ctx, st.Gid, connTx, autoCommitPtr, &parser.RollbackStmt{})
	default:
		return false, nil
	}
}

// execPrepareTransaction implements `PREPARE TRANSACTION 'gid'`. It marks the
// open transaction prepared (keeping it open) so a later COMMIT/ROLLBACK
// PREPARED can finalise it. PREPARE TRANSACTION is only valid inside an
// explicit, non-aborted transaction block. M0118-0009.
func (s *Server) execPrepareTransaction(w *protocol.FrameWriter, ctx *executor.Context, st *parser.PrepareTransactionStmt, connTx *connTxState, autoCommitPtr *bool) error {
	if connTx == nil || !connTx.InExplicit() {
		return s.writeQueryError(w, sqlstate.NoActiveSQLTransaction,
			"PREPARE TRANSACTION can only be used in transaction blocks")
	}
	if connTx.IsFailed() {
		// PREPARE TRANSACTION on an aborted transaction block silently rolls
		// back in PostgreSQL: EndTransactionBlock moves TBLOCK_ABORT →
		// TBLOCK_ABORT_END and PrepareTransactionBlock returns result=false, so
		// no error and no PREPARE result tag are sent — the transaction is just
		// discarded. Reuse the canonical ROLLBACK path (clears the failed state,
		// ends the txn); the subsequent COMMIT/ROLLBACK PREPARED of this gid then
		// reports "does not exist", exactly as upstream. M0118-0009.
		return s.executeOneSimpleStmt(w, ctx, &parser.RollbackStmt{}, connTx, autoCommitPtr)
	}
	// SSI dangerous-structure check at PREPARE time. Upstream's
	// PrepareTransaction calls PreCommit_CheckForSerializationFailure before the
	// prepare record is written; a failure aborts the transaction so the later
	// COMMIT PREPARED reports "does not exist". This is what makes the third
	// PREPARE in prepared-transactions.spec (against an already-prepared pivot)
	// fail on itself. On success the SerializableXact is marked PREPARED so a
	// later committer cannot doom it. M0118-0009.
	explicitTx := connTx.Tx()
	if explicitTx.Isolation == mvcc.IsolationSerializable && explicitTx.Handle != 0 {
		if ssiErr := s.cfg.TxnMgr.PrepareCheckForSerializationFailure(explicitTx.Handle); ssiErr != nil {
			return s.abortForPrepareSSIFailure(w, ctx, connTx, explicitTx, ssiErr)
		}
	}
	// Keep the transaction open; record the gid. The connection stays "in a
	// transaction block" from goopg's view, which is invisible to the isolation
	// tester (it does not inspect the ReadyForQuery status byte) and lets the
	// subsequent COMMIT/ROLLBACK PREPARED reuse the canonical finalisation path
	// with the transaction's session/locks fully wired.
	connTx.MarkPrepared(st.Gid)
	if autoCommitPtr != nil {
		*autoCommitPtr = false
	}
	return w.WriteCommandComplete("PREPARE TRANSACTION")
}

// abortForPrepareSSIFailure rolls back the transaction whose PREPARE failed the
// SSI dangerous-structure check and reports SQLSTATE 40001, mirroring the
// COMMIT-time SSI rollback in the simple-query dispatch path. The transaction
// is fully torn down (no prepared marker is set) so a subsequent COMMIT/ROLLBACK
// PREPARED of this gid reports "does not exist", exactly as upstream. M0118-0009.
func (s *Server) abortForPrepareSSIFailure(w *protocol.FrameWriter, ctx *executor.Context, connTx *connTxState, explicitTx mvcc.Transaction, ssiErr error) error {
	if sess := connTx.Session(); sess != nil {
		if rs := s.cfg.Catalog.Routines(); rs != nil {
			for _, r := range sess.TakePendingRoutineDrops() {
				_, _ = rs.Create(r, true)
			}
		}
	}
	_ = s.cfg.TxnMgr.Rollback(explicitTx)
	undoEnumDDLForRollback(connTx, s.cfg.Catalog)
	connTx.End()
	if ctx != nil && ctx.EndLocalTransaction != nil {
		ctx.EndLocalTransaction()
	}
	if ctx != nil {
		ctx.PendingEnumValues = nil
		ctx.PendingEnumRenames = nil
		ctx.PendingCreatedEnums = nil
		ctx.PendingCreatedComposites = nil
	}
	var ssiFields []protocol.ErrorField
	if sfe, ok := ssiErr.(*mvcc.SerializationFailureError); ok {
		if d := sfe.Detail(); d != "" {
			ssiFields = append(ssiFields,
				protocol.ErrorField{Code: protocol.FieldDetail, Value: d},
				protocol.ErrorField{Code: protocol.FieldHint, Value: "The transaction might succeed if retried."})
		}
	}
	return s.writeQueryError(w, "40001",
		"could not serialize access due to read/write dependencies among transactions",
		ssiFields...)
}

// execFinalizePrepared implements COMMIT PREPARED / ROLLBACK PREPARED. It
// validates the gid against the connection's prepared transaction, drops the
// prepared marker, then runs the supplied finalise statement (COMMIT or
// ROLLBACK) through the canonical executeOneSimpleStmt path so SSI checks,
// deferred catalog operations and NOTIFY publication all behave identically to
// a normal COMMIT/ROLLBACK. M0118-0009.
func (s *Server) execFinalizePrepared(w *protocol.FrameWriter, ctx *executor.Context, gid string, connTx *connTxState, autoCommitPtr *bool, finalize parser.Stmt) error {
	if connTx == nil || connTx.PreparedGid() != gid {
		// Same-backend only: a gid this connection did not prepare is treated as
		// non-existent (cross-backend prepared-xact lookup is deferred).
		return s.writeQueryError(w, sqlstate.UndefinedObject,
			"prepared transaction with identifier \""+gid+"\" does not exist")
	}
	connTx.ClearPrepared()
	return s.executeOneSimpleStmt(w, ctx, finalize, connTx, autoCommitPtr)
}
