package postmaster

import (
	"context"
	"errors"
	"os"
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/utils/adt/array"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage/lmgr"
	"github.com/goopg/goopg/internal/storage/lmgr/lockwait"
	"github.com/goopg/goopg/internal/utils/mb"
	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/access/transam/xlog"
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

// ——— Forced-GC feature flag (user request: make the explicit GC triggers
// switchable and ship them DISABLED) ———
//
// forcedGCEnabledFlag gates the ENTIRE maybeForceGCAfterCommit body,
// including its condition checks: when cleared, neither the per-query
// counter nor ReadMemStats/HeapInuse sampling ever runs, and no
// runtime.GC()/debug.FreeOSMemory is issued from this path. Default is
// OFF; operators re-enable with GOOPG_FORCED_GC=on|true|1 (env read once
// at init) or programmatically via SetForcedGCEnabled.
var forcedGCEnabledFlag atomic.Bool

func init() {
	forcedGCEnabledFlag.Store(parseForcedGCEnv(os.Getenv("GOOPG_FORCED_GC")))
}

// parseForcedGCEnv interprets the GOOPG_FORCED_GC environment value.
// Anything other than an affirmative token leaves the trigger disabled.
func parseForcedGCEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

// SetForcedGCEnabled flips the forced-GC trigger at runtime (tests, future
// control-plane wiring).
func SetForcedGCEnabled(v bool) { forcedGCEnabledFlag.Store(v) }

// forcedGCEnabled reports whether explicit commit-path GC triggers may run.
func forcedGCEnabled() bool { return forcedGCEnabledFlag.Load() }

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
	if !forcedGCEnabled() {
		// Flag-disabled: skip BOTH the condition bookkeeping (counter bump)
		// and any GC/FreeOSMemory work — the call becomes a single atomic
		// load on the hot path.
		return
	}
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
func (s *Server) dispatchSimpleQueryViaExecutor(ctx context.Context, r *libpq.FrameReader, w *libpq.FrameWriter, sess *misc.SessionRegistry, sql string, connTx *connTxState, prepStmts *preparedStatements) error {
	resolveCurrentGUC := currentGUCResolver(func(name string) (string, bool) {
		if sess == nil {
			return "", false
		}
		_, eff, ok := sess.GetDisplay(name)
		return eff, ok
	})
	// DROP DATABASE has real parser grammar (DropCompatStmt, a generic no-op
	// DDL absorption added after M0054-0001's bypass below) that would
	// otherwise shadow tryHandleDatabaseDDL's real catalog-backed DROP
	// entirely: DropCompatStmt's "database" arm (internal/executor/
	// operators_ddl.go execDropCompat) is a hardcoded pre-M0054-0001 stub
	// that always reports "does not exist" regardless of catalog state, so
	// parser.Parse succeeding for this statement must not be allowed to win.
	// Checked here, before Parse, so it takes precedence; CREATE/ALTER
	// DATABASE need no such pre-check because the parser has no grammar for
	// them at all (parser.Parse always fails, hitting the bypass below).
	if kind, _ := classifyDatabaseDDL(sql); kind == databaseDDLDrop {
		if handled, err := s.handleDatabaseDDLBypass(sql, connTx.DBName, connTx.NonSuperuserRole, resolveCurrentGUC, w); handled {
			return err
		}
	}
	// Parse uses the heap-backed tokenSlicePool (allocation-free in steady
	// state). The M0107-0003 Phase C.3 mctx token-arena fast path was retired
	// as fundamentally GC-unsafe — see docs/design/0107-0003d-token-pool-gc-safety.md
	// — so we no longer acquire a throwaway KindExpr child just to pass it in.
	stmts, err := parser.Parse(sql)
	if err != nil {
		// M0054-0001: CREATE DATABASE / DROP DATABASE are intercepted
		// here (the parser doesn't recognise CREATE DATABASE / ALTER
		// DATABASE) so we can (a) update the catalog so subsequent
		// connections see the database in pg_database / can connect to
		// it, and (b) emit a WAL record so the registration survives a
		// crash. Other commands fall through to the wire-protocol no-op
		// tag handler.
		if handled, err := s.handleDatabaseDDLBypass(sql, connTx.DBName, connTx.NonSuperuserRole, resolveCurrentGUC, w); handled {
			return err
		}
		// A multi-statement batch whose FIRST statement is CREATE/DROP ROLE
		// (which the parser does not recognise, so the whole batch lands here)
		// must not be swallowed wholesale by the single-statement role-DDL
		// intercept below — that would drop the trailing statements (e.g. the
		// CREATE TABLE in a "CREATE ROLE x; CREATE TABLE y" isolation setup).
		// Peel the leading role statement off, handle it, then recurse on the
		// remainder so every statement runs. M0118-0008.
		if first, rest, ok := splitLeadingRoleDDL(sql); ok {
			if handled, herr := s.tryHandleRoleDDL(first, connTx.DBName, resolveCurrentGUC, connTx.NonSuperuserRole); handled {
				if herr != nil {
					return s.writeQueryError(w, roleErrorSQLState(herr), herr.Error(), roleErrorDetailFields(herr)...)
				}
				normFirst := normalizeCompatSQL(first)
				tag := "CREATE ROLE"
				if !strings.HasPrefix(normFirst, "create ") {
					tag = "DROP ROLE"
				}
				if err := w.WriteCommandComplete(tag); err != nil {
					return err
				}
				return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, rest, connTx, prepStmts)
			}
		}
		// Role DDL (CREATE/DROP ROLE/USER) is not yet in the parser but needs
		// actual role tracking so DROP ROLE fails on nonexistent roles.
		if handled, herr := s.tryHandleRoleDDL(sql, connTx.DBName, resolveCurrentGUC, connTx.NonSuperuserRole); handled {
			if herr != nil {
				return s.writeQueryError(w, roleErrorSQLState(herr), herr.Error(), roleErrorDetailFields(herr)...)
			}
			norm := normalizeCompatSQL(sql)
			var tag string
			switch {
			case strings.HasPrefix(norm, "create "):
				tag = "CREATE ROLE"
			case strings.HasPrefix(norm, "alter "):
				tag = "ALTER ROLE"
			default:
				tag = "DROP ROLE"
			}
			if err := w.WriteCommandComplete(tag); err != nil {
				return err
			}
			return w.ReadyForQuery()
		}
		if first, rest, tag, ok := splitLeadingCompatNoopDDL(sql); ok {
			if tag == "CREATE SCHEMA" {
				if werr := s.registerCompatNoopSchema(first, connTx.NonSuperuserRole, connTx.SessionUser); werr != nil {
					return s.writeQueryError(w, compatNoopSchemaErrorCode(werr), werr.Error())
				}
			}
			if err := w.WriteCommandComplete(tag); err != nil {
				return err
			}
			return s.dispatchSimpleQueryViaExecutor(ctx, r, w, sess, rest, connTx, prepStmts)
		}
		// A multi-statement batch that isn't the parser-gap workaround case
		// above must not have compatNoopCommandTag matched against its raw,
		// unsplit text below — that would absorb the WHOLE batch (including
		// a later, genuinely invalid statement) as a bare success instead of
		// reporting the real syntax error, silently executing nothing.
		// M0119-0004-ACLHEAP loop #87 follow-up.
		if !isMultiStatementSQL(sql) {
			if tag, ok := compatNoopCommandTag(sql); ok {
				if tag == "CREATE SCHEMA" {
					if werr := s.registerCompatNoopSchema(sql, connTx.NonSuperuserRole, connTx.SessionUser); werr != nil {
						return s.writeQueryError(w, compatNoopSchemaErrorCode(werr), werr.Error())
					}
				}
				if err := w.WriteCommandComplete(tag); err != nil {
					return err
				}
				return w.ReadyForQuery()
			}
		}
		// M0134-0155: a PARSE-time error inside an explicit transaction
		// aborts the block too — PostgreSQL rejects any subsequent
		// statement with 25P02 until ROLLBACK (postgres.c's error handler
		// aborts on ANY statement error, not just executor ones).
		// Mirrors the M0132-S5 plan-error gate below; before this,
		// `BEGIN; <syntax error>; SELECT 1` left the block live.
		if connTx != nil && connTx.InExplicit() {
			connTx.Fail()
			connTx.ReleasePinnedSnapshotOnFail(s.cfg.TxnMgr)
		}
		msg, extra := syntaxErrorMsg(err)
		return s.writeQueryError(w, syntaxErrorCode(err), msg, extra...)
	}
	if len(stmts) == 0 {
		if err := w.WriteEmptyQueryResponse(); err != nil {
			return err
		}
		return w.ReadyForQuery()
	}
	// Session-level explicit transaction support (M0096-0005):
	// When the client has issued BEGIN, reuse the open TxnMgr transaction
	// rather than starting a fresh auto-commit one.  Each statement-level
	// dispatch that is NOT inside an explicit transaction still auto-commits.
	var tx transam.Transaction
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
		tx, err = s.cfg.TxnMgr.Begin(transam.IsolationReadCommitted, pn)
		if err != nil {
			return s.writeQueryError(w, errcodes.SystemError, err.Error())
		}
	}
	// Each Query message gets a fresh BackendID for the lock
	// manager; the youngest-backend victim policy from M0012-0002
	// relies on monotonic IDs.
	backendID := lmgr.BackendID(s.nextBackendID.Add(1))
	commit := false
	var advisoryReleaseTarget any
	// ectx is assigned once the executor.Context is built below; predeclared
	// here so the abort branch of this defer can reach it. A later statement's
	// error in this same multi-statement message must undo any earlier CREATE
	// TABLE/INDEX in the batch, not just the mvcc heap writes: RegisterTable
	// is a non-transactional in-memory catalog mutation, so without this the
	// table registration survives an aborted implicit batch while its
	// pg_class/pg_attribute heap rows (written under the rolled-back tx) do
	// not, permanently desyncing pg_dump-visible catalog state from the live
	// catalog (M0110-0001 DU-002 slice 444 deferral, 2026-07-04).
	var ectx *executor.Context
	defer func() {
		if autoCommit && !commit {
			if ectx != nil {
				if bs, ok := ectx.Session.(*executor.BasicSession); ok {
					executor.ProcessRollbackUndos(ectx, bs)
				}
				// Undo any CREATE TYPE .../ALTER TYPE ... enum/composite DDL this
				// batch's throwaway session tracked (TracksDDLUndo(), above) — the
				// same bug class ProcessRollbackUndos fixes for CREATE TABLE/INDEX.
				// root-0024 residual, M0110-0001.
				executor.UndoEnumDDLOnAbort(ectx)
			}
			_ = s.cfg.TxnMgr.Rollback(tx)
			executor.ReleaseAdvisoryTransactionLocks(advisoryReleaseTarget)
		}
		// Always drop locks at txn end so a leftover holder
		// can't outlive the connection. ReleaseAll is a no-op
		// when LockMgr is nil.
		if s.cfg.LockMgr != nil {
			s.cfg.LockMgr.ReleaseAll(backendID)
		}
		// Drop any tuple-level (row) locks this statement took on the
		// always-on tableLockMgr under the per-statement backend id. In the
		// production server (LockMgr==nil) FOR UPDATE / FOR SHARE route their
		// tuple locks here so concurrent waiters on the same row acquire in
		// FIFO arrival order; releasing at statement end hands the "wait until
		// the holder's txn ends" duty back to the persisted xmax (design
		// 0021-0012). Disjoint from LOCK TABLE, which holds under the
		// transaction-scoped TxnLockBackendID.
		executor.ReleaseTupleLocks(backendID)
	}()
	snap, err := s.cfg.TxnMgr.SnapshotFor(tx)
	if err != nil {
		return s.writeQueryError(w, errcodes.SystemError, err.Error())
	}
	// M0107-0001: per-statement mctx. Parent is the session mctx
	// threaded from serveConn via connTx.SessCtx (nil for tests
	// that don't wire a full server).
	var sessCtxForStmt *mmgr.Context
	if connTx != nil {
		sessCtxForStmt = connTx.SessCtx
	}
	stmtCtx := mmgr.Acquire(sessCtxForStmt, mmgr.KindStmt)
	defer stmtCtx.Release()

	ectx = executor.NewContext()
	// Stage 9 (D4.2): SubPlan handles keep sublink operator trees open
	// across outer rows for rescan; tear them down when the dispatch
	// (and with it this statement batch's Context) ends. Lock-safe:
	// Operator.Close never releases heavyweight locks.
	defer ectx.CloseSubPlans()
	ectx.Mctx = stmtCtx
	ectx.Ctx = ctx
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	// PlanCatalog will be set to a search-path-aware wrapper after sess is wired.
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.MultiXact = s.cfg.MultiXact
	ectx.Tx = tx
	// Wire the per-connection session into the executor so advisory locks
	// and other session-scoped state are properly tracked.
	if connTx != nil {
		if sess := connTx.Session(); sess != nil {
			ectx.Session = sess
			// M0129-S8.3: seed the fresh context's command counter from
			// the session so it persists across simple-query messages
			// within the same explicit transaction. Without this, every
			// message starts at curcid=0 and self-inserted tuples are
			// hidden from the next message's DML scans (cmin >= curcid).
			ectx.SetCmdCounter(sess.CmdCounter())
		} else {
			// Autocommit implicit batch (no explicit BEGIN): still give
			// execCreateTable/execCreateIndex a *BasicSession to record
			// against (RecordDDLCreate asserts on ctx.Session, unconditional
			// on explicit-transaction status) so the abort defer above can
			// undo a half-applied CREATE via ProcessRollbackUndos when a
			// LATER statement in this same message fails. Message-scoped
			// only — never shared with connTx, so it carries nothing across
			// Query messages. InExplicitTransaction() stays false on it, so
			// every other Session-gated code path (TRUNCATE/DROP-in-savepoint
			// snapshotting, deferred FK/UNIQUE/EXCLUDE check timing) is
			// unaffected — but TracksDDLUndo() reports true so CREATE TYPE
			// .../ALTER TYPE ... enum/composite tracking also covers this
			// batch (root-0024 residual, M0110-0001; see the write-back guard
			// below for why this must never reach connTx).
			ectx.Session = executor.NewAutocommitUndoSession()
		}
		// Seed the statement session's ON COMMIT {DELETE ROWS|DROP}
		// registrations from the connection's persisted list (written back at
		// each statement end, writeBackConnTxState + below), so a registration
		// made by an autocommit CREATE TEMP TABLE in an earlier message
		// reaches the explicit COMMIT of a later one — PG keeps on_commits in
		// backend-local CacheMemoryContext (tablecmds.c register_on_commit_
		// action), session-scoped across transactions. M0134-0072.
		if bs, ok := ectx.Session.(*executor.BasicSession); ok {
			bs.SetOnCommitActions(connTx.OnCommitActions)
		}
		// Share the per-connection TEMP TABLE shadow map so it persists
		// across statements in the same connection. M0097-0003.
		ectx.TempTableShadows = connTx.TempTableShadows
		ectx.PendingEnumValues = connTx.PendingEnumValues
		ectx.PendingEnumRenames = connTx.PendingEnumRenames
		ectx.PendingCreatedEnums = connTx.PendingCreatedEnums
		ectx.PendingCreatedComposites = connTx.PendingCreatedComposites
		ectx.PendingCreatedRangeTypes = connTx.PendingCreatedRangeTypes
		// Wire session-authorization role tracking so LEAKPROOF privilege checks
		// work after SET SESSION AUTHORIZATION regress_unpriv_user, and so
		// session_user()/current_user() (M0134-0009) see the live identity.
		ectx.NonSuperuserRole = connTx.NonSuperuserRole
		ectx.SetRoleIsActive = connTx.SetRoleIsActive
		ectx.SessionUser = connTx.SessionUser
		// SET SESSION AUTHORIZATION <r> sets the session user AND clears any
		// active SET ROLE (PG parity: guc.c:4092-4127 — SET session_authorization
		// forcibly performs "SET ROLE NONE"/"RESET role" with the same
		// context/source, per the SQL spec; NOT miscinit.c
		// SetSessionAuthorization, which is documented as deliberately
		// commutative with SetCurrentRoleId and does NOT itself clear
		// SetRoleIsActive). role=="" means DEFAULT/RESET: restore the
		// connect-time login user. M0134-0009.
		ectx.SetSessionAuthorization = func(role string, local bool) {
			connTx.SnapshotLocalRoleIfNeeded(local)
			if role == "" {
				role = connTx.LoginUser
			}
			connTx.SessionUser = role
			connTx.SetRoleIsActive = false
			if strings.EqualFold(role, "postgres") {
				connTx.NonSuperuserRole = ""
			} else {
				connTx.NonSuperuserRole = role
			}
			ectx.SessionUser = connTx.SessionUser
			ectx.SetRoleIsActive = connTx.SetRoleIsActive
			ectx.NonSuperuserRole = connTx.NonSuperuserRole
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
		}
		// SET ROLE <r> sets only the effective role; session_user is
		// untouched (miscinit.c GetSessionUserId is unaffected by SET ROLE).
		// role=="" means NONE/DEFAULT/RESET: clear the active role.
		// M0134-0009 (split from SetSessionAuthorization, was previously the
		// same closure — M0119-0004).
		ectx.SetRole = func(role string, local bool) {
			connTx.SnapshotLocalRoleIfNeeded(local)
			switch {
			case role == "":
				// A bare RESET ROLE with no SET ROLE active must be a no-op
				// (PG parity, miscinit.c SetRoleIsActive) — it must not wipe
				// out a SET SESSION AUTHORIZATION role override.
				if connTx.SetRoleIsActive {
					connTx.NonSuperuserRole = ""
				}
				connTx.SetRoleIsActive = false
			case strings.EqualFold(role, "postgres"):
				// SET ROLE postgres: explicit target, not a NONE/DEFAULT
				// synonym (round-2 review R7). NonSuperuserRole stays "" —
				// postgres is the bootstrap superuser, so the
				// NonSuperuserRole=="" privilege-check convention must
				// still see "superuser" — but SetRoleIsActive=true records
				// that a role IS active so EffectiveUserName reports
				// "postgres" (see its invariant comment) instead of
				// falling back to SessionUser.
				connTx.NonSuperuserRole = ""
				connTx.SetRoleIsActive = true
			default:
				connTx.NonSuperuserRole = role
				connTx.SetRoleIsActive = true
			}
			ectx.NonSuperuserRole = connTx.NonSuperuserRole
			ectx.SetRoleIsActive = connTx.SetRoleIsActive
			setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
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
		ectx.EndLocalTransaction = func(committed bool) {
			sess.EndTransaction(committed)
			// Re-sync is_superuser / the executor-context mirror after
			// connTx.End() (called by the caller just before this) restores
			// NonSuperuserRole/SessionUser/SetRoleIsActive from a pending
			// SET LOCAL ROLE / SESSION AUTHORIZATION snapshot
			// (SnapshotLocalRoleIfNeeded). M0119-0004, M0134-0009 round 2 (R1).
			if connTx != nil {
				ectx.NonSuperuserRole = connTx.NonSuperuserRole
				ectx.SessionUser = connTx.SessionUser
				ectx.SetRoleIsActive = connTx.SetRoleIsActive
				setIsSuperuserGUC(sess, connTx.NonSuperuserRole == "")
			}
		}
		// Set PlanCatalog to a search-path-aware wrapper so DDL executor can
		// use it when calling planner.Plan for internal validation. M0097-0022.
		// Resolved directly from connTx.DBName (not ectx.CurrentDatabaseOid)
		// since wireExtensionRows hasn't stamped it yet at this point in the
		// request. M0122-0007 slice 4c.
		var connDBName string
		if connTx != nil {
			connDBName = connTx.DBName
		}
		ectx.PlanCatalog = sessionPlanCatalog(sess, s.cfg.Catalog, resolveConnDBOid(s.cfg.Catalog, connDBName))
	}
	// Match advisorySessionIDFromContext's preference order: the per-connection
	// AdvisorySessionIdentity (SessionRegistry) is the stable advisory owner, so
	// xact-scoped advisory locks must be released under THAT identity at txn end
	// — not the BasicSession, which is nil before the first BEGIN. M0118-0003.
	if ectx.AdvisorySessionIdentity != nil {
		advisoryReleaseTarget = ectx.AdvisorySessionIdentity
	} else if ectx.Session != nil {
		advisoryReleaseTarget = ectx.Session
	}
	ectx.EnableOpportunisticPrune = sessionOpportunisticPrune(sess)
	ectx.FSM = s.cfg.FSM
	ectx.VM = s.cfg.VM
	ectx.FreezeMinAge = sessionFreezeMinAge(sess)
	ectx.PubSub = s.cfg.PubSub
	ectx.LockMgr = s.cfg.LockMgr
	ectx.BackendID = backendID
	// Inside an explicit transaction block, expose the stable per-connection
	// lock-manager identity so LOCK TABLE can hold a transaction-scoped
	// heavyweight lock that survives this statement's ReleaseAll(backendID)
	// and is released only at COMMIT/ROLLBACK (connTxState.End). Zero outside
	// an explicit block keeps autocommit LOCK display-only. M0118-0003.
	if connTx != nil && connTx.InExplicit() {
		ectx.TxnLockBackendID = connTx.LockBackendID
	}
	ectx.Activity = s.cfg.Activity
	// Wire pg_cancel_backend(pid) to the process-wide cancel registry so a
	// backend can signal a peer's in-flight query (privileged, no secret key —
	// the caller is an authenticated session, not an off-wire CancelRequest).
	// M0118-0008 (detach-partition-concurrently-3/4 s1cancel).
	ectx.CancelBackend = func(pid int32) bool {
		if pid <= 0 {
			return false
		}
		return s.cancelReg.cancelByPID(uint32(pid))
	}
	// Wire pg_terminate_backend(pid) to the process-wide registry so a backend
	// can terminate a peer's connection (privileged in-server path). Self-
	// termination does NOT reach here — the expr layer returns ErrSelfTerminate
	// so the serve loop emits the FATAL and tears down its own connection.
	// M0118-0009 (temp-schema-cleanup process-exit permutation).
	ectx.TerminateBackend = func(pid int32) bool {
		if pid <= 0 {
			return false
		}
		return s.cancelReg.terminateByPID(uint32(pid))
	}
	// pg_notify(channel, payload) buffers into the connection's transaction so it
	// publishes to LISTENers at commit, exactly like the NOTIFY statement.
	// M0118-0009 (async-notify).
	if connTx != nil {
		ectx.QueueNotify = func(channel, payload string) {
			connTx.bufferNotify(channel, payload, connTx.BackendPID)
		}
	}
	// pg_notification_queue_usage() reports the occupied fraction of the async
	// queue (undelivered notifications across all listeners). M0118-0009.
	if s.notify != nil {
		ectx.NotifyQueueUsage = s.notify.QueueUsage
	}
	if connTx != nil {
		ectx.ProcNum = connTx.ProcNum
	}
	ectx.WAL = s.cfg.WAL
	ectx.SyncRep = s.cfg.SyncRep
	ectx.SyncCommitMode = sessionSyncCommitMode(sess)
	ectx.AsyncCommit = sessionAsyncCommit(sess)
	if s.applyLauncher != nil {
		ectx.OnSubscriptionChange = s.applyLauncher.Wake
	}
	ectx.DataDir = s.cfg.DataDir
	// Keep the connection-time role set + auth UserStore in sync when the
	// executor's execDropCompat role arm drops a role (DROP ROLE parses as a
	// generic DropStmt, bypassing tryHandleRoleDDL). root-0021.
	ectx.OnRoleDropped = func(name string) {
		_ = s.unregisterRole(name, true)
		s.removeRoleCredential(name)
	}
	// ON COMMIT DROP at transaction COMMIT changes the catalog with no DDL
	// statement to trip the per-statement invalidation below; runOnCommit
	// calls this hook so a plan cached before the commit (referencing the
	// dropped temp relation) is not reused. M0134-0072.
	if s.pc != nil {
		ectx.OnCommitDDL = s.pc.Invalidate
	}
	ectx.Promote = s.cfg.Promote
	// Same registry the walsender's CREATE_REPLICATION_SLOT /
	// DROP_REPLICATION_SLOT commands mutate, so the SQL functions and the
	// wire commands see one shared slot set (sibling-path rule).
	// M-NIGHTLY AI-20260810-011258-003.
	ectx.ReplSlots = s.cfg.Slots
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
		_ = w.WriteNoticeResponse([]libpq.ErrorField{
			{Code: libpq.FieldSeverity, Value: "NOTICE"},
			{Code: libpq.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: libpq.FieldSQLState, Value: "00000"},
			{Code: libpq.FieldMessage, Value: msg},
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

	// Wire PL/pgSQL transaction control (COMMIT/ROLLBACK inside a DO block or a
	// procedure) ONLY in auto-commit mode — i.e. not inside an explicit BEGIN
	// block, where PG rejects transaction termination (SQLSTATE 2D000). The
	// callback commits/rolls back the current auto-commit transaction and chains
	// into a fresh one, updating the outer `tx`/`snap` so the trailing
	// auto-commit (and per-statement RC snapshot refresh) operate on the new
	// transaction. Session-scoped advisory locks survive (only xact-scoped locks
	// are released), matching PG. M0118-0008 (plpgsql-toast).
	if autoCommit {
		var chainPN int32
		if connTx != nil {
			chainPN = connTx.ProcNum
		}
		ectx.PLpgSQLCommitChain = func(rollback bool) error {
			if rollback {
				_ = s.cfg.TxnMgr.Rollback(tx)
			} else if cerr := ectx.CommitTransaction(tx); cerr != nil {
				return cerr
			}
			executor.ReleaseAdvisoryTransactionLocks(advisoryReleaseTarget)
			newTx, berr := s.cfg.TxnMgr.Begin(transam.IsolationReadCommitted, chainPN)
			if berr != nil {
				return berr
			}
			newSnap, serr := s.cfg.TxnMgr.SnapshotFor(newTx)
			if serr != nil {
				_ = s.cfg.TxnMgr.Rollback(newTx)
				return serr
			}
			tx = newTx
			snap = newSnap
			ectx.Tx = newTx
			ectx.Snap = newSnap
			return nil
		}
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
		// Keep the transaction-scoped lock identity in sync with the LIVE
		// transaction state across statements in a single simple-query message.
		// ectx.TxnLockBackendID is seeded once before this loop from the
		// message-entry state, which is autocommit for a "BEGIN; LOCK ..." step
		// (the upstream isolationtester sends such a step as one PQexec message).
		// A BEGIN earlier in the SAME message opens the explicit block, so a
		// later LOCK TABLE — or a transaction-scoped maintenance lock — in that
		// message must be transaction-scoped too; without this refresh it would
		// see TxnLockBackendID==0 and acquire a display-only no-op lock that no
		// concurrent session blocks on (regressing vacuum-concurrent-drop /
		// vacuum-skip-locked). M0118-0009.
		if connTx != nil && connTx.InExplicit() {
			ectx.TxnLockBackendID = connTx.LockBackendID
		} else {
			ectx.TxnLockBackendID = 0
		}
		// Check for failed transaction state (25P02) — reject all statements
		// except COMMIT/ROLLBACK/ABORT/END that clear the failed state.
		// PostgreSQL semantics: an error inside an explicit transaction block
		// marks the block as aborted; all subsequent statements get 25P02
		// until the client issues ROLLBACK. M0100-0005.
		if connTx != nil && connTx.IsFailed() {
			_, isCommit := stmt.(*parser.CommitStmt)
			_, isRollback := stmt.(*parser.RollbackStmt)
			_, isRollbackTo := stmt.(*parser.RollbackToSavepointStmt)
			// Two-phase-commit statements are also allowed through: PG's
			// PREPARE TRANSACTION on an aborted block silently rolls back
			// (no 25P02), and COMMIT/ROLLBACK PREPARED of a gid the failed
			// connection never prepared reports "does not exist". Handled in
			// execTwoPhaseStmt. M0118-0009 (prepared-transactions).
			if !isCommit && !isRollback && !isRollbackTo && !isTwoPhaseStmt(stmt) {
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
					return s.writeQueryError(w, errcodes.SystemError, fmt.Sprintf("prepared statement %q has no body", ex.Name))
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
					if plan, planErr := optimizer.PlanWithSettings(ps.Query, sessionPlanCatalog(sess, ectx.Catalog, ectx.CurrentDatabaseOid), sessionPlannerSettings(sess)); planErr == nil {
						schema := plan.Output()
						if len(schema) > 0 {
							resultTypes := make([]string, len(schema))
							resultNames := make([]string, len(schema))
							for k, col := range schema {
								resultTypes[k] = normResultType(col.Type.Name)
								resultNames[k] = col.Name
							}
							prepStmts.SetResultSchema(ps.Name, resultNames, resultTypes)
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
						return s.writeQueryError(w, errcodes.SystemError, fmt.Sprintf("prepared statement %q has no body", es.Name))
					}
					// Detect a cached-plan result-type change before executing
					// (M0134-0054 bucket 5), mirroring PostgreSQL's
					// RevalidateCachedQuery (plancache.c:858): it compares
					// PlanCacheComputeResultDesc(tlist) against
					// plansource->resultDesc via equalRowTypes whenever
					// plansource->fixed_result is set (i.e. the statement is
					// row-returning). goopg has no cached physical plan, so we
					// re-derive the current result descriptor by re-planning
					// against the live catalog and compare it against the
					// descriptor captured at PREPARE time. Only applies when
					// PREPARE captured a non-empty result descriptor — a
					// non-data-returning statement (INSERT/UPDATE/DELETE
					// without RETURNING, DDL, …) has none, mirroring PG's
					// fixed_result gate.
					if len(prepDef.resultTypes) > 0 && ectx.Catalog != nil {
						if plan, planErr := optimizer.PlanWithSettings(prepDef.stmt, sessionPlanCatalog(sess, ectx.Catalog, ectx.CurrentDatabaseOid), sessionPlannerSettings(sess)); planErr == nil {
							schema := plan.Output()
							changed := len(schema) != len(prepDef.resultTypes)
							if !changed {
								for k, col := range schema {
									if col.Name != prepDef.resultNames[k] || normResultType(col.Type.Name) != prepDef.resultTypes[k] {
										changed = true
										break
									}
								}
							}
							if changed {
								return s.writeQueryError(w, "0A000", "cached plan must not change result type")
							}
						}
					}
					// Validate parameter count when the PREPARE declared a type list.
					if prepDef.paramTypes != nil && len(es.Params) != len(prepDef.paramTypes) {
						detail := fmt.Sprintf("Expected %d parameters but got %d.",
							len(prepDef.paramTypes), len(es.Params))
						return s.writeQueryError(w, "08P01",
							fmt.Sprintf("wrong number of parameters for prepared statement %q", es.Name),
							libpq.ErrorField{Code: libpq.FieldDetail, Value: detail})
					}
					params, err := evalExecuteParams(es.Params)
					if err != nil {
						if _, ok := err.(*executor.ExecError); ok {
							return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
						}
						return s.writeQueryError(w, errcodes.SyntaxError, err.Error())
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
								libpq.ErrorField{Code: libpq.FieldHint, Value: "You will need to rewrite or cast the expression."})
						}
					}
					// Coerce each argument to the declared parameter type, mirroring
					// PG's EvaluateParams (prepare.c). Without this, the raw literal
					// datum reaches the plan un-cast (e.g. a regclass[] arg stays
					// KindString), so OID comparisons silently evaluate false.
					// M0134-0005.
					for idx := range params {
						if idx >= len(prepDef.paramTypes) {
							break
						}
						coerced, cerr := executor.CoerceParamToDeclaredType(params[idx], prepDef.paramTypes[idx], ectx)
						if cerr != nil {
							if _, ok := cerr.(*executor.ExecError); ok {
								return s.writeQueryError(w, execErrCode(cerr), execErrMsg(cerr), execErrDetailFields(cerr)...)
							}
							return s.writeQueryError(w, errcodes.SyntaxError, cerr.Error())
						}
						params[idx] = coerced
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
					if _, ok := err.(*executor.ExecError); ok {
						return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
					}
					return s.writeQueryError(w, errcodes.SyntaxError, err.Error())
				}
				// Sibling of the *parser.ExecuteStmt branch above: coerce each
				// argument to the declared parameter type. M0134-0005.
				for idx := range params {
					if idx >= len(prepDef.paramTypes) {
						break
					}
					coerced, cerr := executor.CoerceParamToDeclaredType(params[idx], prepDef.paramTypes[idx], ectx)
					if cerr != nil {
						if _, ok := cerr.(*executor.ExecError); ok {
							return s.writeQueryError(w, execErrCode(cerr), execErrMsg(cerr), execErrDetailFields(cerr)...)
						}
						return s.writeQueryError(w, errcodes.SyntaxError, cerr.Error())
					}
					params[idx] = coerced
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
				// Materialise the cursor eagerly at DECLARE, mirroring PG: a
				// cursor's portal is opened and its snapshot taken at DECLARE,
				// not at first FETCH. This matters for snapshot stability and
				// for locking: inside an explicit transaction the materialising
				// scan takes a txn-scoped AccessShare on every relation it reads
				// (acquireScanReadLockTxn), held to commit, so a concurrent
				// ALTER TABLE … DETACH PARTITION … CONCURRENTLY parks behind the
				// open cursor (it waits for relation lockers) and a later FETCH
				// returns the declaration-time partition set rather than the
				// post-detach one. goopg already buffers all rows at first FETCH,
				// so this only shifts the materialisation point earlier (no new
				// memory cost). M0118-0008 detach-partition-concurrently-4
				// (design 0118-0063). A materialisation error surfaces at DECLARE
				// (as in PG, where planning/opening happens at DECLARE).
				if cur, found := connTx.cursorLookup(dc.Name); found {
					if err := s.materializeCursor(ectx, cur, dc.Name); err != nil {
						return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
					}
				}
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
		if ectx.Tx.Isolation == transam.IsolationReadCommitted {
			// The pre-loop SnapshotFor(tx) already captured a fresh RC snapshot
			// (and CAS-lowered the proc-array xmin) into ectx.Snap for this
			// Query message. For the FIRST statement, reuse it instead of
			// re-capturing — the two would be taken microseconds apart with no
			// intervening commit visible to this backend, so it is the same RC
			// command-start snapshot; this removes the redundant second capture
			// on the single-statement autocommit hot path (pgbench -S,
			// perf-optimize3-dash/08 doc 05). Later statements (i>0) still
			// refresh per-statement for RC freshness. (i>0 also covers the
			// case where an in-loop BEGIN promoted the tx; the pre-loop snap
			// only ever belongs to statement 0, before any promotion.)
			if i > 0 {
				snap2, err := s.cfg.TxnMgr.SnapshotFor(tx)
				if err != nil {
					return s.writeQueryError(w, errcodes.SystemError, err.Error())
				}
				ectx.Snap = snap2
			}
		} else if stmtTakesSnapshot(stmt) {
			// RR/SSI: pin the transaction's snapshot at the FIRST snapshot-taking
			// batched statement after a `BEGIN ISOLATION LEVEL …` that shares its
			// simple-query message with following statements (PG-correct timing —
			// PG defers the SSI/RR snapshot to the first statement that actually
			// reads MVCC data, NOT to BEGIN and NOT to a utility statement like
			// SET/SHOW/RESET). SnapshotFor pins firstSnap + registers the
			// proc-array xmin on the first call and returns the pinned clone
			// thereafter, so this is idempotent across the batch. Without it a
			// batched `BEGIN ISOLATION LEVEL REPEATABLE READ; SELECT 1;` never
			// registers its xmin and OldestXmin/VACUUM ignore the session
			// (horizons perm 4). Gating on stmtTakesSnapshot keeps a batched
			// `BEGIN … SERIALIZABLE; SET debug_parallel_query = on;` from pinning
			// the snapshot before the session's first real read
			// (serializable-parallel). The lost-update hazard this earlier pin
			// would otherwise expose is handled authoritatively in the
			// EvalPlanQual write paths (epqXmaxSettled). Design 0118-0105.
			snap2, err := s.cfg.TxnMgr.SnapshotFor(ectx.Tx)
			if err == nil {
				ectx.Snap = snap2
			}
		}
		// Per-statement reset: clear the regular-CTE row cache from any
		// previous statement. The row cache is query-scoped: a CTE named
		// "q" in query 1 must not bleed into query 2 (they may produce
		// different rows).
		// M0129-S8.2: advance the transaction command counter for the new
			// statement. CommandCounterIncrement only advances when the used flag
			// is set (a prior statement in this transaction wrote a tuple),
			// matching PostgreSQL's lazy-advance scheme. Pin the result as the
			// statement's es_output_cid.
			ectx.CommandCounterIncrement()
			ectx.CmdID = ectx.GetCurrentCommandId(true)
		ectx.CTERowCache = nil
		ectx.DeadlockVictim = false

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
						connTx.ReleasePinnedSnapshotOnFail(ectx.TxnMgr)
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
		var precached optimizer.Node
		var cacheKey string
		if s.pc != nil && len(stmts) == 1 && !disablePlanCache && !isNotifyStmt(stmt) && !isTwoPhaseStmt(stmt) && !isCurrentOfDML(stmt) && !sessionTempInheritanceActive(s.cfg.Catalog) && !partitionDetachPending(s.cfg.Catalog) && !inheritanceChangePending(s.cfg.Catalog) {
			cacheKey = planCacheKey(sql, ectx.CurrentDatabaseOid, sessionPlannerFingerprint(sess))
			if cached, ok := s.pc.Get(cacheKey); ok {
				precached = cached
			} else {
				// Cache miss: plan now so we can store it.
				freshNode, perr := optimizer.PlanWithSettings(stmt, sessionPlanCatalog(sess, s.cfg.Catalog, ectx.CurrentDatabaseOid), sessionPlannerSettings(sess))
				if perr != nil {
					// M0132-S5 (S1 finding (i)): a PLAN-time error must abort
					// the block too. Every other error path reaches
					// connTx.Fail() via errQueryErrorSent below, but this
					// cache-miss planning site returns straight out of the
					// dispatch loop, so the block used to stay live and
					// healthy after `BEGIN; INSERT INTO <missing table>;`.
					// PostgreSQL aborts on ANY error inside the block
					// (postgres.c's error handler, not the executor).
					if !autoCommit && connTx != nil && connTx.InExplicit() {
						connTx.Fail()
						connTx.ReleasePinnedSnapshotOnFail(ectx.TxnMgr)
					}
					code, msg := planErrorFields(perr)
					return s.writeQueryError(w, code, msg, planErrorHintFields(perr)...)
				}
				if planCacheIsCacheable(freshNode) {
					s.pc.Put(cacheKey, freshNode)
				}
				precached = freshNode
			}
			// P6: wrap AFTER the cache read/write, so the CACHE holds the
			// serial plan and the Gather is chosen per statement from this
			// session's GUCs. See applyParallelPostPass.
			precached = applyParallelPostPass(precached, sess, ectx)
		}
		// M0097-0059: enforce statement_timeout by deriving a deadline
		// context. The executor checks ctx.Ctx.Err() at each outer-row
		// boundary; when the deadline fires the next check returns
		// context.DeadlineExceeded and the executor surfaces error 57014.
		savedCtx := ectx.Ctx
		var stmtCancel context.CancelFunc
		stmtCtx := ctx
		if timeoutMs := sessionStatementTimeout(sess); timeoutMs > 0 {
			stmtCtx, stmtCancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		}
		// Carry the session's lock_timeout down to the lock-wait primitives
		// (lockmgr / WaitForXID) so a statement that blocks on a lock is
		// aborted with "canceling statement due to lock timeout" after the
		// configured interval, independent of statement_timeout. M0118-0009.
		if ltMs := sessionLockTimeout(sess); ltMs > 0 {
			stmtCtx = lockwait.WithTimeout(stmtCtx, time.Duration(ltMs)*time.Millisecond)
		}
		ectx.Ctx = stmtCtx
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
					connTx.ReleasePinnedSnapshotOnFail(ectx.TxnMgr)
					// A deadlock victim releases its XID at the abort, not at the
					// eventual explicit ROLLBACK, so a peer blocked on its catalog
					// tuple xmax unblocks immediately. M0118-0009 (design 0118-0115,
					// intra-grant-inplace perm 8).
					if ectx.DeadlockVictim {
						connTx.AbortInPlaceOnFail(ectx.TxnMgr)
					}
				}
				return nil
			}
			return err
		}
		// End-of-statement drain for DEFERRABLE (but not currently deferred-to-
		// COMMIT) UNIQUE/PK checks queued during the statement that just
		// succeeded — PostgreSQL fires every constraint trigger whose deferred
		// flag is not set at the end of its own SQL statement, regardless of
		// whether the statement runs in autocommit or inside an explicit
		// transaction block (postgres/src/backend/catalog/index.c:2080-2082,
		// indimmediate=false for any DEFERRABLE index; trigger.c's
		// end-of-command AfterTriggerFireDeferred firing). A violation here
		// rolls the (auto-begun or explicit) transaction back with 23505, the
		// same shape the immediate synchronous check raises. No-op when
		// nothing was queued. b4-s1-stmt-end-unique.
		if bs, ok := ectx.Session.(*executor.BasicSession); ok {
			if drainErr := executor.RunStmtEndDeferredUniqueChecks(ectx, bs); drainErr != nil {
				if !autoCommit && connTx != nil && connTx.InExplicit() {
					connTx.Fail()
					connTx.ReleasePinnedSnapshotOnFail(ectx.TxnMgr)
				} else {
					_ = ectx.TxnMgr.Rollback(tx)
				}
				code := errcodes.UniqueViolation
				var fields []libpq.ErrorField
				msg := drainErr.Error()
				if ee, eok := drainErr.(*executor.ExecError); eok {
					if ee.Code != "" {
						code = errcodes.Code(ee.Code)
					}
					msg = ee.Message
					if ee.Detail != "" {
						fields = append(fields, libpq.ErrorField{Code: libpq.FieldDetail, Value: ee.Detail})
					}
				}
				return s.writeQueryError(w, code, msg, fields...)
			}
		}
		// An explicit transaction block (BEGIN/START TRANSACTION … COMMIT/ROLLBACK)
		// ended mid-batch. The message-level transaction `tx` was finalized by the
		// verb (committed or rolled back) and is now dead, and *autoCommitPtr was
		// left false so the trailing auto-commit wouldn't double-commit it — but any
		// REMAINING statement in this message would then run against the finalized
		// transaction and fail with "mvcc: unknown transaction". PG starts a fresh
		// autocommit transaction for each statement that follows an ended block, so
		// re-arm here: begin a fresh RC transaction and re-mark the message as
		// auto-committing, exactly like the PLpgSQLCommitChain closure above.
		if !autoCommit && connTx != nil && !connTx.InExplicit() {
			var chainPN int32
			if connTx != nil {
				chainPN = connTx.ProcNum
			}
			newTx, berr := s.cfg.TxnMgr.Begin(transam.IsolationReadCommitted, chainPN)
			if berr != nil {
				return s.writeQueryError(w, errcodes.SystemError, berr.Error())
			}
			newSnap, serr := s.cfg.TxnMgr.SnapshotFor(newTx)
			if serr != nil {
				_ = s.cfg.TxnMgr.Rollback(newTx)
				return s.writeQueryError(w, errcodes.SystemError, serr.Error())
			}
			tx = newTx
			snap = newSnap
			ectx.Tx = newTx
			ectx.Snap = newSnap
			autoCommit = true
		}
		// Write back the temp-table shadow map so it persists across statements. M0097-0003.
		if connTx != nil && ectx.TempTableShadows != nil {
			connTx.TempTableShadows = ectx.TempTableShadows
		}
		// Write back the session's ON COMMIT registrations so an autocommit
		// CREATE TEMP TABLE ... ON COMMIT ... registration made in a
		// message-scoped session survives to a later message's explicit
		// COMMIT (seeded back into each ectx's Session at setup above).
		// M0134-0072.
		if connTx != nil {
			if bs, ok := ectx.Session.(*executor.BasicSession); ok {
				connTx.OnCommitActions = bs.OnCommitActions()
			}
		}
		// Write back pending enum values/renames/creates (including nil after
		// COMMIT/ROLLBACK) — but ONLY while an explicit transaction is open.
		// Since TracksDDLUndo() now also lets a message-scoped autocommit
		// throwaway session (NewAutocommitUndoSession) populate
		// ectx.PendingCreatedEnums/etc., writing those back unconditionally
		// would leak them into connTx past the end of THIS Query message —
		// the next, wholly unrelated autocommit message would then inherit a
		// stale "pending" entry for an already-committed type and could have
		// it incorrectly dropped by an unrelated abort (the same collateral-
		// damage bug class conn_tx.go's Session() staleness fix closed for
		// pendingDDL). A real explicit transaction still needs this write-back
		// to carry the pending set across Query messages until its own
		// COMMIT/ROLLBACK. root-0024 residual, M0110-0001 — the mid-batch
		// (autocommit-then-BEGIN, same message) combination is a separate,
		// still-open follow-up (see the design doc).
		if connTx != nil && connTx.InExplicit() {
			connTx.PendingEnumValues = ectx.PendingEnumValues
			connTx.PendingEnumRenames = ectx.PendingEnumRenames
			connTx.PendingCreatedEnums = ectx.PendingCreatedEnums
			connTx.PendingCreatedComposites = ectx.PendingCreatedComposites
			connTx.PendingCreatedRangeTypes = ectx.PendingCreatedRangeTypes
		}
		// Keep the savepoint-aware NOTIFY buffer in sync with the just-executed
		// savepoint command so a later ROLLBACK TO SAVEPOINT discards the
		// notifications queued since the savepoint and RELEASE merges them
		// (PostgreSQL async.c per-subtransaction pendingNotifies). Runs only on
		// success — an erroring statement returned above. M0118-0009.
		if connTx != nil {
			switch sp := stmt.(type) {
			case *parser.SavepointStmt:
				connTx.notifySavepoint(sp.Name)
			case *parser.ReleaseSavepointStmt:
				connTx.notifyReleaseSavepoint(sp.Name)
			case *parser.RollbackToSavepointStmt:
				connTx.notifyRollbackToSavepoint(sp.Name)
			}
		}
	}
	// Update pg_stat_activity after successful execution: upstream parks as
	// STATE_IDLE when no transaction block is open and
	// STATE_IDLEINTRANSACTION otherwise (postgres.c main loop).
	if reg := s.cfg.Activity; reg != nil && connTx != nil {
		state := "idle"
		if connTx.InExplicit() {
			state = "idle in transaction"
		}
		reg.UpdateState(connTx.ProcNum, state, "")
	}
	if autoCommit {
		if err := ectx.CommitTransaction(tx); err != nil {
			return s.writeQueryError(w, errcodes.SystemError, err.Error())
		}
		executor.ReleaseAdvisoryTransactionLocks(advisoryReleaseTarget)
		commit = true
		// Forced GC only helps after a transaction that actually wrote (a
		// read-only SELECT produces no retained heap worth a GC round). Gate
		// on the write-XID predicate so pgbench -S never pays even the atomic
		// counter add (perf-optimize3-dash/08 doc 10). Use ectx.Tx.XID, the
		// Context's in-place-updated copy, not the stale outer tx.
		if ectx.DidWrite() {
			maybeForceGCAfterCommit()
		}
		// NOTIFY becomes visible to listeners at the notifying transaction's
		// commit; publish the buffer accumulated by this autocommit batch.
		// M0118-0009.
		s.publishPendingNotify(connTx)
	}
	// Deliver any notifications queued for this session (by this transaction's
	// own NOTIFY and/or other backends) at the command boundary, before
	// ReadyForQuery. M0118-0009 (async-notify).
	if err := s.deliverNotifications(w, connTx); err != nil {
		return err
	}
	return w.ReadyForQuery()
}

// sessionStatsTarget reads the effective `default_statistics_target`
// GUC from the per-connection session registry. Zero is returned
// when sess is nil or the value can't be parsed; callers (the
// executor's analyzeOp) treat zero as "use the upstream default".
func sessionStatsTarget(sess *misc.SessionRegistry) int {
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
func sessionFreezeMinAge(sess *misc.SessionRegistry) int64 {
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
func sessionSyncCommitMode(sess *misc.SessionRegistry) xlog.SyncRepMode {
	if sess == nil {
		return xlog.SyncRepRemoteFlush
	}
	_, eff, ok := sess.Get("synchronous_commit")
	if !ok {
		return xlog.SyncRepRemoteFlush
	}
	return xlog.ParseSyncCommitLevel(strings.ToLower(strings.TrimSpace(eff)))
}

// sessionAsyncCommit reports whether the session-effective
// `synchronous_commit` GUC is literally "off" — the only level that also
// skips the LOCAL WAL flush before returning to the client. Every other
// level (including "local", which sessionSyncCommitMode collapses together
// with "off" into SyncRepOff for the *remote*-wait decision) still requires
// the local flush. M0117-0007 Part B.
func sessionAsyncCommit(sess *misc.SessionRegistry) bool {
	if sess == nil {
		return false
	}
	_, eff, ok := sess.Get("synchronous_commit")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(eff)) {
	case "off", "false", "0", "no":
		return true
	}
	return false
}

func sessionOpportunisticPrune(sess *misc.SessionRegistry) bool {
	if sess == nil {
		return true // default on
	}
	_, eff, ok := sess.Get("enable_opportunistic_prune")
	if !ok {
		return true // GUC not registered yet, default on
	}
	return strings.EqualFold(strings.TrimSpace(eff), "on")
}

// applyParallelPostPass wraps a finished plan in a Gather when the statement
// and the session's settings allow it. P6 of docs/design/parallel-query/.
//
// It is called AFTER the plan-cache lookup on BOTH protocol paths, and that
// placement is load-bearing rather than incidental. plancache.go is
// process-wide and cross-session, keyed on namespace-oid + normalised SQL only
// — no session identity, no GUC fingerprint. Caching a plan that already
// contained a Gather would let one session's max_parallel_workers_per_gather
// leak into another's execution, so `SET max_parallel_workers_per_gather = 0`
// would silently fail to disable parallelism. The cache therefore stores
// SERIAL plans, and this wraps per statement.
//
// planner.MaybeAddGather is correspondingly non-mutating: it returns a new
// root sharing the cached children, because that cached node is being read
// concurrently by every other session running the same SQL.
// sessionGetter adapts a SessionRegistry to the plain (name) -> (effective,
// ok) getter the *From helpers take, so the registry channel and the
// executor.Context channel share one body per GUC. A nil registry reads as
// "nothing set", which is what every accessor's nil branch meant.
func sessionGetter(sess *misc.SessionRegistry) func(string) (string, bool) {
	if sess == nil {
		return func(string) (string, bool) { return "", false }
	}
	return func(name string) (string, bool) {
		_, eff, ok := sess.Get(name)
		return eff, ok
	}
}

func applyParallelPostPass(node optimizer.Node, sess *misc.SessionRegistry, ectx *executor.Context) optimizer.Node {
	return applyParallelPostPassFrom(node, sessionGetter(sess), ectx)
}

// ctxApplyParallelPostPass is applyParallelPostPass for the paths that hold an
// executor.Context but no SessionRegistry — the simple-query route, which is
// the one that plans a statement the cache did not supply. Same two-channel
// split, and same reason, as sessionPlannerSettings / ctxPlannerSettings.
func ctxApplyParallelPostPass(node optimizer.Node, ectx *executor.Context) optimizer.Node {
	if ectx == nil || ectx.GetSetting == nil {
		return node
	}
	return applyParallelPostPassFrom(node, ectx.GetSetting, ectx)
}

func applyParallelPostPassFrom(node optimizer.Node, get func(string) (string, bool), ectx *executor.Context) optimizer.Node {
	if node == nil || ectx == nil {
		return node
	}
	return optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
		MaxWorkersPerGather: maxParallelWorkersPerGatherFrom(get),
		MinTableScanBlocks:  minParallelTableScanSizeFrom(get),
		LeaderParticipates:  parallelLeaderParticipationFrom(get),
		DebugParallelQuery:  debugParallelQueryFrom(get),
		IsSerializable:      ectx.Tx.Isolation == transam.IsolationSerializable,
		BlocksForTable:      parallelBlocksForTable(ectx),
	})
}

// parallelBlocksForTable returns the relation-size lookup the parallel size
// gate uses. It is a live O(1) counter read (storage.relFile.nBlocks holds the
// value in memory), NOT a statistics lookup and NOT a scan — the same input
// PG's compute_parallel_worker() gets from RelationGetNumberOfBlocks().
//
// This is what lets the gate work on a freshly started server: goopg's ANALYZE
// row count is not restored at startup (ledger row pq-P6), so anything keyed on
// it would refuse every query until an ANALYZE had run.
func parallelBlocksForTable(ectx *executor.Context) func(*catalog.Table) (int64, bool) {
	if ectx == nil {
		return nil
	}
	return parallelBlocksForTableFrom(ectx.Pool, ectx.Catalog)
}

// parallelBlocksForTableFrom is the pool/catalog form, for the extended
// protocol where planning happens before an executor context exists.
func parallelBlocksForTableFrom(pool *storage.Pool, cat catalog.Catalog) func(*catalog.Table) (int64, bool) {
	if pool == nil || cat == nil {
		return nil
	}
	return func(t *catalog.Table) (int64, bool) {
		if t == nil {
			return 0, false
		}
		n, err := pool.NBlocks(cat.RelFileNode(t))
		if err != nil {
			return 0, false
		}
		return int64(n), true
	}
}

// ── parallel-query session GUCs (docs/design/parallel-query, P1) ──────────
//
// These follow the sessionStatsTarget shape exactly: three layers of
// defensive fallback (nil registry / unregistered GUC / unparseable value),
// each landing on a value that is CORRECT rather than a sentinel. That matters
// here more than usual — six other executor.NewContext() sites (copy.go,
// database_ddl.go, role_ddl.go) set no session GUCs at all, so a zero value
// must mean something sane on its own.
//
// All of them read via sess.Get, never GetDisplay: Get returns the canonical
// bare integer ("1024"), GetDisplay returns the human form ("8MB"). Internal
// arithmetic must use the former (internal/config/session.go:85-90).

// sessionMaxParallelWorkersPerGather reads `max_parallel_workers_per_gather`.
// Zero means "no parallelism" and is a legitimate user setting, not an
// absence — so an unreadable GUC falls back to 0 (serial), the safe direction.
func sessionMaxParallelWorkersPerGather(sess *misc.SessionRegistry) int {
	return maxParallelWorkersPerGatherFrom(sessionGetter(sess))
}

func maxParallelWorkersPerGatherFrom(get func(string) (string, bool)) int {
	eff, ok := get("max_parallel_workers_per_gather")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(eff))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// sessionMaxParallelWorkers reads the cluster-wide `max_parallel_workers`
// cap. Same fallback reasoning as above: 0 disables parallelism entirely,
// which is the direction that cannot cause harm.
func sessionMaxParallelWorkers(sess *misc.SessionRegistry) int {
	if sess == nil {
		return 0
	}
	_, eff, ok := sess.Get("max_parallel_workers")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(eff))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// sessionMinParallelTableScanSize reads `min_parallel_table_scan_size` in
// BLOCKS — the GUC's native unit as of P0. A relation smaller than this never
// gets a parallel path (upstream compute_parallel_worker,
// postgres/src/backend/optimizer/path/allpaths.c:4273).
//
// Zero here would mean "every relation qualifies", which is the unsafe
// direction, so an unreadable GUC falls back to PG's default of 1024 blocks
// (8MB) rather than to zero.
func sessionMinParallelTableScanSize(sess *misc.SessionRegistry) int64 {
	return minParallelTableScanSizeFrom(sessionGetter(sess))
}

func minParallelTableScanSizeFrom(get func(string) (string, bool)) int64 {
	const pgDefaultBlocks = 1024 // (8 * 1024 * 1024) / BLCKSZ
	eff, ok := get("min_parallel_table_scan_size")
	if !ok {
		return pgDefaultBlocks
	}
	n, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64)
	if err != nil || n < 0 {
		return pgDefaultBlocks
	}
	return n
}

// sessionParallelLeaderParticipation reads `parallel_leader_participation`.
// Upstream's default is on; an unreadable GUC keeps that.
func sessionParallelLeaderParticipation(sess *misc.SessionRegistry) bool {
	return parallelLeaderParticipationFrom(sessionGetter(sess))
}

func parallelLeaderParticipationFrom(get func(string) (string, bool)) bool {
	eff, ok := get("parallel_leader_participation")
	if !ok {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(eff), "on")
}

// sessionDebugParallelQuery reads `debug_parallel_query`, upstream's lever for
// forcing parallel plans in testing. Returns the canonical enum value
// ("off" / "on" / "regress"); the P0 synonym work means a user may have
// written `true`, and canonicalisation has already mapped it to "on".
func sessionDebugParallelQuery(sess *misc.SessionRegistry) string {
	return debugParallelQueryFrom(sessionGetter(sess))
}

func debugParallelQueryFrom(get func(string) (string, bool)) string {
	eff, ok := get("debug_parallel_query")
	if !ok {
		return "off"
	}
	v := strings.ToLower(strings.TrimSpace(eff))
	switch v {
	case "on", "regress":
		return v
	default:
		return "off"
	}
}

func sessionWorkMem(sess *misc.SessionRegistry) int64 {
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
func sessionStatementTimeout(sess *misc.SessionRegistry) int64 {
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

// sessionLockTimeout reads the effective `lock_timeout` GUC from the session
// and returns it in milliseconds. Returns 0 (no timeout) if the setting is
// missing, zero, or unparseable. Unlike statement_timeout this bounds only
// the time spent *waiting for a lock*, so it is plumbed into the lock-wait
// primitives via lockwait.WithTimeout rather than the statement deadline.
// M0118-0009.
func sessionLockTimeout(sess *misc.SessionRegistry) int64 {
	if sess == nil {
		return 0
	}
	_, eff, ok := sess.Get("lock_timeout")
	if !ok {
		return 0
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return ms
}

// sessionTempInheritanceActive reports whether the base catalog currently has a
// session-owned temporary inheritance child. When true the cross-session plan
// cache must be bypassed: a query scanning an inheritance parent expands to a
// session-specific child set (RELATION_IS_OTHER_TEMP), so one session's cached
// plan would wrongly be served to another. Returns false (cache enabled) for
// any catalog that does not expose the check. Design 0118-0037 (inherit-temp).
func sessionTempInheritanceActive(base catalog.Catalog) bool {
	type tempInheritChecker interface{ HasTempInheritanceChildren() bool }
	type unwrapper interface{ Unwrap() catalog.Catalog }
	for {
		if c, ok := base.(tempInheritChecker); ok {
			return c.HasTempInheritanceChildren()
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
		} else {
			return false
		}
	}
}

// partitionDetachPending reports whether the base catalog currently has a
// partition child marked detach-pending by an in-progress
// ALTER TABLE … DETACH PARTITION … CONCURRENTLY. When true the cross-session
// plan cache must be bypassed: the visible partition set is snapshot-relative
// (catalog.VisiblePartitionChildren), so a query scanning the parent must
// re-plan against its own snapshot epoch rather than reuse a plan baked at a
// different epoch. Returns false (cache enabled) for any catalog that does not
// expose the check. Design 0118-0059 (detach-partition-concurrently).
func partitionDetachPending(base catalog.Catalog) bool {
	type checker interface{ HasPendingPartitionDetach() bool }
	type unwrapper interface{ Unwrap() catalog.Catalog }
	for {
		if c, ok := base.(checker); ok {
			return c.HasPendingPartitionDetach()
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
		} else {
			return false
		}
	}
}

// inheritanceChangePending reports whether the base catalog currently has an
// ALTER TABLE … {NO} INHERIT deferred to COMMIT by an in-progress explicit
// transaction. When true the cross-session plan cache must be bypassed: a query
// scanning the inheritance parent must re-plan against the current (pre-commit)
// child set rather than reuse a plan cached across the inheritance change — the
// same constraint partitionDetachPending imposes for concurrent detach. Returns
// false for any catalog that does not expose the check. Design 0118-0080
// (M0118-0008 alter-table-4).
func inheritanceChangePending(base catalog.Catalog) bool {
	type checker interface{ HasPendingInheritanceChange() bool }
	type unwrapper interface{ Unwrap() catalog.Catalog }
	for {
		if c, ok := base.(checker); ok {
			return c.HasPendingInheritanceChange()
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
		} else {
			return false
		}
	}
}

// sessionPlanCatalog returns a search-path-aware catalog wrapper for use when
// calling planner.Plan. The wrapper re-reads search_path dynamically so that
// SET search_path changes take effect on the next statement. When sess is nil
// the base catalog is returned unchanged. dbOid is the querying connection's
// real database oid (resolveConnDBOid/ectx.CurrentDatabaseOid); it seeds
// SearchPathCatalog.DBOid so LookupTable/LookupIndex key off the connection's
// own namespace (M0122-0007 slice 4c, design 0122-0018). Pass 0 for
// connection-less/embedded callers — effectiveDBOid falls back to
// DefaultDBOid. M0097-0022.
// scanToggleGUCs are the four scan-method toggles (enable_seqscan /
// enable_indexscan / enable_bitmapscan / enable_indexonlyscan) that reach the
// planner through the catalog wrapper (sessionPlanCatalog), NOT through
// PlannerSettings — so the cache fingerprint must carry them separately.
// B-18 commit 1 (take2 P2-04 cache-key half): sessions with a toggle off no
// longer bypass the shared cache; they key into their own entry.
var scanToggleGUCs = [...]string{"enable_seqscan", "enable_indexscan",
	"enable_bitmapscan", "enable_indexonlyscan"}

// sessionScanToggleOff reports whether sess turned the named scan-method
// toggle off. Bool GUCs normalise to "on"/"off".
func sessionScanToggleOff(sess *misc.SessionRegistry, name string) bool {
	if sess == nil {
		return false
	}
	_, eff, ok := sess.Get(name)
	return ok && strings.EqualFold(eff, "off")
}

// sessionPlannerSettings builds the per-statement planner context from the
// session's GUCs — take2 P2-02, the item that finally makes `SET
// random_page_cost` change a plan.
//
// Every field starts at the planner's own default and is overwritten only when
// the GUC parses, so a malformed or missing value degrades to today's behaviour
// rather than to a zero cost.
//
// UNITS. Both memory GUCs are registered `UnitKB`, and the GUC machinery
// normalises the display form, so `work_mem` reads back as "524288" and
// `effective_cache_size` as "4194304" — plain KB integers, not "512MB"/"4GB".
// The planner wants BYTES for work_mem and BLOCKS for effective_cache_size, and
// the two conversions differ. Getting either wrong is silent: the plan simply
// comes out costed for the wrong machine. The round-trip is pinned by test.
func sessionPlannerSettings(sess *misc.SessionRegistry) optimizer.PlannerSettings {
	if sess == nil {
		return optimizer.DefaultPlannerSettings()
	}
	return plannerSettingsFrom(func(name string) (string, bool) {
		_, eff, ok := sess.Get(name)
		return eff, ok
	})
}

// ctxPlannerSettings is sessionPlannerSettings for the paths that hold an
// executor.Context rather than a SessionRegistry — the simple-query route
// (executeOneSimpleStmt) among them.
//
// Both channels must exist because both are real: the extended-protocol and
// prepared-statement sites have `sess`, while the simple-query site has only
// `ctx.GetSetting`. Building one from the other is not possible at either site,
// so they share the BODY instead. Missing this second channel is what made the
// first live probe of P2-02 show unchanged costs while every unit test passed.
func ctxPlannerSettings(ctx *executor.Context) optimizer.PlannerSettings {
	if ctx == nil || ctx.GetSetting == nil {
		return optimizer.DefaultPlannerSettings()
	}
	return plannerSettingsFrom(ctx.GetSetting)
}

func plannerSettingsFrom(get func(string) (string, bool)) optimizer.PlannerSettings {
	ps := optimizer.DefaultPlannerSettings()
	readFloat := func(name string, dst *float64) {
		if eff, ok := get(name); ok {
			if v, err := strconv.ParseFloat(strings.TrimSpace(eff), 64); err == nil && v >= 0 {
				*dst = v
			}
		}
	}
	readFloat("seq_page_cost", &ps.SeqPageCost)
	readFloat("random_page_cost", &ps.RandomPageCost)
	readFloat("cpu_tuple_cost", &ps.CPUTupleCost)
	readFloat("cpu_index_tuple_cost", &ps.CPUIndexTupleCost)
	readFloat("cpu_operator_cost", &ps.CPUOperatorCost)
	readFloat("parallel_setup_cost", &ps.ParallelSetupCost)
	readFloat("parallel_tuple_cost", &ps.ParallelTupleCost)
	readFloat("hash_mem_multiplier", &ps.HashMemMultiplier)

	// take2 P2-05: the planner-method toggles. These were registered GUCs with
	// no consumer anywhere outside the pg_settings view — `SET
	// enable_hashjoin = off` was accepted and did nothing. They set
	// Path.DisabledNodes rather than skipping a producer, which is PG 18's own
	// mechanism: a query whose only legal plan uses a disabled method still
	// gets that plan.
	readBool := func(name string, dst *bool) {
		if eff, ok := get(name); ok {
			switch strings.ToLower(strings.TrimSpace(eff)) {
			case "on", "true", "yes", "1":
				*dst = true
			case "off", "false", "no", "0":
				*dst = false
			}
		}
	}
	readBool("enable_hashjoin", &ps.EnableHashJoin)
	readBool("enable_mergejoin", &ps.EnableMergeJoin)
	readBool("enable_nestloop", &ps.EnableNestLoop)
	readBool("enable_memoize", &ps.EnableMemoize)
	readBool("enable_nestloop_index", &ps.EnableNestLoopIndex)
	readBool("enable_hashagg", &ps.EnableHashAgg)
	readBool("enable_presorted_aggregate", &ps.EnablePresortedAggregate)
	readBool("geqo", &ps.Geqo)
	// take2 P3-10: the five remaining GEQO knobs. Zero is MEANINGFUL for
	// geqo_pool_size and geqo_generations (PG reads it as "derive me"), so they
	// accept it rather than treating it as unset.
	readIntMin := func(name string, dst *int, min int) {
		if eff, ok := get(name); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(eff)); err == nil && n >= min {
				*dst = n
			}
		}
	}
	readIntMin("geqo_effort", &ps.GeqoEffort, 1)
	readIntMin("geqo_pool_size", &ps.GeqoPoolSize, 0)
	readIntMin("geqo_generations", &ps.GeqoGenerations, 0)
	readFloat("geqo_selection_bias", &ps.GeqoSelectionBias)
	readFloat("geqo_seed", &ps.GeqoSeed)

	if eff, ok := get("geqo_threshold"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(eff)); err == nil && n >= 2 {
			ps.GeqoThreshold = n
		}
	}

	// work_mem: KB -> BYTES. The same conversion sessionWorkMem applies for the
	// executor's hash sizing — planner and executor must agree, which is what
	// cost_funcs.go's workMem comment demands.
	if eff, ok := get("work_mem"); ok {
		if kb, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64); err == nil && kb > 0 {
			ps.WorkMem = kb * 1024
		}
	}

	// effective_cache_size: KB -> BLOCKS.
	if eff, ok := get("effective_cache_size"); ok {
		if kb, err := strconv.ParseInt(strings.TrimSpace(eff), 10, 64); err == nil && kb > 0 {
			ps.EffectiveCacheSize = float64(kb) * 1024 / blockSizeBytesForPlanner
		}
	}
	return ps
}

// blockSizeBytesForPlanner mirrors optimizer's blockSizeBytes (relsize.go). It
// is restated here rather than exported because the optimizer's copy is the
// authority and a second EXPORTED constant would invite the two to drift; the
// unit test asserts the conversion against the planner's own default instead of
// against this number.
const blockSizeBytesForPlanner = 8192

// sessionPlannerFingerprint returns the plan-cache fingerprint for sess: its
// full PlannerSettings value plus its four scan-method toggles.
//
// B-18 commit 1 (take2 P2-04 cache-key half). The plan cache is server-level
// and cross-session; keying on (dbOid, normalized SQL) alone serves a plan
// costed under one connection's `random_page_cost` to every other connection.
// Sessions with their own planner inputs now key into their own cache entry
// instead of bypassing the shared cache.
//
// ParallelSettings is deliberately EXCLUDED: MaybeAddGather runs post-cache
// (applyParallelPostPass), so it never affects the cached serial plan — and it
// carries a func field (BlocksForTable) that is not formattable.
func sessionPlannerFingerprint(sess *misc.SessionRegistry) string {
	ps := sessionPlannerSettings(sess)
	return plannerCacheFingerprint(ps,
		sessionScanToggleOff(sess, "enable_seqscan"),
		sessionScanToggleOff(sess, "enable_indexscan"),
		sessionScanToggleOff(sess, "enable_bitmapscan"),
		sessionScanToggleOff(sess, "enable_indexonlyscan"))
}

// plannerCacheFingerprint formats every planner input the cache key must
// capture: the full PlannerSettings value (cost GUCs, method toggles, GEQO
// knobs, memory budgets) plus the four scan-method toggles, which travel
// through the catalog wrapper rather than through PlannerSettings.
//
// Floats use FormatFloat 'g' with precision -1: the shortest round-trip form,
// exact, never rounded. A fixed-precision verb (%.6f and friends) would fold
// distinct costs onto one key and serve a plan costed under the wrong value.
func plannerCacheFingerprint(ps optimizer.PlannerSettings, disableSeqScan, disableIndexScan, disableBitmapScan, disableIndexOnlyScan bool) string {
	float := func(v float64) string {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	bit := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	// Positional fields in PlannerSettings declaration order, then the four
	// scan toggles. Positions are fixed, so no field names are needed — but
	// every field must be present; the per-GUC fingerprint test fails if a
	// PlannerSettings input ever stops reaching the key.
	fields := []string{
		float(ps.SeqPageCost),
		float(ps.RandomPageCost),
		float(ps.CPUTupleCost),
		float(ps.CPUIndexTupleCost),
		float(ps.CPUOperatorCost),
		float(ps.ParallelSetupCost),
		float(ps.ParallelTupleCost),
		float(ps.EffectiveCacheSize),
		strconv.FormatInt(ps.WorkMem, 10),
		bit(ps.EnableHashJoin),
		bit(ps.EnableMergeJoin),
		bit(ps.EnableNestLoop),
		bit(ps.EnableSort),
		bit(ps.EnableSeqScan),
		bit(ps.EnableIndexScan),
		bit(ps.EnableBitmapScan),
		bit(ps.EnableHashAgg),
		bit(ps.EnablePresortedAggregate),
		bit(ps.Geqo),
		strconv.Itoa(ps.GeqoThreshold),
		bit(ps.EnableNestLoopIndex),
		strconv.Itoa(ps.GeqoEffort),
		strconv.Itoa(ps.GeqoPoolSize),
		strconv.Itoa(ps.GeqoGenerations),
		float(ps.GeqoSelectionBias),
		float(ps.GeqoSeed),
		bit(ps.EnableMemoize),
		float(ps.HashMemMultiplier),
		bit(disableSeqScan),
		bit(disableIndexScan),
		bit(disableBitmapScan),
		bit(disableIndexOnlyScan),
	}
	return strings.Join(fields, "\x1f")
}

func sessionPlanCatalog(sess *misc.SessionRegistry, base catalog.Catalog, dbOid uint32) catalog.Catalog {
	if sess == nil {
		return base
	}
	wrapped := catalog.WithSearchPath(base, func() []string {
		return searchPathSchemas(sess)
	})
	wrapped.DBOid = dbOid
	// Carry the session's temp-relation ownership token so the planner drops
	// other-session temp inheritance children during expansion. Must match
	// executor.sessionTempOwner: "s"+UniqueID(). Design 0118-0036 (inherit-temp).
	if id := sess.UniqueID(); id != 0 {
		wrapped.TempOwnerToken = "s" + strconv.FormatUint(id, 10)
	}
	// Carry the session's enable_seqscan toggle so the planner can promote an
	// ordered covering index scan to an IndexOnlyScan when seqscan is disabled
	// (PG-faithful, drops the Sort). Bool GUCs normalise to "on"/"off". Design
	// 0118-0103 (M0118-0009 horizons enabler).
	if _, eff, ok := sess.Get("enable_seqscan"); ok && strings.EqualFold(eff, "off") {
		wrapped.DisableSeqScan = true
	}
	// ... and the three index-side toggles, which the planner honors by
	// declining that scan shape (review/260831-2 X-8; they used to be accepted
	// and ignored).
	sessOff := func(name string) bool {
		_, eff, ok := sess.Get(name)
		return ok && strings.EqualFold(eff, "off")
	}
	wrapped.DisableIndexScan = sessOff("enable_indexscan")
	wrapped.DisableBitmapScan = sessOff("enable_bitmapscan")
	wrapped.DisableIndexOnlyScan = sessOff("enable_indexonlyscan")
	return wrapped
}

// resolveConnDBOid resolves dbName to its real, physical pg_database.oid via
// cat's databaseOidResolver, returning 0 (SearchPathCatalog.effectiveDBOid's
// "use DefaultDBOid" sentinel) when dbName is empty or unresolvable — the
// same fallback wireExtensionRows leaves ectx.CurrentDatabaseOid at. Exists
// because the single-statement and cross-session plan-cache paths
// (dispatch.go's cache-miss branch, executeExtendedQueryViaExecutor) build
// their PlanCatalog before wireExtensionRows stamps ectx.CurrentDatabaseOid —
// this resolves the same oid directly from the connection's dbName instead.
// M0122-0007 slice 4c.
func resolveConnDBOid(cat catalog.Catalog, dbName string) uint32 {
	if dbName == "" {
		return 0
	}
	if dr, ok := cat.(databaseOidResolver); ok {
		if oid, ok := dr.ResolveDatabaseOid(dbName); ok {
			return oid
		}
	}
	return 0
}

// ctxPlanCatalog is like sessionPlanCatalog but reads search_path from an
// executor.Context's GetSetting hook. Used inside executeOneSimpleStmt and
// materializeCursor which receive an executor.Context rather than a *config.SessionRegistry.
func ctxPlanCatalog(ctx *executor.Context, base catalog.Catalog) catalog.Catalog {
	if ctx == nil || ctx.GetSetting == nil {
		return base
	}
	getSetting := ctx.GetSetting // capture
	wrapped := catalog.WithSearchPath(base, func() []string {
		sp, ok := getSetting("search_path")
		if !ok || sp == "" {
			return []string{"public"}
		}
		return parseSearchPathSchemas(sp)
	})
	// Seed the connection's real database oid so LookupTable/LookupIndex key
	// off its own namespace (M0122-0007 slice 4c). Zero (no live connection)
	// falls back to DefaultDBOid via effectiveDBOid.
	wrapped.DBOid = ctx.CurrentDatabaseOid
	// Carry the session temp-owner token (matches executor.sessionTempOwner) so
	// the planner applies the RELATION_IS_OTHER_TEMP exclusion. Design 0118-0036.
	if s, ok := ctx.AdvisorySessionIdentity.(interface{ UniqueID() uint64 }); ok && s != nil {
		if id := s.UniqueID(); id != 0 {
			wrapped.TempOwnerToken = "s" + strconv.FormatUint(id, 10)
		}
	}
	// Carry this statement's snapshot partition-detach epoch so the planner
	// omits partition children concurrently detach-pending at or before the
	// snapshot (catalog.VisiblePartitionChildren). Plan caching is bypassed
	// while any detach is pending (partitionDetachPending), so this re-plans
	// per statement against the live snapshot epoch. Design 0118-0059.
	wrapped.SnapshotPartitionDetachEpoch = ctx.Snap.PartitionDetachEpoch
	// Carry the session's enable_seqscan toggle (matches sessionPlanCatalog) so
	// the planner promotes an ordered covering index scan to an IndexOnlyScan
	// when seqscan is disabled. Design 0118-0103 (horizons).
	if v, ok := getSetting("enable_seqscan"); ok && strings.EqualFold(v, "off") {
		wrapped.DisableSeqScan = true
	}
	// Sibling of sessionPlanCatalog's index-side toggles (review/260831-2 X-8).
	ctxOff := func(name string) bool {
		v, ok := getSetting(name)
		return ok && strings.EqualFold(v, "off")
	}
	wrapped.DisableIndexScan = ctxOff("enable_indexscan")
	wrapped.DisableBitmapScan = ctxOff("enable_bitmapscan")
	wrapped.DisableIndexOnlyScan = ctxOff("enable_indexonlyscan")
	return wrapped
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
func searchPathSchemas(sess *misc.SessionRegistry) []string {
	if sess == nil {
		return []string{"public"}
	}
	_, eff, ok := sess.Get("search_path")
	if !ok || eff == "" {
		return []string{"public"}
	}
	return parseSearchPathSchemas(eff)
}

// publicSchemaVisible reports whether "public" is visible on the effective
// search_path, mirroring internal/executor/expr.go's regObjectSchemaVisible
// (used there to decide regproc/regoperator/regtype schema-qualification).
// Unlike searchPathSchemas above, an explicitly empty search_path (pg_dump's
// search_path=”, ALWAYS_SECURE_SEARCH_PATH_SQL) is NOT defaulted back to
// public here — it correctly yields no visible schemas, forcing
// RegtypeName's caller to schema-qualify a user-defined type name.
// getSetting is nil-safe (a nil executor.Context.GetSetting, or no session
// at all) and falls back to the same default search_path executor/expr.go's
// searchPathSchemas uses. M0122-0005 pg_typeof()::oid follow-up.
func publicSchemaVisible(getSetting func(name string) (string, bool)) bool {
	sp := `"$user", public`
	if getSetting != nil {
		if eff, ok := getSetting("search_path"); ok {
			sp = eff
		}
	}
	for _, raw := range strings.Split(sp, ",") {
		s := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
		if strings.EqualFold(s, "public") {
			return true
		}
	}
	return false
}

func compatNoopCommandTag(sql string) (string, bool) {
	norm := normalizeCompatSQL(sql)
	switch {
	case strings.HasPrefix(norm, "create user "), strings.HasPrefix(norm, "create role "):
		return "CREATE ROLE", true
	case strings.HasPrefix(norm, "create schema "), norm == "create schema":
		return "CREATE SCHEMA", true // name extraction done separately in registerCompatNoopSchema
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
	case strings.HasPrefix(norm, "comment on "):
		return "COMMENT", true
	case strings.HasPrefix(norm, "security label "):
		return "SECURITY LABEL", true
	}
	return "", false
}

// isMultiStatementSQL reports whether sql carries a real second statement
// after its first top-level ';' (a trailing terminator with nothing but
// whitespace after it does not count). Used to gate the compatNoopCommandTag
// absorption below: matching a batch's leading prefix is only safe to do
// against a single statement, never against the raw text of a multi-
// statement batch. M0119-0004-ACLHEAP loop #87 follow-up.
func isMultiStatementSQL(sql string) bool {
	end := firstTopLevelSemicolon(sql)
	return end >= 0 && strings.TrimSpace(sql[end+1:]) != ""
}

// splitLeadingCompatNoopDDL splits a multi-statement batch whose FIRST
// statement matches compatNoopCommandTag AND whose grammar is entirely
// unimplemented (parser.Parse fails even in isolation — e.g. CREATE SCHEMA,
// CREATE/ALTER/DROP DATABASE; CREATE/ALTER/DROP ROLE is peeled off earlier by
// splitLeadingRoleDDL). Mirrors splitLeadingRoleDDL's split-first-handle-
// recurse-rest shape (M0118-0008) so trailing statements in the batch are not
// silently dropped.
//
// GRANT/REVOKE/COMMENT ON/SECURITY LABEL are deliberately excluded from this
// split: their grammar exists and a lone instance of any of them always
// parses successfully (verified M0119-0004-ACLHEAP loop #87), so if parsing
// the FULL batch still fails, the failure must come from a LATER statement
// being genuinely invalid SQL. In that case ok is false and the caller must
// fall through to the real syntax error for the whole batch rather than
// absorb it — the multi-statement-masking bug this function closes.
func splitLeadingCompatNoopDDL(sql string) (first, rest, tag string, ok bool) {
	end := firstTopLevelSemicolon(sql)
	if end < 0 {
		return "", "", "", false
	}
	first = sql[:end]
	rest = strings.TrimSpace(sql[end+1:])
	if rest == "" {
		return "", "", "", false
	}
	tag, matched := compatNoopCommandTag(first)
	if !matched {
		return "", "", "", false
	}
	if _, err := parser.Parse(first); err == nil {
		// The first statement has real grammar and parses fine standalone
		// (e.g. GRANT/REVOKE/COMMENT ON/SECURITY LABEL) — the batch's
		// overall parse failure comes from a later statement, so this is
		// not a parser-gap workaround case.
		return "", "", "", false
	}
	return first, rest, tag, true
}

// registerCompatNoopSchema applies CREATE SCHEMA's catalog+WAL side effect
// for the compatNoopCommandTag absorption path (a CREATE SCHEMA form the
// parser doesn't recognise, e.g. lacking IF NOT EXISTS support). Shared by
// both wire protocols — dispatchSimpleQueryViaExecutor and
// executeExtendedQueryViaExecutor's tryCompatNoopExtended — so the schema
// registers and persists identically regardless of which protocol the
// client used. M0119-0004-ACLHEAP follow-up (loop #86, item (3) of the
// loop #84 row).
func (s *Server) registerCompatNoopSchema(sql string, actingRole, sessionUser string) error {
	norm := normalizeCompatSQL(sql)
	// PG parity (parse_utilcmd.c transformCreateSchemaStmt/setSchemaName,
	// ERRCODE_INVALID_SCHEMA_DEFINITION): every schema-qualified object name
	// inside a `CREATE SCHEMA ... CREATE <element> ...` sub-command must
	// match the schema being created, or the WHOLE statement fails — nothing
	// (not even the schema itself) gets created. Checked before any catalog
	// mutation below. The goopg parser has no grammar for CREATE SCHEMA's
	// sub-command list at all (M0134-0009/M0134-0115), so this text-level
	// check is the only enforcement available; it does not attempt to
	// execute a schema-matching sub-command (REFACTOR-tier, deferred).
	if hdr, ok := parseCreateSchemaHeader(norm); ok && hdr.subCmd != "" {
		if targetSchema, hasTarget := createSchemaSubElementSchema(hdr.subCmd); hasTarget {
			owning := hdr.schemaName
			if owning == "" {
				owning = resolveSchemaAuthRole(hdr.authRole, actingRole, sessionUser)
			}
			if owning != "" && targetSchema != owning {
				return &schemaQualMismatchError{msg: fmt.Sprintf(
					"CREATE specifies a schema (%s) different from the one being created (%s)",
					targetSchema, owning)}
			}
		}
	}
	if s.cfg.Catalog == nil {
		return nil
	}
	schemaName := schemaNameFromCreate(norm)
	if schemaName == "" {
		if hdr, ok := parseCreateSchemaHeader(norm); ok {
			schemaName = hdr.schemaName
			if schemaName == "" {
				schemaName = resolveSchemaAuthRole(hdr.authRole, actingRole, sessionUser)
			}
		}
	}
	if schemaName == "" {
		return nil
	}
	s.cfg.Catalog.RegisterSchema(schemaName)
	// B1.1: persist via a real pg_namespace heap row (frozen-xid variant —
	// this branch handles CREATE SCHEMA forms the parser rejects and runs
	// without a live transaction). The parsed CompatNoopStmt path journals
	// the same row from execCompatNoop under the statement's xid. Replaces
	// the bespoke RecordKindCreateSchema record (M0110-0003, retired).
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok || s.cfg.Pool == nil {
		return nil
	}
	return executor.SyncCompatSchemaToCatalogHeap(s.cfg.Pool, im, s.currentDatabaseOidForCompat(), schemaName)
}

// currentDatabaseOidForCompat resolves the catalog-write database for the
// parse-recovery compat paths (no session in scope): DefaultDBOid routing,
// same as every catalog write from the postgres/default databases.
func (s *Server) currentDatabaseOidForCompat() uint32 {
	return catalog.DefaultDBOid
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

// schemaQualMismatchError signals CREATE SCHEMA's schema-qualification
// mismatch check (PG's setSchemaName, ERRCODE_INVALID_SCHEMA_DEFINITION /
// 42P15) — distinct from the generic errcodes.SystemError the rest of
// registerCompatNoopSchema's callers use, so compatNoopSchemaErrorCode can
// pick the right SQLSTATE for the wire error. M0134-0115.
type schemaQualMismatchError struct{ msg string }

func (e *schemaQualMismatchError) Error() string { return e.msg }

// compatNoopSchemaErrorCode picks the SQLSTATE for a registerCompatNoopSchema
// failure: schemaQualMismatchError maps to 42P15 (invalid_schema_definition,
// matching real PG's setSchemaName ereport), anything else keeps the prior
// generic errcodes.SystemError behavior.
func compatNoopSchemaErrorCode(err error) errcodes.Code {
	if _, ok := err.(*schemaQualMismatchError); ok {
		return errcodes.InvalidSchemaDefinition
	}
	return errcodes.SystemError
}

// createSchemaHeader is the parsed head of a `CREATE SCHEMA [name]
// [AUTHORIZATION role] [CREATE <element> ...]` statement the goopg parser
// does not recognise (no grammar for the sub-command list at all,
// M0134-0009/M0134-0115) — enough to run PG's schema-qualification-mismatch
// check without executing the sub-command.
type createSchemaHeader struct {
	schemaName string // "" when unspecified (AUTHORIZATION-only form: name = role)
	authRole   string // "" when no AUTHORIZATION clause is present
	subCmd     string // trailing "create ..." sub-element text, "" if none
}

// parseCreateSchemaHeader parses norm (already normalizeCompatSQL'd: single-
// spaced, lowercased outside literals) as a CREATE SCHEMA statement head.
// Mirrors gram.y's OptSchemaName/OptSchemaEltList shape closely enough for
// the single-sub-command forms create_schema.sql exercises.
func parseCreateSchemaHeader(norm string) (createSchemaHeader, bool) {
	if norm != "create schema" && !strings.HasPrefix(norm, "create schema ") {
		return createSchemaHeader{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(norm, "create schema"))
	var hdr createSchemaHeader
	if !strings.HasPrefix(rest, "authorization ") {
		ident, tail := consumeSQLIdent(rest)
		if ident != "" {
			hdr.schemaName = ident
			rest = tail
		}
	}
	if strings.HasPrefix(rest, "authorization ") {
		rest = strings.TrimSpace(rest[len("authorization "):])
		ident, tail := consumeSQLIdent(rest)
		hdr.authRole = ident
		rest = tail
	}
	if strings.HasPrefix(rest, "create ") {
		hdr.subCmd = rest
	}
	return hdr, true
}

// consumeSQLIdent reads one leading SQL identifier (quoted or unquoted) off
// s and returns it along with the trimmed remainder — extractFirstSQLIdent's
// sibling, but also reporting how much of s the identifier consumed so
// callers can keep parsing the rest of the statement.
func consumeSQLIdent(s string) (ident, rest string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if s[0] == '"' {
		end := strings.Index(s[1:], "\"")
		if end < 0 {
			return s[1:], ""
		}
		return s[1 : end+1], strings.TrimSpace(s[end+2:])
	}
	end := strings.IndexAny(s, " \t\n\r;,")
	if end < 0 {
		return s, ""
	}
	return s[:end], strings.TrimSpace(s[end:])
}

// resolveSchemaAuthRole resolves a CREATE SCHEMA AUTHORIZATION clause's role
// token to a concrete role name: CURRENT_ROLE/CURRENT_USER/USER resolve to
// the session's active role (actingRole, i.e. connTx.NonSuperuserRole — ""
// means no active SET ROLE, so fall back to the login/session user),
// SESSION_USER resolves to sessionUser directly, and anything else is
// already a literal role name.
func resolveSchemaAuthRole(authRole, actingRole, sessionUser string) string {
	switch authRole {
	case "current_role", "current_user", "user":
		if actingRole != "" {
			return actingRole
		}
		return sessionUser
	case "session_user":
		return sessionUser
	default:
		return authRole
	}
}

// createSchemaSubElementSchema extracts the schema-qualification of the
// object name targeted by a single embedded CREATE <element> sub-command
// (the SEQUENCE/TABLE/VIEW/INDEX-ON/TRIGGER-ON forms create_schema.sql
// exercises — parse_utilcmd.c's transformCreateSchemaStmtElements checks
// every element type in the sub-command list, but goopg parses none of
// them, so only the single-element case that reaches here is covered).
// hasTarget is false when the referenced name carries no explicit schema
// qualification (unqualified names default silently to the enclosing
// schema in real PG — never a mismatch) or the sub-command shape isn't
// recognised.
func createSchemaSubElementSchema(subCmd string) (schema string, hasTarget bool) {
	fields := strings.Fields(subCmd)
	if len(fields) < 2 || fields[0] != "create" {
		return "", false
	}
	var target string
	switch fields[1] {
	case "sequence", "table", "view":
		idx := 2
		if idx+2 < len(fields) && fields[idx] == "if" && fields[idx+1] == "not" && fields[idx+2] == "exists" {
			idx += 3
		}
		if idx < len(fields) {
			target = fields[idx]
		}
	case "index", "trigger":
		for i := 2; i < len(fields); i++ {
			if fields[i] == "on" && i+1 < len(fields) {
				target = fields[i+1]
				break
			}
		}
	default:
		return "", false
	}
	dot := strings.Index(target, ".")
	if dot <= 0 {
		return "", false
	}
	return target[:dot], true
}

func normalizeCompatSQL(sql string) string {
	// Comments are WHITESPACE to PG's lexer, not text: scan.l:213-215 defines
	// `comment ("--"{non_newline}*)` and `whitespace ({space}+|{comment})`,
	// and the `{whitespace}` rule (scan.l:443) emits no token at all — so
	// `/* c */ CREATE ROLE x` reaches the grammar as `CREATE ROLE x`. goopg's
	// compat tier classifies those statement classes the parser deliberately
	// does not carry (role DDL, database DDL, the CREATE SCHEMA header — see
	// the goyacc playbook §12's hand-written-scanner list) by prefix-matching
	// this normalized text, and every one of those matches failed the moment a
	// comment preceded the statement, because the comment survived
	// normalization and no `create role `/`create database ` prefix was left.
	// A leading comment is the normal shape of real SQL scripts, so CREATE /
	// ALTER / DROP ROLE|USER|GROUP and CREATE / ALTER / DROP DATABASE were all
	// unreachable from any commented script (M0134-0159; found via
	// regproc.sql's `/* If objects exist, return oids */\nCREATE ROLE …`).
	// Stripping first also lets the trailing-semicolon trim below see a `;`
	// that a trailing comment would otherwise hide.
	s := stripSQLComments(sql)
	s = strings.TrimSpace(s)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	// Lowercase keywords/identifiers but preserve string literal case.
	// Lowercasing string literals would cause 'A' and 'a' to map to the
	// same plan-cache key, returning the wrong cached plan. M0097-0003.
	return normalizeSQLPreservingLiterals(s)
}

// stripSQLComments replaces every SQL comment with a single space, the way
// PG's lexer does (scan.l:213-215 folds `--…` and `/*…*/` into {whitespace},
// whose rule body is `/* ignore */`). Everything else is copied byte for byte:
// this is a lexical pre-pass for normalizeCompatSQL's prefix classification and
// the plan-cache key, NOT a rewriter.
//
// Comment introducers inside a literal are not comments, so the scan skips the
// same four literal forms firstTopLevelSemicolon (role_ddl.go) skips —
// single-quoted strings, double-quoted identifiers, and dollar-quoted strings —
// plus the E'' escape-string form, where a backslash escapes the next byte and
// `E'a\'-- still in the literal'` must not be cut in half.
//
// Block comments NEST in PostgreSQL (scan.l's <xc> state carries an `xcdepth`
// counter, scan.l:455-467), unlike the deliberately simplified non-nesting scan
// in firstTopLevelSemicolon, so the depth is tracked here.
//
// An UNTERMINATED comment or literal is copied through verbatim rather than
// swallowed: this function must never make a malformed statement look like a
// well-formed one to the prefix matcher — the parser is what reports the
// syntax error.
func stripSQLComments(sql string) string {
	if !strings.Contains(sql, "--") && !strings.Contains(sql, "/*") {
		return sql // overwhelmingly the common case; no copy
	}
	var b strings.Builder
	b.Grow(len(sql))
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '\'':
			// A leading E/e marks an escape string, in which a backslash
			// escapes the following byte (including a quote). Plain and
			// standard-conforming literals only escape a quote by doubling it.
			esc := i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') &&
				(i == 1 || !isIdentByte(sql[i-2]))
			j := i + 1
			for j < n {
				if esc && sql[j] == '\\' && j+1 < n {
					j += 2
					continue
				}
				if sql[j] == '\'' {
					if j+1 < n && sql[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(sql[i:min(j, n)])
			i = j
		case c == '"':
			j := i + 1
			for j < n {
				if sql[j] == '"' {
					if j+1 < n && sql[j+1] == '"' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(sql[i:min(j, n)])
			i = j
		case c == '$':
			tag, after, isDollar := scanDollarTag(sql, i)
			if !isDollar {
				b.WriteByte(c)
				i++
				continue
			}
			end := after
			if rel := strings.Index(sql[after:], tag); rel >= 0 {
				end = after + rel + len(tag)
			} else {
				end = n // unterminated: copy the remainder verbatim
			}
			b.WriteString(sql[i:end])
			i = end
		case c == '-' && i+1 < n && sql[i+1] == '-':
			// Line comment: runs to the end of the line. The terminating
			// newline is itself whitespace, so emitting one space for the
			// whole comment loses nothing (scan.l never distinguishes them).
			for i < n && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < n && sql[i+1] == '*':
			depth := 1
			j := i + 2
			for j < n && depth > 0 {
				if sql[j] == '/' && j+1 < n && sql[j+1] == '*' {
					depth++
					j += 2
					continue
				}
				if sql[j] == '*' && j+1 < n && sql[j+1] == '/' {
					depth--
					j += 2
					continue
				}
				j++
			}
			if depth > 0 {
				// Unterminated /* — PG raises "unterminated /* comment"
				// (scan.l:483). Copy it through so the parser sees it and
				// reports that error itself.
				b.WriteString(sql[i:])
				return b.String()
			}
			b.WriteByte(' ')
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// isIdentByte reports whether c can appear in an SQL identifier — used to tell
// the escape-string prefix in `E'…'` from the tail of an identifier that merely
// ends in "e" (`table'x'` is not an escape string).
func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// planCacheKey builds the cross-session plan cache key: the planner-context
// fingerprint plus normalizeCompatSQL's text form, prefixed with the querying
// connection's effective table/index namespace oid
// (catalog.NamespaceDBOid(connDBOid)). Without the oid prefix,
// a plan cached while resolving names against one database's namespace could
// be replayed for a connection reading a different one — the cache is a
// single server-wide map shared by every connection (M0122-0007 slice 4c;
// see plancache.go's doc comment). Two connections whose NamespaceDBOid
// agrees (e.g. both "postgres") AND whose planner fingerprints agree
// intentionally still share one entry.
func planCacheKey(sql string, connDBOid uint32, fingerprint string) string {
	return strconv.FormatUint(uint64(catalog.NamespaceDBOid(connDBOid)), 10) + "\x00" + fingerprint + "\x00" + normalizeCompatSQL(sql)
}

// normalizeSQLPreservingLiterals lowercases SQL outside string literals and
// quoted identifiers, and collapses whitespace. Literal contents are preserved
// verbatim so that INSERT ('A') and INSERT ('a') get distinct cache keys, and
// quoted identifiers likewise: PG's lexer downcases only UNquoted identifiers
// (scan.l's {identifier} rule calls downcase_truncate_identifier, while
// <xd> yields the delimited text as-is), so "Foo" and "foo" are two different
// tables. Folding them together handed `SELECT * FROM "foo"` the plan cached
// for `SELECT * FROM "Foo"` — a wrong-results bug, not just a cache-key nicety.
func normalizeSQLPreservingLiterals(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSingleQuote := false
	inDoubleQuote := false
	prevWasSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inDoubleQuote {
			// Inside a quoted identifier — preserve case exactly.
			b.WriteByte(c)
			if c == '"' {
				// A doubled double quote is an escaped quote, not the end.
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
				} else {
					inDoubleQuote = false
				}
			}
			prevWasSpace = false
			continue
		}
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
		if c == '"' {
			inDoubleQuote = true
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
	s := strings.ToLower(strings.TrimSpace(t))
	if s == "" {
		return false
	}
	if strings.Count(s, "(") != strings.Count(s, ")") {
		return false
	}

	// Strip a typmod: a balanced "( ... )" group, wherever it appears (PG
	// allows it inside a multi-word name, e.g. "timestamp(3) with time
	// zone"). Re-collapse surrounding whitespace to a single space.
	for {
		open := strings.IndexByte(s, '(')
		if open == -1 {
			break
		}
		closeRel := strings.IndexByte(s[open:], ')')
		if closeRel == -1 {
			// Unbalanced typmod; reject rather than silently accepting.
			return false
		}
		close := open + closeRel
		s = strings.Join(strings.Fields(s[:open]+" "+s[close+1:]), " ")
	}

	// Strip any number of trailing array-dimension suffixes: "[]" or "[N]".
	// PG treats all array dimensionalities of a base type as the same type.
	for {
		s = strings.TrimSpace(s)
		if !strings.HasSuffix(s, "]") {
			break
		}
		open := strings.LastIndexByte(s, '[')
		if open == -1 {
			return false
		}
		inner := s[open+1 : len(s)-1]
		if inner != "" {
			if _, err := strconv.Atoi(inner); err != nil {
				return false
			}
		}
		s = s[:open]
	}
	s = strings.TrimSpace(s)

	switch s {
	case "int", "int2", "int4", "int8", "integer", "smallint", "bigint",
		"float", "float4", "float8", "real", "double", "double precision",
		"bool", "boolean",
		"text", "varchar", "char", "bpchar", "name",
		"character", "character varying",
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
		"path", "box", "circle", "line", "lseg", "polygon", "point",
		"regclass", "regproc", "regprocedure", "regoper", "regoperator",
		"regtype", "regrole", "regnamespace", "regconfig", "regdictionary",
		"regcollation",
		"bit", "bit varying", "varbit",
		"inet", "cidr", "macaddr", "macaddr8",
		"money", "xml", "tsvector", "tsquery", "jsonpath",
		"int2vector", "oidvector", "pg_snapshot",
		`"char"`:
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
	// Defensive clamp. Not every statement node carries a real position: some
	// node kinds still report Pos() == 0 (see the deferral-ledger row
	// 2026-08-29 "parser Pos() regression"), and a following statement whose
	// Pos is 0 or below this statement's start otherwise produced an inverted
	// slice — `sql[28:0]` — which panicked the whole backend goroutine and
	// dropped the client connection on any multi-statement batch containing
	// such a node. PostgreSQL cannot crash here (`pg_prepared_statements`
	// stores the raw text captured at PREPARE time), so degrade to "the rest
	// of the batch" rather than take the process down. M0134-0157.
	if start < 0 || start > len(sql) {
		start = 0
	}
	if end < start {
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

// databaseOidResolver is implemented by catalogs that can resolve a database
// name to its real, physical pg_database.oid (catalog.InMemory). M0122-0007
// physical-storage-isolation slice 4a.
type databaseOidResolver interface {
	ResolveDatabaseOid(name string) (uint32, bool)
}

// wireExtensionRows installs the per-database pg_extension view on ectx so an
// extension installed in one database is invisible in another (PostgreSQL's
// pg_extension is per-database; goopg shares one in-memory catalog). Also
// resolves and stamps ectx.CurrentDatabaseOid (M0122-0007 slice 4a plumbing —
// not yet consumed by any lookup site). Used by both the simple- and
// extended-query executor paths. M0110-0003 (gap #7c).
func (s *Server) wireExtensionRows(ectx *executor.Context, dbName string) {
	ectx.CurrentDatabase = dbName
	if el, ok := s.cfg.Catalog.(extensionLister); ok {
		ectx.ExtensionRows = func() [][]string { return el.ExtensionRowsForDB(dbName) }
	}
	if dr, ok := s.cfg.Catalog.(databaseOidResolver); ok {
		if oid, ok := dr.ResolveDatabaseOid(dbName); ok {
			ectx.CurrentDatabaseOid = oid
		}
	}
	// pg_class must reflect the connecting database's own tables/indexes, not
	// always DefaultDBOid's (M0122-0007 4e). Captures ectx.CurrentDatabaseOid
	// by reference so this stays correct even though it's stamped above in
	// this same call (ResolveDatabaseOid failing leaves it at its prior/zero
	// value, which NamespaceDBOid maps back to DefaultDBOid — unchanged
	// legacy behavior for embedded/test contexts with no real connection).
	if pc, ok := s.cfg.Catalog.(pgClassRowLister); ok {
		ectx.PgClassRows = func() [][]string {
			return pc.PGClassRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_indexes / pg_tables must likewise reflect the connecting database's
	// own indexes/tables, not always DefaultDBOid's. Mirrors the pg_class
	// wiring above. M0122-0007 4e follow-up 24.
	if pi, ok := s.cfg.Catalog.(pgIndexesRowLister); ok {
		ectx.PgIndexesRows = func() [][]string {
			return pi.PGIndexesRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	if pt, ok := s.cfg.Catalog.(pgTablesRowLister); ok {
		ectx.PgTablesRows = func() [][]string {
			return pt.PGTablesRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_constraint must likewise reflect the connecting database's own
	// tables'/indexes' constraints, not always DefaultDBOid's. Mirrors the
	// pg_class wiring above. M0122-0007 4e follow-up 25.
	if pc, ok := s.cfg.Catalog.(pgConstraintRowLister); ok {
		ectx.PgConstraintRows = func() [][]string {
			return pc.PGConstraintRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_index must likewise reflect the connecting database's own indexes,
	// not always DefaultDBOid's. Mirrors the pg_constraint wiring above.
	// M0122-0007 4e follow-up 26.
	if pix, ok := s.cfg.Catalog.(pgIndexRowLister); ok {
		ectx.PgIndexRows = func() [][]string {
			return pix.PGIndexRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_attrdef / pg_depend must likewise reflect the connecting database's
	// own column defaults / dependency rows, not always DefaultDBOid's.
	// Mirrors the pg_index wiring above. Both close over the SAME
	// NamespaceDBOid(ectx.CurrentDatabaseOid) call so pg_depend's
	// attrdef→sequence rows stay in oid-numbering lockstep with pg_attrdef's
	// own rows (PGAttrdefRowsForDBOid's doc comment). M0122-0007 4e
	// follow-up 27.
	if pad, ok := s.cfg.Catalog.(pgAttrdefRowLister); ok {
		ectx.PgAttrdefRows = func() [][]string {
			return pad.PGAttrdefRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	if pd, ok := s.cfg.Catalog.(pgDependRowLister); ok {
		ectx.PgDependRows = func() [][]string {
			return pd.PGDependRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_inherits must likewise reflect the connecting database's own
	// inheritance/partition parent-child rows, not always DefaultDBOid's.
	// Mirrors the pg_depend wiring above. M0122-0007 4e follow-up 28.
	if pih, ok := s.cfg.Catalog.(pgInheritsRowLister); ok {
		ectx.PgInheritsRows = func() [][]string {
			return pih.PGInheritsRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_policy must likewise reflect the connecting database's own tables'
	// row-level-security policies, not always DefaultDBOid's. Mirrors the
	// pg_inherits wiring above. M0122-0007 4e follow-up 29.
	if pp, ok := s.cfg.Catalog.(pgPolicyRowLister); ok {
		ectx.PgPolicyRows = func() [][]string {
			return pp.PGPolicyRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_trigger must likewise reflect the connecting database's own tables'
	// triggers, not always DefaultDBOid's. Mirrors the pg_policy wiring
	// above. M0122-0007 4e follow-up 30.
	if pt, ok := s.cfg.Catalog.(pgTriggerRowLister); ok {
		ectx.PgTriggerRows = func() [][]string {
			return pt.PGTriggerRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_rewrite must likewise reflect the connecting database's own tables'
	// CREATE RULE DO-NOTHING rules, not always DefaultDBOid's. Mirrors the
	// pg_trigger wiring above. M0122-0007 4e follow-up 31.
	if pr, ok := s.cfg.Catalog.(pgRewriteRowLister); ok {
		ectx.PgRewriteRows = func() [][]string {
			return pr.PGRewriteRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_foreign_table must likewise reflect the connecting database's own
	// foreign tables, not always DefaultDBOid's. Mirrors the pg_rewrite
	// wiring above. M0122-0007 4e follow-up 32.
	if pf, ok := s.cfg.Catalog.(pgForeignTableRowLister); ok {
		ectx.PgForeignTableRows = func() [][]string {
			return pf.PGForeignTableRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_sequence must likewise reflect the connecting database's own
	// sequences, not always DefaultDBOid's. Mirrors the pg_foreign_table
	// wiring above. M0122-0007 4e follow-up 34.
	if ps, ok := s.cfg.Catalog.(pgSequenceRowLister); ok {
		ectx.PgSequenceRows = func() [][]string {
			return ps.PGSequenceRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_foreign_server must likewise reflect the connecting database's own
	// CREATE SERVER'd servers, not always DefaultDBOid's. Mirrors the
	// pg_sequence wiring above. M0122-0007 4e follow-up 36.
	if pfs, ok := s.cfg.Catalog.(pgForeignServerRowLister); ok {
		ectx.PgForeignServerRows = func() [][]string {
			return pfs.PGForeignServerRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_user_mappings must likewise reflect the connecting database's own
	// CREATE USER MAPPING'd mappings, not always DefaultDBOid's. Mirrors the
	// pg_foreign_server wiring above. M0122-0007 4e follow-up 37.
	if pum, ok := s.cfg.Catalog.(pgUserMappingsRowLister); ok {
		ectx.PgUserMappingsRows = func() [][]string {
			return pum.PGUserMappingsRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_collation must likewise reflect the connecting database's own
	// CREATE COLLATION'd collations, not always DefaultDBOid's. Mirrors the
	// pg_user_mappings wiring above. M0122-0007 4e follow-up (DU-002
	// round-trip probe unblock).
	if pc, ok := s.cfg.Catalog.(pgCollationRowLister); ok {
		ectx.PgCollationRows = func() [][]string {
			return pc.PGCollationRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_conversion must likewise reflect the connecting database's own
	// CREATE [DEFAULT] CONVERSION'd conversions, not always DefaultDBOid's.
	// Mirrors the pg_collation wiring above. M0122-0007 4e follow-up
	// (DU-002 round-trip probe unblock).
	if pcv, ok := s.cfg.Catalog.(pgConversionRowLister); ok {
		ectx.PgConversionRows = func() [][]string {
			return pcv.PGConversionRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_ts_dict must likewise reflect the connecting database's own CREATE
	// TEXT SEARCH DICTIONARY'd dictionaries, not always DefaultDBOid's.
	// Mirrors the pg_conversion wiring above. M0122-0007 4e follow-up
	// (DU-002 round-trip probe unblock).
	if ptd, ok := s.cfg.Catalog.(pgTSDictRowLister); ok {
		ectx.PgTSDictRows = func() [][]string {
			return ptd.PGTSDictRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_ts_config must likewise reflect the connecting database's own
	// CREATE TEXT SEARCH CONFIGURATION'd configurations, not always
	// DefaultDBOid's. Mirrors the pg_ts_dict wiring above. M0122-0007
	// 4e follow-up (DU-002 round-trip probe unblock).
	if ptc, ok := s.cfg.Catalog.(pgTSConfigRowLister); ok {
		ectx.PgTSConfigRows = func() [][]string {
			return ptc.PGTSConfigRowsForDBOid(catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_publication must likewise reflect the connecting database's own
	// CREATE PUBLICATION'd publications, not always DefaultDBOid's.
	// Mirrors the pg_ts_config wiring above. PubSub is separate from
	// catalog.InMemory, so we wire it directly through s.cfg.PubSub.
	// M0119-0004 (DU-002 per-DB publication scoping).
	if s.cfg.PubSub != nil {
		ectx.PgPublicationRows = func() [][]string {
			return publicationRowsForDBOid(s.cfg.PubSub, catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
	// pg_subscription must likewise reflect the connecting database's own
	// CREATE SUBSCRIPTION'd subscriptions, not always DefaultDBOid's.
	// Mirrors the pg_publication wiring above. M0119-0004 (DU-002 per-DB
	// subscription scoping).
	if s.cfg.PubSub != nil {
		ectx.PgSubscriptionRows = func() [][]string {
			return subscriptionRowsForDBOid(s.cfg.PubSub, catalog.NamespaceDBOid(ectx.CurrentDatabaseOid))
		}
	}
}

// pgClassRowLister is implemented by catalog.InMemory to expose a
// per-database pg_class row-set (M0122-0007 4e), mirroring extensionLister
// above.
type pgClassRowLister interface {
	PGClassRowsForDBOid(dbOid uint32) [][]string
}

// pgIndexesRowLister / pgTablesRowLister are implemented by catalog.InMemory
// to expose per-database pg_indexes / pg_tables row-sets, mirroring
// pgClassRowLister above. M0122-0007 4e follow-up 24.
type pgIndexesRowLister interface {
	PGIndexesRowsForDBOid(dbOid uint32) [][]string
}

type pgTablesRowLister interface {
	PGTablesRowsForDBOid(dbOid uint32) [][]string
}

// pgConstraintRowLister is implemented by catalog.InMemory to expose a
// per-database pg_constraint row-set, mirroring pgClassRowLister above.
// M0122-0007 4e follow-up 25.
type pgConstraintRowLister interface {
	PGConstraintRowsForDBOid(dbOid uint32) [][]string
}

// pgIndexRowLister is implemented by catalog.InMemory to expose a
// per-database pg_index row-set, mirroring pgConstraintRowLister above.
// M0122-0007 4e follow-up 26.
type pgIndexRowLister interface {
	PGIndexRowsForDBOid(dbOid uint32) [][]string
}

// pgAttrdefRowLister / pgDependRowLister are implemented by catalog.InMemory
// to expose per-database pg_attrdef / pg_depend row-sets, mirroring
// pgIndexRowLister above. M0122-0007 4e follow-up 27.
type pgAttrdefRowLister interface {
	PGAttrdefRowsForDBOid(dbOid uint32) [][]string
}

type pgDependRowLister interface {
	PGDependRowsForDBOid(dbOid uint32) [][]string
}

// pgInheritsRowLister is implemented by catalog.InMemory to expose a
// per-database pg_inherits row-set, mirroring pgDependRowLister above.
// M0122-0007 4e follow-up 28.
type pgInheritsRowLister interface {
	PGInheritsRowsForDBOid(dbOid uint32) [][]string
}

// pgPolicyRowLister is implemented by catalog.InMemory to expose a
// per-database pg_policy row-set, mirroring pgInheritsRowLister above.
// M0122-0007 4e follow-up 29.
type pgPolicyRowLister interface {
	PGPolicyRowsForDBOid(dbOid uint32) [][]string
}

// pgTriggerRowLister is implemented by catalog.InMemory to expose a
// per-database pg_trigger row-set, mirroring pgPolicyRowLister above.
// M0122-0007 4e follow-up 30.
type pgTriggerRowLister interface {
	PGTriggerRowsForDBOid(dbOid uint32) [][]string
}

// pgRewriteRowLister is implemented by catalog.InMemory to expose a
// per-database pg_rewrite row-set, mirroring pgTriggerRowLister above.
// M0122-0007 4e follow-up 31.
type pgRewriteRowLister interface {
	PGRewriteRowsForDBOid(dbOid uint32) [][]string
}

// pgForeignTableRowLister is implemented by catalog.InMemory to expose a
// per-database pg_foreign_table row-set, mirroring pgRewriteRowLister above.
// M0122-0007 4e follow-up 32.
type pgForeignTableRowLister interface {
	PGForeignTableRowsForDBOid(dbOid uint32) [][]string
}

// pgSequenceRowLister is implemented by catalog.InMemory to expose a
// per-database pg_sequence row-set, mirroring pgForeignTableRowLister above.
// M0122-0007 4e follow-up 34.
type pgSequenceRowLister interface {
	PGSequenceRowsForDBOid(dbOid uint32) [][]string
}

// pgForeignServerRowLister is implemented by catalog.InMemory to expose a
// per-database pg_foreign_server row-set, mirroring pgSequenceRowLister
// above. M0122-0007 4e follow-up 36.
type pgForeignServerRowLister interface {
	PGForeignServerRowsForDBOid(dbOid uint32) [][]string
}

// pgUserMappingsRowLister is implemented by catalog.InMemory to expose a
// per-database pg_user_mappings row-set, mirroring pgForeignServerRowLister
// above. M0122-0007 4e follow-up 37.
type pgUserMappingsRowLister interface {
	PGUserMappingsRowsForDBOid(dbOid uint32) [][]string
}

// pgCollationRowLister is implemented by catalog.InMemory to expose a
// per-database pg_collation row-set, mirroring pgUserMappingsRowLister
// above. M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
type pgCollationRowLister interface {
	PGCollationRowsForDBOid(dbOid uint32) [][]string
}

// pgConversionRowLister is implemented by catalog.InMemory to expose a
// per-database pg_conversion row-set, mirroring pgCollationRowLister above.
// M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
type pgConversionRowLister interface {
	PGConversionRowsForDBOid(dbOid uint32) [][]string
}

// pgTSDictRowLister is implemented by catalog.InMemory to expose a
// per-database pg_ts_dict row-set, mirroring pgConversionRowLister above.
// M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
type pgTSDictRowLister interface {
	PGTSDictRowsForDBOid(dbOid uint32) [][]string
}

// pgTSConfigRowLister is implemented by catalog.InMemory to expose a
// per-database pg_ts_config row-set, mirroring pgTSDictRowLister above.
// M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
type pgTSConfigRowLister interface {
	PGTSConfigRowsForDBOid(dbOid uint32) [][]string
}

// publicationRowsForDBOid builds the pg_publication VirtualRows for dbOid
// from the PubSub registry, matching the wireExtensionRows convention of
// filtering catalog views to the connecting database. M0119-0004 (DU-002
// per-DB publication scoping).
func publicationRowsForDBOid(ps *catalog.PubSub, dbOid uint32) [][]string {
	pubs := ps.PublicationsForDBOid(dbOid)
	if len(pubs) == 0 {
		return nil
	}
	out := make([][]string, 0, len(pubs))
	for _, pub := range pubs {
		out = append(out, []string{
			fmt.Sprintf("%d", pub.OID),
			pub.Name,
			fmt.Sprintf("%d", pub.Owner),
			boolToText(pub.AllTables),
			boolToText(pub.PublishInsert),
			boolToText(pub.PublishUpdate),
			boolToText(pub.PublishDelete),
			"f", // pubtruncate
			"f", // pubviaroot
			"n", // pubgencols
		})
	}
	return out
}

// subscriptionRowsForDBOid builds the pg_subscription VirtualRows for dbOid
// from the PubSub registry, matching the publicationRowsForDBOid convention.
// M0119-0004 (DU-002 per-DB subscription scoping).
func subscriptionRowsForDBOid(ps *catalog.PubSub, dbOid uint32) [][]string {
	subs := ps.SubscriptionsForDBOid(dbOid)
	if len(subs) == 0 {
		return nil
	}
	out := make([][]string, 0, len(subs))
	for _, sub := range subs {
		out = append(out, []string{
			fmt.Sprintf("%d", sub.OID),
			fmt.Sprintf("%d", catalog.FirstUserOID), // subdbid — matches pg_database.oid
			sub.Name,
			fmt.Sprintf("%d", sub.Owner), // subowner
			boolToText(sub.Enabled),
			"f",   // subbinary
			"f",   // substream
			"d",   // subtwophasestate disabled
			"f",   // subdisableonerr
			"t",   // subpasswordrequired (upstream default)
			"f",   // subrunasowner (upstream default)
			"any", // suborigin (LOGICALREP_ORIGIN_ANY upstream default)
			"f",   // subfailover (upstream default)
			sub.Conninfo,
			sub.SlotName,
			"local", // subsynccommit
			formatStringList(sub.Publications),
		})
	}
	return out
}

// boolToText converts a bool to "t"/"f" for pg_catalog VirtualRows
// (mirrors replicate_views.go:boolText). M0119-0004.
func boolToText(b bool) string {
	if b {
		return "t"
	}
	return "f"
}

// formatStringList formats a []string as a PG text-array literal
// (mirrors initdb/replication_views.go:formatStringList). M0119-0004.
func formatStringList(xs []string) string {
	if len(xs) == 0 {
		return "{}"
	}
	out := "{"
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out + "}"
}

func undoEnumDDLForRollback(connTx *connTxState, cat catalog.Catalog, dbOid uint32) {
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
			inm.RemoveEnumValue(typeName, label, dbOid)
		}
	}
	// Step 2: Undo renames in reverse order; track name changes in created-set.
	created := make(map[string]bool, len(connTx.PendingCreatedEnums))
	for k, v := range connTx.PendingCreatedEnums {
		created[k] = v
	}
	for i := len(connTx.PendingEnumRenames) - 1; i >= 0; i-- {
		r := connTx.PendingEnumRenames[i]
		_ = inm.RenameEnum(r.NewName, r.OldName, dbOid)
		if created[r.NewName] {
			delete(created, r.NewName)
			created[r.OldName] = true
		}
	}
	// Step 3: Drop types created in this transaction (now at original names).
	for name := range created {
		_ = inm.DropEnum(name, false, dbOid)
	}
	// Step 4: Drop composite types created via CREATE TYPE … AS (...) in this
	// transaction.  Mirrors undoEnumDDLFromContext step 4.  DU-002 slice 244.
	for name := range connTx.PendingCreatedComposites {
		_ = inm.DropCompositeType(name, dbOid)
	}
	// Step 5: Drop range types created via CREATE TYPE … AS RANGE in this
	// transaction.  Mirrors undoEnumDDLFromContext step 5.  M0122-0007 4e
	// follow-up (fifth loop).
	for name := range connTx.PendingCreatedRangeTypes {
		_ = inm.DropRangeType(name, dbOid)
	}
}

// autoCommitPtr, if non-nil, is set to false when a BEGIN starts an
// explicit transaction (telling the caller not to auto-commit).
// cachedNode, when non-nil, is a pre-validated plan from the cross-session
// plan cache — planner.Plan is skipped. M0098-0005.
func (s *Server) executeOneSimpleStmt(w *libpq.FrameWriter, ctx *executor.Context, stmt parser.Stmt, connTx *connTxState, autoCommitPtr *bool, cachedNode ...optimizer.Node) error {
	// M0127-P3.3: unlink whatever spill files this statement still owns,
	// success or failure. Operators unlink eagerly when they can, but an
	// Open that errors after a build spilled — or a cancelled query — never
	// reaches the Close that would do it. Per STATEMENT rather than per
	// dispatch: a batch of 100 statements must not accumulate 100
	// statements' worth of temp files. Cursors are safe because they
	// materialise their rows (cursorEntry.Rows) instead of holding an open
	// operator across statements.
	defer ctx.ReleaseSpillFiles()
	// LISTEN / NOTIFY / UNLISTEN are handled at the server layer: the
	// notification hub is cross-session server state, not an executor operator,
	// and NOTIFY buffers until the transaction commits. Handle before planning
	// (the planner has no node for them). M0118-0009 (async-notify).
	if handled, err := s.execNotifyStmt(w, stmt, connTx); handled {
		return err
	}
	// PREPARE TRANSACTION / COMMIT PREPARED / ROLLBACK PREPARED — two-phase
	// commit. Handled at the server layer (the planner has no node for them);
	// COMMIT/ROLLBACK PREPARED re-enter this function with a synthetic
	// COMMIT/ROLLBACK so the canonical finalisation path runs. M0118-0009.
	if handled, err := s.execTwoPhaseStmt(w, ctx, stmt, connTx, autoCommitPtr); handled {
		return err
	}
	// M0134-0074: resolve WHERE CURRENT OF at dispatch time. The cursor's
	// current tid is substituted as a concrete `ctid = '(block,off)'` equality
	// that flows through the existing ctid-string-equality path — no
	// optimizer/executor change. The tid is statement-scoped, so CURRENT OF
	// statements bypass the plan cache (see isCurrentOfDML at the cache site).
	// EXPLAIN (ANALYZE ...) wraps the DML in an ExplainStmt — tidscan.sql runs
	// every CURRENT OF statement under EXPLAIN ANALYZE — and optimizer.Plan
	// recursively plans the ExplainStmt's Inner (planner.go:260), so resolving
	// the inner DML's Where before the Plan call below covers both forms.
	if err := s.resolveCurrentOfInStmt(connTx, stmt); err != nil {
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
	}
	var node optimizer.Node
	if len(cachedNode) > 0 && cachedNode[0] != nil {
		node = cachedNode[0]
	} else {
		var err error
		node, err = optimizer.PlanWithSettings(stmt, ctxPlanCatalog(ctx, s.cfg.Catalog), ctxPlannerSettings(ctx))
		if err != nil {
			code, msg := planErrorFields(err)
			return s.writeQueryError(w, code, msg, planErrorHintFields(err)...)
		}
		// The parallel post-pass belongs to EVERY plan, not just cached ones.
		//
		// applyParallelPostPass used to run only inside the dispatch loop's
		// plan-cache block, so a statement that bypassed the cache was never
		// considered for a Gather and ran strictly serially. Every cache-bypass
		// reason triggered it: SET enable_seqscan=off, WHERE CURRENT OF, a
		// pending partition detach — and, once take2 P2-02b made a conf-file
		// work_mem count as a session input, the whole TPC-H bench. That last
		// one is how it surfaced: Q9 went 15.8s -> 69.3s with an unchanged
		// plan shape and an unchanged work_mem, because the Gather over four
		// workers had silently disappeared.
		//
		// Wrapping here rather than at the cache site keeps the invariant the
		// cache comment already states — the CACHE holds the serial plan and
		// the Gather is chosen per statement from this session's GUCs — while
		// extending it to the statements that never reach the cache.
		node = ctxApplyParallelPostPass(node, ctx)
		// Note: plan cache storage happens at the dispatch level (caller
		// stores if cacheKey was computed). This function only executes.
		//
		// P6: this fallback plans for statements the caller's cache block
		// skipped (multi-statement queries, NOTIFY/2PC, pending DDL). It
		// needs the parallel post-pass too — the caller only applies it to
		// the cached path, and a Gather that appears on one entry point but
		// not another is exactly the kind of inconsistency that makes a
		// feature look intermittent.
		if sess, _ := ctx.AdvisorySessionIdentity.(*misc.SessionRegistry); sess != nil {
			node = applyParallelPostPass(node, sess, ctx)
		}
	}
	// Transaction verbs: BEGIN/COMMIT/ROLLBACK require per-connection
	// explicit transaction management (M0096-0005). The state machine itself
	// lives in applyTransactionVerb (txn_verb.go) so the extended query
	// protocol drives the SAME one rather than a re-derived copy (M0132-S2);
	// this arm only renders the outcome as simple-query wire frames.
	if txNode, ok := node.(*optimizer.Transaction); ok {
		if out := s.applyTransactionVerb(ctx, connTx, txNode, autoCommitPtr); out.Handled {
			if out.Err != nil {
				return s.writeQueryError(w, out.Err.Code, out.Err.Msg, out.Err.Fields...)
			}
			if out.Warn {
				_ = w.WriteNoticeResponse(noTransactionInProgressNotice())
			}
			return w.WriteCommandComplete(out.Tag)
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
		if errors.Is(err, executor.ErrSelfTerminate) {
			return err
		}
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
		fields := make([]libpq.FieldDescription, len(schema))
		for i, sc := range schema {
			oid := typeOIDFor(sc.Type)
			// Array column (e.g. `p int4[]`): advertise the array pg_type OID
			// (_int4 = 1007) so the client parses the "{1,2}" text as an array
			// rather than a scalar int4. M0118-0002.
			if sc.Type.IsArray {
				oid = catalog.ArrayOIDForBase(oid)
			}
			fields[i] = libpq.FieldDescription{
				Name:         sc.Name,
				TypeOID:      oid,
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
			if errors.Is(err, executor.ErrSelfTerminate) {
				return err
			}
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
					// Per-arg-type visibility for the regprocedure arglist (73rd
					// slice): the same RegObjectSchemaVisible the COPY path uses,
					// so SELECT and COPY cannot drift on arg-type qualification.
					valueBuf = s.appendTypedCellText(valueBuf, d, schema[i].Type, ctx.GetSetting,
						func(s string) bool { return executor.RegObjectSchemaVisible(ctx, s) })
				} else {
					valueBuf = d.AppendValueText(valueBuf)
				}
				cell := valueBuf[start:len(valueBuf)]
				if cell == nil {
					// Non-null Datum rendered to zero bytes (empty
					// string/bytea) on a still-nil scratch buffer: slicing
					// a nil []byte with [0:0] yields nil, indistinguishable
					// from the d.IsNull() sentinel above. Coerce to a
					// non-nil zero-length slice so the DataRow encoder
					// emits length 0, not the NULL length -1
					// (M0134-datarow-empty-string).
					cell = []byte{}
				}
				cells = append(cells, cell)
			}
			cells = s.maybeConvertCellsForClientEncoding(cells, ctx.GetSetting)
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
		if nerr := w.WriteNoticeResponse([]libpq.ErrorField{
			{Code: libpq.FieldSeverity, Value: "NOTICE"},
			{Code: libpq.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: libpq.FieldSQLState, Value: "00000"},
			{Code: libpq.FieldMessage, Value: msg},
		}); nerr != nil {
			return nerr
		}
	}
	// Emit NOTICE+DETAIL messages (e.g. DROP CASCADE cascade list). M0097-0020.
	for _, n := range ctx.TakeNoticesWithDetail() {
		fields := []libpq.ErrorField{
			{Code: libpq.FieldSeverity, Value: "NOTICE"},
			{Code: libpq.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: libpq.FieldSQLState, Value: "00000"},
			{Code: libpq.FieldMessage, Value: n.Message},
		}
		if n.Detail != "" {
			fields = append(fields, libpq.ErrorField{Code: libpq.FieldDetail, Value: n.Detail})
		}
		if nerr := w.WriteNoticeResponse(fields); nerr != nil {
			return nerr
		}
	}

	// Emit accumulated WARNING messages before CommandComplete. M0097-0021.
	for _, msg := range ctx.TakeWarnings() {
		if nerr := w.WriteNoticeResponse([]libpq.ErrorField{
			{Code: libpq.FieldSeverity, Value: "WARNING"},
			{Code: libpq.FieldSeverityNonLocal, Value: "WARNING"},
			{Code: libpq.FieldSQLState, Value: "55000"},
			{Code: libpq.FieldMessage, Value: msg},
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
	//
	// take2 P1-03b: statistics-changing utilities invalidate too. ANALYZE and
	// VACUUM are planned as *optimizer.Utility, not *optimizer.DDL, so before
	// this a session could ANALYZE a relation and then re-run a cached query
	// and still get the plan chosen from the OLD statistics — the one case
	// where a user has explicitly asked the planner to reconsider.
	//
	// Upstream reaches the same place by a different route: vac_update_relstats
	// and the pg_statistic writes emit relcache invalidation messages, which
	// plancache.c's ResetPlanCache picks up. goopg's cache has no such message
	// bus, so the trigger is the statement kind.
	if s.pc != nil && planCacheInvalidatingStmt(node) {
		s.pc.Invalidate()
	}
	return w.WriteCommandComplete(tag)
}

// planCacheInvalidatingStmt reports whether the executed statement can have
// changed something a cached plan was built from.
//
// DDL changes the schema; ANALYZE and VACUUM change the STATISTICS the planner
// costed with. Both make a cached plan stale, and goopg has no invalidation
// message bus to notice the second kind, so it is recognised here by statement
// kind. VACUUM is included because its Analyze pass updates reltuples/relpages
// (P1-03) even without the ANALYZE keyword.
func planCacheInvalidatingStmt(node optimizer.Node) bool {
	switch n := node.(type) {
	case *optimizer.DDL:
		return true
	case *optimizer.Utility:
		switch n.Stmt.(type) {
		case *parser.AnalyzeStmt, *parser.VacuumStmt:
			return true
		}
	}
	return false
}

// commandTagFor builds the upstream-shaped CommandComplete tag for
// the executed plan. Matches the strings libpq uses to drive
// `PQcmdStatus` / `PQcmdTuples`.
func commandTagFor(node optimizer.Node, op executor.Operator, rowCount int64) string {
	switch n := node.(type) {
	case *optimizer.DDL:
		return ddlTag(n.Stmt)
	case *optimizer.Insert:
		return fmt.Sprintf("INSERT 0 %d", rowsAffected(op))
	case *optimizer.Update:
		return fmt.Sprintf("UPDATE %d", rowsAffected(op))
	case *optimizer.Delete:
		return fmt.Sprintf("DELETE %d", rowsAffected(op))
	case *optimizer.Transaction:
		return transactionTag(n.Verb)
	case *optimizer.Utility:
		return utilityTag(n.Stmt)
	case *optimizer.Checkpoint:
		_ = n
		return "CHECKPOINT"
	case *optimizer.Explain:
		_ = n
		return "EXPLAIN"
	case *optimizer.Call:
		_ = n
		return "CALL"
	}
	// Read-shaped: SELECT N. Catches Project/Sort/Limit/Filter/Aggregate/
	// Join/SeqScan/IndexScan/Values root nodes.
	return fmt.Sprintf("SELECT %d", rowCount)
}

func transactionTag(v optimizer.TransactionVerb) string {
	switch v {
	case optimizer.TxBegin:
		return "BEGIN"
	case optimizer.TxCommit:
		return "COMMIT"
	case optimizer.TxRollback:
		return "ROLLBACK"
	case optimizer.TxSavepoint:
		return "SAVEPOINT"
	case optimizer.TxRelease:
		return "RELEASE"
	case optimizer.TxRollbackTo:
		return "ROLLBACK"
	}
	return "OK"
}

func ddlTag(stmt parser.Stmt) string {
	switch v := stmt.(type) {
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
		if v.TagOverride != "" {
			return v.TagOverride
		}
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
	case *parser.AlterDomainStmt:
		return "ALTER DOMAIN"
	case *parser.CreateExtensionStmt:
		return "CREATE EXTENSION"
	case *parser.CreateCollationStmt:
		return "CREATE COLLATION"
	case *parser.AlterCollationStmt:
		return "ALTER COLLATION"
	case *parser.AlterConversionStmt:
		return "ALTER CONVERSION"
	case *parser.AlterTSConfigStmt:
		return "ALTER TEXT SEARCH CONFIGURATION"
	case *parser.AlterTSDictStmt:
		return "ALTER TEXT SEARCH DICTIONARY"
	case *parser.CreateAggregateStmt:
		return "CREATE AGGREGATE"
	case *parser.AlterAggregateRenameStmt, *parser.AlterAggregateOwnerStmt:
		return "ALTER AGGREGATE"
	case *parser.CreateTablespaceStmt:
		return "CREATE TABLESPACE"
	case *parser.DropTablespaceStmt:
		return "DROP TABLESPACE"
	case *parser.AlterTablespaceStmt:
		return "ALTER TABLESPACE"
	case *parser.CreateStatisticsStmt:
		return "CREATE STATISTICS"
	case *parser.AlterStatisticsStmt:
		return "ALTER STATISTICS"
	case *parser.AlterSchemaStmt:
		return "ALTER SCHEMA"
	case *parser.AlterOpFamilyAddStmt, *parser.AlterOpFamilyDropStmt:
		return "ALTER OPERATOR FAMILY"
	case *parser.CreateOpClassStmt:
		return "CREATE OPERATOR CLASS"
	case *parser.AlterDefaultPrivilegesStmt:
		return "ALTER DEFAULT PRIVILEGES"
	}
	// CompatNoopStmt carries its own tag. M0097-0016.
	if ns, ok := stmt.(*parser.CompatNoopStmt); ok && ns.Tag != "" {
		return ns.Tag
	}
	if _, ok := stmt.(*parser.CommentOnStmt); ok {
		return "COMMENT"
	}
	if dc, ok := stmt.(*parser.DropCompatStmt); ok {
		if tag, ok := dropCompatTags[dc.ObjType]; ok {
			return tag
		}
	}
	return "OK"
}

// dropCompatTags maps a DropCompatStmt's ObjType (see parser.DropCompatStmt)
// to PostgreSQL's real CommandComplete tag
// (postgres/src/include/tcop/cmdtaglist.h). DROP ROLE/USER/GROUP all parse
// into the same DropRoleStmt node in real PG and share the CMDTAG_DROP_ROLE
// tag regardless of the surface keyword used (utility.c CreateCommandTag),
// so "group"/"role"/"user" all map to "DROP ROLE" here too.
var dropCompatTags = map[string]string{
	"database":                  "DROP DATABASE",
	"foreign table":             "DROP FOREIGN TABLE",
	"foreign-data wrapper":      "DROP FOREIGN DATA WRAPPER",
	"user mapping":              "DROP USER MAPPING",
	"aggregate":                 "DROP AGGREGATE",
	"operator class":            "DROP OPERATOR CLASS",
	"operator family":           "DROP OPERATOR FAMILY",
	"operator":                  "DROP OPERATOR",
	"text search dictionary":    "DROP TEXT SEARCH DICTIONARY",
	"text search parser":        "DROP TEXT SEARCH PARSER",
	"text search template":      "DROP TEXT SEARCH TEMPLATE",
	"text search configuration": "DROP TEXT SEARCH CONFIGURATION",
	"cast":                      "DROP CAST",
	"transform":                 "DROP TRANSFORM",
	"sequence":                  "DROP SEQUENCE",
	"schema":                    "DROP SCHEMA",
	"collation":                 "DROP COLLATION",
	"materialized view":         "DROP MATERIALIZED VIEW",
	"extension":                 "DROP EXTENSION",
	"server":                    "DROP SERVER",
	"language":                  "DROP LANGUAGE",
	"access method":             "DROP ACCESS METHOD",
	"event trigger":             "DROP EVENT TRIGGER",
	"group":                     "DROP ROLE",
	"role":                      "DROP ROLE",
	"user":                      "DROP ROLE",
	"conversion":                "DROP CONVERSION",
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
	case *parser.SetConstraintsStmt:
		return "SET CONSTRAINTS"
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

// appendTypedCellText formats a single result-row cell according to its
// declared column type, matching PostgreSQL's per-type output function
// (float4out/float8out/bpcharout/date_out/time_out/timetz_out/byteaout/
// regclassout). Shared by the simple-query streaming path
// (dispatchSimpleQueryViaExecutor) and the extended-query materializing path
// (executeExtendedQueryViaExecutor) so both protocols render a given column
// type identically — the extended path previously only special-cased
// float4/float8 and fell back to AppendValueText for everything else,
// diverging from simple-query on date/time/timetz/bytea/regclass columns.
// visible is the regprocedure arglist's per-arg-type visibility predicate
// (73rd slice, deferral row 1342); an empty variadic keeps RegOut's
// bare-arglist behavior, so the ~30 direct-call test callers need no edit.
func (s *Server) appendTypedCellText(dst []byte, d executor.Datum, typ catalog.Type, getSetting func(name string) (string, bool), visible ...func(schema string) bool) []byte {
	switch strings.ToLower(typ.Name) {
	case "float4", "real":
		// float4/real uses float32 precision (~7 significant digits).
		// Use strconv bit=32 so the shortest float32 round-trip representation
		// is produced (e.g. 4.56789e+15 not 4.567889919082496e+15). M0097-0022.
		return appendFloatText(dst, d, 32)
	case "float8", "double precision", "double":
		// float8/float4 values must display in PostgreSQL's output format:
		// scientific notation for very large/small values, shortest decimal
		// for normal ones. Convert KindNumeric to float64 and use %g. M0097-0003.
		return appendFloat8Text(dst, d)
	case "char", "bpchar":
		// The comment this replaces claimed bpcharout trims via bcTruelen; it
		// does not — bpcharout is a bare TextDatumGetCString
		// (postgres/src/backend/utils/adt/varchar.c), so PG sends all N
		// declared characters. Measured on PG 18.3: `SELECT c` from a
		// `char(10)` holding 'ab' returns 10 bytes, goopg returned 2. Input
		// coercion stores the value trimmed by design (codec.go's
		// coerceTextLikeDatum), so the declared width is restored here, by the
		// same catalog.PadBpchar the COPY and pgoutput renderers call.
		// M0119-0006 (57th slice).
		if d.Kind == executor.KindString {
			return append(dst, catalog.PadBpchar(typ, d.StringValue())...)
		}
		return d.AppendValueText(dst)
	case "date":
		// Date columns render per the session's DateStyle GUC (style x
		// order), matching PostgreSQL's date_out/EncodeDateOnly. Previously
		// hardcoded ISO regardless of `SET datestyle`. M0097-0004,
		// M-NIGHTLY (run 20260714-011651) DateStyle output-rendering
		// follow-up.
		if d.Kind == executor.KindTime {
			style, order := "ISO", "MDY"
			if getSetting != nil {
				if v, ok := getSetting("datestyle"); ok {
					style, order = misc.ParseDateStyleValue(v)
				}
			}
			return append(dst, misc.FormatDate(d.TimeValue(), style, order)...)
		}
		return d.AppendValueText(dst)
	case "timestamp":
		// Timestamp columns render per the session's DateStyle GUC (style x
		// order), matching PostgreSQL's EncodeDateTime with print_tz=false.
		// Previously hardcoded ISO regardless of `SET datestyle`. M-NIGHTLY
		// (run 20260714-011651) DateStyle output-rendering follow-up.
		if d.Kind == executor.KindTime {
			style, order := "ISO", "MDY"
			if getSetting != nil {
				if v, ok := getSetting("datestyle"); ok {
					style, order = misc.ParseDateStyleValue(v)
				}
			}
			return append(dst, misc.FormatTimestamp(d.TimeValue(), style, order)...)
		}
		return d.AppendValueText(dst)
	case "timestamptz":
		// timestamptz_out: convert the stored (UTC) instant into the session's
		// TimeZone GUC and print the zone, which plain `timestamp` above does
		// not. Split off from the shared "timestamp" case in M0119-0006 —
		// before that, goopg printed a timestamptz with no zone at all and
		// never left UTC, so under `SET TimeZone` the text READ as a different
		// instant than the one stored.
		if d.Kind == executor.KindTime {
			style, order := "ISO", "MDY"
			zone := ""
			if getSetting != nil {
				if v, ok := getSetting("datestyle"); ok {
					style, order = misc.ParseDateStyleValue(v)
				}
				if v, ok := getSetting("timezone"); ok {
					zone = v
				}
			}
			return append(dst, misc.FormatTimestampTZ(d.TimeValue(), style, order, zone)...)
		}
		return d.AppendValueText(dst)
	case "time":
		// Time columns display as HH:MM:SS[.ffffff] with column precision. M0097-0004.
		if d.Kind == executor.KindTime {
			return appendTimeText(dst, d, typ)
		}
		return d.AppendValueText(dst)
	case "timetz":
		// Timetz displays as HH:MM:SS[.ffffff]±HH[:MM]. M0097-0004.
		if d.Kind == executor.KindTime {
			dst = appendTimeText(dst, d, typ)
			return appendTimeTZOffset(dst, d.TimeTZOffsetSecs())
		}
		return d.AppendValueText(dst)
	case "bytea":
		// Bytea values render per the session's `bytea_output` GUC: hex
		// (\xhexstring, the default) or escape (PG's traditional backslash-
		// octal format). Previously hardcoded hex regardless of the GUC.
		// M0097-0035, M0134-0001 S12.
		if d.Kind == executor.KindBytes {
			mode := "hex"
			if getSetting != nil {
				if v, ok := getSetting("bytea_output"); ok {
					mode = v
				}
			}
			return append(dst, array.ByteaOutStyled(d.BytesValue(), mode)...)
		}
		return d.AppendValueText(dst)
	case "regclass", "regproc", "regprocedure", "regtype", "regrole", "regcollation":
		// OID values of the reg* family display as the object's name
		// (regclassout/regprocout/regprocedureout/regtypeout/regroleout/
		// regcollationout) — a direct SELECT of a reg* column, pg_authid.rolname,
		// pg_type.typinput, pg_typeof(...), an <oid>::reg* cast, etc. Rendered
		// through the SAME executor.RegOut the COPY TO path (datumToCopyText)
		// uses, so SELECT and COPY cannot drift apart again (Hard-won Rule #2,
		// pattern_sibling_paths_must_agree; M0119-0006 68th slice). RegOut
		// implements each reg*out's three-verdict shape: OID 0 (InvalidOid) →
		// "-" (matching the ::reg* CastExpr special-cases in executor/expr.go),
		// a catalog hit → the name (regtype schema-qualified when "public" is
		// NOT visible on the session's effective search_path — e.g. pg_dump's
		// search_path='' — matching real regtypeout; M0122-0005 pg_typeof()::oid
		// follow-up), a dangling OID → numeric (RoleNameForOID/regroleout
		// already render the numeral for a since-dropped role).
		if d.Kind == executor.KindInt {
			var argVisible func(s string) bool
			if len(visible) > 0 {
				argVisible = visible[0]
			}
			return append(dst, executor.RegOutArgVisible(typ.Name, uint32(d.Int),
				s.cfg.Catalog, !publicSchemaVisible(getSetting), argVisible)...)
		}
		return d.AppendValueText(dst)
	case "xid8":
		// xid8 is a 64-bit UNSIGNED transaction ID (xid8out, xid.c) but
		// goopg's Datum.Int carries it as a signed int64 (e.g. the wrapped
		// literal '-1'::xid8 stores the two's-complement bit pattern for
		// 2^64-1). The default AppendValueText below does a plain SIGNED
		// strconv.AppendInt and would render such a value as "-1" instead of
		// "18446744073709551615" — the same unsigned-vs-signed mismatch the
		// binary/COPY encoders already avoid via pgUnsignedIDFromDatum
		// (internal/executor/codec.go); this is the TEXT-protocol twin,
		// M0134-0087 (xid.sql sizing) uncovering the same class the
		// binary path fixed earlier without extending it here.
		if d.Kind == executor.KindInt {
			return strconv.AppendUint(dst, uint64(d.Int), 10)
		}
		return d.AppendValueText(dst)
	default:
		return d.AppendValueText(dst)
	}
}

// maybeConvertCellsForClientEncoding transcodes each non-nil DataRow cell from
// server encoding (UTF8) to client_encoding when the two differ. The conversion
// uses mb.BuiltinLookup (the 128 bootstrap pg_conversion rows); user-created
// CREATE CONVERSION entries are not yet consulted. A conversion failure falls
// back to the raw server-encoding bytes (noError semantics — a malformed
// character in one cell should not break the whole result set).
// M0122-0008: encoding conversion first slice (LATIN1 ↔ UTF8).
func (s *Server) maybeConvertCellsForClientEncoding(cells [][]byte, getSetting func(string) (string, bool)) [][]byte {
	encName, ok := getSetting("client_encoding")
	if !ok || encName == "" {
		return cells
	}
	encName = strings.ToUpper(encName)
	if encName == "UTF8" || encName == "UNICODE" {
		return cells
	}
	// Resolve encoding name to PG encoding ID via the catalog table.
	// catalog.EncodingNameToID returns -1 for unknown names.
	clientEnc := catalog.EncodingNameToID(encName)
	if clientEnc < 0 {
		return cells
	}
	// SQL_ASCII means no conversion.
	if clientEnc == 0 {
		return cells
	}
	for i, cell := range cells {
		if cell == nil {
			continue
		}
		converted, err := mb.DoEncodingConversion(cell, mb.PG_UTF8, clientEnc, mb.BuiltinLookup)
		if err != nil {
			// Fall back to raw bytes on conversion failure (noError).
			continue
		}
		cells[i] = converted
	}
	return cells
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
	// PostgreSQL float4out/float8out: shortest round-trip decimal with PG's
	// fixed-vs-scientific exponent thresholds (differs per type — see PGFloatOut).
	return append(dst, executor.PGFloatOut(f, bitSize)...)
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
	// PostgreSQL's float8out uses the shortest round-trip representation with
	// PG's fixed-vs-scientific exponent thresholds; PGFloatOut also renders the
	// canonical special-value names (Infinity/-Infinity/NaN).
	return append(dst, executor.PGFloatOut(f, 64)...)
}

// appendTimeText formats a KindTime datum as a time-of-day string matching PostgreSQL's
// time output format: HH:MM:SS with optional fractional seconds up to the declared precision.
// Precision 0 → "HH:MM:SS", precision N → "HH:MM:SS.ffffff" (N digits). M0097-0004.
func appendTimeText(dst []byte, d executor.Datum, _ catalog.Type) []byte {
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

	// Fractional seconds — only emit when non-zero. The declared precision is
	// applied at INPUT now (roundTimeDatumToPrecision → AdjustTimeForTypmod), so
	// the stored value already holds at most its precision's non-zero fractional
	// digits and the render is verbatim: format the full 6 microsecond digits,
	// strip trailing zeros. M0119-0006 (62nd slice).
	if ns != 0 {
		micro := ns / 1000
		frac := make([]byte, 6)
		for i := 5; i >= 0; i-- {
			frac[i] = byte('0' + micro%10)
			micro /= 10
		}
		// Strip trailing zeros.
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
func typeOIDFor(t catalog.Type) uint32 {
	switch strings.ToLower(t.Name) {
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
	case "char":
		// Quoted `"char"` (pg_type OID 18) never carries a typmod, unlike the
		// bare CHAR keyword which the parser always gives an implicit or
		// explicit length. See planner.exprType's *CastExpr case. M0122-0005.
		if len(t.Args) == 0 {
			return catalog.OIDChar
		}
		return 1042
	case "bpchar":
		return 1042
	case "numeric", "decimal":
		return 1700
	case "pg_lsn":
		return 3220
	case "regtype":
		// pg_typeof()'s declared SQL return type; also `<oid>::regtype`/
		// `<name>::regtype` casts. M0122-0005 pg_typeof()::oid follow-up.
		return catalog.OIDRegtype
	}
	return 25
}

// executeFetch executes or resumes a cursor fetch. It materialises the cursor's
// result set on first access and then tracks the cursor position across FETCH
// FORWARD / FETCH BACKWARD calls. count < 0 means ALL. forward=true for FORWARD
// (default), false for BACKWARD. M0097-0042 cursor position tracking.
func (s *Server) executeFetch(_ context.Context, w *libpq.FrameWriter, ectx *executor.Context, cur *cursorEntry, cursorName string, count int64, forward bool) error {
	// Materialise on first access.
	if !cur.Materialized {
		if err := s.materializeCursor(ectx, cur, cursorName); err != nil {
			return s.writeQueryError(w, execErrCode(err), execErrMsg(err), execErrDetailFields(err)...)
		}
	}

	schema := cur.Schema
	if schema != nil {
		fields := make([]libpq.FieldDescription, len(schema))
		for i, sc := range schema {
			oid := typeOIDFor(sc.Type)
			// Array column (e.g. `p int4[]`): advertise the array pg_type OID
			// (_int4 = 1007) so the client parses the "{1,2}" text as an array
			// rather than a scalar int4. M0118-0002.
			if sc.Type.IsArray {
				oid = catalog.ArrayOIDForBase(oid)
			}
			fields[i] = libpq.FieldDescription{
				Name:         sc.Name,
				TypeOID:      oid,
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
	// Cursor position model (PostgreSQL semantics, M0097-0042, corrected by M0134-0056):
	//   pos=0       = BOF (before first row)
	//   pos=1..N    = AT row k; next FORWARD returns rows[k..], next BACKWARD returns rows[k-1]
	//   pos=N+1     = EOF (past last row); FORWARD returns nothing
	//
	// FETCH FORWARD n from pos P: return rows[P..P+n-1] (0-indexed), new pos = min(P+n, N)
	//   (where N = total rows; we use N not N+1 as EOF sentinel because len(cur.Rows) == N)
	// FETCH BACKWARD n (finite) from pos P: the row at index P-1 is the CURRENT row (already
	//   returned by the preceding fetch) and must NOT be re-returned. Return
	//   rows[P-1-n..P-2] reversed (nearest-first), new pos = max(P-n, 1). Only BACKWARD ALL
	//   includes the current row. M0134-0056.
	// FETCH BACKWARD ALL from pos P: return rows[0..P-1] reversed (includes the current row),
	//   new pos = 0 (BOF)
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
			cur.AtEnd = true // FETCH ALL always ends at EOF (M0134-0074)
		} else {
			cur.Pos = end
			// Past-end forward fetch returned nothing → positioned past the
			// last row (M0134-0074).
			cur.AtEnd = (len(rowsToSend) == 0)
		}
	} else if fetchAll {
		// FETCH BACKWARD ALL: includes the current row (index cur.Pos-1).
		end := cur.Pos // exclusive upper bound for 0-indexed slice
		if end > total {
			end = total
		}
		start := 0 // go all the way to BOF
		// The rows from start..end-1 in reverse order.
		n := end - start
		rev := make([]executor.Row, n)
		for i := 0; i < n; i++ {
			rev[i] = cur.Rows[start+n-1-i]
		}
		rowsToSend = rev
		cur.Pos = 0 // BOF
		cur.AtEnd = false // BACKWARD moves toward BOF, not EOF (M0134-0074)
	} else {
		// FETCH BACKWARD n (finite): exclude the current row (index cur.Pos-1) from the
		// window — it was already returned by the preceding fetch. M0134-0056.
		cur.AtEnd = false // BACKWARD moves toward BOF, not EOF (M0134-0074)
		if cur.Pos == 0 {
			// already at BOF, nothing precedes
			rowsToSend = nil
		} else {
			end := cur.Pos - 1 // exclusive bound; excludes the current row
			if end > total {
				end = total
			}
			start := end - int(count)
			if start < 0 {
				start = 0
			}
			n := end - start
			rev := make([]executor.Row, n)
			for i := 0; i < n; i++ {
				rev[i] = cur.Rows[start+n-1-i]
			}
			rowsToSend = rev
			cur.Pos = start + 1
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
				cell := valueBuf[start:len(valueBuf)]
				if cell == nil {
					// Non-null Datum rendered to zero bytes (empty
					// string/bytea) on a still-nil scratch buffer: slicing
					// a nil []byte with [0:0] yields nil, indistinguishable
					// from the d.IsNull() sentinel above. Coerce to a
					// non-nil zero-length slice so the DataRow encoder
					// emits length 0, not the NULL length -1
					// (M0134-datarow-empty-string).
					cell = []byte{}
				}
				cells = append(cells, cell)
			}
			cells = s.maybeConvertCellsForClientEncoding(cells, ectx.GetSetting)
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

	node, planErr := optimizer.PlanWithSettings(selectStmt, ctxPlanCatalog(ectx, s.cfg.Catalog), ctxPlannerSettings(ectx))
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
	cur.TIDs = nil
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
			// Capture the slot's carried self-tid for WHERE CURRENT OF
			// resolution (M0134-0074), in lockstep with Rows. A zero
			// tid is stored when the slot does not carry one (synthesized
			// rows) — CURRENT OF on such a row is a pre-existing gap.
			block, off, ok := slot.TID()
			tid := storage.ItemPointer{}
			if ok {
				tid.Block = storage.BlockNumber(block)
				tid.Offset = off
			}
			cur.TIDs = append(cur.TIDs, tid)
		}
	}
	cur.Pos = 0
	cur.AtEnd = false
	cur.Materialized = true
	return nil
}

// resolveCurrentOf resolves a WHERE CURRENT OF cursor reference to a concrete
// `ctid = '(block,off)'` equality on the cursor's current row. Mirrors PG's
// execCurrentOf (postgres/src/backend/executor/execCurrent.c:44,65-70,134-139):
// an unknown cursor raises 34000 (ERRCODE_UNDEFINED_CURSOR) and a cursor
// before the first row or past the last row raises 24000
// (ERRCODE_INVALID_CURSOR_STATE). M0134-0074.
func (s *Server) resolveCurrentOf(connTx *connTxState, name string) (parser.Expr, error) {
	cur, ok := connTx.cursorLookup(name)
	if !ok {
		return nil, &executor.ExecError{Code: "34000", Message: fmt.Sprintf("cursor %q does not exist", name)}
	}
	if cur.Pos == 0 || cur.AtEnd {
		return nil, &executor.ExecError{Code: "24000", Message: fmt.Sprintf("cursor %q is not positioned on a row", name)}
	}
	tid := cur.TIDs[cur.Pos-1]
	return &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Column: "ctid"},
		Right: &parser.StringConst{Value: fmt.Sprintf("(%d,%d)", tid.Block, tid.Offset)},
	}, nil
}

// resolveCurrentOfInStmt resolves the WHERE CURRENT OF clause (if any) on an
// UPDATE/DELETE to a concrete `ctid = '(block,off)'` equality, mutating the
// DML's Where field in place. EXPLAIN (ANALYZE ...) wraps the DML in an
// ExplainStmt (tidscan.sql does this for every CURRENT OF statement); the
// inner DML is planned by the same optimizer.Plan call in the caller
// (planner.go:260 ExplainStmt → Plan(s.Inner)), so resolving before that call
// covers both the bare and EXPLAIN-wrapped forms. Returns nil when stmt
// carries no CURRENT OF clause. M0134-0074.
func (s *Server) resolveCurrentOfInStmt(connTx *connTxState, stmt parser.Stmt) error {
	inner := stmt
	if es, ok := stmt.(*parser.ExplainStmt); ok {
		inner = es.Inner
	}
	switch d := inner.(type) {
	case *parser.UpdateStmt:
		if d.CurrentOf == "" {
			return nil
		}
		where, err := s.resolveCurrentOf(connTx, d.CurrentOf)
		if err != nil {
			return err
		}
		d.Where = where
	case *parser.DeleteStmt:
		if d.CurrentOf == "" {
			return nil
		}
		where, err := s.resolveCurrentOf(connTx, d.CurrentOf)
		if err != nil {
			return err
		}
		d.Where = where
	}
	return nil
}

// isCurrentOfDML reports whether stmt is an UPDATE or DELETE with a WHERE
// CURRENT OF clause, unwrapping an EXPLAIN wrapper. Such statements carry a
// statement-scoped tid resolution and must never be served from the
// cross-session plan cache (the cursor position changes between executions).
// M0134-0074.
func isCurrentOfDML(stmt parser.Stmt) bool {
	inner := stmt
	if es, ok := stmt.(*parser.ExplainStmt); ok {
		inner = es.Inner
	}
	switch d := inner.(type) {
	case *parser.UpdateStmt:
		return d.CurrentOf != ""
	case *parser.DeleteStmt:
		return d.CurrentOf != ""
	}
	return false
}
