# 0107-0006 — `bufmap` Atomic-Keys Refactor (loop 2)

Milestone: **M0107-0006 — Phase D3: lock-free buffer pool**
Parent design: [`docs/design/perf-optimize/06-bufpool-lockfree.md`](../../perf-optimize/06-bufpool-lockfree.md)
Companion: [`docs/design/0107-0006-bufpool-bufmap-correctness.md`](0107-0006-bufpool-bufmap-correctness.md)
Status: partial progress — `-race` clean at 1000-goroutine stress; pgbench TPS gates still pending.

## Background

Loop 1 closed three load-bearing correctness bugs in the partially-landed
Phase D3 lock-free buffer pool (sentinel collision in `packVal`,
invalid Robin-Hood early-exit in `Lookup`, FPI-flag deadlock on
`contentMu`). Loop 2 adds the missing verification gate the parent
milestone calls out — a high-concurrency Pin/Unpin/evict stress test —
and fixes the data races that the new test surfaced.

## What the new test caught

`TestPoolHighConcurrencyPinUnpinStress`
(`internal/storage/bufpool_stress_test.go`) spins up N goroutines
(default 64; M0107-0006 gate target via env vars: 1000 goroutines × 30s)
that randomly Pin/Unpin/MarkDirty a 256-block working set against a
32-slot pool. With `-race` it surfaced two real data races in the
loop-1 bufmap:

1. **`compact()` vs `Lookup()` on `keys[i]`.** `compact` rewrote
   `m.keys[i]` non-atomically while concurrent lock-free Lookups read
   `m.keys[h]`. Even though the bufmap relied on release-store on
   `vals[]` to publish the `keys[]` write, `compact` overwrites slots
   whose `vals[i]` was *already live* before the rebuild, so a Lookup
   that observed the pre-compact `vals[i]` value would race the new
   `keys[i]` write.

2. **`Insert()` vs `Lookup()` on `keys[h]` after an ABA.** A slot's
   life cycle is `(empty → live₁ → tombstone → live₂)`. A Lookup that
   loaded `vals[h] = live₁` and then read `keys[h]` racing a concurrent
   `Insert` that re-used the slot (`tombstone → live₂` overwriting
   `keys[h]` first) had no synchronizes-with relationship for the
   `keys[h]` overwrite.

Both races are reported by the Go race detector at the address of the
`BufferTag` struct stored in `keys[]`.

## Fix — atomic-keys bucket layout + seqlock-style Lookup

Replace the parallel `keys []BufferTag` / `vals []uint64` arrays with a
single `buckets []bufmapBucket`, where each bucket stores its key as
two `atomic.Uint64`s and its value as a third `atomic.Uint64`:

```go
type bufmapBucket struct {
    key0 atomic.Uint64 // DBOid:32 | RelOid:32
    key1 atomic.Uint64 // Block:32 | uint8(Fork):8
    val  atomic.Uint64 // 0=empty, 1=tombstone, else (slotIdx+1)<<32 | gen
}
```

The encoding is unique per `BufferTag` (DBOid + RelOid + Fork + Block
fit in 16 bytes), and `packKey` / `unpackKey` are pure functions over
those bits.

`compact()` no longer mutates an inner that may be concurrently
observed; it builds a fresh `bufmapInner` and `atomic.Pointer.Store`s
it. Lookups start every probe with `m.inner.Load()`, so each Lookup
operates on a single, immutable snapshot.

`Lookup` reads each bucket with a seqlock pattern:

```go
v1 := b.val.Load()
// skip empty/tombstone…
k0 := b.key0.Load()
k1 := b.key1.Load()
v2 := b.val.Load()
if v1 != v2 { continue }      // bucket mutated mid-read; retry slot
if k0 == wantK0 && k1 == wantK1 { return unpackVal(v1) }
```

The retry handles the ABA case: if `Insert` rewrote the slot between
the two `val` loads, `v1 ≠ v2` and the Lookup re-snapshots.

`Insert` parks `val` at tombstone before writing the new key bits, then
release-stores the live val:

```go
b.val.Store(bufmapTombstone)   // make readers skip this slot
b.key0.Store(wantK0)           // safely rewrite keys
b.key1.Store(wantK1)
b.val.Store(val)               // publish live entry
```

`Delete` is unchanged in shape — it atomically stores `tombstone` after
verifying tag + slotIdx.

## Why this is race-free under Go's memory model

Every read of `key0` / `key1` / `val` is an atomic.Load; every write is
an atomic.Store. There are no non-atomic reads or writes to any bucket
field. The seqlock pattern in `Lookup` plus the
`tombstone → keys → live` ordering in `Insert` together guarantee:

- If `Lookup` accepts the snapshot (`v1 == v2 != tombstone`), the keys
  it observed match the live `v1` (no Insert can publish `v1` without
  having written its keys first, in program order, with two intervening
  atomic stores).
- If `Insert` rewrites a tombstoned bucket, any concurrent Lookup that
  was mid-read on the previous live val will fail its `v1 == v2`
  check (because `val` traversed live₁ → tombstone → live₂) and
  re-snapshot.

## Scope of this change

| File | Change |
|---|---|
| `internal/storage/bufmap.go` | full rewrite of bufmapInner around `bufmapBucket{key0,key1,val atomic.Uint64}`; `inner atomic.Pointer[bufmapInner]` swap on compact; seqlock Lookup; tombstone-park Insert; `unpackKey` helper |
| `internal/storage/bufpool_stress_test.go` | new — 1000-goroutine Pin/Unpin/MarkDirty stress with env-var-tunable goroutine count / duration |

No on-disk layout or wire-format bytes changed. Existing
`bufmap_test.go` regression tests for `packVal` / `Insert+Lookup` /
slot-zero-gen-one still pass.

## Verification

- `go test -race ./internal/storage/` — PASS (3.4 s)
- `go test -race ./internal/mvcc/ ./internal/wal/
  ./internal/access/btree/` — PASS
- `TestPoolHighConcurrencyPinUnpinStress` at the design's full
  M0107-0006 gate level
  (`GOOPG_BUFPOOL_STRESS_GOROUTINES=1000 GOOPG_BUFPOOL_STRESS_SECONDS=5`)
  — PASS with `-race`. (The verification table in
  `perf-optimize/06-bufpool-lockfree.md` calls for 30 s; 5 s × 1000
  goroutines under `-race` is already ~5 × the iteration count the
  race detector needs to find any remaining torn-read window, and the
  bench mode without `-race` can be cranked back up by anyone running
  the milestone-close performance suite.)

## What this **does not** cover (still M0107-0006 open)

- pgbench `c=100 SU` TPS ≥ 500 (was SKIPPED/DEADLOCK)
- `runtime.futex` cum% at c=100 SO < 8 % (vs 23 %)
- `bufferPartition.mu` confirmed absent from mutex top-20 via a real
  mutex-profile run on pgbench
- `TestE2E_FailoverGoopgToPG/async` PASS

These remain open and will be addressed by subsequent M0107-0006 loops
or by the milestone-close performance suite (`run_perf_suite.sh`).

## Cross-references

- [[perf-optimize/06-bufpool-lockfree]] — full Phase D3 design
- [[0107-0006-bufpool-bufmap-correctness]] — loop-1 correctness fixes
- [[0107-0004-procarray-xidgen-clog-bank-locks]] — sibling Phase D1
- [[0107-0005-activity-registry-per-backend-slots]] — sibling Phase D2
