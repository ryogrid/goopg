package server

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
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

const queriesPerForcedFree = 8

// maybeForceGCAfterCommit triggers `runtime.GC()` +
// `debug.FreeOSMemory()` at the end of a Query message when
// either:
//   - HeapInuse > heapReleaseThresholdBytes  (this query was big), or
//   - we've gone queriesPerForcedFree queries without a Free   (drift).
//
// Performance: each Free call is ~50–500 ms on a 4 GiB heap and
// happens at most once per query, on the path where the client
// has *already* received its CommandComplete (the GC pause is
// invisible to the just-finished query). The next query may pay
// a cold-cache penalty on first allocation, but at our query
// granularity (seconds) that is negligible.
func maybeForceGCAfterCommit() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	n := atomic.AddInt64(&queriesWithoutFreeCounter, 1)
	if ms.HeapInuse < heapReleaseThresholdBytes && n < queriesPerForcedFree {
		return
	}
	atomic.StoreInt64(&queriesWithoutFreeCounter, 0)
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
func (s *Server) dispatchSimpleQueryViaExecutor(ctx context.Context, w *protocol.FrameWriter, sess *config.SessionRegistry, sql string, connTx *connTxState, prepStmts *preparedStatements) error {
	stmts, err := parser.Parse(sql)
	if err != nil {
		// M0054-0001: CREATE DATABASE / DROP DATABASE are intercepted
		// here (the parser doesn't recognise them yet) so we can
		// (a) update the catalog so subsequent connections see the
		// database in pg_database / can connect to it, and (b) emit a
		// WAL record so the registration survives a crash. Other
		// commands fall through to the wire-protocol no-op tag handler.
		if handled, herr := s.tryHandleDatabaseDDL(sql); handled {
			if herr != nil {
				return s.writeQueryError(w, sqlstate.SystemError, herr.Error())
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
		tx, err = s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted)
		if err != nil {
			return s.writeQueryError(w, sqlstate.SystemError, err.Error())
		}
	}
	// Each Query message gets a fresh BackendID for the lock
	// manager; the youngest-backend victim policy from M0012-0002
	// relies on monotonic IDs.
	backendID := lockmgr.BackendID(s.nextBackendID.Add(1))
	commit := false
	defer func() {
		if autoCommit && !commit {
			_ = s.cfg.TxnMgr.Rollback(tx)
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
	ectx := executor.NewContext()
	ectx.Ctx = ctx
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.Tx = tx
	// Wire the per-connection session into the executor so advisory locks
	// and other session-scoped state are properly tracked.
	if connTx != nil {
		if sess := connTx.Session(); sess != nil {
			ectx.Session = sess
		}
	}
	ectx.Snap = snap
	ectx.Checkpointer = s.cfg.Checkpointer
	ectx.StatsTarget = sessionStatsTarget(sess)
	ectx.WorkMem = sessionWorkMem(sess)
	ectx.EnableOpportunisticPrune = sessionOpportunisticPrune(sess)
	ectx.FSM = s.cfg.FSM
	ectx.VM = s.cfg.VM
	ectx.FreezeMinAge = sessionFreezeMinAge(sess)
	ectx.PubSub = s.cfg.PubSub
	ectx.LockMgr = s.cfg.LockMgr
	ectx.BackendID = backendID

	// Update pg_stat_activity before dispatching.
	if reg := s.cfg.Activity; reg != nil {
		if _, pid, _ := sess.Get("goopg.backend_pid"); pid != "" {
			q := sql
			if len(q) > 1024 {
				q = q[:1024]
			}
			reg.UpdateState(pid, "active", q)
		}
	}

	for _, stmt := range stmts {
		// Handle PREPARE / EXECUTE / DEALLOCATE inline (M0096-0006).
		// These require per-connection state not available in the executor.
		if ps, ok := stmt.(*parser.PrepareStmt); ok {
			tag := "PREPARE"
			if prepStmts != nil && ps.Name != "" {
				// Store the raw SQL for later EXECUTE.  We reconstruct it
				// from the original batch since the parsed form may lose
				// position information for non-SELECT queries.
				prepStmts.Store(ps.Name, sql)
			}
			if err := w.WriteCommandComplete(tag); err != nil {
				return err
			}
			continue
		}
		if es, ok := stmt.(*parser.ExecuteStmt); ok {
			if prepStmts != nil {
				if prepSQL, found := prepStmts.Lookup(es.Name); found {
					// Re-dispatch the stored SQL as a fresh query.
					if err := s.dispatchSimpleQueryViaExecutor(ctx, w, sess, prepSQL, connTx, prepStmts); err != nil {
						return err
					}
					continue
				}
			}
			return s.writeQueryError(w, "26000", fmt.Sprintf("prepared statement %q does not exist", es.Name))
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

		// Refresh snapshot per statement for ReadCommitted parity.
		snap2, err := s.cfg.TxnMgr.SnapshotFor(tx)
		if err != nil {
			return s.writeQueryError(w, sqlstate.SystemError, err.Error())
		}
		ectx.Snap = snap2

		if err := s.executeOneSimpleStmt(w, ectx, stmt, connTx, &autoCommit); err != nil {
			return err
		}
	}
	// Update pg_stat_activity to idle after successful execution.
	if reg := s.cfg.Activity; reg != nil {
		if _, pid, _ := sess.Get("goopg.backend_pid"); pid != "" {
			reg.UpdateState(pid, "idle", "")
		}
	}
	if autoCommit {
		if err := s.cfg.TxnMgr.Commit(tx); err != nil {
			return s.writeQueryError(w, sqlstate.SystemError, err.Error())
		}
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

func compatNoopCommandTag(sql string) (string, bool) {
	norm := normalizeCompatSQL(sql)
	switch {
	case strings.HasPrefix(norm, "create user "), strings.HasPrefix(norm, "create role "):
		return "CREATE ROLE", true
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

func normalizeCompatSQL(sql string) string {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// executeOneSimpleStmt plans and runs one statement, emitting the
// per-statement wire messages but NOT ReadyForQuery (the caller
// terminates the batch).
//
// connTx, if non-nil, tracks the per-connection explicit transaction
// state so BEGIN/COMMIT/ROLLBACK can open/close real TxnMgr transactions.
// autoCommitPtr, if non-nil, is set to false when a BEGIN starts an
// explicit transaction (telling the caller not to auto-commit).
func (s *Server) executeOneSimpleStmt(w *protocol.FrameWriter, ctx *executor.Context, stmt parser.Stmt, connTx *connTxState, autoCommitPtr *bool) error {
	node, err := planner.Plan(stmt, s.cfg.Catalog)
	if err != nil {
		code, msg := planErrorFields(err)
		return s.writeQueryError(w, code, msg)
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
				connTx.Begin(ctx.Tx)
			}
			return w.WriteCommandComplete(transactionTag(txNode.Verb))
		case planner.TxCommit:
			if connTx != nil && connTx.InExplicit() {
				if err := s.cfg.TxnMgr.Commit(connTx.Tx()); err != nil {
					connTx.End()
					return s.writeQueryError(w, sqlstate.SystemError, err.Error())
				}
				connTx.End()
				maybeForceGCAfterCommit()
				// Leave *autoCommitPtr = false so the caller does NOT attempt
				// a second TxnMgr.Commit on the already-committed transaction.
			}
			return w.WriteCommandComplete(transactionTag(txNode.Verb))
		case planner.TxRollback:
			if connTx != nil && connTx.InExplicit() {
				_ = s.cfg.TxnMgr.Rollback(connTx.Tx())
				connTx.End()
				// Leave *autoCommitPtr = false to avoid a second rollback attempt.
			}
			return w.WriteCommandComplete(transactionTag(txNode.Verb))
		default:
			// Other verbs (SAVEPOINT, ROLLBACK TO, RELEASE) pass through
			// the existing logic.
			return w.WriteCommandComplete(transactionTag(txNode.Verb))
		}
	}
	op, err := executor.Build(node)
	if err != nil {
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
	}

	// Emit RowDescription for read-shaped plans (those whose Output
	// schema is non-nil); writing operators (Insert/Update/Delete/
	// DDL/Transaction) have empty schemas and emit only the command
	// tag.
	schema := node.Output()
	// CALL plans have a dynamic schema that depends on the procedure's
	// OUT params; the operator reports it after Open.
	if schema == nil {
		schema = op.Schema()
	}
	if len(schema) > 0 {
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
			return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
		}
		if len(schema) > 0 {
			row := slot.Row()
			// M0092-0004: per-connection scratch buffers back the
			// wire frame so the simple-query result loop is O(1)
			// allocation across rows AND statements.
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
				_ = op.Close()
				return err
			}
			rowCount++
		}
	}
	if err := op.Close(); err != nil {
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
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

	tag := commandTagFor(node, op, rowCount)
	if tag == "" {
		tag = "OK"
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
	}
	// CompatNoopStmt carries its own tag. M0097-0016.
	if ns, ok := stmt.(*parser.CompatNoopStmt); ok && ns.Tag != "" {
		return ns.Tag
	}
	return "OK"
}

func utilityTag(stmt parser.Stmt) string {
	switch stmt.(type) {
	case *parser.VacuumStmt:
		return "VACUUM"
	case *parser.AnalyzeStmt:
		return "ANALYZE"
	}
	return "OK"
}

func rowsAffected(op executor.Operator) int64 {
	if rc, ok := op.(executor.RowCounter); ok {
		return rc.RowsAffected()
	}
	return 0
}

// typeOIDFor maps a goopg type name to a pg_type.oid the wire
// protocol can advertise. Unknown types fall back to text (25),
// which is wire-compatible with libpq's text-format reader.
func typeOIDFor(name string) uint32 {
	switch strings.ToLower(name) {
	case "int4", "integer", "int":
		return 23
	case "int8", "bigint":
		return 20
	case "bool", "boolean":
		return 16
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
	}
	return 25
}
