# 01 — `mctx`: PG-style MemoryContext

This is the keystone chapter. It designs the application-level allocator
that every subsequent chapter builds on. The goal is to remove per-statement
allocations from Go's GC heap entirely, mirroring PostgreSQL's `MemoryContext`
discipline (`postgres/src/backend/utils/mmgr/aset.c`, `mcxt.c`).

Cross-references: [[02-datum-pointer-free]] (Datum's `ArenaID` field
references contexts here), [[03-executor-concrete]] (`Slot`, `OpNode`,
`PlanNode` are allocated from `mctx`), [[04-mvcc-procarray]]
(`Snapshot.InProgress` slice is `mctx`-allocated).

## 1. Goal and non-goals

### Goals

- Replace every GC-heap allocation on the per-statement hot path
  (parser, planner, executor, protocol scratch) with a bump allocation
  inside a backend-owned `*Context`.
- Reduce pointer density of the live working set to near zero. Each
  `Context` holds slice-headers of `[]byte` chunks; the GC sees one
  scan root per chunk regardless of how many logical objects live
  inside.
- Provide explicit lifetime boundaries that match SQL semantics
  (session, transaction, statement, expression).
- Stay 100 % within the Go toolchain (no CGO, no Go stdlib `arena`).

### Non-goals

- Replace allocations on the **session** scope (per-connection state,
  prepared-statement plans). These are long-lived and small; the GC
  handles them fine.
- Replace the `shared_buffers` arena (`internal/storage/arena.go`).
  That allocator already produces pointer-free 4 KiB-aligned page
  slabs; it is unaffected.
- Provide thread-safety inside a `*Context`. Each backend owns its
  own context tree exclusively. Concurrent access is a contract
  violation.

## 2. Current state (what we delete)

The existing in-tree allocator is `internal/executor/Arena`
(M0072-0004, design `docs/design/0068-0003-batch-string-arena.md`):

```go
// internal/executor/arena.go:23-37
type Arena struct {
    pages    [][]byte
    cur      int
    pageSize int
    permanent   bool
    registryIdx int32
}
```

API surface: `NewArena(pageSize int) *Arena`, `Allocate(n int) ([]byte, int)`,
`Bytes(offset, length int) []byte`, `Reset()`, `Drop()`. Backed by 64 KiB
pages, no alignment, no typed allocation helpers. A per-operator
ownership model: every `seqScanOp`, `indexScanOp` constructs one in
its `Open()` and drops it in `Close()`.

The companion `internal/executor/arena_registry.go` provides a global
256-slot registry indexed by `int32`, plus a single `permArena` (slot
0, never reset) for process-global literal payloads. It is **inert in
production today**: M0073-0001 (`Datum.arena *Arena`) bumped the
pointer-typed field into the struct but the registry lookup path
(M0075) was deferred.

Limitations of the current code that motivate the rewrite:

1. **No hierarchy** — every arena is independent; there is no parent /
   child relationship matching SQL statement boundaries.
2. **No typed allocation** — callers manually cast `[]byte` to typed
   structs, which is unsafe and proliferates `unsafe.Pointer` use.
3. **Per-operator ownership** — when a sort or hash-join operator
   needs to hold rows across `Next()` calls from a child, the child
   resets its arena and the consumer's references become dangling.
   Today the workaround is `cloneRowOwned` (`internal/executor/datum.go:310+`)
   which allocates on the GC heap; that defeats the purpose.
4. **No alignment helpers** — typed allocation of structs with
   `uint64` fields silently mis-aligns on some platforms.
5. **No `Datum`-pointer-free integration** — `Datum.arena *Arena` is
   still a pointer; the GC scans it on every cycle.

The new package `internal/mctx` retires all the above. Migration is
described in §8.

## 3. Hierarchy

Three primary tiers, plus an optional fourth, mirroring PG:

```
SessionContext   (per-connection; created at serveConn, dropped at disconnect)
└── TxnContext   (per-transaction; created at BEGIN/implicit-begin,
    │                                 freed at COMMIT/ROLLBACK)
    └── StmtContext  (per-simple-query / per-extended-Execute;
        │             created in dispatch, freed at end-of-statement)
        └── ExprContext   (optional; reset every N rows for per-row
                            scratch; mirrors PG ExprContext.ecxt_per_tuple_memory)
```

Lifetime contracts:

- A `*Context`'s allocations are valid until **the same context** is
  `Reset()` or `Release()`d.
- A child's `Reset` does not affect the parent; a parent's `Reset`
  cascades to all children.
- Children are owned by the parent's slab; their `chunks` slice is a
  sub-range of the parent's free space when the parent has slack.
- `Release()` returns the chunks to the per-class `sync.Pool` for
  reuse on the next `Acquire(parent, kind)` call.

PG reference: `MemoryContextInit`, `AllocSetContextCreate`,
`MemoryContextReset`, `MemoryContextDelete` in
`postgres/src/backend/utils/mmgr/mcxt.c` (`MemoryContextInit` around
line 342, `AllocSetContextCreate` exported in `aset.c`) and
`aset.c:441` (chunk-class allocator).

## 4. Backing allocator: slab + bump

### Chunk layout

A `Context` owns a slice of `chunk` records:

```go
type chunk struct {
    buf []byte    // backing array; len(buf) == cap(buf) == chunkSize
    // No other fields; the bump pointer lives on Context.
}

const (
    defaultChunkSize = 64 * 1024   // 64 KiB, matches current Arena
    smallChunkSize   = 4 * 1024    // for short-lived ExprContext
    largeChunkSize   = 256 * 1024  // for sort/hash-join build sides
)
```

The 64 KiB default matches the existing `arenaPageSize` (`internal/
executor/arena.go:42`); tuning is per-context-class.

### Bump pointer

The `Context` holds `head int` (index into `chunks`) and `offset uint32`
(bytes consumed in `chunks[head].buf`). `Alloc(n)` performs:

```
1. If chunks[head].buf has cap-offset >= n+align:
       slice = chunks[head].buf[offset : offset+n]
       offset += n+pad
       return slice
2. Else if n > chunkSize:
       allocate dedicated chunk of size n, splice it AFTER head
       (preserves the small-chunk tail), return its buf
3. Else:
       grow chunks by one (or reuse a pooled chunk), advance head
       to the new chunk, retry step 1.
```

This matches the existing `Arena.Allocate` pattern (`arena.go:72-122`)
with the addition of explicit alignment. The growth strategy is the
"insert after head" pattern from the current Arena, preserving the
ability of subsequent small allocations to continue using the existing
chunk.

### Reset

```
Reset():
    For i in range chunks:
        chunks[i].buf = chunks[i].buf[:0]  // preserves backing array
    head, offset = 0, 0
    gen++                                  // for weak-ref validation
```

Backing arrays are kept; the next allocation reuses them. This matches
the existing `Arena.Reset` and is the dominant cost-saver: a steady-
state pgbench workload allocates the chunk set once during the first
few transactions and never `make([]byte, ...)`s again.

### Release

```
Release():
    For i in range chunks:
        chunks[i].buf = chunks[i].buf[:0]
    chunkPool[sizeClass].Put(chunks)
    *c = Context{}                         // zero out, mark invalid
```

`chunkPool` is a per-size-class `sync.Pool` keyed by `chunkSize`. Pool
discipline: only `Release` puts back; `Acquire` gets, falls back to
`make([]chunk, 0, 4)` and per-chunk `make([]byte, 0, chunkSize)`.

## 5. API surface (concrete Go signatures)

```go
package mctx

// ContextID is a dense 16-bit handle into ctxRegistry. Lets Datum
// reference a context without holding a Go pointer (so Datum stays
// pointer-free for GC purposes).
type ContextID uint16

const (
    InvalidContextID ContextID = 0
    PermContextID    ContextID = 1   // process-global literals; never reset
)

type Kind uint8

const (
    KindInvalid Kind = iota
    KindSession
    KindTxn
    KindStmt
    KindExpr
)

type Context struct {
    parent *Context
    chunks []chunk
    head   int32
    offset uint32

    chunkSize uint32
    id        ContextID
    gen       uint32   // bumped by Reset; debug weak refs verify equality
    kind      Kind

    // children []*Context  // for cascading Reset/Release; appended on Acquire
    children []*Context
}

// Acquire returns a context of the given kind, parented to parent
// (or nil for the root SessionContext). It draws chunks from the
// per-class pool; first-call cost is one make() per chunk, steady-state
// cost is one slice copy.
func Acquire(parent *Context, kind Kind) *Context

// Alloc returns n bytes of zero-initialised storage owned by c. The
// returned slice aliases c's backing storage; callers MUST NOT retain
// it past c.Reset() or c.Release().
func (c *Context) Alloc(n int) []byte

// AllocAligned returns aligned storage. align must be a power of two
// in [1, 64]. The implementation pads offset to the alignment before
// bumping.
func (c *Context) AllocAligned(n, align int) []byte

// AllocString copies s into c and returns an (offset, length) pair
// resolvable via c.Bytes. The offset is opaque to the caller; it is
// the logical offset within c's chunk stream (chunk i begins at
// i*chunkSize), matching the existing Arena.Allocate offset
// semantics.
func (c *Context) AllocString(s string) (offset, length uint32)

// AllocBytes copies b into c and returns (offset, length).
func (c *Context) AllocBytes(b []byte) (offset, length uint32)

// Bytes resolves a previously-returned (offset, length) into a slice
// aliasing c's backing storage. Returns nil if length == 0. The
// returned slice is invalidated by c.Reset() / c.Release().
func (c *Context) Bytes(offset, length uint32) []byte

// Reset truncates every chunk to zero length, rewinds the bump
// pointer, and recursively Resets every child. Backing arrays are
// retained for the next allocation cycle. gen is bumped.
func (c *Context) Reset()

// Release returns c's chunks to the per-class pool, cascades Release
// to children, and clears c's slot in ctxRegistry. After Release, c
// must not be used.
func (c *Context) Release()

// ID returns c's registry slot.
func (c *Context) ID() ContextID

// Generation returns c's current generation counter; used by debug
// weak-ref code to detect use-after-Reset.
func (c *Context) Generation() uint32

// AllocFor returns *T allocated from c. The returned pointer is valid
// until c.Reset() or c.Release(). T must not contain GC-traced fields
// that point outside c, or the pointer-free invariant is violated.
func AllocFor[T any](c *Context) *T

// AllocSlice returns []T of length n allocated from c.
func AllocSlice[T any](c *Context, n int) []T

// Lookup returns the Context for the given id, or nil if id is
// invalid / has been Release()d. Used by Datum.StringValue /
// BytesValue to resolve payload bytes.
func Lookup(id ContextID) *Context
```

### Typed allocation via generics

`AllocFor[T any](c *Context) *T` implementation:

```go
func AllocFor[T any](c *Context) *T {
    var zero T
    size := int(unsafe.Sizeof(zero))
    align := int(unsafe.Alignof(zero))
    buf := c.AllocAligned(size, align)
    return (*T)(unsafe.Pointer(&buf[0]))
}
```

The cast is safe because `buf[0]` resides inside `c`'s chunk backing
array, which is live as long as `c` is alive. Callers must respect the
lifetime contract: returning a `*T` past `c.Reset()` is a use-after-
free.

**Keep-alive discipline (practice doc §5).** Every backend's context
tree (session → txn → stmt) is reachable from the backend's serving
struct (`internal/server/backend.go`); that struct is itself
reachable from the backend goroutine's stack throughout the
connection's lifetime. Therefore the chain `goroutine → backend →
sessionCtx → ... → chunks → []byte` keeps the chunk-backing arrays
live for the whole connection: a Go GC cannot reclaim the backing
slab while any descendant `*T` is held. Code paths that hand a `*T`
to a different goroutine (e.g., the WAL writer worker reading a
slot's bytes by index) are by contract synchronous within the
backend's lifetime and do not require an additional
`runtime.KeepAlive`. The single exception is the per-P xid cache in
[[08-runtime-internals]], whose backing slab is the global `XidGen`
struct and whose lifetime is the process — no KeepAlive needed.
Debug-build `mctxProbe` (see §7) verifies this contract at runtime.

`AllocSlice[T any]` is analogous, returning `unsafe.Slice((*T)(...), n)`.

### Pointer-free guarantee — and acknowledged GC roots per Context

The `chunks []chunk` field is a slice of structs, each containing one
`[]byte`. The Go GC scans each `[]byte`'s slice header (one pointer
each), not the bytes themselves. A `Context` with ~10 chunks scanned
once per GC cycle contributes 10 pointer reads. Compared to today's
per-statement allocation rate of ~19 KB / SELECT × `unsafe.Sizeof(Datum)
× 50 columns × 3 pointers/Datum = many thousands of pointer reads per
query, the savings are >100×.

Critically, the bytes *inside* the chunks may store anything (typed
struct allocations via `AllocFor`, AST nodes, plan nodes, Datum payload
bytes) — the GC does **not** descend into them because the slice
element type is `byte` (a non-pointer type). This is the central
trick of the design.

**Acknowledged GC roots per Context** (for completeness, since later
chapters claim "Datum is pointer-free, ProcArray is pointer-free,
bufmap is pointer-free" — `Context` itself is not aspirationally
pointer-free; these are the deliberate exceptions):

| Field            | Type            | Per-cycle scan cost  | Justification                                                              |
|------------------|-----------------|----------------------|----------------------------------------------------------------------------|
| `parent`         | `*Context`      | one pointer per ctx  | Cascade `Reset`/`Release`; cold path                                       |
| `chunks`         | `[]chunk`       | one slice header     | Chunks carry `[]byte` (pointer-free); GC reads slice headers only          |
| `chunks[i].buf`  | `[]byte`        | one slice header per chunk | Pointer-free bytes inside; GC stops at the byte boundary             |
| `children`       | `[]*Context`    | one slice header + N pointers | Cascade `Reset`/`Release`; cold path; size bounded by max children   |

A live backend with `session → txn → stmt → expr` = 4 contexts ×
(2 pointer fields + ~3 chunk slice headers) ≈ 20 pointer reads per
backend per GC cycle. At 100 backends ≈ 2 000 reads. Compare today's
per-Datum scan cost of ~10⁶ pointers per second; the residual cost is
in the noise.

## 6. Registry

```go
var (
    ctxRegistry [65536]*Context  // index by ContextID
    ctxRegistryFreeList []ContextID
    ctxRegistryMu sync.Mutex      // protects free-list; cold path
)
```

- Slot 0 (`InvalidContextID`) is reserved.
- Slot 1 (`PermContextID`) holds the process-global `permanentContext`,
  created at init, never `Reset` or `Release`d. It holds literal
  string/bytes Datum payloads from parsed SQL constants and one-time
  catalog setup. Mirrors today's `permArena` (`internal/executor/
  arena_registry.go:48-64`).
- Slots 2..65535 are claimed via `Acquire` and returned via `Release`.
  Claim is a `sync.Mutex`-protected pop from `ctxRegistryFreeList`;
  if empty, the next monotonic ID is assigned up to 65536.

ID space sizing: 65 535 live contexts is far more than the worst-case
need. With `max_connections=100`, ~5 contexts per backend at any time
(session, txn, stmt, two expr siblings), we expect ~500 simultaneously
live IDs. Recycling on Release keeps usage steady-state.

[[02-datum-pointer-free]] stores the `ContextID` (2 bytes) directly in
the `Datum` struct, which eliminates the `*Arena` pointer in today's
Datum and makes the struct fully pointer-free.

## 7. Lifetime contract & weak-ref validation

The fundamental rule: **any pointer or slice obtained from a `*Context`
is invalidated when that context is `Reset()` or `Release()`d.**

Two enforcement layers:

1. **Code review and documentation.** Every API site that takes a
   `*mctx.Context` argument states the lifetime. Most callers obtain
   a context, allocate within it, and never store the pointers past
   the function return.
2. **Debug-build weak references.** In debug builds (`//go:build mctxdebug`),
   `AllocFor`/`AllocSlice` returns a wrapper that remembers
   `(ContextID, gen)`. Any dereference checks `ctxRegistry[id].gen
   == storedGen`; if not, a panic with a stack trace fires. Production
   builds (no tag) compile the wrapper down to a plain pointer.

The `gen` counter is `uint32`, bumped on every `Reset`. Wraparound
after 4 billion resets is treated as practically impossible (one
statement per microsecond for ~136 hours).

## 8. Backend wiring

The lifecycle is threaded through the existing server code:

1. **`internal/server/server.go::serveConn`** — at connection start,
   after PID allocation but before the auth handshake, call
   `sessionCtx := mctx.Acquire(nil, mctx.KindSession)`. Store
   `sessionCtx` on the backend's serving struct. Defer
   `sessionCtx.Release()` on connection teardown. (Symbol reference,
   not a line number: `serveConn` lives at `server.go:563+` in the
   current tree, but the hook lands wherever the activity registration
   happens today — implementer should use the symbol, not a stale
   line.)
2. **`internal/server/dispatch.go::executeOneSimpleStmt`** — at
   statement entry, `stmtCtx := mctx.Acquire(sessionCtx,
   mctx.KindStmt)`. After the statement completes (success or error),
   `stmtCtx.Release()`. The existing `executor.Context` struct gains
   a `Mctx *mctx.Context` field threaded through to every Open / Next /
   Close call.
3. **`internal/server/dispatch.go::handleBegin/handleCommit`** — at
   `BEGIN`, `txnCtx := mctx.Acquire(sessionCtx, mctx.KindTxn)`. At
   `COMMIT`/`ROLLBACK`, `txnCtx.Release()`. The TxnContext becomes
   the parent of all StmtContexts within the transaction; on
   transaction end every per-statement allocation is reclaimed in
   one cascade.
4. **Parser entry** — `parser.Parse(buf []byte, ctx *mctx.Context)
   ([]Stmt, error)`. AST nodes are `mctx.AllocFor[BeginStmt](ctx)`
   etc. The existing `tokenSlicePool` / `parserPool` are deleted;
   the parser's internal scratch state is itself allocated from a
   short-lived `ExprContext` inside `ctx`.
5. **Planner entry** — `planner.Plan(stmt parser.Stmt, cat catalog.Catalog,
   ctx *mctx.Context) (planner.Node, error)`. Plan nodes allocated
   from `ctx`. The 65 concrete plan-node types collapse into the
   `PlanNode` sum-type per [[03-executor-concrete]].
6. **Executor entry** — `executor.Build(plan planner.Node, ec *executor.Context)
   *executor.OpRoot`. OpNodes allocated from `ec.Mctx`. The per-row
   pipeline allocates **nothing** from the GC heap; row payloads
   live in the operator-local sub-region of the StmtContext.
7. **Per-row scratch** — operators that need per-tuple scratch (e.g.,
   `filter` evaluating an expression that allocates a varlena) call
   `exprCtx := mctx.Acquire(ec.Mctx, mctx.KindExpr)` once at `Open`,
   `exprCtx.Reset()` before each `Next`, `exprCtx.Release()` at
   `Close`. This is PG's `ExprContext` pattern, sized at the row
   level.

The thread-of-ownership invariant is:

```
backend goroutine
└── sessionCtx (Acquire at serveConn entry, Release at exit)
    └── txnCtx (Acquire at BEGIN, Release at COMMIT/ABORT)
        └── stmtCtx (Acquire at executeOneSimpleStmt, Release at exit)
            ├── ASTs, plan tree, operator tree
            └── per-row exprCtx (Reset between rows; Release at op Close)
```

Cross-goroutine sharing is illegal; the `gen`-counter debug check
catches mistakes.

## 9. Allocation cost model

The existing Arena allocates one 64 KiB chunk per ~3 200 small strings
(20-byte average). The new mctx pays the same chunk cost amortised
across all per-statement allocations: AST nodes, plan nodes, Datum
payload bytes, scratch slices, intermediate row data. At 19 KB / SELECT
(`03-memory-and-allocs.md` §3.2), one chunk per ~3.4 statements. With
chunk reuse via the per-class pool, steady-state `make([]byte, ...)`
calls drop to near zero after the first few transactions.

GC scan cost: a backend with a session + one txn + one stmt + one expr
= 4 contexts, each ~3 chunks = 12 chunk slice-headers = 12 pointer
reads per GC cycle per backend. With 100 backends = 1 200 reads.
Compare today's per-Datum scan cost: ~150 pointers per row × 6 400
rows/sec at c=100 SO = ~1 million pointers/sec.

## 10. Verification

After Phase A of [[09-migration-and-rollout]] ships:

- **Compile-time** — `internal/executor/arena.go` and `arena_registry.go`
  no longer exist; `grep -RIn 'executor.Arena\|executor.NewArena' internal/`
  returns zero hits.
- **Sizeof** — `unsafe.Sizeof(mctx.Context{})` is documented (target:
  ≤ 96 B) and asserted in `internal/mctx/types_test.go`.
- **GC behaviour** — re-run `analysis/perf-optimize/scripts/run_perf_suite.sh`;
  capture c=10 SO CPU profile; expect `gcBgMarkWorker cum%` to drop
  from 63.3 % to **< 40 %** at Phase A (only mctx in place;
  Datum-pointer-free is Phase B). Final target (after Phase B)
  is **< 15 %**.
- **Heap-allocs profile** — `dispatchSimpleQueryViaExecutor` cum%
  in `allocs.pb.gz` drops from 13.9 % to **< 3 %** at c=10 SO.
- **Leak test** — a long-running pgbench (1 hour) shows inuse heap
  stable within ±5 % of the static `shared_buffers`; the chunk pool
  amortises growth.
- **Test suite** — full `go test ./...` passes; TPC-H regression
  suite passes (no row-count or wall-clock regression > 10 %).

[[02-datum-pointer-free]] depends on this chapter; the `ContextID`
field there resolves to a context in this registry.
