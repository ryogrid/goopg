# Milestone 0127 — PG-shaped join search (PG-identical join enumeration)

**Status:** planned
**Filed:** 2026-08-03
**Reference plan:** `.ralph/fix_plan.md` (M0127 section)
**Design of record:** `docs/design/leftdeep-joins/` — 10-chapter design bundle
(README / 01–09 / IMPLEMENTATION-TODO). **The implementation plan (task breakdown)
is `docs/design/0127-pg-shaped-join-search.md`**. The bundle chapters are the sole
design authority; the implementation plan references them by pointer, never
restating design content. Files under leftdeep-joins/ are not modified.
**Prerequisites:** completion of the remaining M0125 items that M0127 needs first
(exprwalk stabilisation = M0125-0002 commits 5–8, deterministic tie-break =
M0125-0047). (M0125-0040 ROLLUP is an independent track outside bundle scope and
is not an M0127 prerequisite.)
**Branch:** derived from `tpcds-fix2` (all implementation tasks executed in git
worktrees off pinned clean HEAD, staged by explicit pathspec, re-running own
guard tests after rebase/handoff — inherits the 0126 discipline)

## Background

M0126 under `analysis/cost-driven-second-try-200731/` terminated as a **documented
no-go** on 2026-08-03 (`evidence/acceptance-run-2.txt`: Q9 remained hang-class,
the -0013 build-side memory penalty newly regressed Q5 to 600 s+). At M0126's
close, goopg's join planner is in a double deadlock:

- **Planner side** — the subset-bitmask DP (`enumerateBushyPlans`) is trapped in a
  costed search space where M0126-0013 measured "no good order exists." Q9's
  order blocker (FK-chain ndistinct-product explosion → class-(a), unpriced
  consecutive 6M-row hash builds → class-(c)) cannot be fixed within this search.
- **Executor side** — the post-MHJ-retirement binary cascade (M0126-0011,
  `mhjPackingEnabled=false`) re-materialises at every probe seam, so Q3/Q10/Q18/Q7
  pay seam costs. Runtime fusion (M0126-0006/-0007) is permanently OFF for
  correctness and is not a viable path.

This milestone converts the `docs/design/leftdeep-joins/` bundle into **shipped
behaviour**, per the user directive (2026-08-02, amended 2026-08-03): replace
join search with a full three-phase PG 18.3 `standard_join_search` /
`join_search_one_level` analogue (clause joins + bushy + last-ditch), constrain
emitted plan trees to PG-shaped binary joins (left-deep chains + bushy
composite-composite), rework the join executor to PG-grade efficiency (streaming
probe, zero intermediate materialisation, multi-column keys, work_mem-bounded
hybrid hash spill), and render `MultiHashJoin` and runtime fusion **deletable**.
The entire bundle remains DESIGN ONLY; this milestone is the first implementation
vehicle, and the bundle chapters are the implementation spec.

## Goals (from the bundle README scope)

1. **PG-shaped plan trees** — every emitted plan is a binary join
   (`*planner.Join` only; `MultiHashJoin` deleted as a plan node) whose shape
   PG 18.3's `join_search_one_level` can produce (left-deep chains *and* bushy
   composite-composite joins).
2. **PG-shaped level-wise DP** — replace the subset-bitmask DP with a PG-shaped DP
   (`standard_join_search` / `join_search_one_level` analogue over `RelOptInfo`
   pathlists, all three phases). Join methods are generated as costed paths
   **inside** the search (no post-DP method rewrites).
3. **Fusion-free join executor** — a binary hash-join cascade that is
   executionally identical to MHJ (N hash tables built at Open, one streaming
   probe pass, zero intermediate materialisation) and additionally degrades
   gracefully under large builds via PG's hybrid hash spill instead of OOMing.
   Makes `MultiHashJoin` and `fusedHashJoinOp` permanently unnecessary.

## Acceptance bar (`docs/design/leftdeep-joins/09-verification-and-acceptance.md` §3 is normative)

This section is a transcription of 09 §3 and the synchronisation source for the
fix_plan M0127 section and the milestones-README row. **The norm is 09 §3** —
when 09 §3 changes, update from there.

> **M0127 acceptance bar (S5 exit).** TPC-H SF1, fresh capped server per arm,
> symmetric 600 s timeout, server age held constant (sweep-tail discipline):
>
> 1. **22/22 complete** — zero hangs / OOMs / timeouts / row-count mismatches.
> 2. **Total wall time ≤ 1.2×** — against the faster of pinned R0 (493.31 s)
>    and the same-HEAD contemporaneous integer arm.
> 3. **No single query > 2× R0** — Q9 is explicitly **≤ 170.9 s**
>    (2 × R0's 85.46 s; the integer default arm's 58.83 s is the aspirational target).
> 4. **TPC-DS SF0.5: zero deltas** — zero row-count deltas and zero checksum
>    deltas (against git-tracked oracle).
> 5. **No `MultiHashJoin` in emitted plans, fusion never triggers**
>    (verified via EXPLAIN sweep of both suites).
> 6. **Bushy capability (PG-identical search)** — on every searched query where
>    PG 18.3's EXPLAIN shows a bushy join spine (composite ⋈ composite), goopg
>    must be able to produce the same composite⋈composite pairing with the same
>    relset split (verified via the §4 parity gate spine diff). Alternative
>    shapes based on cost constants / statistics fidelity are tolerated under
>    the ratchet, but **a bushy shape PG can produce that goopg's search cannot
>    express is a hard failure** (02's contract is "PG-identical shape," not a
>    trade).
>
> A documented no-go (with §6 attribution) is also a successful S5 completion —
> in that case the flag remains OFF and the bundle's planner half returns to
> design. The executor stages S0–S4 stand independently on their own gates
> (below).
>
> **S1 exit (in-flight gate):** Q3 / Q10 / Q18 / Q7 each ≤ 1.2× their R0
> times (8.46 / 6.04 / 27.58 / 25.13 s; R0 = integer+MHJ pinned,
> total 493.31 s), remaining queries ≤ 1.2× pre-S1 HEAD.
> Artefact `analysis/leftdeep-joins/<date>-s1-ab.txt`.
>
> **S3 exit (in-flight gate):** Q21 completes at SF1 under the standard
> cgroup cap and default `work_mem`; forced-spill run (lower `work_mem` on Q3
> to nbatch ≥ 4) returns byte-identical results to the no-spill run.
> Artefact `analysis/leftdeep-joins/…-s3-spill.txt`.

## Relationship to M0125 / M0126

- **Successor to M0126.** M0126-0013's terminal no-go named `docs/design/leftdeep-joins/`
  as its successor ("join-search restructure absorbing the Q9 enumeration
  blocker"). M0126's open tail (-0013's "join-enumeration improvement or
  fusion-operator integration" residual, -0004's slot-chaining deferral) is
  resolved by this bundle.
- **M0126-0004's deferral is un-deferred at S1** (P1.1 = legacy-path seam
  de-materialisation carries the slot chaining — 05 §2).
- **Physical deletion of M0126-0011 (MHJ retirement) is P6.2** (08 §4 deletion
  inventory). `mhjPackingEnabled` / `SetMHJPackingEnabled` /
  `GOOPG_MHJ_PACKING_OFF` remain revivable until deleted at S7 (08 §2 rollback
  contract).
- **Physical deletion of M0126-0006/-0007 (fusion, permanently OFF) is P6.1**
  (`fused_hash_join.go`, hook, env vars, orphan-export check).
- **Inherits M0126's acceptance protocol** (symmetric timeouts, order-attribution
  taxonomy, class-(a)/(c) analysis — 09 §6). The `GOOPG_COST_DRIVEN_JOINORDER`
  flag and its documentation are **retired at S5** (replaced by
  `GOOPG_PGSHAPED_DP`, 08 §6).
- **Obsoletes specific M0125 tasks** (this milestone's implementation directly
  subsumes their acceptance). Skip annotations are applied on each task in
  fix_plan:

| M0125 task | Reason obsoleted (which M0127 artefact subsumes it) |
|---|---|
| **M0125-0031** (warm-stats planning line: timeout-class elimination + runtime optimisation) | Timeout-class elimination and Q18/Q7/Q3/Q10 recovery are this bundle's acceptance itself (09 §1/§2/§3). Remaining fixes are subsumed by the S0–S5 stage gates |
| **M0125-0032** (TPC-H Q21 shape-class timeout) | Q21 completion (OOM stop removal) is the S3 exit gate itself (06 hybrid hash spill) + the 22/22 bar. The post-M0077 shape issue is counted among this bundle's recovery targets per 01 §6(3) |
| **M0125-0033** (TPC-DS Q18 warm 2.1× regression) | Q18 ≤ 1.2× R0 is the S1 exit gate (05 seam de-materialisation) + 01 §6(1) |
| **M0125-0037 stage (ii)** (DP cannot see through set-op nodes) | P5.1's `PathPrebuilt` leaves (subquery/CTE/VALUES/pinned unnest) close the leaf-whitelist gap. Acceptance row (Q5 `5\|OK\|100`) is already green (measured 2026-07-31) |
| **M0125-0041** (C3 second half: Q30/Q81 correlated scalar aggregate) | The residual is C1 = `Nested Loop (CROSS)` shape; P5's DP (join methods generated inside the search) fixes it as the successor to `-0034`'s join-order arm. Q30/Q81 are covered by the SF0.5 zero-delta gate |

- **Remaining M0125 items that must complete before M0127 are kept as-is:**
  - **M0125-0002 (exprwalk commits 5–8)** — walker substrate stabilisation. P5.5's
    searched-subtree tagging and "legacy passes skip searched trees" contract,
    and P6.3's old-DP deletion, rest on this substrate. 4 of 8 commits done.
  - **M0125-0047 (restart non-determinism tie-break)** — must close first for the
    EXPLAIN A/B consistency that P5.4's deterministic tie-break and P5.9's
    plan-shape ratchet depend on.
  - **M0125-0040 (C6 ROLLUP → multi-branch UNION ALL)** — grouping-sets
    `AGG_MIXED`/`AGG_SORTED` is an Aggregate feature outside bundle scope; the
    N-scan structure persists after MHJ deletion. Proceeds independently.
  - **M0125-0013 bookkeeping half** (adjudicate the Q47 8.4× runtime doc
    contradiction) — documentation repair, unrelated to M0127 beyond needing a
    quiet host.
  - **M0125-0003 stage 3** (`estimateBaseRelInfo.baseRows`) — the relsize
    fallback is an S-cold safety net (inactive warm due to `RowCount > 0` early
    return, measured 2026-07-30). Before landing stage 3 on the old DP,
    cross-check against P5.1/P5.6's rows-once-per-RelOptInfo design (M0127's
    04 §2 may redefine where base rel rows are sourced; defer landing until
    after M0127 P5.1, re-evaluate then).
- **Mechanisms M0125 closed but M0127 deletes** (deletion via P6.2/P6.3 inventory;
  supersession stamps via P6.4):
  M0125-0035b's `reselectDegenerateHashKeys` (→ deleted at P2.2,
  Q78-class degeneracy regression test added same commit), M0126-0011's
  `rewriteMultiWayChain`/`multi_hash_join.go` (→ P6.2), M0126-0006/-0007's
  `fusedHashJoinOp`/`tryFuseHashCascade` (→ P6.1), M0126-0004's
  slot-chaining deferral (→ un-deferred at P1.1).

## Stage map (08 §2 is normative)

| stage | content | flag / default | gate to advance |
|---|---|---|---|
| **S0** | E2 (`mergedKeySlot` hoist) + E3 (single-pass single-map build) | none — unconditional (pure wins) | units + spotcheck + pgbench smoke; stage0-style A/B shows no query worse |
| **S1** | E1 (legacy-path seam de-materialisation) | `GOOPG_JOIN_SLOT_CHAIN` default ON, env kill-switch OFF | full regress-port + TPC-H SF1 sweep + SF0.5 checksum gate; Q3/Q10/Q18/Q7 ≤ 1.2× R0 |
| **S2** | E4 (multi-column keys, planner+executor) | plan-affecting → plan-snapshot re-baseline same commit | spotcheck + SF0.5 + Q78-class degeneracy probe; `reselectDegenerateHashKeys` deleted same commit |
| **S3** | 06 hybrid hash spill | `work_mem`-honouring ON; `GOOPG_HASH_SPILL=off` escape | Q21 SF1 completes (cgroup cap); no-spill plans byte-identical results |
| **S4** | 07 §§2–4 (streaming merge, hash outer-fill, Materialize+NL) | per-operator, plan-affecting parts follow S5's flag | regress-port outer-join files; SF0.5 |
| **S5** ✅ **FLIPPED 2026-08-06** | new PG-shaped DP (03, all 3 phases + bushy) + cost binding (04) | `GOOPG_PGSHAPED_DP` — **now ON by default**, surviving as a kill-switch (`=0` only); was OFF while soaking, flipped ON as the acceptance event (09 §3.14); `GOOPG_COST_DRIVEN_JOINORDER` retired. Collapse-limit wiring gets its own sub-flag `GOOPG_PGSHAPED_COLLAPSE`, soaked separately | the full acceptance bar above (collapse OFF → ON, two passes) |
| **S6** | E5 (compiled key/residual eval) | none (behaviour-neutral) | units + parity spot-diffs on expression corpora |
| **S7** | deletion (08 §4) | none — only after S5 default-ON has survived ≥ 1 clean nightly cycle | nightly green; grep-clean inventory |

Rollback story (08 §2): S0/S2/S6 revert by commit, S1/S3 flip their env switch,
S5 flips `GOOPG_PGSHAPED_DP` OFF, restoring the current `tryBushyDP` enumerator
(itself bushy-capable) — the old search is not deleted until S7.

## Required Design Docs

| Task | Content | Design location |
|---|---|---|
| Full implementation plan | P0–P6 Ralph task breakdown (34 tasks; P2–P5 are 1–2 loops each) | `docs/design/0127-pg-shaped-join-search.md` (created by this milestone) |
| Individual tasks | Per-task thin implementation spec | The executing loop creates and indexes `docs/design/0127-<task>-<short-slug>.md` in the same loop per repo rule. Bundle chapters are the design authority; the thin spec stays at "translation of bundle XX §N" |

## Order

```
M0125 remaining items (exprwalk 5–8 / -0047 / -0040 / -0013 etc.) →
P0.1 → P0.2 → P0.3 →            [S0: executor pure wins]
P1.1 → P1.2 → P1.3 →            [S1: the seam + A/B artefact]
P2.1 → P2.2 → P2.3 →            [S2: multi-column keys (planner+executor one-commit pair)]
P3.1 → P3.2 → P3.3 → P3.4 → P3.5 → [S3: hybrid hash spill]
P4.1 → P4.2 → P4.3 → P4.4 →     [S4: other join operators]
P5.1 → P5.2 → P5.3 → P5.3a → P5.4 → P5.5 → P5.6 → P5.7 → P5.8 → P5.9 → [S5: DP, 1–2 loops each]
PS6.1 → PS6.2 →                  [S6: compiled eval]
P6.1 → P6.2 → P6.3 → P6.4 →     [S7: deletion, after clean nightly ≥ 1 cycle]
```

- P0–P4 immediately improve the current default planner's output (executor first —
  the M0125-0002 lesson: "direction unpredictable per query; measure per commit").
- Each P5 task lands dark behind `GOOPG_PGSHAPED_DP` (coexistence rules during
  soak are 08 §3: searched roots are tagged, legacy passes skip tagged subtrees;
  `reconcileNLILayout` asserts no-op on searched trees).
- P5.8's collapse-limit soaks separately behind the `GOOPG_PGSHAPED_COLLAPSE`
  sub-flag.
- Deletion (S7) only after S5 default-ON survives ≥ 1 clean nightly cycle.

## Definition of Done

1. **S0–S4 complete at their gates**: pure wins (S0) → seam artefact (S1,
   `s1-ab.txt`, Q3/Q10/Q18/Q7 ≤ 1.2× R0) → multi-column keys (S2, snapshot
   re-baseline, `reselectDegenerateHashKeys` deleted + Q78 degeneracy regression
   test) → spill (S3, Q21 SF1 completes + forced-spill byte-identical) → other
   operators (S4, regress-port outer-join files green). Each stage's artefact
   committed under `analysis/leftdeep-joins/`.
2. **S5 search implements PG's full three phases**: clause joins / bushy /
   last-ditch (03 §4, `joinrels.c:118 / :141-198 / :200-256` analogues). The
   bushy phase is the PG-verbatim structure of `joinrels.c:141-198` (k-loop,
   clauseless-rel skip, mirror-half `first_rel` rule,
   `have_relevant_joinclause` pair gate).
3. **Methods generated inside the search**: `addPathsToJoinrel` (hash both build
   sides, NLI+Memoize parameterised, merge, NL fallback). Post-DP
   `rewriteJoinsToNLI`/qual-placement passes skip searched trees.
4. **`GOOPG_PGSHAPED_DP` flip or documented no-go**: measure the full acceptance
   bar (collapse OFF → ON order), met → default ON + snapshot recapture + update
   all "ships off by default" text; missed → no-go document (failing clause,
   remaining queries, attribution, successor nomination). **An unmeasured outcome
   is the only failure mode.**
5. **`MultiHashJoin` and fusion are deleted**: at S7 per the 08 §4 inventory
   (MHJ ~34 arms/18 files, fusion 707 lines + hook + env vars), plus old
   subset-bitmask DP + layout/remap family + integer cost + `IsSmallDimensionSide`
   pinning + `chooseInnerJoinAlgo`. `buildBindingsPosMap`/
   `applyJoinTreePosMap` are **held back** until the 03 §10 boundary coordinate
   map is proven in production (explicitly noted as the single most
   regression-prone change in S7).
6. **Supersession stamps**: 0034-0001 / 0038-0001 / cost-model/09 §3 /
   0043 / 0063 / 0125 / 0126 MHJ chapters get `superseded by: leftdeep-joins/`
   headers (never deleted), README index status flips, `.ralph/deferral_ledger.md`
   rows for every deliberately-skipped PG behaviour (GEQO, skew buckets,
   `join_is_legal`-inference-dependent semi/anti-in-DP and join_order_restriction,
   etc.).
7. **PG plan-shape parity gate** (`scripts/pg-plan-shape-diff.sh --strict`) runs
   in report mode + pinned mismatch budget (ratchet) on every plan-affecting
   commit from S5 onward — mismatch count does not increase commit-to-commit.
   **There is no `expected-bushy` category** (goopg implements the bushy phase,
   so a bushy spine PG produces that goopg cannot is a genuine divergence).
8. Milestone index row updated; this file's status is `accepted`.

## Evidence ledger

| artefact | owed by |
|---|---|
| `analysis/leftdeep-joins/<date>-s1-ab.txt` | M0127-P1.3 (S1 exit) |
| `analysis/leftdeep-joins/…-s3-spill.txt` | M0127-P3.5 (S3 exit) |
| `analysis/leftdeep-joins/…-s5-acceptance.txt` | M0127-P5.9 (S5 exit) |
| `plan_snapshots/` re-baselines (S2, S5, flip) | M0127-P2.1, P5.5, P5.9 |
| estimate audit (Q9 final joinrel ≤ 10²×) | M0127-P5.6 (09 §5) |
| pre-deletion grep inventory (re-acquired at S7 time) | M0127-P6.2 |
| parity gate mismatch records (ratchet) | every plan-affecting commit from M0127-P5.9 onward |

## Out of scope

- **GEQO** (genetic search port) — 03 §7.
- **Parallel hash build** (leader-serial shared build stays —
  `docs/design/parallel-query/` owns it).
- **`join_is_legal` constraint inference** — v1 keeps outer/semi/anti joins as
  opaque pinned inputs (03 §4.4 temporary measure). The bushy phase itself
  (03 §4.3) **is included in v1**, however.
- **Extended statistics, bitmap heap scans**.
- **New executor IR** (`create_plan` still translates to existing `Operator` nodes).
- **Grouping-sets `AGG_MIXED`/`AGG_SORTED`** (M0125-0040 proceeds independently).
- Any modification to `./postgres/`.

## PostgreSQL References

- `postgres/src/backend/optimizer/path/joinrels.c` — `join_search_one_level`
  (:73), `make_rels_by_clause_joins` (:118, clauseless cartesian :120-137),
  bushy phase (:141-198), last-ditch (:200-256)
- `postgres/src/backend/optimizer/path/joinpath.c` — `add_paths_to_joinrel` (:124)
- `postgres/src/backend/optimizer/util/pathnode.c` — `add_path` (dominated path immediate discard)
- `postgres/src/backend/optimizer/path/costsize.c` — `initial/final_cost_hashjoin`
  (:4134/:4160)
- `postgres/src/backend/executor/nodeHash.c` — `ExecChooseHashTableSize` (:658),
  `get_hash_memory_limit` (:3622)
- `postgres/src/backend/executor/nodeHashjoin.c` — build-side-only
  materialisation, pipelined outer
- `postgres/src/backend/optimizer/plan/initsplan.c` — `distribute_restrictinfo_to_rels`
- `postgres/src/backend/optimizer/plan/planner.c` — collapse limits,
  `preprocess_grouping_sets` (reference only; grouping-sets itself is out of scope)
