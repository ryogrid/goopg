# R62 SCOPE — Yao through parameterized probe: decline per-scan evidence (2026-09-11)

Follows R61 LANDED (`r61-groupcount-search-input/REPORT.md`, commit
`09888fe69`): Q11 groups 80 (input fix), display 26 (80×1/3); PG wants
32000 → 10667. The remaining 80-vs-32000 gap is R61's ledgered #2, and the
R61 re-triage defers (a) Materialize (unchanged producer gap) and (b) Q4
(NOT a rows defect — goopg's Sort→HashAgg rows=5 is bit-identical pre/post
R61 and cheaper than PG's GroupAgg←Sort; it needs cost-model framing, not
an estimator round). #2 is the flagship: it completes Q11 to PG-exact.

## 1. Probe verdicts (live instrumented run, SF1 clone :5533, seed 20260905)

Temporary `GOOPG_R62DBG` prints on `estimateNumGroups`' Yao site +
`relFilteredRowsWalk`'s `n == rel` arm (FULLY reverted, tree clean).
Bin `/tmp/pp2/bin/goopg-r62probe` (instrumented, superseded); log
`/tmp/pp2/r62probe-start.log` (tmp-only).

### (i) The hit IS the parameterized probe — detection is a type+field check

```
R62DBG hit type=*optimizer.IndexScan rows=80 Key=*optimizer.OuterColumnRef Keys=0   (×8, identical)
```

`resolveBaseColumn` returns the inner probe scan itself; its rows (80) are
per-scan, and its `Key` is DIRECTLY an `*OuterColumnRef` (no wrapper).
Measurement, not hypothesis: a parameterized probe is recognised by
`*IndexScan` with an `OuterColumnRef` inside `Key`/`Keys` (`plan.go:778-779`
fields; walker knows the leaf type — `exprwalk.go:119`).

### (ii) The crush, exactly chained

```
R62DBG yao tuples=800000 filtered=80 pre=201356 post=80   (×8, identical)
```

`examineGroupVar` is fine (`rawRows`=800000 unfiltered partsupp, nd=201356
per R61). The walk seals the per-probe 80 at the `n == rel` arm
(`IndexScan` 80 = 1 row per call site × 80 outer calls is the M0127-P5.9
semantic; the stale "1 row" comment stays ledgered), `joinSide` propagates
it, and Yao computes 201356×(1−((800000−80)/800000)^(800000/201356)) ≈ 80.
All 8 calls (both consumers × live agg sites) agree — one mechanism.

### (iii) PG oracle semantics: the unparameterized base rel

`estimate_num_groups` (selfuncs.c) resolves grouping vars to the
UNPARAMETERIZED baserel (`find_base_rel` by varno): partsupp is unfiltered
at base (tuples=rows=800000) → NO Yao discount → 203361 → clamp
min(203361, input 32000) = 32000 (R61 §1.iii verified 32000/10667 live).
goopg's estimate tree carries no unparameterized rel — the scan node IS the
probe — so the faithful cut is to DECLINE the per-probe evidence, not to
invent base rows. Stamp-style alternatives are refuted by construction:
there is no second source holding 800000-with-restriction; the base count
lives only in `rawRows` (already used as N).

### (iv) The R61 gate pattern re-applies, unchanged

Same accessor family (node type + field check), same fail-closed default
(non-probe scans keep today's behavior bit-identically), both consumers
inherit (sole `relFilteredRows` caller is the Yao site — verified by grep:
def :1462 + call :1318 only).

## 2. The cut

ONE arm in `relFilteredRowsWalk`'s `n == rel` (`cardinality.go:1471`): when
the hit node is `*IndexScan` with an `OuterColumnRef` inside `Key`/`Keys`
( containment via the existing `exprChildSlots` walker, `exprwalk.go:109`;
bare-Key fast path first), return `(0, false, false)` — found=false, i.e.
decline per-probe restriction evidence. Yao math, clamps, both group-count
consumers, display code, executor: UNCHANGED. The Yao term then fires only
for genuine base-level restriction evidence, exactly PG's shape.

Deliberate non-mirrors (not drift): no Yao formula change; no
`estimateNLIndexJoin` change (its outer-only rows stay wrong-but-unreached
here); no LowKey/HighKey/SAOPKeys outer-ref detection (ledgered — extend
the same predicate if a live case appears in gates); no display change
(NUMBERS move only); no GUC.

Known corner R62-#1 (ledgered, gates adjudicate): a rel that is BOTH
locally restricted AND probe-parameterized (Filter-above-probe, or
multi-conjunct probe key) now skips Yao ENTIRELY, where PG would still
discount for the local restriction — over-count toward the tuples clamp.
Q11/Q5 carry no such shape (probe inners unrestricted); no live case is
known. Any gate movement consistent with over-count triggers re-audit, not
celebration.

## 3. Falsifiable predictions

| # | Claim | Mechanism (§1) | Verdict on miss |
|---|-------|----------------|-----------------|
| P0 | decline arm fires for Q11 (all 8 Yao calls skip) | §1.i detection on the measured hit shape | display unmoved → STOP, re-audit |
| P1 | Q11 groups 80→EXACTLY 32000 (search rel) | reldistinct stays 201356 (no discount) → closing clamp min(201356, 32000); inputRows=32000 from R61 | ≠32000 → re-audit (nd/rawRows moved or clamp path differs) |
| P2 | display HashAggregate 32000→10666, Sort passthrough, Filter sel still 1/3 | 32000×(1/3)=10666.67→**10666 by truncation** (R61: 26.67→26). **Predict 10666, NOT PG's 10667** — the ±1 is truncation-vs-round, cosmetic, own scope (ledgered) | ≠10666 → re-audit; =10667 → bonus, still record rounding path |
| P3 | values md5 MATCH all TPC-H queries; **Q5 groups STAY 25 (immobile)** | Q5 groups by nation.n_name; nation is the OUTER seq scan, not a probe — no hit, no movement. Sharp R61-vs-R62 discriminator | ANY values mismatch → STOP; Q5 moves → re-audit (unexpected probe hit — find it) |
| P4 | pp stays 5/15/0/2 modulo Q11's 26→10666 number-move (shapes identical); DS sweep PASS=94 + Q72 TIMEOUT; the 28-plan set moves numbers-only toward-oracle (predict Q39 body agg 48→~7823; no NEW shape flip vs R61's 7) | planner-only evidence-decline; executor untouched | new MISMATCH/CKMISMATCH/TIMEOUT, or any unadjudicated flip → explain-or-stop |

WATCH (direction-checked, not gated): DS-Q3 (R61 1→15; lifts further iff
its group rel is probe-restricted); Q4/Q10 immobile expected (Q4 verified
bit-identical r59==r61).

Re-audit rule (R59/R60/R61): any P-miss triggers DPTRACE A/B against the
pre-R62 binary (non-Yao lines bit-identical), not celebration.

## 4. Sibling audit + executor inventory (why the cut is planner-only)

- **Sole consumer.** `relFilteredRows` has exactly one caller (the Yao site
  :1318). No other call site can change behavior.
- **Other `estimateNumGroups` callers** (set-op dedup, grouping-sets, CTE
  synthesis, Distinct): they inherit the decline only if their child tree
  contains a parameterized probe — `OuterColumnRef` exists ONLY under
  parameterized joins, so non-join callers are unaffected by construction;
  join-bearing CTE bodies get parity-direction lifts (R61 showed no fire;
  gate 3 re-checks).
- **Partial tournament** inherits via `sizeGroupingRelFromAgg` (R61 pattern);
  per-worker rows untouched (different quantity).
- **Executor OUT**: estimates never reach execution; P3 proves it.
- **No GUC, no knob, no display-code change.**

## 5. Gates (implementation round)

1. `go test ./internal/optimizer/` green (no `-count=1`); `go vet` clean.
2. Q11: groups EXACTLY 32000, display 10666 (truncation recorded); values
   md5 MATCH vs R61 (8+q11); Q5 groups pinned at 25 (immobility asserted).
3. DPTRACE A/B vs R61 binary: only Yao-input lines move (grouped.Rows
   80→32000, display 26→10666); joins bit-identical; CTE cases identical.
4. `scripts/pg-plan-parity-diff.py`: 5/15/0/2; Q11 move recorded; Q4/Q5/Q10
   shapes identical.
5. DS SF0.5 sweep foreground, fresh build, private lane: PASS=94 (same set)
   + Q72 TIMEOUT alone; 28-plan set re-adjudicated per P4.
6. REPORT.md with the A/B numbers, then review, then commit + push.

## 6. Ledgered follow-ups (not this round)

- R62-#1 corner (restricted + parameterized same rel), rounding parity
  (10666 vs 10667), LowKey/HighKey/SAOPKeys probe detection.
- Unchanged queue: (a) Materialize producer, (b) Q4 (cost-model framing —
  goopg shape cheaper; NOT an estimator round), M1-display, NLI staleness
  comment, R61 #4 (CTE-body tagging), R61 #5 (executor watch).

Evidence tmp-only `/tmp/pp2/` (`r62probe-start.log` G-verdict lines,
`r62probe-q11.plan/.err`, superseded `bin/goopg-r62probe`); tree holds NO
probe code (reverted, `git status` clean for `internal/`).
