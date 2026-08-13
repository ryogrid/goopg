package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// executeExtendedQueryViaExecutor is the parser→planner→executor
// path the extended-query protocol takes when the server has storage
// handles wired. Unlike the simple-query path which streams rows
// directly to the wire writer, the extended path materialises rows
// into the portal so multiple Execute(maxRows) batches can drain
// the same portal — that's what `extendedQueryResult` carries.
//
// Bind parameters arrive as text-format strings (binary parameters
// are rejected at Bind time); we feed them through to
// executor.Context.Params and let the executor's expression
// evaluator coerce inside ParamRef.
func (s *Server) executeExtendedQueryViaExecutor(ctx context.Context, sess *config.SessionRegistry, query string, params []boundParam, procNum int32, dbName string, connTx *connTxState) (*extendedQueryResult, *extendedQueryError) {
	// DROP DATABASE has real parser grammar (a generic no-op DDL absorption,
	// DropCompatStmt) that would otherwise shadow tryHandleDatabaseDDL's real
	// catalog-backed DROP entirely — see dispatchSimpleQueryViaExecutor's
	// identical pre-parse check (dispatch.go) for the full explanation.
	if kind, _ := classifyDatabaseDDL(query); kind == databaseDDLDrop {
		if res, qerr, handled := s.tryHandleDatabaseOrRoleDDLExtended(query, dbName, connTx.NonSuperuserRole, sess); handled {
			return res, qerr
		}
	}
	stmts, err := parser.Parse(query)
	if err != nil {
		// CREATE/DROP/ALTER DATABASE and CREATE/DROP/ALTER ROLE are not
		// parser grammar (same string-prefix wire-dispatch bypass the
		// simple-query path uses in dispatchSimpleQueryViaExecutor) — try
		// that bypass here too before surfacing a syntax error. M0119-0004.
		if res, qerr, handled := s.tryHandleDatabaseOrRoleDDLExtended(query, dbName, connTx.NonSuperuserRole, sess); handled {
			return res, qerr
		}
		if res, qerr, handled := s.tryCompatNoopExtended(query); handled {
			return res, qerr
		}
		msg, extra := syntaxErrorMsg(err)
		qerr := &extendedQueryError{Code: syntaxErrorCode(err), Message: msg}
		for _, f := range extra {
			if f.Code == protocol.FieldPosition {
				if p, _ := strconv.Atoi(f.Value); p > 0 {
					qerr.Position = p
				}
			}
		}
		return nil, qerr
	}
	// Per-statement query logging (GOOPG_LOG_STATEMENT), extended protocol.
	// Logged at Execute (the portal's source query) rather than Bind, so a
	// reused portal is not logged per-batch — mirroring PostgreSQL's
	// log_statement on the extended path. No-op when disabled. root-0023.
	stmtStart := time.Now()
	wasLogged := s.logStatement("extended", query, sess, connTx)
	// `log_min_duration_statement` (check_log_duration, postgres.c): timed
	// across every return path below via defer. root-0023 follow-up.
	defer s.logDuration(stmtStart, wasLogged, "extended", query, sess, connTx)
	if len(stmts) == 0 {
		return &extendedQueryResult{Empty: true}, nil
	}
	if len(stmts) > 1 {
		return nil, &extendedQueryError{Code: sqlstate.SyntaxError, Message: "extended query may contain only one statement"}
	}
	stmt := stmts[0]
	// M0098-0005: cross-session plan cache for extended protocol.
	// The same parameterized query is shared across all 100 pgbench
	// connections — one planning call serves them all.
	var node planner.Node
	// Resolved directly from dbName (not an executor.Context field) since
	// ectx doesn't exist yet at this point in the request. M0122-0007 slice 4c.
	connDBOid := resolveConnDBOid(s.cfg.Catalog, dbName)
	if s.pc != nil && !sessionTempInheritanceActive(s.cfg.Catalog) && !partitionDetachPending(s.cfg.Catalog) && !inheritanceChangePending(s.cfg.Catalog) {
		key := planCacheKey(query, connDBOid)
		if cached, ok := s.pc.Get(key); ok {
			node = cached
		} else {
			var perr error
			node, perr = planner.Plan(stmt, sessionPlanCatalog(sess, s.cfg.Catalog, connDBOid))
			if perr != nil {
				code, msg := planErrorFields(perr)
				return nil, &extendedQueryError{Code: code, Message: msg}
			}
			if planCacheIsCacheable(node) {
				s.pc.Put(key, node)
			}
		}
	} else {
		var perr error
		node, perr = planner.Plan(stmt, sessionPlanCatalog(sess, s.cfg.Catalog, connDBOid))
		if perr != nil {
			code, msg := planErrorFields(perr)
			return nil, &extendedQueryError{Code: code, Message: msg}
		}
	}

	// M0132-S2: BEGIN/COMMIT/ROLLBACK are no longer answered with a bare
	// command tag. They drive the SAME state machine the simple path drives
	// (applyTransactionVerb, txn_verb.go) — but only once the executor context
	// below exists, because the machine needs ctx.Tx, ctx.Session, the
	// Begin/EndLocalTransaction hooks and the pending type-DDL queues.
	txNode, isTxVerb := node.(*planner.Transaction)

	if !isTxVerb {
		// P6: wrap AFTER the plan-cache read/write above, so the cache holds the
		// SERIAL plan and the Gather is chosen per statement from this session's
		// GUCs. See applyParallelPostPass in dispatch.go for why the placement
		// matters.
		//
		// Note this path reads the GUCs from `sess` directly rather than from an
		// executor context: on the extended protocol, planning happens ~40 lines
		// before ectx exists.
		node = planner.MaybeAddGather(node, planner.ParallelSettings{
			MaxWorkersPerGather: sessionMaxParallelWorkersPerGather(sess),
			MinTableScanBlocks:  sessionMinParallelTableScanSize(sess),
			LeaderParticipates:  sessionParallelLeaderParticipation(sess),
			DebugParallelQuery:  sessionDebugParallelQuery(sess),
			// The size lookup needs only the pool and catalog, both available on
			// the server here — unlike ectx, which does not exist yet on this path.
			BlocksForTable: parallelBlocksForTableFrom(s.cfg.Pool, s.cfg.Catalog),
		})
	}

	// M0132-S3: an Execute that arrives inside an explicit block joins the
	// block's transaction (connTx.Tx(), the accessor — it returns the session's
	// current transaction once an XID has been materialised, M0100-0002, so a
	// statement self-sees the block's earlier writes) instead of beginning and
	// committing one of its own. Mirrors dispatch.go's identical branch.
	inBlock := connTx != nil && connTx.InExplicit()
	var tx mvcc.Transaction
	if inBlock {
		tx = connTx.Tx()
	} else {
		// Out of block: this Execute owns its transaction, which lives on the
		// connection's OWN ProcArray slot — exactly as the simple path does
		// (dispatch.go). The connection holds that slot exclusively for its
		// lifetime (connHeld), and out of block nothing else uses it, so the
		// historical `(procNum + halfSize) % ConnSlotCount` offset was both
		// unnecessary and unsafe: it deterministically landed the transaction
		// on a DIFFERENT connection's slot, where `Begin` unconditionally
		// clobbers whatever is there, producing `mvcc: unknown transaction`
		// aborts (doc 09 §5 I3). M0132-S7.
		var err error
		tx, err = s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted, procNum)
		if err != nil {
			return nil, &extendedQueryError{Code: sqlstate.SystemError, Message: err.Error()}
		}
	}
	ownTx := !inBlock
	commit := false
	var advisoryReleaseTarget any
	defer func() {
		// Only a transaction this Execute began is finalised here; a block's
		// transaction outlives the Execute and is finalised by its COMMIT /
		// ROLLBACK (or by connection teardown).
		if ownTx && !commit {
			_ = s.cfg.TxnMgr.Rollback(tx)
			executor.ReleaseAdvisoryTransactionLocks(advisoryReleaseTarget)
		}
	}()
	snap, err := s.cfg.TxnMgr.SnapshotFor(tx)
	if err != nil {
		return nil, &extendedQueryError{Code: sqlstate.SystemError, Message: err.Error()}
	}

	datums, perr := paramsToDatums(params)
	if perr != nil {
		return nil, perr
	}

	ectx := executor.NewContext()
	// Stage 9 (D4.2): tear down SubPlan rescan handles with the
	// statement's Context (see dispatch.go for rationale).
	defer ectx.CloseSubPlans()
	// M0127-P3.3: the extended protocol's twin of the simple path's
	// per-statement spill-file release (dispatch.go). Extended Execute is
	// one statement, so the dispatch scope IS the statement scope here.
	defer ectx.ReleaseSpillFiles()
	ectx.Ctx = ctx
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	s.wireExtensionRows(ectx, dbName) // per-database pg_extension (M0110-0003 gap #7c)
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.MultiXact = s.cfg.MultiXact
	ectx.Tx = tx
	// The connection's ProcArray slot. The simple path sets this from
	// connTx.ProcNum (dispatch.go); the extended path must too, or
	// applyTransactionVerb's `BEGIN ISOLATION LEVEL <level>` re-begin
	// (txn_verb.go) lands the correctly-levelled transaction on slot 0 instead
	// of the connection's own slot — so two concurrent SERIALIZABLE blocks
	// opened over this protocol would collide there. M0132-S6.
	ectx.ProcNum = procNum
	ectx.Snap = snap
	ectx.Params = datums
	ectx.Checkpointer = s.cfg.Checkpointer
	// M0127-P3.3: the simple path has always set this (dispatch.go); the
	// extended path never did, so an extended-protocol query's spill files
	// resolved to os.TempDir() instead of <datadir>/base/pgsql_tmp — the
	// sibling-path divergence class again. DataDir is read-only state here.
	ectx.DataDir = s.cfg.DataDir
	ectx.StatsTarget = sessionStatsTarget(sess)
	ectx.WorkMem = sessionWorkMem(sess)
	ectx.MaxParallelWorkersPerGather = sessionMaxParallelWorkersPerGather(sess)
	ectx.MaxParallelWorkers = sessionMaxParallelWorkers(sess)
	ectx.MinParallelTableScanBlocks = sessionMinParallelTableScanSize(sess)
	ectx.ParallelLeaderParticipation = sessionParallelLeaderParticipation(sess)
	ectx.DebugParallelQuery = sessionDebugParallelQuery(sess)
	if sess != nil {
		ectx.AdvisorySessionIdentity = sess
		ectx.GetSetting = func(name string) (string, bool) {
			_, eff, ok := sess.Get(name)
			return eff, ok
		}
		ectx.GetSettingDisplay = func(name string) (string, bool) {
			_, eff, ok := sess.GetDisplay(name)
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
		ectx.AllSettingsDisplay = func() []executor.SettingValue {
			all := sess.AllDisplay()
			out := make([]executor.SettingValue, 0, len(all))
			for _, kv := range all {
				out = append(out, executor.SettingValue{Name: kv.Name, Value: kv.Value})
			}
			return out
		}
		ectx.ExplainSettings = func() []executor.SettingValue {
			all := sess.ExplainVariables()
			out := make([]executor.SettingValue, 0, len(all))
			for _, kv := range all {
				out = append(out, executor.SettingValue{Name: kv.Name, Value: kv.Value})
			}
			return out
		}
		ectx.ResetSetting = sess.Reset
		ectx.ResetAllSettings = sess.ResetAll
		ectx.BeginLocalTransaction = sess.BeginTransaction
		ectx.EndLocalTransaction = func() {
			sess.EndTransaction()
			// Re-sync is_superuser / the executor-context mirror after
			// connTx.End() (called by the caller just before this) restores
			// NonSuperuserRole from a pending SET LOCAL ROLE / SESSION
			// AUTHORIZATION snapshot (SnapshotLocalRoleIfNeeded). M0119-0004.
			if connTx != nil {
				ectx.NonSuperuserRole = connTx.NonSuperuserRole
				setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
			}
		}
	}
	// M0132-S2/S3: inside an explicit block the statement runs against the
	// block's session, not against a fresh throwaway one — the same wiring
	// dispatch.go performs. Without it an in-block Execute would neither see
	// the block's savepoint/DDL-undo state nor advance the block's command
	// counter, so a second Execute could not see the first one's writes.
	if inBlock {
		if bs := connTx.Session(); bs != nil {
			ectx.Session = bs
			ectx.SetCmdCounter(bs.CmdCounter())
			ectx.CommandCounterIncrement()
			ectx.CmdID = ectx.GetCurrentCommandId(true)
		}
		ectx.TempTableShadows = connTx.TempTableShadows
		ectx.PendingEnumValues = connTx.PendingEnumValues
		ectx.PendingEnumRenames = connTx.PendingEnumRenames
		ectx.PendingCreatedEnums = connTx.PendingCreatedEnums
		ectx.PendingCreatedComposites = connTx.PendingCreatedComposites
		ectx.PendingCreatedRangeTypes = connTx.PendingCreatedRangeTypes
		// Transaction-scoped lock identity: a LOCK TABLE (or a maintenance
		// lock) taken by an in-block statement must be held for the block,
		// not for the statement. dispatch.go keeps the same invariant.
		ectx.TxnLockBackendID = connTx.LockBackendID
	} else {
		// Out of block: wire a throwaway autocommit session exactly as the
		// simple path does (dispatch.go). Without it transactionOp.Open fails
		// on `ctx.Session == nil`, so a SAVEPOINT / RELEASE / ROLLBACK TO
		// issued with no explicit block reports XX000 "transaction statements
		// require Session in Context" instead of PostgreSQL's 25P01 "SAVEPOINT
		// can only be used in transaction blocks". InExplicitTransaction()
		// stays false on this throwaway, so the executor's own 25P01 guard is
		// what fires. M0132-S10.
		ectx.Session = executor.NewAutocommitUndoSession()
	}
	// Wire session-authorization/role tracking so a SET SESSION AUTHORIZATION
	// or SET ROLE that reaches the executor (rather than the fast-path
	// switch in executeExtendedQuery) still updates connTx.NonSuperuserRole
	// and the reportable is_superuser GUC — same wiring as the simple-query
	// executor path (dispatch.go). M0119-0004: previously unwired entirely,
	// so any such statement here was silently dropped.
	if connTx != nil {
		ectx.NonSuperuserRole = connTx.NonSuperuserRole
		ectx.SetSessionAuthorization = func(role string, local bool) {
			connTx.SnapshotLocalRoleIfNeeded(local)
			connTx.NonSuperuserRole = role
			ectx.NonSuperuserRole = role
			setIsSuperuserGUC(sess, role == "")
		}
		ectx.SetRole = ectx.SetSessionAuthorization
	}
	// Match advisorySessionIDFromContext's preference: the per-connection
	// AdvisorySessionIdentity (SessionRegistry) is the stable advisory owner, so
	// xact-scoped advisory locks release under it at txn end (not the
	// BasicSession, which is nil before the first BEGIN). M0118-0003.
	if ectx.AdvisorySessionIdentity != nil {
		advisoryReleaseTarget = ectx.AdvisorySessionIdentity
	} else if ectx.Session != nil {
		advisoryReleaseTarget = ectx.Session
	}
	ectx.PubSub = s.cfg.PubSub
	ectx.WAL = s.cfg.WAL
	ectx.SyncRep = s.cfg.SyncRep
	ectx.SyncCommitMode = sessionSyncCommitMode(sess)
	ectx.AsyncCommit = sessionAsyncCommit(sess)
	if s.applyLauncher != nil {
		ectx.OnSubscriptionChange = s.applyLauncher.Wake
	}
	// Wire pg_cancel_backend(pid) here too (sibling of the simple-query path) so
	// the SQL function works regardless of protocol. Depends only on the
	// process-wide cancel registry, not on per-session Activity. M0118-0008.
	ectx.CancelBackend = func(pid int32) bool {
		if pid <= 0 {
			return false
		}
		return s.cancelReg.cancelByPID(uint32(pid))
	}
	// pg_terminate_backend(pid) sibling of the simple-query path. M0118-0009.
	ectx.TerminateBackend = func(pid int32) bool {
		if pid <= 0 {
			return false
		}
		return s.cancelReg.terminateByPID(uint32(pid))
	}

	// M0132-S2/S4: BEGIN/COMMIT/ROLLBACK run the shared state machine here,
	// with the executor context fully wired. Because it is the SAME function
	// the simple path calls, the extended COMMIT inherits the whole
	// finalisation sequence (deferred FK/UNIQUE/EXCLUDE checks, the SSI
	// pre-commit check, the deferred DROP INDEX / ATTACH PARTITION / INHERIT /
	// DROP TABLE / DROP FUNCTION appliers, enum-DDL undo, the NOTIFY publish)
	// rather than a hand-written re-derivation of it.
	if isTxVerb {
		// autoCommit means the same thing it does on the simple path: "this
		// dispatch still owns the transaction". applyTransactionVerb clears it
		// when BEGIN promotes the transaction into the block, which is exactly
		// the signal that the deferred rollback above must not fire.
		autoCommit := ownTx
		if out := s.applyTransactionVerb(ectx, connTx, txNode, &autoCommit); out.Handled {
			if !autoCommit {
				commit = true
			}
			s.writeBackConnTxState(ectx, connTx)
			if out.Err != nil {
				return nil, extendedQueryErrorFromVerb(out.Err)
			}
			return &extendedQueryResult{CommandTag: out.Tag, WarnFields: verbWarnFields(out)}, nil
		}
		// Handled == false: SAVEPOINT / RELEASE / ROLLBACK TO are not owned by
		// the state machine; they fall through to the executor exactly as they
		// do on the simple path (M0097-0023). M0132-S10 ruled this correct:
		// transactionOp's execSavepoint/execRelease/execRollbackTo drive the
		// sub-transaction stack off the block's session (wired above), and the
		// out-of-block case reaches their 25P01 guard via the autocommit
		// throwaway session rather than XX000 "requires Session in Context".
	}

	op, err := executor.Build(node)
	if err != nil {
		return nil, newExtendedQueryError(err)
	}
	if err := op.Open(ectx); err != nil {
		_ = op.Close()
		return nil, newExtendedQueryError(err)
	}

	schema := node.Output()
	res := &extendedQueryResult{}
	// A read-shaped plan reports a non-nil schema; writers (Insert/Update/
	// Delete/DDL/Transaction) report nil. A zero-column read (e.g.
	// `SELECT FROM t`, `SELECT;`) reports a non-nil zero-length schema and
	// MUST still emit one DataRow per source row per PostgreSQL — so gate on
	// `schema != nil`, NOT `len(schema) > 0` (which silently dropped every
	// zero-column row here while the simple-query path emitted them). Mirrors
	// dispatch.go's `if schema != nil` guard.
	if schema != nil {
		res.Fields = make([]protocol.FieldDescription, len(schema))
		for i, sc := range schema {
			res.Fields[i] = protocol.FieldDescription{
				Name:         sc.Name,
				TypeOID:      typeOIDFor(sc.Type),
				TypeSize:     -1,
				TypeModifier: -1,
				Format:       0,
			}
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
			return nil, newExtendedQueryError(err)
		}
		if schema != nil {
			row := slot.Row()
			cells := make([][]byte, len(row))
			for i, d := range row {
				if d.IsNull() {
					cells[i] = nil
					continue
				}
				// Render through the same per-type formatter the
				// simple-query path uses (appendTypedCellText) so both
				// wire protocols agree on float/date/time/timetz/bytea/
				// regclass output, not just AppendValueText's generic
				// fallback. M0119-0004 (dispatch_extended vs dispatch
				// type-switch divergence).
				if i < len(schema) {
					cells[i] = s.appendTypedCellText(nil, d, schema[i].Type, ectx.GetSetting)
					continue
				}
				cells[i] = d.AppendValueText(nil)
			}
			res.Rows = append(res.Rows, cells)
			rowCount++
		}
	}
	if err := op.Close(); err != nil {
		return nil, newExtendedQueryError(err)
	}
	// M0132-S3: only an Execute that began its own transaction commits one.
	// In a block the work stays uncommitted until the client's COMMIT, and the
	// transaction-scoped advisory locks stay held for the block.
	if ownTx {
		if err := ectx.CommitTransaction(tx); err != nil {
			return nil, &extendedQueryError{Code: sqlstate.SystemError, Message: err.Error()}
		}
		executor.ReleaseAdvisoryTransactionLocks(advisoryReleaseTarget)
	}
	commit = true
	s.writeBackConnTxState(ectx, connTx)

	res.CommandTag = commandTagFor(node, op, rowCount)
	if res.CommandTag == "" {
		res.CommandTag = "OK"
	}
	// DDL invalidates the cross-session plan cache. M0098-0005.
	if _, isDDL := node.(*planner.DDL); isDDL && s.pc != nil {
		s.pc.Invalidate()
	}
	return res, nil
}

// paramsToDatums maps bound text-format parameters into
// executor.Datum values. v0 doesn't carry per-parameter type
// information through Bind, so every value lands as a string Datum
// and the planner/expression evaluator coerces on read (e.g. via
// `$1::int4` casts). NULL parameters become NullDatum.
// tryHandleDatabaseOrRoleDDLExtended is the extended-query-protocol
// counterpart of dispatchSimpleQueryViaExecutor's CREATE/DROP/ALTER
// DATABASE and CREATE/DROP/ALTER ROLE wire-dispatch bypass (dispatch.go
// ~line 122-183): goopg's parser has no grammar for these statements at
// all, so both protocols intercept them by string-prefix matching before
// falling through to a syntax error. Until this fix the extended path had
// no such hook, so a client that sends these DDL statements through a
// prepared statement (JDBC, npgsql, psycopg2's default protocol, etc.
// rather than psql's simple-query default) got a silent 42601 syntax
// error instead of the DDL being applied — a real correctness bug, not
// just missing test coverage. M0119-0004-ACLHEAP.
//
// Unlike the simple-query path, a single Parse message may only carry one
// SQL command (wire protocol spec), so the splitLeadingRoleDDL
// multi-statement recursion dispatch.go needs has no counterpart here.
//
// Returns handled=false when query is not a DDL form this bypass
// recognises, so the caller falls through to its normal syntax-error path.
func (s *Server) tryHandleDatabaseOrRoleDDLExtended(query, dbName, actingRole string, sess *config.SessionRegistry) (*extendedQueryResult, *extendedQueryError, bool) {
	resolveCurrentGUC := currentGUCResolver(func(name string) (string, bool) {
		if sess == nil {
			return "", false
		}
		_, eff, ok := sess.GetDisplay(name)
		return eff, ok
	})
	if handled, notice, herr := s.tryHandleDatabaseDDL(query, dbName, actingRole, resolveCurrentGUC); handled {
		if herr != nil {
			return nil, &extendedQueryError{Code: databaseDDLErrorSQLState(herr), Message: herr.Error()}, true
		}
		return &extendedQueryResult{CommandTag: databaseDDLCommandTag(query), Notice: notice}, nil, true
	}
	if handled, herr := s.tryHandleRoleDDL(query, dbName, resolveCurrentGUC); handled {
		if herr != nil {
			return nil, &extendedQueryError{Code: roleErrorSQLState(herr), Message: herr.Error(), Detail: roleErrorDetail(herr)}, true
		}
		norm := normalizeCompatSQL(query)
		var tag string
		switch {
		case strings.HasPrefix(norm, "create "):
			tag = "CREATE ROLE"
		case strings.HasPrefix(norm, "alter "):
			tag = "ALTER ROLE"
		default:
			tag = "DROP ROLE"
		}
		return &extendedQueryResult{CommandTag: tag}, nil, true
	}
	return nil, nil, false
}

// tryCompatNoopExtended is the extended-query-protocol counterpart of
// dispatchSimpleQueryViaExecutor's compatNoopCommandTag absorption
// (dispatch.go ~line 180): GRANT/REVOKE/CREATE SCHEMA/COMMENT ON/SECURITY
// LABEL forms (and a few CREATE/ALTER/DROP ROLE/DATABASE spellings already
// covered by tryHandleDatabaseOrRoleDDLExtended above) that the parser
// doesn't recognise at all. Until this fix a client using Parse/Bind/
// Execute (JDBC/npgsql/psycopg2's default) for one of these forms got a
// hard 42601 syntax error instead of the same no-op absorption psql's
// simple-query default receives — a real correctness gap, not just missing
// coverage. M0119-0004-ACLHEAP follow-up (loop #86, item (3) of the loop
// #84 row, `0119-0004cv`/`0119-0004cw`).
//
// Returns handled=false when query does not match any compatNoopCommandTag
// prefix, so the caller falls through to its normal syntax-error path.
func (s *Server) tryCompatNoopExtended(query string) (*extendedQueryResult, *extendedQueryError, bool) {
	tag, ok := compatNoopCommandTag(query)
	if !ok {
		return nil, nil, false
	}
	if tag == "CREATE SCHEMA" {
		if werr := s.registerCompatNoopSchema(query); werr != nil {
			return nil, &extendedQueryError{Code: sqlstate.SystemError, Message: werr.Error()}, true
		}
	}
	return &extendedQueryResult{CommandTag: tag}, nil, true
}

func paramsToDatums(params []boundParam) ([]executor.Datum, *extendedQueryError) {
	out := make([]executor.Datum, len(params))
	for i, p := range params {
		if p.IsNull {
			out[i] = executor.NullDatum
			continue
		}
		// Try to interpret obviously-integer strings as int Datums
		// first — most pgbench bind targets are int4 (`oid =
		// $1::regclass`, `aid = $1`). The parser/planner will accept
		// ints transparently in arithmetic and equality contexts.
		// Anything else stays as a string and the executor casts as
		// needed.
		if v, err := strconv.ParseInt(p.Text, 10, 64); err == nil {
			out[i] = executor.Datum{Kind: executor.KindInt, Int: v}
			continue
		}
		out[i] = executor.NewStringDatum(p.Text)
	}
	if false {
		// Reserved for future per-type coercion; references kept to
		// keep the package import set stable when adding type
		// inference later.
		_ = fmt.Sprintf
	}
	return out, nil
}
