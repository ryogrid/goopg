package server

import (
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// M0132-S2: the BEGIN/COMMIT/ROLLBACK state machine, extracted verbatim from
// the simple-query dispatch arm that used to live inline in
// executeOneSimpleStmt (dispatch.go, "Transaction verbs:" block).
//
// Why an extraction and not a second implementation on the extended path:
// the arm carries a sequence no re-derivation would reproduce — the
// COMMIT-in-failed-block → ROLLBACK rule, the inline deferred FK/UNIQUE/
// EXCLUDE checks that exist precisely because this dispatch bypasses
// transactionOp.execCommit, the SSI pre-commit check, the deferred
// DROP INDEX / ATTACH PARTITION / INHERIT / DROP TABLE / DROP FUNCTION
// appliers, the enum-DDL undo, the Begin/EndLocalTransaction hooks and the
// pending enum/composite/range-type queues. M0132-S4's obligation ("prove
// the extended COMMIT runs the deferred sequence") is discharged by this
// being one function rather than two.
//
// Two asymmetries the signature absorbs:
//
//   - the simple path *promotes* a transaction that dispatch entry already
//     began, while the extended path reaches the verb before beginning
//     anything — so the transaction travels in ctx.Tx and the promotion is
//     conditional on autoCommitPtr being non-nil;
//   - the simple arm wrote its own wire frames. The helper writes none: it
//     returns what happened and each caller renders it in its own protocol's
//     shape (CommandComplete + ReadyForQuery on the simple path; the
//     extended path's own framing).
//
// This file is a pure refactor: the behaviour of the simple path is
// unchanged, which is the precondition M0132-S2 states before any new
// extended behaviour is switched on.

// txnVerbError is an error the caller must render as an ErrorResponse in its
// own protocol shape, carrying the SQLSTATE and the optional DETAIL/HINT/
// POSITION fields the inline arm used to pass to writeQueryError.
type txnVerbError struct {
	Code   sqlstate.Code
	Msg    string
	Fields []protocol.ErrorField
}

func (e *txnVerbError) Error() string { return e.Msg }

// txnVerbOutcome is the decision applyTransactionVerb reached. Handled=false
// means the verb is not one this state machine owns (SAVEPOINT, ROLLBACK TO,
// RELEASE) and the caller must fall through to the executor, exactly as the
// inline arm did (M0097-0023).
type txnVerbOutcome struct {
	Handled bool
	// Warn asks the caller to emit the 25P01 "there is no transaction in
	// progress" WARNING before the command tag — PG's response to COMMIT or
	// ROLLBACK outside a block.
	Warn bool
	// Tag is the CommandComplete tag to write when Err is nil. Note it is
	// not always transactionTag(verb): a COMMIT in a failed block reports
	// "ROLLBACK".
	Tag string
	Err *txnVerbError
}

// noTransactionInProgressNotice is the WARNING PG emits for COMMIT/ROLLBACK
// issued outside a transaction block.
func noTransactionInProgressNotice() []protocol.ErrorField {
	return []protocol.ErrorField{
		{Code: protocol.FieldSeverity, Value: "WARNING"},
		{Code: protocol.FieldSeverityNonLocal, Value: "WARNING"},
		{Code: protocol.FieldSQLState, Value: "25P01"},
		{Code: protocol.FieldMessage, Value: "there is no transaction in progress"},
	}
}

// endExplicitBlock performs the teardown every terminal path of the arm
// shares: undo enum DDL, release the per-connection explicit state, fire the
// EndLocalTransaction hook and clear the pending type-DDL queues. The inline
// arm repeated this block five times; keeping it one function is what makes
// the five paths verifiably identical.
func (s *Server) endExplicitBlock(ctx *executor.Context, connTx *connTxState) {
	undoEnumDDLForRollback(connTx, s.cfg.Catalog, ctx.CurrentDatabaseOid)
	connTx.End()
	if ctx.EndLocalTransaction != nil {
		ctx.EndLocalTransaction()
	}
	ctx.PendingEnumValues = nil
	ctx.PendingEnumRenames = nil
	ctx.PendingCreatedEnums = nil
	ctx.PendingCreatedComposites = nil
	ctx.PendingCreatedRangeTypes = nil
}

// abortedBlockMessage is the 25P02 text PostgreSQL raises for any statement
// issued while the current transaction block is aborted.
const abortedBlockMessage = "current transaction is aborted, commands ignored until end of transaction block"

// allowedInAbortedBlock reports whether a statement may run while the block is
// in the failed (25P02) state. PostgreSQL rejects EVERYTHING except the verbs
// that end (or unwind) the block — a constant `SELECT 1`, a `SHOW`, and a
// `SET` included; see xact.c's AbortCurrentTransaction / postgres.c's
// TBLOCK_ABORT handling.
//
// upper is the whitespace-normalised, upper-cased statement text (the form
// both protocols' fast-path switches already compute). Matching on text rather
// than on a parsed node is what lets the gate sit AHEAD of the fast paths that
// answer `SELECT 1` / `SHOW` / `SET` without ever parsing — the hole
// M0132-S1 found on the simple path and pinned in
// TestM0132S1_ConstantSelectBypassesTheAbortedBlockGate.
//
// The two-phase verbs are allowed for the reason dispatch.go's per-statement
// gate allows them: PG's PREPARE TRANSACTION on an aborted block silently
// rolls back, and COMMIT/ROLLBACK PREPARED of an unknown gid reports "does not
// exist" rather than 25P02.
func allowedInAbortedBlock(upper string) bool {
	for _, p := range []string{"COMMIT", "ROLLBACK", "ABORT", "END", "PREPARE TRANSACTION"} {
		if upper == p || strings.HasPrefix(upper, p+" ") || strings.HasPrefix(upper, p+";") {
			return true
		}
	}
	return false
}

// failExplicitBlock marks an open explicit block aborted after an error.
// PostgreSQL aborts the block from its top-level error handler (postgres.c),
// so ANY error inside a block aborts it — parse and plan errors included, not
// only executor errors. M0132-S5.
func failExplicitBlock(connTx *connTxState) {
	if connTx != nil && connTx.InExplicit() {
		connTx.Fail()
	}
}

// verbWarnFields renders the state machine's Warn flag as the NoticeResponse
// field set the extended protocol carries on extendedQueryResult. Nil when
// the outcome carries no warning.
func verbWarnFields(out txnVerbOutcome) []protocol.ErrorField {
	if !out.Warn {
		return nil
	}
	return noTransactionInProgressNotice()
}

// extendedQueryErrorFromVerb renders a txnVerbError in the extended
// protocol's error shape. The structured DETAIL/HINT/POSITION fields the
// deferred-constraint and SSI failures carry would otherwise be dropped —
// they are the whole reason the state machine returns a typed error instead
// of a bare `error`.
func extendedQueryErrorFromVerb(ve *txnVerbError) *extendedQueryError {
	qerr := &extendedQueryError{Code: ve.Code, Message: ve.Msg}
	for _, f := range ve.Fields {
		switch f.Code {
		case protocol.FieldDetail:
			qerr.Detail = f.Value
		case protocol.FieldHint:
			qerr.Hint = f.Value
		case protocol.FieldPosition:
			if p, err := strconv.Atoi(f.Value); err == nil && p > 0 {
				qerr.Position = p
			}
		}
	}
	return qerr
}

// writeBackConnTxState persists the per-connection state a statement may have
// mutated back onto the connection, so it survives to the next message. The
// simple path does exactly this at the foot of its per-statement loop
// (dispatch.go); before M0132-S2 the extended path had no explicit block to
// carry state across, so it did not need to.
func (s *Server) writeBackConnTxState(ctx *executor.Context, connTx *connTxState) {
	if connTx == nil {
		return
	}
	if ctx.TempTableShadows != nil {
		connTx.TempTableShadows = ctx.TempTableShadows
	}
	// Pending type-DDL undo state is written back ONLY while an explicit
	// transaction is open — see dispatch.go's identical guard for why an
	// autocommit statement must not leak its pending set onto the connection.
	if connTx.InExplicit() {
		connTx.PendingEnumValues = ctx.PendingEnumValues
		connTx.PendingEnumRenames = ctx.PendingEnumRenames
		connTx.PendingCreatedEnums = ctx.PendingCreatedEnums
		connTx.PendingCreatedComposites = ctx.PendingCreatedComposites
		connTx.PendingCreatedRangeTypes = ctx.PendingCreatedRangeTypes
	}
}

// applyTransactionVerb drives the per-connection explicit transaction state
// machine for BEGIN/COMMIT/ROLLBACK (M0096-0005). BEGIN promotes the current
// auto-commit transaction into a persistent explicit one; COMMIT/ROLLBACK
// finalise it and release the session-level state.
func (s *Server) applyTransactionVerb(ctx *executor.Context, connTx *connTxState, txNode *planner.Transaction, autoCommitPtr *bool) txnVerbOutcome {
	switch txNode.Verb {
	case planner.TxBegin:
		if connTx != nil && !connTx.InExplicit() && autoCommitPtr != nil {
			// Promote the current auto-commit transaction to explicit.
			*autoCommitPtr = false
			// M0104-0008: honour `BEGIN ISOLATION LEVEL <level>`. The
			// auto-commit tx allocated at dispatch entry was created at
			// the session default (typically READ COMMITTED); when the
			// BEGIN carries an explicit isolation level we must replace
			// it with a fresh tx at the requested level so SSI hooks
			// (`ssiActive` predicates on `tx.Isolation`) fire correctly
			// for the remainder of the explicit block.
			if txNode.IsolationLevel != "" {
				parsedLvl, perr := mvcc.ParseIsolationLevel(txNode.IsolationLevel)
				if perr != nil {
					return txnVerbOutcome{Handled: true, Err: &txnVerbError{Code: sqlstate.SyntaxError, Msg: perr.Error()}}
				}
				if parsedLvl != ctx.Tx.Isolation {
					// Roll back the placeholder auto-commit tx so it does
					// not consume an XID / leak SSI bookkeeping.
					_ = s.cfg.TxnMgr.Rollback(ctx.Tx)
					newTx, berr := s.cfg.TxnMgr.Begin(parsedLvl, ctx.ProcNum)
					if berr != nil {
						return txnVerbOutcome{Handled: true, Err: &txnVerbError{Code: sqlstate.SystemError, Msg: berr.Error()}}
					}
					ctx.Tx = newTx
					// PG-parity: for RR/SSI the snapshot is captured at the FIRST
					// real statement after BEGIN, not at BEGIN time. For RC, the
					// snapshot is refreshed per-statement anyway, so timing does
					// not matter. Leaving state.firstSnapshot unset here allows
					// the per-dispatch SnapshotFor call at line 171 to capture it
					// at first-statement time. M0100-0001.
					if parsedLvl == mvcc.IsolationReadCommitted {
						snap, serr := s.cfg.TxnMgr.SnapshotFor(newTx)
						if serr != nil {
							_ = s.cfg.TxnMgr.Rollback(newTx)
							return txnVerbOutcome{Handled: true, Err: &txnVerbError{Code: sqlstate.SystemError, Msg: serr.Error()}}
						}
						ctx.Snap = snap
					}
				}
			}
			// Capture the throwaway autocommit session's (if any) pending
			// DDL-create undo list BEFORE connTx.Begin() lazily allocates the
			// real BasicSession below — otherwise a `CREATE TABLE t1(...);
			// BEGIN; ...; ROLLBACK` compound batch silently drops t1's undo
			// entry: the throwaway session that recorded it (wired at dispatch
			// entry because no explicit transaction existed yet) is discarded
			// in favour of connTx's brand-new session, so a later ROLLBACK in
			// the same message can no longer undo t1 via ProcessRollbackUndos
			// (root-0024 residual: "compound batch still loses t1's undo entry").
			var priorDDLCreates []executor.DDLUndoEntry
			if bs, ok := ctx.Session.(*executor.BasicSession); ok && bs != nil {
				priorDDLCreates = bs.TakePendingDDLCreates()
			}
			connTx.Begin(ctx.Tx)
			// connTx.Begin lazily creates the BasicSession; when BEGIN is the
			// first statement of a multi-statement simple-query message the
			// session did not exist at dispatch entry (ectx.Session was wired
			// nil), so a later same-batch SAVEPOINT/transaction op would fail
			// with "transaction statements require Session in Context". Re-wire
			// the now-live session for the remainder of the batch. M0118-0009.
			if sess := connTx.Session(); sess != nil {
				ctx.Session = sess
				for _, e := range priorDDLCreates {
					sess.RecordDDLCreate(e)
				}
			}
			// Propagate READ ONLY / READ WRITE mode from START TRANSACTION / BEGIN.
			if connTx.Session() != nil {
				connTx.Session().SetReadOnlyTxn(txNode.ReadOnly)
			}
			// Record the declared READ ONLY / DEFERRABLE modes on the SSI
			// xact so the snapshot path applies PostgreSQL's GetSafeSnapshot
			// deferral for a SERIALIZABLE READ ONLY DEFERRABLE transaction.
			// No-op for RC/RR. M0118-0001 (read-only-anomaly-3).
			if ctx.Tx.Isolation == mvcc.IsolationSerializable {
				s.cfg.TxnMgr.MarkSerializableModes(ctx.Tx.Handle, txNode.ReadOnly, txNode.Deferrable)
			}
			if ctx.BeginLocalTransaction != nil {
				ctx.BeginLocalTransaction()
			}
		}
		return txnVerbOutcome{Handled: true, Tag: transactionTag(txNode.Verb)}
	case planner.TxCommit:
		if connTx != nil && connTx.InExplicit() {
			// COMMIT in a failed transaction → PostgreSQL semantics: issue
			// a WARNING and ROLLBACK instead of committing. M0100-0005.
			if connTx.IsFailed() {
				// COMMIT in a failed transaction block → ROLLBACK (PG semantics).
				_ = s.cfg.TxnMgr.Rollback(connTx.Tx())
				s.endExplicitBlock(ctx, connTx)
				return txnVerbOutcome{Handled: true, Tag: "ROLLBACK"}
			}
			explicitTx := connTx.Tx()
			// Run FK constraint checks deferred to COMMIT (DEFERRABLE
			// INITIALLY DEFERRED, or made deferred by SET CONSTRAINTS …
			// DEFERRED). The simple-query dispatch bypasses
			// transactionOp.execCommit, so the checks queued on the session
			// during INSERT/UPDATE/DELETE must be run here BEFORE
			// TxnMgr.Commit; a violation aborts the transaction with 23503,
			// exactly as PG raises a deferred constraint violation at COMMIT.
			// RunDeferredFKChecks runs them under a fresh "latest" snapshot
			// (mvcc.Manager.FreshSnapshot) so a concurrently-committed parent
			// row satisfies the check even under REPEATABLE READ — matching
			// PG's deferred-RI snapshot semantics (fk-snapshot). 0119-0004.
			if sess := connTx.Session(); sess != nil {
				// Deferred FK checks first, then deferred UNIQUE/PK checks
				// (0119-0004 deferred-unique). Both run under a fresh "latest"
				// snapshot and roll the transaction back on a violation — FK with
				// 23503, UNIQUE with 23505. The ExecError's own Code drives the
				// SQLSTATE below, so a single rollback block serves both.
				deferErr := executor.RunDeferredFKChecks(ctx, sess)
				if deferErr == nil {
					deferErr = executor.RunDeferredUniqueChecks(ctx, sess)
				}
				if deferErr == nil {
					// Deferred EXCLUDE constraint checks (23P01). 0119-0004
					// (deferred-exclusion). Same fresh-snapshot + rollback block.
					deferErr = executor.RunDeferredExclusionChecks(ctx, sess)
				}
				if deferErr != nil {
					_ = s.cfg.TxnMgr.Rollback(explicitTx)
					s.endExplicitBlock(ctx, connTx)
					code := sqlstate.ForeignKeyViolation
					var fields []protocol.ErrorField
					if ee, ok := deferErr.(*executor.ExecError); ok {
						if ee.Code != "" {
							code = sqlstate.Code(ee.Code)
						}
						if ee.Detail != "" {
							fields = append(fields, protocol.ErrorField{Code: protocol.FieldDetail, Value: ee.Detail})
						}
						if ee.Pos > 0 {
							fields = append(fields, protocol.ErrorField{Code: protocol.FieldPosition, Value: strconv.Itoa(ee.Pos + 1)})
						}
						return txnVerbOutcome{Handled: true, Err: &txnVerbError{Code: code, Msg: ee.Message, Fields: fields}}
					}
					return txnVerbOutcome{Handled: true, Err: &txnVerbError{Code: code, Msg: deferErr.Error()}}
				}
			}
			// M0104-0008: SSI pre-commit dangerous-structure check.
			// The executor's transactionOp.execCommit invokes this for
			// COMMIT routed through the executor; the simple-query
			// dispatch bypasses execCommit and goes straight to
			// TxnMgr.Commit, so the check must be re-invoked here for
			// `BEGIN ISOLATION LEVEL SERIALIZABLE ... COMMIT` to abort
			// with SQLSTATE 40001 when a dangerous rw-structure is
			// detected. Returns nil for RC/RR / write-less SERIALIZABLE.
			if explicitTx.Isolation == mvcc.IsolationSerializable && explicitTx.Handle != 0 {
				if ssiErr := s.cfg.TxnMgr.PreCommitCheckForSerializationFailure(explicitTx.Handle); ssiErr != nil {
					// SSI failure: rollback.
					_ = s.cfg.TxnMgr.Rollback(explicitTx)
					s.endExplicitBlock(ctx, connTx)
					// Primary message is the bare upstream errmsg; the reason
					// code rides in DETAIL (predicate.c parity). isolationtester
					// and psql print only the errmsg line.
					var ssiFields []protocol.ErrorField
					if sfe, ok := ssiErr.(*mvcc.SerializationFailureError); ok {
						if d := sfe.Detail(); d != "" {
							ssiFields = append(ssiFields,
								protocol.ErrorField{Code: protocol.FieldDetail, Value: d},
								protocol.ErrorField{Code: protocol.FieldHint, Value: "The transaction might succeed if retried."})
						}
					}
					return txnVerbOutcome{Handled: true, Err: &txnVerbError{
						Code:   "40001",
						Msg:    "could not serialize access due to read/write dependencies among transactions",
						Fields: ssiFields,
					}}
				}
			}
			// M0118-0008: apply DROP INDEX removals deferred to COMMIT (the
			// simple-query path bypasses execCommit). Must run BEFORE
			// TxnMgr.Commit so the drop WAL precedes the commit record.
			if sess := connTx.Session(); sess != nil {
				executor.ApplyPendingIndexDrops(ctx, sess)
				// M0118-0008: register ATTACH PARTITION deferred to COMMIT (the
				// simple-query path bypasses execCommit).
				executor.ApplyPendingPartitionAttaches(ctx, sess)
				// M0118-0008: apply ALTER TABLE {NO} INHERIT deferred to COMMIT
				// (the simple-query path bypasses execCommit).
				executor.ApplyPendingInheritanceChanges(ctx, sess)
				// M0118-0008 (alter-table-4 perm 3): apply DROP TABLE removals
				// deferred to COMMIT (the simple-query path bypasses execCommit).
				executor.ApplyPendingTableDrops(ctx, sess)
				// M0118-0009 (`stats`): apply DROP FUNCTION removals deferred to
				// COMMIT (the simple-query path bypasses execCommit).
				executor.ApplyDeferredRoutineDrops(ctx, sess)
			}
			if err := ctx.CommitTransaction(explicitTx); err != nil {
				s.endExplicitBlock(ctx, connTx)
				return txnVerbOutcome{Handled: true, Err: &txnVerbError{Code: sqlstate.SystemError, Msg: err.Error()}}
			}
			// Publish NOTIFYs buffered by this explicit transaction now that it
			// has committed — BEFORE connTx.End() discards the buffer. Delivery
			// to this session happens at the trailing ReadyForQuery via
			// deliverNotifications. M0118-0009 (async-notify).
			s.publishPendingNotify(connTx)
			s.endExplicitBlock(ctx, connTx)
			maybeForceGCAfterCommit()
			// Leave *autoCommitPtr = false so the caller does NOT attempt
			// a second TxnMgr.Commit on the already-committed transaction.
			return txnVerbOutcome{Handled: true, Tag: transactionTag(txNode.Verb)}
		}
		// COMMIT outside an explicit transaction: emit warning.
		return txnVerbOutcome{Handled: true, Warn: true, Tag: transactionTag(txNode.Verb)}
	case planner.TxRollback:
		if connTx != nil && connTx.InExplicit() {
			// Undo DDL creates, TRUNCATE page snapshots, and RESTART IDENTITY
			// before TxnMgr.Rollback so catalog lookups still work.
			if sess := connTx.Session(); sess != nil {
				executor.ProcessRollbackUndos(ctx, sess)
			}
			_ = s.cfg.TxnMgr.Rollback(connTx.Tx())
			s.endExplicitBlock(ctx, connTx)
			// Leave *autoCommitPtr = false to avoid a second rollback attempt.
			return txnVerbOutcome{Handled: true, Tag: transactionTag(txNode.Verb)}
		}
		// ROLLBACK outside an explicit transaction: emit warning.
		return txnVerbOutcome{Handled: true, Warn: true, Tag: transactionTag(txNode.Verb)}
	}
	// SAVEPOINT, ROLLBACK TO, and RELEASE are not owned by this state
	// machine: the caller falls through to BuildFastIterator so
	// execSavepoint / execRollbackTo / execRelease run properly (M0097-0023).
	return txnVerbOutcome{}
}
