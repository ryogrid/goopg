# 09 — Verification and acceptance

Companion to [08-target-design.md](08-target-design.md). This document defines
**what is measured, with which instrument, and what number counts as done** for
every phase in [TODO.md](TODO.md).

It is deliberately written before the implementation. The single most expensive
recurring failure in this project's planner history is a change that passed
every gate it was given and broke something the gates could not see. Section 1
lists those incidents; everything after it exists to close a specific one.

---

## 1. Five gate failures that already happened

These are not hypotheticals. Each one cost days.

| # | Incident | What the gate said | What was true |
|---|---|---|---|
| 1 | `cost_index` `loop_count` arm (pg-plan-parity DESIGN §9) | 21/21 TPC-H result sets **byte-identical**; every unit test green | TPC-H Q2 went 2.0 s → **87.3 s**. A plan-shape change returns the same rows a different way, so a row-count gate is structurally blind to it. |
| 2 | `ab8fbc334` bitmap double-charge removal | commit message: *"HONEST RESULT: no plan changed"* — true of TPC-H, which is what was run | TPC-DS Q72 went 73 s → **>400 s TIMEOUT**, Q47/Q69 timed out with it. Bisected over 425 commits. "No plan changed" is scoped to the suite you ran it on. |
| 3 | Bitmap plan census (memory: `goopg_plan_census_greps_a_label`) | `BitmapHeapScan` count read **0** for two full runs | Bitmap paths were already winning. `describePlan` had no arm for the node, so the census measured the *labeller*, not the planner. |
| 4 | Q8 cost investigation (DESIGN §14) | five consecutive cost hypotheses, each internally consistent | The bitmap survived only where the index producer emitted **nothing**. An absent path is indistinguishable from an infinitely expensive one at every point downstream of `addPath`. |
| 5 | Flag provenance (M0125-0005, M0127-P5.9) | artifact header said `GOOPG_RELSIZE_FALLBACK=off`, then `GOOPG_PGSHAPED_DP=off` | Both were wrong, and the second mis-stamped **the acceptance run of its own default flip**. A hand-typed flag label is a claim, not a measurement. |

The five rules that follow from them are binding on every item in TODO.md:

- **R1 — A plan-shape change must be timed.** Row counts and checksums are
  necessary and never sufficient. Diff plans, then time every query whose plan
  moved, on both suites, fresh server per arm.
- **R2 — "No plan changed" names a suite.** TPC-H and TPC-DS SF0.5 are both
  run, or the claim is scoped in writing to the one that ran.
- **R3 — A census measures its labeller.** Before reading a count of node type
  X, confirm the renderer has an arm for X's Go type.
- **R4 — Verify both candidates were generated before comparing their costs.**
  Instrument `addPath` and the producer, not only the cost function.
- **R5 — Flag labels are computed, never typed.** Every artifact carries a
  `planner-flags:` line derived from the same resolver the binary uses
  (`internal/optimizer/flaglabels.go` → `scripts/planner-flags.env`, guarded by
  `TestFlagProvenanceEnvIsGenerated`).

---

## 2. Instruments that exist today

| instrument | invocation | what it proves | what it **cannot** prove |
|---|---|---|---|
| units suite | `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | package-level correctness; the bar CI enforces | nothing about plans or time |
| pgbench smoke | `.githooks/pre-commit` (automatic, every commit) | no concurrency/TPC-B regression | nothing OLAP |
| TPC-H spot-check | `scripts/tpch-spotcheck.sh` | canonical Q12/Q13 row counts on a fresh capped server | every *completing* plan-shape regression passes it |
| TPC-H value diff | `cmd/tpch-runner -diff` | value-level equality of two arms, not just row counts | plan shape; time |
| TPC-DS SF0.5 gate | `scripts/tpcds-sf05-regression.sh sweep` | 99-query correctness vs a git-tracked PG oracle, ~1 h, no PG instance needed | 12 queries have 0-row oracles (0 == 0 passes trivially); 42/99 are count-only; TIMEOUTs are reported, not enforced |
| goopg plan stability | `make plan-gate` (`cmd/plan-snapshot`) | goopg's TPC-H plans vs **goopg's own** earlier snapshot | nothing about PG; silently exits 0 without `PATH` + `PLAN_DB`/`PLAN_USER`; the nightly baseline is `m0077-final` (May 2026) so it reports 22/22 diverged every night and carries no signal |
| TPC-DS plan stability | `scripts/tpcds-sf05-regression.sh plans` | goopg vs goopg previous capture; non-blocking | nothing about PG |
| **estimate audit** | `cmd/estimate-audit --label L --reference <pg.plans.txt>` or `--ref-port 65432` | **the only committed goopg-vs-PG instrument**: per-joinrel row-estimate parity ratchet (`--parity-slack 10.0`, `--parity-floor 100.0`) and the **join-spine pairing diff** (which relations are joined in which order) | node-level plan shape: scan type, join method, parallelism, sort/agg strategy. It compares *spines and estimates*, not plans. |
| enumeration trace | `GOOPG_PGSHAPED_DP_TRACE=1` + `estimate-audit --enum-trace <server.log>` | whether a PG-only join pairing was OFFERED / DECLINED / NOT-ENUMERATED — i.e. cost-and-stats vs search-space attribution | why a declined candidate was declined |
| result oracle | `scripts/pg-oracle-diff.sh` | goopg result text == PG result text | plans |
| regress parity | `scripts/pg-regress-runner.sh` | SQL-surface parity % | OLAP plans or time |
| race gate | `make race-gate` | no data race in concurrency-critical packages | — |

**Two facts to carry into the design.** First, `scripts/pg-plan-shape-diff.sh`
is referenced by `docs/design/leftdeep-joins/09-verification-and-acceptance.md`
§4 but **was never created**; the spine diff landed inside `estimate-audit`
instead. Second, the last committed spine-parity numbers are from 2026-08-05:

| arm | `parity_violations` | `shape_mismatches` | joinrels matched |
|---|---|---|---|
| `GOOPG_PGSHAPED_DP=0` (legacy) | 0 | 67 | 21 |
| `GOOPG_PGSHAPED_DP=1` (today's default) | 0 | **46** | 32 |

`shape_mismatches` was recorded as an **upper bound** rather than a defect
count, on the grounds that goopg's EXPLAIN did not de-duplicate repeated
relation names, so Q8/Q17/Q18 lost their final-joinrel comparison to rendering
rather than to planning. That attribution is now known to be imprecise:
de-duplication **does** exist (`internal/executor/explain_names.go`), with a
divergence from `select_rtable_names` in how the suffixes are numbered.
Aligning the renderer and re-measuring (Phase 0) is a prerequisite for treating
this number as a parity metric at all.

---

## 3. Instruments this work must build (Phase 0)

Phase 0 exists because **the acceptance criterion of this project — "goopg
emits PostgreSQL's plan" — currently has no instrument.** Building it first is
not overhead; without it every later phase is unfalsifiable.

### 3.1 `plan-parity` — node-level goopg-vs-PG plan diff (P0-1)

A new mode of `cmd/plan-snapshot` (or a sibling command) that captures EXPLAIN
from **both** engines and diffs the plan **trees**, not the text.

- **Corpus**: TPC-H 22 queries (goopg :65433 / PG :65432, db `tpch`) and
  TPC-DS 99 queries (goopg :65437 SF0.5 / PG :65438 db `tpcds05`).
- **Comparison unit**: the plan tree, normalised — node type (including the
  `Parallel ` prefix), the relation or index each scan touches, join type and
  join method, the sort/aggregate strategy, and the child order. Costs, rows,
  widths, times and buffer counts are **excluded** from the shape comparison
  and reported in a separate column.
- **Output**: per query, one of `MATCH`, `SHAPE-DIFF` (with an aligned tree
  diff), `MISSING-NODE` (a PG node type goopg never emits anywhere in the
  query), `ERROR`, `TIMEOUT`. Plus a corpus roll-up:
  `PLAN-PARITY: queries=N match=N shape-diff=N missing-node=N`.
- **Divergence taxonomy** (each diff is classified, because the phases are
  organised by these classes): `join-order`, `join-method`, `scan-type`,
  `parameterisation`, `aggregation-strategy`, `sort-strategy`, `parallelism`,
  `qual-placement`, `rendering`.
- **Normalisation policy, declared up front.** PostgreSQL emits a standalone
  `Hash` node as the inner child of every Hash Join; goopg emits none — it
  renders `Hash Join` with the build side as a direct child. That is an
  executor-structure difference, not a planning decision, so the diff
  **strips PostgreSQL's `Hash` nodes** before comparing and records the policy
  in the report header. The same treatment applies to any node whose presence
  carries no planning choice. Every normalisation rule is written down, because
  an undeclared one turns a real divergence into a silent MATCH — and the
  report prints the list it applied, so a reviewer can see what was forgiven.
- **Committed artifacts**: the PG capture is committed
  (`bench/tpch/plans-pg/<date>.txt`, `bench/tpcds/plans-pg/<date>.txt`) so the
  diff runs without a live PG instance, exactly as the SF0.5 oracle does. It is
  re-captured only when the query files or the dataset change.
- **Mode**: report-only at first, with a **pinned mismatch budget** that must
  not grow. A hard match-all bar is declined while cost constants and stats
  fidelity still differ — the same decision `leftdeep-joins/09` §4 made, for
  the same reason.

**R3 applies to this instrument itself.** Before the first roll-up is believed,
assert that the renderer emits a distinct label for every node type in
`internal/optimizer/plan.go`, with a test that fails when a new node type is
added without an EXPLAIN arm.

### 3.2 EXPLAIN cost surfacing (P0-2)

`internal/executor/operators_explain.go` currently prints a literal
`(cost=0.00..0.00 rows=%d width=0)`, and the `rows=` it does print comes from
the legacy `optimizer.EstimateRows`, not from the `Path` the search chose. Until
the real `(startup, total)`, `rows` and `width` of the chosen path reach
EXPLAIN:

- no cost-model change can be observed except through its effect on the winner;
- `MODE=semantic-cost` in `make plan-diff` compares zeros;
- a reviewer cannot tell a cost bug from a candidate-generation bug (§1 #4).

Acceptance: for every TPC-H and TPC-DS query, EXPLAIN prints non-zero costs and
a non-zero width on every node, `COSTS OFF` suppresses them, and the numbers
match the `Path` the planner selected (asserted by a unit test that plans a
query and compares the rendered numbers to `finalPath()`).

### 3.3 Renderer parity fixes (P0-3)

Align the existing relation-name de-duplication with `select_rtable_names`'
suffix numbering, then re-measure the 46 mismatches. Fix the mode asymmetry
where a plain `EXPLAIN` takes its `rows=` from `attachedFilterNode` and
`EXPLAIN ANALYZE` takes it from the node itself, so the two can disagree on the
same scan — a parity capture must fix one mode and stay in it. Add
`GOOPG_INDEX_PROBE_MULT` to the generated flag provenance
(`internal/optimizer/flaglabels.go` → `scripts/planner-flags.env`); it is the
one plan-shaping knob no artifact can currently state (R5).

### 3.4 Baseline re-pinning and oracle hygiene (P0-4 … P0-6)

- Re-pin the nightly `make plan-gate` baseline (currently `m0077-final`, May
  2026, 22/22 diverge nightly).
- Re-capture the TPC-DS PG oracle with **sub-second** resolution. The committed
  one (`bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`, 2026-07-29)
  stores integer seconds, so 83 of 95 queries read `0s` and no ratio can be
  formed against them.
- Fix `ci/batch/tpcds-row-anchors.csv` consumption: `ci/batch/lib/summarize.py`
  reads `r["rows"]`, the CSV column is `expected_rows`, so `anchors_tpcds` is
  always empty and every TPC-DS anchor is inert.

---

## 4. The correctness floor (every phase, non-negotiable)

No item in TODO.md is complete unless all of these are green:

1. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — clean.
   **Never `-count=1`** (it defeats the test-result cache: ~5 min warm becomes
   ~40 min cold). A cached PASS is a real PASS.
2. The pgbench smoke fires automatically on every commit via
   `.githooks/pre-commit`. **Never `git commit --no-verify`** for code.
3. `scripts/tpch-spotcheck.sh` — canonical Q12/Q13 row counts, fresh capped
   server, on every planner/executor/cost change.
4. `scripts/tpcds-sf05-regression.sh sweep` — **zero** row-count deltas and
   **zero** checksum deltas against the git-tracked oracle. `PASS=95
   MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4` is the current state and
   is a floor, not a target.
5. `cmd/tpch-runner -diff` → `VERDICT: PASS` (value-level, not row-count-level)
   against the pre-change arm, for any change that can move a plan.
6. `make race-gate` for changes touching `internal/lock`, `internal/mvcc`,
   `internal/storage`, `internal/aio`, `internal/wal`, or any shared planner
   state.
7. Full regress-port suite after any codec- or catalog-format-adjacent change
   (statistics persistence work in Phase 1 is in this class).

A red or flaky shared suite is fixed **in the same commit**, even when the
failure is unrelated to the item being worked. A flaky test counts as failing.

---

## 5. Per-phase gates

Each TODO.md item names one row of this table. `PP` = the §3.1 plan-parity
instrument; `EA` = `estimate-audit`; `T` = timing arm per §6.

| phase | what changes | required gates beyond the §4 floor | pass condition |
|---|---|---|---|
| **P0** instruments | EXPLAIN, renderer, capture tooling, oracles | PP self-test; EXPLAIN cost unit test; both suites captured | PP runs end-to-end on both suites and produces a committed baseline roll-up. **No planner behaviour may change in P0** — PP `changed=0` against the pre-P0 goopg capture. |
| **P1** statistics fidelity | ANALYZE outputs, stats persistence, index stats, extended stats | PP both suites; `EA --reference`; T on every query whose plan moved; regress suite (catalog format) | estimate ratchet does not regress (`parity_violations` stays 0, per-joinrel ratios improve or hold); PP mismatch budget does not grow; no query >1.2× its pre-change time |
| **P2** cost inputs | session GUCs into the planner, `disabled_nodes`, missing cost functions | PP both suites; T on every moved plan; a GUC-effect test per newly-live GUC (`SET seq_page_cost=…` demonstrably changes a plan) | every cost GUC in §7 changes at least one plan; PP budget does not grow; no query >1.2× |
| **P3** search coverage | jointree flattening, special joins in the DP, collapse limits, pathkeys into the search | PP both suites; `EA --enum-trace` clause-6 adjudication; T on every moved plan | every PG-only join spine is OFFERED at the level it belongs to, or is recorded with a named reason; TPC-DS Q72-class queries produce **one** search problem, not six; PP `join-order` diffs strictly decrease |
| **P4** upper planner as paths | agg/sort/distinct/limit/window/setop as upper-rel paths | PP both suites; T; regress suite (plan shape reaches many regress cases) | PP `aggregation-strategy` and `sort-strategy` diffs strictly decrease; no correctness delta |
| **P5** parallelism in the path model | partial paths, `cost_gather`, parallel scan eligibility | PP both suites with parallelism **on** and a serial control arm; T | PP `parallelism` diffs strictly decrease; serial arm unchanged |
| **P6** consolidation | delete the legacy estimator, the legacy rewrite passes, the coordinate map | PP both suites; T; full units + regress | byte-identical plans to the pre-deletion arm on both suites, or every difference explained and timed |
| **P7** acceptance | — | everything | §7 bars |

**Sequencing rule (learned in M0126):** one variable per commit, enforced by
ordering. M0126 had to land MHJ retirement *before* the join-order flip because
`GOOPG_COST_DRIVEN_JOINORDER=1` also set `mhjPackingEnabled=false` as a side
effect — one commit would have moved two variables and made the measurement
uninterpretable. Any TODO item that would change two planner inputs at once is
split.

---

## 6. Measurement methodology

Getting this wrong has invalidated whole rounds of numbers. All of it is
mandatory.

### 6.1 Arm construction

- **Fresh server per arm**, started through the cgroup cap
  (`scripts/goopg-test-run.sh`, distinct `GOOPG_CG_UNIT` per concurrent run).
- **Hold server age constant.** A goopg server that has just run a timeout
  query sits at `GOMEMLIMIT` with `GOGC=off` and thrashes GC; the resulting
  "sweep-tail collapse" mimics a code regression. Measured instance: TPC-DS Q6
  read **423.94 s** immediately after a 1200 s Q5 and **5.82 s** on a clean
  server — a factor of 73.
- **Never `pkill -f goopg`** — it self-matches the invoking shell (exit 144).
  Stop via `goopg stop -D <dir>` or the lifecycle scripts.
- **Reap orphans.** `timeout N psql` kills only the client; the server keeps
  executing. Materialise the victim set before `pg_terminate_backend`
  (`WITH victims AS MATERIALIZED (…)`) — the naive form has killed a healthy
  backend.
- **One benchmark at a time.** The SF0.5 gate refuses to run while the SF=1
  harness is active; a `FORCE=1` report is stamped so its seconds are not read
  as valid. The nightly `ci/batch` lane contaminates any arm run beside it, and
  rebuilds `tmp/goopg-bench-bin` mid-run — build to a private `-o` path when
  the nightly is live.

### 6.2 Runtime settings

| setting | value | note |
|---|---|---|
| `GOGC` / `GOMEMLIMIT` | `100` / `12GiB` for TPC-H timing arms | the arm both 2026-08-31 tables used |
| `GOGC` / `GOMEMLIMIT` | `off` / `12GiB` for the TPC-DS harness | `bench/tpcds/env_tpcds.sh` default |
| cgroup | `GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0` | `GOMEMLIMIT` is soft; a 2026-08-28 Q5 run reached 28 GB RSS on a 31 GB host |
| per-query timeout | 300 s (both suites' timing arms) | SF0.5 sweep 300 s, oracle capture 600 s, plan capture 180 s |
| `statement_timeout` | 0 in every bench cluster | timeouts are client-side only |
| parallelism | suite default (`max_parallel_workers_per_gather = 4`); a **serial control arm** sets it to 0 | `estimate-audit --serial` sets 0 so nodes under a Gather report actual rows |

### 6.3 The noise band

**±17 %**, measured from a pair of arms proven to produce byte-identical plans
(`analysis/m0125-0031-warm-tpch-20260730.md` §3). Every headline number in this
project is a single run on a shared workstation. Consequences:

- A per-query threshold tighter than **1.2×** is not enforceable with today's
  harness. Bars in §7 respect this.
- A sub-second PG time yields no usable ratio. TPC-DS PG times are integer
  seconds today, so only ~12 of 95 queries have a meaningful ratio at all
  (P0-5 fixes this).
- Report **totals** for suite-level claims and **per-query** only above the
  band, or run repeats.

### 6.4 The two TPC-H clusters are not configured alike

Comparing `bench/tpch/runtime/pgdata/postgresql.conf` (PG, :65432) with
`bench/tpch/runtime_goopg/data/postgresql.conf` (goopg, :65433):

| setting | PG reference | goopg bench | ratio |
|---|---:|---:|---|
| `work_mem` | **64MB** (explicit) | **512MB** (boot default, line commented out) | goopg **8× more** |
| `shared_buffers` | 512MB | 2048MB | goopg 4× more |
| `effective_cache_size` | **2GB** (explicit) | 4GB (boot default) | goopg 2× more — though inert in goopg's planner today |
| `max_parallel_workers_per_gather` | 4 | 4 (boot default) | equal |
| `maintenance_work_mem` | 256MB | boot default | — |

**The headline 9.9× is therefore measured with goopg holding a memory
advantage**, which makes the real gap wider than reported rather than
narrower. More importantly for this work, it makes any `work_mem`-sensitive
cost comparison between the two engines meaningless: hash-join batch counts and
sort-spill decisions are computed from an input that differs by 8× between the
arms.

This has not been recorded before and it interacts directly with P2-02b. The
sequence is:

1. Align the goopg bench cluster's `work_mem` and `effective_cache_size` with
   the PG reference cluster's explicit values (64MB and 2GB), and record the
   change — plans will move, so it is its own commit with both suites timed.
2. Separately, correct the `work_mem` **BootVal** to PostgreSQL's 4MB
   (P2-02b), which is a fidelity fix for users who do not set it, not a
   benchmark change.

Doing (2) without (1) would swing goopg from 8× more memory than PG to 16×
less, and would be read as a catastrophic regression that is really a
configuration change.

### 6.5 The statistics regime, and why it is a stated variable

The TPC-H gate runs **S-cold** (fresh server, no same-session ANALYZE) while
the PG arm is permanently ANALYZEd. That is a systematic bias against goopg on
the ratio column, and it is an open question inherited from
`pg-plan-parity/TODO.md`.

Measured cost of the choice (`analysis/m0125-0031-warm-tpch-20260730.md`,
2026-07-30, 21-query stream):

| arm | stats | `GOOPG_RELSIZE_FALLBACK` | total | vs c1 |
|---|---|---|---:|---:|
| c1 | S-cold | 0 (off) | 693.8 s | 1.00× |
| c2 | S-cold | 2 (default) | 494.0 s | 1.40× faster |
| w1 | WARM | 0 | 413.3 s | 1.68× faster |
| w2 | WARM | 2 | 420.1 s | 1.65× faster |

Warm vs S-cold at the shipped default: **494.0 → 420.1 s, −15 %**. Warm moved
plans and never moved an answer. Flag on/off produce 22/22 byte-identical plans
once ANALYZEd, so `GOOPG_RELSIZE_FALLBACK` is an S-cold-only mechanism.

**Rule for this work:** every timing artifact states its regime
(`stats=S-cold|WARM`) in its header, and an A/B never mixes regimes. Phase 1
is expected to shrink the S-cold/WARM gap; that shrinkage is itself a P1
acceptance signal, measured with the c2/w2 arms above as the reference points.

### 6.6 Artifact header

Every timing or parity artifact produced by this work begins with:

```
label:        <arm name>
date:         <ISO-8601 with timezone>
goopg:        <commit> (dirty=<n>)
pg:           18.3 @ <port>
suite:        tpch-sf1 | tpcds-sf05 | tpcds-sf1
regime:       stats=S-cold|WARM  parallel=on|off  GOGC=<v> GOMEMLIMIT=<v>
timeout:      <per-query>
planner-flags: <generated line from scripts/planner-flags.env>
host-load:    <1-min load average at start>
```

The `planner-flags:` line is **generated**, per R5. The `host-load:` line
exists because the 2026-07-30 TPC-DS Q47 numbers were taken at load ~10 and are
void.

---

## 7. Acceptance bars

### 7.1 Plan parity (the primary objective)

Measured by §3.1 `PP` on both suites, S-cold and WARM.

| bar | metric | current | target |
|---|---|---|---|
| A1 | TPC-H queries with a PG-identical plan tree | **unmeasured** — set by P0-07 | see below |
| A2 | TPC-DS SF0.5 queries with a PG-identical plan tree | **unmeasured** — set by P0-07 | see below |
| A3 | `MISSING-NODE` — a PG node type goopg cannot emit anywhere, after §3.1 normalisation | Incremental Sort, bounded/top-N Sort, parallel index scan (plain). **`Hash` is not on this list**: goopg emits none, and §3.1 strips PostgreSQL's before comparing | **0** |
| A4 | join-spine parity (`estimate-audit`) | `parity_violations=0`, `shape_mismatches=46`, matched 32 | violations 0; mismatches strictly below the P0 re-measurement; matched ≥ 40 |
| A5 | per-joinrel estimate ratchet | `--parity-slack 10.0`, `--parity-floor 100.0`, 0 violations | tighten the slack **to the smallest value that holds at the end of P1**, and ratchet it down per phase thereafter |

**A1 and A2 deliberately carry no number yet.** The metric has never been
measured — that is the whole reason Phase 0 exists — and inventing a target for
an unmeasured metric is how a bar becomes decoration. P0-07 commits the baseline
roll-up and *then* sets A1/A2, as `baseline + N`, with N argued from the
divergence-category counts. Until then the enforceable bars are the **monotone
per-category ones in §5**: each phase must strictly decrease the diff count in
its own category and must not increase any other. Those are better bars than a
corpus-wide percentage anyway, because they attribute.

They will be set below 100 %: some divergences are correct responses to real
goopg cost differences (§7.4), and a bar that forbids them would force
query-specific fiat, which is prohibited (§8).

A5's original draft named slack 3.0. That is contradicted by evidence already in
this bundle — TPC-H q18's joinrel is 42,837× over where PostgreSQL is 5,387×
over, a ratio near 8 — so a fixed 3.0 would fail on a query whose estimate is
not goopg's fault. Ratcheting from the measured value is the honest form.

### 7.2 Time

Measured per §6, both suites, fresh server per arm.

| bar | metric | current (2026-08-31) | target |
|---|---|---|---|
| B1 | TPC-H SF=1 total vs PG | 227.0 s vs 22.9 s = **9.9×** | ≤ 3.0× |
| B2 | TPC-DS SF=0.5 total vs PG | 1173 s vs 536 s = **2.2×** | ≤ 1.5× |
| B3 | worst single TPC-H query ratio | q05 92.5×, q09 81.7×, q07 45.4× | ≤ 10× |
| B4 | worst single TPC-DS query ratio | Q72 82×, Q23 78×, Q88 52× | ≤ 10× |
| B5 | no regression | — | no query slower than 1.2× its pre-phase time |

**B1–B4 are directional targets for the engine, NOT acceptance criteria for
this bundle.** They are recorded so the work has a destination, and they are
explicitly excluded from P7-01's acceptance run. The bundle's acceptance is
A1–A5 plus B5 (no regression) plus C1–C4. Anyone reading B1/B2 as a commitment
of this design is reading it wrong, and the next paragraph explains why.

**B1–B4 are not reachable by plan parity alone, and this document says so.**
The executor carries a per-row tax that survives an identical plan: TPC-H Q6
runs the *node-for-node PG-identical plan* and still takes 23.40 s serial
against PG's 0.99 s. Its causes — the 48-byte `Datum` against PG's 8, probe-seam
re-materialisation at every join level, no bounded sort, no hash skew buckets —
are catalogued in `07-gap-analysis.md` §6 as **out of scope for this bundle**,
with pointers. The honest statement of what this work delivers is A1–A5 plus
whatever B-movement follows; B1/B2 need the executor work as well, and are
recorded here so that the bundle is not read as promising them on its own.

### 7.3 Correctness

| bar | metric | target |
|---|---|---|
| C1 | TPC-DS SF0.5 gate | `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |
| C2 | TPC-H value diff vs pre-change arm | `VERDICT: PASS` |
| C3 | units + regress-port | no new failure, no new flake |
| C4 | spot-check anchors | Q12 and Q13 at their pinned counts |

### 7.4 Permitted divergences

A plan that differs from PostgreSQL's is **not automatically a defect**. It is
acceptable, and must be recorded in `REVIEW.md` or the deferral ledger with its
evidence, when:

1. goopg's operator has a genuinely different cost (e.g. `sortPartialRootPays`
   declines PG's `Gather Merge → Sort → Parallel scan` shape because measurement
   showed the leader-side sort is faster for goopg: q16 0.9 s vs 1.6 s, q10 3.0
   vs 3.4, q13 4.8 vs 5.1), **and** the measurement is committed; or
2. the divergence is a rendering artifact, not a planning difference; or
3. PG's shape is unreachable for a reason recorded in the ledger with a resume
   point.

Case 1 requires the measurement. A divergence justified by argument rather than
by a committed arm is a defect.

---

## 8. Prohibitions

These are inherited from prior rounds and apply to every item in TODO.md.

1. **No query-specific forcing.** No rule, penalty, threshold or shape
   preference that identifies a benchmark query, a table name, or a
   recognisable query shape. (`pg-plan-parity/DESIGN.md` §5.)
2. **No new penalty multiplier on cost totals, and no shape preference.**
   Doc `cost-model/15` established both after a DP-integrated MHJ candidate
   could not be made to win without them; M0126 then confirmed that threshold
   penalties make the search dodge the penalised operator by routing work
   through extra passes — Q5 went 8.15 s → 600 s+ that way.
3. **No calibration constant introduced to fix one query.**
   `GOOPG_INDEX_PROBE_MULT` (default 1.0) exists and stays at 1.0 unless a
   committed measurement across both suites justifies otherwise.
4. **No `-count=1` in a gate.** One-off probes only.
5. **No `git commit --no-verify`** for code changes.
6. **Every deferral gets a ledger row.** `.ralph/deferral_ledger.md`, the
   existing 7-column format, with an upstream `postgres/` citation and a
   concrete resume point. A skip with no ledger row is not done.

---

## 9. Reporting

Each TODO.md item, when closed, records in its checkbox line:

```
- [x] P<n>-<k> <title> — <commit>; gates: <list>; artifacts: <paths>
```

Phase closure adds a short verdict file under
`analysis/planner-refactor-take2/<phase>-<date>/README.md` carrying the §6.6
header, the before/after `PP` roll-up, the timing table for every query whose
plan moved, and an explicit statement of anything that got worse. Negative
results are kept verbatim: the reason `cost-model/15`, `pg-plan-parity` §9 and
§13.4, and `analysis/0125-0005`'s wrong pre-registration are still useful is
that nobody rewrote them after the fact.
