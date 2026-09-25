# M0141-S0 — Scoping recon: sizing the `AGGSPLIT_INITIAL_SERIAL` / `AGGSPLIT_FINAL_DESERIAL` programme

| field | value |
| --- | --- |
| status | accepted |
| date | 2026-09-15 |
| task | M0141-S0 (`.ralph/fix_plan.md` M0141 section) |
| milestone | `docs/milestones/0141-upper-planner-ordering-contest.md` |
| kind | recon — measurement + design note, **no production change** |

## Mandate

Per the milestone doc, S0 must produce one of two outcomes: (1) a size for the
`AGGSPLIT_INITIAL_SERIAL`/`AGGSPLIT_FINAL_DESERIAL` programme in goopg terms,
a slice list `S1..Sn` filed into the M0141 fix\_plan section with an entry
gate, or (2) a recorded no-go. This document delivers (1).

## Headline finding: K96/K97 describe a narrower gap than they read, and it splits cleanly by corpus

K96/K97 (`METHODOLOGY3/01-what-we-learned.md`, `02-open-problems.md` §B4) name
one item — "the real item is PG's `AGGSPLIT_INITIAL_SERIAL`/`AGGSPLIT_FINAL_DESERIAL`,
a multi-round executor programme" — without separating what is already built
from what is missing, or which corpus the missing part actually gates. Both
turn out to matter.

**What already exists (do not rebuild).** goopg has had a working
Partial/Finalize aggregate split since `docs/design/parallel-query/06-parallel-aggregation.md`
(2026-07-21) landed. Confirmed live at HEAD by this recon:

- `optimizer.AggMode` (`Simple`/`Partial`/`Final`, `plan.go:1414-1425`) and
  `Aggregate.PartialSource` (`plan.go:1307-1312`) — the Finalize-to-Partial
  back-link PG gets by walking through the Gather, goopg states explicitly.
- `combineAggRuntime` (`internal/executor/parallel_agg_combine.go:53`) — a
  per-aggregate combine-rule dispatch covering every entry in the
  decomposability whitelist (`count`, `sum`/`avg`, `min`/`max`, `bool_and`/`or`,
  `bit_and`/`or`/`xor`, `any_value`, the `var_*`/`stddev_*` three-lane family,
  the `regr_*`/`covar_*`/`corr` family, and user aggregates via `CombineFunc`).
  This is PG's `aggcombinefn` catalog surface, already written.
- `AggregateIsDecomposable` (`internal/optimizer/parallel_agg.go:20-65`) — a
  deliberate whitelist (not blacklist) refusing `DISTINCT`, `WITHIN GROUP`,
  order-dependent aggregates (`array_agg`/`string_agg`), and user aggregates
  without a `CombineFunc`.
- **Two** independent producers already build `Finalize -> Gather -> Partial`
  shapes: a legacy post-plan-cache pass (`splitAggregate`,
  `internal/optimizer/parallel.go:1243-1277`, gated by five hand-calibrated
  constants in `splitAggregateIsProfitable`) and a newer costed-path version
  (`internal/optimizer/partialaggupper.go`/`partialaggpaths.go`, "C-19g
  remainder", priced through `costAgg`/`addPath`/`setCheapest` like any other
  path, gated by `GOOPG_PARTIAL_AGG_PATHS`, default off). Per the newer file's
  own header, the costed version is *measurably behind* the legacy pass it is
  meant to replace — 7/22 TPC-H matches vs 12/22 — an existing, unresolved
  regression this recon surfaces but does not fix.

**What is genuinely missing, and it is narrower than "the whole split doesn't
work".** The gap is one specific combination: `AggStrategySorted` crossed with
`AggMode ∈ {Partial, Final}` — i.e. exactly the shape PG calls `Finalize
GroupAggregate <- Gather Merge <- Partial GroupAggregate`, K96's "unreachable
by construction" node. Evidence:

- `aggregateOp.Open` dispatches to the sorted executor path (`openSorted`,
  `operators_join_agg.go:2672`) only when `Mode == AggModeSimple`
  (`operators_join_agg.go:2223`). A `Partial`- or `Final`-mode node with
  `Strategy = AggStrategySorted` has **no executor arm at all** — not a bug in
  an existing arm, an absent one.
- Today's Partial mode (hash strategy, the only one it supports) emits **zero
  rows** by design: it merges into a shared in-process accumulator
  (`aggPartialAccum`, `internal/executor/parallel_agg_split.go`) and returns
  `o.rows = nil` (`operators_join_agg.go:2350-2374`). `gatherOp` never learns
  it is carrying aggregate state — the merge happens entirely out-of-band
  (`parallel_agg_split.go:18-39`'s own design note explains why: a
  pointer-bearing `Datum` kind would cut against the pointer-free-Datum
  perf work, and a side channel keeps `Gather` aggregation-agnostic).
- **That side channel is incompatible with `GatherMerge` by construction, not
  just "awkward".** Doc 06 §3 flagged this as a risk to "re-examine during
  implementation": `GatherMerge` interleaves rows by sort key across streams,
  which requires a real row per group to interleave. A side channel that
  evaporates the row stream entirely (`o.rows = nil`) has nothing for
  `GatherMerge` to interleave. The `Finalize GroupAggregate` shape therefore
  needs a **second, row-emitting** Partial mechanism — the accumulator
  approach cannot be extended to cover it, it has to be built alongside it.
  This is the concrete content behind K97's "architecturally impossible"
  verdict on R45's fix.
- `aggRuntime` (`operators_join_agg.go:1205-1281`) is a fat struct with
  pointer fields (`numericSum Datum`, `intSx`/`intSxx *big.Int`,
  `numericSx`/`numericSxx *big.Rat`, `userState Datum`, plus fields already
  excluded from the decomposable set — `distinct`, `arrayElems`,
  `withinGroupElems`). A row-emitting Partial needs to serialize this to a
  transportable form and a row-consuming Final needs to deserialize it — this
  is PG's `aggserialfn`/`aggdeserialfn`, and doc 06 §3/§7 record that goopg's
  existing design **deliberately does not have it** ("goopg needs no
  serialisation... This removes an entire category of work"). Building the
  row-emitting path re-introduces exactly the feature the accumulator design
  was chosen to avoid, but only for the sorted/GatherMerge shape, not
  corpus-wide.

## The corpus split that changes the size estimate

`AGENT.md` §"Plan-parity harness" states the measurement fact that makes this
programme's actual scope much smaller than "fix `aggregation-strategy`/
`sort-strategy` everywhere": **TPC-H's canonical scoring protocol runs with
`-serial=true`, which sets `max_parallel_workers_per_gather = 0` on both
engines.** No Gather of any kind appears in either engine's TPC-H plan under
that protocol — confirmed already by M0140-0003's finding that the
`GOOPG_GATHER_PATHS` flip is "provably inert" to TPC-H's canonical score.

**Consequence: none of TPC-H's `aggregation-strategy` (10) or `sort-strategy`
(9) mismatches can be caused by the missing Partial/Final-Sorted/GatherMerge
machinery, because that machinery never activates under `-serial=true`.**
TPC-H's whole share of these two categories is therefore a **serial**
Hashed-vs-Sorted cost/path-selection question — a different, much cheaper
mechanism than the one K96/K97 name, and one whose executor capability
(`openSorted` under `AggModeSimple`) and planner candidate-path construction
(`groupingpaths.go`'s hashed/sorted `PathAgg` candidates, `costAgg`-priced and
adjudicated by `addPath`/`setCheapest` like any other path) **already exist**.
This recon found `createplansimple.go:173,219` already wires the winning
path's `AggStrategy` onto the executor node's `Strategy` field in both
construction sites — which appears to contradict the stale-looking comment at
`plan.go:1346-1348` ("The planner does not set it yet"). That contradiction is
unresolved by this recon and is exactly the first thing S1 must settle before
writing any new code: it is plausible TPC-H's whole aggregation-strategy/
sort-strategy gap is a live cost-comparison or pathkey-availability defect in
already-shipped machinery, not a missing capability at all.

TPC-DS is different: its `parallelism` category is itself 87/99 mismatched
(M0137/M0140), so most TPC-DS queries where PG picks a parallel
`Finalize GroupAggregate` shape are *already* blocked on the parallelism floor
before aggregation-strategy is even reachable. TPC-DS's 69/76 counts are an
unseparated mix of (a) the same serial Hashed-vs-Sorted question TPC-H has,
and (b) genuine parallel Partial/Final-Sorted cases gated behind M0140's own
open items (K92's Parallel-Hash execution model, K41's heap-density floor).
Sizing (b) precisely requires a live capture+category diff restricted to
TPC-DS's non-parallel-blocked queries — a measurement this recon does not run
(S0's mandate is programme size, not a live corpus pass; S1 below is priced to
include it as its own first step).

## Slice list

| slice | scope | new machinery needed | entry gate |
| --- | --- | --- | --- |
| **S1** | Serial Hashed-vs-Sorted cost/path-selection audit — TPC-H's *entire* share of `aggregation-strategy`(10)/`sort-strategy`(9), plus a live capture+diff to split TPC-DS's 69/76 into serial-shaped vs parallel-shaped. | None expected — `openSorted`, the sorted `PathAgg` candidate builder, and `createplansimple.go`'s `Strategy` wiring already exist. First action: resolve the `plan.go:1346-1348` vs `createplansimple.go:173,219` contradiction found above. | **Selectable now.** No dependency beyond M0137. |
| **S2** | Fix whatever S1 finds (cost formula, pathkey availability, or a genuine wiring gap) for the serial case, on TPC-H first, then the TPC-DS non-parallel-blocked subset S1 identified. | Depends on S1's finding; expected to be an existing-file edit (`groupingpaths.go` cost terms or `costAgg`), not a new subsystem. | Needs S1's finding. |
| **S3** | Partial-Sorted row emission: a second Partial-mode code path (`Strategy = AggStrategySorted`) that emits real rows — group key plus one serialized-state column per aggregate — instead of merging into `aggPartialAccum`. Reuses `combineAggRuntime`'s existing combine rules; does not touch the hash-strategy accumulator path, which stays as-is for the shapes it already serves. | New executor operator variant. | Gated on S1/S2's live measurement showing a TPC-DS-specific, genuinely parallel-shaped residual large enough to justify it — do not start on K96/K97's say-so alone; the corpus split above means the multi-round programme may earn back far less than its name suggests once S1/S2 land. |
| **S4** | `aggRuntime` serialize/deserialize, bounded to the decomposable whitelist's actual pointer surface (`numericSum`, `intSx`/`intSxx *big.Int`, `numericSx`/`numericSxx *big.Rat`, `userState`, the float/bool/count/sum scalars) — `DISTINCT`/`WITHIN GROUP`/`array_agg`/`string_agg` are already refused by `AggregateIsDecomposable`, so they are out of scope here too. This is PG's `aggserialfn`/`aggdeserialfn`, deliberately absent per doc 06 §3/§7. | New serialize/deserialize functions, one bytea-shaped encoding per decomposable aggregate family. | Needs S3 (defines the transport shape) landed or in progress. |
| **S5** | `GatherMerge`-fed Finalize-Sorted: a new merge-combine algorithm that consumes the key-interleaved stream `GatherMerge` produces from S3/S4's rows and folds same-key runs across workers via `combineAggRuntime` before finalizing — replacing today's Finalize path, which only handles "drain the Gather to EOF, then read the whole accumulator" (correct for the hash/side-channel shape, wrong for a merge-ordered stream where the same key can recur). | New executor operator. | Needs S3+S4. |
| **S6** | Wire the new shape into a plan producer (decide which of the two existing producers — `parallel.go`'s legacy pass or `partialaggupper.go`'s costed-path version — hosts it, given the costed version's own 7/22-vs-12/22 regression is unresolved and out of this recon's scope to fix), fix the `Aggregate`/`GroupAggregate (N keys)` EXPLAIN mislabel (doc 06 §4.1 — both currently render as a hash aggregate regardless of `Strategy`), and re-measure the full corpus. | Plan-producer wiring + EXPLAIN fix, no new algorithm. | Needs S5. |

## What this recon deliberately does not do

- **Does not enumerate which specific TPC-H/TPC-DS queries** carry each
  category tag today — that is S1's first action (a live capture+diff), not
  a size-of-programme question.
- **Does not fix** the costed-path producer's 7/22-vs-12/22 regression against
  the legacy pass; noted for S6 to weigh, not resolved here.
- **Does not resolve** the `plan.go:1346-1348` comment vs `createplansimple.go`
  wiring contradiction; flagged for S1 to settle first.

## Why this is not a no-go

Aggregation-strategy and sort-strategy are, per K24, the largest named
category pair in the workstream (TPC-DS 69+76, TPC-H 10+9). This recon found
that a meaningful share of that count — all of TPC-H's, and an unmeasured but
plausibly large share of TPC-DS's — is reachable through S1/S2 alone, using
machinery that already exists, at a cost far below "a multi-round executor
programme". The expensive programme (S3-S6) remains real and is not
minimised: `GatherMerge`'s incompatibility with today's side-channel design is
now confirmed, not just risked, and building a second, row-emitting,
serialize-capable Partial/Finalize path is genuinely comparable in scope to
the C-19 series slices M0140-0004 and M0140-0005 already declined to attempt
in one task. But firing that expensive programme should follow, not precede,
the cheap measurement in S1 — starting there is the entry gate's whole point.
