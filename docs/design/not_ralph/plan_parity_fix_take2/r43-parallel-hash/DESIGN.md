# R43 — parallel-hash parity, re-scoped (K79/K80)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: DESIGN **rev 3** — rev 2's review settled the gating question and
re-sequenced; rev 3 adds the HEAD measurement (§7a) that REFUTES rev 2's
sequencing claim. Rev 1's plan was mis-scoped; see §9.*

## 1. How this was found — a method change worth keeping

Every round this session chased *declines* on TPC-DS Q78. Ranking TPC-H by
**how few categories each query differs in** points elsewhere:

| categories differing | query |
|---|---|
| 0 | **Q13, Q6 — MATCH** |
| **1** | **Q14 — `[parallelism]` only** |
| 2 | Q1 — `[sort-strategy, parallelism]` |
| 4-5 | Q4, Q11, Q12, Q19, Q2, Q20, Q21, Q22 … |

**Correction to a figure this workstream has repeated: TPC-H is
`match=2`, not 1/22.** Q13 *and* Q6 match at HEAD. The 1/22 came from the
R36 baseline and was carried into every later report without
re-measurement. Authoritative at HEAD: `queries=22 match=2 shapediff=20`;
`join-order=18 join-method=12 scan-type=13 parameterisation=6
aggregation-strategy=10 sort-strategy=13 parallelism=18 qual-placement=7
rendering=7`.

## 2. The gap on the nearest-miss query

```
PG                                    goopg
 -> Parallel Hash Join                 -> Hash Join
      -> Parallel Seq Scan on lineitem     -> Parallel Seq Scan on lineitem
      -> Parallel Hash                     -> Seq Scan on part
           -> Parallel Seq Scan on part
```

## 3. Scale

| corpus | PG queries using `Parallel Hash` | true `Parallel Hash` **build** nodes |
|---|---|---|
| TPC-H | **7 / 22** (Q3 Q9 Q10 Q14 Q16 Q18 Q21) | **9** |
| TPC-DS | **69 / 99** | **157** |

*(Rev 1 said 18 / 310. That was a bare `grep -c "Parallel Hash"`, which
also matches `Parallel Hash Join` / `… Semi Join` lines — roughly double.
Query counts were correct and stand.)*

## 4. The gating question, ANSWERED: TRUTHFUL (K79)

The goal forbids forcing plans to match, so rev 1 refused to proceed until
this was settled from source. It now is, and the answer is **truthful**:
goopg's cooperative build really does partition the BUILD relation's scan
across workers.

Rev 1 feared `coopDrivingScan`'s "widened by exactly one node kind: a HASH
join's PROBE side" meant it only ever partitions a *probe* scan. **That
reading was wrong.** The walker is applied to the **build** plan; the
probe-side widening only governs how it descends *through* a nested join
found inside the build subtree:

- `parallel_hash_build.go:522-531` — `buildPlan := o.plan.Right` (or
  `.Left` when `buildLeft`), then `coopDrivingScan(buildPlan)`. The
  argument is the build child, never the probe.
- `:609` — ONE shared `pscan := newParallelScanState(0)`.
- `:663-680` — N producers rebuild the build subtree and
  `attachParallelScan(tree, pscan)` wires that single shared block
  allocator into the driving `seqScanOp`, so **each producer claims a
  disjoint block range**. `newParallelScanState` is a shared atomic
  allocator, not per-worker.
- `operators_join_agg.go:666-668` — `buildLazyHashTable` itself dispatches
  to `parallelBuildLazyHashTable` when eligible, so the leader pre-build
  (`prebuildSharedHashJoins`) and the cooperative build **compose**; they
  do not compete.

For Q14 specifically, `parallelBuildEligible` passes every gate (INNER;
no `preserveCTIDRel`; `coopDrivingScan` non-nil; `part` ≈ 3800 blocks vs
the 1024-block threshold). So today goopg's plan text **under-describes
what it runs**, and correcting it is not relabelling.

**Two caveats that must stay in the doc:**

1. goopg parallelises the build **scan+filter**; hash *insertion* is
   single-consumer, where PG parallelises insertion too. EXPLAIN claims
   nothing about insertion, so the label stays true — but this is not full
   semantic equivalence and must not be asserted as such.
2. `parallelBuildEligible` has **no parallel-mode gate at all** — it fires
   in *serial* queries too, where PG shows a plain `Hash`. So "the
   executor does it in parallel" must NOT be used as the labelling rule.
   The label must follow the PATH MODEL, not observed executor behaviour.

## 5. The `GOOPG_GATHER_PATHS` flip — a separate, independently valuable win

*(Rev 2 called this "the real first step". §7a's HEAD measurement shows it
is independent of Q14, not a prerequisite for it. Kept because it is worth
landing on its own merits.)*

Rev 1's biggest error. **`Parallel Hash Join` needs no `parallel_hash=true`
work at all.** `addPartialHashJoinPath` already sets `ParallelAware: true`
(`joinpathsparallel.go:205`). It is dead solely because the partial-path
machinery sits behind a default-OFF knob: `gatherPathModeFromEnv`'s default
arm returns `gatherPathsOff` (`gatherpaths.go:70,82`), and every partial
path producer returns early at `joinpathsparallel.go:89`.

R10 measured the flip as `Parallel Hash Join` **0 → 19 on TPC-H** and
**0 → 132 on TPC-DS** (PG: 9 and 139) with TPC-H `parallelism` **18 → 15**
— with *no* parallel-hash work whatsoever. Those numbers are stale;
§7a re-measures at HEAD (`parallelism` 18 → 16, net −4/+1 categories).

**The flip is not landed at HEAD, and R13 (adjudicate the failures R12
left — 5 at HEAD, see §7b) is its hard prerequisite.** Rev 1 never mentioned this existed. As rev 1 was
written, its step 2 would have landed code behind
`if gatherPathsMode == gatherPathsOff { return }` — **inert at the
shipping default and unmeasurable by the sweep it named as its binding
gate.** Rev 1's §9 risk paragraph was therefore false at the default.

## 6. Work — two INDEPENDENT tracks (see §7a)

1. **R13 + the flip** — adjudicate the remaining failures, land
   `GOOPG_GATHER_PATHS` on by default, regenerate
   `scripts/planner-flags.env`. Largest parity-per-risk ratio available;
   this is its own round.
2. **Independently**, the Q14 gap, which is one node:
   `Seq Scan on part` → `Parallel Seq Scan on part`. That is what
   `try_partial_hashjoin_path(parallel_hash = true)` buys —
   read `inner.PartialPathlist`, price it PG-faithfully, let
   `add_partial_path` choose.
3. Wire `enable_parallel_hash`: the GUC is declared
   (`catalog/catalog.go:12171`, default on) but **nothing reads it**.
   Landing (2) without wiring it repeats this repo's known
   declared-but-unconsumed-GUC failure mode.

**Dropped from the parity justification:** rendering a standalone
`Parallel Hash` *node*. The differ splices PG's `Hash`/`Parallel Hash`
nodes out entirely (`pg-plan-parity-diff.py:571-574`, normalisation N2;
`Hash` is deliberately excluded from MISSING-NODE). goopg emits zero
`Hash` nodes anywhere, and the metric does not care. Q14 needs exactly two
existing flags: `Join.ParallelAware` and `SeqScan.Parallel`. Keep the node
work, if at all, as separate EXPLAIN-fidelity work (a real 19/114-node
divergence the differ normalises away).

## 7. The inverse risk step 2 must design against (K80)

If the path model emits `parallel_hash=true` and cost picks it, EXPLAIN
prints `Parallel Seq Scan on <build>` — but `parallelBuildEligible` can
still **decline at runtime** (build under 1024 blocks, `preserveCTIDRel`
set, `coopDrivingScan` nil for an Aggregate/Sort/index-scan build side,
or `GOOPG_COOP_JOIN_BUILD=off`). The plan would then claim parallelism the
executor does not perform — a NEW misdescription in the opposite
direction, and precisely the "arbitrary" outcome the goal forbids.

So the planner predicate must be a **pinned twin** of
`parallelBuildEligible`: same block threshold, same `coopDrivingScan`
shape test, following the existing `drivingScan`/`probeSideIsLeft`
twinning pattern, and pinned by a test the way `probeSideIsLeft` is.

## 7a. MEASURED at HEAD (rev 3) — what the flip actually buys, and what it does not

R43 rev 2 relied on R10's numbers. Those are stale (R40/R41/R42 have
landed since). Re-measured at HEAD, TPC-H, `GOOPG_GATHER_PATHS=all` vs the
shipping default:

| category | flip off | flip on |
|---|---|---|
| join-method | 12 | **11** |
| scan-type | 13 | **12** |
| parallelism | 18 | **16** |
| qual-placement | 7 | **6** |
| aggregation-strategy | 10 | **11** |
| join-order / parameterisation / sort-strategy / rendering | 18 / 6 / 13 / 7 | unchanged |
| **match** | **2** | **2** |

Net **−4 categories, +1**. Worth landing on the category metric — but it
produces **no new match**, and R10's "parallelism 18 → 15" is 18 → 16 at
HEAD.

**The finding that changes §6's ordering: Q14 is BYTE-IDENTICAL under the
flip.** Still `Hash Join` over `Seq Scan on part`, still
`SHAPE-DIFF [parallelism]`. Its `Gather` already comes from the
partial-aggregate path, not from this knob, so the flip is *orthogonal* to
Q14. The flip changes other queries.

Consequence: **step 2 (`parallel_hash=true`) is what Q14 needs, and it does
not depend on step 1.** Rev 2 implied the flip would move Q14 toward the
match; measurement says it does not. The two steps are independent:

- step 1 (flip, needs R13) — a −4 category win across the corpus, no match;
- step 2 (`parallel_hash=true`) — the only thing that can close Q14.

Either may be done first. Step 2 is no longer blocked on step 1, which was
rev 2's central sequencing claim.

## 7b. R13's actual scope, re-measured at HEAD

R12 left 7 failing tests under the flip; at HEAD **5 fail**, two having
been fixed by intervening rounds:

| test | kind |
|---|---|
| `TestPartialPathIsNeverTheFinalPath` | stage pin — *"join rel 0x3 has partial paths before C-19d"* |
| `TestSplitEqualityForHashMultiKey/searched_enumerator` | **real** — *"fell back to Nested Loop"* |
| `TestSlice3LiveQ9ShapeDerivation` | **real** — different build sides narrowed (Q9) |
| `TestSlice3FilterColumnSurvivesNarrowing` | **real** — narrow-build/filter-column pin |
| `TestOwnedBuildPoisonPrebuiltBoundary` | **real** — narrow-build boundary |

The four "real" ones must be adjudicated **against PG**, not against the
new output: the question is whether the shape the flip produces is the one
PG produces. That is the goal's criterion and it is what R13 is for.

## 8. Prediction, recorded before implementing (METHODOLOGY §2)

- The flip (step 1) takes TPC-H `parallelism` 18 → 16 and is −4/+1 on
  categories overall — MEASURED at HEAD (§7a), not predicted.
- Step 2 should make **Q14 goopg's first new MATCH (3/22)**, since
  `parallelism` is its only differing category. Step 2 does NOT depend on
  step 1: Q14 is byte-identical under the flip.
- **TPC-DS will NOT jump to 69 matches.** Those queries differ in several
  other categories; `Parallel Hash` is one. Expect a large drop in the
  `parallelism` count with few or no new matches — the same "necessary but
  not sufficient" shape as R41's eligibility gain.
- **MATCH here is shape-only.** Q14's row estimates stay ~300× apart
  (goopg `Hash Join rows=6,001,255` vs PG `rows=18,444`; goopg's
  `lineitem` scan does not apply the `l_shipdate` selectivity at all).
  N1 pushes estimates to a side column, so the differ does not see it.
  A match on this metric is not estimate parity, and the report must say
  so.

## 9. Cost consequence rev 1 elided

`initial_cost_hashjoin`'s `startup_cost += inner_path->total_cost` with
`parallel_hash=true` charges the **already-divided** partial inner —
the build is priced at ~1/N. So "no participant multiplier" is right, but
this is not unchanged pricing: it is an **~N× cheaper build** for every
hash join that gains the path, and *that*, not the label, is what will
drive corpus-wide churn. It happens to be more faithful to what goopg's
coop build actually does, which strengthens §4's verdict.

## 10. Review record

Adversarial subagent review, full HEAD re-derivation. It **verified** the
`joinpathsparallel.go` refusal quote, the structural non-reading of
`inner.PartialPathlist`, the D-05 multiplier reasoning against PG's
`costsize.c:4187`, `try_partial_hashjoin_path` at `joinpath.c:1290/1299`,
the Q14 gap diagram, `match=2`, `Q14 [parallelism]`, the 7/22 and 69/99
query sets, and the coop build being default-ON.

It corrected rev 1 on seven points, all adopted: the TRUTHFUL verdict with
its evidence chain (§4, refuting rev 1's own doubt); the ~2× inflated
occurrence counts (§3); the unmentioned `GOOPG_GATHER_PATHS`/R12
prerequisite that made rev 1's plan inert at the default (§5); the
inverse-misdescription risk (§7); the unconsumed `enable_parallel_hash`
GUC (§6.3); that the `Parallel Hash` node buys no parity because the
differ strips it (§6); and that MATCH is shape-only with estimates still
300× apart (§8).
