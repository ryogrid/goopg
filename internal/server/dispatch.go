package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// maybeForceGCAfterCommit was introduced by M0032-0006 to bound RSS
// growth after commits. M0032-0005 throttled it to every 64 commits.
// Removed entirely: GOMEMLIMIT provides the RSS ceiling without
// forced stop-the-world pauses, and the throttled GC still appeared
// as 91 % GC overhead in run-005's Q9 profile on 1.8 M-row data.
func maybeForceGCAfterCommit() {}

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
func (s *Server) dispatchSimpleQueryViaExecutor(w *protocol.FrameWriter, sess *config.SessionRegistry, sql string) error {
	stmts, err := parser.Parse(sql)
	if err != nil {
		if tag, ok := compatNoopCommandTag(sql); ok {
			if err := w.WriteCommandComplete(tag); err != nil {
				return err
			}
			return w.WriteReadyForQuery(protocol.TxStatusIdle)
		}
		return s.writeQueryError(w, sqlstate.SyntaxError, err.Error())
	}
	if len(stmts) == 0 {
		if err := w.WriteEmptyQueryResponse(); err != nil {
			return err
		}
		return w.WriteReadyForQuery(protocol.TxStatusIdle)
	}
	// Begin one statement-level transaction per Query message and
	// commit at the end. v0 doesn't yet plumb session-level
	// BEGIN/COMMIT into the wire layer.
	tx, err := s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		return s.writeQueryError(w, sqlstate.SystemError, err.Error())
	}
	// Each Query message gets a fresh BackendID for the lock
	// manager; the youngest-backend victim policy from M0012-0002
	// relies on monotonic IDs.
	backendID := lockmgr.BackendID(s.nextBackendID.Add(1))
	commit := false
	defer func() {
		if !commit {
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
	ctx := executor.NewContext()
	ctx.Pool = s.cfg.Pool
	ctx.Catalog = s.cfg.Catalog
	ctx.TxnMgr = s.cfg.TxnMgr
	ctx.Tx = tx
	ctx.Snap = snap
	ctx.Checkpointer = s.cfg.Checkpointer
	ctx.StatsTarget = sessionStatsTarget(sess)
	ctx.WorkMem = sessionWorkMem(sess)
	ctx.PubSub = s.cfg.PubSub
	ctx.LockMgr = s.cfg.LockMgr
	ctx.BackendID = backendID

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
		// Refresh snapshot per statement for ReadCommitted parity.
		snap2, err := s.cfg.TxnMgr.SnapshotFor(tx)
		if err != nil {
			return s.writeQueryError(w, sqlstate.SystemError, err.Error())
		}
		ctx.Snap = snap2

		if err := s.executeOneSimpleStmt(w, ctx, stmt); err != nil {
			return err
		}
	}
	// Update pg_stat_activity to idle after successful execution.
	if reg := s.cfg.Activity; reg != nil {
		if _, pid, _ := sess.Get("goopg.backend_pid"); pid != "" {
			reg.UpdateState(pid, "idle", "")
		}
	}
	if err := s.cfg.TxnMgr.Commit(tx); err != nil {
		return s.writeQueryError(w, sqlstate.SystemError, err.Error())
	}
	commit = true
	maybeForceGCAfterCommit()
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
	case strings.HasPrefix(norm, "grant "):
		return "GRANT", true
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
func (s *Server) executeOneSimpleStmt(w *protocol.FrameWriter, ctx *executor.Context, stmt parser.Stmt) error {
	node, err := planner.Plan(stmt, s.cfg.Catalog)
	if err != nil {
		code, msg := planErrorFields(err)
		return s.writeQueryError(w, code, msg)
	}
	// Transaction verbs are no-ops at the wire layer for v0: every
	// simple-query Query message already runs inside a per-batch
	// ReadCommitted transaction (see dispatchSimpleQueryViaExecutor).
	// Pgbench -i's BEGIN ... COMMIT envelope around the COPY/ALTER
	// block needs to succeed without erroring; full session-tx
	// semantics (multi-batch atomicity, savepoints) wait for the
	// session-state plumbing in a follow-up loop.
	if tx, ok := node.(*planner.Transaction); ok {
		return w.WriteCommandComplete(transactionTag(tx.Verb))
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
		row, err := op.Next()
		if err == executor.EOF {
			break
		}
		if err != nil {
			_ = op.Close()
			return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
		}
		if len(schema) > 0 {
			cells := make([][]byte, len(row))
			for i, d := range row {
				if d.IsNull() {
					cells[i] = nil
					continue
				}
				cells[i] = []byte(d.Format())
			}
			if err := w.WriteDataRow(cells); err != nil {
				_ = op.Close()
				return err
			}
			rowCount++
		}
	}
	if err := op.Close(); err != nil {
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
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
