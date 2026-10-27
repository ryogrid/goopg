# M0122-0012 C44 — the int-keyed hash table keeps each key's first build row inline

Status: design (2026-10-28, loop #227); implementation is the next M0122-0012 slice. Area: `internal/executor`
(`joinOp` hash join, int64 lane). Perf-only: plans, values and EXPLAIN output unchanged. Parent: M0122-0012
(banner item 10). The Target is written in the M0122-0012 entry.

## Symptom

TPC-H Q13 (census-20261028 #4, 950 ms over release PG) is stall-bound, so instructions:u barely see it. A cycles:u
profile at `4370e7f2c` (`tmp/g3-227/h13c.data`) shows where the time goes:

| symbol | cycles | what |
|---|---|---|
| `runtime.mapaccess1_fast64` | 16.4 % | 10.6 % in the hash join's probe, 5.2 % in the aggregate |
| `MaterializedSlot.Get` | 6.9 % | the projection's first read of the matched build row |
| `joinOp.nextLazy` | 6.5 % | self |

Q13 probes about 1.5 M orders rows against 150 k customers. Each probe costs:

- the swiss-map group: control word and slot;
- the `[]Row` slice's backing array, a separate one-element allocation per key that holds the `Row` header;
- the row's cells.

That is three dependent cache misses. PG's hash join has two: the bucket array, then the `HashJoinTuple` chain entry,
whose key it compares in place (ExecScanHashBucket, nodeHash.c).

## Design

`lazyIntHash map[int64][]Row` becomes `map[int64]intHashRows`, where:

    type intHashRows struct {
        first Row   // the key's first build row, inline in the map slot
        more  []Row // the second and later rows, nil for a unique key
    }

- A unique build key, the common case for a primary-key build such as customer, is reached in two misses: the map
  slot, then the cells. The one-element backing array, and its allocation per build key, disappear.
- Readers that need a `[]Row` take `o.intMatches(ik)`. It returns `more`, with `first` prepended, when there is more
  than one row. For a single row it returns `o.oneRow[:1]`, a scratch slice in the operator.
- The scratch is overwritten only by the next lookup. That is safe at the probe sites, which consume a key's matches
  before looking up the next key.
- Each of the 36 `lazyIntHash` sites is audited, and any site that holds a match slice across a later lookup copies
  it. These include the batch spill (`join_batch.go`), the outer-fill sweep (`join_outer_fill.go`), the demotion to
  the string map (`demoteIntHash`) and the shared or parallel build adoption (`applySharedBuild`).
- Writers use `o.intAppend(ik, row)`.
- `lazyMatchedI` (the RIGHT/FULL matched bits) keeps its `[]bool`, indexed by the match ordinal, which is unchanged.

PG citation: ExecHashTableInsert / ExecScanHashBucket keep the tuple in the bucket chain, so a probe reaches the
first candidate's key directly.

## Target and measurement

- **Target:** TPC-H SF1 Q13 wall time, the median of 5 warm runs on an idle host (16 cores, load < 2), on
  `tmp/q17-clone`, alternating A/B. Bar: −3 %; predicted ~5 %.
- Instructions:u must not rise by more than 1 %.
- Q13 is stall-bound, so the banner's wall-time metric applies.

## Tests

- An on/off equivalence test (`intHashInlineDisabled`) over inner, left, right, full, semi and anti hash joins on
  int keys. The data includes unique and duplicate keys, NULL keys, an empty build, a batched (spilled) build, a
  parallel shared build and a demotion to the string lane. Rows must be identical, in order.
- Gates as for any join change: units, spotcheck, acceptance, sf025 with plans, fire-set, RegressSuite and the
  isolation suite.

## Risks

- **A site that keeps the scratch slice across a lookup would read the next key's row.** The audit and the
  duplicate-key and batched cases in the equivalence test cover it.
- **Memory.** A map value grows from 24 to 48 bytes, while the per-key one-element array (8 + 24 bytes, plus
  allocator rounding) goes away. The net change is about neutral.
