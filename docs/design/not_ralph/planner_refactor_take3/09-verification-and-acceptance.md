# 09 — Verification and acceptance (take3 synthesis)

Companion to [08-target-design.md](08-target-design.md). What is measured,
with which instrument, and what number counts as done — updated for take2's
landed instruments, its three methodology defects found by being wrong
first, and the gates that must not be re-weakened.

Written before the remaining implementation, like take2's edition. Take2 §1
names the recurring failure this document exists to close: a change that
passed every gate it was given and broke something the gates could not see.

---

## 1. Gate failures that already happened (take2 §1, preserved + extended)

| # | incident | gate said | true | rule |
|---|---|---|---|---|
| 1 | `cost_index` `loop_count` arm | 21/21 byte-identical, units green | Q2 2.0 s → **87.3 s**; row-count gates blind to shape | **R1**: shape change ⇒ time every moved plan, both suites, fresh server per arm |
| 2 | `ab8fbc334` bitmap double-charge removal | "no plan changed" (TPC-H only) | Q72 73 s → **timeout**, Q47/Q69 with it; bisected over 425 commits | **R2**: "no plan changed" names a suite; both suites or scoped in writing |
| 3 | bitmap census | `BitmapHeapScan` 0, two runs | paths winning; renderer had no arm — census measured the labeller | **R3**: confirm the renderer arm before reading a count |
| 4 | Q8 cost investigation | five consistent hypotheses | bitmap survived only where producer emitted nothing | **R4**: verify both candidates generated (`DPPATH`/producer) before comparing costs |
| 5 | flag provenance (M0125-0005, M0127-P5.9) | header `RELSIZE_FALLBACK=off`, `PGSHAPED_DP=off` | both wrong; second mis-stamped its own flip's acceptance | **R5**: flag labels computed (`flaglabels.go` → `planner-flags.env`, `TestFlagProvenanceEnvIsGenerated`), never typed |
| 6 | P2-09 qual cost (new) | PASS=95, MISMATCH=0, per-query moves 0 | sweep TOTAL **+3.3%**, outside ±0.4% band; 60 shapes moved (take2 TODO P2-09; 07 §7.6) | **R6**: TOTAL arm + plan capture complementary; per-query gate insufficient |
| 7 | P2-11 Q76/Q12 "regressions" (new) | per-query arm named 2.5×/2.0× slower | **byte-identical plans**; sweep-context variance (take2 TODO P2-11; 07 §7.7) | **R7**: runtime move attributable only if the plan changed |
| 8 | merge wrong-answers (new) | 175 rows, correct count | wrong tuples, sum 4.02× (take2 `impl/FINDING-CRITICAL-mergejoin-wrong-answers.md`; 07 §7.4) | **R8**: projection/join changes gate on **values** (`-digest` + `-diff`), never counts |

R1–R5 binding on every item (take2 09 §1). R6–R8 added by take2 landings.

---

## 2. Instruments

### 2.1 Existing (take2 09 §2, take3 status)

| instrument | invocation | proves | cannot prove / status |
|---|---|---|---|
| units suite | `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | package correctness | plans/time; **never `-count=1`** (09 §4 C) |
| pgbench smoke | `.githooks/pre-commit` automatic | no TPC-B/concurrency regression | OLAP; **never `--no-verify`** |
| TPC-H spot-check | `scripts/tpch-spotcheck.sh` | canonical Q12/Q13 counts, fresh capped server | completing shape regressions pass it |
| TPC-H value diff | `cmd/tpch-runner -diff` (+ `-digest`) | value-level equality vs pre-change arm | shape; time. **Required** gate for projection/join work (R8: caught P4-01b, merge wrong-answers) |
| TPC-DS SF0.5 gate | `scripts/tpcds-sf05-regression.sh sweep` (~1 h, git-tracked oracle, no PG) | 99-query correctness | 12× 0-row oracles trivial; 42/99 count-only; TIMEOUTs reported not enforced |
| sweep diff TOTAL arm | `scripts/tpcds-sweep-diff.py` TOTAL | broad shallow moves (R6: caught +3.3%) | attribution — needs plan capture beside it (take2 TODO P2-12: 5 movers −1 s, 90 identical +23 s drift) |
| goopg plan stability | `make plan-gate` (`cmd/plan-snapshot`) | goopg-vs-goopg TPC-H | nothing about PG; pin `warm-stats-base.txt` (Aug 2026) — re-pin P0-08 |
| TPC-DS plans channel | `scripts/tpcds-sf05-regression.sh plans` | goopg-vs-goopg | nothing about PG |
| estimate audit | `cmd/estimate-audit --label L --reference <pg.plans.txt>` / `--ref-port 65432` | only committed goopg-vs-PG instrument: join-spine diff + per-joinrel ratchet (`--parity-slack 10.0`, `--parity-floor 100.0`) | node-level shape (scan/join/parallel/sort-agg strategy) |
| `DPPATH` provenance (P0-11) | `GOOPG_PGSHAPED_DP_TRACE=1` + `enumtrace.go` | OFFERED / ACCEPTED / dominated per producer per relset (take3 04 §6: NLI 694/23, margins 0.05%–12%) | why declined (needs producer-side reason) |
| enumeration trace | `estimate-audit --enum-trace <server.log>` | cost-vs-search-space attribution (OFFERED/DECLINED/NOT-ENUMERATED) | decline reason |
| hash batching | EXPLAIN `Batches:` on the hash build + `hashsize.Choose` NBatch | width fixes (P4-01 witness Q9: `Batches:` 8→1 at `work_mem` 64 MB S-cold; narrowed width ≈100, not 6) | per-node widths (reported in the PP separate column) |
| result oracle | `scripts/pg-oracle-diff.sh` | goopg text == PG text | plans |
| regress parity | `scripts/pg-regress-runner.sh` | SQL-surface % | OLAP plans/time |
| race gate | `make race-gate` | no race in lock/mvcc/storage/aio/wal/shared planner state | — |

`scripts/pg-plan-shape-diff.sh` (leftdeep-joins/09 §4) was never created;
the spine diff lives inside `estimate-audit` (take2 09 §2).

### 2.2 To build in Phase 0 (P0-05/06/07; spec take2 09 §3.1, design 08 §3)

**`plan-parity` capture/diff.** EXPLAIN from both engines (TPC-H 22 on
:65433/:65432 db `tpch`; TPC-DS 99 on :65437/:65438 db `tpcds05`),
normalised tree comparison (node type incl. `Parallel` prefix,
relation/index, join type/method, sort/agg strategy, child order;
costs/rows/widths/times excluded to a separate column). Per-query verdicts
**`MATCH` / `SHAPE-DIFF` / `MISSING-NODE` / `ERROR` / `TIMEOUT`** plus
roll-up `PLAN-PARITY: queries=N match=N shape-diff=N missing-node=N`.
Nine-category taxonomy (`join-order`, `join-method`, `scan-type`,
`parameterisation`, `aggregation-strategy`, `sort-strategy`, `parallelism`,
`qual-placement`, `rendering`) — phases are organised by these counts and
their decrements are the exit criteria (09 §5). Declared normalisation
(strip PG standalone `Hash`; print the applied list). PG captures committed
(`bench/tpch/plans-pg/`, `bench/tpcds/plans-pg/`); re-captured only on
query/dataset change. Report-only first with a pinned mismatch budget.

**Estimate-audit ratchet.** `--parity-slack` ratcheted down from the
measured value per phase (fixed 3.0 contradicted by Q18 8×-over-PG evidence;
take2 09 §7.1 A5). P1-16 re-baselines Q9's final joinrel bar.

### 2.3 How the instruments combine on a single item

No single instrument covers shape, values, and time. The required
combination per plan-moving item (take2 09 §5; R1/R2/R6/R7/R8):

| step | instrument | what it catches | precedent for skipping it |
|---|---|---|---|
| 1 | PP diff both suites | which plans moved + category | P2-09 qual cost: 60 shapes moved, per-query gate silent |
| 2 | plan-change attribution | whether a timing move is yours | P2-11 Q76/Q12 byte-identical "regressions" |
| 3 | TOTAL arm | broad shallow aggregate moves | P2-09 +3.3% invisible per-query |
| 4 | `-digest` + `-diff` values | wrong tuples at right counts | merge wrong-answers 175 rows 4.02×; P4-01b Q18 |
| 5 | T on every moved plan | per-query time moves above band | `cost_index` loop arm Q2 87.3 s at 21/21 identical |
| 6 | EA ratchet | estimate regressions behind unchanged shapes | Q9 chain 1,250 → 5.9e15 class |

`--serial` (`estimate-audit --serial` sets workers 0 so nodes under a
Gather report actual rows) is the serial-control companion for any P5
adjacent measurement (take2 09 §6.2). The SF0.5 gate's 12× 0-row oracles
(0 == 0 passes trivially) and 42/99 count-only coverage stay recorded
limitations — they are a floor (bar B1), never the values gate (R8).

---

## 3. Correctness floor (every phase, non-negotiable; take2 09 §4 carried)

1. Units suite clean. **Never `-count=1`** (cache defeat: ~5 min warm →
   ~40 min cold).
2. pgbench smoke via hook. **Never `git commit --no-verify`** for code.
3. `scripts/tpch-spotcheck.sh` on every planner/executor/cost change (fresh
   capped server; Q12/Q13 load-pinned — re-pin after any reload).
4. `scripts/tpcds-sf05-regression.sh sweep`: zero row-count + zero checksum
   deltas (`PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` floor, not
   target) **plus** the TOTAL arm read with plan attribution (R6/R7).
5. `cmd/tpch-runner -diff` → `VERDICT: PASS` (values) vs pre-change arm for
   any plan-moving change; `-digest` + `-diff` for projection/join work (R8).
6. `make race-gate` for lock/mvcc/storage/aio/wal/shared-planner-state
   touches.
7. Full regress-port after codec/catalog-format-adjacent changes (P1-11
   TOAST class).

Red/flaky shared suite fixed **in the same commit**; flaky counts as failing.

---

## 4. Acceptance bars

### 4.1 Bar A — plan parity (primary objective; take2 09 §7.1 carried)

Measured by §2.2 `PP` on both suites, S-cold and WARM.

| bar | metric | current | target |
|---|---|---|---|
| A1 | TPC-H PG-identical plan trees | **unmeasured** — set by P0-07 | `baseline + N` (N argued from category counts; below 100% — correct cost-difference divergences permitted, 09 §4.4) |
| A2 | TPC-DS SF0.5 PG-identical plan trees | **unmeasured** — set by P0-07 | same form as A1 |
| A3 | `MISSING-NODE` (Incremental Sort, bounded Sort, plain Parallel Index Scan; `Hash` excluded by normalisation) | ≥3 | **0** (Incremental Sort excluded until its executor counterpart lands, per P4-05) |
| A4 | join-spine parity | violations 0, mismatches 46 (STALE), matched 32 | violations 0; mismatches/matched numberless until the P0-04 re-measurement, then set as `baseline + N` exactly like A1/A2 (09 §4.1) |
| A5 | per-joinrel estimate ratchet | slack 10.0, floor 100.0, 0 violations | tighten to smallest holding value end of P1; ratchet down per phase |

A1/A2 deliberately numberless until P0-07 measures (inventing targets for
unmeasured metrics makes decoration). Enforceable until then: per-category
monotone decrements in §5.

### 4.2 Bar B — no-regression plus directional targets

| bar | metric | target |
|---|---|---|
| B1 | row counts | TPC-DS gate zero deltas (§3 item 4); TPC-H value PASS (§3 item 5) |
| B2 | timing ceilings | **no query slower than 1.2× its pre-phase time** (enforceable; respects ±17% band) |
| B3 | estimate ratchet monotone | A5 slack/floor never loosen |
| B4 | engine time targets (directional, NOT bundle acceptance) | TPC-H ≤ 3.0× PG; TPC-DS ≤ 1.5×; worst query ≤ 10× (take2 09 §7.2 B1–B4: unreachable by plan parity alone per Q6 23.4 s vs 0.99 s; 07 §6) |

Bundle acceptance = A1–A5 + B1–B3 + C (§4.3). B4 recorded as destination,
explicitly excluded from P7-01.

### 4.3 Bar C — hygiene (take2 09 §8 + R6–R8)

C1 no forcing (no benchmark/table/shape-identifying rule, penalty, or
preference). C2 no new penalty multiplier / shape preference / one-query
calibration constant (`GOOPG_INDEX_PROBE_MULT` stays 1.0 absent cross-suite
measurement). C3 both-suites timing for shape changes; C4 fresh server per
arm; C5 never `-count=1` in a gate; C6 never `--no-verify`; C7 every
deferral a ledger row (upstream citation + resume point).

### 4.4 Permitted divergences (take2 09 §7.4, carried)

A plan differing from PostgreSQL's is **not automatically a defect**. It is
acceptable, recorded in `.review-design.md` or the deferral ledger with evidence, when:

1. goopg's operator has a genuinely different cost (e.g.
   `sortPartialRootPays` declines PG's `Gather Merge → Sort → Parallel scan`
   because measurement showed leader-side sort faster: q16 0.9 s vs 1.6 s,
   q10 3.0 vs 3.4, q13 4.8 vs 5.1; take2 §8) **and** the measurement is
   committed — under 08 Phase 5 the choice becomes a cost comparison between
   two generated paths rather than a hard-coded preference; or
2. the divergence is a rendering artifact, not a planning difference (P0-04
   numbering class); or
3. PG's shape is unreachable for a ledgered reason with a resume point
   (e.g. pre-P3-04 outer-join coverage; 07 §3.1).

Case 1 requires the measurement. A divergence justified by argument rather
than by a committed arm is a defect.

---

## 5. Per-phase gates and exit criteria

`PP` = §2.2 instrument; `EA` = `estimate-audit`; `T` = timing per §6.

| phase | gates beyond §3 floor | exit (must all hold) |
|---|---|---|
| **P0** instruments | PP self-test; EXPLAIN-cost unit test; both suites captured | PP end-to-end both suites + committed baseline roll-up (A1/A2 set); **no planner behaviour changed** — PP `changed=0` vs pre-P0 goopg; collapse control reports TPC-H 0 + Q72/Q75 only |
| **P1** stats tail | PP both; `EA --reference`; T on moved plans; regress (catalog) | A5 ratchet monotone; PP budget does not grow; B2 holds; S-cold/WARM gap narrows (c2/w2 reference, take2 09 §6.5) |
| **P2** costs | PP both; T on moved plans; GUC-effect test per newly-live GUC; TOTAL arm + attribution | every cost GUC changes a plan (remaining four need index/parallel fixtures); PP budget does not grow; B2; P2-02b lands alone with both-suites timing |
| **P3** coverage | PP both; `EA --enum-trace` adjudication; T on moved plans; values-diff (R8) | every PG-only spine OFFERED at its level (DPPATH per-producer OFFERED/ACCEPTED, 04 §6) or named reason via `--enum-trace` (§2.1); Q72-class (mixed comma + `LEFT JOIN`; Q72 witness, full list in the phase verdict) one search problem; `join-order` diffs strictly decrease |
| **P4** upper paths | PP both; T; regress (shape reaches regress cases); `-digest` + `-diff` values | `aggregation-strategy` + `sort-strategy` diffs strictly decrease; P4-01 witness TPC-H Q9 (join tree inside a subquery, P4-A §12 EXPLAIN levels) at `work_mem` 64 MB S-cold (§6 header): EXPLAIN `Batches:` 8→1 on the hash build, DPPATH join.hash total below mergejoin 754717, narrowed width ≈100 not 6; `-digest` + `-diff` values gate (R8) + both-suites timing (C3); no correctness delta |
| **P5** parallel | PP both, parallel-on + serial control; T | `parallelism` diffs strictly decrease; serial arm unchanged |
| **P6** consolidation | PP both; T; full units + regress; values-diff | byte-identical plans vs pre-deletion arm, or every difference explained + timed; P6-05 oracle retained |
| **P7** acceptance | everything (§7) | §7 bars |

Sequencing rule (M0126): one variable per commit; two-input items split
before start.

---

## 6. Measurement methodology (mandatory; take2 09 §6 carried + refined)

**Arm construction.** Fresh server per arm through the cgroup cap
(`scripts/goopg-test-run.sh`, distinct `GOOPG_CG_UNIT`); hold server age
constant (sweep-tail collapse: Q6 423.94 s post-timeout vs 5.82 s clean);
never `pkill -f goopg` (self-match exit 144) — `goopg stop -D <dir>` or
lifecycle scripts; reap orphans (client `timeout` doesn't stop the server;
MATERIALIZED victim set before `pg_terminate_backend`); one benchmark at a
time (`FORCE=1` stamped invalid; nightly `ci/batch` contaminates — private
`-o` build path when live). Throwaway ports 5533/5534; 6543x block per
`CLAUDE.md`.

**Runtime settings.** TPC-H timing `GOGC=100 GOMEMLIMIT=12GiB` (Q21 OOM at
`GOMEMLIMIT=18GiB` needs the `GOGC=100` + 12 GiB form); TPC-DS harness
`GOGC=off` default (`bench/tpcds/env_tpcds.sh`); cgroup
`GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0`; per-query 300 s
(oracles 600 s, plan capture 180 s); `statement_timeout = 0` (client-side
only); suite-default parallelism + serial control arm
(`estimate-audit --serial`).

**Noise band ±17%** (byte-identical-plan pair; take2 09 §6.3): per-query
thresholds tighter than 1.2× unenforceable; sub-second PG times yield no
ratio (P0 oracle hygiene mitigates); suite claims on totals, per-query only
above band or on repeats. TPC-H drift band widened past ~1.7% late-session
(take2 TODO P3-10: baseline 213.84 s re-ran 221.01 s identical code) —
**re-run the BASELINE before believing a TPC-H delta of that size**.

**Memory parity.** State both `work_mem` and `effective_cache_size` per arm
(07 §7.1); `shared_buffers` divergent by design (Go-heap arena;
take3 04 §12.4).

**Statistics regime.** Every artifact states `stats=S-cold|WARM`
(parallel=on|off, GOGC/GOMEMLIMIT); A/B never mixes regimes (take2 09 §6.5).

**Artifact header** (take2 09 §6.6, carried): label, date, goopg commit
(dirty count), pg 18.3 @ port, suite, regime, timeout, generated
`planner-flags:` line (R5), host-load (Q47-at-load-10 void precedent).
Example skeleton (values illustrative, field set normative):

```
label:         p2-02b-bootval-4mb
date:          2026-09-03T00:00:00+09:00
goopg:         d5f8a6ff9 (dirty=0)
pg:            18.3 @ 65432
suite:         tpch-sf1
regime:        stats=S-cold parallel=on GOGC=100 GOMEMLIMIT=12GiB
timeout:       300s per query
planner-flags: <generated line from scripts/planner-flags.env>
host-load:     0.42 (1-min at start)
```

---

## 7. Gate-failure rules distilled (take2 §8 + take2 09 §6.4 + past failures)

1. **`shape_mismatches` renderer confound.** 46 attributed to missing dedup;
   dedup exists, numbering diverges (P0-04). Do not quote 46 as a defect
   count until re-measured post-alignment.
2. **8× memory confound.** Pre-P0-12 figures compare mismatched budgets;
   quote the honest 17.6× form or state both settings (07 §2.1).
3. **Sweep-tail collapse.** Post-timeout arms read up to 73× slow on clean
   queries; hold age constant, fresh server per arm (§6).
4. **goopg-vs-goopg re-pin on EXPLAIN/capture changes.** P0-02/03-class
   renderer changes invalidate `plan-gate` snapshots + TPC-DS `plans-*.txt`
   — re-pin in the same commit (take2 TODO P0-02 note; P0-08 three-edit rule).
5. **Sweep-delta attribution.** TOTAL moves without plan moves are drift;
   per-query moves without plan moves are variance (R6/R7; take2 TODO
   P2-11/P2-12). Always check which report the delta used (reverted-run
   flattery precedent: −4.3% vs honest −1.2%).
6. **Row-count blindness.** Counts pass shape regressions (R1) and wrong
   tuples (R8: Q9 175 rows 4.02× sum; P4-01b Q18). Values-diff required
   class-wide for projection/join changes.

---

## 8. Acceptance run (P7-01)

Both suites, S-cold and WARM, full PP roll-up, `EA --reference` ratchet,
complete timing table, §6 headers on every artifact. Bars: A1–A5 + B1–B3 +
C1–C7 (§4). B4 directional only. Verdict document under
`analysis/planner-refactor-take3/acceptance-<date>/README.md` with
before/after roll-ups and an explicit worse-statement; negative results kept
verbatim (take2 09 §9 form). Take2's interim acceptance (take2 TODO P7-02,
`analysis/planner-refactor-take2/acceptance-20260903/README.md`) is
explicitly not this run — Phases 3–5 unlanded — but its what-got-worse and
methodology sections carry forward.

---

## 9. Reporting (take2 09 §9, carried)

Each executed item, when closed, records in its checklist line:

```
- [x] P<n>-<k> <title> — <commit>; gates: <list>; artifacts: <paths>
```

Phase closure adds a short verdict file under
`analysis/planner-refactor-take3/<phase>-<date>/README.md` carrying the §6
header, the before/after PP roll-up, the timing table for every query whose
plan moved, and an explicit statement of anything that got worse. Negative
results are kept verbatim: the reason `cost-model/15`, `pg-plan-parity` §9
and §13.4, and take2's wrong pre-registrations (propagation-then-width,
spill-not-choice, 39×-as-cause) are still useful is that nobody rewrote them
after the fact. Take2's FINDING arc (claim → withdrawal → fair-comparison
restatement, take2 `impl/FINDING-planner-settings-not-propagated.md`) is the
form to imitate: the correction preserved alongside the claim is what keeps
the next reader from re-asserting the withdrawn cause.

(End of file)
