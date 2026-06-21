package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// copyInState carries the per-connection state for an in-flight
// COPY FROM STDIN. Either fromExec is non-nil (executor-backed path,
// when Server has Catalog/Pool/TxnMgr configured) and lineBuf gathers
// CopyData fragments into newline-terminated rows, or fromExec is
// nil (legacy path) and rows tracks the upstream-style row count by
// counting newlines.
type copyInState struct {
	// Legacy path (no storage configured).
	rows       int64
	partialRow bool

	// Executor path.
	fromExec *executor.CopyFromExecutor
	lineBuf  []byte
	tx       mvcc.Transaction
	mgr      *mvcc.Manager
}

func (s *Server) handleQueryOrCopy(ctx context.Context, r *protocol.FrameReader, w *protocol.FrameWriter, sess *config.SessionRegistry, payload []byte, connTx *connTxState, prepStmts *preparedStatements) (*copyInState, error) {
	q, err := extractCString(payload)
	if err != nil {
		if err := s.writeQueryError(w, sqlstate.ProtocolViolation,
			fmt.Sprintf("malformed Query message: %v", err)); err != nil {
			return nil, err
		}
		return nil, nil
	}
	_, matchable, upper, empty := normalizeSimpleQuery(q)
	// Multi-statement simple-query batches (psql's `\;`-joined commands
	// arrive as a single Query message with internal ';') must go through
	// the multi-statement dispatcher even when the first statement is a
	// COPY — otherwise dispatchCopyViaExecutor's single-statement guard
	// rejects them with "expected exactly one COPY statement". The
	// dispatcher runs each COPY inline via runInlineCopy. M0097-0024.
	multiStatement := strings.ContainsRune(matchable, ';')
	if empty || !strings.HasPrefix(upper, "COPY ") || multiStatement {
		if err := s.handleQuery(ctx, r, w, sess, payload, connTx, prepStmts); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Try the real path first whenever the server has storage
	// configured. The planner does its own SQLSTATE-aligned errors
	// (42P01, 42703, 42601, 0A000); the wire layer just forwards
	// them.
	if s.cfg.hasStorage() {
		st, err := s.dispatchCopyViaExecutor(ctx, w, matchable, connTx)
		if err != nil {
			return nil, err
		}
		return st, nil
	}

	// Fallback: the v0 string-matching path that ships when the
	// server has no catalog/pool/mvcc handles. Useful for the
	// protocol-only tests and for early integration where storage
	// hasn't been wired up yet.
	switch {
	case strings.HasSuffix(upper, "TO STDOUT"):
		return s.handleCopyToStdoutQuery(w, matchable)
	case strings.Contains(upper, " FROM STDIN"):
		if err := w.WriteCopyInResponse(0, nil); err != nil {
			return nil, err
		}
		return &copyInState{}, nil
	default:
		if err := s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("COPY shape not supported by goopg v0: %q", matchable)); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

// dispatchCopyViaExecutor parses + plans the COPY statement and
// either streams rows out (CopyTo) or arms the connection's CopyIn
// frame consumer (CopyFrom). connTx is the connection's explicit
// transaction state (may be nil); its ProcNum is forwarded to the
// COPY-internal transaction so it does not collide with the
// connection's own ProcArray slot.
func (s *Server) dispatchCopyViaExecutor(ctx context.Context, w *protocol.FrameWriter, sql string, connTx *connTxState) (*copyInState, error) {
	stmts, err := parser.Parse(sql)
	if err != nil {
		// Render parser syntax errors the same way the main simple-query
		// path does: strip the internal " (byte N)" suffix and attach the
		// Position field so psql shows `LINE n:` with a caret. Without
		// this the COPY path leaked e.g. `syntax error at or near "from"
		// (byte 27)` and no caret. M0097-0024.
		msg, extra := syntaxErrorMsg(err)
		if err := s.writeQueryError(w, sqlstate.SyntaxError, msg, extra...); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if len(stmts) != 1 {
		if err := s.writeQueryError(w, sqlstate.SyntaxError, "expected exactly one COPY statement"); err != nil {
			return nil, err
		}
		return nil, nil
	}
	node, err := planner.Plan(stmts[0], s.cfg.Catalog)
	if err != nil {
		code, msg := planErrorFields(err)
		// Carry any planner hint (e.g. "Try the COPY (SELECT ...) TO
		// variant." for a COPY-from-view rejection) onto the wire.
		if err := s.writeQueryError(w, code, msg, planErrorHintFields(err)...); err != nil {
			return nil, err
		}
		return nil, nil
	}
	plan, ok := node.(*planner.Copy)
	if !ok {
		// Wire layer routed this here only because the SQL starts
		// with "COPY "; if the planner returned something else, the
		// SQL was a non-COPY statement that happened to start with
		// the COPY keyword (none exist today, but future-proofed).
		if err := s.writeQueryError(w, sqlstate.FeatureNotSupported, "expected COPY statement"); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Use a dedicated procNum for the COPY-internal transaction so it
	// does not overwrite the connection's own ProcArray slot. The COPY
	// always runs in its own auto-commit transaction regardless of
	// whether the client has opened an explicit BEGIN block.
	// Offset by half the ProcArray to avoid the typical connection range.
	var copyProcNum int32
	if connTx != nil {
		const halfSize = mvcc.DefaultProcArraySize / 2
		copyProcNum = (connTx.ProcNum + halfSize) % mvcc.DefaultProcArraySize
	}
	tx, err := s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted, copyProcNum)
	if err != nil {
		if err := s.writeQueryError(w, sqlstate.SystemError, err.Error()); err != nil {
			return nil, err
		}
		return nil, nil
	}
	snap, err := s.cfg.TxnMgr.SnapshotFor(tx)
	if err != nil {
		_ = s.cfg.TxnMgr.Rollback(tx)
		if err := s.writeQueryError(w, sqlstate.SystemError, err.Error()); err != nil {
			return nil, err
		}
		return nil, nil
	}
	ectx := executor.NewContext()
	ectx.Ctx = ctx
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.MultiXact = s.cfg.MultiXact
	ectx.Tx = tx
	ectx.Snap = snap
	ectx.LogCanonical = s.cfg.LogCanonical

	switch plan.Direction {
	case planner.CopyTo:
		count, errorSent, err := s.runCopyToStream(w, ectx, plan)
		if err != nil {
			_ = s.cfg.TxnMgr.Rollback(tx)
			return nil, err
		}
		if errorSent {
			// runCopyToStream already wrote ErrorResponse + RFQ.
			_ = s.cfg.TxnMgr.Rollback(tx)
			return nil, nil
		}
		// Commit BEFORE CommandComplete so COPY (DML … RETURNING)
		// writes are durable/visible by the time the client proceeds.
		if cErr := s.cfg.TxnMgr.Commit(tx); cErr != nil {
			if wErr := s.writeQueryError(w, sqlstate.SystemError, cErr.Error()); wErr != nil {
				return nil, wErr
			}
			return nil, nil
		}
		if err := w.WriteCommandComplete(fmt.Sprintf("COPY %d", count)); err != nil {
			return nil, err
		}
		if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
			return nil, err
		}
		return nil, nil
	case planner.CopyFrom:
		if plan.Endpoint == planner.CopyEndpointFile {
			// Server-side COPY FROM 'file': read file directly and insert rows.
			count, err := executor.RunCopyFromFile(ectx, plan)
			if err != nil {
				_ = s.cfg.TxnMgr.Rollback(tx)
				if wErr := s.writeQueryError(w, execErrCode(err), execErrMsg(err)); wErr != nil {
					return nil, wErr
				}
				return nil, nil
			}
			_ = s.cfg.TxnMgr.Commit(tx)
			if err := w.WriteCommandComplete(fmt.Sprintf("COPY %d", count)); err != nil {
				return nil, err
			}
			if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
				return nil, err
			}
			return nil, nil
		}
		from, err := executor.NewCopyFromExecutor(ectx, plan)
		if err != nil {
			_ = s.cfg.TxnMgr.Rollback(tx)
			if err := s.writeQueryError(w, execErrCode(err), execErrMsg(err)); err != nil {
				return nil, err
			}
			return nil, nil
		}
		fmtCode := byte(0)
		if executor.IsBinaryFormat(plan.Options) {
			fmtCode = 1
		}
		if err := w.WriteCopyInResponse(fmtCode, nil); err != nil {
			_ = s.cfg.TxnMgr.Rollback(tx)
			return nil, err
		}
		return &copyInState{
			fromExec: from,
			tx:       tx,
			mgr:      s.cfg.TxnMgr,
		}, nil
	}
	_ = s.cfg.TxnMgr.Rollback(tx)
	if err := s.writeQueryError(w, sqlstate.FeatureNotSupported, "unknown COPY direction"); err != nil {
		return nil, err
	}
	return nil, nil
}

// runInlineCopy executes a COPY statement that appears inside a
// multi-statement simple-query batch (psql's `\;`-joined commands, which
// libpq delivers as a single Query message with internal ';'). Unlike
// dispatchCopyViaExecutor — which runs the COPY in its own dedicated
// auto-commit transaction and emits its own ReadyForQuery — this runs
// within the batch's shared executor context (ectx, already carrying the
// per-statement transaction and snapshot) and writes only CommandComplete
// on success. The surrounding dispatch loop emits the single trailing
// ReadyForQuery that covers the whole Query message, matching PostgreSQL's
// exec_simple_query semantics (one RFQ per message, one CommandComplete
// per statement).
//
// COPY ... TO STDOUT and server-side COPY ... FROM 'file' are supported
// because neither needs client→server streaming mid-batch. COPY FROM STDIN
// inside a batch is handled by runInlineCopyFromStdin, which reads the
// client's CopyData/CopyDone frames synchronously from r (the connection's
// main read loop is parked until the batch finishes).
//
// On a handled error the relevant write helper has already emitted
// ErrorResponse + ReadyForQuery, so this returns errQueryErrorSent and the
// caller aborts the rest of the batch without emitting a second RFQ.
func (s *Server) runInlineCopy(r *protocol.FrameReader, w *protocol.FrameWriter, ectx *executor.Context, cs *parser.CopyStmt) error {
	node, perr := planner.Plan(cs, s.cfg.Catalog)
	if perr != nil {
		code, msg := planErrorFields(perr)
		return s.writeQueryError(w, code, msg, planErrorHintFields(perr)...)
	}
	plan, ok := node.(*planner.Copy)
	if !ok {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported, "expected COPY statement")
	}

	switch plan.Direction {
	case planner.CopyTo:
		count, errorSent, err := s.runCopyToStream(w, ectx, plan)
		if err != nil {
			// runCopyToStream already wrote ErrorResponse + RFQ on a
			// mid-stream executor error; propagate the sentinel so the
			// batch aborts cleanly.
			return err
		}
		if errorSent {
			return errQueryErrorSent
		}
		return w.WriteCommandComplete(fmt.Sprintf("COPY %d", count))
	case planner.CopyFrom:
		if plan.Endpoint == planner.CopyEndpointFile {
			// Server-side COPY FROM 'file' reads on the server with no
			// wire interaction, so it works inline.
			count, err := executor.RunCopyFromFile(ectx, plan)
			if err != nil {
				return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
			}
			return w.WriteCommandComplete(fmt.Sprintf("COPY %d", count))
		}
		return s.runInlineCopyFromStdin(r, w, ectx, plan)
	}
	return s.writeQueryError(w, sqlstate.FeatureNotSupported, "unknown COPY direction")
}

// runInlineCopyFromStdin drives the CopyInResponse / CopyData* / CopyDone
// handshake for a COPY FROM STDIN that appears inside a multi-statement
// simple-query batch (psql's `\;`-joined commands). It reads the client's
// CopyData/CopyDone/CopyFail frames synchronously from r — the connection's
// main read loop is parked in handleQueryOrCopy until the whole batch
// finishes, so consuming frames here is safe and keeps statement ordering.
//
// Unlike the single-COPY path (copyInState + handleCopyInFrame, driven by the
// main loop), this neither commits nor emits ReadyForQuery: the COPY shares
// the batch's transaction (ectx.Tx), which the dispatch loop commits once at
// the end, and the dispatch loop emits the single trailing RFQ for the whole
// Query message. On success it writes only CommandComplete "COPY n".
//
// A client COPY failure (CopyFail) or a decode/insert error writes
// ErrorResponse + RFQ via writeQueryError and returns errQueryErrorSent so the
// dispatch loop aborts the rest of the batch without a second RFQ. A frame
// read error (or Terminate) returns a non-sentinel error so the connection
// loop tears the connection down.
func (s *Server) runInlineCopyFromStdin(r *protocol.FrameReader, w *protocol.FrameWriter, ectx *executor.Context, plan *planner.Copy) error {
	from, err := executor.NewCopyFromExecutor(ectx, plan)
	if err != nil {
		return s.writeQueryError(w, execErrCode(err), execErrMsg(err))
	}
	fmtCode := byte(0)
	if executor.IsBinaryFormat(plan.Options) {
		fmtCode = 1
	}
	if err := w.WriteCopyInResponse(fmtCode, nil); err != nil {
		return err
	}
	// The client only sends CopyData after it has seen CopyInResponse, so the
	// buffered frame must reach the wire before we block on ReadFrame.
	if err := w.Flush(); err != nil {
		return err
	}

	var lineBuf []byte
	for {
		f, err := r.ReadFrame()
		if err != nil {
			return err
		}
		switch f.Type {
		case protocol.MsgCopyData:
			if from.IsBinary() {
				done, perr := from.PushBinaryData(f.Payload)
				if perr != nil {
					return s.writeQueryError(w, execErrCode(perr), execErrMsg(perr))
				}
				if done {
					// Binary trailer seen inside CopyData — same as CopyDone.
					return w.WriteCommandComplete(fmt.Sprintf("COPY %d", from.RowsInserted()))
				}
				continue
			}
			lineBuf = append(lineBuf, f.Payload...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := lineBuf[:idx]
				// `\.` end-of-data marker (deprecated in v3 but still sent by
				// psql before CopyDone): skip it, matching upstream text COPY.
				if !isCopyTextEOD(line) {
					if perr := from.PushLine(line); perr != nil {
						return s.writeQueryError(w, execErrCode(perr), execErrMsg(perr))
					}
				}
				lineBuf = lineBuf[idx+1:]
			}
		case protocol.MsgCopyDone:
			// Flush any partial trailing line (no terminating newline).
			if !from.IsBinary() && len(lineBuf) > 0 && !isCopyTextEOD(lineBuf) {
				if perr := from.PushLine(lineBuf); perr != nil {
					return s.writeQueryError(w, execErrCode(perr), execErrMsg(perr))
				}
			}
			return w.WriteCommandComplete(fmt.Sprintf("COPY %d", from.RowsInserted()))
		case protocol.MsgCopyFail:
			msg, merr := extractCString(f.Payload)
			if merr != nil || msg == "" {
				msg = "COPY from stdin aborted by frontend"
			}
			return s.writeQueryError(w, sqlstate.QueryCanceled, msg)
		case protocol.MsgFlush:
			// No-op: nothing buffered to push out mid-COPY.
		case protocol.MsgTerminate:
			return io.EOF
		default:
			return s.writeQueryError(w, sqlstate.ProtocolViolation,
				fmt.Sprintf("unexpected message %q in COPY FROM STDIN", f.Type))
		}
	}
}

// runCopyToStream drives the CopyOutResponse / CopyData* / CopyDone
// sequence for a CopyTo plan and returns the streamed row count. It
// deliberately stops short of CommandComplete / ReadyForQuery so the
// caller can commit the COPY-internal transaction *before* signalling
// completion — critical for COPY (INSERT/UPDATE/DELETE … RETURNING),
// whose mutations must be durable and visible by the time the client
// is told the command finished. (Streaming a read-only SELECT/table is
// unaffected by the ordering, but COPY(DML) loses visibility otherwise:
// the client races ahead with its next query before the commit lands.)
//
// On a mid-stream executor error this writes the wire ErrorResponse +
// ReadyForQuery itself and returns errorSent=true so the caller rolls
// back and emits nothing further.
func (s *Server) runCopyToStream(w *protocol.FrameWriter, ctx *executor.Context, plan *planner.Copy) (count int64, errorSent bool, err error) {
	// Peek at the options to decide the wire format code before opening
	// the operator — WriteCopyOutResponse must precede any CopyData frames.
	binary := executor.IsBinaryFormat(plan.Options)
	fmtCode := byte(0)
	if binary {
		fmtCode = 1
	}
	if err := w.WriteCopyOutResponse(fmtCode, nil); err != nil {
		return 0, false, err
	}
	count, _, execErr := executor.RunCopyTo(ctx, plan, func(line []byte) error {
		return w.WriteCopyData(line)
	})
	if execErr != nil {
		if wErr := s.writeQueryError(w, execErrCode(execErr), execErrMsg(execErr)); wErr != nil {
			return count, true, wErr
		}
		return count, true, nil
	}
	if err := w.WriteCopyDone(); err != nil {
		return count, false, err
	}
	// Flush NOTICEs accumulated by DML operators (e.g. trigger RAISE NOTICE). M0097-0140.
	for _, msg := range ctx.TakeNotices() {
		_ = w.WriteNoticeResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "NOTICE"},
			{Code: protocol.FieldSeverityNonLocal, Value: "NOTICE"},
			{Code: protocol.FieldSQLState, Value: "00000"},
			{Code: protocol.FieldMessage, Value: msg},
		})
	}
	return count, false, nil
}

func (s *Server) handleCopyToStdoutQuery(w *protocol.FrameWriter, matchable string) (*copyInState, error) {
	if !strings.EqualFold(matchable, "COPY (SELECT 1) TO STDOUT") {
		if err := s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("COPY TO STDOUT shape not supported by goopg v0: %q", matchable)); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := w.WriteCopyOutResponse(0, nil); err != nil {
		return nil, err
	}
	if err := w.WriteCopyData([]byte("1\n")); err != nil {
		return nil, err
	}
	if err := w.WriteCopyDone(); err != nil {
		return nil, err
	}
	if err := w.WriteCommandComplete("COPY 1"); err != nil {
		return nil, err
	}
	if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Server) handleCopyInFrame(w *protocol.FrameWriter, st *copyInState, f protocol.Frame) (bool, error) {
	switch f.Type {
	case protocol.MsgCopyData:
		if st.fromExec != nil {
			var err error
			if st.fromExec.IsBinary() {
				var done bool
				done, err = st.fromExec.PushBinaryData(f.Payload)
				if err == nil && done {
					// Trailer found inside CopyData — treat as CopyDone.
					rows := st.fromExec.RowsInserted()
					if st.mgr != nil {
						_ = st.mgr.Commit(st.tx)
					}
					if cerr := w.WriteCommandComplete(fmt.Sprintf("COPY %d", rows)); cerr != nil {
						return true, cerr
					}
					maybeForceGCAfterCommit()
					if cerr := w.WriteReadyForQuery(protocol.TxStatusIdle); cerr != nil {
						return true, cerr
					}
					return true, nil
				}
			} else {
				err = st.consumeExecCopyData(f.Payload)
			}
			if err != nil {
				// Decode/insert error: abort transaction and report.
				if st.mgr != nil {
					_ = st.mgr.Rollback(st.tx)
				}
				st.fromExec = nil
				if werr := w.WriteErrorResponse([]protocol.ErrorField{
					{Code: protocol.FieldSeverity, Value: "ERROR"},
					{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
					{Code: protocol.FieldSQLState, Value: execErrCodeStr(err)},
					{Code: protocol.FieldMessage, Value: execErrMsg(err)},
					{Code: protocol.FieldRoutine, Value: "server.handleCopyInFrame"},
				}); werr != nil {
					return true, werr
				}
				if werr := w.WriteReadyForQuery(protocol.TxStatusIdle); werr != nil {
					return true, werr
				}
				return true, nil
			}
			return false, nil
		}
		st.consumeTextCopyData(f.Payload)
		return false, nil
	case protocol.MsgCopyDone:
		if st.fromExec != nil {
			// Flush any partial trailing line (no terminating \n on
			// the wire).
			if len(st.lineBuf) > 0 {
				if err := st.fromExec.PushLine(st.lineBuf); err != nil {
					if st.mgr != nil {
						_ = st.mgr.Rollback(st.tx)
					}
					if werr := w.WriteErrorResponse([]protocol.ErrorField{
						{Code: protocol.FieldSeverity, Value: "ERROR"},
						{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
						{Code: protocol.FieldSQLState, Value: execErrCodeStr(err)},
						{Code: protocol.FieldMessage, Value: execErrMsg(err)},
						{Code: protocol.FieldRoutine, Value: "server.handleCopyInFrame"},
					}); werr != nil {
						return true, werr
					}
					if werr := w.WriteReadyForQuery(protocol.TxStatusIdle); werr != nil {
						return true, werr
					}
					return true, nil
				}
				st.lineBuf = st.lineBuf[:0]
			}
			rows := st.fromExec.RowsInserted()
			if st.mgr != nil {
				_ = st.mgr.Commit(st.tx)
			}
			if err := w.WriteCommandComplete(fmt.Sprintf("COPY %d", rows)); err != nil {
				return true, err
			}
			maybeForceGCAfterCommit()
			if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
				return true, err
			}
			return true, nil
		}
		if st.partialRow {
			st.rows++
			st.partialRow = false
		}
		if err := w.WriteCommandComplete(fmt.Sprintf("COPY %d", st.rows)); err != nil {
			return true, err
		}
		if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
			return true, err
		}
		return true, nil
	case protocol.MsgCopyFail:
		msg, err := extractCString(f.Payload)
		if err != nil || msg == "" {
			msg = "COPY from stdin aborted by frontend"
		}
		if st.fromExec != nil && st.mgr != nil {
			_ = st.mgr.Rollback(st.tx)
		}
		if err := w.WriteErrorResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "ERROR"},
			{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
			{Code: protocol.FieldSQLState, Value: string(sqlstate.QueryCanceled)},
			{Code: protocol.FieldMessage, Value: msg},
			{Code: protocol.FieldRoutine, Value: "server.handleCopyInFrame"},
		}); err != nil {
			return true, err
		}
		if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
			return true, err
		}
		return true, nil
	case protocol.MsgFlush:
		return false, nil
	case protocol.MsgTerminate:
		if st.fromExec != nil && st.mgr != nil {
			_ = st.mgr.Rollback(st.tx)
		}
		return true, nil
	default:
		if st.fromExec != nil && st.mgr != nil {
			_ = st.mgr.Rollback(st.tx)
		}
		if err := w.WriteErrorResponse([]protocol.ErrorField{
			{Code: protocol.FieldSeverity, Value: "ERROR"},
			{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
			{Code: protocol.FieldSQLState, Value: string(sqlstate.ProtocolViolation)},
			{Code: protocol.FieldMessage, Value: fmt.Sprintf("unexpected message %q in COPY FROM STDIN", f.Type)},
			{Code: protocol.FieldRoutine, Value: "server.handleCopyInFrame"},
		}); err != nil {
			return true, err
		}
		if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
			return true, err
		}
		return true, nil
	}
}

// consumeExecCopyData appends chunk into st.lineBuf and pushes every
// completed line through the executor. Returns the first push error
// encountered.
func (st *copyInState) consumeExecCopyData(chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	st.lineBuf = append(st.lineBuf, chunk...)
	for {
		idx := bytes.IndexByte(st.lineBuf, '\n')
		if idx < 0 {
			return nil
		}
		line := st.lineBuf[:idx]
		// libpq's PQputline / PQputCopyData callers (notably
		// pgbench) terminate the data stream with a `\.` line
		// before issuing CopyDone. Upstream's text-mode COPY treats
		// it as an end-of-data marker and drops it; protocol v3
		// uses CopyDone for the actual signal but the literal `\.`
		// frame still arrives. Match upstream's behaviour and
		// silently skip it.
		if isCopyTextEOD(line) {
			st.lineBuf = st.lineBuf[idx+1:]
			continue
		}
		if err := st.fromExec.PushLine(line); err != nil {
			st.lineBuf = st.lineBuf[:0]
			return err
		}
		st.lineBuf = st.lineBuf[idx+1:]
	}
}

// isCopyTextEOD reports whether a single decoded COPY-text line is
// the deprecated `\.` end-of-data marker. The line passed in has
// already had its trailing `\n` stripped; we also tolerate `\r`
// before the `\.` for CRLF clients.
func isCopyTextEOD(line []byte) bool {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return len(line) == 2 && line[0] == '\\' && line[1] == '.'
}

func (st *copyInState) consumeTextCopyData(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	for _, b := range chunk {
		if b == '\n' {
			st.rows++
		}
	}
	st.partialRow = chunk[len(chunk)-1] != '\n'
}

// syntaxErrorMsg returns the human-readable message and optional extra
// ErrorFields for a parser error. When err is *parser.SyntaxError it strips
// the internal "(byte N)" suffix and returns a FieldPosition field carrying
// the 1-based byte offset so psql can render the caret-pointer line.
func syntaxErrorMsg(err error) (msg string, extra []protocol.ErrorField) {
	var se *parser.SyntaxError
	if errors.As(err, &se) {
		msg = se.Error()
		if idx := strings.LastIndex(msg, " (byte "); idx >= 0 {
			msg = msg[:idx]
		}
		if se.Pos >= 0 {
			extra = []protocol.ErrorField{
				{Code: protocol.FieldPosition, Value: strconv.Itoa(se.Pos + 1)},
			}
		}
		return msg, extra
	}
	return err.Error(), nil
}

// planErrorFields extracts the SQLSTATE + message from a planner
// error. Falls back to FeatureNotSupported when the error isn't a
// *PlanError.
func planErrorFields(err error) (sqlstate.Code, string) {
	var pe *planner.PlanError
	if errors.As(err, &pe) {
		return sqlstate.Code(pe.Code), pe.Message
	}
	return sqlstate.FeatureNotSupported, err.Error()
}

// planErrorHintFields returns extra ErrorField(s) for any hint carried
// by the error. Returns nil when no hint is present. M0097-0003.
func planErrorHintFields(err error) []protocol.ErrorField {
	var pe *planner.PlanError
	if !errors.As(err, &pe) {
		return nil
	}
	var fields []protocol.ErrorField
	if pe.Detail != "" {
		fields = append(fields, protocol.ErrorField{Code: protocol.FieldDetail, Value: pe.Detail})
	}
	if pe.Hint != "" {
		fields = append(fields, protocol.ErrorField{Code: protocol.FieldHint, Value: pe.Hint})
	}
	return fields
}

func execErrCode(err error) sqlstate.Code {
	var ee *executor.ExecError
	if errors.As(err, &ee) && ee.Code != "" {
		return sqlstate.Code(ee.Code)
	}
	return sqlstate.SystemError
}

// execErrDetailFields returns extra ErrorField(s) for any Detail and Hint carried by the error. M0097-0003.
func execErrDetailFields(err error) []protocol.ErrorField {
	var ee *executor.ExecError
	if !errors.As(err, &ee) {
		return nil
	}
	var fields []protocol.ErrorField
	if ee.Detail != "" {
		fields = append(fields, protocol.ErrorField{Code: protocol.FieldDetail, Value: ee.Detail})
	}
	if ee.Hint != "" {
		fields = append(fields, protocol.ErrorField{Code: protocol.FieldHint, Value: ee.Hint})
	}
	if ee.Context != "" {
		fields = append(fields, protocol.ErrorField{Code: protocol.FieldWhere, Value: ee.Context})
	}
	return fields
}

func execErrCodeStr(err error) string { return string(execErrCode(err)) }

func execErrMsg(err error) string {
	var ee *executor.ExecError
	if errors.As(err, &ee) {
		return ee.Message
	}
	return err.Error()
}
