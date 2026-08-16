package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// checkpointOp drives the SQL `CHECKPOINT` verb. It calls
// Context.Checkpointer.CheckpointNow synchronously on Open and
// returns a no-row stream so the wire layer emits just the
// CommandComplete tag.
type checkpointOp struct{ plan *optimizer.Checkpoint }

func newCheckpointOp(p *optimizer.Checkpoint) *checkpointOp {
	return &checkpointOp{plan: p}
}

func (o *checkpointOp) Schema() optimizer.Schema { return nil }

func (o *checkpointOp) Open(ctx *Context) error {
	if ctx == nil || ctx.Checkpointer == nil {
		return &ExecError{
			Code:    "0A000", // feature_not_supported
			Pos:     o.plan.Pos(),
			Message: "CHECKPOINT requires a server started with a WAL writer; the v0 in-process server has none",
		}
	}
	if err := ctx.Checkpointer.CheckpointNow(); err != nil {
		return &ExecError{
			Code:    "XX000", // internal_error
			Pos:     o.plan.Pos(),
			Message: "CHECKPOINT failed: " + err.Error(),
		}
	}
	return nil
}

func (o *checkpointOp) Next() (TupleSlot, error) { return nil, EOF }
func (o *checkpointOp) Close() error       { return nil }
