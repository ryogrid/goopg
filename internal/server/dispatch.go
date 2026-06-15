package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/wal"
)

// queryHeapHighWaterMark is the per-query peak HeapInuse seen at
// the end of `dispatchSimpleQueryViaExecutor`. If a query crosses
// the soft threshold (heapReleaseThresholdBytes) we trigger a GC
// and return memory to the OS via debug.FreeOSMemory(). Cheaper
// queries skip the call to avoid the GC-overhead regressions
// M0032-0005 documented (91 % GC time on Q9).
//
// M0061-0004: WSL2 went down during the M0061-0003 sweep with
// peak VmHWM=16 GB, suggesting a process-level memory pressure.
// `maybeForceGCAfterCommit` had been a no-op since M0032 ripped
// out the unconditional GC. We now do a *conditional* free —
// only when HeapInuse crossed the threshold during this query.
const heapReleaseThresholdBytes = 4 << 30 // 4 GiB

// queriesWithoutFreeCounter accumulates queries since the last
// FreeOSMemory(). Even if no single query crosses the threshold,
// we still issue one Free every N queries so a long sequence of
// medium queries (Q1..Q22 sweep) cannot accumulate unreclaimed
// retained allocations indefinitely.
var queriesWithoutFreeCounter int64

// queriesPerForcedFree gates how often we invoke runtime.GC()+FreeOSMemory()
// when no single query has exceeded heapReleaseThresholdBytes.  The original
// value of 8 was sized for TPC-H (queries that take seconds each); at pgbench
// rates (thousands of queries per second) it caused a world-stop ReadMemStats
// on *every* query and a full GC every ~8 queries — accounting for 43% of
// CPU at c=10 SO.  10 000 still guards against long TPC-H drifts (22 queries
// × hours = far below 10 000) while eliminating the pgbench overhead.
const queriesPerForcedFree = 10_000

// maybeForceGCAfterCommit triggers `runtime.GC()` +
// `debug.FreeOSMemory()` at the end of a Query message when
// either:
//   - HeapInuse > heapReleaseThresholdBytes  (this query was big), or
//   - we've gone queriesPerForcedFree queries without a Free   (drift).
//
// Hot-path discipline: the atomic counter check is evaluated first (no
// STW).  runtime.ReadMemStats (which requires a brief stop-the-world) is
// only called when the counter says a GC round is due — keeping the common
// sub-threshold path to a single atomic operation.
func maybeForceGCAfterCommit() {
	n := atomic.AddInt64(&queriesWithoutFreeCounter, 1)
	if n < queriesPerForcedFree {
		return // fast path: single atomic add, no STW
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	atomic.StoreInt64(&queriesWithoutFreeCounter, 0)
	if ms.HeapInuse < heapReleaseThresholdBytes {
		return
	}
	runtime.GC()
	debug.FreeOSMemory()
}

// dispatchSimpleQueryViaExecutor is the parser-driven path for the
// simple-query protocol: it parses the SQL, plans each statement,
// builds the executor operator tree, runs it, and emits the right
// shape of wire messages (RowDescription / DataRow / CommandComplete)
// terminated by a single ReadyForQuery.
//
// Multi-statement queries are split semicolon-wise by the parser; for
// each plan node we issue exactly one CommandComplete plus any rows
// the operator produces. Errors abort the run — every later statement
// in the same Query message is skipped, matching upstream's
// "abort-on-error" semantics for the simple-query path.
//
// COPY is handled in dispatchCopyViaExecutor; this function returns
// nil after delegating when the parsed statement is a COPY.
func (s *Server) dispatchSimpleQueryViaExecutor(ctx context.Context, r *protocol.FrameReader, w *protocol.FrameWriter, sess *config.SessionRegistry, sql string, connTx *connTxState, prepStmts *preparedStatements) error {
	// Parse uses the heap-backed tokenSlicePool (allocation-free in steady
	// state). The M0107-0003 Phase C.3 mctx token-arena fast path was retired
	// as fundamentally GC-unsafe — see docs/design/0107-0003d-token-pool-gc-safety.md
	// — so we no longer acquire a throwaway KindExpr child just to pass it in.
	stmts, err := parser.Parse(sql)
	if err != nil {
		// M0054-0001: CREATE DATABASE / DROP DATABASE are intercepted
		// here (the parser doesn't recognise them yet) so we can
		// (a) update the catalog so subsequent connections see the
		// database in pg_database / can connect to it, and (b) emit a
		// WAL record so the registration survives a crash. Other
		// commands fall through to the wire-protocol no-op tag handler.
		if handled, notice, herr := s.tryHandleDatabaseDDL(sql); handled {
			if herr != nil {
				return s.writeQueryError(w, sqlstate.SystemError, herr.Error())
			}
			if notice != "" {
				_ = w.WriteNoticeResponse([]protocol.ErrorField{
					{Code: protocol.FieldSeverity, Value: "NOTICE"},
					{Code: protocol.FieldSeverityNonLocal, Value: "NOTICE"},
					{Code: protocol.FieldSQLState, Value: "00000"},
					{Code: protocol.FieldMessage, Value: notice},
				})
			}
			tag := databaseDDLCommandTag(sql)
			if err := w.WriteCommandComplete(tag); err != nil {
				return err
			}
			return w.WriteReadyForQuery(protocol.TxStatusIdle)
		}
		// Role DDL (CREATE/DROP ROLE/USER) is not yet in the parser but needs
		// actual role tracking so DROP ROLE fails on nonexistent roles.
		if handled, herr := s.tryHandleRoleDDL(sql); handled {
			if herr != nil {
				return s.writeQueryError(w, roleErrorSQLState(herr), herr.Error())
			}
			norm := normalizeCompatSQL(sql)
			var tag string
			if strings.HasPrefix(norm, "create ") {
				tag = "CREATE ROLE"
			} else {
				tag = "DROP ROLE"
			}
			if err := w.WriteCommandComplete(tag); err != nil {
				return err
			}
			return w.WriteReadyForQuery(protocol.TxStatusIdle)
		}
		if tag, ok := compatNoopCommandTag(sql); ok {
			// Side-effect: register schema for CREATE SCHEMA statements.
			if tag == "CREATE SCHEMA" && s.cfg.Catalog != nil {
				norm := normalizeCompatSQL(sql)
				if schemaName := schemaNameFromCreate(norm); schemaName != "" {
					s.cfg.Catalog.RegisterSchema(schemaName)
					// M0110-0003: persist so the schema survives a restart.
					// This branch handles CREATE SCHEMA forms the parser
					// rejects; the parsed CompatNoopStmt path emits the same
					// record from execCompatNoop.
					if s.cfg.WAL != nil {
						if im, ok := s.cfg.Catalog.(*catalog.InMemory); ok {
							oid := im.SchemaOID(schemaName)
							if _, _, werr := s.cfg.WAL.Append(wal.EncodeCreateSchema(schemaName, oid)); werr != nil {
								return s.writeQueryError(w, sqlstate.SystemError, werr.Error())
							}
						}
					}
				}
			}
			if err := w.WriteCommandComplete(tag); err != nil {
				return err
			}
			return w.WriteReadyForQuery(protocol.TxStatusIdle)
		}
		msg, extra := syntaxErrorMsg(err)
		return s.writeQueryError(w, sqlstate.SyntaxError, msg, extra...)
	}
	if len(stmts) == 0 {
		if err := w.WriteEmptyQueryResponse(); err != nil {
			return err
		}
		return w.WriteReadyForQuery(protocol.TxStatusIdle)
	}
	// Session-level explicit transaction support (M0096-0005):
	// When the client has issued BEGIN, reuse the open TxnMgr transaction
	// rather than starting a fresh auto-commit one.  Each statement-level
	// dispatch that is NOT inside an explicit transaction still auto-commits.
	var tx mvcc.Transaction
	autoCommit := true
	if connTx != nil && connTx.InExplicit() {
		tx = connTx.Tx()
		autoCommit = false
	} else {
		var err error
		var pn int32
		if connTx != nil {
			pn = connTx.ProcNum
		}
		tx, err = s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted, pn)
		if err != nil {
			return s.writeQueryError(w, sqlstate.SystemError, err.Error())
		}
	}
	// Each Query message gets a fresh BackendID for the lock
	// manager; the youngest-backend victim policy from M0012-0002
	// relies on monotonic IDs.
	backendID := lockmgr.BackendID(s.nextBackendID.Add(1))
	commit := false
	var advisoryReleaseTarget any
	defer func() {
		if autoCommit && !commit {
			_ = s.cfg.TxnMgr.Rollback(tx)
			executor.ReleaseAdvisoryTransactionLocks(advisoryReleaseTarget)
		}
		// Always drop locks at txn end so a leftover holder
		// can't outlive the connection. ReleaseAll is a no-op
		// when LockMgr is nil.
		if s.cfg.LockMgr != nil {
			s.cfg.LockMgr.ReleaseAll(backendID)
		}
	}()
	snap, err := s.cfg.TxnMgr.SnapshotFor(tx)
	if err != nil {
		return s.writeQueryError(w, sqlstate.SystemError, err.Error())
	}
	// M0107-0001: per-statement mctx. Parent is the session mctx
	// threaded from serveConn via connTx.SessCtx (nil for tests
	// that don't wire a full server).
	var sessCtxForStmt *mctx.Context
	if connTx != nil {
		sessCtxForStmt = connTx.SessCtx
	}
	stmtCtx := mctx.Acquire(sessCtxForStmt, mctx.KindStmt)
	defer stmtCtx.Release()

	ectx := executor.NewContext()
	ectx.Mctx = stmtCtx
	ectx.Ctx = ctx
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	// PlanCatalog will be set to a search-path-aware wrapper after sess is wired.
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.Tx = tx
	// Wire the per-connection session into the executor so advisory locks
	// and other session-scoped state are properly tracked.
	if connTx != nil {
		if sess := connTx.Session(); sess != nil {
			ectx.Session = sess
		}
		// Share the per-connection TEMP TABLE shadow map so it persists
		// across statements in the same connection. M0097-0003.
		ectx.TempTableShadows = connTx.TempTableShadows
		ectx.PendingEnumValues = connTx.PendingEnumValues
		ectx.PendingEnumRenames = connTx.PendingEnumRenames
		ectx.PendingCreatedEnums = connTx.PendingCreatedEnums
		// Wire session-authorization role tracking so LEAKPROOF privilege checks
		// work after SET SESSION AUTHORIZATION regress_unpriv_user.
		ectx.NonSuperuserRole = connTx.NonSuperuserRole
		ectx.SetSessionAuthorization = func(role string) {
			connTx.NonSuperuserRole = role
			ectx.NonSuperuserRole = role
		}
		// Wire per-connection sequence session state (currval/lastval) so
		// values persist across statements within the same connection. M0097-0042.
		if connTx.SeqCurrVals != nil {
			ectx.CurrSeqVals = connTx.SeqCurrVals
		}
		ectx.LastSeqVal = connTx.SeqLastVal
		ectx.LastSeqSet = connTx.SeqLastSet
		ectx.LastSeqName = connTx.SeqLastName
		// Save sequence session state back to the connection after dispatch.
		defer func() {
			if ectx.CurrSeqVals != nil {
				connTx.SeqCurrVals = ectx.CurrSeqVals
			}
			connTx.SeqLastVal = ectx.LastSeqVal
			connTx.SeqLastSet = ectx.LastSeqSet
			connTx.SeqLastName = ectx.LastSeqName
		}()
	}
	ectx.Snap = snap
	ectx.Checkpointer = s.cfg.Checkpointer
	ectx.StatsTarget = sessionStatsTarget(sess)
	ectx.WorkMem = sessionWorkMem(sess)
	if sess != nil {
		ectx.AdvisorySessionIdentity = sess
		ectx.GetSetting = func(name string) (string, bool) {
			_, eff, ok := sess.Get(name)
			return eff, ok
		}
		ectx.SetSetting = func(name, value string, isLocal bool) error {
			return sess.Set(name, value, isLocal)
		}
		ectx.AllSettings = func() []executor.SettingValue {
			all := sess.All()
			out := make([]executor.SettingValue, 0, len(all))
			for _, kv := range all {
				out = append(out, executor.SettingValue{Name: kv.Name, Value: kv.Value})
			}
			return out
		}
		ectx.ResetSetting = sess.Reset
		ectx.ResetAllSettings = sess.ResetAll
		ectx.BeginLocalTransaction = sess.BeginTransaction
		ectx.EndLocalTransaction = sess.EndTransaction
		// Set PlanCatalog to a search-path-aware wrapper so DDL executor can
		// use it when calling planner.Plan for internal validation. M0097-0022.
		ectx.PlanCatalog = sessionPlanCatalog(sess, s.cfg.Catalog)
	}
	if ectx.Session != nil {
		advisoryReleaseTarget = ectx.Session
	} else if ectx.AdvisorySessionIdentity != nil {
		advisoryReleaseTarget = ectx.AdvisorySessionIdentity
	}
	ectx.EnableOpportunisticPrune = sessionOpportunisticPrune(sess)
	ectx.FSM = s.cfg.FSM
	ectx.VM = s.cfg.VM
	ectx.FreezeMinAge = sessionFreezeMinAge(sess)
	ectx.PubSub = s.cfg.PubSub
	ectx.LockMgr = s.cfg.LockMgr
	ectx.BackendID = backendID
	ectx.Activity = s.cfg.Activity
	if connTx != nil {
		ectx.ProcNum = connTx.ProcNum
	}
	ectx.WAL = s.cfg.WAL
	ectx.LogCanonical = s.cfg.LogCanonical
	ectx.SyncRep = s.cfg.SyncRep
	ectx.SyncCommitMode = sessionSyncCommitMode(sess)
	if s.applyLauncher != nil {
		ectx.OnSubscriptionChange = s.applyLauncher.Wake
	}
	ectx.DataDir = s.cfg.DataDir
	ectx.Promote = s.cfg.Promote
	if s.cfg.IsStandby != nil {
		ectx.IsStandby = s.cfg.IsStandby()
	}
	// Wire inline-NOTICE delivery so RAISE NOTICE emitted before a row-level
	// lock wait (e.g. from noisy_oper() in eval-plan-qual) reaches the client
	// before blockDetectWait fires in the isolation runner.  Without this,
	// notices are buffered in ctx.Notices and only sent at CommandComplete
	// time — AFTER the wait resolves — causing the isolation runner to print
	// them after <waiting ...> instead of before the step header.
	// M0100-0005 (eval-plan-qual / eval-plan-qual-trigger).
	ectx.NoticeFlush = func(msg string) {
		_ = w.WriteNoticeResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "NOTICE"},
			{Code: protocol.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: protocol.FieldSQLState, Value: "00000"},
			{Code: protocol.FieldMessage, Value: msg},
		})
		_ = w.Flush()
	}

	// Wire pg_prepared_statements session rows into the executor context.
	if prepStmts != nil {
		ectx.PrepStmtsRows = prepStmts.ListRows
	}

	// Wire the per-database pg_extension view (M0110-0003 gap #7c): goopg shares
	// one in-memory catalog across all databases, so pg_extension is scoped to
	// the connecting database here. Mirrors the extended-query path in
	// executeExtendedQueryViaExecutor.
	if connTx != nil {
		s.wireExtensionRows(ectx, connTx.DBName)
	}

	// Update pg_stat_activity before dispatching.
	// M0107-0005: use procNum (int32) for the atomic hot path.
	if reg := s.cfg.Activity; reg != nil && connTx != nil {
		q := sql
		if len(q) > 1024 {
			q = q[:1024]
		}
		reg.UpdateState(connTx.ProcNum, "active", q)
	}

	for i, stmt := range stmts {
		// Check for failed transaction state (25P02) — reject all statements
		// except COMMIT/ROLLBACK/ABORT/END that clear the failed state.
		// PostgreSQL semantics: an error inside an explicit transaction block
		// marks the block as aborted; all subsequent statements get 25P02
		// until the client issues ROLLBACK. M0100-0005.
		if connTx != nil && connTx.IsFailed() {
			_, isCommit := stmt.(*parser.CommitStmt)
			_, isRollback := stmt.(*parser.RollbackStmt)
			_, isRollbackTo := stmt.(*parser.RollbackToSavepointStmt)
			if !isCommit && !isRollback && !isRollbackTo {
				return s.writeQueryError(w, "25P02",
					"current transaction is aborted, commands ignored until end of transaction block")
			}
			// COMMIT/ROLLBACK clears the failed state — handled below in
			// executeOneSimpleStmt → TxCommit/TxRollback path, which calls
			// connTx.End() (resetting failed=false). Fall through.
			// ROLLBACK TO SAVEPOINT clears the failed state so subsequent
			// statements within the same transaction can proceed.
			if isRollbackTo {
				connTx.ClearFailed()
			}
		}

		// EXPLAIN EXECUTE <name> (M0100-0005h): the planner wraps an
		// `ExecuteStmt` Inner as a `Utility` node and EXPLAIN renders
		// it as the placeholder `Utility *parser.ExecuteStmt`.  PG
		// instead expands the prepared statement and renders its
		// actual plan tree.  We replay that here by looking up the
		// stored PREPARE SQL, re-parsing it, and substituting the
		// prepared `Query` Stmt for the `ExecuteStmt` before the rest
		// of the loop falls into `planner.Plan(stmt, …)`.  The
		// re-parse is cheap for the EXPLAIN-only path and keeps the
		// registry interface (raw-SQL store/lookup) unchanged.
		//
		// `rewroteExplainExecute` disables the plan cache for this
		// statement so a later re-PREPARE of the same name (which
		// does not invalidate the cache) cannot serve the stale plan.
		disablePlanCache := false
		if es, ok := stmt.(*parser.ExplainStmt); ok {
			if ex, exok := es.Inner.(*parser.ExecuteStmt); exok {
				if prepStmts == nil {
					return s.writeQueryError(w, "26000", fmt.Sprintf("prepared statement %q does not exist", ex.Name))
				}
				prepDef, found := prepStmts.Lookup(ex.Name)
				if !found {
					return s.writeQueryError(w, "26000", fmt.Sprintf("prepared statement %q does not exist", ex.Name))
				}
				if prepDef.stmt == nil {
					return s.writeQueryError(w, sqlstate.SystemError, fmt.Sprintf("prepared statement %q has no body", ex.Name))
				}
				es.Inner = prepDef.stmt
				disablePlanCache = true
				// fall through to executeOneSimpleStmt below
			}
		}
		// Handle PREPARE / EXECUTE / DEALLOCATE inline (M0096-0006).
		// These require per-connection state not available in the executor.
		if ps, ok := stmt.(*parser.PrepareStmt); ok {
			if prepStmts != nil && ps.Name != "" && ps.Query != nil {
				// Validate declared parameter types.
				for _, pt := range ps.ParamTypes {
					if !isValidSQLTypeName(pt) {
						return s.writeQueryError(w, "42704",
							fmt.Sprintf("type %q does not exist", pt))
					}
				}
				if ok := prepStmts.Store(ps.Name, ps.Query, stmtSQL(sql, stmts, i), ps.ParamTypes); !ok {
					return s.writeQueryError(w, "42P05",
						fmt.Sprintf("prepared statement %q already exists", ps.Name))
				}
				// Infer result column types and undeclared parameter types by planning/walking.
				if ectx.Catalog != nil {
					if plan, planErr := planner.Plan(ps.Query, sessionPlanCatalog(sess, ectx.Catalog)); planErr == nil {
						schema := plan.Output()
						if len(schema) > 0 {
							resultTypes := make([]string, len(schema))
							for k, col := range schema {
								resultTypes[k] = normResultType(col.Type.Name)
							}
							prepStmts.SetResultTypes(ps.Name, resultTypes)
						}
					}
					// Infer parameter types from comparison contexts.
					inferred := inferParamTypesFromStmt(ps.Query, ectx.Catalog, ps.ParamTypes)
					if inferred != nil {
						prepStmts.SetParamTypes(ps.Name, inferred)
					}
				}
			}
			if err := w.WriteCommandComplete("PREPARE"); err != nil {
				return err
			}
			continue
		}
		restoreParams := ectx.Params
		if es, ok := stmt.(*parser.ExecuteStmt); ok {
			if prepStmts != nil {
				if prepDef, found := prepStmts.Lookup(es.Name); found {
					if prepDef.stmt == nil {
						return s.writeQueryError(w, sqlstate.SystemError, fmt.Sprintf("prepared statement %q has no body", es.Name))
					}
					// Validate parameter count when the PREPARE declared a type list.
					if prepDef.paramTypes != nil && len(es.Params) != len(prepDef.paramTypes) {
						detail := fmt.Sprintf("Expected %d parameters but got %d.",
							len(prepDef.paramTypes), len(es.Params))
						return s.writeQueryError(w, "08P01",
							fmt.Sprintf("wrong number of parameters for prepared statement %q", es.Name),
							protocol.ErrorField{Code: protocol.FieldDetail, Value: detail})
					}
					params, err := evalExecuteParams(es.Params)
					if err != nil {
						if ee, ok := err.(*executor.ExecError); ok {
							return s.writeQueryError(w, sqlstate.Code(ee.Code), ee.Message)
						}
						return s.writeQueryError(w, sqlstate.SyntaxError, err.Error())
					}
					// Validate type compatibility with declared parameter types.
					for idx, param := range params {
						if idx >= len(prepDef.paramTypes) {
							break
						}
						target := strings.ToLower(prepDef.paramTypes[idx])
						if execParamTypeIncompatible(param, target) {
							srcName := execParamKindName(param)
							dstName := strings.Trim(normPrepParamType(prepDef.paramTypes[idx]), `"`)
							return s.writeQueryError(w, "42804",
								fmt.Sprintf("parameter $%d of type %s cannot be coerced to the expected type %s", idx+1, srcName, dstName),
								protocol.ErrorField{Code: protocol.FieldHint, Value: "You will need to rewrite or cast the expression."})
						}
					}
					stmt = prepDef.stmt
					ectx.Params = params
					disablePlanCache = true
				} else {
					return s.writeQueryError(w, "26000", fmt.Sprintf("prepared statement %q does not exist", es.Name))
				}
			}
		}
		if ds, ok := stmt.(*parser.DeallocateStmt); ok {
			if prepStmts != nil {
				if ds.Name == "" {
					prepStmts.DeleteAll()
				} else {
					prepStmts.Delete(ds.Name)
				}
			}
			if err := w.WriteCommandComplete("DEALLOCATE"); err != nil {
				return err
			}
			continue
		}
		// CREATE TABLE name AS EXECUTE name(params) [WITH NO DATA].
		// Resolve the prepared statement to a SelectSource so execCreateTableAs
		// can handle it without needing access to per-connection prepared statements.
		if cs, ok := stmt.(*parser.CreateTableStmt); ok && cs.ExecuteSource != nil {
			if prepStmts != nil {
				es := cs.ExecuteSource
				prepDef, found := prepStmts.Lookup(es.Name)
				if !found {
					return s.writeQueryError(w, "26000", fmt.Sprintf("prepared statement %q does not exist", es.Name))
				}
				selStmt, ok2 := prepDef.stmt.(*parser.SelectStmt)
				if !ok2 {
					return s.writeQueryError(w, "42601", "EXECUTE in CREATE TABLE AS must reference a SELECT prepared statement")
				}
				params, err := evalExecuteParams(es.Params)
				if err != nil {
					if ee, ok := err.(*executor.ExecError); ok {
						return s.writeQueryError(w, sqlstate.Code(ee.Code), ee.Message)
					}
					return s.writeQueryError(w, sqlstate.SyntaxError, err.Error())
				}
				ectx.Params = params
				disablePlanCache = true
				cs.SelectSource = selStmt
				cs.ExecuteSource = nil
				stmt = cs
			}
		}
		// DECLARE ... CURSOR FOR select (M0097-0003).
		if dc, ok := stmt.(*parser.DeclareCursorStmt); ok {
			if connTx != nil {
				// Store the cursor's SELECT SQL for later FETCH.
				// Re-extract the raw SQL for this cursor declaration.
				// Since we have the parsed query, reconstruct by storing
				// the original sql text (trimmed to the cursor portion).
				connTx.cursorDeclare(dc.Name, sql)
			}
			if err := w.WriteCommandComplete("DECLARE CURSOR"); err != nil {
				return err
			}
			continue
		}
		// FETCH [ALL|n] [FROM|IN] cursor_name (M0097-0003 / M0097-0042).
		if fs, ok := stmt.(*parser.FetchStmt); ok {
			if connTx != nil {
				if cur, found := connTx.cursorLookup(fs.CursorName); found {
					if err := s.executeFetch(ctx, w, ectx, cur, fs.CursorName, fs.Count, fs.Forward); err != nil {
						return err
					}
					continue
				}
			}
			return s.writeQueryError(w, "34000", fmt.Sprintf("cursor \"%s\" does not exist", fs.CursorName))
		}
		// CLOSE cursor_name (M0097-0003).
		if cs, ok := stmt.(*parser.CloseStmt); ok {
			if connTx != nil {
				if cs.Name != "" {
					if _, found := connTx.cursorLookup(cs.Name); !found {
						return s.writeQueryError(w, "34000", fmt.Sprintf("cursor \"%s\" does not exist", cs.Name))
					}
				}
				connTx.cursorClose(cs.Name)
			}
			if err := w.WriteCommandComplete("CLOSE CURSOR"); err != nil {
				return err
			}
			continue
		}

		// PG-parity: RC refreshes snapshot per statement; RR/SSI hold the
		// BEGIN-time snapshot for the whole transaction (M0100-0001).
		// Use ectx.Tx.Isolation (not the outer tx) so execBegin's
		// promotion of the implicit RC tx to an explicit RR tx is visible.
		if ectx.Tx.Isolation == mvcc.IsolationReadCommitted {
			snap2, err := s.cfg.TxnMgr.SnapshotFor(tx)
			if err != nil {
				return s.writeQueryError(w, sqlstate.SystemError, err.Error())
			}
			ectx.Snap = snap2
		}
		// Per-statement reset: clear the DML-CTE write fence and the regular-CTE
		// row cache from any previous statement. The row cache is query-scoped:
		// a CTE named "q" in query 1 must not bleed into query 2 (they may
		// produce different rows). CTEWriteFence is cleared for the same reason.
		ectx.CTEWriteFence = nil
		ectx.CTENewToOld = nil
		ectx.CTESelfModifiedErrors = nil
		ectx.CTESelfModErr = nil
		ectx.InDMLCTE = false
		ectx.CTERowCache = nil

		// COPY inside a multi-statement simple-query batch (psql `\;`).
		// Intercept before the plan-cache / executeOneSimpleStmt path —
		// the executor has no COPY operator (COPY is driven from the wire
		// layer). runInlineCopy streams within the batch's shared txn and
		// writes only CommandComplete; the trailing ReadyForQuery below
		// covers the whole Query message. COPY FROM STDIN reads its
		// CopyData/CopyDone frames synchronously from r mid-batch. M0097-0024.
		if cs, ok := stmt.(*parser.CopyStmt); ok {
			if err := s.runInlineCopy(r, w, ectx, cs); err != nil {
				if errors.Is(err, errQueryErrorSent) {
					// ErrorResponse + RFQ already sent; abort the rest of
					// the batch (PG aborts the whole message on error).
					if !autoCommit && connTx != nil && connTx.InExplicit() {
						connTx.Fail()
					}
					return nil
				}
				return err
			}
			continue
		}

		// M0098-0005: plan cache for single-statement queries (the
		// common OLTP case). On hit: skip planner.Plan. On miss:
		// plan, cache, then execute.
		var precached planner.Node
		var cacheKey string
		if s.pc != nil && len(stmts) == 1 && !disablePlanCache {
			cacheKey = normalizeCompatSQL(sql)
			if cached, ok := s.pc.Get(cacheKey); ok {
				precached = cached
			} else {
				// Cache miss: plan now so we can store it.
				freshNode, perr := planner.Plan(stmt, sessionPlanCatalog(sess, s.cfg.Catalog))
				if perr != nil {
					code, msg := planErrorFields(perr)
					return s.writeQueryError(w, code, msg, planErrorHintFields(perr)...)
				}
				if planCacheIsCacheable(freshNode) {
					s.pc.Put(cacheKey, freshNode)
				}
				precached = freshNode
			}
		}
		// M0097-0059: enforce statement_timeout by deriving a deadline
		// context. The executor checks ctx.Ctx.Err() at each outer-row
		// boundary; when the deadline fires the next check returns
		// context.DeadlineExceeded and the executor surfaces error 57014.
		savedCtx := ectx.Ctx
		var stmtCancel context.CancelFunc
		if timeoutMs := sessionStatementTimeout(sess); timeoutMs > 0 {
			var stmtCtx context.Context
			stmtCtx, stmtCancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
			ectx.Ctx = stmtCtx
		}
		err := s.executeOneSimpleStmt(w, ectx, stmt, connTx, &autoCommit, precached)
		if stmtCancel != nil {
			stmtCancel()
		}
		ectx.Ctx = savedCtx
		ectx.Params = restoreParams
		if err != nil {
			if errors.Is(err, errQueryErrorSent) {
				// Error + ReadyForQuery already sent to the client (M0097-0003).
				// Do NOT send another ReadyForQuery — that would produce a double
				// RFQ that causes psql to print "message type 0x5a arrived from
				// server while idle". Just return nil so the connection stays alive.
				// Mark the explicit transaction as failed so subsequent statements
				// in the same transaction block get 25P02 (M0100-0005).
				if !autoCommit && connTx != nil && connTx.InExplicit() {
					connTx.Fail()
				}
				return nil
			}
			return err
		}
		// Write back the temp-table shadow map so it persists across statements. M0097-0003.
		if connTx != nil && ectx.TempTableShadows != nil {
			connTx.TempTableShadows = ectx.TempTableShadows
		}
		// Write back pending enum values/renames/creates (including nil after COMMIT/ROLLBACK).
		if connTx != nil {
			connTx.PendingEnumValues = ectx.PendingEnumValues
			connTx.PendingEnumRenames = ectx.PendingEnumRenames
			connTx.PendingCreatedEnums = ectx.PendingCreatedEnums
		}
	}
	// Update pg_stat_activity to idle after successful execution.
	if reg := s.cfg.Activity; reg != nil && connTx != nil {
		reg.UpdateState(connTx.ProcNum, "idle", "")
	}
	if autoCommit {
		if err := s.cfg.TxnMgr.Commit(tx); err != nil {
			return s.writeQueryError(w, sqlstate.SystemError, err.Error())
		}
		executor.ReleaseAdvisoryTransactionLocks(advisoryReleaseTarget)
		commit = true
		maybeForceGCAfterCommit()
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// sessionStatsTarget reads the effective `default_statistics_target`
// GUC from the per-connection session registry. Zero is returned
// when sess is nil or the value can't be parsed; callers (the
// executor's analyzeOp) treat zero as "use the upstream default".
func sessionStatsTarget(sess *config.SessionRegistry) int {
	if sess == nil {
		return 0
	}
	_, eff, ok := sess.Get("default_statistics_target")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(eff))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// sessionWorkMem reads the effective `work_mem` GUC from the session
// registry and returns it as bytes. Returns 0 (unlimited) when sess
// is nil or the value can't be parsed.
// sessionOpportunisticPrune reads the enable_opportunistic_prune GUC
// (M0046-0002). Returns true (enabled) when sess is nil or the GUC value
// can't be parsed, matching the BootVal "on" default.
// sessionFreezeMinAge reads vacuum_freeze_min_age (M0046-0005).
// Returns 50_000_000 (50M XIDs) when sess is nil or the GUC is missing.
func sessionFreezeMinAge(sess *config.SessionRegistry) int64 {
	if sess == nil {
		return 50_000_000
	}
	_, eff, ok := sess.Get("vacuum_freeze_min_age")
	if !ok {
		return 50_000_000
	}
	v, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64)
	if err != nil || v < 0 {
		return 50_000_000
	}
	return v
}

// sessionSyncCommitMode reads the effective `synchronous_commit` GUC from
// the session registry and maps it to a SyncRepMode. Empty or unknown values
// fall back to SyncRepRemoteFlush (treat as "on"), matching upstream.
// M0102-0005.
func sessionSyncCommitMode(sess *config.SessionRegistry) wal.SyncRepMode {
	if sess == nil {
		return wal.SyncRepRemoteFlush
	}
	_, eff, ok := sess.Get("synchronous_commit")
	if !ok {
		return wal.SyncRepRemoteFlush
	}
	return wal.ParseSyncCommitLevel(strings.ToLower(strings.TrimSpace(eff)))
}

func sessionOpportunisticPrune(sess *config.SessionRegistry) bool {
	if sess == nil {
		return true // default on
	}
	_, eff, ok := sess.Get("enable_opportunistic_prune")
	if !ok {
		return true // GUC not registered yet, default on
	}
	return strings.EqualFold(strings.TrimSpace(eff), "on")
}

func sessionWorkMem(sess *config.SessionRegistry) int64 {
	if sess == nil {
		return 0
	}
	_, eff, ok := sess.Get("work_mem")
	if !ok {
		return 0
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64)
	if err != nil || kb < 0 {
		return 0
	}
	// work_mem is stored in KB; convert to bytes.
	return kb * 1024
}

// sessionStatementTimeout reads the effective `statement_timeout` GUC from
// the session and returns it in milliseconds. Returns 0 (no timeout) if the
// setting is missing, zero, or unparseable. M0097-0059.
func sessionStatementTimeout(sess *config.SessionRegistry) int64 {
	if sess == nil {
		return 0
	}
	_, eff, ok := sess.Get("statement_timeout")
	if !ok {
		return 0
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return ms
}

// sessionPlanCatalog returns a search-path-aware catalog wrapper for use when
// calling planner.Plan. The wrapper re-reads search_path dynamically so that
// SET search_path changes take effect on the next statement. When sess is nil
// the base catalog is returned unchanged. M0097-0022.
func sessionPlanCatalog(sess *config.SessionRegistry, base catalog.Catalog) catalog.Catalog {
	if sess == nil {
		return base
	}
	return catalog.WithSearchPath(base, func() []string {
		return searchPathSchemas(sess)
	})
}

// ctxPlanCatalog is like sessionPlanCatalog but reads search_path from an
// executor.Context's GetSetting hook. Used inside executeOneSimpleStmt and
// materializeCursor which receive an executor.Context rather than a *config.SessionRegistry.
func ctxPlanCatalog(ctx *executor.Context, base catalog.Catalog) catalog.Catalog {
	if ctx == nil || ctx.GetSetting == nil {
		return base
	}
	getSetting := ctx.GetSetting // capture
	return catalog.WithSearchPath(base, func() []string {
		sp, ok := getSetting("search_path")
		if !ok || sp == "" {
			return []string{"public"}
		}
		return parseSearchPathSchemas(sp)
	})
}

// parseSearchPathSchemas parses a search_path string (e.g. "temp_func_test, public")
// into an ordered list of user schemas (pg_catalog and information_schema excluded).
func parseSearchPathSchemas(sp string) []string {
	var out []string
	for _, raw := range strings.Split(sp, ",") {
		s := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
		if s == "" || s == "$user" {
			continue
		}
		lc := strings.ToLower(s)
		if lc == "pg_catalog" || lc == "information_schema" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		out = []string{"public"}
	}
	return out
}

// searchPathSchemas parses the session's search_path GUC and returns the
// ordered list of schemas to search for unqualified name resolution.
// Called by the SearchPathCatalog wrapper on every table lookup. M0097-0022.
func searchPathSchemas(sess *config.SessionRegistry) []string {
	if sess == nil {
		return []string{"public"}
	}
	_, eff, ok := sess.Get("search_path")
	if !ok || eff == "" {
		return []string{"public"}
	}
	return parseSearchPathSchemas(eff)
}

func compatNoopCommandTag(sql string) (string, bool) {
	norm := normalizeCompatSQL(sql)
	switch {
	case strings.HasPrefix(norm, "create user "), strings.HasPrefix(norm, "create role "):
		return "CREATE ROLE", true
	case strings.HasPrefix(norm, "create schema "), norm == "create schema":
		return "CREATE SCHEMA", true // name extraction done separately in dispatchSimpleQueryViaExecutor
	case strings.HasPrefix(norm, "grant "), norm == "grant":
		return "GRANT", true
	case strings.HasPrefix(norm, "revoke "), norm == "revoke":
		return "REVOKE", true
	case strings.HasPrefix(norm, "create database "):
		return "CREATE DATABASE", true
	case strings.HasPrefix(norm, "alter database "):
		return "ALTER DATABASE", true
	case strings.HasPrefix(norm, "alter user "), strings.HasPrefix(norm, "alter role "):
		return "ALTER ROLE", true
	case strings.HasPrefix(norm, "drop database "):
		return "DROP DATABASE", true
	case strings.HasPrefix(norm, "drop user "), strings.HasPrefix(norm, "drop role "):
		return "DROP ROLE", true
	case strings.HasPrefix(norm, "set constraints "):
		return "SET CONSTRAINTS", true
	case strings.HasPrefix(norm, "comment on "):
		return "COMMENT", true
	case strings.HasPrefix(norm, "security label "):
		return "SECURITY LABEL", true
	}
	return "", false
}

// schemaNameFromCreate extracts the schema name from a normalised CREATE SCHEMA statement.
func schemaNameFromCreate(norm string) string {
	if !strings.HasPrefix(norm, "create schema ") {
		return ""
	}
	rest := strings.TrimSpace(norm[len("create schema "):])
	// Skip optional AUTHORIZATION keyword.
	if strings.HasPrefix(rest, "authorization ") {
		return ""
	}
	return extractFirstSQLIdent("", rest)
}

func normalizeCompatSQL(sql string) string {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	// Lowercase keywords/identifiers but preserve string literal case.
	// Lowercasing string literals would cause 'A' and 'a' to map to the
	// same plan-cache key, returning the wrong cached plan. M0097-0003.
	return normalizeSQLPreservingLiterals(s)
}

// normalizeSQLPreservingLiterals lowercases SQL outside string literals
// and collapses whitespace. String literal contents are preserved verbatim
// so that INSERT ('A') and INSERT ('a') get distinct cache keys.
func normalizeSQLPreservingLiterals(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSingleQuote := false
	prevWasSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSingleQuote {
			// Inside a string literal — preserve case exactly.
			b.WriteByte(c)
			if c == '\'' {
				// Check for doubled single quote (escape).
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
				} else {
					inSingleQuote = false
				}
			}
			prevWasSpace = false
			continue
		}
		if c == '\'' {
			inSingleQuote = true
			b.WriteByte(c)
			prevWasSpace = false
			continue
		}
		// Outside literal: lowercase and collapse whitespace.
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevWasSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevWasSpace = true
			}
		} else {
			if c >= 'A' && c <= 'Z' {
				c = c + 32 // lowercase ASCII
			}
			b.WriteByte(c)
			prevWasSpace = false
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// stmtSQL extracts the raw SQL text for stmts[idx] from the full batch sql.
// Used to record the original PREPARE text in pg_prepared_statements.
// inferParamTypesFromStmt walks the parsed statement to infer $N parameter
// types from comparison contexts (column op $N, SET col = $N).
// declared contains already-declared param types (nil if none).
// Returns a slice of type names, or nil if nothing could be inferred.
func inferParamTypesFromStmt(stmt parser.Stmt, cat catalog.Catalog, declared []string) []string {
	if cat == nil {
		return nil
	}
	// Collect target table name and WHERE/SET source.
	var tblName parser.ObjectName
	var whereExpr parser.Expr
	var setAssigns []parser.UpdateAssign

	switch s := stmt.(type) {
	case *parser.SelectStmt:
		if len(s.From) > 0 {
			tblName = parser.ObjectName{Schema: s.From[0].Schema, Name: s.From[0].Name}
		}
		whereExpr = s.Where
	case *parser.UpdateStmt:
		tblName = parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name}
		whereExpr = s.Where
		setAssigns = s.Set
	default:
		return nil
	}

	// Build column type map from the primary table.
	colType := map[string]string{}
	if tbl, ok := cat.LookupTable(tblName); ok {
		for _, col := range tbl.Columns {
			colType[strings.ToLower(col.Name)] = col.Type.Name
		}
	}
	if len(colType) == 0 {
		return nil
	}

	// Find max param number.
	maxParam := len(declared)
	var walkCount func(e parser.Expr)
	walkCount = func(e parser.Expr) {
		if e == nil {
			return
		}
		if pr, ok := e.(*parser.ParamRef); ok && pr.Number > maxParam {
			maxParam = pr.Number
		}
		if bo, ok := e.(*parser.BinaryOp); ok {
			walkCount(bo.Left)
			walkCount(bo.Right)
		}
	}
	walkCount(whereExpr)
	for _, a := range setAssigns {
		if pr, ok := a.Expr.(*parser.ParamRef); ok && pr.Number > maxParam {
			maxParam = pr.Number
		}
	}
	if maxParam == 0 {
		return nil
	}

	// Initialize types from declared, defaulting to "".
	types := make([]string, maxParam)
	for i, dt := range declared {
		if i < maxParam {
			types[i] = strings.ToLower(dt)
		}
	}

	// Infer from WHERE binary comparisons: column op $N.
	var walkInfer func(e parser.Expr)
	walkInfer = func(e parser.Expr) {
		if e == nil {
			return
		}
		bo, ok := e.(*parser.BinaryOp)
		if !ok {
			return
		}
		// Try column op $N or $N op column.
		var colName string
		var paramNum int
		if cr, ok2 := bo.Left.(*parser.ColumnRef); ok2 {
			if pr, ok3 := bo.Right.(*parser.ParamRef); ok3 {
				colName = strings.ToLower(cr.Column)
				paramNum = pr.Number
			}
		} else if cr, ok2 := bo.Right.(*parser.ColumnRef); ok2 {
			if pr, ok3 := bo.Left.(*parser.ParamRef); ok3 {
				colName = strings.ToLower(cr.Column)
				paramNum = pr.Number
			}
		}
		if colName != "" && paramNum >= 1 && paramNum <= maxParam {
			if ct, ok := colType[colName]; ok && (types[paramNum-1] == "" || types[paramNum-1] == "unknown") {
				types[paramNum-1] = normResultType(ct)
			}
		}
		walkInfer(bo.Left)
		walkInfer(bo.Right)
	}
	walkInfer(whereExpr)

	// Infer from UPDATE SET col = $N.
	for _, a := range setAssigns {
		if pr, ok := a.Expr.(*parser.ParamRef); ok {
			paramNum := pr.Number
			if paramNum >= 1 && paramNum <= maxParam {
				if ct, ok2 := colType[strings.ToLower(a.Column)]; ok2 && (types[paramNum-1] == "" || types[paramNum-1] == "unknown") {
					types[paramNum-1] = normResultType(ct)
				}
			}
		}
	}

	// Only return inferred types if we found something useful.
	hasNew := false
	for _, t := range types {
		if t != "" && t != "unknown" {
			hasNew = true
			break
		}
	}
	if !hasNew {
		return nil
	}
	return types
}

// normResultType normalizes planner-internal type names to PostgreSQL canonical
// names as shown in pg_prepared_statements.result_types.
func normResultType(t string) string {
	switch strings.ToLower(t) {
	case "int4", "int8", "integer":
		return "integer"
	case "int2":
		return "smallint"
	case "bool":
		return "boolean"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "", "unknown":
		return "text"
	}
	return strings.ToLower(t)
}

// isValidSQLTypeName reports whether t is a known built-in SQL type name.
// Used to validate PREPARE parameter type declarations (SQLSTATE 42704).
func isValidSQLTypeName(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "int", "int2", "int4", "int8", "integer", "smallint", "bigint",
		"float", "float4", "float8", "real", "double", "double precision",
		"bool", "boolean",
		"text", "varchar", "char", "bpchar", "name",
		"oid", "xid", "cid", "tid",
		"date", "time", "timetz",
		"time without time zone", "time with time zone",
		"timestamp", "timestamptz",
		"timestamp without time zone", "timestamp with time zone",
		"interval",
		"numeric", "decimal",
		"bytea", "uuid",
		"json", "jsonb",
		"unknown", "void", "any", "anyarray", "anyelement", "record",
		"pg_lsn", "txid_snapshot",
		"path", "box", "circle", "line", "lseg", "polygon", "point":
		return true
	}
	return false
}

// execParamTypeIncompatible returns true when datum d cannot be implicitly
// coerced to targetType (lowercase). Boolean↔numeric is the main case PG rejects.
func execParamTypeIncompatible(d executor.Datum, targetType string) bool {
	isBool := d.Kind == executor.KindBool
	isNumericTarget := func() bool {
		switch targetType {
		case "int", "int2", "int4", "int8", "integer", "smallint", "bigint",
			"float", "float4", "float8", "real", "double", "double precision",
			"numeric", "decimal":
			return true
		}
		return false
	}
	if isBool && isNumericTarget() {
		return true
	}
	return false
}

// execParamKindName returns the PostgreSQL type name for a datum's kind,
// used in "parameter $N of type X cannot be coerced" error messages.
func execParamKindName(d executor.Datum) string {
	switch d.Kind {
	case executor.KindBool:
		return "boolean"
	case executor.KindInt:
		return "integer"
	case executor.KindNumeric:
		return "double precision"
	case executor.KindString:
		return "text"
	default:
		return "unknown"
	}
}

func stmtSQL(sql string, stmts []parser.Stmt, idx int) string {
	start := stmts[idx].Pos()
	end := len(sql)
	if idx+1 < len(stmts) {
		end = stmts[idx+1].Pos()
	}
	if end > len(sql) {
		end = len(sql)
	}
	raw := strings.TrimRight(sql[start:end], " \t\n\r")
	// PostgreSQL's pg_prepared_statements always shows a trailing semicolon.
	if !strings.HasSuffix(raw, ";") {
		raw += ";"
	}
	return raw
}

func evalExecuteParams(params []parser.Expr) ([]executor.Datum, error) {
	if len(params) == 0 {
		return nil, nil
	}
	out := make([]executor.Datum, len(params))
	for i, p := range params {
		d, err := evalConstExpr(p)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}

// evalConstExpr evaluates a constant expression (no column refs) to a Datum.
// Used for EXECUTE parameter binding. Handles literals and casts.
func evalConstExpr(e parser.Expr) (executor.Datum, error) {
	switch v := e.(type) {
	case *parser.IntegerConst:
		return executor.NewIntDatum(v.Value), nil
	case *parser.StringConst:
		return executor.NewStringDatum(v.Value), nil
	case *parser.NumericConst:
		return executor.NewStringDatum(v.Value), nil
	case *parser.BooleanConst:
		return executor.NewBoolDatum(v.Value), nil
	case *parser.NullConst:
		return executor.NullDatum, nil
	case *parser.TypedStringLit:
		return executor.NewStringDatum(v.Value), nil
	case *parser.UnaryOp:
		// Handle unary minus on numeric literals: -5, -10.5
		inner, err := evalConstExpr(v.Operand)
		if err != nil {
			return executor.NullDatum, err
		}
		if v.Op == parser.OpSub {
			if inner.Kind == executor.KindInt {
				return executor.NewIntDatum(-inner.Int), nil
			}
			// For string/numeric, prepend "-" and re-parse as string datum.
			return executor.NewStringDatum("-" + inner.StringValue()), nil
		}
		return inner, nil
	case *parser.CastExpr:
		// ::type cast: evaluate operand then coerce kind for the target type.
		inner, err := evalConstExpr(v.Operand)
		if err != nil {
			return executor.NullDatum, err
		}
		return coerceExecParam(inner, v.Type.Name), nil
	default:
		return executor.NullDatum, fmt.Errorf("EXECUTE parameter type %T not supported", e)
	}
}

// coerceExecParam coerces a Datum to match the target type for EXECUTE parameters.
// Integer and numeric types are kept as-is since the executor evaluates
// predicates at runtime with the correct type comparison.
func coerceExecParam(d executor.Datum, targetType string) executor.Datum {
	switch strings.ToLower(targetType) {
	case "int2", "smallint", "int4", "integer", "int", "int8", "bigint":
		if d.Kind == executor.KindString {
			if n, err := strconv.ParseInt(d.StringValue(), 10, 64); err == nil {
				return executor.NewIntDatum(n)
			}
		}
		if d.Kind == executor.KindNumeric {
			return executor.NewIntDatum(d.NumericMantissaValue())
		}
		return d
	case "float4", "real", "float8", "double precision", "float", "double":
		if d.Kind == executor.KindString {
			s := d.StringValue()
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				// Store as KindNumeric so casts like $3::bigint in the prepared
				// body hit the numeric→int path (roundNumericToInt) rather than
				// the string→int path which rejects "10.5". M0097-0021.
				formatted := strconv.FormatFloat(f, 'f', -1, 64)
				if i := strings.IndexByte(formatted, '.'); i >= 0 {
					frac := formatted[i+1:]
					mant, _ := strconv.ParseInt(strings.ReplaceAll(formatted, ".", ""), 10, 64)
					return executor.NewNumericInt64Datum(mant, int16(len(frac)))
				}
				mant, _ := strconv.ParseInt(formatted, 10, 64)
				return executor.NewNumericInt64Datum(mant, 0)
			}
		}
		return d
	default:
		return d
	}
}

// executeOneSimpleStmt plans and runs one statement, emitting the
// per-statement wire messages but NOT ReadyForQuery (the caller
// terminates the batch).
//
// connTx, if non-nil, tracks the per-connection explicit transaction
// state so BEGIN/COMMIT/ROLLBACK can open/close real TxnMgr transactions.
// undoEnumDDLForRollback reverses enum DDL (ADD VALUE, RENAME TO, CREATE TYPE AS ENUM)
// recorded in connTx.  Must be called before connTx.End() on ROLLBACK paths.  M0097-0022.
// extensionLister is implemented by catalogs that can scope pg_extension rows
// to a single database (catalog.InMemory). M0110-0003 (AC-002 gap #7c).
type extensionLister interface {
	ExtensionRowsForDB(db string) [][]string
}

// wireExtensionRows installs the per-database pg_extension view on ectx so an
// extension installed in one database is invisible in another (PostgreSQL's
// pg_extension is per-database; goopg shares one in-memory catalog). Used by
// both the simple- and extended-query executor paths. M0110-0003 (gap #7c).
func (s *Server) wireExtensionRows(ectx *executor.Context, dbName string) {
	ectx.CurrentDatabase = dbName
	if el, ok := s.cfg.Catalog.(extensionLister); ok {
		ectx.ExtensionRows = func() [][]string { return el.ExtensionRowsForDB(dbName) }
	}
}

func undoEnumDDLForRollback(connTx *connTxState, cat catalog.Catalog) {
	if connTx == nil {
		return
	}
	inm, ok := cat.(*catalog.InMemory)
	if !ok {
		return
	}
	// Step 1: Remove enum values added via ALTER TYPE … ADD VALUE in this tx.
	// Do before undo-renames so type names are still at current (renamed) values.
	for typeName, labels := range connTx.PendingEnumValues {
		for label := range labels {
			inm.RemoveEnumValue(typeName, label)
		}
	}
	// Step 2: Undo renames in reverse order; track name changes in created-set.
	created := make(map[string]bool, len(connTx.PendingCreatedEnums))
	for k, v := range connTx.PendingCreatedEnums {
		created[k] = v
	}
	for i := len(connTx.PendingEnumRenames) - 1; i >= 0; i-- {
		r := connTx.PendingEnumRenames[i]
		_ = inm.RenameEnum(r.NewName, r.OldName)
		if created[r.NewName] {
			delete(created, r.NewName)
			created[r.OldName] = true
		}
	}
	// Step 3: Drop types created in this transaction (now at original names).
	for name := range created {
		_ = inm.DropEnum(name, false)
	}
}

// autoCommitPtr, if non-nil, is set to false when a BEGIN starts an
// explicit transaction (telling the caller not to auto-commit).
// cachedNode, when non-nil, is a pre-validated plan from the cross-session
// plan cache — planner.Plan is skipped. M0098-0005.
func (s *Server) executeOneSimpleStmt(w *protocol.FrameWriter, ctx *executor.Context, stmt parser.Stmt, connTx *connTxState, autoCommitPtr *bool, cachedNode ...planner.Node) error {
	var node planner.Node
	if len(cachedNode) > 0 && cachedNode[0] != nil {
		node = cachedNode[0]
	} else {
		var err error
		node, err = planner.Plan(stmt, ctxPlanCatalog(ctx, s.cfg.Catalog))
		if err != nil {
			code, msg := planErrorFields(err)
			return s.writeQueryError(w, code, msg, planErrorHintFields(err)...)
		}
		// Note: plan cache storage happens at the dispatch level (caller
		// stores if cacheKey was computed). This function only executes.
	}
	// Transaction verbs: BEGIN/COMMIT/ROLLBACK require per-connection
	// explicit transaction management (M0096-0005). BEGIN promotes the
	// current auto-commit transaction into a persistent explicit one;
	// COMMIT/ROLLBACK finalise it and release the session-level state.
	if txNode, ok := node.(*planner.Transaction); ok {
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
						return s.writeQueryError(w, sqlstate.SyntaxError, perr.Error())
					}
					if parsedLvl != ctx.Tx.Isolation {
						// Roll back the placeholder auto-commit tx so it does
						// not consume an XID / leak SSI bookkeeping.
						_ = s.cfg.TxnMgr.Rollback(ctx.Tx)
						newTx, berr := s.cfg.TxnMgr.Begin(parsedLvl, ctx.ProcNum)
						if berr != nil {
							return s.writeQueryError(w, sqlstate.SystemError, berr.Error())
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
								return s.writeQueryError(w, sqlstate.SystemError, serr.Error())
							}
							ctx.Snap = snap
						}
					}
				}
				connTx.Begin(ctx.Tx)
				// Propagate READ ONLY / READ WRITE mode from START TRANSACTION / BEGIN.
				if connTx.Session() != nil {
					connTx.Session().SetReadOnlyTxn(txNode.ReadOnly)
				}
				if ctx.BeginLocalTransaction != nil {
					ctx.BeginLocalTransaction()
				}
			}
			return w.WriteCommandComplete(transactionTag(txNode.Verb))
		case planner.TxCommit:
			if connTx != nil && connTx.InExplicit() {
				// COMMIT in a failed transaction → PostgreSQL semantics: issue
				// a WARNING and ROLLBACK instead of committing. M0100-0005.
				if connTx.IsFailed() {
					// COMMIT in a failed transaction block → ROLLBACK (PG semantics).
					// Restore routines dropped in this transaction.
					if sess := connTx.Session(); sess != nil {
						if rs := s.cfg.Catalog.Routines(); rs != nil {
							for _, r := range sess.TakePendingRoutineDrops() {
								_, _ = rs.Create(r, true)
							}
						}
					}
					_ = s.cfg.TxnMgr.Rollback(connTx.Tx())
					undoEnumDDLForRollback(connTx, s.cfg.Catalog)
					connTx.End()
					if ctx.EndLocalTransaction != nil {
						ctx.EndLocalTransaction()
					}
					ctx.PendingEnumValues = nil
					ctx.PendingEnumRenames = nil
					ctx.PendingCreatedEnums = nil
					return w.WriteCommandComplete("ROLLBACK")
				}
				explicitTx := connTx.Tx()
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
						// SSI failure: rollback and restore dropped routines.
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
						if ctx.EndLocalTransaction != nil {
							ctx.EndLocalTransaction()
						}
						ctx.PendingEnumValues = nil
						ctx.PendingEnumRenames = nil
						ctx.PendingCreatedEnums = nil
						return s.writeQueryError(w, "40001",
							"could not serialize access due to read/write dependencies among transactions: "+ssiErr.Error())
					}
				}
				if err := s.cfg.TxnMgr.Commit(explicitTx); err != nil {
					undoEnumDDLForRollback(connTx, s.cfg.Catalog)
					connTx.End()
					if ctx.EndLocalTransaction != nil {
						ctx.EndLocalTransaction()
					}
					ctx.PendingEnumValues = nil
					ctx.PendingEnumRenames = nil
					ctx.PendingCreatedEnums = nil
					return s.writeQueryError(w, sqlstate.SystemError, err.Error())
				}
				// Clear pending routine drops — committed, no restoration needed.
				if sess := connTx.Session(); sess != nil {
					sess.TakePendingRoutineDrops()
				}
				connTx.End()
				if ctx.EndLocalTransaction != nil {
					ctx.EndLocalTransaction()
				}
				ctx.PendingEnumValues = nil
				ctx.PendingEnumRenames = nil
				ctx.PendingCreatedEnums = nil
				maybeForceGCAfterCommit()
				// Leave *autoCommitPtr = false so the caller does NOT attempt
				// a second TxnMgr.Commit on the already-committed transaction.
			} else {
				// COMMIT outside an explicit transaction: emit warning.
				_ = w.WriteNoticeResponse([]protocol.ErrorField{
					{Code: protocol.FieldSeverity, Value: "WARNING"},
					{Code: protocol.FieldSeverityNonLocal, Value: "WARNING"},
					{Code: protocol.FieldSQLState, Value: "25P01"},
					{Code: protocol.FieldMessage, Value: "there is no transaction in progress"},
				})
			}
			return w.WriteCommandComplete(transactionTag(txNode.Verb))
		case planner.TxRollback:
			if connTx != nil && connTx.InExplicit() {
				// Undo DDL creates, TRUNCATE page snapshots, and RESTART IDENTITY
				// before TxnMgr.Rollback so catalog lookups still work.
				if sess := connTx.Session(); sess != nil {
					executor.ProcessRollbackUndos(ctx, sess)
					// Restore any routines dropped in this transaction.
					if rs := s.cfg.Catalog.Routines(); rs != nil {
						for _, r := range sess.TakePendingRoutineDrops() {
							_, _ = rs.Create(r, true)
						}
					}
				}
				_ = s.cfg.TxnMgr.Rollback(connTx.Tx())
				undoEnumDDLForRollback(connTx, s.cfg.Catalog)
				connTx.End()
				if ctx.EndLocalTransaction != nil {
					ctx.EndLocalTransaction()
				}
				ctx.PendingEnumValues = nil
				ctx.PendingEnumRenames = nil
				ctx.PendingCreatedEnums = nil
				// Leave *autoCommitPtr = false to avoid a second rollback attempt.
			} else {
				// ROLLBACK outside an explicit transaction: emit warning.
				_ = w.WriteNoticeResponse([]protocol.ErrorField{
					{Code: protocol.FieldSeverity, Value: "WARNING"},
					{Code: protocol.FieldSeverityNonLocal, Value: "WARNING"},
					{Code: protocol.FieldSQLState, Value: "25P01"},
					{Code: protocol.FieldMessage, Value: "there is no transaction in progress"},
				})
			}
			return w.WriteCommandComplete(transactionTag(txNode.Verb))
		}
		// SAVEPOINT, ROLLBACK TO, and RELEASE fall through to BuildFastIterator
		// so execSavepoint / execRollbackTo / execRelease run properly (M0097-0023).
	}
	op, err := executor.BuildFastIterator(node)
	if err != nil {
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
	}

	// Emit RowDescription for read-shaped plans (those whose Output
	// schema is non-nil); writing operators (Insert/Update/Delete/
	// DDL/Transaction) return nil from Output() and emit only the
	// command tag.
	schema := node.Output()
	// CALL plans have a dynamic schema that depends on the procedure's
	// OUT params; the operator reports it after Open.
	if schema == nil {
		schema = op.Schema()
	}
	// Send RowDescription when schema is non-nil (even 0 columns —
	// e.g. `SELECT;` returns 1 row with 0 columns per PostgreSQL).
	if schema != nil {
		fields := make([]protocol.FieldDescription, len(schema))
		for i, sc := range schema {
			fields[i] = protocol.FieldDescription{
				Name:         sc.Name,
				TypeOID:      typeOIDFor(sc.Type.Name),
				TypeSize:     -1,
				TypeModifier: -1,
				Format:       0,
			}
		}
		if err := w.WriteRowDescription(fields); err != nil {
			_ = op.Close()
			return err
		}
	}

	var rowCount int64
	for {
		slot, err := op.Next()
		if err == executor.EOF {
			break
		}
		if err != nil {
			_ = op.Close()
			return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
		}
		if schema != nil {
			row := slot.Row()
			// M0092-0004: per-connection scratch buffers back the
			// wire frame so the simple-query result loop is O(1)
			// allocation across rows AND statements.
			cells, valueBuf := w.DataRowScratch(len(row))
			for i, d := range row {
				if d.IsNull() {
					cells = append(cells, nil)
					continue
				}
				start := len(valueBuf)
				if i < len(schema) {
					sc := schema[i]
					switch strings.ToLower(sc.Type.Name) {
					case "float4", "real":
						// float4/real uses float32 precision (~7 significant digits).
						// Use strconv bit=32 so the shortest float32 round-trip representation
						// is produced (e.g. 4.56789e+15 not 4.567889919082496e+15). M0097-0022.
						valueBuf = appendFloatText(valueBuf, d, 32)
					case "float8", "double precision", "double":
						// float8/float4 values must display in PostgreSQL's output format:
						// scientific notation for very large/small values, shortest decimal
						// for normal ones. Convert KindNumeric to float64 and use %g. M0097-0003.
						valueBuf = appendFloat8Text(valueBuf, d)
					case "char", "bpchar":
						// bpcharout (PG) uses bcTruelen which trims trailing spaces before
						// sending over the wire. Input coercion already strips trailing spaces
						// (codec.go), so just emit the stored value without re-padding.
						valueBuf = d.AppendValueText(valueBuf)
					case "date":
						// Date columns display as YYYY-MM-DD. M0097-0004.
						if d.Kind == executor.KindTime {
							valueBuf = d.TimeValue().AppendFormat(valueBuf, "2006-01-02")
						} else {
							valueBuf = d.AppendValueText(valueBuf)
						}
					case "time":
						// Time columns display as HH:MM:SS[.ffffff] with column precision. M0097-0004.
						if d.Kind == executor.KindTime {
							valueBuf = appendTimeText(valueBuf, d, sc.Type)
						} else {
							valueBuf = d.AppendValueText(valueBuf)
						}
					case "timetz":
						// Timetz displays as HH:MM:SS[.ffffff]±HH[:MM]. M0097-0004.
						if d.Kind == executor.KindTime {
							valueBuf = appendTimeText(valueBuf, d, sc.Type)
							valueBuf = appendTimeTZOffset(valueBuf, d.TimeTZOffsetSecs())
						} else {
							valueBuf = d.AppendValueText(valueBuf)
						}
					case "bytea":
						// Bytea values display as \xhexstring (default hex mode). M0097-0035.
						if d.Kind == executor.KindBytes {
							valueBuf = append(valueBuf, '\\', 'x')
							const hexChars = "0123456789abcdef"
							for _, b := range d.BytesValue() {
								valueBuf = append(valueBuf, hexChars[b>>4], hexChars[b&0x0f])
							}
						} else {
							valueBuf = d.AppendValueText(valueBuf)
						}
					case "regclass":
						// OID values with type regclass display as relation names. M0097-0023.
						if d.Kind == executor.KindInt {
							oid := uint32(d.Int)
							found := false
							if im, ok2 := s.cfg.Catalog.(*catalog.InMemory); ok2 {
								if tbl, ok3 := im.LookupTableByOID(oid); ok3 {
									valueBuf = append(valueBuf, tbl.Name...)
									found = true
								} else if idx, ok3 := im.LookupIndexByOID(oid); ok3 {
									valueBuf = append(valueBuf, idx.Name...)
									found = true
								}
							}
							if !found {
								valueBuf = d.AppendValueText(valueBuf)
							}
						} else {
							valueBuf = d.AppendValueText(valueBuf)
						}
					default:
						valueBuf = d.AppendValueText(valueBuf)
					}
				} else {
					valueBuf = d.AppendValueText(valueBuf)
				}
				cells = append(cells, valueBuf[start:len(valueBuf)])
			}
			if err := w.PutDataRowScratch(cells, valueBuf); err != nil {
				_ = op.Close()
				return err
			}
			rowCount++
		}
	}
	if err := op.Close(); err != nil {
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
	}

	// Emit accumulated NOTICE messages before CommandComplete. M0097-0008.
	for _, msg := range ctx.TakeNotices() {
		if nerr := w.WriteNoticeResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "NOTICE"},
			{Code: protocol.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: protocol.FieldSQLState, Value: "00000"},
			{Code: protocol.FieldMessage, Value: msg},
		}); nerr != nil {
			return nerr
		}
	}
	// Emit NOTICE+DETAIL messages (e.g. DROP CASCADE cascade list). M0097-0020.
	for _, n := range ctx.TakeNoticesWithDetail() {
		fields := []protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "NOTICE"},
			{Code: protocol.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: protocol.FieldSQLState, Value: "00000"},
			{Code: protocol.FieldMessage, Value: n.Message},
		}
		if n.Detail != "" {
			fields = append(fields, protocol.ErrorField{Code: protocol.FieldDetail, Value: n.Detail})
		}
		if nerr := w.WriteNoticeResponse(fields); nerr != nil {
			return nerr
		}
	}

	// Emit accumulated WARNING messages before CommandComplete. M0097-0021.
	for _, msg := range ctx.TakeWarnings() {
		if nerr := w.WriteNoticeResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "WARNING"},
			{Code: protocol.FieldSeverityNonLocal, Value: "WARNING"},
			{Code: protocol.FieldSQLState, Value: "55000"},
			{Code: protocol.FieldMessage, Value: msg},
		}); nerr != nil {
			return nerr
		}
	}

	tag := commandTagFor(node, op, rowCount)
	if tag == "" {
		tag = "OK"
	}
	// Invalidate plan cache after DDL so stale schema references are
	// never reused by concurrent sessions. M0098-0005.
	if _, isDDL := node.(*planner.DDL); isDDL && s.pc != nil {
		s.pc.Invalidate()
	}
	return w.WriteCommandComplete(tag)
}

// commandTagFor builds the upstream-shaped CommandComplete tag for
// the executed plan. Matches the strings libpq uses to drive
// `PQcmdStatus` / `PQcmdTuples`.
func commandTagFor(node planner.Node, op executor.Operator, rowCount int64) string {
	switch n := node.(type) {
	case *planner.DDL:
		return ddlTag(n.Stmt)
	case *planner.Insert:
		return fmt.Sprintf("INSERT 0 %d", rowsAffected(op))
	case *planner.Update:
		return fmt.Sprintf("UPDATE %d", rowsAffected(op))
	case *planner.Delete:
		return fmt.Sprintf("DELETE %d", rowsAffected(op))
	case *planner.Transaction:
		return transactionTag(n.Verb)
	case *planner.Utility:
		return utilityTag(n.Stmt)
	case *planner.Checkpoint:
		_ = n
		return "CHECKPOINT"
	case *planner.Explain:
		_ = n
		return "EXPLAIN"
	case *planner.Call:
		_ = n
		return "CALL"
	}
	// Read-shaped: SELECT N. Catches Project/Sort/Limit/Filter/Aggregate/
	// Join/SeqScan/IndexScan/Values root nodes.
	return fmt.Sprintf("SELECT %d", rowCount)
}

func transactionTag(v planner.TransactionVerb) string {
	switch v {
	case planner.TxBegin:
		return "BEGIN"
	case planner.TxCommit:
		return "COMMIT"
	case planner.TxRollback:
		return "ROLLBACK"
	case planner.TxSavepoint:
		return "SAVEPOINT"
	case planner.TxRelease:
		return "RELEASE"
	case planner.TxRollbackTo:
		return "ROLLBACK"
	}
	return "OK"
}

func ddlTag(stmt parser.Stmt) string {
	switch stmt.(type) {
	case *parser.CreateTableStmt:
		return "CREATE TABLE"
	case *parser.CreateIndexStmt:
		return "CREATE INDEX"
	case *parser.DropTableStmt:
		return "DROP TABLE"
	case *parser.DropIndexStmt:
		return "DROP INDEX"
	case *parser.CreateViewStmt:
		return "CREATE VIEW"
	case *parser.DropViewStmt:
		return "DROP VIEW"
	case *parser.TruncateStmt:
		return "TRUNCATE TABLE"
	case *parser.AlterTableStmt:
		return "ALTER TABLE"
	case *parser.CreateTypeStmt:
		return "CREATE TYPE"
	case *parser.AlterTypeStmt:
		return "ALTER TYPE"
	case *parser.DropTypeStmt:
		return "DROP TYPE"
	case *parser.CreateDomainStmt:
		return "CREATE DOMAIN"
	case *parser.DropDomainStmt:
		return "DROP DOMAIN"
	case *parser.CreateExtensionStmt:
		return "CREATE EXTENSION"
	case *parser.CreateTablespaceStmt:
		return "CREATE TABLESPACE"
	case *parser.DropTablespaceStmt:
		return "DROP TABLESPACE"
	}
	// CompatNoopStmt carries its own tag. M0097-0016.
	if ns, ok := stmt.(*parser.CompatNoopStmt); ok && ns.Tag != "" {
		return ns.Tag
	}
	if _, ok := stmt.(*parser.CommentOnStmt); ok {
		return "COMMENT"
	}
	return "OK"
}

func utilityTag(stmt parser.Stmt) string {
	switch stmt.(type) {
	case *parser.VacuumStmt:
		return "VACUUM"
	case *parser.AnalyzeStmt:
		return "ANALYZE"
	case *parser.ShowStmt:
		return "SHOW"
	case *parser.SetStmt:
		return "SET"
	case *parser.ResetStmt:
		return "RESET"
	case *parser.DiscardStmt:
		return "DISCARD"
	}
	return "OK"
}

func rowsAffected(op executor.Operator) int64 {
	if rc, ok := op.(executor.RowCounter); ok {
		return rc.RowsAffected()
	}
	return 0
}

// appendFloat8Text formats a datum for wire output as a float8/float4 value.
// Uses strconv.FormatFloat so large/small values display in scientific notation
// appendFloatText formats a datum for wire output using the specified bitSize (32 or 64).
// bitSize=32 gives float32 precision (shortest round-trip via float32), bitSize=64 gives float8.
func appendFloatText(dst []byte, d executor.Datum, bitSize int) []byte {
	if d.IsNull() {
		return dst
	}
	var f float64
	switch d.Kind {
	case executor.KindInt:
		if bitSize == 32 {
			f = float64(float32(d.Int))
		} else {
			f = float64(d.Int)
		}
	case executor.KindString:
		s := d.StringValue()
		if parsed, err := strconv.ParseFloat(s, bitSize); err == nil {
			f = parsed
		} else {
			return append(dst, s...)
		}
	default:
		s := d.Format()
		if parsed, err := strconv.ParseFloat(s, bitSize); err == nil {
			f = parsed
		} else {
			return append(dst, s...)
		}
	}
	if math.IsInf(f, 1) {
		return append(dst, "Infinity"...)
	}
	if math.IsInf(f, -1) {
		return append(dst, "-Infinity"...)
	}
	if math.IsNaN(f) {
		return append(dst, "NaN"...)
	}
	s := strconv.FormatFloat(f, 'g', -1, bitSize)
	if idx := strings.IndexByte(s, 'e'); idx >= 0 {
		exp, err := strconv.Atoi(s[idx+1:])
		if err == nil && exp >= 1 && exp <= 14 {
			s = strconv.FormatFloat(f, 'f', -1, bitSize)
		}
	}
	return append(dst, s...)
}

// (e.g. 1.2345678901234e+200) matching PostgreSQL's float8out behavior. M0097-0003.
func appendFloat8Text(dst []byte, d executor.Datum) []byte {
	if d.IsNull() {
		return dst
	}
	// Convert datum to float64.
	var f float64
	switch d.Kind {
	case executor.KindInt:
		f = float64(d.Int)
	case executor.KindString:
		s := d.StringValue()
		if parsed, err := strconv.ParseFloat(s, 64); err == nil {
			f = parsed
		} else {
			// NaN / infinity / unparseable — return as-is.
			return append(dst, s...)
		}
	default:
		// KindNumeric: convert via text representation.
		s := d.Format()
		if parsed, err := strconv.ParseFloat(s, 64); err == nil {
			f = parsed
		} else {
			return append(dst, s...)
		}
	}
	// PostgreSQL uses canonical names for special values, not Go's "+Inf"/"-Inf".
	if math.IsInf(f, 1) {
		return append(dst, "Infinity"...)
	}
	if math.IsInf(f, -1) {
		return append(dst, "-Infinity"...)
	}
	if math.IsNaN(f) {
		return append(dst, "NaN"...)
	}
	// PostgreSQL's float8out uses the shortest round-trip representation.
	// Go's 'g',-1 uses scientific notation for exponents >= 1, but PostgreSQL
	// uses decimal for exponents in [1,14] (equivalent to %.15g). Convert back
	// to decimal in that range to match PostgreSQL's formatting.
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if idx := strings.IndexByte(s, 'e'); idx >= 0 {
		exp, err := strconv.Atoi(s[idx+1:])
		if err == nil && exp >= 1 && exp <= 14 {
			s = strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return append(dst, s...)
}

// appendTimeText formats a KindTime datum as a time-of-day string matching PostgreSQL's
// time output format: HH:MM:SS with optional fractional seconds up to the declared precision.
// Precision 0 → "HH:MM:SS", precision N → "HH:MM:SS.ffffff" (N digits). M0097-0004.
func appendTimeText(dst []byte, d executor.Datum, typ catalog.Type) []byte {
	if d.IsNull() {
		return dst
	}
	t := d.TimeValue()
	h := t.Hour()
	m := t.Minute()
	s := t.Second()
	ns := t.Nanosecond()
	// 24:00:00 is stored as 1970-01-02 00:00:00 (next-day midnight).
	if t.Day() == 2 && t.Month() == 1 && t.Year() == 1970 && h == 0 && m == 0 && s == 0 && ns == 0 {
		return append(dst, "24:00:00"...)
	}

	dst = append(dst, byte('0'+h/10), byte('0'+h%10), ':',
		byte('0'+m/10), byte('0'+m%10), ':',
		byte('0'+s/10), byte('0'+s%10))

	// Fractional seconds — only emit if non-zero or precision requested.
	prec := 6 // default microseconds
	if len(typ.Args) > 0 && typ.Args[0] >= 0 {
		prec = int(typ.Args[0])
	}
	if prec > 0 && ns != 0 {
		// Format up to 6 microsecond digits, then trim to declared precision.
		micro := ns / 1000
		frac := make([]byte, 6)
		for i := 5; i >= 0; i-- {
			frac[i] = byte('0' + micro%10)
			micro /= 10
		}
		// Trim to declared precision.
		if prec < 6 {
			frac = frac[:prec]
		}
		// Strip trailing zeros after applying precision.
		for len(frac) > 0 && frac[len(frac)-1] == '0' {
			frac = frac[:len(frac)-1]
		}
		if len(frac) > 0 {
			dst = append(dst, '.')
			dst = append(dst, frac...)
		}
	}
	return dst
}

// appendTimeTZOffset appends the timezone offset to dst in PostgreSQL's format:
// "+HH", "-HH", "+HH:MM", or "-HH:MM". offsetSecs is seconds east of UTC.
// UTC (0) is rendered as "+00".
func appendTimeTZOffset(dst []byte, offsetSecs int) []byte {
	if offsetSecs < 0 {
		dst = append(dst, '-')
		offsetSecs = -offsetSecs
	} else {
		dst = append(dst, '+')
	}
	h := offsetSecs / 3600
	m := (offsetSecs % 3600) / 60
	dst = append(dst, byte('0'+h/10), byte('0'+h%10))
	if m != 0 {
		dst = append(dst, ':', byte('0'+m/10), byte('0'+m%10))
	}
	return dst
}

// typeOIDFor maps a goopg type name to a pg_type.oid the wire
// protocol can advertise. Unknown types fall back to text (25),
// which is wire-compatible with libpq's text-format reader.
func typeOIDFor(name string) uint32 {
	switch strings.ToLower(name) {
	case "int2", "smallint", "smallserial":
		return 21
	case "int4", "integer", "int", "serial":
		return 23
	case "int8", "bigint", "bigserial":
		return 20
	case "float4", "real":
		return 700
	case "float", "float8", "double precision", "double":
		return 701
	case "bool", "boolean":
		return 16
	case "oid":
		return 26
	case "oidvector":
		return 30
	case "name":
		return 19
	case "uuid":
		return 2950
	case "date":
		return 1082
	case "time":
		return 1083
	case "timetz":
		return 1266
	case "interval":
		return 1186
	case "timestamp":
		return 1114
	case "timestamptz":
		return 1184
	case "text", "":
		return 25
	case "varchar":
		return 1043
	case "char", "bpchar":
		return 1042
	case "numeric", "decimal":
		return 1700
	case "pg_lsn":
		return 3220
	}
	return 25
}

// executeFetch executes or resumes a cursor fetch. It materialises the cursor's
// result set on first access and then tracks the cursor position across FETCH
// FORWARD / FETCH BACKWARD calls. count < 0 means ALL. forward=true for FORWARD
// (default), false for BACKWARD. M0097-0042 cursor position tracking.
func (s *Server) executeFetch(_ context.Context, w *protocol.FrameWriter, ectx *executor.Context, cur *cursorEntry, cursorName string, count int64, forward bool) error {
	// Materialise on first access.
	if !cur.Materialized {
		if err := s.materializeCursor(ectx, cur, cursorName); err != nil {
			return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
		}
	}

	schema := cur.Schema
	if schema != nil {
		fields := make([]protocol.FieldDescription, len(schema))
		for i, sc := range schema {
			fields[i] = protocol.FieldDescription{
				Name:         sc.Name,
				TypeOID:      typeOIDFor(sc.Type.Name),
				TypeSize:     -1,
				TypeModifier: -1,
				Format:       0,
			}
		}
		if err := w.WriteRowDescription(fields); err != nil {
			return err
		}
	}

	// Determine which rows to emit based on direction and position.
	//
	// Cursor position model (PostgreSQL semantics, M0097-0042):
	//   pos=0       = BOF (before first row)
	//   pos=1..N    = AT row k; next FORWARD returns rows[k..], next BACKWARD returns rows[k-1]
	//   pos=N+1     = EOF (past last row); FORWARD returns nothing
	//
	// FETCH FORWARD n from pos P: return rows[P..P+n-1] (0-indexed), new pos = min(P+n, N)
	//   (where N = total rows; we use N not N+1 as EOF sentinel because len(cur.Rows) == N)
	// FETCH BACKWARD n (finite) from pos P: return rows[P-n..P-1] reversed, new pos = max(P-n, 1)
	//   The minimum finite-backward pos is 1 (not 0 = BOF). Only BACKWARD ALL reaches BOF.
	// FETCH BACKWARD ALL from pos P: return rows[0..P-1] reversed, new pos = 0 (BOF)
	// FETCH FORWARD ALL from pos P: return rows[P..N-1], new pos = N (EOF)
	total := len(cur.Rows)
	fetchAll := count < 0
	var rowsToSend []executor.Row
	if forward {
		// FETCH [FORWARD] [n|ALL]
		start := cur.Pos
		if start >= total {
			start = total
		}
		end := total // ALL
		if !fetchAll {
			end = start + int(count)
			if end > total {
				end = total
			}
		}
		rowsToSend = cur.Rows[start:end]
		if fetchAll {
			cur.Pos = total // EOF
		} else {
			cur.Pos = end
		}
	} else {
		// FETCH BACKWARD [n|ALL]
		end := cur.Pos // exclusive upper bound for 0-indexed slice
		if end > total {
			end = total
		}
		start := 0 // ALL: go all the way to BOF
		if !fetchAll {
			start = end - int(count)
			if start < 0 {
				start = 0
			}
		}
		// The rows from start..end-1 in reverse order.
		n := end - start
		rev := make([]executor.Row, n)
		for i := 0; i < n; i++ {
			rev[i] = cur.Rows[start+n-1-i]
		}
		rowsToSend = rev
		if fetchAll {
			cur.Pos = 0 // BOF
		} else {
			// Finite BACKWARD: new pos = max(P-n, 1) when P > 0; stays at 0 when already at BOF.
			// Minimum is 1 (not 0=BOF) so that the row just fetched can be re-fetched on the next
			// FETCH ALL — but only when we actually were above BOF. M0097-0042.
			if end == 0 {
				cur.Pos = 0 // already at BOF, no change
			} else {
				newPos := end - int(count)
				if newPos < 1 {
					newPos = 1
				}
				cur.Pos = newPos
			}
		}
	}

	var rowCount int64
	for _, row := range rowsToSend {
		if schema != nil {
			cells, valueBuf := w.DataRowScratch(len(row))
			for _, d := range row {
				if d.IsNull() {
					cells = append(cells, nil)
					continue
				}
				start := len(valueBuf)
				valueBuf = d.AppendValueText(valueBuf)
				cells = append(cells, valueBuf[start:len(valueBuf)])
			}
			if err := w.PutDataRowScratch(cells, valueBuf); err != nil {
				return err
			}
		}
		rowCount++
	}
	return w.WriteCommandComplete(fmt.Sprintf("FETCH %d", rowCount))
}

// materializeCursor executes the cursor's SELECT once and buffers all rows in cur.
func (s *Server) materializeCursor(ectx *executor.Context, cur *cursorEntry, cursorName string) error {
	stmts, err := parser.Parse(cur.SQL)
	if err != nil {
		return &executor.ExecError{Code: "26000", Message: fmt.Sprintf("cursor query parse error: %v", err)}
	}
	var selectStmt parser.Stmt
	for _, st := range stmts {
		if dc, ok := st.(*parser.DeclareCursorStmt); ok {
			if strings.EqualFold(dc.Name, cursorName) {
				selectStmt = dc.Query
				break
			}
		}
		if _, ok := st.(*parser.SelectStmt); ok {
			selectStmt = st
			break
		}
	}
	if selectStmt == nil {
		return &executor.ExecError{Code: "26000", Message: fmt.Sprintf("cursor \"%s\" query not found", cursorName)}
	}

	node, planErr := planner.Plan(selectStmt, ctxPlanCatalog(ectx, s.cfg.Catalog))
	if planErr != nil {
		code, msg := planErrorFields(planErr)
		return &executor.ExecError{Code: string(code), Message: msg}
	}
	op, buildErr := executor.BuildFastIterator(node)
	if buildErr != nil {
		return buildErr
	}
	if openErr := op.Open(ectx); openErr != nil {
		_ = op.Close()
		return openErr
	}
	defer func() { _ = op.Close() }()

	cur.Schema = op.Schema()
	cur.Rows = nil
	for {
		slot, nextErr := op.Next()
		if nextErr == executor.EOF {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		// Clone the row so it survives after the operator is closed.
		if slot != nil {
			row := slot.Row()
			cloned := make(executor.Row, len(row))
			copy(cloned, row)
			cur.Rows = append(cur.Rows, cloned)
		}
	}
	cur.Pos = 0
	cur.Materialized = true
	return nil
}
