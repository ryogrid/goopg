# TODO — planner refactor take 3

Execution checklist for [08-target-design.md](08-target-design.md), gated by
[09-verification-and-acceptance.md](09-verification-and-acceptance.md).

This is a **fresh plan scoped to the remaining work** after take2 — not a
copy of take2's TODO (retained as history). Items take2 landed are recorded
below as `[x]` with their commits so the starting state is explicit; every
`[ ]` item is work still to do, re-sequenced PathTarget-first per 08 §2.3.

**One checkbox ≈ one commit.** An item that would move two planner inputs at
once is split before it is started (08 §1 P5).

Legend: `[ ]` not started · `[~]` in progress · `[x]` done · `[-]` dropped
(with a reason and a ledger row) · `[!]` blocked (blocker named inline) ·
`[>]` moved (pointer to the checkbox that executes elsewhere).

When closing an item, rewrite its line as:

```
- [x] P0-00 <title> — <commit>; gates: <list>; artifacts: <paths>
```

Each item below names its design pointer (08 §) and its gate (09 §) in 2–4
lines. Phase-closure verdict files go under
`analysis/planner-refactor-take3/<phase>-<date>/README.md` (09 §9).

---

## Ground rules (do not start an item without these)

1. **No query-specific forcing**, no penalty multipliers, no shape preferences
   (08 §1 P3; 09 §4.3 C1–C2).
2. **Plan-shape changes are timed on both suites** — TPC-H SF=1 and TPC-DS
   SF0.5, fresh server per arm; per-query and TOTAL arms are complementary
   (09 §1 R1/R2/R6; 09 §4.3 C3–C4).
3. **Never `-count=1`** in a gate; **never `git commit --no-verify`** for code
   (09 §4.3 C5–C6).
4. **Every deferral gets a `.ralph/deferral_ledger.md` row** with an upstream
   `postgres/` citation and a resume point (08 §1 P6; 09 §4.3 C7).
5. **Sibling paths move together** (08 §1 P4): the two cardinality estimators
   and the two NLI routes, until Phase 6 removes them.
6. **Verify both candidates were generated** before concluding a cost bug
   (`DPPATH`/producer, 09 §1 R4); a runtime move is attributable only if the
   plan changed (09 §1 R7); projection/join changes gate on **values**
   (`-digest` + `-diff`), never counts (09 §1 R8).

---

## Phase 0 — Instruments (complete what take2 started)

*Design: 08 §3. Gates: 09 §5 P0 row + §3 floor.*
Exit: the parity instrument produces a committed baseline roll-up for both
suites (bars A1/A2 set as `baseline + N`, 09 §4.1), and PP reports
`changed=0` against the pre-P0 goopg capture — P0 moves no plan.

Landed in take2 (record, not work):

- [x] P0-01 Node-type EXPLAIN coverage — `7677faaed`; gates:
      `TestEveryPlanNodeTypeHasAnExplainArm`, child-walk tests, units. Fixed 18
      missing arms, 2 `%T` arms, 4 unwalked child sets (WITH RECURSIVE now
      structurally identical to PG's).
- [x] P0-02/P0-03 Real costs on every node — `9cbc7661b`; gates:
      `TestNoNodeRendersZeroCost`, `TestLegacyDisplayCostIsMonotone`, units.
      `PlanCost` on the node; first finding was the width gap (P4-01's case).
- [x] P0-04b EXPLAIN `rows=` mode asymmetry — gate:
      `TestWalkersAgreeOnRowEstimate`, units. Negative control: 200×
      overstatement on filtered scans in the plain walker with the defect in.
- [x] P0-04c Flag-provenance detector — `f2ac4fdfc`; gates:
      `TestFlagProvenanceEnvIsGenerated`, detector test, optimizer suite.
      `GOOPG_INDEX_PROBE_MULT` registered; flag labels computed, never typed (R5).
- [x] P0-04d Schema qualification follows verbosity — `2a63fbe21`; gate:
      `TestSchemaQualificationFollowsVerbosity`, units. Removed a guaranteed
      rendering divergence on every scan node of every plan.
- [x] P0-09 Oracle ms timer — rescoped to a timer change only, no re-capture
      (field 5 has no readers); gate: `bash -n`.
- [x] P0-11 Path provenance (`DPPATH`) — gates:
      `TestPathTraceRecordsProducerAndVerdict`,
      `TestPathTraceVerdictSurvivesEviction`, units. `addPath` (12 sites) +
      `addPartialPath`; NLI measured at 694 offered / 23 accepted.
- [x] P0-12 Bench-cluster memory alignment — `78ef045c8`; gates: honest-control
      A/B. `work_mem` 512 MB → 64 MB, `effective_cache_size` 4 GB → 2 GB;
      248.71 s → 403.27 s (+62%), counts identical — the honest ratio is ~17.6×.

Remaining:

- [ ] P0-04 Align EXPLAIN relation-name suffix numbering with
      `select_rtable_names` (dedup exists in `explain_names.go`, numbering
      diverges); then re-measure spine `shape_mismatches` = 46, STALE until
      then (07 §2.2). Fold in P0-04e (JSON `Project`/`Filter` wrapper collapse:
      tree-shape, not estimate — schedule here, blocks nothing).
      *design: 08 §3; gate: 09 §5 P0 + units/regress; re-pin the goopg-vs-goopg
      baseline in the same commit (09 §7.4).*
- [ ] P0-05 `plan-parity` capture mode: EXPLAIN from goopg and PG for TPC-H 22
      and TPC-DS 99; commit the PG side as a fixture (`bench/tpch/plans-pg/`,
      `bench/tpcds/plans-pg/`, re-captured only on query/dataset change).
      *design: 08 §3 (spec: take2 09 §3.1); gate: 09 §5 P0 — capture runs and
      is reproducible.*
- [ ] P0-06 `plan-parity` diff mode: normalised tree comparison (costs/rows/
      widths/times in a separate column), per-query verdicts `MATCH` /
      `SHAPE-DIFF` / `MISSING-NODE` / `ERROR` / `TIMEOUT`, the nine-category
      taxonomy, and the corpus roll-up line. Report-only with a pinned
      mismatch budget; declared normalisation (strip PG standalone `Hash`).
      *design: 08 §3; gate: 09 §5 P0 — unit tests over recorded plan pairs;
      R3 applies to the instrument itself.*
- [ ] P0-07 Commit the baseline roll-up for both suites and record it as the
      starting numbers for bars A1/A2 (09 §4.1). Until measured, enforce the
      per-category monotone decrements in 09 §5 instead of invented targets.
      *design: 08 §3; gate: 09 §5 P0 row.*
- [ ] P0-08 Re-pin the stale plan baseline — three coordinated edits in one
      commit: commit `plan_snapshots/take2-p0-<date>.txt`; update
      `stage-tpch.sh:234`'s `LABEL=`; update `summarize.py:689` (nightly calls
      `plan-diff`, not `plan-gate`; `plan-gate`'s `ls -t` selects
      `warm-stats-base.txt`, not `m0077-final`).
      *design: 08 §3; gate: 09 §5 P0.*
- [ ] P0-13 Flip `GOOPG_PGSHAPED_COLLAPSE` on as the parity instrument's
      **positive control**. Pre-registered blast radius, pinned by
      `TestCollapseIsAControlOnTheTPCHCorpus` (TPC-H `changed=0`) and
      `TestCollapseEligibilityOfTheTPCDSCorpus` (exactly Q72/Q75 moved);
      re-opens the voided NO-GO. Zero dependency on P1/P2.
      *design: 08 §3; gate: 09 §5 P0 — parity both suites + Q72/Q75 timing.*

---

## Phase 1 — Statistics fidelity (finish the tail; ratchet, not timing)

*Design: 08 §4. Gates: 09 §5 P1 row + §3 floor.*
Exit: estimate ratchet monotone (bar A5/B3), parity mismatch budget does not
grow, no query >1.2× (B2), S-cold/WARM gap narrows. Judge the tail by the
**estimate ratchet, not per-item timing**: absent-stats restoration moved
time (−10.5%) while inaccurate-stats refinement did not (06 preamble).

Landed in take2 (record, not work):

- [x] P1-01 Real index relpages/reltuples — `287232e17`; gates:
      `internal/{catalog,executor,optimizer}` units. Reads the live block count
      via `RelNBlocksFunc` (what `get_relation_info` does) — persisting would
      add a staleness class PG lacks. Remainder rescoped into P1-02.
- [x] P1-03 VACUUM persists relsize — `3bcac056c`; gates: optimizer/executor/
      catalog units. In-memory `222 / 0` is now durable across restart.
- [x] P1-03b TRUNCATE reset + ANALYZE/VACUUM cache invalidation — `d3e12b3b4`,
      `ada899c38`; gate: `TestPlanCacheInvalidatingStmt`. Empty table estimates
      2550, exactly PG; no stale cached plan after ANALYZE/VACUUM.
- [x] P1-04 allvisfrac — verified already satisfied, no code change
      (`pathindexonly.go:109` reads `catalog.RelAllVisible`; ~1.0 on the bench
      cluster). Per-producer computation, not on `RelOptInfo`, changes no plan.
- [x] P1-05 `GOOPG_RELSIZE_FALLBACK` retired — `85bdad317`; gates: rewritten
      positive-direction tests, units. `scripts/planner-flags.env` regenerated.
- [x] P1-07 `n_distinct` override — `febe89168`; gate:
      `TestNDistinctOverrideBeatsTheSampledFraction`, units. Clears
      `NDistinctFrac` alongside; ledger row 777 stale, close on sight.
- [x] P1-08 `analyze_mcv_list` — `bf2c29d95`; gate:
      `TestAnalyzeMCVListMatchesUpstream`, units. Hypergeometric test incl.
      complete-list short-circuit (`l_orderkey` 100→0 MCVs, `l_returnflag` 1→3).
- [x] P1-09 Histogram bucket count — verified already satisfied, no code change
      (`bucketCount+1` bounds = `analyze.c:2744-2746`; agreement on all probed
      columns incl. the all-MCV case).
- [x] P1-11b Date scalar (half) — `36c78e28c`; gate:
      `TestConvertTimevalueToScalar`, units. Worst-case error down ~80×. Text/
      network variants remain open (`bucketFraction` 0.5 pinned by test).
- [x] P1-11c pg_statistic decode — `f07c20b1f`; gate:
      `TestPGStatisticRoundTripPreservesHistogram`, units. Three silent bugs;
      TPC-H 288.10 s → 257.75 s (−10.5%), Q5 −32.2%, Q7 −17.2%, counts
      identical. Pre-Sep figures were measured blind on restarted servers.
- [x] P1-12 Conjunction = `clauselist_selectivity` — `71653da23` (with P1-13);
      gates: units. `RestrictInfo` caching is planning-speed, not plan-quality
      → filed under Phase 6, not kept open here.
- [x] P1-13 RangeQuery pairing — `71653da23`; gates: three unit tests, units.
      2.04× over → 0.9% under; timing +0.45% neutral — negative result, kept.
- [x] P1-14 `nulltestsel` — `13430fc3a`; gates: two unit tests, units. Combined
      with P1-25 at +0.88% (noise); judged by the ratchet, not timing.
- [x] P1-15 MCV pairing in `eqjoinsel_inner` — `b0097a2af`; gates:
      `TestEqjoinselInnerMCVBeatsFlatNDistinct`, decline-without-both-lists,
      units. Full matchprod/unmatchprod formula, smaller viewpoint wins. Open
      half: MCV equality by rendered text, not oprcode — moves with the semi
      arm together (sibling rule).
- [x] P1-17 Semi arm — verified already satisfied, no code change (MCV arm,
      `nd2` clamp, `(1-nullfrac1)` factor, `isdefault → 0.5` all present).
- [x] P1-19 isunique single-column override — `ca9328ed0`; gate:
      `TestUniqueSingleColumnKeyOverridesSampledNDistinct`, units. Composite
      keys correctly say nothing about members. Open half: nullfrac derate in
      the FK formula (reachable-as-future-concern, not needed yet).
- [x] P1-20 Equiv-class constant propagation — `7ef387324`; gates:
      `TestEquivClassPropagatesConstants`, non-literal non-propagation, units.
      470× cost reduction on the `a = b AND a = 42` probe. Constant half only;
      transitive `a = c` stays legacy; `nconst_ec` reachable but not needed.
- [x] P1-25 DISTINCT sizing — gate: `TestDistinctIsSizedNotPassedThrough`,
      units. `DISTINCT l_shipmode` → rows=7, exactly PG. Set-op sizing stays
      open (`estimateSetOp` separate). Same commit fixed the latent bitmap
      `mctx` crash this newly-winnable path exposed.
- [x] P1-26 Resolver collapse — `4c8ea479f`; gate:
      `TestColumnStatsResolverIsOneArmList`, units. `columnStatsForChild`
      delegates to `resolveBaseColumn`; drift now impossible, and the
      index-probed leaf gains MCV/histogram access.
- [x] P1-28 `pg_stats.correlation` — `86b3b96a2`; gate:
      `TestPgStatsRendersCorrelation`, units. `cost_index` consumes it; a zero
      priced every index scan at `max_IO_cost`.
- [x] P1-29 ndistinct two-form read — `dd22e656c`; gates: values-diff both
      suites. `ResolvedNDistinct` applies PG's sign convention at both call
      sites (`IN (1..5)` 5000→5, exactly PG). TPC-H −8.1%, TPC-DS −3.6%, 79
      shapes moved, both runtime moves faster.
- [-] P1-06 Two-stage sampling — declined as written (ANALYZE not in the query
      path; exact `reltuples` kept). *See Dropped table.*

Remaining:

- [ ] P1-02 Tree height + partial-index tuples (rescoped: `relpages`
      superseded by storage). `_bt_getrootheight` analogue for height;
      `indexTuples = relTuples` is wrong for partial indexes. Own commit,
      lands before P1-10 (one checkbox = one commit; 08 §1 P5).
      *design: 08 §4; gate: 09 §5 P1 (PP + EA ratchet).*
- [ ] P1-10 Expression-index stats (`compute_index_stats`; 06 Appendix).
      Own commit, lands after P1-02. *design: 08 §4; gate: 09 §5 P1
      (EA ratchet).*
- [ ] P1-11 TOAST in the catalog heap writer: wide-text histograms dropped +
      size-row loss (`orders`, `customer`, `partsupp`). **Re-measure first**
      post-`f07c20b1f` — the wide case may now behave differently (06 §2.11).
      Acceptable interim: bounded-width with a ledger row.
      *design: 08 §4; gate: 09 §5 P1 + full regress (catalog format); re-init
      the data dir.*
- [ ] P1-14b Remaining per-clause estimators: general `scalararraysel`,
      `patternsel` (LIKE/regex beyond the access-path prefix), `rowcomparesel`,
      `booltestsel`, `var_eq_non_const`; align every `DEFAULT_*` with 03 §5.1.
      Judge by the ratchet, not timing.
      *design: 08 §4; gate: EA ratchet (09 §4.1 A5); no per-item A/B.*
- [ ] P1-16 Re-diagnose TPC-H Q9's cardinality error with `estimate-audit`.
      The recorded single-`nd` explanation is RETIRED (M0127-P5.6-f folds every
      pair); close ledger rows 779/781/784 as stale and file what the audit
      actually shows on the final joinrel.
      *design: 08 §4; gate: EA final-joinrel bar on Q9 (09 §2.2).*
- [>] P1-18 Outer/semi/anti join sizing — moved to Phase 3, executes after
      P3-04 (see the P1-18 checkbox there).
- [ ] P1-21 `max(outer,inner)` fallback cap — KEEP. Precondition NOT met: the
      cap sits in the unmeasurable fallback (`cardinality.go:655-666`), which
      P1-15 cannot reach; deleting it now inflates big inputs with no evidence.
      Verify-and-ledger as a keep decision, one commit.
      *design: 08 §4; gate: units pinning the guard's conditions
      (09 §5 P1 + §3 floor).*
- [ ] P1-22 Build extended statistics during ANALYZE
      (`BuildRelationExtStatistics`: ndistinct, dependencies, MCV) into
      `pg_statistic_ext_data`. Largest new Phase-1 subsystem with P1-23/24;
      may split further.
      *design: 08 §4; gate: full regress (catalog-adjacent)
      (09 §3 item 7 + §5 P1).*
- [ ] P1-23 `statext_clauselist_selectivity` with the `estimatedclauses`
      bitmap, dependencies + MCV selectivity, `choose_best_statistics`,
      `statext_is_compatible_clause` (03 §9.2–§9.3). Note PG's NullTest-MCV-only
      rule (dependencies has no NullTest branch).
      *design: 08 §4; gate: 09 §5 P1 — plan-parity both suites.*
- [ ] P1-24 `estimate_multivariate_ndistinct` for GROUP BY (03 §8.1).
      Correlated predicates are where independence fails and TPC-DS is built
      from them — this is the Phase-1 item most likely to move TPC-DS shapes.
      *design: 08 §4; gate: EA ratchet + PP (09 §4.1 A5 + §5 P1).*
- [ ] P1-27 CTE-agg statistics — rescoped: plain-CTE columns already resolve
      (`*CTEScan` arm); the gap is **aggregated** outputs (`year_total` →
      0.005/conjunct, same as PG) plus goopg's helping `rows ≤ 1` guard.
      Replacing the guard needs genuinely derived statistics (propagation
       through aggregation); removing it without them regresses. Keep open with
       a ledger row if deferred.
       *design: 08 §4; gate: EA ratchet (09 §4.1 A5 + §5 P1).*
- [ ] P1-30 `get_variable_range` index-endpoint probe + MCV widening
      (absent; histogram-endpoint drift for monotonic keys, 06 §4.3).
      Judge by the ratchet, not timing.
      *design: 08 §4; gate: EA ratchet (09 §4.1 A5 + §5 P1).*
- [ ] P1-31 Text scalar `convert_string_to_scalar` + network variants
      (take2 TODO P1-11b still-open half; `bucketFraction` 0.5 pinned by
      test, 06 §5.3/§12.3). Date half landed (P1-11b). Judge by the
      ratchet, not timing.
      *design: 08 §4; gate: EA ratchet (09 §4.1 A5 + §5 P1).*

---

## Phase 2 — Cost-model completeness (close the remainder in order)

*Design: 08 §5. Gates: 09 §5 P2 row + §3 floor.*
Exit: every cost GUC changes a plan (remaining four need index/parallel
fixtures — file, don't claim); parity budget does not grow; no query >1.2×;
P2-02b lands alone with both-suites timing.

Landed in take2 (record, not work):

- [x] P2-01 Planner-settings carrier — gates:
      `TestPlannerSettingsReachTheJoinSearch`,
      `TestDefaultPlannerSettingsMatchTheHardWiredParams`,
      `TestUnstampedContextGetsDefaultsNotZeroes`, units. Plan-neutral by
      construction; display-only path still on `defaultCostParams()`.
- [x] P2-02 Session cost GUCs reach the planner — `f93ea20dd` (FROM-clause
      slice); gates: round-trip/override/hash-join/total-conversion tests +
      live probes (`seq_page_cost=1000` flips hash→merge; `work_mem` reprices
      14835→23478). Two channels needed (extended-protocol + simple-query);
      EXPLAIN threads its inner plan. Four GUCs still need fixtures.
- [x] P2-02c Six `registry.OnChange` bridges removed — `62a5006c7`,
      `d69765485`, `294b82ec9`; gates: cross-session isolation, units. Zero
      bridges remain; globals now supply only defaults for session-less calls.
- [x] P2-03 `hash_mem_multiplier` — `7c95b2c83`; gates:
      `TestHashMemMultiplierReachesTheBudget` + sibling guard, units. Session
      block −37.1% (390.70 s → 245.71 s); Q14 −96.1%, Q9 −85.2%. Re-read: partly
      a default change (budget 64→128 MB), not only a session-GUC change.
- [x] P2-04 Plan-cache guard for cost GUCs — gates: override-detection tests,
      units. Override-based bypass (not BootVal comparison). Cache-key half
      remains open.
- [x] P2-05 `enable_*` via `DisabledNodes`, join methods — `656236ab1`;
      gates: per-flag tests. Scans still producer-skip → remainder item below.
- [x] P2-06 NL inner priced as materialised — `788eda72b`; gates: TPC-H neutral
      24 MATCH, TPC-DS 95 PASS, 27 shapes attributed. No Material path (executor
      materialises unconditionally); no merge mark/restore decision to cost.
      Q54 14→12 s, Q47 12→11 s.
- [x] P2-07 `cost_rescan` — `5918fe094`; gates: TPC-H inside drift 24 MATCH,
      TPC-DS 95 PASS (Q94 7→2 s). Defect was inverted framing: startup was paid
      per outer row, not free. CTE re-execution arm remains (no path kind →
      Phase 4).
- [x] P2-09 Unique-index single-tuple clamp (partial) — `10ee792b0`; gates:
      TPC-H best-of-session 24 MATCH, TPC-DS 95 PASS, 99 same/0 changed. The
      per-tuple index qual cost was implemented, measured +3.3% on the TOTAL
      arm with green per-query gates, and REVERTED — it rejoins in the batch
      item below (aggregate acceptance, R6).
- [x] P2-11 Hash bucket-size term, ndistinct half — `bb32b976c`; gates: TPC-H
      best-of-bundle 24 MATCH, TPC-DS −1.2%, 88 shapes. Q76/Q12 "regressions"
      byte-identical = sweep variance (attribution rule R7; baseline-hygiene
      note: honest −1.2%, not −4.3% vs the reverted run).
- [x] P2-12 Merge END selectivities — `b3a53afe0`; gates: TPC-H neutral 24
      MATCH, TPC-DS 95 PASS, 5 movers net −1 s (+2.1% aggregate reading =
      drift: identical-plan queries net +23 s). START selectivities omitted as
      a no-op (goopg's merge performs no seek). Type-aware `histCmp` included.
- [x] P2-13 Bitmap lossy + no double charge — verified done in-tree, no new
      commit. Census goopg 9 vs PG 6 (was 22–24 vs 6); Q72 105 s PASS.
- [x] P2-14 Merge costed on `mergejointuples` — `c281b0830`; gates: TPC-H
      258.28 s → 240.73 s. Root-caused via DPPATH: merge charged post-filter
      rows while emitting pre-filter rows (0.0031/tuple vs 0.01).
- [x] P2-15 Merge dropped-clause wrong answers — `13d53603f`; gates: 24 MATCH
      on values, perf-neutral. Pre-existing bug (512 MB default hid it); P2-02b
      correct-but-slow only after this.

Remaining:

- [!] P2-02b `work_mem` BootVal 512 MB → 4 MB — BLOCKED, ordered after P0-12
      (landed) **and** P4-01 **and** the P2-02d/e/f propagation slices
      (08 §5.1, §10; each slice lands after P4-01 or pays the same
      regression).
      History, preserved whole: attempted and reverted; post-`13d53603f` purely
      performance (+23.1%, entirely Q9+Q7, values correct); isolated split width
      ~87% / lost Gather ~13% (take2
      `impl/MEASUREMENT-p202b-width-vs-gather.md`; the withdrawn 39×-as-cause
      arc in `impl/FINDING-planner-settings-not-propagated.md` stays on record).
      Three lines move together (GUC BootVal, `postgresql.conf.sample`,
      `hashsize.DefaultMemLimitBytes`) + `TestSampleConfigCoversRegistry`.
      Lands alone, both suites timed, expect plans to move.
      *design: 08 §5.1; gate: 09 §5 P2 + B2.*
- [ ] P2-02d Derived-table propagation: thread the settings by hand through
      `planSelectWithParent` for `(SELECT …) AS alias` FROM items (one
      caller family, one commit; after P4-01 per 08 §10.1). The reverted
      mechanical attempt threaded from the wrong scope — hand-thread only.
      *design: 07 §3.3 + 04 §12.3 + 08 §5.1; gate: 09 §5 P2 (GUC-effect +
      both-suites timing).*
- [ ] P2-02e Set-operation operand propagation: same hand-threading for
      set-operation operands (one caller family, one commit; after P4-01
      per 08 §10.1).
      *design: 07 §3.3 + 04 §12.3 + 08 §5.1; gate: 09 §5 P2 (GUC-effect +
      both-suites timing).*
- [ ] P2-02f Scalar-subquery propagation: same hand-threading for
      scalar-subquery sites (one caller family, one commit; after P4-01
      per 08 §10.1).
      *design: 07 §3.3 + 04 §12.3 + 08 §5.1; gate: 09 §5 P2 (GUC-effect +
      both-suites timing).*
- [ ] P2-09a ScalarArrayOp index path. `num_sa_scans` is blocked on a
      missing *path* (`IN` → seq scan + filter; 04 §5, 05 §3.3): build the
      index `= ANY` descents + `ceil(pages/3)` clamp (02 §3.4). Gate on
      path existence/shape on IN-list fixtures.
      *design: 08 §5.2; gate: 09 §5 P2 (PP shape on IN-list fixtures).*
- [ ] P2-09b `btcostestimate` batch. Land the batched remainder (descent
      present; the reverted per-tuple qual cost rejoins here) whole, with
      acceptance on the **aggregate sweep TOTAL**, not the per-query gate
      (R6).
      *design: 08 §5.2; gate: 09 §5 P2 + TOTAL arm with plan attribution.*
- [ ] P2-11b MCV-frequency half (P2-11 remainder): plumb the inner key's MCV list to the cost site
      for the skew term (02 §5.4: `estimate_hash_bucket_stats`, clamp
      [1e-6,1]); per-orientation closures as landed. Zero/isdefault fraction
      suppresses the term rather than guessing.
      *design: 08 §5.3; gate: best-of-bundle discipline + TPC-DS aggregate;
      plan-change attribution before believing per-query moves (R7).*
- [ ] P2-16a `disabled_nodes` sort + material setters (02 §1.2). One
      commit; makes the family A/B-able for later phases.
      *design: 08 §5.4; gate: GUC-effect test per newly-live GUC (09 §5 P2).*
- [ ] P2-16b `disabled_nodes` agg-hashed/mixed setters (02 §1.2). One
      commit.
      *design: 08 §5.4; gate: GUC-effect test per newly-live GUC (09 §5 P2).*
- [ ] P2-16c `disabled_nodes` gather-merge setter (02 §1.2). One commit.
      *design: 08 §5.4; gate: GUC-effect test per newly-live GUC (09 §5 P2).*
- [ ] P2-16d Retire producer-skipping for scans where PG counts instead of
      gating. Hard gates stay gates (index-only, TID-except-CURRENT-OF,
      memoize, incremental-sort; 01 §12, 02 §1.2). Own commit — scan
      shapes change.
      *design: 08 §5.4; gate: GUC-effect test per newly-live GUC (09 §5 P2).*
- [ ] P2-16e Retire `enable_nestloop_index` once NLI is an ordinary
      parameterised-nestloop path (08 §6.4). Own commit with its own gate.
      *design: 08 §5.4 + §6.4; gate: GUC-effect test (09 §5 P2).*

---

## Phase 3 — Search coverage (the structural phase)

*Design: 08 §6. Gates: 09 §5 P3 row + §3 floor + values-diff (R8).*
**Ordering bar: do not start P3-02/03/04 before P4-01 lands** — wider
search is not safe until the numbers ranking it are right (08 §2.3;
08 §10.7). P4-01 is necessary but not sufficient (P4-01b: narrowing
alone takes `orders` 128→64 batches, not 1; 48 B/column co-dominant) —
which is why Phase 3 must not run on pre-P4-01 numbers.
Exit: every PG-only join spine OFFERED at its level (DPPATH
per-producer OFFERED/ACCEPTED, 04 §6) or named reason via
`estimate-audit --enum-trace` (09 §2.1); Q72-class queries (mixed
comma + `LEFT JOIN` FROM lists; Q72 witness, full list enumerated by
the enum-trace run) one search problem; `join-order` diffs strictly
decrease.

Landed in take2 (record, not work):

- [x] P3-09 `RelSet` → `uint32`, `maxSearchRels` 32 — `cb6a725d0`; gates: TPC-H
      24 MATCH, TPC-DS 95 PASS, 1 shape of 99. Capability change, not a plan
      change (no benchmark query nears sixteen relations).
- [x] P3-10 GEQO seven knobs on the settings — `ab6fef649`; gates: TPC-H 24
      MATCH, TPC-DS 95 PASS, 99 same/0 changed (unreachable below threshold 12;
      zero stays meaningful for pool/generations). TPC-H deltas of this size
      need an A/A control first (drift band widened past 1.7%).
- [x] P3-11 NLI census answered — docs only. Premise wrong twice: 1-vs-25 is
      STALE (now 18 vs 30), and NLI loses narrowly (0.05%–12%; DPPATH 694
      offered/23 accepted). Action is the remaining `btcostestimate` + hash
      terms — a targeted NLI change at these margins is benchmark-tuning.
- [x] P3-12 `reorderCommaFromByCardinality` deleted — `6f5eefa85`, `362cfa91c`;
      gates: TPC-H 24 MATCH, TPC-DS 95 PASS, 64 movers −41 s attributed.
      `joinorder.go` deleted (1327−/48+); shared walkers moved, Q85
      determinism test kept. Identical-plan "moves" not attributable (R7).

Remaining:

- [ ] P3-01 `SpecialJoinInfo` population. The field set already exists
      (`specialjoin.go:16`); what is missing is population, blocked on name
      resolution (`makeSpecialJoinInfo` at `specialjoin.go:54`, called from
      `collapse.go:416`, runs before Vars
      carry relation indexes; `ColumnRef` carries names). Two routes: thread a
      name-to-leaf map from FROM items (smaller), or deconstruct after
      resolution (what PG effectively does). Partial fix UNSAFE (underestimate
      = wrong answers; fall back to `syn` on any uncertainty). Moves no plan
      alone — unobservable until P3-03/P3-04 consume it.
      *design: 08 §6.1; gate: units.*
- [ ] P3-02 `distribute_qual_to_rels` with `check_outerjoin_delay`: a qual is
      **placed**, not copied down. Supersedes `pushInnerJoinInputQuals` copying
      (ledger 802: the executor evaluates it twice).
      *design: 08 §6.2 (01 §4.1–§4.2); gate: 09 §5 P3 + values-diff (R8).*
- [ ] P3-03 `join_is_legal` consulting real SJIs; the DP builds outer, semi and
      anti joinrels directly (01 §6). Wider search is unsafe until the ranking
      numbers are right (08 §2.3) — stats + cost precede this.
      *design: 08 §6.2; gate: 09 §5 P3 (DPPATH OFFERED/ACCEPTED +
      `estimate-audit --enum-trace`, 09 §2.1).*
- [ ] P3-04 Delete `splitOuterSpine` + the `pinnedOuter()` whole-statement
       decline; a mixed comma + `LEFT JOIN` FROM list becomes **one** search
       problem. TPC-DS Q72 is the witness. **Unblocks P1-18.**
       *design: 08 §2.2 + §6.2; gate: 09 §5 P3 — plan-parity both suites +
       timing.*
- [!] P1-18 Outer/semi/anti join sizing — executes HERE, after P3-04 (a
      jointype switch before P3-04 would be dead code: probe reports
      `nrels=1, pairs=0`; take2 TODO P1-18). Port
      `calc_joinrel_size_estimate`'s jointype switch (02 §6).
      *design: 08 §4 + §6.2; gate: EA ratchet.*
- [ ] P3-05 Retire `GOOPG_PGSHAPED_COLLAPSE` once P3-04 makes it the only
      jointree path; `from_collapse_limit`/`join_collapse_limit` take their
      upstream meaning including `join_collapse_limit = 1` preserves order
      (01 §12). Own commit, both suites timed.
      *design: 08 §6.3; gate: 09 §5 P3.*
- [ ] P3-06 `standard_qp_callback` analogue (`query_pathkeys = group ?:
      window ?: longer(distinct, sort) ?: setop`; 01 §4.4; needs P2-01, landed)
      on the planner context; complete `has_useful_pathkeys` so ORDER BY and
      GROUP BY motivate index paths (04 §5/§9).
      *design: 08 §6.3; gate: 09 §5 P3 (PP).*
- [ ] P3-07 `param_source_rels` (hard-coded 0 today; 04 §7) with
      `allow_star_schema_join` semantics (01 §7). GEQO reachability follows
      this phase's coverage work, not more wiring.
      *design: 08 §6.3; gate: 09 §5 P3 (PP).*
- [ ] P3-08 `reduce_unique_semijoins` (unique-inner SEMI becomes INNER) — after
      clearing SEMI left-only `Output()` re-indexing (ledger 794).
      *design: 08 §6.3; gate: 09 §5 P3 (PP + values-diff).*

---

## Phase 4 — Upper planner as paths (PathTarget first)

*Design: 08 §7. Gates: 09 §5 P4 row + §3 floor + values-diff (R8).*
Exit: `aggregation-strategy` / `sort-strategy` diffs strictly decrease; no
correctness delta (values, not counts).

Landed in take2 (record, not work):

- [x] P4-01a Per-path width — gate: `TestPathCarriesItsOwnWidth`, units. Paths
      carry `NCols`/`AvgVarBytes` via `pathNCols`/`pathAvgVarBytes`
      (`path.go:348`, `:360`); the per-rel/per-path
      blocker is gone, so narrowing is now a matter of setting two fields.
- [!] P4-01b Leaf narrowing — ATTEMPTED, REVERTED, WRONG ANSWERS (Q2/Q5 0 rows,
      Q18 wrong tuples; faster-and-wrong, caught by `-digest` + `-diff`; row
      counts alone would have passed Q18). Lesson binding on P4-01: projection
      must be a path property with setrefs-style fixup, not a leaf swap.
      Review findings carried: `ExtractExpr` collector gap fixed (`915ce7882`);
      scan-operator `Output()` invariant means a real `Project` below the build
      side must be re-costed; narrowing alone takes `orders` 128→64 batches,
      not 1 (48 B/column Datum co-dominant, out of scope). Pre-code: re-take the
      §12 EXPLAIN table on current HEAD (predates nine planner commits).

Remaining (P4-01 executes before Phase 3 per 08 §10.7 — it blocks P2-02b
alongside the P2-02d/e/f propagation slices; P4-01 is necessary but not
sufficient per P4-01b above):

- [ ] P4-01 `PathTarget` + projection: per-path targets
      (`make_group_input_target`, `make_window_input_target`,
      `make_sort_input_target`, `apply_scanjoin_target_to_paths`; 01 §3) with
      setrefs-style fixup at `create_plan` time. Gate on **values**
      (`tpch-runner -digest` + `-diff`, all items), `NBatch` 2→1 on the
      witness (TPC-H Q9 hash build, EXPLAIN `Batches:` at `work_mem` 64 MB
      S-cold), `DPPATH` hash total below merge total. Threshold correction:
      narrowed width is ≈100, not PG's 6 (declared NUMERIC keys).
      *design: 08 §7; gate: 09 §5 P4.*
- [ ] P4-00 Pre-phase scoping: grouping-sets interaction, `remove_useless_joins`,
      `reduce_outer_joins.go` interaction, and FROM-subquery pull-up coverage —
      each a scoped item before the phase starts (still unscheduled).
      *design: 08 §7; gate: scoped items filed, not code (09 §9 reporting).*
- [ ] P4-02 Upper `RelOptInfo`s (`GROUP_AGG`, `WINDOW`, `DISTINCT`, `ORDERED`,
      `FINAL`) with pathlists (01 §3).
      *design: 08 §7; gate: 09 §5 P4 (PP).*
- [ ] P4-03 Promote `PathSort` to a real upper-rel path (has a
      `createPlanNode` arm; today only ever a merge-join child, so it never
      competes with a hashed alternative).
      *design: 08 §7; gate: 09 §5 P4 (PP).*
- [ ] P4-04 Bounded / top-N sort (`cost_sort`'s `limit_tuples` arm) — the
      largest recorded `ORDER BY … LIMIT` win.
      *design: 08 §7; gate: 09 §5 P4 (PP + timing).*
- [ ] P4-05 Incremental Sort node + `create_incremental_sort_path`. No
      executor counterpart exists (no Incremental Sort operator) — BLOCKED:
      resume after executor support (ledger `take2-P2-08` class) and
      excluded from the A3-closure promise until then.
      *design: 08 §7; gate when unblocked: 09 §5 P4 (PP).*
- [ ] P4-06 `create_grouping_paths` / `add_paths_to_grouping_rel` offering
      sorted + hashed aggregation priced by `cost_agg` incl. the hash spill arm
      (02 §4.4); retire `applyIndexOrderedGroupingRule`,
      `applyPresortedAggregateRule`, `applyEnableHashAggRule`.
      *design: 08 §7; gate: 09 §5 P4 (PP + timing).*
- [ ] P4-07 `create_distinct_paths` (hashed / sorted / unique-over-sorted).
      Depends on P1-25 (landed).
      *design: 08 §7; gate: 09 §5 P4 (PP).*
- [ ] P4-08 `tuple_fraction` end-to-end: reaches every upper rel, not only the
      join search (01 §1: fraction ≥1 ÷ rows, parameterised excluded,
      `disabled_nodes` first).
      *design: 08 §7; gate: 09 §5 P4 (PP).*
- [ ] P4-09 `create_window_paths` and set-operation paths, priced.
      *design: 08 §7; gate: 09 §5 P4 (PP).*

---

## Phase 5 — Parallelism in the path model (costed, not forced)

*Design: 08 §8. Gates: 09 §5 P5 row + §3 floor.*
Exit: `parallelism` diffs strictly decrease; the serial control arm is
unchanged. Ordering trap, measured early: at small budgets the plan moves
onto index-driven joins the old post-pass cannot drive — recovery is
downstream of shape, which is downstream of width (07 §3.2).

- [ ] P5-01 `consider_parallel` per rel (`set_rel_consider_parallel`).
      *design: 08 §8; gate: 09 §5 P5 (PP + serial control).*
- [ ] P5-02 `create_plain_partial_paths` populating `PartialPathlist`;
      `computeParallelWorkers` moves into path generation (`addPartialPath`'s
      only caller today is the production-dead `generateScanPaths`).
      *design: 08 §8; gate: 09 §5 P5 (PP).*
- [ ] P5-03 Parallel eligibility for plain index scans:
      extend `drivingScan` (SeqScan/BitmapHeapScan/IndexOnlyScan +
      `*Filter`/`*Project`/join-probe wrappers today; plain IndexScan still
      missing) so PG's Parallel Index Scan has a counterpart. Closes a
      `MISSING-NODE` entry.
      *design: 08 §8; gate: 09 §5 P5 (PP).*
- [ ] P5-04 `generate_useful_gather_paths` producing `PathGather` and
      `PathGatherMerge` priced by `cost_gather`/`cost_gather_merge` (02 §3.6),
      with `createPlanNode` arms.
      *design: 08 §8; gate: 09 §5 P5 (PP, parallel-on + serial control).*
- [ ] P5-05 Re-decide the `Gather Merge → Sort → Parallel scan` shape by cost
      rather than `sortPartialRootPays`' hard-coded decline. If goopg's costs
      still choose leader-side sorting, record it as a permitted divergence
      with the committed q16/q10/q13 measurement (09 §4.4 case 1).
      *design: 08 §8; gate: 09 §5 P5 (timing both shapes).*
- [ ] P5-06 Parallel hash join as a `parallel_aware` hash path, priced.
      Executor counterpart: the parallel hash build/probe
      (`parallel_hash_build.go`) — gate includes a consumer check (a
      fixture where the path wins must execute as parallel hash).
      *design: 08 §8; gate: 09 §5 P5 (PP).*
- [ ] P5-07 Partial aggregation as paths (`create_partial_grouping_paths`),
      replacing `splitAggregate`. Depends on P4-06.
      *design: 08 §8; gate: 09 §5 P5 (PP + values-diff).*
- [ ] P5-08 Retire `MaybeAddGather`.
      *design: 08 §8; gate: 09 §5 P5 — plan-parity both suites, parallel and
      serial arms.*

---

## Phase 6 — Single-planner deletion (with must-not-delete oracles)

*Design: 08 §9. Gates: 09 §5 P6 row + §3 floor + values-diff (R8).*
Exit: byte-identical plans to the pre-deletion arm on both suites, or every
difference explained and timed.

- [ ] P6-01 One cardinality estimator: delete legacy `estimateJoin`/
      `EstimateRows` + the `joinkeyproof.go` mirror; everything reads
      `calcJoinrelSize`. Prerequisite: EXPLAIN `rows=` from the path (P0-02
      remainder) + legacy consumers gone (P4).
      *design: 08 §9; gate: 09 §5 P6 (PP + EA ratchet).*
- [ ] P6-02 `PathTarget` + range table replacing `baseLeaf`/`baseOffset`;
      delete `joinlayout.go` remapping + `createplanroot.go` boundary
      assertions. Deletes the largest silent wrong-answer class (Q8/Q9 0-rows).
      *design: 08 §9; gate: value-level `tpch-runner -diff`, never counts.*
- [!] P6-03 `rewriteScanInputsWithSingleTablePredicates` — ATTEMPTED,
      LOAD-BEARING, MUST-NOT-DELETE (Q20 6.5×: correlated SubPlan degenerates,
      `Index Cond` collapses to `Filter: (true)`). Retire only once the costed
      producers cover the correlated-scan-input case (Phase 4 work).
      *design: 08 §9; gate when retried: 09 §5 P6 byte-identical.*
- [!] P6-04 `rewriteJoinsToNLI` + `GOOPG_NLI_COSTGATE` — ATTEMPTED,
      LOAD-BEARING, MUST-NOT-DELETE (Q4 semi-join 12.5× → Hash Semi over full
      lineitem; Q21 anti-join same class). Retire only once the search selects
      NLI on its own merits (remaining `btcostestimate` + hash terms, per
      P3-11 — not a targeted NLI adjustment).
      *design: 08 §9; gate when retried: 09 §5 P6 byte-identical.*
- [-] P6-05 `reconcileNLILayout` — MUST NOT DELETE: no direct production caller,
      but the live oracle for the `assertSearchedTreeNeedsNoReconcile` tripwire
      (runs unconditionally on every searched plan; panics on a move). Deleting
      it removes the check, not the code; replacing the pass means replacing
      the oracle first. *See Dropped table.*
- [ ] P6-06a Retire `GOOPG_INDEXKEY_HARVEST`, regenerating
      `planner-flags.env`. Own commit with a before/after parity roll-up +
      timing table.
      *design: 08 §9; gate: byte-identical plans for the flip (09 §5 P6).*
- [ ] P6-06b Retire `GOOPG_INDEX_PROBE_MULT`, regenerating
      `planner-flags.env`. Own commit with a before/after parity roll-up +
      timing table.
      *design: 08 §9; gate: byte-identical plans for the flip (09 §5 P6).*
- [ ] P6-06c Retire `GOOPG_HASH_OUTER_JOIN`, regenerating
      `planner-flags.env`. Measured safe (CKMISMATCH=0) but a wash (+1 s
      net): NOT flipped; re-measure after the `btcostestimate` batch
      (P2-09b). Own commit with a before/after parity roll-up + timing
      table.
      *design: 08 §9; gate: byte-identical plans for the flip (09 §5 P6).*
- [ ] P6-06d Retire `GOOPG_NLI_COSTGATE`, regenerating `planner-flags.env`.
      Only once the search selects NLI on its own merits (remaining
      `btcostestimate` + hash terms, per P3-11). Own commit with a
      before/after parity roll-up + timing table.
      *design: 08 §9; gate: byte-identical plans for the flip (09 §5 P6).*
- [ ] P6-06e Retire `GOOPG_PGSHAPED_DP` last, regenerating
      `planner-flags.env`. The off path is unretirable while the legacy
      rewrites are load-bearing (P6-03/P6-04). (Collapse retires once in
      P3-05, not here.) Own commit with a before/after parity roll-up +
      timing table.
      *design: 08 §9; gate: byte-identical plans for the flip (09 §5 P6).*
- [ ] P6-07 `setrefs` phase, if P6-02 shows the executor still needs explicit
      column resolution.
      *design: 08 §9; gate: 09 §5 P6.*
- [ ] P6-08 `RestrictInfo` caching (planning-speed, not plan-quality, per
      the P1-12 record above). Filed here, not in Phase 1.
      *design: 08 §9; gate: 09 §5 P6 (planning-time comparison, plans
      byte-identical).*

---

## Phase 7 — Acceptance

- [ ] P7-01 Full acceptance run: both suites, S-cold and WARM, full PP
      roll-up, `EA --reference` ratchet, complete timing table, §6 headers on
      every artifact. Bars: A1–A5 + B1–B3 + C1–C7 (09 §4); B4 directional only,
      explicitly excluded. Verdict document under
      `analysis/planner-refactor-take3/acceptance-<date>/README.md` with
      before/after roll-ups and an explicit worse-statement; negative results
      kept verbatim. Take2's interim `acceptance-20260903` carries the
      what-got-worse and methodology sections forward but is NOT this run.
      *design: 08 (all phases); gate: 09 §8.*

---

## Blocked / sequencing notes

- P1-18 executes with Phase 3, after P3-04: port the jointype switch only
  after the search can see a non-inner join; before that it is dead code
  (take2 TODO P1-18).
- P4-01 executes before Phase 3 (08 §10.7): P3-02/03/04 must not start
  before P4-01 lands (08 §2.3 safety rule).
- P2-02b on P0-12 (landed) + P4-01 + the P2-02d/e/f propagation slices;
  each slice lands after P4-01 or pays the same regression
  (08 §10.1; 08 §5.1, 07 §3.3, 04 §12.3).
- P4-01 before derived-table propagation (P2-02d) before P2-02b (08 §10.4).
- P2-08/P2-10 stay declined-premature for take3 (no SubPlan/semi-anti
  consumers); resume after Phase 3/4 consumers exist (ledger `take2-P2-08`,
  `take2-P2-10`, `take2-P2-09-saop`, `take2-P2-09-qualcost`).
- P2-09b qual-cost batch lands whole with aggregate-TOTAL acceptance (R6);
  bitmap-style cancelling pairs move in one commit (08 §1 P5).
- P6-03/04 only after the search selects those shapes on its merits (07 §3.8);
  P6-05 never without a replacement oracle (08 §10.5).
- One variable per commit (08 §1 P5); every flip its own commit with a
  before/after parity roll-up + timing table for moved plans (09 §9).

---

## Progress log

One row per closed phase. Numbers come from the 09 §6 artifact header.

| phase | closed | commit range | TPC-H total (goopg / PG) | TPC-DS SF0.5 total | plan-parity TPC-H | plan-parity TPC-DS | notes |
|---|---|---|---|---|---|---|---|

## Dropped

Items removed from the plan, with the reason and the ledger row. Keep the
original wording — negative results are only legible if they survive
(08 §1, 09 §9).

| item | date | reason | ledger row |
|---|---|---|---|

(End of file)
