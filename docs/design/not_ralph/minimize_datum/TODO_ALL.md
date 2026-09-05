# TODO_ALL — unified planner + executor performance plan

A single execution checklist for closing the whole planner + executor
performance gap against vanilla PostgreSQL 18.3. It consolidates the
remaining work from four sources and records their dependencies and
blockers in one place:

- `../planner_refactor_take2/TODO.md` (take2 — history; authoritative for
  what already landed, not for what remains)
- `../planner_refactor_take3/TODO.md` (take3 planner — fresh plan over the
  remainder; authoritative for planner sequencing)
- `../planner_refactor_take3/TODO_EXECUTOR.md` (take3 executor bundle —
  authoritative for executor sequencing)
- `TODO.md` in this directory (minimize_datum — the packed-retention
  bundle; blocked items marked inline)

**One checkbox ≈ one commit.** An item that would move two planner inputs
(or two executor inputs) at once is split before it is started (take3 08
§1 P5 / 13 §1 EX-P3).

Legend: `[ ]` not started · `[~]` in progress · `[x]` done · `[-]` dropped
(with a reason and a ledger row) · `[!]` blocked (blocker named inline).

When closing an item, rewrite its line as:

```
- [x] <ID> <title> — <commit>; gates: <list>; artifacts: <paths>
```

Each item names its design pointer and its gate in 2–4 lines.
Phase-closure verdict files go under
`analysis/planner-refactor-take3/<phase>-<date>/README.md` (planner) or
`analysis/executor-refactor/<phase>-<date>/README.md` (executor),
carrying the measurement-protocol header, the before/after pin + timing +
alloc table, and an explicit statement of anything that got worse.

Scope decision (recorded): this file is the **full planner + executor
mega-plan**, not an MD-centric pointer list. Dependent counterpart tasks
are consumed *while working* the item that needs them — each entry below
says which dependency it digests inline.

---

## 0. Status reconciliation (take2 vs take3 vs the tree)

Take2's TODO kept being updated after take3's TODO was written, so the two
disagree on several Phase-0 items. Every row below was verified against the
tree (`git log -S` / `--grep`) on 2026-09-04; the **tree verdict** governs.

| item | take2 state | take3 state | tree verdict + evidence |
|---|---|---|---|
| P0-08 re-pin plan baseline | `[x]` closed | `[ ]` remaining | **DONE** — `f4a5e7e75` re-pinned to `take2-p0-20260903`. Take3 entry is stale; do not redo. |
| P0-13 collapse positive control | `[x]` closed | `[ ]` remaining | **DONE** — `82dd30bbc` defaulted `GOOPG_PGSHAPED_COLLAPSE` on. Take3 entry is stale; do not redo. |
| P0-04 EXPLAIN naming | `[x]` closed (alias-drop fix; numbering left open) | `[ ]` remaining (suffix numbering) | **Consistent** — renderer fix landed (`P0-04` close entry), suffix numbering vs `select_rtable_names` still open. Work the numbering only. |
| P0-05/06/07 plan-parity instrument | `[ ]` open | `[ ]` remaining | **Consistent** — no capture commits, no `bench/tpch/plans-pg/` or `bench/tpcds/plans-pg/` fixtures exist. Still open. |
| P4-01 PathTarget/projection | take2: rev-10 steps 1–5 landed, default ON (`00d56df90`) | take3: slice 1 landed, slices 2/3 per DESIGN.md | **Superseded by the tree** — slices 1 (`588aa5fb5`), 2 (`8458b7552`) and 3 (`1d804ae02`) all landed; build-side Project narrowing is live (Q9 witness `Batches:` **2→1**, widths 1096→776/896→640/896→736/710→582 per `docs/design/planner-p4-01-target/DESIGN.md:61-71`; the 8-batch figure is the pre-retake baseline). Remaining: deferred slices (a) merge/NL input policy, (b) scan-node application, (c) upper targets (DESIGN.md:105-110) — owned by B-01a/b/c below. |
| EX0 instruments | n/a (take3 work) | all `[x]`, phase closed | **Confirmed** — `6ace9567e`/`503f12cf7`/`b6254f911` (EX0-01/02), `eaff87ee0`/`a7e2f86f6`/`eedc924de` (EX0-03/03b/03c), `86bf7473f` (EX0-04), `14f8d8f1a` (EX0-05), `e8c7400f9` (EX0-06). MD blocker (3) is discharged. |
| EX1 narrowing | n/a | EX1-01/02/02b `[x]`, EX1-03 dropped, EX1-04 cut 1 `[x]` | **Confirmed** — incl. `230a32bd0` (EX1-03 drop), `134324df6` (EX1-04 Cut 0 + unblock review). |
| EX2 clone elimination | n/a | audit + 02a/02c `[x]`, 02b dropped, 03 measure-only | **Confirmed** — incl. `efbca66e4` (EX2-01 audit: 45 sites), `fe2808bc9` (02b drop). MD-04 consumes the EX2-01 audit; no second audit. |
| EX3 spill/sort/hash | n/a | 01/02/03-step1/05-cutA `[x]`, step2 blocked, cutB dropped | **Confirmed** — incl. `53e41fdbf` (EX3-01), `7c586eaf7` (step-2 blocked + resume patch), `a9cf79c81` (cut-B drop). **Plus post-reconciliation landings: EX3-02 Cut 2 (`68ccd68c3`, stratum-D chunk views, unit 2.002→0.005) and EX3-05 Cut A (`fd2e2ae7a`, wantCTIDs gate) with full gates; E-12's "Cut 2 blocked" premise is stale (corrected inline). |

---

## 1. Dependency graph (binding order)

```
P0-04/05/06/07 (parity instrument) ──► everything that gates on PP roll-ups
P4-01 slices 1–3 [x] ──► B-01a/b/c (deferred slices) ──► EX1 sort half ──► EX3 geometry
P4-01 slices 1–3 [x] ──► E-14 (EX1 build-half redesign) ──► D-05/D-06 geometry
P4-01 slices 1–3 [x] ──► EX1 build half (EX1-01/02/02b [x], EX1-04 cut 1 [x])
P4-01 slices 1–3 [x] ──► B-12d/e/f ──► B-13 (P2-02b)
P3-01 ──► P3-03/P3-04 ──► P1-18 (a jointype switch before P3-04 is dead code)
B-16 (MCV-frequency half) + EX1 exit ──► E-02 (EX3-06 skew sizing)
E-07 (Gather slab parity) ──► E-08 (Gather-arm compilation)
C-14 (Incremental Sort) ◄──► E-15 (contract-publication spike, unconditional) ──► E-03 (conditional implementation)
EX2-01 audit [x] ──► D-05 (consume; never re-audit independently)
F-01 (duplicate build map) ──► D-05 (deletes a map D-05 would otherwise convert)
F-02 (probe-seam traffic) ──► D-05 (same assumed win; measure first)
P4-01 ──► EX1 ──► batching geometry ──► D-05 (take3 13 §8.2 + §8.6)
MD-01 ──► D-09 (descriptor needed for the alignment codec)
D-02 verdict ──► D-03 and everything after it (stop is a legal outcome)
D-04 (MD-03.5) measurement ──► D-05 go/no-go (stopping rule 05 §6)
All retention sites (SKIPped sites only with a ledger row, see D-10) ──► D-10
P2-08/P2-10 resume after Phase 3/4 consumers exist (take3 ledger
`take2-P2-08`, `take2-P2-10`); no checkbox until then.
```

---

## 2. Track A — Instruments, parity, licence (first)

*Exit: P0 parity instrument committed with baseline roll-ups for both
suites; plan pin non-skippable; take3-owner acceptance for the MD code
tracks recorded.*

- [x] **A-01 P0-04 suffix numbering.** Aligned 2026-09-05: (i) IOS
  `Alias` stamp + rule-based IndexScan alias fix; (ii) planner-stamped
  global RTID (cuts 1–3: scope, threading, explain_names migration +
  substitution propagation at 8 sites + `rangeBinding.rtid`); (iv) cut 4
  re-measure — ZERO duplicate printed scan labels across 21 planned
  queries (was: Q8/Q17/Q18 lost comparisons + Q11 unresolvable); Q11
  fully pairs; the 46 "upper bound" qualifier discharged (absolute
  parity count re-measured at C-21).
  Gates per cut: optimizer + Explain suites, TPC-H 24/24 MATCH, PP
  label-only DIFFERs all PG-faithful, TPC-DS PASS=95 (cuts 1–2).
  Artifacts: `docs/design/executor-a01ii-rtable-identity/DESIGN.md`
  (F1–F10 folded), `analysis/planner-refactor-take3/a01ii-cut4-20260905/`,
  `analysis/leftdeep-joins/a01ii-cut3*.txt`.
- [x] **A-02 P0-05 plan-parity capture.** Committed 2026-09-05:
  `bench/tpch/plans-pg/` (22, split from the paired PG capture) +
  `bench/tpcds/plans-pg/` (99, captured per-query vs 65438/tpcds05
  SF0.5 with the sweep's EXPLAIN-prefix trick; Q36/70/86 SKIP per
  oracle) + re-capture policy READMEs. Reproducible: capture commands
  recorded in the READMEs; fixtures byte-stable (EXPLAIN-only).
- [x] **A-03 P0-06 plan-parity diff.** Landed 2026-09-05:
  `scripts/pg-plan-parity-diff.py` (normalised tree compare, estimates
  side-column only; verdicts MATCH/SHAPE-DIFF/MISSING-NODE/ERROR/
  TIMEOUT; nine-category taxonomy; declared normalisations incl.
  strip-PG-Hash + alias/suffix canonicalisation) +
  `scripts/pg-plan-parity-diff-test.py` (budget pinned in-test).
  TPC-H roll-up: match=5 shapediff=15 missingnode=2 (PG-only
  Materialize: Q5/Q8) error=0 timeout=0; report-only, exit 0 always.
  Gates: self-test 15/15, unittest 5/5.
- [x] **A-04 P0-07 baseline roll-up.** Committed 2026-09-05:
  `analysis/planner-refactor-take3/a04-baseline-20260905/README.md` —
  TPC-H power table (TOTAL ≈235 s serial, 24/24), TPC-DS PASS=95
  sweep pointer, parity starting roll-up 5/15/2/0/0 with per-category
  decrements enforced from the pinned test budget.
- [x] **A-05 Non-skippable plan pin.** Landed 2026-09-05: `make
  plan-gate` strict by default (missing baseline / unreachable server →
  FAIL; explicit `PLAN_GATE_ALLOW_SKIP=1` opt-out); `--mode costs`
  (shape + cost/rows/width exact; structural stays default); TPC-DS
  plan-shape tail promotable via `SF05_PLAN_PIN=1` +
  `SF05_PLANS_BASELINE` (unset-baseline-under-pin is FAIL; accept by
  re-pointing; `=none` suppresses one run) — no second channel.
  Gates: plan-snapshot 15/15 (5 new: cost-only detection, SKIP-loud,
  unknown-mode), shell syntax, live dry-runs (FAIL without server,
  SKIP with opt-out, strict diff on fixtures).
- [x] **A-06 Take3-owner acceptance for MD tree commits.** ACCEPTED
  2026-09-05 by Ryo Kanbayashi (take3 owner): MD tree commits may land
  on `master` per the authorised approach in this item (D-02 document-
  only audit; D-04 throwaway prototype with deleted code; D-01/D-03/D-09
  through reviewed design and flag-gated prototype only; D-05 onward
  additionally needs E-14 + B-01c; landing code outside these bounds
  re-opens REVIEW.md B1). Pointer: this line.
  Original terms (kept for the record): MD is a new row representation
  with no re-proposal path in take3 13 §10 (README §Status, 04 §0.2),
  and reviewer (c) returned "do not start MD-01" (REVIEW.md B1).

---

## 3. Track B — Planner width + cost foundations

*Exit: widths fed into costs are right (P4-01 complete); `work_mem`
means the same thing in planner and executor (P2-02b landed);
`btcostestimate` batch lands whole; estimate ratchet monotone.*

Order inside this track: B-01a/b/c → B-12d/e/f → B-13 (P2-02b). Everything
else sequences freely but lands one variable per commit. B-05 and D-08
are EPICS — split into one-checkbox-per-commit items before starting
(ground rule 11).

- [x] **B-01a P4-01 deferred slice (a): merge/NL input policy.**
  Landed 2026-09-05: `deriveJoinKeepsAt` stamps both merge inputs
  (each gated on own `joinSubtreeNarrowable`); `narrowMergeInput`
  with `mergeKeepCoversSortKeys` gate (side-oriented via relids,
  fail-closed); NL = decline policy (probe keys live on inner
  `IndexClauses`, not the join path; `Project` above probe trips
  `innerBase` — plain-inner left as resume point).
  Sort-key-preservation proof: HashKeys order untouched (ascending
  keep), absorbed sorts impose nothing (keys ARE clause operands),
  keep ⊇ sort keys by construction + gate enforcement +
  `translateToLayout` tripwire.
  Gates: 6 new tests (2 existing assertions updated for the policy
  flip); TPC-H 24/24 MATCH; PP zero shape-kind changes (estimate/text
  only); Q12 merge 998→172 live; identical hashes at 64/4/512 MB;
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `internal/optimizer/{narrowoutput,createplanjoin}.go`,
  `pathtarget_test.go`.
- [-] **B-01b P4-01 deferred slice (b): scan-node application.**
  DECLINED 2026-09-05 after pre-implementation review (no safe first
  cut: in-place narrowing replays P4-01b; scan-arm Project lacks
  ancestor context — NLI/probe/outer-ref/poison — and reopens F4;
  restricted variant buys zero memory). Revisit needs (i) executor
  scan projection, (ii) narrowing-aware OuterColumnRef/Args rewriter,
  (iii) NLI probe inventory from IndexClauses, (iv) walkPlanExprs
  coverage + consumer. Ledger `take3-B-01b-declined`.
- [~] **B-01c P4-01 deferred slice (c): upper targets.**
  Review verdict: sort-input compute-only first (group second with
  Filter/Passthrough decline rules; window last after walker coverage;
  NO applying cut — no narrowing-aware upper rewriter exists).
  Slice 1 LANDED 2026-09-05: `Sort.InputTarget`/`InputTargetKnown`
  additive payload + `sort_input_target.go` derivation (keys ∪ above
  via existing walkers; unknown on unenumerable) + fail-closed assert;
  keys-only stamp at construction + above-aware re-stamp before return
  (upper tree built after the Sort).
  Gates: 17 new tests; optimizer suite; PP identical to B-01a run
  (zero drift); TPC-H 24/24 MATCH (assert never fired live).
  Remaining: window-compute (walker coverage first),
  applying cuts (need upper rewriter + key-preservation gate).
  Slice 2 (group-input) LANDED 2026-09-05: `Aggregate.InputTarget`
  payload + `group_input_target.go` (keys ∪ above; Filter/Passthrough
  decline; fail-closed assert) + construction stamp in
  `buildAggregateStage`, re-stamp-to-unknown at both Passthrough append
  sites and after `applyIndexOrderedGroupingRule`,
  splitAggregate Final-decline (Partial keeps).
  Gates: 22 group tests; optimizer suite; PP zero drift; TPC-H 24/24.
  Slice 3 (window-input) LANDED 2026-09-05: prerequisite walker
  widening first (`walkPlanExprs` Aggregate Filter/Passthrough +
  WindowAgg Funcs.Args/Filter/frame offsets; all 15 callers audited —
  sole mutator `remapOuterRefsInSubplan` now covers previously-missed
  same-scope refs = fix; readers flip to bail at worst) +
  `WindowAgg.InputTarget` payload + `window_input_target.go` (no
  field-level decline — every field enumerated post-widening; collector
  veto only) + keys-only construction stamps per spec.
  Gates: 29 new tests; optimizer suite; PP zero drift; TPC-H 24/24.
- [x] **B-02 P1-11 TOAST in the catalog heap writer.** Landed 2026-09-05
  as the pre-approved bounded-width interim: try-full-then-truncate in
  `persistStatsToPGStatistic` (pre-write exact measurement; 64 B bound
  cap UTF-8-safe → even thinning endpoints-kept → MCV-tail drop →
  scalar-only; scalars never altered; fitting rows bit-for-bit; NO
  format change, no re-init). Live-verified: c_comment/o_comment/
  ps_comment histograms persist post-ANALYZE (were dropped).
  Gates: stats/analyze suites + catalog suite; PP zero drift (ANALYZE
  changed stats, plans unmoved); TPC-H 24/24 MATCH.
  Fidelity loss ledgered (`take3-B-02-interim`); remove at TOAST
  out-of-line storage (M0125-0029).
  Artifacts: `internal/executor/operators_analyze.go`,
  `internal/executor/pgstatistic_truncate_test.go`.
- [-] **B-03 P1-14b remainder.** CLOSED 2026-09-05 with no
  implementation — all three sub-items predict zero EA-ratchet
  movement: regex ops (no `~` in either suite — dead path; resume =
  port regex_selectivity_sub + prefix analogue); prefix-range precision
  (blend cell unreachable; belongs to B-08, then eq-clamp tail);
  multibyte case-folding (ASCII-only data; executor-owned correctness).
  Ledger `take3-B-03-declined` (+ EA file-level baseline note).
- [x] **B-04 P1-21 `max(outer,inner)` fallback cap — verified-and-KEPT**
  2026-09-05: cap fires iff min(l,r)>200 on the unmeasurable fallback;
  proven both directions (MCV-fires ⟹ measured path; fallback-fires ⟹
  P1-15 declined); deleting moves 6M×800k 6M→2.4e13. Pinned by
  `cardinality_fallbackcap_test.go` (6 tests). Ledger `take3-B-04-kept`.
- [x] **B-05a P1-22 extended-statistics build.** Landed 2026-09-05:
  `_data` write path + ndistinct + dependencies builders (oracle
  magics/layouts pinned); `StatisticsObjectsForTable` accessor; hook
  at end of `analyzeRelationWith`; stattarget incl. 0-skip; reload
  decoder (consumer = later phase); codec KindBytes arms for the 4
  blob types. MCV/expressions explicitly deferred (TOAST +
  `_pg_statistic` encoding). Gates: 12 round-trip tests; catalog +
  initdb suites; live CREATE STATISTICS + ANALYZE builds a decodable
  3429 row; TPC-H 24/24 MATCH (Q1 stats-driven partial-agg move is
  values-correct); PP zero drift otherwise; TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `extstats_{build,ndistinct,dependencies}.go`,
  `sys_pg_statistic_ext_data.go`, `extstats_build_test.go`.
- [x] **B-05b P1-23 `statext_clauselist_selectivity`.** Landed
  2026-09-05: pure-planner port (in-memory registry keyed by table OID;
  empty ⇒ bit-identical old arithmetic); `choose_best_statistics` +
  strongest-dependency + backwards conditional-probability combine;
  clause compatibility = Var-half (col=const, IN, same-col OR, NOT,
  bare bool; ranges/joins/expressions decline); wired at
  conjunction/filter/clause-AND sites (join sizer deliberately NOT
  wired — oracle never runs ext on join lists).
  Gates: 15 tests; optimizer suite; units precommit scope green;
  PP zero drift (Q1 MATCH — fresh-server shape; the B-05a-run partial
  was ANALYZE-warmed stats, see `take3-stats-persistence-gap`);
  TPC-H 24/24 MATCH on a clean fresh-server sweep (prior sweep
  invalidated by mid-sweep server stop — relaunched);
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `internal/optimizer/extstats.go`, `extstats_test.go`.
- [x] **B-05c P1-24 `estimate_multivariate_ndistinct` for GROUP BY.**
  Landed 2026-09-05: exact-set combo lookup first (order-independent),
  fallback to untouched product; clamp/Yao paths unchanged; subset/
  superset fall back (iterative remainder = resume point).
  Gates: 9 tests; optimizer suite; PP zero drift; TPC-H 24/24 MATCH.
  Zero movement by construction (no production registry writer yet —
  loader is the resume point with the 3429 decoder).
  TPC-DS PASS=95 MISMATCH=0.
  Artifacts: `internal/optimizer/{extstats,cardinality}.go`,
  `extstats_ndistinct_test.go`.
- [ ] **B-06 P1-27 CTE-agg statistics.** DEFERRED-OPEN 2026-09-05
  (probe): guard is load-bearing (removal reverts Q74 99s→14s); PG has
  no answer either (single-key uniqueness; Q74 groups by 4). Genuine
  fix needs beyond-PG work (OID-less CTE-output synthesis + multi-key
  per-column ndistinct + FD-bound-only for agg outputs); no safe
  ratchet-moving increment. Ledger `take3-B-06-deferred` (resume steps
  inline). Keep open; revisit only with the synthesis design.
- [ ] **B-07 P1-30 index-endpoint probe + MCV widening.** DEFERRED-OPEN
  2026-09-05 (probe): endpoint probe architecturally blocked (no
  plan-time storage path in the pure planner; PG itself keeps it
  `#ifdef NOT_USED`); pure half (MCV-widen `histogramMax`, cutoff
  clamp) predicts ~0 ratchet (fresh endpoints, in-bounds literals).
  Ledger `take3-B-07-deferred`. Resume only on demonstrated
  in-suite out-of-range case or accepted zero-ratchet fidelity.
- [x] **B-08 P1-31 text/network scalars.** Text half LANDED 2026-09-05:
  `convertStringToScalar` faithful port (range scan + class widening +
  <9 ASCII rule + prefix strip + ≤12-char base-N) wired for the text
  family; 0.5 pin flipped to interpolation; B-03 tail clamp
  `Max(prefixsel, eq_sel)`; adjacent float4/float8 label fix.
  Gates: convert/string-histogram/pattern/range suites; PP zero drift;
  TPC-H 24/24 MATCH (timing neutral as predicted).
  Network half DEFERRED (zero in-suite columns/queries; inequality-only
  would not cover inclusion ops) — ledger `take3-B-08-network-deferred`.
  Artifacts: `selectivity.go`, `patternsel.go`, `rangequery_test.go`.
- [x] **B-09 P1-02 tree height + partial-index tuples.** Closed
  2026-09-05 with no code change: partial half already landed
  (`2bcd6b551`, unit-pinned) — verified (partial test green, PP zero
  drift, zero partial indexes in bench DDL so gates vacuous by
  construction); height analogue declined-deferred (fanout already on
  real pages; measured 0.0004%; true analogue needs storage hook +
  nbtree dep for degenerate-only moves). Ledger
  `take3-B-09-height-deferred` (resume: bloated-tree witness;
  PG-shaped predicate-fold reshape filed).
- [-] **B-10 P1-10 expression-index stats.** DECLINED 2026-09-05, same
  test take2 applied: NO consumer — every estimate path resolves bare
  ColumnRef→table-column stats (joinselectivity gap comments,
  pathkeysindex/pathparamindex/indexordered/planner guards, executor
  errors on expression keys); ANALYZE iterates table columns only;
  zero expression indexes in either suite. Ledger `take3-B-10-declined`
  (resume: access paths → collection → matching).
- [ ] **B-11 P1-16 re-diagnose Q9.** The single-`nd` explanation is RETIRED;
  close ledger rows 779/781/784 as stale and file what `estimate-audit`
  actually shows on the final joinrel.
  *design: take3 08 §4; gate: EA final-joinrel bar on Q9.*
- [ ] **B-12d Derived-table propagation: thread the settings by hand
  through `planSelectWithParent` for `(SELECT …) AS alias` FROM items.**
  After B-01a/b/c per take3 08 §10.1. The reverted mechanical attempt
  threaded from the wrong scope — hand-thread only.
  *design: take2 07 §3.3 + 04 §12.3 + take3 08 §5.1; gate: take3 09 §5 P2
  (GUC-effect + both-suites timing).*
- [ ] **B-12e Set-operation operand propagation:** same hand-threading for
  set-operation operands. After B-01a/b/c.
  *design: take2 07 §3.3 + 04 §12.3 + take3 08 §5.1; gate: take3 09 §5 P2
  (GUC-effect + both-suites timing).*
- [ ] **B-12f Scalar-subquery propagation:** same hand-threading for
  scalar-subquery sites. After B-01a/b/c.
  *design: take2 07 §3.3 + 04 §12.3 + take3 08 §5.1; gate: take3 09 §5 P2
  (GUC-effect + both-suites timing).*
- [!] **B-13 P2-02b `work_mem` BootVal 512 MB → 4 MB — BLOCKED on B-01a/b/c +
  B-12d/e/f.** Three lines move together (GUC BootVal,
  `postgresql.conf.sample`, `hashsize.DefaultMemLimitBytes`) +
  `TestSampleConfigCoversRegistry`. Lands alone, both suites timed, expect
  plans to move. Last measurement: +23.1% (entirely Q9+Q7, values
  correct); the gap to close is tuple WIDTH (B-01a/b/c) + lost Gather
  (Track C P5).
  *design: take3 08 §5.1; gate: take3 09 §5 P2 + B2.*
- [ ] **B-14 P2-09a ScalarArrayOp index path.** `num_sa_scans` needs the
  missing *path*: build index `= ANY` descents + `ceil(pages/3)` clamp.
  Gate on path existence/shape on IN-list fixtures.
  *design: take3 08 §5.2; gate: take3 09 §5 P2 (PP shape on IN-list
  fixtures).*
- [ ] **B-15 P2-09b `btcostestimate` batch.** Land the remainder whole
  (descent present; the reverted per-tuple qual cost rejoins here), with
  acceptance on the **aggregate sweep TOTAL**, not the per-query gate
  (R6 — the per-query gate missed a 3.3% move once).
  *design: take3 08 §5.2; gate: take3 09 §5 P2 + TOTAL arm with plan
  attribution.*
- [ ] **B-16 P2-11b MCV-frequency half.** Plumb the inner key's MCV list to
  the cost site for the skew term (`estimate_hash_bucket_stats`, clamp
  [1e-6,1]); zero/isdefault fraction suppresses the term. Unblocks EX3-06
  (Track E).
  *design: take3 08 §5.3; gate: best-of-bundle discipline + TPC-DS
  aggregate; plan-change attribution before believing per-query moves
  (R7).*
- [ ] **B-17a `disabled_nodes` sort + material setters** (take3 02 §1.2).
  Makes the family A/B-able for later phases.
  *design: take3 08 §5.4; gate: GUC-effect test per newly-live GUC
  (take3 09 §5 P2).*
- [ ] **B-17b `disabled_nodes` agg-hashed/mixed setters** (take3 02 §1.2).
  *design: take3 08 §5.4; gate: GUC-effect test per newly-live GUC.*
- [ ] **B-17c `disabled_nodes` gather-merge setter** (take3 02 §1.2).
  *design: take3 08 §5.4; gate: GUC-effect test per newly-live GUC.*
- [ ] **B-17d Retire producer-skipping for scans** where PG counts instead
  of gating. Hard gates stay gates (index-only, TID-except-CURRENT-OF,
  memoize, incremental-sort; take3 01 §12, 02 §1.2). Own commit — scan
  shapes change.
  *design: take3 08 §5.4; gate: GUC-effect test per newly-live GUC.*
- [ ] **B-17e Retire `enable_nestloop_index`** once NLI is an ordinary
  parameterised-nestloop path (take3 08 §6.4). Own commit with its own
  gate.
  *design: take3 08 §5.4 + §6.4; gate: GUC-effect test (take3 09 §5 P2).*
- [ ] **B-18 P2-04 cache-key half.** P2-04 landed the override bypass; the
  planner context as part of the cache KEY (rather than a bypass), and
  removing the bypass once keyed, remains open. Also attach the P2-02
  remainder here: GUC-effect fixtures for `random_page_cost`,
  `cpu_index_tuple_cost`, `effective_cache_size`, `parallel_*` (file,
  don't claim).
  *design: take3 08 §5.1; gate: `SET random_page_cost` changes the cached
  plan; GUC-effect test per fixture GUC.*

---

## 4. Track C — Search, upper planner, parallelism, deletion, acceptance

*Exit: PG-only join spines OFFERED or reasoned; upper rels are paths;
parallelism costed not forced; single planner; acceptance run green.
**Do not start P3-02/03/04 before B-01 lands** (take3 08 §2.3 safety
rule).*

- [ ] **C-01 P3-01 `SpecialJoinInfo` population.** Field set exists; what is
  missing is population, blocked on name resolution. Partial fix UNSAFE
  (underestimate = wrong answers; fall back to `syn` on any uncertainty).
  Moves no plan alone — unobservable until C-03/C-04 consume it.
  *design: take3 08 §6.1; gate: units.*
- [ ] **C-02 P3-02 `distribute_qual_to_rels` + `check_outerjoin_delay.**
  A qual is **placed**, not copied down. Supersedes
  `pushInnerJoinInputQuals` copying (double evaluation). After B-01.
  *design: take3 08 §6.2; gate: take3 09 §5 P3 + values-diff (R8).*
- [ ] **C-03 P3-03 `join_is_legal` on real SJIs.** DP builds outer, semi
  and anti joinrels directly. After B-01.
  *design: take3 08 §6.2; gate: take3 09 §5 P3 (DPPATH OFFERED/ACCEPTED +
  `estimate-audit --enum-trace`).*
- [ ] **C-04 P3-04 delete `splitOuterSpine` + `pinnedOuter()` decline.**
  Mixed comma + `LEFT JOIN` FROM becomes **one** search problem (Q72
  witness). **Unblocks P1-18 (C-05).** After B-01.
  *design: take3 08 §2.2 + §6.2; gate: take3 09 §5 P3 — PP both suites +
  timing.*
- [ ] **C-05 P1-18 outer/semi/anti sizing — executes HERE, after C-04.**
  Port `calc_joinrel_size_estimate`'s jointype switch.
  *design: take3 08 §4 + §6.2; gate: EA ratchet.*
- [ ] **C-06 P3-05 retire `GOOPG_PGSHAPED_COLLAPSE`.** Once C-04 makes it
  the only jointree path; `from_collapse_limit`/`join_collapse_limit`
  take upstream meaning. Own commit, both suites timed.
  *design: take3 08 §6.3; gate: take3 09 §5 P3.*
- [ ] **C-07 P3-06 `standard_qp_callback` analogue** (`query_pathkeys =
  group ?: window ?: longer(distinct, sort) ?: setop`) + complete
  `has_useful_pathkeys` so ORDER BY / GROUP BY motivate index paths.
  *design: take3 08 §6.3; gate: take3 09 §5 P3 (PP).*
- [ ] **C-08 P3-07 `param_source_rels`** (hard-coded 0 today) with
  `allow_star_schema_join` semantics.
  *design: take3 08 §6.3; gate: take3 09 §5 P3 (PP).*
- [ ] **C-09 P3-08 `reduce_unique_semijoins`** — after clearing SEMI
  left-only `Output()` re-indexing (ledger 794).
  *design: take3 08 §6.3; gate: take3 09 §5 P3 (PP + values-diff).*
- [ ] **C-10 P4-00 pre-phase scoping.** Grouping-sets interaction,
  `remove_useless_joins`, `reduce_outer_joins.go` interaction,
  FROM-subquery pull-up coverage — each a scoped item before the phase
  starts.
  *design: take3 08 §7; gate: scoped items filed (take3 09 §9 reporting).*
- [ ] **C-11 P4-02 upper `RelOptInfo`s** (`GROUP_AGG`, `WINDOW`,
  `DISTINCT`, `ORDERED`, `FINAL`) with pathlists.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-12 P4-03 real upper-rel `PathSort`** (has a `createPlanNode` arm;
  today only ever a merge-join child, never competing with a hashed
  alternative).
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-13 P4-04 bounded / top-N sort** (`cost_sort`'s `limit_tuples`
  arm) — the largest recorded `ORDER BY … LIMIT` win.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP + timing).*
- [ ] **C-14 P4-05 Incremental Sort** node + `create_incremental_sort_path`.
  No executor counterpart exists — BLOCKED: resume after executor support
  and excluded from closure until then. Publish the executor input
  contract first (take3 13 §8.7 tiebreaker), do not absorb silently.
  *design: take3 08 §7; gate when unblocked: take3 09 §5 P4 (PP).*
- [ ] **C-15 P4-06 `create_grouping_paths`** (sorted + hashed agg priced by
  `cost_agg` incl. hash spill arm); retire the three aggregate rules.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP + timing).*
- [ ] **C-16 P4-07 `create_distinct_paths`** (hashed / sorted /
  unique-over-sorted). Depends on landed P1-25.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-17 P4-08 `tuple_fraction` end-to-end** (every upper rel, not only
  the join search).
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-18 P4-09 `create_window_paths`** + set-operation paths, priced.
  *design: take3 08 §7; gate: take3 09 §5 P4 (PP).*
- [ ] **C-19a P5-01 `consider_parallel` per rel**
  (`set_rel_consider_parallel`).
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP + serial control).*
- [ ] **C-19b P5-02 `create_plain_partial_paths` populating
  `PartialPathlist`.** `computeParallelWorkers` moves into path
  generation.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP).*
- [ ] **C-19c P5-03 parallel eligibility for plain index scans.** Extend
  `drivingScan` (SeqScan/BitmapHeapScan/IndexOnlyScan + wrappers today;
  plain IndexScan still missing) so PG's Parallel Index Scan has a
  counterpart. Closes a `MISSING-NODE` entry.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP).*
- [ ] **C-19d P5-04 `generate_useful_gather_paths`** producing `PathGather`
  and `PathGatherMerge` priced by `cost_gather`/`cost_gather_merge`, with
  `createPlanNode` arms.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP, parallel-on + serial
  control).*
- [ ] **C-19e P5-05 re-decide Gather Merge → Sort → Parallel scan by
  cost** rather than `sortPartialRootPays`' hard-coded decline. If goopg's
  costs still choose leader-side sorting, record it as a permitted
  divergence with the committed measurement (take3 09 §4.4 case 1).
  *design: take3 08 §8; gate: take3 09 §5 P5 (timing both shapes).*
- [ ] **C-19f P5-06 parallel hash join as a `parallel_aware` hash path,
  priced.** Executor consumer check required (a fixture where the path
  wins must execute as parallel hash).
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP).*
- [ ] **C-19g P5-07 partial aggregation as paths**
  (`create_partial_grouping_paths`), replacing `splitAggregate`. Depends
  on C-15.
  *design: take3 08 §8; gate: take3 09 §5 P5 (PP + values-diff).*
- [ ] **C-19h P5-08 retire `MaybeAddGather`.**
  *design: take3 08 §8; gate: take3 09 §5 P5 — plan-parity both suites,
  parallel and serial arms.*
  (Serial control arm unchanged throughout C-19a–h. Ordering trap already
  measured: at small budgets the plan moves onto index-driven joins the
  old post-pass cannot drive — take2 07 §3.2.)
- [ ] **C-20a P6-01 one cardinality estimator.** Delete legacy
  `estimateJoin`/`EstimateRows` + the `joinkeyproof.go` mirror;
  everything reads `calcJoinrelSize`. Prerequisite: EXPLAIN `rows=` from
  the path (P0-02 remainder) + legacy consumers gone (C-11…C-18).
  *design: take3 08 §9; gate: take3 09 §5 P6 (PP + EA ratchet).*
- [ ] **C-20b P6-02 `PathTarget` + range table.** Replace
  `baseLeaf`/`baseOffset`; delete `joinlayout.go` remapping + the
  `createplanroot.go` boundary assertions. Deletes the largest silent
  wrong-answer class — value-level `tpch-runner -diff`, never counts.
  *design: take3 08 §9; gate: value-level `-diff`, never counts.*
- [ ] **C-20c P6-06a retire `GOOPG_INDEXKEY_HARVEST`**, regenerating
  `planner-flags.env`. Own commit with before/after parity roll-up +
  timing table.
  *design: take3 08 §9; gate: byte-identical plans for the flip
  (take3 09 §5 P6).*
- [ ] **C-20d P6-06b retire `GOOPG_INDEX_PROBE_MULT`**, regenerating
  `planner-flags.env`. Own commit with before/after parity roll-up +
  timing table.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [ ] **C-20e P6-06c retire `GOOPG_HASH_OUTER_JOIN`**, regenerating
  `planner-flags.env`. Measured safe (CKMISMATCH=0) but a wash (+1 s
  net): NOT flipped; re-measure after the `btcostestimate` batch
  (B-15). Own commit with before/after parity roll-up + timing table.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [ ] **C-20f P6-06d retire `GOOPG_NLI_COSTGATE`**, regenerating
  `planner-flags.env`. Only once the search selects NLI on its own
  merits (remaining `btcostestimate` + hash terms, per P3-11). Own commit
  with before/after parity roll-up + timing table.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [ ] **C-20g P6-06e retire `GOOPG_PGSHAPED_DP` last**, regenerating
  `planner-flags.env`. The off path is unretirable while the legacy
  rewrites are load-bearing (P6-03/P6-04 must-not-delete). Own commit
  with before/after parity roll-up + timing table.
  *design: take3 08 §9; gate: byte-identical plans for the flip.*
- [ ] **C-20h P6-07 `setrefs` phase + P6-08 `RestrictInfo` caching.**
  `setrefs` only if C-20b shows the executor still needs explicit column
  resolution; caching is planning-speed, not plan-quality.
  *design: take3 08 §9; gate: take3 09 §5 P6 (P6-08: planning-time
  comparison, plans byte-identical).*
  P6-03/04/05 stay **must-not-delete** (measured 6.5× / 12.5× /
  live-tripwire oracle — record, do not retry without new evidence).
  *design: take3 08 §9; gate: take3 09 §5 P6 byte-identical or
  explained-and-timed.*
- [ ] **C-21 P7-01 full acceptance run.** Both suites, S-cold and WARM,
  full PP roll-up, EA ratchet, complete timing table, §6 headers on every
  artifact. Bars A1–A5 + B1–B3 + C1–C7 (take3 09 §4); B4 directional only.
  Verdict under `analysis/planner-refactor-take3/acceptance-<date>/`
  with before/after roll-ups and an explicit worse-statement; negative
  results kept verbatim.
  *design: take3 08 (all); gate: take3 09 §8.*

---

## 5. Track D — minimize_datum core

*Gates: 06 (per-commit floor + per-slice gates + measurement protocol).
`Datum` stays 48 bytes — the bundle's falsifiable claim (06 §5.5).
No wall-time target in acceptance (06 §5).*

Geometry-free slices (D-01/D-02/D-03/D-04/D-09) proceed per A-06;
D-05 onward additionally needs A-06 acceptance + E-14 + B-01c.

- [ ] **D-01 MD-01 `TupleDesc` + type metadata.** `attlen`/`attbyval`/
  `attstorage` on `colTypeInfo`, sharing **one** `pg_type.dat`
  transcription with `userTypeAttrsForOID` (reuse the existing
  name→OID→descriptor bridge — REVIEW M-goopg-2). Honour
  `coltypeinfo.go:12-25`'s DDL-staleness contract. **On landing:
  re-derive the site count from the compiler and record the delta
  against 05 §2 (05 §5).** The A-05 plan-pin hardening is owned by Track
  A — this item points at it and does not duplicate it.
  *design: 04 §3 (D-1), 03 §5 (TD-1); gate: 06 §3 MD-01 (agreement test +
  oracle spot-check); files: `coltypeinfo.go`,
  `pg18_user_catalog_rows.go`, new.*
- [ ] **D-02 MD-02 R-1 audit — derived-column type fidelity.** Count plan
  nodes whose output schema contains a column `NewTupleDesc` declines, by
  reason, raw and weighted by estimated retained rows, over both suites.
  **Verdict in the words proceed / re-scope / stop. This item can stop
  the bundle. It is not a formality.** Every declining type name is also
  a latent on-disk retyping bug on a path that already ships — ledger
  each whatever the verdict.
  *design: 04 §3 (D-2), §9.2; gate: 06 §3 MD-02; document only.*
- [ ] **D-03 MD-03 `PackedTuple` + `PackedSlot`, unreachable.** MinimalTuple
  layout, 15-byte header, `hoff`-relative accessors, 4-byte hash prefix;
  `(nvalid, off)` watermark over the real decode wrapper (call
  `decodeRowRangeInfo` or a new exported wrapper taking `info` — NOT
  `DecodeRowRangeIntoMctxPGTupleStyled`, which hardcodes `info = nil`;
  REVIEW M-goopg-3); **all six type-switch arms** + `attcacheoff` fast
  path + exhaustiveness tests moved from `spill.go`. **No producer.**
  Resolve R-7 (descriptor ownership past the operator) and R-8 (scratch
  reset point) before or in this slice — R-8 is the single most
  consequential unstated detail.
  *design: 04 §2, §6 (D-4), §9.1 (R-0), 03 §7.1 (TD-3), TD-2; gate: 06 §3
  MD-03 (a test per switch, watermark property test, escape check);
  files: `slot.go`, `opnode.go`, `expr.go`, `exprnode.go`,
  `operators.go`, `codec.go`, new.*
- [ ] **D-04 MD-03.5 throwaway prototype.** Pack and unpack the Q9 build
  side behind a flag with a hardcoded descriptor, measure 05 §6's four
  numbers, **delete the code**. Tests MD-04's hypothesis (incl. the R-3
  encode-cost question — `encodeValuePGCtx` is ~617 lines per column per
  row) before ~900 LOC is sunk.
  *design: 05 §6; gate: values-diff only (the code does not land); not a
  commit to `master`.*
- [!] **D-05 MD-04 hash join — BLOCKED on A-06 + E-14 + A-05.**
  Serial and parallel in one commit, `hashsize` model re-derived in the
  same commit. The load-bearing EX1 dependency for hash-join geometry is
  the **build half** (E-14 redesign), not the sort half; EX0 is landed.
  Consumes the EX2-01 audit
  (`analysis/planner-refactor-take3/ex201-audit-20260904/README.md`;
  either EX2-01 lands first or MD-04 produces the audit for the sites it
  touches — EX2-01 landed, so consume it). F-01/F-02 measure first (graph
  edges above).
  **Two corrections applied before starting:** (a) the gate is restated
  over the hash-join operator on a named class of shapes (multi-join
  hash cascades over million-row builds at PG `work_mem`, Q9 reported as
  the example — never the gate), satisfying take3 EX-P2 which forbids
  single-query gates; (b) the missing fifth retention lane — the
  composite multi-key lane (ledger M0127), unbounded, packed-byte keys —
  joins Tier A. Report against the **measured** pre-state (Q9 witness
  `Batches:` 2→1, narrowed width ≈100; ledger `take2-executor-residual`:
  widths 1098 B vs PG 23 B, 97 MB vs 38 MB peak, 63.8 s vs 6.2 s), not
  the modelled 128/64.
  **Stopping rule 05 §6: if the measurement says stop, revert MD-04 —
  do not keep it and stop (R-4).**
  *design: 04 §4.1, §5 (D-3), §9.4 (R-3); gate: model-vs-reality test
  (`hashsize.EntryBytes` predicts retained bytes/row within tolerance) +
  batch-count movement on the named shape class + 06 §2 floor with
  `CKMISMATCH=0`; files: `operators_join_agg.go`,
  `parallel_hash_build.go`, `hashsize/hashsize.go`.*
- [!] **D-06 MD-05 sort — BLOCKED on B-01c (sort-side projection).**
  Gate checks **ordering explicitly**, not membership
  (`operators.go:1010-1015`: mismatched sort/merge comparators emit
  out-of-order rows with no error). Needs two deformed rows at once; a
  `PackedSlot` has one scratch `Row` — R-11, re-priced, not mechanical.
  *design: 04 §4.1; gate: 06 §3 MD-05.*
- [ ] **D-07 MD-06 materialize** (`operators_material.go:68`).
  *design: 04 §4.1; gate: 06 §2 floor + alloc arm.*
- [ ] **D-08 MD-07…MD-12 Tier B [EPIC — split per site before start].**
  Window (whole partition set, no spill path — report OOM-exposure
  reduction, not batch counts; goopg has no tuplestore, 04 §4.2); memoize
  + CTE cache + recursive worktable + lateral CTE (recursive-CTE
  lifetimes are the risk; resolve R-7 first); Gather + Gather Merge
  (serial control + worker arms); RETURNING buffers + `conn_tx.Rows`;
  outer-join sweep + agg group representative (four `[][]Datum`
  accumulators **stay Datums** — Tier C boundary); ~13 small `rows []Row`
  sites (mechanical, low value; one commit or dropped).
  *design: 04 §4.1, §8; gate per slice: 06 §2 floor + alloc arm; MD-09
  adds the parallel arm
  (`parallel_substrate_test.go:26-80` pattern).*
- [ ] **D-09 MD-1x conditional alignment.** `att_align_datum` on encode,
  `att_align_pointer` on decode, generalising `catalog/codec.go:1693-1695`.
  **Changes the on-disk format.** Independent of D-03…D-08 (03 §7.2); may
  land before D-03 or after D-08, never inside another item. Blocked on:
  D-01.
  *design: 03 §3 (D1), §3.3, TD-4; gate: 06 §3 MD-1x — byte goldens vs
  live PG 18.3 (TOAST excluded, reason stated), backward read of old
  nominal-aligned tuples, forward read of a PG-authored unaligned short
  varlena.*
- [!] **D-10 MD-last spill payload — BLOCKED on every in-memory
  retention site.** Convert `spill.go`'s payload to the PG format;
  framing (`WriteRowHashed`'s hash-then-tuple) unchanged. The nine
  existing test functions are **re-pointed at the new codec, not
  deleted**. A site SKIPped under §8 unblocks D-10 only if the SKIP
  records it explicitly out of scope with a ledger row; an
  unrecorded site keeps D-10 blocked.
  *design: 03 TD-5, 04 §4.1 Tier D; gate: 06 §3 MD-last + round-trip
  across a real spill on a spilling shape.*
- [ ] **D-11 MD acceptance + open-gap ledgering.** 06 §5 six conditions:
  values unchanged every commit both suites; one retention format (Tier A
  + significant Tier B + spill; R-4 closed by construction); model
  matches storage (test); batch-count witness moved and recorded;
  **`Datum` still 48 bytes** (`datum.go:187` untouched); byte-format
  fidelity stated honestly (D1 goldens / D2 ledgered open / D3-D4
  non-issues). No time target. Also files the ownerless ledger rows: the
  PG-format TOAST-pointer gap (03 §4 D2 — out of scope, resume point
  required) and any D-08 slice deferred with a keep-open row.
  *gate: 06 §5.*

---

## 6. Track E — Executor remainder (take3 bundle)

*Gates: TODO_EXECUTOR ground rules (no query-specific forcing; timing +
alloc arms together; plan-shape pin `changed=0`; both suites fresh server
per arm; values never counts for projection/join-adjacent changes).*

- [!] **E-01 EX3-04 sort spill runs + merge discipline — BLOCKED on the
  EX1 sort half (B-01c).** Run formation on `flushChunk`, tape-style
  merge-back (logtape analogue, take3 10 §9). Spill thresholds are
  batching geometry: on pre-EX1 widths this is premature by rule (take3
  13 §8.2, EX-P7).
  *design: take3 13 §5; gate: spilling-sort shapes; values + pin.*
- [!] **E-02 EX3-06 skew residency + single-pass build — BLOCKED on B-16 +
  EX1 exit.** MCV-pinned hot keys (consumes planner B-16 input) +
  collapse two-pass/two-map build. Skew-residency sizing is an §8.4 scale
  argument over narrowed rows — last in EX3 for that reason.
  *design: take3 13 §5; gate: skewed-shape A/B; values + pin + alloc
  arm.*
- [ ] **E-15 EX3-07 input-contract publication spike (unconditional).**
  The 13 §8.7 tiebreaker owned by someone: publish the executor's
  presorted-prefix input contract (what a future Incremental Sort input
  must guarantee) as a doc + contract test against current sort
  behaviour. Breaks the C-14/E-03 circular wait.
  *design: take3 13 §8.7; gate: contract doc + test, no behaviour
  change.*
- [!] **E-03 EX3-07 presorted-prefix implementation — file ONLY if
  planner C-14 activates.** E-15 already published the contract, so this
  item is purely conditional. Do not absorb silently.
- [ ] **E-14 EX1 build-half redesign (no second truncation).** The EX1-04
  unblock review proved owned-payload shortening unsafe without
  projection; P4-01 landed projection at the build-side `Project` site,
  so the hash-build half is unblocked-but-needs-redesign (Cut 0 alloc arm
  measured: Q9 20.14→13.88 s, alloc 9.43→8.52 GB, values identical).
  Implement narrowed-width retention on the Project shape without a
  second `[0,bound)` truncation. Unblocks D-05 geometry pricing.
  *design: `docs/design/executor-ex1-04-owned/DESIGN.md` + unblock review
  (`134324df6`); gate: poison tests on the Project shape (Cut 1 pattern)
  + alloc arm + values + pin.*
- [ ] **E-04 EX4-01 `filterOp` compilation (serial).** Stop re-interpreting
  the prefilter predicate. No planner dependency.
  *design: take3 13 §6; gate: double-evaluation gone from the EX0-04
  slice; twin-parity tests; values + pin.*
- [ ] **E-05 EX4-02 join residual + key compilation.** `mergeResidualMatch`
  and per-`plan.Algo` key evaluators through the slab. Values-diff
  mandatory (join-adjacent, R8).
  *design: take3 13 §6; gate: twin-parity per arm; values + pin.*
- [ ] **E-06 EX4-03 agg transition fast path.** Compile builtin-agg
  `transfn` expressions; `MIXED`/spill behaviour unchanged.
  *design: take3 13 §6; gate: per-row transfn slice down; values + pin.*
- [ ] **E-07 EX5-01 slab parity for `Gather`.** `buildRec` `Gather` arm;
  parallel queries stop falling back to legacy `Build`. Unlocks E-08;
  re-proves EX1–EX4 wins on workers.
  *design: take3 13 §7; gate: parallel slab coverage test; serial arm
  unchanged; pin.*
- [!] **E-08 EX4-04 `Gather`-arm slab reachability — BLOCKED on E-07.**
  E-04…E-06 proceed independently.
  *design: take3 13 §6; gate when unblocked: parallel filterOp compiled;
  serial arm unchanged.*
- [ ] **E-09 EX5-02 shared build hardening.** Barrier discipline for
  build/probe phases, cooperative-stall measurement under skew,
  worker-count scaling on Q9-class shapes.
  *design: take3 13 §7; gate: scaling + skew A/B; Datum-safety tests;
  serial arm.*
- [ ] **E-10 EX5-03 Gather/GatherMerge ordering + exchange.**
  Worker-sorted slices, leader heap merge. Cost-decision stays with
  planner (permitted-divergence candidate).
  *design: take3 13 §7; gate: ordering tests; pin; serial arm.*
- [ ] **E-11 EX5-04 AIO `ReadStream` decision.** Measurement item with two
  legal outcomes: wire with depth policy + timing/alloc gate, or
  ledger-decline (pool hints + workers suffice). Not a commitment.
  *design: take3 13 §7; gate: A/B or ledger row.*
- [!] **E-12 EX3-02 Cut 3 (oversize + teardown) — BLOCKED on E-14.**
  (Record correction 2026-09-04: Cut 2 already LANDED — `68ccd68c3`,
  unit headers 2.002→0.005, TPC-H 24/24 + PP 22/22 + TPC-DS PASS=95;
  only Cut 3 remains here.) Queued behind landed Cut 0/1/2; arena sizing is
  batching geometry over the redesigned build rows.
  *design: `docs/design/executor-ex3-02-dense-build/DESIGN.md`; gate:
  poison tests + gate suite + values + pin.*
- [ ] **E-13 EX1-04 Cut 2 (owned-row tightening on Project-declined
  paths).** Only if a later alloc arm shows a residual. Cut 1 (poison
  tests on the Project shape) landed.
  *gate: alloc arm residual demonstrated first.*
- [!] **E-16 EX3-03 step-2 resume — BLOCKED on spill-cost calibration.**
  Session `work_mem` threading is implemented and unit-green but moves
  Q7/Q9 plans to slower merge shapes at bench `work_mem` (model prices
  hash above merge while forced-hash proves faster). Resume with the
  filed artifact
  (`analysis/planner-refactor-take3/ex303-step2-deferred-20260904/`,
  README + clean-applying `plumbing.patch`, ledger
  `take3-EX3-03-step2-blocked`): recalibrate the spill-cost model first,
  then re-apply the plumbing and re-gate.
  *design: `docs/design/executor-ex3-03-workmem/DESIGN.md`; gate:
  Q7/Q9 plans at bench `work_mem` + forced-hash proof + values + pin.*

---

## 7. Cheap interventions first (05 §1.5)

Priced against the MD bundle; each gets a measurement slice before any
larger work that assumes the same win (graph edges in §1). SKIP with a
ledger row if the measurement says no.

- [ ] **F-01 Delete the duplicate build map** (`lazyHash` + `lazyIntHash`
  both maintained — peak build memory ~2× on the int-key path). One
  commit. NOTE: 04 §4.1 lists both maps as things to *convert*, doubling
  Tier A work — deleting first removes that scope.
  *evidence: 02 §3, take2 07 §6; gate: ~2× build-side memory verified on
  a Q9-class shape; values + pin.*
- [ ] **F-02 Probe-seam re-materialisation** (hash cascade re-materialises
  probe input at every level, twice, both paths; ~18 M pool round-trips,
  ~2×2.3 GB `Datum` traffic Q9-class). Bounded, split before start if it
  touches two seams.
  *evidence: take2 07 §6; gate: traffic delta measured; values + pin.*
- [ ] **F-03 24 B pointer-free `Datum` remainder** (`Buf []byte` hidden
  behind ~43 non-test references; Kind→1 byte etc. already landed).
  NOTE: `Datum` re-layout was attempted and reverted (04 §11.2) — treat
  as hostile until the measurement says otherwise; 04 §0.1's dismissal
  priced only the weakest ~32 B variant, not this 2× design.
  *evidence: `docs/design/perf-optimize/02-datum-pointer-free.md`, 05 §3;
  gate: 2× claim vs MD-arithmetic; values + pin.*

---

## 8. SKIP policy

The following outcomes authorise SKIP for any item above, regardless of
sunk effort. SKIP is recorded, never silent: a ledger row in
`.ralph/deferral_ledger.md` (with `postgres/` citation + resume point)
plus a row in this file's Dropped table. Precedents: EX1-03 (owner
direction + no witness), EX2-02b (infeasible — no sole-owner transfer),
EX3-05-cutB (verified no-win), P1-06 (wrong trade), P1-10 (no consumer),
P6-03/04/05 (load-bearing).

1. **No measured performance gain** — verified by the item's own A/B under
   the measurement protocol (fresh server per arm, cgroup cap, stated
   `work_mem`, ±17% noise band respected). A timing claim inside the band
   is not a claim.
2. **Severe maintainability regression** — the change makes its area
   materially harder to reason about, extend, or debug, out of proportion
   to the measured gain. State the mechanism, not the feeling. Applies to
   **non-acceptance items only** (never C-21/D-11), and requires
   take3-owner sign-off (or a second independent measurement) recorded on
   the SKIP row — one agent's judgment alone does not drop plan scope.
3. **Infeasible in goopg's architecture** — the design cannot be realised
   (missing consumer, violated invariant, unbounded liability) no matter
   the effort. Name the architectural fact, not the schedule.

---

## 9. Ground rules (all tracks)

1. **Values are the gate, never counts** — TPC-H `-digest` + `-diff`,
   TPC-DS `CKMISMATCH=0` (not just `MISMATCH=0`); `ck=n/a` queries are
   row-count-only and are not values evidence.
2. **One variable per commit**; cancelling pairs move in one commit.
3. **Plan-shape pin on every commit** — a moved plan is reported with its
   cost roll-up (A-05 non-skippable pin once landed), never fixed
   executor-side by preference.
4. **Timing + allocator arms together** — a CPU win that doubles
   allocations is not a win.
5. **Never `-count=1`** in a gate run. Never `git commit --no-verify`
   for code (`-n` is authorised for **documentation** commits only).
6. **Sibling paths move together**: encode↔decode, serial↔parallel,
   the two cardinality estimators, the two NLI routes.
7. **Every deferral gets a `.ralph/deferral_ledger.md` row** with an
   upstream `postgres/` citation and a resume point.
8. **No query-specific forcing**, no penalty multipliers, no shape
   preferences; gates name operators, never queries.
9. **Fix_plan + working-set discipline**: tick boxes in
   `.ralph/fix_plan.md` per loop; `make ralph-state-guard` before the
   status block; rewrite `.ralph/working_set.md`.
10. **Pre-commit bar**: `RALPH_PRECOMMIT_SCOPE=units
    scripts/ralph-precommit-test.sh` green before every code commit; the
    hook's pgbench smoke is machine-enforced — never bypass.
11. **Epics split before start**: an item covering N commits (B-05, D-08
    and any item marked [EPIC]) is replaced by N checkboxes with existing
    or new IDs before the first of them starts — the split rule binds the
    plan, not just the work.

---

## Progress log

| item | closed | commit | effect | notes |
|---|---|---|---|---|

## Dropped

| item | date | reason | ledger row |
|---|---|---|---|

(End of file)
