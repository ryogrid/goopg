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
	"sort"
	"sync"
	"sync/atomic"
	"time"
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

	// Target is a free-form descriptor identifying what this
	// I/O is reading from / writing to (typically the relfile
	// path for storage Ops or the segment path for WAL Ops).
	// Surfaced through `pg_aios.target_desc`. Empty when the
	// caller doesn't supply one — the view renders blank in
	// that case. Mirrors upstream's `pgaio_target_desc`.
	Target string

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

	// id is the engine-assigned monotonic identifier. Used by
	// the in-flight tracking map (and the future pg_aios view)
	// to key off a stable, lookup-friendly value.
	id uint64
	// submittedAt records when Submit registered the handle.
	// Powers the elapsed-time column in pg_aios.
	submittedAt time.Time

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
	method    Method
	inFlight  atomic.Int64
	submitted atomic.Uint64
	completed atomic.Uint64
	errored   atomic.Uint64

	// Per-direction breakdown of the same counters above.
	// `submitted` / `completed` / `errored` are the totals;
	// these are the read / write split. Mirrors upstream's
	// pg_stat_io row shape, which splits by operation.
	readSubmitted  atomic.Uint64
	readCompleted  atomic.Uint64
	readErrored    atomic.Uint64
	writeSubmitted atomic.Uint64
	writeCompleted atomic.Uint64
	writeErrored   atomic.Uint64

	// Per-direction latency observability. SumMicros tracks
	// the cumulative observed latency (now - submittedAt at
	// completion time) so consumers can compute the average
	// latency as SumMicros/Completed. MaxMicros tracks the
	// worst-case sample observed so far, CAS-clamped to
	// monotonic-forward. Both are micros (not nanos) to
	// fit comfortably in uint64 even at high I/O rates.
	readLatencySumMicros  atomic.Uint64
	readLatencyMaxMicros  atomic.Uint64
	writeLatencySumMicros atomic.Uint64
	writeLatencyMaxMicros atomic.Uint64

	// nextID hands out per-Submit identifiers. Monotonic.
	nextID atomic.Uint64

	// inflightMu guards the inflight map. Acquired only on
	// Submit (insert) and on completion (delete) — never on
	// the I/O hot path, which uses atomic counters above.
	inflightMu sync.RWMutex
	inflight   map[uint64]inFlightEntry
}

// inFlightEntry is the per-Op record the engine keeps while a
// handle is outstanding. Compact (no pointers to Op or Buffer
// — those would pin large allocations longer than necessary)
// and copy-cheap so InFlight snapshots stay fast.
type inFlightEntry struct {
	id          uint64
	submittedAt time.Time
	direction   Direction
	length      int
	offset      int64
	target      string
}

// InFlightInfo is the public, copy-on-read view of one
// outstanding Op. Mirrors the columns the future `pg_aios`
// view will surface.
type InFlightInfo struct {
	ID          uint64
	Direction   Direction
	Length      int
	Offset      int64
	SubmittedAt time.Time
	Target      string
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
	e := &Engine{inflight: map[uint64]inFlightEntry{}}
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
	switch op.Direction {
	case DirRead:
		e.readSubmitted.Add(1)
	case DirWrite:
		e.writeSubmitted.Add(1)
	}
	return e.method.Submit(&op)
}

// registerInFlight inserts a freshly-built Handle into the
// inflight map. Called by Methods after building the Handle and
// before kicking off the actual I/O so a Snapshot taken
// concurrently always sees the entry. Returns the id assigned
// so the Method can stamp it on the Handle.
func (e *Engine) registerInFlight(h *Handle, op *Op) {
	id := e.nextID.Add(1)
	h.id = id
	h.submittedAt = time.Now()
	entry := inFlightEntry{
		id:          id,
		submittedAt: h.submittedAt,
		direction:   op.Direction,
		length:      len(op.Buffer),
		offset:      op.Offset,
		target:      op.Target,
	}
	e.inflightMu.Lock()
	e.inflight[id] = entry
	e.inflightMu.Unlock()
}

// finishHandle is the shared completion path: bookkeeping +
// in-flight removal + Handle.finish. Methods call this once
// per Op so the counters / inflight map stay coherent.
func (e *Engine) finishHandle(h *Handle, r Result) {
	e.completed.Add(1)
	isErr := r.Err != nil && !errors.Is(r.Err, io.EOF)
	if isErr {
		e.errored.Add(1)
	}
	// Latency: compute once and stash on both per-direction
	// SumMicros and MaxMicros. Clamps SumMicros at uint64
	// overflow (in practice impossible — would take 18M years
	// at 1B I/Os per second).
	elapsedMicros := uint64(time.Since(h.submittedAt).Microseconds())
	switch h.op.Direction {
	case DirRead:
		e.readCompleted.Add(1)
		if isErr {
			e.readErrored.Add(1)
		}
		e.readLatencySumMicros.Add(elapsedMicros)
		advanceMax(&e.readLatencyMaxMicros, elapsedMicros)
	case DirWrite:
		e.writeCompleted.Add(1)
		if isErr {
			e.writeErrored.Add(1)
		}
		e.writeLatencySumMicros.Add(elapsedMicros)
		advanceMax(&e.writeLatencyMaxMicros, elapsedMicros)
	}
	e.inFlight.Add(-1)
	e.inflightMu.Lock()
	delete(e.inflight, h.id)
	e.inflightMu.Unlock()
	h.finish(r)
}

// advanceMax CAS-clamps p to max(p, v) — monotonic-forward
// without taking a lock. Used by the latency-max accountancy
// in finishHandle.
func advanceMax(p *atomic.Uint64, v uint64) {
	for {
		cur := p.Load()
		if v <= cur {
			return
		}
		if p.CompareAndSwap(cur, v) {
			return
		}
	}
}

// Close shuts down the engine. Idempotent.
func (e *Engine) Close() error { return e.method.Close() }

// Method reports the configured method name (useful for the
// startup log line and the future pg_aios view).
func (e *Engine) Method() string { return e.method.Name() }

// Stats is a point-in-time snapshot of engine counters. Read-
// only; mutating fields here does not affect the engine.
//
// The aggregate counters (Submitted/Completed/Errored) sum the
// per-direction counters; both are exposed so a future surface
// can render either flavor without recomputing.
type Stats struct {
	Method    string
	Submitted uint64
	Completed uint64
	Errored   uint64
	InFlight  int64

	// Per-direction breakdown. Sum of Read* and Write*
	// matches the aggregate counter above.
	ReadSubmitted  uint64
	ReadCompleted  uint64
	ReadErrored    uint64
	WriteSubmitted uint64
	WriteCompleted uint64
	WriteErrored   uint64

	// Per-direction latency observability (cumulative micros
	// of observed completion latency, plus worst-sample max).
	// Average per-direction latency = SumMicros / Completed.
	// Both stay zero when no I/O has completed.
	ReadLatencySumMicros  uint64
	ReadLatencyMaxMicros  uint64
	WriteLatencySumMicros uint64
	WriteLatencyMaxMicros uint64
}

// InFlight returns a sorted copy of every currently-outstanding
// Op's metadata. Sort key is the Op's ID (monotonic by
// submission order) so the future pg_aios view returns rows in
// a stable, oldest-first order. Empty slice when no Ops are
// in flight.
func (e *Engine) InFlight() []InFlightInfo {
	e.inflightMu.RLock()
	defer e.inflightMu.RUnlock()
	out := make([]InFlightInfo, 0, len(e.inflight))
	for _, en := range e.inflight {
		out = append(out, InFlightInfo{
			ID:          en.id,
			Direction:   en.direction,
			Length:      en.length,
			Offset:      en.offset,
			SubmittedAt: en.submittedAt,
			Target:      en.target,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Stats returns a coherent counter snapshot. Used by the
// future `pg_aios`-summary surface and tests.
func (e *Engine) Stats() Stats {
	return Stats{
		Method:         e.method.Name(),
		Submitted:      e.submitted.Load(),
		Completed:      e.completed.Load(),
		Errored:        e.errored.Load(),
		InFlight:       e.inFlight.Load(),
		ReadSubmitted:         e.readSubmitted.Load(),
		ReadCompleted:         e.readCompleted.Load(),
		ReadErrored:           e.readErrored.Load(),
		WriteSubmitted:        e.writeSubmitted.Load(),
		WriteCompleted:        e.writeCompleted.Load(),
		WriteErrored:          e.writeErrored.Load(),
		ReadLatencySumMicros:  e.readLatencySumMicros.Load(),
		ReadLatencyMaxMicros:  e.readLatencyMaxMicros.Load(),
		WriteLatencySumMicros: e.writeLatencySumMicros.Load(),
		WriteLatencyMaxMicros: e.writeLatencyMaxMicros.Load(),
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

