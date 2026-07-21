package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/planner"
)

// genRowsOp is an unbounded synthetic child: it emits int rows forever.
// Distinct from operator_reset_test.go's stubRowsOp (finite, replayed);
// this one exists to prove a drain loop honours cancellation mid-stream —
// without the ctx.Err() check the DISTINCT drain would never return.
type genRowsOp struct {
	schema planner.Schema
	slot   *MaterializedSlot
	n      int64
}

func (g *genRowsOp) Open(*Context) error    { return nil }
func (g *genRowsOp) Close() error           { return nil }
func (g *genRowsOp) Schema() planner.Schema { return g.schema }
func (g *genRowsOp) Next() (TupleSlot, error) {
	g.n++
	g.slot.row = Row{NewIntDatum(g.n)}
	return g.slot, nil
}

// TestDistinctOpenAbortsOnCancel pins the csq-S6 fix: the distinctOp Open
// drain (the `Unique` node of the spin-incident plan) must notice a
// cancelled context and return SQLSTATE 57014 promptly instead of draining
// an effectively unbounded child to completion.
func TestDistinctOpenAbortsOnCancel(t *testing.T) {
	schema := planner.Schema{{Name: "n"}}
	child := &genRowsOp{schema: schema}
	child.slot = SlotFromRow(schema, nil)

	// Construct directly (same package): a zero-value planner.Distinct has
	// no child wired for Output(), and only plan/child/schema matter here.
	op := &distinctOp{plan: &planner.Distinct{}, child: child, schema: schema}

	cctx, cancel := context.WithCancel(context.Background())
	ctx := &Context{Ctx: cctx}
	time.AfterFunc(50*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() { done <- op.Open(ctx) }()

	select {
	case err := <-done:
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Code != "57014" {
			t.Fatalf("want ExecError 57014, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("distinctOp.Open did not abort within 5s of cancellation")
	}
}
