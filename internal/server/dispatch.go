package server

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

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
func (s *Server) dispatchSimpleQueryViaExecutor(w *protocol.FrameWriter, sql string) error {
	stmts, err := parser.Parse(sql)
	if err != nil {
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
	commit := false
	defer func() {
		if !commit {
			_ = s.cfg.TxnMgr.Rollback(tx)
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
	if err := s.cfg.TxnMgr.Commit(tx); err != nil {
		return s.writeQueryError(w, sqlstate.SystemError, err.Error())
	}
	commit = true
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
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
