# R3 — the memory-blind HashAggregate

*Round 3 of `../TODO.md`. Supersedes the round K11a described; §0
records why that description was wrong.*

## 0. Correction to K11a (my own claim, from R2 §6)

R2's report asserted that **"goopg's planner never chooses a sorted
aggregate"**, citing a comment in `operators_explain.go`: *"The planner
does not set Strategy yet, so a hand-built node is the only way this
renders GroupAggregate today."*

**That comment is stale and the claim was wrong.** The planner sets
`AggStrategySorted` in two places, and — checked at the call sites, not
just read (K4) — `addGroupingPaths` (`groupingpaths.go:378-400`) builds
**both** sorted variants: the index-driven one *and* a plain
`Sort`-then-`GroupAggregate` candidate over the ordinary seed
(`sortPathForBounded(seed, ...)` → `addPath(..., AggStrategySorted)`).

So the candidate is generated and offered to `addPath` on essentially
every grouped query. This is a **costing** finding, not a missing
candidate — the same correction the root-causes document had to make in
its own §6, made again, from a stale comment this time instead of a dead
function. The error class is identical: **believing a comment about what
the code does instead of checking what it does.**

The *measurement* behind K11a stands and is what matters:

| TPC-DS SF0.5, node counts | goopg | PG 18.3 |
|---|---|---|
| `GroupAggregate` | **1** | **100** |
| `HashAggregate` | **133** | 29 |
| `Partial/Finalize GroupAggregate` | 0 / 0 | 13 / 14 |
| `Partial/Finalize HashAggregate` | 12 / 12 | 5 / 4 |

The two engines' choices are very nearly inverted, with the candidate
present on both sides.

## 1. Cause, from goopg's own source

`costAgg` (`cost_funcs.go:370-420`) states it:

> *"No spill arm exists … `aggregateOp` performs grouped aggregation in
> memory with no spill path, so there is nothing to charge."*

PG's `cost_agg` charges one (`costsize.c:2783-2840`): when the hash
table exceeds the hash-memory limit it accrues spill writes at
`random_page_cost`, reads at `seq_page_cost`, both doubled by an
explicit HashAgg-vs-Sort penalty, plus `depth × input_tuples × 2 ×
cpu_tuple_cost` of CPU.

goopg's hashed arm is therefore **memory-blind**: it prices a
100-million-group hash table exactly as it prices a 10-group one, and
its sorted rival always pays a full `Sort`. Hash wins by construction on
any large-cardinality grouping — which is precisely the population where
PG spills and picks `GroupAggregate`. That is the inversion in §0.

## 2. The standing objection, and why the round re-opens it

The same comment records a previous attempt:

> *"a fictional charge flips real plans (measured: it drove Q3/Q10/Q13/Q18
> to sorted, Q13 5.67 s → 8.71 s, all four away from PG's hash)."*

Two of the three reasons that objection carried have since dissolved:

1. **The timing half is no longer a veto.** The goal rule for this
   workstream is explicit: if the plan matches PG, a slower runtime is
   **not** a regression. `Q13 5.67 s → 8.71 s` cannot block this round;
   only a *parity* move can.
2. **The parity half was measured against an invalid reference.** K9/K10:
   TPC-H was diffed against a stale serial fixture and TPC-DS ran at
   128x different `work_mem`. "Away from PG's hash" was asserted against
   a target we now know was wrong. It must be re-measured, not inherited.

The third reason stands and is the round's real risk:

3. **goopg's executor genuinely cannot spill a hash aggregate.** Charging
   for I/O the executor will never perform is a fiction.

**Why the fiction is nonetheless the PG-faithful choice here.** The
charge's *purpose* in PG is not to predict goopg's I/O; it is to express
"this hash table does not fit". That fact is true of goopg too — more
so, because goopg cannot even spill in response to it. The charge steers
the planner to `GroupAggregate`, whose input `Sort` **does** spill in
goopg (`tuplesortMergeOrder` and the run-merge model are implemented).
So the arm moves goopg from a plan it cannot execute safely to one it
can. The comment's own words — *"A memory-blind model that picks hash
for a 100M-group query risks the OOM instead, but that failure already
exists today"* — describe a defect this round removes rather than adds.

## 3. Transcription (read-only `postgres/`)

`cost_agg` (`costsize.c:2783-2840`), applied for `AGG_HASHED`:

```
hashentrysize = hash_agg_entry_size(numTrans, input_width, transitionSpace)
hash_agg_set_limits(hashentrysize, numGroups, 0, &mem_limit, &ngroups_limit, &num_partitions)
nbatches   = max(ceil(max(numGroups*hashentrysize/mem_limit, numGroups/ngroups_limit)), 1)
depth      = ceil(log(nbatches) / log(max(num_partitions, 2)))
pages      = relation_byte_size(input_tuples, input_width) / BLCKSZ
written = read = pages * depth;  read *= 2;  written *= 2
startup += written*random_page_cost + depth*input_tuples*2*cpu_tuple_cost
total   += written*random_page_cost + read*seq_page_cost + (same CPU)
```

`hash_agg_entry_size` (`nodeAgg.c:1701`):
`MAXALIGN(MAXALIGN(SizeofMinimalTupleHeader) + tupleWidth) + numTrans *
sizeof(AggStatePerGroupData)` (+ a transition-space chunk when
`transitionSpace > 0`).

`hash_agg_set_limits` (`nodeAgg.c:1809`): when
`input_groups * hashentrysize <= hash_mem_limit` there is **no** spill —
`mem_limit = hash_mem_limit`, `ngroups_limit = hash_mem_limit /
hashentrysize`, `num_partitions = 0`, and `nbatches` collapses to 1,
`depth` to 0, and the whole arm to **zero**. This is the property that
makes the change safe for small groupings: it is exactly inert below the
threshold, so no plan that fits in memory can move.

`hash_mem_limit` is `get_hash_memory_limit()` = `work_mem *
hash_mem_multiplier`.

## 4. The change

One arm in `costAgg`'s hashed branch, behind the same inputs the
function already carries. `inNcols` and `inAvgVarBytes` are already
threaded to every caller and the function's own comment says they were
kept *"so the resume does not re-plumb callers"* — this is that resume.

- `input_width` = `inAvgVarBytes` (the input tuple's average width).
- `numTrans` = `nAggs`.
- `transitionSpace` = 0 (goopg has no per-aggregate transition-space
  estimate; PG's own arm degrades to the same when `transitionSpace = 0`,
  and getting it wrong can only *under*-charge, never invent a spill).
- `work_mem` / `hash_mem_multiplier` read from `costParams`.

Not changed: the sorted arm, the plain arm, `numGroups` estimation, any
producer's admission logic, `enable_hashagg`. No candidate is added or
removed.

## 5. Gates and the falsification this round owes

- Unit: (a) **inertness below the threshold** — a grouping that fits
  prices bit-identically to today (this is the load-bearing pin);
  (b) the charge appears above it; (c) `depth` and `nbatches` reproduce
  PG's formula on a worked example; (d) startup and total both move, and
  by PG's respective amounts (writes on both, reads on total only).
- Suites: optimizer + executor.
- **Values: both corpora, all-zero.** A cost change that moves
  aggregation strategy is exactly the shape that can expose an executor
  bug in the less-travelled `AggStrategySorted` path — this is the
  round's real correctness risk, not the costing.
- **Parity: both corpora, adjudicated per query, against the LIVE
  references** (K9/K10). The round SUCCEEDS if TPC-DS `GroupAggregate`
  adoption moves toward PG's 100 and the match count does not fall.
- **The falsification**: if goopg moves to sorted where PG stays hashed
  (the old objection's TPC-H Q3/Q10/Q13/Q18), the arm is not the
  problem — `numGroups` or `inAvgVarBytes` is, since PG runs the same
  formula and keeps hash. In that case the round reports the input
  defect and does **not** tune the arm to compensate. Stated in advance
  so it cannot be rationalised afterwards.

## 6. Prediction, stated in advance

TPC-DS `GroupAggregate` adoption rises substantially from 1. TPC-H moves
little: its groupings are small (Q1 has 6 groups, Q13 200), so §3's
inertness property should leave most of it untouched — which also makes
TPC-H the natural check on the old objection. Match counts rise on
TPC-DS or stay flat; a fall on either corpus is a failure of the round.

## 7. Review record

Subagent delegation remains unavailable in this environment (`Task` not
exposed; recorded since R0). Adversarial self-review performed, with
these falsifications actually run rather than asserted:

- **The K11a claim was falsified** (§0) by grepping `AggStrategySorted`
  and then reading `addGroupingPaths` to the end — the Sort-driven
  `addPath` sits *after* the index-driven `return`, which is why a
  partial read missed it. Recorded as an error of the same class the
  root-causes document already carries.
- **The inversion was re-counted** from the R2 capture pair, not quoted
  from R2's prose.
- **The inertness property was derived from `hash_agg_set_limits`'s
  early return**, which is what makes the "this will move everything"
  worry false; it is pinned by a unit test rather than argued.
- **The old objection was not dismissed**: two of its three legs are
  shown to have dissolved for stated reasons, and the third is answered
  on its merits (§2) rather than waved past.
