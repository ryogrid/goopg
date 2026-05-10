package server

import (
	"context"
	"fmt"
	"strconv"

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
func (s *Server) executeExtendedQueryViaExecutor(ctx context.Context, sess *config.SessionRegistry, query string, params []boundParam) (*extendedQueryResult, *extendedQueryError) {
	stmts, err := parser.Parse(query)
	if err != nil {
		msg, extra := syntaxErrorMsg(err)
		qerr := &extendedQueryError{Code: sqlstate.SyntaxError, Message: msg}
		for _, f := range extra {
			if f.Code == protocol.FieldPosition {
				if p, _ := strconv.Atoi(f.Value); p > 0 {
					qerr.Position = p
				}
			}
		}
		return nil, qerr
	}
	if len(stmts) == 0 {
		return &extendedQueryResult{Empty: true}, nil
	}
	if len(stmts) > 1 {
		return nil, &extendedQueryError{Code: sqlstate.SyntaxError, Message: "extended query may contain only one statement"}
	}
	stmt := stmts[0]
	node, err := planner.Plan(stmt, s.cfg.Catalog)
	if err != nil {
		code, msg := planErrorFields(err)
		return nil, &extendedQueryError{Code: code, Message: msg}
	}

	if tx, ok := node.(*planner.Transaction); ok {
		return &extendedQueryResult{CommandTag: transactionTag(tx.Verb)}, nil
	}

	tx, err := s.cfg.TxnMgr.Begin(mvcc.IsolationReadCommitted)
	if err != nil {
		return nil, &extendedQueryError{Code: sqlstate.SystemError, Message: err.Error()}
	}
	commit := false
	defer func() {
		if !commit {
			_ = s.cfg.TxnMgr.Rollback(tx)
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
	ectx.Ctx = ctx
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.Tx = tx
	ectx.Snap = snap
	ectx.Params = datums
	ectx.Checkpointer = s.cfg.Checkpointer
	ectx.StatsTarget = sessionStatsTarget(sess)
	ectx.WorkMem = sessionWorkMem(sess)
	ectx.PubSub = s.cfg.PubSub

	op, err := executor.Build(node)
	if err != nil {
		return nil, &extendedQueryError{Code: execErrCode(err), Message: execErrMsg(err)}
	}
	if err := op.Open(ectx); err != nil {
		_ = op.Close()
		return nil, &extendedQueryError{Code: execErrCode(err), Message: execErrMsg(err)}
	}

	schema := node.Output()
	res := &extendedQueryResult{}
	if len(schema) > 0 {
		res.Fields = make([]protocol.FieldDescription, len(schema))
		for i, sc := range schema {
			res.Fields[i] = protocol.FieldDescription{
				Name:         sc.Name,
				TypeOID:      typeOIDFor(sc.Type.Name),
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
			return nil, &extendedQueryError{Code: execErrCode(err), Message: execErrMsg(err)}
		}
		if len(schema) > 0 {
			row := slot.Row()
			cells := make([][]byte, len(row))
			for i, d := range row {
				if d.IsNull() {
					cells[i] = nil
					continue
				}
				cells[i] = []byte(d.Format())
			}
			res.Rows = append(res.Rows, cells)
			rowCount++
		}
	}
	if err := op.Close(); err != nil {
		return nil, &extendedQueryError{Code: execErrCode(err), Message: execErrMsg(err)}
	}
	if err := s.cfg.TxnMgr.Commit(tx); err != nil {
		return nil, &extendedQueryError{Code: sqlstate.SystemError, Message: err.Error()}
	}
	commit = true

	res.CommandTag = commandTagFor(node, op, rowCount)
	if res.CommandTag == "" {
		res.CommandTag = "OK"
	}
	return res, nil
}

// paramsToDatums maps bound text-format parameters into
// executor.Datum values. v0 doesn't carry per-parameter type
// information through Bind, so every value lands as a string Datum
// and the planner/expression evaluator coerces on read (e.g. via
// `$1::int4` casts). NULL parameters become NullDatum.
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
