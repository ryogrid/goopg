# Completed milestones — archived 2026-10-15

Fully-`[x]` milestone sections: M0138, M0139, M0143, M0144, M0145.

## M0138 — PG-faithful ANALYZE statistics (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0138-pg-faithful-analyze-statistics.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
**Source:** the owner's answer of 2026-09-14 to `METHODOLOGY3/04-forward-plan.md` §1.2 Question 2
**Prerequisite:** M0137.

**The owner directed that goopg reproduce PG's estimates, errors included,
because otherwise identical plan generation is impossible.** Two consequences:
04 §1.2's proposed `PARITY-BLOCKED-BY-ORACLE-ERROR` rule is **rejected**, and
**R79's verdict (b) — "keep the superior statistics" — is OVERTURNED**; do not
cite it to decline sampling work. Q9 stays in the parity target and M0142
becomes scopeable.

**Frame it correctly: this is not error-injection.** The goal statement already
requires the same plan reached by the **same statistics**, so goopg's sampler is
a divergence from PG and removing it is PG-faithfulness work. **Port
`acquire_sample_rows` and let its output be whatever it is** — never tune a
constant toward a target number, never special-case a query, never add a fudge
factor. A task whose diff contains a constant chosen to make an estimate match
is rejected. If a faithfully ported sampler still disagrees with PG, that is a
finding to record, not a gap to paper over.

**Per-task discipline:** design note when the task is selected, indexed in the
same commit; cite the PG oracle by `file:function`; same-epoch A/B only (this
milestone moves estimates corpus-wide); values gates bind — large plan churn is
expected and acceptable, wrong rows are not. **Time every query whose plan
changed**: moving an estimate toward PG is the class that took TPC-DS Q72 from
4s to a 320s TIMEOUT (B6 — goopg has no Memoize on the NL probe path PG plans
that shape with), and B8's `indexProbeCostMultiplier = 2.0` and B10's `corr = 0`
fallback both shift index-probe pricing corpus-wide. See `AGENT.md` §"The
toward-oracle hazard"; a slower matching plan lands with a ledger row, an
unmeasured one does not.

- [x] **M0138-0001 — divergence census (recon, no production change)** — measure and
  record exactly where goopg's ANALYZE differs from PG's `acquire_sample_rows`
  (`postgres/src/backend/commands/analyze.c:1199`). Already upstream-faithful:
  `upstreamDefaultStatsTarget = 100`, the `targrows = target * 300` multiplier
  (`internal/executor/operators_analyze.go:581-589`), the Algorithm-R reservoir, the
  physical-order re-sort, and the Duj1 estimator. The measured divergence is the
  **block representation** — goopg scans every block, PG samples random blocks. Produce
  a per-column divergence table over both corpora; no code change in this task.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0001-analyze-divergence-census.md`.
  Live `pg_stats` capture over all four bench clusters (TPC-H SF1 + TPC-DS SF0.25,
  goopg vs PG) reconfirmed the block-representation gap and surfaced three
  previously-unnamed divergences — `RowCount`/`reltuples` (goopg exact vs PG
  extrapolated from `bs.m` sampled blocks), correlation tie-break (PG's
  `tupnoLink`-deterministic tie order vs goopg's unspecified `sort.Slice` order,
  live-correlated with a `[0.09,0.16]` correlation band on 36/118 TPC-DS columns
  vs PG's 7/119), and `pg_stats.avg_width` (PG falls back to fixed `typlen` for
  non-varlena types, goopg has no such fallback — `avg_width=0` on 32/61 TPC-H and
  70/121 TPC-DS columns). Three ledger rows filed (`m0138-0001-*`), each citing a
  resume point in M0138-0002 or M0138-0004. No production code touched.
- [x] **M0138-0002 — port PG's block sampler and two-stage row selection** —
  `BlockSampler_Init`/`BlockSampler_Next`
  (`postgres/src/backend/utils/misc/sampling.c:39,64`) plus
  `reservoir_init_selection_state` / `reservoir_get_next_S` / `sampler_random_fract`
  as `analyze.c:1228-1290` drives them, including PG's `pg_prng` sequence, so the
  sampled TID set matches PG's for a fixed seed on a shared relation.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0002-block-sampler-two-stage-row-selection.md`.
  New `internal/executor/analyze_block_sampler.go` ports `pgPRNGState`
  (xoroshiro128**), `blockSampler` (Algorithm S) and `reservoirState`
  (Algorithm Z) line-for-line from `sampling.c`; `analyzeRelationWith`'s block
  loop now visits only sampled blocks and its row selection uses PG's
  skip-count reservoir instead of Algorithm R. Resolved the M0138-0001
  `reltuples` scope question (ledger row flipped to `resolved`): confirmed
  VACUUM has its own separate reltuples path, so `RowCount` now
  unconditionally extrapolates via `floor((liverows/bs.m)*totalblocks+0.5)`,
  degrading to exact when every block is sampled -- every pre-existing
  small-table ANALYZE unit test stayed green under that degenerate path.
  `AvgWidth`'s denominator moved from `RowCount` to sampled-live-row-count to
  match. New tests in `analyze_block_sampler_test.go` (block sampler +
  reservoir primitives, plus a 60k-row integration case confirming actual
  block skipping). Dead-row tracking deferred, no consumer yet (ledger row
  `m0138-0002`). Gates: `go build ./...` clean;
  `go test ./internal/executor/...` full package green;
  `RALPH_PRECOMMIT_SCOPE=units` green except the pre-existing, already-filed,
  confirmed-unrelated `internal/parser` `TestLockingClauseParity` AST-drift
  failure (verified via `git stash` of this task's files reproducing the same
  failure without them). Corpus-wide TPC-H/TPC-DS re-measurement intentionally
  deferred to M0138-0005 (declared-epoch requirement).
- [x] **M0138-0003 — verify `stadistinct` parity end to end; do NOT re-implement what
  exists** — goopg stores upstream's one signed `stadistinct` as **two** fields,
  `NDistinct` (absolute) and `NDistinctFrac`
  (`internal/catalog/catalog.go:1880-1903`), but `ColumnStats.StaDistinct()`
  (`catalog.go:1942`) **already** reconstructs PG's signed convention including
  upstream's 10% absolute-to-fraction switch (`analyze.c:2650-2658`), and it is already
  consumed at the `pg_statistic` heap row, the `pg_stats` view and
  `joinselectivity.go:235`. So the convention is not the gap. The task is to confirm
  the switch fires where PG's does **on the sample M0138-0002 now produces**, and that
  every consumer reads the reconstructed value rather than a bare `NDistinct` (the
  take2 P2-09 class of bug, where a scaling column read as absolute-zero). R78's
  witness: goopg `l_orderkey` `-0.1956` vs PG's absolute `347537` — re-measure it and
  report. Land a change only where a real divergence is found.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0003-stadistinct-parity-verification.md`.
  Consumer audit confirmed all three call sites already go through
  `StaDistinct()` (no bare-`NDistinct` reads). Re-measured R78's witness on a
  HEAD build (carrying M0138-0002) against the live TPC-H bench pair
  (:65433/:65432): goopg's `l_orderkey` ndistinct moved from the pre-M0138-0002
  `-0.1956` frac (≈1.17M, 3.4x off) to `327804` absolute, landing inside PG's
  own unpinned 3-run noise band (`336410`-`366886`). Five more spot-checked
  columns matched PG within the same noise, including both engines reporting
  `-1` for a unique PK (confirms the 10% switch fires identically). No
  production change: the gap was M0138-0002's block-sampler fix, not a
  `stadistinct`-convention bug, so no diff earns landing per the milestone's
  anti-tuning rule.
- [x] **M0138-0004 — MCV, histogram and correlation from the shared sample** — apply
  PG's `compute_scalar_stats` / `compute_distinct_stats` selection rule to the sample
  M0138-0002 produces, so every slot is computed from the same rows PG would have seen.
  DONE 2026-09-15: `docs/design/0100-0149/m0138-0004-mcv-histogram-correlation-selection-rule.md`.
  Built on a prior loop's uncommitted WIP found already in flight (AvgWidth typlen
  fallback, correlation tie-break via `sort.SliceStable`, MCV bucket tie-break by
  ascending value on a count tie — resolves the two open M0138-0001 ledger rows for
  correlation tie-break and avg_width). Found and fixed three more divergences by
  reading `compute_scalar_stats` (`analyze.c:2402-2919`) line-by-line: (1) the
  "complete MCV list" shortcut wrongly fired whenever `len(buckets) <= statsTarget`,
  regardless of whether any distinct value was a singleton — PG's `track_cnt ==
  ndistinct` can only hold when EVERY distinct value repeated, since `track[]` never
  gains a singleton entry at all; fixed to `nmultiple == len(buckets) &&
  len(buckets) <= statsTarget && stats.StaDistinct() > 0`. (2) `analyzeMCVList`'s
  candidate list was capped by the total distinct count instead of `nmultiple`
  (PG's `track_cnt`), padding its significance walk with singleton noise; fixed to
  cap by `nmultiple`. (3) the histogram deduped adjacent equal boundary values under
  a comment claiming PG does too — checked against `analyze.c:2806-2836` and that's
  false, PG stores raw evenly-spaced values with no distinctness check, and the
  selectivity consumer already implements PG's own `binfrac = 0.5` equal-bounds
  fallback (`selfuncs.c:1234-1237`); fixed by removing the dedup. Three new
  regression tests pin all three fixes with hand-derived expected values (one
  probed against the pre-fix code via a throwaway copy to confirm the behavior
  actually changed before committing to it). Non-orderable-kind `compute_distinct_stats`
  (bytea/interval) deliberately left unported — no TPC-H/TPC-DS column exercises it
  (ledger row `m0138-0004`). Found two orphaned bench servers (`:65433`/`:65437`)
  left running by the interrupted prior loop's own measurement work; reaped via
  `goopg stop -D` (not `pkill`) so `scripts/tpch-spotcheck.sh` could get a
  quiescent snapshot. Gates: `go build ./...`, `go test
  ./internal/executor/... ./internal/optimizer/...` clean;
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green for both
  touched packages (pre-existing unrelated `internal/parser` 60-test AST-drift and
  `bak/` build failure — see the "Blast radius correction" note above);
  `scripts/tpch-spotcheck.sh` `RESULT=PASS` (Q12=2/Q13=34, canonical anchors).
  Corpus-wide plan/timing re-measurement is M0138-0005's job, not repeated here.
- [x] **M0138-0005 — corpus re-measure at a declared epoch** — commit a per-column
  statistics diff vs PG 18.3 over both corpora, then the plan and category movement it
  causes. Every remaining disagreement is explained or filed as a ledger row. Expect
  large plan churn; the values gates are the bar.
  - **DONE 2026-09-15.** Measurement-only (no production change). Design doc
    `docs/design/0100-0149/m0138-0005-corpus-remeasure-declared-epoch.md`.
  - Plan/category movement vs the milestone-filing baseline
    (`METHODOLOGY3/README.md` "post-R128"): **TPC-H byte-identical** — same
    match set (Q1/Q6/Q10/Q11/Q14/Q15a), all nine category counts unchanged.
    **TPC-DS verdict tuple unchanged** (`2/69/0/25/3/0`, reproduces
    `TODO.md:4845-4849`'s R120 baseline exactly) but `join-order` and
    `qual-placement` each +1 with no verdict flip — one unidentified query's
    plan structure moved sideways; ledger row filed, not bisected (99-query
    corpus, recon-scoped budget).
  - Per-column `pg_stats` diff over M0138-0001's column set, re-run post-fix:
    `n_distinct` sign/order-of-magnitude agreement now near-total (TPC-H
    60/61, TPC-DS 120/120 sign match) — the corpus-wide confirmation the
    earlier spot-checks predicted.
  - **Important correction**: the correlation-banding symptom M0138-0001
    found (`[0.09,0.16]` band on high-duplicate-density FK columns) **still
    reads 36/120 vs PG's 7/120 after M0138-0004's tie-break fix landed** —
    unchanged from the pre-fix 36/118 vs 7/119 reading. Re-verified the
    tie-break mechanism is correctly ported (source-level re-check: both
    engines sort the reservoir into physical TID order before computing
    correlation, and PG's own `tupno` is the post-sort array index, exactly
    what goopg's `pos` already is) — the fix itself is genuine and stays
    landed, but it does not explain the corpus symptom it was credited with
    resolving. **Reopened** the `.ralph/deferral_ledger.md` `m0138-0001`
    correlation row (flipped from `resolved` back to `-`) with a corrected
    resume point: a synthetic controlled-layout test to distinguish a
    remaining goopg bug from a genuine TPC-DS-loader physical-layout
    artifact between the two storage engines.
  - New finding: `avg_width=0` still reproduces for goopg's **fast-path**
    `numeric` (int64 mantissa inline, no heap arena) — M0138-0004's typlen
    fallback correctly only covers fixed-width types, but `numeric`'s
    measured-payload branch (`datumVariablePayloadWidth`'s `KindNumeric`
    case) only measures the slow (big-numeric) path. Reproduces on 28/61
    TPC-H columns (every numeric-typed key/price/cost column — HammerDB
    declares TPC-H keys `numeric`) and 17/120 TPC-DS columns. Ledger row
    filed; needs PG's actual numeric-varlena size formula, not a literal
    constant (anti-tuning rule).
  - Gates: `go build ./...` unaffected (no source changed this loop). No
    values-gate re-run needed — no production code touched.
- [x] **M0138-0006 — re-measure Q9's estimate against R130's table and reconcile the
  ANALYZE seed (DONE 2026-09-15)** — Q9's actual output is 175 rows; goopg estimated 97
  and PG estimates 60,125. **Moving toward PG's 60,125 is the expected result** and is
  the empirical test of the Question 2 decision; if it does not move, that is the
  finding and M0142's premise must be re-examined before slices are scoped. In the same
  task, reconcile `GOOPG_ANALYZE_SEED`'s determinism with PG's own sequence — retire it,
  or document why both must coexist.
  - Re-measured at HEAD (`estimate-audit -plan-only -queries 9`, pinned
    `GOOPG_ANALYZE_SEED=20260905`, live `:65433`/`:65432` lanes): goopg's estimate moved
    **97 → 146** — closer to the actual 175, **not toward PG's 60,125**. The
    pre-registered prediction did NOT hold; M0142's premise needs re-examination, now
    with a narrowed starting hypothesis instead of an open-ended one (see below).
  - Leaf-level check: the `partsupp ⋈ part` join (filtered `p_name LIKE '%green%'`)
    estimates now agree within 1.2x between engines (12,121/48,484 goopg vs
    10,101/40,404 PG) — the underlying per-column statistics converged (consistent with
    M0138-0005's corpus-wide `n_distinct` finding). The 344x top-level gap is therefore
    NOT a residual statistics-precision gap of the kind M0138 targets.
  - Hypothesis for M0142-0001/-0002 (not adjudicated here — recon-scoped, and forcing
    shapes is against the milestone's goal): the gap is **join-shape-driven**. goopg
    drives Q9 with FK-indexed Nested Loops (each probe correctly `rows=1`); PG runs an
    all-Hash-Join chain where `eqjoinsel`/`calc_joinrel_size_estimate`
    (`postgres/src/backend/utils/adt/selfuncs.c:2280`,
    `postgres/src/backend/optimizer/path/costsize.c:5501`) compound independent
    per-join selectivities down a correlated FK chain with no extended statistics.
    Reproducing PG's estimate likely requires goopg to choose PG's Hash-Join shape
    first — a join-order/method question, not an ANALYZE-precision one.
  - `GOOPG_ANALYZE_SEED` reconciliation: confirmed against PG's actual source
    (`analyze.c:1227`'s `pg_prng_uint32(&pg_global_prng_state)`, a process-global PRNG
    with no reproducibility knob) — PG has nothing to retire the variable in favour of.
    goopg's unset/zero path already reproduces PG's "fresh per-backend draw" property
    exactly; the pinned path is a harness-only determinism knob with no PG counterpart.
    **Verdict: keep, no code change** — the existing code comment was already correct.
  - Design doc:
    `docs/design/0100-0149/m0138-0006-q9-estimate-remeasure-and-seed-reconcile.md`.
    Ledger row filed narrowing M0142-0001/-0002's starting hypothesis (see
    `.ralph/deferral_ledger.md`, task-id `m0138-0006`).
  - Gates: `go build ./...` unaffected (no source changed). No values-gate re-run
    needed — no production code touched this loop.
  - **M0138 is now fully landed and measured** (all six tasks [x]) — M0142's
    prerequisite gate is satisfied; M0142-0001 becomes selectable.
- [x] **M0138-0007 — give `numeric` columns a real `avg_width`** — M0138-0005
  measured that the `numeric` fast path leaves `avg_width=0`, affecting **28 of
  61 TPC-H columns and 17 of 120 TPC-DS columns**, and filed a ledger row with no
  owner. HammerDB's TPC-H declares every primary and foreign key `NUMERIC`, so
  this hits the join columns the whole programme turns on. Resume point in the
  row: `operators_analyze.go:1159-1174` plus PG's `numeric_size` arithmetic. A
  width of zero is not a PG-faithful statistic — this is squarely M0138's remit.
  - **DONE 2026-09-15.** The ledger row's own guessed formula (`NUMERIC_HDRSZ`
    plus digits) turned out to be the wrong PG function — that is
    `numeric_maximum_size`'s typmod-derived worst case (already used
    elsewhere, `relsize.go`'s `numericHeaderSize`, for the planner's row-width
    *upper bound*), not what `compute_scalar_stats` measures. Verified against
    live PG 18.3 instead: `VARSIZE_ANY` of the raw on-heap Datum —
    `pg_column_size(l_quantity)=5` for `18`, `pg_stats.avg_width=8` for
    `l_extendedprice`, both reproduced exactly by (1-or-4-byte short/long
    varlena header) + (PG NumericData's own 2-or-4-byte internal header) +
    (2 bytes per stripped base-10000 digit). New `numericFastPathOnDiskWidth`
    reuses `internal/nodes.NumericBodyFromText` (the same `numeric_in` port
    `codec.go` already uses for the heap's on-disk numeric form) instead of
    re-deriving digit-grouping. Sibling-path check: `spill.go`'s
    `estimatedRowBytes` correctly stays at `+0` for this arm (in-memory
    footprint, a different, already-documented-as-divergent quantity from the
    on-disk stats ruler) — its cross-check test's coincidental equality for
    this one case was removed with an explanatory comment, not silently left
    to bit-rot. Design doc:
    `docs/design/0100-0149/m0138-0007-numeric-avg-width.md`. Ledger row
    `m0138-0005` (numeric avg_width) flipped `resolved`. Gates: `go build
    ./...`; `go test ./internal/executor/... ./internal/optimizer/...` clean
    (new `TestNumericFastPathOnDiskWidth`, updated
    `TestDatumVariablePayloadWidth` + `TestEstimatedRowBytesCountsEnumAndBigNumeric`);
    `RALPH_PRECOMMIT_SCOPE=units` green except the pre-existing, already-filed
    `internal/parser` `GroupedJoinUnaliased` AST-drift (60 test functions,
    confirmed unrelated); `scripts/tpch-spotcheck.sh` `RESULT=PASS`
    (Q12=2/Q13=34, canonical anchors — no plan/category shift observed at
    this scale).
- [x] **M0138-0008 — bisect the category shift M0138 caused** — **DONE
  2026-09-15.** Built goopg at the pre-M0138-0002 commit (`0e97c94b3`) and at
  M0138-0004 (`44085c324`), loaded a private SF0.25 dataset with each (shared
  `:65437` cluster untouched), diffed against the live PG reference. Four
  unpinned trials found `join-order`/`qual-placement`/three other categories
  move run-to-run at a FIXED commit from ANALYZE reservoir-seed variance alone
  (`Q26`, `Q45`, `Q48`, `Q51`, `Q97` each flip a category tag) — the same
  noise mechanism M0138-0009 found for `correlation`, now shown to also flip
  plan-parity verdict categories, this milestone group's own success metric.
  Pinning the existing (but here-unused) `GOOPG_ANALYZE_SEED` knob made
  captures reproducible; with it pinned, the shift reproduces in M0138-0005's
  original direction and narrows to exactly `Q21` (join-order, a
  cardinality-estimate-driven join-spine change) and `Q48` (qual-placement, a
  `store` access-path change). Design doc:
  `docs/design/0100-0149/m0138-0008-category-shift-bisect.md`. Follow-up
  **M0137-0020** (below, filed in this loop) owns pinning the seed in the
  capture harness itself. Ledger row: `m0138-0008-category-shift-bisect`.
- [x] **M0138-0009 — re-open the correlation banding finding** — **RESOLVED
  2026-09-15, NOT A DEFECT.** Ran the synthetic-table comparison the row
  prescribed in two parts. (1) `internal/testport/m0138_correlation_synthetic_test.go`
  loaded a hand-constructed periodic column identically on goopg and real PG
  18.3 (server-side `COPY FROM file`, N below `targrows` so ANALYZE fully
  scans, no reservoir randomness): both engines produced BYTE-IDENTICAL
  correlation (`0.095866`), refuting a computation bug and a simple-load
  physical-order divergence. (2) `internal/executor/operators_analyze_test.go:TestAnalyzeReservoirSeedCausesCorrelationVarianceOnPeriodicFK`
  then varied only goopg's reservoir-sampler RNG seed at a realistic
  subsampling ratio and reproduced a correlation spread (`[0.05,0.18]`) wider
  than the census's own `[0.09,0.16]` banding from seed variance alone.
  Conclusion: ordinary reservoir-sampling variance on a periodic/
  low-duplicate-density column, equally present in PG's own independently
  -seeded sampler — nothing to port, nothing to fix. Ledger row `m0138-0001`
  (correlation) flipped `resolved`; no follow-up task filed. Design doc:
  `docs/design/0100-0149/m0138-0009-correlation-banding-synthetic-isolation.md`.


## M0139 — Executor-side narrowing / projection pushdown (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0139-executor-side-narrowing-projection-pushdown.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
**Source:** the owner's answer of 2026-09-14 to `METHODOLOGY3/04-forward-plan.md` §1.1 Question 1 — **(a) build it**
**Prerequisite:** M0137 (M0137-0010's qual-placement census gates every slice here).

**Scope is projection pushdown only.** The packed retention format
(`PackedTuple`/`PackedSlot`) stays declined and needs an owner decision informed
by S3's measurement; a `Datum` re-layout below 48 B is declined and nobody
proposes it. `minimize_datum/README.md` states `Datum` **stays exactly 48 bytes**
— so "the `DatumBytes` half" is the wrong label for the retention format and must
not be used (`05-work-estimate.md` §1 calls confusing the two "the single most
likely way to misprice this work").

**The structural blocker is located:** inside a join tree there is no `*Project`
above the scan at all — the join reads the scan directly, so there is nowhere to
hang a projection. The machinery for *applying* a narrowing already exists and
ships default-ON, but in **three separate files**: `GOOPG_NARROW_BUILD` in
`internal/optimizer/narrowoutput.go` (hash build side and merge input),
`GOOPG_NARROW_UPPER` in `upper_narrow_apply.go:90`, `GOOPG_NARROW_UPPER_SORT` in
`upper_narrow_chain.go:124`. **`attr_needed` is NOT the blocker** — two earlier
diagnoses said it was and both were wrong.

**Argue this campaign on Q4 and the executor gap, not on Q9.**
`internal/optimizer/entrywidth.go:38-48` carries a measured comment headed "WHAT
THIS DOES NOT BUY": Q9's `nbatch` is non-monotone in entry width (4 at 112..194,
2 only at 96..111, back to 4 below 96), commit `2e15b8ca3` records that a packed
retention format would make Q9's batching **worse**, and R129 measured the lever
that comment names as parity-inert.

- [x] **M0139-S1 — a hook point inside the join tree** (gated on M0137-0010) — pre-registered prediction:
  **no parity movement; the pass fires N > 0 times**. A slice that predicts a match
  flip is mis-scoped. Do not re-derive that `attr_needed` is the blocker.
  - DONE 2026-09-15: recon narrowed the gap — `joinInputsFor` already narrows
    a hash join's inner/build side and both merge-join sides; only a hash
    join's outer/probe side and both nested-loop sides (plain + NLI) were
    never reached. `narrowPlanOutput` itself already declines to wrap a
    no-op cut, so the hook could not be "an identity Project" (it would be
    silently absorbed) — it had to be a new call site instead. Landed
    `internal/optimizer/joinleghook.go`'s `narrowJoinLeg`, gated by new
    default-ON flag `GOOPG_NARROW_LEG_HOOK`, called from `joinInputsFor` on
    both legs of every join kind. **Unconditionally a decline for S1**: it
    counts every currently-unhooked eligible leg but returns the pair
    byte-identical to its input in every case — "no parity movement" is
    guaranteed by construction, not merely predicted, and proven directly
    by `TestNarrowJoinLegDeclinesButCounts` plus a live two-table-join test
    (`TestNarrowJoinLegFiresOnLiveJoinSearch`, fires 1 time, plan shape
    identical hook-on vs off via `unaDump`). M0139-S2 reuses
    `narrowBuildInput`'s existing keep-set derivation at this hook rather
    than duplicating it. Gates: `go build`/`go test` clean for
    `internal/optimizer`/`internal/executor`; `RALPH_PRECOMMIT_SCOPE=units`
    green except the pre-existing, already-filed `internal/parser`
    AST-drift + `bak/` build failure; `scripts/tpcds-sf025-regression.sh
    sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0.
    `scripts/tpch-spotcheck.sh` **could not run** — shared `:65433` was up
    the whole task (peer-owned, not to be stopped) and the private-clone
    snapshot step requires it fully down; retried 3x over ~15 min, still
    busy. Not a values-safety gap given the mechanical no-op proof; re-run
    once the shared server frees up. Design doc:
    `docs/design/0100-0149/m0139-s1-join-leg-hook.md`. No ledger row (pure
    goopg-internal plumbing).
- [x] **M0139-S2 — narrow scan output at the new hook** (gated on M0137-0010) — reuse the existing
  narrowing rather than duplicating it. It lives in three files, not one:
  `narrowoutput.go` (`GOOPG_NARROW_BUILD`, hash build + merge input),
  `upper_narrow_apply.go:90` (`GOOPG_NARROW_UPPER`) and `upper_narrow_chain.go:124`
  (`GOOPG_NARROW_UPPER_SORT`).
  - DONE 2026-09-15: `narrowJoinLeg` (joinleghook.go) now reuses
    `narrowBuildInput`'s exact chain (`joinKeepSet`→`buildKeepSet`→
    `neededKeepSet`→`narrowPlanOutput`) instead of always declining;
    signature grew `*Path` + `nliInner bool`. Safety for NL legs: the
    tightest tier (`joinKeepSet`) correctly reports unknown under an NL
    (deriveJoinKeepsAt never stamps JoinKeep there) and falls through to
    the two join-kind-agnostic, over-inclusive fallback tiers. ONE case is
    structurally forbidden regardless of keep-set: an NLI's INNER slot is
    typed concretely (`*IndexScan`/`*BitmapHeapScan`), so wrapping it in a
    `*Project` is a plan-time panic, not a narrower plan — caught LIVE by
    two pre-existing tests
    (`TestQ2DecorrelatedGroupKeyResolvesInAggregateInput`,
    `TestDerivedTableUnderIndexNLReturnsRows`) before the `nliInner`
    exclusion (covers both `"PathNestLoop(NLI)"` and
    `"PathNestLoop(NLI-bitmap)"`) was added. Landing real narrowing
    legitimately changed plan shape wherever a previously-unwrapped leg
    had something to drop, so 5 pre-existing regression tests needed
    their hard-coded build-count oracle re-derived (via throwaway probe
    tests, deleted before commit), not loosened:
    `TestSlice3LiveQ9ShapeDerivation` (5→7 builds),
    `TestSlice3LateralDeclinesDerivation` + executor twin
    `TestOwnedBuildPoisonCorrAboveDecline` (1→2),
    `TestSlice3DerivedTableAliasMapping` (1→2),
    `TestSlice3SelfJoinInDerivedTable` (one 6-col combined build splits
    into two 3-col builds), `TestPlanJoinPicksHashAlgo` (a NULL-pad
    restoration Project can now sit below the top SELECT Project; fixed
    by reusing `findFirstJoin` instead of a single type assertion).
    Gates: `go build ./...`; `go test ./internal/optimizer/...
    ./internal/executor/... ./internal/testutil/tpch/...
    ./internal/postmaster/...` clean; `RALPH_PRECOMMIT_SCOPE=units`
    green except the pre-existing, already-filed `internal/parser`
    AST-drift; **qual-placement census on the full TPC-DS SF0.25 corpus,
    99/99 queries, mismatch=0** (private clone `tmp/goopg-m0139s2-bin`);
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3, non-blocking status-delta
    verdict-changes=none total-delta=-1.7% (no timing regression).
    `scripts/tpch-spotcheck.sh` could not run — shared `:65433` up the
    whole task (peer-owned); not a values-safety gap given the
    corpus-agnostic mechanism plus the clean TPC-DS gates; re-run
    opportunistically. Design doc:
    `docs/design/0100-0149/m0139-s2-narrow-join-leg-output.md`. No
    ledger row (goopg-internal executor plumbing).
- [x] **M0139-S3 — measure the residue against K67's floor** (gated on M0137-0010) — even narrowed to one
  column goopg is 72 B/row -> 103 MB and still spills at `work_mem=64MB` where PG is
  22 B/row -> 31 MB. Report the post-pushdown figure as a **number**, not an argument.
  - DONE 2026-09-15 (recon, no production diff): K67's "72 B/row -> 103 MB,
    narrowed to one column" was analytical (`EntryBytes(1,0)`), never
    measured, and predates S1/S2. A throwaway probe
    (`internal/testutil/tpch/zz_probe_m0139s3_test.go`, deleted before
    commit) built a private disposable cluster from HEAD (never touching
    the shared, peer-owned `:65433` server — whose binary is stale and
    shows zero narrowing on Q12) with `internal/testutil/cluster` +
    `scaleLoader` (20,000 real-DDL orders/lineitem rows), then ran
    `EXPLAIN (VERBOSE)`, `EXPLAIN (ANALYZE, VERBOSE)`, and a
    `pg_stats.avg_width` read for Q12. Confirmed the hook fires (Hash
    Join `Output:` narrows 16+9 columns down to 7), but the real
    orders-side retained set is **`{o_orderkey, o_orderpriority}` — 2
    columns, not K67's assumed 1** (the join key must stay for
    probe-time verification even though nothing above the join
    references it), with real `avg_width(o_orderpriority)=8.3701` (not
    K67's assumed 0). Formula `EntryBytes(2, 8.3701) ~= 128.37 B/row`,
    cross-checked against the executor's own measured `Buckets: 32768
    Batches: 1 Memory Usage: 4044kB` (subtracting the bucket-table term
    reproduces 128.4 B/row). Extrapolated to SF=1's 1.5M orders the same
    way K67 did (entries only): **~193 MB vs K67's 103 MB and PG's
    unchanged 22 B/row -> 31 MB** — S1/S2's real narrowing made the
    measured residue *larger* than the campaign's own anchor, not
    smaller. Still spills at PG 18's default `work_mem=64MB x
    hash_mem_multiplier=2.0` = 128 MB budget vs 192.6 MB of entries.
    Feeds M0139-0006's owner decision; does not itself decide
    `minimize_datum` (still NOT APPROVED TO START). Design doc:
    `docs/design/0100-0149/m0139-s3-k67-residue-measurement.md`. No
    ledger row (no PG-incompatibility surfaced).
- [x] **M0139-0004 — re-measure the "duplicate hash build map" premise, then act on
  what you find** — `minimize_datum/05-work-estimate.md` §1.5 (quoting `02` §3) prices a
  "delete the duplicate build map" win at "one commit", claiming `lazyHash` **and**
  `lazyIntHash` are both maintained for ~2x peak build memory.
  - DONE 2026-09-15 (recon, no production diff): confirmed the premise **refuted**
    at HEAD. `buildLazyHashTable` decides the lane once, before the first row,
    from the plan's static key types (`operators_join_agg.go:651`);
    `presizeLazyHash` allocates exactly one of the two maps (pinned by
    pre-existing `join_presize_test.go` tests); every insert commits to one
    lane per call; the only place both maps are ever non-nil is
    `demoteIntHash`'s own transient copy loop — a rare fallback for a static
    int64-key promise turning out false mid-build, not a standing property of
    the routine path (pinned by `TestPresizedIntTableStillDemotes` and
    `dense_build_cut2_test.go`). The parallel/cooperative build path reuses
    the same presize/insert functions rather than re-deriving the lane logic.
    Corrected `minimize_datum/05-work-estimate.md` §1.5's table row and the
    originating claim in `02-goopg-current-representation.md` §3 (both now
    marked refuted, citing the design doc). Does not decide or advance
    `minimize_datum` (still NOT APPROVED TO START) — removes one stale input
    from a document feeding a future owner decision. Design doc:
    `docs/design/0100-0149/m0139-0004-duplicate-hash-map-refutation.md`. No
    `.ralph/deferral_ledger.md` row (not a PG-incompatibility, out of that
    ledger's scope; refutation recorded in the design docs directly, per the
    M0139-S3 precedent).
- [x] **M0139-0005 — re-measure Q4's grouping election** — R81 located the divergence
  one rel above the grouping contest, in `electOrderedGrouping`
  (`upperorderedgrouping.go:148`), on a startup ratio against `stdFuzzFactor = 1.01`:
  goopg 1.0086 (inside fuzz, tie-break picks hashed) vs PG 1.0118 (outside, sorted
  wins). Report whether the narrowed widths move that ratio across the band.
  - DONE 2026-09-15 (recon, no production diff): a throwaway probe
    (private HEAD cluster, 20,000 orders, deleted before commit) plus
    temporary debug instrumentation in `electOrderedGrouping` and
    `narrowJoinLeg` (both fully reverted — `git diff --stat` empty at HEAD)
    found **zero narrowing anywhere in Q4's subtree**: `narrowJoinLeg` is
    never called at all for this query. Root cause: Q4's `EXISTS` is
    decorrelated by `unnestExistsExpr` (`unnest.go:4078`), which builds the
    physical `Join{SEMI, Hash}` node directly on the raw parse tree, before
    any search joinrel exists — the same fork METHODOLOGY3 F11/K63
    (R73-R77) already named for the costing symptom ("no SEMI path is ever
    filed... before any search joinrel exists, so no sjinfo -> no joinrel
    -> no path -> no price") — so it never reaches
    `createHashJoinPlan`/`joinInputsFor`, the one call site
    `narrowJoinLeg` is wired into. **Verdict: the ratio has not moved and
    cannot move under the current mechanism** — Q4's grouping input width
    is byte-identical to before M0139-S1/S2, independent of scale, so a
    bigger-scale re-measurement would not be informative until the bypass
    itself is fixed (a materially larger task, out of this recon's scope).
    Secondary finding: at 20,000-order scale the two ORDERED-rel
    candidates aren't even fuzzy-tied (one dominates outright in
    `addPath`) — a separate scale-sensitivity note, not pursued further.
    Feeds M0139-0006's owner packet alongside S3's residue finding — both
    point at "the join-leg hook doesn't reach every join the search can
    produce," from opposite ends. Design doc:
    `docs/design/0100-0149/m0139-0005-q4-grouping-ratio-remeasurement.md`.
    No ledger row: the narrowing-bypass finding is a corollary of the
    already-tracked F11/K63/`m0137-0011` lineage, not a newly discovered
    PG-incompatibility on its own.
- [x] **M0139-0006 — put the packed-retention decision to the owner** (DONE
  2026-09-15) — carried S3's measured residue, `entrywidth.go`'s
  non-monotonicity finding, and the two blockers the `minimize_datum` review
  itself raised into one packet. **Not implemented** — `minimize_datum`
  remains NOT APPROVED TO START; this task only poses the owner question.
  - Packet contents: (1) S3's Q12 residue — 128.4 B/row measured (≈193 MB at
    SF=1) vs K67's analytical 72 B/row (103 MB) floor, ~5.8× PG's 22 B/row
    (31 MB), worse than budgeted, not better. (2) `entrywidth.go`'s
    non-monotonicity (`minimize_datum/TODO_ALL.md` "D-05 prereq #1"): entry
    width 194→120 B/row left `NBatch` unchanged (4→4); 2-batch threshold is
    ≤111.8 B/row; a further cut to 63 B/row lands back on 4 — batch count is
    governed by `MapSlotBytes`, not monotonically by entry size, so a packing
    win is not guaranteed to reduce spilling without re-deriving the batching
    geometry. (3) The review's own B2/B3 (packing alone was estimated to
    close only ~5× of a ~48× gap; narrowing — M0139's own mechanism — owns
    the rest) and B5 (take3 13 §8.2 sequencing: EX1-before-geometry, now
    concretely satisfied by M0139-S1/S2/S3 — the one thing that changed since
    the review, but it clears only the sequencing gate, not effectiveness).
  - Owner question posed explicitly, not answered: authorize `minimize_datum`
    now that sequencing is clear, given packing was independently estimated
    to close only a fraction of the residue, or hold it out-of-scope for a
    milestone group whose success metric is plan-structure parity, not
    memory footprint.
  - Design doc:
    `docs/design/0100-0149/m0139-0006-packed-retention-owner-decision.md`.
  - No ledger row: no new PG-incompatibility surfaced, this is a synthesis
    of existing evidence, not a new discovery.
  - **All six M0139 tasks (S1, S2, S3, -0004, -0005, -0006) are now DONE.**
    M0139 has no further open items; the next M0139-family work (if any)
    would come from a future owner decision on `minimize_datum`, which is
    out of scope until that decision is made.
- [x] **M0139-0007 — absorb the irreducible width difference into the cost model
  (owner decision B2)** — **TOP PRIORITY with M0141-S2a-fix.** M0139-S1/S2 landed
  real narrowing and moved **zero** plans, because `applyUpperNarrowing`
  (`planner.go:189`) runs after cost and strategy are decided in
  `planStmtWithSettings` (defined `planner.go:210`, called `:141`); measured proof is that TPC-H costs
  are byte-identical pre/post M0139 while only `width=` shrank. Packed retention
  is **NO-GO** (B1), so the route is B2's absorption principle: give the cost
  model the **PG-equivalent** width — what PG's own tuple representation would
  be for the same logical row — while the executor keeps allocating goopg's real
  `48*ncols + 24 + avgVar`. **This is not tuning**: it feeds PG's formula PG's
  input, rather than bending a goopg number until the output matches. Start with
  a scoping recon (no production diff) that names every site where a goopg-native
  quantity currently reaches a PG-derived cost formula — `hashsize.EntryBytes`
  into the hash-join spill decision is the witness, `MapSlotBytes` and the sort
  footprint are the other candidates — then slice. Each absorption site needs a
  design-doc justification and a test pinning the two currencies apart. Expect a
  slower executor and unchanged values; a matching plan that runs slower is
  **not** a regression.
  - **Recon DONE 2026-09-15, no production diff** (design doc
    `docs/design/0100-0149/m0139-0007-absorption-scoping-recon.md`, full
    inventory table). Findings:
    - The task's own witness site is **half-absorbed already**:
      `GOOPG_NARROW_COST_INPUTS` (R121/R128) landed the build-entry-footprint
      half default-ON since R128 (moved TPC-H `join-method` 10→9); only the
      **batch/spill decision** half remains — and that half's absorption is
      **already built**, just never measured or promoted:
      `hashjoin_pggeometry.go`/`hashjoin_pgtuplesizing.go` port PG's packed
      `HashJoinTuple` sizing behind `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108,
      default-off).
    - A second, parallel arm exists for **Sort's** spill decision (which also
      feeds WindowAgg's internal sort via `costWindow`):
      `sort_pgrelationbytes.go` ports PG's `relation_byte_size` behind
      `GOOPG_PG_SORT_RELATION_BYTES_COST` (R113, default-off). Neither R108
      nor R113 has been measured against the post-M0137–M0142 corpus.
    - HashAggregate's width currency is a **settled, already-decided
      divergence** (R120 built a fix, R124 measured it net-neutral paired with
      narrowing, M0137-0009 deleted the flag) — do **not** reopen without new
      evidence.
    - A genuinely **new, unabsorbed** site: Memoize's entry-byte estimate
      (`joinpathsmemoize.go:133-139`) uses goopg's map currency where PG's
      `cost_memoize_rescan` (`postgres/src/backend/optimizer/path/
      costsize.c:2541`) uses `relation_byte_size` (already ported in-tree as
      `pgRelationByteSize`, directly reusable) plus
      `ExecEstimateCacheEntryOverheadBytes` (not yet ported, small named PG
      function).
    - Bitmap heap scan's entry sizing (`costbitmap.go:tbmEntryBytes`) is
      planner/executor self-consistent within goopg, not a cross-currency
      case — not sliced further.
  - Two bounded next slices filed below: **M0139-0007a** (measure/adopt the
    two already-built R108/R113 arms) and **M0139-0007b** (port Memoize's
    currency). Ledger row appended (task-id `m0139-0007`).
- [x] **M0139-0007a — measure and adopt/hold the two already-built R108/R113
  absorption arms.** `GOOPG_PG_HASH_TUPLE_SPILL_COST` (hash-join spill/batch
  decision) and `GOOPG_PG_SORT_RELATION_BYTES_COST` (Sort spill decision, also
  feeds WindowAgg) both already port a named PG formula (file:line cited in
  their source comments) but have never been measured against the
  post-M0137–M0142 corpus. Measure each independently first (row 3 of
  M0139-0007's inventory feeds row 1's competing Sort-based plan shapes, so a
  combined flip could move a plan for a reason neither arm alone explains),
  decide adopt/hold per the plan-parity metric, one design doc per arm,
  following the `GOOPG_GATHER_PATHS` promotion precedent
  (`docs/design/0100-0149/m0140-0003-gather-paths-flip-lands-default-on.md`).
  - **DONE 2026-09-15, both arms measured, both HOLD (design docs
    `docs/design/0100-0149/m0139-0007a-hash-tuple-spill-cost-measurement.md`,
    `docs/design/0100-0149/m0139-0007a-sort-relation-bytes-cost-measurement.md`).**
    Single HEAD binary (env vars read once at package-var init, so on/off
    share one build); measured independently, never combined. **TPC-H: both
    arms byte-identical to the current-HEAD-default baseline for all 22
    queries** (`match=8` unchanged either way, `shape-delta.sh`
    `shape-changed=0` on both, same-PG-reference control against fix1's
    committed capture to strip PG-side ANALYZE sampling noise). **TPC-DS:
    both arms leave every one of the 9 category counts unchanged**
    (`match=2` unchanged); R108 moves Q64's cost digits, R113 moves Q4's and
    Q64's, but all three stay `MISSING-NODE` (PG plans them with Incremental
    Sort or an order-of-magnitude-different estimate) before and after, so no
    category tag is affected — verified via `pg-plan-parity-diff.py
    --verbose`, not assumed from the totals agreeing. **Decision: HOLD for
    both, stay default-off.** No plan-parity upside anywhere to weigh a
    promotion against (unlike `GOOPG_GATHER_PATHS`, which had a genuine bug
    fix to weigh against its category rise); B2 also flags both arms'
    direction as the risky one — PG's tuple/row currency is smaller than
    goopg's real executor allocation for both hash-join spill and Sort spill,
    so promoting either needs the TPC-H SF=1 execution acceptance-arm gate
    first, not run here because nothing in this measurement justifies
    running it. Neither arm deleted (both are correctly-derived PG-formula
    ports, kept as controls). Ledger row appended (task-id `m0139-0007a`).
    M0139-0007's recon inventory rows 1 and 3 are now fully resolved; only
    M0139-0007b (Memoize) remains open below as the one remaining sub-task of
    the banner's top-priority "M0141-S2a-fix and M0139-0007 — costing-order
    unblock" line — M0141-S2a's fix1/fix2 pair is landed-and-decided, and of
    M0139-0007's three filed pieces (recon, 0007a, 0007b) only 0007b is still
    open. The next loop should take M0139-0007b next (still inside the
    top-priority group) unless its own recon surfaces a reason to defer it,
    in which case the banner's item 1 (M0137's re-opened 0014–0017) is next.
- [x] **M0139-0007b — port PG's Memoize entry-byte currency** (DONE
  2026-09-15). Gave `joinpathsmemoize.go`'s `costMemoizeRescan` a new
  default-off arm, `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` (R108/R113-shaped):
  `pgRelationByteSize(tuples, pathWidth(innerPath))` (reused verbatim from
  R113) plus a newly-ported `pgMemoizeEntryOverheadBytes`
  (`ExecEstimateCacheEntryOverheadBytes`, `nodeMemoize.c:1171-1176`, derived
  from the actual `MemoizeEntry`/`MemoizeKey`/`MemoizeTuple` C struct sizes:
  24+24+16·tuples). Pinned by 3 new tests
  (`internal/optimizer/memoize_pgentrybytes_test.go`).
  - **Measured: byte-identical on both corpora.** TPC-H's one Memoize node
    (`rows=1 width=490`) and TPC-DS SF0.25's 13 Memoize nodes (all `rows=1`)
    price identically in both arms — the byte currency only reaches the final
    cost via `evictRatio`, and every candidate's `estCacheEntries` swamps
    `ndistinct` under a 64 MB `work_mem` regardless of currency. Same
    "arm never reaches its own memory-constrained regime" shape 0007a found
    for R108/R113. **Decision: HOLD, stays default-off.**
  - Deferred: the per-key `get_expr_width` term is not absorbed (no goopg
    per-expression width statistic exists yet) — ledger row `m0139-0007b`,
    follow-up filed as **M0139-0007c** below.
  - Design doc: `docs/design/0100-0149/m0139-0007b-memoize-entry-bytes-absorption.md`.
  - **With this, all three of M0139-0007's filed pieces (recon, 0007a, 0007b)
    are resolved** — the banner's "M0141-S2a-fix and M0139-0007" line's
    `M0139-0007` half is complete.
- [x] **M0139-0007c — port `get_expr_width` for Memoize's cache-key width
  Kind: impl
  term.** `costMemoizeRescan`'s per-key contribution
  (`hashsize.EntryBytes(nkeys, 0)`, both currencies) still stands in for PG's
  `get_expr_width` sum over `mpath->param_exprs`
  (`postgres/src/backend/optimizer/path/costsize.c:2566-2567`) because goopg
  has no per-expression average-width statistic wired to this site. Needs a
  per-column/per-expression width lookup (candidate: extend
  `pathAvgVarBytes`/`typeWidth`-style catalog lookups to a bare `*ColumnRef`
  cache key, since `getMemoizePath`'s gates already guarantee every key is
  one) before it can be absorbed rather than approximated. Not currently
  gating the banner's top-priority line — pick up per the milestone's normal
  ordering.
  - **DONE 2026-09-18.**
    Parent: M0139-0007b.
    Kind: impl. Movement: none
    (the ported term only reaches the price behind the already-default-off
    `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`, unchanged by this task). New
    `memoizeKeyWidths` (`joinpathsmemoize.go`) mirrors `memoizeKeyNDistinct`'s
    loop over `innerPath.IndexClauses`, reading each cache key's ANALYZEd
    `ColumnStats.AvgWidth` via `examineJoinVar` (PG's `attr_widths[]`/
    `stawidth` analogue) and falling back to `typeWidth` (`get_typavgwidth`)
    when unanalyzed — the exact fallback order `get_expr_width` uses for a
    `Var`, and the only Node shape reachable here since every admitted cache
    key is a bare `*ColumnRef`. `costMemoizeRescan` gained a `keyWidth
    float64` parameter that only feeds the price when the PG currency is on
    AND `pgRelationByteSize` itself succeeded — the same gate the tuple-bytes
    term beside it already uses, so neither term switches currency alone.
    Two new tests (`TestMemoizeKeyWidthsUsesAnalyzedStatThenTypeWidth`,
    `TestCostMemoizeRescanPGEntryBytesUsesKeyWidth`); five pre-existing
    `costMemoizeRescan` call sites updated for the new parameter (`0`,
    provably inert per the switch-off/currency-separation pins). Design doc:
    `docs/design/0100-0149/m0139-0007c-memoize-key-width-absorption.md`
    (indexed). Does NOT reopen M0139-0007b's HOLD decision — `evictRatio`
    still never activates on either measured corpus, so completing this term
    does not change which regime they exercise. Resolves ledger row
    `m0139-0007b`'s deferred scope (left `status: -` for M0119 to flip, per
    the ledger's own protocol). Gates: `go build ./...` clean, `go vet
    ./internal/optimizer/...` clean, `go test ./internal/optimizer/...` PASS
    (full package, no `-count=1`), `scripts/tpch-spotcheck.sh` PASS
    (Q12=2/Q13=34, unaffected by an off-by-default arm on a never-ANALYZEd
    fresh server).


## M0143 — Engine correctness carry-overs from the parity programme (filed 2026-09-14)

**Milestone doc:** `docs/milestones/0143-engine-correctness-carry-overs.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
**Source:** `METHODOLOGY3/04-forward-plan.md` §3 "Continuous — engine correctness, not gated on anything"
**Prerequisites:** none. Order: `.ralph/fix_plan.md` banner (item 9; M0143-0008 is handled with P0-E5).

Real engine defects the parity programme discovered as a side effect. The case
for treating them as a milestone rather than footnotes: **two genuine wrong-rows
bugs were found by parity work, not by the correctness gates** — the Q13
33-vs-34 Memoize-over-RIGHT-JOIN bug (R63/R64, latent since 2026-09-03, and that
round's own gate should have caught it) and the Limit-below-Unique truncation
(R83, masked on current data only because `78 < 100`).

**Per-task discipline:** each fix lands with a test that fails before it — every
item here exists because nothing in the suite crossed the boundary that would
have caught it. These are not parity tasks: no category movement is expected or
reported, and the values and unit gates are the bar.

- [x] **M0143-0001 — an in-process test that crosses a DATABASE boundary** —
  `CREATE DATABASE` is a dispatch-layer statement the in-process parser rejects, so
  `pgConstraintTableRel`'s per-DB branch and the reload's `ListDatabases` loop — the
  exact paths TPC-H rides — have manual psql evidence only. This is *why* two per-DB
  defects were found in two consecutive rounds; closing it makes the rest of this
  milestone findable by the suite. Highest leverage of the six.
  - **Scoping investigation 2026-09-17 (read-only, no code changed):**
    `parser.Parse("CREATE DATABASE ...")` genuinely errors 42601 — the
    parser has zero grammar for it (`internal/postmaster/dispatch_extended.go:634-645`
    states this explicitly); both wire protocols intercept it by
    string-prefix match (`classifyDatabaseDDL`, `internal/postmaster/database_ddl.go:412`)
    *before* parsing, via `(s *Server) tryHandleDatabaseDDL` (`database_ddl.go:1485`),
    which requires a `*postmaster.Server` — a different package from
    `internal/executor`'s fast in-process test harness (`newVMFixture`/`newHOTFixture`,
    `internal/executor/vm_test.go:11`), and `internal/postmaster` cannot be
    imported from `internal/executor` (import cycle: postmaster already
    imports executor). So the "manual psql evidence only" claim is only
    half true: `internal/postmaster/database_ddl_test.go` already has
    dozens of in-process (no socket, no psql) calls to
    `tryHandleDatabaseDDL` directly (e.g. line 294, 358, 420, `Server`
    constructed at :417/:447), and `internal/postmaster/database_oid_wiring_test.go`
    already in-process-tests the DB-switch mechanism (`executor.Context.CurrentDatabaseOid`,
    a plain settable field, stamped in the real path by
    `(s *Server) wireExtensionRows` at `dispatch.go:3231`, called from
    `dispatch.go:690`/`dispatch_extended.go:229`). The genuinely
    undemonstrated gap is a single in-process test that *chains* both
    halves — `tryHandleDatabaseDDL("CREATE DATABASE r", ...)` then running
    DDL against the new DB's own namespace via the executor (`ectx.CurrentDatabaseOid`
    set from `im.ResolveDatabaseOid("r")`, then `parser.Parse`/`optimizer.Plan`/
    `executor.Build` as normal) — belongs in `internal/postmaster` (not
    `internal/executor`), mirroring `database_ddl_test.go` +
    `database_oid_wiring_test.go` + `internal/executor/fk_dbid_routing_test.go`'s
    `ctx.CurrentDatabaseOid`-direct-set pattern. The harder, less-precedented
    half of this task is exercising the *reload* `ListDatabases` loop
    (`internal/initdb/catalog_heap_reload.go:252,335,442,730,884`,
    `internal/initdb/open.go:1564,3577,3873`) and `pgConstraintTableRel`'s
    per-DB branch (`internal/executor/sys_pg_constraint.go:119-139`) under a
    genuine multi-database WAL-replay/restart in-process (no server
    socket/psql, but heavier than a single `Server`+`Context` — needs
    `internal/initdb`'s open/recovery entry point against a temp data dir
    directly) — no existing precedent for that half. Separate wrinkle:
    `internal/testport/database_template_oid_collision_test.go:9-11`'s own
    comment says the in-process `*postmaster.Server` harness "hangs on
    multi-DB-write shutdown," a pre-existing harness limitation that may
    bite the reload-loop half specifically. Resume point: start with the
    `internal/postmaster`-level chain test (concrete, low-risk, closes the
    "manual psql evidence" gap for the DDL-execution half); scope the
    reload-loop half as a likely-separate follow-up once the shutdown-hang
    wrinkle is understood, rather than attempting both in one sitting.
  - **Landed 2026-09-17 (DDL-execution half only — still open for the
    reload-loop half; not ticked).** `TestDatabaseDDLChainedExecutorDML`
    (`internal/postmaster/database_ddl_chain_test.go`): chains
    `s.tryHandleDatabaseDDL("CREATE DATABASE r", ...)` with
    `s.wireExtensionRows(ectx, "r")` (the real per-connection oid-resolution
    path, not a synthetic `ctx.CurrentDatabaseOid` constant) and runs
    CREATE TABLE + a REFERENCES FK against the new database's own real oid
    via the executor pipeline, mirroring
    `internal/executor/storage_ddl_test.go`'s `runDDL`/
    `fk_dbid_routing_test.go`'s `runQueryUnderDBOid` patterns (reimplemented
    locally in `internal/postmaster` since those are unexported executor
    test helpers). A second context bound to `"postgres"` gets its own
    table+FK. Verifies `pg_constraint`'s FK row (`pgConstraintTableRel`'s
    per-database branch, reached via `wireExtensionRows`' `PgConstraintRows`
    wiring) by `conname` identity, not just count, and cross-database
    namespace isolation (each database's table is invisible under the
    other's oid). Test-design finding worth keeping: a count-only assertion
    would NOT have caught a real regression here — temporarily forcing
    `PGConstraintRowsForDBOid`'s `dbOid` argument to `DefaultDBOid`
    (mirroring the exact hardcode shape `M0143-0002`/`-0002b` fixed
    elsewhere) left both databases' row counts at 1 while `db r`'s query
    silently returned `db postgres`'s FK row instead of its own — caught
    only because the assertion checks the actual `conname` value. Verified
    the mutation makes the test fail, then reverted it (temporary,
    `catalog.go` carries no diff). No production code changed. Design doc:
    `docs/design/0100-0149/m0143-0001-database-boundary-chain-test.md`
    (indexed in `docs/design/README.md`). Gates: `go build ./...` clean;
    `go test ./internal/postmaster/... ./internal/catalog/...
    ./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` PASS (units scope excludes
    `internal/postmaster`, run separately above).
  - **Reload-loop half landed 2026-09-17 — task now fully complete.**
    Investigated the "hangs on multi-DB-write shutdown" wrinkle first (a
    research pass, not code): traced it to `Server.Run`'s shutdown path
    (`internal/postmaster/server.go:582-701`) — no existing in-process
    `*postmaster.Server`-with-`Run()` test harness sets `shutdownDeadline`,
    so shutdown always falls to the unbounded `s.connWG.Wait()` branch
    (`:693`), which blocks forever if any backend goroutine is still alive;
    no repro/stack trace exists anywhere, the claim traces to a single
    commit message with no further diagnosis. Sidestepped entirely rather
    than fixed: `TestDatabaseDDLReloadAcrossRestart`
    (`internal/postmaster/database_ddl_reload_test.go`) uses
    `postmaster.Server` purely as a `tryHandleDatabaseDDL`/
    `wireExtensionRows` method-holder (`Run()` is never called, so there is
    no listener/accept-loop/`connWG` for the hang to occur in), against a
    real `internal/initdb.Runtime` (`Open`/`Close`/`Open` on the same data
    dir), mirroring `internal/initdb/heap_catalog_load_test.go`'s existing
    restart shape. Drives two non-default databases (`r1`, `r2`), each with
    its own FK-bearing table pair, through a genuine restart and
    re-verifies post-reload: `reloadDatabasesFromHeap` repopulated
    `ListDatabases()` with both; `loadForeignKeysFromHeap`'s per-database
    scan (`pgConstraintTableRel`'s `tableCatalogHeapDBOid` routing)
    reconstructs each database's own FK by `conname` identity, not just
    count; cross-database namespace isolation holds after reload. Two live
    mutations verified the test is a real tripwire, both reverted before
    commit: (1) omitting `Config.TxnMgr` reproduces a genuine latent
    durability gap this test surfaced — `syncPgDatabaseHeapRow`
    (`internal/postmaster/database_ddl.go:1365-1368`, `runPgDatabaseHeapTxn`)
    silently no-ops without a `TxnMgr`, so `CREATE DATABASE` would look
    successful yet vanish after restart with no error anywhere (the
    already-landed DDL-chain test never caught this because it never
    restarts); (2) forcing `loadForeignKeysFromHeap`'s per-database loop to
    always pass `catalog.DefaultDBOid` (simulating a reload-time version of
    the `M0143-0002`/`-0002b` bug class) makes both FK assertions fail with
    empty results, confirming the test exercises the reload loop's
    per-database routing, not just the already-covered query-time routing.
    Design doc and README index updated in the same commit. Gates: `go
    build ./...` clean; `go vet ./internal/postmaster/...` clean; `go test
    ./internal/postmaster/... ./internal/catalog/... ./internal/initdb/...
    ./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` PASS. No production code changed —
    test-only, no deferral-ledger row needed.
- [x] **M0143-0002 — `ALTER TABLE … DROP CONSTRAINT` on an FK reports success and does
  nothing** — `InMemory.DropForeignKeyConstraint` hardcodes `DefaultDBOid`
  (`catalog.go:22241`) and `execAlterTableDropConstraint` discards the result
  (`operators_ddl.go:13303`). `HasPrimaryKey` (`catalog.go:22261`) has the same shape,
  and six `deleteCatalogRowsForOID` sites were filed for the same check and never
  confirmed. R126 made this worse in effect, because such an FK now survives restarts.
  - **Done 2026-09-17.** Fixed the confirmed defect this task names: changed
    `DropForeignKeyConstraint`'s signature from `(tableOID uint32, name string)
    bool` to `(tbl *Table, name string) bool` (`internal/catalog/catalog.go`)
    so it mutates the caller's already-resolved live `*Table` pointer directly
    instead of re-resolving by OID via `tableByOID(tableOID, DefaultDBOid)` —
    which silently returned `ok=false` (so nothing was removed) for any table
    outside the default database. Single call site updated
    (`internal/executor/operators_ddl.go`'s FK branch of
    `execAlterTableDropConstraint`). New regression test
    (`internal/testport/m0143_0002_fk_drop_constraint_nondefault_db_test.go`)
    creates the FK in database `"r"` (non-default) and asserts a COMMITted
    `DROP CONSTRAINT` actually disables enforcement — confirmed to genuinely
    fail pre-fix (git-stashed the fix, red) and pass post-fix. Also re-pointed
    `TestPort_M0143_0008b_DropForeignKeyRollbackUndo`
    (`p0e5b_alter_drop_constraint_rollback_undo_test.go`) from the default-db
    workaround it needed pre-fix back onto database `"r"`, per that test's own
    documented resume point — still green. **Not covered by this fix, filed
    as M0143-0002b below:** `HasPrimaryKey`/`dropIndexByName` share the exact
    same `c.ns(DefaultDBOid).byTable[...]` hardcode shape (confirmed by
    reading, not live-tested this loop — `catalog.go`'s index registries
    genuinely are stored per-DB via `c.ns(dbOid).byTable`, so this is very
    likely a live bug for PK/UNIQUE/EXCLUDE constraints on a non-default DB
    table too); the "six `deleteCatalogRowsForOID` sites... never confirmed"
    note in this task's own original text was not investigated this loop
    (scope: this loop fixed the one defect with an in-hand live repro, not
    every same-shape sibling named in the task's prose).
- [x] **M0143-0002b — audit `HasPrimaryKey`/`dropIndexByName`'s DefaultDBOid
  hardcode for the same non-default-DB no-op M0143-0002 had.**
  Parent: M0143-0002. Filed 2026-09-17 when M0143-0002's fix confirmed the
  FK-specific instance of this hardcode shape live but left the PK/UNIQUE/
  EXCLUDE-backed siblings and the original task's "six
  `deleteCatalogRowsForOID` sites" note unconfirmed. Both read
  `c.ns(DefaultDBOid).byTable[tableOID]` (`catalog.go:22214`, `:22261`)
  unconditionally, but index registration is genuinely per-DB
  (`c.ns(dbOid).byTable[tbl.OID]`, confirmed by reading the registration
  sites). If a PK/UNIQUE/EXCLUDE-backed table lives in a non-default
  database, `HasPrimaryKey` likely always reports `false` for it and
  `dropIndexByName` (backing `DropUniqueConstraint`/`DropExclusionConstraint`)
  likely always silently no-ops its DROP — the same failure shape M0143-0002
  fixed for FKs, unconfirmed live. Also re-check the "six
  `deleteCatalogRowsForOID` sites... filed for the same check and never
  confirmed" note from M0143-0002's original text (not re-derived this loop;
  re-`grep`/re-read the call sites at `operators_ddl.go` before assuming
  which six were meant). Resume point: write a `HasPrimaryKey`/
  `DropUniqueConstraint`/`DropExclusionConstraint` non-default-DB repro
  mirroring `m0143_0002_fk_drop_constraint_nondefault_db_test.go`, confirm
  which are actually broken before changing any of them (a wrong theory here
  would not be the first time a "same shape" claim needed live confirmation
  first — mirrors this task's own FK finding, which WAS live-confirmed before
  the fix landed).
  - **Done 2026-09-17.** Confirmed the hypothesis live: `dropIndexByName`
    (`internal/catalog/catalog.go`, shared by `DropPrimaryKeyConstraint`/
    `DropUniqueConstraint`/`DropExclusionConstraint`) and `HasPrimaryKey`
    both hardcoded `c.ns(DefaultDBOid)` the exact M0143-0002 shape. Fixed by
    changing all three public wrappers plus `dropIndexByName` itself to take
    the resolved `*Table` (not a bare `tableOID`) and key `c.ns()` off
    `tbl.DBOid` (falling back to `DefaultDBOid` when zero, the same idiom
    `TableRealPages`/`relAllVisibleCell` already use); `HasPrimaryKey` got
    the same `table.DBOid`-keyed fix without a signature change (it already
    took a `*Table`). Five call sites updated: `operators_ddl.go`'s
    PK/UNIQUE/EXCLUDE `DROP CONSTRAINT` branches (3) and `catalog_test.go`'s
    `TestDropPrimaryKeyConstraint`-style unit test (2 calls). New test
    `internal/testport/m0143_0002b_index_backed_drop_constraint_nondefault_db_test.go`
    — one sub-test per constraint kind (PK, UNIQUE, EXCLUDE `USING btree (a
    WITH =)`), each created in database `"r"`, COMMITted (not ROLLBACKed)
    `DROP CONSTRAINT`, asserting enforcement is actually gone; confirmed
    genuinely red pre-fix via `git stash` of the three touched source files
    (`23505`/`23P01` still-enforced errors on all three) and green post-fix.
    **Not investigated this loop** (task's own text flagged it as a separate
    open question, not this task's scope): the original M0143-0002 "six
    `deleteCatalogRowsForOID` sites... never confirmed" note —
    `deleteCatalogRowsForOID`'s ~15 call sites already take an explicit
    `dbOid` parameter (grep'd), so whichever six sites the original note
    meant need their own fresh identification, not an assumption they share
    this hardcode shape. Design doc:
    `docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md` new
    "M0143-0002b" section; `docs/design/README.md` p0-e4 row title +
    description updated. Gates: `go build ./...` clean; `go vet
    ./internal/testport/... ./internal/catalog/... ./internal/executor/...`
    clean; `go test ./internal/catalog/... ./internal/executor/...` PASS;
    `go test -v -run
    'TestPort_M0143_0002|TestPort_M0143_0002b|TestPort_M0143_0008b|TestPort_P0E4|TestPort_P0E5'
    ./internal/testport/` PASS 12/12; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` — same pre-existing 60 `internal/parser`
    failures (M0143-0006, no parser file touched, same count/shape as prior
    loops); `scripts/tpcds-sf025-regression.sh sweep` — `PASS=96 MISMATCH=0
    CKMISMATCH=0 ERROR=0 TIMEOUT=0`, plan-shapes 99/99 identical.
    `tpch-spotcheck`/`tpch-acceptance-arm` still SKIP-BLOCKED by the
    `:65433` hold (same accepted G6 exception, P0-E7 remains the re-run
    owner).
- [x] **M0143-0002c — re-identify the "six `deleteCatalogRowsForOID` sites"
  M0143-0002's original text named.**
  Parent: M0143-0002b. Filed 2026-09-17
  when M0143-0002b closed without resolving this forward-reference (it was
  deferred from M0143-0002 to M0143-0002b, and M0143-0002b's own fix scope
  was the PK/UNIQUE/EXCLUDE `DefaultDBOid` hardcode only). Both M0143-0002's
  and M0143-0002b's loops confirmed `deleteCatalogRowsForOID`'s ~15 call
  sites already take an explicit `dbOid` parameter (grep'd, not exhaustively
  read), so the original "six sites... filed for the same check and never
  confirmed" note does not describe this hardcode shape as-is — it needs
  fresh identification of what it actually meant (a different check,
  possibly at a different layer) before it can be confirmed or fixed.
  Resume point: `grep -n "deleteCatalogRowsForOID" internal/executor/operators_ddl.go`,
  read each call site's surrounding context for a *different* per-DB
  hardcode shape (not the `c.ns(DefaultDBOid)` one M0143-0002/-0002b already
  fixed), and check `git log`/blame around the M0143-0002 filing date
  (2026-09-14, `METHODOLOGY3/04-forward-plan.md` §3) for the original
  six-sites claim's source context.
  - **Done 2026-09-17 (investigation only, no code changed — see
    M0143-0002d for the fix).** The original claim's source is
    `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/02-open-problems.md`
    §N11 (via `r126-fk-persistence/SCOPE.md:155-160`, `REPORT.md:273-276`,
    `TODO.md:5812-5817`) — "N11: Six `deleteCatalogRowsForOID` call sites
    hardcode `catalog.DefaultDBOid` ... at :24974/:25014/:25057/:25104/
    :25349/:25401 [historical line numbers from R126's era] instead of
    looping `tableCatalogDBOids`." Re-grepping at HEAD (`operators_ddl.go`
    has grown to 27,475 lines) finds exactly six live matches for
    `deleteCatalogRowsForOID(.*catalog\.DefaultDBOid` — confirmed as the
    composite-type re-sync branches of `execAlterType` (four
    single-subcommand forms: ADD/RENAME/DROP/ALTER ATTRIBUTE, `:25242`,
    `:25282`, `:25325`, `:25372`), `execAlterTypeAttrCmds`'s combined form
    (`:25617`), and `execDropType`'s composite branch (`:25669`). These are
    genuinely distinct from the M0143-0002/-0002b hardcode (which was
    `c.ns(DefaultDBOid)` inside `catalog.InMemory`'s table/index registries)
    — this one is a `storage.RelFileNode{DBOid: catalog.DefaultDBOid, ...}`
    literal picking which database's *physical heap file* to xmax-stamp.
    **The re-identification surfaced something much bigger than "these six
    call sites are wrong in isolation":** their sibling *write* paths —
    `writeTypeHeapRowWithIndexes` (`:18447-18458`, the pg_type row writer
    shared by every `CREATE TYPE` of any kind — enum/domain/composite/range)
    and `syncCompositeTypeToCatalogHeap`'s `classRel`/`attrRel`
    (`:18539-18544`/`:18555-18561`, the composite's implicit pg_class/
    pg_attribute row writer) — hardcode the exact same `catalog.DefaultDBOid`
    literal. So the six ALTER/DROP delete sites are *consistent* with
    CREATE, not independently broken: every user-declared type's physical
    catalog-heap row, in every database, has always landed in the shared
    DEFAULT database's pg_type/pg_class/pg_attribute heap files. **Live-
    confirmed the consequence on a throwaway 55xx cluster** (test written,
    run, and DELETED after confirming — not committed, since this loop
    lands no fix): `CREATE TYPE samename AS (a int)` in the default
    database, then `CREATE DATABASE r; \c r; CREATE TYPE samename AS (x
    text, y text)` — the second CREATE TYPE succeeds with **no name-
    collision error** (PG would reject neither, since composite types are
    correctly scoped per-database in the in-memory registry — `compositeKey`
    folds in dbOid — so no error is actually wrong here), but
    `SELECT a.attname FROM pg_attribute a JOIN pg_type t ON
    t.typrelid=a.attrelid WHERE t.typname='samename'` run from **either**
    database returns the **union** `[a, x, y]` instead of each database's
    own `[a]` / `[x, y]` — genuine cross-database catalog corruption visible
    to plain SQL (pg_dump, `\d`, information_schema all ride this same
    path). Contrast: `pg_constraint` already has the correct pattern for
    this class of problem — `pgConstraintTableRel`
    (`sys_pg_constraint.go:133`) resolves via `tableCatalogHeapDBOid(ctx)`,
    and even the TYPE-catalog's own **index** inserts already route
    correctly the same way (`insertCanonicalSysBtreeLeaf`,
    `sys_catalog_index_insert.go:423-428`, routes via
    `tableCatalogHeapDBOid(ctx)` — comment says "a distinct-dbOid database
    now has its own bootstrapped catalog btrees") — so per-DB index files
    already exist and are already used correctly; only the **heap row
    writes/reads** for user-defined types never got the same treatment.
    **Why this loop does not attempt the fix:** changing only the six
    ALTER/DROP delete sites to target the connection's real dbOid, while
    CREATE (`writeTypeHeapRowWithIndexes`/`syncCompositeTypeToCatalogHeap`)
    keeps writing to `DefaultDBOid`, would make ALTER/DROP's xmax-stamp
    target a heap location with **no matching row** (the real row is in
    Default) — a *new*, different no-op regression, not a fix. The correct
    fix must move CREATE, ALTER, and DROP together across all four type
    kinds (enum/domain/composite/range all share `writeTypeHeapRowWithIndexes`),
    and must also account for the read side (SeqScan of pg_type/pg_class/
    pg_attribute appears to read the same shared Default file regardless of
    the querying connection's database, per the live repro above) — too
    large and too risky for a single-task loop, especially with the
    milestone banner's "no reverts" rule (R3) raising the cost of a wrong
    first attempt. Filed as **M0143-0002d** below with this loop's findings
    as its resume point. Ledger: 1 new row (`.ralph/deferral_ledger.md`).
    Gates: none run (no code changed — investigation-only loop, mirrors the
    M0143-0001 scoping loop's precedent).
- [x] **M0143-0002d — user-defined type catalog-heap storage
  (pg_type/pg_class/pg_attribute for enum/domain/composite/range types) is a
  single un-partitioned store shared by every database, unlike pg_constraint.**
  Parent: M0143-0002c. Filed 2026-09-17 from M0143-0002c's re-identification
  loop, which found the "six `deleteCatalogRowsForOID` sites" are a symptom
  of this much larger gap, not an independently fixable bug. **Live-confirmed
  repro** (see M0143-0002c's Done note for the exact commands and query): two
  databases each declaring a composite type of the same name do not collision-
  error (correctly — they're separate types) but their pg_attribute/pg_type
  rows are visibly the UNION of both types' fields from *either* database,
  because every user type's physical row always lands in `catalog.DefaultDBOid`'s
  heap files regardless of which database's session created it.
  **Write-side hardcodes to fix together** (all `storage.RelFileNode{DBOid:
  catalog.DefaultDBOid, ...}` literals in `internal/executor/operators_ddl.go`):
  `writeTypeHeapRowWithIndexes` (`:18447-18458`, shared pg_type writer — every
  `CREATE TYPE`/`CREATE DOMAIN` of any kind funnels through this), the
  `syncCompositeTypeToCatalogHeap` `classRel`/`attrRel` pair (`:18539-18544`/
  `:18555-18561`, composite's implicit pg_class/pg_attribute), and the six
  M0143-0002c delete-side sites (`execAlterType` `:25242`/`:25282`/`:25325`/
  `:25372`, `execAlterTypeAttrCmds` `:25617`, `execDropType`'s composite
  branch `:25669`) plus its enum branch (`~:25644-25648`, same hardcode for
  `deleteTypeFromCatalogHeap`/`deleteEnumLabelRowsByTypid`) and range branch
  (`~:25684-25689`) — not independently counted in "the six" but sharing the
  identical shape and needing the same fix. **Fix these together, not
  piecemeal**: changing only a subset would make some paths target the
  connection's real dbOid while others still write to Default, which is a
  *worse* inconsistency than today's uniform (wrong) DefaultDBOid — every
  write site above must move to `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
  (or the resolved `o.ctx.CurrentDatabaseOid` if already normalized at the
  call site — check `NamespaceDBOid`'s 0/`PostgresDBOid` folding, `catalog.go:25196`)
  in the same commit. **Read-side root cause — LOCATED 2026-09-17 (this loop,
  investigation-only, no code changed):** `pg_type`/`pg_attribute` are
  registered exactly ONCE, at server startup, by
  `loadSystemCatalogsIfPresent` (`internal/initdb/open.go:2931-2972`) — it
  builds one `catalog.Table{OID: catalog.TypeRelationId/.AttributeRelationId}`
  literal per relation (no `DBOid` field set, so it defaults to Go's zero
  value) and calls `cat.RegisterRealTable(t)` with no dbOid argument, which
  resolves to `DefaultDBOid`'s namespace only (`resolveDBOid`,
  `catalog.go:4078-4083`) — there is no equivalent call for any other
  database, ever. Every `SeqScan`/write against pg_type or pg_attribute
  (from `copy.go`, `operators_bitmap.go`, `operators_ddl.go`, etc. — all
  route through the one shared `Catalog.RelFileNode(tbl)`, confirmed by
  grepping every call site) resolves via `catalog.InMemory.RelFileNode`
  (`catalog.go:22354-22366`): `dbOid := c.dbOid; if table.DBOid != 0 &&
  table.DBOid != DefaultDBOid { dbOid = table.DBOid }` — since these two
  `Table` structs' `DBOid` field is always `0`, the condition never fires and
  every connection, regardless of `ctx.CurrentDatabaseOid`, resolves to the
  same `c.dbOid` (a single process-wide field stamped once by `SetDBOID` at
  startup — see the `RelFileNode` doc comment at `catalog.go:22301-22320`).
  This is the exact mechanism behind the cross-database UNION repro. Contrast
  confirmed: `pgConstraintTableRel` (`sys_pg_constraint.go:133`) is different
  in kind, not just routing — it computes `tableCatalogHeapDBOid(ctx)` fresh
  on every call from `ctx.CurrentDatabaseOid`, whereas pg_type/pg_attribute's
  `RelFileNode` resolution is baked into a single shared `Table` struct at
  registration time and never revisited per-connection. **Fix shape implied**
  (real PG: pg_type/pg_class/pg_attribute are NOT shared catalogs —
  `relisshared=false` — each database has its own physical copy under its own
  `base/<dbOid>/`): `loadSystemCatalogsIfPresent`'s registration needs to run
  once per database (not just DefaultDBOid) with `t.DBOid` set to that
  database's own oid, AND each database needs its own physical
  `base/<dbOid>/1247`/`base/<dbOid>/1249` heap file provisioned — most likely
  at `CREATE DATABASE` time (mirror `syncCopiedTableCatalogHeap`'s per-table
  pattern in `internal/postmaster/database_ddl.go:1291-1335`, which already
  does the equivalent per-table dance for ordinary user tables copied from a
  template) plus a startup-reload pass that registers every already-existing
  database's copy, not just the default one. This is materially bigger than
  the write-side hardcode list above — it is genuinely no small tweak, so
  scope the implementing loop(s) to design-first (a design doc is mandatory
  here per AGENT.md D3 before code lands) rather than attempting the
  provisioning + registration + write-side fix all in one pass. **Index
  entries are already correct** — `insertCanonicalSysBtreeLeaf` (`sys_catalog_index_insert.go:423-428`)
  already routes via `tableCatalogHeapDBOid(ctx)`, so `pg_type_oid_index`/
  `pg_type_typname_nsp_index`/`pg_class_relname_nsp_index`/
  `pg_attribute_relid_attnum_index` inserts do not need to change — only the
  heap rows they point at. Test with two databases each declaring a
  same-named enum, domain, composite, and range type (mirrors the live repro
  above) and confirm each database's `pg_dump`/`SELECT ... FROM pg_type`
  shows only its own type's shape after the fix. Needs a design note
  (non-trivial, cross-cutting reload-adjacent change) per AGENT.md's
  Plan-parity harness D3.
  - **Done 2026-09-17 (design-first, per this task's own filing
    instruction — no production diff).** Design doc:
    `docs/design/0100-0149/m0143-0002d-per-database-type-catalog.md`
    (indexed in `docs/design/README.md`). Confirms both root causes with
    file:line citations and, live-checked this loop, **refutes the
    "materially bigger... provisioning" fear the original filing carried**:
    every database already gets its own physical `base/<dbOid>/1247`/`1249`
    heap file at `CREATE DATABASE` time (`copyBootstrapCatalogImage`,
    `internal/initdb/initdb.go:426-473`, copies template0's whole catalog
    image) — the remaining gap is in-memory registration/routing only, not
    disk layout. Also explains, newly, why ordinary tables don't share the
    bug despite `pg_class` being an equally-global singleton `catalog.Table`:
    `pg_class` output is virtual (per-database-filtered already), while
    `pg_type`/`pg_attribute` are real heap-backed relations reached by an
    actual `SeqScan` through the buggy `RelFileNode` resolution. Fix shape
    mirrors the identical problem already solved for indexes
    (`RegisterIndexDuringRecoveryForDB`), decomposed below into two
    loop-sized tasks (read-side registration must land before the write-side
    hardcode fix — reversed order would make a distinct-dbOid connection's
    own new type invisible to itself, worse than today's union bug). No
    ledger row: recon/design only, no production diff; the two child tasks
    below own the actual fix and their own deferral bookkeeping if either
    lands partially.
- [x] **M0143-0002e — per-database `catalog.Table` registration for
  `pg_type`/`pg_attribute` (read side of M0143-0002d).**
  Parent: M0143-0002d. Filed 2026-09-17 from M0143-0002d's design doc, fix-shape steps 1-3. Add a
  `catalog.InMemory` method that registers a `Table` for a system relation
  (pg_type/pg_attribute shape) into `c.ns(dbOid)` with `Table.DBOid = dbOid`
  set (mirrors `RegisterIndexDuringRecoveryForDB`,
  `internal/catalog/catalog.go:6584-6698`); wire it into (a) a startup reload
  loop over `cat.ListDatabases()` right after `loadSystemCatalogsIfPresent`'s
  existing `DefaultDBOid` call (`internal/initdb/open.go:1376`), mirroring
  the index precedent's loop at `open.go:3576-3585` (skip
  DefaultDBOid/PostgresDBOid/`cat.DBOID()`, check `heapFilePresent` on
  `base/<dbOid>/1247|1249` before registering); and (b) `CREATE DATABASE`
  time, next to `copyTemplateTables`'s existing
  `im.TryRegisterUserTable(newTbl, newOid)` call
  (`internal/postmaster/database_ddl.go:995`) — registration-only, the
  physical files already exist from `copyBootstrapCatalogImage`. **No
  observable SQL behavior change yet** (write side still hardcodes
  DefaultDBOid until M0143-0002f) — gate is a new unit test confirming
  `pg_type`/`pg_attribute` `RelFileNode` resolution differs per
  `ctx.CurrentDatabaseOid` after this lands, plus the full existing
  unit/regress suite staying green (the DefaultDBOid path is every existing
  test and must be byte-identical).
  - **Done 2026-09-17.** Implemented as an enhancement of the EXISTING
    `RegisterRealTable(t *Table, dbOid ...uint32)` rather than a brand-new
    method — it already carried an unused `dbOid ...uint32` param wired
    nowhere; `internal/catalog/catalog.go:12696` now sets `t.DBOid =
    resolved` whenever the resolved dbOid is a genuine non-default database
    (omitting the arg, or passing `DefaultDBOid` explicitly, leaves
    `t.DBOid` at its zero value — byte-identical to every pre-existing
    caller/test). `loadSystemCatalogsIfPresent`
    (`internal/initdb/open.go:2931`) is now a thin wrapper: the original
    body moved to `loadSystemCatalogsIfPresentForDB(dataDir, cat, heapDBOid,
    nsDBOid)`, called once for the `DefaultDBOid` pass (unchanged asymmetry:
    reads `base/<cat.DBOID()>`, registers into `DefaultDBOid`'s namespace)
    then once per other already-registered database from `cat.ListDatabases()`
    (heapDBOid==nsDBOid==dbOid), skipping
    DefaultDBOid/PostgresDBOid/`cat.DBOID()` exactly like the index
    precedent's loop. New exported `initdb.RegisterSystemCatalogsForDB(dataDir,
    cat, dbOid)` wraps the same per-DB helper for `CREATE DATABASE` time;
    wired into `internal/postmaster/database_ddl.go`'s
    `tryHandleDatabaseDDL` right after `createDatabasePhysicalDirectory`
    succeeds (new `(*Server).registerSystemCatalogsForNewDB`, unconditional
    on `tmplTables` since every database gets its own pg_type/pg_attribute
    files regardless of template content) — a failure there rolls back the
    same way a scaffolding failure does (`cat.DropDatabase` +
    `removeDatabasePhysicalDirectory`). Two new unit tests:
    `internal/catalog/register_real_table_dbid_test.go`
    (`TestRegisterRealTablePerDatabaseRelFileNode`, the catalog-primitive
    gate — confirms `RelFileNode` now differs per dbOid and the no-dbOid
    call path is unchanged) and
    `internal/initdb/system_catalog_dbid_test.go`
    (`TestRegisterSystemCatalogsForDBRoutesToOwnNamespace`, the
    physical-file + wiring gate — real `CreatePerDatabaseScaffolding` +
    `RegisterSystemCatalogsForDB` round trip, plus a no-scaffolding dbOid
    no-op case). Gates run: `go build ./...` clean;
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` all green
    including `internal/catalog` and `internal/initdb`
    (125s, real cost — the initdb package boots a full cluster per test);
    `go test ./internal/postmaster/...` green separately (both new unit
    tests plus the full existing suite, confirming the CREATE DATABASE path
    change is byte-identical for every existing test — none of them create a
    second database with types, so none observes the new registration).
    **No SQL-observable behavior change** — confirmed by design: writes
    still hardcode `DefaultDBOid` until M0143-0002f, so this task's own
    correctness claim rests entirely on the two new unit tests above, not on
    any end-to-end SQL assertion (an end-to-end test would show nothing
    different yet). M0143-0002f (write-side route-through) is next, in
    order, now unblocked.
- [x] **M0143-0002f — route type-catalog heap writes through
  `tableCatalogHeapDBOid(ctx)` (write side of M0143-0002d).**
  Parent: M0143-0002d. Depends on M0143-0002e `[x]`. Filed 2026-09-17 from
  M0143-0002d's design doc, fix-shape step 4. Change
  `writeTypeHeapRowWithIndexes`
  (`internal/executor/operators_ddl.go:18447-18458`),
  `updateTypeHeapRowWithIndexes` (`:18483-18504`),
  `syncCompositeTypeToCatalogHeap`'s `classRel`/`attrRel` pair
  (`:18539-18543`/`:18555-18559`), and the M0143-0002c delete-side sites
  (`execAlterType` `:25242`/`:25282`/`:25325`/`:25372`,
  `execAlterTypeAttrCmds` `:25617`, `execDropType`'s composite/enum/range
  branches `~:25644-25689`) from the `catalog.DefaultDBOid` literal to
  `tableCatalogHeapDBOid(ctx)` (`:18697-18699`, already
  `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`) — **all sites in the same
  commit**, per the design doc's sequencing note (a partial change is worse
  than today's uniform bug). Test: mirror
  `internal/postmaster/database_ddl_reload_test.go`'s
  `TestDatabaseDDLReloadAcrossRestart` shape — two databases each declare a
  same-named enum/domain/composite/range type, restart, confirm each
  database's `pg_type`/`pg_attribute`/`pg_dump` output shows only its own
  type's shape (the M0143-0002c repro, now also across a restart, not just
  live-session).
  - **Done 2026-09-17.**
    Movement: none — real correctness work, but not one of S3's three
    instruments (owner reclassification 2026-09-18). What it achieved: a
    same-named composite/domain/range type declared in two
    non-default databases now shows each database's own shape (not the
    M0143-0002c UNION) in both a live session and, after this task's own
    restart bug fix below, across a `Close`/`Open` restart; confirmed by a
    new SQL-observable test, not just a unit-level structural check.
    All enumerated sites changed to
    `tableCatalogHeapDBOid(ctx)`/`tableCatalogHeapDBOid(o.ctx)` in one
    commit; live pre-restart verification matched the design (each
    database's composite type shows only its own fields, not the union).
    **The restart test caught a second, independent bug** in M0143-0002e's
    own landing: its per-database `pg_type`/`pg_attribute` registration loop
    lived inside `loadSystemCatalogsIfPresent` (`internal/initdb/open.go`),
    called (line 1376) *before* `reloadDatabasesFromHeap` (line 1546)
    populates `cat.ListDatabases()` — the very list the loop iterates — so
    it silently registered zero non-default databases on every restart
    (symptom: 0 rows post-restart, not the union, for either database's own
    type). Fixed by relocating the per-DB loop to run right after
    `reloadDatabasesFromHeap`'s `loadUserTablesFromHeapForDB` loop (same
    precondition, same shape); `loadSystemCatalogsIfPresent` is now just the
    DefaultDBOid pass again. New test:
    `internal/postmaster/database_ddl_type_reload_test.go`
    (`TestDatabaseDDLTypeCatalogReloadAcrossRestart`), covering composite
    (pg_attribute join, the exact M0143-0002c repro), domain (typbasetype
    identity), and range (pg_type isolation only — `pg_range`'s own
    DBOid routing is a separate, still-hardcoded catalog, out of this
    task's enumerated scope). Two write sites found while re-auditing every
    remaining `catalog.DefaultDBOid` literal were deliberately NOT touched
    since they were never in M0143-0002d's enumerated list — `pgRangeRel`
    (`internal/executor/sys_pg_range.go:54-59`) and `execDropDomain`/the
    ACL-resync functions (`resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`,
    `operators_ddl.go:23537,23667-23669,26366,26369`) — filed as
    **M0143-0002g** below with a `.ralph/deferral_ledger.md` row (dated
    2026-09-17). Design doc updated:
    `docs/design/0100-0149/m0143-0002d-per-database-type-catalog.md` (§
    M0143-0002f Done note) and `docs/design/README.md` (status → done).
    Gates: `go build ./...` clean; `go test ./internal/postmaster/...
    ./internal/executor/... ./internal/initdb/... ./internal/catalog/...`
    PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full
    green; `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS against
    the staged tree. `tpch-spotcheck` not run — no TPC-H data needed for
    this unit-scoped fix, consistent with the P0-E6-wait selection rule.
- [x] **M0143-0002g — the two write-site gaps M0143-0002f's own audit found
  but did not fix (out of that task's enumerated scope).**
  Parent: M0143-0002d. Filed 2026-09-17 from M0143-0002f's Done note. (1)
  `pgRangeRel` (`internal/executor/sys_pg_range.go:54-59`, backs
  `writeRangeCatalogRow`/`deleteRangeCatalogRow`) still hardcodes
  `catalog.DefaultDBOid` — a range type's `pg_range` row (subtype/
  collation/opclass linkage) always lands in the default database's heap
  regardless of which database declared the range, even though its
  `pg_type` rows are now correctly per-database (M0143-0002f). (2)
  `execDropDomain`'s two `deleteTypeFromCatalogHeap` calls
  (`operators_ddl.go:26366`,`:26369`) and the GRANT/REVOKE re-sync functions
  `resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`
  (`operators_ddl.go:23537`,`:23667-23669`) were never in M0143-0002d's
  enumerated write-site list, so DROP DOMAIN and GRANT/REVOKE ON
  TYPE/table-column-ACL on a non-default database still target the wrong
  heap file. Fix: same one-line `catalog.DefaultDBOid` →
  `tableCatalogHeapDBOid(ctx)` swap at each site; for (1), also extend
  `TestDatabaseDDLTypeCatalogReloadAcrossRestart`'s range assertion from
  pg_type-only to the `rngsubtype` join it currently avoids. Each needs its
  own regression test (a non-default-DB DROP DOMAIN / GRANT ON TYPE /
  CREATE TYPE AS RANGE case).
  - **Done 2026-09-17 (item 2 only — item 1 carved out to M0143-0002h).**
    Movement: none — real correctness work, not S3 movement (owner
    reclassification 2026-09-18). What it achieved: a GRANT/REVOKE ON TYPE or DROP DOMAIN issued against a
    type declared in a non-default database now correctly mutates that
    database's own `pg_type`/`pg_attribute` heap instead of silently
    corrupting it (a stray duplicate row on GRANT, a surviving row on DROP);
    confirmed by two new SQL-observable restart tests, each shown to fail
    with the exact predicted symptom when the fix is reverted.
    Landed the one-line `catalog.DefaultDBOid` → `tableCatalogHeapDBOid(ctx)`
    swap at `execDropDomain`'s two `deleteTypeFromCatalogHeap` calls and at
    `resyncTypeACLHeapRow`/`resyncAttrACLHeapRow`'s delete+attrRel
    construction (`operators_ddl.go:23537,23667-23670,26366,26369`). Two new
    restart regression tests
    (`internal/postmaster/database_ddl_type_acl_domain_reload_test.go`):
    `TestDatabaseDDLTypeGrantOnTypeNonDefaultDBReload` and
    `TestDatabaseDDLTypeDropDomainNonDefaultDBReload`, each verified by
    temporarily stashing the fix — the GRANT case failed with a duplicate
    pg_type row (2 rows, want 1: the ACL resync's xmax stamp missed the
    right heap while the already-fixed insert landed a new row there,
    leaving both live) and the DROP case failed with a surviving "ghost" row
    (1 row, want 0: the xmax stamp had nothing to compensate for). **Item 1
    (pg_range) intentionally NOT fixed this loop**, and pgRangeRel is
    unchanged from `catalog.DefaultDBOid` — before writing its own test,
    checked whether `pgRangeRel`'s write-side fix is safe alone the way item
    2's fixes were (their read side, `loadSystemCatalogsIfPresentForDB`, was
    already per-database from M0143-0002e/f) and found it is NOT:
    `reloadUserRangeTypesFromHeap` (`catalog_heap_reload.go:1685`) and
    `RegisterRangeTypeDuringRecovery` both key exclusively off `cat.DBOID()`
    with no per-database loop, so a write-only fix would make a non-default
    database's range type silently lose its pg_range row on the very next
    restart — trading today's cosmetic wrong-file placement for actual data
    loss. Filed as **M0143-0002h** below with a `.ralph/deferral_ledger.md`
    row (dated 2026-09-17) recording the full read-side gap and resume
    point. Gates: `go build ./...` clean; `go test
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...
    ./internal/catalog/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green;
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 ERROR=0
    TIMEOUT=0, plan-shapes 99/99 identical. `tpch-spotcheck` not run — no
    TPC-H data needed, consistent with the P0-E6-wait selection rule.
- [x] **M0143-0002h — pg_range needs a paired write+read per-database fix,
  not the one-line swap M0143-0002g's own text assumed.**
  Parent: M0143-0002d. Filed 2026-09-17 from M0143-0002g's Done note.
  `pgRangeRel` (`internal/executor/sys_pg_range.go:54-59`) still hardcodes
  `catalog.DefaultDBOid`, but unlike the pg_type/pg_class/pg_attribute sites
  M0143-0002e/f fixed, there is NO existing per-database read-side loop for
  pg_range to land the write fix against:
  `reloadUserRangeTypesFromHeap` (`internal/initdb/catalog_heap_reload.go:1685`)
  scans only `cat.DBOID()`'s pg_range+pg_type heaps in a single unconditional
  pass (called once from `open.go:2272`, not looped over
  `cat.ListDatabases()` the way `loadUserTablesFromHeapForDB`/
  `loadSystemCatalogsIfPresentForDB` are at `open.go:1564,1586`), and
  `RegisterRangeTypeDuringRecovery` (`catalog_heap_reload.go:1756`)
  hardcodes `DBOid: cat.DBOID()` on every reloaded `RangeType` regardless of
  which database's heap the row logically came from. Landing the write-side
  swap alone would therefore REGRESS restart durability: a range type
  created in a non-default database would have its pg_range row written to
  that database's own heap file, but the reload pass would never scan that
  file, so the row (and hence `rngsubtype`) would silently vanish on the
  next restart — worse than today's cosmetic-but-durable wrong-file
  placement. Fix (land together, one commit): (1) give `pgRangeRel` the
  same `tableCatalogHeapDBOid(ctx)` swap; (2) extend
  `reloadUserRangeTypesFromHeap` into a per-database loop mirroring
  `loadSystemCatalogsIfPresentForDB` (`open.go:1586`) — for each
  distinct-dbOid database in `cat.ListDatabases()`, re-run the scan against
  that database's own pg_range+pg_type heaps and pass its real dbOid into
  `RegisterRangeTypeDuringRecovery` instead of `cat.DBOID()`; (3) extend
  `TestDatabaseDDLTypeCatalogReloadAcrossRestart`'s range assertion from
  pg_type-only to the `rngsubtype` join it currently avoids (join `pg_range`
  to confirm `r1`'s samerange resolves subtype `integer` and `r2`'s
  resolves `bigint` post-restart, isolated per database). Ledger:
  `M0143-0002g` row dated 2026-09-17.
  - **Done 2026-09-17.** Landed all three parts in one commit, as required
    (write-before-read alone would have repeated the exact bug this task
    exists to fix). (1) `pgRangeRel` (`internal/executor/sys_pg_range.go:54`)
    swapped `catalog.DefaultDBOid` → `tableCatalogHeapDBOid(ctx)`. (2)
    `reloadUserRangeTypesFromHeap`
    (`internal/initdb/catalog_heap_reload.go:1680`) gained a `heapDBOid,
    nsDBOid uint32` parameter pair (mirroring
    `loadSystemCatalogsIfPresentForDB`'s split): its pg_range/pg_type
    `RelFileNode`s now read `heapDBOid` instead of `cat.DBOID()`, and the
    `RegisterRangeTypeDuringRecovery` call now stamps `nsDBOid` instead of
    `cat.DBOID()` — `catalog.go`'s `rangeKey(dbOid, name)` registry already
    supported per-DB keys, so no catalog.go change was needed, only the
    caller. `internal/initdb/open.go:2272`'s call site keeps the historical
    `cat.DBOID(), cat.DBOID()` main pass and adds a new loop over
    `cat.ListDatabases()` (skipping oid 0/DefaultDBOid/PostgresDBOid/
    `cat.DBOID()`) immediately after it — placed after `reloadDatabasesFromHeap`
    (line 1546) so, unlike M0143-0002f's own read-side loop, there was no
    ordering hazard to rediscover. (3) Extended
    `TestDatabaseDDLTypeCatalogReloadAcrossRestart`
    (`internal/postmaster/database_ddl_type_reload_test.go`) with a
    `rangeSubtypeOf` helper joining `pg_type`→`pg_range` on `rngtypid`,
    asserting `r1`'s samerange resolves `integer` and `r2`'s resolves
    `bigint` post-restart. Verified pre-fix failure by stashing the two
    production files (`sys_pg_range.go`, `catalog_heap_reload.go`,
    `open.go`) and re-running: failed with "expected exactly 1 samerange
    pg_range row, got 0" for db r1 — the exact predicted silent-loss
    symptom — then restored the fix and re-ran green. Gates: `go build
    ./...` clean; `go test ./internal/postmaster/... ./internal/executor/...
    ./internal/initdb/... ./internal/catalog/...` PASS;
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full
    green; `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
    ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS against
    the staged tree; `python3 scripts/ralph-lineage-guard.py` exit 0.
    `tpch-spotcheck` not run — no TPC-H data needed for this unit-scoped
    fix, consistent with the P0-E6-wait selection rule. This closes the
    M0143-0002 lineage's pg_range gap; the `M0143-0002g` deferral-ledger row
    stays `-` (status flips are M0119's job, not the filer's).
- [x] **M0143-0003 — `pg_constraint` returns 0 rows of any contype after a restart** —
  including the `'p'`/`'u'` rows synthesised from indexes that demonstrably survive. A
  second, independent reload gap that R126 explicitly did not touch.
  - **Done 2026-09-17 (recon + decomposition; see
    `docs/design/0100-0149/m0143-0003-pg-constraint-reload-gap.md`).**
    `pg_constraint` is fully virtual, synthesised per-connection from FOUR
    independent in-memory sources (one per contype) — not one root cause.
    `f` (FK) was already fixed by R126. `p`/`u`/`x` (index-backed) all depend
    on `catalog.Index.IsConstraint`, never restored by
    `RegisterIndexDuringRecoveryForDB` (`internal/catalog/catalog.go:6595`) —
    every reloaded index vanished from `pg_constraint` regardless of type.
    Split into M0143-0003a (landed this loop) through M0143-0003e (filed
    below); parent ticked because the recon+decomposition milestone this
    task's text named is complete, mirroring M0143-0002d's own precedent.
- [x] **M0143-0003a — restore `Index.IsConstraint` for PRIMARY KEY on reload.**
  Parent: M0143-0003. `IsConstraint: primary` at `RegisterIndexDuringRecoveryForDB`'s
  `Index{}` construction (`internal/catalog/catalog.go:6595`) — lossless for
  PRIMARY KEY specifically (PG's `indisprimary` always implies a
  `pg_constraint` row; no "bare primary index" concept, unlike UNIQUE/EXCLUDE).
  - **Done 2026-09-17.** Verified pre-fix failure by temporarily reverting to
    `IsConstraint: false`: `TestDatabaseDDLReloadAcrossRestart`'s new PK
    assertions (added this loop, `internal/postmaster/database_ddl_reload_test.go`)
    failed with the exact predicted symptom (`pg_constraint PK rows = [],
    want exactly [parent_pkey]` for both `r1`/`r2`), restored the fix,
    re-ran green. Gates: `go build ./...` clean; `go test
    ./internal/catalog/... ./internal/postmaster/... ./internal/executor/...
    ./internal/initdb/...` PASS (postmaster 49s, initdb 131s). No TPC-H data
    needed for this unit-scoped fix, consistent with the P0-E6-wait
    selection rule.
- [x] **M0143-0003b — CHECK constraint durable persistence (write + reload).**
  Parent: M0143-0003. **Highest-severity remaining piece — this is an
  enforcement-loss correctness bug, not a display gap.**
  `catalog.Table.CheckConstraints`/`NamedChecks` are never written to any
  heap and never reloaded (confirmed by exhaustive grep: zero hits for
  `checkconstraints`/`namedchecks` in `internal/initdb/*.go` outside this
  citation). `copy.go`/`operators_fk.go`/`operators_storage.go` all gate
  CHECK enforcement on `len(tbl.CheckConstraints) > 0`, so **every CHECK
  constraint on every table silently stops being enforced after any server
  restart**, with no error at restart or at the first violating write.
  - **Done 2026-09-17.** Write path: `buildPGConstraintRowForTableCheck`/
    `writeCheckConstraintRow`/`stampCheckConstraintRows`
    (`internal/executor/sys_pg_constraint.go`), field-for-field matching the
    synthesised view's own CHECK projection (`catalog.go`'s
    `PGConstraintRowsForDBOid`). Rather than hand-wiring a new wrapper at
    each of the 9 `AddCheck*`/`AddCheckInherited` call sites found this loop
    (`operators_ddl.go:4319,4338,4345,4372,4405,5556,5582,9368,13106`), wired
    the write into the existing `syncTableToCatalogHeap`
    (write-all-current-checks loop, mirroring the FK loop) +
    `deleteCatalogRowsForOID` (`stampCheckConstraintRows` call, mirroring
    `stampForeignKeyConstraintRows`) funnel, then made sure every one of the
    9 sites actually reaches a resync: 5 sites in `execCreateTable` and 2 in
    `execCreatePartitionChild` fall into the SAME "mutated after the
    function's one early `syncTableToCatalogHeap` call" trap those
    functions' own comments already document for NOT NULL (M0134-0005y) —
    extended `execCreateTable`'s resync condition to
    `notNullHeapDirty || len(tbl.NamedChecks) > 0` and added an equivalent
    conditional resync block at the end of `execCreatePartitionChild` (which
    had none at all before this). The ALTER-ADD site (`:9368`) and the
    ADD-cascade-to-children site (`:13106`, inside
    `cascadeCheckToChildrenAt` — both the merge and the fresh-inherited
    branch) now call `o.syncConstraintCatalogRow` (an ALREADY-EXISTING
    delete-then-resync helper the DROP-cascade path was calling all along,
    just with nothing yet to persist). DROP CONSTRAINT: the cascade-to-child
    path (`:13153-13158`ish) already called `syncConstraintCatalogRow(child)`
    pre-existing; the TOP-LEVEL table's own truncation
    (`tbl.CheckConstraints`/`NamedChecks` splice, `:13422`ish) had NO resync
    at all — added `o.syncConstraintCatalogRow(tbl)` there. Reload:
    `loadCheckConstraintsFromHeapForDB`/`loadCheckConstraintsFromHeap`
    (`internal/initdb/catalog_heap_reload.go`) mirroring
    `loadForeignKeysFromHeapForDB`'s shape, wired in `open.go` right after
    `loadForeignKeysFromHeap`. Test: `TestDatabaseDDLReloadAcrossRestart`
    (`internal/postmaster/database_ddl_reload_test.go`) extended with a
    `gauge` table carrying `CONSTRAINT gauge_level_check CHECK (level >= 0)`;
    asserts the post-restart `pg_constraint` row AND actual enforcement (a
    satisfying `INSERT` succeeds, a violating one still fails). Verified
    live: temporarily no-op'd the `loadCheckConstraintsFromHeap` call and
    confirmed both new assertions fail with the exact predicted symptom
    (`pg_constraint CHECK rows = []`, violating INSERT unexpectedly
    succeeds), restored, re-ran green. Also had to widen the existing PK
    assertion (`gauge` is itself `id int4 PRIMARY KEY`, so the PK-row-count
    query now returns 2 rows, not 1) using an order-independent `conNames`
    helper. Residual found while auditing every `.NamedChecks` mutator for
    completeness: `VALIDATE CONSTRAINT`/`RENAME CONSTRAINT` (metadata-only,
    not enforcement) still don't resync — filed as a deferral-ledger row
    (2026-09-17), not a new fix_plan task (small, single-loop-sized follow-up
    named directly in the ledger's resume point).
    Gates: `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...`
    all PASS; `TestDatabaseDDLReloadAcrossRestart` re-verified with
    `-count=1` (not just cached); `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green;
    `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 ERROR=0
    TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS against the
    staged tree. No TPC-H data needed, consistent with the P0-E6-wait
    selection rule.
- [x] **M0143-0003c — UNIQUE (non-PRIMARY-KEY) constraint-backed index
  `IsConstraint` durability.**
  Parent: M0143-0003. Depends on: sequence after
  M0143-0003b (smaller diff if its write-path plumbing exists to reuse).
  Architecturally harder than 0003b: no durable signal today distinguishes
  `ALTER TABLE t ADD CONSTRAINT u UNIQUE (col)` from a bare `CREATE UNIQUE
  INDEX u ON t(col)` after a restart — real PG uses
  `pg_constraint.conindid`, which goopg does not persist for `contype IN
  ('u','x')`.
  - **Done 2026-09-18.** Resolved the schema decision in favor of option (a)
    (a `pg_constraint` heap row) over option (b) (a new `pg_index` bit): PG
    already solves this via `conindid`, so a heap row is the PG-faithful
    mechanism and reuses 0003b/0003d's exact funnel. Write:
    `buildPGConstraintRowForUnique`/`writeUniqueConstraintRow`/
    `stampUniqueConstraintRows` (`internal/executor/sys_pg_constraint.go`),
    `contype='u'`, `conindid=idx.OID`, `conkey` = key-column ordinals,
    `condeferrable`/`condeferred` sourced from `idx.Deferrable`/
    `idx.InitiallyDeferred` (closes an unrelated latent gap for free — neither
    field reloaded from anywhere before this). PRIMARY KEY excluded from the
    write loop (0003a already covers it). Wired into the same
    `syncTableToCatalogHeap`/`deleteCatalogRowsForOID` funnel. Resync
    coverage: since the constraint object here IS the index (no `Table`-owned
    list to length-check), added `tableHasUniqueConstraintIndex` (scans
    `IndexesOnTable` for `Unique && !Primary && IsConstraint`) as the
    equivalent dirty-trigger in `execCreateTable`'s and
    `execCreatePartitionChild`'s resync gates; the three ALTER-path sites that
    set `IsConstraint=true` outside those two functions
    (`execAlterTableAddUnique`'s build-new-index branch,
    `adoptExistingIndexAsConstraint`'s USING-INDEX branch, guarded to
    `!primary`) each gained a `syncConstraintCatalogRow` call, mirroring
    0003b's precedent for its own ALTER-path sites. Reload:
    `loadUniqueConstraintsFromHeap`/`loadUniqueConstraintsFromHeapForDB`
    (`internal/initdb/catalog_heap_reload.go`), wired in `open.go` right
    after `loadNotNullConstraintsFromHeap`; resolves `conindid` via a new
    `catalog.InMemory.LookupIndexByOIDAllDBs` (mirrors
    `LookupTableByOIDAllDBs`'s cross-database fallback). Test:
    `TestDatabaseDDLReloadAcrossRestart` extended — `gauge` now also carries
    `tag text CONSTRAINT gauge_tag_unique UNIQUE`; post-restart assertions
    check the `pg_constraint` row AND a behavior gated on `IsConstraint`
    (`RENAME CONSTRAINT`, which real PG/goopg both require
    `!Primary && Unique && IsConstraint` for) plus a genuine UNIQUE-violation
    INSERT. Verified live: temporarily no-op'd `loadUniqueConstraintsFromHeap`
    in `open.go` — failed with the predicted symptom (empty row, RENAME
    42704), restored, re-ran green. Found but deliberately NOT fixed
    (pre-existing, unrelated bug — ledgered): `execCreatePartitionChild`'s
    `poc.UniqueColumns` and `LIKE ... INCLUDING INDEXES` both clone/create a
    unique index without ever setting `IsConstraint=true`, even live,
    pre-restart.
    Gates: `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...`
    all PASS; `TestDatabaseDDLReloadAcrossRestart` green and via the
    temporary-revert probe (red as predicted); `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green; `scripts/tpcds-sf025-regression.sh
    sweep` PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical,
    gate-stamp PASS against the staged tree; `tpch-spotcheck.sh`
    SKIP-BLOCKED (expected — `:65433` still under the P0-E6 evidence hold),
    accepted via the standing G6 exception (ledger: P0-E7 remains the re-run
    owner); `python3 scripts/ralph-lineage-guard.py` exit 0. No TPC-H data
    needed for the change itself, consistent with the P0-E6-wait selection
    rule.
- [x] **M0143-0003d — NOT NULL constraint named-metadata durable
  persistence.**
  Parent: M0143-0003. Depends on: sequence after M0143-0003b
  (reuses its wrapper pattern). Lower severity than 0003b: `Column.NotNull`
  (actual enforcement) already reloads correctly via `pg_attribute.attnotnull`
  (`internal/initdb/open.go`'s `loadUserTablesFromHeapForDB`, `NotNull:
  ar.AttNotNull` — confirmed by grep, NOT a repeat of the CHECK gap). Only
  `catalog.Table.NotNullConstraints` (the PG18 named-constraint list used for
  `pg_constraint` `contype='n'` rows) is unreloaded — metadata only.
  - **Done 2026-09-18.** Write path: `buildPGConstraintRowForNotNull`/
    `writeNotNullConstraintRow`/`stampNotNullConstraintRows`
    (`internal/executor/sys_pg_constraint.go`), field-matched against the
    synthesised view's own NOT NULL projection (`catalog.go`'s
    `PGConstraintRowsForDBOid`): `conenforced` hardcoded true (PG has no NOT
    ENFORCED spelling for NOT NULL), `convalidated = !NotValid`, `conkey`
    carries the single column ordinal (unlike CHECK, real PG's NOT NULL
    `conkey` is non-null). Wired into the SAME `syncTableToCatalogHeap`
    write loop / `deleteCatalogRowsForOID` stamp funnel 0003b uses (added
    right after the CHECK loop/stamp call, same funnel, not a new one).
    Resync coverage: `execCreateTable` needed NO change — its pre-existing
    `notNullHeapDirty` flag (predates this task, added for
    `pg_attribute.attnotnull` sync per M0134-0005y) already fires on every
    `AddNotNull` and already triggers a full `syncTableToCatalogHeap`
    re-run, which now emits the NOT NULL rows for free.
    `execCreatePartitionChild` DID need a change: its 0003b-added resync
    block gated on `len(tbl.NamedChecks) > 0` only, but that function's own
    named-NOT-NULL block (parent-inherited + explicit `poc.NotNullColumns`)
    mutates `tbl.NotNullConstraints` via `AddNotNull` after the same early
    sync call — widened the condition to `len(tbl.NamedChecks) > 0 ||
    len(tbl.NotNullConstraints) > 0`. Reload:
    `loadNotNullConstraintsFromHeap`/`loadNotNullConstraintsFromHeapForDB`
    (`internal/initdb/catalog_heap_reload.go`), mirroring
    `loadCheckConstraintsFromHeapForDB`'s shape and reusing the FK loader's
    `fkAttnumsFromArrayText`/`fkColumnNames` helpers to decode `conkey` back
    to a column name; wired in `open.go` right after
    `loadCheckConstraintsFromHeap`. Test:
    `TestDatabaseDDLReloadAcrossRestart` extended — `gauge` now also
    carries `code text CONSTRAINT gauge_code_not_null NOT NULL`. Live
    finding: a `PRIMARY KEY` column (`gauge.id`) turns out to ALSO carry its
    own auto-named `<table>_<col>_not_null` constraint, so an unfiltered
    `contype='n'` scan picks up every table's PK column too — the
    assertion filters on `conrelid = 'gauge'::regclass` and expects both
    `gauge_code_not_null` and `gauge_id_not_null`. Verified live: temporarily
    no-op'd the `loadNotNullConstraintsFromHeap` call in `open.go`, re-ran
    with `-count=1` — failed with the predicted symptom
    (`pg_constraint NOT NULL rows = []`), restored, re-ran green.
    Gates: `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/postmaster/... ./internal/executor/... ./internal/initdb/...`
    all PASS (one flaky, unrelated `TestSimpleQueryBatchAbortUndoesEarlierCreateType`
    failure under full-package parallel run, reproduced identically on
    unmodified HEAD and passed clean on every isolated/solo re-run — not
    caused by this change); `TestDatabaseDDLReloadAcrossRestart` re-verified
    with `-count=1` both green and via the temporary-revert probe (red as
    predicted). No TPC-H data needed, consistent with the P0-E6-wait
    selection rule.
- [x] **M0143-0003e — EXCLUDE constraint `indisexclusion` durability.**
  Parent: M0143-0003. Depends on: M0143-0003c `[x]` (done 2026-09-18) — reuse
  its landed shape directly: a `contype='x'` sibling of
  `buildPGConstraintRowForUnique`/`writeUniqueConstraintRow`/
  `stampUniqueConstraintRows` (same `conindid`-keyed `pg_constraint` row,
  not a new `pg_index` bit — 0003c's design doc section records why that
  option was dropped), and a reload loader that sets `idx.IsExclusion = true`
  (mirroring `loadUniqueConstraintsFromHeapForDB`'s `idx.IsConstraint = true`)
  instead of a new schema/heap-format decision. `indisexclusion` is a declared
  `pg_index` heap column (`internal/initdb/initdb.go:4766`) but is never
  written by the real index-creation path (confirmed by grep: zero
  non-comment hits for `indisexclusion`/`IndIsExclusion` outside catalog
  schema declarations and an unrelated hardcoded-false synthetic row,
  `internal/executor/pg18_user_catalog_rows.go:1460`) and never decoded on
  reload — `Index.IsExclusion` is lost on every restart, taking `x`-contype
  `pg_constraint` rows and `deferred_exclusion.go`'s deferred-exclusion-check
  machinery with it for any EXCLUDE constraint surviving a restart.
  - **Done 2026-09-18.** `buildPGConstraintRowForExclude`/
    `writeExclusionConstraintRow`/`stampExclusionConstraintRows`
    (`internal/executor/sys_pg_constraint.go`) mirror 0003c's UNIQUE shape
    exactly (`contype='x'`, `conindid=idx.OID`), but the write-loop gate in
    `syncTableToCatalogHeap` is `idx.IsExclusion` alone, not `idx.IsConstraint
    && idx.Unique` — the non-btree-equality EXCLUDE path
    (`createExclusionIndexStub`, e.g. `EXCLUDE USING gist (c WITH &&)`) sets
    neither, yet real PG still creates a `pg_constraint` row for it (the
    synthesised view already emits on this same OR condition,
    `catalog.go:7242`). New `tableHasExclusionConstraintIndex` widens the
    `execCreateTable`/`execCreatePartitionChild` resync-dirty triggers
    alongside `tableHasUniqueConstraintIndex`; `execAlterTableAddExclude`
    gained `syncConstraintCatalogRow` calls on BOTH branches (neither called
    it before — the btree-equality branch had no durability call at all, and
    the stub branch's own `createExclusionIndexStub` → `syncIndexToCatalogHeap`
    writes pg_class/pg_index/pg_attribute for the index relation only, never
    pg_constraint). Reload: `loadExclusionConstraintsFromHeap`/
    `loadExclusionConstraintsFromHeapForDB`
    (`internal/initdb/catalog_heap_reload.go`), wired in `open.go` right
    after `loadUniqueConstraintsFromHeap`. Unlike 0003c, restoring the flag
    alone would not be enough for enforcement to survive: `idx.ExclusionOp`
    ("=" or "&&") gates `checkExclusionConstraintsForInsert`'s switch
    (`operators_storage.go:8808`, no default arm), so an empty `ExclusionOp`
    post-reload would silently disable the check even with `IsExclusion=true`
    restored. `ExclusionOp` has no natural `pg_constraint` column (`conexclop`
    is a real `oid[]` goopg does not resolve operators into), so it rides the
    otherwise-NULL `conbin` column as raw text — the same smuggling
    convention `sys_pg_constraint.go`'s header comment already documents for
    CHECK/domain `adbin`; real PG never reads `conbin` for an `x` row. Test:
    `TestDatabaseDDLReloadAcrossRestart` extended with `gauge` gaining a
    `zone int4` column and `CONSTRAINT gauge_zone_excl EXCLUDE USING btree
    (zone WITH =)`. Verified live: temporarily no-op'd
    `loadExclusionConstraintsFromHeap` in `open.go`, re-ran — failed with
    BOTH predicted symptoms at once (empty `pg_constraint` row AND a
    duplicate-key INSERT that should raise `23P01` succeeding silently),
    restored, re-ran green. Gates: `go build ./...` clean; `go vet` (targeted
    packages) clean; `go test ./internal/catalog/... ./internal/postmaster/...
    ./internal/executor/... ./internal/initdb/...` all PASS (one
    `TestSimpleQueryBatchAbortUndoesEarlierCreateTable` failure under
    full-package run, reproduced as flaky — passes solo and passes on a
    second full-package run, unrelated real-TCP-socket timing test, not this
    change); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
    full green; `FORCE=1 scripts/tpcds-sf025-regression.sh sweep` PASS=96
    MISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes 99/99 identical, gate-stamp PASS
    against the staged tree; `tpch-spotcheck.sh` SKIP-BLOCKED (expected —
    `:65433` still under the P0-E6 evidence hold), gate stamp refreshed
    against the staged tree, commit accepted via the documented
    `ledger:`/SKIP-BLOCKED exception (P0-E7 is the re-run owner);
    `make ralph-state-guard` clean (auto-repaired a stale `status`/`progress`
    mismatch from the previous loop's exit, unrelated to this change).
    Residual found while auditing every DROP-CONSTRAINT call site for where
    the new sync calls belonged: the UNIQUE/EXCLUDE branches of
    `execAlterTableDropConstraint` never remove the dropped index's own
    `pg_class`/`pg_index` heap rows, so it should resurrect after a restart —
    out of scope here (indexes that should NOT survive a restart, not
    indexes that should). **Not filed as a new fix_plan task**:
    `scripts/ralph-lineage-guard.py` rejected the commit with a new
    M0143-0003g descendant, since M0143-0003's last 5 completed descendants
    (0003a-e) all carry `Movement: none` and the lineage budget is exhausted
    — recorded ledger-only (`.ralph/deferral_ledger.md`, 2026-09-18
    M0143-0003e row) per the guard's own remedy instead of a fix_plan item.
- [x] **M0143-0003f — `poc.UniqueColumns`/`LIKE ... INCLUDING INDEXES` never
  set `IsConstraint` (live bug, not restart-related).**
  Parent: M0143-0003. Filed 2026-09-18, discovered while researching
  M0143-0003c's write-loop call sites (deferral-ledger row dated 2026-09-18).
  `execCreatePartitionChild`'s `PARTITION OF ... (col UNIQUE)` inline-column
  path (`poc.UniqueColumns`) and the `LIKE ... INCLUDING INDEXES` unique-index
  clone both call `createBTreeIndex` but never set `idx.IsConstraint = true`
  afterward, unlike every sibling UNIQUE-constraint path (inline column,
  table-level, named, PK auto-index). This is a LIVE bug — the resulting index
  never shows up in `pg_constraint` even before any restart — not a repeat of
  M0143-0003c's restart-durability gap. Fix: add `idx.IsConstraint = true`
  (plus a `syncConstraintCatalogRow` call, now that M0143-0003c's write path
  exists) right after both `createBTreeIndex` calls; add a regression test
  (`PARTITION OF ... (col UNIQUE)` and `LIKE ... INCLUDING INDEXES` each
  asserting the resulting index appears in `pg_constraint` with `contype='u'`).
  - **Done 2026-09-18.**
    Movement: none — real correctness work, not S3 movement (owner
    reclassification 2026-09-18). What it achieved: two independent CREATE-TABLE-time UNIQUE-index creation
    paths (`operators_ddl.go`'s `likeUniqueIndexes` loop and
    `execCreatePartitionChild`'s `poc.UniqueColumns` loop) now populate
    `pg_constraint` with a `contype='u'` row, matching every sibling
    UNIQUE-constraint path, on a table that never restarts — a genuinely new
    class of bug from M0143-0003a-e's restart-reload gaps, not a repeat.
    No explicit `syncConstraintCatalogRow` call turned out to be needed: both
    call sites already sit inside `execCreateTable`/`execCreatePartitionChild`
    ahead of an existing tail-of-function resync
    (`tableHasUniqueConstraintIndex(...)`, landed by M0143-0003c) that scans
    `IndexesOnTable` directly rather than a fixed call-site list — setting
    `idx.IsConstraint = true` right after `createBTreeIndex` was sufficient to
    make that pre-existing gate fire and write the row via
    `syncTableToCatalogHeap`. Two new regression tests in
    `internal/executor/operators_ddl_like_indexes_test.go`:
    `TestLikeIncludingIndexesMarksUniqueAsConstraint` (LIKE ... INCLUDING
    INDEXES with a source `UNIQUE` column) and
    `TestPartitionOfInlineUniqueMarksAsConstraint` (`PARTITION OF ... (col
    UNIQUE)`), both asserting `idx.IsConstraint == true` on the cloned index
    (consistent with this file's existing `TestLikeIncludingIndexesCopiesPKDeferrable`
    style — an in-memory catalog assertion, not a live `pg_constraint` SELECT,
    since `newDDLFixture` is the same fixture already used there).
    Gates: `go build ./...` clean; `go test ./internal/executor/...` PASS
    (12.4s); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
    full green (all packages, including `internal/initdb` at 122.7s and
    `cmd/goopg` at 24.0s); no TPC-H data needed, consistent with the
    P0-E6-wait selection rule (this is a unit-scoped CREATE-TABLE-time fix,
    no planner/executor row-count surface). `python3
    scripts/ralph-lineage-guard.py` not implicated — M0143-0003f already
    existed as an open `[ ]` task at HEAD (filed by the previous loop), so
    flipping it to `[x]` adds no new descendant under M0143-0003.
- [x] **M0143-0004 — `PhysicalTypeIsVarlena` has no `IsArray` arm**
  (`physical_align.go:85-107`) — latent for ordinary user `int4[]` columns, not just
  catalogs.
  - **Done 2026-09-18.** Added an `if t.IsArray { return true }` short-circuit at
    the top of `PhysicalTypeIsVarlena` (`internal/catalog/physical_align.go`),
    mirroring the `IsArray` arm `PhysicalTypeAlign` already had. A user array
    column carries `Type{Name:<element>, IsArray:true}` (Name = element type,
    DU-002 slice 62); every element name in the switch's non-default arms
    (`int4`, `int2`, `int8`, `bool`, `oid`, `float4/8`, `date`, `timestamp[tz]`,
    `uuid`, `name`, `xid`, …) is fixed-width on its own, so without the arm an
    `int4[]`/`bool[]`/`date[]`/… column was misclassified as NOT varlena even
    though every array is a varlena `ArrayType` blob on disk. Since this
    function is the single source of truth shared by the heap codec, pgoutput's
    walker, and the catalog statistic paths (comment at `physical_align.go:72-77`),
    one fix covers all three; `sys_pg_constraint.go:141-163` had already
    hand-documented this exact gap and worked around it for `pg_constraint`'s
    `conkey`/`confkey` by declaring them WITHOUT `IsArray` — that workaround
    comment can stay (still true and still needed: those two columns are still
    NOT `IsArray`), it just stops being the only mitigation in the tree.
    **Confirmed live, not just by code reading**: built both the pre-fix and
    post-fix binaries, started throwaway `init`+`start` clusters on 5533/5534,
    `CREATE TABLE onlyarr (tags int4[]); INSERT INTO onlyarr VALUES
    (ARRAY[1,2,3]);`, then parsed the raw heap page bytes — the buggy binary
    wrote `t_infomask=0x0800` (`HEAP_HASVARWIDTH` UNSET) on a row whose only
    column is a non-null `int4[]`; the fixed binary correctly writes
    `0x0802` (SET). This is precisely the `nocachegetattr` fast-path hazard
    `sys_pg_constraint.go:158-163` and `codec.go:1550-1557` describe: a real
    PG18 (or any PG-faithful decoder honoring the infomask) walking this row
    would use the fixed-prefix `attcacheoff` shortcut and either skip past or
    misread the array, corrupting it and every following column.
    Also checked (and found harmless by construction, so no code change
    needed there): `encodeArrayValuePGCtx` (`codec_array.go:104-113`) always
    writes the 4-byte/long-form varlena header (`total<<2`, low 2 bits always
    0), so `isShortVarlenaHeader` is always false for arrays regardless of
    this bug — `packableShortColumn`'s encode-side alignment decision for
    array columns was already unaffected, and `AttAlignPointer`'s decode-side
    peek vs. force-align both land on the identical offset whenever the
    writer always force-aligns (proved by cases: pre-aligned offset — peek
    and force agree trivially; offset needing padding — the byte the peek
    inspects IS the real zero pad byte the encoder wrote). Verified this
    equivalence live too: byte-identical relation file sizes (139264 and
    344064 bytes respectively) between the two binaries for both an
    `int4[]`-as-last-column table and a `bool` + `int4[]` table across 2000
    and 5000 rows. New test: `TestPgRowHasVarWidthDetectsVarlenaCols`
    (`internal/executor/pg18_user_catalog_rows_test.go`) gained an
    `{Name:"int4", IsArray:true}` case pinning the exact infomask bug (the
    pre-existing case there used `{Name:"text[]"}`, a catalog-form Name that
    was never in the fixed-width switch and so never exercised the missing
    arm); `TestPhysicalTypeIsVarlenaArray`
    (`internal/catalog/physical_align_test.go`) pins the helper directly for
    every fixed-width element name, both array and scalar form. Gates:
    `go build ./...` clean; `go test ./internal/catalog/...
    ./internal/executor/... ./internal/access/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` full green; `scripts/tpcds-sf025-regression.sh
    sweep` `PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0`, plan-shapes 99/99 identical;
    `tpch-spotcheck.sh` SKIP-BLOCKED (exit 3) by the `:65433` P0-E6 evidence
    hold — ledger: P0-E7 is the re-run owner, same standing exception as
    M0143-0003b/c/d/e/f.
- [x] **M0143-0005 — `ParamRef` LIMIT + DISTINCT returns wrong rows** — R83 fixed the
  `IntegerConst` case and pinned it; the `ParamRef` allowlist was deliberately not
  extended. Fail-closed with zero corpus impact today, which is exactly why it stays
  invisible until someone writes the case.
  - **Already resolved before filing; closed 2026-09-18 without new code.**
    This item duplicates M0137-0010 open problem C1
    (`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/02-open-problems.md`),
    which commit `d03f0a656` ("optimizer,scripts(M0137-0010): qual-placement
    census + close C1 (ParamRef LIMIT+DISTINCT)", 2026-09-15) already closed:
    `limitBoundMovable` (`internal/optimizer/tuplefraction.go:116-125`) accepts
    `*ParamRef` alongside `*IntegerConst`, and
    `TestDistinctLimitAppliesAboveDistinct_ParamRef`
    (`internal/executor/distinct_limit_paramref_test.go`) pins the exact
    150×'a'+50×'b' `SELECT DISTINCT v FROM dlp ORDER BY v LIMIT $1` case this
    item describes. Verified this loop: `go test ./internal/executor/ -run
    TestDistinctLimitAppliesAboveDistinct_ParamRef -v` PASSES at HEAD with no
    code change. M0143-0005 was filed three days after d03f0a656 landed
    without checking whether the C1 closure already covered it — no ledger
    row (nothing was deferred; the fix was already complete, only the
    fix_plan checkbox was stale).
- [x] **M0143-0006 — triage `internal/parser`'s 60 failing tests** — pre-existing,
  verified unrelated to R126, and unowned. Fix them or convert them into filed, owned
  tasks; "unowned" is not an end state.
  - **Done 2026-09-17.** Root cause: `dc91bd6b7` ("fix(planner): preserve
    grouped USING bindings for lateral", 2026-09-13) added a new field,
    `RangeVar.GroupedJoinUnaliased` (`internal/parser/ast.go:676`, set by
    `internal/parser/support.go:169`), which changed every `RangeVar`'s
    `dumpStmts` text. The checked-in oracle,
    `internal/parser/testdata/parity_goldens.txt` (1679 pinned statements,
    `internal/parser/goldens_test.go`), was never regenerated after that
    commit, so all 454 golden entries containing a `RangeVar` drifted —
    `TestParityGoldensAreCurrent` plus every `assertParity`-based test whose
    corpus happened to touch a `FROM` clause (60 test funcs; the other
    ~1200 golden entries with no `RangeVar` were unaffected, which is why
    some `_test.go` files in the package still passed). Not a parser
    regression: verified programmatically (diff every changed golden pair,
    strip the `GroupedJoinUnaliased=<bool>` token, assert byte-identical)
    that all 454 changed lines differ **only** by the new field's presence —
    zero other AST divergence. Fix: `GOOPG_UPDATE_GOLDENS=1 go test
    ./internal/parser/` to regenerate, reviewed the diff per the above,
    committed the refreshed `parity_goldens.txt` (no `.go` files changed).
    Verified: `go test ./internal/parser/...` 0 failures (was 60);
    `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full green.
    Coverage gap noted, not blocking: the corpus has zero statements
    exercising `GroupedJoinUnaliased=true` (`grep -c
    'GroupedJoinUnaliased=true' parity_goldens.txt` = 0) — the synthetic
    unaliased parenthesized-JOIN case R104 introduced has no golden pin of
    its own. Left as a coverage note rather than a new task since it is a
    missing-test gap, not a divergence.

- [x] **M0143-0008 — `ALTER TABLE` subcommands have no rollback-undo; a
  `ROLLBACK` after e.g. `ADD CONSTRAINT` silently leaves it permanently
  committed** — filed by M0142-0003h. `docs/design/0000-0049/0030-0006-transactional-ddl.md`
  Phase 1 wired rollback-undo (`DDLUndoEntry`/`RecordDDLCreate`,
  `session.go`; consumed by `execRollback` in `operators_tx.go:253`) for
  exactly two DDL forms — `CREATE TABLE` and `CREATE INDEX` (six call
  sites total, all in `operators_ddl.go`). Every other DDL form, confirmed
  live for `ALTER TABLE ADD CONSTRAINT` (`syncConstraintCatalogRow`,
  `operators_ddl.go:12912`) via a two-session repro on a throwaway cluster:
  `BEGIN; ALTER TABLE t ADD CONSTRAINT ...; ROLLBACK;` leaves the
  constraint permanently in `pg_constraint` — `ROLLBACK` reports success
  but does nothing. This is a correctness bug (silent, permanent wrong
  catalog state), not merely a documented-and-bounded limitation like the
  separate "concurrent DDL visibility deferred" gap (also confirmed live in
  the same repro, but pre-existing/known — do not conflate the two, see
  the -0003h design doc). Resume point: extend the `DDLUndoEntry`
  undo-list pattern (or design a small generalized "catalog mutation undo"
  record, since `ALTER TABLE` forms vary widely — ADD/DROP CONSTRAINT, ADD
  COLUMN, SET DEFAULT, ...) to cover at minimum `ADD CONSTRAINT`/`DROP
  CONSTRAINT`; likely overlaps `M0143-0002`'s `DROP CONSTRAINT` FK bug
  (same subsystem, `execAlterTableDropConstraint`,
  `operators_ddl.go:13259`) — triage both together. Test precedent:
  `transactional_ddl_test.go`'s `TestTransactionalCreateTableRollback` et
  al.; add an `ALTER TABLE ADD CONSTRAINT` analogue that fails before the
  fix.
  - **Done 2026-09-17 (landed as part of P0-E5).** `ADD CONSTRAINT ...
    {PRIMARY KEY|UNIQUE} USING INDEX` (`AlterIndexUndoEntry`/
    `NotNullUndoEntry`) and `DROP CONSTRAINT`'s three index-backed forms
    (PK/UNIQUE/EXCLUDE, via the existing `DDLDropUndoEntry`/`RestoreIndex`
    mechanism) now undo on ROLLBACK — this is the "at minimum ADD
    CONSTRAINT/DROP CONSTRAINT" bar this task's own text set. See P0-E5's
    entry above and `docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md`
    §"P0-E5 — the fix" for the full mutation-mapping/design rationale.
    Remaining `DROP CONSTRAINT` forms (CHECK/FOREIGN KEY/NOT NULL — in-place
    slice/field mutations, not map-removals, so they need their own
    snapshot/restore rather than reusing `DDLDropUndoEntry`) are **not**
    covered — filed as **M0143-0008b** below; also the column-list `ADD
    CONSTRAINT ... PRIMARY KEY (cols)` form's brand-new-index creation
    already gets undone by the existing CREATE-INDEX `DDLUndoEntry` path
    (unaffected/unchanged by this task), but was not itself re-verified with
    a dedicated test this loop.
- [x] **M0143-0008b — `ALTER TABLE DROP CONSTRAINT` on CHECK/FOREIGN
  KEY/NOT NULL still has no rollback-undo.**
  Parent: M0143-0008. Filed
  2026-09-17 when P0-E5 landed undo for `ADD CONSTRAINT`/`DROP CONSTRAINT`'s
  index-backed forms (PK/UNIQUE/EXCLUDE) but explicitly scoped out these
  three: `execAlterTableDropConstraint`'s CHECK branch splices
  `tbl.CheckConstraints`/`tbl.NamedChecks` in place (and cascades via
  `cascadeCheckDropToChildren`); its FOREIGN KEY branch splices
  `tbl.ForeignKeys` in place (`catalog.InMemory.DropForeignKeyConstraint`);
  its NOT NULL branch (`clearNotNullConstraint`) sets
  `tbl.Columns[i].NotNull = false` and filters `tbl.NotNullConstraints` in
  place (and cascades via `cascadeNotNullDropToChildren`) — all three are
  real field/slice mutations on a pointer that stays live and reachable
  throughout, not the map-removal-only shape `DDLDropUndoEntry`/
  `RestoreIndex` already handles for the index-backed forms, so they need
  their own snapshot-and-restore (the same pattern P0-E5's
  `NotNullUndoEntry` already established for the ADD-direction NOT NULL
  synthesis — a DROP-direction sibling snapshotting `CheckConstraints`/
  `NamedChecks`/`ForeignKeys`/`NotNullConstraints`/`Columns[i].NotNull`
  wholesale before the mutation, restoring the whole slice/value on
  ROLLBACK, applied to the target table plus every cascade-reachable child
  — mirror `collectNotNullCascadeClosure`'s read-only pre-walk pattern,
  `operators_ddl.go`, added by P0-E5). Likely overlaps `M0143-0002`'s
  `DROP CONSTRAINT` FK bug (same subsystem) — triage together. Test
  precedent: `internal/testport/p0e5_alter_rollback_undo_test.go` (P0-E5).
  - **Done 2026-09-17.** `DropConstraintUndoEntry` (session.go) landed
    exactly as scoped: one wholesale snapshot type + `RecordDropConstraintUndo`/
    `TakePendingDropConstraintUndos`, a `snapshotDropConstraintState` helper
    (operators_ddl.go), recording calls at all three
    `execAlterTableDropConstraint` branches (CHECK and NOT NULL also
    snapshot `collectNotNullCascadeClosure(im, tbl)`'s children; FOREIGN KEY
    has no cascade, tbl only), and a restore block in `ProcessRollbackUndos`
    (operators_tx.go). 3 new testport cases
    (`internal/testport/p0e5b_alter_drop_constraint_rollback_undo_test.go`),
    each verified to genuinely fail with the fix reverted (git-stashed) and
    pass with it applied. **Live discovery while writing the FK sub-test:**
    confirmed M0143-0002 (`DropForeignKeyConstraint` hardcodes
    `DefaultDBOid`) live — on a non-default DB a plain COMMITted (not even
    ROLLBACKed) `DROP CONSTRAINT` on an FK silently no-ops, so the FK
    sub-test runs against the cluster's default-db handle instead (see
    ledger row `M0143-0008b`, dated 2026-09-17, for the full evidence and
    the re-verify-on-db-"r" resume point once M0143-0002 lands). Gates:
    `go build ./...` clean; `go test ./internal/executor/... ./internal/catalog/...`
    PASS; `go test -v -run 'TestPort_P0E4|TestPort_P0E5|TestPort_M0143_0008b'
    ./internal/testport/` PASS 8/8; `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` — all packages PASS except the
    pre-existing, already-documented `internal/parser` GroupedJoinUnaliased
    AST-drift (unrelated, no parser file touched); `scripts/tpcds-sf025-regression.sh
    sweep` — `PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0`, plan-shapes 99/99
    identical.
- [x] **M0143-0007 — separate the dimension-table `relpages` divergence (K41)** —
  `customer` 1,979 pages vs PG's 2,872, `item` 716 vs 1,284. M0140-0005 filed it
  as out of planner reach and that is correct — **but `relpages` is an input to
  every page-priced cost term and to `compute_parallel_worker`'s size ladder, so
  no cost-model change can ever correct it.** The leading candidate is R23's
  `character(N)` blank-padding, a real PG-compat defect that shifts `relpages` on
  every `bpchar` table; R22 (per-page free-space comparison) separates the two.
  Storage work, not planner work, which is why it belongs here. Ledger:
  `m0140-0005-nonplanner-heap-density-floor`.
  - **Done 2026-09-18.** Direct per-page free-space walk (raw `pd_lower`/
    `pd_upper` parse, both engines share PG18's byte-identical page header —
    `internal/storage/page.go`) on `customer`/`item` at TPC-DS SF0.25, no
    server start needed (read the on-disk relation files directly; PG side
    cross-checked live against the already-running read-only `:65438`
    oracle's `pg_class.relpages`/`reltuples`, SELECT-only). Result: goopg's
    per-page free space is SMALLER than PG's on both tables (82.8B vs
    117.3B customer; 173.4B vs 255.5B item — goopg packs pages MORE
    tightly, ruling out R22/fill in this direction), while the page-count
    ratio and the used-bytes ratio are nearly identical (0.689/0.696
    customer; 0.558/0.584 item) — the entire K41 gap is tuple width, not
    page fill. Cross-checked by an independent TSV-column-length estimate
    (67.9/229.3 B/row) agreeing with the page-derived deltas (69.8/226.2
    B/row) to a few percent. **R23 (`character(N)` blank-padding) confirmed
    as ~full explanation; R22 ruled out** for these two tables. Full
    writeup: `docs/design/0100-0149/m0143-0007-relpages-bpchar-padding-confirmed.md`.
    Recon/measurement only, no production diff — implementing R23 itself is
    filed as **M0143-0007b** below since it reverses a documented,
    load-bearing design convention (trimmed bpchar storage,
    `internal/catalog/bpchar.go`) across multiple sibling paths. Ledger:
    `.ralph/deferral_ledger.md`, row dated 2026-09-18.
- [x] **M0143-0007b — implement (or owner-decline) R23 `character(N)` on-disk
  blank-padding.**
  Parent: M0143-0007. M0143-0007 confirmed PG pads `bpchar`
  storage to its declared width and goopg does not, and that this fully
  explains the K41 `relpages` gap on `customer`/`item`. Closing the gap for
  real means storing `bpchar` padded — but goopg's trimmed convention is
  explicit, documented, load-bearing design (`internal/catalog/bpchar.go`,
  "M0103-0007 rung 24"; `compareDatum`'s padding-insensitive bpchar equality
  and `codec.go`'s `coerceTextLikeDatum` both rest on it;
  `bpchar_declared_width_test.go:78-86` states explicitly why padding on
  decode instead would be wrong). Before writing code: get an owner decision
  on whether to reverse the trimmed-storage convention at all (it is a
  genuine on-disk PG-compat defect per the project's absolute-compatibility
  rule, but reversing it touches heap comparisons, `internal/access/nbtree`
  key comparators, and WAL `pgoutput` encoding — every sibling boundary that
  currently assumes trimmed storage — plus TOAST thresholds/index key
  sizes/WAL record sizes store-wide). If approved: design doc first (own
  `docs/design/<id>-*.md`), then land per-boundary with the sibling-path
  audit this project's practice card requires, each slice gated by its own
  regress run plus the sf025 sweep. Ledger:
  `.ralph/deferral_ledger.md`, row dated 2026-09-18 (task M0143-0007).
  - **DESIGN DONE 2026-09-21 (loop 52), no production change.** Design doc
    `docs/design/0100-0149/m0143-0007b-bpchar-padded-storage.md`, written as
    the task's own text requires before any code.
    - **Boundary inventory measured against the tree, and TWO of the
      boundaries this task names do NOT need changing:**
      - `PGCompareBpcharC` (`internal/access/nbtree/pgcompare_types.go`)
        already strips trailing blanks via `bcTruelen` — upstream's own
        `bpcharcmp` rule — so it is correct under EITHER convention.
      - `catalog.PadBpchar` pads only a SHORT value, so its four render
        callers (DataRow, COPY text, COPY binary, `octet_length`) become
        idempotent no-ops rather than needing removal.
    - **What genuinely changes**: one storage decision
      (`coerceTextLikeDatum`, `internal/executor/codec.go`: trim → pad,
      keeping the unbounded-typmod arm intact) plus the SIZE consequences of
      physically larger values — index max-key-size and TOAST threshold.
    - **Compatibility property that makes incremental landing safe**: old
      trimmed and new padded data both read correctly, because comparison
      ignores trailing blanks on both sides and rendering re-pads only short
      values. No migration step is required, and `relpages` parity
      materialises only for NEWLY WRITTEN data — the honest scope of the fix.
    - **Four-slice plan**, each gated by its own regress run plus the SF0.25
      sweep: (1) the storage flip; (2) size consequences (index max-key-size,
      TOAST threshold), where a previously-accepted value can start erroring;
      (3) the `pgoutput` path — verify it derives from the stored datum, since
      no pgoutput site calls `PadBpchar`; (4) re-measure `relpages` on
      `customer`/`item`, the only way K41's gap can be shown closed.
    - **Slice 1 is the next unit of work.** Its sibling audit: re-verify the
      four `PadBpchar` callers as no-ops and do NOT remove them — removing
      them would break reads of pre-existing trimmed data. Ledgered.
    - **SLICE 1 LANDED 2026-09-22 (loop 53).** `coerceTextLikeDatum` pads
      instead of trimming, keeping upstream's strip-then-error-then-pad order
      and the unbounded-typmod arm. The K41 `relpages` mechanism is closed
      for newly written data.
      - Four tests that pinned the old convention were updated with their
        rationale rewritten, not their expectations merely flipped;
        `...RoundTripsToTrimmedStorage` renamed `...ToPaddedStorage`.
      - New `TestBpcharStoredPaddedAndRenderBoundariesStayInert` pins the
        contract WITH the sibling audit, including that a PRE-FLIP trimmed
        image still renders at full width — which is why the four
        `PadBpchar` callers must stay, not be deleted as redundant.
      - **The inventory MISSED a boundary and the regress gate caught it.**
        `length()` rune-counted the stored image directly, so under padded
        storage `length('x'::char(4096))` returned 4096 where PG's
        `bpcharlen` strips trailing blanks and gives 1. Fixed via
        `declaredBpcharTypmod`; the `length` STRIPS / `octet_length` PADS
        asymmetry is now commented as a sibling pair not to unify.
      - **No unit test covered it, and the SF0.25 sweep, tpch-spotcheck and
        TPC-H acceptance arm were ALL green with the bug present.** Only the
        upstream regress suite saw it — exactly what Hard-won Rule #5 keeps
        that gate for.
      - **Lesson for slices 2-4**: auditing the sites that RE-PAD is not
        sufficient — they all route through `PadBpchar` and are idempotent
        by construction. The dangerous boundaries are CONSUMERS that read the
        stored image and assumed it was trimmed.
      - Gates: units PASS; upstream regress `char` PASS (172 lines) and
        `varchar` PASS; `text`/`strings` fail on pre-existing unrelated
        grounds (missing HINT; Unicode-escape and regex parsing) and the
        `strings` diff shrank 265 → 263 when the `length()` hunk was fixed,
        with no remaining changed line mentioning bpchar; tpch-spotcheck
        PASS (Q12=2 Q13=33); TPC-DS SF0.25 PASS=96 MISMATCH=0 with plans
        99/99 identical; TPC-H acceptance arm 24 MATCH.
      - **Next: slice 2** — size consequences (index max-key-size, TOAST
        threshold), where a previously-accepted value can start erroring.
        Ledgered.
    - **SLICE 2 LANDED 2026-09-22 (loop 54) — and the design's guess about
      it was wrong.** A private PG 18.3 instance (`initdb` in /tmp, port
      5581; no reference cluster touched) settled it.
      - **PG compresses the padding**: `char(3000)` holding 'x' gives
        `octet_length` 3000, `length` 1, **`pg_column_size` 45**, and
        indexes on `char(3000)`/`char(8000)` both accept the insert. So
        nothing errors at the TOAST threshold or `BTMaxItemSize`, and the
        "previously-accepted value starts erroring" case does not arise.
      - **What goopg actually had was a 50x REGRESSION from slice 1**:
        200 rows of `char(3000)` took **819,200** heap bytes against PG's
        **16,384**.
      - **Root cause, read not inferred**: `coerceTextLikeDatum` pads inside
        `encodeValuePGCtx` — AFTER `ToastLargeColumnsIfNeeded` has decided.
        The toast check saw the 1-character datum, declined, and the encoder
        then wrote 3000 raw bytes inline. Upstream pads at INPUT
        (`bpchar_input`) and so decides on the padded value.
      - **Fix restores upstream's ORDER**: pad the width-carrying bpchar
        before the threshold check, writing it back into the row
        (copy-on-write preserved; `PadBpchar` is idempotent). goopg's heap is
        now **16,384 bytes — byte-identical to PG**, values unchanged.
      - **No corpus gate saw it.** The SF0.25 sweep, tpch-spotcheck and TPC-H
        acceptance arm were ALL green at 819 KB, because every VALUE was
        correct and only the bytes on disk were wrong — which is precisely
        what R23 is about. `TestToastPadsBpcharBeforeDeciding` is the
        witness, and a unit test is the right kind for a storage-shape
        defect.
      - **Second boundary the slice-1 inventory missed** (the first was
        `length()`, caught by the upstream regress suite). Both were
        consumers or decision points that READ the stored image, not sites
        that re-pad it — the lesson already recorded, now confirmed twice.
      - Gates: units PASS; upstream regress `char` PASS (172 lines) +
        `varchar` PASS, `strings` unchanged at 263 lines; tpch-spotcheck PASS
        (Q12=2 Q13=33); TPC-DS SF0.25 PASS=96 MISMATCH=0 with plans 99/99
        identical; TPC-H acceptance arm 24 MATCH.
      - **Next: slice 3** — `pgoutput`'s bpchar rendering is unverified; no
        pgoutput site calls `PadBpchar`. Ledgered.
    - **SLICE 3 LANDED 2026-09-22 (loop 55).**
      - **Correction to this task's own design doc**: it claimed no pgoutput
        site calls `PadBpchar`. It does — `pgoDecodePhysicalValue`
        (`xlog/pgoutput.go`), already pinned by a test. The wrong claim came
        from grepping a package path that does not exist (`internal/wal/`).
      - **Two more consumers assumed trimmed storage.** On a STORED
        `char(10)` = 'ab', measured against PG 18.3:
        `bit_length` **80 → should be 16**, `length(c::text)` **10 → should
        be 2**. Root: **`char(n) → text` is `rtrim1`** (`pg_proc.dat` oid
        401), and `bit_length` has no bpchar overload so it resolves through
        that cast. Both now apply the rule explicitly.
      - The upstream `strings` regress diff shrank **263 → 248** lines.
      - **The existing test could not catch it**: literal forms
        (`'ab'::char(10)`) never reach `coerceTextLikeDatum`, so their datum
        stayed trimmed and the old agreement held.
        `TestBpcharStoredColumnLengthFamilyMatchesPG` is the stored-column
        witness.
      - **Slice 2 widened a pre-existing v0 limitation**:
        `pgoDecodePhysicalValue` fails loudly on an external TOAST pointer,
        and wide bpchar values are now toasted. PG has no such limitation.
        Ledgered as its own feature, not part of R23.
      - Every comment asserting the old trimmed convention was corrected, each
        also recording WHY the `PadBpchar` call must stay (pre-flip rows on
        disk are trimmed).
      - **The pattern, now confirmed three times**: sites that RE-PAD are safe
        (one idempotent helper); the dangerous ones are CONSUMERS that read
        the stored image. Each miss was invisible to a different gate — the
        regress suite, a byte-level measurement, and a stored-column witness.
      - Gates: units PASS; regress `char` PASS (172 lines) + `varchar` PASS,
        `strings` 263 → 248, `text` unchanged at 14 (pre-existing);
        tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 PASS=96 MISMATCH=0
        with plans 99/99 identical; TPC-H acceptance arm 24 MATCH.
      - **Next: slice 4** — reload and re-measure `relpages` on
        `customer`/`item`, the only way K41's gap is shown closed in number
        rather than mechanism.
    - **SLICE 4 LANDED 2026-09-22 (loop 56) — M0143-0007b is COMPLETE.**
      - The mechanism being right is not the number being right, so slice 4
        reloads and MEASURES. `customer`/`item` built from the upstream
        TPC-DS schema on a fresh private goopg and filled from the same
        SF0.25 TSVs; page counts read off the relfilenode on disk; PG's
        `relpages` read SELECT-only from the read-only reference.
      - **K41's gap is closed**:

        ```
        table      rows      PG   before    after      gap
        customer 100000    2872     1979     2854    -0.63%
        item      18000    1284      716     1242    -3.27%
        ```

        against M0143-0007's original **-31.1%** and **-44.2%**. Row counts
        identical on both sides.
      - **The residual is NOT claimed closed**: under 1% / 3.3% is goopg
        packing pages more densely, which M0143-0007 already separated as
        its own effect (free-space-per-page, not tuple width). It belongs to
        whichever task owns heap page density.
      - Why these two tables: between them they carry eight `char(N)`
        columns totalling ~150 characters per row, so the padding is a large
        fraction of the tuple — which is why K41 surfaced on the dimension
        tables and not the fact tables. Measuring the SAME tables the
        original finding used is what makes "closed" mean the same thing.
      - Gates: units PASS. No production code changed this slice
        (measurement only), so the corpus gates were not re-run; slices 1-3
        each ran the full set.
      - **Still open, each its own task**: the heap page-density residual,
        and logical replication of TOASTED values (slice 3's ledger row).
        Ledgered.
- **OWNER DECISION 2026-09-20 — APPROVED.** The owner approves reversing
  the trimmed-`bpchar`-storage convention: implement R23 padded
  `character(N)` storage. Proceed per the task text — design doc first,
  then land per-boundary with the sibling-path audit (heap comparisons,
  `internal/access/nbtree` key comparators, `pgoutput` WAL encoding,
  TOAST thresholds/index-key/WAL record sizes), each slice gated by its
  own regress run plus the sf025 sweep. Context: the owner also reloaded
  `:65433` today with the canonical 8-FK schema on a binary rebuilt at
  `2b8afa538` (HEAD at reload time; `09bde885a` re-pinned the anchors) —
  FK-driven plan comparisons now run against the FK-bearing cluster.
- [x] **M0143-0009 — `SELECT … FOR UPDATE` over a join drops every row when a
  locked leaf is not the rightmost scan (resjunk-ctid ColumnRef shift).**
  RESOLVED Loop #21 — `rebaseRowMarkPlan` post-order walk rebases every
  expression after ctid injection (per-node old→new position remaps; join
  preds/keys/`UsingCols` in merged coords — the executor's
  `mergedKeySlot`/`keySlot.rebind` prove keys are merged-space, not
  side-local; NLI probe keys outer-scoped), `SchemaColumn.Resjunk` marks
  injected cols, `LockRows.Output()`/`lockRowsOp` strip by resjunk-position
  set (not trailing-N), `distinctOp` dedups excluding resjunk positions,
  `CtidResno` resolves for non-Project roots, `LockedRel.ColPos` fixes EPQ
  merge on projected outputs, AI-007 self-join guard retired (alias-aware
  `tagScan`). Walk-wide `seen` dedupes shared `*ColumnRef` objects
  (HashKeys[0] aliases LeftKey/RightKey by pointer). Design:
  `docs/design/0100-0149/m0143-0009-rowmark-ctid-global-expr-rebase.md`.
  Test: `TestLockRowsJoinCtidShift` (8 arms) + updated `locking_test.go`
  self-join pins; verified end-to-end on scratch cluster incl. hash join.
  Kind: impl.
  Parent: none.
  Filed 2026-09-19 (Loop \#21) — live wrong-results defect found while probing
  rowmark ordering during task selection; verified at HEAD `2b7e97053` on a
  scratch cluster (:5533). Broadens the deferral-ledger `AI-007 self-join
  lock` row (2026-08-09, `status: -`): the blast radius recorded there
  ("self-joins") is under-scoped — ordinary two-table joins are corrupted
  the same way.
  - Repro (tables `a(i int, t text)`, `b(i int, t text)`, 200k rows each,
    one `i=31` row per side):
    `SELECT a.i FROM a JOIN b ON a.i=b.i WHERE a.i=31` → 1 row;
    `… FOR UPDATE` → 0 rows (WRONG); `… FOR UPDATE OF a` → 0 rows (WRONG);
    `… FOR UPDATE OF b` → 1 row; `SELECT b.i FROM b JOIN a … FOR UPDATE OF a`
    (a rightmost) → 1 row. `EXPLAIN (ANALYZE)` shows `Rows Removed by Join
    Filter: 1` inside the Nested Loop under LockRows — the join emits
    nothing, so LockRows never sees a row. Reproduces identically under
    Hash Join (`SELECT *` variant).
  - Mechanism: `wireRowMarkCtidColumns` (`internal/optimizer/planner.go:2663`)
    appends a `ctid<N>` resjunk column to each rowmarked leaf's schema;
    `recomputeIntermediateSchemas` (:2813) rebuilds Join/NLI schemas as
    left++right but rebases `ColumnRef.Index` ONLY for `*Project` targets
    (`fixColumnRefIndices`, :2882). `Join.Predicate` ColumnRefs are absolute
    merged-row positions (`joinPredicateMatch` → `evalExpr` over the merged
    row), so a ctid injected into any non-rightmost leaf shifts every later
    sibling's refs — the `b.i` operand reads the ctid datum, the join filter
    misfires, every row is removed. Locking only the rightmost leaf appends
    at the merged row's end → no shift → works; that is why `OF b` and
    right-leaf cases pass and why the ported rowmark tests did not catch it.
  - Same hazard class, un-rebased: hash/merge key expressions and the
    residual re-eval (`execResidual` defaults to the full `Predicate`,
    `internal/executor/operators_join_agg.go` ~line 353 — what kills the
    Hash Join variant), `Filter` predicates above the join, Sort/Aggregate/
    Distinct keys.
  - `hasSelfJoinLockedTable` (:2639) only skips injection when the same
    table OID appears twice (the AI-007 workaround); the general case is
    unguarded.
  - Secondary wrinkle: `CtidResno` is set only when the plan root is
    `*Project` (:2796); Join-rooted plans inject ctid columns that nothing
    reads — pure harm (shift refs, then the executor falls back to
    `currentTID`).
  - Fix direction (PG-faithful): PostgreSQL never shifts positions —
    resjunk ctids are built into the scan targetlist at path time and
    `postgres/src/backend/optimizer/plan/setrefs.c`
    (`set_plan_refs`/`set_join_references`) remaps every Var through the
    constructed targetlists. goopg's post-hoc injection needs the same
    global rebase: after `recomputeIntermediateSchemas`, walk the whole
    plan tree and rebase every expression holding absolute ColumnRefs
    (join Predicate + hash/merge keys, Filter.Predicate, Sort keys,
    Aggregate keys, …) via the existing (name, SourceTableIdx)→position
    map. `hasSelfJoinLockedTable` removal stays out of scope until the
    rebase exists (its guard currently masks self-joins only).
  - Required per M0143 discipline: a failing regression test first —
    `FOR UPDATE` over a two-table join must return the join's rows
    (locked vs unlocked results identical), covering left-leaf and
    right-leaf `OF` arms plus bare `FOR UPDATE`, under NL and hash plans.
    Executor/planner gates per the practice card. Resolving this task
    resolves the `AI-007` ledger row's residual
    (`.ralph/deferral_ledger.md`, row dated 2026-08-09).
- [x] **M0143-0010 — index probes must resolve the heap update chain; raw
  `PageGetHeapTuple` on the index ItemPointer reads a dead chain root.**
  RESOLVED Loop \#22 — new `eachHeapChainMember` iterator
  (`internal/executor/operators_index.go`, the
  `followHOTChainNoCopy`/`resolveDeferredUniqueChainTail` traversal shape:
  redirect stubs → target, `ItemIDNormal` members in chain order via
  `IsHotUpdated`/`CTID` links, `MaxHeapTuplesPerPage` bound) now backs every
  index-driven heap probe that must judge the live row version:
  `scanIndexForFKMatch` (snapshot `TupleVisibleSubxact` per member),
  `uniqueCheckWithWait.scanOnce` (in-flight-xmin wait +
  `isLiveForUniqueCheck` per member, `conflictPtr` = member slot),
  `findInProgressConflictKey` + `probeSpeculativeConflict`
  (upsert arbiter), `exclusionCheckOnce`, and
  `recheckDeferredExclusionEq` (chain = one logical row — counts once if
  ANY member is live, never per member). Already-correct siblings
  unchanged: `followHOTChain` probes (index/index-only/update scans,
  arbiter :673), `resolveDeferredUniqueChainTail`, EPQ/rowmark exact-ctid
  refetches. Design:
  `docs/design/0100-0149/m0143-0010-index-probe-hot-chain-resolution.md`.
  Tests: `TestFKInsertAfterParentHotUpdate`,
  `TestUniqueInsertAfterHotUpdate` (both verified red→green); the three
  isolation specs PASS.
  Kind: impl.
  Parent: none.
  Movement: none — executor correctness fix (false 23503/duplicate-key
  bypass); no planner, costing, or plan-shape surface touched.
  Filed 2026-09-20 (Loop \#22) — discovered while root-causing M-NIGHTLY
  items `testport/TestPort_IsolationFkContention`
  (AI-20260917-004357-008), `TestPort_IsolationFkDeadlock`
  (AI-20260917-004357-009) and `TestPort_UpdateLockedTuple`
  (AI-20260917-004357-012): all three are `pass`-required specs that
  regressed between nightly `20260916-035206` (sha `48cf54f8`, all PASS)
  and `20260917-004357` (sha `1b54b00f`, all FAIL with spurious 23503
  where PG expects a clean match or a wait).
  - Mechanism: a non-key UPDATE is HOT — the b-tree entry keeps pointing
    at the chain ROOT line pointer, whose tuple is dead once the updater
    commits (the live successor sits deeper in the same-page t_ctid
    chain). Any index probe that fetches `ptr.Offset` verbatim and tests
    only that tuple reports a false no-match for a live row. The
    regression window's culprit is `a53c5b807` (M0142-0003g), whose new
    `scanIndexForFKMatch` index-probe twin of `scanRelForFKMatchSeq`
    (FK INSERT `assertParentExists` / `scanTableForMatchFKWait`) reads
    the raw pointer — so `INSERT INTO child` fails 23503 after any
    committed non-key parent UPDATE. Verified live on a scratch cluster
    (`:5533`): `UPDATE foo SET b='y'; INSERT INTO bar VALUES (42)` →
    `ERROR: ... violates foreign key constraint "bar_a_fkey"`.
  - Same blind spot, **older, worse**: `uniqueCheckWithWait`'s
    `scanOnce` (`internal/executor/operators_storage.go:8998`) — the
    plain `INSERT`/`UPDATE` unique-enforcement probe — has never walked
    the chain either. Verified live: `CREATE TABLE uq(a int PRIMARY
    KEY, b text); INSERT INTO uq VALUES (7,'x'); UPDATE uq SET b='y';
    INSERT INTO uq VALUES (7,'dup')` **succeeds — duplicate PRIMARY KEY
    rows**. Also raw-pointer and unchained: upsert arbiter probes
    `findInProgressConflictKey` (`operators_upsert.go:869`) and the
    speculative-recheck probe (`:1402`). The :673 arbiter arm already
    follows the chain via `followHOTChain` (M0100-0005 Bug A), and
    `recheckDeferredUniqueKey` resolves via
    `resolveDeferredUniqueChainTail` (M0134-0005e) — the pattern exists,
    the newer/older probes just never got it.
  - Fix direction: a shared per-member chain iterator (redirect stubs →
    target, `ItemIDNormal` members yielded in chain order via
    `IsHotUpdated`/`CTID` links, `MaxHeapTuplesPerPage` bound — the
    `followHOTChainNoCopy`/`resolveDeferredUniqueChainTail` traversal
    shape) so each probe keeps its own predicate
    (`TupleVisibleSubxact`+`fkPendingOutcome` for FK;
    `isLiveForUniqueCheck`+in-flight-xmin for the unique/upsert probes)
    but can never again see only the dead root. Per-member evaluation in
    chain order preserves the abort-rescan case (root's aborted xmax is
    live under `isLiveForUniqueCheck`) that a tail-only resolution would
    lose.
  - Gates: the three failing isolation specs PASS at HEAD after the fix
    (verified red at `ad778446e` first);
    `TestFKInsertAfterParentHotUpdate` (new, red-then-green verified via
    stash); executor package suite; the FK/upsert/unique isolation
    siblings; tpch-spotcheck; tpcds-sf025; tpch-acceptance-arm.

- [x] **M0143-0011 — bare `VACUUM` (no target list) must cover the
  CURRENT database, not the default one** (impl; filed 2026-10-02 by
  owner decision — the record is the open deferral in
  `.ralph/deferral_ledger.md`'s 2026-07-30 M0125-0028 row (status `-`),
  which until now had no unchecked owning task; this entry is that
  owner — do not also file a fresh bug for it).
  Kind: impl
  Parent: M0125-0028
  - **Defect.** `vacuumOp.expandVacuumTargets` / `vacuumTableTargets`
    (`internal/executor/operators_vacuum.go`) resolve named targets
    through the per-connection catalog correctly, but the no-target arm
    still walks `im.AllTables()` — the `DefaultDBOid` namespace, as
    **deep copies**. Two consequences: (a) connected to a non-default
    database, bare `VACUUM` vacuums the WRONG database's tables —
    verified live 2026-10-02 on `:65433`, where `VACUUM` in db `tpch`
    returned success yet `pg_class.relallvisible` for tpch's tables
    stayed 0 (the owner had to vacuum each table explicitly during the
    corpus alignment); (b) in every database the no-target arm's
    `UpdateRelStats`/`RelFrozenXID` writes land on throwaway copies and
    are silently lost — db-wide VACUUM's bookkeeping has never taken
    effect. PG semantics (`get_all_vacuum_rels`,
    `postgres/src/backend/commands/vacuum.c`): bare VACUUM covers all
    vacuumable relations in the *current* database.
  - **Fix direction** (the ledger row's resume point): switch the
    no-target arms of `expandVacuumTargets`/`vacuumTableTargets` to
    `im.UserTableHandles(NamespaceDBOid(ctx.CurrentDatabaseOid))` — the
    live-handle iterator M0125-0028 added for bare ANALYZE — so the
    expansion and the stats/frozen-xid writes ride live handles like
    the named-target path already does.
  - **Ledger's own warning, binding here:** the live-handle switch makes
    db-wide VACUUM's freeze/stats writes take effect for the FIRST time
    — a freeze-bookkeeping behaviour change that deserves its own
    verification pass, not a rider on an unrelated commit. Watch the
    vacuum/isolation specs for newly-visible effects.
  - **Same-row sibling residual — note only, do NOT bundle:**
    `VACUUM <missing-table>` silently succeeds (PG raises 42P01 —
    `expandVacuumTargets` `continue`s on failed lookup while ANALYZE
    already has the correct 42P01 arm). If touched, it is its own task
    with a vacuum-spec sweep, per the ledger row.
  - **Test that fails before it (M0143 per-task discipline):** an
    in-process two-database test — create a second db via
    `tryHandleDatabaseDDL` (the `internal/postmaster/database_ddl_test.go`
    / `database_oid_wiring_test.go` in-process pattern per M0143-0001's
    scoping note), one table in each db, then bare `VACUUM` with
    `ctx.CurrentDatabaseOid` pointed at the non-default db: assert the
    non-default db's table was vacuumed (a stat/freeze write that lands
    on the LIVE handle — e.g. its relstats change persists to a second
    read through the same catalog, not a re-lookup of a copy) and the
    default db's table was NOT touched. Both assertions are red at HEAD.
  - **Gates:** executor package suite; `pg-regress-runner.sh` vacuum +
    analyze cases and the vacuum-adjacent isolation specs (freeze /
    horizons / vacuum-concurrent-drop — the freeze-bookkeeping change
    may flip visible behaviour there, that is the verification pass);
    `scripts/tpch-spotcheck.sh`; tpcds-sf025 gate.
  - **Done:** the test above is green AND, on the real `:65433` cluster
    (owner-run post-landing), bare `VACUUM` in db `tpch` flips
    `pg_class.relallvisible` — the exact operational check that exposed
    the bug. Owner runbook updated so ops use explicit per-table lists
    until then (`maintenance_prompts/cluster-ops-runbook.md`).
  - **DONE 2026\-10\-02** \(loop part\)\. Both no\-target arms
    \(`expandVacuumTargets`, `vacuumTableTargets`\) iterate
    `im\.UserTableHandles\(NamespaceDBOid\(CurrentDatabaseOid\)\)`\.
    `vacuumTableTargets` used to type\-assert the plan catalog, which is
    not an `\*InMemory`, so its db\-wide arm had returned nothing\. Design
    `docs/design/0100\-0149/0125\-0028\-warm\-stats\-programme\.md`
    § "M0143\-0011"\.
    - Test `TestBareVacuumCoversCurrentDatabaseOnly`: reltuples and
      relfrozenxid \(under FREEZE\) land on the live per\-DB handle, and
      the default DB is untouched\. Red at HEAD\.
    - Verification pass: 6 vacuum\-adjacent isolation specs PASS; regress
      vacuum / vacuum\_parallel / stats unchanged vs a HEAD baseline;
      units, tpch\-spotcheck, sf025 96/96, TPC\-H arm, fire set \(none\),
      ea\-ratchet PASS\.
    - Owner\-run check still due: bare `VACUUM` in db `tpch` on `:65433`
      should flip `pg\_class\.relallvisible`\. The loop may not write to the
      reference cluster\.
  Movement: none — correctness fix — no plan instrument


## M0144 — Measurement-first parity: censuses, instrumented PG, route-order alignment (filed 2026-09-20)

**Milestone doc:** `docs/milestones/0144-measurement-first-parity.md`
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity harness (M0137–M0145)"
**Source:** `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/03-forward-plan.md`
(adopted by owner decision 2026-09-20, with two owner overrides recorded in the
milestone doc: parallel-mode TPC-H parity is now canonical, and route-order
verification is prioritised because several landed fixes never moved a plan —
goopg's processing route diverged from PG's upstream of the fix).
**Prerequisites:** none. Order: `.ralph/fix_plan.md` banner item 2.

- [x] **M0144-0001 — canonicalise parallel-mode TPC-H parity** (owner decision
  2026-09-20). **DONE 2026-09-20** (code `2a2ebc3f0`). Design doc:
  `docs/design/0100-0149/m0144-0001-parallel-tpch-parity-canonical.md`.
  `-serial` now defaults `false` (`cmd/estimate-audit/main.go`); caller audit
  found `tpch-estimate-audit-arm.sh` was the only binary invocation site and
  already pinned `-serial=true`; no test pinned the old default. Fresh
  canonical parallel capture on a private lane (G3, HEAD binary, served-sha
  verified): **TPC-H parallel `match=1/22` (Q6)**, artefacts
  `analysis/m0144/m0144-0001-{goopg,pg}-parallel.*` — measured on the
  post-2026-09-20-8-FK-reload stats epoch (both sides' epochs differ from
  P0-E7's pair), so the drop from P0-E7's provisional 3 is a new-baseline
  reading, not a same-data regression: Q13/Q11's PG plans are unchanged in
  shape while goopg's flipped on the new stats, and no production commit in
  between touches aggregation/join-order costing. The measured number is
  reported for the owner to write into `AGENT.md` §Goal (harness section is
  owner-edited). Also corrected `m0137-0003` §2's stale `-ref-port 65432`
  recipe (R1: it ANALYZEs the reference) to the two-invocation form.
  Movement: none
  Kind: impl
  Parent: none
  Original scope (kept for the record): flip `estimate-audit -serial`'s
  default to `false` (`cmd/estimate-audit/main.go:293`) so the canonical
  tool measures the target protocol by default; fix any test pinning the
  old default; keep `-serial=true` working as the diagnostic mode.
  **Caller audit first**:
  `scripts/tpch-estimate-audit-arm.sh` relied on the serial default — already
  pinned `-serial=true` in the same owner change that filed this task; sweep
  for any other invocation site that omits the flag before flipping. The
  `cmd/` commit needs the `tmp/gate-stamps/{tpch-spotcheck,tpcds-sf025}.json`
  PASS stamps for the staged tree plus a `PARITY: N/A — measurement-tool flag
  default` body line (G2). Then capture a fresh
  parallel-mode TPC-H pair on a private lane per G3 (`-plan-only -serial=false`,
  HEAD binary, `GOOPG_EXPECT_BIN_SHA256` set) and record the resulting MATCH
  count — that number re-pins the `AGENT.md` §Goal floor (provisionally ≥3
  from P0-E7's 2026-09-18 measure; report the actual). Update
  `m0137-0003-baseline-capture-procedure.md` §2 if the flag flip lands after
  the doc update. The capture artefacts land in
  `analysis/m0144/` with a `docs(…)`/`analysis(…)` commit.
- [x] **M0144-0002 — first-divergence census** (03-forward-plan §1).
  **DONE 2026-09-20.** `scripts/pg-plan-first-divergence.py` built as the
  sibling instrument (imports the differ's parser + N1–N7 normalisation +
  category vocabulary; accepts both `=== Qn` and `===== Qn =====` section
  styles; stop-at-first PG-order aligner, one mutually-exclusive record per
  divergent query). Ran over all three committed corpora — TPC-H parallel
  on the fresh M0144-0001 pair, TPC-DS SF0.25 latest sweep vs the
  m0142-0012verify PG reference, TPC-DS SF1 on P0-E7's pair. Census MATCH
  sets reproduce the differ's floors exactly (TPC-H Q6 1/22; SF0.25 Q9+Q41
  2/99; SF1 Q41 1/99). Ranked tables:
  `analysis/m0144/m0144-0002-first-divergence-census.md` (+3 raw `.txt`).
  Headline: TPC-DS's dominant first-divergence is ordered aggregation under
  `LIMIT` (`Limit → PG GroupAggregate/Incremental Sort vs goopg Sort`,
  ~25 records SF0.25 / ~20 SF1 — the M0141-S7 family, now with a measured
  blast radius); TPC-H leads with aggregation-strategy at the root/Sort
  boundary (7/21 — the phased-agg decision the serial corpus hid).
  Design doc:
  `docs/design/0100-0149/m0144-0002-first-divergence-census.md`.
  Movement: none
  Kind: recon
  Parent: none
- [x] **M0144-0003 — route-order verification** (owner directive 2026-09-20:
  "fixes landed but plans didn't move because the processing order differs
  from PG" — verify, then align).
  **DONE 2026-09-20.** Route-diff table:
  `analysis/m0144/m0144-0003-route-order-verification.md`; design doc
  `docs/design/0100-0149/m0144-0003-route-order-verification.md`.
  Verdicts: **(a) confirmed** — the `Filter{sunk}` inside `Semi.Left`
  (`pushConjunctsBelowSemiAnti`, unnest.go:372) is one opaque leaf to
  `extractSearchLeaves` (joinsearchseam.go:1456) → `leaf-count` decline at
  :325 before `semiAntiLinksHaveSJInfos`; PG's `pull_up_sublinks`
  (prepjointree.c:468, planner.c:737) is jointree-level so no Filter node
  exists. **(b) confirmed** — `addPartialSetOpPath`
  (windowsetoppaths.go:551) produces at the `*SetOp` plan-node level while
  PG flattens UNION ALL-in-FROM to an appendrel (`pull_up_simple_union_all`
  prepjointree.c:1617 via planner.c:754) and files partial paths per-rel
  (`add_paths_to_append_rel` allpaths.c:1321) — appendrel is a join-input
  citizen; goopg SF0.25 has 0 Parallel Append vs PG's 9 sites.
  **(c) REFUTED as ordering** — producer sits at PG's position
  (upperordered.go:186 ≈ planner.c:5374); inert via
  `GOOPG_INCREMENTAL_SORT` default-off (incrementalsortpaths.go:81) plus
  S7's measured cost loss — costing question, banner item 6's scope.
  **(d) confirmed** — `applyUpperNarrowing` at planner.go:190 runs after
  the tournament; PG prices narrowed width into candidates via
  `set_pathtarget_cost_width` (costsize.c:6367) from
  `make_*_input_target`s built in `grouping_planner` (planner.c:1676-1744).
  **(e) confirmed, same root as (a)** — the pin is load-bearing only
  because Phase B declines; PG admits semi/anti pairs inside DP via
  `join_is_legal` (joinrels.c:350/:713). Impl tasks filed: M0144-0003a/b/c.
  Movement: none
  Kind: recon
  Parent: none
- [x] **M0144-0003a — Phase-B leaf admission through the sunk-conjunct
  Filter** (filed by M0144-0003 item (a)+(e), same root). Phase B's chain
  `spineJoins[0]` is the outermost pinned Semi/Anti join whose `Left`
  carries `Filter{sunk}(origChain)` (predp.go:49-55's documented shape);
  `extractSearchLeaves` counts that Filter as one opaque leaf
  (joinsearchseam.go:1456-1461) so `leaf-count` (:325) fires for every
  corpus IN/EXISTS statement (measured `leaf-count×62`, `nrels=4 nleaves=2`
  on Q56) before `semiAntiLinksHaveSJInfos` (:1889) runs. Fix shape: make
  the walk descend a `*Filter` whose conjuncts re-base into the chain's
  WHERE/qual space (the conjuncts are already in the outer chain's column
  space — `pushConjunctsBelowSemiAnti` only moved them down, never
  re-based), so the sunk Filter contributes its leaves plus its conjuncts
  rather than collapsing to one opaque leaf. Alternative (bigger,
  PG-faithful): move unnesting to jointree level — the durable direction
  per the route-diff pattern. Constraint: Q78's `outer-over-derived`
  firewall must not weaken. Expected movement per S5: IN/EXISTS-derived
  semi/anti links reach the searched pair space → census semi/anti
  first-divergence records move (SF1 Q16 `NL Left Semi vs HJ Left Anti`
  class; the ~62 leaf-count declines re-attributed).
  Kind: impl
  Parent: M0144-0003
  - **FIX SHAPE CORRECTED 2026-09-20 (loop \#56) by corpus census —
    the opaque leaf is a `*Project`, NOT a `*Filter`.** Design doc:
    `docs/design/0100-0149/m0144-0003a-opaque-leaf-census.md`; evidence:
    `analysis/m0144/m0144-0003a-opaque-leaf-census.txt`. Movement: none
    (census + decision; probe reverted, no production code changed).
    - Every opaque leaf at the `leaf-count` decline, all 99 SF0.25 queries:
      `Project` 53 occurrences, `SeqScan` 4, `NestedLoopIndexJoin` 1,
      `Gather` 1. **Zero `*Filter` leaves.** `Project` is the only kind in
      23 of the 26 declines and appears in 10 of the 11 declining queries.
    - The decline count is **26 across 11 queries** (Q14 Q16 Q23 Q33 Q35
      Q56 Q58 Q60 Q83 Q94 Q95), not the filed `leaf-count×62` — consistent
      with `M0142-0008-producer` and the `-plumbing` chain landing between
      the two censuses.
    - Provenance of the Projects was already established by
      `M0142-0008a-3i-verify`: `unnestExistsExpr` clones the EXISTS body's
      already-planned subquery tree, and every planned `SELECT` carries its
      own top-level output-list `*Project`.
    - **This is EASIER than the filed fix**, not harder: a Project carries
      no conjuncts, so the whole "re-base into the chain's WHERE/qual
      space" half of the filed fix shape disappears. What remains is
      structural — descend, and let the walk flatten the body's own join
      tree into real leaves.
    - **Decision (recorded before implementation, AGENT.md C3): descend a
      `*Project` that satisfies `projectIsPositionalIdentity`** — the
      predicate `M0144-0011a-3` already landed and tested
      (`internal/optimizer/upperorderedinput.go`; target `j` is
      `ColumnRef{Index: j}` for every `j`, equal widths, not
      `IsolatedScope`). Under it a Project re-assigns nothing, so the
      seam's position-based coordinate machinery
      (`buildLeafSpans`/`remapWalkOrderFlatToSpans`/
      `pgShapedOffsetChecksOK`) sees the same positions before and after.
      The `*Filter` arm is NOT to be implemented — it has no witness.
    - The implementing loop must still establish three things this census
      did not (it wrote no code):
      - do the body's exposed leaves line up with `ctx.bindings`?
        `nprefix`=4 while `scans`=3 on Q16, so the arithmetic only works if
        the body's relations are among the `nprefix` FROM items;
      - the P0-H11 `cumulativeFromSpans` span round-trip must close in the
        SAME change — it is latent only because nothing gets through today;
      - **Q35 is out of scope** (`Gather` + `NestedLoopIndexJoin` leaves,
        the `M0142-0008a-3i-leafcount` finding), so expect 10 of 11.
  - **THAT DECISION IS REFUTED 2026-09-20 (loop \#57), by probe, before any
    production code was written.** Design doc:
    `docs/design/0100-0149/m0144-0003a-project-descent-refuted.md`;
    evidence: `analysis/m0144/m0144-0003a-project-descent-refuted.txt`.
    Movement: none (probe reverted, no production code changed).
    - Instrumented every opaque leaf at the decline with
      `projectIsPositionalIdentity` plus a count of the leaves its child
      would expose, across all 11 declining SF0.25 queries.
    - **Not one `*Project` is a positional identity** — every one prints
      `P-NONident`. Several are not even width-preserving: Q14's is 73
      targets over a 29-column child, Q33's 97 over 67. They are wide
      re-projections, not the identity renames the decision assumed.
    - **`would == scans` in EVERY case.** Descending every identity Project
      exposes ZERO additional leaves; the gap (want 4-6, have 2-3) does not
      narrow by one. The arm would have been completely inert.
    - Why: the Project children are `*Gather`, `*CTEScan`, `*Filter`,
      `*Join`, `*NestedLoopIndexJoin` — **already-planned composites**, the
      same finding as `M0142-0008a-3i-leafcount`. A `*Gather` under a
      106-target Project is a finished plan fragment with a chosen worker
      count; no leaf-level admission predicate can turn it back into base
      relations.
    - **So M0144-0003a is NOT implementable as a leaf-admission change.**
      It joins M0142-0008a-3's increments (i) and (ii) and
      M0142-0008a-3i-lateral on ONE blocker:
      **`M0142-0008a-3i-lateral-route`** — Phase B must search before Phase
      A lowers anything. That is a FOURTH independent confirmation of
      M0144-0003's own thesis.
    - **Superseded by M0145-0003 (owner decision 2026-09-20):** the recon
      then showed the lowering is one stage earlier still — the resolver
      plans sublink bodies before either phase — and the real fix is the
      jointree-level pull-up, not this leaf admission. Stays `[!]`; the
      wall is M0145-0003.
    - Method lesson recorded in the doc §4: **a census of node KINDS does
      not license an assumption about those nodes' SHAPE.** The previous
      loop censused the type and inferred the shape; probe the predicate
      you intend to gate on, not just the type switch.
    - **CLOSED 2026-09-23 — owner disposition (delegated, scope
      M0137–M0145): superseded into M0146-0008.** The named wall
      (M0145-0003's jointree-level pull-up) has landed; this entry's own
      probe refuted the leaf-admission fix shape, so nothing here
      transfers verbatim. The live residual — opaque `*Project` leaves
      that are already-planned composites — is re-censused on the
      jointree arm by **M0146-0008**, which owns whatever admission work
      survives that census.
- [x] **M0144-0003b-1 — carry each set operation's SETOP rel to the link
  above** (the enabling slice of M0144-0003b; LANDED, inert). Refutes the
  parent's factual premise: `addPartialSetOpPath` does NOT admit 0/99 — the
  INNERMOST link of every UNION ALL chain already files a partial path
  (Q14/Q71/Q76 measured), and on Q14/Q71 the `Gather` over it wins the
  link's tournament outright. What failed is COMPOSITION: goopg builds a
  left-deep `*SetOp` chain where PG flattens the union into one appendrel
  (`pull_up_simple_union_all`, prepjointree.c:1617), and `searchedRelOf`
  stops at any node without exactly one boundary child
  (searchedtree.go:184) — a `*SetOp` has two — so every OUTER link read
  `nil-rel` on the side holding the rest of the chain and returned before
  either arm ran. Fix: carry the SETOP rel out on the node
  `createSetOpPaths` returns (`setopbranchrel.go`, the mechanism
  `searchedTree` already uses) and widen the branch accessor by exactly that
  one terminus. Absorption: 3 outer links across SF0.25 newly file a partial
  path, none removed; no cost change. Inert — SF0.25 `same=99 changed=0`,
  verdict-changes=none; TPC-H structurally cannot exercise it (zero
  `Append`/`SetOp` nodes in all 22 plans). Movement: none.
  Design: docs/design/0100-0149/m0144-0003b-1-setop-chain-branch-rel.md
  Kind: impl
  Parent: M0144-0003b
- [x] **M0144-0003b — jointree-level UNION ALL flattening**
  (`pull_up_simple_union_all` analog; filed by M0144-0003 item (b)). PG
  flattens UNION ALL-in-FROM into an appendrel during jointree
  preprocessing (prepjointree.c:1617 via pull_up_subqueries planner.c:754)
  so `add_paths_to_append_rel` (allpaths.c:1321) files partial paths
  per-rel and the append is a join-input citizen. goopg has no flattening —
  `addPartialSetOpPath` produces at the `*SetOp` plan-node level and admits
  0/99 corpus-wide (SF0.25: 0 Parallel Append vs PG's 9 sites). Scope: an
  appendrel representation in the searched problem + partial-path filing at
  the appendrel level (reusing the landed pure/mixed arms' cost math where
  they fit); must not let a flattened appendrel bypass the Q78 firewall or
  the outer-join ordering rules. Expected movement per S5: the TPC-DS
  Parallel Append sites (Q5×3, Q2, Q14, Q71, Q76 and census `parallelism`
  records) become reachable.
  **BLOCKED on M0142-0008a-3i-lateral-route, and the filed premise is
  half-refuted.** M0144-0003b-1 (above) measured the producer and found it
  already admits at the innermost link of every chain; the chain-composition
  half is now landed. The RESIDUAL is branches whose subtree never reached
  the search at all (Q5's `*Project:nil-rel` on BOTH sides, Q2/Q33/Q56/Q60
  likewise): there is no rel to carry, because Phase A of the pre-DP unnest
  spliced a BUILT plan before Phase B's search ran. No accessor widening
  can reach it — this is the same two-phase wall that blocks M0144-0003a
  and the three M0142-0008a-3 items, and it belongs behind the same gate.
  Do not re-scope as "build an appendrel representation" until the route
  order is fixed. **Superseded by M0145-0004 (owner decision 2026-09-20):**
  the appendrel lives at jointree level in the new flow — flattening
  happens there, before lowering. Stays `[!]`; the wall is M0145-0001's
  IR plus 0003/0004.
  **CLOSED 2026-09-23 — owner disposition (delegated, scope M0137–M0145).**
  The wall is discharged: M0145-0004 landed jointree-level UNION ALL
  flattening and M0145-0004a completed the whole-chain recursion — this
  task's substance exists, on the jointree arm, as designed. Any residual
  appendrel-citizenship gap is part of the flow-completion evidence the
  M0145-0008 flip re-measures, not separate work here.
  Kind: impl
  Parent: M0144-0003
- [x] **M0144-0003c — pre-cost upper narrowing** (filed by M0144-0003 item
  (d)). `applyUpperNarrowing` (planner.go:190) narrows Aggregate/Sort input
  width on the finished tree — post-tournament, so it can never move a plan
  choice. PG narrows the input target BEFORE costing: `grouping_planner`
  builds `make_*_input_target`s (planner.c:1676-1744) finalized by
  `set_pathtarget_cost_width` (costsize.c:6367); `cost_sort`
  (costsize.c:2328) and hash/agg sizing (:3709/:3722/:3765) price the
  narrow row into every candidate. Scope: carry the derived keep-set into
  the ordered/grouping rel's path sizing so `costSort`/agg candidates are
  priced on the narrowed width (the keep-list derivation already exists —
  `group_input_target.go` name-level union consumed by the post-pass);
  delete nothing, re-point the costing inputs. Expected movement per S5:
  width-dependent cost branches — sort memory feasibility and hash-table
  sizing inside the sort-strategy (43 SF0.25) / aggregation-strategy census
  clusters.
  Kind: impl
  Parent: M0144-0003
  Movement: none
  - **LANDED 2026-09-20 (loop \#58).** Design doc:
    `docs/design/0100-0149/m0144-0003c-pre-cost-sort-width.md` (its §3
    decision and §4 limit were both written before any parity number).
    - **The filed premise is half wrong.** goopg ALREADY narrows pre-cost at
      three sites, all from banner item 5's M0141-S2a-fix1 family: the
      ORDERED rel's Sort (`narrowOrderedRelWidths`, upperordered.go:82,
      sweep-a), WINDOW's internal sort (`window_sort_narrow.go`, sweep-b),
      and the aggregate's own entry sizing (`aggInputWidth`,
      groupingpaths.go:363, fix1). `applyUpperNarrowing` (planner.go:190) is
      the COMMIT step that inserts the narrowing Project — item (d) read it
      and concluded the COSTING was post-tournament. `aggInputWidth`'s own
      comment already says the two are deliberately separate.
    - **What WAS genuinely un-narrowed**: inside `addGroupingPaths` the
      aggregate is priced through `aggInputWidth` (narrow) while the Sort
      beneath it went through `sortPathForBounded` → `pathNCols`/
      `pathAvgVarBytes` → the INPUT rel (full row). One input, two widths.
    - Fixed by handing both sort sites a narrowed shallow COPY of the seed
      carrying `NCols`/`AvgVarBytes` from `aggInputWidth` — the per-path
      override `pathNCols` already prefers (path.go:819-842). No new
      constant (R6), no newly chosen quantity (C3). Both sort sites changed
      together (Hard-won Rule #2 — the PLAIN presorted sort and the SORTED
      group-key sort are one twin pair).
    - PG citation: `make_group_input_target`
      (`postgres/src/backend/optimizer/plan/planner.c:1676-1744`) narrows
      once, `set_pathtarget_cost_width` (`costsize.c:6367`) finalises the
      width, `cost_sort` (`costsize.c:2328`) reads that same width.
    - **Result: INERT on both corpora, and the doc predicted why.** TPC-H
      plans capture BYTE-IDENTICAL to HEAD (`sha256 75599dae4efbf31c`) from
      a DIFFERENT binary (`08d905fa9ed819d4` vs `2326ec51aef8b431`) — G3's
      inertness proof; TPC-DS SF0.25 `same=99 changed=0`. Parity identical
      on both.
      - The reason, recorded in §4 BEFORE measuring: `costSortRunWithWidth`
        lets the width reach the price ONLY through the spill branch
        (`nruns := inputBytes / work_mem`). An in-memory sort costs the same
        at any width, correctly. No corpus grouping sort spills at a width
        where narrowing changes the run count.
      - The first unit test used a 100k-row fixture, priced both widths
        identically and read as a wiring failure — it was not; the fixture
        never reached the branch. It now runs at 5M rows where the branch is
        live, and a separate assertion pins that the override does reach
        `pathNCols`.
    - Lands anyway: two readers of one input no longer disagree about its
      width, R3 forbids discarding a PG-faithful change for lack of number
      movement, and it is a prerequisite for anything that later makes the
      spill branch bite (larger SF, lower `work_mem`, or promoting
      `GOOPG_PG_SORT_RELATION_BYTES_COST`).
    - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
      `tpcds-sf025 sweep` PASS; `tpch-acceptance-arm` PASS (24/24 VALUES);
      `make ea-ratchet` `N/A`.
- [x] **M0144-0004 — instrumented PG 18.3, instrument 1: `OPTIMIZER_DEBUG`
  build** (03-forward-plan §3). Build PG 18.3 from a **scratch checkout**
  (`./postgres/` stays read-only) with `OPTIMIZER_DEBUG` defined; serve a
  private clone of the corpus on a `55xx` port (never `:65432`/`:65438` — R1
  applies to the instrumented build's data sources). Document the build recipe
  and capture the per-rel survivor pathlists for the top first-divergence
  queries from 0002. Known limit (record it): survivors-only — `add_path`
  prints nothing, rejected candidates are already evicted.
  Done: scratch clone of `62d6c7d3df6` under `tmp/pg18-optdebug/`,
  `CPPFLAGS="-DOPTIMIZER_DEBUG"` (Makefile.global / `CONFIGURE_ARGS`), private
  `tpcds025` clone on `:5560` loaded from the harness's own TSVs + `tpcds.sql`
  + `ANALYZE` (zero reference-cluster access; `max_parallel_workers_per_gather=4`
  mirrored). 12-query capture set covering every census category n≥2; all 12
  plans shape-match the :65438 reference (ANALYZE re-sample drift only).
  Distiller `scripts/pg-optdebug-survivors.py` → per-rel survivor tables in
  `analysis/m0144/optdebug-0004/` (211 MB raw nodeToString stays in `tmp/`,
  reproducible). Gotchas: nodeToString namespaces path fields
  (`path.`/`jpath.path.`/bare); `cheapest_total_path` is a node-valued field
  at list-less depth; TPC-DS Q23 is two-statement — EXPLAIN each.
  `docs/design/0100-0149/m0144-0004-optdebug-instrumented-pg.md`;
  `analysis/m0144/m0144-0004-optdebug-build.md`.
  Kind: impl
  Parent: none
- [x] **M0144-0005 — instrumented PG, instrument 2: `debug_plan_candidates`
  trace GUC** (03-forward-plan §3). Same scratch-checkout build plus a small
  patch adding a GUC that emits, per `RelOptInfo`: every pathlist entry at
  `add_path` time (node type, startup/total cost, rows, pathkeys, param_info,
  required-outer rels), the rejection verdict (which comparator won), and the
  `set_cheapest` winner. This is the per-candidate artefact goopg's
  `GOOPG_PGSHAPED_DP_TRACE` gets diffed against — separating candidate gap /
  costing gap / tie-break gap, which today all read as "join-order".
  Done: `debug_plan_candidates` (PGC_USERSET/DEVELOPER_OPTIONS, default off)
  patched into the same `tmp/pg18-optdebug/src` scratch tree
  (`pathnode.c` emitters + `guc_tables.c` registration + `pathnode.h`
  extern; patch lives in the scratch checkout only, oracle untouched).
  `PLANCAND` records to backend stdout (same channel as pprint):
  add/padd candidate, ok/pok+removed-count, rej/prej+first-dominator+
  comparator vector (via=cost/keys/tie/rows/outer/psafe), preskip/ppreskip
  precheck kills, win set_cheapest total/startup/parameterized. Verified
  non-invasive: off emits zero lines; Q7 Limit→GroupAggregate→Gather Merge
  and Q5 Parallel Append shapes unchanged. 12-query captures (same set as
  0004) distilled by `scripts/pg-plancand-distill.py` →
  `analysis/m0144/optdebug-0005/` (raw slices in `tmp/pg18-optdebug/plancand/`).
  First reads: precheck kills ≈ half of all rejections; via=cost dominates
  post-build (zero rows/outer/psafe/dis rejections corpus-wide); emit
  verdict BEFORE pfree (first draft had a use-after-free).
  `docs/design/0100-0149/m0144-0005-debug-plan-candidates.md`;
  `analysis/m0144/m0144-0005-debug-plan-candidates.md`.
  Kind: impl
  Parent: none
- [x] **M0144-0006 — instrumented PG, instrument 3: `-finstrument-functions`
  call-graph build** (03-forward-plan §3). Same private setup; the trace
  answers "which route through the planner did this query take"
  (`make_one_rel` DP search / `join_search_one_level` ordering /
  `create_unique_path` / degenerate path) for each divergent query — the
  PG-side input to M0144-0003's route-diff table and the audit-scope namer
  for vertical slices.
  Done: `-finstrument-functions` via `CFLAGS +=` in the five
  `src/backend/optimizer/` subdir Makefiles; hooks +
  `debug_plan_callgraph` GUC in new `optimizer/util/calltrace.c`
  (`no_instrument_function`, no-alloc, buffered stdout — same channel as
  pprint/PLANCAND). `CGT e/x` raw this_fn addrs resolved offline via `nm`
  (`CGT base` PIE anchor on `&standard_planner`); `CGT tag` arg markers at
  `make_rel_from_joinlist` (levels + degenerate/hook/geqo/standard route),
  `join_search_one_level` (level + lower relids), `make_join_rel`
  (joinrel/outer/inner relids + jt + illegal). Verified: off emits zero;
  Q7 shape preserved; all 13 slices e/x-balanced. 12-query captures →
  `analysis/m0144/optdebug-0006/` via `scripts/pg-calltrace-distill.py`.
  Gotchas recorded: `.SECONDARY:` means deleted .o never rebuild as
  prereqs (name .o targets explicitly); Makefile `CFLAGS +=` does not
  retrigger compiles; GCC clones extern leaf fns into instrumented TUs.
  `docs/design/0100-0149/m0144-0006-callgraph-build.md`;
  `analysis/m0144/m0144-0006-callgraph-build.md`.
  Kind: impl
  Parent: none
- [x] **M0144-0007 — cost-margin census** (03-forward-plan §2). For each
  first-divergence node from 0002, force PG's shape in goopg (the
  M0142-0016c forced-plan comparator pattern plus the `GOOPG_*` admission
  arms) and record the margin class: <1% (election/tie-break), 1–20% (input
  divergence — attribute rows/width/cost-term first), >20% or unexpressible
  (structural gap). Output: margin column appended to 0002's table in
  `analysis/m0144/`. Depends on 0002.
  Done: `scripts/goopg-margin-census.py` — session `enable_*` /
  parallel-cost arms with forced-plan re-census via `census_query`, plus
  per-query DPPATH candidate margins and `upper.ordered.seed` presorted-
  input evidence on trace-enabled private lanes (:5590/:5591/:5592).
  210 records across the three corpora: ~72% structural
  (unexpressible[/no-arm] 50%, dominated-noncost 9%, priced-structural
  13%), ~28% election-class (election 22%, forced-cheaper 5%, input 1).
  The dominant `Limit → {GroupAggregate|Incremental Sort} | Sort` family
  is a pathkey-propagation generation gap (`nonemptykeys=0` even under
  `GOOPG_INCREMENTAL_SORT=1 GOOPG_PARTIAL_SORT_PATHS=1`), not a cost
  loss. `docs/design/0100-0149/m0144-0007-margin-census.md`;
  `analysis/m0144/m0144-0007-margin-census.md` +
  `m0144-0007-margin-*.txt` raw rows.
  Movement: none — recon produced the margin census (measurement only;
  no plan movement expected or claimed).
  Kind: recon
  Parent: M0144-0002
- [x] **M0144-0008 — TPC-DS SF1 cadence capture** (03-forward-plan §5). The
  corpus's goal is SF1 and exactly one SF1 capture exists (P0-E7, match=1/99).
  Take a second SF1 capture on a private lane per G3 and record the
  per-milestone-boundary SF1 convention in the design doc.
  Kind: recon
  Parent: none
  - **DONE 2026-09-20 (loop \#40).** Movement: none — second capture is
    measurement only; no plan movement claimed. G3 lane: `cp -r` clone of
    `bench/tpcds/runtime_goopg/data` (source down, no postmaster.pid) →
    `tmp/m0144-0008-data-sf1` on `:5593` under cgroup `m0144-0008-sf1`,
    HEAD `5fa3c98c9` binary sha256 `6a76d290…`; PG arm `:65438`/`tpcds`
    read-only. **Result `match=1/99` (Q41), headline counts identical to
    P0-E7** — but real category movement: `aggregation-strategy` 72→45
    (−27, consistent with M0141-S2b-11/S2b-13 grouping-order work in the
    38 production commits between `a0e741a68..5fa3c98c9`),
    `sort-strategy` −5, `join-method`/`qual-placement` −4,
    `parameterisation` +6 newly-exposed; 55/99 queries changed category
    sets. Diff-tool drift ruled out (only a comment changed, `44b17d459`).
    Convention pinned: SF1 capture at every milestone boundary + before any
    stocktake (~7 s EXPLAIN-only, private lane). Design doc:
    `docs/design/0100-0149/m0144-0008-tpcds-sf1-cadence.md`; record +
    artifacts `analysis/m0144/m0144-0008-*`. Lane stopped after capture.
- [x] **M0144-0009 — `plan-gate` reframing evaluation** (03-forward-plan §5).
  The gate diffs live `:65433` (binary lags HEAD) vs the newest committed
  snapshot — it can never see staged-code regressions. Evaluate: keep
  re-pinning on a schedule (M0137-0022) vs diff a fresh private-clone HEAD
  build. Decide, then either close or amend M0137-0022's scope in place.
  Also pin the golden-record rule while here (03-forward-plan §5's fourth
  bullet): `bench/tpch/runtime_goopg/tpch-golden-20260919/` is the
  record-level reference for `:65433`; if the cluster is ever reloaded,
  re-dump it first — add the rule to `maintenance_prompts/cluster-ops-runbook.md`
  if absent.
  Kind: recon
  Parent: none
  - **DONE 2026-09-20 (loop \#41).** Movement: none — harness-hygiene
    decision, no plan movement claimed. **Decision: REFRAME.** Verified
    the structural blindness: `plan-gate` diffs live-`:65433` (binary
    `goopg-bin` sha `736aaa57…`, rebuilt `2b8afa538`, 4 `internal/`/`cmd/`
    commits behind HEAD) vs the `m0137-0005-rebaseline-20260915` pin —
    staged code is in neither side, so it detects only server/data drift,
    never code regressions. The reframed path needs no new mechanism:
    `tpch_private_clone_snapshot` (pg_basebackup online clone, R1-allowed)
    + `55xx` lane + `plan-snapshot diff --port` — same pattern M0144-0008
    used for TPC-DS SF1. **M0137-0022 amended in place**: it now lands
    the private-lane capture/diff path AND takes the re-pin through it —
    the "needs owner rebuild of `:65433`" blocker is removed by
    construction. Re-pinning is re-scoped, not eliminated: trigger becomes
    each landed plan-moving change, not a calendar schedule.
    **Golden-record rule added** to
    `maintenance_prompts/cluster-ops-runbook.md` §"Record-level golden
    dump" (was absent): reload ⇒ re-dump a new dated golden dir before
    record-level comparisons are trusted. Design doc:
    `docs/design/0100-0149/m0144-0009-plan-gate-reframing.md`.
- [x] **M0144-0010 — ledger bulk-triage tooling** (03-forward-plan §6). ~2,100
  open `.ralph/deferral_ledger.md` rows. Build tooling that (1) flags rows
  whose referenced code/tests no longer exist (`git log -S` checks), (2)
  folds same-mechanism rows into cluster rows, (3) emits only the survivors
  for per-task triage. This is the M0119-successor cadence mechanism.
  Kind: impl
  Parent: none
  - **DONE 2026-09-20 (loop \#42).** Movement: none — harness tooling; no
    plan movement claimed. Landed `scripts/ledger-triage.py` +
    `make ledger-triage` (`LEDGER_TRIAGE_FAST=1` ~3 s vs ~2.5 min
    attributed; `LEDGER_TRIAGE_OUT`, `LEDGER_TRIAGE_FULL`). Read-only:
    ledger is append-only, so the tool emits a report, never edits rows.
    Pipeline: parse 7-col table (2,296 rows, 2,129 open) → extract
    file/`Test*`/backtick-symbol refs (PG-oracle `.c`/`.h` refs annotated
    but never vote for staleness) → basename→paths existence index +
    `git log -S` attribution for gone refs → classify
    `stale-candidate`/`partial-stale`/`live`/`unverifiable` → cluster by
    most-referenced code file (task-id family fallback; union-find
    rejected — hub files chained 425-row mega-clusters). First report at
    HEAD `f2782479e`: 28 stale-candidates (e.g. `cast_ddl_recovery.go`
    retired by the catalog heap-journaling conversions,
    `scripts/tpcds-sf05-regression.sh` by `e2a50de40`, `join_agg.go` never
    existed), 58 partial-stale, 593 live, 1,450 unverifiable prose-only
    rows survive by default (`stale-candidate` is high-precision, not
    exhaustive). Report: `analysis/m0144/m0144-0010-ledger-triage.md`;
    design: `docs/design/0100-0149/m0144-0010-ledger-bulk-triage.md`.
- [x] **M0144-0011 — first vertical-slice campaign** (03-forward-plan §4).
  **ESCALATED 2026-09-20 (loop \#49) under AGENT.md S4 — five consecutive
  completed descendants reporting `Movement: none`. No further descendant
  is selected or filed; only the owner reopens this root.**
  (For when it is reopened: the `unexpressible` substrate floors the
  slices kept hitting now have filed owners — row-emitting PartialAgg is
  M0141-S3–S6, parallel-inner hash build is M0140-0007, derived-input
  statistics is M0145-0009, parameterized-path legality is M0145-0010.
  **Updated 2026-09-23:** those homes moved — M0146-0003 (adopting
  S3–S6), M0146-0002, M0146-0009, M0146-0011 respectively.)
  - What was tried, and what each step proved:
    - **M0144-0011a** — `inputNodePathkeys` gained an `*Aggregate` arm, so a
      sorted `GroupAggregate` that already emits the ORDER BY order is taken
      as-is. **`Movement: yes` — SF0.25 `sort-strategy` 67 → 60 (−7)**, 16
      plans moved. This is the lineage's one movement.
    - **M0144-0011a-3** — the walk crosses a positional-identity `*Project`
      (PG `pathnode.c:2936-2937`). Q21's first divergence advanced
      `depth=1 sort-strategy` → `depth=1 qual-placement` with the depth-1
      node kind now MATCHING PG. `Movement: none` — the category line counts
      every category a query diverges in anywhere, so Q21 kept its tally.
    - **M0144-0011a-2** — a 99-query ORDERED-seam census; the candidate
      minimum dropped 2 → 1 to match PG's minimum-free pathlist iteration
      (`planner.c:5337`). Proved the gate was SHAPE-inert and that the
      `keys=0` residue is entirely hashed lone candidates — an
      aggregation-strategy question, not an ordering one. `Movement: none`.
    - **M0144-0011b** (recon) — refuted its own filed premise: goopg's Q8
      nested loop (19852.53) is far CHEAPER than PG's (28502.37), not
      dearer. Found the real defect a layer down. `Movement: none`.
    - **M0144-0011b-1** — fixed it: a sub-plan join-search leaf is priced
      from its own subtree (`cost_subqueryscan`'s shape,
      `costsize.c:1491-1493`) instead of a fabricated seq scan. Q8's
      `Hash Join (1.27..11.28)` over a `HashSetOp (…7360.42)` became
      `Hash Join (6458.80..7374.05)`, and **Q8's top join now elects PG's
      Nested Loop** (27215 vs PG's 28502, was a Hash Join at 19455). 30/99
      plans changed, `MISMATCH=0`. `Movement: none` — SF0.25 `join-method`
      70 → 68, inside ±3.
    - **M0144-0011c** (recon) — sized `Materialize`. `Movement: none`.
  - The blocker: every remaining mechanism in this lineage is either
    already correct or too small to clear the ±3 band on its own. The
    lineage is not unproductive in substance — it removed a redundant Sort
    corpus-wide, repaired two false premises by measurement, and fixed a
    650x cost-monotonicity violation that flipped Q8's join method to PG's
    — but it has not moved a category past ±3 since 0011a.
  - **Expected movement if unblocked, with the size:** `Materialize` is the
    one hypothesis left with a claim outside the band — **`missingnode`
    25 → 14 (−11)** on Q1 Q8 Q10 Q16 Q47 Q57 Q59 Q65 Q77 Q78 Q91 (Q35 and
    Q58 also cite `Incremental Sort` and would stay). Size: four slices,
    detailed in `docs/design/0100-0149/m0144-0011c-materialize-sizing.md`
    §4 — (1) plan node + EXPLAIN + `createPlan`, inert; (2) `cost_material`
    + `cost_rescan`'s Material arm as tested pure functions, inert;
    (3) the NL admission rule files the matpath candidate AND
    `join_nl_stream.go` stops wrapping unconditionally — the slice that
    moves plans; (4) re-time the Q54-class `nlInnerWorkMemEnabled` cliff,
    which becomes priceable once (2) lands.
  - Also still open under this root, complete in substance but NOT closed
    because S4 forbids completing another descendant: **M0144-0011b**, whose
    stated completion condition ("the leaf is priced and Q8's rel {0,1,2,3}
    election is re-measured") is already satisfied by M0144-0011b-1's
    committed evidence. Its residue is Q8's remaining `depth=3` join-ORDER
    divergence.
  **Not selectable until M0144-0002 and M0144-0007 have landed.** Take the top
  first-divergence cluster, ONE representative query, and drive it to MATCH
  end-to-end — this task is a recon: it *files* each surfaced layer
  (admission → candidate → cost input → election → executor existence) as a
  `Kind: impl` child (`Parent: M0144-0011`) naming expected movement per S5;
  production changes happen in those children, not here. Ends when the query
  matches or the residue is a named, measured, unfunded capability. Only then
  generalise to the cluster — the depth-first inversion of the era's
  breadth-first mechanism builds.
  Kind: recon
  Parent: M0144-0002
  - **IN PROGRESS 2026-09-20 (loop \#43): slice picked + five-layer trace
    landed.** Representative: **TPC-DS SF0.25 Q8** — top cluster
    (`Limit→{GroupAgg|IncrSort|Sort+CTE}` vs `Sort`, ~25 SF0.25 / ~20 SF1
    records), `dominated-noncost` candm −46.8% (m0144-0007). Trace on a
    private `:5595` lane at HEAD `30e8ea711`
    (`analysis/m0144/m0144-0011-q8-slice-trace.md`): admission OK;
    candidate-generation gap is the first-divergence cause —
    `inputNodePathkeys` returns nil for `*Aggregate` (seed `keys=0` →
    redundant Sort) and `electOrderedGrouping` declines `cands<2(1)`
    while PG iterates the whole input pathlist (`planner.c:5337`); the
    NL-vs-HJ join election under the agg and `Materialize` existence are
    the deeper layers. Children 0011a/b/c filed below. Design doc:
    `docs/design/0100-0149/m0144-0011-q8-vertical-slice.md`.
  - **CLOSED 2026-09-23 — owner disposition (delegated, scope
    M0137–M0145): superseded into M0146, not reopened.** The campaign's
    surviving hypotheses are re-homed, each with its measured size:
    `Materialize` (the only hypothesis with a claim outside the ±3 band —
    `missingnode` 25 → 14, sized in four slices by
    `docs/design/0100-0149/m0144-0011c-materialize-sizing.md` §4) is
    **M0146-0010**; Q8's residual `depth=3` join-ORDER divergence and the
    general join-order class are **M0146-0005**; the
    `unexpressible` substrate floors are M0146-0002/0003/0004. Reopening
    this root would restart a breadth-first campaign whose remaining
    members all have named owners downstream — the milestone boundary is
    the cleaner accounting.
- [x] **M0144-0011a — ordering-claim propagation at the ORDERED-step
  boundary** (filed by M0144-0011). `inputNodePathkeys`
  (`internal/optimizer/upperorderedinput.go:176`) returns nil for every
  ordered-emitting node top that is not `*Sort`/searched-root — add the
  `*Aggregate` emission case first (winning PathAgg's group-key ordering,
  translated to output coordinates; `groupingEmissionPathkeys` already
  computes this for candidate offers), then survey MergeJoin/GatherMerge/
  IncrementalSort tops for the same `default: nil` blind spot. Also review
  `electOrderedGrouping`'s `cands<2` gate: PG's `create_ordered_paths`
  (`postgres/src/backend/optimizer/plan/planner.c:5337`) iterates
  `input_rel->pathlist` with no minimum — a lone AggPath must still be
  offered (PG citations: `planner.c:5344-5348`, `pathnode.c:3412-3416`).
  Kind: impl
  Parent: M0144-0011
  Expected movement: SF0.25 `sort-strategy` category on the
  `Limit→{GroupAggregate|Finalize}` first-divergence family — ≤18 records
  (named dominated-noncost: Q8, Q21, Q26, Q45, Q50, Q62, Q99;
  election-0%: Q17, Q25, Q29) advance past depth-1 where the seed
  ordering was the sole blocker. Measured: private-lane SF0.25 capture →
  `pg-plan-parity-diff.py` category delta + `pg-plan-first-divergence.py`
  census diff vs the m0144-0002 table; match-count movement possible but
  not required.
  **Residual handoff (owner decision 2026-09-20):** the gates this task's
  chain could not remove at node level — `inputNodePathkeys`'s remaining
  `default: nil` tops (`*MergeJoin`/`*GatherMerge`/`*IncrementalSort`/
  `*WindowAgg`), `electOrderedGrouping`'s `gate-precondition` skip when
  `node != agg.node`, `electOrderedDistinct`'s `cands<2`
  (upperordereddistinct.go:140) — are owned by **M0145-0006** (upper-rel
  pathlists), where election happens over candidate sets rather than
  node-top walks.
  - **LANDED 2026-09-20 (loop \#44).** `inputNodePathkeys`' walk gained an
    `*Aggregate` arm — `aggregateEmissionPathkeys`
    (`internal/optimizer/upperorderedinput.go`), the node-level twin of
    `groupingEmissionPathkeys` — which derives a sorted aggregate's
    group-key emission order in OUTPUT coordinates, verified per key
    against the group-prefix layout rather than assumed. PG citations:
    `postgres/src/backend/optimizer/util/pathnode.c:3412-3416`
    (`AGG_SORTED` copies the subpath's pathkeys) and
    `postgres/src/backend/optimizer/plan/planner.c:5337`, `:5344-5348`
    (`create_ordered_paths` reads them per input path). Design doc:
    `docs/design/0100-0149/m0144-0011a-ordered-step-ordering-claim.md`.
    Measured on TPC-DS SF0.25 with `pg-plan-parity-diff.py`: the
    `CATEGORIES-EXCL-MATCH` `sort-strategy` count fell 67 → 60 (−7) while
    match held at 2 → 2 and `missingnode` held at 25.
    - Clean same-epoch A/B (both arms `GOOPG_ANALYZE_SEED=20260905`,
      stats epoch `e4a554b2a4cfb710`, distinct binaries
      `bdb4d6bade614b25` vs `163ba219e018a91a`). The first, unpinned
      attempt read TPC-H `match 1 → 2` — that was the sampling epoch, not
      the code; the pinned pair reads `match 2 → 2` with every TPC-H
      category inside ±3.
    - Plan-shape channel: 16/99 SF0.25 plans moved (Q7 Q8 Q10 Q15 Q17 Q25
      Q26 Q29 Q35 Q37 Q40 Q45 Q50 Q66 Q69 Q82) — seven of the nine named
      queries. Q21, Q62 and Q99 did NOT move and are not explained by this
      slice.
    - Q8 now plans `Limit → GroupAggregate → Sort → Hash Join …`, PG's own
      depth-1/-2 shape; the redundant `Sort` over `GroupAggregate` is gone.
    - Collateral: `TestExplainIndentDeepNesting` and its ANALYZE twin used
      an ASCENDING `ORDER BY 2` fixture whose 4-level shape WAS the
      redundant Sort. Repaired to `ORDER BY 2 DESC` — a genuine
      re-ordering — so the indent assertions are unchanged and the root
      `Sort Key: b DESC` doubles as evidence the new arm carries direction.
    - Gates: units PASS, `tpch-spotcheck` PASS (Q12=2 Q13=33),
      `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`), `tpch-acceptance-arm` PASS (24/24 VALUES vs the HEAD
      baseline). `make ea-ratchet`: `N/A — no estimate/selectivity/stats
      code touched`.
    Kind: impl
    Parent: M0144-0011
    Movement: yes — TPC-DS SF0.25 CATEGORIES-EXCL-MATCH sort-strategy 67 → 60 (−7)
- [x] **M0144-0011a-2 — the remaining `default: nil` ordering tops, and
  `electOrderedGrouping`'s `cands<2` gate** (filed by M0144-0011a). Two
  divergences M0144-0011a reviewed and deliberately did not bundle:
  - `electOrderedGrouping` (`internal/optimizer/upperorderedgrouping.go`)
    declines at `len(cands) < 2`; PG's `create_ordered_paths` iterates
    `input_rel->pathlist` with NO minimum
    (`postgres/src/backend/optimizer/plan/planner.c:5337`), so a lone
    translatable `PathAgg` must still be offered. With M0144-0011a landed
    the single-candidate ordering claim already reaches the ORDERED step
    through the normal `createOrderedPaths` route, so relaxing the gate is
    expected to be near-inert on today's corpus while widening the loop's
    blast radius across every single-candidate grouping query — it needs
    its own measurement, not a free ride on 0011a's.
  - the walk's other `default: nil` TOPS: `*MergeJoin`, `*GatherMerge` and
    `*IncrementalSort` as the input node's top (0011a handles
    `*GatherMerge` only as an aggregate's CHILD). Each needs its own
    output-coordinate soundness argument; bundling them makes a category
    move unattributable.
  Kind: impl
  Parent: M0144-0011a
  - **PREMISE CORRECTED 2026-09-20 (loop \#45), by probe.** A
    `GOOPG_PGSHAPED_DP_TRACE=1` run on a private `:5595` SF0.25 lane
    showed that NONE of the three queries this task was filed for is
    blocked by either bullet above:
    - Q21 declines with `reason=gate-precondition`, not `cands<2(1)`, and
      its walk then stops at a renaming `*Project` over the aggregate
      (`DPWALK stop-at=*optimizer.Project`). That mechanism is filed and
      LANDED as **M0144-0011a-3** below; Q21 is spent.
    - Q62 and Q99 plan a `Finalize HashAggregate`. A hashed aggregate
      emits no order, so no ordering-claim work can move them — their
      divergence is `aggregation-strategy` and belongs to that lineage.
    - The two bullets above therefore remain genuinely UNMEASURED: they
      have no named query left from the 0011a residue, and the first job
      of whoever selects this task is to find one (or to retire the task).
  Expected movement: **was withdrawn pending a named query**; the census
  below supplied the answer and it is `Movement: none` — see the
  RESOLVED block.
  - **RESOLVED 2026-09-20 (loop \#46) — census first, then one bullet
    implemented and one retired.** Design doc:
    `docs/design/0100-0149/m0144-0011a-2-ordered-seam-candidate-minimum.md`;
    census: `analysis/m0144/m0144-0011a-2-ordered-seam-census.md`.
    Kind: impl
    Parent: M0144-0011a
    Movement: none
    - The 99-query ORDERED-seam census (private `:5595` lane,
      `GOOPG_PGSHAPED_DP_TRACE=1`, per-query byte-offset log reads) splits
      the corpus by `electOrderedGrouping` verdict: `gate-precondition`
      44, `cands<2(1)` 37, `parallel-finalize-agg-present` 4, elected 14.
    - Of the 37 `cands<2(1)` declines, 27 ALREADY recover their ordering
      claim at the ORDERED step via `inputNodePathkeys` (17 with
      `contained=true`, i.e. no Sort stacked at all; 10 with a partial
      claim). Only 11 arrive with `keys=0`.
    - Those 11 (`Q5 Q14 Q16 Q18 Q22 Q27 Q77 Q80 Q92 Q94 Q95`) are EXACTLY
      the 11 that decline `anyTranslated=false` in the relaxed arm, and
      every per-candidate decline in the corpus reads
      `strategy-or-mode strategy=0` — a HASHED lone candidate, which emits
      no order. They belong to the `aggregation-strategy` lineage, not to
      ordering-claim propagation.
      - Bonus: that identity is a corpus-wide measurement of the
        sibling-agreement invariant — `groupingEmissionPathkeys` (Path
        twin) and `aggregateEmissionPathkeys` (Node twin) decline on the
        same query set, query for query.
    - **Bullet 2 RETIRED by measurement**: the `*MergeJoin`/`*GatherMerge`/
      `*IncrementalSort` walk tops have NO query on this corpus. Ledger row
      keeps the divergence from being lost.
    - **Bullet 1 LANDED**: `len(cands) < 2` → `len(cands) < 1`. PG has no
      minimum — `create_ordered_paths` iterates the whole pathlist
      (`postgres/src/backend/optimizer/plan/planner.c:5337`). Pinned by
      `TestElectOrderedGroupingOffersALoneCandidate` and
      `TestElectOrderedGroupingStillDeclinesAnEmptyRel`.
    - Result: SHAPE-inert on both corpora — cost-stripped plan diff is 0
      lines for all 99 SF0.25 and all 22 TPC-H queries, parity match 2 → 2
      and every `CATEGORIES-EXCL-MATCH` count identical. 14 queries (10
      SF0.25 + 4 TPC-H) reprice their ORDER BY `Sort` through
      `addOrderedPaths` instead of the prebuilt seed's
      `DeriveLegacyDisplayCost`, which is the direction the ORDERED rel was
      built for.
    - **METHODOLOGY CORRECTION, recorded deliberately**: the private
      `:5595` lane reported ZERO plan changes; the gate-owned `:65437`
      cluster reported ten. The lane is a stale clone with its own
      statistics — sound for MECHANISM traces, unsound for OUTCOME claims.
      An earlier draft comment asserting "byte-identical either way" on
      lane evidence was corrected before landing.
    - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
      `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`); `tpch-acceptance-arm` PASS (24/24 VALUES);
      `make ea-ratchet` `N/A`. All re-run after a late comment-only edit,
      since the stamp hashes the staged `internal/` tree.
    - Left open, deliberately NOT filed as children here (neither is an
      ordering-claim problem — filing them here would repeat this task's
      own predecessor's mistake): `gate-precondition` is now the largest
      decline class (44/99) and is entirely unexamined; the 11 hashed-lone-
      candidate queries are an aggregation-strategy election question.
- [x] **M0144-0011a-3 — crossing a positional-identity `Project` at the
  ORDERED seam** (filed by M0144-0011a-2's probe). `inputNodePathkeys`
  (`internal/optimizer/upperorderedinput.go`) stopped at every `*Project`,
  so TPC-DS SF0.25 Q21 — whose ordered input is `Project{Aggregate}` doing
  nothing but renaming the two aggregate outputs to `inv_before`/
  `inv_after` — never reached the `*Aggregate` arm M0144-0011a added.
  Landed: the walk now descends through a Project that is a POSITIONAL
  IDENTITY (`projectIsPositionalIdentity`: target `j` is
  `ColumnRef{Index: j}` for every `j`, equal widths, not `IsolatedScope`)
  and restamps the claim's labels at the SAME index
  (`relabelPathkeysTo`). PG citation:
  `postgres/src/backend/optimizer/util/pathnode.c:2936-2937` — "Projection
  does not change the sort order", `create_projection_path` copies
  `subpath->pathkeys` — which PG may say for ANY projection only because a
  PG pathkey names an EquivalenceClass; goopg's names a POSITION, so only
  the identity case ports and everything else still stops the walk. Design
  doc: `docs/design/0100-0149/m0144-0011a-3-identity-project-descent.md`.
  Kind: impl
  Parent: M0144-0011a
  Movement: none
  - Result the first-divergence census DOES see: Q21 advances
    `depth=1 [sort-strategy] under Limit: PG GroupAggregate | goopg Sort`
    → `depth=1 [qual-placement] under Limit: PG GroupAggregate | goopg
    GroupAggregate`. The depth-1 node kind now MATCHES PG and the residue
    is the HAVING qual's placement.
  - Why it is still `Movement: none`: S3's three instruments are the match
    count, `CATEGORIES-EXCL-MATCH` beyond ±3, and `ea-ratchet`. SF0.25
    reads match 2 → 2 and `sort-strategy` 60 → 60 — because that line
    counts every category a query diverges in ANYWHERE, and Q21's inner
    `Sort` still differs in strategy, so Q21 keeps its tally while also
    gaining `qual-placement` (24 → 25). Calling the census advance
    "movement" would be instrument-shopping.
  - Blast radius: 1/99 SF0.25 plans changed (Q21 only). TPC-H parallel is
    byte-identical to the 0011a capture
    (`sha256 1727126e8ed71456`) from a DIFFERENT binary
    (`7372bbf60b170386` vs `163ba219e018a91a`) — G3's proof that the
    equality is inertness on that corpus, not a re-measured binary.
  - Deferred inside the same mechanism: a NARROWING identity Project
    (`len(out) < len(child)`) is sound in principle — positions
    `0..len(out)-1` are still an identity — and is refused only because
    the walk's per-step agreement check is a column-count test with no
    "prefix" mode. Ledger row filed.
  - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
    `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
    TIMEOUT=0`); `tpch-acceptance-arm` PASS (24/24 VALUES);
    `make ea-ratchet` `N/A — no estimate/selectivity/stats code touched`.
- [x] **M0144-0011b — join election under Q8's aggregate input**
  (filed by M0144-0011). At rel `{0,1,2,3}` ({dd⋈ss} ⋈ {store,ca-view}):
  goopg elected `join.hash total=19455.50`; the PG-chosen
  `join.nestloop total=19852.53` was generated and dominated — a
  cost-input/election divergence, not generation. PG's inner is
  `Materialize(NL(store Index Scan ⋈ ca-view-subquery))` rescanned per
  outer row; goopg prices a different inner shape (Seq Scan store, no
  Materialize). Scope: reprice the parameterised NL inner PG-faithfully
  and verify the stats inputs (818 vs 812 rows). If the residual needs
  Materialize, it feeds 0011c rather than a workaround.
  Kind: recon
  Parent: M0144-0011
  Expected movement: `join-method`, `scan-type`, `join-order` categories
  on Q8 (SF0.25) — carried by the impl child M0144-0011b-1 below.
  Measured: DPPATH candidate margins at rel {0,1,2,3} (nestloop vs
  join.hash totals vs PG's own pricing) + `pg-plan-parity-diff.py` on a
  private-lane capture.
  - **RECON COMPLETE 2026-09-20 (loop \#47); PREMISE CORRECTED; this task
    is now BLOCKED on M0144-0011b-1.** Design doc:
    `docs/design/0100-0149/m0144-0011b-nontable-leaf-pricing.md`; evidence:
    `analysis/m0144/m0144-0011b-q8-dppath-head.txt`,
    `m0144-0011b-q8-plan-head.txt`.
    Movement: none
    - The filed premise ("goopg prices the NL too dear") is FALSE. At HEAD
      `97eceacfc`, goopg's `join.nestloop` at rel {0,1,2,3} is 19852.53 and
      its `join.hash` 19455.50 — while **PG's own NL for the same join is
      28502.37**. goopg prices the nested loop far too CHEAP, not too dear.
      Repricing it upward as filed would have been tuning on a false
      premise (AGENT.md R6).
    - The real divergence is one layer down and is visible in the EXPLAIN
      output: a `Hash Join (cost=1.27..11.28 rows=32)` sits directly above
      a `HashSetOp Intersect (cost=6457.53..7360.42 rows=535)` — the
      parent is 650x cheaper than its own child. PG's corresponding inner
      costs 9327.62.
    - Root cause: `internal/optimizer/joinsearch.go:437` prices EVERY
      join-search leaf with `costSeqscan`, and `baseSeqScanCostInputs`'
      non-table branch INVENTS a page count from the row count. A set-op /
      CTE / subquery / VALUES / function-scan leaf therefore enters the
      search with its whole subtree free:
      `joinsearch.prebuilt relids={3} rows=535 total=8.35`.
    - PG starts from the subpath's cost instead —
      `postgres/src/backend/optimizer/path/costsize.c:1491-1493`
      (`cost_subqueryscan`), with `cost_ctescan` / `cost_functionscan` the
      same shape.
    - `Materialize` (M0144-0011c) is NOT what decides this election: PG's
      Materialize wrapper adds ~65 over its 9327.62 child, while the
      missing leaf cost is ~7350.
    - Blast radius: 32/99 SF0.25 queries join over a non-table leaf (Q1 Q2
      Q4 Q5 Q8 Q11 Q14 Q23 Q24 Q30 Q31 Q33 Q38 Q39 Q47 Q51 Q54 Q56 Q57 Q58
      Q59 Q60 Q64 Q74 Q75 Q77 Q78 Q80 Q83 Q87 Q95 Q97).
    - NOT landed this loop on purpose: most goopg sub-plan nodes (`SetOp`
      among them) carry no real `Path` cost, only
      `DeriveLegacyDisplayCost`, whose header states "this is NOT a cost
      model and nothing may plan against it". Choosing how to price them
      is a design decision with a 32-query blast radius; see the child.
- [x] **M0144-0011b-1 — price a non-table join-search leaf from its own
  subtree, not as a sequential scan** (filed by M0144-0011b's recon).
  `internal/optimizer/joinsearch.go:437` + its partial twin
  (`addBaseRelPartialPaths`): when `leafBaseScan(leaf)` is not a base heap
  scan, the leaf's cost must derive from the sub-plan it wraps, the way
  `cost_subqueryscan` derives from `subpath`
  (`postgres/src/backend/optimizer/path/costsize.c:1491-1493`), instead of
  `costSeqscan` over a page count invented from the row count.
  Kind: impl
  Parent: M0144-0011b
  Expected movement: `join-method`, `join-order` and `scan-type` on the 32
  SF0.25 queries that join over a non-table leaf, Q8 first (its inner is
  priced 11.28 against PG's 9327.62). Measured: `pg-plan-parity-diff.py`
  `CATEGORIES-EXCL-MATCH` + match count on a gate-cluster SF0.25 capture,
  and the DPPATH margin at Q8's rel {0,1,2,3} (`join.nestloop` 19852.53 vs
  `join.hash` 19455.50 today) against PG's 28502.37.
  - **Design decision required before coding** — three options, stated in
    the recon doc §6, NOT interchangeable:
    - (1) use the leaf's carried `PlanCost` where it has one (a searched
      subtree does) and fall back otherwise — correct where it applies,
      but leaves `SetOp`/CTE leaves, i.e. Q8, on the legacy number;
    - (2) give the sub-plan classes real upper-rel paths (`cost_subqueryscan`
      / `cost_ctescan` ports with genuine inputs) — the PG-faithful answer
      and the larger piece of work;
    - (3) plan against `DeriveLegacyDisplayCost` for these leaves —
      fastest, and it directly contradicts that function's stated scope
      rule (`internal/optimizer/plancost.go:116`).
  - Whichever is chosen, the choice and its PG citation go in the design
    doc BEFORE any parity number is taken (AGENT.md C3).
  - **LANDED 2026-09-20 (loop \#48).** Design doc:
    `docs/design/0100-0149/m0144-0011b-1-subplan-leaf-cost.md` (its §3
    decision was written before any parity number was taken).
    Movement: none
    - **Decision taken: a narrowed option (2)** — port `cost_subqueryscan`'s
      SHAPE (`postgres/src/backend/optimizer/path/costsize.c:1491-1493`) for
      sub-plan leaves ONLY: `startup = subtree.startup`,
      `total = subtree.total + cpu_tuple_cost*rows`
      (`costSubplanLeaf`/`isSubplanLeaf`, internal/optimizer/joinsearch.go).
      The qual term PG adds is deliberately NOT charged — goopg's leaf is a
      finished tree whose local filter is already inside the node priced, so
      charging it again would double-count.
    - Index and bitmap leaves are LEFT on today's pricing on purpose, so any
      category movement is attributable to sub-plan leaves alone. Ledger row
      filed; they are mispriced too, by a different mechanism
      (`cost_index`).
    - The defect is closed: Q8's `Hash Join (cost=1.27..11.28)` over a
      `HashSetOp Intersect (cost=6457.53..7360.42)` is now
      `Hash Join (cost=6458.80..7374.05)` — parent above child again.
    - **Q8's top join now elects PG's method**: `Nested Loop
      (9934.68..27215.31)` where it had a `Hash Join (3487.55..19455.50)`,
      against PG's own `Nested Loop (12329.66..28502.37)`. First divergence
      advances `depth=3 [join-method] PG Nested Loop | goopg Hash Join` →
      `depth=3 [join-order] PG Nested Loop | goopg Nested Loop`.
    - SF0.25: match 2 → 2, `join-method` 70 → 68 (the named category, right
      direction), `qual-placement` 25 → 26, `missingnode` 25 unchanged.
      30/99 plans changed — the expected size, since 32 queries join over a
      sub-plan leaf. All deltas inside ±3, hence `Movement: none`.
    - TPC-H parallel: parity lines and the plans capture BYTE-IDENTICAL to
      HEAD (`sha256 0bf7e4d1540cf0e4`) from a different binary
      (`e1bb094f5aeae53f` vs `42dfcd6069014ad2`) — G3's proof of inertness
      rather than a re-measured binary. No TPC-H query joins over a
      sub-plan leaf.
    - Three queries whose plans changed got slower (Q11 3s→7s, Q58 2s→5s,
      Q95 3s→6s); Q28's 3s→6s is host noise — its plan did not change.
      Ledger row filed. Per AGENT.md §Goal a slower plan that matches is
      not a regression here.
    - Gates: units PASS; `tpch-spotcheck` PASS (Q12=2 Q13=33);
      `tpcds-sf025 sweep` PASS (`MISMATCH=0 CKMISMATCH=0 ERROR=0
      TIMEOUT=0`) — the load-bearing gate here, since 30 plans changed and
      every one still returns the same rows and checksums;
      `tpch-acceptance-arm` PASS (24/24 VALUES); `make ea-ratchet` `N/A`.
    - Still open (both ledgered): index/bitmap leaf pricing, and the fact
      that for a class with no `PlanCost` field (`SetOp` among them) the
      base cost is still `DeriveLegacyDisplayCost` — a FLOOR built from the
      children's own largely-real costs, not PG's number. Full option (2),
      a real upper-rel path per sub-plan class, owns that.
- [x] **M0144-0011c — `Materialize` node existence** (filed by
  M0144-0011). Q8 is `MISSING-NODE: PG-only kinds: Materialize` — goopg
  has no Materialize plan node (`MaterializedCTEScan` is a different
  thing); PG buffers NL inners via `create_material_path`
  (`postgres/src/backend/optimizer/util/pathnode.c:1637`). Scope: the
  path producer (materialize_inner admission for rescan-heavy NL
  inners), the executor node, and EXPLAIN rendering. If it turns out
  unfundable, record it as the slice's named measured residue.
  Kind: impl
  Parent: M0144-0011
  Expected movement: `missingnode` count on the Q8-family records where
  PG materializes NL inners (Q8 + siblings per parity diff), and it
  unblocks 0011b's honest NL pricing. Measured: `pg-plan-parity-diff.py`
  `PG-only node kinds` line on a private-lane capture.
  - **RECON COMPLETE 2026-09-20 (loop \#49) — SIZED, NOT BUILT.** Design
    doc: `docs/design/0100-0149/m0144-0011c-materialize-sizing.md`.
    Kind: recon
    Parent: M0144-0011
    Movement: none
    - **The executor half already exists and already runs.**
      `internal/executor/operators_material.go` (308 lines) is a faithful
      `nodeMaterial.c` analogue, and
      `internal/executor/join_nl_stream.go:108` wraps EVERY streaming
      nested-loop inner in it — unbounded by default
      (`nlInnerWorkMemEnabled` off). goopg does not lack materialization.
    - Missing are exactly three things: the plan node EXPLAIN prints, the
      cost (`cost_material` + `cost_rescan`'s Material arm), and PG's
      admission rule.
    - **Sized: `missingnode` 25 → 14 (−11).** 13 of the 25 records cite
      `Materialize` (Q1 Q8 Q10 Q16 Q35 Q47 Q57 Q58 Q59 Q65 Q77 Q78 Q91);
      Q35 and Q58 also cite `Incremental Sort`, so they stay
      `MISSING-NODE` regardless. That is the only remaining hypothesis in
      this lineage with a claim outside ±3.
    - `Incremental Sort` (14 records) is NOT a missing node — goopg has
      `*IncrementalSort` and `addIncrementalSortPaths`, gated off because
      the candidate loses on cost. That is banner item 6.
    - **Trap the implementer must not miss:** goopg materializes EVERY NL
      inner; PG materializes only the ones its admission rule admits
      (`postgres/src/backend/optimizer/path/joinpath.c:1890-1901` +
      `ExecMaterializesOutput`, `execAmi.c:640-647`). Emitting the node
      wherever the executor materializes would create goopg-ONLY
      `Materialize` nodes and trade 11 `missingnode` records for a new
      mismatch class. Placement must follow PG's rule and the executor's
      unconditional wrap must become conditional in the same slice.
    - Not built this loop because it is a multi-slice build with no honest
      partial landing: a new optimizer node type must be threaded through
      `createPlan`, EXPLAIN, a new `PathKind`, the cost model, the NL
      producer and its partial twin, and several exhaustive node-kind
      switches. Four slices are proposed in the design doc §4 and repeated
      in the root's escalation block.
    - Gates: none — recon, no production code changed (AGENT.md C1/D5).


## M0145 — Jointree-first planner flow (filed 2026-09-20, owner decision)

**Milestone doc:** `docs/milestones/0145-jointree-first-planner-flow.md`.
**Harness (binding, read before selecting):** `AGENT.md` §"Plan-parity
harness (M0137–M0145)" — note G8's dual-pipeline rule, new for this
milestone. **Source:** the plan-flow comparison
(`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/plan-flow-medium-abstraction.md`)
+ the feasibility note (`tmp/planner-rewrite-possibility260920.md`).
**Prerequisites:** none at entry; the in-task `Parent:` edges and the
banner's written order give the sequence. Order: `.ralph/fix_plan.md`
banner **item 3**.

Unifies goopg's planning *route* with PG 18.3's, per the medium-abstraction
comparison in
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/plan-flow-medium-abstraction.md`
and the rewrite-feasibility note `tmp/planner-rewrite-possibility260920.md`.
The strategy is **grow the seam, never rewrite in place**: the existing
`tryPGShapedJoinSearch` lattice (RelOptInfo/pathlist/partial-pathlist/DPPATH)
is reused; what changes is its INPUT (a jointree-level IR instead of a
lowered node subtree) and its SPAN (whole statement instead of the FROM
chain). The new pipeline is selectable behind `GOOPG_JOINTREE_PIPELINE=1`
(default off) until M0145-0008's cutover deletes the legacy one — corpus
gates keep running on the default pipeline the whole time.

**Hard constraint on every task:** Q78's `outer-over-derived` firewall must
not weaken (owner constraint, carried from M0142-0008a-3i-route-a).

**Absorbed walls** (do not work them separately): the resolver lowers every
EXISTS/IN body via `planSelectWithParent` before unnest/search ever run
(`planner.go:1499`→`planExistsExpr`; recon:
`docs/design/0100-0149/m0142-0008a-3i-lateral-route-recon.md`), so
M0144-0003a, M0144-0003b's residual, M0142-0008a-3(i)/(ii) and
`-3i-lateral-route` all block on this milestone's 0003.

- [x] **M0145-0001 — recon: the jointree-level IR and the lowering
  contract** (design the representation the whole milestone builds on).
  **DONE 2026-09-21.**

  > ## ESCALATION 2026-09-24 (ralph2 loop \#49) — lineage budget exhausted after the flip, OWNER DECISION NEEDED
  >
  > The lineage guard refused three new descendants filed by the flip: the
  > last five completed ones — M0145-0004, M0145-0026, M0145-0026a,
  > M0145-0029, M0145-0030 — all carry `Movement: none`. Root marked `[!]`;
  > the loop selects elsewhere until the owner answers.
  >
  > - **Attempted:** the M0145-0008 flip LANDED
  >   \(`docs/design/0100-0149/m0145-0008-cutover-flip.md`; code in
  >   `ddb4eabd4`, swept by a concurrent pathspec\-less commit; NULL\-key
  >   prerequisite `eb0e7d388`\).
  > - **What it proved:** the jointree default is value\-identical \(sweep
  >   96/96, acceptance identical, fire set clean\) and at the parity floors
  >   \(TPC\-H 2/22 = baseline, SF0.25 2\), with every category within the
  >   ±3 band — `Movement: none`, as the loop \#5 escalation predicted.
  > - **The three would\-be descendants \(held here, each with a ledger row\):**
  >   - **\(0008b, recon\)** TPC\-H Q18 1.6x / Q22 3.2x slower on the new
  >     default \(one binary: Q18 12.31 s → 20.19 s, Q22 0.48 s → 1.52 s;
  >     neither the estimator fixes nor the NULL\-key guard cause it\); both
  >     SHAPE\-DIFF vs PG on both arms. Expected movement: none by itself.
  >   - **\(0008c, recon\)** ea\-ratchet NEW
  >     `Q95:cte:ws_wh+customer_address+date_dim+web_sales+web_site` \(Hash
  >     Semi Join est 1 / actual 22, `pg_est` null\), introduced by the flip;
  >     G4 permits no repin without PG\-shared evidence. Expected movement:
  >     ea\-ratchet NEW 1 → 0.
  >   - **\(0008d, impl\)** grouping ignores ORDER BY's DESC direction \(PG:
  >     GroupAggregate over one `Sort … DESC`\). Expected movement:
  >     aggregation/sort\-strategy on `GROUP BY k … ORDER BY k DESC` shapes.
  > - **Remaining in M0145-0008:** the legacy\-deletion slices \(delete
  >   `planSelectLegacyPipeline`, legacy\-only machinery with the group\-L
  >   tests, the seam guards on 0001's retired list, the knob\), then the
  >   fire\-set gate's HEAD\-vs\-staged redesign. Several loops. By
  >   construction also `Movement: none` \(deletion changes no default plan\).
  > - **Owner options:** \(a\) re\-pin `LINEAGE-BASELINE` \(the five above\)
  >   and GO on the deletion slices plus 0008b/c/d; \(b\) GO on 0008b/c/d
  >   only, deferring legacy deletion; \(c\) hold item 3 and continue with
  >   item 4 \(M0141\-S2a\-fix2r\).
  >
  > **OWNER ANSWER 2026\-09\-24 \(delegated\): option \(a\) — re\-pin, GO on
  > 0008b/c/d \(ordered 0008b → 0008c → 0008d\) and on the legacy\-deletion
  > slices, PLUS a bounded exemption:** while any M0145\-0008
  > legacy\-deletion slice is open the loop may self\-append completed
  > M0145\-0001 descendants to this root's `LINEAGE\-BASELINE` line \(see
  > the banner's third re\-pin note\). The five pinned are pre\-flip
  > completions — the instrument\-artefact class the 09\-23 re\-pin already
  > adjudicated; post\-flip, instruments see the default arm \(M0146\-0002's
  > `Movement: yes` proved it\). The three held items are filed as
  > **M0145\-0008b / 0008c / 0008d** below.

  > ## ESCALATION 2026-09-23 (ralph2 loop \#5) — lineage budget exhausted again, OWNER DECISION NEEDED
  >
  > The lineage guard refused two new descendants: the last five completed
  > ones — M0145-0004a, M0145-0005, M0145-0007, M0145-0027, M0145-0028 —
  > all carry `Movement: none` (the `LINEAGE-BASELINE` re-pin is spent).
  > Root marked `[!]`; the loop selects elsewhere until the owner answers.
  >
  > - **Attempted since the last GO:** 0005/0007 closed by measurement
  >   \(slice 5: 0 key drift on 670 joins; M0145\-0026 filed\); the
  >   M0145\-0008 executor\-capability inventory \(unexecutable set EMPTY\);
  >   M0145\-0027 \(knob Q20 3.80 s → 0.18 s, arm gap 1.06x → 1.01x\);
  >   M0145\-0028 \(knob SF0.25 floor match Q41 restored, 1 → 2\).
  > - **What each step proved:** the knob arm is value\-identical on both
  >   corpora, timing\-neutral \(1.01x\) and at the parity floor \(SF0.25 2
  >   = default, TPC\-H 2/22 = default\). Each is `Movement: none` BY
  >   CONSTRUCTION: the instruments measure the default arm, which none of
  >   these touches until the flip.
  > - **Remaining blocker to the flip**
  >   \(`docs/design/0100-0149/m0145-0008-flip-test-triage.md`\): a local
  >   flip fails 54 unit tests — 31 legacy\-machinery tests pinned
  >   \(`3b8b771f6`\), 2 state the default \(flip commit\), and two groups
  >   that would have been filed as descendants: \(I\) ≥10 tests where the
  >   one\-relation search misses an index path the rule bypass produced
  >   \(`TestSAOPWithConjunctMoves`, executor index/IOS tests\) — measure
  >   generated\-vs\-out\-costed, close real gaps; \(B\) 11 behavioural tests
  >   \(grouping strategy, nested scalar subquery, NLI semi/anti election,
  >   hashed\-IN SubPlan, two EXPLAIN expectations — one already classified
  >   stale/PG\-faithful\). Plus a script audit: the SF0.25 sweep and the
  >   TPC\-H estimate\-audit arm default the knob to `0` and must change in
  >   the flip commit.
  > - **Expected movement if unblocked:** the flip is the first commit in
  >   this lineage that CAN move the instruments — the knob arm's category
  >   counts become the default's. Honest size: the arms are within ±3 on
  >   every category today \(e.g. TPC\-DS SF0.25 `join-order` 90 knob vs 91
  >   default\), so the flip's own movement is small; its value is retiring
  >   the dual pipeline \(the deletion slice\).
  > - **Remaining size:** groups I \+ B \(≈21 tests, 1–3 loops\), the flip
  >   commit with full gates \(1 loop\), legacy deletion \(several loops\).
  > - **Owner options:** \(a\) re\-pin `LINEAGE-BASELINE` and GO groups
  >   I/B then the flip; \(b\) accept a flip commit that pins the group I/B
  >   tests to legacy and files them as post\-flip defects; \(c\) hold the
  >   cutover.
  >
  > **OWNER ANSWER 2026-09-23 (delegated): OPTION (a) — re-pin done, GO
  > on groups I/B, then flip.** `LINEAGE-BASELINE` re-pinned with the five
  > spent descendants (banner). Groups I/B are filed as **M0145-0029** and
  > **M0145-0030**; both land BEFORE the flip commit. Option (b) is
  > rejected: the group-I class (one-relation index paths the rule bypass
  > produced and the search loses) is exactly the route-borne bypass-loss
  > defect that regressed Q17/Q20 — flipping over known coverage gaps
  > would put them on the DEFAULT arm. Option (c) is rejected: holding
  > preserves the dual pipeline indefinitely while the measured evidence
  > (value-identical on both corpora, timing-neutral, parity-floor-equal)
  > says the knob arm is ready modulo I/B. The loop now resumes the
  > cutover chain: M0145-0029 → M0145-0030 → M0145-0008 flip slice.

  > **OWNER ANSWER 2026-09-22: GO — CONTINUE.** Root re-opened by
  > owner directive. Per the answer the measured downstream walls
  > sequence first (M0145-0009, then M0145-0019 NL-costing recon→impl,
  > then M0145-0020 `examine_simple_variable` — 0012's prerequisite),
  > then the flow-completion chain (0004 → 0005 → 0007 → 0008)
  > resumes. The deferred-scope note below stays accurate — neither
  > arm there has a firing census bucket.

  > ## ESCALATION 2026-09-22 (loop \#60) — lineage budget exhausted, OWNER DECISION NEEDED
  >
  > The lineage check refused two newly-filed descendants of this root
  > because the last five COMPLETED descendants (M0145-0006, M0145-0014,
  > M0145-0015, M0145-0016, M0145-0017) all carry `Movement: none`. Per its
  > own instruction this root is marked `[!]` and the loop selects
  > elsewhere. Only the owner re-opens it. Writing it up honestly:
  >
  > **What was attempted.** The jointree-first pipeline behind
  > `GOOPG_JOINTREE_PIPELINE=1`: an IR built before lowering (0001/0002),
  > sublink pull-up into it (0003, incl. the EXISTS and ANY arms), then a
  > run of targeted follow-ups the census named — seam admission for pulled
  > CTE leaves (0013), nested-sublink recursion (0014), residual declines
  > (0015), `semianti-not-tail` (0016), never-reach census (0017), firewall
  > relaxation (0018).
  >
  > **What each step proved.** 0003 moved real internal counters
  > (`(pulled)` 9 -> 27, `InExpr` 34 -> 1, seam `leaf-count` 26 -> 10,
  > `D1-sublink` 8 -> 5) — the mechanism works. But 0014/0015/0016/0017
  > each closed either measured-no-gap or with movement that did NOT reach
  > one of S3's three instruments, and 0018 was a hard NO-GO (SF1 Q78 29 s
  > -> >1800 s). The pattern across five tasks is consistent: the pull-up
  > mechanism is correct and reaches more sublinks, yet plan PARITY does
  > not move.
  >
  > **The remaining blocker, stated as a claim the owner can test.** The
  > binding constraint is no longer the jointree/pull-up front end — it is
  > downstream of it. Two specific walls, both already measured:
  >   1. **B-06, CTE-output statistics** (filed as the unchecked
  >      M0145-0009). 15 of the 60 pull-up census fires are ANY bodies whose
  >      FROM is a CTE reference; they cannot be admitted without pushing a
  >      statistics-free input into the DP, which the Q78
  >      `outer-over-derived` firewall exists to prevent (C-04a: 15 s ->
  >      327 s TIMEOUT).
  >   2. **The COST MODEL**, which is what M0145-0018 ran into: relaxing the
  >      firewall produced legal plans that cost out catastrophically. That
  >      escalation is also still unanswered.
  >
  > **Expected movement if unblocked.** Landing B-06 would make the 15
  > CTEScan-leaf bodies admissible and is the named re-evaluation trigger
  > for the `outer-over-derived` decline family. Nobody has demonstrated
  > that this converts into a `match`-count change, and on this lineage's
  > record that should be measured before more is invested.
  >
  > **Remaining size.** M0145-0004, -0005, -0007, -0008, -0009, -0010 are
  > still `[ ]` under this root — including the 0008 cutover that deletes
  > the legacy pipeline. This is a large remaining commitment, which is
  > exactly why the budget signal is worth honouring now rather than after
  > another five tasks.
  >
  > **Deferred scope that would otherwise have been filed as tasks** (filing
  > them was refused; recording it here so it is owned, not lost): the
  > **opaque-body arm** (non-flat bodies as semi/anti citizens carrying
  > their planned Node; needs param-exec machinery inside the problem) and
  > **outer-local-only correlation + ANTI outer-local quals** (no legal slot
  > today — the link carries no join-clause-only qual channel; they hoist
  > fine under SEMI). Both have **zero measured demand** as of 2026-09-22:
  > no census bucket fires for them on TPC-DS SF0.25, and TPC-H is fully
  > inert for this mechanism (`match=22 shapediff=0`). Neither should be
  > built without re-running the census and naming a firing bucket first. Design doc
  `docs/design/0100-0149/m0145-0001-jointree-ir-and-lowering-contract.md`
  (indexed). Decisions: (i) IR = `jtLeafRef`/`jtFromExpr`/`jtJoinExpr`/
  `jtAppendRel` over a per-statement leaf table (`jtScope`), flat
  `ColumnRef.Index` space kept with spans assigned at construction
  (append-only); `SpecialJoinInfo` carried as-is; `OuterColumnRef` re-base
  = Level decrement on splice, `Level:1`→`ColumnRef`, fail-closed when
  `(Index,SourceTableIdx)` doesn't resolve to an emitting leaf in the
  destination scope (Q78 firewall preserved). (ii) Resolution runs ON the
  IR — leaf identity precedes binding (PG: rtable precedes `Var`);
  `resolveExpr` loses its sublink-planning arms, bodies get bound-but-
  unplanned provisional scopes, an `SS_process_sublinks` analogue plans
  survivors post-pull-up. (iii) Lowering contract inventories ~40
  incrementally-established state items, each assigned an
  IR-CON/BIND/PULL/SEARCH/UPPER/LOWER/TAIL owner; `createPlanNode` stays
  the single funnel. (iv) Per-task retirement matrix 0002–0008 in the
  doc. Movement: none (recon; no production file touched).
  Kind: recon
  Parent: none
  Inputs: the plan-flow doc (D1/D2/D4/D6), the lateral-route recon (the
  wall is the resolver, not the phases), route-a step 1's landed
  `ExistsExpr.Subquery`/`InExpr.Subquery` retention and
  `sublinkBodyIsSimple` (`sublinkpullup.go`, port of `is_simple_subquery`
  prepjointree.c:1807). Deliverables in the design doc: (i) IR entry kinds
  — base-rel, semi/anti (SJInfo-equivalent: min lefthand/righthand relsets
  + join quals), appendrel (child list for UNION ALL flattening), with
  OuterColumnRef re-basing semantics spelled out; (ii) where the IR is
  built — it must precede `resolveExpr`'s sublink planning
  (planner.go:1499), so define whether resolution runs on the IR or the
  IR derives from resolved-but-unplanned subtrees; (iii) the Path→Node
  lowering contract — enumerate every piece of state the node-tree stages
  currently establish incrementally (neededCols, outputCols, OuterColumnRef
  remaps, rowmarks, CTE cache semantics, `rewriteJoinsToNLI`,
  `stampSemiProbePrices`, SRF/ProjectSet, set-op handling) and assign each
  a lowering-site owner; (iv) the retirement list — which seam guards
  (leaf-count, lateral, pinned-spine, Phase A/B, `admitSemiAnti`,
  `runJoinSearchBelowPinned`) become dead at which task.
  Kind: recon
  Parent: none
- [x] **M0145-0002 — dual-pipeline measurement harness** (the transition
  must be measurable while the default pipeline stays the value-gated one).
  `GOOPG_JOINTREE_PIPELINE=1` selects the new pipeline on private lanes;
  value gates (`tpcds-sf025 sweep`, `tpch-spotcheck`, acceptance arms)
  always run the default; plan-parity captures are EXPLAIN-only so a plan
  the executor cannot yet run is still comparable evidence. Build: the env
  knob at the planSelectWithSettings dispatch, a
  `pg-plan-parity-diff.py`-compatible capture recipe for the knob arm, and
  a per-stage divergence report (which of the six divergences the record
  belongs to) so progress is visible per-slice, per AGENT.md G8.
  Landed: fail-closed knob (`jointreePipelineFromEnv` — only literal `1`
  arms) + dispatch at `planSelectWithSettings` → `planSelectJointreePipeline`
  (delegating stub until 0003; the renamed body is `planSelectLegacyPipeline`);
  flag registered in the provenance table so every capture stamps its arm;
  `scripts/jointree-parity-capture.sh` (tpch via estimate-audit arm,
  tpcds via `cp -a` private clone, PG arm read-only);
  `scripts/pg-plan-divergence-class.py` (D1–D6 + jointree-search + verdict,
  diverging-pair matching). A/A evidence: SF0.25 plan bodies byte-identical,
  TPC-H shape-identical. Floors held: TPC-DS match=2 (Q9/Q41), TPC-H
  match=1 (Q6). Gates: units, tpch-spotcheck, sf025 sweep (FORCE=1, nightly
  live — values valid). Design doc:
  `docs/design/0100-0149/m0145-0002-dual-pipeline-harness.md` (indexed).
  Kind: impl
  Parent: none
  Movement: none
- [x] **M0145-0003 — sublink pull-up into the jointree** (goopg's
  `pull_up_sublinks`/`pull_up_subqueries` analogue; the milestone's core).
  Using route-a step 1's retained `.Subquery` parse trees, flatten bodies
  passing `sublinkBodyIsSimple` into the IR as semi/anti entries (quals
  merged into outer predicates, correlation `OuterColumnRef{Level:1}`
  re-based into the outer column space at splice — PG gets this free via
  `Var`/range-table indices); bodies that fail the simple test become
  semi/anti entries carrying their planned Node as the non-flattenable
  inner, so the search still sees them as citizens. Route-a step 2 is the
  pilot subset — if it lands first on the node-tree chain, port it onto
  the IR here rather than duplicating the mechanism. Must close the
  P0-H11 `cumulativeFromSpans` span round-trip in the same change
  (M0142-0008a-3i-reach-verify). Expected movement: the seam's
  `leaf-count` decline class (26 across 11 queries) and the five census
  witnesses (Q10/Q16/Q35/Q69/Q94 `join-method` records); re-derive leaf
  arithmetic, do not carry today's shortfall over.
  Kind: impl
  Parent: M0145-0001
  - **Flat-body arm landed this loop** (design:
    `docs/design/0100-0149/m0145-0003-sublink-pullup-into-jointree.md`).
    `jointreepullup.go` ports `convert_EXISTS_sublink_to_join`'s gates
    and binds the body via a provisional `planFromClause` +
    `resolveExpr` on the retained `.Subquery`; `integratePulledSublinks`
    inside `tryPGShapedJoinSearch` appends the body leaves at problem
    tail positions and emits `semiAntiChainLink`+`SpecialJoinInfo`, so
    the whole problem tail runs unchanged. `pulled` marks are
    pointer-keyed and inert outside the seam — every decline returns
    the exact legacy shape. `NOT EXISTS`'s `UnaryOp(OpNot,…)` spelling
    flips to ANTI per the unnest.go:4516 convention.
  - Learnings:
    - `NOT EXISTS` binds as `UnaryOp(OpNot, ExistsExpr{Negated:false})`,
      not `Negated:true` — the parser's canonical spelling; the
      pull-up must recognise both conjunct shapes.
    - `exprListHasLocalAndLevel1Ref` (a conjunct reading a body-local
      column AND a level-1 parent ref) is the pull-up-time proxy for
      "a spanning conjunct exists" — without it `pulled` would mark
      bodies the seam declines anyway and wrongly suppress the pre-DP
      arm for sibling sublinks.
    - Outer-local quals hoist under SEMI (they filter the same rows
      the semijoin keeps) but have no legal slot under ANTI — the link
      has no join-clause-only qual channel, so they decline (ledgered).
    - P0-H11's span round-trip is closed by construction: pulled leaf
      walk-order-flat and out-of-band bases coincide
      (`sum(widths[:pos])`), and `allSpans` uses `buildLeafSpans`'s
      own cumulative rule — attribution is pinned against
      `buildLeafSpans` output in the white-box test.
  - Evidence (knob arm, `tmp/jtcap-m3/`): SF0.25 A/B 91/99 identical;
    the only 5 shape diffs are Q10/Q16/Q35/Q69/Q94 — the named
    witnesses; vs-PG `D1-sublink` 8→6 (D3 +2 as those queries advance
    to their next divergence stage); TPC-H `match=22 shapediff=0`
    (fully inert — legacy already produced the semijoin). Gates:
    units + exprwalk census PASS, tpch-spotcheck PASS (Q12=2 Q13=33),
    SF0.25 sweep PASS (`MISMATCH=0`, plans `same=99`), acceptance arm
    24/24 value-MATCH.
  - **ANY arm landed (loop 2026-09-21 \#20)** —
    `convert_ANY_sublink_to_join` (subselect.c:1333) at the seam's
    granularity. `anyPullupConjunct` + `pullUpAnyBody` produce the same
    `jtPulledBody` the EXISTS arm does, so the seam, leaf numbering and
    `classifyPulledQuals` are untouched.
    - The one new mechanism: an ANY body need not be correlated at all
      (`x IN (SELECT y FROM t)` has no cross-scope reference — the
      correlation IS the testexpr, which lives in the OUTER qual), so
      the arm SYNTHESISES the link conjunct `outerOperand = bodyTarget`
      in the space the EXISTS body quals use — body-local plain
      `*ColumnRef`, outer as `*OuterColumnRef{Level:1}`
      (`outerOperandAsLevel1`). `rebasePulledQual` validates every outer
      index against the emitting bindings, so a wrong coordinate fails
      closed instead of reading the wrong column.
    - `NOT IN` is REFUSED, and that is PG's rule, not a shortcut: it is
      `<> ALL`, an ALL\_SUBLINK, and `pull_up_sublinks_qual_recurse`
      converts ANY and EXISTS only (prepjointree.c:665/731). goopg's
      LEGACY unnest does convert `NOT IN` to ANTI — a pre-existing
      divergence, ledgered separately, not extended onto this pipeline.
    - Measured on the same channel before/after (TPC-DS SF0.25 plans,
      knob on): `(pulled)` 9 -> **21**, unrecognised `InExpr` 34 -> **1**,
      with the residue landing in two NEW named buckets —
      `any-body-scope-not-bindable` 15 and `any-nested-sublink` 6. The
      next gates are named by the census rather than guessed.
    - Correctness evidence had to come from the KNOB arm, because the
      default-arm gates cannot exercise this code: TPC-DS SF0.25 sweep
      with `GOOPG_JOINTREE_PIPELINE=1` gives
      `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0` — row counts and value
      checksums correct on every executable query. Default gates stay
      green and plan-identical.
    - `TestExprSwitchInventoryIsPinned` required registering
      `outerOperandAsLevel1` in `exprSwitchInventory`; it is built on
      `cloneExprRefs` and fails closed (an unenumerated type aborts the
      clone, which declines the pull-up).
  - **ANY residue named (loop 2026-09-21 \#21)** —
    `bindPulledBodyScope`/`flattenPulledBodyTree` now return the
    sub-reason they failed on, and the larger ANY bucket turned out to
    be one shape, not the one the ledger assumed.
    - All 15 former `any-body-scope-not-bindable` fires are
      `any-body-leaf-(*optimizer.CTEScan)`: the ANY body's FROM is a
      **CTE reference** (`WHERE x IN (SELECT … FROM some_cte)`), the
      TPC-DS idiom.
    - They are NOT the opaque-body arm. Admitting a `*CTEScan` leaf
      pushes a statistics-free input into the DP, and `itemIsDerived`
      (relfromjoinlist.go:557) classifies exactly
      `*CTEScan`/`*WorkTableScan` as derived — the class the Q78
      `outer-over-derived` firewall keeps out of join ordering (C-04a:
      Q78 15 s -> 327 s TIMEOUT). The banner makes that firewall a hard
      constraint on every pull-up/flattening task.
    - So this bucket is blocked on **B-06 (CTE-output statistics)**, the
      same blocker M0145-0005 slice 5 and M0145-0006 recorded for
      `outer-over-derived`. Building the opaque-body arm would not move
      it. (B-06 is filed as **M0145-0009**; its completion note names the
      bare-`*SeqScan` relaxation as a sanctioned re-evaluation.)
    - The remaining actionable ANY bucket is `any-nested-sublink` (6):
      PG recurses `pull_up_sublinks` into a pulled body's own quals
      (`pull_up_sublinks_qual_recurse`); goopg does not. Filed as
      **M0145-0014** (owner directive 2026-09-21). The residual
      census classes are likewise filed: OR/NOT-position sublinks +
      the unnamed residual 3 → **M0145-0015**; the never-reached
      population (sublinks outside top-level WHERE conjuncts) →
      **M0145-0017**; the `pulled-leaf-not-scan` seam re-check that
      E2's CTEScan-leaf admission relocated into → **M0145-0013**.
  - **Decline census (loop 2026-09-21 \#19)** — which arm to build next,
    measured instead of ranked by size. `notePullupDecline`
    (`internal/optimizer/nlicensus.go`, `GOOPG_NLI_CENSUS=1`) reports one
    line per WHERE conjunct the pull-up sees. TPC-DS SF0.25, knob arm:
    - `InExpr` **34** — `IN`/`NOT IN`/`= ANY`. PG pulls these up
      (`ANY_SUBLINK`, prepjointree.c:665). **This is the gap.**
    - `SubqueryExpr` 15 — scalar sublinks, which PG does NOT pull up
      either (they stay SubPlans). Not a gap; do not build for it.
    - `(pulled)` 9 — the flat EXISTS arm's successes.
    - `ExistsExpr` 2 — EXISTS not at conjunct top level (under OR).
      Correction 2026-09-21: PG does NOT recurse into OR args
      (`pull_up_sublinks_qual_recurse` returns non-AND clauses
      unmodified, prepjointree.c:877); the earlier ":797" citation
      pointed at the `is_notclause` (NOT EXISTS → ANTI) arm, not an
      OR arm. Whether any of the 2 are pullable is a measurement
      question, filed as **M0145-0015**.
    - NO `pullUpExistsBody` gate fired at all (`body-not-simple`,
      `no-level1-correlation`, `no-spanning-conjunct`, …): every
      recognised EXISTS conjunct was pulled. The EXISTS arm's gates are
      not the limiter — the corpus is mostly `IN`/`ANY`.
    - The classifier names sublinks by Go type via `ExprSubplans`, not a
      hand-written switch: `TestExprSwitchInventoryIsPinned` rejected the
      switch version, and it was right — a census that must be taught
      each Expr type reports the untaught one as "nothing here", so the
      arm nobody built would be the arm that never appears.
    - **Next arm: `convert_ANY_sublink_to_join`** (subselect.c:1333) — 34
      of the 45 unpulled sublink conjuncts, and ANY+EXISTS is exactly
      PG's own `pull_up_sublinks` scope.
  - **CLOSED 2026-09-22 on a re-measurement, loop \#60.** Re-ran the
    knob-arm TPC-DS SF0.25 capture with both census channels on
    (`GOOPG_NLI_CENSUS=1`, `GOOPG_PGSHAPED_DP_TRACE=1`;
    `tmp/jtcap-m3-loop60/`, `tmp/jtcap-m3-l60t/`). Both of the task's own
    stated expected movements are met, and the residue is fully accounted
    for rather than merely smaller.
    Movement: none — this closing loop changed NO production code, so it
    moved no plan. The census deltas below are CUMULATIVE over the arms
    landed in earlier loops, and none of them is one of S3's three
    instruments (`match`, `CATEGORIES-EXCL-MATCH`, `ea-ratchet`); they are
    reported as the task's own acceptance evidence, not as movement.
    Cumulative: `PULLUPCENSUS` `(pulled)` 9 -> 27; `InExpr` 34 -> 1; the
    seam's `leaf-count` decline class 26 -> 10; vs-PG `D1-sublink` 8 -> 5.
    - **Every one of the 60 census fires is now classified, and none of
      them wants an arm that is buildable today:**
      - `(pulled)` **27** — successes.
      - `any-body-leaf-(*optimizer.CTEScan)` **15** — BLOCKED on B-06
        (CTE-output statistics), owned by the unchecked **M0145-0009**.
        Admitting these leaves without it pushes a statistics-free input
        into the DP, which is exactly what the Q78 `outer-over-derived`
        firewall exists to prevent (C-04a: 15 s -> 327 s TIMEOUT), and the
        banner makes that firewall a hard constraint.
      - `SubqueryExpr@scalar` **15** — NOT a gap. PG does not pull scalar
        sublinks up either; they stay SubPlans.
      - `ExistsExpr@or` **2** + `InExpr@or` **1** — NOT a gap.
        `pull_up_sublinks_qual_recurse` returns non-AND clauses unmodified
        (prepjointree.c:877), so PG does not reach an OR-position sublink
        either. Measured and closed under M0145-0015.
    - `SUBLINKCENSUS` cross-check: 285 sublinks route `pinned-spine`
      against 23 `jointree-pullup` — the never-reached population
      M0145-0017 already closed as a measured no-gap (sublinks outside
      top-level WHERE conjuncts, which PG likewise does not pull up).
    - **Why this closes rather than continues.** The `IN`/`NOT IN` item
      previously listed as still-open is DONE — the ANY arm landed it and
      the single surviving `InExpr` is an OR-position one PG declines too.
      What is left (the opaque-body arm, outer-local-only correlation,
      ANTI outer-local quals) has **zero measured demand in either
      corpus**: no census bucket fires for them in TPC-DS SF0.25, and
      TPC-H was already fully inert (`match=22 shapediff=0`). This task's
      own established method is to pick the next arm from the census
      rather than by size, and the census now names no buildable arm.
      Building one anyway would be speculative. Filing them as their own
      tasks was REFUSED (M0145-0001 last five completed descendants all
      `Movement: none`), so the scope is recorded in the M0145-0001
      escalation block instead and that root is now `[!]` pending an
      owner decision — see the escalation for the full write-up.

- [x] **M0145-0004 — UNION ALL flattening to an appendrel jointree entry**
  (`pull_up_simple_union_all` analogue, prepjointree.c:1617). Absorbs
  M0144-0003b's residual: the branches whose subtree never reached the
  search at all (Q5's `*Project:nil-rel` on both sides, Q2/Q33/Q56/Q60)
  become reachable once flattening happens at IR level before lowering.
  `setOpBranchRel`/`addPartialSetOpPath` (0003b-1) already carry the rel
  out of a SetOp node — the IR version makes the appendrel a real join
  input so `add_paths_to_append_rel` semantics apply. Expected movement:
  the TPC-DS Parallel Append sites (Q5×3, Q2, Q14, Q71, Q76) and census
  `parallelism` records.
  Kind: impl
  Parent: M0145-0001
  - **Leaf-hoist arm landed this loop** (design:
    `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`).
    `jointreeappendrel.go` ports `is_simple_union_all` (chain head
    refuses ORDER BY/LIMIT/OFFSET/locking/WITH; every link UNION ALL;
    member-local trailing FOR UPDATE refused; `SetOpOperand`
    groupings declined) and `addAppendRelPartialPaths` hoists the
    nested SETOP rel's Parallel Append candidate onto the leaf rel.
    `plannerSet.appendrelMember`/`ctx.appendrelMember` force each
    member scope through the join search (one-relation floor → 1 for
    exactly one scope level — upstream runs
    `set_base_rel_pathlists` unconditionally); the leaf's
    `ConsiderParallel` inherits the SETOP rel's member-AND. All marks
    are jointree-arm-only — legacy is inert by construction.
  - Two shared latent gaps closed (both arms benefit):
    - Worker sizing through `*SetOp`: `drivingScans` expands a SetOp
      driving node to its streamed member scans (claimed-whole
      branches skipped); `computeParallelWorkers`/`upperSplitWorkers`
      take max-over-branches (PG's create_append_path max rule).
      Previously `scanTable(*SetOp)`=nil → 0 workers → every
      `Aggregate → SetOp` / gather-over-union plan refused.
    - Executor claims through a join probe: `unwrapToSetOp` gained
      `*joinOp` (probe side per algo, mirroring
      `attachParallelScan`'s gates) + `*nestedLoopIndexJoinOp` arms —
      without it a `PathSetOp` under a partial hash join stayed
      unclaimed and every worker replayed the whole union
      (reproduced: 80/120/200 rows at workers=1/2/4 for a 40-row
      join; `TestGatherOverJoinProbeSetOpIdentity` pins 40).
  - Third gap, knob-arm-only, found by the parity capture: a hoisted
    `PathSetOp` is a legal join input for the first time, and
    `createSetOpPlan` returned a nil `outputLayout` for every caller —
    `joinInputsFor` panicked re-basing quals (`EXPLAIN Q5` crashed the
    backend). Fixed: leaf-owned SetOp paths return
    `baseRelLayout(p.Rel, out)` (upper-rel paths stay nil);
    `TestCreateSetOpPlanLeafLayout` pins the contiguous leaf layout.
  - Evidence: SF0.25 sweep 96 PASS / MISMATCH=0 on BOTH arms
    (knob-arm sweep `tmp/m0145-0004-sweep-on/`, post-fix capture
    `tmp/m0145-0004-capture/jt-on-fix.*` — unparsed 0, verdict 3);
    plan-diff shows the witness move — Q5's three unions now plan
    `Gather → Hash Join → Append{Parallel Seq Scan ×2}` on the
    default arm (post-pass gather made selectable by the sizing
    fix), and the knob arm elects a search-level
    `Gather → Nested Loop → Parallel Hash Join → Append{parallel}`
    (PG's `Parallel Hash Join → Parallel Append` shape); Q5 rows
    identical off/on. tpch-spotcheck PASS (Q12=2, Q13=33). New
    white-box tests: admissibility matrix, member-scope search A/B,
    hoist gates + Rel re-targeting, CP inheritance,
    partial-agg-over-union leaf, leaf-owned SetOp layout.
  - **`tlist_same_datatypes` probed (loop 2026-09-21 \#22)** — the gate
    is missing, but it is a FIDELITY gate here, not a live wrong-answer:
    a type-mismatched `UNION ALL` flattens on the knob arm and returns
    the same correct values as the default arm (`1`, `2.5`, `sum` 6.5).
    The probe did surface a real, arm-independent defect — the setop's
    declared output type is the first member's, not
    `select_common_type`'s — filed separately under "Manually
    discovered" rather than folded in here, because it is a parser/type
    defect that both pipelines share.
  - **`tlist_same_datatypes` LANDED 2026-09-22 (loop \#83)** — the last
    gate of `is_simple_union_all`. Movement: none — default arm
    `CATEGORIES-EXCL-MATCH` identical, plan channel `same=99 changed=0`.
    - **Where the question can be asked**: not at MARK time (the parser
      AST has no resolved types) and not after planning (`setOpUnifyBranches`
      has coerced both branches, so the comparison is a tautology). The one
      moment both are false is inside the fold, immediately before the
      coercion — `SetOp.TlistTypesDiffer` records the verdict there and
      `addAppendRelPartialPaths` consults it.
    - Comparing each link's two branches is equivalent to upstream's
      member-vs-`colTypes` comparison for a chain, and a nested link that
      already answered false propagates (`is_simple_union_all_recurse`'s
      `&&` over `larg`/`rarg`). Zero value = "no known difference", so the
      partition/inheritance fan-outs keep their behaviour exactly.
    - **An over-refusal the measurement caught before it shipped.** The
      first version compared `Type.Name` directly — but upstream compares
      OIDs, where `decimal` IS `numeric` (1700) and `int` is `int4`.
      TPC-DS Q5's union mixes a table column with `cast(0 as decimal(7,2))`,
      so it refused a union PG flattens and **removed the `Parallel Append`
      shape this very task had landed**. The knob-arm capture named Q5 as
      the one query that moved; folding both spellings through
      `catalog.ArgTypeDisplayAlias` first leaves only Q36/Q70/Q86, which are
      the capture's three pre-existing parse errors whose text embeds the
      temp FILENAME (the loop \#82 trap, hit again).
    - Using a DISPLAY mapping for identity is sound in this direction —
      distinct types never share a display name — but it is not an OID, so
      an OID-level question (domains over one base type) is outside it. That
      error direction is the safe one: it can only answer "same", never
      refuse a union PG would allow.
    - Three test levels, each non-vacuity checked: the predicate (alias and
      typmod pins), the gate (its matched-types control is load-bearing —
      without it the change is indistinguishable from "never hoist"), and
      the WIRING, which exists because neutralising the capture site left
      the other two green.
    - Gates: units; tpch-spotcheck Q12=2/Q13=33; tpcds-sf025
      `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`; acceptance arm
      24 MATCH; both default-arm floor captures (TPC-DS `match=2` Q9+Q41,
      TPC-H `match=1` Q6) held exactly; pgbench smoke.
  - **Residual RE-SCOPED 2026-09-22 (loop \#84), measurement in the design
    doc's final section.** Movement: none; no production file touched.
    - **Q71 and Q76 have NO CTE at all** — their unions are plain
      `FROM (… UNION ALL …) alias` items, exactly the shape this task's mark
      already handles, so "`CTEScan` hides the carrier" cannot be their
      blocker.
    - **Q14's union CTEs are referenced 6x each**, so upstream does not
      inline them and there is no appendrel to make. Q14 leaves the
      population.
    - **Q2 is the one genuine case**: its `wscs` union CTE has a single
      reference, which is exactly PG's inlining criterion.
    - **The real gap is WHERE THE GATHER SITS**, knob arm vs PG at SF0.25:
      Q71 goopg `Append{Gather{Append{Parallel Seq Scan}}}` vs PG
      `Gather{Parallel Append{…}}`; Q76 goopg `Append{Gather{…}, Gather{…}}`
      vs PG `Gather Merge{Parallel Append{…}}`. goopg gathers INSIDE each
      member and appends the gathered results; PG keeps the members partial
      under ONE Gather above a parallel-aware Append.
    - **The appendrel machinery is not what decides it**: default arm vs
      knob arm in the same capture run, **Q71 and Q76 are byte-identical**
      in Append/Gather structure and Q2 differs only in cost.
    - **NARROWED 2026-09-22 (loop \#85) — all three candidates above are
      REFUTED and the shape is more specific.**
      - The hoist FIRES and its paths WIN: SF0.25 knob-arm DP trace shows
        `baserel.appendrel.partial` 4 paths, **every one `verdict=accepted`**,
        alongside `upper.setop.append.partial` 11 and `.mixed` 3. Nothing is
        failing to fire, so none of `addBaseRelGatherPaths` /
        `upperSplitWorkers` / the member search root is the culprit.
      - **The chain stays NESTED.** Q71's union has THREE members and goopg
        produces `Append{ Gather{Append{ws, cs}}, Gather{store_sales} }` —
        the INNER link of the right-leaning `SetOp(A, SetOp(B,C))` gets the
        parallel Append and the hoist, the OUTER link does not, so its two
        inputs are gathered separately and appended serially. PG has no such
        split: `is_simple_union_all_recurse` walks `larg` AND `rarg` and
        flattens the whole chain into ONE appendrel, over which
        `add_paths_to_append_rel` builds a single partial Append.
      - So **the divergence is chain-flattening DEPTH**; the gather placement
        is its symptom.
      - **A stale comment would have misdirected the fix**:
        `addPartialSetOpPath`'s prose lists "a nested `*SetOp`" among the
        disqualifying wrappers, but `setOpBranchPartialChainOK` ADMITS a
        nested set operation through its carrier check — M0144-0003b-1 added
        exactly that. The comment predates the code and is wrong.
      - ~~**The one measurement still missing**: why the OUTER link produces
        no winning partial path …~~ **TAKEN 2026-09-22 (loop \#86) and
        REFUTED — the outer link DOES produce one.** Single-query trace
        (Q71 alone, private SF0.25 lane, so every DPPATH line is this
        query's):
        - `upper.setop.append.partial relids={0} rows=74 total=18936.41
          accepted` (inner link) and `relids={1} rows=168 total=37279.17
          accepted` (**outer link**). `LeftBranchRel`/`RightBranchRel`/
          `ConsiderParallel` are all fine.
        - **SELF-CORRECTION**: loop \#85 wrote "the hoist fires and its
          paths are accepted". True CORPUS-WIDE (4 filings) and wrong for
          Q71 — **no `baserel.appendrel.partial` line appears in Q71's
          trace at all**. A corpus count was generalised to one query; it
          should not have been.
        - The inner link's partial IS gathered (`gather relids={0}`
          rows=229 total=19959.31 — the plan's inner `Gather`); the OUTER
          link's partial (37279.17) has **no Gather over it**, so the plan
          takes the serial outer Append (39336.25) although gathering the
          partial would have cost about 38300.
        - **NOT the cause** (probed on the parse tree, not assumed):
          `subqueryChainIsSimpleUnionAll` ACCEPTS Q71's union
          (`from[1]: SetOp!=nil=true isSimpleUnionAll=true`), so the mark
          predicate is satisfied.
      - ~~**The one hypothesis left** … a `*Project` over the `*SetOp` …~~
        **REFUTED 2026-09-22 (loop \#87)**: a white-box probe shows a union
        subquery in FROM lowers to a **bare `*SetOp`** as the join input,
        with or without column renames, so `rel.baseLeaf` IS a carrier type.
      - **THE DECLINING CONDITION IS NAMED AT LAST — `ConsiderParallel`.**
        `considerParallel` emits one line per base leaf under the SAME
        `GOOPG_PGSHAPED_DP_TRACE=1` gate the DPPATH lines use
        (`joinsearchtrace.go` `baseCP` → `DPTRACE cpadmit`), and Q71's
        already-captured trace says:
        `DPTRACE cpadmit src=base rel={tmp} cp=0 leaf=other`
        (`tmp` is Q71's union alias; every other leaf reads `cp=1`).
        So `addAppendRelPartialPaths`'s `if !rel.ConsiderParallel { continue }`
        is what declines the hoist.
      - `considerparallel.go` has an appendrel arm written for exactly this
        leaf (`rel.ConsiderParallel = setOpRel.ConsiderParallel` when the
        mark is set and the carrier resolves). `cp=0` means the override did
        NOT take, so exactly one of three holds — each a one-line check now
        that the gate is known:
        1. `s.relInfos[i].appendrel` is false at the search — a propagation
           gap into `baseRelInfo`, since the binding does set `b.appendrel`;
        2. `carrier.setOpBranchRel()` is nil — the node the outer binder
           receives is not the instance `createSetOpPaths` stamped, because
           `createSetOpPlan` rebuilds it through `createPlanNode`;
        3. `setOpRel.ConsiderParallel` is itself false at that moment (an
           ordering question — `addPartialSetOpPath` is what sets it).
        (2) is the most likely on the evidence, but it is a hypothesis and
        this residual's record says to check before coding.
      - **Method note**: `DPTRACE cpadmit` is emitted by the gate this
        milestone already uses everywhere. Four loops asked "which condition
        declined?" and none looked at it. When a silent `continue` is the
        suspect, check whether an existing trace channel already covers the
        predicate before proposing to add one.
      - **2026-09-22 (loop \#88): sub-causes (2) and (3) ELIMINATED, and this
        residual is now BLOCKED.** White-box probe with `jointreePipeline` on
        and parallel-sized stats:
        `*SetOp stampedRel=true  rel.ConsiderParallel=true  partialPaths=1`.
        - (2) refuted — the `*SetOp` the outer binder receives DOES carry the
          stamp; `createSetOpPlan`'s rebuild preserves it.
        - (3) refuted — the stamped rel's `ConsiderParallel` is true and it
          holds a partial path.
        - (1) is all that remains, and the source does not support it either:
          `seamLeafRelInfo` routes a union leaf through `estimateBaseRelInfo`
          (its `b.table` is non-nil — `planSubqueryRangeVar` builds a
          synthetic `catalog.Table` for the alias), and that constructor DOES
          carry `appendrel`. The two constructors that omit the field are the
          Semi/Anti synthetic-leaf arm and a `bindingIdx: -1` default.
      - **BLOCKED — five loops, five refuted hypotheses, no fix.**
        84 CTEScan-hides-carrier / 85 gather placement / 86 outer link has no
        partial / 87 `*Project` wrapper / 88 lost stamp or stale CP — all
        refuted by measurement. The one remaining candidate is unsupported by
        the source, which means the reasoning is wrong somewhere static
        reading and the existing traces cannot see.
      - **THE UNBLOCK, and it needs an owner nod because of C1**: add ONE
        diagnostic line to `addAppendRelPartialPaths` naming which condition
        declined (mark / ConsiderParallel / carrier / empty PartialPathlist /
        tlist), under the existing `GOOPG_PGSHAPED_DP_TRACE` gate. The
        function declines with a bare `continue` and no trace, which is
        exactly why five loops had to guess. C1 makes a trace an **impl**
        task needing the full gate set, so the loop did not do it
        unilaterally — but every guessing route is now exhausted.
        - If it IS a `*Project`, the fix is NOT simply to admit `*Project`:
          the comment's reasoning holds for a row-changing Project but not
          for a pure rename/reorder. The distinction to implement is
          positional-identity vs computing — the same one **M0144-0011a-3**
          already drew for crossing a positional-identity `Project`.
      - **Risk to carry into the fix**: admitting a nested streaming Append
        under one Gather means two levels of block claim cooperating across
        workers, and this task has already been bitten there once (an
        unclaimed `PathSetOp` under a partial hash join made every worker
        replay the whole union — 80/120/200 rows at workers=1/2/4 for a
        40-row join). The same per-worker row-identity pin is required.
      - Expected movement once fixed: the `parallelism` category on SF0.25,
        which reads 84-85 in both arms today.
      - **Mark-propagation repair 2026-09-22 (loop \#1), in progress.**
        - New `DPTRACE appendrel` verdicts named Q71's first refusal as
          `unmarked`, rather than a `ConsiderParallel` failure. The cause was
          `planSubqueryRangeVar` treating a non-nil resolver context as SQL
          LATERAL. A later comma-separated FROM item receives that context for
          table-function name resolution even when its `RangeVar.Lateral` is
          false.
        - The mark now keys on `!rv.Lateral`. Q71 reaches `verdict=admitted`
          and files an accepted `baserel.appendrel.partial` path; the paired
          test keeps a genuine LATERAL union refused. The final Q71 plan does
          not move because the new candidate loses cost election. Whole-chain
          flattening remains the open PG-fidelity gap.
        - **Acceptance gate coverage loss verified 2026-09-22 (loop \#2).**
          A clean HEAD baseline/candidate run, taken with this staged change
          stashed then restored, gives the same 23 MATCH plus Q9 BOTH-ERROR at
          the 600-second cancellation as the changed-tree run. This proves the
          timeout predates the mark repair but does not satisfy the required
          PASS stamp, so the production change remains staged and uncommitted.
        - **Q9 blocker classified 2026-09-22 (loop \#3).** The timeout is the
          already-closed `q9_costdriven_mhj_cannot_be_cost_forced` no-go, not
          an appendrel effect: its safeguards retain hash behaviour because an
          unsafe NLI alternative can create a match-set explosion. No timeout
          increase or out-of-scope join-order change is permitted here; the
          staged implementation cannot be committed without a passing
          acceptance stamp.
    - **LINEAGE NOTE — why this is not a separate task, and an ESCALATION.**
      Filing it as `M0145-0004a` was REFUSED by
      `scripts/ralph-lineage-guard.py`: root M0145-0001's last five
      completed descendants (M0145-0006, -0014, -0015, -0016, -0017) all
      carry `Movement: none`, so the budget is exhausted and the guard's
      remedy is to mark the ROOT `[!]` and select elsewhere. **The owner
      re-opened M0145-0001 on 2026-09-22 and sequenced this very chain
      (0004 → 0005 → 0007 → 0008) after that escalation was answered**, so
      marking it `[!]` again would contradict a live owner decision, and the
      loop does not do that (R4). The guard cannot see the re-open because
      the five `Movement: none` rows are still the last five. The finding is
      therefore recorded here, inside an EXISTING task, rather than as a new
      descendant. **This will recur on every attempt to file work under this
      root**, which is a standing blocker on the banner's own chain and
      needs the owner to clear it — by re-pinning the lineage baseline or by
      exempting the re-opened root.
      **RESOLVED 2026\-09\-22 \(owner\)**: the owner chose the re\-pin —
      `LINEAGE\-BASELINE:` in the banner pins the six stale completions out
      of the window, so descendants may again be filed under this root.

  - **UNBLOCKED and LANDED 2026\-09\-22 \(loop \#4\)** as `f8349122c`.
    Movement: none — knob\-arm only; default\-arm `CATEGORIES-EXCL-MATCH`
    byte\-identical and `PLAN-SHAPE same=99 changed=0`.
    - **The block was never real.** `scripts/tpch-acceptance-arm.sh` defaults
      `PGSHAPED=0` \(`GOOPG_PGSHAPED_DP=0`\), which is NOT the shipped planner
      configuration. Q9 cannot finish in 600 s on the legacy search and takes
      **2.8 s** on the default. Re\-run as
      `PGSHAPED=1 ACCEPT_BASELINE=tmp/arm-on-20260922-loop77.txt …`:
      **`SUMMARY: 24 MATCH / VERDICT: PASS`**. No owner resolution, no gate
      exception and no Q9 work were needed, and loop \#2's and \#3's
      conclusions above are withdrawn.
    - Three loops plus an owner escalation were spent on an artifact, and the
      same artifact separately held M0145\-0020a. The
      `q9_costdriven_mhj_cannot_be_cost_forced` no\-go is a true fact that was
      recruited to explain something it did not cause — a correct citation is
      not a diagnosis.
    - **A second correction before staging**: the staged `planner.go` carried
      ~11 unrelated reformatting hunks \(expanded one\-line bodies, reordered
      imports, deleted blank lines\) — a newer local `gofmt -w` against the
      go1.25 baseline \(`CLAUDE.md` §Git\). Only the six\-line
      `appendrelSubquery` hunk is the change; the noise was reverted.
    - Non\-vacuity: restoring `lateralCtx == nil` fails exactly
      `TestAppendrelMarkSurvivesEarlierFromSibling/plain_later_FROM_subquery_is_marked`
      and leaves the paired LATERAL\-refused subtest green.
    - Gates: units; tpch\-spotcheck Q12=2/Q13=33; tpcds\-sf025
      `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`; acceptance arm
      24 MATCH; floors held \(TPC\-DS `match=2`, TPC\-H parallel `match=1`\);
      pgbench smoke. ea\-ratchet not run — no estimator/selectivity/stats code
      touched and the default arm's plans are byte\-identical.
    - **Still `[ ]`, not `[x]`**: the task's headline — whole\-chain UNION ALL
      flattening \(`is_simple_union_all_recurse` walks `larg` AND `rarg`;
      goopg flattens only the inner link of a right\-leaning chain\) — is
      unchanged by this repair and remains its open residual.

  - Still open (ledgered): member-level rtable entries +
    parent-qual distribution into members (`distribute_qual_to_rels` —
    M0145-0005's IR work); LATERAL union propagation; ~~CTE-wrapped union
    leaves (`CTEScan` hides the carrier — Q2/Q14/Q71/Q76's shapes)~~
    **RE-SCOPED 2026-09-22 (loop \#84) — the description is wrong for three
    of the four; the corrected residual is recorded in the sub-bullets
    below and NOT filed as a separate task, see the lineage note**;
    serial-side member-path competition (leaf serial path stays
    prebuilt-over-nested-winner); `is_safe_append_member`'s pull-up
    half is inapplicable in this model (members are not promoted —
    member WHERE quals ride `spliceBranchEmission` per worker).
  - **CLOSED 2026-09-23 (loop \#13).** The headline — whole-chain
    UNION ALL flattening — landed as M0145-0004a (`Gather → Parallel
    Append{ws,cs,store_sales}` verified against PG 18.3, both arms,
    290 rows). Every remaining bullet above is a ledgered deferral
    with a named resume point (deferral_ledger rows under
    M0145-0004): member rtable + `distribute_qual_to_rels` is
    M0145-0005's IR work, serial-side member-path competition is its
    member-rel costing question, LATERAL propagation and the
    `is_safe_append_member` pull-up half are recorded as
    inapplicable-at-seam-granularity, and Q2's single-reference
    union CTE is the already-tracked `inline_cte` structural
    question (M0142-0016c/d lineage). Nothing selectable remains
    inside this task.
  Movement: none — bookkeeping closure only; the plan movement
    (Q14/Q66/Q71 `parallelism` convergence) is recorded under
    M0145-0004a's own Movement line below.
- [x] **M0145-0004a — whole-chain UNION ALL flattening**
  (filed 2026-09-22 under the re-pinned lineage baseline; the residual
  M0145-0004's LINEAGE NOTE could not file). PostgreSQL's
  `is_simple_union_all_recurse` (`prepjointree.c:1617`) recurses BOTH
  `larg` and `rarg`, so an n-way UNION ALL chain collapses into ONE
  flat appendrel that `add_paths_to_append_rel` plans as a single
  (Parallel) Append. goopg flattens only the INNER link of a
  right-leaning chain: `SetOp(A, SetOp(B,C))` produces the nested
  `Append{Gather{Append{ws,cs}}, Gather{store_sales}}` where PG emits
  `Gather{Parallel Append{ws,cs,store_sales}}` — which is why Q71's
  admitted appendrel candidate loses its cost election and no plan
  moves. Extend `jointreeappendrel.go`'s mark/hoist to walk the whole
  chain so the full member set joins one appendrel. Carry the
  recorded risk: admitting a nested streaming Append under one Gather
  means two levels of block claim cooperating across workers — the
  per-worker row-identity pin (`TestGatherOverJoinProbeSetOpIdentity`
  family) is required, this task was already bitten there once.
  Expected movement: the `parallelism` category on SF0.25 (reads
  84-85 in both arms today) and Q71's admitted-but-unelected
  candidate becoming electable.
  Kind: impl
  Parent: M0145-0004
  - **LANDED 2026-09-23.** Recon corrected the task's mental model: the
    mark+hoist was already whole-chain — `setOpBranchTag` carries the
    inner link's SETOP rel out, so the outer link's partial
    `PathSetOp{PathSetOp{m1,m2}, m3}` IS the flat candidate, and
    `addAppendRelPartialPaths` already hoisted it. The refusal was one
    `default: return false` in `setOpBranchDrivingKindIsSupported`: a
    nested `PathSetOp` branch had no executor twin because leaf claim
    sets carried no `setOpLeft`/`setOpRight` of their own.
  - Landed: (i) lazy nested claim sets — `parallelClaimSet.setOpBranch`
    grows the branch pair under a per-set `sync.Once`, so an N-link
    chain materialises N−1 levels of branch state and every member scan
    claims independently (the N+1-copies defect the refusal protected
    against); (ii) `bitmapPrebuildTargets` descends nested `*setOp`
    branches so member bitmaps publish into the level workers attach;
    (iii) the `PathSetOp` admission arm delegates to
    `partialPathDrivingKind(p) == PathSetOp` — the parent's own
    contract one level down, still fail-closed on Sort/Memoize/etc.;
    (iv) `SetOp.ParallelAware` — PG's generic `parallel_aware` label
    (explain.c:1630) — stamped from `p.ParallelAware` in
    `createSetOpPlan`, rendered "Parallel Append", cleared by
    `unstampParallelScan` when a plan loses its Gather post-cache.
  - Tests: `TestWholeChainPartialPathIsGatherable` (nested + 3-link
    gatherable; Sort and non-parallel-safe claimed-whole branches still
    refuse), `TestUnionAllChainLeafFilesGatherablePartial` (e2e
    path-level pin: the 3-member leaf's SETOP rel carries a runnable
    nested partial; election deliberately NOT pinned — equal-member
    fixtures compress the margin below add_path's fuzz, ~0.2%),
    `TestGatherOverNestedSetOpIdentity` +
    `TestGatherOverNestedSetOpClaimedWholeIdentity` (390 rows, workers
    1/2/4, no duplicates), claim-kind anti-drift updated for
    `setOpKidsOnce`.
  - Lane verify: Q71 on private SF0.25 clones — BOTH arms emit
    `Gather → Parallel Append{web_sales, catalog_sales, store_sales}`,
    290 rows = PG 18.3. The machinery is shared, so the shape change is
    not knob-gated.
  - Gates: units PASS; tpch-spotcheck PASS (Q12=2, Q13=33); SF0.25
    sweep PASS=96 MISMATCH=0 TIMEOUT=0, plan-diff 3 movers (Q14, Q66,
    Q71 — all gaining the flat `Gather → Parallel Append` /
    `Parallel Append` label); acceptance arm PGSHAPED=1 24 MATCH PASS;
    tpcds-fireset PASS (sf025+sf1, no introduced timeouts); plan-gate
    16/22 = the standing M0137-0022 baseline drift (zero SetOp/UNION/
    Append lines in the diff; the gate compares live-:65433 vs the
    2026-09-15 pin — staged code in neither side — and the count moved
    14→16 on cluster stats drift, not this change).
  - **Movement: Q14/Q66/Q71 plan shapes converge to PG's
    single-Gather/Parallel-Append form on the corpus** (sweep
    plan-diff, both arms — machinery is arm-independent); the vs-PG
    `parallelism` category count is ~unchanged (candidate 84 vs
    baseline 85 — the delta is Q10, an arm asymmetry unrelated to this
    change; Q14/Q66/Q71 keep their records because they still diverge
    in other categories). Rows: all value-identical.
  - Deferral (ledgered in the design doc): bare-member legacy-arm
    fixtures stay serial (the `s.Where == nil` seam gate needs
    outerLink/appendrelMember/jointree — the same posture M0145-0004
    recorded); member-scope Gather election compressing the whole-chain
    margin is a costing question for M0145-0005's member-rel work.
  - Design doc:
    `docs/design/0100-0149/m0145-0004a-whole-chain-union-all-flattening.md`.
- [x] **M0145-0005 — single-pass DP over the jointree** (the search
  consumes the IR directly; semi/anti entries are legal searched partners
  via a `join_is_legal` port over the SJInfo-equivalent, joinrels.c:350).
  Retires the Phase A/B split, `runJoinSearchBelowPinned`, the pinned
  spine, splice-time re-resolution and the `admitSemiAnti` call-site
  restriction — each retirement lands only when its deletion is exercised
  by the corpus under `GOOPG_JOINTREE_PIPELINE=1`. Executor-capability
  gates stay in path generation (parallel_hash refusal, partial nestloop
  jointype whitelist) until the D3 substrate tasks land — flow parity
  must not silently generate unexecutable shapes. Depends on M0145-0004
  too: appendrel entries must exist in the IR before the single pass can
  be complete for UNION ALL chains (a query whose chain still contains a
  `*SetOp` falls back to the legacy pipeline, not to a partial search).
  Kind: impl
  Parent: M0145-0003
  - Loop 2026-09-21: design doc
    `docs/design/0100-0149/m0145-0005-single-pass-dp-over-jointree.md`
    + slice 1 landed.
    - Recon: the DP substrate the task text calls for is already complete —
      `joinIsLegal` is the joinrels.c:350 port in full (SEMI/ANTI match,
      `reversed`, LEFT/FULL association), `buildJoinRelRestrictList`
      implements outer-join clause distribution incl. nullable-side
      filters, `addPaths(..., sjinfo)` → `Path.Jointype` →
      `planJoinTypeFor` → executor types; `makeRelFromJoinlist` already
      recurses pinned sub-joinlists. The remainder is construction-side
      retirement, not capability.
    - Key correction to the task's mental model: `joinPinned` pins ONLY
      FULL since C-04a/b (LEFT/RIGHT deconstruct flat + SJInfos — a flat
      `a LEFT b LEFT c` was already a single searched problem on BOTH
      arms); the only pinned tops left are FULL and outer-over-FULL,
      which `spineLinkSearchable` never certifies — a certified non-empty
      spine was unreachable, so `splitOuterSpine`/splice/`prefixNullable`
      were already dead on reachable inputs.
    - Slice 1 (landed): knob arm skips `splitOuterSpine` entirely in
      `tryPGShapedJoinSearch` — `node` + `ctx.joinlist` feed the
      machinery directly; pinned tops decline one gate later
      (`extractSearchLeaves` leaf-count / `pinnedUnsearchable`) with the
      same `used=false` syntactic fall-back. No reachable plan change on
      either arm — pure retirement.
    - Remaining slices per the doc: semi/anti as real leaf items (retires
      `semiAntiChainLink` synthetic leaves + remap family), IR-direct
      leaf materialisation (retires `extractSearchLeaves` node-walk +
      spans/offset validation), one-rel floor (`isSimpleSingle`/
      `GOOPG_ONEREL_SEARCH`), Phase A/B + `admitSemiAnti` retirement.
    - **Decline-bucket ownership (2026-09-21 audit, owner directive)**:
      `leaf-count` (26 fires, dominant) is owned by the IR-direct leaf
      materialisation slice — its members are already-planned composites
      (`*Project`, `*Gather`, `*NestedLoopIndexJoin`, `*CTEScan`) that no
      leaf-admission predicate can decompose (M0144-0003a's census
      measured both candidate predicates as zero-witness); only leaves
      sourced from the jointree IR make them searchable.
      `leaf-count-overflow` is NOT automatically covered — it is a
      `RelSet` bit-width refusal (`len(scans) > maxSearchRels`,
      joinsearchseam.go:474) that survives any change in WHERE the
      leaf count comes from; the IR slice must either widen `RelSet`
      or record the limit as a deliberate divergence. `outer-spine`
      (2) fires on the
      legacy arm only — the knob arm bypasses `splitOuterSpine`, so the
      class dies with the arm at cutover; no separate task. The
      pull-up-census buckets are filed separately: `pulled-leaf-not-scan`
      → **M0145-0013**, `any-nested-sublink` → **M0145-0014**,
      residual/OR-position → **M0145-0015**, `semianti-not-tail` →
      **M0145-0016** (subsume-check first), never-reached population →
      **M0145-0017**, `outer-over-derived` relaxation → **M0145-0018**
      (owner GO).
    - Tests: `internal/optimizer/joinsearch_m0145_test.go` — searched
      flat LEFT spine pin, both-arms parity pin, FULL fail-closed pin.
    - Slice 4 (landed, loop 2026-09-21 #7): one-relation and degenerate
      scopes through the same entry on the knob arm —
      `isSimpleSingle` bypass + WHERE-arm rule-chooser guard gained
      `&& !jointree`, the filterless arm gained `|| jointree`, and the
      seam floor is `1` whenever `jointreePipeline`
      (`GOOPG_ONEREL_SEARCH` now governs the legacy arm alone — its only
      readers were inside the lifted guards).
    - Behaviour the lift unlocks: single-table statements on the knob arm
      are searched (base-rel pathlist on cost, `set_base_rel_pathlists`
      analogue); a flat correlated EXISTS/NOT EXISTS over one table now
      reaches `pullUpSublinksIntoJointree` — previously the
      `isSimpleSingle` bypass kept every pull-up test on the post-hoc
      unnest path without exercising pull-up at all.
    - Latent crash fixed in the same commit: `pullUpExistsBody` called
      `resolveExpr(sub.Where)` unguarded — WHERE-less EXISTS bodies
      (`EXISTS (SELECT 1 FROM t)`) SIGSEGV'd; reachable before via
      multi-table outers, now declined at the correlation check.
    - Executor gaps the lift exposed (fixed in the same commit):
      - `Join{Lateral, Semi/Anti}` (searched parameterised NL anti/semi,
        e.g. Q22's `NOT EXISTS` under a derived table) had no emit-once
        arm in `lateralJoinStream` — the generic semi/anti `Open` check
        refused predicate-less keyed joins before the lateral arm ran.
        Taught the stream SEMI/ANTI emit-once mirroring the fused
        `nestedLoopIndexJoinOp` semantics, reordered `Open` so `Lateral`
        routes first, and kept a fail-closed refusal for null-aware
        lateral shapes.
      - Cooperative parallel hash build + nested Gather: a producer's
        rebuilt build subtree is Closed wholesale after probing the
        leader-published shared table, and `gatherOp.Close`/
        `gatherMergeOp.Close` dereferenced `o.group` unconditionally —
        nil for a never-Opened Gather. The panic, converted by
        `ParallelGroup.Go`, cancelled SIBLING producers mid-scan, and
        `group.Wait()`'s error was discarded in the build's cleanup —
        silent partial builds (TPC-H Q20: 85-99 of 101 suppliers across
        identical runs, plan stable, gone at 1 worker or
        `GOOPG_COOP_JOIN_BUILD=off`). Both Closes are now no-ops when
        never Opened, and the build propagates the producer-group error
        instead of swallowing it (fail-closed: a producer failure fails
        the query rather than returning partial results).
    - Test premise updates (retired, not weakened):
      `TestJointreePipelineDispatchDelegates` +
      `TestJointreePullupDeclineParity`/`NoExistsKeepsLegacyIdentical`
      byte-equality → shape + decorrelation + join-type equality plus the
      `treeHasSearched` routing marker.
    - Skipped on the knob arm by design (value-preserving missed opts,
      ledgered): `injectLikeRangePredicates`, `reduceNotNullQuals`
      incl. always-false → childless `Result`, `planIndexScanFromWhere`.
    - **`reduceNotNullQuals` CLOSED (loop 2026-09-21 \#23)** — it was
      the one of the three that carries cutover RISK, because it is
      goopg's port of `restriction_is_always_true`/`_false`
      (initsplan.c `add_base_clause_to_rel`), i.e. a PG SHAPE rule and
      not merely an optimisation. Measured before the fix: the knob arm
      planned `Filter{SeqScan}` for `WHERE not_null_col IS NULL` where
      the default arm emits PG's childless
      `Result / One-Time Filter: false`; likewise it kept a redundant
      Filter for `IS NOT NULL` where the default arm drops the qual.
    - The reduction now runs in the generic arm, gated to `jointree` and
      to single-binding scopes (the chooser's own condition). Downstream
      reads `whereQual != nil` as "there is a Filter to search under"
      and the pre-DP arm asserts `node.(*Filter)` on it, so a reduced
      scope marks the clause spent (`whereQual = nil`) — without that an
      always-false WHERE still carrying an `EXISTS` would have hit an
      unchecked assertion on a `*Result`.
    - Pins are ARM-EQUALITY pins (`notnull_reduce_jointree_test.go`):
      the property M0145-0008 needs is that flipping the knob cannot
      change these plans.
    - The LIKE-range pair stays ledgered: index selection, not a PG
      shape rule, so no cutover risk.
    - **LIKE-range measured (loop 2026-09-21 \#24), and its resume point
      CORRECTED.** Corpus witnesses: zero. TPC-H has no `LIKE` predicate
      at all; TPC-DS has exactly two (`hd_buy_potential LIKE 'Unknown%'`)
      and that column is unindexed in the benchmark schema, so no index
      path exists to elect on either arm.
    - The earlier resume point ("teach the search's base-rel pathgen
      about the synthetic range conjuncts") is HAZARDOUS taken
      literally: adding `col >= 'foo' AND col < 'fop'` to the rel's
      restriction clauses double-counts the LIKE's selectivity, and no
      corpus query would show it.
    - PG's actual mechanism (`like_support.c` header): the derived
      clauses are approximate INDEX-SCAN quals with the original
      operator re-applied as a qpqual — "using a regular index as if it
      were a lossy index" — so the estimate still comes from the LIKE
      alone. The chooser arm already separates the two lists
      (`whereForIndex` vs `whereQual`); the port must reproduce that
      separation at PATH level rather than merging them.
    - Slice 2 (landed, loop 2026-09-21 #8): pulled semi/anti bodies are
      REAL numbered leaf items — `pullUpSublinksIntoJointree` appends
      one `leafItem` per pulled leaf to `ctx.joinlist` (`pu.base` /
      `pu.nLeaves`), `splicePulledLeaves` inserts the scans at
      `[nReal, nprefix)` and shifts extracted leaf indexes
      (`shiftRelSetAbove` over link hands, SJInfo fields, outer-link
      sides, `belowNullable`), `classifyPulledQuals` rebases+classifies
      quals straight into the conjunct pool and appends each body's
      SJInfo to `joinInfoList`. No `semiAntiChainLink` is produced for
      a pulled body.
    - Retired this slice: `integratePulledSublinks`,
      `buildPulledSemiAntiLink`, the pulled share of the synthetic-leaf
      splice + `searchJl` leafItem-append, and the `admitSemiAnti`
      flag itself (one call site, literal `true` since b2 — dead
      flexibility). `remapWalkOrderFlatToSpans` survives for the chain
      arm with a pulledBase/pulledLeaves index translation;
      `semiAntiOnQualsOK` + the `searchJl` append survive for
      chain-extracted leaves only. `pgShapedOffsetChecksOK` now takes
      the non-emitting RelSet directly (`[nReal, len(scans))`).
    - White-box pins: `TestJointreePullupRealLeafItems`/`…Anti`
      (joinlist leaf item, splice position, cumulative span, pooled
      conjunct relids, SJInfo on `joinInfoList`, `len(semiAnti)==0`);
      DPTRACE probe: `rels=jtp_o,a,b` enumerate + SEMI pair priced.
    - Gates: optimizer suite PASS; tpch-spotcheck Q12=2/Q13=33; SF0.25
      96/96 with 99/99 plan shapes identical; acceptance arm 24/24
      under JOINTREE+PGSHAPED; units PASS. plan-gate 16/22 vs the
      recorded 14/22 stale-baseline drift — the +2 (Q3/Q18) is the
      Sep-20 `:65433` rebuild (slice-1/4 era), not this change: the
      gate diffs the live old binary and Q3/Q18 cannot reach the
      pulled path (Q3 has no sublink; Q18's IN body has GROUP BY).
    - Slice 3 (landed, loop 2026-09-21 #9): IR-direct leaf
      materialisation — `jtScopeTable` (jointreescope.go) records the
      leaf/link structure at `planFromItem` construction time in the
      walk's own DFS order (descendable join = link+leaf; demoted
      semi/anti = link+synthetic leaf; FULL/other = fold to one opaque
      leaf, erasing inner links). `planFromClause` concatenates
      per-item tables and pins `jtScope.root = root`. The seam reads
      `extractScopeLeaves` only when `jointreePipeline &&
      ctx.jtScope.root == chain` — the node walk stays for the legacy
      arm and for post-rewrite chains (S5a Phase B).
    - `extractScopeLeaves` rebuilds the identical (scans, widths,
      onQuals, outer, semiAnti) tuple: width/realWidth as prefix sums
      over the leaf table, base/rightBase sampled at loLeft/loRight,
      belowNullable as range-containment over processed outer-link
      subtrees, `preserved` as the RIGHT-ancestor enclosure test
      (every link sits in its ancestors' left subtree — right subtrees
      are single leaves). Rebases, keyed-pred fold, FlattenedRHS
      expansion and Min-side narrowing run unchanged on jn.Predicate.
    - Did NOT retire (contra the ledger's original line): the
      coordinate-translation family — `rebaseChainQual`,
      `rebaseSemiAntiChainQual`, `remapWalkOrderFlatToSpans`,
      `buildLeafSpans`, `localizeExprToLeaf`, `pgShapedOffsetChecksOK`.
      They translate between two coordinate spaces that both still
      exist (a Join's Predicate is in its own concat coords whatever
      path reads it); the walk's removal removes discovery, not the
      spaces.
    - White-box pins: `TestScopeExtractionMatchesWalk` — 20-shape
      matrix, field-wise identity incl. same scan node pointers,
      reflect.DeepEqual rebased preds, sjinfo pointer identity;
      `TestScopeExtractionDeclinesLikeWalk` — fail-closed arms.
      DPTRACE probe: scope-extracted problems enumerate end-to-end
      (`rels=a,b` pulled EXISTS, SEMI pair priced, searched subtree).
    - Gates: optimizer suite PASS; units PASS; tpch-spotcheck
      Q12=2/Q13=33; SF0.25 96/96 with 99/99 plan shapes identical;
      acceptance arm 24/24 under JOINTREE+PGSHAPED. plan-gate 16/22
      unchanged stale-pin drift (diffs the live Sep-20 :65433 binary,
      which this change cannot affect).
    - Also fixed this loop: reverted a wholesale `gofmt -w` that the
      newer local gofmt had applied to planner.go/joinsearchseam.go
      (repo baseline is go1.25 — `gofmt -w` rewrites unrelated lines;
      re-applied the edits manually).
    - Slice 5-partial (landed, loop 2026-09-21 #10): searched-subtree
      opacity for the residual qual-redistribution family.
    - Census scoping (`tmp/m0144-0011a2-census.log`, pre-slice-3): 132
      seam declines — `leaf-count` 104, `outer-over-derived` 12,
      `outer-spine` 8 (retired by slice 1), `lateral` 8.
      `outer-on-qual`/`inner-on-qual-*` have ZERO corpus witnesses —
      retired by absence, no decline class left to migrate.
      `outer-over-derived` stays (B-06 CTE-output-stats firewall,
      R42/Q78 — B-06 is filed as **M0145-0009**; its completion note
      carries the firewall's re-evaluation checklist); `lateral` stays
      (real deps Q30/Q68 — parameterized-path legality, filed as
      **M0145-0010**; its completion note carries this family's
      re-evaluation).
    - `leaf-count` dominant cause identified by joinlist-vs-bindings
      probe: FULL-join folds (opaque leaf covering multiple joinlist
      rels, `a FULL b JOIN c` → nprefix 3, scans 2) — the
      executor-substrate-blocked class already ledgered as staying.
      Grouped `j.Right` joins ruled out (one binding AND one joinlist
      item — admitted as 2-rel problems).
    - **`leaf-count-overflow` OWNED and CLOSED 2026\-09\-22 \(loop \#5\)** —
      recorded as a deliberate divergence, with the boundary pinned. Movement:
      none — test\-only.
      Design: the design doc's §"The relset ceiling".
      - PG has NO ceiling: `Relids` is a `Bitmapset` \(bitmapset.c\) grown on
        demand. What bounds PG is strategy, not representation — the collapse
        limits \(8\) and `geqo_threshold` \(12\). goopg reproduces the strategy
        half exactly \(`relfromjoinlist.go:846` routes to `geqoSearch` at the
        same default 12\) and diverges on the representation half:
        `RelSet` is `uint32`, `maxSearchRels` 32, refused at three independent
        sites.
      - **Measured live**, 33 tables of 3 rows joined on one key: at 32
        relations the problem is searched \(31 `Merge Join`s\); at 33 it falls
        back to the syntactic tree \(32 `Hash Join`s in FROM order\) and
        **returns the correct answer** \(3 rows, checksum 288\). The ceiling is
        a missed optimisation, never a wrong answer.
      - Corpus witnesses remain **zero** — no sweep's seam census has recorded
        a `leaf-count-overflow` fire.
      - Pinned by `TestSeamDeclinesAtTheRelSetWidth` \(+ the constructor guard
        pin\). The 32\-relation arm is the non\-vacuity control and is
        load\-bearing: at exactly the width the identical fixture IS searched,
        so the refusal is provably the width.
      - **Not widened, and why**: the change is two lines \(`RelSet` ->
        `uint64`, `maxSearchRels` -> 64\) and structurally safe \(nothing
        allocates on `1 << nrels`; GEQO already caps enumeration above 12\), but
        it costs 4 bytes on every `RelSet` field of every `Path`/`RelOptInfo`
        in every query for a population that measures zero. Ledgered with that
        resume point per R6.
    - The pushdown family cannot die wholesale (declined + legacy-arm
      statements still need the post-search cleanups); the correct
      retirement shape is stopping it at the searched boundary. Audit:
      `pushOneConjunct`, `rewriteScanInputs` outer walk and
      `rewriteJoinsToNLI` already pruned (P5.9-b); two holes closed —
      `pushSingleSideQualsIntoInnerJoinInputs` had no `isSearchedTree`
      awareness at any level (walker descent, Filter-level
      `pushInnerJoinInputQuals` into searched join inputs, and the
      `pushConjunctIntoSubtree` descent to searched grandchildren), and
      `findUniqueSeqScanByColumn` (`absorbConjunctsIntoSubtree`'s hunt)
      could IndexScan-rewrite a scan the costed search elected —
      wrong-answer class for nullable-held conjuncts (pushed below the
      null-extension keeps rows the residual drops).
    - Mechanism: `pushTrace.noSearched` flag; all statement-level
      descents route through `pushConjunctIntoSubtreeTracedNoSearched`
      (incl. `deriveConstAcrossJoinEquality`'s sibling seeding).
      `pushConjunctIntoSubtree` stays permissive — CTE-inline conjuncts
      arrive from OUTSIDE the searched body's scope and crossing the
      boundary is PG's own parse-level qual pushdown (R42/Q78 witness);
      a blanket guard there is the regression, not the fix.
    - Pins (`searched_opacity_test.go`): searched-root + searched-
      grandchild refusal, unsearched-side positive control, CTE-inline
      crossing counter-pin, scan-hunt opacity + unsearched
      findability. `markSearchedTree` usable on `*Join`/`*SeqScan` in
      tests (embeds `searchedTree`).
    - **Slice 6 (landed, loop 2026-09-23): chain-extracted semi/anti as
      DEFERRED real leaf items** — slice 2's chain half, retiring the
      synthetic tail for `semiAntiChainLink` on the jointree arm. Design
      doc §"Slice 6".
      - Two-band numbering (`deconstructJointreeScopedSJI` +
        `jointreeItemEmittingRels`): emitting leaves `[0, emitTotal)`,
        every SEMI/ANTI link's RHS a leaf item in
        `[emitTotal, emitTotal+nSemiAnti)` in link order, then the
        pulled band continues. `sub_members` counts deferred members so
        the collapse-limit comparison stays upstream-shaped. The SJI is
        built via `makeSpecialJoinInfoForSets` (explicit relsets — the
        deferred right side is not contiguous in the item's local
        joinlist) with `leftScope` tracking PG's `qualscope`.
      - `sjiLeaf.leafIdx`: `newSjiScope` stamps each leaf's RelSet bit
        (position == index under the legacy numbering; diverges for
        deferred leaves), so `resolveSjiColumn` returns real bits for
        every name-resolution arm.
      - `extractScopeLeaves` emits canonically — emitting band then
        deferred band in joinlist order — and marks semianti links
        `realLeaf` with `rhs` naming the real item; the link's SJI is
        matched off `ctx.joinInfoList` by (jointype, SynL, SynR), so the
        searched constraint carries deconstruction's `lower`-scan
        narrowing rather than the plan-node placeholder.
      - Seam: `nChain = nprefix - nPulled - nrels`; deferred leaves at
        `[nReal, pulledBase)` get the SeqScan-vs-opaque dispatch
        (catalog stats / `EstimateRows`), `realLeaf` links skip
        `syntheticBits`, the walk→span remap and the `searchJl` append
        (nleaves == nprefix there). New `chain-leaf-desync` fail-closed
        check cross-verifies extraction's deferred band against the
        joinlist count.
      - Pins: `TestScopeExtractionSemiAntiDeferred` (joinlist
        `{a=0,c=1,b=2}` for a demoted ANTI, ANTI SJI `{0}`/`{2}`,
        realLeaf link bound to that SJI); `TestJointreeSearchesADemotedAntiLink`
        — `jtp_o ANTI jtp_i ⋈ jtp_i2` plans a searched left-only ANTI on
        the jointree arm where the shape previously declined at
        `leaf-count`.
      - Gates: optimizer suite PASS; units PASS; tpch-spotcheck
        Q12=2/Q13=33; SF0.25 sweep below.
    - **Slice 7 (landed, loop 2026-09-23): Phase A/B retirement on the
      jointree arm** — `runJoinSearchBelowPinned`, the pinned spine and
      the splice/re-resolution family are unreachable on the jointree
      pipeline. Design doc §"Slice 7".
      - The route was still load-bearing: knob-arm census had
        `pinned-spine=285` vs `jointree-pullup=23` — it caught every
        statement whose pull-up declined ALL conjuncts. A probe build
        gating S5a with `!jointree` measured the retirement first:
        SF0.25 knob-arm EXPLAIN capture vs the pre-gate baseline is
        `same=99 changed=0` — Phase A searched the same problem the
        Filter arm does and the post-hoc unnest pins the same spine
        Phase B's splice produced.
      - Change: `unnestPreDPEnabled() && !jointree && ...` on the S5a
        gate (planner.go) — declined pull-ups take the Filter arm's
        single-pass search, `unnestSubqueriesInPlan` pins the spine
        afterwards (`preDPUnnested` stays false on this arm).
      - New census route `jointree-posthoc` (nlicensus.go) for the
        redirected population; `sublinkRouteCounts` is the test-visible
        twin of the stderr census.
      - Machinery stays for the legacy arm until M0145-0008 — doc
        comments on `runJoinSearchBelowPinned`, `tryJoinSearch`,
        `splitOuterSpine`, census consts updated to say so.
      - Pin: `TestJointreeArmBypassesThePinnedSpineRoute` — declined
        uncorrelated-EXISTS over a 2-table FROM: `pinned-spine=0` +
        `jointree-posthoc=1` on the knob arm, `pinned-spine=1` on
        legacy; `TestJointreePullupDeclineParity` still passes all
        classes.
      - Remaining for the task: slice-5 ledgered families
        (`outer-over-derived` post-B-06, `lateral`).
    - **CLOSED 2026-09-23 (loop \#14).** Every named retirement in the
      task text is landed on the jointree arm — Phase A/B +
      `runJoinSearchBelowPinned` + the pinned spine + splice-time
      re-resolution are unreachable there (slice 7, `same=99
      changed=0` probe evidence), and `admitSemiAnti` is unconditional
      in production. Their code deletion is M0145-0008's scope, not
      this task's. Every remaining bullet is ledgered or filed:
      - `outer-over-derived` — RESOLVED by M0145-0018 (firewall
        retired; seam census `3 → 0` on both arms).
      - `lateral` — filed as **M0145-0010** (recon + design doc
        landed; the `lateral_relids`/`direct_lateral_relids` port).
      - `leaf-count` residual (10 fires post-0013/0016/0018) —
        already-planned composite leaves (`*Project`/`*Gather`/NLI/
        `*CTEScan`) no admission predicate decomposes; the
        positional-identity `*Project` descent is **M0144-0003a**'s
        filed scope, whose recorded wall (resolver-time lowering via
        M0145-0003/0004) is now discharged — flagged for owner
        re-adjudication of the `[!]` mark.
      - `leaf-count-overflow` — measured live, recorded as a
        deliberate divergence, pinned (`TestSeamDeclinesAtTheRelSetWidth`).
      - `residual-hits-pad` (4 fires) — ledgered under M0145-0013's
        row (census the class, name wall-vs-bug).
      - Missed-opts skips (`injectLikeRangePredicates`,
        `planIndexScanFromWhere`, the default-arm ONEREL reduction
        gap) — three deferral-ledger rows with corrected resume
        points; `reduceNotNullQuals` closed.
      - Depends-on M0145-0004 — satisfied (whole-chain flattening via
        0004a; CTE-wrapped residual ledgered under 0004).
      Nothing selectable remains inside this task.
  Movement: none — bookkeeping closure; every movement was recorded
    under its own slice/task entry.
- [x] **M0145-0006 — upper-rel pathlists** (extend the lattice through
  `create_grouping_paths`/`create_ordered_paths` analogues so ordering and
  grouping are elected over candidate sets, not by stage-builder
  producers). Absorbs D4's residual gates: `inputNodePathkeys`'s
  `default: nil` tops (`*MergeJoin`/`*GatherMerge`/`*IncrementalSort`/
  `*WindowAgg`), `electOrderedGrouping`'s `gate-precondition` skip when
  `node != agg.node`, and `electOrderedDistinct`'s surviving `cands<2`
  gate (upperordereddistinct.go:140).
  Kind: impl
  Parent: M0145-0005
    - Slice 1 (landed, loop 2026-09-21 \#11): the three order-delivering
      tops `inputNodePathkeys`' walk swallowed in its `default: nil`.
      Design doc: `docs/design/0100-0149/m0145-0006-upper-rel-pathlists.md`.
    - `*IncrementalSort` claims the FULL `Keys` list — `PresortedCount`
      bounds the WORK the node does, never the order it emits
      (`create_incremental_sort_path`, pathnode.c:3191, takes the whole
      list too).
    - `*GatherMerge` claims `Keys`: the leader's merge preserves the
      worker ordering, which is exactly what distinguishes it from
      `*Gather` (pathnode.c:2128 vs the nil `create_gather_path` sets).
    - `*WindowAgg` descends to the CHILD's claim
      (`create_windowagg_path`, pathnode.c:3740-3741: "WindowAgg
      preserves the input sort order"), but ONLY when `Presorted` — with
      the flag false goopg's executor sorts the input privately by
      `PartitionBy ++ OrderBy` and records no direction for that sort, so
      the arm fails closed rather than reconstructing one.
    - New coordinate rule: the walk carries a `limit` (how many LEADING
      columns of the published output the current space must agree with)
      and narrows it when crossing a `*WindowAgg`, because the window
      functions are APPENDED to the child's schema so the child's columns
      keep their positions. The prefix is re-checked with
      `schemaCoordinatesAgree(out[:len(child)], child)`, never assumed —
      a node that PREPENDS still returns nil.
    - Witness: TPC-DS SF0.25 **Q51** lost its redundant top Sort
      (`Sort Key: item_sk, d_date` over the WindowAgg), cost
      `419.08..422.64` -> `277.76..381.02`, rows unchanged at 100. It is a
      parity move: PG 18.3 on the same dataset plans
      `Limit -> Subquery Scan -> WindowAgg -> Sort -> Merge Full Join`
      with no second Sort.
    - Slice 2 (landed, loop 2026-09-21 \#12): the merge-join top.
    - The ledgered plan (stamp `p.Pathkeys` onto the node) was WRONG about
      where the gap is: `stampSearchPathkeys` already stamps every merge
      join the PG-shaped search produces, and the walk reads that stamp
      before its type switch. What has no stamp is the LEGACY
      constructor's merge join — `chooseInnerJoinAlgo` (joincost.go:33)
      elects merge on cost and `chooseOuterFillJoinAlgo` leaves
      RIGHT/FULL on merge — which is the route every seam-declined
      statement takes.
    - Four soundness conditions, each a wrong-ordering guard: the
      comparator direction is FIXED (ascending / NULLs-last,
      `mergeSortedSource.less`), the join-type rule routes through
      `buildJoinPathkeys` (FULL/RIGHT claim nil), the key list is the
      EXECUTOR's `ExecMergeKeyPlan().Keys` and not `HashKeys` (an
      unsafe pair is dropped into the residual, so `HashKeys` would
      claim an ordering the sort never keyed), and the result is run
      through `validatedSearchPathkeys` against the node's own schema.
      An unsafe LEAD key refuses outright — `Keys[0]` is kept
      unconditionally, so it may be ordered by `compareDatum` while SQL
      `=` disagrees.
    - A searched join with an EMPTY stamp returns nil rather than
      re-deriving: the search was given the chance to claim and
      declined, and a weaker mechanism must not overrule it.
    - NO corpus witness: SF0.25 shapes `same=99 changed=0` and the
      acceptance arm 24/24 — every merge join those 121 statements bring
      to the seam is search-built and already stamped. The unit pins are
      the guarantee; the corpus is the no-regression channel only.
    - Slice 3-partial (landed, loop 2026-09-21 \#13): the election sees
      through a rename.
    - `electOrderedGrouping` required the seam input to BE `agg.node`;
      it now admits a chain of positional-identity `*Project`s
      (`identityProjectChainTo`, sharing `projectIsPositionalIdentity`
      with `inputNodePathkeys`' walk so the two routes agree on which
      projections are transparent).
    - The SPLICE is what needed pinning: the elected spec is copied back
      onto `agg.node` in place, so the chain already carries it, but the
      winner was BUILT over the bare aggregate — returning that node
      drops the projection and publishes the aggregate's labels. A sort
      winner is re-parented over the chain; a bare-aggregate winner
      returns the chain.
    - MEASURED FIRST (`dpTrace` probe, three shapes): the plain aliased
      `GROUP BY … ORDER BY` already elected (its rename Project is added
      ABOVE the ORDER BY stage), the subquery-rename shape has no
      grouping surface in that scope at all, and the reachable decliner
      is `Filter{Aggregate}` — HAVING. So the admitted Project case has
      no witness among the probed shapes; report it as capability, not
      movement.
    - HAVING is NOT admitted with it, deliberately: `addOrderedPaths`
      prices its sort arm from the input path's Rows/Cost, which for a
      `PathAgg` are PRE-HAVING, while the normal `createOrderedPaths`
      call it would replace prices from the finished `Filter` node.
      Admitting the filter without pricing it swaps an accurate cost for
      an optimistic one. PG has no gap here — HAVING quals live on the
      `AggPath` (`create_agg_path`'s `qual`), so its pathlist entries
      already carry post-HAVING rows. Ledgered with that resume point.
    - Slice 4 (landed, loop 2026-09-21 \#14): `electOrderedDistinct`'s
      candidate minimum drops from 2 to 1.
    - The ledgered reading was WRONG a second time: this was never a
      candidate-SUPPLY gap. `addDistinctPaths` always files both the
      hashed and the unique-over-sorted candidate; what removes the
      second one is `add_path` DOMINANCE, downstream of supply. The gate
      was refusing the election whenever one candidate simply lost.
    - PG has no minimum — `create_ordered_paths` iterates the whole
      input pathlist (`planner.c:5337`) — and M0144-0011a-2 already made
      exactly this change on the grouping twin, so the gate was a
      sibling-path divergence (`pattern_sibling_paths_must_agree`).
    - Witness (`dpTrace` A/B, `select distinct c from t order by c` with
      `enable_hashagg = off` so only one candidate survives): the old
      gate declined and left a `*Sort` over the DistinctOn; the new one
      elects the bare `*DistinctOn`. The unique candidate's producer
      Sort orders every output column ascending, so the ORDER BY key is
      a prefix of what it already delivers and the Sort was redundant.
    - Both corpora stay plan-identical (SF0.25 `same=99 changed=0`,
      acceptance 24/24): neither benchmark runs a lone-candidate DISTINCT
      with an ORDER BY, so the probe is the witness here.
    - Slice 5 (landed, loop 2026-09-21 \#15): the HAVING filter,
      admitted WITH pricing — closing the residue slice 3 ledgered.
    - `seeThroughChainTo` replaces `identityProjectChainTo`: it also
      crosses `*Filter`s and RETURNS the quals it crossed. The filter is
      ordering-transparent (same schema, removes rows without
      reordering), but its predicate is priced onto every candidate by
      `applyHavingQualsToOffer`, which is PG's `cost_agg` `if (quals)`
      arm — `total_cost += qual_cost.startup + output_tuples *
      qual_cost.per_tuple` then `output_tuples = clamp_row_est(
      output_tuples * selectivity)`.
    - The ORDERED rel is now sized from `node` (post-qual) rather than
      `agg.node`; identical objects when nothing wraps the aggregate, so
      the unwrapped path is byte-unchanged.
    - A/B on a `GROUP BY … HAVING … ORDER BY` statement: decline
      `gate-precondition` before, `elected shape=Sort-over-Aggregate`
      after, SAME `Project -> Sort -> Filter -> Aggregate` shape — the
      grouping twin's M0144-0011a-2 result (shape-inert, cost-different)
      on that statement.
    - CORPUS MOVEMENT, unlike slices 2-4: TPC-DS SF0.25 moves two
      shapes, both HAVING statements, with `PASS=96 MISMATCH=0`.
      **Q24 is a parity move** — `Sort{HashAggregate, Filter: sum(...) >
      InitPlan}` became `GroupAggregate{Filter, Sort{...}}` with no top
      Sort, and PG 18.3 on the same dataset plans
      `GroupAggregate … Filter: (sum(ssales.netpaid) > (InitPlan 2).col1)`
      with no top Sort either. **Q6** is cost-only
      (`35244.72..35244.74` -> `35244.96..35244.98`).
    - Deferred (ledger rows 2026-09-21):
      `electOrderedGrouping`'s `node != agg.node` precondition, and
      `electOrderedDistinct`'s `cands<2` gate (really a
      `createDistinctPaths` candidate-supply gap).
- [x] **M0145-0007 — single Path→Node lowering** (the `create_plan`
  analogue): consolidate every post-election resolution step the stage
  builders currently interleave — per 0001's lowering-contract inventory —
  into one pass that runs once on the elected path tree. During
  transition the legacy stages must still fire on the old pipeline only;
  a lowering bug must never silently reach the default pipeline.
  Kind: impl
  Parent: M0145-0005
    - Recon complete + slice 1 landed (loop 2026-09-21 \#16). Design doc:
      `docs/design/0100-0149/m0145-0007-single-path-node-lowering.md`.
    - The recon RE-ORDERED the four items M0145-0001 assigned here, and
      two of them turn out not to be refactors at all.
    - `translateToLayout` is ALREADY lowering-local: every call site
      sits in a `createplan*.go` file (4 join, 3 nl, 2 simple, 1 gather;
      none elsewhere). Retired by absence. Slice 1 converts the
      measurement into an enforced invariant
      (`TestTranslateToLayoutIsLoweringLocal`), verified to FAIL when the
      exclusion is lifted, so it is a guard and not a tautology.
    - `fillJoinHashKeys` must retire LAST, not first. Its lateness is a
      deliberate defence: `Predicate` is mutated after join construction
      by predRebind, the bushy sub-remap, constant folding,
      `lowerSubPlanParams` and the qual-placement passes, and a field
      filled early is one every mutator must maintain — the one time
      that was missed, chained NATURAL JOINs probed the wrong column
      (M0097-0060). It can fold into the join arm only after the
      mutators are gone.
    - `rewriteJoinsToNLI` + `stampSemiProbePrices` need no folding:
      `walkRewriteNLI` already returns at `isSearchedTree` (P5.9-b) and
      the stamp mirrors its descent exactly, so both serve ONLY the
      population the PG-shaped search never built. They die with the
      legacy pipeline at M0145-0008. What 0007 owes is a MEASUREMENT
      that the search's NLI paths cover that population (slice 2).
    - The splice/re-resolution family is the real work and is entirely
      live: `reresolveJoinByName` 28 non-test refs, `remapByPosMap` 16,
      `applyJoinTreePosMap` 10, `remapWithBindings` 9, `layoutPosMap` 7,
      `remapPosMapAfterRewrite` 5, `remapSublinkOuterRefs` 4,
      `spliceSearchedSpine` 3, `remapExprRefsToMHJ` 2.
    - Five-slice plan in the design doc; slice 3 is the
      `OuterColumnRef` outer-layout mapping, slice 4 the rest of the
      re-resolution family, slice 5 the hash keys.
    - Slice 2 (landed, loop 2026-09-21 \#17): the NLI route census
      (`internal/optimizer/nlicensus.go`, `GOOPG_NLI_CENSUS=1`, one
      stderr line per built node carrying route + join type + probed
      index; registered in `flagProvenanceExempt` as diagnostic-only).
    - TPC-DS SF0.25 (99 queries): search 143 nodes, all INNER; rewrite
      **1** node, INNER, probing `date_dim_pkey`.
    - TPC-H SF1 (22-query acceptance arm): search 11 nodes, all INNER;
      rewrite **4** nodes — **2 SEMI + 2 ANTI**.
    - THE REWRITE IS NOT DEAD CODE, and the shape of its population is
      the finding: on TPC-H every node it builds is a SEMI or ANTI join
      and the search builds none of those. Same fact
      `stampSemiProbePrices`' header states from the other side ("the
      search never sees the unnested relset"), now counted.
    - **CORRECTED 2026-09-21 (loop \#25)**: the cause named below is
      wrong. `addNLIPaths` (joinpathsnli.go:280) declines only
      `JoinRight` — its comment states the admitted set as
      "Inner/Left/Semi/Anti" — so the join TYPE was never the gate. The
      real mechanism is that the default arm's semijoin is built by the
      legacy unnest AFTER the search, so no joinrel exists to file a
      path against. Knob-arm census: TPC-H rewrite-built drops from 4
      (2 SEMI + 2 ANTI) to **1 SEMI** because the pull-up feeds three of
      them to the search — which then elects a non-NLI method, leaving
      search-built SEMI/ANTI NLIs at zero on both arms. The open
      question is whether an NLI path is generated and out-costed or
      never generated; the next probe instruments `addNLIPaths`' other
      decline points (`uniq == uniqueSideOuter`, `o.RequiredOuter != 0`,
      the `param_source_rels` test), not its join-type set.
    - **ANSWERED 2026-09-21 (loop \#26): the paths ARE filed, and the
      claimed cutover blocker is REMOVED.** `noteNLIPathGate` reports
      `addNLIPaths`' outcome per semi/anti joinrel; on the knob arm
      TPC-H gives `semi gate=filed` and `anti gate=filed` — no gate
      declines. The search generates the NLI path and `add_path`
      out-costs it, preferring a hash or merge semijoin. Deleting
      `rewriteJoinsToNLI` at the cutover therefore removes the legacy
      route's OVERRIDE, not the capability. Whether the cost preference
      is right is a separate, live cost-accuracy question
      (`stampSemiProbePrices` exists because a rewrite-built NLI carries
      no path price), but it does not gate M0145-0008.
    - ~~**Hard constraint for M0145-0008**~~ (superseded by the line
      above): deleting `rewriteJoinsToNLI`
      with the legacy pipeline deletes the ONLY route that builds a
      SEMI/ANTI index-probe join. The cutover depends on `addNLIPaths`
      electing SEMI/ANTI NLI paths first (it files INNER/LEFT today) —
      a pathgen task, not a lowering one. Ledgered.
    - Slice 3 (landed, loop 2026-09-21 \#18): the sublink-route census,
      and it lands the same verdict as slice 2 — this task's remaining
      items are CUTOVER-blocked, not lowering refactors.
    - Caller tracing first: `spliceSearchedSpine` and
      `remapSublinkOuterRefs` have NO callers outside `predp.go`, and
      every live `layoutPosMap`/`remapByPosMap` call is in `predp.go`
      too. The splice/re-resolution family is not the lowering walk's
      leftovers — it is ONE route's machinery, the post-search
      re-resolution of the pinned semi/anti spine.
    - Census (`SUBLINKCENSUS` lines, same env gate; counts exceed the
      query count because subplans and CTEs plan recursively):
      default arm TPC-DS 207 pinned-spine / 0 jointree-pullup; default
      arm TPC-H 16 / 0; **knob arm TPC-DS 294 / 5**.
    - So even with `GOOPG_JOINTREE_PIPELINE=1` the pull-up handles under
      2% of sublink-planning events — exactly what M0145-0003 landed and
      ledgered (flat EXISTS/NOT EXISTS only; IN/NOT IN, non-flat bodies
      and outer-local-only correlation deferred). Everything else falls
      back to the pinned spine on BOTH arms.
    - Therefore slice 4 is blocked on **M0145-0003**, not on M0145-0005's
      clause distribution as the recon first assumed.
    - **Blocker RE-TESTED (loop 2026-09-21 \#25) — it HOLDS, with a new
      number.** The block was stated as a measured ratio and two
      M0145-0003 changes landed after that measurement (the ANY arm and
      the body-local-qual fix `fef25625d`), so the condition was
      re-measured rather than carried forward.
      - Knob arm: 291 pinned-spine / **17** jointree-pullup (was
        294 / 5) — coverage 1.7% -> **5.5%**, 3.4x more pulled
        conjuncts. Default arm: 207 / 0, byte-identical to before —
        the control proving every movement is knob-arm only.
      - Verdict UNCHANGED: 94.5% of sublink-planning events still take
        the pinned spine, so the splice/re-resolution family cannot
        retire here. The blocker no longer cites "under 2%"; it cites
        5.5%.
      - Of the 60 conjuncts the pull-up examines (vs 308 total events —
        248 never reach it): 21 pulled, 15 `CTEScan` bodies, 15 scalar
        `SubqueryExpr`, 6 nested-sublink, 3 residual. The 15 scalar
        declines are CORRECT (PG has no `EXPR_SUBLINK` conversion).
      - `PULLUPCLASSIFY` now fires ZERO refusals — the
        `body-qual-not-consumable` class is gone, independently
        confirming `fef25625d`.
      - The one lever on this ratio is the CTEScan-body class (15),
        whose blocker is B-06 = **M0145-0009** — whose census has since
        shown the column channel is exhausted upstream; the measured
        re-evaluation of that class is **M0145-0011** (E2 arm —
        DONE: the pull-up gate now admits the leaves and the class
        relocated to the seam's `pulled-leaf-not-scan`, filed as
        **M0145-0013**). Resume there, then re-run this census and
        re-adjudicate slice 4.
      - **Consequence for M0145-0008**: its text requires this task, so
        the cutover is NOT selectable as written — even though BOTH of
        its named TIMING blockers are now discharged (gap 1.06x).
        Whether the flip can be split from the legacy-pipeline deletion
        is a scoping decision for the banner's owner.
    - **Slice-4 blocker RE-ADJUDICATED 2026-09-23 (loop \#13) —
      DISCHARGED by architecture, verdict changed to NO-FOLD.** The
      2026-09-21 measurement predates M0145-0005 slice 7
      (`15fbcf78d`), which retired the pinned-spine route on the
      jointree arm entirely. Fresh `GOOPG_NLI_CENSUS=1` +
      `jointree-parity-capture` on private clones, both corpora:
      - Route counts (`SUBLINKCENSUS`): default arm SF0.25 = 207
        pinned-spine / TPC-H = 16 (byte-stable control); knob arm
        SF0.25 = **0 pinned-spine, 23 jointree-pullup, 22
        jointree-posthoc**; knob arm TPC-H = **0 / 5 / 4**. Declined
        conjuncts now route to `jointree-posthoc`
        (`unnestSubqueriesInPlan`, planner.go:2075), which never
        touches `predp.go`.
      - Denominator correction: the old knob-arm 291 counted every
        WHERE-bearing statement S5a fired for (its eligibility test is
        vacuous for sublink-free predicates); the jointree-arm notes
        fire only when sublinks are present. Honest sublink-bearing
        events: SF0.25 = 45 (51% pulled), TPC-H = 9 (56%).
      - `PULLUPCENSUS` knob arm: SF0.25 = 27 pulled + declines
        `CTEScan`-leaf 15 (B-06 / M0145-0009; `GOOPG_PULLUP_CTE_LEAF`
        stays measurement-only OFF), `SubqueryExpr@scalar` 15 (correct
        — PG does not convert `EXPR_SUBLINK`), `ExistsExpr@or` 2,
        `InExpr@or` 1. TPC-H = 6 pulled + scalar 4, `InExpr@top` 1,
        `any-body-not-simple` 1.
      - Caller trace post-slice-7: `spliceSearchedSpine` (predp.go:207
        only), `layoutPosMap`/`remapByPosMap`/`remapSublinkOuterRefs`
        live only in `predp.go`; `runJoinSearchBelowPinned`'s sole
        caller is planner.go:1932 under `!jointree`. Four recon-named
        members (`applyJoinTreePosMap`, `remapWithBindings`,
        `remapPosMapAfterRewrite`, `remapExprRefsToMHJ`) no longer
        exist as functions — absorbed since the recon.
        `reresolveJoinByName` + `remapOuterRefsInSubplan` are shared
        `joinlayout.go` machinery serving the posthoc route on BOTH
        arms — not splice leftovers.
      - Verdict: slice 4 has NO fold work. The `predp.go` members die
        wholesale at M0145-0008's cutover (they must NOT be deleted
        now — the default arm still fires them 207/16 times); the
        M0145-0003 coverage gate recorded above is obsolete — coverage
        now selects pullup-vs-posthoc, never whether the splice
        family runs. Remaining 0007 work: slice 5
        (`fillJoinHashKeys`) only.
    - TRAP, recorded in the design doc: a plans capture taken under
      `GOOPG_JOINTREE_PIPELINE=1` lands in the same results directory the
      next DEFAULT sweep diffs against. The sweep after this census
      reported `same=74 changed=25` purely because its baseline was the
      knob-arm capture; diffing the two default-arm captures directly
      showed them byte-identical.
    - Attribution of the single TPC-DS INNER fire is still open:
      `cmd_plans` captures all 99 queries regardless of `QUERIES`, so a
      bisect through that channel measures the same full run each time.
      Use the `sweep` channel (which does honour `QUERIES`) or add an
      outer-relation field to the census line.
    - **Slice 5 RE\-ADJUDICATED 2026\-09\-23 \(ralph2 loop \#1\) — already
      folded; task CLOSED.** Design doc §"Slice 5 RE\-ADJUDICATED".
      - `createHashJoinPlan`/`createMergeJoinPlan` already publish
        `HashKeys` at construction from the path's key list; the late
        `fillJoinHashKeys` only recomputes it. So the question was whether
        the recomputation ever differs — measured with a throwaway
        structural probe \(`GOOPG_HASHKEY_PROBE=1`, not committed; patch
        `docs/design/0100-0149/m0145-0007-hashkeyprobe.patch`\), `jointree-parity-capture.sh`
        on private clones.
      - **0 structural drift on 670 lowering\-built joins** \(SF0.25 knob
        328, SF0.25 default 313, TPC\-H knob at `PGSHAPED=1` 29\): the
        mutators item 2 feared do not move searched keys. A first
        pointer\-identity run reported 10 false diffs — copy\-on\-write
        rebuilds into equal trees; compare structurally.
      - One real difference: a DUPLICATE pair in the path key list on 5
        Q78 ANTI hash joins \(`ss_ticket_number=sr_ticket_number` twice\),
        de\-duplicated only by the late pass → filed **M0145\-0026**.
      - The late pass's necessary population is joins built OUTSIDE
        lowering: SF0.25 knob 33 / default 51, TPC\-H knob 6 — posthoc
        `unnest.go` builders, `planner.go` FULL merge, `pushdown.go`
        CROSS→hash promotion, and shallow copies. They survive the 0008
        cutover, so `fillJoinHashKeys` stays; ledgered.
      - TRAP: `jointree-parity-capture.sh tpch` inherits
        `tpch-estimate-audit-arm.sh`'s `PGSHAPED=0` default — the first
        TPC\-H run measured the legacy DP \(0 lowering\-built joins\).
        Pass `PGSHAPED=1` for the shipped planner.
      - Movement: none \(measurement\).
- [x] **M0145-0008 — cutover**: flip `GOOPG_JOINTREE_PIPELINE` default,
  re-run the full corpus gates on the new pipeline (sf025 sweep,
  tpch-spotcheck, acceptance arm, plan-parity capture), then delete the
  legacy pipeline and the retired seam guards from 0001's list. Requires
  the executor-substrate tracking note: parallel-hash-build and
  row-emitting PartialAgg (plan-flow doc D3; M0144-0011c's Materialize
  sizing) are parity-enablers that stay OUT of this milestone — the
  cutover records which census records remain `unexpressible` for that
  reason so the executor milestone inherits an exact list (filed homes —
  **updated 2026-09-23**: **M0146-0002** for the partial-inner hash build
  (re-scoped from M0140-0007), **M0146-0003** for row-emitting PartialAgg
  (adopting M0141-S3–S6), **M0146-0004** for per-worker Memoize,
  **M0146-0010** for Materialize). Requires
  M0145-0006 as well as 0007: the cutover must not retire the stage
  builders while upper-rel elections still live in them.
  **Prerequisite sub-inventory (owner direction 2026-09-22,
  progress-report §5.6)**: before the flip, the cutover must produce an
  explicit executor-capability ledger — every shape the jointree arm can
  elect that the executor cannot run — so no "generatable but
  unexecutable" plan survives the cutover. Known members: (1) the
  parallel hash build over a partial inner (M0140-0007); (2)
  row-emitting PartialAgg (M0141-S3–S6); (3) the partial-NLI executor
  arm — M0145-0010 scope (d) already measured half of this; (4) the
  `appendrelMember`-root Q17-class defect, ledgered 2026-09-21 with its
  witness unmeasured — the inventory must either measure it or carry it
  forward with a named owner task. NLI rewrite/stamping populations are
  NOT inventory members: they only exist for shapes the PG-shaped
  search never builds and retire with the legacy pipeline.
  Kind: impl
  Parent: M0145-0007
  - **BLOCKED by a measured timing regression (loop 2026-09-21 \#27).**
    Design doc:
    `docs/design/0100-0149/m0145-0008-cutover-readiness-timing-ab.md`.
    - Every gate in this milestone compares VALUES, and the two arms are
      value-identical (acceptance arm 24/24 on both), so a cost
      misjudgement is invisible to all of them. Runtime is the only
      channel that can see one — which is why this A/B exists.
    - Two acceptance-arm runs, TPC-H SF1, serial, fresh capped server
      each, **identical engine-id and binary** (`3e61809f585fd51c`), the
      only difference being `GOOPG_JOINTREE_PIPELINE`.
    - The pipeline is timing-NEUTRAL on 19 of 24 labels (within ±10%).
      The entire 1.64x total is four sublink queries:
      **Q4 35.1x** (0.37s -> 12.98s), **Q20 27.5x** (0.13s -> 3.58s),
      **Q17 20.0x** (0.38s -> 7.61s), **Q21 7.2x** (2.63s -> 18.94s).
      **Q22 0.8x** is the control — being a sublink query is not
      sufficient to regress.
    - Q4/Q20/Q21 are the semijoins M0145-0007 traced: the search files
      an NLI path (`gate=filed`) and `add_path` out-costs it, and the
      plan it prefers instead runs 7-35x slower. That ANSWERS the
      ledgered cost question — the preference is not merely unvalidated,
      it is wrong wherever it fires.
    - Q17 is a correlated SCALAR subquery the pull-up does not touch
      (PG does not convert `EXPR_SUBLINK` either), so its 20x is a
      DIFFERENT, unattributed mechanism — do not fold it into the
      semijoin story.
    - Flipping the default today would make TPC-H 1.64x slower with every
      value gate green. Two prerequisites are filed: the semijoin NLI
      cost comparison (log the filed-vs-winning path costs at `addPath`
      for Q4/Q20/Q21) and Q17's mechanism.
    - **Both prerequisites are now discharged as diagnoses (2026-09-21).**
      - The semijoin one is FIXED, not merely answered: `fef25625d`
        (M0145-0003, body-local quals are base restrictions) took Q4
        12.98s -> 1.02s and Q21 18.94s -> 2.12s, knob-arm total
        102.46s -> 77.37s, arm-vs-arm gap 1.64x -> 1.24x, values
        unchanged. The reading that `add_path` out-costs the NLI was
        itself wrong and is retired in the design doc.
      - **Q17 is ATTRIBUTED (this loop), and the note above it is
        wrong**: the arms are NOT plan-identical. Serial A/B on one
        clone, identical values — default 1021 ms keeps PG's shape
        (`Filter: l_quantity < (SubPlan 1)`, est. cost 32301); the knob
        arm decorrelates into a `HashAggregate` over a full
        6,001,988-row `Seq Scan` (est. cost 224656, i.e. goopg's own
        model prices the elected plan at 7x the default's, so the cheap
        candidate is never GENERATED — the Q4 failure class again). PG
        18.3 keeps the `SubPlan`, so the knob arm is the unfaithful one.
      - Mechanism, measured by instrumenting `canUnnestSubquery`'s
        S6/D6.2 guard on both arms: the guard is intact, but the arms
        hand it different bodies —
        `Project(Aggregate(BitmapHeapScan(BitmapIndexScan)))`
        probeCheap=true (refuses) vs
        `Project(Aggregate(Filter(SeqScan)))` probeCheap=false
        (unnests). `innerPlanIsIndexProbeCheap` is a SHAPE predicate
        used as a cost proxy, and it is sound only after index
        selection has run on the body.
      - Fix deferred to its own loop ON PURPOSE (ledgered): `unnest.go`
        is shared with the DEFAULT shipping pipeline, so touching the
        guard is a default-arm plan-shape change needing the full value
        + timing gate set. Recommended direction is arm-local — have
        the jointree route select the body's index path before the
        unnest decision, so both arms hand the guard the same body.
    - **Q17 FIXED (loop 2026-09-21 \#24), and the attribution above was
      too narrow.** Instrumenting body planning AND the guard shows no
      drift between them: the body is BORN without its index path, so
      the defect is in body planning, not the unnest pass.
      - The rule-based `isSimpleSingle` bypass is the ONLY producer of
        an index path driven by a correlated restriction
        (`planIndexScanFromWhere`), and THREE routes skip it —
        `jointree`, `oneRelSearchEnabled()` and `appendrelMember`.
      - Decisive test: the **DEFAULT** arm with
        `GOOPG_ONEREL_SEARCH=on` reproduces the regression exactly
        (10625 ms vs the bypass's 1021 ms). The defect is
        **route-borne, not arm-borne**, and is reachable on the
        shipping arm behind a documented flag — same family as the
        ledgered `c07-single-rel-never-reaches-ordered-index-producer`.
      - Fix offers the bypass's producer at the end of the generic arm,
        gated STRICTLY NARROWER than the bypass it restores: it fires
        only when the search elected no index path at all
        (`planIsBareSeqScanTree`, fail-closed), so it can never
        displace a costed index choice.
      - Result: all three routes produce PG's shape with identical
        values — jointree 11155 ms -> 881 ms, ONEREL\_SEARCH 10625 ms ->
        700 ms, bypass control unchanged. Knob-arm acceptance:
        **Q17 8.52s -> 0.39s** (default 0.38s), total 77.37s -> 65.97s,
        arm-vs-arm gap **1.24x -> 1.06x** (1.64x at the original A/B).
      - `appendrelMember` deliberately excluded and ledgered (no
        witness measured on that route).
      - **Both named cutover prerequisites are now discharged.** What
        remains before flipping the default is the cutover's own
        corpus-gate re-run, not a known defect.
  - **Executor\-capability sub\-inventory PRODUCED 2026\-09\-23 \(ralph2
    loop \#2\) — the unexecutable set is EMPTY; one new readiness finding.**
    Design doc: `docs/design/0100-0149/m0145-0008-executor-capability-inventory.md`.
    - Named members, classified from code: \(1\) Parallel Hash over a
      partial inner — never generated \(`joinpathsparallel.go` never reads
      `inner.PartialPathlist`\); \(2\) row\-emitting Partial aggregate —
      never generated \(`addPartialAggSplitArm` builds Gather only\);
      \(3\) partial NLI — generated and executable; \(4\)
      `appendrelMember` Q17\-class body — timing gap, not capability.
      \(1\)\-\(2\) are parity floors with their filed homes, not cutover
      blockers.
    - Corpus execution on the knob arm: TPC\-DS fire set 25/25 PASS at
      SF0.25 and at SF1 \(M0145\-0018 run; no `internal/`/`cmd/` change
      since\); TPC\-H full acceptance arm 24/24 value\-identical to the
      default baseline \(`tmp/m0145-0008-inv-acceptance-knob.txt`\).
    - **NEW: Q20 is 29x slower on the knob arm** \(0.13 s → 3.80 s\),
      hidden inside the 1.06x total that this record carried as "no known
      defect". The knob arm decorrelates the correlated scalar subquery
      nested inside the `IN` body into a whole\-lineitem HashAggregate;
      PG and the default arm keep the SubPlan. Filed **M0145\-0027**;
      it should land before the flip.
      - FIXED 2026\-09\-23 by M0145\-0027 \(`a80588e30`\): knob Q20 0.18 s,
        knob/default total 1.01x.
    - **Pre\-flip floor check 2026\-09\-23:** knob SF0.25 read match=1 \(Q41
      lost\) → fixed by **M0145\-0028**; knob now SF0.25 match=2 \(= floor\),
      TPC\-H 2/22 \(= default; the parallel floor of 3 is unmet on BOTH
      arms — M0145\-0025's re\-baseline\). Next slice: the flip itself.
    - **Flip unit\-suite triage 2026\-09\-23 \(ralph2 loop \#5\)** — design doc
      `docs/design/0100-0149/m0145-0008-flip-test-triage.md`. A local flip
      \(`jointreePipelineFromEnv`: `v == "1"` → `v != "0"`\) fails **54**
      unit tests \(37 optimizer, 17 executor\).
      - Correctness claim checked first: `TestUnnestCorrelatedIn_Rejects*`
        guard a LEGACY keying bug; on the new pipeline the correlated IN
        becomes a semi join with BOTH conditions in its predicate — PG 18.3
        converts it the same way \(LATERAL, `convert_ANY_sublink_to_join`\).
      - Group L \(31 legacy\-machinery tests\): pinned with
        `pinLegacyPipeline\(t\)` — test\-only commit `3b8b771f6`.
      - Group K \(2, polarity \+ `planner-flags.env`\): change in the flip
        commit. Groups I \(≥10, one\-rel index\-path coverage\) and B \(11
        behavioural\) are NOT filed: the S4 lineage budget of root
        M0145\-0001 is exhausted — held in its escalation block.
      - Script audit: `tpcds-sf025-regression.sh:310` and
        `tpch-estimate-audit-arm.sh:108` default the knob to `0` — must
        change in the flip commit or the gates keep measuring legacy.
    - TPC\-H capture reads `match=2/22` on BOTH arms at `PGSHAPED=1`
      \(floor 3, inside the ±3 band, arm\-independent\) — for
      M0145\-0025's re\-baseline.
    - Movement: none \(measurement\).
  - **OWNER ORDERING 2026\-09\-23 \(delegated\): the flip slice runs
    AFTER M0145\-0029 and M0145\-0030 land** \(the two unfiled triage
    groups, now filed — the M0145\-0001 escalation's option \(a\)\). The
    executor\-capability sub\-inventory is DONE \(its four members are
    classified, the generatable\-but\-unexecutable set is empty, and the
    `unexpressible` handoff list now has milestone homes: M0146\-0002 /
    0003 / 0004 / 0010\). Sequence: 0029 → 0030 → flip commit \(with the
    group\-K tests and the two script knob defaults changing in it\) →
    legacy\-deletion slices → this task's gate suite.
  - **ESCALATION 2026\-09\-23 \(ralph2 loop \#25\): the flip commit is
    blocked on two owner decisions.** 0029 and 0030 are both `[x]`. A full
    flip run of the optimizer and executor packages at `324bd3ffb` leaves
    these tests red; the flip commit cannot land while any stays red:
    - **\(1\) `indexProbeCostMultiplier` \(owner call handed on by
      M0145\-0029\):** `TestIOS_HeapFallback` and
      `TestIndexOnlyDeformColdAndVisible`. PG prices index vs bitmap
      within 0.01; the 2x multiplier decides. The executor tests cannot
      set the multiplier \(it is a package var read from
      `GOOPG_INDEX_PROBE_MULT` at init\). Options: \(a\) retire the
      multiplier, \(b\) accept the bitmap shape and update the tests,
      \(c\) sanction a test\-only setter.
    - **\(2\) executor tests of legacy\-only machinery \(handed on by
      M0145\-0030\):** `TestParallelNLIJointypeIdentity/semi`, `/anti`,
      `TestSubqueryUnnestKillSwitch`; plus the non\-PG
      `TestHashedInMixedKindFallsBack`. `pinLegacyPipeline` is local to
      the optimizer package's tests. AGENT.md forbids a second
      pipeline\-selection mechanism, so exporting a pin needs owner
      sanction. Otherwise the flip commit deletes these arms, and the
      legacy fused\-NLI semi/anti and unnest\-switch code goes uncovered
      until the legacy\-deletion slice.
    - Also in the flip commit, unchanged: the stale group\-I expectations
      \(M0145\-0029's close list\), group K's two tests, and the script
      sites in `m0145\-0030\-group\-b\-adjudication.md` §Script audit.
    - Loop continues with item 3's selectable recons \(M0145\-0024 →
      0025 → 0026\) until the owner answers.
  - **OWNER ANSWERS 2026\-09\-24 \(delegated; OWNER DECISIONS 2026\-09\-24
    in the banner\):** \(1\) `indexProbeCostMultiplier` — **option \(c\)**,
    a test\-only setter is sanctioned; retiring stays parked \(measured
    2–3x wall\-clock regression\) and the bitmap re\-baseline would drop
    index\-probe executor coverage. \(2\) executor legacy pin — **export
    sanctioned** as a shared testutil helper \(same env\-knob mechanism,
    not a second selection mechanism; precedent `3b8b771f6`\). Task is
    `[ ]` again: the flip commit sequence from the 2026\-09\-23 ordering
    stands \(group\-K tests, stale group\-I expectations, the two script
    knob defaults, then this task's gate suite\).
  - **FLIP LANDED 2026\-09\-24 \(ralph2 loop \#49\)**: the jointree pipeline
    is the default \(`GOOPG_JOINTREE_PIPELINE=0` = legacy\). Design doc:
    `docs/design/0100-0149/m0145-0008-cutover-flip.md`.
    - **Attribution:** the gated staged flip was swept into a concurrent
      session's pathspec\-less commit `ddb4eabd4` \("analysis\(latency\-trend\)"\),
      already pushed. That commit IS the flip; the gates ran on exactly that
      staged tree. History is not rewritten.
    - Prerequisite fixed on its own first, `eb0e7d388`: WRONG RESULTS, the
      index\-only producer's bare branch skipped the NULL\-key guard
      \(reachable on the legacy default too, through any searched join\).
    - Two PG\-faithful estimator fixes the flip exposed: IN\-list elements get
      eqsel's `isunique` \(scalararraysel, selfuncs.c:1821/:338\); an unanalysed
      base column divides by `rel->tuples` \(`EstRelRows`\).
    - Owner\-sanctioned hooks `SetJointreePipeline` /
      `SetIndexProbeCostMultiplier`. 13 red tests dispositioned against PG
      18.3 \(table in the doc\).
    - Gates on the new default: units, tpch\-spotcheck, sf025 96/96
      \(PLAN\-SHAPE changed=25, the fire set\), acceptance values identical,
      fireset clean, TPC\-H match=2/22 \(= baseline\), SF0.25 match=2
      \(floor\). ea\-ratchet 53→50 with 1 NEW \(Q95, unmatched in PG\): held in
      the M0145\-0001 escalation 2026\-09\-24.
    - Wall time: arm total 1.14x; Q18 1.6x and Q22 3.2x slower \(held in
      the M0145\-0001 escalation 2026\-09\-24\).
    Movement: none — TPC\-H CATEGORIES\-EXCL\-MATCH within ±3 of the
    baseline, SF0.25 within ±1 of the same\-binary legacy control, match
    unchanged on both corpora.
  - Remaining here: the legacy\-deletion slices \(`planSelectLegacyPipeline`
    and legacy\-only machinery with the group\-L tests, the seam guards on
    0001's retired list, the knob\), then the fire\-set gate's HEAD\-vs\-staged
    redesign.
  - **Legacy\-deletion slice 1 LANDED 2026\-09\-24: the fire\-set gate is
    HEAD\-vs\-staged** \(done first — deleting the knob under the old
    legacy\-vs\-jointree A/B would make the gate derive zero fires forever\).
    Design: `docs/design/0100-0149/m0145-0008-del1-fireset-head-vs-staged.md`;
    evidence `analysis/m0145/m0145-0008-del1/`.
    - `tpcds-fireset-gate.sh`: baseline engine built from `BASELINE_REV`
      \(default HEAD, `git archive` of `go.mod go.sum cmd internal`\),
      candidate from the checkout; both arms on the shipped pipeline,
      `NO_BUILD=1`. Resume reuses the recorded baseline sha;
      `BASELINE_REV=worktree` refuses an A/A; `RUNNER\_BIN` built for the
      TPC\-H lane.
    - The gate now deletes its four per\-corpus private clones
      \(`FIRESET\_KEEP\_CLONES=1` keeps them\); 132 stale execution clones
      \(262 GB\) were removed, disk 89% → 62%.
    - A/A \(HEAD vs worktree\): `fires=none` at SF0.25 and SF1, stamp PASS.
      Non\-vacuity vs `bb90e51a4^`: 8 fires executed, `introduced=none`.
    Movement: none — harness only.
    - Next slice: delete `planSelectLegacyPipeline` / the `jointree`
      parameter and the `jointreePipeline` branch sites with their
      legacy\-only machinery and the tests pinned through
      `SetJointreePipeline\("0"\)`; then retire the knob
      \(`flagProvenanceRetired`\) and the sf025 legacy control pass.
  - **Legacy\-deletion slice 2 LANDED 2026\-09\-24: the legacy pipeline and
    `GOOPG\_JOINTREE\_PIPELINE` are deleted.** Design:
    `docs/design/0100-0149/m0145-0008-del2-legacy-pipeline-deleted.md`;
    evidence `analysis/m0145/m0145-0008-del2/`.
    - Dispatch collapsed into `planSelectWithSettings`; every
      `jointree`/`jointreePipeline` branch folded \(rule\-based single\-table
      bypass, S5a pre\-DP arm, outer\-spine peel, floor 2, legacy leaf
      numbering\); the knob is retired via `flagProvenanceRetired`; the sf025
      legacy control pass is off by default.
    - Legacy\-pinned tests were run unpinned first: passing ones kept, 30
      legacy\-shape pins deleted, and the correlated\-IN correctness pins
      rewritten to PG 18.3's LATERAL semi join property \(both conjuncts
      kept\).
    - Gates: units, spotcheck, fire\-set HEAD\-vs\-staged `fires=none` at
      SF0.25 \+ SF1, sweep 96/96 \(shapes 99/99 same\), acceptance 24 MATCH,
      ea\-ratchet 52/52.
    Movement: none — no plan moved on either corpus.
    - Note for the owner: AGENT.md §"Plan\-parity harness" G8 still
      describes `GOOPG\_JOINTREE\_PIPELINE` as the live dual\-pipeline
      knob; that section is owner\-edited.
    - Next slice \(dead code\): the 20 functions `deadcode ./cmd/goopg` newly
      reports \(`predp.go`, the spine peel, `enclosingtree.go` splice
      asserts, remap helpers, `joinTreeHasOuterLink`\), retiring the unread
      `GOOPG\_ONEREL\_SEARCH` / `GOOPG\_UNNEST\_PREDP`, the spine\-aware code
      in `tryPGShapedJoinSearch`, the sf025 `SF025\_FLOW\_KNOB` block and the
      scripts' `JOINTREE` variables; then the retired seam guards.
  - **Legacy\-deletion slice 3 LANDED 2026\-09\-24: the dead code.** Design:
    `docs/design/0100-0149/m0145-0008-del3-dead-code.md`; evidence
    `analysis/m0145/m0145-0008-del3/`.
    - Deleted `predp.go` and `onerelsearch.go` whole, the outer\-spine
      peel/splice and every `spine` read in `tryPGShapedJoinSearch`, the
      enclosing\-tree splice tripwires, the splice\-only remap helpers and
      `joinTreeHasOuterLink`; retired `GOOPG\_UNNEST\_PREDP` and
      `GOOPG\_ONEREL\_SEARCH`; removed the sf025 knob lane and the scripts'
      `JOINTREE` variables. deadcode 106 → 84 \(below the pre\-slice\-2 87\).
    - Gates: units, spotcheck, fire\-set `fires=none` at SF0.25, SF1 and
      TPC\-H, sweep 96/96 \(shapes 99/99 same\), acceptance 24 MATCH,
      ea\-ratchet 52/52.
    Movement: none.
    - Next: an AUDIT of M0145\-0001's §6 retirement rows. Many are still
      the live route \(`tryJoinSearch`, the post\-hoc unnest family,
      `planFromClause`, the `joinlist` proto\-IR, several decline
      classes\), so they need a per\-row verdict: gone / dead / live plus
      its blocking task. Close the dead rows and file the live ones.
  - **Legacy\-deletion slice 4 LANDED 2026\-09\-24 — task CLOSED:
    `GOOPG\_PGSHAPED\_DP` retired; §6 retirement audit.** Design:
    `docs/design/0100-0149/m0145-0008-del4-pgshaped-knob-and-retirement-audit.md`;
    evidence `analysis/m0145/m0145-0008-del4/`.
    - The kill\-switch \(`=0` = no join search\) is gone with its guards,
      resolver and script variables. `tpch\-estimate\-audit\-arm.sh`
      defaulted to it, so the TPC\-H capture lane read `match=1`; on the
      shipped planner it reads `match=3`. Both fire\-set arms are identical,
      so that is a lane correction, not a plan move.
    - 15 rule\-arm tests \(NLI promotion, small\-dim build side, OR\-of\-ANDs
      key\) were deleted; searched\-arm counterparts exist; the residue is
      ledgered.
    - §6 audit: everything §6 assigns to 0008 is GONE. The LIVE rows are the
      shipped route that 0003/0005/0007 were meant to replace \(post\-hoc
      unnest, the node\-tree seam walk, rule passes, rowmark passes,
      `planFromClause`\) → M0145\-0008n.
    - Gates: units, spotcheck, fire\-set `fires=none` at SF0.25, SF1 and
      TPC\-H, sweep 96/96 \(shapes 99/99 same\), acceptance 24 MATCH,
      ea\-ratchet 52/52, deadcode 84.
Movement: none — no plan moved on TPC-DS SF0.25/SF1 or TPC-H across all four deletion slices.

- [x] **M0145\-0008n — sequence the M0145\-0001 §6 rows that are still the
  live route** \(filed 2026\-09\-24 by M0145\-0008 slice 4\). The audit table in
  `m0145\-0008\-del4\-pgshaped\-knob\-and\-retirement\-audit.md` lists them:
  the post\-hoc unnest family, the node\-tree seam walk
  \(`tryJoinSearch`/`extractSearchLeaves`\) and its decline classes, the rule
  passes \(`rewriteJoinsToNLI`, scan\-input and pushdown rewrites\), the
  rowmark passes and `planFromClause`. Retiring them is reimplementation,
  not deletion.
  Kind: recon
  Parent: M0145-0008
  - First step: map each LIVE row to the M0146\-0001 census categories it
    affects \(does a row produce a first divergence from PG on the corpora?\);
    rows that never do are refactor\-only and wait; rows that do become impl
    tasks naming their expected movement \(S5\).
  - **DONE 2026\-09\-25 \(recon\).** Design:
    `docs/design/0100-0149/m0145-0008n-live-route-attribution.md`; evidence
    `analysis/m0145/m0145\-0008n/` \(per\-query `NLICENSUS` /
    `SUBLINKCENSUS` routes, TPC\-H via `--queries N` runs, TPC\-DS SF0.25 via
    log offsets; pull\-up total 23 matches the sweep\).
    - **Produces first divergences:**
      - Post\-hoc unnest decorrelates a correlated scalar subquery PG keeps
        as a SubPlan: TPC\-H Q2, TPC\-DS Q1, Q6, Q32, Q92 \(PG 2 SubPlans,
        goopg 0; first divergence at that join\) → M0145\-0008y.
      - Post\-hoc semi join where PG pulls up: TPC\-H Q18 \(grouped IN →
        PG unique\-ified inner Parallel Hash Join\), TPC\-DS Q14/Q23 extra
        semi/anti joins → M0145\-0008z.
    - **Refactor\-only \(same shape as PG, waits\):** the post\-hoc route on
      Q16, Q17, Q20, Q30, Q41, Q45, Q54, Q58, Q81, Q95; `rewriteJoinsToNLI`
      \(one node in all corpora, TPC\-H Q20\); rowmark passes \(no corpus
      query locks rows\); seam declines \(2, `leaf\-count`\);
      `planFromClause` \(on every query, no per\-query attribution\).
    Movement: none — recon.


- [x] **M0145\-0008y — keep a correlated scalar subquery as a SubPlan, as
  PG does** \(filed 2026\-09\-25 by M0145\-0008n\). goopg\'s post\-hoc unnest
  decorrelates `x op \(SELECT agg … WHERE correlated\)` into a join; PG
  keeps it as a correlated SubPlan \(`pull\_up\_sublinks` converts only
  ANY/EXISTS; scalar sublinks stay SubPlans\). First divergence at that join
  on TPC\-H Q2 and TPC\-DS Q1, Q6, Q32, Q92 \(PG plans show 2 SubPlans,
  goopg none\).
  Kind: impl
  Parent: M0145-0008n
  - First step: locate the scalar\-subquery arm of the post\-hoc unnest
    \(`unnestSubqueriesInPlan` family\), decline it the way M0146\-0002c
    declined the NOT IN arm, and measure the five queries\' first
    divergences plus execution times \(a per\-row SubPlan can be slower in
    goopg than the decorrelated join; PG runs it with index access\).
  - Expected movement: TPC\-DS SF0.25 first divergences on Q1/Q6/Q32/Q92
    and TPC\-H Q2 move past that join; measured on the canonical captures.
  - **BLOCKED 2026\-09\-25 on M0146\-0012** \(evidence
    `analysis/m0145/m0145\-0008y/prototype\-fireset.txt`\) — confirmed by
    the 2026\-09\-25 owner decision on M0145\-0008ac: parameterised inner
    paths land first; this stays `[!]` until then.
    - Prototype \(not committed\): `canUnnestSubquery` refuses every scalar
      sublink. The fire\-set gate fired exactly TPC\-H Q2 and TPC\-DS Q1, Q6,
      Q32, Q92 and introduced no timeouts. Categories improved slightly
      \(aggregation\-strategy SF0.25 41→37, SF1 44→40, TPC\-H 4→3\); match
      counts unchanged.
    - **TPC\-H Q2: 1.50 s → 307.36 s** with identical rows. goopg\'s SubPlan
      scans the inner in full per outer row; PG makes it cheap by planning
      the correlation as a parameterised index qual \(`ps\_partkey = \$1`\),
      which is M0146\-0012 \(`is\_pseudo\_constant\_for\_index`\).
    - The S6 comment in `canUnnestSubquery` claims upstream "can decorrelate
      unconditionally"; that is wrong. `pull\_up\_sublinks` never converts a
      scalar sublink, and the decorrelation is goopg\'s performance
      substitute for M0146\-0012.
    - Resume: after M0146\-0012 lands, re\-run the prototype and the Q2
      timing.
  - **2026\-10\-05 — UNBLOCKED and DONE\.** M0146\-0012 and M0146\-0012a
    landed, so the hold condition was met and the task re\-opened\. Design
    `docs/design/0100\-0149/m0145\-0008y\-scalar\-sublink\-subplan\.md`;
    evidence `analysis/m0145/m0145\-0008y/`\.
    - `canUnnestSubquery` refuses every scalar sublink unless
      `GOOPG\_SCALAR\_UNNEST=on`\. The machinery stays as a rollback path,
      and the 15 tests pinning it enable the switch\.
    - Re\-measure: TPC\-H plans are identical to HEAD \(Q2 0\.20 s, not
      307 s\)\. Only TPC\-DS Q6 changes: it keeps PG\'s SubPlan,
      402 → 485 ms\.
  Movement: yes — CATEGORIES\-EXCL\-MATCH SF1 match 33 \-> 34, join\-order 53 \-> 52, join\-method 22 \-> 21, parameterisation 33 \-> 32, aggregation\-strategy 17 \-> 15; SF0\.25 join\-method 25 \-> 24, aggregation\-strategy 9 \-> 8

- [x] **M0145\-0008z — the pull\-up declines sublinks PG pulls up \(TPC\-H
  Q18, TPC\-DS Q14/Q23\)** \(filed 2026\-09\-25 by M0145\-0008n\). TPC\-H Q18\'s
  `IN \(SELECT l\_orderkey … GROUP BY … HAVING …\)` goes to the post\-hoc
  route and becomes a Left Semi join; PG\'s `convert\_ANY\_sublink\_to\_join`
  pulls it up and then unique\-ifies the grouped side into an inner Parallel
  Hash Join. TPC\-DS Q14 and Q23 carry more semi/anti joins than PG \(5 vs
  0, 8 vs 4\).
  Kind: recon
  Parent: M0145-0008n
  - First step: read the `PULLUPCENSUS decline=` reason for each sublink
    of Q18, Q14 and Q23 \(`GOOPG\_NLI\_CENSUS=1`\) and compare with PG\'s
    admission rules for grouped/HAVING subqueries.
  - **DONE 2026\-09\-25 \(recon\).** Evidence
    `analysis/m0145/m0145\-0008z/decline\-reasons.txt`.
    - TPC\-H Q18: `decline=any\-body\-not\-simple`. The grouped/HAVING `IN`
      body fails `sublinkBodyIsSimple` \(`pullUpAnyBody`,
      `jointreepullup.go`\).
    - TPC\-DS Q14 \(5\) and Q23 \(8\): `decline=any\-body\-leaf\-\(\*CTEScan\)`.
      The `IN \(SELECT … FROM cte\)` body\'s leaf is a CTE scan, and the
      pull\-up admits only base\-scan leaves.
    - PG converts both. `convert\_ANY\_sublink\_to\_join` \(subselect.c\) makes
      any uncorrelated body a subquery RTE on the semi side, and
      `pull\_up\_subqueries` flattens it only if simple. The planner may then
      unique\-ify the inner and join it as an inner join \(Q18: an inner
      Parallel Hash Join; Q14/Q23: no semi joins in PG\'s plans\). goopg
      already has the unique\-ify path \(`createuniquepath.go`\).
    - `sublinkBodyIsSimple` is goopg\'s own gate, not PG\'s → M0145\-0008aa.
    Movement: none — recon.

- [x] **M0145\-0008aa — pull up an ANY sublink whose body is not simple
  as one derived semi\-side leaf, as PG does** \(filed 2026\-09\-25 by
  M0145\-0008z\). A grouped/HAVING body \(TPC\-H Q18\) or a body over a CTE
  scan \(TPC\-DS Q14 ×5, Q23 ×8\) is declined \(`any\-body\-not\-simple`,
  `any\-body\-leaf\-\(\*CTEScan\)`\) and takes the post\-hoc route to a
  Left Semi join; PG makes the body a subquery RTE on the semi side and can
  then unique\-ify it into an inner join.
  Kind: impl
  Parent: M0145-0008z
  - First step: admit a non\-simple body as a single derived leaf \(the
    plan of the whole body, like M0145\-0013\'s pulled `\*CTEScan` leaf\)
    in `pullUpAnyBody`, and check that the search considers the
    unique\-ified inner for it. Watch the Q78 `outer\-over\-derived` lesson:
    run the fire\-set gate at both scales.
  - Expected movement: first divergences on TPC\-H Q18 \(`parallelism`\) and
    TPC\-DS Q14 \(`join\-order`\) / Q23 \(`scan\-type`\), measured on the
    canonical captures.
  - **DONE 2026\-09\-25 \(`9e99ea944`\), derived\-body arm only.** Design doc
    `docs/design/0100\-0149/m0145\-0008aa\-any\-derived\-body\-pullup.md`;
    evidence `analysis/m0145/m0145\-0008aa/`.
    - A non\-simple body is planned as a FROM subquery aliased
      `ANY\_subquery` and enters the search as ONE leaf
      \(`pullUpAnyDerivedBody`\); the seam admits it without a catalog table
      \(`seamLeafBinding`\). Correlated bodies \(PG\'s LATERAL case\) still
      decline.
    - TPC\-H fire set: Q18 only. Its join order moved toward PG\'s
      \(`aggregation\-strategy` 4 → 3\), but its estimate got worse
      \(3.0M rows against PG\'s 470K\): goopg keeps a semi join where PG
      unique\-ifies into an inner join → M0145\-0008ab.
    - TPC\-DS fire set: no changed query at SF0.25 or SF1.
    - Promoting the CTE\-leaf admission \(Q14/Q23\) failed the SF1 fire\-set
      gate \(Q95 TIMEOUT introduced\) → held as M0145\-0008ac; the diff is
      preserved as a patch.
  Movement: none — TPC-H PLAN-PARITY match 3 -> 3; aggregation-strategy 4 -> 3 is inside the noise band
- [x] **M0145\-0008ab — unique\-ify a derived semi\-side leaf into an inner
  join, as PG\'s create\_unique\_path does** \(filed 2026\-09\-25 by
  M0145\-0008aa\). PG proves the grouped `ANY\_subquery` distinct
  \(`query\_is\_distinct\_for`,
  `postgres/src/backend/optimizer/plan/analyzejoins.c`\), so its unique path
  costs nothing extra, and it joins TPC\-H Q18 as an inner Hash Join against
  117,504 groups. goopg\'s `createUniquePath` is reachable only for the
  legacy `PathPrebuilt` atomic RHS, and `pulledSemiJoinInfo` never fills
  `SemiRhsExprs`, so the derived leaf stays a Hash Semi Join estimated at 750K
  of 1.5M `orders` rows.
  Kind: impl
  Parent: M0145-0008aa
  - First step: populate `SemiRhsExprs`/`SemiCanHash`/`SemiCanBtree` for a
    pulled derived body and let `createUniquePath` accept its leaf path;
    port the `query\_is\_distinct\_for` GROUP BY / DISTINCT arms as the
    no\-cost fast path \(pathnode.c `create\_unique\_path`\).
  - Expected movement: TPC\-H Q18 `join\-method`/`join\-order` against PG
    \(inner Hash Join over the grouped body\) and its row estimate
    \(3.0M → PG\'s 470K\), measured on the TPC\-H fire set / parallel
    capture.
  - **DONE 2026\-09\-25 \(`f8fb6d64c`\).** Design doc
    `docs/design/0100\-0149/m0145\-0008ab\-derived\-leaf\-unique\-ify.md`;
    evidence `analysis/m0145/m0145\-0008ab/`.
    - Ported `create\_unique\_path`\'s NOOP arm via `query\_is\_distinct\_for`
      \(`queryIsDistinctForFirstColumn`, `createPulledUniquePath`\).
    - Ported the JOIN\_UNIQUE\_INNER paths PG offers beside the plain SEMI
      paths \(`addPathsToJoinrel` → `addPathsForJointype` twice\).
    - Ported `examine\_simple\_variable`\'s `isunique` for a lone
      DISTINCT/GROUP BY subquery column \(`subqueryOutputIsUnique`,
      `joinVarStats.isUnique`\).
    - TPC\-H Q18: semi estimate 750K → 135K \(PG 117K\), final 3.0M → 539K
      \(PG 470K\), join order now PG\'s \(`orders ⋈ grouped lineitem`, then an
      index nested loop\). The unique\-inner candidates are built but lose on
      cost to the Hash Semi Join → M0145\-0008ad.
    - TPC\-DS: no changed query at either scale; EA\-RATCHET 52 → 52.
  Movement: none — TPC-H PLAN-PARITY match 3 -> 3; category moves within the noise band
- [x] **M0145\-0008ad — TPC\-H Q18 elects a Hash Semi Join where PG elects
  the unique\-inner Hash Join** \(filed 2026\-09\-25 by M0145\-0008ab\). Both
  candidates now exist for `orders ⋈ ANY\_subquery` \(DP trace shows the
  JOIN\_UNIQUE\_INNER arms costed\); goopg\'s semi path wins, PG\'s inner path
  wins. The difference is in the hash\-join costing of SEMI against INNER
  \(`final\_cost\_hashjoin`\'s semi arm, `outer\_match\_frac` / unmatched
  probes, `postgres/src/backend/optimizer/path/costsize.c`\) or in the
  semifactors fed to it.
  Kind: recon
  Parent: M0145-0008ab
  - First step: print both candidates\' cost components for Q18 on a private
    TPC\-H clone \(`GOOPG\_PGSHAPED\_DP\_TRACE=1`\) and compare with PG\'s
    `final\_cost\_hashjoin` inputs for the same pair.
  - Expected movement: TPC\-H Q18 `join\-method` \(Hash Semi Join → Hash
    Join\), measured on the TPC\-H fire set.
  - **DONE 2026\-09\-25, recon.** Design doc
    `docs/design/0100\-0149/m0145\-0008ad\-q18\-semi\-vs\-inner\-recon.md`;
    evidence `analysis/m0145/m0145\-0008ad/`.
    - goopg: the semi and unique\-inner hash paths cost exactly the same
      \(246725.1975 serial, 233111.13 partial\); `addPath` keeps the
      incumbent semi.
    - PG \(instrumented build, private copy of the Q18 tables\): all 63
      candidates for the pair are JOIN\_INNER; there is no SEMI path at all.
      `reduce\_unique\_semijoins` \(`analyzejoins.c:844`, `planmain.c:234`\)
      deletes the SJInfo because the RHS is provably unique.
    - Not a costing gap: the missing pass is filed as M0145\-0008ae.
  Movement: none
- [x] **M0145\-0008ae — port `reduce\_unique\_semijoins`: drop a semijoin\'s
  SpecialJoinInfo when its single\-rel RHS is unique for the join clauses**
  \(filed 2026\-09\-25 by M0145\-0008ad\). PG runs it in `query\_planner`
  before the join search \(`postgres/src/backend/optimizer/plan/analyzejoins.c:844`,
  `innerrel\_is\_unique` / `rel\_is\_distinct\_for`\), so a pair like Q18\'s
  `orders ⋈ ANY\_subquery` is planned and estimated as an ordinary inner
  join. goopg keeps the SJInfo and elects the semi join on an exact cost tie.
  Kind: impl
  Parent: M0145-0008ad
  - First step: in `classifyPulledQuals` \(jointreepullup.go\), skip appending
    the SJInfo \(and mark the body\'s link an inner join qual\) when the body
    is one leaf and either `jtPulledBody.distinct` holds \(RTE\_SUBQUERY arm\)
    or a `uniqueKeyColumnSets` key of its table is covered by the link\'s RHS
    columns \(RTE\_RELATION arm\). Check that the lowering still emits the
    body once \(no duplicate rows\) and that estimation switches to the inner
    arm.
  - Expected movement: TPC\-H Q18 `join\-method` \(Hash Semi Join → Hash
    Join\); any pulled `IN \(SELECT pk …\)` in either corpus becomes an inner
    join. Measured on the TPC\-H fire set and the two\-scale TPC\-DS fire set.
  - **DONE 2026\-09\-25 \(`f19b6f7cc`\).** Design doc
    `docs/design/0100\-0149/m0145\-0008ae\-reduce\-unique\-semijoins.md`;
    evidence `analysis/m0145/m0145\-0008ae/`.
    - `pulledSemiRhsIsUnique` proves a single\-leaf RHS unique \(derived
      `distinct`, or a covered non\-partial unique index\); `classifyPulledQuals`
      then appends no SJInfo.
    - TPC\-H: Q18 becomes PG\'s Hash Join; Q20\'s `part ⋈ partsupp` becomes
      PG\'s index nested loop. Values identical serially and in parallel;
      `join\-method` 11 → 10.
    - TPC\-DS: no changed query at either scale; EA\-RATCHET 52 → 52.
    - Q20\'s `ps\_availqty > \(SubPlan\)` filter now runs above the Gather,
      unprinted; PG keeps it on the `partsupp` scan \(ledgered\).
  Movement: none — TPC-H PLAN-PARITY match 3 -> 3; join-method 11 -> 10 inside the noise band
- [x] **M0145\-0008ac — promote the pulled `\*CTEScan` leaf admission
  \(`GOOPG\_PULLUP\_CTE\_LEAF`\)** \(filed 2026\-09\-25 by M0145\-0008aa\).
  PG pulls a CTE reference in a simple sublink body up like any base rel;
  goopg admits it only behind the default\-off knob. Promoting it moves
  TPC\-DS Q14/Q23/Q95 with values identical, but Q95 goes 3 s → 16 s at
  SF0.25 and **times out at SF1** \(fire\-set gate FAIL, introduced\).
  Kind: impl
  Parent: M0145-0008aa
  - Patch: `analysis/m0145/m0145\-0008aa/combined\-derived\-arm\-plus\-cte\-leaf\-promotion.patch`
    \(the CTE half is the `flaglabels.go`, `planner\-flags.env`,
    `pullup\_cte\_leaf\_test.go` and `flattenPulledBodyTree` hunks\).
  - **ESCALATION \(2026\-09\-25\):** blocked, not reverted \(R3\).
    - What was tried: unconditional admission; values held at SF0.25; the
      SF1 fire set timed out Q95.
    - What it proved: goopg\'s new Q95 plan has PG\'s shape \(nested\-loop
      semi joins with the `ws\_wh` CTE scan inside\), but PG\'s inner
      `web\_returns` access is an Index Only Scan parameterised by the outer
      `ws1.ws\_order\_number`, pushed through a hash join; goopg builds no
      parameterised path through a join, so it rescans a 3M\-row hash join
      per outer row.
    - Blocker: parameterised inner paths through a join — the
      M0146\-0012 / M0145\-0010 family.
    - Expected movement if unblocked: TPC\-DS Q14/Q23/Q95 first divergences
      \(`join\-order` / `scan\-type`\) at both scales.
    - Size: the promotion itself is the patch above; the blocker is the
      large part.
    - Owner decision needed: sequence the parameterised\-path work ahead of
      this, or accept Q95\'s SF1 timeout as a coverage loss under G5.
    - **OWNER ANSWER 2026\-09\-25 \(delegated\): option \(i\) — sequence
      the parameterised\-path work ahead.** A 60x\+ Q95 timeout is a
      real coverage hole, not an acceptable G5 loss. This task stays
      `[!]` until parameterised inner paths through a join \(the
      M0146\-0012 / M0145\-0010 family\) land; then re\-apply the
      preserved patch and re\-run the fire set at SF0\.25 and SF1. The
      same blocker gates M0145\-0008y, so unblocking it clears two
      items.
  - **Blocker landed 2026\-10\-03 \(M0146\-0049d, re\-opened per the owner
    answer above\):** parameterised inner paths through a join exist —
    `addParameterizedHashJoinPaths` \(paramjoin\.go\) plus the executor
    prerequisites \(0049d1 empty\-inner exit, 0049d2 CTE materialised once
    under a LATERAL\)\. Next: re\-apply the preserved patch and re\-run the
    fire set at SF0\.25 and SF1, as the owner answer directs\.
  - **LANDED 2026\-10\-03 \(`c317b037b`\)\.** The CTE half re\-applied by hand:
    `flattenPulledBodyTree` admits `\*CTEScan` unconditionally,
    `GOOPG\_PULLUP\_CTE\_LEAF` retired\. Design
    `docs/design/0100\-0149/m0145\-0008ac\-cte\-leaf\-promotion\.md`\.
    - Q95\'s semi inner is PG\'s parameterised Hash Join; cost 258193 →
      142781 at SF0\.25 \(PG 141550\); fire set: no timeout at either scale\.
    - ea\-ratchet: 8 new keys, all PG\-shared at the nearest scope —
      re\-pinned under G4\'s extension with
      `analysis/m0145/m0145\-0008ac/ea\-repin\-attribution\.md`\.
    - Runtime regression Q14 8s → 39s, Q95 3s → 7s at SF0\.25 — filed
      M0145\-0008af \(R3\)\.
    - Gates: units, spotcheck, sweep 96/96, fire set ×2 \(PASS\), TPC\-H
      arm 24/24, ea\-ratchet after re\-pin, regress 7 suites identical\.
  Movement: yes — fire set CATEGORIES\-EXCL\-MATCH SF0\.25 join\-method 28→27, aggregation\-strategy 17→16, parallelism 35→34 \(parameterisation 27→28, rendering 11→13\); SF1 join\-method 28→27 \(qual\-placement 6→8\); match flat 38/29
- [x] **M0145\-0008af — REGRESSION: Q14 4\.9x and Q95 2\.3x slower at SF0\.25
  after the CTE\-leaf promotion** \(filed 2026\-10\-03 by M0145\-0008ac\)\.
  Q14 now runs PG\'s nested loop over HashAggregate\(cross\_items\) but probes
  store\_sales with a Bitmap Heap Scan \(~0\.7 ms/probe × 16173\) where PG
  index\-scans \(0\.06 ms\); Q95\'s web\_returns\_pkey probe on a non\-leading
  column takes ~85 ms where PG\'s skip scan takes ~1 ms\. Values identical\.
  Kind: impl
  Parent: M0145\-0008ac
  > ## ESCALATION 2026\-10\-03 \(R3\) — a PG\-faithful promotion regressed two runtimes
  > Landed, not reverted \(R3\)\. Owner: confirm the order — the probe\-cost
  > fix sits under M0146\-0049\'s S4 hold\.
  - First step: break down cost\_index for the parameterised store\_sales
    probe \(goopg prices index probes ~2x PG — M0146\-0049\'s escalation\)
    and time goopg\'s skip\-scan probe against PG\'s Index Searches\.
  - Design: `docs/design/0100\-0149/m0145\-0008ac\-cte\-leaf\-promotion\.md`\.
  - **Diagnosis 2026\-10\-03:** two separate causes\.
    - Q14: the plan choice, not the executor\. goopg\'s bitmap probe of
      store\_sales \(2\.5 ms cold\) is on par with PG\'s own bitmap \(1\.7 ms\),
      its index scan on par with PG\'s \(0\.067 vs 0\.041 ms\); PG picks the
      index scan in the loop, goopg the bitmap because
      `indexProbeCostMultiplier = 2` doubles every index random page
      \(16\.27 vs PG 8\.30 on the d3 dataset\)\. Retiring that multiplier is
      owner\-parked \(OWNER DECISIONS 2026\-09\-24\) → split out as
      M0145\-0008ag `\[\!\]`\.
    - Q95: goopg\'s skip scan descended twice per distinct prefix
      \(web\_returns\_pkey probe 226 ms vs PG 1 ms, one Index Search\)\.
  - **LANDED 2026\-10\-03 \(`c92622dd1`\)** — the Q95 half: the skip scan
    walks the leaf level and re\-descends only past a whole\-leaf group
    \(`btreeSkipEnum\.walkLeaf`, PG 18\'s `\_bt\_advance\_array\_keys`\)\. Probe
    226 → 14 ms; sweep Q95 7s → 3s, Q16 13s → 1s; plans unchanged\.
    `TestSkipScanLeafWalk`\.
  Movement: none — executor runtime only \(sweep Q95 7s→3s, Q16 13s→1s\); plans unchanged
- [x] **M0145\-0008ag — Q14 probes store\_sales by bitmap where PG index\-scans
  \(4\.9x slower at SF0\.25\)** \(filed 2026\-10\-03 by M0145\-0008af\)\.
  Kind: impl
  Parent: M0145\-0008af
  - **RE-OPENED 2026\-10\-12 evening \(owner, delegated\):** the owner park
    on `indexProbeCostMultiplier` was lifted by the Option\-A decision —
    **M0146\-0159** \(the retirement impl\) is filed and first; re\-measure
    this task's Q14 shape after it lands, since mult=1 may change the
    premise itself\.
  - Blocked on the owner\-parked `indexProbeCostMultiplier` retirement
    \(OWNER DECISIONS 2026\-09\-24, M0145\-0008 flip unblock \(a\): "retiring
    it is the measured\-regression exit and stays parked"\)\. The multiplier
    doubles a parameterised index probe\'s random\-page cost, so the bitmap
    probe wins the loop where PG\'s cost\_index \(with loop\_count caching\)
    elects an Index Scan: Q14 24\.9s vs PG 4\.8s on its first statement\.
  - Evidence for the owner: the multiplier now costs runtime in the
    opposite direction from the one it was calibrated for \(Q14 here, and
    the bitmap\-for\-index swaps on Q54\'s web\_sales and the d3 dataset —
    M0146\-0049\'s escalation\)\.
  - Done 2026\-10\-13 \(re\-measure, no code\): resolved by M0146\-0159\. Retired, Q14\'s scan inventory equals PG\'s at
    SF0\.25 and SF1 \(Index Scans on store\_sales\_pkey / item\_pkey; x2 had 3 \+ 5 bitmap probes at SF1\); Q14 matches at
    SF0\.25; SF1 keeps only a D3 Parallel\-Hash divergence on the item builds\. Wall\-clock SF0\.25 goopg 18\.6s vs PG
    median 9\.75s \(×1\.9, executor side → M0146\-0171\)\. Evidence `analysis/m0145/m0145\-0008ag/`; design
    `docs/design/0100\-0149/m0145\-0008ag\-q14\-probe\-remeasure\.md`\.
Movement: none — instrument artefact — Q14\'s match is already counted in M0146\-0159\'s 56 → 68


- [x] **M0145-0009 — CTE-output statistics (B-06 resume): wire the landed
  synthesis into the estimator** (filed 2026-09-21 by owner directive;
  carries TODO_ALL B-06 / ledger `take3-B-06-deferred`). Three of this
  milestone's residuals are blocked on derived-input statistics: the ANY
  arm's `body-leaf-(*optimizer.CTEScan)` residue (15 fires, M0145-0003),
  the `outer-over-derived` decline family (12 corpus fires, M0145-0005
  slice 5), and the Q78 firewall's own named resume condition
  (`relfromjoinlist.go:724-725` — "lift when B-06 wires CTE-output stats").
  Scope per the ledger's 4-step resume:
  - (1) Design exists and is reviewed:
    `docs/design/planner-b06-cte-stats/DESIGN.md`. The synthesis slice is
    already landed INERT (`internal/optimizer/cte_stats_synthesis.go` —
    group-key / aggOut-FD / union-literal rules + 16 tests, no consumers
    wired, so it cannot change a plan today).
  - (2) Wire consumers: per-column ndistinct from the group combo clamped
    by output rows; the FD bound for agg outputs; the OID-less registry
    (DESIGN §G2) so `*CTEScan` leaves actually receive the synthesized
    stats in `EstimateRows`/selectivity.
  - (3) Measure before trusting: EA ratchet on the `year_total` shapes
    (Q74; Q4/Q11 share that CTE — Q78's CTEs are `ws`/`cs`/`ss`), the
    SF0.25 sweep, and the TPC-H acceptance arm — stats
    wiring is pipeline-agnostic, so the DEFAULT arm is gated too, not
    only the knob arm; report `CATEGORIES-EXCL-MATCH` and any plan
    movement explicitly.
  - (4) The `rows<=1` guard (`joinsearch.go:520-526`, `initialRelRows`) and the Q78
    firewall stay UNTOUCHED inside this task — the ledger's criterion is
    "guard removal only after derived ≥ guard effect", and the firewall
    is a hard owner constraint.
  Fail-closed throughout: an unrecognized body shape yields
  `cteColUnknown` and today's defaults, never a guess.
  Kind: impl
  Parent: none
  - **Step 2 slice 1 landed (loop 2026-09-21 \#26): the ndistinct
    consumer.** Design updated:
    `docs/design/planner-b06-cte-stats/DESIGN.md` (now indexed in
    `docs/design/README.md`, which it was not before).
    - Identity/lifetime needed no registry: `CTEScan.cte` already points
      at the `*plannedCTE` that `synthesizeCTEStats` consumes, so the
      synthesis is memoized on the entry (`plannedCTE.outputStats()`).
      The entry pointer is a strictly stronger identity than the
      design's `DeclKey()` map key with exactly the lifetime it asks
      for. Recorded as a deliberate, documented divergence.
    - `cteSynthNDistinct` is wired as the LAST arm of
      `columnNDistinctForChild`, so a catalog resolution always wins,
      and is fail-closed at every step — it can only REPLACE a default,
      never invent a number.
    - **It is measurably INERT on the corpus, and the instrumentation
      says why.** DEFAULT arm TPC-DS SF0.25: plans 99/99 IDENTICAL. The
      consumer is reached **852 times** per run and declines every one:
      **648** asks are body shapes the synthesis does not classify,
      **204** are group keys it classifies but does not number, and the
      two kinds it CAN number (agg outputs, union literals) are asked
      for **ZERO** times.
    - So gap G3's agg-output FD bound has no consumer in this channel at
      all, and gap G2's group-combo rule is the only thing that can
      number the 204 live asks. **None of the three residuals this task
      exists to unblock can move until G2 lands.**
    - Slice 2, in order: (1) census which body shapes produce the 648
      `unknown` asks before widening the synthesis; (2) land the
      group-combo rule (G2 / synthesis step 3); (3) EA ratchet on the
      `year_total` shapes + SF0.25 + acceptance arm on the DEFAULT arm,
      reporting plan movement explicitly.
    - Step 4 respected: the `rows<=1` guard and the Q78 firewall were
      NOT touched.
  - **Step 2 slice 2 landed (loop 2026-09-21 \#27): the group-combo rule
    (gap G2).** A group key's output ndistinct is
    `min(input ndistinct, group count)`, applied ONLY when the input is
    known.
    - **Census first, per this task's own ordering**, and it inverted
      the expected target. The 648 slice-1 `unknown` asks are:
      `Project(WindowAgg)` **366** (56%, no rule exists at all),
      `Project(Filter)` 158, bare `SetOp` 59, `Project(SetOp)` 41,
      `DistinctOn` 24. Window functions dominate — not set-ops.
    - **The group count alone is NOT a usable fallback, and the gate
      proved it.** The first version used it when the input was unknown:
      sound as a bound, wrong as an estimate for one key of a multi-key
      grouping. TPC-DS `d_week_seq` got priced at the whole group count,
      which collapsed Q59 from 43 estimated rows to 1 and flipped
      Hash Join -> Nested Loop. **Values stayed green — only the PLAN
      channel saw it**, which is exactly why step 3 gates the default
      arm and reports plan movement explicitly.
    - Corrected rule: **99/99 plans identical** against the TRUE
      pre-change baseline (the intermediate capture was the buggy
      version — the baseline-drift trap the design doc records),
      `PASS=96 MISMATCH=0`, acceptance arm 24/24.
    - It **fires 18 times per corpus run** with large corrections
      (`in=4 groups=655237` -> nd=4, against `defaultNumDistinct` 200),
      so it is exercised and safe — but no SF0.25 plan depends on those
      columns yet. The value is banked for the residuals, not realised.
    - **A G1 claim in the design is contradicted by measurement**:
      it says plain bodies need no synthesis because the resolver
      recurses into them, yet `Project(Filter)` bodies reach the
      consumer 158×/run — and the consumer is only reached when
      `resolveBaseColumn` FAILS. Ledgered; one of the two is wrong.
    - Next (ledgered, in order): the `WindowAgg` arm (366 asks, the
      largest population), then diagnose why `synthUnionLiterals`
      declines the 100 live `SetOp` asks, then the EA/q-error ratchet on
      the `year_total` shapes — NOT run here, so estimate quality on the
      18 fired columns is still unmeasured.
    - Step 4 respected again: the `rows<=1` guard and the Q78 firewall
      remain UNTOUCHED.
  - **Step 2 slice 3 landed (loop 2026-09-21 \#28): the `WindowAgg`
    arm — and it REDIRECTS this task.**
    - The rule: a `WindowAgg` is row-preserving, so a pass-through
      column HAS its input's distinctness exactly (no clamp). Window
      function outputs stay unknown. 3 pins; gates green (plans 99/99
      identical, acceptance arm 24/24).
    - **It fires ZERO times, and the instrumentation names why.** Only
      FOUR corpus CTE bodies are genuinely `Project(WindowAgg)`-shaped
      (they generate all 366 asks). For each, the pass-through input is
      unresolvable: the `WindowAgg`'s child is always a `*Sort`, and
      beneath it sits another `*WindowAgg` (15 declines) or an
      `*Aggregate` (4).
    - **So `resolveBaseColumn` cannot cross `Aggregate`/`WindowAgg` —
      that is gap G1 itself, one level BELOW the CTE boundary.**
    - Three consecutive slices have now landed correct, pinned, safe and
      INERT for this same reason. The blocker is NOT missing synthesis
      rules at the boundary; it is the resolver stopping beneath it, so
      any boundary rule asks for a number nothing can produce.
    - **Next slice must be G1 proper, not a fourth boundary rule**: add
      `*Aggregate` and `*WindowAgg` arms to `resolveBaseColumn`
      (`internal/optimizer/joinkeyproof.go`), then re-run the ask
      census and expect the 366 window asks plus a share of the 204
      group-key asks to convert. Only then is the EA/q-error ratchet on
      the `year_total` shapes meaningful.
    - PG has no such gap: `examine_simple_variable` resolves a
      subquery-output Var to the base `pg_statistic` row regardless of
      intervening nodes, which is why upstream needs no synthesis.
    - Step 4 respected: `rows<=1` guard and Q78 firewall UNTOUCHED.
  - **Step 2 slice 4 landed (loop 2026-09-21 \#29): G1 proper — and it
    CLOSES the "add more rules" direction of this task.**
    - **The plan was half wrong and the code said so.** The
      `*Aggregate` arm in `resolveBaseColumn` ALREADY exists,
      restricted to `AggModePartial`, because **M0127-P5.6-g-ii filed
      exactly that widening and MEASURED it worse — upstream does not
      have it**. `groupUniqueNDistinct`'s single-grouping-column rule is
      documented as "upstream's and load-bearing, not conservatism".
      Only the `*WindowAgg` arm was genuinely missing.
    - Added it across the whole resolver family;
      `TestResolverFamilyArmListsAgree` mechanically caught a THIRD
      member (`relFilteredRowsWalk`) the plan had not accounted for.
    - **The arm works and still converts ZERO asks**, and the chain
      terminus explains why: every TPC-DS window CTE is a window over a
      MULTI-KEY aggregate —
      `Sort>WindowAgg>Sort>Aggregate[groupExprs=6]` (x8),
      `[groupExprs=5]` (x7), `Sort>Aggregate[groupExprs=2]` (x4).
      With 2+ grouping columns none is unique on its own, so upstream's
      `isunique` counting argument does not exist.
    - **These are columns PostgreSQL itself would not resolve.** The
      B-06 gap is far smaller than the 648-unknown figure implied.
    - Remaining honest scope: (a) the `SetOp` population (100 asks)
      reaching `synthUnionLiterals` without matching — diagnose before
      widening; (b) the unexplained `Project(Filter)` contradiction
      (158 asks/run) — the likeliest place a REAL gap hides. An
      EA/q-error ratchet is premature.
    - **ESCALATION for the owner**: if this task's three dependent
      residuals need estimates PG cannot produce, it is the RESIDUALS'
      unblock conditions that need revisiting, not the statistics. The
      loop is not re-scoping them itself.
    - Step 4 respected: `rows<=1` guard and Q78 firewall UNTOUCHED.
  - **Step 2 slice 5 (loop 2026-09-21 \#30): census COMPLETE, no rule
    added — and a RETRACTION.**
    - **RETRACTION**: slices 2-3 recorded that the design's G1 note was
      contradicted because `Project(Filter)` bodies reach the consumer
      158x/run. **That was the loop's own classification error** — the
      census looked one level below the `Project`. One level further:
      `Project>Filter>Aggregate` (142), `Project>Filter>Project>
      Aggregate` (16). Every `Filter` is a **HAVING over an aggregate**,
      exactly the shape `groupUniqueNDistinct`'s doc describes. **G1's
      claim STANDS.**
    - Full classification of the 648 unknown asks:
      - 158 = group keys of **3-key** (142) and **4-key** (16)
        aggregates.
      - 366 = windows over **6-key** (8), **5-key** (7), **2-key** (4)
        aggregates.
      - 100 = `UNION ALL` outputs whose branches project DATA columns,
        not the literal tags `synthUnionLiterals` exists for.
      - 24 = `DistinctOn`, unexamined (almost certainly the same class).
    - The first two are ONE class: a group key of a MULTI-key
      aggregate, where upstream's `isunique` counting argument does not
      exist. The third has no `pg_statistic` row upstream either.
      **The entire population is columns PostgreSQL would not resolve.**
    - **There is no remaining PG-faithful synthesis work on this task.**
    - **OWNER SCOPING DECISION REQUIRED (escalated, not taken):** this
      task exists to unblock three residuals, and the statistics they
      wait for do not exist upstream. Either the residuals' unblock
      conditions are revisited, or unblocking them means deliberately
      diverging from PG's estimator — which the loop will not decide.
      - **Owner answer (2026-09-21)**: the unblock conditions are
        revisited as **measurement**, not divergence — filed as
        **M0145-0011** (knob-arm E1/E2 evidence for the firewall and
        the CTEScan-leaf rule; the residuals' named conditions are
        row-estimate conditions, and row-level CTE estimates are
        already PG-equivalent) and **M0145-0012** (retire the
        goopg-only `rows<=1` fallback once `derived >= guard effect`
        is demonstrated). Proposal:
        `tmp/blocker-re-think-260912/01-proposal.md`.
    - Step 4 respected throughout: `rows<=1` guard and Q78 firewall
      UNTOUCHED.
  - **On completion — reconsider the blocked work (evaluate, do not
    auto-do):**
    - `flattenPulledBodyTree`'s bare-`*SeqScan` rule (M0145-0003 ANY
      residue): RESOLVED into tasks — M0145-0011 E2 measured the rule
      relaxed (class relocated to the seam's `pulled-leaf-not-scan`),
      and the seam admission it exposed is filed as **M0145-0013**.
    - `outer-over-derived` firewall (`relfromjoinlist.go`; 12 corpus
      fires at filing, 3 on the loop-#34 re-measure): M0145-0011 E1
      measured the bypass clean at SF0.25 AND SF1 (Q77/Q78 only, hash
      join holds, values identical, timings improve); the owner
      accepted the redefined condition and the lift is filed as
      **M0145-0018** — sequenced after M0145-0013 and gated on a
      fresh E1 re-verification at execution time.
    - The `rows<=1` guard (`joinsearch.go:520-526`, `initialRelRows`): same criterion —
      removal only after derived estimates demonstrably reach the
      guard's effect on the year_total shapes (ledger step 4). That
      removal is filed as **M0145-0012** — the arm has no PG
      counterpart (`clamp_row_est` keeps the collapse), so it is a
      goopg-only divergence to retire.
    - Re-run the pull-up/seam decline census afterwards so the residual
      buckets reflect the new state.
  - **CLOSED 2026-09-22 (loop \#78): the completion census is run and the
    task's four ledger steps are all discharged.**
    Movement: none — `CATEGORIES-EXCL-MATCH` unchanged on the default arm;
    a recon, no production file touched.
    Design: `docs/design/planner-b06-cte-stats/DESIGN.md`
    §"Closing census (2026-09-22)".
    - **The census answers the step's question, and the answer is that the
      `*CTEScan` bucket is NOT a residual.** TPC-DS SF0.25 knob arm,
      `GOOPG_NLI_CENSUS=1`, all 99 queries, both arms from this loop (same
      recipe, same binary sha):
      - `GOOPG_PULLUP_CTE_LEAF` **off** (default): `(pulled)` 27,
        `any-body-leaf-(*optimizer.CTEScan)` 15, `SubqueryExpr@scalar` 15,
        `ExistsExpr@or` 2, `InExpr@or` 1.
      - `GOOPG_PULLUP_CTE_LEAF` **on**: `(pulled)` **42**, `*CTEScan`
        **0**, the other three unchanged.
      - 27 + 15 = 42 — the class converts ONE-FOR-ONE, with no partial
        conversion and no other class moving. M0145-0013's admission
        already addresses it in code; what is left is that arm's
        promote-or-delete decision (C5), not more synthesis.
    - The `@or` classes and `SubqueryExpr@scalar` are unchanged, as
      M0145-0015 predicted — declines PG makes too
      (`prepjointree.c:877` `/* Stop if not an AND */`; EXPR sublinks are
      never jointree citizens).
    - **The arm is not free**: 302 plan-diff lines between the two goopg
      captures and `qual-placement` 28 -> 29 with `match` still 1. Inside
      the ±3 noise band, so not a measured regression — but not "no plan
      change" either. M0145-0013's decision should re-measure rather than
      read that +1 off this run.
    - **Measurement caveat, recorded as a rule**: M0145-0015's census
      (nominally the same recipe) reported almost exactly DOUBLE every
      figure across all five classes, proportions unchanged. Five
      independent halvings are less likely than one capture-recipe
      difference. Unresolved here; the consequence is that **absolute
      census totals compare only within an identical capture recipe, and
      the class PROPORTIONS are what carries across captures**. The
      conclusion above rests on a within-capture A/B, so it holds either
      way. Ledgered.
    - Step 4 respected to the end: the `rows<=1` guard and the Q78
      firewall were NOT touched by this task in any slice.

- [x] **M0145-0010 — parameterized-path legality: port
  `reparameterize_path` / `required_outer`** (filed 2026-09-21 by owner
  directive; carries the `lateral` decline family's resume point). The
  seam declines `lateral` problems — 8 corpus fires, real lateral
  dependencies in Q30/Q68, the wall `-3i-lateral-route`'s recon named —
  and partial-NLI admission stays a jointype whitelist `{INNER, SEMI}`
  where PG's nestloop dispatch admits `{INNER, LEFT, SEMI, ANTI}`
  (`joinpath.c:1842-1846`), because PG decides legality from
  parameterization (`param_info`/`required_outer`), not from an
  enumerated set. goopg's gap is the REL level: `Path.RequiredOuter`
  and its calculators already exist (`path.go:404`,
  `calcNestloopRequiredOuter`/`calcNonNestloopRequiredOuter` in
  `pathparam.go`, `ppi_rows` pricing in `pathparamindex.go`), but there
  is no rel-level `param_info`/`lateral_relids` and no
  `reparameterize_path` re-costing an existing path against a larger
  outer set. Scope:
  - (a) the RelOptInfo/jointree leaf gains a `required_outer`/`param_info`
    relid set — on the jointree arm this is an IR field, on the legacy
    arm it is another coordinate-translation consumer; prefer whichever
    arm is default when picked up, and sequencing after M0145-0005
    avoids building the translation twice.
  - (b) `reparameterize_path`/`get_param_path_clause_serials` analogues:
    re-price an existing path under additional outer parameterization
    (PG `postgres/src/backend/optimizer/util/pathnode.c:4242` and
    `:1910` respectively — invoked FROM `joinpath.c`), extending the
    existing `RequiredOuter` machinery rather than building from zero.
  - (c) legality reads parameterization where PG does — `join_is_legal`
    and the nestloop dispatch — instead of the whitelist.
  - (d) executor-capability check FIRST for every newly admitted shape:
    a parameterized path the executor cannot drive is a new
    `unexpressible` record, not a win — verify driveability before
    filing the path. Known asymmetry to check: the executor's ordinary
    (non-fused) `ordinaryInnerNestedLoopPartial` arm is INNER-ONLY while
    the planner whitelist admits {INNER, SEMI} — a SEMI/LEFT/ANTI
    partial NL that reaches it would mis-execute, so the check must
    cover that arm.
  Kind: impl
  Parent: none
  - **Scope (d) done FIRST (loop 2026-09-21 \#31) and it found a LIVE
    WRONG-ANSWER DEFECT at HEAD — fixed.**
    - M0137-0019b admitted SEMI on BOTH planner gates and left the
      executor twin `ordinaryInnerNestedLoopPartial` at INNER. Its own
      comment states the invariant that was broken.
    - Not a safe decline: the unattached driving scan means every worker
      scans the WHOLE outer, because `attachAll`'s result is ignored by
      `gatherOp` (`e10-attachall-precondition-unenforced`).
    - Measured: `count(*)` over a plain `Nested Loop Semi Join`
      returned **450000 parallel vs 150000 serial / 150000 ground
      truth** — exactly 3x with 3 workers.
    - **Invisible to every existing gate**: the acceptance arm runs
      SERIAL; no corpus query makes the shape (it needs a non-equality
      correlation over an UNINDEXED inner, else a hash semi join or a
      `*NestedLoopIndexJoin` is elected — TPC-H Q4, M0137-0019b's own
      named consumer, is the indexed shape and proves nothing here);
      and the PLAN was correct, so the plan channel had nothing to flag.
    - Fixed by widening the executor to `{INNER, SEMI}` (narrowing the
      planner would revert a deliberate widening). Pinned end-to-end by
      `TestParallelSemiNestedLoopIdentity`, verified red before the fix.
    - Design: `docs/design/parallel-query/09-verification-and-measurement.md`.
    - **Still open**: LEFT/ANTI diverge from PG on both sides (PG admits
      `{INNER, LEFT, SEMI, ANTI}`, `joinpath.c:1842-1846`); and the
      `gatherpaths.go:722` spine mirror tests `!= JoinInner` although its
      comment claims it mirrors an arm that now admits SEMI — a refusal,
      so safe, but a documented-invariant mismatch. Both ledgered.
    - Scopes (a) and (b) remain untouched.
  - **Scope (c) jointype half landed (loop 2026-09-21 \#32).** The
    ordinary partial nested-loop family now admits
    `{INNER, LEFT, SEMI, ANTI}` — PG's nestloop dispatch set minus
    RIGHT/FULL.
    - Widened across ALL FOUR gates in one change (executor predicate,
      planner node gate, path arm, spine mirror), the discipline the
      SEMI wrong answer teaches.
    - **Capability verified BEFORE admission**, per scope (d), rather
      than trusted from the in-tree comments that already called the
      refusal "scope, not correctness": both shapes showed the N-copy
      signature under a forced Gather BEFORE the widening and agree
      with serial after.
    - Closed a drift: the spine mirror's comment claimed to follow the
      path arm "guard-for-guard" while testing a narrower set. The two
      now share one predicate, `partialNestLoopJointype`.
    - **Fixture trap recorded**: a plain `LEFT JOIN ... ON <non-equi>`
      COMMUTES to a RIGHT join, so a naive fixture tests the REFUSAL
      while appearing to test the admission. The test now asserts the
      planned jointype so it cannot go vacuous.
    - Four existing refusal pins updated deliberately, not deleted.
    - **No corpus movement** (plans 99/99 identical, acceptance arm
      24/24): a capability and faithfulness change, not a performance
      one. Do not describe it as a win.
    - Still out: RIGHT/FULL need a cross-worker inner-match reduction
      that does not exist.
  - **Scope (c) fused half landed (loop 2026-09-21 \#33).** The FUSED
    NLI family now admits `{INNER, LEFT, SEMI, ANTI}` too, so BOTH
    families match PG's dispatch set minus RIGHT/FULL.
    - **Capability measured BEFORE admission** per scope (d): the fused
      ANTI returned 700 vs a serial 350 and LEFT 800 vs 400 under a
      forced Gather BEFORE the widening; all three agree after.
    - **Design lesson recorded**: this family has NO executor twin to
      drift — its attach arm calls the planner predicate directly — so
      the same-day SEMI wrong answer could not have happened here. A
      shared predicate is structurally stronger than two predicates
      plus a comment saying they must agree; the ordinary family should
      eventually follow.
    - Both families now share one jointype set per type domain
      (`partialNestLoopJoinType` / `partialNestLoopJointype`).
    - Five more refusal pins updated deliberately, including two
      descent tests that had used LEFT/ANTI merely AS a refused
      jointype and now use CROSS.
    - **No corpus movement** (plans 99/99 identical, acceptance arm
      24/24). Capability and faithfulness, not performance.
    - **HONEST SCOPE NOTE**: widening whitelists is NOT this task's
      objective. Scopes (a) and (b) — rel-level
      `required_outer`/`param_info` and a `reparameterize_path`
      analogue — remain untouched, and until they land goopg reaches
      PG's ANSWER by an enumerated set while PG derives it from
      parameterization. That is the substance, and it is next.
  - **Recon + design doc landed (loop 2026-09-21 \#34)**:
    `docs/design/0100-0149/m0145-0010-parameterized-path-legality.md`
    (the task's first, now indexed). It names the three gaps precisely
    and **corrects this task's own census figure**.
    - **The filed "8 corpus fires" for the `lateral` family is STALE.**
      Re-measured on TPC-DS SF0.25: `leaf-count` 26,
      `semianti-not-tail` 3, `outer-over-derived` 3, `outer-spine` 2,
      **`lateral` 2** — 36 total, where the 8 came from a pre-slice-3
      census with 132 total declines.
    - `leaf-count` (26) now DOMINATES and is ledgered as
      executor-substrate-blocked — out of this task's reach. Its owner
      is M0145-0005's IR-direct leaf materialisation slice (named in
      that task's decline-bucket ownership note, 2026-09-21 audit).
    - Gap detail: `RelOptInfo` has no lateral/param sets at all; PG's
      `join_is_legal` needs BOTH `lateral_relids` (direction) and
      `direct_lateral_relids` (rejects indirect refs),
      `joinrels.c:569-596`. goopg's lateral refusal is a STATEMENT-level
      veto whose marker lives on a chain node the leaf flattening
      DISCARDS — deriving per-leaf lateral relids before that is the
      real work of scope (a), not adding fields.
    - **Next step is a MEASUREMENT, not the refactor**: instrument where
      a parameterized path is refused for want of re-pricing and count
      scope (b)'s corpus fires. Scope (b) is not a lateral feature and
      its population has never been measured.
    - **ESCALATION for the owner**: the task was filed with an 8-fire
      lateral consumer that is now 2. Whether that still justifies a
      rel-level refactor is a scoping decision the loop does not take.
  - **Scope (b) MEASURED (loop 2026-09-21 \#35) — the second premise is
    wrong too. This task cannot be built as written.**
    - Widened the `NLIGATE` census from SEMI/ANTI to every jointype (it
      had been scoped to semijoins for M0145-0007, so it reported
      NOTHING about the general population). TPC-DS SF0.25:
      `no-parameterised-inner` **15 014**, `filed` 10 814,
      `inner-rejected` 1 625, `jointype-right` 77.
    - **The 15 014 are NOT scope (b)'s population.**
      `reparameterize_path` does not CREATE parameterized paths — it
      re-prices an EXISTING path under a LARGER `required_outer` and
      refuses a non-superset outright (`pathnode.c:4249`). The paths
      that populate the list come from `create_index_paths`' join half,
      **which goopg already ports** (`pathparamindex.go`). Those 15 014
      are rels with no usable index clause, where PG finds nothing
      either.
    - **The task text's call site is factually wrong.**
      `reparameterize_path` is not "invoked FROM joinpath.c"; its
      callers are `get_cheapest_parameterized_child_path`
      (`allpaths.c:2096`) for appendrel CHILD paths — **partitionwise
      joins** — and its own Append/Material/Memoize recursion.
      **goopg has no partitionwise joins**, as `pathparam.go`'s own
      comment records. Scope (b)'s primary consumer does not exist here.
    - **OWNER DECISION REQUIRED — re-scope or close.** Three options in
      the design doc: narrow to the lateral admission alone (2 fires);
      defer behind partitionwise joins; or close, recording that scope
      (c)'s structural intent was largely met by the shared jointype
      predicates the two widening loops introduced.
    - The loop does not choose. Building as filed would implement
      machinery for consumers measurement says are absent.
    - **OWNER ANSWER 2026-09-23 (delegated): CLOSE — option (c).** This
      task is `[x]`. What the task actually needed already landed: the
      substantive legality work is scope (c)'s shared jointype
      predicates (`partialNestLoopJoinType`/`partialNestLoopJointype`)
      plus scope (d)'s capability-before-admission discipline, both
      measured; scope (b)'s named consumer (appendrel child-path
      re-pricing for partitionwise joins) does not exist in goopg; and
      scope (a)'s rel-level `param_info`/`lateral_relids` refactor has a
      measured consumer of 2 corpus fires (Q30/Q68 — the `lateral`
      decline family keeps its posthoc fallback post-flip, so this is
      reachability, not correctness). A rel-level refactor for 2 fires
      fails the measured-demand bar. The lateral residual is carried
      forward as **M0146-0011** (post-cutover re-census; escalate to an
      impl task only if the population grows or `lateral_relids` become
      load-bearing for another admitted shape). `Movement: none` —
      scopes (c)/(d) landed under this entry's earlier slices and were
      already recorded; the closure itself changes no code.
  - **On completion — reconsider the blocked work (evaluate, do not
    auto-do):** the `lateral` decline family (Q30/Q68 witnesses) on the
    then-default arm; the partial-NLI whitelist's LEFT/ANTI entries
    (ledgered "by scope" refusals — admit only where legality says so
    and the executor check passes); re-run the pull-up/seam decline
    census afterwards so the buckets reflect the new state.
- [x] **M0145-0011 — measured re-evaluation of the derived-input
  blockers (proposal 案A)** (filed 2026-09-21 by owner directive;
  rationale `tmp/blocker-re-think-260912/01-proposal.md` — local,
  gitignored). M0145-0009's census proved the 648 unresolvable
  column asks are all columns PG itself would not resolve — but the
  residuals' named unblock conditions are ROW-estimate conditions, and
  row-level CTE estimates already sit at PG-equivalent granularity
  (`EstimateRows(*CTEScan)` recurses the body ≈
  `set_cte_size_estimates`'s `plan_rows` propagation; the M0129-S1
  fallback is strictly more conservative than PG, which keeps the
  collapsed estimate via `clamp_row_est`). Whether DP still misprices
  catastrophically under today's estimates is a MEASUREMENT question,
  not an argument. This task gathers the evidence:
  - (a) add a diagnostic env flag (e.g. `GOOPG_DERIVED_FIREWALL=off`)
    that bypasses `problemPairsOuterWithDerived` — EXPLAIN/probe use
    only, default arm unchanged, same precedent as the census flags.
  - (b) E1: on the KNOB arm with the flag, capture plans + timings +
    values for Q78 and the `outer-over-derived` corpus fires (~12
    pre-slice-3; **3** on the loop-#34 re-measure — re-run the census
    first to get the current set). Pass criteria: no NL-epsilon shape,
    Q78 holds ~19 s, values identical; on failure record WHICH query,
    WHICH election, and by how much it degraded.
  - (c) E2 (downstream of (a)): relax `flattenPulledBodyTree`'s
    bare-`*SeqScan` rule for `*CTEScan` leaves on the knob arm —
    REQUIRED pairing with (a)'s flag because pulled ANY/EXISTS bodies
    become JoinSemi/JoinAnti SJIs and the firewall's jointype switch
    (`relfromjoinlist.go:604`) covers Semi/Anti, so without the flag
    the problems still decline. Re-run the pull-up/seam decline census
    afterwards.
  - (d) adjudicate and REPORT to the owner: clean evidence → the
    unblock conditions can be redefined as "PG-equivalent row
    estimates + measured safety" and relaxation can be filed as its
    own task; dirty → the residual is the cost model or the search
    shape, not statistics — then options are the structural narrowing
    (veto NL paths with a derived inner, `inner.Relids ∩ derived`,
    or preserved-side-only decline) or documented permanence. The
    loop does not pick.
  Hard constraints: the Q78 `outer-over-derived` firewall and the
  `rows<=1` guard stay UNTOUCHED on the default arm; knob-arm captures
  are EXPLAIN-only private evidence (G8); no relaxation is landed by
  this task. (Kind is `impl`, not `recon`: scope (a) lands a
  diagnostic flag under `internal/` — C1 counts env-gated additions
  as production changes even when the rest of the task is
  measurement.)
  Kind: impl
  Parent: none
  - **(a)+(b) DONE 2026-09-21** (`8b91593c7`); **(d) reported 2026-09-21** —
    design doc `docs/design/0100-0149/m0145-0011-derived-input-blocker-remeasurement.md`.
    E1 is clean at SF0.25 AND at **SF1** (private clone of the SF=1 datadir on
    `:5547`, knob arm, same build both ways). Only Q77 and Q78 move in either
    arm at either scale; Q78 stays a hash join (no NL-epsilon shape) and its
    CTE estimates go honest (cs 11->2280, ws 7->1485); values byte-identical
    (Q77 ck 9bd1900a34ce55c5, Q78 ck 3331a74f6d9f53e6); timings IMPROVE with
    the firewall off (Q77 9015->5306 ms, Q78 50885->48066 ms). Adjudication
    for the owner: the unblock conditions can be redefined as "PG-equivalent
    row estimates + measured safety at the scale the catastrophe was
    measured", and a relaxation is filed as its own task (owner accepted:
    **M0145-0018**, sequenced after M0145-0013). **No relaxation is
    landed here**; the firewall and the `rows<=1` guard are untouched on the
    default arm.
  - **(c)/E2 is the only scope still open** — relax
    `flattenPulledBodyTree`'s bare-`*SeqScan` rule for `*CTEScan` leaves on
    the knob arm, then re-run the pull-up/seam decline census. This task
    stays `[ ]` until (c) lands.
  - **(c)/E2 MEASURED 2026-09-21, and the finding closes the task.**
    `GOOPG_PULLUP_CTE_LEAF=on` (default OFF, flag-provenance table, not
    exempt) admits `*CTEScan` leaves into `flattenPulledBodyTree`'s splice.
    Pull-up census over TPC-DS SF0.25, all 99 queries:
    `any-body-leaf-(*optimizer.CTEScan)` 30 -> 0, `(pulled)` 42 -> 72, every
    other class unchanged, no `PULLUPCLASSIFY` refusal either way; all 30 are
    Q14/Q23/Q95. **But the seam census says nothing reaches the DP**: those
    same statements go from `seam-decline leaf-count` to `seam-decline
    pulled-leaf-not-scan`. The bare-`*SeqScan` rule is ONE invariant held at
    TWO sites — `flattenPulledBodyTree` produces, `tryPGShapedJoinSearch`
    re-checks — so relaxing the producer alone only relocates the decline.
    The three plans that move do so via the documented `pulled`-suppression
    side effect (estimated cost falls 1.4x-2.4x, measured runtime is flat to
    2-11% WORSE, values byte-identical, semi/anti counts preserved).
  - **Resume point FILED as M0145-0013** (owner directive 2026-09-21):
    the seam's pulled-leaf binding loop in
    `internal/optimizer/joinsearchseam.go` needs a `rangeBinding` for a
    leaf with no `Table`/`Alias` and an
    `estimateBaseRelInfo`/`applyRelSizeFallback` arm for a leaf with no
    catalog statistics; the adjacent `flat-leaf-not-scan` check is the
    third site to audit. Both sibling comments now cross-reference each
    other. The firewall relaxation (d) recommended is filed as
    **M0145-0018** (owner GO 2026-09-21), sequenced after 0013.
    Ledgered 2026-09-21.
  Movement: none — the default arm is unchanged by construction (both flags
  default to today's behaviour) and the gates confirm it; the knob-arm census
  moved `any-body-leaf-(*optimizer.CTEScan)` 30 -> 0.
- [x] **M0145-0012 — retire the `rows<=1` CTE fallback guard
  (`initialRelRows`, `joinsearch.go:520-526`)** (filed 2026-09-21 by
  owner directive; same proposal). The M0129-S1 arm — when a
  filter-wrapped `*CTEScan` leaf's estimate collapses to <=1, substitute
  the body's UNFILTERED row count — has NO PostgreSQL counterpart:
  `set_cte_size_estimates` keeps the collapsed estimate
  (`clamp_row_est` floors at 1, `costsize.c:5356-5363`); only the
  `rows<1→1` floor itself is faithful. So the fallback is a goopg-only
  divergence and must eventually go. Per the ledger criterion
  (`derived >= guard effect`), either
  - demonstrate by measurement that removing the arm reproduces no
    collapse-class plan change (the corpus fires where the arm
    currently engages, incl. the `year_total` shapes) — i.e. derived
    estimates without the arm are already >= the guard's effect; OR
  - wire a mechanism that makes that true without the arm (e.g.
    qual-aware estimation through eligible single-ref CTE bodies —
    the `pushQualsThroughSingleRefCTEs` inline path already gives
    real selectivity for refs==1) and then demonstrate it;
  then remove the arm and update the M0129-S1 comment. Gates: the same
  measurement discipline as M0145-0011 — knob-arm private evidence
  first, full gate set before any default-arm removal; a removal that
  reproduces the C-04a class is an automatic no-go. Sequenced AFTER
  M0145-0011 so its evidence base is reused.
  Kind: impl
  Parent: none
  - **`[!]` BLOCKED 2026-09-21 by measurement — BOTH filed routes are
    refuted.** Design doc
    `docs/design/0100-0149/m0145-0012-cte-rows-fallback-retirement.md`;
    apparatus `GOOPG_CTE_ROWS_FALLBACK=off` + a `CTEROWSFALLBACK` DP-trace
    line, both landed. No removal landed.
    - Corpus census, TPC-DS SF0.25, DEFAULT arm, all 99 queries: the arm
      engages on exactly THREE queries — Q31 `ws` 1→1846, Q39 `inv` 1→20,
      Q74 `year_total` 1→8325 — and nowhere else.
    - **Route 1 is FALSE**: removing the arm moves those same three plans
      and each moves INTO the collapse class the arm exists for (every
      equi-condition demoted from a join condition to a `Join Filter` on a
      Nested Loop). Values byte-identical; **Q74 1509 → 25005 ms, 16.6x**,
      Q31 +12%, Q39 flat. Scale-dependent per M0145-0011, so SF1 is worse.
      The task's own text calls this an automatic no-go.
    - **Route 2 is INAPPLICABLE**: `pushQualsThroughSingleRefCTEs` needs
      `refs == 1`, and all three fires are MULTI-reference CTEs (`ws` ×3,
      `inv` ×2, `year_total` ×4).
    - **The real prerequisite** is a third thing neither route names:
      `examine_simple_variable`'s non-recursive-CTE arm
      (`postgres/src/backend/utils/adt/selfuncs.c:5736-5870`) locates the
      CTE's `subroot` via `cte_plan_ids` and recurses on the target-list
      `Var`, so PG estimates a qual on `year_total.dyear` from
      `date_dim.d_year`'s real statistics and never collapses. Porting that
      lookup is what lets this arm go.
    - Note the methodological hazard this exposed: the regression is
      INVISIBLE to every value gate in the repository — values are
      byte-identical in all three queries. Only the plan shape and the clock
      move. `TestInitialRelRowsCTEFallbackGate` is the unit-level witness.
    - **ESCALATION**: the owner decides whether to file the
      `examine_simple_variable` port as its own task (which would subsume
      route 2) or to accept the arm as documented permanence. The loop does
      not pick. Ledgered 2026-09-21.
    - **OWNER ANSWER 2026-09-22: GO — the port is filed as M0145-0020.**
      This task resumes after it lands; the fallback stays until then.
    - **OWNER DIRECTION 2026-09-22 (delegated; progress-report §3.3)**:
      M0145-0020 closed measured-no-gap — PG punts on all three corpus
      fires (`setOperations`, single-grouping-column) — and 0020a fixed
      the true grouped-output factor, but its own record states the arm
      A/B was never re-run and Q74 still moved into the collapse class
      on the last A/B. **This task stays `[!]`**; the named next step is
      filed as **M0145-0024** (Q74 `year_total` residual recon — the
      estimate_num_groups / set-op-output question "PG punts here too,
      yet does not collapse" — plus the 99-query arm ON/OFF A/B). The
      fallback stays until that recon lands AND the A/B shows no
      collapse-class move; do not remove the arm on the strength of
      0020a alone.
  - **OWNER QUESTION 2026\-09\-23 \(from M0145\-0024\):** on the current tree
    the arm fires on Q74 alone, and there it makes goopg diverge from PG\'s own
    plan. PG 18.3 collapses Q74 exactly as goopg does with the arm off.
    Option \(a\): retire the arm for plan parity \(Q74 2.27 s → 29.61 s; PG
    takes 53.49 s\). Option \(b\): keep it as a named, owner\-accepted
    performance divergence and close this task won\'t\-do. The removal steps
    are in `m0145\-0024\-q74\-residual\-collapse\-recon.md`.
  - **OWNER ANSWER 2026\-09\-24 \(delegated; OWNER DECISIONS 2026\-09\-24 in
    the banner\): option \(a\) — retire the arm for plan parity.** The
    recon inverted the trade: the collapse class the arm prevents is now
    PG\'s own elected plan on Q74, so the arm is a divergence, not a
    guard. The \"reproduces the collapse class = automatic no\-go\"
    criterion above is superseded — collapsing IS matching PG here.
    Post\-removal goopg \(29.61 s\) still beats PG \(53.49 s\) on the same
    plan. Execute with the filed gate discipline \(knob\-arm evidence
    first, full gates before default\-arm removal\); task is `[ ]` again.
  - **DONE 2026\-09\-24 — the arm is retired.** Design:
    `docs/design/0100-0149/m0145-0012-cte-rows-fallback-retirement.md`
    §"Retirement"; evidence `analysis/m0145/m0145-0012/`.
    - Knob\-arm evidence was M0145\-0024's 99\-query ON/OFF A/B \(only Q74
      moves\). Removed the arm in `initialRelRows`, `cteRowsFallbackEnabled`,
      its trace and the `GOOPG\_CTE\_ROWS\_FALLBACK` flag \(retired\); the
      unit witness now pins PG's collapsed\-and\-floored estimate.
    - Fire\-set HEAD vs staged: exactly Q74 at SF0.25 and SF1, rows
      identical, `introduced=none` \(no SF1 timeout\). SF0.25 sweep 96/96,
      only Q74 changed, 2 s → 29 s \(PG 53 s on the same plan\). Spotcheck,
      acceptance 24 MATCH, ea\-ratchet 52/52.
    Movement: none — CATEGORIES-EXCL-MATCH SF0.25 join-method 67 -> 66 only (within ±3); SF1 and match counts unchanged.
- [x] **M0145-0013 — admit pulled `*CTEScan` leaves at the seam
  (`pulled-leaf-not-scan` / `flat-leaf-not-scan`)** (filed 2026-09-21
  by owner directive; the follow-on task M0145-0011's E2 resume point
  named). E2's flag measurement moved `any-body-leaf-(*CTEScan)`
  30 -> 0 at the pull-up census (all Q14/Q23/Q95) — and every one of
  them then declined at the seam's `pulled-leaf-not-scan` re-check:
  the bare-`*SeqScan` rule is one invariant held at two sites
  (`flattenPulledBodyTree` produces, `tryPGShapedJoinSearch`
  re-checks). Land the admission machinery E2 scoped
  (`internal/optimizer/joinsearchseam.go`):
  - a `rangeBinding` for a pulled leaf with no `Table`/`Alias` —
    the body's own bindings already exist on the `jtPullup` record
    (`leafScans`/`bodyBindings`); hand them through rather than
    synthesising opaque ones;
  - an `estimateBaseRelInfo`/`applyRelSizeFallback` arm for a leaf
    with no catalog statistics — `EstimateRows(*CTEScan)` recurses
    the body (≈ `set_cte_size_estimates`' `plan_rows` propagation,
    `postgres/src/backend/optimizer/path/allpaths.c:2959`); do NOT
    synthesise column stats (M0145-0009's census proved the
    remaining asks are columns PG itself leaves unknown);
  - audit the third site, the `flat-leaf-not-scan` check, the same
    way.
  Hard constraint, unchanged: a `pulled` mark must not suppress the
  statement's legacy subplan route when the seam still declines the
  problem — either delay marking until admission, or fall back to
  the post-DP unnest on decline (the Q4 16s regression shape in the
  M0145-0008 ledger row is this window). Correctness gate: values
  identical on the moved statements.
  Expected movement: `pulled-leaf-not-scan` 30 -> 0 on the SF0.25
  seam census (Q14/Q23/Q95); the admitted problems then reach the
  `outer-over-derived` check — Semi/Anti SJIs touching the derived
  leaf still decline there until M0145-0018, which is the correct
  ordering (admission machinery first, relaxation last). Re-run
  PULLUPCENSUS + seam-decline census + SUBLINKCENSUS and record the
  new pinned-spine/jointree-pullup ratio. Knob arm only; default
  arm byte-identical.
  Kind: impl
  Parent: M0145-0011
  - **DONE 2026-09-21, knob arm.** Design doc
    `docs/design/0100-0149/m0145-0013-seam-admission-for-pulled-cte-leaves.md`.
    Both consumer sites route through `seamLeafBinding` (admission) and
    `seamLeafRelInfo` (pricing routed on `b.table`), so the invariant is held
    in ONE place instead of three.
    - Seam census SF0.25 knob arm, vs a pre-change binary from a worktree at
      HEAD: **`pulled-leaf-not-scan` 19 → 0**; `leaf-count` 15/15,
      `semianti-not-tail` 6/6, `outer-over-derived` 6/6,
      `flat-leaf-not-scan` 0/0; **`residual-hits-pad` 0 → 4** is the new wall.
      Pull-up census unchanged, as it must be.
    - Correctness gate PASSED: exactly Q14/Q23/Q95 move and all three are
      value-identical to M0145-0011's captures. With the CTE-leaf flag off the
      refactor moves **0/100** plans, so the `*SeqScan` path is inert.
    - The `*CTEScan` binding keeps `table == nil` DELIBERATELY —
      `leafIsDerivedInput` reads it, which is what holds the
      `outer-over-derived` firewall in force until M0145-0018.
      `TestSeamLeafBindingAdmission` pins it.
    - **RESIDUAL, reported not hidden**: the unlocked plans are mispriced.
      Against the honest `leaf-off` knob-arm baseline Q23 −14%, Q14 +25%,
      **Q95 3.0x slower** (3004 → 9148 ms, estimated cost 70693 → 1232685).
      Values identical, so this is pricing, not correctness. It is the first
      thing M0145-0018's re-verification precondition will trip on. Ledgered.
    - The `pulled`-suppression hard constraint is unchanged, not discharged:
      0013 reduces how often the seam declines, not what a decline does.
  Movement: none — the default arm is byte-identical by construction
  (`GOOPG_PULLUP_CTE_LEAF` defaults off, so no CTE leaf reaches the seam) and
  the gates confirm it; none of S3's three instruments move. The knob-arm
  seam census moved `pulled-leaf-not-scan` 19 → 0, which is this task's own
  expected-movement criterion, not a lineage instrument.
- [x] **M0145-0014 — recurse the pull-up into pulled bodies' own
  quals (`any-nested-sublink`)** (filed 2026-09-21 by owner
  directive). Census: 6 conjuncts on TPC-DS SF0.25 decline because
  the ANY/EXISTS body's own WHERE still contains a sublink. PG
  recurses — after `convert_ANY_sublink_to_join`/
  `convert_EXISTS_sublink_to_join` splices the body in,
  `pull_up_sublinks_qual_recurse` is re-run on the pulled-up quals
  (`postgres/src/backend/optimizer/prep/prepjointree.c:682-693`,
  `:736-747`, and the NOT arm at `:836-845`), so
  `x IN (SELECT … WHERE EXISTS (…))` becomes two semi/anti
  jointree entries, not one declined conjunct. Port the recursion:
  when `pullUpAnyBody`/`pullUpExistsBody` binds a body whose quals
  still carry EXISTS/IN sublinks, run the same classifier on them
  (depth-guarded; each level re-bases correlation one step further
  out) and fold the resulting bodies into the same `jtPullup`
  record. A nested sublink that is scalar stays declined — the same
  correct-decline rule as the outer level.
  Expected movement: `any-nested-sublink` 6 -> 0; re-run the
  pull-up census and record the ratio.
  Kind: impl
  Parent: M0145-0003
  - **PARTIAL 2026-09-21 — the population is TWO shapes, not one.** Design
    doc `docs/design/0100-0149/m0145-0014-nested-sublink-pullup.md`.
    - Attribution: the class fires on exactly two TPC-DS queries. **Q58**
      nests a SCALAR sublink (`d_week_seq = (SELECT …)`), **Q83** nests an
      ANY (`d_week_seq IN (SELECT …)`). Only Q83 needs the recursion.
    - **The gate was over-broad against the oracle.** PG converts the OUTER
      sublink first — `convert_ANY_sublink_to_join` gates on correlation and
      volatility only (`subselect.c:1345-1386`) — and recurses afterwards, so
      a nested sublink PG would not convert never blocks the outer
      conversion. goopg refused both via a blanket `exprHasSublinkPlan`.
    - LANDED: `bodyQualsAdmitSublinks` splits the refusal at BOTH pull-up
      arms (a sibling pair holding the identical gate), and
      `rebasePulledQual` now clones under `scopeSignal` rather than
      `scopeVeto` — the latter ABORTS at the first inner-plan slot, which
      made every admitted qual fail one step later as `rebase-failed`. A
      CORRELATED subplan still declines (`planHasOuterRef`).
    - Census SF0.25 knob arm: `any-nested-sublink` **12 → 0**,
      `any-nested-sublink-convertible` **6** (Q83, deferred), `(pulled)`
      42 → 48, no `REBASEFAIL`. Exactly Q58 moves; cost 20670 → 13692 with
      the `Hash Semi Join` retained.
    - **Verified against the PG oracle**, because Q58 returns 0 rows at
      SF0.25 and is a weak witness on its own: the same nested-scalar shape
      over `store_sales` gives `3654|181827` on PG 18.3 (`:65438`) and on
      goopg both before and after, and goopg now produces PG's shape with
      the nested scalar as an `InitPlan` filter on the body leaf.
    - **DEFERRED — the recursion itself** (Q83's 6). It needs a link
      predicate referring to the OUTER BODY's leaves, and
      `outerOperandAsLevel1`/`rebasePulledQual` only handle a Level-1 outer
      ref resolving to an EMITTING binding, so a link across two pulled
      bodies has no coordinate path today. Ledgered 2026-09-21.
    - **RECURSION HALF ATTEMPTED AND STOPPED 2026-09-21 (loop 45). Nothing
      landed; the finding is the deliverable.**
      - The coordinate path named just above as the blocker **was built and
        does resolve**: `children`/`parent` on `jtPulledBody`,
        `extractNestedPullups` (PG's `pull_up_sublinks_qual_recurse` re-run,
        depth-guarded), `flattenPulledBodies` (parent-before-child, so
        "body order IS leaf order" still holds), an ancestor-chain walk in
        `rebasePulledQual`, and a per-body `leftBits` replacing the
        hard-coded `emittingBits`.
      - **The real blocker is a THIRD thing**: a pulled leaf is
        NON-EMITTING — a SEMI/ANTI join never projects its RHS — so a parent
        body's columns exist only at the parent's own join node. A nested
        body's link qual reads a parent-body column from a DIFFERENT join
        node and the lowering has nowhere to evaluate it.
      - Witness, and it is an EXISTING test:
        `TestJointreePullupDeclineParity/nested-exists` panics with
        `createPlan: join clause references binding column 3 (v), which is
        not among the 3 output columns it is being re-based onto`.
      - Restricting the nested SJI's `syn_lefthand` to ancestor leaves only
        (PG's `j->rarg` + `available_rels = child_rels`) is NECESSARY but
        not sufficient — the residual is in the lowering, not the ordering.
      - **Resume: answer the lowering question FIRST.** Either the pulled
        leaves project their columns into the parent's schema for the
        duration of the search, or the nested semijoin is lowered as a
        subtree of the parent's RHS with its clause attached there. The
        witness runs in seconds and needs no cluster. Ledgered.
      - **RESOLVED 2026-09-21 (loop 46). M0145-0014 is `[x]`.** The first
        attempt's panic looked like a lowering gap and was not.
        - Printing the columns actually available at the failing join gave
          `have=[0 1 4]` — emitting plus the CHILD leaf, the PARENT leaf
          ABSENT. The search had chosen `(emitting SEMI parentLeaf) SEMI
          childLeaf`, and a semijoin does not project its right side, so the
          parent body's columns were gone before the nested link qual ran.
        - **The fix is ONE field**: `jtPulledBody.subtreeLeaves` makes a
          parent's `syn_righthand` cover its whole SUBTREE — what PG gets for
          free by splicing the nested conversion into `j->rarg`, whose arm
          comes back covering `child_rels` (`prepjointree.c:682-693`).
          Restricting `syn_lefthand` was necessary but never sufficient.
        - Census SF0.25 knob arm: `any-nested-sublink-convertible` **6 → 0**,
          `(pulled)` 48 → 54, no `REBASEFAIL`, no `PULLUPCLASSIFY`.
        - Exactly Q83 moves; values byte-identical and **762 → 553 ms**. Both
          nested shapes verified against the **PG oracle**: nested
          EXISTS-in-EXISTS and nested ANY-in-ANY each give `3654|181827` on
          PG 18.3 and on goopg.
        - `TestJointreePullupDeclineParity/nested-exists` RETIRED (it pinned
          the decline this task removes); replaced by
          `TestJointreePullupNestedExistsIsPulled`.
        - **Deferred**: the recursion is depth-guarded at 3; PG has no limit.
          The guard exists because every pulled leaf lands in ONE problem
          capped by `maxSearchRels`. Ledgered.
        - Method note worth carrying: the panic pointed at the lowering and
          the lowering was innocent. Printing the coordinates that WERE
          available beat reasoning about which mechanism ought to own it.
  Movement: none — knob arm only; the default arm is unchanged and the gates
  confirm it. The knob-arm pull-up census moved `any-nested-sublink` 12 → 0
  (6 pulled, 6 re-classified as the deferred convertible half), which is this
  task's own expected-movement criterion, not a lineage instrument.
- [x] **M0145-0015 — name the residual pull-up decline class and
  cover the PG-reachable subset under OR/NOT positions** (filed
  2026-09-21 by owner directive). The 60-conjunct census leaves 3
  "residual" conjuncts plus 2 `ExistsExpr` not at conjunct top
  level (under OR). Measurement first: extend the pull-up census so
  each unrecognised conjunct reports its sublink `%T` AND the clause
  position it was found under (OR arg, NOT arg, nested inside a
  scalar expression). Then implement only what PG actually converts:
  `pull_up_sublinks_qual_recurse` stops at non-AND clauses
  (`prepjointree.c:877` — OR args are NOT recursed), while a
  NOT-wrapped EXISTS does convert (the `is_notclause` arm,
  `:789-845`); EXISTS buried deeper (OR args, scalar contexts) stays
  a SubPlan in PG too — upstream's `convert_EXISTS_to_ANY`
  (`plan/subselect.c:1717`) merely rewrites it as a hashed ANY
  subplan, not a jointree citizen. So check whether goopg already
  has an equivalent hashed-subplan path before building anything,
  and file only the missing PG-equivalent piece. If the pullable
  subset is zero, record that and close as measured-no-gap.
  Expected movement: residual 3 named; the OR/NOT-position
  conjuncts moved for exactly the subset PG moves.
  Kind: impl
  Parent: M0145-0003
  - **CLOSED measured-no-gap 2026-09-21.** Design doc
    `docs/design/0100-0149/m0145-0015-residual-pullup-declines.md`.
    - **Step 1 done**: the census now reports `<kind>@<position>` —
      `top`/`not`/`or`/`scalar` — because in PG the POSITION decides
      reachability, not the kind. `pull_up_sublinks_qual_recurse` recurses
      AND and a NOT wrapper (`prepjointree.c:789-845`) and then
      `/* Stop if not an AND */ return node;` at `:877`, so OR args are
      never recursed.
    - **Step 2, the measurement** (SF0.25, knob arm, 99 queries):
      `SubqueryExpr@scalar` 29, `ExistsExpr@or` 4, `InExpr@or` 2, and
      **NONE at `@top` or `@not`**. Every residual decline is one upstream
      makes too — EXPR sublinks are never jointree citizens, and OR args are
      not recursed — so **the pullable subset is ZERO**.
    - **Step 3**: both fallbacks the task said to check for already exist and
      are LIVE — `internal/optimizer/exists_to_any.go` (goopg's
      `convert_EXISTS_to_ANY`, default ON) and
      `internal/executor/subplan_hash.go` (the hashed ANY probe, default ON,
      reached from `evalInExpr`). Nothing missing to file.
    - **No gate was widened**, deliberately: moving an `@or` conjunct would
      make goopg convert a sublink PG leaves as a SubPlan — a divergence the
      corpus value gates cannot see, because the rows would still be right.
    - Adjacent divergence ledgered rather than lost: goopg's hashed subplan
      degenerates PG's partial-match table to a single NULL bit, sound only
      while IN test expressions stay single-column.
  Movement: none — measured-no-gap. The census extension is the whole
  production change; it is what lets a later loop tell `@top` (a gap) from
  `@or` (parity) without re-deriving it.
- [x] **M0145-0016 — `semianti-not-tail`: admit non-tail synthetic
  leaves (leaf reorder + relset remap)** (filed 2026-09-21 by owner
  directive; ledgered at `joinsearchseam.go`'s construction contract
  as "real future work, not a guard to relax"). 3 fires on the
  SF0.25 census — the demoted-ANTI mid-chain shape
  (`web_sales ANTI web_returns JOIN date_dim`, walking
  `[real, synthetic, real]`): c2's contract requires every
  synthetic leaf in a tail slot, so a real leaf after a synthetic
  one in walk order declines. The work is leaf reorder +
  relset/span remap so `jl`'s leaf items, `ctx.bindings`, and the
  synthetic span bindings stay consistent for arbitrary placement —
  Q78's `translateToLayout` panic was this assumption violated, so
  every admitted shape needs a values check, not just a green
  census. NOTE: M0145-0005's remaining slice (a) makes semi/anti
  real numbered leaf items, which retires the synthetic-slot
  contract by construction — if that slice lands first, verify this
  decline is gone on the corpus and close this task as subsumed
  rather than building the remap.
  Expected movement: `semianti-not-tail` 3 -> 0 (Q78's chain is the
  witness — keep it oracle-verified, it is the query that panicked
  when the assumption broke).
  Kind: impl
  Parent: M0145-0005
    - **BUILT AND LANDED 2026-09-21 (loop 49).** `semianti-not-tail` **6 → 0**
      on the corpus; every other decline class unchanged; the two new reasons
      (`semianti-not-tail-with-pulled`, `semianti-perm-desync`) both at 0.
      - **It was a small change, and the reason is worth carrying**: the
        walk-to-problem index translation lives in exactly ONE place —
        `remapWalkOrderFlatToSpans`' `problemIndex` closure, which already
        encoded the pulled-splice shift — so the permutation composes there
        rather than being threaded through every consumer.
      - **Column-neutrality is what makes the composition safe.**
        `buildLeafSpans` assigns real spans in position order and synthetic
        spans out-of-band after them, so a STABLE partition preserves both
        relative orders: every span's `lo` is unchanged and only the index
        holding it moves. Nothing is renumbered, so quals already rebased
        into walk-order-flat space survive untouched.
      - **Acceptance bar met, as the task set it**: exactly Q78 moves, and it
        is verified against the **PG oracle**, not row-counted — 15 rows,
        `ck 4a8a89aae2584676` on PG 18.3 and on goopg both before and after,
        4041 → 3855 ms. The admitted problem elects a parallel `Gather` +
        `Finalize HashAggregate` for the `ws` CTE.
      - Cost-model note for whoever owns it: Q78's estimated cost ROSE
        (Limit 4724 → 8184) while measured time fell — the same
        estimate/measurement disagreement M0145-0013 recorded.
      - **Scope bound kept**: partition-AND-pulled-leaves still declines, now
        as its own named reason. The non-synthetic set would be two
        populations there and a single stable partition does not preserve the
        three-way split; ZERO corpus fires, so it is declined rather than
        guessed at. Ledgered with the resume point.
  Movement: none on the lineage instruments (plan shape is not one of S3's
  three). **NOT knob-arm-only — correcting the scoping assumption**: the seam
  `tryPGShapedJoinSearch` is shared, so the DEFAULT arm moves too. The SF0.25
  gate reports `same=98 changed=1`, and the one mover is Q78 — the same query,
  `PASS 15 rows ck=c06cf981a7819a37` against the git-tracked PG oracle, 3501 ms.
  The knob-arm seam census moved `semianti-not-tail` 6 → 0, which is this
  task's own expected-movement criterion.
  - **Step 0 done 2026-09-21: NOT subsumed.** Design doc
    `docs/design/0100-0149/m0145-0016-semianti-not-tail.md`.
    - M0145-0005 is still `[ ]` with only slice 1 landed, and the corpus
      agrees: fresh seam census on HEAD (SF0.25, knob arm, 99 queries) gives
      `leaf-count` 19, **`semianti-not-tail` 6** (3 per Q78 run, Q78 ONLY),
      `residual-hits-pad` 6, `outer-over-derived` 6.
    - **The shape is measured, not inferred.** New `traceSeamNotTail` prints
      `SEAMNOTTAIL synthetic=0x0002 want=0x0004 nprefix=2 nleaves=3` — three
      leaves, synthetic at walk position 1 where the contract wants 2: the
      `[real, synthetic, real]` demoted-ANTI walk the task predicts. The fix
      is the stable partition `[0, 2, 1]`.
    - **Stability is load-bearing**: real leaves carry the emitting column
      space, so a stable partition moves only POSITION masks and leaves every
      column offset attached to its own leaf.
    - **Inventory for the build loop** (in the design doc, so it is not
      re-derived): `scans`/`widths`; `walkWidths`, whose
      `remapWalkOrderFlatToSpans` ALREADY encodes one leaf-index translation
      across the pulled-leaf insertion that a permutation composes with;
      `semiAntiChainLink.lhs`/`.rhs` plus the SHARED `sjinfo`'s four RelSets
      (mutate in place — its comment forbids drift);
      `outerChainLink.preserved`/`.nullable`; `chainOnQual.belowNullable`;
      the `jl`/`ctx.bindings[:nprefix]` pairing; and `spans`, whose records
      move with their leaves while `lo` is never renumbered.
    - **Remap NOT built this loop**, deliberately: it is a coordinate change
      in the seam's most position-sensitive function, composing with an
      existing translation — the combination whose violation produced Q78's
      `translateToLayout` panic. A half-landed permutation is a wrong-answer
      path the corpus value gates cannot see. Ledgered.
  - **BANNER-ORDER QUESTION for the owner (not an edit, an escalation).**
    Item 3 lists `0001 → … → 0018` and the selection rule is "the first item
    that has a selectable (`[ ]`, dependencies met) task, in the order
    written". By that reading **M0145-0003 is first** — it is `[ ]`, as are
    0004, 0005, 0007 and 0008 — yet loops 39-48 have worked 0009 → 0016, and
    the owner filed 0011-0018 mid-chain without re-ordering. The loop has
    assumed 0003-0008 are umbrella/in-flight items whose children are the
    selectable work (0003's own named movement, the `leaf-count` class, is
    still open at 19 fires). **If that assumption is wrong, say so and the
    loop will restart at 0003.**
- [x] **M0145-0017 — census the never-reached sublink population
  and pull the PG-pullable subset** (filed 2026-09-21 by owner
  directive). Of 308 knob-arm sublink-planning events, only ~60
  conjuncts ever reach `pullUpSublinksIntoJointree` — the rest
  carry sublinks outside top-level WHERE conjuncts (SELECT target
  list, HAVING, ON clauses, nested expression positions). Most are
  probably correct non-pulls (PG keeps target-list scalar
  subqueries as SubPlans too), but the population has never been
  measured by clause position, and PG's reach is wider than WHERE:
  `pull_up_sublinks_jointree_recurse` runs the pull-up on every
  jointree level's quals, including join ON clauses — with PG's
  legality boundary preserved (`prepjointree.c:587-610`): ON quals
  are processed for INNER (both sides), LEFT (the RHS only) and
  RIGHT (the LHS only) joins, and NEVER for FULL — a sublink pulled
  from a null-preserved side is a wrong-answer class, so the census
  must record join side+type, not just "in an ON clause". Step 1:
  extend the census so a statement's sublinks are counted by clause
  position and kind, on both arms, and name which positions hold
  EXISTS/ANY conjuncts PG would pull. Step 2: extend
  `pullUpSublinksIntoJointree`'s walk to the ON quals of explicit
  joins honouring the same side/type restrictions, for the pullable
  subset only.
  If the census finds no PG-pullable class outside WHERE, record
  that and close as measured-no-gap.
  Expected movement: the 248-event never-reach population
  decomposed into "correctly out of reach" vs "newly pulled".
  Kind: impl
  Parent: M0145-0003
  - **CLOSED measured-no-gap 2026-09-21.** Design doc
    `docs/design/0100-0149/m0145-0017-never-reach-sublink-census.md`.
    - **PG's reach bounded from the oracle, not assumed**: `pull_up_sublinks`
      (`prepjointree.c:468`) begins and ends at `root->parse->jointree` and
      has exactly one caller (`plan/planner.c:737`). A jointree carries quals
      in two places — the `FromExpr`'s (the WHERE) and each `JoinExpr`'s (an
      explicit join's ON). Target list, HAVING, GROUP BY, ORDER BY and nested
      scalar contexts are **never candidates upstream either**, so goopg
      keeping them SubPlans is parity. The difference between the two reaches
      is EXACTLY the ON clauses.
    - **Step 1 done**: `noteOnQualSublinks` reports
      `ONSUBLINK jointype=<t> site=<kind>@<position>` at `planJoinPredicate`.
      The join TYPE is recorded because PG's legality boundary differs by it
      (INNER both sides, LEFT the RHS only, RIGHT the LHS only, **FULL
      never** — pulling from a null-preserved side is a wrong-answer class).
      The site string reuses M0145-0015's vocabulary.
    - **The measurement**: TPC-DS SF0.25, all 99 queries, BOTH arms —
      **ONSUBLINK 0**; routes knob 46 jointree-pullup + 567 pinned-spine,
      default 398 pinned-spine. A textual scan of the query corpus agrees.
    - **Decomposition the task asked for**: "correctly out of reach"
      entirely, "newly pulled" empty. Step 2 would be built against zero
      witnesses, which is this task's own measured-no-gap arm.
    - **What stays unbuilt, ledgered**: goopg's ON-clause walk itself. A
      statement that DOES put an EXISTS/ANY in an INNER join's ON clause
      would be pulled up by PG and not by goopg. The census line is
      permanent and prints exactly the fields needed to decide, so the
      resume point needs no new instrument.
  Movement: none — measured-no-gap. The census line is the whole production
  change.
- [x] **M0145-0018 — relax the `outer-over-derived` firewall
  (owner decision GO, 2026-09-21)**. M0145-0011's E1 measured the
  diagnostic bypass clean at BOTH scales: on SF0.25 AND SF1
  (private `:5547` clone) only Q77/Q78 move, Q78 stays a hash join
  with honest CTE estimates (cs 11->2280, ws 7->1485), values
  byte-identical, timings improve (Q77 9015->5306 ms, Q78
  50885->48066 ms). Owner has authorised the relaxation; this task
  lands it at the correct time — NOT now, because the fixes that
  make the relaxed path actually exercised are still in flight.
  Preconditions, checked at execution time, not assumed:
  - **M0145-0013 landed**: the pulled-CTE-body problems that E2
    admitted past `flattenPulledBodyTree` reach the firewall check
    only once the seam's leaf admission exists — relaxing earlier
    would leave the biggest derived-input population untested.
  - **Fresh E1 re-verification on the current tree**: re-run the
    SF0.25 seam census for the current `outer-over-derived` set
    (the population drifts — it was 12, then 3), then re-capture
    plans + timings + values for Q77/Q78 and the current fire set
    on the knob arm with `GOOPG_DERIVED_FIREWALL=off`, at SF0.25
    AND SF1. Pass criteria unchanged: no NL-epsilon election, Q78
    holds its ~19 s-class shape, values identical. Any C-04a-class
    regression is an automatic no-go — keep the firewall and
    escalate the measurement instead.
  Then, and only then: remove the decline in
  `problemPairsOuterWithDerived` (`relfromjoinlist.go`) — including
  its Semi/Anti arms, which is what lets pulled CTE bodies into DP
  — delete the diagnostic flag and the now-dead comment, and run
  the full gate set on the DEFAULT arm (units, tpch-spotcheck,
  SF0.25 sweep values + plan-shape channel, TPC-H acceptance, an
  SF1 timing spot-check on Q77/Q78). The `rows<=1` guard
  (M0145-0012) is a separate arm and stays untouched here — E1 was
  measured WITH it on, so removing the firewall does not depend on
  retiring it.
  Expected movement: `outer-over-derived` 3 -> 0 on the seam
  census; the Q14/Q23/Q95 pulled-CTE problems admitted by
  M0145-0013 reach DP.
  Kind: impl
  - **FRESH E1 RE-VERIFICATION RUN 2026-09-22 (loop \#82) — the
    2026-09-21 blocker is GONE, but one pass criterion is still
    violated, so the relaxation was NOT executed.** Movement: none
    (measurement only; `problemPairsOuterWithDerived` untouched).
    Design: the design doc's new §"Fresh E1 re-verification (2026-09-22)".
    - **Fire set re-derived, as the task insists.** SF0.25 knob arm census:
      `outer-over-derived` **3**, and the class vanishes entirely with the
      firewall off (`declines` 17 -> 14), so the census measures what it
      claims. Affected queries: **Q77 and Q78** — unchanged.
      - **Plan-diff trap worth recording**: a naive A/B plan diff reports
        FIVE changed queries (Q36, Q70, Q77, Q78, Q86). Three are the
        capture's pre-existing parse `error=3` queries whose only
        difference is the temp FILENAME inside the psql error text. A
        future loop diffing captures will hit this.
    - **The 2026-09-21 no-go is gone.** That was Q78 at SF1 electing
      `Nested Loop Left Join (cost=5494.86..1147565.07)` and not finishing
      in 1800 s. With **M0145-0019a** landed it elects
      `Hash Left Join (cost=16457.31..37717.11)` again.
    - **SF1 execution A/B** (private clone, fresh server per arm):
      - Q77: ON 5492 ms / OFF **6373 ms**, 44 rows, ck `e2f12e6ef310f604` both;
      - Q78: ON 29608 ms / OFF 29140 ms, 100 rows, ck `5e12c7e6baa093e8` both.
      Values byte-identical, Q78 holds its ~29 s class, no timeout, no
      C-04a-class regression.
    - **Why it is still not executed**: criterion 1 is "no NL-epsilon
      election" and Q77 has one — at both scales a `rows=1` `Append` branch
      goes `Hash Left Join (cost=0.00..0.03)` ->
      `Nested Loop Left Join (cost=0.00..0.06)` with the degenerate
      condition `s_store_sk = s_store_sk` demoted to a `Join Filter`, and
      the statement's top cost rises (SF0.25 `Limit` 10.65 -> 11.58). It is
      measurably benign (+881 ms, +16% on a 5 s query) and nothing like
      C-04a's 15 s -> 327 s, but it is literally what criterion 1 names.
    - **OWNER DECISION NEEDED — waive or keep.** Executing DELETES production
      code (`problemPairsOuterWithDerived` incl. its Semi/Anti arms, plus the
      diagnostic flag), which R3 makes non-reversible; the GO was conditioned
      on passing, and one criterion does not; and **0018's option choice is
      already awaiting a re-take** from loop \#79. The loop delivers the
      measurement and leaves the call.
    - **OWNER ANSWER 2026-09-22: EXECUTE — criterion 1 is WAIVED as
      measured-benign** (progress-report review §3.1, owner selected (a)).
      The single remaining election — Q77's `rows=1` `Append` branch going
      `Hash Left Join (0.00..0.03)` -> `Nested Loop Left Join (0.00..0.06)`
      with the degenerate `s_store_sk = s_store_sk` demoted to a Join
      Filter — is benign at both scales (+881 ms / +16% on a ~5 s SF0.25
      query; SF1 measures ON 5492 ms / OFF 6373 ms, i.e. faster with the
      firewall off), values byte-identical, no timeout, no C-04a-class
      move. The criterion's PURPOSE — excluding the C-04a regression class
      — is discharged by the fresh E1 itself. The criterion is re-scoped
      to "no NL election on a NON-degenerate join condition and no
      timeout-class move"; both hold. Execution proceeds as written:
      remove `problemPairsOuterWithDerived` including its Semi/Anti arms,
      delete `derivedFirewallEnabled`/`GOOPG_DERIVED_FIREWALL` and its
      `flaglabels.go`/`scripts/planner-flags.env` entries plus the pinned
      tests (`outer_over_derived_test.go`, the probe's consumer-#3 arm,
      `semiantichain_test.go`'s decline cases), then the full default-arm
      gate set — units, tpch-spotcheck, SF0.25 sweep values + plan-shape
      channel, TPC-H acceptance, SF1 timing spot-check on Q77/Q78 — and
      the fire-set gate, which M0145-0021a now enforces for this exact
      path. Expected `outer-over-derived` 3 -> 0 on the seam census.
  - **EXECUTED 2026-09-23 (loop \#5)** — the owner-GO'd removal landed:
    - `relfromjoinlist.go`: `problemPairsOuterWithDerived`,
      `derivedFirewallEnabled`, the `searchOneProblem` decline site
      (incl. `traceSeamDecline("outer-over-derived", …)`), and
      `leafIsDerivedInput` (no other reader — the `isSemiAntiSyntheticLeaf`
      flag's c8 consumer; its live reader is the c11 fillable rule) all
      deleted; `os`/`parser` imports dropped.
    - `flaglabels.go`: resolver deleted; `GOOPG_DERIVED_FIREWALL` moved to
      `flagProvenanceRetired` as `retired(M0145-0018)` per the retirement
      convention (order entry kept so older artefacts still decode);
      `scripts/planner-flags.env` regenerated.
    - Tests: `outer_over_derived_test.go` deleted wholesale (all eight
      tests pinned the removed function; its helpers had no other users);
      `semiantichain_test.go`'s three `TestProblemPairsOuterWithDerived*`
      cases, `joinsearch_c04c_trace_test.go`'s two firewall pins
      (`TestSeamBelowInnerLeftLinkOverDerivedInputsDeclines`,
      `TestProblemPairsOuterWithDerivedBelowInnerLink`),
      `joinsearch_rightlink_test.go`'s
      `TestSeamRightLinkOverDerivedInputsDeclines`, and the probe's
      consumer-\#3 arm all removed; admission/SJI/right-link pins kept.
    - Comments updated where they described the firewall as live:
      `joinsearchseam.go` (3 sites), `jointreepullup.go` (the
      PULLUP_CTE_LEAF pairing requirement — now stands alone),
      `cardinality.go` (field doc), `seam_leaf_admission_test.go` (nil
      `table` now justified by the pricing route, not the firewall),
      `pullup_cte_leaf_test.go`, `in_unnest_sjinfo_test.go`,
      `joinsearchspine_test.go`.
    - Gates (all PASS): optimizer suite; `units`; tpch-spotcheck Q12=2 /
      Q13=33; SF0.25 sweep 96/96 with the plan channel moving exactly
      Q77+Q78; seam census `outer-over-derived` **3 -> 0** on both arms;
      TPC-H acceptance 24/24 MATCH; SF1 spot-check Q77 5.76 s / Q78 29.01 s
      values identical; fire-set gate PASS (all fires, both corpora, both
      arms, `introduced=none`).
    - **The waived regression did not recur.** With the firewall gone the
      pulled CTE leaves reach DP carrying `EstimateRows` cardinalities
      (rows=12/rows=60), not the `rows=1` epsilon — so Q77's degenerate
      branch elects `Hash Left Join`, not the waived NL. Q78 keeps its
      ~29 s hash-join class at SF1.
    - **Infra note**: first fire-set run wedged in
      `jointree-parity-capture.sh` — an ~11 min clone start (concurrent
      SF1-server memory pressure) beat the 120 s readiness loop, and
      cleanup's `wait` blocked forever on the healthy server it never
      stopped. Re-ran clean. Design doc §"Execution (2026-09-23)" records it.
  Parent: M0145-0011
  - **`[!]` NO-GO 2026-09-21 — the firewall was NOT relaxed.** Design doc
    `docs/design/0100-0149/m0145-0018-firewall-relaxation-no-go.md`.
    - **Precondition 1 satisfied**: M0145-0013 landed (loop 43).
    - **Precondition 2 FAILS.** Current fire set: `outer-over-derived` 3 per
      run — Q77 (2), Q78 (1); `semianti-not-tail` now 0.
    - **SF0.25 is clean** and reproduces loop 39 exactly: only Q77/Q78 move,
      Q77 788 → 851 ms, Q78 3869 → 3931 ms, values byte-identical, Q78's
      join kinds IDENTICAL (3 Hash Anti, 2 Hash, 2 Hash Left — no NL
      election), Q77 gaining one `Nested Loop Left Join` at rows=1.
    - **SF1 is CATASTROPHIC.** Private SF=1 clone: Q78 goes **29002 ms → did
      not finish in 1800 s**, electing
      `Nested Loop Left Join (cost=5494.86..1147565.07 rows=5731)` with all
      three equi-conditions demoted to a `Join Filter` over a 10317-row
      outer and a `cs` CTE-scan inner — the C-04a shape verbatim. Q77 at SF1
      is unchanged (5680 → 5760 ms).
    - **Why this differs from loop 40's SF1 evidence** (which found Q78 stayed
      a hash join): M0145-0013, -0014 and -0016 all landed in between, each
      letting more of Q78's problem into the DP. The search now has a
      join-order choice it did not have, and takes it badly. **This is what
      the precondition existed to catch** — re-using the old numbers would
      have landed a 60x-plus SF1 regression behind a GREEN SF0.25 gate.
    - **ESCALATION**: the blocker is the **COST MODEL**, not statistics and
      not admission. The estimates are already honest (M0145-0011 measured
      that), and the search still prefers a nested loop it prices at 1.1M.
      Owner options, none of which the loop picks: (a) keep the firewall
      permanently, documented as a cost-model backstop; (b) **narrow it to
      the shape** — veto an NL path whose inner is a derived input rather
      than declining the whole problem (M0145-0011 scope (d) listed this;
      the SF1 evidence now argues for it specifically); (c) fix NL pricing
      for a derived inner and re-run this verification. Ledgered.
    - **OWNER ANSWER 2026-09-22: GO — pursue (c) via M0145-0019**
      (cost-diff recon on the instrumented PG first, then the fix or, if
      the divergence proves structural, (b) as interim). This task stays
      `[!]` until that lands and the fresh E1 re-verification passes at
      BOTH scales.
  Movement: none — no-go. `problemPairsOuterWithDerived` is untouched and
  `GOOPG_DERIVED_FIREWALL` stays default ON.

- [x] **M0145-0019 — nested-loop costing for a derived inner** (owner GO
  2026-09-22; the resolution path for M0145-0018's NO-GO — its option
  (c), with (b) as the interim fallback). 0018's fresh E1 proved the
  blocker is the COST MODEL: with the diagnostic bypass at SF1, Q78
  elects `Nested Loop Left Join (cost=5494.86..1147565.07 rows=5731)`
  over a `cs` CTE-scan inner — all three equi-conditions demoted to a
  Join Filter — and does not finish inside 1800 s, while the estimates
  are already honest (M0145-0011 measured that the decline itself
  manufactured the epsilons). The working HYPOTHESIS — to be tested,
  not assumed — is that PG plans this shape without a categorical
  guard because its NL pricing rejects the same election on the merits.
  - Step 1: capture PG's cost breakdown for the same shape on the
    instrumented tree (the M0144-0004/0005/0006 assets) and diff goopg's
    NL cost components against it — find WHERE the 1.1M estimate
    diverges from what PG would charge, or refute the hypothesis.
  - Deliverable: a design doc naming the divergence and the fix it
    implies — correct NL pricing for a derived inner (option (c), the
    faithful route) or, if the divergence is structural, narrowing the
    firewall to an NL-inner-derived veto (option (b), interim). The
    implementation is filed as the follow-on task this recon names —
    it is deliberately NOT bundled here.
  - On the follow-on's landing, re-run M0145-0018's fresh E1 (SF0.25
    AND SF1, the current `outer-over-derived` fire set — Q77/Q78 today,
    re-derive per run): the relaxation stays `[!]` until that passes.
  Kind: recon
  Parent: M0145-0018
  Movement: none — the recon refuted its nested-loop-costing hypothesis and filed M0145-0019a.
  - **DONE 2026-09-22 (loop \#79) — the hypothesis is REFUTED, and the
    divergence is somewhere else.** Movement: none.
    Design: `docs/design/0100-0149/m0145-0019-nl-costing-derived-inner.md`.
    - Reproduced Q78's election at SF1 on a private `:5561` clone, knob arm,
      `GOOPG_DERIVED_FIREWALL=off`, EXPLAIN-only — identical to 0018's record.
    - **Both candidates are generated AND accepted** (`GOOPG_PGSHAPED_DP_TRACE`
      at the final joinrel): `join.hash` startup=16457.31 total=**37659.80**
      accepted, `join.nestloop` startup=5494.86 total=**1147507.76** accepted.
      So the NL is NOT mispriced, the hash path is NOT missing, and 0018's
      "losing to nothing" reading does not hold. `add_path` keeping both is
      correct — the NL has the lower startup cost.
    - **The fault is that goopg applies the LIMIT fraction to the WRONG REL.**
      `searchCtx.finalPath` (`internal/optimizer/joinsearch.go:317`) calls
      `getCheapestFractionalPath` at the JOIN SEARCH ROOT. With
      `tupleFraction=100` over 10317 rows (f=0.009693) the fractional costs are
      hash **16662.82** vs nestloop **16564.09** — the NL wins by 0.6% and is
      then fed to a `Sort` that must consume every row, turning that 0.6% into
      30x. That is the SF1 regression.
    - **PG cannot make this election**: `planner.c:439` runs
      `get_cheapest_fractional_path` on `final_rel` (the top UPPER rel, after
      the Sort is priced); `planner.c:5314` `create_ordered_paths` takes
      `input_rel->cheapest_total_path`; `planner.c:7646`+ `make_ordered_path`
      returns NULL for any unsorted input that is not cheapest-total. Upstream
      sorts the cheapest-TOTAL path and asks the fraction afterwards.
    - `finalPath`'s own doc comment cites `planner.c:437` for this call — the
      **citation is right and the rel is wrong**.
    - **This closes both of 0018's remaining options**: (c) has nothing to fix
      (a change to NL pricing here would be tuning toward a plan, R6), and (b)
      would suppress the symptom while leaving the rule — any query whose LIMIT
      fraction is resolved before a mandatory Sort can elect a low-startup path
      the same way, with no derived input in it at all.
    - Deliverable filed below as **M0145-0019a** (S5: it names its expected
      movement and how it is measured).

- [x] **M0145-0019a — apply the LIMIT fraction at the final upper rel, not at
  the join search root** (the fix M0145-0019's recon names).
  Kind: impl
  Parent: M0145-0019
  - Move `getCheapestFractionalPath` off `searchCtx.finalPath`
    (`internal/optimizer/joinsearch.go:317`) to the top of the upper-rel
    lattice, so it sees the Sort; feed the ordered step from the join rel's
    cheapest-TOTAL path, as `create_ordered_paths` does
    (`planner.c:5314`, `planner.c:7646`+, `planner.c:439`).
  - **Expected movement, and how it is measured** (S5): Q78 at SF1 elects the
    `Hash Left Join` (total 37659.80) instead of the `Nested Loop Left Join`
    (total 1147507.76) with `GOOPG_DERIVED_FIREWALL=off`, measured by the
    EXPLAIN plan plus the `DPPATH` verdict at the final joinrel. Corpus-level:
    report `CATEGORIES-EXCL-MATCH` on both corpora — the placement has been
    live since M0127-P5.9 (2026-08-06), so movement in either direction is
    expected and neither is assumed.
  - **Blast radius — do NOT treat this as a Q78 fix.** Every `ORDER BY … LIMIT`
    query over a join whose pathlist holds a low-startup/high-total path can
    change plan. Full value-gate set plus a plan-parity capture of its own.
  - On landing, re-run M0145-0018's fresh E1 at SF0.25 AND SF1 against the
    then-current `outer-over-derived` fire set (re-derive per run, do not reuse
    Q77/Q78) — M0145-0018 stays `[!]` until that passes.
  - **LANDED 2026-09-22 (loop \#80).** Movement: none —
    SF0.25 `CATEGORIES-EXCL-MATCH parameterisation` 55 -> 54 (one fewer
    divergence, inside the ±3 band); TPC-H categories byte-identical.
    Design: `docs/design/0100-0149/m0145-0019a-limit-fraction-ordering-gate.md`.
    - `getCheapestFractionalPathOrdered` reproduces upstream's asymmetry at
      goopg's one seam: with an ordering requested, the fraction may only be
      won by a path that ALREADY satisfies it; when none does the answer is
      `CheapestTotal`, which is the path `create_ordered_paths` would sort
      (`planner.c:5314`, `:7646`+, already-sorted arm `:5337`+). With no
      ordering requested it is the old function unchanged.
    - `queryPathkeys` was already on `searchCtx` (PG's `root->query_pathkeys`),
      so the rule needed no new plumbing.
    - **The literal deliverable ("move the call to the upper-rel lattice") is
      NOT expressible today** and the doc records why: `createOrderedPaths`
      takes an already-lowered `Node`, not the join rel's pathlist, so there is
      no pathlist up there to choose among — that is what **M0145-0007**
      (single Path→Node lowering) exists to change. Reproducing the RULE at the
      existing seam reaches the same answer without pre-empting 0007, and the
      seam disappears with it.
    - **Expected movement met**: Q78 at SF1 (private `:5562`, knob arm,
      firewall off) goes `Nested Loop Left Join (cost=5494.86..1147565.07)` ->
      `Hash Left Join (cost=16457.31..37717.11)`; the `Limit` estimate drops
      `1147784.10..1147799.43` -> **`37936.15..37951.48`**, and the three
      equi-conditions demoted to a `Join Filter` are join conditions again.
    - Non-vacuity checked: neutralising the ordering gate fails exactly the two
      arms that test it and leaves the control green. The control is
      load-bearing — `LIMIT` with no `ORDER BY` must STILL pick the fast-start
      path, or the change would be "ignore the fraction whenever there is an
      ORDER BY", which is a different and wrong change. A third arm pins that a
      path sorted the WRONG way does not win, so the gate cannot be reading
      `len(Pathkeys) > 0`.
    - **Plans moved, as this task predicted.** SF0.25: 6 shapes changed
      (Q6, Q8, Q35, Q43, Q44, Q69), values identical
      (`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
      `verdict-changes=none runtime-moves=0 total-delta=+1.6%`). TPC-H: shapes
      byte-identical, costs slightly lower (Q1 148207.10 -> 147979.67).
    - **Floors held exactly**, and the figure matters: TPC-H parallel
      `match=1` (Q6) is the floor **M0144-0001 re-pinned**, not AGENT.md's
      older `>= 3` P0-E7 number — checking the re-pin is what kept this loop
      from filing a false floor-breach escalation. TPC-DS SF0.25 `match=2`
      (Q9, Q41).
    - Gates: units; tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 sweep; TPC-H
      acceptance arm 24 MATCH; both parity captures taken with AND without the
      change (G7 discipline); pgbench smoke.

- [x] **M0145-0020 — port `examine_simple_variable`'s non-recursive CTE
  arm** (`postgres/src/backend/utils/adt/selfuncs.c:5737-5912`) (owner GO
  2026-09-22; M0145-0012's named prerequisite). PG resolves a qual on a
  CTE output column to the underlying base column's real statistics by
  locating the CTE's subroot via `cte_plan_ids` and recursing on the
  target-list `Var`; goopg has no such lookup, so quals on multi-ref CTE
  outputs (`ws` x3, `inv` x2, `year_total` x4 — the Q31/Q39/Q74 fires)
  collapse to the default selectivity and only the goopg-only `rows<=1`
  fallback catches the resulting epsilon-NL elections. Scope: wire the
  subroot/Var resolution for NON-RECURSIVE CTEs only (the upstream arm
  punts on shapes it cannot resolve — mirror that). Success test: the
  three corpus fires estimate >= the fallback's effect WITHOUT the arm —
  then M0145-0012 resumes to remove it.
  Kind: impl
  Parent: M0145-0012
  - **CLOSED measured-no-gap 2026-09-22 (loop \#81) — the premise is
    REFUTED, twice, and no code was needed to answer it.** Movement: none.
    Design: `docs/design/0100-0149/m0145-0020-examine-simple-variable-cte-arm.md`.
    - **(1) Upstream punts on all three named fires before reaching any
      statistics.** `examine_simple_variable`'s CTE arm has three exits ahead
      of the recursion and each CTE hits one:
      - `selfuncs.c:5843` `if (subquery->setOperations || subquery->groupingSets) return;`
        — **Q74 `year_total`** is a `UNION ALL` of two grouped selects.
      - `selfuncs.c:5876-5883` `if (subquery->groupClause) { … return; }`,
        which sets `isunique` ONLY for a single grouping column —
        **Q31 `ws`** groups by 3 (`ca_county, d_qoy, d_year`).
      - the same exit one level down — **Q39 `inv`**'s body is
        `select … from (… group by w_warehouse_name, w_warehouse_sk,
        i_item_sk, d_moy) foo`, so the recursion lands on the grouped
        subquery and punts there.
      So a FAITHFUL port returns no statistics for exactly the columns the
      task wants them for; the success test is unattainable by this route.
      Same conclusion M0145-0009's census reached on the ndistinct channel,
      arrived at independently on the selectivity channel.
    - goopg already reaches that same answer by a different route:
      `resolveBaseColumn` (`internal/optimizer/joinkeyproof.go`) has a
      `*CTEScan` arm that recurses into the body and stops at the same
      `Aggregate`/set-op boundary. The two engines agree; there is nothing
      to port.
    - **(2) The measured cause of Q39's collapse is somewhere else.** PG does
      NOT collapse this CTE scan. Instrumented SF0.25 cluster vs goopg's own
      SF0.25 capture:
      - HashAggregate INPUT: goopg 11703, PG 11606 (agree within 1%);
      - HashAggregate OUTPUT after the `cov > 1` HAVING: goopg **20**,
        PG **3869** (PG = `11606 × 0.3333` — every input row its own group,
        then `DEFAULT_INEQ_SEL`);
      - `CTE Scan on inv` after `d_moy = 1`: goopg 1, PG **19**.
      The CTE-output qual gets the SAME default 0.005 in both engines, so the
      qual's statistics are not the problem: 3869 × 0.005 = 19 survives,
      20 × 0.005 = 0.1 clamps to 1. **The collapse is the body estimate.**
    - **ESCALATION — the owner's sequencing assumption does not hold.** The
      2026-09-22 GO filed this as M0145-0012's prerequisite. It is not one:
      0012's `rows<=1` fallback is load-bearing because the grouped-output
      estimate under-shoots ~193x, which is filed below as **M0145-0020a**.
      The loop is not re-ordering the banner; recording the evidence so the
      owner can.

- [x] **M0145-0020a — the grouped-output cardinality under-shoots ~193x**
  (the real prerequisite M0145-0020's measurement named).
  Kind: impl
  Parent: M0145-0020
  - Q39's `inv` CTE, SF0.25: same input (goopg 11703 / PG 11606), grouped
    output goopg **20** vs PG **3869** over the 4-key
    `group by w_warehouse_name, w_warehouse_sk, i_item_sk, d_moy` plus a
    `CASE … > 1` HAVING.
  - PG's number decomposes cleanly and is the MORE conservative one:
    `estimate_num_groups` capped at the input (every row its own group) then
    `DEFAULT_INEQ_SEL` (0.3333) for the HAVING. Establish which of the two
    factors goopg gets wrong BEFORE changing either — the 193x could be the
    group count, the HAVING selectivity, or both, and this loop did not
    separate them.
  - **Expected movement, and how it is measured** (S5): Q39's
    `CTE Scan on inv` estimates ~19 instead of 1 with
    `GOOPG_CTE_ROWS_FALLBACK=off`, measured by EXPLAIN; then re-run
    M0145-0012's arm-ON/arm-OFF plan A/B over all 99 queries and report
    whether Q31/Q39/Q74 still move into the collapse class. Corpus level:
    `CATEGORIES-EXCL-MATCH` on both corpora.
  - This is what unblocks **M0145-0012**, not M0145-0020.

  - **LANDED 2026\-09\-22 \(loop \#1\)** as `4c6f7ab8d`.
    Movement: yes — ea-ratchet 54 -> 53 findings (3 FIXED, 2 NEW).
    Node level: Q39's
    `CTE Scan on inv` 1 -> 19 and its aggregate 20 -> 3901 at SF0.25, against
    PG's 19 / 3869 from the same input, measured by EXPLAIN.
    Design: `docs/design/0100-0149/m0145-0020a-grouped-output-cardinality.md`.
    - **The Q9 acceptance failure that blocked this task for three loops was an
      ARM-CONFIGURATION ARTIFACT, not a pre-existing red gate.**
      `scripts/tpch-acceptance-arm.sh` defaults `PGSHAPED=0`
      \(`GOOPG_PGSHAPED_DP=0`\), which is NOT the shipped planner
      configuration. Q9 cannot finish in 600 s on the legacy search; on the
      default it takes 2.8 s. Re-run with `PGSHAPED=1` against the loop\-77
      baseline: `SUMMARY: 24 MATCH / VERDICT: PASS`. The same artifact is
      recorded against M0145\-0004 in the ledger \(2026\-09\-22\) — any loop
      that hits `1 BOTH-ERROR, 23 MATCH` should check the arm's knob state
      before spending a loop proving the gate red at clean HEAD.
      **Owner call 2026\-09\-22: the arm's default is now `PGSHAPED=1`**
      \(the shipped configuration\); the explicit\-pin guidance below stands
      for A/B comparability.
    - **The gates were run on a tree carrying ONLY this change.** ~1,200 lines
      of unrelated WIP were parked in a `git stash` first, so all three stamps
      share one `code_tree` and the commit is what was measured. Before that,
      `tpch-spotcheck` stamped FAIL purely because the built tree was not the
      index.
    - Gates: tpch\-spotcheck PASS \(Q12=2 Q13=33\); tpcds\-sf025 sweep
      `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
      `PLAN-SHAPE same=99 changed=0`; tpch\-acceptance\-arm PASS 24 MATCH;
      floors held \(TPC\-DS SF0.25 `match=2`, TPC\-H parallel `match=1`\);
      pgbench smoke. `CATEGORIES-EXCL-MATCH: join-order=91 join-method=66
      scan-type=61 parameterisation=54 aggregation-strategy=44
      sort-strategy=61 parallelism=85 qual-placement=27 rendering=26`.
    - `make ea-ratchet` is FAIL and the clean\-tree number is far smaller than
      the contaminated 61: `baseline findings: 54 current: 53`, 3 FIXED
      \(Q62, Q80, Q99\) and 2 NEW — filed as **M0145-0020c**.
    - This does NOT by itself unblock M0145\-0012: the arm\-ON/arm\-OFF plan
      A/B over all 99 queries that 0012 needs is still unrun, and Q74 still
      moved into the collapse class in the earlier A/B.

- [x] **M0145-0020c — the 2 NEW ea-ratchet findings the grouped-output repair
  introduced** — REFUTED: it introduced neither.
  Kind: recon
  Parent: M0145-0020a
  - `make ea-ratchet` on the committed change: `baseline findings: 54
    current: 53`; FIXED Q62, Q80, Q99; **NEW** `Q44:item+ss1`
    \(`Hash Join`, est 1831 vs actual 10, qerr 183.1\) and
    `Q83:cte:sr_items+cte:wr_items` \(`CTE Scan on wr_items`, est 4 vs
    actual 171, qerr 42.8\).
  - Both relsets are grouped/CTE\-derived, i.e. exactly the population whose
    estimate this task moved, so they are attributed to the change rather
    than claimed pre\-existing. Confirm that attribution per finding against
    clean HEAD before proposing a fix — a larger grouped\-output estimate is
    the intended direction, and Q44's 183x over\-shoot may be a second
    factor \(the group count vs the join selectivity\) rather than this term.
  - Resume point: `internal/optimizer/cardinality.go` `estimateNumGroups` /
    `relFilteredRowsWalk`, plus `scripts/estimate-parity-gate.sh --json` for
    the per\-node evidence.
  - Ledger row: `.ralph/deferral_ledger.md`, 2026\-09\-22, M0145\-0020a.

  - **CLOSED measured\-no\-gap 2026\-09\-22 \(loop \#2\).**
    Movement: none — recon, no production change.
    Design: `docs/design/0100-0149/m0145-0020c-ea-new-findings-attribution.md`.
    - **The A/B settles it in one environment**, which every earlier ea number
      lacked. Reverting exactly the committed two production files and re\-running
      the gate: WITHOUT the change **56 findings including both NEW ids**; WITH it
      \(= HEAD\) **53**, same two NEW, plus 3 FIXED \(Q62, Q80, Q99\). The change
      introduced zero findings and removed three.
    - **The earlier contradictory totals are explained**: M0145\-0020b's
      clean\-HEAD run ratcheted against the **20260907** baseline \(178 findings\)
      while `make ea-ratchet`'s default is the **20260915** one \(54\). "61 NEW"
      and "17 NEW" were never comparable to each other or to the gate. Both ids
      do appear in 0020b's clean\-HEAD NEW list, which corroborates the A/B.
    - **Both findings are `pg_est=null` \(UNMATCHED\-IN\-PG\)** — the scorer found
      no PG node with that relset key, so each says "goopg formed a relset PG's
      plan does not contain", not "goopg estimates worse than PG".
      - Q83 `cte:wr_items`: PG **inlines** the CTE \(no `CTE Scan` in
        `bench/tpcds/plans-pg/Q83.txt`\) and estimates the same branch at
        `rows=5` against the same actual 171 — PG is wrong in the same
        direction. Chasing it would tune toward PG's own wrong number.
      - Q44 `item+ss1`: PG never forms this relset \(it joins `item` last through
        two `Nested Loop`s\). The 183x is real but priced on a join order PG does
        not use, so the divergence is join ORDER, not this estimate.
    - **Residual, filed as M0145-0020d**: both keys post\-date the 2026\-09\-15
      baseline, so plan churn made the baseline stale; G4 forbids a repin in a
      code commit and the loop may not repin on its own judgement, so the gate
      is red with no sanctioned route back to green.

- [x] **M0145-0020d — the ea-ratchet baseline is stale for two goopg-only
  relsets, and nothing may repin it** \(owner decision needed\).
  Kind: recon
  Parent: M0145-0020c
  - **OWNER ANSWER 2026-09-22: (b) — G4 widened.** AGENT.md G4 now permits
    a repin — still only as a standalone non-code commit — when the
    baseline went stale through goopg's own plan churn and the
    NEW-vs-baseline diff consists solely of UNMATCHED-IN-PG keys, with
    the attribution A/B attached. M0145-0020c's clean-HEAD A/B is exactly
    that evidence, so the repin runs now under the widened rule: fresh
    baseline at
    `analysis/planner-refactor-take3/c20a-estimator-census-20260922/`,
    `scripts/estimate-parity-gate.sh`'s `EA_BASELINE` default repointed
    (the baseline default lives there, not in the Makefile), the 20260915
    baseline left in place for archaeology. The interim "own A/B shows no NEW"
    landing discipline is retired — the gate is green again by baseline.
    Movement: none — a baseline/policy record, no production change.
  - `make ea-ratchet` is FAIL for every task from now on with the same two NEW
    ids \(`Q44:item+ss1`, `Q83:cte:sr_items+cte:wr_items`\), neither of which any
    current task caused: they entered by plan churn after the 2026\-09\-15
    baseline capture, and M0145\-0020c showed both are UNMATCHED\-IN\-PG keys
    rather than estimator regressions.
  - The rule gap: AGENT.md G4 permits `make ea-ratchet-repin` **only** when the
    corpus or dataset changes, never in a code commit — so a baseline made
    stale by goopg's own plan churn has no path back to green, and each later
    task inherits a red gate it did not cause and may not clear.
  - Needs an owner call on ONE of: \(a\) repin the baseline now as a standalone
    non\-code commit; \(b\) widen G4 to allow a repin whose diff is only
    UNMATCHED\-IN\-PG keys, with the evidence attached; \(c\) leave it red and
    treat "no NEW id beyond this named set" as the pass condition.
  - Until then, a task may land on the FAIL as long as its own A/B shows it
    introduced no finding — the discipline M0145\-0020c established.
  - Ledger row: `.ralph/deferral_ledger.md`, 2026\-09\-22, M0145\-0020c.

- [x] **M0145-0021 — harness: SF1 fire-set gate for diagnostic-flag /
  estimation / cost-model tasks** (owner GO 2026-09-22; progress-doc
  proposal 5.4). M0145-0018 showed an SF0.25-green result masking a
  60x-plus SF1 regression — caught only because the task TEXT mandated a
  fresh E1, not because the harness enforced it. Land a reusable gate
  template: any task touching the derived-input firewall, row
  estimation, or the cost model must A/B the current fire set
  (Q77/Q78 today; re-derive per run from the seam census) at SF0.25 AND
  SF1, with "no timeout-class increase" as a mechanical pass condition.
  Extend the SF0.25 regression script or a sibling; do NOT weaken
  existing gates.
  Kind: impl
  Parent: none
  - **Slice 1 LANDED 2026-09-22 (loop \#90): the fire-set derivation is now
    trustworthy.** Movement: none — a harness fix.
    Design: `docs/design/0100-0149/m0145-0021-fireset-derivation.md`.
    - **Why this slice first**: the task requires re-deriving the fire set
      per run. Loop \#82 did it by hand and found the obvious route does not
      work — the seam census's server log carries NO query markers, so
      declines cannot be attributed to queries. The derivation that works is
      a plan A/B, which makes `tpcds-plan-diff.py` the gate's load-bearing
      component. It had two defects.
    - **Defect 1 — the harness has TWO capture formats and the tool knew
      one**: `===== Qn =====` (the SF0.25 sweep) and `=== Qn`
      (`jointree-parity-capture.sh`, i.e. every firewall/knob A/B). Pointing
      it at a jointree capture printed
      `PLAN-SHAPE: queries=0 same=0 changed=0` — a **vacuous pass
      indistinguishable from "the plans agree"**. That is why loops \#82 and
      \#84 each hand-rolled a diff and were then bitten by psql-path noise
      this tool already normalises (both "found" Q36/Q70/Q86 changed when
      only the temp FILENAME inside their parse errors differed).
    - **Defect 2 — a capture parsing to zero blocks reported success.** Now
      fatal: `FATAL — <path> parsed 0 query blocks`, exit **2** (distinct
      from `--strict`'s 1). A capture with no blocks is a broken capture,
      never a clean result.
    - **Measured payoff** — re-run over loop \#82's firewall A/B captures,
      which the tool previously could not read at all:
      `changed (2): Q77 Q78` (the three parse-error queries correctly
      filtered) and `JOIN-METHOD-ELECTION: moved=1 into-nestloop=1
      suspects=Q77`. Both of that loop's hand-derived findings now fall out
      automatically, including the criterion-1 violation that stopped the
      firewall relaxation.
    - Tests: 3 new cases in `scripts/tpcds-plan-diff-test.py` (both header
      formats parse; a zero-block capture is fatal). Non-vacuity checked —
      reverting either change fails exactly the tests that assert it.
  - **Loop \#91 implementation in progress**: `tpcds-fireset-gate.sh` now
    derives each corpus fire set from fresh baseline/candidate captures and
    executes it in separate private clone arms. `jointree-parity-capture.sh`
    records normalized statuses plus raw results and restarts only its private
    clone after a timeout, so the comparator cannot accept a contaminated
    follow-up result. The remaining acceptance evidence is the actual two-scale
    SF0.25 and SF1 run.
    - **Loop \#92 smoke evidence**: the capture-skipping execution arm ran Q2
      on isolated SF0.25 baseline and candidate clones. Both records were PASS;
      the comparator reported no introduced, unchanged, or missing timeout.
    - **Loop \#93 resumability evidence**: `FIRESET_RESUME=1` plus
      `FIRESET_BATCH_SIZE=1` preserved the derived 24-query SF0.25 set and
      appended Q2 PASS in both arms. The comparator rejected the other 23 as
      missing, as required. A deterministic short clone tag fixes the Unix
      control-socket path limit exposed by the long generated execute label.
    - **Loop \#94 continuation**: Q5 and Q6 each completed PASS in both
      private arms at the 600-second policy. The comparator still rejected the
      remaining 21 missing IDs, preserving the no-vacuous-pass contract.
    - **Loop \#95 continuation**: Q8 completed PASS in both private SF0.25
      arms (25.8 seconds wall time). The derived set remains 24 IDs; the
      comparator rejected the remaining 20 missing IDs, so this partial batch
      cannot be mistaken for an acceptance result.
    - **Loop \#96 continuation**: Q9 completed PASS in both private SF0.25
      arms (21.2 seconds wall time). The comparator rejected the remaining 19
      missing IDs, preserving the no-vacuous-pass contract.
    - **Loop \#97 continuation**: Q10 completed PASS in both private SF0.25
      arms (21.2 seconds wall time). The comparator rejected the remaining 18
      missing IDs, preserving the no-vacuous-pass contract.
    - **Loop \#98 continuation**: Q14 completed PASS in both private SF0.25
      arms. The command's first output was a foreground yield while the
      candidate remained under its 600-second policy; waiting for that same
      process confirmed the candidate PASS record. The comparator now rejects
      the remaining 17 missing IDs.
    - **Loop \#99 continuation**: Q16 completed PASS in both private SF0.25
      arms (20.1 seconds wall time). The comparator rejected the remaining 16
      missing IDs, preserving the no-vacuous-pass contract.
    - Gate: the real SF0.25 sweep still runs clean (`PASS=96 MISMATCH=0
      CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `PLAN-SHAPE same=99 changed=0`).
    - **COMPLETE 2026\-09\-22 \(loop \#1\) — the two\-scale acceptance run is
      green at BOTH scales.** Movement: none — a harness gate.
      - SF0.25: all 24 derived fires PASS in both private arms;
        `FIRE\-SET\-TIMEOUTS: introduced=none unchanged=none missing=none`,
        exit 0. Evidence `tmp/m0145\-0021\-loop91/tpcds\-sf025/`.
      - SF1: the plan A/B derived the SAME 24 IDs; all 24 PASS in both arms;
        same verdict, exit 0. Evidence `tmp/m0145\-0021\-sf1/tpcds\-sf1/`.
      - **The SF1 half was NOT "the bulk of the work"** — that estimate was
        wrong. The SF1 capture of both arms \(two 3.3 GB offline clones \+ the
        PG reference arm, 99 EXPLAINs each\) took **89 seconds**; the 24 fires
        ran in three batches of 1m59s–3m59s. Under 15 minutes total.
      - **What actually cost eight loops was `FIRESET_BATCH_SIZE=1`.** The
        execution arm starts its clone server ONCE per invocation and then
        walks every id in `FIRESET_QUERIES`, so a batch of 8 pays one
        clone\+start for 8 queries. Loops \#92–\#99 each used batch size 1 and
        so advanced exactly one id per loop. Bound the batch to whatever the
        foreground interval allows — never to 1 out of caution.
      - **SF1 derived the same fire set as SF0.25**, so the two\-scale
        requirement is about the scale\-sensitive TIMEOUT CLASS, not about a
        different set per scale.
      - Non\-vacuity of the pass condition checked directly: baseline PASS \+
        candidate TIMEOUT → `introduced=Q2`, exit 1; TIMEOUT in both →
        `unchanged=Q2`, exit 0; an 8\-of\-24 batch → `missing=<16 ids>`,
        nonzero. `tpcds\-fireset\-status.py \-\-self\-test` 1/1;
        `tpcds\-plan\-diff\-test.py` 10 cases OK.
      - SF1 arm parity numbers, recorded because the gate is NOT a parity
        claim: baseline `match=1 shapediff=73`, candidate `match=0
        shapediff=74` vs PG 18.3. The gate asserts no timeout\-class increase
        only.
      - Usage: `CANDIDATE_ENV_FILE=… scripts/tpcds\-fireset\-gate.sh <id>
        tmp/<id>` — `CORPORA` already defaults to both corpora, so the
        default invocation IS the two\-scale gate.

- [x] **M0145-0021a — make the fire-set gate ENFORCED, not merely
  available**
  Kind: impl
  Parent: M0145-0021
  M0145-0021 landed the two-scale gate and proved it non-vacuous, but
  nothing makes a task touching the derived-input firewall, row estimation
  or the cost model run it. M0145-0018's exposure is therefore reduced to a
  documented procedure, not a mechanical barrier.
  - Resume point: the plan-shape PIN's acceptance-line pattern
    \(`SF025_PLANS_BASELINE`\) is the precedent — require a fire-set verdict
    file for commits touching the cost/estimation/firewall paths.
  - **Needs an owner scope call first**: which paths are in scope is a policy
    decision, and getting it wrong either blocks unrelated commits or trains
    the loop to stamp its own verdict.
  - Ledger row: `.ralph/deferral_ledger.md`, 2026-09-22, M0145-0021, gap \(a\).
  - **OWNER SCOPE CALL 2026-09-22 (delegated; progress-report §3.5),
    LANDED.** The stamp-and-exception machinery (G2) is extended rather
    than a parallel mechanism:
    - **Scope** — a commit needs a `tmp/gate-stamps/tpcds-fireset.json`
      PASS when it stages a non-test `.go` under `internal/optimizer/` or
      `internal/planner/`, or any non-test `.go` under `internal/`/`cmd/`
      whose path matches `*cost*|*stat*|*selfuncs*`. `internal/executor/`
      is deliberately OUT: it cannot move plan election, and its timeout
      class is already covered by the sf025 sweep's own TIMEOUT counter.
      `scripts/fireset-scope.sh` carries the same predicate for
      pre-commit checks.
    - **Level** — commit-msg, not task-template: the task's own resume
      point imagined the verdict requirement at commit time, the stamp
      contract (`code_tree` of the index + gate-exceptions escape hatch)
      already exists, and the gate writes the verdict itself, so there is
      no self-attestation hole the task-template alternative was meant to
      avoid.
    - **Corpora** — `CORPORA` default stays `tpcds-sf025 tpcds-sf1`;
      `tpch` remains OPT-IN (its fires execute at SF1 through the
      acceptance arm, and an enforced gate must stay cheap enough that
      tasks actually run it — the measured two-scale TPC-DS run is
      ~15 min; revisit once TPC-H fire runs are routine).
    - Mechanism: `tpcds-fireset-gate.sh` writes the `tpcds-fireset`
      stamp from its EXIT trap (0 → PASS, capture preflight rc 3 →
      SKIP-BLOCKED, else FAIL); `commit-msg` requires it for the scope
      above and `gate-exceptions.md` covers it like the other row gates.
    Movement: none — a harness/policy change, no production code.

- [x] **M0145-0021b — extend the fire-set gate to the TPC-H corpus**.
  Kind: impl
  Parent: M0145-0021
  `scripts/jointree-parity-capture.sh`'s `tpch` branch returns BEFORE the
  `FIRESET_QUERIES` execution block, so the template covers the TPC-DS
  corpora only — while the TPC-H floor pin is where M0144-0001 measures
  parity.
  - Resume point: move the `FIRESET_QUERIES` block above the corpus branch,
    or duplicate it into the `tpch` arm using
    `tpch-estimate-audit-arm.sh`'s private clone; then extend the gate's
    `CORPORA` validation to accept `tpch`.
  - Out of M0145-0021's stated scope \(its fire set is re-derived from the
    TPC-DS seam census\); the TPC-H arm has its own clone lifecycle.
  - Ledger row: `.ralph/deferral_ledger.md`, 2026-09-22, M0145-0021, gap \(b\).
  - **LANDED 2026\-09\-22 \(loop \#6\).** Movement: none — harness.
    Design: `docs/design/0100-0149/m0145-0021b-tpch-fireset-lane.md`.
    - **The corpus extension is the SMALLER half of what this found.** A gate
      that derives its fire set from a plan A/B needs a zero noise floor, so
      the A/A was measured first: two back\-to\-back captures, same binary,
      same arm, nothing changed between them, reported
      `queries=21 same=1 changed=20`. 20 queries differed in cost/rows only;
      **Q3 flipped SHAPE** \(`GroupAggregate`/`Gather Merge` ->
      `HashAggregate`/`Gather`\), and the CATEGORIES counts moved with it
      \(`aggregation-strategy` 6 vs 5, `sort-strategy` 10 vs 9\).
    - Cause: `scripts/tpch-estimate-audit-arm.sh` never exported
      `GOOPG_ANALYZE_SEED`. `tpch-acceptance-arm.sh` pins it and its header
      states the reason \(per\-connection sampled statistics; measured A/A
      noise LARGER than most A/B signals\); the TPC\-DS lanes get theirs from
      `env_tpcds.sh`. Only the TPC\-H **plan\-parity** lane — the canonical
      M0144\-0001 arm — was unpinned. One export fixes it; the A/A re\-run is
      `queries=22 same=22 changed=0`.
    - **Consequence to carry**: every TPC\-H `CATEGORIES-EXCL-MATCH`
      comparison taken on that lane before this commit carried the noise, so a
      ±1 category delta there was not necessarily a signal. Captures from
      before and after the pin are not comparable — re\-take a baseline rather
      than diffing across it.
    - Second defect fixed: `tpcds-plan-diff.py`'s block regex stopped at the
      digits, so the TPC\-H capture's `=== Q15a-VIEWBODY` header matched
      nothing and its plan was appended to **Q14's** block — Q15 absent from
      every comparison, Q14 compared against a body that is not Q14's. Sub
      \-labelled blocks now fold onto their numeric id with a marker line, so
      a renamed or reordered sub\-block is still a difference
      \(`queries=21` -> `22`\). TPC\-DS has no sub\-labels: unchanged there.
    - The extension: TPC\-H has no query `.sql` files \(the bank is
      `cmd/tpch-runner`\), so the ids execute through
      `tpch-acceptance-arm.sh QUERIES=…` and `scripts/tpch-fireset-parse.py`
      maps its per\-query lines onto `PASS|TIMEOUT|ERROR`. SQLSTATE **57014**
      is the timeout class — calling it `ERROR` would hide exactly what the
      gate watches for, and calling every ERROR a timeout would invent them.
    - Two deliberate defaults: `PGSHAPED=1` \(the shipped planner, not the arm
      script's own `0` — the false\-red trap\), and `GATE_STAMP_DIR` redirected
      into the run's own directory so a subset run's `NO-COMPARE` can never
      overwrite the real acceptance\-arm verdict \(verified byte\-identical
      across a run\).
    - `tpch` is supported but NOT a `CORPORA` default: its fires execute at
      SF1, so it is opt\-in.
    - Tests: `tpch-fireset-parse.py --self-test` 5/5; `tpcds-plan-diff-test.py`
      12 cases \(2 new\). Non\-vacuity checked — neutralising the sub\-label
      match fails exactly those two and leaves the other ten green. Real\-run
      evidence: `Q12 PASS` at `GOOPG_PGSHAPED_DP=1`, and `Q12 TIMEOUT` from a
      genuine 57014 cancellation at `FIRESET_TIMEOUT=1`.

- [x] **M0145-0022 — harness: plan-shape election + wall-clock
  regression channel on the SF0.25 sweep** (owner GO 2026-09-22;
  proposal 5.5). M0145-0012/0018 both produced plans whose values were
  verified byte-identical AT SF0.25 while shape and clock regress
  (Q74 16.6x there; at SF1 Q78 did not finish, so no value comparison
  exists at that scale) — a class invisible to every value gate. Extend the sweep's existing plan-shape
  channel to classify join-method elections (flag any move INTO Nested
  Loop as a suspect direction — that is how C-04a manifests) and add a
  wall-clock threshold report (e.g. >2x worse flagged). Report-only vs
  gate-deciding is an implementer call; start report-only and promote
  after one clean corpus cycle.
  Kind: impl
  Parent: none
  - **LANDED report-only 2026-09-22 (loop \#89).** Movement: none — a
    harness channel; it reports, it does not plan.
    Design: `docs/design/0100-0149/m0145-0022-join-method-election-channel.md`.
    - **Half the task already existed, and checking first is the finding**:
      the wall-clock threshold report is already in
      `scripts/tpcds-sweep-diff.py` — `STATUS-DELTA … runtime-moves=N …
      total-delta=±X%` with a >=2.0x per-query threshold, a 5 s floor and
      TIMEOUT readings excluded. Only the election classification was
      missing.
    - `scripts/tpcds-plan-diff.py` now counts join nodes per family
      (nestloop / hash / merge) over the queries the shape diff already
      found changed, prints only the DELTA, and names a move **into**
      Nested Loop as a suspect — C-04a's signature is an equi-join demoted
      to a `Join Filter` on a nested loop priced off an epsilon estimate.
      Moves out of it and merge/hash exchanges are reported without
      judgement.
    - **Report-only by construction**: nothing touches the exit status,
      which stays `--strict`'s alone.
    - **It retro-validates M0145-0019a.** Over the capture pair bracketing
      the LIMIT-fraction ordering gate: `moved=4 into-nestloop=0`, with
      Q6/Q8/Q43/Q44 each `hash+N nestloop-N` — exactly the direction that
      fix should produce, confirmed independently of the reasoning behind
      it. Against an 11-day-old baseline the same channel reports
      `moved=43 into-nestloop=25`, so it is not simply reporting zero.
    - Tests: `scripts/tpcds-plan-diff-test.py` (7 cases) covers the
      node-name matching — `Parallel Hash Join` must count once as hash and
      not also as the `Parallel Hash` build node, `Nested Loop Left Join` is
      still a nested loop — both directions of the suspect call, and that a
      cost-only change is not listed. Non-vacuity checked.
    - **A trap the first draft fell into, recorded because this repo has hit
      it before**: the fixtures used `=== Q1` where captures write
      `===== Q1 =====`, so the tool parsed ZERO blocks and three of seven
      tests passed VACUOUSLY. Same off-by-two as the awk range in the
      set-op common-type work. The fixture writer now carries a comment.
    - Gate: the real SF0.25 sweep runs clean with the channel in
      (`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
      `PLAN-SHAPE same=99 changed=0`), and the channel is correctly silent
      when nothing moved.

- [x] **M0145-0023 — harness: flow-convergence instrument (secondary
  metric, NOT a movement instrument)** (owner GO 2026-09-22; proposal
  5.6). The S3 instruments measure final parity only, so M0145-style
  multi-task structural migrations record `Movement: none` even when the
  internal flow measurably converges — five consecutive such records
  tripped the M0145-0001 lineage guard. Aggregate per sweep: the
  pinned-spine vs jointree-pullup route ratio (SUBLINKCENSUS) and the
  seam-decline bucket counts, appended as a trend log so convergence is
  visible without counting as Movement. Deliberately NOT added to the
  lineage-guard instruments — the guard's strictness is working; this is
  observability only.
  Kind: impl
  Parent: none
  Movement: none — report\-only observability; it is deliberately not a lineage movement instrument.
  - **LANDED 2026\-09\-22 \(loop \#1\).** Design:
    `docs/design/0100\-0149/m0145\-0023\-flow\-convergence\-instrument.md`.
    - The fresh SF0.25 EXPLAIN tail now enables the existing route and seam
      diagnostics only for its own server instance, extracts its exact new log
      range, and leaves the value and plan\-pin verdicts unchanged.
    - `scripts/flow\-convergence.py` requires non\-empty `SUBLINKCENSUS`,
      reports pinned\-spine versus jointree\-pullup plus every seam\-decline
      bucket, and appends a tab\-separated row to `flow\-convergence.tsv`.
      A zero\-event capture is a report failure, never a vacuous zero ratio.
    - Tests: `flow\-convergence.py \-\-self\-test`; `bash \-n
      scripts/tpcds\-sf025\-regression.sh`; full SF0.25 sweep follows as the
      integration gate.
  - **FOLLOW-UP 2026\-09\-22 (progress-report §5.3, knob-arm lane landed).**
    The instrument was structurally blind: every TSV row read
    `jointree-pullup=0` because the sweep only ever ran the default arm —
    the route ratio that shows convergence exists only on the knob arm.
    `sf025_plan_channel` now runs a second EXPLAIN-only pass under
    `GOOPG_JOINTREE_PIPELINE=1` (off-switch `SF025_FLOW_KNOB=0`), feeding
    `flow-convergence.py` a `*-knob` label into the same TSV. The knob
    capture is named `plansknob-*` so it can never match the `plans-*.txt`
    baseline glob; like the rest of the channel it is report-only and
    never touches the verdict.

- [x] **M0145-0024 — recon: Q74 `year_total`'s residual collapse and the
  `rows<=1` arm's remaining coverage** (filed 2026-09-22, delegated owner
  direction on M0145-0012; progress-report §3.3/§5.2). M0145-0020 ported
  `examine_simple_variable`'s non-recursive CTE arm and closed
  measured-no-gap — PG punts on all three corpus fires (`setOperations`
  for Q74's UNION ALL, single-grouping-column for Q31/Q39) — and
  M0145-0020a repaired the real grouped-output factor (~193x
  underestimation; Q39 `CTE Scan on inv` 1 → 19). But 0020a's own record
  states the 99-query arm-ON/arm-OFF A/B was never re-run, and the last
  A/B still moved Q74 into the collapse class. This task answers the
  residual question **before** 0012 can resume: `year_total` is a UNION
  ALL of grouped selects — a shape where PG also punts — so why does PG
  not collapse? Candidates to separate by measurement: (a)
  `estimate_num_groups` over appendrel / set-op output (the 0020a fix
  covered the grouped SELECT inside one member, not necessarily the
  union-of-groups output); (b) a set-op-crossing estimate channel PG
  carries that goopg lacks. Deliverable: the 99-query
  `GOOPG_CTE_ROWS_FALLBACK` ON/OFF A/B at SF0.25 re-run on the current
  tree (Q31 `ws` 1→1846, Q39 `inv` 1→20, Q74 `year_total` 1→8325 were
  the three fires), a named mechanism for whatever still collapses, and
  — only if the A/B shows no collapse-class move — the removal plan for
  0012. A collapse-class regression on removal remains an automatic
  no-go (0012's own criterion).
  Kind: recon
  Parent: M0145-0012
  - **CLOSED 2026\-09\-23 \(ralph2 loop \#25\).** Design doc
    `docs/design/0100-0149/m0145-0024-q74-residual-collapse-recon.md`.
    - The arm now fires on Q74 only \(`year_total` ×4\). Q31's `ws` and Q39's
      `inv` no longer collapse after M0145\-0020a.
    - 99\-query plan A/B at SF0.25: `same=98 changed=1` \(Q74 Hash Join ×3 →
      Nested Loop ×3\).
    - **PG 18.3 elects the arm\-OFF plan**: rows=1 CTE Scans under three NL
      Join Filters, via the set\-operation punt in `examine_simple_variable`
      \(selfuncs.c:5845\). M0145\-0012\'s "PG does not collapse" premise is
      corrected in its design doc.
    - Q74 timing, values identical: goopg arm ON 2.27 s, arm OFF 29.61 s, PG
      53.49 s.
    - The removal plan is written, conditional on an owner call, and is
      escalated under M0145\-0012.
  Movement: none

- [x] **M0145-0025 — re-take the TPC-H plan-parity baseline on the
  pinned-seed lane** (filed 2026-09-22, delegated owner direction;
  progress-report §5.4). M0145-0021b found `tpch-estimate-audit-arm.sh`
  had never pinned `GOOPG_ANALYZE_SEED`: the canonical TPC-H parity lane
  carried A/A noise of `same=1/21 changed=20` (a Q3 shape flip), so every
  `CATEGORIES-EXCL-MATCH` figure captured before the pin is possibly
  noise-contaminated and no capture may be diffed across the pin. Re-take
  the baseline: `JOINTREE=0 scripts/jointree-parity-capture.sh tpch
  <label> <dir>` per G3 (private clone, HEAD binary, engine-id recorded),
  bless the result as the lane's comparison base, and record which
  earlier TPC-H deltas (e.g. the M0144-0001 floor numbers) are thereby
  superseded. A capture and blessing only — no production code.
  Kind: recon
  Parent: M0145-0021b
  - **CLOSED 2026\-09\-23 \(ralph2 loop \#26\).** Design doc
    `docs/design/0100-0149/m0145-0025-tpch-parity-baseline-retake.md`.
    - Shipped configuration \(`PGSHAPED=1`, parallel, seed 20260905\),
      `318dd9f3d`: **match=2/22 \(Q6, Q11\)**, CATEGORIES\-EXCL\-MATCH
      join\-order=15 join\-method=10 scan\-type=10 parameterisation=6
      aggregation\-strategy=7 sort\-strategy=11 parallelism=15
      qual\-placement=4 rendering=2.
    - A/A: two captures byte\-identical \(goopg sha `952c4a77…`, PG
      `548ceee5…`\).
    - Blessed under `analysis/m0145/m0145\-0025\-tpch\-\*`.
    - The capture script\'s arm default `PGSHAPED=0` reads match=1/22 with
      different categories, so future comparisons must name the flag.
    - Superseded: M0144\-0001\'s floor capture \(pre\-pin, likely the
      legacy search\) and the M0145\-0002 TPC\-H captures. The §Goal floor
      re\-pin stays with the owner.
  Movement: none
- [x] **M0145-0026 — recon: duplicate hashclause in the path key list**
  \(filed 2026-09-23 by M0145-0007 slice 5\). On TPC-DS SF0.25 \(both
  arms\) 5 ANTI hash joins from Q78's `LEFT JOIN … WHERE sr_ticket_number
  IS NULL` arms reach lowering with the path key list
  `[ss_ticket_number=sr_ticket_number, ss_ticket_number=sr_ticket_number,
  ss_item_sk=sr_item_sk]` \(web/catalog arms alike\); only the late
  `fillJoinHashKeys` de-duplicates it, so EXPLAIN hides it. PG builds
  hashclauses from a restrictlist that cannot hold the same RestrictInfo
  twice \(`postgres/src/backend/optimizer/path/joinpath.c`
  `hash_inner_and_outer`\). Find where the duplicate enters \(the
  reduce-outer-joins anti conversion vs. the equivalence/restrictlist
  build\) and whether `final_cost_hashjoin`'s analogue or the join
  selectivity reads the duplicated list \(double-counted clause\).
  Reproduce with the probe patch `docs/design/0100-0149/m0145-0007-hashkeyprobe.patch`
  \(`GOOPG_HASHKEY_PROBE=1`, `verdict=DIFFLEN` lines\). Recon only.
  Kind: recon
  Parent: M0145-0007
  - **CLOSED 2026\-09\-23 \(ralph2 loop \#27\).** Design doc
    `docs/design/0100-0149/m0145-0026-duplicate-hashclause-recon.md`; probe
    `m0145-0026-dupkey-probe.patch`.
    - The duplicates are Q78\'s three LEFT→ANTI \(`FromOuterReduction`\)
      joins: `t = r` and `r = t`, both explicit and in the same EC.
    - Entry point: the seam\'s semi/anti fold
      \(`joinsearchseam.go:1888` / `:2252`\) re\-adds `LeftKey = RightKey`.
      It assumes the unnest convention, but `planFromItem`
      \(`planner.go:4467`\) keeps the equality in `Predicate`.
    - Selectivity: no double count \(`oneClausePerEquivClass`\). Cost:
      `numHashClauses` 3 instead of 2, one extra `cpu_operator_cost` per
      build and probe row.
    - Fix filed as M0145\-0026a.
  Movement: none
- [x] **M0145-0026a — fold a semi/anti link's key equality only when its
  predicate lacks it** \(filed 2026\-09\-23 by M0145\-0026\). At
  `extractSearchLeaves` \(`joinsearchseam.go:1888`\) and
  `extractScopeLeaves` \(`:2252`\), add `LeftKey = RightKey` only when no
  conjunct of the link\'s `Predicate` is that equality in either
  orientation, so the restrictlist holds one restrictinfo per clause, as PG
  does \(`hash_inner_and_outer`, joinpath.c\). Witness: a unit test on the
  Q78 arm shape through the semi/anti link route asserting `n=2` hash keys;
  movement = Q78 hash\-anti costs before/after, on both pipelines. Full
  default\-arm gates plus the fire\-set gate.
  Kind: impl
  Parent: M0145-0026
  - **LANDED 2026\-09\-23 \(ralph2 loop \#28\), `31403fe46`.**
    `semiAntiPredHasKeyEq` gates both folds; witness
    `TestExtractSearchLeaves_OuterReductionAntiKeyFoldedOnce` fails without
    the fix.
    - Q78\'s three hash anti joins drop exactly one clause\'s charge on the
      default arm \(web 1256.80..10452.70 → 1212.00..9958.01\); the knob arm
      moves the same way. No plan shape moved.
    - Gates: units, tpch\-spotcheck, tpcds\-sf025 \(PASS=96\), acceptance arm
      \(values identical\), fire\-set \(introduced=none\) — all PASS.
    - Cost\-only effect \(not an S3 instrument\): Q78 web\-arm hash anti total \-494.69; no match or category change.
  Movement: none
- [x] **M0145-0027 — knob-arm TPC-H Q20 decorrelates a correlated scalar
  subquery PG keeps as a SubPlan** \(filed 2026-09-23 by the M0145-0008
  executor-capability inventory\). Full 22-query acceptance arm, same
  binary: Q20 0.13 s default → **3.80 s** knob \(29x\), values identical.
  Plan capture: PG 18.3 and the default arm keep `ps_availqty > \(SubPlan
  1\)` with an index probe on `lineitem_part_supp_fkidx`; the knob arm
  builds `HashAggregate \(l_partkey, l_suppkey\)` over a full lineitem
  seq scan joined back to partsupp. PG never converts `EXPR_SUBLINK`
  \(`./postgres/src/backend/optimizer/prep/prepjointree.c:652`\), so the
  knob arm is unfaithful. Same outcome class as Q17, but the Q17 fix
  \(`planner.go` `planIsBareSeqScanTree` gate, M0145-0008\) does not
  reach it — here the scalar sublink sits inside an `IN` body the knob
  arm pulls up. Step 1: instrument where the scalar body is planned and
  what `canUnnestSubquery`'s probe-cheap guard sees on each arm \(Q17's
  method, design doc `m0145-0008-cutover-readiness-timing-ab.md`\); step
  2: fix on the knob route only. Expected movement: Q20's knob plan
  regains PG's SubPlan shape \(scan-type / aggregation-strategy
  categories for Q20 in the knob-arm TPC-H capture\) and its time
  returns to the default arm's; measured by
  `scripts/jointree-parity-capture.sh tpch` at `PGSHAPED=1` plus the
  full knob acceptance arm. Blocks the M0145-0008 flip.
  Kind: impl
  Parent: M0145-0008
  Movement: none
  - **FIXED 2026\-09\-23 \(ralph2 loop \#3\)** — code `a80588e30`, pin
    `b9316d276`. Design doc `m0145-0008-cutover-readiness-timing-ab.md`
    §"Q20 FIXED".
    - The filed hypothesis \(nesting inside the pulled `IN` body\) was
      WRONG. Probe on both arms: the rule\-based producer declines Q20's
      4\-conjunct WHERE on BOTH arms; the default arm gets its probe from
      `rewriteScanInputsWithSingleTablePredicates`, which on the knob arm
      meets `Filter\{corr\}\(Filter\_searched\{range\}\(SeqScan\)\)` — the
      search stranded the correlated conjuncts above a searched leaf
      \(`conjunctIsLocalEligible` refuses `OuterColumnRef`\), and the pass
      skips searched subtrees.
    - Fix: `flattenCorrelatedSeqScanFilters` in the M0145\-0008 restoring
      rule merges that chain into the bypass's unsearched
      `Filter\{SeqScan\}`; fail\-closed; knob/one\-rel routes only.
    - Knob Q20 3.80 s → **0.18 s** \(default 0.15 s\); total gap 1.06x →
      **1.01x**; values 24/24. Blast radius: TPC\-H knob only Q20; TPC\-DS
      SF0.25 knob only Q41 \(now one Filter, as PG prints it\); default
      arm sweep plans same=99 changed=0.
    - Gates: units, tpch\-spotcheck, acceptance arm, SF0.25 sweep,
      fire set \(SF0.25\+SF1, 25 fires, introduced=none\) — all PASS.
    - Residual: PG's mechanism \(outer ref = Param = pseudo\-constant
      index qual on the composite index\) is ledgered.
- [x] **M0145-0028 — knob arm loses the TPC-DS SF0.25 floor match Q41**
  \(filed and fixed 2026-09-23, ralph2 loop \#4, found by the M0145-0008
  pre-flip floor check\). Knob SF0.25 read match=1 vs floor 2 \(Q9, Q41\).
  Kind: impl
  Parent: M0145-0008
  Movement: none
  - Same split as M0145-0027 with a sublink: the one\-rel search put Q41's
    `i_manufact_id` range in a searched leaf Filter and stranded
    `\(SubPlan 1\) > 0` above it — SubPlan priced per leaf row \(986.78 vs
    180.78\), EXPLAIN printing one of two Filters, a redundant second Sort.
  - Fix `6de31332c`: `flattenStrandedSeqScanFilters` \(renamed,
    generalised to every `conjunctIsLocalEligible` refusal, merged in WHERE
    source order\).
  - Knob SF0.25 match 1 → 2, only Q41 changed; TPC\-H knob plans
    unchanged; default sweep plans same=99. Gates: units, spotcheck,
    acceptance arm, SF0.25 sweep, fire set \(24 fires\) — all PASS.
  - Ledgered: PG orders scan quals by per\-tuple cost
    \(`order_qual_clauses`\); goopg uses the WHERE order.
- [x] **M0145-0029 — group I: close the one-relation index-path coverage
  gaps before the flip** (filed 2026-09-23 by the M0145-0001 second
  lineage escalation, owner GO same day). The local flip fails ≥10 tests
  where the one-relation search on the jointree arm misses an index path
  the rule-based bypass produced (`TestSAOPWithConjunctMoves`, executor
  index/IOS tests — full list in
  `docs/design/0100-0149/m0145-0008-flip-test-triage.md`). For each: on
  the knob arm, measure whether the index path is GENERATED-but-out-costed
  or never generated; if never generated, port the producer (the bypass
  is rule machinery the search must subsume); if out-costed, record the
  cost gap and escalate if it reproduces on the corpus. Every closed gap
  is pinned by the re-enabled test; do NOT pin a gap to the legacy
  pipeline and proceed — that was owner-rejected (option (b)): these are
  the same defect class as the Q17/Q20 route-borne bypass losses.
  Kind: impl
  Parent: M0145-0008
  - **Slice 1 LANDED 2026\-09\-23 \(ralph2 loop \#12\), `fe3d1b0aa`.** Design
    doc `docs/design/0100-0149/m0145-0029-one-rel-index-path-coverage.md`.
    - Measured: the one\-rel search filed NO plain restriction index path
      at all \(only prebuilt \+ equality\-only bitmap\) — a generation gap.
    - Slice 1 ports `build_index_paths`' baserestrictinfo arm for the
      equality prefix \(`pathindexrestrict.go`\); lowering drops the
      consumed conjuncts from the leaf Filter. No plan moves on either arm.
    - PG 18.3 oracle: the varchar witness is a Bitmap Heap Scan in PG too
      \(stale expectation\); the composite witness is an Index Only Scan
      WITH quals \(gap → slice 3\).
    - Remaining: slice 2 range quals, 3 index\-only with quals, 4 SAOP,
      5 re\-run group I \+ update stale expectations.
  - **Slice 3 LANDED 2026\-09\-23 \(ralph2 loop \#13\), `6d50210f4`.**
    Index\-only scan WITH index quals.
    - `addIndexOnlyPaths` admits a filtered leaf when an index's equality
      prefix consumes EVERY local qual \(`consumingIndexClauses`\); the
      lowering sets `IndexOnlyScan.Key`/`Keys`. Residual quals over an
      index\-only scan stay refused \(ledgered\).
    - One path per index, as `build_index_paths`: the plain restriction
      producer declines where the index\-only one builds the same probe
      \(`restrictionPathIsIndexOnly`\). Before, the cost\-tied plain twin was
      filed first and won the tie.
    - `IndexOnlyScan` now embeds `searchedTree`: a covering index\-only scan
      reached the search root with no boundary Project and panicked in
      `markSearchedTree`.
    - Local flip: `TestIOS_\*` \(4\) and `TestArrayIndexOnlyScanAnswersFromKey`
      elect PG's shape only at `GOOPG_INDEX_PROBE_MULT=1`. At the shipped 2
      the bitmap wins: PG IOS 8.17 vs bitmap 8.18; goopg 16.27 vs 12.27 —
      the multiplier doubles the index path's heap fetch but not the
      bitmap's heap page. Slice 5 must decide per test \(or the owner, on
      the multiplier\).
    - `TestIndexOnlyDeformColdAndVisible`: 4 analysed rows, seq 1.01 wins —
      PG seq\-scans too; stale expectation for slice 5.
    - No plan moved: default sweep same=99; knob fire\-set plans unchanged
      at SF0.25 and SF1.
    - Remaining: slice 2 range quals, 4 SAOP, 5 group\-I re\-run.
  - **Slice 2a LANDED 2026\-09\-23 \(ralph2 loop \#14\), `fc716f1f8`.**
    Range quals on the index's LEADING column, plain restriction producer.
    - `restrictionLeadingRange`: first lower \(`>`/`>=`\) and first upper
      \(`<`/`<=`\) bound, either operand order, canonicalised; BETWEEN is the
      same pair. Only when no equality prefix binds the index.
    - `indexPathClause.op` \(zero = equality\); `createRangeIndexScanPlan`
      lowers onto `LowKey`/`HighKey` \+ `LowOp`/`HighOp`.
    - Selectivity via `conjunctionSelectivity` \(a lower\+upper pair is one
      band\). Single\-column index drops the bounds from the Filter;
      composite keeps them as a recheck \(`tryRangeIndexScan`'s guard\).
    - Local\-flip probe on 5002 rows with NULLs: every bound form returns
      the right count, NULLs excluded.
    - The same probe found a pre\-existing WRONG\-RESULTS bug in composite
      index\-only prefix probes \(trailing NULL skipped\), on the default
      pipeline too — filed as its own bug task above.
    - Remaining: 2b range on the bitmap producer \+ equality prefix
      followed by a range column \(needs an executor probe shape\); 4 SAOP;
      5 group\-I re\-run.
  - **Slice 4 LANDED 2026\-09\-23 \(ralph2 loop \#15\), `729027e28`.**
    ScalarArrayOp \(`col IN \(…\)` / `= ANY`\) on the leading column.
    - `restrictionLeadingSAOP` with `trySAOPIndexScan`'s gates; lowered onto
      `IndexScan.SAOPKeys`, IN dropped from the Filter; cost via
      scalararraysel \+ `numSAScans`.
    - Knob arm: TPC\-DS Q45 SubPlan 1 now costed by this path — rows
      18000 → 10 \(PG 10\), same shape; Q45 joins the fire set \(cost\-only\),
      executes PASS at both scales.
    - `TestSAOPWithConjunctMoves` under the flip: the SAOP path is generated
      but the no\-stats fixture \(1 row, 1 page\) elects the seq scan —
      fixture artefact for slice 5; with 100k measured rows the path wins
      \(`TestRestrictionSAOPIndexScanOnJointreePipeline`\).
    - The probe widened the filed trailing\-NULL bug to plain and SAOP
      composite prefix probes.
    - Remaining: 2b \(bitmap range; eq\-prefix\+range\), 5 group\-I re\-run.
  - **Slice 5 re\-run DONE 2026\-09\-23 \(ralph2 loop \#16\), `9e368ade8`.** Per\-test
    PG 18.3 oracle, fixture replayed in statement order; table in the design
    doc §Slice 5.
    - Root cause of the index\-only witnesses FIXED: the planner read the VM
      under `DefaultDBOid` while VACUUM and `pg_class` use the session
      database, so `allvisfrac` was 0 on a live server \(pg\_class said 9\).
      The live flip server now plans PG's `Index Only Scan … Index Cond`.
      6 subtests pass.
    - Stale expectations \(goopg already matches PG's Seq Scan\):
      `TestIndexScan\{Varchar,Char,Timestamp\}EndToEnd` — update in the flip
      commit \(still valid on today's legacy default\).
    - Multiplier\-dependent \(PG prices index vs bitmap within 0.01; the 2x
      `indexProbeCostMultiplier` decides\): `TestIOS_HeapFallback`,
      `TestIndexOnlyDeformColdAndVisible` — **owner decision needed**
      \(retire the multiplier, or accept the bitmap shape\).
    - Real gaps \(ledgered\): CREATE INDEX never records heap
      `reltuples`/`relpages` \(PG `index_update_stats`\), which breaks
      `TestIndexDeformRescanPersistsBound`; a SAOP followed by a range on the
      next column, which breaks `TestSAOPWithConjunctMoves` \(slice 2b\).
    - Remaining: 2b; the `index_update_stats` port; the owner's multiplier
      call. Then group I is empty for the flip.
  - **`index_update_stats` ported 2026\-09\-23 \(ralph2 loop \#17\), `292b1af2e`.**
    - btree builds publish the heap's `reltuples`/`relpages` with PG's
      guards \(empty never\-measured heap stays \-1; nothing written unless
      autovacuum \+ track\_counts are on and `autovacuum_enabled` is not
      false\); matches PG 18.3 on the same script.
    - The `w` witness now plans PG's Seq Scan \(1.05\).
    - Six tiny\-table executor tests now run with `autovacuum = off`
      \(`withAutovacuumOff`\), under which PG also keeps the size unmeasured;
      their flip\-arm check becomes PG's Bitmap Heap Scan, not the Seq Scan.
    - Remaining: 2b; the owner's multiplier call; non\-btree/REINDEX size
      recording \(ledgered\).
  - **Slice 2b LANDED 2026\-09\-23 \(ralph2 loop \#18\), `ea8fb4fce`.** Equality
    prefix \+ range on the next column, as the new probe shape
    `IndexScan.RangePrefix`.
    - Executor bounds \= prefix parts \+ bound part; producer keeps the bound
      as a Filter recheck and drops the prefix equalities.
    - Consumer audit \(site by site\): EXPLAIN, UPDATE/DELETE
      `indexScanPredicate`, clone/demotion, expression walkers, parallel
      safety and cardinality taught; IOS promotions REFUSE \(copying bounds
      without the prefix returned 9476 rows for PG's 2 before the guard\).
    - Found and fixed: `matchBitmapIndexQuals` built a gapped clause list →
      backend panic on the jointree arm \(`WHERE c = 99` on `\(a, b, c\)`\).
    - Live flip server vs PG 18.3: SELECT, subquery count, UPDATE and DELETE
      through prefix\+range predicates all identical.
    - Remaining for group I: SAOP\-prefix \+ range
      \(`TestSAOPWithConjunctMoves`\); the owner's multiplier call.
  - **Planner toggles \+ eqsel isunique 2026\-09\-23 \(ralph2 loop \#19\),
    `8a8f1c11f`.**
    - `enable_seqscan/indexscan/bitmapscan/sort` were never read into
      PlannerSettings: the search priced every path as enabled on both
      pipelines. Now read; the prebuilt leaf path counts its scan's toggle.
    - eqsel `isunique`: `col = const` on a unique key is 1/reltuples
      \(was 1/200 without stats\).
    - **Regression fixed:** `292b1af2e` \(CREATE INDEX size recording\)
      broke the pass\-required `TestPort_IsolationMultipleRowVersions`
      \(1M\-row PK point UPDATE seq\-scanned\). I had not run the isolation
      family for that commit. Bisected and fixed here.
    - `TestPort_IsolationEvalPlanQual` is intermittent at HEAD as well
      \(already filed by the nightly\).
  - **SAOP \+ range and the NULL\-key guard 2026\-09\-23 \(ralph2 loop \#20\),
    `98f68622d`.**
    - `a IN \(…\) AND b > x` on `\(a, b\)` is one probe \(SAOPKeys \+
      second\-column bounds\); live flip server identical to PG.
    - Restriction producers now decline an index whose UNBOUND key columns
      are nullable: the byte\-key btree stores no entry with a NULL key
      column \(root cause of the filed trailing\-NULL bug\).
    - Group\-I witness `TestSAOPWithConjunctMoves` still fails under the
      flip only because its fixture has no stats \(1 row, 1 page\); the
      shape it wants now exists.
    - Remaining for group I: the owner's multiplier call; fixture
      dispositions in the flip commit.
  - **CLOSED 2026\-09\-23 \(ralph2 loop \#21\).** Every generation gap the
    group\-I witnesses exposed is ported and pinned by a unit test
    \(restriction equality / range / SAOP / prefix\+range / SAOP\+range
    probes, index\-only with quals, allvisfrac, index\_update\_stats,
    planner toggles, eqsel isunique\). What is left is not a generation
    gap, and is handed on:
    - to the OWNER: `indexProbeCostMultiplier` decides
      `TestIOS_HeapFallback` and `TestIndexOnlyDeformColdAndVisible` on a
      live server \(PG prices index vs bitmap within 0.01\); no corpus plan
      reproduces it, so per this task's rule it is recorded, not escalated
      as a corpus defect;
    - to the M0145\-0008 flip commit: the stale expectations
      \(`TestIndexScan\{Varchar,Char,Timestamp\}EndToEnd`,
      `TestIndexScanEndToEndConstantKey`,
      `TestTPCHNumericSingleColumnIndexesAccepted`,
      `TestIndexDeformRescanPersistsBound` — now under `autovacuum = off`,
      so re\-check against PG's Bitmap Heap Scan\) and the no\-stats fixture
      of `TestSAOPWithConjunctMoves`;
    - to the filed bug: the byte\-key btree's missing NULL\-key entries.
  Movement: none
- [x] **M0145-0030 — group B: adjudicate the 11 behavioural flip-triage
  tests before the flip** (same filing). The flip-triage doc lists 11
  behavioural failures: grouping strategy, nested scalar subquery, NLI
  semi/anti election, hashed-IN SubPlan, two EXPLAIN expectations (one
  already classified stale/PG-faithful). For each: classify as
  stale-pin/PG-faithful-divergence (update the test to assert the jointree
  arm's PG-faithful shape, recording the oracle evidence) or real
  regression (fix before the flip, or file a named post-flip defect with
  owner visibility). Also in this task's scope: the script audit —
  enumerate every harness site that defaults the knob to `0` (known:
  `scripts/tpcds-sf025-regression.sh:310`,
  `scripts/tpch-estimate-audit-arm.sh:108`) and hand the flip commit the
  exact list. Do NOT change the defaults here — they must flip atomically
  WITH the pipeline default in the M0145-0008 commit, or pre-flip
  baselines silently switch arms.
  Kind: impl
  Parent: M0145-0008
  - **Re\-run under the flip 2026\-09\-23 \(ralph2 loop \#21\), after all of
    M0145\-0029:** 6 of the 11 now pass \(NLI semi/anti residual, parallel
    NLI jointype, hashed\-IN probe, RunFastJoinConcrete\). Still failing:
    - `TestCreateGroupingPathsGucOnPkFdStaysHash` — sorted instead of hashed;
    - `TestPlannerSettingsReachScalarSubqueryJoin/…/nested`,
      `TestScalarSubqueryPropagationKeepsDefaultPlan/nested` — 2 costed
      inner plans, want 1;
    - `TestExplainSelfCorrelatedExistsDoesNotAliasCollide` — a REAL EXPLAIN
      defect on the jointree arm: `Hash Cond: \(t1.a = t1.a\)` where the
      inner side is t2;
    - `TestExplainAnalyzeRowsRemovedByJoinFilter` — already classed stale.
  - **Fix 1 LANDED 2026\-09\-23 \(ralph2 loop \#22\), `903780b3e`.** Design doc
    `docs/design/0100\-0149/m0145\-0030\-group\-b\-adjudication.md`.
    - Alias collision: pulled\-up sublink bodies now get their own
      SourceTableIdx range \(`assignPulledSourceOffsets`\), as the legacy
      unnest does with `remapSourceTableIdx`. The witness passes under the
      flip; knob\-arm fire\-set plans identical apart from qualifiers.
    - Housekeeping: the fire\-set gate hit ENOSPC; 91 loop\-owned
      `tmp/m0145\-00\*\-data\*` clones were removed \(≈150G\). The gate
      leaves ~14G of clones per run.
  - **Loop \#23 \(2026\-09\-23\): fix 2 plus two stale pins; the loop \#21
    baseline is corrected.** Design doc §Fix 2 and §Script audit.
    - Fix 2: `TestCreateGroupingPathsGucOnPkFdStaysHash` was a real defect.
      The index\-ordered sorted variant was priced from its rule\-era display
      cost \(0.01\), not the search rel\'s `cost_index` path \(16.14\).
      `searchRelIndexPathCost` fixes the price, hashed wins as in PG 18.3,
      and the legacy arm is byte\-identical. Pinned by
      `TestIndexOrderedGroupingPricedFromSearchRelJointree`.
    - Stale pins, PG\-evidenced and updated: the nested scalar pair \(the
      middle scan is now costed at PG\'s 145; the innermost join is
      unchanged\) and `TestExplainAnalyzeRowsRemovedByJoinFilter` \(PG
      filters the one\-sided qual at the scan; now uses a two\-sided
      residual\).
    - **Correction:** loop \#21\'s "6 of 11 pass" was wrong. At HEAD and at
      `d9b962326` under the exact code flip, `TestNLISemiResidualExecution`,
      `TestNLIAntiResidualExecution`, `TestParallelNLIJointypeIdentity/semi`,
      `TestHashedInProbeActuallyFires` and `TestRunFastJoinConcrete` all
      fail. **Still open: those 5.**
    - Script audit done: the exact site list for the flip commit is in the
      design doc. The fire\-set gate\'s legacy\-vs\-knob comparison needs a
      post\-flip definition, which the flip commit decides.
  - **CLOSED 2026\-09\-23 \(loop \#24\).** Every group\-B test is dispositioned
    against PG 18.3 \(design doc table and §Hand\-off\).
    - Re\-based or updated, passing on both arms: NLI semi/anti residual
      \(large\-inner fixture; PG itself probes only there\), hashed\-IN probe
      and budget \(IN under OR, PG\'s hashed\-SubPlan shape\),
      `TestRunFastJoinConcrete` \(Project stack\).
    - Handed to the M0145\-0008 flip commit, still red under the flip:
      `TestParallelNLIJointypeIdentity/semi`, `/anti` \(legacy\-only fused
      family\), `TestSubqueryUnnestKillSwitch` \(legacy\-only switch\),
      `TestHashedInMixedKindFallsBack` \(non\-PG premise: PG raises
      42883\). `pinLegacyPipeline` cannot be reached from the executor, so
      the flip commit either deletes them or adds a sanctioned test pin.
    - Ledgered: no JOIN\_RIGHT\_ANTI/RIGHT\_SEMI; partial decomposed
      lateral semi/anti; int IN text\-subquery accepted instead of 42883;
      the rule\-era sorted\-input builder; hash\-build EXPLAIN ANALYZE
      `loops=0`.
    - Group I \(index coverage\) remains M0145\-0029\'s residue.

- [x] **M0145-0008b — recon: attribute the Q18/Q22 slowdown on the
  jointree default** (filed 2026-09-24, held in the M0145-0001 third
  lineage escalation until the owner answer above; ledger row exists).
  On the flip commit's own binary, TPC-H Q18 runs 1.6x and Q22 3.2x
  slower on the jointree default than on legacy (12.31 s → 20.19 s,
  0.48 s → 1.52 s); neither the estimator fixes nor the NULL-key guard
  causes it, and both queries are SHAPE-DIFF vs PG on both arms. Diff
  the two arms' plans per query, find the divergence the jointree arm
  adds, and name the fix (or attribute it to an already-filed task).
  Kind: recon
  Parent: M0145-0008
  - Expected movement: none by itself (recon); impl follow-ups file
    their own.
  - **DONE 2026\-09\-24 \(recon, no code\).** Design:
    `docs/design/0100-0149/m0145-0008b-q18-q22-jointree-slowdown-attribution.md`;
    evidence `analysis/m0145/m0145-0008b/` \(EXPLAIN ANALYZE of both arms on
    one binary; PG 18.3 probes\). Q18 9.94 s → 19.93 s, Q22 0.51 s → 1.38 s.
    - Q18 factor 1: the jointree lowering keeps join tuples unnarrowed \(width
      1622 vs 268; build 869 MB in 2 batches vs 581 MB in 1\) → M0145\-0008e.
    - Q18 factor 2: the jointree arm elects PG's HashAggregate for the grouped
      semi body; goopg's HashAggregate build takes 12.2 s where PG's takes
      3.3 s serial → M0145\-0008f.
    - Q22: the outer estimate for `substr\(c\_phone,1,2\) IN \(7\)` is 1/3 \(or
      1.0\) where PG gives 0.035, so the NL anti \(PG's shape, legacy's
      rule\) costs 265k and loses to a hash anti over all of orders →
      M0145\-0008g.
    Movement: none — recon.

- [x] **M0145\-0008e — the jointree lowering narrows join tuples as the
  legacy lowering does**: on TPC\-H Q18 the jointree arm's `orders ⋈
  customer` hash carries width 1622 \(869 MB, 2 batches\) where legacy
  carries 268 \(581 MB, 1 batch\); PG's widths are 25–45 \(only referenced
  columns reach a join's tlist, `build\_joinrel\_tlist` / attr\_needed\).
  Find where the legacy lowering narrows \(`narrowoutput.go`\) and why the
  jointree path skips it.
  Kind: impl
  Parent: M0145-0008b
  - Expected movement: Q18 build side back to one batch; acceptance\-arm
    Q18 time; width columns of the parity capture.
  - **DONE 2026\-09\-24 `d7dad3c1a` — premise corrected.** Design:
    `docs/design/0100-0149/m0145-0008e-residual-pad-check-uncorrelated-sublink.md`;
    evidence `analysis/m0145/m0145-0008e/`.
    - A trace in `narrowBuildInput` shows the jointree arm DOES narrow. The
      real cause was `seam\-decline reason=residual\-hits\-pad`: Q18's
      non\-pulled\-up grouped IN made the pad check's walk partial, the
      subquery's own names matched padded outer slots, and the search was
      discarded for the syntactic tree \(hash on 6M lineitem\).
    - `residualColumnRefsByName` steps over an uncorrelated inner plan; the
      shared body is `walkColumnRefsByName`.
    - Q18 on the jointree default: 13.17 s → 9.53 s \(legacy 9.94 s\).
      Gates: units, spotcheck, sf025 96/96 \(shapes 99/99 same\), acceptance
      arm, fire\-set, ea\-ratchet 52/52.
    - Found: EXPLAIN omits one of two stacked Filters → M0145\-0008j.
    Movement: none — CATEGORIES-EXCL-MATCH unchanged on TPC-H and SF0.25 (Q18's category set holds with the searched join order); the time moved, which is reported not judged.

- [x] **M0145\-0008j — EXPLAIN omits one of two stacked Filters over a
  scan** \(found 2026\-09\-24 by M0145\-0008e; reproduces at HEAD `877b51039`\):
  `SELECT a FROM ht1 WHERE a \> 1 AND EXISTS \(SELECT 1 FROM ht3\)` prints only
  `Filter: \(EXISTS\(SubPlan 1\)\)` on the Seq Scan, although both predicates
  execute \(results are correct\). PG prints `Filter: \(a \> 1\)` on the scan and
  the EXISTS as a gating `One\-Time Filter` \(pseudoconstant qual,
  `create\_gating\_plan`, createplan.c\). The plan is a residual Filter stacked
  on the searched leaf Filter; the EXPLAIN scan arm absorbs one \(the
  `attachedFilter` path in operators\_explain.go\) and drops the other.
  Kind: impl
  Parent: M0145-0008e
  - First step: EXPLAIN\-render every Filter in a Filter\-over\-Filter\-over\-scan
    chain, then consider the PG shape: a qual with no Vars becomes a gating
    Result One\-Time Filter. Expected movement: `rendering` /
    `qual\-placement` on shapes with an uncorrelated EXISTS conjunct.
  - **DONE 2026\-09\-24.** Design:
    `docs/design/0100-0149/m0145-0008j-explain-stacked-filters.md`.
    - `walkPlanFiltered` collapses stacked Filter wrappers into ONE
      `Filter:` line carrying their conjunction \(inner first\); the
      outermost wrapper stays the row source. Pinned by
      `TestExplainRendersEveryStackedFilter`.
    - PG 18.3 plans the shape differently \(gating `Result` with
      `One\-Time Filter: \(InitPlan 1\).col1` over `Filter: \(a \> 1\)`\) →
      M0145\-0008o.
    - Gates: units, spotcheck, fire\-set `fires=none` at SF0.25, SF1 and
      TPC\-H \(no corpus plan text changed\), sweep 96/96.
    Movement: none — no corpus plan text changed; CATEGORIES-EXCL-MATCH identical on both arms at SF0.25 and SF1.

- [x] **M0145\-0008o — pseudoconstant quals gate the plan; an uncorrelated
  EXISTS is an InitPlan** \(filed 2026\-09\-24 by M0145\-0008j\). PG 18.3 plans
  `WHERE a \> 1 AND EXISTS \(SELECT 1 FROM t3\)` as `Result / One\-Time Filter:
  \(InitPlan 1\).col1 / InitPlan 1 / \-\> Seq Scan on t3 / \-\> Seq Scan on t1
  Filter: \(a \> 1\)`; goopg evaluates the EXISTS per row as a SubPlan in the
  scan's Filter. Port `create\_gating\_plan` \(createplan.c: a qual with no
  current\-level Vars and no volatile functions goes to a gating
  `Result.OneTimeFilter`\) and make an uncorrelated `EXISTS\_SUBLINK` an
  initplan param \(subselect.c `make\_subplan`\), rendered `\(InitPlan N\).col1`.
  Kind: impl
  Parent: M0145-0008j
  - Expected movement: `qual\-placement` / `rendering` on uncorrelated
    EXISTS / constant\-qual shapes. The fire\-set gate shows neither corpus
    has this shape today, so the measurement is the upstream regress cases
    \(`subselect`, `join`\) plus a PG\-pinned EXPLAIN unit test.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008o-pseudoconstant-gating-initplan-exists.md`.
    - `gatePseudoconstantQuals` lifts uncorrelated\-sublink conjuncts \(a
      fail\-closed whitelist\) out of the WHERE residual into a gating
      `Result` One\-Time Filter above the scope's FROM tree; an uncorrelated
      EXISTS renders as `\(InitPlan N\).col1`; `EstimateRows\(Result\)` passes
      the child's rows through.
    - Four shapes pinned line for line against PG 18.3 \(EXISTS, NOT EXISTS,
      scalar InitPlan comparison, target\-list EXISTS\) plus values.
    - Gates: units, spotcheck, fire\-set `fires=none` at SF0.25, SF1 and
      TPC\-H, sweep 96/96 \(one mem\_guard\-killed attempt under nightly load,
      re\-run clean\), acceptance 24 MATCH, ea\-ratchet 52/52.
    Movement: none — no corpus plan changed.

- [x] **M0145\-0008f — HashAggregate build throughput on a many\-group
  input**: TPC\-H Q18's semi body \(`GROUP BY l\_orderkey HAVING sum \> 313`, 6M
  rows → 1.5M groups\) builds in 12.2 s inside goopg's plan, while PG's
  HashAggregate runs the whole aggregate in 3.3 s serial. The election is
  PG's, and the executor is 3–4x slower. Profile the hash\-agg build \(group
  lookup, transition state allocation, numeric sum\).
  Kind: impl
  Parent: M0145-0008b
  - Expected movement: acceptance\-arm Q18 time \(and every HashAggregate\-heavy
    query\); no plan change.
  - **DONE 2026\-09\-24 `11968441a`.** Design:
    `docs/design/0100-0149/m0145-0008f-hashagg-and-scan-row-copies.md`;
    evidence `analysis/m0145/m0145-0008f/`.
    - The seq scan's retention clone copies only the deformed survivor window
      \(`cloneRowOwnedPrefix`\); the hashed grouping loop uses scratch key
      buffers and detaches keys only for a group\-founding row
      \(`evalGroupKeysScratch`, single\-key `setGroupKey`\).
    - Serial GROUP BY 4.55 s → 3.8 s \(PG 1.86 s\); with HAVING sum ~6.8 s →
      ~6.1 s \(PG 3.05 s\); acceptance arm Q18 9.53 → 8.48 s, total 64.0 →
      62.6 s, 24 MATCH. Parallel aggregate tests pass under \-race.
    - The remaining gap is the scan's per\-row full\-width row allocation
      \(and the GC it drives\) → M0145\-0008k.
    Movement: none — executor only; sf025 plan shapes 99/99 unchanged; the acceptance-arm time moved (reported, not judged).

- [x] **M0145\-0008k — the seq scan allocates and copies a row per tuple where
  PG copies nothing**: at its retention boundary the scan allocates a
  full\-width row \(`acquireRow`\) and deep\-copies the survivor window for every
  visible tuple, because it releases the page lock before the parent reads
  the slot. PG hands the parent a slot pointing into a PINNED shared buffer
  \(heapgettup / ExecStoreBufferHeapTuple; a pin blocks pruning's cleanup
  lock\), so an aggregate over lineitem copies nothing. On TPC\-H lineitem this
  is about half of a serial grouped aggregate's time plus most of its GC.
  Also: `count\(\*\)` over lineitem \(4.95 s\) is slower than
  `sum\(l\_quantity\)` \(3.2 s\) on goopg \(PG 0.96 s / 1.40 s\).
  Kind: recon
  Parent: M0145-0008f
  - First step: explain the count\(\*\) \> sum\(\) inversion \(plan and profile\),
    then decide whether a pin\-held slot \(copy on retention only\) is sound
    under goopg's page mutation paths \(HOT prune compaction, hint bits\).
    Expected movement: none on parity; acceptance\-arm scan\-heavy times.
  - **DONE 2026\-09\-25 \(recon\).** Design:
    `docs/design/0100-0149/m0145-0008k-seqscan-row-copy-recon.md`; evidence
    `analysis/m0145/m0145-0008k/`.
    - Inversion explained: `effectiveDeformBound` maps `deformBoundNone` to
      FULL width, and a zero\-arm Aggregate \(`count\(\*\)`\) leaves the bound at
      `deformBoundNone`, so the query that reads no column deforms and clones
      all 16 \(`decodeScanRow` 5.5 s \+ `cloneRowOwned` 4.1 s cumulative\).
      A throwaway probe \(bound 0\) took serial `count\(\*\)` 4.8 s → 1.55 s
      and parallel 1.76 → 0.61 s, values unchanged → M0145\-0008p.
    - Pin\-held slot: UNSOUND today. All three goopg compaction paths
      \(opportunistic prune, VACUUM, IOS on\-access prune\) compact under the
      exclusive content lock alone; PG requires a cleanup lock \(pin count
      1, pruneheap.c:245, vacuumlazy.c:1343\) → M0145\-0008q, then the
      zero\-copy slot.
    Movement: none — recon.

- [x] **M0145\-0008p — a scan under a zero\-consumer aggregate deforms and
  copies nothing** \(filed 2026\-09\-25 by M0145\-0008k\). `deformBoundBelow`'s
  Aggregate arm returns `deformBoundNone` when no arm folds a reference
  \(`count\(\*\)`\), and `effectiveDeformBound` maps that to full width. Add a
  distinct "zero columns consumed" bound; make the seq scan's survivor window
  0 \(skip deform and the retention clone; visibility and the prefilter path
  unchanged\); audit the index / bitmap leaf stamps and the parallel worker
  closure; pin with the EX1 tail\-poison test harness.
  Kind: impl
  Parent: M0145-0008k
  - Expected movement: none on parity \(executor only\); serial `count\(\*\)`
    over TPC\-H lineitem 4.8 s → ~1.5 s \(probe\), parallel 1.76 → ~0.6 s;
    acceptance\-arm times on count\(\*\)\-shaped queries.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008p-zero-consumer-deform.md`; evidence
    `analysis/m0145/m0145-0008p/`.
    - `deformBoundZero` \(\-2\) is the Aggregate arm\'s fresh bound
      \(Finalize keeps None\); `effectiveDeformBound` stamps
      `deformWidthZero`; the SeqScan resolves it through
      `seqScanSurvivorWidth` to window 0. Index/bitmap leaves read the stamp
      as full width \(a Zero bound cannot reach them: key columns and
      recheck quals always fold\).
    - Measured A/B on a private clone: serial `count\(\*\)` 4.8 → 1.5 s,
      2 workers 1.7 → 0.57 s; `sum` unchanged; values identical.
    - Gates: units, spotcheck, sf025 96/96 \(99/99 shapes same\),
      acceptance 24 MATCH, ea\-ratchet 52/52, full regress suite = the same
      two pre\-existing failures as a clean HEAD worktree \(→ M0145\-0008r,
      M0145\-0008s\).
    Movement: none — executor only; no plan moved.

- [x] **M0145\-0008q — page compaction takes a cleanup lock \(pin count 1\),
  as PG's does** \(filed 2026\-09\-25 by M0145\-0008k\). goopg compacts heap
  pages under the exclusive content lock alone in the opportunistic prune
  \(`PagePruneOpt`\), VACUUM \(`PageVacuumPrune`\) and the index\-only
  on\-access prune. PG requires a cleanup lock:
  `ConditionalLockBufferForCleanup` in `heap\_page\_prune\_opt`
  \(pruneheap.c:245\), which skips a pinned page, and
  `LockBufferForCleanup` in VACUUM \(vacuumlazy.c:1343/1373\). Port the pair
  onto the buffer pool's pin count and take it at every compaction site.
  This is the prerequisite for a pin\-held \(zero\-copy\) scan slot.
  Kind: impl
  Parent: M0145-0008k
  - Expected movement: none on parity; enables the zero\-copy slot
    \(retention clone only at Sort / Hash build / Material, PG's
    `ExecMaterializeSlot` point\). Gates: isolation family \(prune vs
    concurrent scan\), race gate, pgbench.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008q-cleanup-lock.md`; evidence
    `analysis/m0145/m0145-0008q/`.
    - `Pool.IsCleanupOK` / `ConditionalLockForCleanup` / `LockForCleanup`
      \(capped\-backoff retry\) on the shared pin count. HOT\-path prune uses
      `IsCleanupOK`; IOS temp\-page prune is conditional; VACUUM is
      conditional, a non\-aggressive pass does `lazy\_scan\_noprune`
      \(`PageCountVacuumLive` under the share lock, `Stats.SkippedPinned`
      guards relfrozenxid via `RelfrozenxidGuarded`\), an aggressive pass
      waits. Redo compacts private page copies, so it needs no lock.
    - pgbench TPC\-B A/B: tps within noise \(\~625 both\); small\-table
      growth identical except tellers 84 → 90 blocks. The \~80\-block growth
      of 2\-row branches exists on HEAD too → M0145\-0008t.
    - Gates: units, race \(storage \+ vacuum\), spotcheck, acceptance 24
      MATCH, sf025 96/96, isolation family = the two failures a clean HEAD
      worktree also shows \(open nightly items\).
    Movement: none — storage discipline; no plan or value moved.

- [x] **M0145\-0008t — prune a heap page when it is read, as PG does**
  \(filed 2026\-09\-25 by M0145\-0008q\). goopg prunes only when a HOT update
  finds its page full; PG calls `heap\_page\_prune\_opt` whenever a scan
  or index fetch reads a page \(`heap\_prepare\_pagescan`,
  `heapam\_index\_fetch\_tuple`, heapam.c / heapam\_handler.c\), gated on
  `pd\_prune\_xid` and free space below the fill threshold, under a
  conditional cleanup lock. Symptom: after 30 s of pgbench TPC\-B at scale
  2, `pgbench\_branches` \(2 rows\) reaches heap block 83 on HEAD.
  Kind: impl
  Parent: M0145-0008q
  - First step: call `storage.PagePruneOpt` under
    `Pool.ConditionalLockForCleanup` from the seq\-scan page switch and the
    index heap fetch when `pd\_prune\_xid` is set and the page is under
    PG\'s free\-space threshold \(`Max\(fillfactor target, BLCKSZ/10\)`\);
    measure branches/tellers growth and TPC\-B tps against HEAD.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008t-prune-on-access.md`; evidence
    `analysis/m0145/m0145-0008t/`.
    - `storage.PagePruneXIDSet` / `PagePruneOnAccessWanted` port the
      `heap\_page\_prune\_opt` gate; `executor.pruneHeapPageOnAccess` prunes
      under `ConditionalLockForCleanup` from the seq scan \(pool pins\), the
      index fetch \(per heap\-block switch\) and the bitmap heap scan. Skipped
      on a standby, with the GUC off, and for catalogs.
    - **Premise refuted:** pgbench small\-table growth is unchanged
      \(branches 80/83 → 80/81, tellers 85 → 86/87 blocks; tps within
      noise\). `PageAddHeapTuple` never reuses an unused line pointer, so a
      page that has held 291 versions is full however much it is pruned →
      M0145\-0008v.
    - sf025: the first candidate pass took Q39 3.1 → 5.4 s \(one\-time prune
      of stale\-hint pages in the gate cluster\); the second pass 3.06 s.
    - Gates: units, spotcheck, acceptance 24 MATCH, sf025 96/96 ×2,
      isolation and full regress = only the failures HEAD also has.
    Movement: none — no plan, value or table growth moved.

- [x] **M0145\-0008v — heap line\-pointer lifecycle: LP\_DEAD on prune,
  LP\_UNUSED after index vacuum, recycling** \(filed 2026\-09\-25 by
  M0145\-0008t\). goopg\'s `PageAddHeapTuple` always appends a line
  pointer and `PageGetHeapFreeSpace` returns 0 at `MaxHeapTuplesPerPage`,
  so a frequently updated row fills its page after 291 versions however
  much the page is pruned: pgbench\'s 2\-row `pgbench\_branches` reaches
  \~80 heap blocks in 30 s. PG recycles unused line pointers
  \(`PageAddItemExtended` with `PD\_HAS\_FREE\_LINES`, bufpage.c\) and
  truncates the trailing ones \(`PageTruncateLinePointerArray`\). That is
  safe in PG because prune marks a dead non\-HOT tuple `LP\_DEAD`, and only
  VACUUM sets `LP\_UNUSED` after its index entries are gone
  \(`heap\_page\_prune\_and\_freeze`, `lazy\_vacuum\_heap\_page`\). goopg\'s
  `pagePruneCore` marks such tuples `LP\_UNUSED` at once while index
  entries still point at them, which is why it must never recycle today.
  Kind: impl
  Parent: M0145-0008t
  - First step: make `pagePruneCore` mark dead non\-HOT tuples `LP\_DEAD`
    \(WAL and redo siblings too\), have VACUUM turn `LP\_DEAD` into
    `LP\_UNUSED` after index cleanup, then let `PageAddHeapTuple` reuse
    `LP\_UNUSED` slots under `PD\_HAS\_FREE\_LINES`. Measure pgbench
    small\-table growth and TPC\-B tps against HEAD; gate on the isolation
    family and the index\-scan correctness tests.
  - **S1 LANDED 2026\-09\-25** \(design
    `docs/design/0100-0149/m0145-0008v-line-pointer-lifecycle.md`, slice
    plan S1–S3\): prune writes storage\-less LP\_DEAD for dead non\-HOT
    tuples, dead roots and dead\-chain redirects \(`PruneResult.Dead`,
    `PruneHeapPageBySlots`\); the PG prune record carries
    `XLHP\_HAS\_DEAD\_ITEMS` and redo applies it byte\-identically; VACUUM
    hands every LP\_DEAD item to index cleanup \(`PageDeadItems`\).
    - Gates: units, spotcheck, acceptance 24 MATCH, sf025 96/96,
      isolation and regress = HEAD\'s failures only, amcheck ports, real\-PG
      WAL consumption tests; pgbench A/B within host noise.
    - Next: S2 — the post\-index\-cleanup heap pass \(LP\_DEAD → LP\_UNUSED,
      `PageTruncateLinePointerArray`, `PRUNE\_VACUUM\_CLEANUP` record\) and
      autovacuum\'s missing index pass; then S3 recycling.
  - **S2 LANDED 2026\-09\-25:** VACUUM\'s second heap pass
    \(`vacuum.VacuumDeadItems` → `storage.PageVacuumDeadItems` \+
    `PageTruncateLinePointerArray`, logged as PG\'s
    `PRUNE\_VACUUM\_CLEANUP`; truncate\-only redo, byte\-identical\). It
    runs only when every index is clean \(btree incl. `USING hash`; GIN or
    any failure blocks it\) and only on clusters with the new
    `heap\_lp\_lifecycle` capability \(pre\-S1 clusters hold LP\_UNUSED
    items with live index entries\).
    - Verified with PG 18.3 `pg\_waldump` on a fresh cluster: both new
      record shapes decode.
    - Gates: units, spotcheck, acceptance 24 MATCH, sf025 96/96, isolation
      and regress = HEAD\'s failures, WAL/amcheck testport set, pgbench A/B
      neutral.
    - Next: S3 — autovacuum index \+ second pass, `PageAddHeapTuple` reuse
      under `PD\_HAS\_FREE\_LINES` \(capability\-gated\), free\-space
      ceiling, SSI SIREAD audit, pgbench growth measurement.
  - **S3a LANDED 2026\-09\-25:** closed a race S2 opened \(two interleaved
    VACUUMs: A\'s stale dead TIDs could delete a reused offset\'s NEW index
    entries, because goopg\'s VACUUM lock is acquire\-then\-release and
    autovacuum takes none\) with a per\-relation VACUUM gate held first
    pass → second pass \(manual blocks, SKIP\_LOCKED/autovacuum skip\).
    Autovacuum now runs the index pass \(`executor.VacuumRelationIndexes`,
    namespace confirmed by identity\) and the second pass.
    - Gates: units, race \(vacuum \+ lifecycle tests\), spotcheck,
      acceptance 24 MATCH, sf025 96/96, isolation and regress = HEAD\'s
      failures.
    - Next: S3b — reuse in `PageAddHeapTuple` \+ free\-space ceiling
      \(capability\-gated\), after the SSI SIREAD audit; measure pgbench
      growth.
  - **S3b LANDED 2026\-09\-25 — task complete.**
    - `PageAddHeapTuple` reuses the first LP\_UNUSED item without storage
      under `PD\_HAS\_FREE\_LINES` \(`PageAddItemExtended`\) and
      `PageGetHeapFreeSpace` stops answering 0 at the ceiling while a free
      line exists; both gated on `heap\_lp\_lifecycle`. Prune compaction
      sets/clears the hint like `PageRepairFragmentation`. Redo of an insert
      into a reused line is byte\-identical.
    - SSI audit: INSERT now checks conflict\-in from the PAGE grain
      \(`ssiRecordTupleInsert`\), since a tuple\-grain SIREAD on a new TID can
      only be a stale lock on a reused slot; PG checks the relation grain.
    - Catalogs and TOAST relations never reach the second pass \(their
      on\-disk indexes may not be listed in the catalog\).
    - Measured: pgbench small\-table growth UNCHANGED \(branches \~85
      blocks both arms, tps equal\). Page dump: emptied pages carry free
      lines but are never inserted into again → M0145\-0008w.
    - Gates: units, spotcheck, acceptance 24 MATCH, sf025 96/96 ×2
      \(single\-query swings Q8, Q67 reverse between runs\), isolation and
      regress = HEAD\'s failures, WAL/amcheck testport set.
    Movement: none — storage lifecycle; no plan or value moved.

- [x] **M0145\-0008w — pgbench\'s small tables still grow \~40 heap blocks per
  10 s: emptied pages are never inserted into again** \(filed 2026\-09\-25 by
  M0145\-0008v\). After 10 s of TPC\-B at scale 2, `pgbench\_branches`
  blocks 0–3 each hold 226 line pointers that are all LP\_UNUSED or
  LP\_DEAD, with `pd\_upper = 8192` and `PD\_HAS\_FREE\_LINES` set
  \(`analysis/m0145/m0145\-0008v/pgbench\-s3b.txt`\).
  Kind: recon
  Parent: M0145-0008v
  - Hypotheses to measure, not yet verified:
    - Non\-HOT updates dominate: the LP\_DEAD survivors show updates that
      left their page. The HOT path prunes only under `IsCleanupOK`, which
      a concurrent reader\'s pin defeats with 8 clients on 2 rows.
    - The insertion target \(FSM / target block\) never learns about space
      an on\-access prune freed, so emptied pages are only revisited after
      VACUUM updates the FSM. PG\'s `RelationGetBufferForTuple` tries the
      update\'s own page first \(`heap\_update`\) and keeps `rd\_targblock`.
  - First step: count HOT vs non\-HOT updates in a 10 s run \(the
    `s321Note` counters in `tryApplyHOTUpdate`\) and log which block each
    non\-HOT new version lands on; compare with PG 18.3 running the same
    pgbench on a scratch cluster.
  - **RECON COMPLETE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008w-pd-prune-xid-keeps-newest-recon.md`;
    evidence `analysis/m0145/m0145\-0008w/measurements.txt`.
    - Both hypotheses refuted as the main cause. The cleanup lock was never
      refused \(probe: `lockfail=0`, HOT\-path foreign pin 36×\), but the
      prune GATE failed: 18,575 hinted reads, 258 passed.
    - Cause: five `pd\_prune\_xid` setters in `storage/heap.go` keep the
      NEWEST xid \(`xmax > pruneXID`\); PG\'s `PageSetPrunable` keeps the
      OLDEST. A continuously updated page\'s hint is never below the
      horizon, so neither prune path runs.
    - Uncommitted keep\-oldest build: branches 85 → 18 blocks, tellers 91 →
      0, `hot\_nospace` 5,211 → 3,547, tps 641 → 662 \(PG: 7 / 2 blocks\) →
      M0145\-0008x.
    Movement: none — recon.

- [x] **M0145\-0008x — `pd\_prune\_xid` keeps the OLDEST prunable xid, as
  PG\'s `PageSetPrunable` does** \(filed 2026\-09\-25 by M0145\-0008w\). Five
  setters in `internal/storage/heap.go` \(xmax stamp, non\-HOT update stamp,
  HOT old\-tuple stamp, chain\-update stamp, multixact updater stamp\) keep
  the NEWEST, so a continuously updated page never passes the prune gate.
  Kind: impl
  Parent: M0145-0008w
  - Port `PageSetPrunable` \(`bufpage.h`\) as one helper used by all six
    setters, with `XIDPrecedes` \(wraparound\-safe\); check whether the redo
    arms should set the hint too \(PG\'s delete/update redo calls
    `PageSetPrunable`\).
  - Expected movement: pgbench `pgbench\_branches` \~85 → \~18 blocks,
    `pgbench\_tellers` \~90 → 0–2, TPC\-B tps \+3%; pin with a unit test and
    the pgbench block counts.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008x-pd-prune-xid-keeps-oldest.md`.
    - The five producers call the existing `pageSetPrunablePG` \(now
      `XIDPrecedes`\-ordered\); `PagePruneOpt`\'s fast path uses the same
      modular order. Runtime and redo now agree on `pd\_prune\_xid`.
    - pgbench A/B: branches 73/85 → 21/13 blocks, tellers 80/94 → 1/1 \(PG:
      7 / 2\); tps within host noise.
    - Gates: units, spotcheck, acceptance 24 MATCH, sf025 96/96, isolation
      and regress = HEAD\'s failures.
    Movement: none — storage bookkeeping; no plan or value moved.

- [x] **M0145\-0008u — a pin\-held \(zero\-copy\) seq\-scan slot**
  \(filed 2026\-09\-25 by M0145\-0008q\). With compaction under a cleanup
  lock, a scan may hand its parent a slot that reads the pinned page after
  the content lock is released, as `ExecStoreBufferHeapTuple` does, and
  clone only where a consumer retains the row \(Sort, Hash build, Material:
  PG\'s `ExecMaterializeSlot` point\). Removes the per\-tuple retention
  clone the M0145\-0008p/k measurements attribute seq\-scan time to.
  Kind: impl
  Parent: M0145-0008q
  - First step: audit every consumer that keeps a row past the next
    `Next\(\)` call and confirm it already materializes; then drop
    `cloneRowOwnedPrefix` in `seqScanOp.Next` behind that audit, with the
    tail\-poison harness and the isolation family as gates.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008u-borrowed-scan-rows.md`; evidence
    `analysis/m0145/m0145-0008u/`.
    - Audit result: the clone detaches the reused row slice and the per\-page
      arena; datums never alias page bytes. A seq scan now hands its own row
      to a direct\-parent aggregate that keeps nothing \(built\-in
      count/sum/avg/min/max; no passthrough, user aggs, DISTINCT, ORDER BY,
      WITHIN GROUP, sorted strategy or Finalize\).
    - Serial `count\(\*\)` 1.50 → 0.95 s \(PG 0.96\), `sum` 2.91 → 2.13 s,
      parallel 0.57 → 0.24 / 1.10 → 0.65 s; TPC\-H Q1 4.17 → 3.12 s; values
      identical.
    - Found: test helper `sortedRowStrings` renders only `Datum.Int`; the
      0008v lifecycle tests now use a value\-rendering helper.
    - Gates: units, race, spotcheck, acceptance 24 MATCH, sf025 96/96,
      isolation and regress = HEAD\'s failures.
    Movement: none — executor only; no plan moved.

- [x] **M0145\-0008r — WRONG RESULTS: a bitmap scan over an unproven partial
  index drops the rows its predicate excludes** \(found 2026\-09\-25 while
  filing nightly AI\-20260925\-002342\-005; reproduces at HEAD `b1f93fb81`\).
  The must\-pass regress case `portals\_p2` returns `\(0 rows\)` for `SELECT
  \* FROM onek2 WHERE unique1 = 50` \(and `= 60`\); PG returns one row each.
  `create\_index` adds `onek2\_u1\_prtl ON onek2\(unique1\) WHERE stringu1 \<
  'B'`, and goopg plans `Bitmap Heap Scan / Recheck Cond: \(stringu1 \< 'B'\)
  / Bitmap Index Scan on onek2\_u1\_prtl`. Minimal repro: a 1000\-row table
  with that partial index; `unique1 = 51` \(stringu1 'Z'\) returns 0 rows.
  Kind: impl
  Parent: M0145-0008
  - Root cause: `buildOneBitmapPath` \(pathbitmap.go\) builds a path for a
    partial index whose predicate the query does not imply, on the premise
    that rechecking the predicate on the heap keeps it correct. It does
    not: a row the predicate excludes has no index entry, so no recheck
    can bring it back. PG generates the path only when `predOK` holds
    \(`check\_index\_predicates` / `create\_index\_paths`, indxpath.c\). The
    other producers \(`addOneOrderedIndexPath`, `findBTreeIndexForColumn`,
    `pathindexonly.go`, `pathindexrestrict.go`\) already decline; the
    last reuses the M0134\-0017b narrow `Var op Const` prover.
  - Likely trigger \(not bisected\): first seen in the 2026\-09\-25 nightly.
    The legacy single\-table WHERE bypass \(`planIndexScanFromWhere`, which
    declined unproven partial indexes\) was deleted by the M0145\-0008
    legacy\-deletion slice 2, so single\-table queries now reach the
    bitmap producer.
  - First step: in `buildOneBitmapPath`, decline a partial index unless its
    predicate is proven from the leaf's conjuncts \(reuse the prover
    `findBTreeIndexForColumn` uses\); drop the recheck\-append premise and
    its comment. Pin with the repro above and re\-run `create\_index` \+
    `portals\_p2` \(`GOOPG\_REGRESS\_DIFF\_DIR` shows the diff\).
  - **DONE 2026\-09\-25 \(`f56d94e86`\).** Design doc
    `docs/design/0100\-0149/m0145\-0008r\-partial\-index\-bitmap\-proof.md`;
    evidence `analysis/m0145/m0145\-0008r/`.
    - `buildOneBitmapPath` declines a partial index unless a leaf conjunct
      proves its predicate \(`partialPredicateProvenBy` →
      `provePartialIndexPredicate`\); the recheck premise is gone.
    - Repro: `unique1 = 51` returns its row \(Seq Scan\). Full
      `TestPort\_RegressSuite`: `portals\_p2` and `select` PASS; `union`
      still red → M0145\-0008s.
    - Gates: units, spotcheck, acceptance arm 24 MATCH, sf025 96 PASS \(99/99
      shapes same\), fire set none at both scales, pgbench smoke.
  Movement: none

  > ## ESCALATION 2026\-09\-25 \(S2\) — wrong results
  >
  > M0145\-0008r: a single\-table equality query returns no row when goopg
  > elects a bitmap scan over a partial index the query does not imply.
  > The must\-pass regress case `portals\_p2` is red in the nightly. Filed
  > and not selected ahead of the banner, per S2; the owner decides its
  > placement.
  >
  > **OWNER ANSWER 2026\-09\-25 \(delegated\):** placed FIRST in banner
  > item 2a — wrong results on an ordinary single\-table equality query,
  > the most reachable of the open S2 defects.

- [x] **M0145\-0008s — WRONG RESULTS: a HashSetOp runs per worker over
  partial \(Parallel Seq Scan\) inputs** \(found 2026\-09\-25 while triaging
  nightly AI\-20260925\-002342\-005; reproduces at HEAD `b1f93fb81` on a clean
  worktree\). The must\-pass regress case `union` returns 1660 / 1721 \(varies
  per run\) for `select count\(\*\) from \( select unique1 from tenk1 intersect
  select fivethous from tenk1 \) ss`; PG returns 5000. goopg's plan is
  `Finalize Aggregate / Gather \(4 workers\) / Partial Aggregate / HashSetOp
  Intersect / Parallel Seq Scan on tenk1, Parallel Seq Scan on tenk1\_1`.
  Each worker intersects its own share of each side, so a match whose two
  rows land in different workers is lost. It shows only in the full suite
  \(the earlier cases leave tenk1 with statistics that make the parallel
  plan win\); `union` alone passes.
  Kind: impl
  Parent: none
  - PG never builds a SetOp over partial paths: `generate\_nonunion\_paths`
    \(prepunion.c\) plans both children as complete paths, and a SetOp path
    is not partial, so a Gather can only sit below it on each child.
  - First step: find where goopg makes the setop subtree parallel \(Gather
    stamping above a Partial Aggregate whose input is a SetOp, see memory
    "goopg parallelism = Gather stamping"\) and refuse a partial scan under
    a SetOp. Pin with the regress query at 4 workers.
  - **DONE 2026\-09\-25 \(`031e8452c`\).** Design doc
    `docs/design/0100\-0149/m0145\-0008s\-setop\-partial\-driver.md`;
    evidence `analysis/m0145/m0145\-0008s/`.
    - The parallel post-pass reached the SetOp through `drivingScan`\'s
      `\*SetOp` arm, which resolved a driver for every set operation; the
      path\-level `addPartialSetOpPath` already required `setOpStreams`.
      The `drivingScan` / `stampParallelScan` / `drivingScanCrossesSort`
      arms now require it too.
    - Repro \(10k\-row wide table\): INTERSECT 3672/3968 → 5000; UNION ALL
      keeps its parallel Append. Full `TestPort\_RegressSuite` PASS.
    - Gates: units, spotcheck, acceptance arm 24 MATCH, sf025 96 PASS \(99/99
      shapes same\), fire set none at both scales, pgbench smoke.
  Movement: none

  > ## ESCALATION 2026\-09\-25 \(S2\) — wrong results
  >
  > M0145\-0008s: `INTERSECT` \(and presumably `EXCEPT`\) under a parallel
  > aggregate returns a nondeterministic, too\-small result, because each
  > worker applies the set operation to only its share of both inputs. The
  > must\-pass regress case `union` is red in the nightly. Filed and not
  > selected ahead of the banner, per S2; the owner decides its placement.
  >
  > **OWNER ANSWER 2026\-09\-25 \(delegated\):** placed second in banner
  > item 2a — nondeterministic wrong results under a plan shape the
  > corpus can elect \(TPC\-DS carries INTERSECT/EXCEPT queries\).

- [x] **M0145\-0008g — `expr = ANY \(const list\)` with an expression operand is
  estimated as PG does**: PG prices `substr\(c\_phone,1,2\) IN \(7 values\)` at
  0.035 \(scalararraysel: per\-element eqsel, default 1/DEFAULT\_NUM\_DISTINCT
  for an expression without stats, OR\-merged\); goopg's `clauseSelectivity`
  / `clauseSelectivityWithSource` IN arms return 1/3 for a non\-ColumnRef
  operand, and the TPC\-H Q22 leaf shows even less reduction. That makes the
  NL anti cost 265k, and it loses to a hash anti over all of orders.
  Kind: impl
  Parent: M0145-0008b
  - Also check the anti NL's per\-outer\-row charge: ≈5.3 in goopg vs ≈0.8 in
    PG \(`final\_cost\_nestloop` semi/anti early exit\). Expected movement:
    TPC\-H Q22 elects PG's NL Anti Join \+ Index Only Scan
    \(`join\-method`/`parameterisation`\); acceptance\-arm Q22 time.
  - **DONE 2026\-09\-24 `70d0478bd`.** Design:
    `docs/design/0100-0149/m0145-0008g-inlist-expr-selectivity.md`;
    evidence `analysis/m0145/m0145-0008g/`.
    - `inListExprSelectivity` applies the scalar clause estimator to each
      synthesized `operand <op> element`, merged by the shared
      `inListMergeSelectivity`; both arms, pinned at PG's 0.035.
    - Q22's customer leaf 1750 rows \(PG 1750\); upper plan now PG's
      GroupAggregate over Gather Merge. The anti join stays hash: goopg's
      `nestloopCost` has no semi/anti early\-exit arm and joins get no
      index\-only inner → M0145\-0008l.
    - Gates: units, spotcheck, sf025 96/96, acceptance arm, fire\-set,
      ea\-ratchet 52/52.
    Movement: yes — CATEGORIES-EXCL-MATCH TPC-H parameterisation 7 -> 6, aggregation-strategy 4 -> 3, sort-strategy 9 -> 8, rendering 3 -> 4 (Q22 net -2); SF0.25 unchanged.

- [x] **M0145\-0008l — nestloop semi/anti costing follows
  `final\_cost\_nestloop`**: goopg's `nestloopCost` \(cost\_funcs.go\) charges the
  full inner rescan for every outer row of a SEMI/ANTI \(or inner\-unique\)
  nested loop. PG's `final\_cost\_nestloop` \(costsize.c:3349\+\) has a branch
  for those join types, using `compute\_semi\_anti\_join\_factors`'
  `outer\_match\_frac` / `match\_count`: matched outer rows scan only until the
  first match \(`inner\_scan\_frac = 2 / \(match\_count \+ 1\)`\), and unmatched
  ones are priced by whether the join quals are indexed. On TPC\-H Q22 with
  hash joins off, goopg's NL anti costs 5450 \+ 1500 × 55.85 = 89470. PG
  elects NL Anti \+ Index Only Scan on `order\_customer\_fkidx` at ~5881 \(its
  inner is also an index\-only path goopg does not generate for joins\).
  Kind: impl
  Parent: M0145-0008g
  - Expected movement: TPC\-H Q22 `join\-method` / `parameterisation` \(NL anti
    elected\); check for Q21 / TPC\-DS NOT EXISTS moves with the fire\-set gate
    and ea\-ratchet.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008l-nestloop-semi-anti-costing.md`;
    evidence `analysis/m0145/m0145-0008l/`.
    - Ported `compute\_semi\_anti\_join\_factors` \(once per pair\), the
      SEMI/ANTI branch of `final\_cost\_nestloop` and `has\_indexed\_join\_quals`
      into the plain, NLI and partial nested\-loop producers.
    - TPC\-H Q4, Q21, Q22 leave `join\-method`; Q22 elects PG's Nested Loop
      Anti Join \(cost 63,838 → 28,044; PG 13,200\). TPC\-DS SF0.25 Q64/Q94
      moved \(no category change\), SF1 none. Values identical, no
      introduced timeout.
    - Gates: units, spotcheck, fire\-set \(SF0.25, SF1, TPC\-H\), sweep 96/96,
      acceptance 24 MATCH, ea\-ratchet 52/52 \(acceptance and sweep ran with
      FORCE=1: nightly batch live, values only\).
    Movement: none — CATEGORIES-EXCL-MATCH TPC-H join-method 14 -> 11 (not beyond ±3); match counts unchanged.

- [x] **M0145-0008c — recon: PG-shared attribution for the flip's NEW
  ea-ratchet key `Q95:cte:ws_wh+customer_address+date_dim+web_sales+
  web_site`** (same filing). The flip introduced a Hash Semi Join est 1
  vs actual 22 (`pg_est` null) finding; G4 forbids repinning an
  introduced key without PG-shared evidence. Run the PG nearest-scope
  estimate for this relset; then either fix the estimate or repin under
  the G4 PG-shared extension with the measured evidence.
  Kind: recon
  Parent: M0145-0008
  - Expected movement: ea-ratchet NEW findings 1 → 0 (fix or attributed
    repin).
  - **DONE 2026\-09\-24.** Design:
    `docs/design/0100-0149/m0145-0008c-ea-ratchet-pg-shared-attribution.md`;
    evidence `analysis/m0145/m0145-0008c/`.
    - At HEAD the ratchet had 3 NEW keys. A/B at `a04b5c00e` shows Q95 is
      the flip's and both Q14 keys came from M0146\-0002e `bb90e51a4`, which
      had not run the ratchet.
    - All three are PG\-shared. Q95: PG estimates the 4\-rel base and the
      full semi scope at 1, with the same 22x/12x error. Q14: the estimates
      did not change; the scorer's largest\-actual representative moved
      under a Gather, and PG's nearest scope `\{cross\_items,date\_dim,item,
      store\_sales\}` is est 1 vs 21135.
    - Repinned under the G4 PG\-shared extension \(standalone non\-code
      commit\); `make ea\-ratchet` PASS 52 vs 52.
    Movement: yes — ea-ratchet NEW findings 3 -> 0 (attributed PG-shared repin, the task's stated exit; FIXED Q49 x2, Q54, Q94 dropped from the baseline).

- [x] **M0145-0008d — grouping pathkeys follow the ORDER BY direction
  when compatible** (same filing; ledger row exists). PG plans
  `GROUP BY c … ORDER BY c DESC` as GroupAggregate over one
  `Sort … DESC` — the group clause adopts ORDER BY's direction
  (planner.c `preprocess_groupclause`, pathkeys) — keeping the Subquery
  Scan; goopg hashes or sorts twice. Port the pathkey-direction
  adoption for grouping when compatible with the ordering request.
  Kind: impl
  Parent: M0145-0008
  - Expected movement: `aggregation-strategy` / `sort-strategy` on
    `GROUP BY k … ORDER BY k DESC` shapes on the canonical parallel
    capture.
  - **DONE 2026\-09\-24 `e32442db1`.** Design:
    `docs/design/0100-0149/m0145-0008d-group-clause-follows-order-by.md`;
    evidence `analysis/m0145/m0145-0008d/`.
    - `processedGroupOrder` \(transformGroupClauseExpr direction copy \+
      preprocess\_groupclause prefix reorder\) feeds `Aggregate.GroupClause`
      and the presearch group\_pathkeys; the sort producers, both emission
      twins and EXPLAIN's Group Key follow it.
    - The five\-plan `enable\_hashagg=off` probe is identical to PG 18.3,
      pinned as `TestExplainGroupClauseFollowsOrderBy`.
    - Gates: units, spotcheck, sf025 96/96, acceptance arm, fire\-set,
      ea\-ratchet 52/52.
    - The upstream `agg\_sort\_order` query crashes the planner at HEAD →
      M0145\-0008h \(S2 escalation below\).
    Movement: yes — CATEGORIES-EXCL-MATCH TPC-H aggregation-strategy 3 -> 4, sort-strategy 8 -> 9, rendering 4 -> 3 (Q18 only; its five PK-determined group keys PG prunes to two, M0145-0008i); SF0.25 unchanged.

- [x] **M0145\-0008h — planner panic crashes the server on `GROUP BY pk …
  ORDER BY` a dependent column with an ordered aggregate** \(found
  2026\-09\-24 by M0145\-0008d; reproduces at HEAD `7a46f9fab`\). The upstream
  regress query `aggregates.out:3158`, `SELECT array\_agg\(c1 ORDER BY c2\), c2
  FROM agg\_sort\_order WHERE c2 \< 100 GROUP BY c1 ORDER BY 2` with c1 a
  primary key and a unique index on c2, panics in
  `assertSortInputTargetCoversKeys` \(`createPlan: Sort input target \[\] drops
  sort\-key column "b" of a 2\-column input row`, `sort\_input\_target.go:191`
  via `stampSortInputTarget`, planner.go:2513\), and the whole server process
  exits, dropping every session. PG plans it as Sort / GroupAggregate / Sort
  c1, c2 / Index Scan.
  Kind: impl
  Parent: M0145-0008d
  - First step: reproduce as a unit plan \(the deep\-indent fixture with `a
    PRIMARY KEY` and `GROUP BY a ORDER BY 2`\); find why the Sort's input
    target is empty for a functionally\-dependent sort key; and check why a
    planner panic is not contained to the session.

  > ## ESCALATION 2026\-09\-24 \(S2\) — a planner panic takes the whole server down
  >
  > M0145\-0008h: one SELECT \(the upstream `aggregates` regress query\) kills
  > the server process for every session. It is filed and not selected
  > ahead of the banner, per S2; the owner decides its placement.
  >
  > **CORRECTION 2026\-09\-24 \(loop, M0145\-0008h\):** re\-checked on a HEAD
  > build: `serveConn` recovers the panic, so only the SESSION is dropped
  > \(`connection closed reason=panic`\); the server process stays up. The
  > "kills the server" wording above was a misreading of a job\-control line.
  > The defect is fixed by `39751edc9`; see below.
  - **DONE 2026\-09\-24 `39751edc9` \(comment fix `71b5b84d6`\).** Design:
    `docs/design/0100-0149/m0145-0008h-ordered-grouping-passthrough-snapshot.md`.
    - Root cause: the ORDER BY key appends the PK\-dependent column as an
      aggregate passthrough AFTER the grouping\-path snapshot;
      `electOrderedGrouping` rebuilt its winner from the snapshot, and the
      copy\-back erased the passthrough.
    - Fix: the loop declines \(`agg\-surface\-grew\-after\-snapshot`\); the normal
      ordered arm sorts over `agg.node`. The plan now equals PG's
      aggregates.out line for line; `TestAggSortOrderPassthroughOrderBy` pins
      the plan and rows.
    - Gates: units, spotcheck, sf025 96/96 \(shapes 99/99 same\), acceptance
      arm, fire\-set, ea\-ratchet; TPC\-H categories unchanged.
    - Found a PRE\-EXISTING wrong result with the same root → M0145\-0008m.
    Movement: none — no corpus plan changed; a regress case's panic is gone.

- [x] **M0145\-0008m — WRONG RESULTS: a PK\-dependent column reads NULL when
  grouping elects an index\-ordered input** \(found 2026\-09\-24 by
  M0145\-0008h; reproduces at HEAD `714738019`\). `SELECT sum\(c1\), c2 FROM
  agg\_sort\_order GROUP BY c1` \(c1 PRIMARY KEY\) returns `1|`, `2|`, … with c2
  NULL; PG returns the c2 values. The grouping election picks
  `indexOrderedAggInput`'s Index Only Scan on the primary key, whose coverage
  check \(`buildIndexOrderedScan`, groupagg\_indexorder.go\) reads
  `aggNode.Passthrough`, still empty at election time. The functionally
  dependent c2 is appended as a passthrough later, during target
  resolution \(`resolveTargetsAfterAggregate` /
  `resolveExprAfterAggregate`, planner.go\), and reads a column the narrowed
  child does not produce.
  Kind: impl
  Parent: M0145-0008h
  - First step: have the index\-ordered variant \(and any grouping candidate
    whose child narrows\) know every column referenced above the aggregate
    before the election. Either collect the functionally\-dependent
    passthroughs before grouping paths are built, or make the lazy append
    re\-validate the elected child and fall back to a plain\-scan input.
    Pin with the values probe in `analysis/m0145/m0145\-0008h/probe.txt`.
  - **DONE 2026\-09\-25 \(`356cb8b11`\).** Design doc
    `docs/design/0100\-0149/m0145\-0008m\-funcdep\-passthrough\-before\-election.md`;
    evidence `analysis/m0145/m0145\-0008m/`.
    - `prefetchFuncDepPassthroughs` resolves the target list / ORDER BY /
      DISTINCT ON columns before `createGroupingPaths`, skipping aggregate
      arguments, sub\-selects and grouping sub\-expressions \(the fire set
      caught a needless Q23 passthrough in the first cut\).
    - Probe matches PG row for row; full `TestPort\_RegressSuite` PASS; fire
      set none at both scales.
  Movement: none

  > ## ESCALATION 2026\-09\-24 \(S2\) — wrong results
  >
  > M0145\-0008m: `GROUP BY pk` selecting a functionally dependent column
  > returns NULL for that column when the grouping election picks the
  > index\-ordered Index Only Scan input. Filed and not selected ahead of the
  > banner, per S2; the owner decides its placement.
  >
  > **OWNER ANSWER 2026\-09\-25 \(delegated\):** placed third in banner
  > item 2a — wrong results on a narrower election shape than 0008r /
  > 0008s but still live query corruption.

- [x] **M0145\-0008i — multi\-relation `remove\_useless\_groupby\_columns`**:
  PG drops GROUP BY columns that a primary key or unique NOT NULL index of
  their relation makes redundant, for every relation in a join
  \(`remove\_useless\_groupby\_columns`, initsplan.c\). goopg's
  `pruneUselessGroupByColumns` handles only a single\-relation FROM. TPC\-H Q18
  groups on five keys that PG prunes to `\(c\_custkey, o\_orderkey\)`. With five,
  the ORDER BY prefix now reorders goopg's group clause and a GroupAggregate
  wins; with two, PG keeps HashAggregate \+ Sort.
  Kind: impl
  Parent: M0145-0008d
  - Expected movement: TPC\-H Q18 `aggregation\-strategy` / `sort\-strategy`
    \(and its Group Key rendering\) on the canonical parallel capture.
  - **DONE 2026\-09\-25.** Design:
    `docs/design/0100-0149/m0145-0008i-groupby-prune-every-relation.md`;
    evidence `analysis/m0145/m0145-0008i/`.
    - Pruning runs per base\-relation binding and unions the drops
      \(PG's `surplusvars`\); synthesised \(OID 0\) and view bindings are
      skipped explicitly; no outer\-join guard, as in PG.
    - TPC\-H Q18 and Q10 print PG's exact Group Key; Q18 elects PG's
      HashAggregate \+ Sort \(sort\-strategy 9→8, rendering 3→2\). TPC\-DS
      Q39/Q64 group\-key text only. Values identical; alternating A/B shows
      no timing regression.
    - Gates: units, spotcheck, fire\-set \(SF0.25, SF1, TPC\-H\), sweep 96/96,
      acceptance 24 MATCH, ea\-ratchet 52/52 \(FORCE=1 where the nightly was
      live\).
    Movement: none — CATEGORIES-EXCL-MATCH TPC-H sort-strategy 9 -> 8, rendering 3 -> 2 (within ±3); match counts unchanged.


