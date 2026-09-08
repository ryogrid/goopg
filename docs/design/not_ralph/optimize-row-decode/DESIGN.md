# Speeding up the row-decode path — design

Status: DESIGN, pre-implementation. Branch: `optimize-row-decode`, cut from
local `master` (`60d257504`). Date: 2026-09-08.

## 0. Summary

Profiling the row-decode path found that **the largest single cost in TPC-H is
not decoding at all**. It is `indexScanOp.Next` calling
`catalog.InMemory.LookupEnum` **once per column per row** to discover that the
column is not an enum — on a corpus that contains **zero enum types**.

That is **21.09 s of 117.56 s = 17.94% of TPC-H CPU**, and most of it is
RWMutex traffic rather than useful work.

The fix is not a new design. **The sequential-scan path already does the right
thing** — it resolves enum-ness once in `Open` — and the index-scan path simply
never got the same treatment. This is sibling divergence, the failure mode the
repo's own practice card names, and closing it is a small, local change.

Three targets, in descending order of measured value:

| # | target | TPC-H | TPC-DS |
|---|---|---:|---:|
| 1 | per-row `LookupEnum` in `indexScanOp.Next` | **17.94%** | not hot |
| 2 | `ToLower` in `catalog.PhysicalTypeIsVarlena` | 2.78% | **2.16%** |
| 3 | `ToLower` in `decodeRowRangeInfo`'s `info == nil` fallback | 1.64% | 0.84% |

## 1. Evidence

All figures from CPU profiles captured on private clones (goopg at
`cd637dd5b`): `tpch-cold.pprof` (TPC-H SF=1, 117.56 s of samples) and
`tpcds-cold.pprof` (TPC-DS SF0.5, 272.72 s of samples).

### 1.1 Target 1 — the enum lookup

```
(*indexScanOp).Next                       65.74s  55.92% cum
  -> DecodeHeapTupleRowInto               35.03s  53.29%
  -> (*InMemory).LookupEnum               21.09s  32.08%   <<<
  -> followHOTChainNoCopy                  3.21s   4.88%
```

`LookupEnum` is called **100% from `indexScanOp.Next`**, and breaks down as:

| component | time | share of LookupEnum |
|---|---:|---:|
| `sync.(*RWMutex).RLock` | 7.75 s | 36.75% |
| `sync.(*RWMutex).RUnlock` | 5.78 s | 27.41% |
| `strings.ToLower` | 3.24 s | 15.36% |
| `lookupEnumByNameLocked` | 2.60 s | 12.33% |
| flat | 1.69 s | 8.01% |

**Two thirds of it (13.53 s) is lock acquire/release.** For context,
`RWMutex.RLock` + `RUnlock` together are 11.68% of all TPC-H CPU, and
essentially all of that traffic originates here.

The call is `internal/executor/operators_index.go:756-771`: for every row, a
loop over `o.plan.Table.Columns` calling `im.LookupEnum(col.Type.Name)`.

**TPC-H declares no enum types**, so every one of those calls takes a read
lock, lowercases a type name, misses the map, and returns false.

### 1.2 The sequential-scan path already solved this

`internal/executor/operators_storage.go:1486-1494`, in `Open`:

```go
// Pre-compute which columns are enum types so Next() can inject KindEnum datums
// for correct ORDER BY semantics (M0097-enum).
if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
    o.enumTypes = make([]*catalog.EnumType, len(o.cols))
    for i, col := range o.cols {
        if et, isEnum := im.LookupEnum(col.Type.Name); isEnum {
            o.enumTypes[i] = et
        }
    }
}
```

This is exactly the shape the index scan needs, already written, already
proven, already in the same package. It is also why **TPC-DS does not show this
cost**: TPC-DS is sequential-scan dominated, so it takes the fixed path.

That asymmetry is the whole finding — the same operation is O(1) per scan on
one path and O(rows × columns) on its twin.

### 1.3 Targets 2 and 3 — `strings.ToLower`

`strings.ToLower` is 7.30% of TPC-H CPU (8.58 s) and 3.16% of TPC-DS. By
caller:

| caller | TPC-H | TPC-DS |
|---|---:|---:|
| `catalog.PhysicalTypeIsVarlena` | 3.27 s (38.11%) | 5.89 s (68.41%) |
| `(*InMemory).LookupEnum` | 3.24 s (37.76%) | 0.10 s (1.16%) |
| `executor.decodeRowRangeInfo` | 1.93 s (22.49%) | 2.30 s (26.71%) |

The `LookupEnum` row is subsumed by target 1. The other two are separate:

- **`PhysicalTypeIsVarlena`** (`internal/catalog/physical_align.go:85`) does
  `strings.ToLower(t.Name)` on every call, and `decodeRowRangeInfo` calls it
  **per value** at `codec.go:1450` — even though the memoised `info[i]` is
  already in hand two lines above, supplying `align` and `lower`. The
  memoisation was applied to two of the three per-value string scans and this
  one was missed.
- **`decodeRowRangeInfo`'s own `ToLower`** is the `info == nil` fallback at
  `codec.go:1444`. Its cost means **some hot caller is passing `nil`** rather
  than a resolved slice. That is a wiring gap to find, not a new mechanism.

## 2. Design

### 2.1 Target 1 — resolve enum columns once, in `indexScanOp.Open`

Mirror the sequential-scan path exactly: add an `enumTypes []*catalog.EnumType`
field to `indexScanOp`, populate it in `Open`, and have `Next` consult the
slice instead of the catalog.

Two properties matter beyond the obvious:

1. **The common case must cost nothing.** When no column is an enum — every
   TPC-H and TPC-DS table — the per-row loop should not run at all. A single
   `hasEnum bool` guard, set at `Open`, skips it entirely rather than iterating
   columns to find all-nils. The sequential path allocates the slice
   unconditionally; the index path should carry the guard as well, and the
   sequential path should gain it too so the twins stay identical.
2. **Staleness.** This memoises a catalog fact, so the hazard is DDL. The
   resolution happens in `Open`, i.e. per execution, which is the same lifetime
   the sequential scan already relies on and the same rule `colTypeInfo`
   documents ("derive it wherever the column list itself is resolved, never
   cache against a table across DDL").

The column list used must be `o.plan.Table.Columns`, matching what `Next`
indexes today, so positions cannot drift.

### 2.2 Target 2 — `isVarlena` on `colTypeInfo`

Add an `isVarlena bool` field, resolved in `resolveColTypeInfo`, and use
`info[i].isVarlena` at `codec.go:1450`.

**It must be `catalog.PhysicalTypeIsVarlena(t)`, not `attLen == -1`.** Those
are deliberately different: `physical_align.go:78-84` documents that the
function classifies `tid`, `money`, `macaddr`/`macaddr8` as varlena although
their `typlen` is fixed, and argues that is harmless *for this consumer*.
Substituting the descriptor answer would silently change alignment behaviour
for those types. The memo must preserve the existing answer exactly.

### 2.3 Target 3 — thread `info` into the `nil` callers

Identify which callers of `decodeRowRangeInfo` pass `info == nil` on a hot
path and give them a resolved slice at their own `Open`. If a caller genuinely
cannot (no stable column list), that is a finding to record rather than force.

## 3. Correctness plan

This changes **when** a fact is computed, never **what** it is. The risks are
staleness and position drift, so:

- **Values must be byte-identical.** TPC-H 22/22 ordered digests and TPC-DS
  SF0.5 `PASS=95` all-zero, both against the tracked oracle.
- **An enum must still sort by declaration order.** This is the behaviour the
  per-row loop exists to provide (`M0097-0022`), and no TPC query exercises it.
  A dedicated test must create an enum type, index it, force an index scan, and
  assert `ORDER BY` follows declaration order rather than label text — with a
  **mutation check** that it fails when the precompute is disabled.
- **DDL staleness must be pinned**: `ALTER TYPE ... ADD VALUE` between two
  executions of the same prepared statement must be visible to the second.
- **The two scan paths must not diverge again.** The sequential and index paths
  should share the resolution helper, so a future change cannot fix one and
  miss the other — that divergence is what this item is.

## 4. What is NOT claimed

No end-to-end number is claimed until measured. The 17.94% is the *share of CPU
attributable to the call*, which is an upper bound on the saving, not a
prediction: removing it shifts the bottleneck elsewhere (`DecodeHeapTupleRowInto`
is 53.29% of the same operator), and TPC-H's wall time is not purely CPU-bound.

TPC-DS is expected to show **little or nothing** from target 1, because it is
sequential-scan dominated and already takes the fixed path. Targets 2 and 3
are where its ~3% sits. Reporting a TPC-H win as if it generalised would repeat
the error the SIMD item's report was careful to avoid.
