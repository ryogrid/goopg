# Row decode part 3: the row pool is a net cost — remove it

Status: DESIGN, pre-implementation. Branch `optimize-row-decode`.
Follows parts 1 and 2. Profiles are **fresh, taken on the current build**
(`7642bac63`) — parts 1 and 2 changed the operator mix, and part 2's report
records what happens when a bound is derived from a stale profile.

## 0. Summary

`acquireRow` draws every executor row from a width-keyed `sync.Pool`
(`internal/executor/row_pool.go`, M0068-0004). **The pool's hit rate is
0.1%.** It is not recycling rows; it is an allocator wrapped in
synchronisation, interface boxing and a redundant zeroing loop.

| | TPC-H | TPC-DS |
|---|---:|---:|
| `acquireRow` cum | 24.94 s / 297.25 s = **8.4%** | 37.04 s / 222.72 s = **16.6%** |
| of which `sync.Pool.Get` | 21.99 s (7.4%) | 34.77 s (15.6%) |
| **pool overhead beyond plain allocation** | **~4.2%** | **~4.4%** |

Removing the pool and calling `make(Row, width)` directly is expected to
recover most of that on both corpora. This is the first item in this series
where **TPC-DS carries the larger share**.

## 1. Evidence

### 1.1 The pool essentially never hits

`sync.Pool.Get`'s own breakdown. `popHead` is the fast path — an actual hit:

| component | TPC-H | share | TPC-DS | share |
|---|---:|---:|---:|---:|
| `New` (i.e. **allocate a fresh row**) | 15.07 s | 68.5% | 26.33 s | 75.7% |
| `getSlow` (steal / victim cache) | 3.96 s | 18.0% | 6.64 s | 19.1% |
| `pin` + `procUnpin` | 0.77 s | 3.5% | 0.62 s | 1.8% |
| **`popHead` — the HIT path** | **0.02 s** | **0.09%** | **0.14 s** | **0.40%** |

Two independent instruments agree:

- **Direct counters** on a real join query (`SELECT … FROM rp_a JOIN rp_b …`,
  5 executions): **acquire = 26,720, fresh allocations = 26,704, releases =
  20. Hit rate 0.1%; release:acquire = 0.001.**
- A synthetic acquire-only loop: 200,000 acquires, 200,000 allocations, **0.0%**.

### 1.2 Why it never hits — structural, not tuning

`acquireRow` is called **per row** (`cloneRowOwned` → 95.4% from
`bitmapHeapScanOp.fetchExact` on TPC-DS, 81.8% from `seqScanOp.Next` on
TPC-H). `releaseRow` is called at roughly a dozen sites, nearly all operator
`Close` paths.

Rows flow downstream to consumers with no ownership contract, so they are
simply garbage-collected. **The pool is drained by design and refilled by
nobody.** No amount of sizing or width tuning changes that; the acquire and
release call counts differ by three orders of magnitude.

### 1.3 What the pool costs to deliver 0.1%

Everything below is paid on *every* row and would not exist with `make`:

- `getSlow` — 3.96 s / 6.64 s. Cross-P stealing and victim-cache scanning, on
  a pool that has nothing to steal.
- `runtime.convTslice` — 6.06 s (TPC-H) / 8.39 s (TPC-DS) cum. `sync.Pool`
  stores `any`, so every `Put` **boxes the slice header — an allocation caused
  by the allocation-avoidance mechanism.**
- `poolChain.popTail` — 2.47 s / 3.67 s.
- `pin`/`procUnpin` — pins the goroutine to its P on every acquire.
- **A redundant zeroing loop.** `acquireRow` zeroes every `Datum` "defensively"
  after `Get`. On the ~100% of calls that land in `New`, the memory came from
  `make`, which **already returns zeroed memory** — so the loop re-zeroes what
  the allocator just zeroed (`memclrHasPointers`, 2.05 s / 1.41 s).

## 2. Design

Delete the pool. `acquireRow(width)` becomes `make(Row, width)`;
`releaseRow` becomes a no-op retained at its call sites, or is removed with
its callers.

Rationale for removal rather than repair: making the pool *work* would require
an ownership contract for rows that flow into joins, aggregates and sorts —
i.e. deciding when a downstream consumer is finished with a row. That is a
lifetime redesign, not a tuning change, and the packed-retention work
(`minimize_datum`) already covers the retained-bytes problem from a direction
that does not require it.

**`make` already gives the two properties `acquireRow` documents**: length
`width`, all-zero `Datum`s. The "cap equals len" guarantee also holds.

Keep `releaseRow` as an explicit no-op with a comment rather than deleting the
call sites, so the diff stays reviewable and a future ownership design has an
obvious hook. Its zeroing loop must go: zeroing a row nobody will reuse is
pure cost.

### 2.1 The risk, stated plainly

**GC pressure.** The stated purpose of the pool was to reduce allocation.
Since it hits 0.1% of the time it is not achieving that, and removing it
should *reduce* total allocations (no more `convTslice` boxing). But this must
be **measured, not assumed** — allocation count and bytes, not just wall time.

The benchmark clusters run `GOGC=100 GOMEMLIMIT=12GiB`, which is a realistic
GC regime; the historical `GOGC=off` measurements would hide a regression here
and must not be used to judge it.

## 3. Correctness plan

Removing a pool cannot change values *if* the replacement honours the same
contract. The hazards are aliasing and staleness:

- **Values byte-identical**: TPC-H 24/24 ordered digests, TPC-DS SF0.5
  `PASS=95` all-zero.
- **An aliasing test**: `releaseRow` currently zeroes to avoid retaining
  string/`big.Int` pointers in pool-resident memory. With no pool there is no
  pool-resident memory, but a test must pin that two successive `acquireRow`
  calls return **distinct** backing arrays, so no caller can accidentally
  depend on the old recycling behaviour.
- **Zeroing contract**: assert `acquireRow` returns all-zero `Datum`s, which
  is what callers assigning individual columns rely on.
- `-race` on `internal/executor`, units scope, `tpch-spotcheck.sh`.

## 4. What is NOT claimed

The ~4.2% / ~4.4% figures are the **overhead beyond plain allocation** — an
estimate of the recoverable portion, not a forecast of suite improvement.
Allocation itself (`New`, 15.07 s / 26.33 s) does not disappear; it moves to
`make`. Whether removing the surrounding machinery converts cleanly into wall
time depends on allocator and GC behaviour, which is exactly what §2.1 says
must be measured.

`decodeRowRangeInfo` remains the largest single flat cost on both corpora
(10.0% TPC-H, 17.7% TPC-DS) and is untouched by this item. It is the
successor, not this.
