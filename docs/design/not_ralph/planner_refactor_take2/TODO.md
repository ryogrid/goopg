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
- [x] **P0-02** / **P0-03** Real costs on every node — `9cbc7661b`; gates:
      `TestNoNodeRendersZeroCost`, `TestLegacyDisplayCostIsMonotone`, units.
      Landed **together**, deliberately: P0-02 alone prices the scan/join region
      and leaves the legacy upper planner at `0.00`, and a plan MIXING real
      costs with `0.00` is worse than an all-zero one (a free node and an
      unpriced node become indistinguishable). Mechanism is `PlanCost` embedded
      in the node, as PG carries it on `struct Plan` — the side-index design was
      tried and abandoned (no per-statement context on the chain;
      `createPlanNode` returns bottom-up; a pointer-keyed map collapses shared
      CTE subtrees). `rows=` still sources `EstimateRows`. FORMAT JSON gained
      the three missing cost keys.
      **First finding from the instrument:** on a filtered TPC-H aggregate goopg
      renders `width=550` where PG renders `width=2` — goopg carries the whole
      tuple where PG projects to the two bytes it needs. That is P4-01's
      `PathTarget` case, now measurable.
- [ ] ~~**P0-02** EXPLAIN emits the chosen path's real `(startup, total)`, `rows`
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
      *(closed above, with P0-02)*
- [x] **P0-04d** Schema qualification followed no mode — `2a63fbe21`; gate:
      `TestSchemaQualificationFollowsVerbosity`, units. **Not in the original
      plan**, found while reading P0-04's numbering rule and larger in impact:
      PG qualifies a relation in VERBOSE only (`explain.c:4409-4411`) and never
      qualifies an index (`explain_get_index_name`), while goopg qualified both
      in both modes — one guaranteed rendering divergence on **every scan node
      of every plan**, which would have swamped the P0-05/06 roll-up. Threading
      the mode was load-bearing: `describePlanVerbose` delegates to
      `describePlan` for types with no verbose arm, so a mode-independent
      renderer makes one of the two modes wrong whichever way it is fixed.
- [ ] **P0-04** Align EXPLAIN's relation-name suffix numbering with
      `select_rtable_names`. De-duplication already exists
      (`internal/executor/explain_names.go`); the numbering diverges, and the
      `shape_mismatches = 46` figure was attributed to its absence, so
      re-measure after aligning. *design: 08 §3 P0-3; gate: units + regress;
      re-pin the goopg-vs-goopg baseline in the same commit.*
- [x] **P0-04b** EXPLAIN mode asymmetry on `rows=` — `TestWalkersAgreeOnRowEstimate`;
      units green. Negative control: with the defect reintroduced the plain
      walker prints `rows=50` and ANALYZE prints `rows=10000` for the same
      filtered scan — a **200x** overstatement on exactly the nodes where the
      estimate matters most, in the mode most artefacts are captured in.
      The test needed a *selective* predicate: with a `true` constant
      (selectivity 1.0) it passed with the defect present.
- [ ] **P0-04d** *(new, landed `2a63fbe21`)* — see the closed entry below.
- [ ] **P0-04e** `planToJSON` does not collapse `Project`/`Filter` wrappers, so
      `EXPLAIN (FORMAT JSON)` emits a different TREE from the text walker and
      from PG, which has no such nodes at all. Found while fixing P0-04b;
      deliberately not half-fixed there, since the JSON defect is tree shape
      rather than the row estimate. The parity instrument parses TEXT, so this
      does not block P0-05/06. *design: impl/P0-A §3.5.*
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
- [x] **P0-09** Oracle timer at millisecond resolution — `71653da23`; gate:
      `bash -n`. Per the rescope, **no re-capture was triggered**.
      ~~Rescoped (impl/P0-B §6). Change the oracle timer to
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
- [x] **P0-11** Path provenance tracing — gates: `TestPathTraceRecordsProducerAndVerdict`,
      `TestPathTraceVerdictSurvivesEviction`, `TestRelSetBitsIsParseable`, units.
      **Design deviation, deliberate:** the record uses a DISTINCT `DPPATH` tag
      rather than a third `DPTRACE path` kind. `enumtrace.go` counts
      DPTRACE-tagged lines it cannot parse as `Malformed`, so a new DPTRACE kind
      would couple emitter and parser in one commit *and* make any older parser
      report a bogus Malformed count on a newer log. A distinct tag has neither
      problem — `enumtrace.go` ignores it (its anchor is the DPTRACE substring),
      verified by running that package's tests. Covers `addPath` (12 sites) and
      `addPartialPath`, `producer` passed as an argument, never recovered from
      the stack. `pathlistVerdict` checks the list TAIL, not its length: an
      accepted path can evict several incumbents and shrink the list, which a
      length comparison would misread as rejection.
      ~~Path provenance tracing — **extends an existing channel**
      (impl/P0-B §8). `internal/optimizer/joinsearchtrace.go` already emits
      `DPTRACE` blocks under `GOOPG_PGSHAPED_DP_TRACE=1` at *join-pair*
      granularity, parsed by `estimateaudit/enumtrace.go`. Add a third `path`
      record type from `addPath` (`path.go:555`) **and `addPartialPath`
      (`:562`)** — 12 + 1 call sites, `producer` passed as an argument, never
      recovered from the stack. The emitter and the matching `EnumPath` parser
      arm **must land in one commit**: `enumtrace.go` counts unparseable
      `DPTRACE` lines as `Malformed`, so a lone emitter would look like a
      regression in the existing instrument. *design: 09 §1 R4; gate: units,
      `Malformed == 0`.*~~
- [x] **P0-12** Bench-cluster memory alignment — landed after P2-02 unblocked
      it. `work_mem` 512MB→64MB and `effective_cache_size` 4GB→2GB, matching the
      PG reference cluster. **Result: goopg is 62 % SLOWER (248.71 s →
      403.27 s), row counts identical** — see
      [impl/FINDING-workmem-advantage.md](impl/FINDING-workmem-advantage.md).
      The recorded 9.9× headline was measured with goopg holding an 8×
      `work_mem` advantage; the honest ratio is nearer **17.6×**. Q14 slows 24×,
      Q3 11×, Q10 4× — and PG runs all three in about a second at the *same*
      setting, so **the dominant remaining cost is the executor's spill path,
      not the planner**. The alignment is kept: the faster arm was measuring a
      configuration advantage. `shared_buffers` stays divergent by design
      (Go-heap arena under GOMEMLIMIT).
      ~~**BLOCKED on P2-02 — the recorded ordering is unsafe.**
      *(established 2026-09-02 by reading the code, not yet acted on.)*
      Setting `work_mem = 64MB` in goopg's bench conf would make the
      **executor** honour 64MB (`hashsize.EffectiveMemLimit` reads the session)
      while the **planner** keeps pricing at `hashsize.DefaultMemLimitBytes`
      = 512MB — `defaultCostParams()` is hard-wired and no session reaches it
      (`cost_funcs.go`'s `workMem` comment says so outright, and warns: "The two
      must agree or the planner prices a geometry the executor will not build").
      Today they agree by accident, both at 512MB. P0-12 alone would break that
      agreement and make plans worse in a way that looks like a config change.
      **So P2-02 (session cost GUCs reach the planner) must land first**, and
      then P0-12 and P2-02b together. The bundle's ordering — P0-12 *before*
      P2-02b — is right about P2-02b and silent about P2-02, which is the one
      that actually gates it.
      Also measured: `shared_buffers` is 4x apart (PG 512MB, goopg 2048MB from
      `bench/tpch/setup_goopg.sh:55`). That one is a **deliberate architectural
      divergence**, not drift — goopg's buffer arena is a Go-heap object under
      `GOMEMLIMIT` (M0032-0001) — so it should be recorded as a permitted
      difference rather than "aligned" by shrinking goopg to 512MB, which would
      measure Go's GC rather than the planner.
      ~~Align the goopg TPC-H bench cluster's memory settings with the
      PG reference cluster's. Today PG sets `work_mem = 64MB` and
      `effective_cache_size = 2GB` explicitly while goopg's conf leaves both at
      boot defaults (512MB and 4GB), so the headline 9.9× is measured with
      goopg holding an **8× `work_mem` advantage** and any `work_mem`-sensitive
      cost comparison between the engines is meaningless. **Measured 2026-09-02
      (impl/P0-B §2.2), and wider than recorded:** `shared_buffers` is also 4×
      apart (PG 512MB, goopg 2GB). It cannot change a plan (`effective_cache_size`
      is the cost-model input) but it changes runtime, so it is aligned too.
      All other cost/collapse/parallel GUCs were verified identical.~~
- [ ] **P2-02b** *(now unblocked)* — with P0-12 landed, the `work_mem` BootVal
      correction 512MB→4MB can proceed. **Expect it to be large**: dropping
      512MB→64MB already cost 62 %, and 4MB is a further 16×. Do this **before**
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

> **RESOLVED 2026-09-02 by `f07c20b1f`** — three bugs in the pg_statistic
> physical-tuple decoder, each silent, each masking the next:
> (1) `decodeTextArray` advanced by each element's *unpadded* length where PG
> aligns array elements to the element typalign; (2) `readVarlena` assumed the
> 4-byte varlena header while the writer emits PG's 1-byte short header for
> values under 128 bytes; (3) `readVarlena` aligned every slot to 4, but
> `stavalues*` is `anyarray` with `typalign 'd'` (8). The stats were on disk
> all along and were read back empty. `l_shipdate` now restores mcv=100
> hist=101 with no new ANALYZE, and the estimate for a date-range predicate
> moved from `2000418` (= rows/3, `DEFAULT_INEQ_SEL`) to `2567922` against
> PostgreSQL's ~2.58 M. Gate: `TestPGStatisticRoundTripPreservesHistogram`.
>
> Original finding, kept for the reasoning:
> **[impl/FINDING-histograms-lost-on-restart.md](impl/FINDING-histograms-lost-on-restart.md).**
> ANALYZE histograms are computed, are visible in `pg_stats`, and are **gone
> after a server restart** — for narrow columns (`l_quantity`, `l_shipdate`),
> not only the wide-text ones P1-11 records. `n_distinct` and the relation size
> survive; only the histograms are lost. So on any restarted server every range
> predicate falls to `DEFAULT_INEQ_SEL` and the planner estimates blind, which
> almost certainly includes **every recorded goopg benchmark figure**, the
> 227.0 s / 9.9x headline among them. No cost-model work can recover an input
> that is not there. **Fix this before P1-11b**, and re-measure P1-11b's value
> afterwards — its stated rationale is wrong in both directions (see §6 of the
> finding).


Exit: estimate ratchet does not regress, parity budget does not grow, no query
slower than 1.2×, S-cold/WARM gap narrows.

### 1a — Relation and index statistics

- [x] **P1-01** *(catalog half)* — index `relpages`/`reltuples` in `pg_class`
      were hard-wired `"0"` / `"-1"`; they now report real figures. Verified
      against the bench clusters: `part_pk` 497 pages / 200000 tuples (PG: 551 /
      200000), `orders_pk` 3707 (PG: 4116). Gates: `internal/{catalog,executor,
      optimizer}`, units.
      **Correction to the item as written:** goopg does **not** need to *persist*
      relpages. It reads the live block count through `catalog.IndexRealPages` →
      `RelNBlocksFunc`, which is what `get_relation_info` does upstream
      (`RelationGetNumberOfBlocks`, plancat.c) — there is nothing to keep in
      sync. This change also **proves that path resolves correctly on a real
      cluster**, which answers P1-02: the planner was already receiving real
      index page counts, so `estimateIndexGeometry`'s width-derived synthesis is
      already superseded whenever storage can answer.
      *Still open under this item:* btree tree height is derived from the page
      fanout rather than read via a `_bt_getrootheight` analogue, and
      `indexTuples` is assumed equal to heap tuples (correct for a complete
      non-partial index, wrong for a partial one).
- [ ] **P1-02** *(rescoped by the above)* — the remaining synthesis is tree
      height and the partial-index `indexTuples` case, not `relpages`.
      ~~`costIndexScan` reads the real index geometry; retire
      `estimateIndexGeometry`'s synthesis.~~ *design: 08 §4.1.*
- [x] **P1-03** VACUUM persists `reltuples`/`relpages` durably — units,
      `internal/{executor,catalog,optimizer}`. Confirmed exactly as recorded:
      `catalog.UpdateRelStats` wrote memory only. `persistRelSize` is extracted
      from `persistStatsToPGStatistic` and called from the VACUUM path.
      Negative control, same table both arms:

      | | after VACUUM | after restart |
      |---|---|---|
      | before | `222 / 0` | **`0 / 0`** |
      | after | `222 / 50000` | **`222 / 50000`** |

      Non-fatal on write failure: a VACUUM that has done its real work must not
      fail on a statistics write, the same convention
      `persistStatsToPGStatistic` uses for a column it cannot fit.
- [x] **P1-03b** `TRUNCATE` resets `Table.Stats` — units,
      `internal/{executor,optimizer,catalog}`. Measured, same fixture:

      | | goopg before | goopg after | PG |
      |---|---|---|---|
      | `pg_class` | `222 / 50000` | `0 / 0` | `0 / -1` |
      | scan estimate | **50000** | **2550** | **2550** |

      goopg was estimating 50 000 rows for an empty table. Clearing `Stats`
      returns the relation to the never-analyzed state, which makes the
      block-derived fallback fire and produce PG's 10-page-floor answer exactly.
      The zeroed size row is persisted for the same reason VACUUM's is (P1-03) —
      an in-memory-only reset would come back stale after a restart. `reltuples`
      renders `0` where PG renders `-1`; goopg's virtual `pg_class` already uses
      `0` as its unknown sentinel elsewhere.
      *(second half also done)* `ANALYZE` and `VACUUM` now invalidate the plan
      cache — gate: `TestPlanCacheInvalidatingStmt`. Both are planned as
      `*optimizer.Utility`, not `*optimizer.DDL`, so the existing DDL-only
      trigger missed them and a session could ANALYZE a relation, re-run a
      cached query, and still get the plan chosen from the OLD statistics — the
      one case where the user has explicitly asked the planner to reconsider.
      Upstream reaches the same place differently: the `pg_statistic` and
      relstats writes emit relcache invalidation messages that `plancache.c`'s
      `ResetPlanCache` picks up. goopg has no such bus, so the trigger is the
      statement kind, which is why it is pinned by test. VACUUM is included
      because its Analyze pass updates `reltuples`/`relpages` (P1-03) even
      without the ANALYZE keyword.
- [x] **P1-04** — **verified already satisfied**, no code change. Index-only
      costing already uses the real visible fraction:
      `pathindexonly.go:109` passes `relAllVisibleFraction(tbl, relPages)`,
      which reads `catalog.RelAllVisible` (wired at `initdb/open.go:2525`).
      Measured on the bench cluster, `relallvisible` equals `relpages` for
      `part`/`orders`/`customer` on **both** engines, so the fraction is ~1.0
      and the saving is real. The other index producers correctly leave it
      zero: `heapPagesAfterVM` is a no-op unless `indexOnly`, matching
      `cost_index`. The only unmet part of the item's wording is structural —
      the value is computed per-producer rather than stored on `RelOptInfo` —
      which changes no plan.
- [x] **P1-05** — units, `internal/{optimizer,executor,catalog}`.
      **First half was already done:** `estimateRelSize` already implements
      `estimate_rel_size`'s density and 10-page floor (`relsize.go`, with
      dedicated tests `TestEstimateRelSize_DensityFromTupleWidth`,
      `..._NeverAnalyzedTenPageFloor`). Only the flag retirement remained.
      `GOOPG_RELSIZE_FALLBACK` is retired: it shipped at stage 2 — every
      consumer enabled — since M0125-0005, so the only states it still selected
      were "a planner production does not run", which is the hazard
      `flaglabels.go`'s header describes and which had already mis-stamped
      artefacts twice. The `stage` parameter is **removed**, not ignored: a
      parameter every caller passes a constant to, that nothing reads, is how
      the next reader concludes the staging still works. Three tests that
      asserted the now-unreachable "flag off" behaviour were rewritten to assert
      the positive direction they had been bracketing. No plan changes at the
      default. `scripts/planner-flags.env` regenerated:
      `retired(take2-P1-05)`.

### 1b — ANALYZE algorithm

- [-] **P1-06** **Declined as written — it would trade planner-input accuracy
      for ANALYZE speed.** *(measured 2026-09-02; see the Dropped table.)*
      goopg scans every block and counts visible tuples, so `reltuples` is
      **exact** — `operators_analyze.go:28-32` says so deliberately ("exact, not
      sample-scaled"). PG samples `300 × stattarget` blocks and *estimates*
      reltuples via `vac_estimate_reltuples`. Adopting PG's sampling would make
      ANALYZE ~5.7× faster on `lineitem` (**3.90 s → ~0.69 s**, measured against
      the reference cluster) and would make the planner's single most-used input
      an estimate instead of a measurement.
      ANALYZE is not in the query path, so the cost it saves does not appear in
      any OLAP figure this project is chasing. **Revisit if** autoanalyze
      overhead becomes a measured problem at larger scale factors, or if exact
      `reltuples` is shown not to matter — at which point the sampling and the
      `vac_estimate_reltuples` scaling must land together, since sampled row
      counts without the scaling would under-report by the sample fraction.
- [x] **P1-07** `n_distinct` override — gate:
      `TestNDistinctOverrideBeatsTheSampledFraction`, units. Confirmed exactly
      as recorded. The override wrote only `NDistinct` while `StaDistinct()`
      consults `NDistinctFrac` first above 0.1, so on any column whose sampled
      distinct fraction exceeds 10 % — most keys — it landed in a field nothing
      read. `NDistinctFrac` is now cleared alongside, which keeps the precedence
      rule in one place and matches upstream, where `analyze.c` applies the
      override to `stadistinct` itself and leaves no second field to disagree.
      ~~Fix the `ALTER TABLE … SET (n_distinct = …)` override, which
      writes only the absolute field while `StaDistinct()` consults the
      fraction first — so the override is silently ignored on any column whose
      sampled fraction exceeds 10 %. The Haas–Stokes estimator itself is
      already correct (07 §3.11 item 1); ledger row 777 is stale and should be
      closed.~~ *design: 08 §4.2.*
- [x] **P1-08** `analyze_mcv_list` — gate:
      `TestAnalyzeMCVListMatchesUpstream`, units. The 1.25 margin sat under a
      comment calling it "upstream's MCV_THRESHOLD margin"; **PG 18.3 contains
      no `1.25` in `analyze.c` at all**. Replaced with the hypergeometric
      significance test (`analyze.c:2980`), including upstream's
      complete-list-fits short-circuit (`:2676`). Measured MCV counts:

      | column | before | after | PG |
      |---|---|---|---|
      | `l_orderkey` | **100** | **0** | **0** |
      | `l_shipdate` | 100 | 23 | 21 |
      | `l_returnflag` | 1 | **3** | **3** |
      | `p_type` | 0 | 4 | 1 |

      `l_orderkey` is a ~1.5 M-distinct key and had **100 MCV entries**, so
      every `l_orderkey = ?` lookup was answered from a bogus MCV frequency
      instead of `ndistinct`. `l_returnflag` has three values and had one; it
      now has all three, matching PG — that is the column TPC-H Q1 groups by.
      The walk direction is load-bearing and upstream explains why: values are
      REMOVED from the full list, never added to an empty one, or a column whose
      common values all share a frequency admits nothing.
- [x] **P1-09** — **verified already satisfied**, no code change. goopg computes
      `bucketCount = min(statsTarget, len(nonMCV)-1)` and emits `bucketCount+1`
      bounds, i.e. `min(statsTarget+1, ndistinct − num_mcv)` — algebraically the
      same as `analyze.c:2744-2746` (`num_hist = ndistinct − num_mcv; if
      (num_hist > num_bins) num_hist = num_bins + 1`), and PG's `ndistinct`
      there is the SAMPLE's distinct count, as goopg's is. Measured agreement on
      all five probed columns after P1-08, including the `l_returnflag` case
      where three distinct values are all MCVs and both engines emit no
      histogram at all.
- [ ] **P1-10** `compute_index_stats` for expression indexes. *design: 08 §4.2.*

### 1c — Statistics storage

- [x] **P1-11c** *(new)* pg_statistic physical-tuple decode — `f07c20b1f`;
      gate: `TestPGStatisticRoundTripPreservesHistogram`,
      `internal/{catalog,executor,initdb,optimizer}`, units. Three silent bugs,
      each masking the next: element alignment in `decodeTextArray`, the 1-byte
      short varlena header, and `anyarray`'s `typalign 'd'`. Measured effect:
      TPC-H total **288.10 s → 257.75 s (−10.5 %)**, Q5 −32.2 %, Q7 −17.2 %,
      row counts identical on all 24 items. This is **not** P1-11 (TOAST for
      wide histograms), which remains open — but note the wide-text case may
      now behave differently, so re-measure P1-11 before starting it.

- [ ] **P1-11** TOAST support in the catalog heap writer so wide-text
      histograms persist; today the row is silently dropped and `orders`,
      `customer` and `partsupp` lost trailing-column rows *and* their size row.
      *design: 08 §4.3; gate: full regress suite (catalog format), re-init the
      data dir.* If deferred to a bounded-width interim, file the ledger row.

### 1d — Restriction selectivity

- [x] **P1-11b** *(date/timestamp half)* `convert_timevalue_to_scalar` — gate:
      `TestConvertTimevalueToScalar`, units. Measured on `lineitem.l_shipdate`
      at three cut points, estimate error fell from
      **-0.19 % / -0.99 % / -3.22 %** to **-0.06 % / -0.07 % / -0.04 %** — the
      worst case improving about eightyfold.
      The corrected rationale held: the error was bounded by *one bucket*
      because ISO-8601 strings already sort in date order, so this removes a
      residual half-bucket rather than the 0.31-vs-0.14 collapse the item
      originally claimed. A few percent, not a large win.
      *Still open:* `convert_string_to_scalar` for `text`/`varchar` and the
      network variants — `bucketFraction` still returns the documented 0.5 for
      those, which the test pins so the fallback cannot become a silent wrong
      number.
      ~~`convert_to_scalar` for non-numeric types —
      `convert_string_to_scalar`, `convert_timevalue_to_scalar` and the network
      variants. Today `numericValue` handles only the numeric family, so
      `bucketFraction` returns a flat **0.5** for `date`, `timestamp`, `text`,
      `varchar`, `char` and `bool`: every histogram interpolation on a date
      column lands mid-bucket by construction. **Highest-value single item in
      Phase 1** — date-window predicates are the dominant restriction shape in
      both suites. *design: 08 §4.4; gate: plan-parity + timing both suites.*
- [x] **P1-12** *(main half — landed with P1-13)* `conjunctionSelectivity`
      (`rangequery.go`) **is** `clauselist_selectivity`: it takes the flattened
      conjunct list, pairs range bounds per variable, and multiplies the
      remainder — replacing the inlined AND product that used to live in
      `clauseSelectivity`'s `OpAnd` arm.
      *Still open:* `RestrictInfo` selectivity **caching**. That is a
      planning-speed optimisation, not a plan-quality one — it changes no
      estimate — so it is filed under Phase 6 consolidation rather than kept
      open here as a statistics-fidelity item. *design: 08 §4.4.*
- [x] **P1-13** `RangeQueryClause` pairing — `71653da23`; gates: three unit
      tests, units. Measured on lineitem, one-year window:
      **1 855 086 -> 902 018 against an actual 910 180** (2.04x over -> 0.9%
      under). **Timing was NEUTRAL**: full 24-item A/B 253.51 s -> 254.65 s
      (+0.45 %, inside noise), row counts identical. Recorded as a negative
      result: on this corpus a 2x cardinality correction on the driving scan
      did not change which plan wins, because the shape is constrained by
      things this does not touch (Phases 3-6).
- [x] **P1-14** `nulltestsel` — `13430fc3a`; gates: two unit tests, units.
      Confirmed exactly as recorded. Also makes expressible the
      `nulltestsel(IS_NULL)` term P1-13 had to omit; wiring it into the pairing
      is a follow-up.
> **Measurement guidance for the rest of Phase 1** (established by three A/Bs,
> 2026-09-02): restoring statistics that were entirely ABSENT moved TPC-H time
> by −10.5 %; refining statistics that were merely INACCURATE did not move it at
> all (P1-13 +0.45 %, P1-14/P1-25 +0.88 %, both inside noise) — even though both
> made estimates markedly more PG-like. Judge the remaining Phase 1 items by the
> 09 estimate ratchet, not by per-item TPC-H timing, and do not spend a
> half-hour A/B on each. See analysis/planner-refactor-take2/perf-20260902-cumulative.md §4b.

- [ ] **P1-14b** Remaining per-clause estimators: general `scalararraysel`,
      `patternsel` for LIKE/regex beyond the access-path prefix rewrite,
      `rowcomparesel`, `booltestsel`, `var_eq_non_const`; align every
      `DEFAULT_*` constant with PG. *design: 08 §4.4.*

### 1e — Join selectivity

- [x] **P1-15** MCV pairing in `eqjoinsel_inner` — gates:
      `TestEqjoinselInnerMCVBeatsFlatNDistinct`,
      `TestEqjoinselInnerMCVDeclinesWithoutBothLists`, units. Every inner
      equi-join had been priced at `1/max(nd1, nd2)` — upstream's
      **no-statistics fallback** — even when both sides carried MCV lists. Full
      `matchprodfreq` / `unmatchfreq` / `otherfreq` / `totalsel1`-`totalsel2`
      formula ported, taking the smaller of the two viewpoints as upstream does.
      Pairing is indexed rather than nested-loop, for the reason already
      recorded on the semi arm (review/260831 OP1-2): a nested loop is
      `statistics_target²` comparisons per estimate.
      *Not done:* MCV equality is still compared by **rendered text**, not
      `oprcode`. That half of the item stands — it matters for types whose text
      form is not injective (float, numeric with trailing zeros), and the semi
      arm has the same limitation, so the two should move together.
- [ ] **P1-16** Re-diagnose TPC-H Q9's cardinality error with `estimate-audit`.
      Its recorded explanation — single-`nd` pricing of a two-column join — was
      retired by M0127-P5.6-f, which folds every equi-pair in both estimator
      arms (07 §3.11 item 3). Close ledger rows 779/781/784 as stale and file
      what the audit actually shows. *design: 08 §4.5; gate: `estimate-audit`
      final-joinrel bar on Q9.*
- [x] **P1-17** — **verified already satisfied**, no code change. All three
      named parts are present in `semiPairMatchFraction`: the MCV arm, `nd2`
      clamped by the inner rel's rows, and the `(1-nullfrac1)` factor. Checked
      line-by-line against `eqjoinsel_semi` — including the part that looked
      like a divergence: goopg discounts the matched MCVs from both distinct
      counts before the `nd2/nd1` heuristic, and so does PG
      (`nd1 -= nmatches; nd2 -= nmatches;` immediately before the comparison).
      The `isdefault → 0.5` punt matches too. With P1-15 the *inner* arm now has
      MCV pairing as well, so the note about it existing "only in the semi/anti
      arm" no longer holds.
- [!] **P1-18** **BLOCKED on P3-04** *(established empirically 2026-09-02)*.
      The search cannot see a non-inner join at all, so there is no arm to add a
      jointype switch to. `DPTRACE` for
      `customer LEFT JOIN orders ON c_custkey = o_custkey` reports
      `problem nrels=1 rels=customer … pairs=0`: `splitOuterSpine` peeled the
      outer join and only `customer` entered the search. `joinrelsize.go:100-102`
      states the same thing from the code's side. The item's premise — "the arm
      that chooses the plan sizes an outer join as an inner join" — is therefore
      not quite right: it never sizes one. Port the switch **after** P3-04
      deletes `splitOuterSpine`, or it will be dead code.
      ~~Port `calc_joinrel_size_estimate`'s full jointype switch into
      the search arm. `calcJoinrelSize` has **no join-type branch at all** —
      the LEFT/FULL floors and SEMI/ANTI arms live only in the legacy
      `estimateJoin`, so the arm that chooses the plan sizes an outer join as
      an inner join and a LEFT can estimate fewer rows than its preserved
      input.~~ *design: 08 §4.5.*
- [x] **P1-19** *(isunique half)* — gate:
      `TestUniqueSingleColumnKeyOverridesSampledNDistinct`, units.
      `get_variable_numdistinct`'s isunique branch (selfuncs.c:6332, "assume it
      is unique no matter what pg_statistic says") now overrides the sampled
      distinct count when a column is a unique key **on its own**;
      `has_unique_index` requires `nkeycolumns == 1` (plancat.c:2244), so a
      composite key such as Q9's two-column `partsupp` PK correctly says nothing
      about its members. Reads the scan's stamped `UniqueKeys` rather than
      looking up catalog indexes — one source, one answer, and
      `baseColumnOfTable` has no catalog handle.
      **Recorded as a safety net, not a measured win:** goopg's row count is an
      exact full-scan figure but its per-column statistics come from a capped
      reservoir, so this protects against a sample that understates a unique
      column. On TPC-H the PKs probed already reach the right answer without it
      (`o_orderkey` reports `n_distinct = -1` on both engines, and both estimate
      `rows=1`).
      *Not done:* PG's nullfrac derating in the FK formula — the second half of
      the item, untouched.
- [x] **P1-20** — **the item's premise was inverted, and the real gap was much
      larger.** `nconst_ec` corrects a DOUBLE-COUNT that arises because
      `equivclass.c` propagates a constant to every class member. goopg's
      closure synthesised only column-to-column equalities, so **no constant
      ever propagated** — there was nothing to double-count, and instead the
      transformation itself was missing. Measured on the bench clusters for
      `customer, orders WHERE c_custkey = o_custkey AND c_custkey = 42`:

      | | plan | cost |
      |---|---|---|
      | PG | `Index Cond: (o_custkey = '42')`, 16 rows | **13.30** |
      | goopg before | full scan of all 1 500 000 `orders` | **32249.25** |
      | goopg after | `Filter: (o_custkey = 42)`, 15 rows | **68.74** |

      A **470× cost reduction**. Gates: `TestEquivClassPropagatesConstants`,
      `TestEquivClassDoesNotPropagateNonLiterals`, units.
      Two things the implementation had to get right, both found by tests:
      the closure had **one caller** (`pushPredicatesIntoCrossJoins`, the legacy
      path), so with `GOOPG_PGSHAPED_DP` on by default the searched plan never
      saw it — it is now applied at the seam; and only the **constant** half is
      given to the search. Adding the transitive `a = c` too hands the search
      new *join* clauses and reshaped plans broadly, breaking
      `TestPreDPPinnedSemiKeysResolveAfterDP` on a query with no constants in it.
      That half stays on its legacy caller pending its own evaluation.
      `nconst_ec` itself is now *reachable* as a future concern and is not
      needed yet — goopg has no FK `1/ref_tuples` shortcut to double-count.
- [ ] **P1-21** **Precondition NOT met by P1-15 — restated 2026-09-02.** The
      item says to delete the cap "once P1-15/16 show the backstop unneeded".
      P1-15 improved the **measured** equi-join path (both sides have MCV
      lists); this cap sits in the **unmeasurable fallback**
      (`cardinality.go:655-666`), which fires only when *no key was proven*.
      MCV pairing cannot reach it, so P1-15 does not discharge the
      precondition. Deleting it now would move those joins from
      `min(l·r·0.005, max(l,r))` to `l·r·0.005` — a large increase on big
      inputs, with no evidence the backstop is unneeded.
      ~~Delete the `max(outer,inner)` fallback cap (M0126-0010), which
      has no upstream counterpart, once P1-15/16 show the backstop unneeded.
      It is guarded (it fires only when no key was proven and every residual
      factor was a default), so this is a cleanup, not a correctness fix.~~
      *design: 08 §4.5.*
- [x] **P1-28** `pg_stats.correlation` — `86b3b96a2`; gate:
      `TestPgStatsRendersCorrelation`, units. Confirmed exactly as recorded: the
      view rendered a hard-coded NULL behind a header comment claiming ANALYZE
      does not collect correlation. It does, `cost_index` consumes it, and a
      zero prices every index scan at `max_IO_cost`. Renders NULL when zero,
      mirroring the writer, which omits the slot rather than storing a zero.

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

- [x] **P1-25** Size DISTINCT through `estimate_num_groups` — gates:
      `TestDistinctIsSizedNotPassedThrough`, units. `SELECT DISTINCT l_shipmode
      FROM lineitem` now estimates **rows=7**, matching PG exactly; it was the
      child's 6 001 255. Both `Distinct` (all output columns) and `DistinctOn`
      (the ON list only) are sized. Set-op sizing is left open — `estimateSetOp`
      already has its own arm and is a separate question.
      **Exposed a latent executor crash**: `bitmapHeapScanOp` called
      `o.mctx.Reset()` at two sites without the nil guard `Close()` already had,
      so a bitmap heap scan running without a memory context segfaulted on its
      first page boundary. Unreachable until this item changed which plans win —
      "an unwinnable path is an untested path". Fixed in the same commit.
- [x] **P1-26** Collapse the column-stats resolvers — gate:
      `TestColumnStatsResolverIsOneArmList`, units. `columnStatsForChild` was a
      second full walker duplicating `resolveBaseColumn`'s arms, and its own
      comment recorded the rule it was breaking ("kept in step with
      `columnNDistinctForChild`'s arm list — hard-won rule: sibling paths change
      together"). **They had already drifted**: the selectivity-side walker had
      no `*IndexScan` arm, so a column reached through an index-probed leaf
      resolved to *no statistics at all* and every clause over it fell to a
      default selectivity — while the ndistinct-side walker resolved the same
      column fine. It now delegates, so the index-probed leaf gains MCV and
      histogram access and future drift is impossible rather than merely
      discouraged.
- [ ] **P1-27** CTE output statistics — **scope corrected 2026-09-02.** Plain
      CTE columns already resolve to their base relation's statistics:
      `resolveBaseColumn` has a `*CTEScan` arm (`joinkeyproof.go:164-167`) and
      the `initialRelRows` comment claiming "CTE scans have no per-column
      statistics" is stale for that case. The real gap is **aggregated** CTE
      outputs — TPC-DS's `year_total` shape, where the output columns are
      `sum(...)` and trace to no base column, so `eqsel` falls to
      `DEFAULT_NUM_DISTINCT` at 0.005 per conjunct.
      Note that 0.005 is **also what PostgreSQL does** there, so goopg's
      `rows <= 1` body-count guard is a deviation that happens to help, not a
      fidelity gap. Replacing it therefore needs genuinely derived statistics
      (propagating through aggregation), not a closer port — which is a larger
      item than the wording suggests, and removing the guard without them would
      regress. *design: 08 §4.7.*

---

## Phase 2 — Cost-model completeness

Exit: every cost GUC demonstrably changes at least one plan; parity budget does
not grow; no query slower than 1.2×.

- [x] **P2-01** Planner context — gates:
      `TestPlannerSettingsReachTheJoinSearch` (negative control: reverting the
      seam to `defaultCostParams()` fails it),
      `TestDefaultPlannerSettingsMatchTheHardWiredParams`,
      `TestUnstampedContextGetsDefaultsNotZeroes`, units,
      `internal/{optimizer,executor,postmaster}`. Both real cost sites now read
      the carrier; only the display-only `DeriveLegacyDisplayCost` still calls
      `defaultCostParams()`. Plan-neutral by construction — every `Plan()`
      caller stays on `DefaultPlannerSettings()`, which is *defined as* what
      `defaultCostParams()` reads rather than a second copy of the constants.
      `newResolveContext` initialises `settings` to the DEFAULTS, never the zero
      value: a zero `PlannerSettings` would price every page and tuple at 0.0,
      so an unstamped path degrades to today's behaviour instead of nonsense.
      *design: impl/P2-A-planner-context.md.*
      ~~*design landed and agent-reviewed:*
      [impl/P2-A-planner-context.md](impl/P2-A-planner-context.md).
      Scope corrected: this item builds and threads the CARRIER only; no GUC
      reaches the planner until P2-02. Review found the first design threaded
      the wrong constructor (the 23 `&resolveContext{` literals never reach a
      cost site; the real one is `newResolveContext`, 30 sites) and that a
      `parent`-walking accessor would read **another session's** settings,
      because `parent` is assigned from the package global `planParent`
      (`planner.go:13791`, self-documented as goroutine-thread-unsafe). Settings
      are now a required constructor parameter instead. Also: `bitmapOverCorrelatedProbe`
      is reached from `planUpdate`/`planDelete` through parentless contexts, so
      DML is threaded too.~~
- [x] **P2-02** Session cost GUCs reach the planner — gates:
      `TestSessionPlannerSettingsRoundTripsUnits`,
      `TestSessionPlannerSettingsHonoursOverrides`,
      `TestCostGUCsReachTheCostingOnAHashJoin`,
      `TestCostGUCConversionIsTotal`, units,
      `internal/{optimizer,postmaster,executor}`. Live on the TPC-H bench
      server: `SET seq_page_cost = 1000` switches a parallel Hash Join to a
      **Merge Join over index scans**, and `SET work_mem = '64kB'` reprices the
      same hash join **14835 → 23478**.
      **Two channels were needed, not one.** The extended-protocol and
      prepared-statement sites hold a `*misc.SessionRegistry`; the simple-query
      site (`executeOneSimpleStmt`) holds only `ctx.GetSetting`. Wiring just the
      first left EXPLAIN showing unchanged costs while every unit test passed —
      caught only by a live probe. `EXPLAIN` also plans its inner statement
      recursively and had to be threaded, since EXPLAIN is the only way a user
      *observes* a cost.
      **09 §5's P2 bar is only PARTLY discharged**: three of the nine GUCs are
      pinned by unit test on a hash-join fixture, and `seq_page_cost`/`work_mem`
      by the live evidence above. The remaining four
      (`random_page_cost`, `cpu_index_tuple_cost`, `effective_cache_size`,
      `parallel_*`) need fixtures containing an index or parallel path, which
      the current two-table fixture does not produce. Filed here rather than
      claimed. ~~a test asserts each of
      `seq_page_cost`, `random_page_cost`, `cpu_tuple_cost`,
      `cpu_index_tuple_cost`, `cpu_operator_cost`, `effective_cache_size`,
      `work_mem`, `parallel_setup_cost`, `parallel_tuple_cost` changes a plan.
      *design: 08 §5.1; gate: 09 §5 P2 row.*~~
- [ ] **P2-02b** Correct `work_mem`'s `BootVal` from `512MB` to PostgreSQL
      18.3's `4MB`. **Note (2026-09-02):** the planner's hard-wired copy is
      `hashsize.DefaultMemLimitBytes` (`hashsize.go:83`, `512 << 20`), which is
      also the executor's no-session fallback — so the two agree today only
      because both are wrong in the same direction. Changing one without the
      other is worse than changing neither; this item, P2-02 and P0-12 form one
      ordered group. The planner's copy is hard-wired to the goopg value, so
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
- [x] **P2-04** Plan-cache correctness under live cost GUCs — gates:
      `TestPlannerCostGUCsOverriddenDetectsEveryCostGUC`,
      `TestPlannerSessionInputsActiveCoversBothFamilies`, the existing
      `TestPlannerScanTogglesActiveDetectsEveryToggle`, units,
      `internal/{postmaster,utils/misc,optimizer}`. Landed **before** P2-02, per
      the ordering the P2-A review established. A session that has SET any of
      the nine cost GUCs now neither reads from nor writes to the shared cache,
      exactly as a scan-toggle session already did. Detection is by
      **override**, not by value: comparing against `BootVal` would mean parsing
      unit strings (`4GB` vs `4194304kB`) and would wrongly clear a session that
      SET a GUC to its default — the test pins that case explicitly. The four
      guard sites now call one predicate, `plannerSessionInputsActive`, so a
      third family cannot be added without every site picking it up.
      ~~**PREREQUISITE OF P2-02, not a later item** *(established
      2026-09-02 by review of impl/P2-A)*. `internal/postmaster/plancache.go:42`
      is a server-level cross-session cache keyed on
      `(dbOid, normalizeCompatSQL(sql))` with **no GUC fingerprint**
      (`dispatch.go:1780-1785` states this). Its only guard,
      `plannerScanTogglesActive` (`dispatch.go:1786`), checks four *scan* GUCs
      and none of the nine *cost* GUCs. P2-02 converts `dispatch.go:1161`, which
      sits inside the cache-guarded block — so P2-02 without this would let one
      session's `random_page_cost` leak into another session's cached plan.~~
      *Remaining under this item:* the planner context as part of the cache KEY
      (rather than a bypass), and removing the bypass once keyed.
      ~~Plan-cache correctness under live GUCs — the planner context is
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

- [!] **P4-01b** *(second slice — leaf narrowing: ATTEMPTED, REVERTED, WRONG
      ANSWERS)*. The approach the P4-A review recommended — narrow the leaf on
      the PATH's node, leave the binding space wide, let `baseRelLayout` re-base
      by name and `boundaryMap` pad the pruned coordinates — **does not
      work**. It was implemented, made to fire, and produced **incorrect
      results**:

      | | pre | post |
      |---|---|---|
      | TPC-H Q2 | 418 rows | **0 rows** |
      | TPC-H Q5 | 5 rows | **0 rows** |
      | TPC-H Q18 | 12 rows | 12 rows, **different tuples** |
      | total time | 393.67 s | 379.31 s (**faster, and wrong**) |

      21 of 24 items matched. `cmd/tpch-runner -digest` + `-diff` caught it;
      **row counts alone would have passed Q18**, which is exactly why 08's
      risk R5 requires value-level comparison for a projection change.
      It is reverted. `P4-01a` (per-path width) is independently correct and
      stays.
      **What was learned, so the next attempt does not repeat it:** the
      ordering bug found first (`buildInitialRels` runs eight lines before
      `s.neededCols` is assigned in `makeRelFromJoinlist`, so the narrowing
      silently never fired) is real and must be fixed too — but fixing it only
      exposed the deeper problem. `baseRelLayout`'s by-name re-basing and
      `boundaryMap`'s filler are **not sufficient** to make a pruned base-relation
      leaf safe inside a join tree; something above still reads a coordinate
      the leaf no longer publishes, and a typed-NULL pad turns a join match into
      a non-match (Q2/Q5 → 0 rows). A working version needs the projection to be
      visible to whatever computes those coordinates — i.e. the real
      `PathTarget`/`setrefs` work of P4-01/P6-02/P6-07, not a leaf swap.
      ~~*(second slice — leaf narrowing: CODE LANDED BUT DORMANT)*
      `narrowLeafToNeededColumns` narrows a **bare** seq-scan leaf to the
      columns the statement reads and sets the path's `NCols`/`AvgVarBytes`. It
      is applied to the **path's node**, never `rel.baseLeaf`, so the seam's
      offset invariant holds (narrowing there makes the seam decline and fall
      back to the legacy plan — P4-A §4).
      **It does not fire on TPC-H, because `neededColsKnown` is false.**
      Instrumented and measured on Q14: `narrow-guard known=false table=part
      leaf=*optimizer.SeqScan` — the leaf is exactly the bare seq scan the
      helper wants, and the *needed-column set* is what is unavailable. That is
      also why the code comment in `pathindexonly.go` records the index-only
      path "fired zero times across all 22 TPC-H queries": **the same flag
      gates both.**
      **Resume point:** find why `neededColumnNames` / its carry to `searchCtx`
      yields `known=false` for a plain `SELECT agg(...) FROM a, b WHERE ...`.
      None of `collectStmtColumnNames`' documented decline conditions
      (`SetOp`, `With`, `ValuesRows`, `GroupingSets`, `WindowClause`,
      `Locking`, natural join, table function, LATERAL) apply to Q14, and the
      expression-level `default:` arm was instrumented and never fired — so the
      decline is in the statement walk or in the carry through
      `relfromjoinlist.go` / `geqo.go` / `joinsearchseam.go`. Fixing it unlocks
      **both** this narrowing and index-only paths on the whole corpus.
      **Treat the helper as unverified until then** — an unwinnable path is an
      untested path, and this one has never executed in production.

- [x] **P4-01a** *(first slice — per-path width)* Paths carry their own
      `NCols`/`AvgVarBytes` — gate: `TestPathCarriesItsOwnWidth`, units.
      `pathgen.go` read column counts from the **rels**, under a comment
      justifying it as "a parameterised path returns fewer ROWS than its rel but
      the same columns". True of parameterisation, **false of projection**: an
      index-only path emits only the columns its index covers, so the hash
      geometry was solved for the relation's full width while the executor
      measured the narrowed node's schema at runtime (`len(o.left.Schema())`).
      Planner and executor disagreed about the size of the same hash table —
      the divergence the shared `hashsize.Choose` exists to prevent, live at
      HEAD and found by the P4-A review.
      Read through `pathNCols`/`pathAvgVarBytes` so the fallback to the rel's
      figures lives in one place; zero means "not narrowed".
      *This is the foundation P4-01's remaining slice needs:* narrowing an
      ordinary heap scan to the needed columns is now a matter of setting these
      two fields on its path and emitting the narrowed schema, since the
      per-rel/per-path blocker is gone.


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
| P0 (partial) | 2026-09-02 | `f2ac4fdfc` … `8c3e9ac3c` | A/B on this tree: 288.10 s → **257.75 s** (−10.5 %) | not re-measured | instrument built, roll-up not yet captured (P0-05/06/07 open) | not measured | Instruments only — no planner behaviour changed in P0-01…P0-04d. The timing move comes from `f07c20b1f`, a **statistics** fix found *by* the instrument. The 288.10 s control is this A/B's own and is **not** comparable to the 227.0 s baseline (different binary and histogram state). See [analysis/planner-refactor-take2/perf-20260902-pgstatistic-decode.md](../../../../analysis/planner-refactor-take2/perf-20260902-pgstatistic-decode.md). |
| P1 (mostly closed) | 2026-09-02 | `f07c20b1f` … `7ef387324` | 395.53 s → **399.33 s** (+0.96 %, noise) against the P0-12 aligned control; row counts identical | not re-measured | not yet captured | not re-run since `ae78cc6eb` | 14 items closed, 4 already-satisfied, 1 declined (P1-06), 2 blocked (P1-18 on P3-04, P1-21's precondition). Biggest single win is **P1-20**: constants never propagated across an equivalence class, costing **470×** on `a = b AND a = 42` — invisible on TPC-H because the corpus has no such query. |
| ~~P1 (partial)~~ | 2026-09-02 | `f07c20b1f` … `13430fc3a` | 288.10 s → **257.75 s** (−10.5 %) from `f07c20b1f`; P1-13/P1-14 timing-neutral | not re-measured | not yet captured | not measured | P1-11c is the whole timing move. P1-13 corrected a 2.04× cardinality error to 0.9 % with **no time change** — recorded as a negative result, not buried. See [perf-20260902-cumulative.md](../../../../analysis/planner-refactor-take2/perf-20260902-cumulative.md). |
| P2 (partial) | 2026-09-02 | `6b503e47c` … `78ef045c8` | control moves 248.71 s → **403.27 s** — see note | not re-measured | not yet captured | 95 PASS / 0 MISMATCH (SF0.5 gate, first run this session) | P2-01/P2-04/P2-02 landed in the **corrected order** (P2-04 before P2-02, reversing the bundle). `SET random_page_cost` now changes a plan. The timing move is **P0-12**, not a regression: aligning `work_mem` 512MB→64MB with the PG reference removed an 8× memory advantage goopg had been measured with, so 403.27 s is the first *honest* control. It also exposed the bottleneck — see [FINDING-workmem-advantage.md](impl/FINDING-workmem-advantage.md). |
| P3 | | | | | | | |
| P4 | | | | | | | |
| P5 | | | | | | | |
| P6 | | | | | | | |
| P7 | | | | | | | |

### Pattern: this Phase-1 list over-states what is missing

Four items so far turned out to be already satisfied, or satisfied by a
different and better mechanism than the one specified:

| item | as written | actually |
|---|---|---|
| P0-10 | TPC-DS anchors inert | fixed a month earlier by `63056c544` |
| P1-01 | *persist* per-index relpages | read live via `RelNBlocksFunc`, which is what `get_relation_info` does — persisting would add a staleness class PG does not have |
| P1-02 | retire `estimateIndexGeometry`'s synthesis | already superseded for `relpages` whenever storage answers; only tree height and the partial-index case remain |
| P1-04 | `allvisfrac` reaches the costing | already does, and measures ~1.0 on the bench cluster |

The bundle was written from a code read that under-credited existing work, so
**each Phase-1 item should be verified against the tree before it is
implemented.** Two of the four cost nothing to check and would have cost real
effort to build.

### Priority change established by measurement, 2026-09-02

The `DPPATH` trace (P0-11) gave the search's own numbers for TPC-H Q14 and
settled where the remaining gap lives:

```
producer=index.ordered relids={0} rows=6001255 total=657623.09  accepted
producer=mergejoin     relids={0,1}            total=754717.55  accepted
producer=join.hash     relids={0,1}            total=1811944.24 dominated
```

goopg's `part` rows are **548 bytes** where PostgreSQL's are **6**, because
there is no `PathTarget` — so the hash table is ~50× oversized, batches at
64 MB, and a merge join over a full 6 M-row index scan wins on cost. 13.9 s
against PG's 1.08 s.

**P4-01 is therefore promoted ahead of the rest of Phase 1 and all of Phase 3.**
Design: [impl/P4-A-pathtarget.md](impl/P4-A-pathtarget.md). Three A/Bs had
already shown that refining *cardinality* did not move TPC-H time; this explains
why — the binding constraint is on the **cost** side, and specifically on the
widths fed into it.

## Dropped and deferred

Items removed from the plan, with the reason and the ledger row. Keep the
original wording — negative results are only legible if they survive
(08 §1, 09 §9).

| item | date | reason | ledger row |
|---|---|---|---|
| P0-10 (TPC-DS row anchors inert) | 2026-09-02 | Claim was stale. `ci/batch/lib/summarize.py:651-656` already reads `expected_rows`; fixed by `63056c544` (2026-07-30), a month before the bundle was written. A `git log -S` would have refuted it. | n/a — no defect |
| P0-08's `PLAN_GATE_REQUIRE=1` "set by ci/batch" | 2026-09-02 | Withdrawn: ci/batch never invokes `make plan-gate`; it calls `make plan-diff` with an explicit pinned `LABEL` (`stage-tpch.sh:234`). The flag would never fire. | n/a |
| P0-09's standalone oracle re-capture | 2026-09-02 | Rescoped to a timer change only. Field 5 (`secs`) has no readers anywhere in `scripts/` or `ci/batch/`; a re-capture would truncate a git-tracked fixture for no measurable gain. | n/a |
| P1-06 (two-stage block sampling) | 2026-09-02 | Declined as written. goopg's full scan yields EXACT `reltuples`; PG's sampling estimates it. The change buys ~5.7× on ANALYZE (3.90 s → 0.69 s on `lineitem`) at the cost of making the planner's most-used input an estimate. ANALYZE is not in the query path. Revisit if autoanalyze overhead is measured to matter. | file on revisit |
