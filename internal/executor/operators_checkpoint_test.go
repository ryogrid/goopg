package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

type fakeCheckpointer struct {
	calls int
	err   error
}

func (f *fakeCheckpointer) CheckpointNow() error {
	f.calls++
	return f.err
}

func (f *fakeCheckpointer) CheckpointRedoLSN() uint64 { return 0 }

// TestExecCheckpointInvokesCheckpointer pins the success path:
// the SQL CHECKPOINT verb routes through parser -> planner ->
// executor and triggers exactly one CheckpointNow call. The
// operator emits no rows.
func TestExecCheckpointInvokesCheckpointer(t *testing.T) {
	plan := planOne(t, "CHECKPOINT", catalog.NewInMemory())
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	cp := &fakeCheckpointer{}
	ctx := NewContext()
	ctx.Checkpointer = cp
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows=%d want 0", len(rows))
	}
	if cp.calls != 1 {
		t.Errorf("CheckpointNow calls=%d want 1", cp.calls)
	}
}

// TestExecCheckpointWithoutCheckpointerFails covers the v0
// fallback: a server started without a WAL writer surfaces
// CHECKPOINT as feature_not_supported (SQLSTATE 0A000) rather
// than crashing or silently succeeding.
func TestExecCheckpointWithoutCheckpointerFails(t *testing.T) {
	plan := planOne(t, "CHECKPOINT", catalog.NewInMemory())
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(op, NewContext())
	if err == nil {
		t.Fatal("expected error when Checkpointer is nil")
	}
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "0A000" {
		t.Fatalf("got %v want ExecError code 0A000", err)
	}
}

// TestExecCheckpointPropagatesError ensures a checkpointer
// failure (e.g. WAL append error) reaches the wire layer as a
// runtime ExecError rather than being swallowed.
func TestExecCheckpointPropagatesError(t *testing.T) {
	plan := planOne(t, "CHECKPOINT", catalog.NewInMemory())
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	cp := &fakeCheckpointer{err: errors.New("simulated wal failure")}
	ctx := NewContext()
	ctx.Checkpointer = cp
	_, err = Run(op, ctx)
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "XX000" {
		t.Fatalf("got %v want ExecError code XX000", err)
	}
}
