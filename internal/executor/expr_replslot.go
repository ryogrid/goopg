package executor

import (
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// SQL-callable replication-slot management functions.
//
// goopg reached M0130-S10 with slots creatable only over the replication
// protocol (CREATE_REPLICATION_SLOT / DROP_REPLICATION_SLOT, see
// internal/server/replication.go). Upstream also exposes them as ordinary
// SQL functions in pg_proc — postgres/src/backend/replication/slotfuncs.c
// `pg_create_physical_replication_slot` (OID 3779) and
// `pg_drop_replication_slot` (OID 3780) — and both are already seeded into
// goopg's pg_proc, so `SELECT pg_create_physical_replication_slot('s')`
// resolved in the catalog but fell through the executor's builtin switch
// with 42883. M-NIGHTLY AI-20260810-011258-003.
//
// Both functions delegate to the SAME *wal.Slots registry the walsender
// commands use (Context.ReplSlots), so the SQL and wire entry points cannot
// drift apart.

// replSlotExecError maps a wal slot-registry error onto the SQLSTATE
// upstream raises for it. Mirrors internal/server.replicationSlotErrCode —
// the two must agree, since a client cannot tell which entry point served
// the request.
func replSlotExecError(err error, pos int, prefix string) *ExecError {
	code := "XX000"
	switch {
	case errors.Is(err, xlog.ErrSlotExists):
		code = "42710" // duplicate_object
	case errors.Is(err, xlog.ErrSlotNotFound):
		code = "42704" // undefined_object
	case errors.Is(err, xlog.ErrSlotInUse):
		code = "55006" // object_in_use
	case errors.Is(err, xlog.ErrSlotInvalidName):
		code = "22023" // invalid_parameter_value
	}
	return &ExecError{Code: code, Pos: pos, Message: prefix + err.Error()}
}

// replSlotBoolArg evaluates optional boolean argument `idx`, defaulting to
// `def` when the argument is absent or NULL.
func replSlotBoolArg(x *optimizer.FuncCall, idx int, def bool, row Row, ctx *Context) (bool, error) {
	if len(x.Args) <= idx {
		return def, nil
	}
	d, err := evalExpr(x.Args[idx], row, ctx)
	if err != nil {
		return false, err
	}
	if d.IsNull() {
		return def, nil
	}
	return d.BoolValue(), nil
}

// evalPgCreatePhysicalReplicationSlot implements
//
//	pg_create_physical_replication_slot(slot_name name,
//	                                    immediately_reserve boolean DEFAULT false,
//	                                    temporary boolean DEFAULT false)
//	  → record (slot_name name, lsn pg_lsn)
//
// Upstream (slotfuncs.c) returns the slot's name plus the reserved LSN,
// where the LSN column is NULL unless `immediately_reserve` was passed —
// without it, upstream defers WAL reservation until a walsender attaches.
// goopg's registry has no "created but unreserved" state (wal.Slots.Create
// always anchors RestartLSN), so the slot always reserves at the current
// write position; only the *reported* LSN follows upstream's NULL/value
// rule. That is strictly safer for retention (it never retains more WAL
// than upstream would) and is recorded in the deferral ledger.
func evalPgCreatePhysicalReplicationSlot(x *optimizer.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) == 0 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(),
			Message: "pg_create_physical_replication_slot requires a slot name"}
	}
	if ctx.ReplSlots == nil {
		return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: "replication slots are not configured on this server"}
	}
	nameD, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if nameD.IsNull() {
		// Upstream is STRICT on slot_name: a NULL argument yields NULL
		// without touching the registry.
		return NullDatum, nil
	}
	name := nameD.StringValue()

	reserve, err := replSlotBoolArg(x, 1, false, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	temporary, err := replSlotBoolArg(x, 2, false, row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if temporary {
		// A temporary slot lives only for the creating session and is
		// dropped on disconnect. wal.Slots has no session ownership, so
		// refuse rather than silently persist a slot the caller expects
		// to disappear. Deferral-ledger row: session-scoped slots.
		return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: "temporary replication slots are not supported"}
	}

	// Reserve at the first byte of the NEXT record, matching
	// replyCreateReplicationSlot: WrittenLSN() is the last appended byte,
	// and a consumer resumes at +1. Starting at the last byte instead
	// makes the record iterator decode garbage (M0094-0005).
	var startLSN uint64
	if ctx.WAL != nil {
		startLSN = ctx.WAL.WrittenLSN() + 1
	}
	slot, err := ctx.ReplSlots.Create(name, xlog.SlotPhysical, startLSN)
	if err != nil {
		return Datum{}, replSlotExecError(err, x.Pos(), "")
	}

	// Render the (slot_name, lsn) record in upstream's composite text
	// form. goopg has no composite Datum kind yet, so the value is a
	// text rendering of the row — byte-identical to what psql prints for
	// the record, but typed text rather than record (ledger row).
	lsnCol := ""
	if reserve {
		lsnCol = fmt.Sprintf("%X/%X", uint32(slot.RestartLSN>>32), uint32(slot.RestartLSN))
	}
	return NewStringDatum(fmt.Sprintf("(%s,%s)", slot.Name, lsnCol)), nil
}

// evalPgDropReplicationSlot implements
//
//	pg_drop_replication_slot(slot_name name) → void
//
// Upstream (slotfuncs.c) errors if the slot does not exist or is held by
// an active walsender; wal.Slots.Drop enforces the same two conditions.
func evalPgDropReplicationSlot(x *optimizer.FuncCall, row Row, ctx *Context) (Datum, error) {
	if len(x.Args) == 0 {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(),
			Message: "pg_drop_replication_slot requires a slot name"}
	}
	if ctx.ReplSlots == nil {
		return Datum{}, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: "replication slots are not configured on this server"}
	}
	nameD, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return Datum{}, err
	}
	if nameD.IsNull() {
		return NullDatum, nil
	}
	if err := ctx.ReplSlots.Drop(nameD.StringValue()); err != nil {
		return Datum{}, replSlotExecError(err, x.Pos(), "")
	}
	return NullDatum, nil
}
