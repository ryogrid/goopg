# Row decode, part 2: close the `info == nil` path and the per-value varlena test

Status: DESIGN, pre-implementation. Branch: `optimize-row-decode`.
Follows `DESIGN.md` (targets 2 and 3, which that item deliberately left
unimplemented) and `REPORT.md`. Date: 2026-09-08.

## 0. Summary

Two costs remain in the decode path, and they are **one problem seen twice**:
a per-column fact is re-derived from a *string* on every value, because the
memo that already exists (`colTypeInfo`) does not reach the caller.

| target | TPC-H | TPC-DS |
|---|---:|---:|
| `pgPhysicalTypeIsVarlena` inside `decodeRowRangeInfo` | 6.22 s (5.3%) | 11.02 s (4.0%) |
| `strings.ToLower` inside `decodeRowRangeInfo` | 1.93 s (1.6%) | 2.30 s (0.8%) |
| `physicalPGTypeAlignLowered` inside `decodeRowRangeInfo` | 1.12 s (1.0%) | — |

Addressable: **~9.3 s of 117.56 s (7.9%) on TPC-H, ~13.3 s of 272.72 s (4.9%)
on TPC-DS.** TPC-DS is where the larger *absolute* prize sits, which is the
reverse of part 1 — that item was TPC-H-only.

This is, again, **sibling divergence**. `seqScanOp` resolves `o.colInfo` at
`Open` and threads it into the decoder. `indexScanOp` and `bitmapHeapScanOp`
call the public decode entry points, which hardcode `info = nil`.

## 1. Evidence

Profiles: `tpch-cold.pprof` (117.56 s of samples), `tpcds-cold.pprof`
(272.72 s), goopg at `cd637dd5b`.

`decodeRowRangeInfo` is **36.20% cum on TPC-H and 63.50% on TPC-DS**. Its
callers split cleanly by whether they pass the memo:

| caller | passes `info`? | TPC-H | TPC-DS |
|---|---|---:|---:|
| `DecodeRowRangeIntoMctxPGTupleStyled` | **no — hardcoded nil** | 34.24 s (80.45%) | 42.20 s (24.37%) |
| `seqScanOp.decodeScanRow` | yes (`o.colInfo`) | 0.59 s (1.39%) | 106.37 s (61.42%) |
| `seqScanOp.decodeScanRowRange` | yes (`o.colInfo`) | 7.73 s (18.16%) | 24.61 s (14.21%) |

The nil-info wrapper's own callers, traced to the operators:

- **TPC-H**: `DecodeHeapTupleRowInto` → 34.92 s (29.70% of all CPU), reached
  from `indexScanOp.Next`, where it is 53.29% of that operator.
- **TPC-DS**: `bitmapHeapScanOp.decodeScanRow` → 39.18 s (91% of the wrapper's
  callers), plus `DecodeHeapTupleRowInto` 3.87 s.

**Both operators have a stable column list at `Open`.** There is no reason
either cannot resolve `colTypeInfo` exactly as `seqScanOp` does; they simply
never were wired to.

## 2. Design

### 2.1 `isVarlena` on `colTypeInfo`

`decodeRowRangeInfo` at `codec.go:1450` calls `pgPhysicalTypeIsVarlena(c.Type)`
per value — **even on the non-nil path**, where `info[i]` is already in hand two
lines above supplying `align` and `lower`. The memo was applied to two of the
three per-value string derivations and this one was missed.

Add `isVarlena bool` to `colTypeInfo`, resolved once in `resolveColTypeInfo`.

**It must be `catalog.PhysicalTypeIsVarlena(t)`, not `attLen == -1`.** Those
are deliberately different: `physical_align.go:78-84` documents that the
function classifies `tid`, `money`, `macaddr`/`macaddr8` as varlena even though
their `typlen` is fixed, and argues that is harmless *for this consumer*.
Substituting the descriptor answer would silently change alignment behaviour
for those types — an on-disk-offset change, i.e. a wrong-answer class, not a
performance one. The memo must reproduce today's answer exactly.

This alone fixes the varlena cost on the `seqScanOp` paths, which is where
TPC-DS's 61.42% + 14.21% sits.

### 2.2 Thread `info` into the two nil-info operators

Give `indexScanOp` and `bitmapHeapScanOp` a `colInfo []colTypeInfo` field
resolved at `Open`, and route their decodes through `info`-carrying variants
rather than the nil-hardcoding public wrappers.

The public entry points (`DecodeHeapTupleRowInto`,
`DecodeRowIntoMctxPGTuple`, `DecodeRowRangeIntoMctxPGTupleStyled`) stay
exactly as they are — they have many callers outside the hot path, for whom
resolving a memo per call would be a pessimisation. New internal siblings take
the extra parameter.

**Staleness rule, unchanged from `colTypeInfo`'s own doc**: resolve wherever
the column list is resolved — an operator's `Open`, i.e. per execution — never
cache against a table across DDL.

**Column-list identity matters.** `indexScanOp` decodes with a `cols` variable
inside `Next`, not necessarily `o.plan.Table.Columns`. The memo must be
resolved from, and indexed by, *the same slice the decoder walks*, or entry `i`
describes a different column than the one being decoded. That is a
wrong-answer risk, not a performance one, and it is the main hazard in this
change. If the two lists can differ, the memo must be resolved against the
list actually used, or not used at all on that path.

## 3. Correctness plan

The change moves *when* a fact is computed, never *what* it is.

- **Values byte-identical**: TPC-H 22/22 ordered digests via
  `tpch-runner -diff`, and TPC-DS SF0.5 `PASS=95` all-zero. Values, not row
  counts.
- **A memo-vs-live agreement test**: for a column list spanning fixed-width,
  varlena, array, `char`/`bpchar` and the `tid`/`money`/`macaddr` cases §2.1
  singles out, assert `info[i].isVarlena == catalog.PhysicalTypeIsVarlena(t)`
  for every entry. Mutation-check it by substituting `attLen == -1` — the test
  must fail, proving it pins the distinction rather than restating it.
- **Column-list identity test**: assert that the memo an operator resolves has
  the same length and per-index types as the slice its decoder walks, so a
  future refactor that changes one and not the other fails loudly instead of
  decoding column `i` with column `j`'s descriptor.
- `-race` on `internal/executor`, and the units scope.

## 4. What is NOT claimed

The 7.9% / 4.9% figures are **shares of CPU attributable to these calls —
upper bounds on the saving, not forecasts**. Part 1 predicted 17.94% and
delivered 11.6% for exactly this reason: removing a cost re-weights whatever
is beneath it, and wall time is not purely CPU-bound. `decodePhysicalPGValueLowered`
is 48.19% (TPC-H) and 70.87% (TPC-DS) of `decodeRowRangeInfo` and is untouched
by this item; it is the next thing to look at, not this one.

Unlike part 1, **TPC-DS is expected to move here**, because its decode cost is
dominated by the `seqScanOp` paths that §2.1 fixes. A TPC-DS result of
approximately zero would mean §2.1 did not do what this document claims, and
should be investigated rather than reported as a null.
