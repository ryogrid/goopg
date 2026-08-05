# TPC-DS SF=1 at goopg HEAD — measured baseline and confirm/refute of §13.3

**M0124-0001 deliverable (D7).** This is the merged report of the SF=1
dual-engine re-sweep whose per-chunk results accumulated in
`analysis/tpcds-sf1-resweep-20260728/RESULTS.md`. Its purpose is to *test*
`docs/design/tpcds-round2-fixes/README.md` §13.3 — which was explicitly a
**projection, not a measurement** — and to replace that projection with a
number.

The sweep was **not** re-run to write this document; every figure below is read
back from `RESULTS.md` and the on-disk `chunk-*.txt` / result files.

---

## 1. Headline

- **99/99 queries measured**, one budget, one engine-id, 13 chunks — the first
  complete SF=1 dual-engine sweep at HEAD since RC-1b (`5db0a067`) landed.
- **All 13 §13.3 projections are CONFIRMED at the level §13.3 stated them
  (status + row count). None is refuted outright.** Two — **Q50 and Q46** — are
  confirmed on rows and **refuted on values**: they return PG's row count and
  the wrong answer.
- **The projected defect count of 21 is wrong by a factor of ~2. The measured
  count is 40.** The gap is almost entirely one class §13.3 could not have
  seen, because the protocol it was written under classified a cell by status
  and row count only: **18 queries complete, match PG's row count exactly, and
  return a wrong answer.**
- The engine-commit freeze imposed by M0124-0001 is discharged by this
  document.

---

## 2. Provenance

| field | value |
|---|---|
| engine-id (D4a) | `bba744a817f7ebdec31fd47edfed40362641dd0c c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc` |
| goopg commit at chunk 1 | `6d6bd1ea` (docs/harness only — engine trees as above, unchanged for all 13 chunks) |
| budget | `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1` |
| goopg cluster | port 65436, `-U postgres -d postgres`, `bench/tpcds/runtime_goopg/data` |
| PG 18.3 cluster | port 65438, `-U ryo -d tpcds`, `bench/tpcds/runtime/pgdata` |
| GC regime | `GOGC=off`, `GOMEMLIMIT=12GiB` (`bench/tpcds/env_tpcds.sh`) |
| state (D5) | **S-cold** — `s-cold-proof.txt`: 8 relations at `reltuples=0 relpages=0`, `pg_stats` empty, `store_sales`=2 880 404 rows |
| harness | `scripts/tpcds-bench-compare.sh`, foreground, one chunk per call |
| raw results | `analysis/tpcds-sf1-resweep-20260728/` (`RESULTS.md`, `chunk-*.txt`, `s-cold-proof.txt`, `diag-q47-rerun.txt`, `probe-q72-reprobe.txt`) |

**Comparability.** The binary sha moved several times mid-sweep
(`e6774c4f` → `8f0aac15` → …) because `go build` stamps `vcs.revision` /
`vcs.modified`, and docs-only commits landed between chunks. The comparability
key is therefore `engine-id` (committed engine trees + digest of uncommitted
engine edits), which every chunk header reprints unchanged. The
`*** SWEEP VOID ***` line in `chunk-1-4.txt` is the first-cut guard's false
positive and does not void that chunk (D4a).

"set A" throughout = `analysis/tpcds-sf1-goopg-20260727.md` §5.2 — same scale,
**same 600 s budget**, so comparable under D2.

---

## 3. Confirm / refute — the 13 projections under test

D7 fixed ten rows covering thirteen queries as the projections this sweep
exists to test. Verdicts:

| # | query | §13.3 expected at HEAD | measured at HEAD | verdict |
|---|---|---|---|---|
| 1 | **Q50** | PASS, 6 rows | `OK 19 s / 6` = PG | **CONFIRMED on rows / REFUTED on values** |
| 2 | **Q39** | PASS, 236 rows = PG | `OK 181 s / 236 [230+6]` = PG | **CONFIRMED** |
| 3 | **Q75** | ERROR, division by zero | `ERROR 66 s`, `division by zero` (query75.sql:67) | **CONFIRMED** |
| 4 | **Q72** | TIMEOUT | `TIMEOUT 635 s / 0` | **CONFIRMED** |
| 5 | **Q8** | ERROR `XX000`, server survives | `ERROR 26 s`, server survives | **CONFIRMED** |
| 6 | **Q47** | MISMATCH 0/100 | `OK 142 s / 0` vs PG 100 | **CONFIRMED** (+ unprojected 8.4× slowdown) |
| 7 | **Q49** | MISMATCH 30/34 | `OK 79 s / 30` vs PG 34 | **CONFIRMED** |
| 8 | **Q51** | MISMATCH 0/100 | `OK 587 s / 0` vs PG 100 | **CONFIRMED** (did not flip) |
| 9 | **Q35** | TIMEOUT (651 s set A; 525 s on 07-26) | `TIMEOUT 628 s / 0` | **CONFIRMED**, reclassified **budget-marginal** |
| 10 | **Q82** | OK ~576 s / 2; watch for flapping | `OK 556 s / 2` = PG, values = PG | **CONFIRMED**, did not flap |
| 11 | **Q88** | TIMEOUT 660 s / 0 (**not** SF0.5's 228 s) | `TIMEOUT 638 s / 0` | **CONFIRMED** |
| 12 | **Q34** | OK, 374 rows = PG | `OK 34 s / 374` = PG | **CONFIRMED** |
| 13 | **Q46** | OK, 100 rows = PG | `OK 43 s / 100` = PG | **CONFIRMED on rows / REFUTED on values** |

**11 confirmed as stated, 2 confirmed-on-rows-and-refuted-on-values, 0 refuted
outright.**

### 3.1 The two refutations

**Q50 (projection 1).** RC-1b's row fix holds at SF=1 — the count went `0` → `6`
and now equals PG, at essentially unchanged runtime (15 s → 19 s), so the
SF0.5-derived expectation was right. But under D6a's value comparison Q50's
*values* diverge from PG and attribute to **M0125-0009** (sibling `sum(CASE …)`
collapse). RC-1b therefore did not "fix" Q50; it converted a wrong-row-count
defect into a wrong-answer defect. Net defect count for Q50: unchanged.

**Q46 (projection 13).** §13.3 recorded Q46 (with Q34) as "never an engine
failure — first clean measurements give 374 and 100 rows, both = PG". The row
count is confirmed. The values are not: Q46 is one of the four cells of
**M0125-0010**, the FROM-subquery sibling-aggregate collapse discovered by this
sweep. Q34 has no such problem, so the pair splits.

Both refutations have the same cause: §13.3 was written under a protocol
(D6-as-originally-stated) that classified a cell by status and row count. D6a
exists because of them.

### 3.2 Notes on the confirmations that carry new information

- **Q72 — the projection with a measured contradiction, and the projection
  won.** §13.3 derived "TIMEOUT" from SF0.5, while set A had measured Q72 at
  SF=1 as `OK 14 s / 0 rows`. D7 flagged this explicitly as a hypothesis with a
  measured contradiction at this scale. The hypothesis is now measured true at
  SF=1: `TIMEOUT 635 s`, re-probed fresh at 636 s. That is a ≥45× runtime move
  on a query that used to answer (wrongly) in 14 s — wrong-and-fast became
  slow, exactly the RC-1b outcome §13.3 predicted. **This is a genuine
  regression**, and the only SF0.5→SF=1 extrapolation in the set that had
  contrary SF=1 evidence.

- **Q75 — the second set-A `OK` → HEAD failure.** `ERROR: division by zero`,
  deterministic, server survives (Q76 ran next on the same server). Confirms
  §13.4 item 1: RC-1b's correct data made a *pre-existing* evaluation-order
  defect reachable, and the pre-fix `OK / 100 rows` was a false pass masked by
  `LIMIT 100`. Ledger `tpcds-round2 Q75-eval-order`; fix filed as M0125-0004.

- **Q47 — confirmed on rows, but with an unprojected 8.4× runtime deviation**
  (set A `OK 17 s` → `OK 142 s`). It reproduces (`diag-q47-rerun.txt`: 143 s
  standalone), it is query-specific (Q44/Q46/Q48 in the same chunk, same
  server, same age, all within ±2 s of set A), so it is neither noise nor
  sweep-tail GC collapse. The row count did not move — this is a slowdown *on
  top of* an unfixed wrong answer, not the cost of a newly-correct plan. Not
  root-caused here by design (this task is the measurement baseline); the
  leading suspect is `5db0a067` (RC-1b), which post-dates set A and touches
  Q47's own family. Chunk 49–56 then showed Q49/Q50/Q51 **not** inflated, so
  the cause is specific to Q47 rather than to the RC-1b commit as a whole.
  **↳ SETTLED 2026-08-03 (M0125-0013).** This bullet's *direction* was right and
  chunk 49–56's rebuttal of it was wrong, but the reason given here ("the row
  count did not move") no longer holds: Q47 now returns **100 rows = PG,
  byte-identical at SF=1**, and it got *slower*. Re-measured on a quiet host at
  HEAD `374dc60e`: **goopg 537.55 s vs PG 3.38 s = 159×**. It is now
  **attributed** — ~485 s of it is the `v2` three-way self-join, whose hash key
  degenerates to `i_category` (10 distinct over 63,745 rows vs the 4-key
  composite's 5,667) because `splitEqualityForHash` takes only the first
  disjoint equality; the CTE itself costs 52 s and is evaluated once. That is
  the pre-existing multi-column-hash-key deferral (`M0125-0011` / `M0125-0035`),
  not an RC-1b regression — RC-1b only made the defective join reachable.
  Evidence `analysis/m0125-0013-q47-verdict/`; record in
  `docs/design/0125-0013-q47-q49-q51-three-distinct-defects.md` § Q47.

- **Q88 — the SF0.5 import trap, avoided.** D7 warned that Q88's `228 s / 1 row`
  is an SF0.5 figure. At SF=1 Q88 does not complete inside 600 s (`TIMEOUT
  638 s`), reproducing set A's 660 s. Any report quoting 228 s as an SF=1
  runtime is wrong.

- **Q35 — confirmed but reclassified.** The verdict is `TIMEOUT`, as projected,
  but Q35 **completed at `OK 525 s` in the 2026-07-26 baseline**, so its true
  runtime straddles the 600 s cut. It is budget-marginal, not unbounded-above
  (§5). Consequence: **M0125 must not score a Q35 `OK` as a fix** — it would be
  a re-rolled coin. Q35 does carry a usable PG row count (100), so a genuine
  fix is still validatable on rows. M0124-0004's task — recovering Q35's row
  count — remains open at SF=1.

- **Q51 — confirmed, and the flip that did not happen.** Set A had it at
  `OK 597 s`, i.e. 3 s of headroom; the Q47 inflation made a flip to TIMEOUT
  plausible. It did not occur: `OK 587 s`, 13 s of headroom, 10 s *faster* than
  set A. Its row gap (0 vs 100) is unchanged, and §13.4 item 2's conclusion —
  Q51 is a third defect distinct from Q47's — is untouched by this sweep.

- **Q82 — confirmed, no flap.** `OK 556 s / 2 rows`, values = PG. The narrowest
  OK margin of the sweep (44 s of headroom), but the margin *widened* versus set
  A's 576 s. It stays on the watch list.

---

## 4. The measured defect table — replacing §13.3's projected 21

Classification follows D6 (status by psql line prefix, row counts summing every
`(N rows)` block) plus **D6a** (every `OK`/`OK` cell with equal row counts is
additionally value-compared with `scripts/tpcds-value-diff.py` under graded
normalisation; only a divergence surviving pass 3 both positionally and as a
sorted multiset is a wrong answer).

| class | queries | n | §13.3 projected |
|---|---|---|---|
| **goopg-only ERROR** | Q8, Q75 | **2** | 2 ✓ |
| **goopg-only TIMEOUT — unbounded above** | Q5 Q10 Q14 Q30 Q31 Q54 Q64 Q65 Q67 Q69 Q71 **Q72** Q78 Q81 Q88 | **15** | — |
| **goopg-only TIMEOUT — budget-marginal** | **Q18**, Q35 | **2** | — |
| *(timeout class, total)* | | *(17)* | *16* |
| **completes, wrong ROW COUNT** | Q47, Q49, Q51 | **3** | 3 ✓ |
| **completes, row count = PG, WRONG ANSWER** | Q2 Q16 Q21 Q28 Q40 Q43 **Q46** **Q50** Q59 Q62 Q66 Q68 Q79 Q87 Q94 Q95 Q97 Q99 | **18** | **0 — class did not exist** |
| **total goopg-only defects** | | **40** | **21** |

### 4.1 Where the 19-defect gap comes from

- **+18** — the wrong-answer class. §13.3 could not have projected it: the
  oracle it and the SF0.5 gate both use is row-count-only (§13.4 item 3), and
  restricted to cells that were `OK` on both engines *and* equal in row count,
  **23 of 99 disagreed by value**. Five of those 23 are answer-neutral (§4.3),
  leaving 18 defects.
- **+1** — **Q18** entered the timeout class. Set A `OK 626 s / 100`, this sweep
  `TIMEOUT 627 s / 0`: one second apart, the same work landing on opposite sides
  of the cut. It is a defect only in the bookkeeping sense (§5).
- **0 net** — Q50 left the wrong-row-count class and entered the wrong-answer
  class on the same sweep.

### 4.2 Root causes of the 18 wrong answers (M0124-0006)

Full attribution in `RESULTS.md` §M0124-0006. Four causes, one of them new:

| root cause | queries | n |
|---|---|---|
| **M0125-0009** — sibling `sum(CASE …)` collapse (`parserExprKey` `%T` fallback) | Q2 Q21 Q40 Q43 Q50 Q59 Q62 Q66 Q97 Q99 | 10 |
| **M0125-0010** *(NEW — found by this sweep)* — `remapSubqueryColumnRefs` binds a FROM-subquery's `Project` targets by **column name**, and an `Aggregate` names its outputs after the function, so sibling `sum`s all bind to the first | Q28 Q46 Q68 Q79 | 4 |
| **M0125-0007** — date input rejects unpadded month/day → silently empty result | Q16 Q94 Q95 | 3 |
| **M0125-0006** — set-op chain re-association (`EXCEPT`) | Q87 | 1 |

M0125-0009 and M0125-0010 are **independent** — neither subsumes the other.
M0125-0009's reproducer is flat (`select sum(case…), sum(case…) from date_dim`
→ `10435|10435`, no subquery, so the remap pass never runs); M0125-0010's uses
no `CASE` at all (`select * from (select sum(d_dom), sum(d_year) from date_dim) d`
→ `1149021|1149021` vs PG `1149021|146061700`). Both must be fixed.

The most legible instance anywhere in the sweep is **Q97**:
`store_only|catalog_only|store_and_catalog` = `392155|392155|392155` against PG's
`541140|286927|161`. Those three sets are disjoint by construction — a customer
cannot be store-only, catalog-only and both — so equal cardinalities are not
merely wrong, they are impossible.

### 4.3 Cells that look wrong and are not

Excluded from the defect count, by D6a pass:

| cell(s) | why it is not a defect |
|---|---|
| Q7, Q26, Q27, Q83 | numeric division drops result scale **only when the quotient is exactly zero** (`0.00` vs `0.00000000000000000000`); non-zero quotients byte-identical. Ledger row already filed |
| Q39 | float8 aggregate accumulation order — `cov` differs as `1.4066976767982042` vs `…44`, relative 1.4e-16. A string diff calls this a wrong answer; a 1e-14 relative tolerance does not. This is why D6a has a pass 3 |
| Q98 | values **correct**; its 5068-line raw diff is entirely rendering — `char(n)` not blank-padded (ledger 2026-07-06, M0122-0005: `octet_length(sm_type)` = 30 on PG, 7 on goopg) plus the zero-quotient scale gap. Q98 is **not** a member of the 23 |
| Q4 | TIMEOUT on **both** engines (622 s / 616 s) — not goopg-only (D6) |
| Q11, Q74 | **PG-only** timeouts; goopg completes what PG cannot at this budget (95 rows / 100 rows) |
| Q36, Q70, Q86 | dsqgen artefact — the generated query text is rejected by PostgreSQL too; `PG_SKIP` |
| Q6 | set A's PG `ERROR` was the harness orphan-reap bug. Both engines `OK` / 44 rows here, and goopg is **2.5× faster than PG** on it (57 s vs 140 s) |

Two traps worth restating, both hit in practice: `length()` is not a padding
check (PG's `bpcharlen` ignores trailing blanks — `octet_length` is the
discriminator), and raw-diff size is uncorrelated with defect size (Q98: 5068
lines, correct; Q97: one line, impossible).

---

## 5. The budget-marginal sub-class

A `TIMEOUT` verdict carries information only when the query's true runtime is
*unbounded above* by the cut. This sweep proved the other case exists, so the
timeout class must be reported in two parts:

- **Unbounded (15)** — no run of this sweep or set A has ever seen the query
  finish at this budget. Q30/Q31 are the cleanest members: PG answers both
  cheaply and exactly (13 s / 63 rows, 12 s / 43 rows), goopg has never
  completed either, across four observations (649/647 s set A, 627/629 s here).
  **A verdict change here is signal.**
- **Budget-marginal (2)** — some run has completed the query within ~5 % of the
  budget: **Q18** (`OK 626 s` in set A vs `TIMEOUT 627 s` here) and **Q35**
  (`OK 525 s` on 2026-07-26). **A verdict change here is a re-rolled coin and
  must not be reported as a fix or a regression.** To make such a cell
  informative, classify it by measured runtime or re-measure at a larger
  budget — never by re-running at the same budget until it reads the desired
  way.

`Q82` (`OK 556 s`, 44 s of headroom) and `Q51` (`OK 587 s`, 13 s) are the
mirror-image risk on the `OK` side and are on the same watch list.

Note that a cell's elapsed figure covers the query **plus** the ≤30 s EXPLAIN
capture, which sits outside the timeout-guarded query — which is why an `OK`
cell can report an elapsed above 600 s, and why elapsed is not directly
comparable to `TIMEOUT_SEC`.

---

## 6. Stated limits of this measurement

1. **40 is a lower bound.** D6a's value comparison is only *possible* on cells
   that are `OK` on both engines with equal row counts. The 17 timeouts, the 2
   errors and the 3 row-mismatch cells have never been value-compared at any
   scale — if any of them is later made to complete, its answer is unverified.
2. **No cause is established here.** M0124-0001 is a measurement task. Q47's
   8.4× slowdown is bounded but unattributed; the four wrong-answer root causes
   were established by targeted probing in M0124-0006, not by this sweep's
   verdicts.
   **↳ DISCHARGED 2026-08-03 (M0125-0013).** Both words are now superseded.
   *Unattributed* → attributed: the single-key hash degeneracy in Q47's `v2`
   self-join (see §3.2's 2026-08-03 note). *Bounded* → not bounded by the 142 s
   this report quotes: a quiet-host re-measure at HEAD `374dc60e` reads
   **537.55 s** against PG's **3.38 s**. This limit is retired, not merely
   annotated.
3. **No SF0.5 comparison** appears in this report, per D2 and the protocol's
   non-goals: SF0.5-derived numbers may be *tested* at SF=1 (§3) but never
   quoted as SF=1 results.
4. **EXPLAIN cost/width figures are not signal** — they are hardcoded literals
   in `internal/executor/operators_explain.go`. Only plan **shape** and
   `EXPLAIN ANALYZE` **actual** rows were used.

---

## 7. Consequences

**The engine-commit freeze lifts.** M0124-0001's freeze existed so that no
engine change could land mid-sweep and split the baseline across two engine-ids.
The sweep is complete (99/99, 13/13 chunks, one engine-id) and this document is
its deliverable.

**M0125's baseline is this table**, not §13.3's projection. In particular:

- The largest single class is now **wrong answers behind a matching row count
  (18)**, not timeouts (17). M0125's plan was written when that class was
  believed empty.
- **M0125-0009 is the recommended first fix**: one-line root cause, 10 queries
  of evidence, and the most legible failure in the sweep (Q97).
  **M0125-0010** is a close second — one-line root cause, 4 queries, plausibly
  the same fix session, but an independent defect.
- Phase 6.1 (`GOOPG_RELSIZE_FALLBACK`) remains the only designed item that
  plausibly moves the 15-query unbounded timeout class at once.
- **Do not score Q18 or Q35 verdict flips as wins** (§5), and do not score a
  Q50 or Q46 row-count match as a pass (§3.1).
- **M0124-0005** (value checksum in the SF0.5 oracle) is now justified by
  measurement rather than by argument: 18 of 99 queries at SF=1 would pass a
  row-count-only gate while returning wrong answers.

---

## 8. Source index

| artefact | contents |
|---|---|
| `analysis/tpcds-sf1-resweep-20260728/RESULTS.md` | per-query table (Q1–Q99), per-chunk narrative, M0124-0006 attribution |
| `analysis/tpcds-sf1-resweep-20260728/chunk-*.txt` | raw harness output, 13 chunks |
| `analysis/tpcds-sf1-resweep-20260728/s-cold-proof.txt` | D5 state proof |
| `analysis/tpcds-sf1-resweep-20260728/diag-q47-rerun.txt` | Q47 standalone reproduction (143 s) |
| `analysis/tpcds-sf1-resweep-20260728/probe-q72-reprobe.txt` | Q72 fresh-server re-probe (636 s) |
| `analysis/tpcds-sf1-goopg-20260727.md` §5.2 | set A — prior SF=1 sweep, same budget |
| `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md` | protocol D1–D7, incl. D6a |
| `docs/design/tpcds-round2-fixes/README.md` §13 | the plan self-audit this report closes |
