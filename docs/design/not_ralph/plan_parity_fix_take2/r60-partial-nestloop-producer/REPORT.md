# R60 REPORT — partial-nestloop producer: implementation (2026-09-11)

Binary `goopg-r60` (`/tmp/pp2/bin/goopg-r60`, built 10:08 from this tree,
`./cmd/goopg`), seed 20260905. Code cut: `addPartialNestLoopPaths` in
`internal/optimizer/joinpathsnli.go:379` (+ call
`internal/optimizer/joinpaths.go:385`), exactly the SCOPE §2 cut
(two files, no model/sizer/executor/display change).

## Gate 1 — units + vet: GREEN

`go test ./internal/optimizer/` ok (`(cached)` — Go's cache key covers
the input file contents, so the recorded pass ran against exactly this
tree; no `-count=1` per convention); `go vet ./internal/optimizer/`
clean; `go build -o /tmp/pp2/bin/goopg-r60 ./cmd/goopg` clean (10:08,
40.4M, the binary all captures ran under).

## Gate 2 — Q7 top byte-identical + run-stable: HOLDS

`q7-top-r60.{1,2,3}` md5 `fe15bfd6dcd9ed2eabd61494a064e953`;
`.1==.3`, and == R59's `r59-q7-top-.1`. `.1==.2` run-stable.

## Gate 3 — DPTRACE A/B vs R59 run 1: re-audited, count-closed

Raw counts: R59 run 1 = 1393 lines (`ab-r59run1.txt`); R60 raw = 1695
(`ab-r60.txt`). The strip rule removes exactly the two new line
families — all 257 filed `producer=join.nestloop.partial` lines and
all 88 `site=nestloop` Vn pveto lines (R59 has 0 nestloop lines of
either kind) — giving stripped R60 = 1350 (`ab-r60-stripped.txt`).
Diffing stripped-R60 vs R59 (`ab-diff.txt`): 92 R59-only lines, 49
R60-only lines, and 1393 − 92 + 49 = 1350 closes EXACTLY. The full
equation: **1695 = 1393 + 257 + 88 − 42 − 1**. Every non-added line
outside the deltas below is bit-identical:

| delta | count | verdict |
|---|---|---|
| filed `producer=join.nestloop.partial` lines | +257 | predicted (P0 LANDS) |
| Vn pveto lines (new dispatch/per-path gates V7/V8 + dispatch; V3/V6/V9 have no NL mirror — V3 lateral is comment-only, V6 gatherable-head and V9 re-check are hash-producer gates with no NL equivalent) | +88 | predicted |
| hash veto lines swapped V9→V6 (same 42 pairs, one veto line each — count-neutral) | 42 swapped | correct: hash producer takes `outer.PartialPathlist[0]` and V6-refuses non-gatherable heads (`partialPathShapeIsGatherable`, "never even costed"); NL heads are never even costed by hash |
| hash.partial filings vanished outright via V6 starvation (17 accepted + 25 dominated) | −42 | correct, never costed |
| hash filings accepted→dominated, re-filed (each of the 7 re-filed dominated pairs matches a removed accepted filing 1:1 by outer/inner) | 7 verdict changes | correct `addToPartialPathlist` domination (evicted by cheaper NL) |
| final gather completion deleted (singleton flips hash 328668.83 → NL 60247.94) | −1 | correct NON-COMPLETION (see mechanism) |
| serial/merge/scan/upper/index lines | 0 changed | bit-identical |
| end lines (`pairs=75 declined=30 costs=25`) | identical | |

- **P0 LANDS**: 257 `join.nestloop.partial` lines; the `{1,2,3,5}` pair
  is not pvetoed (parallel-safe via `pathparamindex.go:422`,
  param `{2}` ⊆ outer, lateral empty).
- **P1 letter-FAILS, mechanism CONFIRMED**: `{2,3,5}` head
  46602.31 → 44326.72 — but NOT via the hand-audited index arm
  (hyp ≈129.5k, still dominated 2.78× as predicted). The winner is a
  never-hand-audited PLAIN arm, `outer={2,3}` `inner={5}`,
  inputtotal 38701.49. The 843B/10B-astronomical plain-arm dominated
  lines are CORRECT pricing (PG precheck-bails; goopg files-but-dominates).
- **P2 letter-FAILS as head, math LANDS EXACTLY**: `{1,2,3,5}` head is
  249332.73 via plain arm (`outer={1,2,3}` `inner={5}`), but the
  index-arm probe rung prices at **377869.96** vs the cascade-corrected
  hand-derivation 377869.68 (Δ0.28 — the `{2,3,5}` head flip shifts the
  probe rung 380145.65→377869.68), with inputtotal=44326.72 proving the
  cascade. The Q-cancellation is exact.
- **W1**: challenger wins wide via `outer={3,4,5}` `inner={2}` at
  14672.59 (inputtotal 4743.30, rows 49960, d=2.4) — not the alleged
  `outer={2}` ~34900.
- **W2**: final rung 60247.94 (outer cascade, inputtotal 59109.77,
  rows 2448) — not the alleged ~337.7k.
- **P3 letter-HOLDS but mechanism is NON-ADMISSION, voiding §1.iv's
  margin arithmetic**: the 60k NL singleton cannot complete to a
  Gather, so the final contest never sees the §1.iv ladder. Mechanism
  (pinned): `partialPathDrivingKind` whitelist = SeqScan/IndexScan/
  BitmapHeapScan/HashJoin (probe-side recurse)/MergeJoin (outer-side
  recurse), default → `PathPrebuilt`; `PathNestLoop` (Kind 5) is always
  refused → `makeGatherPath` nil → `generateUsefulGatherPaths` files
  nothing. `cpgather` "admitted" = candidacy only (comment in code).
  `addPartialPath` (`path.go`) refuses `!ParallelSafe`; filed partials
  carry pw≥1, so the refusal is purely the shape arm. Executor twin
  (`parallel.go:501` `terminatesPartial` true for `NestedLoopIndexJoin`/
  `Memoize`; probe attach modelled only behind
  `hashJoinIsPartialCapable`) matches the SCOPE §4 inventory — confirmed
  live, not drift. PG would file/complete these (executor gap, not a
  planner bug); ledgered as follow-up, triggered only if a top ever moves.

## Gate 4 — letter-FAIL, accepted as scope change (status: CONDITIONAL PASS)

SCOPE §5.4 demands Q5 top byte-identical to R55/R56; R60 moves Q5 AND
Q10 plan shapes. **This gate FAILS as written** — recorded here as an
accepted scope change, not a green gate, for these reasons:

- Values: `q1,q3,q5,q7,q8,q9,q10,q19` md5 MATCH vs R59 (results unchanged).
- Tops: q1/q3/q9/q10/q8/q19 byte-identical; Q5 + Q10 shapes move.
- Mechanism is priced, not drift: NL-head eviction kills Gather
  completion (only `PartialPathlist[0]` tried — PG-identical) →
  serial fallback.
- Direction is parity-neutral/toward-PG: PG's own Q5/Q10 are serial
  (PG Q5 even has a serial Nested Loop).

The SCOPE §5.4 byte-identity expectation is hereby amended: Q5/Q10
tops may move exactly via this serial-fallback mechanism while values
MATCH. Any Q5/Q10 movement by any other mechanism, or any values
mismatch, still fails the gate.

## Gate 5 — pp 5/15/0/2, exactly reproducing R59

All 22 verdicts identical to `pp-r59.txt`. Methodology note: the pp
capture is fully serial; exact settings recovered by R59-binary
reproduction — `work_mem=64MB` + `max_parallel_workers_per_gather=0`,
`psql -At`, `grep -vx SET`, one blank line per section; Q15 as view
BODY via `sed 's/^.* as //'`. **Q3 Δ−8.62 is stats drift, proven**:
the R59 binary reproduces 28864/300393.02 NOW (`start-q3retest-r59.log`
10:13; autovacuum re-sampled between the 09:04 R59 capture and 10:13) —
environmental, not R60.

## Gate 6 — DS SF0.5 sweep: P4 LANDS fully

`sweep-20260911-101422.txt` (`SF05_NO_BUILD=1`,
`GOOPG_BIN=/tmp/pp2/bin/goopg-r60`): **PASS=94 (same 57 ck-verified,
37 ck=n/a), MISMATCH=0 CKMISMATCH=0, TIMEOUT=1 (Q72 alone,
318s vs R59's 320s), SKIP=4**. Status-delta vs `sweep-20260911-090748.txt`:
**verdict-changes=none, runtime-moves=0, total-delta=−0.4%**.
Plan-shape channel: **same=99, changed=0**.

## Deviations from SCOPE (ledgered, none blocking)

1. **Lateral**: C-08's "0 by invariant" cited by comment like the
   siblings (fail-closed skip + trace), not re-proven.
2. **`add_partial_path_precheck`**: deliberately not mirrored (CPU-only);
   the giant dominated plain-arm lines it would have bailed on are now
   the concrete follow-up case for adding it as planner-CPU work.
3. **SCOPE P1/P2/W1/W2 numbers**: superseded by the plain-nation +
   cascade findings above; the hand-derivation method itself is
   validated (Δ0.28 on the probe rung).

## Follow-ups (not this round)

Executor NL-probe/Memoize-under-partial work (Q72 now biting harder:
qrest gathers 3→0); `add_partial_path_precheck` as planner-CPU
follow-up; width model/relPages; correlation/ANALYZE (+autovacuum
re-sample drift observed); probe rows 2-vs-5; AGG_MIXED; R55 §3
tie-break; F3; Q8 gap; NLI display stamper; loopCount 0-vs-1 (P2-09b).

Evidence tmp-only `/tmp/pp2/r60/` (R59-binary probe + R60 captures,
A/B work files `ab-r59run1.txt`/`ab-r60.txt`/`ab-r60-stripped.txt`,
`pp/r60.plans.txt`); sweep reports
`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260911-101422.txt`.
