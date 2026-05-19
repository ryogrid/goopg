# 04 — MVCC ProcArray + Atomic XidGen + CLOG Bank Locks

This chapter replaces `internal/mvcc/Manager.mu` — a single
`sync.Mutex` that gates Begin / SnapshotFor / Commit / OldestXmin /
finish — with three independent concurrency primitives modelled on
PostgreSQL: **ProcArray** (per-backend slot array, lock-free reads),
**XidGen** (atomic counter), and **CLOG bank locks** (per-bank RWMutex).

Cross-references: [[01-memory-context]] (Snapshot.InProgress is mctx-
allocated), [[05-activity-perbackend]] (the activity slot array
parallels ProcArray; same `procNum` indexes both), [[08-runtime-
internals]] (per-P xid cache amortises XidGen contention).

## 1. Current state

Verbatim from `internal/mvcc/manager.go:71-104`:

```go
type Manager struct {
    mu         sync.Mutex        //  ← ONE mutex for all snapshot/commit/abort traffic
    nextXID    storage.TransactionID
    nextHandle TxnHandle
    active     map[TxnHandle]*txState
    xactMarker func(storage.TransactionID, XactMarker) error
    commitCond *sync.Cond
    abortedXIDs []storage.TransactionID
    subxactFields
    ssiState ssiState
    predicateLocks predicateLocksRegistry
}

type txState struct {
    isolation     IsolationLevel
    firstSnapshot *Snapshot
    xid           storage.TransactionID
    snapshotXmin  storage.TransactionID
}
```

All of `Begin` (line 185), `SnapshotFor` (line 263), `Commit` (line
305), `OldestXmin` (line 359), `finish` (line 374) acquire `m.mu`. The
critical section also walks `active` (a `map[TxnHandle]*txState` with
`*txState` pointer values) and `abortedXIDs` (a sorted slice).

Evidence from `analysis/perf-optimize/04-contention.md` §4.2 (c=50
simple-update mutex delta, after `pprof -base`):

```
5409.62 s  99.82 %  sync.(*Mutex).Unlock
↳ 5012.00 s  92.48 %  mvcc.(*Manager).Commit / finish
↳  308.53 s   5.69 %  mvcc.(*Manager).SnapshotFor
↳   29.57 s   0.55 %  mvcc.(*Manager).Begin
↳   29.37 s   0.54 %  mvcc.(*Manager).OldestXmin
```

And block delta:

```
1.60 hrs  83.21 %  sync.(*Mutex).Lock
↳ 1.21 hrs  62.73 %  mvcc.(*Manager).SnapshotFor
↳ 0.13 hrs   6.97 %  mvcc.(*Manager).Begin
↳ 0.12 hrs   6.23 %  mvcc.(*Manager).Commit
```

The single mutex is the dominant write-side bottleneck. PG splits the
same four concerns across three lock classes; we adopt that split.

Pointer-typed fields in current Manager that contribute to GC scan:
`active map[TxnHandle]*txState` (map with pointer values),
`commitCond *sync.Cond`, `xactMarker func(...) error` (closure with
captured state), `firstSnapshot *Snapshot` per txState. Post-refactor
ProcArray retains the slab as a pointer-free `[]procSlot`.

## 2. Target architecture

Three independent concerns, separately protected:

1. **`internal/mvcc/procarray.go`** — `ProcArray`: per-backend slot
   array; lock-free SnapshotFor / OldestXmin walks; CAS-based
   slot claim/release.
2. **`internal/mvcc/xidgen.go`** — `XidGen`: atomic uint64 counter +
   optional per-P prefetch cache ([[08-runtime-internals]]).
3. **`internal/mvcc/clog.go`** (rewritten) — bank-locked SLRU-style
   CLOG: one `sync.RWMutex` per `xidsPerBank` xids; bank file format
   stays PG-compatible (2 bits per xid).

`mvcc.Manager` becomes a thin facade that holds pointers to the three
sub-systems plus the SSI / predicate-lock state (which is cold-path
and tolerable as-is). The public method set (`Begin`, `SnapshotFor`,
`Commit`, `Abort`, `OldestXmin`) is preserved so caller-side migration
is mechanical.

## 3. ProcArray

### Slot layout

```go
// internal/mvcc/procarray.go (new)

type procSlot struct {
    // All access is via atomic primitives. 64-byte cache-line
    // alignment ensures no false sharing between slots.
    state            atomic.Uint32   // bit-packed: see below
    xid              atomic.Uint64   // current top-level xid, 0 if none
    xmin             atomic.Uint64   // backend's xmin horizon, MaxUint64 if none
    procNum          int32           // dense backend ID; matches activity slot
    // First-snapshot cache (RR / SERIALIZABLE; see §8). Pointer-free
    // (offset/length into the owning txnCtx). Resolved via
    //   mctx.Lookup(txnCtxID).Bytes(firstSnapshotOff, firstSnapshotLen)
    // Zero when no first snapshot is cached (RC isolation, or no txn).
    txnCtxID         uint16          // mctx.ContextID of owning txnCtx
    firstSnapshotOff uint32          // offset within that ctx's chunk stream
    firstSnapshotLen uint32          // length of the cached Snapshot blob
    _pad             [26]byte        // pad to 64 B (4+8+8+4+2+4+4 = 34; 26 fills to 64)
}
// unsafe.Sizeof(procSlot{}) == 64; asserted in internal/mvcc/procarray_test.go.

// procSlot.state bit layout:
//   bit  0       inUse           (claimed by a backend)
//   bit  1       hasXid          (xid field is valid)
//   bit  2       readOnly        (no writes; xid stays 0)
//   bit  3..7    reserved
//   bit  8..23   isolationLevel  (RC=1, RR=2, SERIALIZABLE=3)
//   bit 24..31   generation      (bumped on each claim; ABA guard)

const (
    flagInUse    uint32 = 1 << 0
    flagHasXid   uint32 = 1 << 1
    flagReadOnly uint32 = 1 << 2
)

type ProcArray struct {
    slots []procSlot   // len == maxBackends; allocated once at server start
}
```

`unsafe.Sizeof(procSlot{}) == 64`. The array is allocated once at
server start with `len = max_connections + reservedSlots`, indexed by
the backend's `procNum` (assigned at connect, released at disconnect;
see §6). Pointer-free.

### Lifecycle methods

```go
// Acquire claims a free slot. Called once per backend at connection
// start. Returns the slot's procNum.
func (pa *ProcArray) Acquire(isolation IsolationLevel) (procNum int32) {
    for i := range pa.slots {
        s := &pa.slots[i]
        old := s.state.Load()
        if old & flagInUse != 0 {
            continue
        }
        new := (old &^ 0xFFFFFFFF) | flagInUse | (uint32(isolation) << 8)
        new += 1 << 24   // bump generation
        if s.state.CompareAndSwap(old, new) {
            return int32(i)
        }
    }
    panic("mvcc: ProcArray exhausted")
}

// Release frees the slot. Called at connection end (deferred at
// serveConn entry).
func (pa *ProcArray) Release(procNum int32) {
    s := &pa.slots[procNum]
    s.xid.Store(0)
    s.xmin.Store(math.MaxUint64)
    // Clear flags, keep generation:
    cur := s.state.Load()
    next := (cur & 0xFF000000)  // preserve gen, drop inUse and isolation
    s.state.Store(next)
}
```

### Begin

```go
// Begin marks the slot as having a top-level xid. xidgen.Allocate
// gives the new xid. Returns the Transaction handle (a value type).
func (m *Manager) Begin(procNum int32) Transaction {
    xid := m.xidgen.Allocate()
    s := &m.procArray.slots[procNum]
    s.xid.Store(uint64(xid))
    // Set the hasXid flag:
    for {
        old := s.state.Load()
        new := old | flagHasXid
        if s.state.CompareAndSwap(old, new) { break }
    }
    return Transaction{ProcNum: procNum, XID: xid, Isolation: m.isolationOf(s)}
}
```

For read-only transactions, the xid is **not allocated** until the
first write; the slot keeps `xid = 0` and `flagHasXid = 0`. This
mirrors PG's `GetNewTransactionId` lazy assignment and reduces XidGen
contention to write transactions only.

### SnapshotFor — lock-free walk

The walk's correctness argument deserves an explicit statement: a
concurrent `Begin` from another backend may complete its
`xidgen.Allocate` after our `Peek()` snapshot of Xmax. That new xid is
above our Xmax horizon and is correctly excluded from our snapshot
(by the `xid >= snap.Xmax` test in the loop). Conversely, a concurrent
`Commit` may clear `flagHasXid` between our `state.Load()` and
`xid.Load()`; we may include the just-committed xid in our in-progress
list, but visibility checks for that xid will consult CLOG via
`clog.GetStatus`, observe `XactCommitted`, and treat it as visible —
matching PG's `GetSnapshotData` + `XidInMVCCSnapshot` two-stage check.

```go
// SnapshotFor builds a Snapshot for procNum's pending statement.
// O(maxBackends) with no mutex; only atomic loads.
func (m *Manager) SnapshotFor(procNum int32, mc *mctx.Context) *Snapshot {
    snap := mctx.AllocFor[Snapshot](mc)
    snap.Xmin = math.MaxUint64
    snap.Xmax = m.xidgen.Peek()       // current "next" xid; everything below could be in flight

    // Walk all slots:
    inflight := mctx.AllocSlice[storage.TransactionID](mc, 0)   // grows in mctx
    for i := range m.procArray.slots {
        s := &m.procArray.slots[i]
        st := s.state.Load()
        if st & flagInUse == 0 || st & flagHasXid == 0 {
            continue
        }
        xid := storage.TransactionID(s.xid.Load())
        if xid == 0 || xid >= snap.Xmax {
            continue   // ungenerated or after snap horizon
        }
        if xid < snap.Xmin {
            snap.Xmin = xid
        }
        inflight = append(inflight, xid)
    }
    snap.InProgress = inflight
    // Write xmin back to our slot for OldestXmin readers:
    m.procArray.slots[procNum].xmin.Store(uint64(snap.Xmin))
    return snap
}
```

PG calls this pattern "the dense xid array walk"
(`postgres/src/backend/storage/ipc/procarray.c::GetSnapshotData` —
the function header lives around line 2179 in PG 18.3, with the
"dense xid array" loop body slightly below; PG 14+ uses `pgxactoff[]`).
The walk is read-only against the slot
array; concurrent Begin/Commit only update individual slots atomically.
A snapshot may include an xid that committed mid-walk (caller treats
it as in-progress, will reread CLOG when checking visibility — same
as PG behaviour).

### Commit / Abort

```go
func (m *Manager) Commit(t Transaction) error {
    s := &m.procArray.slots[t.ProcNum]
    if t.XID != 0 {
        if err := m.clog.SetStatus(t.XID, XactCommitted); err != nil {
            return err
        }
    }
    // Clear xid and hasXid; preserve inUse and isolation:
    s.xid.Store(0)
    for {
        old := s.state.Load()
        new := old &^ flagHasXid
        if s.state.CompareAndSwap(old, new) { break }
    }
    return nil
}

func (m *Manager) Abort(t Transaction) error {
    s := &m.procArray.slots[t.ProcNum]
    if t.XID != 0 {
        if err := m.clog.SetStatus(t.XID, XactAborted); err != nil {
            return err
        }
    }
    s.xid.Store(0)
    for {
        old := s.state.Load()
        new := old &^ flagHasXid
        if s.state.CompareAndSwap(old, new) { break }
    }
    return nil
}
```

No mutex. The CLOG SetStatus takes a per-bank RWMutex (§5); ProcArray
slot updates are atomic.

### OldestXmin

```go
func (m *Manager) OldestXmin() storage.TransactionID {
    min := storage.TransactionID(math.MaxUint64)
    for i := range m.procArray.slots {
        s := &m.procArray.slots[i]
        if s.state.Load() & flagInUse == 0 {
            continue
        }
        x := storage.TransactionID(s.xmin.Load())
        if x < min { min = x }
    }
    return min
}
```

Lock-free read. No contention with Begin/Commit.

### Re-entrance / sub-transactions / SSI

The current `subxactFields` (subtransaction tracking) and `ssiState`
(SERIALIZABLE bookkeeping) remain on `Manager`, but their per-backend
data is moved onto the procSlot via mctx-resident offsets when slices
are needed (e.g., savepoint stack). The detail is implementation-time,
not design-time; the invariant is **no shared mutex in the per-xact
hot path**.

## 4. XidGen

```go
type XidGen struct {
    next atomic.Uint64   // monotonic next xid
}

func (g *XidGen) Allocate() storage.TransactionID {
    return storage.TransactionID(g.next.Add(1))
}

func (g *XidGen) Peek() storage.TransactionID {
    return storage.TransactionID(g.next.Load())
}
```

Atomic `Add` is enough at the rates we're seeing (~10 K xacts/sec at
c=100 SU when uncontended). PG uses `XidGenLock` (LWLock); we use an
atomic. At higher write rates the per-P xid cache from [[08-runtime-
internals]] amortises further.

PG counterpart: `postgres/src/backend/access/transam/varsup.c:77
GetNewTransactionId`.

## 5. CLOG bank locks

```go
type CLog struct {
    path  string
    banks []clogBank   // len == ceil(maxXid / xidsPerBank)
}

type clogBank struct {
    mu   sync.RWMutex   // per-bank; covers reads + writes to this bank
    data []byte         // 32 KiB; covers `xidsPerBank` consecutive xids
    dirty bool
}

const xidsPerBank = 32 * 1024 * 4   // 4 xids per byte (2 bits per xid)
```

`SetStatus(xid, status)` selects bank `(xid / xidsPerBank)`, takes
`bank.mu.Lock()`, sets the 2 bits, marks `dirty`. `GetStatus(xid)`
takes `bank.mu.RLock()`, reads the 2 bits.

The current `internal/mvcc/clog.go:27-31`:

```go
type CLog struct {
    mu   sync.RWMutex
    path string
    data []byte
}
```

— uses a single RWMutex over all of `data`. Splitting into banks
matches PG's per-bank SLRU lock pattern (`postgres/src/backend/access/
transam/slru.c::SimpleLruGetBankLock`; the per-bank lock-granularity
work that the CLOG cache builds on lives in `slru.c`, with CLOG-
specific glue in `clog.c::TransactionIdSetStatusBit`). The on-disk
**file format is unchanged** (2 bits per xid, sequential bytes).
Bank count is computed at startup from the current max xid; banks
grow on demand.

Bank count sizing: at the default `xidsPerBank = 131 072`, a CLOG
covering 1 billion xids needs ~7 600 banks. Each bank's `mu sync.RWMutex`
is ~24 bytes; banks slice = ~200 KB metadata, negligible.

## 6. Backend wiring

The slot ownership lives on each backend's serving struct (an
extension of the existing `internal/server` per-connection state):

```go
// internal/server/backend.go (extension)
type backend struct {
    procNum   int32                // assigned at connect; index into ProcArray + ActivityRegistry
    sessionCtx *mctx.Context       // from [[01-memory-context]]
    txnCtx    *mctx.Context        // nil outside transactions
    stmtCtx   *mctx.Context        // nil between statements
    txn       Transaction          // current top-level Transaction handle
    // ... existing fields
}
```

`backend.procNum` is allocated in `serveConn` after the auth handshake:

```go
b.procNum = mvccManager.procArray.Acquire(isolationFromGUC())
defer mvccManager.procArray.Release(b.procNum)
```

The same `procNum` indexes [[05-activity-perbackend]]'s `ActivitySlot`
array, so the backend has one identity across mvcc + activity. This
mirrors PG's `PGPROC` slot owning both proc-array state and wait-event
state.

The current `mvcc.TxnHandle` opaque integer is replaced by the
`Transaction` value type. Public method signatures preserve names but
swap arguments:

```go
// Before:
func (m *Manager) Begin(...) TxnHandle
func (m *Manager) SnapshotFor(h TxnHandle) *Snapshot
func (m *Manager) Commit(h TxnHandle) error

// After:
func (m *Manager) Begin(procNum int32, iso IsolationLevel) Transaction
func (m *Manager) SnapshotFor(t Transaction, mc *mctx.Context) *Snapshot
func (m *Manager) Commit(t Transaction) error
```

Callers in `internal/executor`, `internal/server`, `internal/vacuum`
update to thread `procNum` through. The work is mechanical (~25 call
sites per the explore inventory).

## 7. Lock-free correctness arguments

### SnapshotFor

The walk reads each slot's `state` and `xid` atomically. A concurrent
`Begin` may set `flagHasXid` between our `state.Load()` and `xid.Load()`
— we either see the new xid (and include it correctly) or miss it (and
treat it as not yet in progress, also correct because the begin happened
"after" us logically). A concurrent `Commit` may clear `flagHasXid`
between our two loads — we might still include the just-committed xid
in the in-progress list, but visibility check via CLOG will return
`XactCommitted` and the caller will re-evaluate. This is **exactly PG
behaviour** (PG's `GetSnapshotData` walks `procArray` under a shared
LWLock and tolerates the same race against `EndTransaction`).

The `gen` bits in `state` guard against ABA on slot reuse: a backend
that releases slot 7 and reclaims it later increments generation, so
any stale snapshot that read slot 7's `xid` during the old occupancy
sees a new `gen` value if it rechecks, and discards the entry.

### OldestXmin

The walk reads each slot's `xmin`. A concurrent `SnapshotFor` writes
the slot's xmin after the walk. We may compute an OldestXmin that is
slightly newer than the true min (because the concurrent SnapshotFor
hadn't published its xmin yet) — that is **conservative**: vacuum will
just leave a few extra dead tuples for one cycle. PG behaviour matches.

### Begin / Commit

Atomic CAS on `state`, atomic stores on `xid`, atomic stores on `xmin`.
No multi-word invariants that need a mutex. The CLOG `SetStatus` takes
the per-bank lock; that is bounded and never escalates to a manager-
wide lock.

## 8. Snapshot allocation from mctx

`Snapshot` is allocated from the caller's `mctx.Context` (typically
`stmtCtx`):

```go
type Snapshot struct {
    Xmin       storage.TransactionID
    Xmax       storage.TransactionID
    InProgress []storage.TransactionID   // mctx-allocated slice
}
```

`Snapshot.InProgress` is `mctx.AllocSlice[TransactionID]`; it grows
within the stmt context's chunks via standard `append` semantics. The
caller is expected to use the snapshot only within the lifetime of
the context.

For first-snapshot caching under RR/SERIALIZABLE (the txState
`firstSnapshot *Snapshot` field today), we allocate from `txnCtx` so
the snapshot survives statement boundaries. The slot's
`firstSnapshotOff uint32` field on procSlot encodes the (offset,
length) into the txnCtx — pointer-free.

## 9. Manager method names (preserved for caller compatibility)

```go
package mvcc

type Manager struct {
    procArray *ProcArray
    xidgen    *XidGen
    clog      *CLog
    // SSI / predicate locks each carry their own private mutex; they
    // are not protected by a manager-wide lock. The pre-refactor
    // discipline ("Manager.mu covers SSI/predlock") is replaced by
    // ssiState.mu (private to ssiState) and predicateLocks.mu
    // (private). SERIALIZABLE isolation already routes through these
    // narrower locks today; the change is internal renaming. Cold
    // path: SERIALIZABLE workloads are rare in pgbench and our
    // optimisation target; the per-subsystem locks are sized for
    // correctness, not throughput.
    ssiState        ssiState
    predicateLocks  predicateLocksRegistry
    // xactMarker callback for WAL commit-status hook:
    xactMarker func(storage.TransactionID, XactMarker) error
}

func New(cfg Config) *Manager
func (m *Manager) AcquireBackend(iso IsolationLevel) int32
func (m *Manager) ReleaseBackend(procNum int32)
func (m *Manager) Begin(procNum int32, iso IsolationLevel) Transaction
func (m *Manager) SnapshotFor(t Transaction, mc *mctx.Context) *Snapshot
func (m *Manager) Commit(t Transaction) error
func (m *Manager) Abort(t Transaction) error
func (m *Manager) OldestXmin() storage.TransactionID
```

## 10. PG counterparts

| goopg concept                  | PG counterpart                                                    |
|--------------------------------|-------------------------------------------------------------------|
| ProcArray                      | `postgres/src/include/storage/proc.h:104`, `procarray.c`          |
| `procSlot` per-backend         | `PGPROC` struct in `proc.h`                                       |
| GetSnapshotData lock-free walk | `procarray.c:2175 GetSnapshotData`                                |
| XidGen atomic counter          | `varsup.c:77 GetNewTransactionId` (under `XidGenLock`)            |
| CLOG bank locks                | `slru.c::SimpleLruGetBankLock` + `clog.c::TransactionIdSetStatusBit` |
| OldestXmin walk                | `procarray.c:1850 GetOldestNonRemovableTransactionId`             |
| First-snapshot caching         | `snapmgr.c FirstXactSnapshot`                                     |

## 11. Verification

After Phase D1 of [[09-migration-and-rollout]] ships:

- **Compile-time** — `grep -RIn 'm\.mu\.' internal/mvcc/manager.go`
  returns zero (the field is gone). `Manager` struct contains no
  `sync.Mutex`.
- **Mutex pprof** — re-run `analysis/perf-optimize/scripts/run_perf_suite.sh`
  c=50 simple-update; `mvcc.Manager.*` does not appear in the top-20
  contention list. Block profile shows no waits routed through any
  `mvcc.*` symbol.
- **Race detector** — `go test -race ./internal/mvcc/...` passes.
  Special focus on a stress test that runs 1 000 goroutines each
  performing Begin / SnapshotFor / Commit loops; assert all xids
  are recorded correctly in CLOG.
- **TPS lift** — c=50 simple-update TPS rises from **347 → ≥ 2 000**
  (08-recommendations.md sized 3–6× lift). c=10 simple-update rises
  from 410 to ≥ 1 000.
- **Snapshot allocation** — heap profile shows zero `mvcc.Snapshot`
  allocations on GC heap; all are in mctx.
- **No ProcArray exhaustion** — with `max_connections=100` and
  `len(slots)=200` (2× over-provision), no test hits exhaustion.
  A unit test asserts the panic path on synthetic 201-backend load.
- **CLOG bank correctness** — write-heavy test (10 000 xacts) with
  status verifications under concurrent SetStatus / GetStatus; no
  lost commits.

[[05-activity-perbackend]] shares the same `procNum` index space; its
verification confirms both subsystems run from the same slot.
