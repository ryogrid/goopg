package executor

import (
	"errors"

	"github.com/goopg/goopg/internal/planner"
)

// EOF is the sentinel returned by Operator.Next when no more rows
// remain. Operators must keep returning EOF on subsequent Next calls.
var EOF = errors.New("executor: end of stream")

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
	Next() (Row, error)
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
