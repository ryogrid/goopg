package executor

import (
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// evalPgLogicalEmitMessage implements
//
//	pg_logical_emit_message(transactional boolean, prefix text,
//	                        message text | bytea, flush boolean DEFAULT false)
//	  → pg_lsn
//
// as logicalfuncs.c's pg_logical_emit_message_bytea / _text call
// LogLogicalMessage (replication/logical/message.c): one RM_LOGICALMSG
// record carrying the database, the transactional flag, the prefix and the
// message. A transactional message first assigns the transaction an xid
// (GetCurrentTransactionId) so decoding can queue it in that transaction; a
// non-transactional one, or any message with flush = true, is flushed at
// once (XLogFlush). The result is the end of the record (XLogInsert's
// return). The function is strict: a NULL argument yields NULL. Like any
// WAL insertion it fails during recovery.
func evalPgLogicalEmitMessage(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 3 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(),
			Message: "function pg_logical_emit_message requires at least 3 arguments"}
	}
	args := make([]Datum, len(x.Args))
	for i, a := range x.Args {
		d, err := evalExprSlot(a, slot, ctx)
		if err != nil {
			return Datum{}, err
		}
		if d.IsNull() {
			return NullDatum, nil
		}
		args[i] = d
	}
	transactional := args[0].BoolValue()
	prefix := args[1].StringValue()
	var content []byte
	if args[2].Kind == KindBytes {
		content = append([]byte(nil), args[2].BytesValue()...)
	} else {
		content = []byte(args[2].StringValue())
	}
	flush := len(args) > 3 && args[3].BoolValue()

	if ctx.IsStandby {
		return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: "cannot make new WAL entries during recovery"}
	}
	if ctx.WAL == nil {
		return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: "pg_logical_emit_message: no WAL writer"}
	}
	if transactional {
		if err := ctx.MaterializeWriterXID(); err != nil {
			return Datum{}, err
		}
	}
	payload, err := xlog.EncodeLogicalMessagePG(ctx.Tx.XID, xlog.LogicalMessage{
		DBOid:         logicalMessageDBOid(ctx),
		Transactional: transactional,
		Prefix:        prefix,
		Content:       content,
	})
	if err != nil {
		return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: err.Error()}
	}
	_, end, err := ctx.WAL.Append(payload)
	if err != nil {
		return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: err.Error()}
	}
	if !transactional || flush {
		if err := ctx.WAL.FlushUpTo(end); err != nil {
			return Datum{}, &ExecError{Code: "XX000", Pos: x.Pos(), Message: err.Error()}
		}
	}
	return NewStringDatum(formatPgLSN(end)), nil
}

// logicalMessageDBOid is MyDatabaseId for xl_logical_message.dbId: the
// physical pg_database.oid of the connection's database, resolved by name
// the way the logical walsender resolves its own (resolveConnDBOid), so
// logicalmsg_decode's database filter compares like with like.
func logicalMessageDBOid(ctx *Context) uint32 {
	type resolver interface {
		ResolveDatabaseOid(name string) (uint32, bool)
	}
	cat := ctx.Catalog
	if sp, ok := cat.(*catalog.SearchPathCatalog); ok {
		cat = sp.Catalog
	}
	if r, ok := cat.(resolver); ok {
		if oid, ok := r.ResolveDatabaseOid(currentDatabaseName(ctx)); ok {
			return oid
		}
	}
	return 0
}
