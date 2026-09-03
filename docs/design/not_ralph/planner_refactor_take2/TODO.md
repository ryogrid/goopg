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
      **ATTEMPTED AND REVERTED (2026-09-03) — BLOCKED on settings propagation.**
      The change itself is three lines (GUC `BootVal`, `postgresql.conf.sample`,
      `hashsize.DefaultMemLimitBytes`) and all three must move together, as the
      note above says. Measured on the aligned bench cluster it cost TPC-H
      245.7 s -> 314.4 s, with Q9 +434 %, Q7 +109 %, Q2 +82 %, at an UNCHANGED
      `work_mem` of 64MB and an unchanged session GUC. Root cause:
      `PlanWithSettings` stamps its settings at exactly one site, while
      `newResolveContext` re-defaults them at 30 — so a subquery's join search
      (Q9 is entirely inside one) plans under the hard-wired default no matter
      what the session says. The 512MB default is therefore LOAD-BEARING for
      real planning, not a fallback. Fix the propagation first, then this item
      is safe. Full evidence, including the four hypotheses ruled out by
      measurement: `impl/FINDING-planner-settings-not-propagated.md`.
      **Re-diagnosed 2026-09-03: this is blocked on P4-01, not on propagation.**
      goopg's tuples are ~39x wider than PG's at the same point in the same plan
      (1542-3164 B vs 23-81 B) because there is no PathTarget projection, so its
      hash tables need ~39x the memory for the same rows. At PG's `work_mem`
      goopg batches (`Batches: 8`, 97 MB) where PG does not (`Batches: 1`,
      38 MB), and Q9 goes 6.2 s (PG) vs 187 s (goopg).
      **CORRECTED, same day:** that causal story is withdrawn. Batching and
      widths are IDENTICAL in goopg's fast and slow arms (`Batches: 8`,
      97482kB, width 3164 in both), so they cannot explain the 12x. What
      actually differs is a join-method flip — a two-key parallel hash join
      becomes a single-key merge join, 6.0M rows become 24.0M, and the Gather is
      lost — and the slow plan is 2.8x MORE expensive by goopg's own cost model
      (2,941,575 vs 1,047,157), so the cheaper candidate was not available at
      that budget. Whether it was never generated or was priced higher is the
      next measurement (instrument `addPath`). P4-01 is a plausible contributor,
      not a proven blocker.
      **ROOT CAUSE FOUND (same day, via DPPATH): the merge join is costed on
      POST-filter rows while it emits PRE-filter rows.** Both candidates ARE
      generated for the flipping relation `{2,4}`; the merge path is simply
      under-priced. `joinpathsmerge.go:362` passes `joinrel.Rows` where PG
      passes `mergejointuples` — its own comment names the right quantity. The
      merge emits 24,989,610 tuples and is charged 77,016 for them (0.0031/tuple
      against a `cpu_tuple_cost` of 0.01); 6,001,255 x 0.01 = 60,012 is the
      charge actually made. Merge cost is work_mem-independent, hash cost is not,
      so at PG's budget the hash path (1,906,774) crosses the under-priced merge
      path (826,630) and Q9 goes 15.4s -> 187s. Full evidence and the fix:
      `impl/FINDING-mergejoin-costed-on-postfilter-rows.md`. That mispricing is
      now FIXED (`c281b0830`, TPC-H 258.28s -> 240.73s, 24 MATCH).
      **P2-02b still must not land, for a worse reason: it produces WRONG
      ANSWERS.** With `work_mem` at PG's default, Q9 returns 175 rows — the
      correct count — with wrong tuples, summing 30,270,658,609.88 against the
      PG oracle's 7,528,869,517.19 (4.02x). The merge join drops an equi-clause
      it does not use as a merge clause and emits the unfiltered product
      (24,005,020 rows where the two-clause join gives 6,001,255). The bug is
      PRE-EXISTING — identical digest before and after the cost fix, reproduces
      at session-start HEAD — and goopg's 512MB `work_mem` default (128x PG's)
      is the only thing hiding it. See
      `impl/FINDING-CRITICAL-mergejoin-wrong-answers.md`. **FIXED in
      `13d53603f`**: `generateMergeJoinPaths`' first candidate trimmed its
      merge-clause list to the groups the outer's ordering serves and passed the
      original residual through, so the dropped clauses were evaluated nowhere.
      With that fixed, P2-02b is CORRECT at PG's `work_mem` (24 MATCH on values)
      and is now purely a performance question: it costs 239.7s -> 295.9s
      (+23.4%). Remaining work on P2-02b is closing that gap, not correctness.
      That gap is ENTIRELY Q9 (+47.1s) and Q7 (+9.7s); every other query is
      neutral.
      **Re-measured 2026-09-03 after the ndistinct two-form fix**, which moved
      TPC-H -8.1% and 79 TPC-DS plan shapes and so invalidated the earlier
      verdict on its face. It did not help here: P2-02b now costs
      215.62s -> 265.44s (**+23.1%**, against +23.4% before), and the breakdown
      is the same two queries with the same character — Q9 11.61->51.68s,
      Q7 8.68->18.04s, together the whole gap. Values are now correct
      (24 MATCH), which they were not before the merge-join dropped-clause fix.
      That is a useful negative result: a 1000x class of estimation error is NOT
      what makes P2-02b expensive, so the diagnosis stands unchanged — tuple
      WIDTH (P4-01) and the lost Gather (Phase 5). Both have the same two causes, measured at equal cardinality
      (goopg 321,056 rows vs PG ~319k on the same join): goopg's tuples are
      14-39x wider (1098-3164 B vs 23-81 B) for want of a PathTarget, so it needs
      97 MB and 8 batches where PG needs 38 MB and 1 — P4-01; and at the smaller
      budget the plan moves onto index-scan-driven joins, which goopg's parallel
      post-pass cannot drive because it only recognises sequential scans, so the
      Gather is lost too — Phase 5. See P4-A rev 4.
- [x] **P2-02c** *(all 6 landed; zero registry.OnChange bridges remain)* Move the six process-global GUC bridges
      (`enable_memoize`, `enable_nestloop_index`, `enable_hashagg`,
      `enable_presorted_aggregate`, `geqo`, `geqo_threshold`) onto the planner
      context. Today a `SET` in one session changes the planner for every
      session, while a cached plan may ignore the same `SET`.
      *design: 08 §5.1; gate: a cross-session isolation test.*
      **Verified 2026-09-03** — the premise is exactly right, and the bridges
      are in `cmd/goopg/main.go` (`registry.OnChange`), not under `internal/`.
      Each writes a package-level `atomic`, and the `enable_nestloop_index`
      bridge's own comment states the consequence: "most-recent SET wins
      process-wide". **`enable_memoize` is now per-statement** — carried on
      `PlannerSettings`, read by `getMemoizePath` from `costParams`, bridge
      deleted; `SetMemoizeEnabled` survives as the test hook and the
      `GOOPG_MEMOIZE` env kill-switch. Verified live: session A `SET
      enable_memoize=off` no longer changes what a fresh session B reads.
      TPC-H 241.94s, 24 MATCH. **`enable_hashagg`,
      `enable_presorted_aggregate`, `geqo` and `geqo_threshold` followed** —
      the three aggregate rules take the settings as a parameter from
      `planSelect`, and the GEQO dispatch reads `prob.cp`. All four bridges are
      deleted; verified live that a session's SET no longer changes what a fresh
      session reads, and that `SET enable_hashagg=off` still yields
      GroupAggregate. TPC-H 240.52s, 24 MATCH.
      **`enable_nestloop_index` followed too, and it WAS a one-liner** — the
      note here previously called it harder on the grounds that
      `rewriteJoinsToNLI` has no `costParams` in scope. It does not need one:
      its single caller sits in `planSelectWithSettings`, where `plannerSet` is
      already in scope. Threading the parameter was the whole change.
      **Zero `registry.OnChange` bridges remain.** Verified live: after one
      session sets all six to off, a fresh session reads `on` for every one.
      Note the seed convention this established: a process global now supplies
      only the DEFAULT for a planner call with no session
      (`DefaultPlannerSettings` reads `HashAggEnabled()` / `GeqoEnabled()` /
      …), which is what keeps the `GOOPG_*` env kill-switches and the test hooks
      working. What a global no longer carries is a session's SET.
- [x] **P2-03** `hash_mem_multiplier` — gate:
      `TestHashMemMultiplierReachesTheBudget`, the existing
      `TestCostParamsWorkMemMatchesExecutorFallback` sibling guard, units.
      goopg budgeted a hash build at `work_mem` **alone**; PG's budget is
      `work_mem × hash_mem_multiplier` (`get_hash_memory_limit`,
      nodeHash.c:3622). With PG's default of 2.0, **every hash table in goopg
      had half the memory PostgreSQL would give it** — at the aligned
      `work_mem = 64MB`, a 64MB budget against PG's 128MB. That is a second,
      independent reason a build PG keeps in one batch spills here, alongside
      the missing projection (P4-01).
      `hashsize.HashMemLimit` is the single shared expression; planner and
      executor both call it, as they both call `Choose`.
      Two things the invariant tests caught: `defaultCostParams` still wrote the
      bare default, and `DefaultPlannerSettings` took its `WorkMem` from the
      already-multiplied `cp.workMem`, **squaring** the multiplier. Also added
      to the P2-04 cache guard, since it changes plans exactly as `work_mem`
      does.
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
- [~] **P2-05** `enable_*` via `Path.DisabledNodes` as PG 18 does; delete
      producer-skipping. All `enable_*` GUCs become live; retire
      `enable_nestloop_index`. *design: 08 §5.2; gate: an `enable_X=off` test
      per flag, plan-parity + timing.*
      **JOIN METHODS LANDED (`656236ab1`).** `enable_hashjoin`, `enable_mergejoin`
      and `enable_nestloop` had exactly one reference outside their registration
      — the `pg_settings` view — so `SET enable_hashjoin = off` was accepted and
      did nothing. They now set `Path.DisabledNodes` via `disabledNodesFor`,
      which is PG 18's mechanism: the producer still runs, so a query whose only
      legal plan uses a disabled method still plans. Verified live
      (Hash Join -> Merge Join, same answer). TPC-H 242.38s, 24 MATCH.
      Still open, hence `[~]`: producer-skipping for the SCAN toggles
      (`enable_seqscan` and friends) is untouched, and `enable_nestloop_index`
      is not retired — it was made per-session under P2-02c instead.
- [x] **P2-06** `cost_material` and a Material path; the merge-join
      materialise-inner decision becomes a cost comparison. *design: 08 §5.3.*
      **Landed 2026-09-03, and NOT as written on either half.**
      (a) *No Material path was introduced, and one would be wrong.* goopg's
      executor materialises unconditionally on BOTH sides — `openNestedLoop`
      always wraps the inner in `newMaterializeOp`
      (join_nl_stream.go:108), and the merge executor buffers per equal-key
      group (`bufferGroup`). A path-level Material node would buffer twice. The
      merge half of this reasoning was already recorded at
      joinpathsmergeouter.go:52-72; the nested-loop half is the same argument.
      (b) *The merge-join materialise-inner decision cannot become a cost
      comparison*, for the same reason: goopg's merge executor does not
      mark/restore, so there is no decision to cost.
      What DID land is `cost_material`'s substance where goopg actually pays it:
      the nested loop's inner is priced as materialised — build charged once at
      `2 * cpu_operator_cost * tuples` plus a spill charge, rescans at
      `cpu_operator_cost * tuples` — instead of a full re-execution per outer
      row. A PARAMETERISED inner is excluded (its parameters differ per outer
      row, so it genuinely re-executes, and PG's create_material_path is
      likewise unreachable for one).
      This is the term the Q54 ledger row at join_nl_stream.go:110-124 asked
      for: "PG never meets that wall because cost_rescan prices exactly this
      case; costInnerNestLoop has no such term yet."
      Gates: TPC-H 240.60s, 24 MATCH on values; TPC-DS SF0.5 PASS=95
      MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, verdict-changes none,
      **runtime-moves 0**, 27 plan shapes changed (attributable — the baseline
      is P2-07's sweep). Q54 14s->12s, Q47 12s->11s.
- [x] **P2-07** `cost_rescan` — nested-loop inner and CTE re-execution stop
      being free. *design: 08 §5.3; gate: plan-parity + timing; expect NL
      counts to move.*
      **Landed 2026-09-03.** The defect was the opposite of the item's framing:
      a rescan was not free, it was charged the inner's FULL total_cost —
      startup included — on every outer row. `cost_nestloop` charges
      `inner_run_cost + (outer_rows-1) * inner_rescan_run_cost`, both RUN costs
      (costsize.c:3304-3327), so the inner's startup is paid once. goopg paid it
      `outerRows` times, which could only make a nested loop look too EXPENSIVE
      — so the shapes it suppressed were never observed. `pathRescanCost` is
      `cost_rescan` (costsize.c:4638) with the Material/Sort arm
      (`cpu_operator_cost` per tuple, plus a re-read charge on spill) and the
      default re-execute arm; the Memoize arm already existed.
      Gates: TPC-H 243.58s against 239.72s (inside the ~1.7% drift), 24 MATCH on
      values; TPC-DS SF0.5 **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0**,
      verdict-changes none, one runtime move (Q94 7s->2s FASTER). CTE
      re-execution is NOT covered — `cost_rescan`'s T_CteScan/T_WorkTableScan
      arm has no goopg path kind to attach to yet, so that half of this item
      remains with Phase 4's upper-planner work.
- [-] **P2-08** `cost_subplan` in `(startup, total)` PG units with hashed and
      non-hashed arms. *design: 08 §5.3.*
      **PREMATURE — its output would feed nothing.** goopg has no SubPlan path
      kind and no site where a subplan's cost reaches `addPath`. The existing
      `estimateSubplanCostPerCall` (subplan_cost.go) is a ROW-COUNT proxy,
      explicitly "ordering safety" only, and it has **zero production callers**:
      of its 12 references, 11 are its own recursion and the 12th is a comment
      in unnest.go:3734. Converting it to PG `(startup, total)` units today
      would be writing a cost function nothing consults. The prerequisite is
      that SubPlan costs participate in path comparison at all, which belongs
      with Phase 4's upper-planner work.
- [~] **P2-09** `btcostestimate` completeness: descent cost, `num_sa_scans` for
      ScalarArrayOp, the unique-index single-tuple clamp, per-tuple index qual
      cost. *design: 08 §5.3.*
      **Descent cost was already present** (`btreeIndexAMCost`, tree height + 1
      at 50 `cpu_operator_cost`). **The unique-index single-tuple clamp landed
      2026-09-03**: a UNIQUE index with equality on every key column matches at
      most one tuple, whatever the selectivity arithmetic says. The selectivity
      route could not reach 1.0 on its own — it multiplies per-column estimates
      that each carry a floor — so a multi-column unique probe was priced for a
      range scan the index can never perform. `fullyBound` at
      pathparamindex.go:360 is exactly PG's "equality on every key column"
      precondition. Gates: TPC-H 237.34s (best of the session), 24 MATCH on
      values; TPC-DS SF0.5 PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0,
      verdict-changes none, **plan shapes: 99 same / 0 changed**, one runtime
      move and it is faster (Q61 5s->2s).
      **The per-tuple index qual cost was implemented, MEASURED, and reverted
      (2026-09-03).** PG charges
      `numIndexTuples * (cpu_index_tuple_cost + qual_op_cost)` where
      `qual_op_cost = cpu_operator_cost * len(indexQuals)` (selfuncs.c:7228-7234);
      goopg charges only the first term, so testing an index qual is free. The
      term is faithful and it is NOT double-charged — goopg's heap side charges
      `cpu_tuple_cost` in PG's filter-qual slot, and `costSeqscan` already takes
      `numQualOps`, so seq scans pay for their quals too and the change is
      symmetric. It still cost TPC-DS SF0.5 **1115s -> 1152s (+3.3%)**, with 60
      plan shapes changed and ten of the twelve movers >=3s being regressions.
      That is OUTSIDE the sweep-to-sweep variance band measured on this harness
      the same day (1110 / 1115 / 1119 s across three sweeps, +/-0.4%), so it is
      a real regression, not noise. The gate did not catch it: PASS=95,
      MISMATCH=0, verdict-changes none, runtime-moves 0 — every individual query
      stayed under the 2.0x threshold while the aggregate moved.
      Adding one correct PG term to a cost model missing its neighbours made
      outcomes worse, which is the bundle's own recorded lesson ("cost-term
      tuning alone never fixed a query"). It should land with the rest of
      `btcostestimate`, not alone, and its acceptance test is the aggregate
      sweep total rather than the per-query gate.
      **`num_sa_scans` is BLOCKED, and looking for it found something bigger.**
      goopg builds no index path for a ScalarArrayOp at all: `p_partkey IN
      (1,2,3,4,5)` plans as a Parallel Seq Scan with
      `Filter: (p_partkey = ANY (...))` where PG uses `Index Scan ... Index
      Cond: = ANY`. So `num_sa_scans`, which prices the repeated descents such a
      scan performs, has no consumer — the third item in this phase in that
      position, after P2-08 and P2-10. The missing PATH is the real gap.
      **The ndistinct two-form bug (FIXED 2026-09-03).** That probe also showed
      goopg estimating 5000 rows for the 5-element IN-list against PG's 5.
      `ColumnStats` stores upstream's one signed `stadistinct` as TWO fields —
      `NDistinct` (absolute) and `NDistinctFrac` (relative) — and both
      `eqSelectivityForColumn` and `resolveBaseColumn` read the ABSOLUTE field
      alone. Every column whose distinct count scales with the relation — most
      keys — therefore read as ndistinct ZERO and fell to `DEFAULT_EQ_SEL`.
      `ColumnStats.ResolvedNDistinct(tuples)` now applies PG's convention
      (`get_variable_numdistinct`: `ndistinct = -stadistinct * ntuples`) and both
      call sites use it.
      Measured: `p_partkey IN (1..5)` 5000 -> **5**, exactly PG's;
      `l_orderkey IN (1,2,3)` 90018 -> **14** against PG's 51 — a 1765x error
      reduced to under 4x, in the safe direction.
      Gates: TPC-H **215.62s** from 234.51s (**-8.1%**, and -12.2% across this
      bundle), 24 MATCH on values. TPC-DS SF0.5 PASS=95 MISMATCH=0 CKMISMATCH=0
      ERROR=0 TIMEOUT=0, verdict-changes none, **aggregate -3.6%**, 79 plan
      shapes changed, two runtime moves and BOTH are faster (Q20 5s->1s,
      Q76 5s->2s). No regression on either suite.
- [-] **P2-10** `compute_semi_anti_join_factors` and the semi/anti early-out in
      nestloop and hashjoin costing. *design: 08 §5.3.*
      **BLOCKED on Phase 3 — there are no semi/anti PATHS to cost.**
      `splitOuterSpine` peels outer/semi/anti links off the chain before the
      search runs (joinsearchseam.go:50-56), so the DP sees inner joins only —
      joinpaths.go:220 says as much ("under 03 §4.4's INNER-only pin"). Verified
      2026-09-03: `JoinTypeSemi` / `JoinTypeAnti` appear in NO cost, path or
      search code, and neither `hashJoinInputs` nor `nestloopCost` takes a join
      type at all. There is nowhere to attach an early-out factor. The
      prerequisite is P3's `deconstruct_jointree` with `SpecialJoinInfo` in the
      DP, which is precisely the item that brings semi/anti into the search.
      Same shape as P2-08: a faithful cost term whose consumer does not exist.
- [~] **P2-11** `estimate_hash_bucket_stats`: bucket-size and MCV-frequency
      skew terms in hash-join costing. *design: 08 §5.3; depends on P1-15.*
      **BUCKET-SIZE TERM LANDED (`bb32b976c`)** — TPC-H 234.51s, 24 MATCH;
      TPC-DS PASS=95 MISMATCH=0, aggregate -1.2%. The MCV-frequency half is NOT
      done: PG folds the inner key's MCV frequency in too, which needs the MCV
      list at the cost site — a second plumbing step. Hence `[~]`.
      The scoping note below predates the implementation and is kept for its
      reasoning:
      **Reachable, unlike P2-10 and P2-08 — hash joins ARE costed — but large.**
      Scoped 2026-09-03. `hashJoinCost` charges the probe as
      `cpu_operator_cost * numHashClauses * outerRows + cpu_tuple_cost *
      outputRows`: it prices hashing the outer key and emitting matches, and
      charges NOTHING for walking a bucket. PG charges
      `hash_qual_cost.per_tuple * outer_matched_rows *
      clamp_row_est(inner_rows * innerbucketsize * innermcvfreq) * 0.5`
      (final_cost_hashjoin), which is the only term that can see a skewed key.
      Without it a hash join on a low-NDistinct column is priced identically to
      one on a unique column — the degeneracy this repo has already been bitten
      by (`reselectDegenerateHashKeys`, Q78's collapsed bucket).
      The work is plumbing, not arithmetic: `hashJoinInputs` carries only
      innerRows/innerCols/avgVarBytes, so the inner key's ndistinct and MCV
      frequency must reach the cost site from the caller, which holds the rel
      and the clause list.
      **LANDED 2026-09-03.** `estimateHashBucketSize` is
      `estimate_hash_bucket_stats` reduced to the ndistinct-derived fraction;
      `hashJoinCost` charges `cpu_operator_cost * numHashClauses * outerRows *
      clamp_row_est(innerRows * bucketsize) * 0.5`. The bucket fraction is
      computed per ORIENTATION — the two hash paths build different relations —
      and arrives as a closure, since `addHashJoinPath` has no `searchCtx`.
      A zero fraction means "no usable statistic" and SUPPRESSES the term
      rather than guessing, so a stats-less plan costs exactly as before; a
      `get_variable_numdistinct` result flagged `isdefault` is treated the same
      way, for the reason PG's own caller checks that flag.
      *Not the whole function.* PG also folds in the inner key's MCV frequency;
      that needs the MCV list at the cost site and is a second plumbing step.
      Recorded rather than hidden.
      Gates: TPC-H **234.51s** (best of the bundle, from 237.34s), 24 MATCH on
      values. TPC-DS SF0.5 PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0,
      verdict-changes none, **aggregate -1.2%**, 88 plan shapes changed.
      *The two apparent regressions were NOT regressions.* The sweep read
      Q76 2s->5s (2.5x) and Q12 4s->8s (2.0x), which breach the P2 exit bar of
      "no query slower than 1.2x", and the landing commit recorded them as real
      follow-up work. Investigated immediately afterwards: **both plans are
      BYTE-IDENTICAL between the two builds**, in the full sweeps and in an
      isolated re-run alike, and re-run alone the timings are Q76 2s->2s (no
      change at all) and Q12 3s->4s. This change moved 88 plan shapes; it did
      not move these two. Same plan and same data cannot be a cost regression —
      it is sweep-context variance (server age, cache, GC), the "sweep-tail
      collapse" trap this repo already records.
      The lesson is the procedure, not the outcome: **a per-query runtime move
      is only attributable if that query's PLAN changed.** The sweep's
      STATUS-DELTA arm reports timing moves without consulting the plan capture
      sitting beside it, so it will keep naming queries whose plans are
      identical. Check the plan before believing a per-query move.
      *Baseline hygiene, worth repeating:* the sweep script diffs against the
      most recent report, which was P2-09's REVERTED qual-cost run (1152s). That
      flattered this change to -4.3%. The honest figure is against the last
      clean sweep (1115s), which is the -1.2% above. Always check which report
      the delta used.
- [x] **P2-12** `mergejoinscansel` start/end selectivities in merge-join
      costing. *design: 08 §5.3.*
      **Landed 2026-09-03, END selectivities only.** A merge join stops once one
      side passes the other's maximum key, so only that fraction of each input's
      RUN cost is paid; `mergeJoinCost` charged a full pass over both.
      *The sort caveat, resolved.* The scoping note below feared that scaling a
      sorted input would price a scan the executor never performs. PG's own code
      settles it (costsize.c:3686-3745): BOTH branches scale identically, and
      the sort's own `startup_cost` — where the full input read lives — is
      charged UNSCALED, with only the read-out portion scaled. goopg's
      `sortPathFor` has the same shape, so the scaling is safe either way.
      *Not implemented:* PG's START selectivities. They model a seek to the
      first match that goopg's merge does not perform, and both are reported as
      0 so the omission is a no-op rather than an approximation.
      *A trap found on the way:* histogram bounds are stored as STRINGS, so
      every comparison needs the column's type — `histCmp` falls back to
      `strings.Compare` without it, which orders "10" before "9".
      `joinVarStats` now carries `typeName`, and a missing type refuses the
      estimate rather than guessing.
      Gates: TPC-H 215.01s against 215.62s (neutral), 24 MATCH on values.
      TPC-DS SF0.5 PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0,
      verdict-changes none, 5 plan shapes changed.
      **The aggregate arm read +2.1% and that reading was NOT this change.**
      Splitting the sweep by plan attribution: the 5 queries whose plans moved
      are net **-1s**; the 90 whose plans are IDENTICAL are net **+23s**. The
      three queries the per-query arm named (Q61, Q45, Q12) all have unchanged
      plans. So the aggregate move is environmental drift.
      *That is the lesson to carry:* the `TOTAL` arm and the plan capture are
      complementary and neither is sufficient alone — TOTAL catches the broad
      shallow regression the per-query arm misses, and the plan capture is what
      says whether a move is yours.
      **Scoped 2026-09-03 — implementable, with one caveat that must not be
      skipped.** The arithmetic is reachable: `mergejoinscansel` needs
      `get_variable_range` and `scalarineqsel`, and goopg already has the
      histogram machinery for both (`histogramOpSelectivity`, `bucketFraction`,
      `histCmp` in selectivity.go). `mergeJoinCost` currently charges a FULL
      pass over each input — `(outer.Total - outer.Startup) + (inner.Total -
      inner.Startup)` — with no start/end scaling at all.
      *The caveat.* The scan selectivities model the merge STOPPING EARLY when
      one side's key range is exhausted. goopg does that only when both inputs
      arrive pre-sorted, where `mergeJoinStream` really is streaming ("one row
      per side plus the current inner group, and nothing else",
      join_merge_stream.go:444). When an input needs sorting,
      `mergeSortedSource.fill` drains the whole child into sorted runs first, so
      the sort reads everything however narrow the join's key overlap is.
      Applying the selectivities to a sorted input's cost would price a partial
      scan the executor never performs — the sibling-path divergence this bundle
      keeps paying for. Check what PG does in `final_cost_mergejoin` for the
      `outersortkeys != NIL` case before porting; do not assume.
      *Plumbing.* `tryMergeJoinPath` has no `searchCtx`, so the key columns'
      stats must arrive as a closure, exactly as `mergeTuplesFor` does (P2-07).
      *Risk direction.* This makes merge joins CHEAPER. Land it with the
      `TOTAL` arm of `scripts/tpcds-sweep-diff.py` watching the aggregate — the
      per-query 2x gate missed a 3.3% move on P2-09's qual cost.
- [x] **P2-13** Bitmap lossy-page handling to match `tbm_calculate_entries`
      **and** removal of the double charge, in ONE commit. Landing the removal
      alone is a known regression (TPC-DS Q72 73 s → timeout). *design: 08 §5.4;
      gate: plan-parity + timing both suites, all bitmap queries.*
      **Already done in-tree; verified and closed 2026-09-03** (the work is
      earlier commits of this bundle, not this entry's — what was missing was
      the bookkeeping). Both halves confirmed by reading the code, not the
      comments: `costbitmap.go` computes `lossyPages` and USES it, where it
      once discarded the pair with `_ =`; and `costBitmapHeapScan` returns
      `startup + runCost`, with no second `indexCost.Total`.
      Acceptance evidence is the census 08 §5.4 names. The recorded failure mode
      for landing the removal ALONE was 22-24 bitmap scans against PG's 6.
      Measured now, both halves in: **goopg 9, PG 6** over the same TPC-H 22.
      TPC-DS Q72 — the query the design records going 73 s -> timeout when the
      two were unpaired — runs **105 s PASS** in this session's sweeps, with
      TIMEOUT=0 across all 99.
      The residual 9-vs-6 is plan-parity work, not these two cost errors.

---

## Phase 3 — Search coverage

Exit: every PG-only join spine is OFFERED at its level or has a named reason;
Q72-class queries produce one search problem; `join-order` diffs decrease.

- [ ] **P3-01** `make_outerjoininfo`-equivalent `SpecialJoinInfo` construction
      with the full field set (`min_lefthand`, `min_righthand`, `syn_*`,
      `lhs_strict`, `semi_can_hash`, `semi_operators`, `semi_rhs_exprs`).
      *design: 08 §6.1.*
      **Scoped 2026-09-03 — the field set already exists; the blocker is name
      resolution, and a partial fix is UNSAFE.**
      `SpecialJoinInfo` (specialjoin.go:16) already declares every field the
      item lists: `MinLefthand`, `MinRighthand`, `Syn*`, `Jointype`, `Ojrelid`,
      the four `Commute*` sets, `LhsStrict`, `SemiCanBtree`, `SemiCanHash`,
      `SemiOperators`, `SemiRhsExprs`. What is missing is POPULATION:
      `Syn*`, `Jointype` and an optimistic `SemiCan*` are filled;
      `MinLefthand`/`MinRighthand` are set to the `Syn*` sets as a deliberate
      conservative overestimate; `LhsStrict`, `Commute*`, `Ojrelid`,
      `SemiOperators` and `SemiRhsExprs` are never populated.
      *The blocker.* PG derives `min_lefthand`/`min_righthand` from the rels the
      join qual REFERENCES. goopg cannot: `makeSpecialJoinInfo` is called from
      `deconstructFromItem(item, firstRel, lim, collapseJoins)`
      (collapse.go:398), which has no catalog, no bindings and no resolver, and
      `parser.ColumnRef` carries only `{Schema, Table, Column}` — names, not a
      relation index. PG has no equivalent problem because by
      `deconstruct_jointree` time every Var already carries `varno`.
      *The hazard, and why this must not be done by halves.* `min = syn` is not
      laziness, it is the SAFE direction: an overestimate only forbids join
      orders. An UNDERestimate permits a reordering PG forbids, which is a
      wrong-answer class, not a slow-plan one. Any implementation that resolves
      qualified references (`a.x = b.y` via the FROM items' aliases) but cannot
      resolve bare ones must therefore fall back to `syn` on ANY uncertainty
      rather than use a partial set.
      *Two routes:* thread a name-to-leaf map built from the FROM items into
      deconstruction, or run deconstruction after resolution. The first is
      smaller and keeps the phase order; the second is what PG effectively does.
      Neither is a small change, and neither is observable until P3-03/P3-04
      consume the result — P3-01 alone moves no plan.
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
- [x] **P3-09** Widen `RelSet` past 16 base relations. *design: 08 §6.5.*
      **Landed 2026-09-03.** `RelSet` is `uint32` and `maxSearchRels` is 32,
      following the representation rather than restating it. A FROM clause with
      more than sixteen base relations could not be REPRESENTED by the search
      and fell back to the legacy planner entirely.
      The blocker `joinsearch.go` recorded against this — "deleting the old
      guard while the old DP is still the production path would hand it
      13-16-relation queries it cannot finish" — is **gone**: that guard lived
      in `bushy.go`, which was deleted with the bushy DP at M0127-P6.3. The
      comment outlived the constraint, which is why this looked riskier than it
      is.
      Gates: TPC-H 213.84s against 215.01s, 24 MATCH on values. TPC-DS SF0.5
      PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, verdict-changes none,
      **1 plan shape changed of 99** — which is the expected result, since no
      benchmark query comes near sixteen relations. This is a capability change,
      not a plan change.
      *Incidental confirmation:* Q45 and Q61 came back FASTER here (6s->2s each)
      after being named SLOWER by the previous sweep with identical plans. Their
      oscillation is independent evidence for the attribution rule recorded
      under P2-12.
- [x] **P3-10** Finish the GEQO wiring. *design: 08 §6.5; depends on P2-01, P0-13.*
      **All seven are on the planner context (2026-09-03).** `geqo` and
      `geqo_threshold` moved off their process-global bridges under P2-02c; the
      other five — `geqo_effort`, `geqo_pool_size`, `geqo_generations`,
      `geqo_selection_bias`, `geqo_seed` — reached NOTHING, with `geqoSearch`
      running at a hard-coded effort of 5, bias at a literal 2.0 and the PRNG at
      a fixed seed. The comment on that seed said "the planner has no session in
      scope to read the GUC", which stopped being true at P2-02 and is the kind
      of stale note this bundle keeps finding.
      Zero is MEANINGFUL for `geqo_pool_size` and `geqo_generations` — PG reads
      it as "derive me from effort / pool size" — so both default to 0 rather
      than to a substitute, and `geqo_seed = 0` maps to the PRNG's existing
      fixed state, which is what makes the change plan-neutral at the defaults.
      Gates: TPC-DS SF0.5 PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0,
      **99 plan shapes same / 0 changed**, total-delta +0.1% — the predicted
      result, since GEQO cannot fire below `geqo_threshold` = 12 and no
      benchmark query approaches twelve relations. TPC-H 24 MATCH on values.
      *TPC-H timing needed an A/A control to read.* P3-10 measured 219.78s and
      222.25s; the P3-09 baseline measured 213.84s and then **221.01s on a
      re-run with identical code**. The ranges overlap, so the apparent +2.8%
      was harness variance — the drift band on this machine had widened well
      past the ~1.7% measured earlier in the session. Re-run the BASELINE before
      believing a TPC-H delta of that size.
- [x] **P3-11** Answer "why does the NLI arm lose 23 of 25 times" using P0-11
      provenance, and act on the finding. *design: 08 §6.4; gate: the NL census
      gap (1 vs 25) moves.*
      **Answered 2026-09-03 — and the premise was wrong in two ways.**
      *The "1 vs 25" baseline is stale.* Measured over the TPC-H 22: goopg emits
      **18** Nested Loops against PG's **30**. A binary from the START of this
      bundle already emitted 19, so the gap was never 1-vs-25 in this tree, and
      the cost work landed since (P2-06, P2-07) moved the census by ONE (19->18)
      — it is not what closed the gap, and nothing in this bundle did.
      *NLI is not being crushed; it loses NARROWLY.* `DPPATH` over the whole
      corpus: `nestloop.index` is offered **694** times and accepted **23**
      (3.3%), against `mergejoin` 1108 accepted / 303 dominated (79%) and
      `join.hash` 246 / 520. But comparing the cheapest NLI against the cheapest
      ACCEPTED path for the same relation set:
      `{0..7}` 197199.66 vs 197093.62 (**0.05%**), `{0..6}` 555436.63 vs
      548039.97 (1.3%), `{0..5,7}` 209059.05 vs 197045.82 (6%), `{0..5}`
      472155.50 vs 420956.23 (12%).
      **The finding, and the action.** There is no single mispricing to fix: at
      a 0.05% margin the winner is decided by accumulated small errors, not by
      one defect. So the action is NOT a targeted NLI change — it is the
      remaining `btcostestimate` and hash-bucket terms, each of which will flip
      a few of these either way. That also explains an observation from this
      bundle that would otherwise be puzzling: cost fixes moved 79-88 TPC-DS
      plan shapes while the NL census barely moved. Tight margins flip in both
      directions.
      A targeted NLI adjustment would be exactly the "cost-term tuning" this
      bundle's own lessons warn never fixed a query, and at these margins it
      would be tuning to the benchmark.
- [x] **P3-12** Delete `reorderCommaFromByCardinality` — the pre-search greedy
      reorder that biases the search's input. *design: 08 §6.6; gate:
      plan-parity + timing both suites, own commit.*
      **The BEHAVIOUR is gone (2026-09-03); the dead code is not yet deleted.**
      `planSelectWithSettings` no longer permutes the FROM list before planning.
      It was a join-ORDER decision taken before the cost-based search ran, so it
      biased the search's input rather than informing it.
      Gates: TPC-H 220.98s, inside the 219.78-222.25s range the previous item
      measured for an unchanged binary, 24 MATCH on values. TPC-DS SF0.5 PASS=95
      MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, verdict-changes none,
      **aggregate -3.2%**, 64 plan shapes changed.
      Split by attribution: the 64 queries whose plans MOVED are **-41s**; the
      35 whose plans are identical are +7s. All three queries the per-query arm
      named (Q12, Q91 slower; Q76 faster) have IDENTICAL plans and are not
      attributable. So the real effect is -41s on the queries it touched.
      *Cleanup done in a second commit:* `joinorder.go` is deleted along with
      three of its four test files — **1327 lines removed, 48 added**.
      Two things were KEPT rather than deleted with it, and neither was obvious
      from the item:
      `flattenOrBranches` and `walkConjuncts` have live users outside the file
      (`qual_canonical.go` and its test), so both moved there; and
      `TestPlanQ85IsDeterministic` guards a property that OUTLIVES the reorder —
      that a plan does not depend on Go map iteration order — so it stays, with
      its comment retargeted away from the FROM-order tests it was written to
      back. Gated: TPC-H 221.29s, 24 MATCH on values, all package suites green.

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
      **Agent-reviewed 2026-09-03; P4-A is at rev 5 and is NOT ready to
      implement against.** Two structural findings:
      (1) *The needed-column collector declined this item's own motivating
      query.* `collectExprColumnNames` had no `ExtractExpr` arm, so a single
      `extract()` set `neededColsKnown = false` for the whole statement —
      disabling index-only scans and every narrowing mechanism. TPC-H Q7, Q8 and
      Q9 all use `extract(year from ...)`. **FIXED in `915ce7882`** and verified
      false-before / true-after.
      (2) *The mechanism §4 chose is the one that returned wrong answers.* The
      seam's coordinates are safe; the invariant P4-01b broke is "a node's
      `Output()` equals the row its operator emits" — `newSeqScanOp` takes
      `schema: p.Output()` but `cols: p.Table.Columns` and decodes the full
      table width. So a `PathTarget` with setrefs fixup is necessary but NOT
      sufficient: either the scan operators project, or a real `Project` goes
      below the build side. The Project option §4 dismissed must be re-costed.
      A review prediction was TESTED and did not hold: fixing the collector does
      NOT return the Gather (Q9 at 4MB is 56.94s against 55.42s before,
      unchanged), so width and Gather do not share a gate and the 87/13 split in
      `MEASUREMENT-p202b-width-vs-gather.md` stands.
      **That check has now been RUN, and the answer is that the item IS
      mis-scoped as P2-02b's blocker.** Narrowing `orders` to Q9's two needed
      columns takes it from 128 batches to **64** at PG's `work_mem` — not to 1;
      the 2.0M-row intermediate goes 512 -> 256. `hashsize.EntryBytes` charges
      `ncols x 48 + 24`, so **48 bytes per column** is co-dominant with the
      column COUNT, and that is 07 §6's Datum residual, currently listed as out
      of scope. P4-01 keeps its own justification (2-4x fewer batches) but does
      not unblock P2-02b on its own. See
      `impl/FINDING-p401-alone-is-not-enough.md`.
      **READY TO IMPLEMENT (P4-A rev 9).** The last open question — whether a
      narrowed leaf disturbs the seam — is answered by shipped behaviour, not by
      reading code: TPC-H Q13 already runs
      `Hash Left Join <- Index Only Scan on customer (width=32)` beside a
      full-width `Seq Scan on orders (width=448)`, and Q9 the same with a
      `Parallel Index Only Scan on partsupp`. A narrowed leaf under a join works
      end to end in the DEFAULT path, with all 24 queries correct. Still
      untested: `Project` above a join SUBTREE rather than above a scan.
      **Mechanism DECIDED (P4-A rev 7): insert a real `Project` below the build
      side; do NOT make the scans project.** `projectOp` sizes
      `o.out = acquireRow(len(o.targets))` and takes its schema from the same
      target list, so it narrows row and schema together BY CONSTRUCTION and
      cannot reproduce P4-01b — where `newSeqScanOp` holds the width in two
      places (`schema: p.Output()` vs `cols: p.Table.Columns`) and P4-01b moved
      one. §7's objection that the parallel leaf switches block this is wrong:
      `drivingScan` already has `case *Project` (parallel.go:457) and
      `extractSeqScanFromPlan` likewise (parallel_hash_build.go:320).
      **§12's table has now been re-taken on current HEAD (P4-A rev 6).** Widths
      per node — goopg 3164/2716/2168/1094 B against PG's 81/54/32/23 — top-level
      cardinality 321,056 against 318,748 (0.7 % apart, so the equal-cardinality
      premise holds), goopg `Batches: 8` at 97482kB against PG's `Batches: 1` at
      38176kB, and 15.9 s against 6.8 s. Two caveats recorded rather than
      smoothed over: goopg emits no per-node `actual rows` for joins under a
      `Gather`, so per-LEVEL cardinality equality cannot be shown from this plan;
      and "97 MB vs 38 MB" is not one comparison, since goopg's figure is one
      batch of eight (≈780 MB total) against PG's single-batch total.
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
- [-] **P6-03** Delete `rewriteScanInputsWithSingleTablePredicates`.
      *design: 08 §9.3; gate: byte-identical plans.*
      **ATTEMPTED 2026-09-03 — the pass is LOAD-BEARING and the gate fails
      decisively.** It is not a legacy leftover the search has superseded.
      The unit suite catches it first:
      `TestIndexKeyRangeCorrelationStaysSubPlan` fails with "range-correlated
      EXISTS must NOT decorrelate to a semi/anti join".
      The item's own gate — byte-identical plans — then fails on TPC-H Q20:
      cost **16035.65 -> 104591.20**, 6.5x, and the correlated SubPlan's
      `Index Cond: (l_partkey = $1)` with its `l_suppkey = $0 AND l_shipdate
      ...` filter collapses to `Filter: (true)`. The pass is what pushes a
      correlated predicate into the scan input so the probe can use an index;
      without it the predicate is lost and the subplan degenerates.
      This is one of the four passes joinsearchseam.go:127 lists as REWRITING a
      searched tree, and it correctly skips searched subtrees — so what it still
      serves is the shapes the search declines, which is exactly why deleting it
      is not neutral. Retiring it needs the costed producers to cover the
      correlated-scan-input case first, which is Phase 4 work, not a Phase 6
      cleanup.
- [-] **P6-04** Delete `rewriteJoinsToNLI` and `GOOPG_NLI_COSTGATE`.
      *design: 08 §9.3; depends on P3-11.*
      **ATTEMPTED 2026-09-03 — load-bearing, and P3-11 explains exactly why.**
      Removing the call changes 99 plan lines. TPC-H Q4's semi-join goes
      **8694.33 -> 109043.90 (12.5x)**: the `Nested Loop Semi Join` disappears
      and is replaced by a `Hash Semi Join` over a full `Seq Scan on lineitem`
      (2,000,418 rows). Q21's anti-join degrades the same way.
      This is the OTHER HALF of P3-11's finding, and the two must be read
      together. P3-11 measured the search's own `nestloop.index` producer at 23
      accepted out of 694 offered, losing by margins as small as 0.05%.
      `rewriteJoinsToNLI` is what supplies the NLI shapes the search does not
      win. Deleting it while the search still under-selects NLI removes the
      compensation without fixing the cause.
      The dependency recorded on P3-11 is therefore right, but the implication
      is the reverse of what the item assumes: P3-11 does not clear this for
      deletion — it shows the search cannot yet replace it. Retire it once the
      search selects NLI on its own merits, which P3-11 says is a calibration
      question spread across the remaining `btcostestimate` and hash terms.
- [-] **P6-05** Delete the dead `reconcileNLILayout`. *design: 08 §9.3.*
      **DO NOT DELETE — it is not dead (verified 2026-09-03).** It has no
      direct production call site, which is what the item's premise rests on,
      but `assertSearchedTreeNeedsNoReconcile` (searchedtree.go:200) calls
      `reconcileNLILayoutBody(n)` and PANICS if the pass moved any column
      reference. That assertion runs UNCONDITIONALLY at
      `createplanroot.go:137` — no build tag, no env gate — on every searched
      plan.
      So the pass executes in production on every plan the search produces. It
      is the ORACLE for a live correctness tripwire: the assertion's own comment
      says "it lets the real pass run and then compares. That is safe precisely
      because the claim is that it changes nothing; if it changed something the
      tree was already wrong and the process is about to stop."
      Deleting the function would delete the tripwire that catches a createPlan
      arm binding a join key to the wrong column — the failure mode that
      produces a plan which RUNS and joins on the wrong column, which is the
      hardest class this bundle has to defend against.
      If the goal is to remove the pass, the assertion must first be replaced by
      an independent oracle; retiring both together removes the check, not just
      the code. The item as written would do exactly that.
- [ ] **P6-06** Retire the planner flags — `GOOPG_PGSHAPED_DP`,
      `GOOPG_PGSHAPED_COLLAPSE`, `GOOPG_RELSIZE_FALLBACK`,
      `GOOPG_INDEXKEY_HARVEST`, `GOOPG_HASH_OUTER_JOIN`,
      `GOOPG_INDEX_PROBE_MULT` — regenerating `scripts/planner-flags.env` from
      `flaglabels.go` each time. *design: 08 §9.4.*
      **`GOOPG_HASH_OUTER_JOIN` measured 2026-09-03 — safe now, but not a win,
      so NOT flipped.** Its deferral condition (ledger 2026-08-04) was "once
      doc 04's cost currency can say when a sort is actually cheaper", and this
      bundle's cost work (P2-06, P2-07, P2-11, P2-12, the ndistinct fix) made
      that testable. With the flag on: TPC-DS SF0.5 PASS=95 MISMATCH=0
      **CKMISMATCH=0** — which is the answer to the recorded worry that the hash
      path "keeps every row but changes their ORDER" — total-delta +0.0%, and
      only **2 plan shapes changed of 99**. Both are attributable: Q51
      14s->11s, Q97 11s->15s, net **+1s**. The three queries the per-query arm
      named (Q53, Q37, Q12) all have IDENTICAL plans. TPC-H 216.41s against
      221.29s, 24 MATCH on values.
      So the risk is gone and the benefit is not there: the cost model still
      cannot tell when the sort is cheaper, it merely breaks even. Flipping a
      default on a wash would be churn. Re-measure after the remaining
      `btcostestimate` terms.
      *The other five flags are not retirable yet, for a reason this bundle
      established today:* `GOOPG_PGSHAPED_DP`'s off path is the legacy planner,
      and P6-03/P6-04 showed the legacy REWRITE passes are still load-bearing
      (6.5x on Q20, 12.5x on Q4). Retiring that flag means deleting a path the
      search cannot yet replace.
- [ ] **P6-07** Add a `setrefs` phase if P6-02 shows the executor still needs
      explicit column resolution. *design: 08 §9.5.*

---

## Phase 7 — Acceptance

- [ ] **P7-01** Full acceptance run: both suites, S-cold and WARM, plan-parity
      roll-up, `estimate-audit --reference` ratchet, complete timing table.
      *gate: 09 §7 bars A1–A5, B5, C1–C4.*
- [~] **P7-02** Verdict document under
      `analysis/planner-refactor-take2/acceptance-<date>/README.md` with the
      09 §6.6 header, before/after roll-ups, and an explicit statement of
      everything that got worse. *gate: 09 §9.*
      **INTERIM document written 2026-09-03**
      (`analysis/planner-refactor-take2/acceptance-20260903/README.md`). It is
      explicitly NOT the acceptance document 09 §6.6 specifies, and says so in
      its first paragraph: Phases 4 and 5 have not started, so the 09 §7 bars
      (A1-A5, B5, C1-C4) cannot be evaluated and P7-01 cannot run meaningfully.
      What it does carry is the shape 09 §6.6 asks for over the work that DID
      land — the stage-by-stage TPC-H roll-up, the five correctness bugs, the
      four things that got WORSE and why each was kept or reverted, the three
      measurement-methodology defects found by being wrong first, the six items
      determined must-not-be-done, and 07 §6's width residual measured at equal
      cardinality. Rewrite it as the real acceptance document when Phases 3-5
      land; do not delete it, since the "what got worse" and methodology
      sections are the parts that do not reproduce themselves.
- [x] **P7-03** Update the deferral ledger for every item left open, and record
      the executor-side residuals (07 §6) as their own follow-up with resume
      points. *gate: 08 §1 P6.*
      **Done 2026-09-03** — 11 rows appended to `.ralph/deferral_ledger.md`,
      one per item this bundle left open or determined must-not-be-done, each
      with its MEASUREMENT and a resume point:
      `take2-P2-02b` (+23.1%, blocked on P4-01 and Phase 5),
      `take2-P2-08` / `take2-P2-10` / `take2-P2-09-saop` (all three blocked on
      an absent consumer, resume after Phase 3/4),
      `take2-P2-09-qualcost` (faithful but -3.3%; land with the rest of
      btcostestimate),
      `take2-P6-03` (6.5x) / `take2-P6-04` (12.5x) / `take2-P6-05` (a live
      tripwire's oracle) — the three Phase 6 "cleanups" that are load-bearing,
      `take2-P6-06` (flip safe but a wash),
      `take2-P3-01` (name-resolution blocker plus the underestimate hazard),
      and `take2-executor-residual` — 07 §6's width gap, measured at EQUAL
      cardinality for the first time (1098-3164 B vs PG's 23-81 B; 97 MB and 8
      batches against 38 MB and 1; Q9 63.8s vs 6.2s).

---

## Progress log

One row per closed phase. Numbers come from the 09 §6.6 artifact header.

| phase | closed | commit range | TPC-H total (goopg / PG) | TPC-DS SF0.5 total | plan-parity TPC-H | plan-parity TPC-DS | notes |
|---|---|---|---|---|---|---|---|
| baseline | 2026-08-31 | `82c05a5f6` … `6c65ceb20` | 227.0 s / 22.9 s = 9.9× | 1173 s / 536 s = 2.2× | not measured (spine `shape_mismatches` = 46) | not measured | starting state; see 07 §2 |
| P0 (partial) | 2026-09-02 | `f2ac4fdfc` … `8c3e9ac3c` | A/B on this tree: 288.10 s → **257.75 s** (−10.5 %) | not re-measured | instrument built, roll-up not yet captured (P0-05/06/07 open) | not measured | Instruments only — no planner behaviour changed in P0-01…P0-04d. The timing move comes from `f07c20b1f`, a **statistics** fix found *by* the instrument. The 288.10 s control is this A/B's own and is **not** comparable to the 227.0 s baseline (different binary and histogram state). See [analysis/planner-refactor-take2/perf-20260902-pgstatistic-decode.md](../../../../analysis/planner-refactor-take2/perf-20260902-pgstatistic-decode.md). |
| P1 (mostly closed) | 2026-09-02 | `f07c20b1f` … `7ef387324` | 395.53 s → **399.33 s** (+0.96 %, noise) against the P0-12 aligned control; row counts identical | not re-measured | not yet captured | not re-run since `ae78cc6eb` | 14 items closed, 4 already-satisfied, 1 declined (P1-06), 2 blocked (P1-18 on P3-04, P1-21's precondition). Biggest single win is **P1-20**: constants never propagated across an equivalence class, costing **470×** on `a = b AND a = 42` — invisible on TPC-H because the corpus has no such query. |
| ~~P1 (partial)~~ | 2026-09-02 | `f07c20b1f` … `13430fc3a` | 288.10 s → **257.75 s** (−10.5 %) from `f07c20b1f`; P1-13/P1-14 timing-neutral | not re-measured | not yet captured | not measured | P1-11c is the whole timing move. P1-13 corrected a 2.04× cardinality error to 0.9 % with **no time change** — recorded as a negative result, not buried. See [perf-20260902-cumulative.md](../../../../analysis/planner-refactor-take2/perf-20260902-cumulative.md). |
| P2 (mostly closed) | 2026-09-02 | `6b503e47c` … `7c95b2c83` | **390.70 s → 245.71 s (−37.1 %)**, verified `24 MATCH / VERDICT: PASS` on VALUES | not re-measured | not yet captured | not re-run | **P2-03 is the session's largest win.** goopg budgeted a hash build at `work_mem` alone where PG uses `work_mem × hash_mem_multiplier`, so every hash table had half PostgreSQL's memory. Q14 −96.1 % (now *faster* than PG), Q9 −85.2 %, Q16 −80.8 %, Q10 −76.9 %, Q7 −69.9 %. P2-01/P2-04/P2-02 landed in the corrected order first. |
| ~~P2 (partial)~~ | 2026-09-02 | `6b503e47c` … `78ef045c8` | control moves 248.71 s → **403.27 s** — see note | not re-measured | not yet captured | 95 PASS / 0 MISMATCH (SF0.5 gate, first run this session) | P2-01/P2-04/P2-02 landed in the **corrected order** (P2-04 before P2-02, reversing the bundle). `SET random_page_cost` now changes a plan. The timing move is **P0-12**, not a regression: aligning `work_mem` 512MB→64MB with the PG reference removed an 8× memory advantage goopg had been measured with, so 403.27 s is the first *honest* control. It also exposed the bottleneck — see [FINDING-workmem-advantage.md](impl/FINDING-workmem-advantage.md). |
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
