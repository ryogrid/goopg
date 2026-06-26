package executor

import (
	"errors"

	"github.com/goopg/goopg/internal/planner"
)

// EOF is the sentinel returned by Operator.Next when no more rows
// remain. Operators must keep returning EOF on subsequent Next calls.
var EOF = errors.New("executor: end of stream")

// ErrSelfTerminate is the sentinel returned when a query calls
// pg_terminate_backend(pid) targeting its own backend PID. The current query
// is aborted immediately (no result row, mirroring PostgreSQL's
// SIGTERM-at-CHECK_FOR_INTERRUPTS behaviour where the connection dies inside
// the function rather than returning a value); the server layer recognises it,
// emits the FATAL "terminating connection due to administrator command"
// ErrorResponse, and closes the connection. M0118-0009 (temp-schema-cleanup
// process-exit permutation).
var ErrSelfTerminate = errors.New("executor: backend self-termination requested")

// Operator is the iterator interface every executor node implements.
//
// Lifecycle:
//
//	Open(ctx) -> Next ... Next -> EOF -> Close
//
// Open prepares any per-statement state (pinning resources,
// pre-computing constant subexpressions). Next advances by one row
// and returns either (row, nil) or (nil, EOF) at end of stream. Close
// releases resources; Close MUST be called even after Next returned
// an error.
type Operator interface {
	Open(ctx *Context) error
	Next() (TupleSlot, error)
	Close() error
	Schema() planner.Schema
}

// RowCounter is implemented by DML operators that report a
// post-execution affected-row count (Insert, Update, Delete). The
// wire-protocol path uses this to build the canonical
// `INSERT 0 N`/`UPDATE N`/`DELETE N` CommandComplete tag.
type RowCounter interface {
	RowsAffected() int64
}

// (M0071-0015 Stage E removed BorrowSemantics / OwnedRow /
// BorrowedRow / Borrowable / setChildBorrow. Slot kind now
// encodes lifetime semantics structurally per design doc
// 0068-0002:
//   - Producer slots (seqScan / indexScan / indexOnlyScan):
//     consumers that retain across Next must call
//     slot.Materialize() to take ownership.
//   - Pass-through slots (filter / limit / instrument):
//     forwarded directly; no copy.
//   - Composing slots (NLI / MHJ / joinOp.nextLazy virtualOut):
//     valid until the next step's source-update; consumers
//     materialize at retention.
// The producer-side "skip clone when borrowed" branches are
// gone — producers always cloneRow for stable output, and
// Stage B's retention-site Materialize calls (sortOp.Open,
// windowOp.Open, lockRowsOp.drainAndStamp, executor.Run)
// continue to handle the lifetime boundary.)
