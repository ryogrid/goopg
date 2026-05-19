# 06 — Lock-Free Buffer Pool

This chapter rips out the 128-partition `sync.Mutex` buf-mapping layer
(M0098-0003), the per-slot `pinCount` / `usageCount` separate atomics
(M0099-0002), the `slot.contentMu sync.RWMutex`, and the
`partition.ioByTag` + `ioCond` machinery. They are replaced by:

1. A single **lock-free open-addressing buf-mapping hash table** with
   pointer-free `[]BufferTag` keys and `[]int32` values.
2. A **packed 64-bit `slotState` CAS** for pin / unpin / dirty / valid /
   ioInflight / generation, with the lock-free pin fast path being a
   single CAS.

The retirement of M0098-0003 / M0099-0002 is on the user's explicit
"full rip-and-replace" directive. The on-disk **page format is
untouched** (heap pages, btree pages, etc., remain PG-compatible).

Cross-references: [[01-memory-context]] (none; bufpool is server-
scoped, not statement-scoped), [[05-activity-perbackend]] (Pin/Read
take an explicit `procNum` for wait-event recording), [[07-wal-fsm-
insert]] (FSM consults the new buf-mapping table to find low-pin pages).

## 1. Current state

Verbatim from `internal/storage/bufpool.go:78-83` and `:180`:

```go
type bufferPartition struct {
    mu      sync.Mutex
    byTag   map[BufferTag]int       // tag → slot index hash table
    ioByTag map[BufferTag]struct{}  // in-flight I/O tags
    ioCond  *sync.Cond              // wait here for I/O on this partition
}

type Pool struct {
    // ...
    partitions [128]bufferPartition
    evictMu    sync.RWMutex
    clockHand    int
    bgwriterHand int
    // ...
}
```

And the Slot (`bufpool.go:15-52`):

```go
type Slot struct {
    page   Page
    tag    BufferTag
    valid  bool
    dirty  bool
    pinCount     atomic.Int32       // M0099-0002
    usageCount   atomic.Int32
    fpiSinceCheckpoint bool
    contentMu    sync.RWMutex
}
```

The Pin fast path (`bufpool.go:923-1102`) acquires `partition.mu` to
look up `byTag`, then releases it before `evictMu.RLock()` + atomic
`pinCount.Add(1)`. The slow path (cache miss) acquires `evictMu.Lock()`
for victim selection, takes `contentMu.Lock` on the slot for the disk
read, then re-locks the partition to publish the tag.

Pointer-typed fields contributing to GC scan:
- 128 × `byTag map[BufferTag]int` — each map has bucket arrays + key
  slots that the GC walks.
- 128 × `ioByTag map[BufferTag]struct{}` — same.
- 128 × `ioCond *sync.Cond` — pointer per partition.
- Per slot: `contentMu sync.RWMutex` (internal pointer state).

Evidence from analysis:
- `analysis/perf-optimize/04-contention.md` §4.4 — 19 goroutines
  blocked on a single `partition.mu` (the one covering pgbench_history's
  tail page) for 23 min at c=100 simple-update.
- `analysis/perf-optimize/02-cpu-bottlenecks.md` §2.3 — `runtime.futex`
  cum% rises from 14.9 % (c=10 SO) to 23.0 % (c=100 SO); a large
  fraction of these wakeups originate from the partition mutex
  hand-offs.
- `analysis/perf-optimize/03-memory-and-allocs.md` §3.6 — the 128
  partitions' map internals are scanned on every GC cycle, contributing
  to the "GC scans the static 2.5 GB buffer pool plus the ~2.5 GB of
  churn" cost.

## 2. Target architecture

### Two concurrent components

1. **`bufmap`** — lock-free open-addressing hash table:
    - Keys: `[]BufferTag` (16 B POD each).
    - Values: `[]uint64` packed `(slotIdx, gen)` so a 32-bit slot
      index and 32-bit generation fit in one word, enabling atomic
      CAS.
    - Pointer-free (no internal Go map structures).
    - Robin-Hood probing for lookup; CAS for insert/delete.

2. **`Slot.state`** — single 64-bit atomic word packing all per-slot
   coordination state. Pin/Unpin in the fast path is a single CAS.

The `Pool` struct retains its high-level fields (`slots []*Slot`,
`mgr`, `arena`, etc.), the clock-sweep hands, and bgwriter coordination.
The 128 partition structs go away.

## 3. `bufmap` design

```go
// internal/storage/bufmap.go (new)

type bufmap struct {
    mask    uint64    // table size - 1; size is a power of two
    keys    []BufferTag    // len == size
    vals    []uint64       // len == size; packed (slotIdx<<32) | gen; 0 == empty
}

// New constructs a table sized for nSlots active entries at load factor 0.5.
// shared_buffers / 4 KiB == 327 680 slots → size 1 048 576 (next pow2 of 2×).
func newBufmap(nSlots int) *bufmap {
    size := uint64(1)
    for size < uint64(nSlots)*2 {
        size <<= 1
    }
    return &bufmap{
        mask: size - 1,
        keys: make([]BufferTag, size),
        vals: make([]uint64, size),
    }
}
```

### Pointer-free property

`keys []BufferTag` and `vals []uint64` are both slices of POD types.
The GC scans the two slice headers (one pointer each). Compared to
today's 128 × map = 256 maps × bucket arrays × key strings = thousands
of GC scan roots, this is a >100× reduction in mark-phase work for
the buf-mapping structure.

### Hash + probe

```go
func bufTagHash(t BufferTag) uint64 {
    // 64-bit fmix from MurmurHash3; runs on a 16-byte POD struct.
    // CRITICAL: every byte of BufferTag must feed the hash, including
    // the Fork (heap / FSM / VM / init) discriminator. A prior draft
    // ignored Fork; that produced guaranteed cross-fork collisions
    // and was caught in design review.
    h := uint64(t.Rel.DBOid) | uint64(t.Rel.RelOid)<<32
    h ^= uint64(t.Rel.Fork) * 0xBF58476D1CE4E5B9   // mix in Fork
    h ^= uint64(t.Block) * 0x9E3779B97F4A7C15
    h ^= h >> 33
    h *= 0xFF51AFD7ED558CCD
    h ^= h >> 33
    h *= 0xC4CEB9FE1A85EC53
    h ^= h >> 33
    return h
}
```

Linear probing with Robin-Hood displacement. The current
`tagPartition` FNV-1a hash (`internal/storage/page.go:202-210`) is
deleted; the new hash mixes more bits and is a one-line table-lookup
in the inner loop.

### Lookup (lock-free)

```go
// Lookup returns (slot, gen) for tag, or (-1, 0) if not present.
// Lock-free: only atomic loads. Tombstones do not terminate probing;
// only true-empty buckets do.
func (m *bufmap) Lookup(tag BufferTag) (slot int32, gen uint32) {
    h := bufTagHash(tag) & m.mask
    dist := uint64(0)
    for {
        v := atomic.LoadUint64(&m.vals[h])
        switch {
        case v == bufmapEmpty:
            return -1, 0   // true empty; tag not present
        case v == bufmapTombstone:
            // Tombstone: continue probing without checking the key
            // (the key field may be stale).
            h = (h + 1) & m.mask
            dist++
            continue
        }
        // Live entry. Read key under acquire ordering: vals' CAS publishes
        // the key write that happened in Insert.
        if m.keys[h] == tag {
            return int32(v >> 32), uint32(v)
        }
        // Robin-Hood: if our probe distance exceeds the resident's,
        // tag is not present.
        residentH := bufTagHash(m.keys[h]) & m.mask
        residentDist := (h - residentH) & m.mask
        if dist > residentDist {
            return -1, 0
        }
        h = (h + 1) & m.mask
        dist++
    }
}
```

At 50 % load factor the expected probe count is ~1.5; the worst case
is bounded by the Robin-Hood property.

### Insert (CAS)

```go
// vals[h] sentinels:
//   0  — empty bucket (no entry ever; safe to skip on Lookup)
//   1  — tombstone   (was occupied, key may still be in keys[h];
//                     Insert may reuse, Lookup must continue probing)
//   *  — live entry (high 32 bits: slotIdx; low 32 bits: gen)

const (
    bufmapEmpty     uint64 = 0
    bufmapTombstone uint64 = 1
)

// Insert publishes a (tag, slotIdx, gen) entry. Returns true on
// success; false if a concurrent inserter beat us with the same tag.
// Callers use Insert when finalising a miss; they should call Lookup
// first to recheck under contention.
func (m *bufmap) Insert(tag BufferTag, slotIdx int32, gen uint32) bool {
    val := uint64(slotIdx)<<32 | uint64(gen)
    h := bufTagHash(tag) & m.mask
    for {
        v := atomic.LoadUint64(&m.vals[h])
        switch {
        case v == bufmapEmpty || v == bufmapTombstone:
            // Reusable bucket. Stage the key write, then publish via
            // CAS — see memory-model note below.
            m.keys[h] = tag
            if atomic.CompareAndSwapUint64(&m.vals[h], v, val) {
                return true
            }
            continue   // someone else won this bucket; retry with same h
        default:
            // Live entry. If it's our tag, we lost the publish race.
            if m.keys[h] == tag {
                return false
            }
            h = (h + 1) & m.mask
        }
    }
}
```

**Memory-model argument.** The 16-byte `BufferTag` store on
`m.keys[h]` compiles to two 8-byte stores; these are not atomic
relative to each other. However, the subsequent `CompareAndSwapUint64`
on `m.vals[h]` is documented by Go's memory model
([go.dev/ref/mem](https://go.dev/ref/mem)) to act as a **release**
operation: every memory write sequenced-before the CAS in program order
is visible to any goroutine performing an atomic-load on the same
location and observing the post-CAS value (the load acts as **acquire**).
A reader executing `atomic.LoadUint64(&m.vals[h])` and observing a
non-`bufmapEmpty`/non-`bufmapTombstone` value is therefore guaranteed to
observe the corresponding `m.keys[h]` write that the inserter staged
immediately before the CAS. The same release-acquire pair holds on
weak-memory architectures (ARM64, RISC-V) because Go's `sync/atomic`
package compiles `CompareAndSwap` to a sequentially-consistent or
release-semantics machine instruction (`STLXR` + `LDAXR` pair on ARM64).

The Robin-Hood property is enforced by the writer strictly increasing
probe distance; tombstone reuse does not break the property because the
tombstone marker preserves the original probe distance for the entry
that was deleted (the next inserter writing into the tombstone slot
starts at its own preferred bucket and only reaches the tombstone via
the same probing sequence).

Edge case — the **stronger** Robin-Hood variant (probe-distance-based
swap during insert) requires more complex CAS sequencing. For our
load factor (≤ 50 %) the simpler "find empty or matching slot" variant
is sufficient; if profiling shows higher load factors, we upgrade to
the full Robin-Hood.

### Delete (mark-as-tombstone)

```go
// Delete clears (tag, slotIdx). Called by evictLocked after the slot
// is recycled. Uses bufmapTombstone (== 1) so Lookup must continue
// probing past deleted entries until it sees a bufmapEmpty bucket.
func (m *bufmap) Delete(tag BufferTag, slotIdx int32) {
    h := bufTagHash(tag) & m.mask
    for {
        v := atomic.LoadUint64(&m.vals[h])
        switch {
        case v == bufmapEmpty:
            return   // not present
        case v == bufmapTombstone:
            h = (h + 1) & m.mask
            continue
        }
        if m.keys[h] == tag && int32(v>>32) == slotIdx {
            atomic.StoreUint64(&m.vals[h], bufmapTombstone)
            return
        }
        h = (h + 1) & m.mask
    }
}
```

Tombstone count is bounded; a background compaction runs when
tombstones exceed 25 % of capacity (cold path, takes a global rebuild
lock).

## 4. Slot state packing

```go
// internal/storage/slot.go (rewritten)
//
// Slot is sized to exactly 64 B (one cache line) so per-slot CAS
// traffic does not cause false sharing across slots. The single GC
// root per slot is the page slice header.

type Slot struct {
    page  Page          // slice header (24 B); aliases shared_buffers slab
    tag   BufferTag     // 16 B POD
    state atomic.Uint64 // 8 B packed; layout below
    _pad  [16]byte      // 16 B pad to 64 (24+16+8+16=64)
}
// unsafe.Sizeof(Slot{}) == 64; asserted in internal/storage/slot_test.go.

// slotState bit layout:
//   bit  0..21  pinCount    (22 bits → 4 M concurrent pins)
//   bit 22..29  usageCount  (8 bits, saturating clocksweep)
//   bit     30  dirty
//   bit     31  valid
//   bit     32  ioInFlight
//   bit 33..47  generation  (15 bits, bumped on eviction; ABA guard)
//   bit 48..63  reserved

const (
    pinShift     = 0
    pinMask      = (1<<22) - 1
    usageShift   = 22
    usageMask    = uint64(((1<<8) - 1) << usageShift)
    dirtyBit     = uint64(1 << 30)
    validBit     = uint64(1 << 31)
    ioInflightBit = uint64(1 << 32)
    genShift     = 33
    genMask      = uint64(((1<<15) - 1) << genShift)
)
```

`unsafe.Sizeof(Slot{}) == 64`. One slot per cache line; no false
sharing across slot pin/unpin updates from different CPUs (each slot
has its own state word at a distinct cache line).

### Pin fast path (single CAS)

```go
// Pin returns (true, pin) on success, (false, _) if the slot is not
// valid or has I/O in flight. The Pin value encodes (slotIdx, gen)
// so Unpin can verify the slot has not been evicted in between.
func (p *Pool) Pin(tag BufferTag, procNum int32) (Pin, error) {
    slotIdx, gen := p.bufmap.Lookup(tag)
    if slotIdx >= 0 {
        s := &p.slots[slotIdx]
        for {
            old := s.state.Load()
            if old & validBit == 0 || old & ioInflightBit != 0 {
                break   // fall to slow path
            }
            if uint32((old & genMask) >> genShift) != gen {
                break   // ABA: slot was evicted and replaced
            }
            newPinCount := (old & uint64(pinMask)) + 1
            if newPinCount > uint64(pinMask) {
                return Pin{}, ErrPinCountOverflow
            }
            // Bump pin and saturate usage (capped at 5):
            newUsage := (old & usageMask) >> usageShift
            if newUsage < 5 { newUsage++ }
            new := (old &^ (uint64(pinMask) | usageMask)) | newPinCount | (newUsage << usageShift)
            if s.state.CompareAndSwap(old, new) {
                return Pin{SlotIdx: slotIdx, Gen: gen}, nil
            }
        }
    }
    // Slow path: cache miss or contended fast-path. See §5.
    return p.pinSlow(tag, procNum)
}
```

Fast path: **one Lookup probe (~1.5 atomic loads), one atomic CAS**.
No mutex acquire, no futex syscall on the uncontended path.

### Unpin (single CAS)

```go
func (p *Pool) Unpin(pin Pin) {
    s := &p.slots[pin.SlotIdx]
    for {
        old := s.state.Load()
        if uint32((old & genMask) >> genShift) != pin.Gen {
            // Slot was evicted while we held the pin? Shouldn't happen if
            // pin protocol is honoured, but be defensive.
            return
        }
        pinCount := old & uint64(pinMask)
        if pinCount == 0 {
            panic("storage: Unpin on slot with pinCount==0")
        }
        // Decrement pinCount via explicit field arithmetic — never rely
        // on (old - 1) even though pinShift == 0 today; explicit form
        // survives future layout reshuffles.
        new := (old &^ uint64(pinMask)) | ((pinCount - 1) & uint64(pinMask))
        if s.state.CompareAndSwap(old, new) {
            return
        }
    }
}
```

### Pin type

```go
type Pin struct {
    SlotIdx int32
    Gen     uint32
}
```

`unsafe.Sizeof(Pin{}) == 8`. Pointer-free. Passed by value through
the executor. Caller invokes `pool.Unpin(pin)` (or `defer
pool.Unpin(pin)`) before the slot's content lifetime expires.

### Helper: `Pool.SlotPinCount`

Consumers outside the Pin/Unpin core that need to inspect a slot's
pin count (e.g., [[07-wal-fsm-insert]]'s FSM hot-page avoidance)
use a helper rather than inlining the bitmask:

```go
// SlotPinCount returns the current pinCount for tag's slot, or 0
// if tag is not currently mapped. Lock-free.
func (p *Pool) SlotPinCount(tag BufferTag) int32 {
    slotIdx, _ := p.bufmap.Lookup(tag)
    if slotIdx < 0 {
        return 0
    }
    return int32(p.slots[slotIdx].state.Load() & uint64(pinMask))
}
```

This isolates the bit-layout details behind a method so future
slotState reshuffles don't ripple into FSM logic.

The slot's `page Page` field is the **only GC root** per slot (the
underlying arena byte slice). The `slots []*Slot` slice header is one
GC root for the whole pool.

## 5. Slow path: cache miss / eviction

The slow path is taken on cache miss or fast-path contention. It must
allocate a victim slot, set up I/O coordination, and publish the new
tag. The classic protocol:

```go
func (p *Pool) pinSlow(tag BufferTag, procNum int32) (Pin, error) {
    // 1. Check inflight: another backend may be reading this tag.
    //    We don't have a per-partition ioByTag map any more; instead
    //    the slot itself signals via ioInflightBit. If a Pin hits a
    //    slot with ioInflightBit set, it waits.

    // 2. Recheck Lookup (someone may have inserted while we were
    //    deliberating):
    slotIdx, gen := p.bufmap.Lookup(tag)
    if slotIdx >= 0 {
        return p.tryPinExisting(slotIdx, gen)
    }

    // 3. Pick a victim via clocksweep on slot.state words:
    victim, victimGen := p.findVictim()

    // 4. CAS the victim into "evicting" state (pinCount==0, ioInflightBit==1):
    if !p.beginEviction(victim, victimGen) {
        // Lost the race; retry from step 2.
        return p.pinSlow(tag, procNum)
    }

    // 5. If victim was dirty, flush it (synchronous WAL + write).
    if p.slots[victim].state.Load() & dirtyBit != 0 {
        if err := p.flushSlot(victim, procNum); err != nil {
            return Pin{}, err
        }
    }

    // 6. Remove victim's old tag from bufmap:
    p.bufmap.Delete(p.slots[victim].tag, victim)

    // 7. Read new tag from disk (releases ioInflightBit on completion):
    if err := p.readBlock(victim, tag, procNum); err != nil {
        // On error, leave the slot in evicting state for a retry.
        return Pin{}, err
    }

    // 8. Publish the new tag:
    p.slots[victim].tag = tag
    newState := validBit | uint64(1) /*pinCount*/ | (uint64(p.slots[victim].state.Load()>>genShift)+1) << genShift
    p.slots[victim].state.Store(newState)
    if !p.bufmap.Insert(tag, victim, uint32(newState>>genShift)) {
        // Someone else published the same tag; we lost. Roll back.
        // ... (rare race; bounded by clocksweep traversal)
    }

    return Pin{SlotIdx: victim, Gen: uint32(newState >> genShift)}, nil
}
```

The slow path uses no mutex. Wait-for-IO is implemented by spinning
briefly on `ioInflightBit` then sleeping on a runtime semaphore via
`//go:linkname` ([[08-runtime-internals]]). Compared to the existing
`sync.Cond.Wait()` machinery, the semaphore version eliminates the
condition-variable's internal mutex.

## 6. Clock-sweep & bgwriter

The clock-sweep `clockHand int` and `bgwriterHand int` retain their
shape but operate on the new `slot.state` word. Bgwriter
(`internal/storage/bgwriter.go:23-81`) iterates slots starting at
`bgwriterHand`, reads each `state`, and if `dirtyBit && pinCount == 0`,
flushes the page.

The current bgwriter contention at c=10 SO (28 % of mutex delay through
`Pool.WriteDirtyPages` per `analysis/perf-optimize/04-contention.md`
§4.5) goes away: there is no shared mutex to contend on.

## 7. Removal of `evictMu`

The `Pool.evictMu sync.RWMutex` (`bufpool.go:31` area) currently
serialises the clock-sweep victim search and dirty-flush. Post-refactor
it is **deleted**:

- Pin fast path: no `evictMu.RLock()` because pin is a single CAS on
  the slot.
- Pin slow path: clock-sweep walks the `slots[]` array atomically;
  victim selection is a CAS on the candidate slot's state, not a
  serialised hand walk.
- Bgwriter: walks slots atomically; flush is per-slot; no shared lock.

Multiple goroutines can clock-sweep simultaneously; the per-slot CAS
in `beginEviction` ensures only one ends up evicting any given slot.

## 8. Removal of `slot.contentMu`

The current `slot.contentMu sync.RWMutex` guards the page-bytes against
concurrent readers and writer eviction. Post-refactor:

- **Read access** (e.g., `seqScanOp` decoding tuples) requires a Pin.
  As long as a backend holds a Pin (pinCount > 0), the slot cannot
  be evicted (eviction requires `pinCount == 0`).
- **Write access** (e.g., `insertOp` writing a heap tuple) requires
  a Pin **and** the heap-level page lock (PG-style: a separate
  per-page `pageLock sync.RWMutex` in the `Slot`, distinct from the
  state-word coordination). The heap-level page lock is **kept** in
  the refactor because PG uses the same dual-lock pattern
  (`LockBufHdr` / `LockBuffer` distinction); the per-page content
  lock is short-held and the lock count is bounded.

The replaced `contentMu` was being used as the **content** lock, not
the **mapping** lock. The mapping lock vanishes; the content lock
becomes a small per-slot pageLock `sync.RWMutex` (kept). Reduced lock
footprint per slot from two to one.

## 9. I/O coordination without `ioByTag`

The current per-partition `ioByTag map` plus `ioCond *sync.Cond`
coordinate concurrent fetches of the same tag. Post-refactor:

- `ioInflightBit` in `slot.state` marks "this slot is reading the tag
  from disk."
- A Pin that observes `ioInflightBit` parks via the per-slot semaphore.
- After the disk read completes, the loader clears `ioInflightBit`
  and releases all waiters.

**Design choice: per-slot semaphore counter in a parallel `[N]uint32`
table** (not a field inside `Slot`, because keeping `Slot` at exactly
64 B is more valuable than locality of the semaphore):

```go
type Pool struct {
    slots    []Slot       // N entries, each 64 B
    slotSema []uint32     // N entries, parallel to slots; used by runtimeshim.SemaAcquire/Release
    // ... other fields
}
```

Wait/wake uses the runtime semaphore primitive via
`internal/runtimeshim` ([[08-runtime-internals]] §5):

```go
// Waiter (called when Pin observes ioInflightBit):
runtimeshim.SemaAcquire(&p.slotSema[slotIdx])

// Loader (clears ioInflightBit and wakes all current waiters):
for i := 0; i < waiterCount(slotIdx); i++ {
    runtimeshim.SemaRelease(&p.slotSema[slotIdx])
}
```

`waiterCount` reads the runtime semaphore's outstanding-wait counter
(maintained by the runtime); on the rare condition that exact counter
isn't accessible, the loader does a fixed-batch release (e.g., 32) and
the slow path retries — bounded and rare.

**Fallback** when `//go:linkname` is unavailable: a parallel
`[]sync.Mutex` + `[]sync.Cond` table per slot, with identical
semantics. Slower wakeup (~10 µs vs ~1 µs for the runtime semaphore)
but functionally equivalent. The fallback's structure is sized so the
hot-path Pin (which does not block) never touches it.

## 10. GC scan implications

Pre-refactor GC roots in the buffer pool:
- 1 × `Pool.slots []*Slot` slice header.
- 128 × `bufferPartition.byTag map` — each map has ~thousands of
  internal pointers (bucket pointers, key string headers, etc.).
- 128 × `bufferPartition.ioByTag map` — same.
- 128 × `bufferPartition.ioCond *sync.Cond`.
- Per slot × 327 680 × 1 `contentMu sync.RWMutex` (internal pointers).

Post-refactor:
- 1 × `Pool.slots []Slot` slice header (slots become value-type
  embedded in the slice, eliminating the per-slot pointer).
- 1 × `bufmap.keys []BufferTag` slice header.
- 1 × `bufmap.vals []uint64` slice header.
- 1 × `Pool.slotSemaphores []sem` slice header (POD).

Reduction is from ~256 maps + per-slot Cond/Mutex pointers (millions
of internal pointer scans per GC cycle) to **4 slice headers**. This
is the largest single contributor to bringing `gcBgMarkWorker` cum%
under 15 %.

The per-page bytes themselves (the static `shared_buffers`) are
allocated as 4 KiB-aligned slabs from `internal/storage/arena.go`;
the GC sees one `[]byte` per slab, scans only the slice header (the
bytes contain page binary, no pointers).

## 11. Migration of M0098-0003 / M0099-0002

Both milestones are explicitly retired. Their design docs at
`docs/design/0098-0003-bufpool-partitioning.md` and `docs/design/0099-
0002-pin-fastpath.md` are marked **SUPERSEDED-BY: docs/design/perf-
optimize/06-bufpool-lockfree.md** in their frontmatter. The retirement
is documented in [[09-migration-and-rollout]] §Phase D3.

The good ideas they contain are preserved:
- Cache-line aligned slot state (M0099-0002) — retained, now packed
  into one 64-bit word.
- 128-way separation of work (M0098-0003) — retained as **virtual**
  separation: different tags hash to different `bufmap` slots, so
  different backends contend on different slot-state words. No mutex.

## 12. PG counterparts

| goopg concept                     | PG counterpart                                              |
|-----------------------------------|-------------------------------------------------------------|
| `bufmap` open-addressing table    | `postgres/src/backend/storage/buffer/buf_table.c:90`        |
| Atomic state-word CAS for pin     | `bufmgr.c:3058,3112 PinBuffer` (atomic state field)         |
| Packed pinCount + usageCount      | `BufferDesc.state` in `postgres/src/include/storage/buf_internals.h` |
| Per-buffer content lock           | `LockBuffer` (`bufmgr.c:5198`)                              |
| I/O inflight bit                  | `BM_IO_IN_PROGRESS` flag in `buf_internals.h`               |
| Clock-sweep                       | `freelist.c StrategyGetBuffer`                              |
| Bgwriter dirty-flush              | `bgwriter.c BgBufferSync`                                   |

PG uses identical primitives: a buf-mapping hash table (with 128 LWLock
partitions; ours has none), atomic state words for pin/usage, separate
content locks. We are stricter than PG on the buf-mapping side
(lock-free vs LWLock-partitioned) because Go mutexes are expensive
relative to atomic operations.

## 13. Verification

After Phase D3 of [[09-migration-and-rollout]] ships:

- **Compile-time** — `grep -RIn 'bufferPartition\|partitions\[' internal/storage/`
  returns zero. `Pool` struct has no `partitions` array.
  `unsafe.Sizeof(Slot{}) == 64` asserted.
- **Mutex pprof** — no `bufferPartition.mu`, no `evictMu`,
  no `slot.contentMu` (replaced by per-page pageLock which is
  separate and bounded). The c=10 SO `Pool.WriteDirtyPages` contention
  (28 % of mutex delay) drops to zero.
- **CPU pprof** — `runtime.futex` cum% drops from 14.9 % to **< 5 %**
  at c=10 SO. The pin fast path's CAS is a few instructions; no syscall.
- **GC scan** — heap profile reveals ~4 GC roots for the entire
  bufmap, vs hundreds of thousands today. `gcBgMarkWorker` cum%
  contribution from the buffer-pool side drops to near zero (the
  remaining contribution is the static `shared_buffers` byte slabs,
  which are pointer-free).
- **c=100 simple-update livelock** — combined with [[07-wal-fsm-insert]],
  the c=100 SU + standard workloads no longer SKIP. Both achieve
  measured TPS ≥ 500.
- **Race detector** — `go test -race ./internal/storage/...` passes,
  including a stress test with 1 000 goroutines doing Pin/Unpin/evict
  cycles for 30 s.
- **bufmap stress** — synthetic micro-benchmark in
  `internal/storage/bufmap_bench_test.go` shows steady-state lookup
  cost ~5 ns at 50 % load factor; CAS-insert ~30 ns under no
  contention, ~150 ns under heavy concurrent insert.
- **Tombstone compaction** — fuzz test with 1 M random insert/delete
  cycles validates compaction kicks in when tombstone ratio > 25 %.

[[07-wal-fsm-insert]] depends on this chapter; FSM consults
`bufmap.Lookup(tag)` (to read the slot's pin count for hotspot
avoidance) without taking any lock.
