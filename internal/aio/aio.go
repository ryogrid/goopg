// Package aio provides goopg's asynchronous-I/O substrate. The
// public surface mirrors PostgreSQL 18's `pgaio_*` API closely
// enough that an operator familiar with upstream's
// `src/backend/storage/aio/` can find their bearings here.
//
// The core idea: callers don't issue `pread`/`pwrite` directly.
// They build an `Op` describing the I/O, hand it to an `Engine`
// via `Submit`, and later `Wait` on the returned `Handle`. The
// engine's chosen `Method` decides whether the I/O actually runs
// synchronously, on a worker goroutine, or eventually through
// `io_uring`. The caller's code path is identical regardless.
//
// What this slice delivers (M0009 / 0009-0001):
//
//   - `Op`, `Direction`, `Handle`, `Result`, `Method`,
//     `Engine` — the public types and the submission /
//     completion contract.
//   - `methodSync` — synchronous fallback; every Submit runs
//     the I/O inline and the Handle is already complete by
//     the time it's returned.
//   - `methodWorker` — bounded goroutine pool that performs
//     `pread`/`pwrite` syscalls on the caller's behalf.
//     Submission blocks once the in-flight cap is hit, so a
//     misbehaving caller cannot allocate unbounded queue depth.
//   - `NewEngine(EngineConfig)` factory honouring the upstream
//     `io_method` GUC values (`sync`, `worker`).
//
// What this slice doesn't deliver (follow-up loops):
//
//   - `io_uring` method (Linux-only, gated on a runtime probe).
//   - Read-stream API on top of the core.
//   - Caller integrations (sequential heap scan, bitmap heap
//     scan, checkpointer dirty-page writeback, WAL writer).
//   - The `pg_aios` virtual catalog view and the
//     stats / wait-event surface.
//
// See docs/design/0009-0001-aio-core.md.

package aio

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Direction is the read-vs-write flavour of an Op.
type Direction int

const (
	// DirRead is a `pread`-style I/O: bytes flow from the
	// File into Buffer at Offset.
	DirRead Direction = iota
	// DirWrite is a `pwrite`-style I/O: bytes flow from
	// Buffer into the File at Offset.
	DirWrite
)

// String renders the direction as the upstream-aligned word
// surfaced in the future `pg_aios.direction` column. Stable —
// don't rename without coordinating with operators' dashboards.
func (d Direction) String() string {
	switch d {
	case DirRead:
		return "read"
	case DirWrite:
		return "write"
	}
	return "unknown"
}

// File is the seam the AIO core sees into the underlying
// resource. Mirrors the subset of `*os.File` the I/O methods
// need; tests pass an in-memory implementation.
type File interface {
	io.ReaderAt
	io.WriterAt
}

// Op describes one I/O the caller wants the engine to perform.
// Buffer is the destination for reads / source for writes. The
// caller owns the buffer's memory: it must remain valid until
// Wait returns. Direction selects pread vs pwrite.
type Op struct {
	File      File
	Buffer    []byte
	Offset    int64
	Direction Direction

	// Callback, if non-nil, fires on the engine's completion
	// goroutine after the I/O lands. Mirrors upstream's
	// `pgaio_io_register_callbacks`. Nil is fine — Wait
	// always sees the same Result.
	Callback func(Result)
}

// Result is the outcome of one completed Op. N is the byte
// count the underlying syscall returned (for an EOF-truncated
// read this can be < len(Buffer)). Err is non-nil on failure;
// EOF is reported as io.EOF.
type Result struct {
	N   int
	Err error
}

// Handle is the caller's reference to one in-flight (or already
// completed) Op. Wait blocks until the I/O finishes; subsequent
// Wait calls return the same Result without blocking.
type Handle struct {
	op   Op
	done chan struct{}
	mu   sync.Mutex
	res  Result

	// engine back-reference is currently unused but reserved
	// for cancellation in a follow-up loop.
	engine *Engine
}

// Wait blocks until the Op completes and returns its Result.
// Idempotent.
func (h *Handle) Wait() Result {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.res
}

// finish stores the result and unblocks any Wait. Internal —
// callers don't invoke this directly; Methods do.
func (h *Handle) finish(r Result) {
	h.mu.Lock()
	h.res = r
	h.mu.Unlock()
	close(h.done)
	if h.op.Callback != nil {
		h.op.Callback(r)
	}
}

// Method is the I/O method abstraction. Each method implements
// the same Submit contract — synchronous, worker-pool, or (in a
// follow-up loop) io_uring.
type Method interface {
	// Submit hands an Op off to the method. The returned
	// Handle's Wait completes when the I/O does. Submit may
	// block on backpressure, but it never blocks waiting on
	// the I/O itself — that's Wait's job.
	Submit(*Op) *Handle

	// Close shuts the method down: drain in-flight I/Os, stop
	// background goroutines, free resources. Safe to call
	// twice; second and later calls are no-ops.
	Close() error

	// Name reports the upstream-aligned method name for the
	// `io_method` GUC and the future `pg_aios` view.
	Name() string
}

// Engine is the front door for callers. Wraps one Method plus
// process-wide bookkeeping (in-flight counter, total submitted
// counter) so the future observability slice has counters to
// surface without touching every method.
type Engine struct {
	method     Method
	inFlight   atomic.Int64
	submitted  atomic.Uint64
	completed  atomic.Uint64
	errored    atomic.Uint64
}

// EngineConfig parameterises NewEngine. Method names mirror
// the upstream `io_method` GUC values (`sync`, `worker`).
// Workers and MaxConcurrency only apply to `worker`. Zero
// values for either fall back to the sensible defaults below.
type EngineConfig struct {
	// Method picks the I/O method. Empty defaults to `sync`
	// (the safe fallback every platform supports).
	Method string

	// Workers is the goroutine count for `method=worker`.
	// Zero defaults to defaultWorkerCount.
	Workers int

	// MaxConcurrency caps in-flight I/Os globally. Zero
	// disables the cap (no backpressure). Both the `worker`
	// and `sync` methods respect this — `sync` blocks Submit
	// briefly if the cap is hit, since each Submit holds the
	// in-flight slot for the duration of the inline syscall.
	MaxConcurrency int
}

const (
	// MethodSync is the upstream-aligned name for the
	// synchronous fallback method.
	MethodSync = "sync"
	// MethodWorker is the upstream-aligned name for the
	// goroutine-pool method.
	MethodWorker = "worker"
	// MethodIOUring is the upstream-aligned name reserved for
	// the future io_uring implementation. Currently unsupported;
	// NewEngine returns an error if it's selected.
	MethodIOUring = "io_uring"

	defaultWorkerCount = 3
)

// ErrUnsupportedMethod is returned by NewEngine when the
// requested method is not implemented in this build.
var ErrUnsupportedMethod = errors.New("aio: unsupported io_method")

// NewEngine constructs an Engine using the requested method.
// Empty Method falls back to MethodSync. MethodIOUring returns
// ErrUnsupportedMethod until the io_uring method lands.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Method == "" {
		cfg.Method = MethodSync
	}
	e := &Engine{}
	switch cfg.Method {
	case MethodSync:
		e.method = newMethodSync(e, cfg.MaxConcurrency)
	case MethodWorker:
		workers := cfg.Workers
		if workers <= 0 {
			workers = defaultWorkerCount
		}
		e.method = newMethodWorker(e, workers, cfg.MaxConcurrency)
	case MethodIOUring:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedMethod, cfg.Method)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedMethod, cfg.Method)
	}
	return e, nil
}

// Submit hands an Op to the underlying method. See Method.Submit.
func (e *Engine) Submit(op Op) *Handle {
	e.submitted.Add(1)
	return e.method.Submit(&op)
}

// Close shuts down the engine. Idempotent.
func (e *Engine) Close() error { return e.method.Close() }

// Method reports the configured method name (useful for the
// startup log line and the future pg_aios view).
func (e *Engine) Method() string { return e.method.Name() }

// Stats is a point-in-time snapshot of engine counters. Read-
// only; mutating fields here does not affect the engine.
type Stats struct {
	Method    string
	Submitted uint64
	Completed uint64
	Errored   uint64
	InFlight  int64
}

// Stats returns a coherent counter snapshot. Used by the
// future `pg_aios`-summary surface and tests.
func (e *Engine) Stats() Stats {
	return Stats{
		Method:    e.method.Name(),
		Submitted: e.submitted.Load(),
		Completed: e.completed.Load(),
		Errored:   e.errored.Load(),
		InFlight:  e.inFlight.Load(),
	}
}

// runOp performs the actual ReadAt / WriteAt and returns the
// Result. Centralised so every method shares the same direction
// dispatch and error mapping.
func runOp(op *Op) Result {
	switch op.Direction {
	case DirRead:
		n, err := op.File.ReadAt(op.Buffer, op.Offset)
		return Result{N: n, Err: err}
	case DirWrite:
		n, err := op.File.WriteAt(op.Buffer, op.Offset)
		return Result{N: n, Err: err}
	}
	return Result{Err: fmt.Errorf("aio: unknown direction %d", op.Direction)}
}

// completionBookkeeping is the shared "I/O finished" path.
// Methods call this after runOp so the engine's counters stay
// in sync regardless of which method ran.
func (e *Engine) completionBookkeeping(r Result) {
	e.completed.Add(1)
	if r.Err != nil && !errors.Is(r.Err, io.EOF) {
		e.errored.Add(1)
	}
	e.inFlight.Add(-1)
}
