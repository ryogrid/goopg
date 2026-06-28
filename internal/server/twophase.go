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
// For SERIALIZABLE transactions (prepared-transactions{,-cic}.spec) goopg keeps
// this same-backend keep-open subset: SSI predicate-lock / rw-conflict state is
// keyed by the transaction Handle and cannot be relocated to a dedicated slot
// without re-keying, and those specs never run further work on the originating
// connection between PREPARE and COMMIT/ROLLBACK PREPARED.
//
// For READ COMMITTED / REPEATABLE READ transactions (stats.spec's 2PC stat-drop
// permutations) goopg additionally supports DETACHING the prepared transaction:
// at PREPARE the transaction is relocated to a dedicated proc slot
// (mvcc.DetachToDedicatedSlot, the dummy-PGPROC analogue) and parked in the
// server's process-wide preparedXactStore, freeing the originating connection to
// run further transactions. A later COMMIT/ROLLBACK PREPARED — from the SAME or
// a DIFFERENT backend — looks the gid up in the store and finalises it through
// the canonical commit/rollback path with the executor context retargeted at the
// parked session/transaction.
//
// Deferred (not needed by any port spec): the pg_prepared_xacts catalog view,
// persistence of prepared state across a restart (pg_twophase), cross-backend
// finalisation of a SERIALIZABLE prepared xact, and full dummy-PGPROC lock
// hand-off.

import (
	"sync"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// preparedXactStore is the process-wide registry of detached (RC/RR) prepared
// transactions, keyed by gid. Each value is a parked connTxState carrying the
// prepared transaction's session, relocated transaction handle and finalisation
// state. M0118-0009 (stats — cross-backend two-phase commit).
type preparedXactStore struct {
	mu sync.Mutex
	m  map[string]*connTxState
}

func newPreparedXactStore() *preparedXactStore {
	return &preparedXactStore{m: make(map[string]*connTxState)}
}

// has reports whether a prepared transaction with this gid is registered.
func (p *preparedXactStore) has(gid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.m[gid]
	return ok
}

// put registers a parked prepared transaction. The caller has already verified
// the gid is free via has().
func (p *preparedXactStore) put(gid string, px *connTxState) {
	p.mu.Lock()
	p.m[gid] = px
	p.mu.Unlock()
}

// take removes and returns the parked prepared transaction for gid.
func (p *preparedXactStore) take(gid string) (*connTxState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	px, ok := p.m[gid]
	if ok {
		delete(p.m, gid)
	}
	return px, ok
}

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
	explicitTx := connTx.Tx()
	if explicitTx.Isolation == mvcc.IsolationSerializable {
		// SSI dangerous-structure check at PREPARE time. Upstream's
		// PrepareTransaction calls PreCommit_CheckForSerializationFailure before the
		// prepare record is written; a failure aborts the transaction so the later
		// COMMIT PREPARED reports "does not exist". This is what makes the third
		// PREPARE in prepared-transactions.spec (against an already-prepared pivot)
		// fail on itself. On success the SerializableXact is marked PREPARED so a
		// later committer cannot doom it. M0118-0009.
		if explicitTx.Handle != 0 {
			if ssiErr := s.cfg.TxnMgr.PrepareCheckForSerializationFailure(explicitTx.Handle); ssiErr != nil {
				return s.abortForPrepareSSIFailure(w, ctx, connTx, explicitTx, ssiErr)
			}
		}
		// Keep the transaction open on its slot; record the gid. The connection
		// stays "in a transaction block" from goopg's view (invisible to the
		// isolation tester, which does not inspect the ReadyForQuery status byte)
		// and the same-backend COMMIT/ROLLBACK PREPARED reuses the canonical
		// finalisation path with the transaction's session/locks/SSI state fully
		// wired. SERIALIZABLE specs never run further work on this connection
		// before finalising, so keeping the slot is safe.
		connTx.MarkPrepared(st.Gid)
		if autoCommitPtr != nil {
			*autoCommitPtr = false
		}
		return w.WriteCommandComplete("PREPARE TRANSACTION")
	}
	// READ COMMITTED / REPEATABLE READ: detach the transaction to a dedicated
	// proc slot and park it in the registry so the originating connection is
	// freed and a later COMMIT/ROLLBACK PREPARED (same OR different backend) can
	// finalise it. M0118-0009 (stats).
	if s.preparedXacts.has(st.Gid) {
		return s.writeQueryError(w, sqlstate.DuplicateObject,
			"transaction identifier \""+st.Gid+"\" is already in use")
	}
	newTx, err := s.cfg.TxnMgr.DetachToDedicatedSlot(explicitTx)
	if err != nil {
		// Relocation failed (e.g. no free slot): fall back to the same-backend
		// keep-open behaviour so the PREPARE still succeeds for a same-backend
		// finalisation.
		connTx.MarkPrepared(st.Gid)
		if autoCommitPtr != nil {
			*autoCommitPtr = false
		}
		return w.WriteCommandComplete("PREPARE TRANSACTION")
	}
	if sess := connTx.Session(); sess != nil {
		sess.RelocateTransaction(newTx)
	}
	px := connTx.DetachPrepared(st.Gid, newTx)
	s.preparedXacts.put(st.Gid, px)
	// M0118-0009 (`stats`, rung 7; design 0118-0131): move the originating
	// backend's staged relation-stat counters into a per-gid 2PC record so the
	// later COMMIT/ROLLBACK PREPARED — from any backend — folds them into that
	// backend's pending counters (AtPrepare_PgStat_Relations). The originating
	// backend's already-pending non-transactional scan counters stay and report
	// normally via its own flush.
	executor.PrepareRelStats(ctx, st.Gid)
	// The per-statement context's transaction-scoped pending enum state moved to
	// the parked holder; clear it so the dispatch write-back does not re-attach it
	// to the now-freed connection.
	if ctx != nil {
		ctx.PendingEnumValues = nil
		ctx.PendingEnumRenames = nil
		ctx.PendingCreatedEnums = nil
		ctx.PendingCreatedComposites = nil
	}
	// Leave *autoCommitPtr = false: the originating transaction now lives on its
	// dedicated slot (owned by the registry), so the dispatch end must NOT commit
	// or roll back the freed connection slot.
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
	// M0118-0009 (`stats`, rung 7): discard the failed transaction's staged
	// relation-stat counters via abort math (this path bypasses execRollback).
	executor.AbortRelStats(ctx)
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
	// Same-backend keep-open path (SERIALIZABLE): the originating connection still
	// holds the prepared transaction on its slot.
	if connTx != nil && connTx.PreparedGid() == gid {
		connTx.ClearPrepared()
		return s.executeOneSimpleStmt(w, ctx, finalize, connTx, autoCommitPtr)
	}
	// Detached path (RC/RR): look the gid up in the process-wide registry and
	// finalise the parked transaction through the canonical commit/rollback path,
	// with the executor context retargeted at the parked session/transaction.
	// Works regardless of which backend issues the COMMIT/ROLLBACK PREPARED.
	px, ok := s.preparedXacts.take(gid)
	if !ok {
		return s.writeQueryError(w, sqlstate.UndefinedObject,
			"prepared transaction with identifier \""+gid+"\" does not exist")
	}
	// M0118-0009 (`stats`, rung 7; design 0118-0131): fold the prepared 2PC
	// relation-stat record into THIS (finalising) backend's pending counters using
	// commit/abort math (pgstat_twophase_post{commit,abort}). ctx still carries the
	// finalising connection's session identity (only ctx.Session is retargeted
	// below), so the counts attach to the backend that flushes them next.
	_, isCommit := finalize.(*parser.CommitStmt)
	executor.FinalizePreparedRelStats(ctx, gid, isCommit)
	sess := px.Session()
	ctx.Session = sess
	ctx.Tx = px.Tx()
	if snap, serr := s.cfg.TxnMgr.SnapshotFor(px.Tx()); serr == nil {
		ctx.Snap = snap
	}
	ctx.PendingEnumValues = px.PendingEnumValues
	ctx.PendingEnumRenames = px.PendingEnumRenames
	ctx.PendingCreatedEnums = px.PendingCreatedEnums
	ctx.PendingCreatedComposites = px.PendingCreatedComposites
	ctx.TxnLockBackendID = px.LockBackendID
	// The parked holder owns its own session/lock lifecycle via px.End() inside
	// the canonical finalise path; the originating connection's SET LOCAL scope
	// is unrelated to this backend's context, so suppress EndLocalTransaction.
	ctx.BeginLocalTransaction = nil
	ctx.EndLocalTransaction = nil
	// Route through the canonical COMMIT/ROLLBACK handling with the parked holder
	// as the active transaction. A throwaway auto-commit flag keeps the finalise
	// from disturbing the issuing connection's own statement bookkeeping.
	var throwaway bool
	return s.executeOneSimpleStmt(w, ctx, finalize, px, &throwaway)
}
