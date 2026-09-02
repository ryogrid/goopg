# TODO — planner refactor take 2

Execution checklist for [08-target-design.md](08-target-design.md), gated by
[09-verification-and-acceptance.md](09-verification-and-acceptance.md).

**One checkbox ≈ one commit.** An item that would move two planner inputs at
once is split before it is started (08 §1 P5).

Legend: `[ ]` not started · `[~]` in progress · `[x]` done · `[-]` dropped
(with a reason and a ledger row) · `[!]` blocked (blocker named inline).

When closing an item, rewrite its line as:

```
- [x] P1-04 <title> — <commit>; gates: <list>; artifacts: <paths>
```

---

## Ground rules (do not start an item without these)

1. **No query-specific forcing**, no penalty multipliers, no shape preferences
   (08 §1 P3, 09 §8).
2. **Plan-shape changes are timed on both suites** — TPC-H SF=1 and TPC-DS
   SF0.5, fresh server per arm (09 §1 R1/R2, 09 §6).
3. **Never `-count=1`** in a gate; **never `git commit --no-verify`** for code.
4. **Every deferral gets a `.ralph/deferral_ledger.md` row** with an upstream
   `postgres/` citation and a resume point (08 §1 P6).
5. **Sibling paths move together** (08 §1 P4): the two cardinality estimators,
   the three column-stats resolvers, the two NLI routes — until Phase 6 removes
   them.
6. **Verify both candidates were generated** before concluding a cost bug
   (09 §1 R4).

---

## Phase 0 — Instruments (no planner behaviour may change)

Exit: the parity instrument produces a committed baseline for both suites and
reports `changed=0` against a pre-P0 goopg capture.

- [x] **P0-01** Node-type EXPLAIN coverage test — `7677faaed`; gates:
      `TestEveryPlanNodeTypeHasAnExplainArm`,
      `TestEveryPlanNodeWithChildrenIsWalked`,
      `TestDescribePlanHasExactlyOneTypeNameFallthrough`, units — all green.
      **Found and fixed more than specified.** 18 node types had no arm and
      printed their Go type name live (21 regress-diff lines in the
      2026-09-02 nightly came from three of them). Two *covered* arms returned
      `%T` themselves. And four types carried children `planChildren` never
      walked, so `SELECT DISTINCT ON (k)` rendered as **one line** where PG
      renders Unique/Sort/Seq Scan — the M0125-0037(i) truncation class. Fixing
      the RecursiveUnion child-walk then exposed a doubled `CTE <name>` header
      (the recursive self-reference is a `CTEScan` over the working table); it
      now renders PG's leaf `WorkTable Scan on <name>`, making goopg's
      WITH RECURSIVE plan structurally identical to PG's.
- [ ] **P0-02** EXPLAIN emits the chosen path's real `(startup, total)`, `rows`
      and `width`; `COSTS OFF` suppresses them. Replace both literal
      `cost=0.00..0.00` sites in `internal/executor/operators_explain.go`.
      Carry the numbers onto the plan node at `createPlan` time rather than
      back-pointing to the `Path`.
      Also add the four cost keys to `planToJSON`, the third render site,
      which emits none today (impl/P0-A §3.5).
      **Corrected sequencing (impl/P0-A §3):** `estimate-audit`'s parser accepts
      arbitrary cost/width (`audit.go:51`) and its `--reference` is the *PG*
      side, so neither needs updating. What invalidates are the two
      **goopg-vs-goopg** baselines — `plan-gate`'s snapshot and the TPC-DS SF05
      `plans-*.txt` channel — re-pinned in the same commit. The "do not start
      until P0-05/P0-06" precondition is withdrawn.
      `rows=` keeps sourcing `EstimateRows` in this commit; switching it to
      `Path.Rows` is a separate item.
      *design: 08 §3 P0-2, impl/P0-A §3; gate: units + a test comparing rendered
      numbers to `finalPath()`; `estimate-audit` green.*
- [ ] **P0-03** Nodes above the seam print costs derived from the legacy
      estimate rather than zeros; a test asserts no node renders
      `cost=0.00..0.00`. *design: 08 §3; gate: units.*
- [ ] **P0-04** Align EXPLAIN's relation-name suffix numbering with
      `select_rtable_names`. De-duplication already exists
      (`internal/executor/explain_names.go`); the numbering diverges, and the
      `shape_mismatches = 46` figure was attributed to its absence, so
      re-measure after aligning. *design: 08 §3 P0-3; gate: units + regress;
      re-pin the goopg-vs-goopg baseline in the same commit.*
- [ ] **P0-04b** Fix the EXPLAIN mode asymmetry: the plain walker sources
      `rows=` from `attachedFilterNode`, the ANALYZE walker from the node, so
      the two modes can print different estimates for the same filtered scan.
      *design: 08 §3 P0-3; gate: units.*
- [x] **P0-04c** — `f2ac4fdfc`; gates: `TestFlagProvenanceEnvIsGenerated`,
      `TestFlagProvenanceTableCoversPlannerEnv`, new
      `TestFlagProvenanceDetectorSeesHelperWrappedReads`, full
      `internal/optimizer` — green. The completeness guard's detector matched
      only literal `os.Getenv("GOOPG_…")` and this flag is read through a
      helper, so the row **and** a `go/ast` string-literal detector landed
      together. Resolved multiplier unchanged at 1.0, so no plan moved.
      ~~Add `GOOPG_INDEX_PROBE_MULT` to
      `internal/optimizer/flaglabels.go` so it reaches the generated
      `scripts/planner-flags.env` — the one plan-shaping knob artifacts cannot
      currently state. **Also fix the guard that missed it**: the detector
      `flaglabels_test.go:94` matches only literal `os.Getenv("GOOPG_…")`, and
      this flag is read through the `envFloatDefault` helper. Replace it with a
      `go/ast` string-literal walk (impl/P0-A §7).~~
- [ ] **P0-05** `plan-parity` capture mode: capture EXPLAIN from goopg and from
      PG for TPC-H 22 and TPC-DS 99; commit the PG side as a fixture
      (`bench/tpch/plans-pg/`, `bench/tpcds/plans-pg/`). *design: 08 §3, 09
      §3.1; gate: the capture runs and is reproducible.*
- [ ] **P0-06** `plan-parity` diff mode: normalised tree comparison, per-query
      verdict (`MATCH` / `SHAPE-DIFF` / `MISSING-NODE` / `ERROR` / `TIMEOUT`),
      the nine-category divergence classification, and the corpus roll-up line.
      *design: 09 §3.1; gate: unit tests over recorded plan pairs.*
- [ ] **P0-07** Commit the baseline roll-up for both suites and record it in
      09 §7.1 as the starting numbers for bars A1/A2. *gate: 09 §5 P0 row.*
- [ ] **P0-08** Re-pin the stale plan baseline. **Corrected (impl/P0-B §5):**
      the nightly calls `make plan-diff LABEL=m0077-final` from
      `ci/batch/stages/stage-tpch.sh:234` with `|| true`, *deliberately*
      informational — it does not use `plan-gate`, and it does not silently pass.
      Meanwhile `make plan-gate`'s `ls -t` today selects `warm-stats-base.txt`
      (Aug 2026), not `m0077-final`. The defect is the four-month-stale pin, and
      the re-pin is **three coordinated edits in one commit**: commit
      `plan_snapshots/take2-p0-<date>.txt`; update `stage-tpch.sh:234`'s
      `LABEL=`; update the hardcoded label in `summarize.py:689`.
      *design: 08 §3 P0-4.*
- [ ] **P0-09** **Rescoped (impl/P0-B §6).** Change the oracle timer to
      millisecond resolution (`scripts/tpcds-sf05-regression.sh:498-500`), but
      do **not** trigger a re-capture for it. Corrections: **54** of 95 `OK`
      rows read `0`, not 83; and field 5 (`secs`) is read by *nothing* — the
      compare path takes fields 2/3/4 only (`:796-801`) and the fixture header
      says `secs are machine-specific; rows and ck are the fixture`. A
      standalone re-capture would truncate a git-tracked fixture for a column
      with no readers. The resolution lands on the next capture required for
      another reason, under design D5. *design: 08 §3 P0-5.*
- [-] **P0-10** ~~Fix TPC-DS row-anchor consumption~~ — **already fixed; claim
      was stale.** `ci/batch/lib/summarize.py:651-656` reads `r["expected_rows"]`
      with an explanatory comment at `:652`; the TPC-H path correctly reads
      `r["rows"]` at `:576-580`. Fixed by `63056c544` (2026-07-30), a month
      before this bundle was written. No code change. *evidence: impl/P0-B §7.*
- [ ] **P0-11** Path provenance tracing — **extends an existing channel**
      (impl/P0-B §8). `internal/optimizer/joinsearchtrace.go` already emits
      `DPTRACE` blocks under `GOOPG_PGSHAPED_DP_TRACE=1` at *join-pair*
      granularity, parsed by `estimateaudit/enumtrace.go`. Add a third `path`
      record type from `addPath` (`path.go:555`) **and `addPartialPath`
      (`:562`)** — 12 + 1 call sites, `producer` passed as an argument, never
      recovered from the stack. The emitter and the matching `EnumPath` parser
      arm **must land in one commit**: `enumtrace.go` counts unparseable
      `DPTRACE` lines as `Malformed`, so a lone emitter would look like a
      regression in the existing instrument. *design: 09 §1 R4; gate: units,
      `Malformed == 0`.*
- [ ] **P0-12** Align the goopg TPC-H bench cluster's memory settings with the
      PG reference cluster's. Today PG sets `work_mem = 64MB` and
      `effective_cache_size = 2GB` explicitly while goopg's conf leaves both at
      boot defaults (512MB and 4GB), so the headline 9.9× is measured with
      goopg holding an **8× `work_mem` advantage** and any `work_mem`-sensitive
      cost comparison between the engines is meaningless. **Measured 2026-09-02
      (impl/P0-B §2.2), and wider than recorded:** `shared_buffers` is also 4×
      apart (PG 512MB, goopg 2GB). It cannot change a plan (`effective_cache_size`
      is the cost-model input) but it changes runtime, so it is aligned too.
      All other cost/collapse/parallel GUCs were verified identical. Do this **before**
      P2-02b, which changes the boot default — doing P2-02b first would swing
      goopg from 8× more memory to 16× less and read as a catastrophic
      regression that is really a configuration change. *design: 09 §6.4; gate:
      plan-parity + timing both suites; expect plans to move.*
- [ ] **P0-13** **Flip `GOOPG_PGSHAPED_COLLAPSE` on**, as the parity
      instrument's **positive control**. Pre-registered blast radius, pinned by
      two git-tracked tests: `TestCollapseIsAControlOnTheTPCHCorpus` (0 of 22
      TPC-H eligible) and `TestCollapseEligibilityOfTheTPCDSCorpus` ({Q72, Q75}
      of 99). So the expected result is **TPC-H `changed=0`** and **exactly two
      TPC-DS plans moved** — an instrument that reports anything else is
      measuring itself wrong, which is what makes this a control rather than a
      structural change. It also re-opens a NO-GO that the tree itself records
      as void (07 §4.2). Zero dependency on P1 or P2. *design: 08 §6.2; gate:
      plan-parity both suites + timing for Q72/Q75.*

---

## Phase 1 — Statistics fidelity

Exit: estimate ratchet does not regress, parity budget does not grow, no query
slower than 1.2×, S-cold/WARM gap narrows.

### 1a — Relation and index statistics

- [ ] **P1-01** ANALYZE and VACUUM collect and persist per-index `relpages`,
      `reltuples` and btree tree height; the catalog index carries them.
      *design: 08 §4.1; oracle: `vac_update_relstats`, `_bt_getrootheight`.*
- [ ] **P1-02** `costIndexScan` reads the real index geometry; retire
      `estimateIndexGeometry`'s synthesis. *design: 08 §4.1; gate: plan-parity
      both suites + timing (this moves index-vs-bitmap crossovers).*
- [ ] **P1-03** VACUUM **and autoanalyze** persist `reltuples`/`relpages`
      durably; today both update memory only and the sidecar is written by SQL
      `ANALYZE` alone, so an autovacuum-maintained cluster plans from stale
      sizes after every restart. *design: 08 §4.1.*
- [ ] **P1-03b** `TRUNCATE` resets `Table.Stats`; `ANALYZE` invalidates the
      cached plans of the relations it touched (today it is planned as a
      `Utility` statement and only DDL invalidates). *design: 08 §4.1.*
- [ ] **P1-04** `allvisfrac` reaches the search `RelOptInfo`; index-only costing
      uses the real visible fraction. *design: 08 §4.1.*
- [ ] **P1-05** Align the never-analyzed fallback with `estimate_rel_size`
      (density, 10-page floor); make `GOOPG_RELSIZE_FALLBACK` unconditional and
      retire the flag. *design: 08 §4.1; gate: S-cold arm timing.*

### 1b — ANALYZE algorithm

- [ ] **P1-06** Two-stage block sampling (`BlockSampler` + Vitter) at
      `300 × stattarget`, replacing the full heap scan plus reservoir sample.
      *design: 08 §4.2; oracle: `acquire_sample_rows`.*
- [ ] **P1-07** Fix the `ALTER TABLE … SET (n_distinct = …)` override, which
      writes only the absolute field while `StaDistinct()` consults the
      fraction first — so the override is silently ignored on any column whose
      sampled fraction exceeds 10 %. The Haas–Stokes estimator itself is
      already correct (07 §3.11 item 1); ledger row 777 is stale and should be
      closed. *design: 08 §4.2.*
- [ ] **P1-08** Adopt 18.3's `analyze_mcv_list` admission rule; goopg's 1.25×
      margin over-admits MCV entries on near-uniform columns, and each admitted
      entry displaces a histogram bound. *design: 08 §4.2.*
- [ ] **P1-09** Histogram bound count `min(stattarget, ndistinct - num_mcv)`
      over the non-MCV portion. *design: 08 §4.2.*
- [ ] **P1-10** `compute_index_stats` for expression indexes. *design: 08 §4.2.*

### 1c — Statistics storage

- [ ] **P1-11** TOAST support in the catalog heap writer so wide-text
      histograms persist; today the row is silently dropped and `orders`,
      `customer` and `partsupp` lost trailing-column rows *and* their size row.
      *design: 08 §4.3; gate: full regress suite (catalog format), re-init the
      data dir.* If deferred to a bounded-width interim, file the ledger row.

### 1d — Restriction selectivity

- [ ] **P1-11b** `convert_to_scalar` for non-numeric types —
      `convert_string_to_scalar`, `convert_timevalue_to_scalar` and the network
      variants. Today `numericValue` handles only the numeric family, so
      `bucketFraction` returns a flat **0.5** for `date`, `timestamp`, `text`,
      `varchar`, `char` and `bool`: every histogram interpolation on a date
      column lands mid-bucket by construction. **Highest-value single item in
      Phase 1** — date-window predicates are the dominant restriction shape in
      both suites. *design: 08 §4.4; gate: plan-parity + timing both suites.*
- [ ] **P1-12** Port `clauselist_selectivity` as a real function (replacing the
      inlined AND product), with `RestrictInfo` selectivity caching.
      *design: 08 §4.4; oracle: `clausesel.c`.*
- [ ] **P1-13** `RangeQueryClause` pairing: `x > a AND x < b` on one variable
      estimated as `s1 + s2 - 1` with the `DEFAULT_RANGE_INEQ_SEL` floor.
      With P1-11b this takes a TPC-H Q6-shaped one-year window from ~0.31 to
      the true ~0.14. *design: 08 §4.4; gate: plan-parity + timing.*
- [ ] **P1-14** `nulltestsel` — `IS NULL` / `IS NOT NULL` has no arm at all and
      falls to a generic default, so the `NullFrac` ANALYZE collects and
      persists is never read for the one clause it exists to answer.
      *design: 08 §4.4.*
- [ ] **P1-14b** Remaining per-clause estimators: general `scalararraysel`,
      `patternsel` for LIKE/regex beyond the access-path prefix rewrite,
      `rowcomparesel`, `booltestsel`, `var_eq_non_const`; align every
      `DEFAULT_*` constant with PG. *design: 08 §4.4.*

### 1e — Join selectivity

- [ ] **P1-15** MCV pairing in `eqjoinsel_inner` (pairwise match through the
      equality operator; `matchprodfreq`/`unmatchfreq`/remainder terms), with
      MCV equality compared by `oprcode` rather than rendered text.
      *design: 08 §4.5; gate: plan-parity + timing both suites.*
- [ ] **P1-16** Re-diagnose TPC-H Q9's cardinality error with `estimate-audit`.
      Its recorded explanation — single-`nd` pricing of a two-column join — was
      retired by M0127-P5.6-f, which folds every equi-pair in both estimator
      arms (07 §3.11 item 3). Close ledger rows 779/781/784 as stale and file
      what the audit actually shows. *design: 08 §4.5; gate: `estimate-audit`
      final-joinrel bar on Q9.*
- [ ] **P1-17** `eqjoinsel_semi` MCV arm, `nd2` clamped by inner rows, the
      `(1-nullfrac1)` factor. Note MCV pairing exists in production **only** in
      the semi/anti arm (`semiPairMatchFraction`). *design: 08 §4.5.*
- [ ] **P1-18** Port `calc_joinrel_size_estimate`'s full jointype switch into
      the search arm. `calcJoinrelSize` has **no join-type branch at all** —
      the LEFT/FULL floors and SEMI/ANTI arms live only in the legacy
      `estimateJoin`, so the arm that chooses the plan sizes an outer join as
      an inner join and a LEFT can estimate fewer rows than its preserved
      input. *design: 08 §4.5; gate: plan-parity + timing both suites.*
- [ ] **P1-19** `isunique` in the `examine_variable` analogue via
      `has_unique_index`, with PG's nullfrac derating in the FK formula.
      *design: 08 §4.5.*
- [ ] **P1-20** `nconst_ec` correction — EquivalenceClasses carry a const flag
      so `1/ref_tuples` stops double-counting a pushed-down `var = const`.
      *design: 08 §4.5.*
- [ ] **P1-21** Delete the `max(outer,inner)` fallback cap (M0126-0010), which
      has no upstream counterpart, once P1-15/16 show the backstop unneeded.
      It is guarded (it fires only when no key was proven and every residual
      factor was a default), so this is a cleanup, not a correctness fix.
      *design: 08 §4.5; gate: plan-parity + timing.*
- [ ] **P1-28** `pg_stats.correlation` is always NULL although correlation is
      collected, persisted in `stakind3` and consumed by the index cost model —
      a stale-comment bug in `internal/catalog/pgstats.go` that makes the
      statistic invisible to anyone diagnosing a plan. *design: 08 §4.3.*

### 1f — Extended statistics

- [ ] **P1-22** Build extended statistics during ANALYZE
      (`BuildRelationExtStatistics`: ndistinct, dependencies, MCV) and store
      them in `pg_statistic_ext_data`. *design: 08 §4.6.*
- [ ] **P1-23** `statext_clauselist_selectivity` with the `estimatedclauses`
      bitmap, `dependencies_clauselist_selectivity`,
      `mcv_clauselist_selectivity`, `choose_best_statistics`,
      `statext_is_compatible_clause`. *design: 08 §4.6; gate: plan-parity both
      suites.*
- [ ] **P1-24** `estimate_multivariate_ndistinct` for GROUP BY.
      *design: 08 §4.6.*

### 1g — Grouping and resolvers

- [ ] **P1-25** Size DISTINCT and set-op outputs through `estimate_num_groups`
      (today rows pass through unchanged). *design: 08 §4.7.*
- [ ] **P1-26** Collapse the three column-stats resolvers into one
      `examine_variable` analogue over a single node-type arm list; an
      index-probed leaf gains MCV and histogram access. *design: 08 §4.8;
      sibling rule.*
- [ ] **P1-27** CTE output statistics, replacing the `initialRelRows` body-count
      fallback that over-estimates filtered CTE scans. *design: 08 §4.7.*

---

## Phase 2 — Cost-model completeness

Exit: every cost GUC demonstrably changes at least one plan; parity budget does
not grow; no query slower than 1.2×.

- [ ] **P2-01** Introduce the planner context (`PlannerInfo`/`PlannerGlobal`
      analogue) built once at `optimizer.Plan` entry from the session, carrying
      `costParams`, collapse limits, GEQO parameters, `enable_*`, and parallel
      settings. Thread it to both `defaultCostParams()` call sites.
      *design: 08 §5.1.*
- [ ] **P2-02** Session cost GUCs reach the planner; a test asserts each of
      `seq_page_cost`, `random_page_cost`, `cpu_tuple_cost`,
      `cpu_index_tuple_cost`, `cpu_operator_cost`, `effective_cache_size`,
      `work_mem`, `parallel_setup_cost`, `parallel_tuple_cost` changes a plan.
      *design: 08 §5.1; gate: 09 §5 P2 row.*
- [ ] **P2-02b** Correct `work_mem`'s `BootVal` from `512MB` to PostgreSQL
      18.3's `4MB`. The planner's copy is hard-wired to the goopg value, so
      hash tables appear to fit where PostgreSQL's would batch and sorts stay
      in memory where PostgreSQL's would spill. **Requires P0-12 first** — the
      bench clusters must already carry explicit matching values, or this looks
      like a catastrophic regression (09 §6.4). Expect many plans to move;
      lands alone, both suites timed. *design: 08 §5.1; gate: plan-parity +
      timing both suites, `TestSampleConfigCoversRegistry`.*
- [ ] **P2-02c** Move the six process-global GUC bridges
      (`enable_memoize`, `enable_nestloop_index`, `enable_hashagg`,
      `enable_presorted_aggregate`, `geqo`, `geqo_threshold`) onto the planner
      context. Today a `SET` in one session changes the planner for every
      session, while a cached plan may ignore the same `SET`.
      *design: 08 §5.1; gate: a cross-session isolation test.*
- [ ] **P2-03** `hash_mem_multiplier` consumed via a `get_hash_memory_limit`
      analogue, shared with the executor's `hashsize`. *design: 08 §5.1.*
- [ ] **P2-04** Plan-cache correctness under live GUCs — the planner context is
      part of the cache key, or a GUC change invalidates; a test asserts
      `SET random_page_cost` changes the cached plan. Removes the
      `plannerScanTogglesActive` bypass. *design: 08 §5.1, risk R7.*
- [ ] **P2-05** `enable_*` via `Path.DisabledNodes` as PG 18 does; delete
      producer-skipping. All `enable_*` GUCs become live; retire
      `enable_nestloop_index`. *design: 08 §5.2; gate: an `enable_X=off` test
      per flag, plan-parity + timing.*
- [ ] **P2-06** `cost_material` and a Material path; the merge-join
      materialise-inner decision becomes a cost comparison. *design: 08 §5.3.*
- [ ] **P2-07** `cost_rescan` — nested-loop inner and CTE re-execution stop
      being free. *design: 08 §5.3; gate: plan-parity + timing; expect NL
      counts to move.*
- [ ] **P2-08** `cost_subplan` in `(startup, total)` PG units with hashed and
      non-hashed arms. *design: 08 §5.3.*
- [ ] **P2-09** `btcostestimate` completeness: descent cost, `num_sa_scans` for
      ScalarArrayOp, the unique-index single-tuple clamp, per-tuple index qual
      cost. *design: 08 §5.3.*
- [ ] **P2-10** `compute_semi_anti_join_factors` and the semi/anti early-out in
      nestloop and hashjoin costing. *design: 08 §5.3.*
- [ ] **P2-11** `estimate_hash_bucket_stats`: bucket-size and MCV-frequency
      skew terms in hash-join costing. *design: 08 §5.3; depends on P1-15.*
- [ ] **P2-12** `mergejoinscansel` start/end selectivities in merge-join
      costing. *design: 08 §5.3.*
- [ ] **P2-13** Bitmap lossy-page handling to match `tbm_calculate_entries`
      **and** removal of the double charge, in ONE commit. Landing the removal
      alone is a known regression (TPC-DS Q72 73 s → timeout). *design: 08 §5.4;
      gate: plan-parity + timing both suites, all bitmap queries.*

---

## Phase 3 — Search coverage

Exit: every PG-only join spine is OFFERED at its level or has a named reason;
Q72-class queries produce one search problem; `join-order` diffs decrease.

- [ ] **P3-01** `make_outerjoininfo`-equivalent `SpecialJoinInfo` construction
      with the full field set (`min_lefthand`, `min_righthand`, `syn_*`,
      `lhs_strict`, `semi_can_hash`, `semi_operators`, `semi_rhs_exprs`).
      *design: 08 §6.1.*
- [ ] **P3-02** `distribute_qual_to_rels` with outer-join delay rules
      (`check_outerjoin_delay`); a qual is **placed**, not copied down.
      Supersedes `pushInnerJoinInputQuals` copying (ledger 802: the executor
      evaluates it twice). *design: 08 §6.1.*
- [ ] **P3-03** `join_is_legal` consulting real `SpecialJoinInfo`s; the DP
      builds outer, semi and anti joinrels directly. *design: 08 §6.1.*
- [ ] **P3-04** Delete `splitOuterSpine` and the `pinnedOuter()`
      whole-statement decline; a mixed comma + `LEFT JOIN` FROM list becomes
      **one** search problem. *design: 08 §2.2, §6.1; gate: plan-parity both
      suites + timing; TPC-DS Q72 is the witness.*
*(The collapse flip moved to Phase 0 as **P0-13** — it is a two-query change
with a pre-registered blast radius, which makes it the parity instrument's
positive control rather than a Phase 3 structural item. See 09 §5.)*
- [ ] **P3-05** Retire `GOOPG_PGSHAPED_COLLAPSE` once P3-04 makes it the only
      jointree path; `from_collapse_limit`/`join_collapse_limit` take their
      upstream meaning including `join_collapse_limit = 1`. *design: 08 §6.2.*
- [ ] **P3-06** `standard_qp_callback` analogue computing `query_pathkeys`,
      `group_pathkeys`, `window_pathkeys`, `distinct_pathkeys`,
      `sort_pathkeys`; complete `has_useful_pathkeys` so ORDER BY and GROUP BY
      can motivate an index path. *design: 08 §6.3; depends on P2-01.*
- [ ] **P3-07** `param_source_rels` (today hard-coded 0). *design: 08 §6.4.*
- [ ] **P3-08** `reduce_unique_semijoins` — unique-inner SEMI becomes INNER.
      Blocker to clear first: SEMI's left-only `Output()` re-indexing (ledger
      794). *design: 08 §6.4.*
- [ ] **P3-09** Widen `RelSet` past 16 base relations. *design: 08 §6.5.*
- [ ] **P3-10** Finish the GEQO wiring: `geqo` and `geqo_threshold` are bridged
      (as process-global atomics — see P2-02c), while `geqo_effort`,
      `geqo_pool_size`, `geqo_generations`, `geqo_selection_bias` and
      `geqo_seed` reach nothing and `geqoSearch` runs at a hard-coded effort of
      5. Move all seven onto the planner context. GEQO is unobservable until
      P0-13 lands, since a problem never approaches 12 items today.
      *design: 08 §6.5; depends on P2-01, P0-13.*
- [ ] **P3-11** Answer "why does the NLI arm lose 23 of 25 times" using P0-11
      provenance, and act on the finding. *design: 08 §6.4; gate: the NL census
      gap (1 vs 25) moves.*
- [ ] **P3-12** Delete `reorderCommaFromByCardinality` — the pre-search greedy
      reorder that biases the search's input. *design: 08 §6.6; gate:
      plan-parity + timing both suites, own commit.*

---

## Phase 4 — Upper planner as paths

Exit: `aggregation-strategy` and `sort-strategy` diffs decrease; no correctness
delta.

- [ ] **P4-01** `PathTarget` analogue on `RelOptInfo`, with
      `make_group_input_target` / `make_window_input_target` /
      `make_sort_input_target` / `apply_scanjoin_target_to_paths`.
      *design: 08 §7; unlocks P6-02.*
- [ ] **P4-02** Upper `RelOptInfo`s (`GROUP_AGG`, `WINDOW`, `DISTINCT`,
      `ORDERED`, `FINAL`) with pathlists. *design: 08 §7.*
- [ ] **P4-03** Promote `PathSort` to an upper-rel path. It already has a
      `createPlanNode` arm but is only ever constructed as a merge-join child,
      so it never competes with a hashed alternative. *design: 08 §7.*
- [ ] **P4-04** Bounded / top-N sort (`cost_sort`'s `limit_tuples` arm) — the
      largest recorded `ORDER BY … LIMIT` win. *design: 08 §7; gate: timing.*
- [ ] **P4-05** Incremental Sort node and `create_incremental_sort_path`.
      *design: 08 §7; closes a `MISSING-NODE` entry (09 §7.1 A3).*
- [ ] **P4-06** `create_grouping_paths` / `add_paths_to_grouping_rel` offering
      sorted and hashed aggregation priced by `cost_agg` including the hash
      spill arm; retire `applyIndexOrderedGroupingRule`,
      `applyPresortedAggregateRule`, `applyEnableHashAggRule`.
      *design: 08 §7; gate: plan-parity + timing.*
- [ ] **P4-07** `create_distinct_paths` (hashed / sorted / unique-over-sorted).
      *design: 08 §7; depends on P1-25.*
- [ ] **P4-08** `tuple_fraction` reaches every upper rel, not only the join
      search. *design: 08 §7.*
- [ ] **P4-09** `create_window_paths` and set-operation paths, priced.
      *design: 08 §7.*

---

## Phase 5 — Parallelism in the path model

Exit: `parallelism` diffs decrease; the serial control arm is unchanged.

- [ ] **P5-01** `consider_parallel` per rel (`set_rel_consider_parallel`).
      *design: 08 §8.*
- [ ] **P5-02** `create_plain_partial_paths` populating `PartialPathlist`;
      `computeParallelWorkers` moves into path generation.
      *design: 08 §8; note `addPartialPath`'s only caller today is the
      production-dead `generateScanPaths`.*
- [ ] **P5-03** Parallel eligibility for index, index-only and bitmap scans
      (`drivingScan` recognises only SeqScan and BitmapHeapScan today, so PG's
      Parallel Index Only Scan is unreachable in general). *design: 08 §8;
      closes a `MISSING-NODE` entry.*
- [ ] **P5-04** `generate_useful_gather_paths` producing `PathGather` and
      `PathGatherMerge` priced by `cost_gather`/`cost_gather_merge`, with
      `createPlanNode` arms. *design: 08 §8.*
- [ ] **P5-05** Re-decide the `Gather Merge → Sort → Parallel scan` shape by
      cost rather than by `sortPartialRootPays`' hard-coded decline. If goopg's
      costs still choose leader-side sorting, record it as a permitted
      divergence with the existing measurement (09 §7.4 case 1).
      *design: 08 §8.*
- [ ] **P5-06** Parallel hash join as a `parallel_aware` hash path, priced.
      *design: 08 §8.*
- [ ] **P5-07** Partial aggregation as paths
      (`create_partial_grouping_paths`), replacing `splitAggregate`.
      *design: 08 §8; depends on P4-06.*
- [ ] **P5-08** Retire `MaybeAddGather`. *design: 08 §8; gate: plan-parity both
      suites, parallel and serial arms.*

---

## Phase 6 — Consolidation and deletion

Exit: byte-identical plans to the pre-deletion arm on both suites, or every
difference explained and timed.

- [ ] **P6-01** Delete the legacy cardinality estimator
      (`estimateJoin`/`EstimateRows`) and its FK/unique mirror in
      `joinkeyproof.go`; everything reads `calcJoinrelSize`.
      *design: 08 §9.1; depends on P0-02, P4-*.*
- [ ] **P6-02** Replace `baseLeaf`/`baseOffset` with the `PathTarget` and range
      table; delete `joinlayout.go`'s remapping and the `createplanroot.go`
      boundary assertions. This deletes the project's largest silent
      wrong-answer bug class. *design: 08 §9.2; gate: value-level
      `tpch-runner -diff`, never row counts (risk R5).*
- [ ] **P6-03** Delete `rewriteScanInputsWithSingleTablePredicates`.
      *design: 08 §9.3; gate: byte-identical plans.*
- [ ] **P6-04** Delete `rewriteJoinsToNLI` and `GOOPG_NLI_COSTGATE`.
      *design: 08 §9.3; depends on P3-11.*
- [ ] **P6-05** Delete the dead `reconcileNLILayout`. *design: 08 §9.3.*
- [ ] **P6-06** Retire the planner flags — `GOOPG_PGSHAPED_DP`,
      `GOOPG_PGSHAPED_COLLAPSE`, `GOOPG_RELSIZE_FALLBACK`,
      `GOOPG_INDEXKEY_HARVEST`, `GOOPG_HASH_OUTER_JOIN`,
      `GOOPG_INDEX_PROBE_MULT` — regenerating `scripts/planner-flags.env` from
      `flaglabels.go` each time. *design: 08 §9.4.*
- [ ] **P6-07** Add a `setrefs` phase if P6-02 shows the executor still needs
      explicit column resolution. *design: 08 §9.5.*

---

## Phase 7 — Acceptance

- [ ] **P7-01** Full acceptance run: both suites, S-cold and WARM, plan-parity
      roll-up, `estimate-audit --reference` ratchet, complete timing table.
      *gate: 09 §7 bars A1–A5, B5, C1–C4.*
- [ ] **P7-02** Verdict document under
      `analysis/planner-refactor-take2/acceptance-<date>/README.md` with the
      09 §6.6 header, before/after roll-ups, and an explicit statement of
      everything that got worse. *gate: 09 §9.*
- [ ] **P7-03** Update the deferral ledger for every item left open, and record
      the executor-side residuals (07 §6) as their own follow-up with resume
      points. *gate: 08 §1 P6.*

---

## Progress log

One row per closed phase. Numbers come from the 09 §6.6 artifact header.

| phase | closed | commit range | TPC-H total (goopg / PG) | TPC-DS SF0.5 total | plan-parity TPC-H | plan-parity TPC-DS | notes |
|---|---|---|---|---|---|---|---|
| baseline | 2026-08-31 | `82c05a5f6` … `6c65ceb20` | 227.0 s / 22.9 s = 9.9× | 1173 s / 536 s = 2.2× | not measured (spine `shape_mismatches` = 46) | not measured | starting state; see 07 §2 |
| P0 | | | | | | | |
| P1 | | | | | | | |
| P2 | | | | | | | |
| P3 | | | | | | | |
| P4 | | | | | | | |
| P5 | | | | | | | |
| P6 | | | | | | | |
| P7 | | | | | | | |

## Dropped and deferred

Items removed from the plan, with the reason and the ledger row. Keep the
original wording — negative results are only legible if they survive
(08 §1, 09 §9).

| item | date | reason | ledger row |
|---|---|---|---|
| P0-10 (TPC-DS row anchors inert) | 2026-09-02 | Claim was stale. `ci/batch/lib/summarize.py:651-656` already reads `expected_rows`; fixed by `63056c544` (2026-07-30), a month before the bundle was written. A `git log -S` would have refuted it. | n/a — no defect |
| P0-08's `PLAN_GATE_REQUIRE=1` "set by ci/batch" | 2026-09-02 | Withdrawn: ci/batch never invokes `make plan-gate`; it calls `make plan-diff` with an explicit pinned `LABEL` (`stage-tpch.sh:234`). The flag would never fire. | n/a |
| P0-09's standalone oracle re-capture | 2026-09-02 | Rescoped to a timer change only. Field 5 (`secs`) has no readers anywhere in `scripts/` or `ci/batch/`; a re-capture would truncate a git-tracked fixture for no measurable gain. | n/a |
