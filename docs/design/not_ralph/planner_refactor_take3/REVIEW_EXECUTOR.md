# REVIEW_EXECUTOR — take3 executor bundle review record (2026-09-03)

Two independent reviewers audited the executor bundle before it was
recorded. Each worked from the source rather than from the documents:
the executor-PG reviewer against `./postgres/` read-only (30+ symbols
via `global -x` + Read), the executor-goopg reviewer against the Go
tree via Grep/Read (~30 claims spot-checked at pin `adf2d1e13` vs tip
`7616ac964`). Their raw reports are kept as dotfiles beside this one —
`.review-executor-pg.md`, `.review-executor-goopg.md` — because a
summary of a review is not a review. Every fix below was re-verified
against the documents by grep/read in this pass (docs edits only, no
code, no servers, no tests). A fix listed here cites the line that
carries it; anything not found is listed under Unresolved instead.

## Coverage

| reviewer | scope | verdict | findings |
|---|---|---|---|
| executor-PG accuracy | 10, 12 | APPROVE-WITH-CHANGES | 2 major · 2 moderate · 4 minor · 3 info (11) |
| executor-goopg accuracy | 11, 13, TODO_EXECUTOR, README_EXECUTOR | REVISE | 1 HIGH · 3 MED · 4 LOW (8) |

## Verification method (this pass)

Read `.review-executor-pg.md` (241 lines) and
`.review-executor-goopg.md` (165 lines) fully, then re-read `REVIEW.md`
(147 lines) for format. Grep-verified six load-bearing fixes before
writing: AGG_MIXED grouping-sets + `hash_agg_check_limits` in 10 §8;
SharedTuplestore in the 10 §6 tail with the GatherMerge binary-heap
rewrite; EX3-01 re-scope in 13 §5 + TODO_EXECUTOR.md; 11 §17 row-8
LANDED mark with the `1d6b1e396` ancestry; EX2-02b split into
02b + 02c; E1 per-query baselines scope in EX0-06. Remaining
moderate/minor/low fixes spot-checked the same way; line pins below
are the lines that carry each fix, not the reviewer's proposal.

## executor-PG — all 11 applied

| # | Sev | File/section | Resolution | Evidence |
|---|---|---|---|---|
| F1 | MAJOR | 10 §8, AGG_MIXED gloss | rewritten as grouping-sets mixing (phase-0 hashed sets + phases 1..n); separate `hash_agg_check_limits` partitioned-tape spill row; §16 hashed-agg row corrected | 10:318-324, 10:342-348, 10:642 |
| F2 | MAJOR | 10 §6 tail + §10, SharedTuplestore | moved to parallel-hash discussion; GatherMerge described as tuple queues + binary heap (`heap_compare_slots`) | 10:260-270, 10:413-421, 10:692-697 |
| F3 | MODERATE | 10 §11, Memoize consumer | "`Memoize` overflow" deleted; consumers are Material, WindowAgg, SetOp/RecursiveUnion, CTEs, portals | 10:444-447 |
| F4 | MODERATE | 10 §6, skew keys | now planner-supplied MCV keys via `skewTable`, pinned by `ExecHashSkewTableInsert`; executor pins, not detects | 10:250-260, 10:639 |
| F5 | MINOR | 10 §7, Memoize bypass | now whole-scan bypass: uncreatable/unstorable entries fall into `MEMO_CACHE_BYPASS_MODE` to end of scan | 10:294-300, 10:683-684 |
| F6 | MINOR | 10 §9, bound pushdown | now the full chain `ExecInitLimit` → `ExecSetTupleBound` (`execProcnode.c:848`, `SortState.bounded/bound`) → `tuplesort_set_bound` | 10:377-384 |
| F7 | MINOR | 10 §17, scan count | now "six scan shapes", Tid/TidRangeScan as one line item as §4 does | 10:671-675 |
| F8 | MINOR | 10 §4, remaining scans | appended `NamedTuplestoreScan`, `TableFuncScan`, `WorkTableScan`/`RecursiveUnion` path, qualified with "including" | 10:175-184 |
| F9 | INFO | 12 §1.2, take6 baseline | take6 row now names "(from the 6.55 s take6 baseline, row 4)"; other chain factors re-divide cleanly | 12:64 |
| F10 | INFO | 12 §2, G-EX4 ranking | G-EX4 rank marked provisional pending the EX3 re-measurement the doc already requires; no numbers changed | 12:97 |
| F11 | INFO | 12 §3 + §13, width range | now "14–39× cross-ratio; level-paired ~39–51×" with all four paired ratios; ledger left as the faithful source | 12:147-149, 12:295-299 |

## executor-goopg — all 8 applied in docs

| # | Sev | File/section | Resolution | Evidence |
|---|---|---|---|---|
| F1 | HIGH | 11 §11 + §17 row 8; 12 §14; 13 §5 EX3-01 | row 8 re-marked LANDED (elimination `1d6b1e396`, cached handle, no per-row Stack walk); 12 §14 "per-row stack walk" deleted; EX3-01 re-scoped to verify-and-close (re-measure Q4/Q7/Q13-class, reader-path audit) | 11:313-330, 11:487; 12:263-299; 13:239-248; TODO:197-206 |
| F2 | MED | TODO E1 + EX0-06 | EX0-06 extended to record per-query timing+alloc baselines on both suites; E1 denominator now "Q6 chain + witness shapes timed per query; suite TOTALs as backstop" | TODO:91-98, TODO:326 |
| F3 | MED | TODO EX4-04; 13 §6 + §8.5 | blocker re-attributed to executor-owned EX5-01 (`buildRec` Gather arm); plan-shape pin kept as interface, not work item | 13:298-305, 13:363-367; TODO:252-257, TODO:275-279 |
| F4 | MED | TODO EX2-02b; 13 §4 | 02b split into 02b (agg input) + 02c (gather transfer, = 13 EX2-04); one-seam-family-per-commit restored | TODO:161-171; 13:210-223 |
| F5 | LOW | 11 §5, `buildKeyOfRow` | cite corrected to `join_batch.go:725`; sibling geometry cites verified exact | 11:180-181 |
| F6 | LOW | TODO EX3-07; 13 §8.7 | tiebreaker added: executor publishes the presorted-prefix input contract first (EX3-04-adjacent work or spike), or P4-05 prototypes behind a flag | 13:371-378; TODO:226-229 |
| F7 | LOW | 11 line pins | refreshed to symbols-first with lines parenthetical (`BuildFast` func `:633`, `schemaIdx :534`, `evalFastExpr :288`); HEAD pin kept | 11:34, 11:39, 11:343-344 |
| F8 | LOW | 11 §14 + §18 item 2 | now "zero production callers (test-only otherwise: 7 sites in `aio/read_stream_test.go`)" | 11:407-408, 11:522-527 |

## What the reviewers judged sound

Discriminating praise only, since the rest is noise:

- Doc 10's citation discipline: every sampled `global -x` symbol
  resolves at the cited file:line (dispatch, scans, joins, hash,
  memoize, agg, sort, gather, tuplestore, expr, mcxt); no invented
  symbols in either doc; §17's honest failure list corroborated.
- Doc 12 carries every sampled number faithfully from 11 §17 / 07
  with no inflation, honors STALE/LANDED, and concedes where PG does
  the same thing (e.g. symmetric probe-side non-batching).
- No query-specific forcing in the goopg bundle: EX-P2 holds;
  Q6/Q9/Q4/Q7/Q13/q16/TOAST-DS mentions are witness-shape citations
  with operator-slice gates; EX3-01 explicitly refuses Q14/Q3/Q10.
- STALE Q14/Q3/Q10 discipline coherent across 11 row 16, 12 §§11/13,
  and TODO EX3-01 exclusion; README_EXECUTOR map accurate (G-EX1…8,
  §17+§18, EX0→EX5, E1–E6 verified; this file no longer "Planned").
- Phases executable in order EX0→EX5; all 39 TODO checkboxes carry
  unique IDs with `design: 13 §` + `gate:` pointers; landed `[x]`
  items carry evidence pointers; bars E2–E5 measurable, E6 excluded.
- Doc 11's self-correction on the spill story is complete: §11 names
  the superseded projection as history, §17 row 8 carries the LANDED
  mark with the residual, 12 §6 refuses to re-price the Stack walk,
  and 13 §5 prices only the remaining encode/I/O + discipline work.
- Doc 13's sequencing bars are now executable as written: EX-P3
  one-variable-per-commit owns the EX2-02 split, EX-P5 owns the
  no-plan-movement pin, and §8 names the real precondition
  (EX5-01, P4-01, P2-11b) on every blocked item.

## Unresolved / deferred (not claimed above)

- D1 — EX5-02 watch (goopg F4 note): barrier discipline + stall
  measurement + worker-count scaling still ship as one item
  (13:326-330; TODO:280-284). Not a numbered finding; split at
  execution time if the first A/B shows independent variables.
- D2 — Line-pin rot (goopg F7 residual): tree tip has relocated past
  pin `adf2d1e13`; symbols-first cites survive moves but bare ranges
  will drift again. Next code-touching commit should re-check pins.
- D3 — 12 §14 residual wording (11 §14 analogue): "zero callers
  outside its own file" style phrasing may recur; the production vs
  test qualifier is the rule for future AIO-class claims.

## Final verdict

**ACCEPT the executor bundle as the execution baseline.** Both
reviewers' conditions are met in the documents: executor-PG 11/11,
executor-goopg 8/8, each cited to the line above. D1–D3 are
follow-ups, not blockers — none misstates a mechanism, and each
names its file and section. Next docs pass should clear D1; code
passes own any executor behaviour change. EX0 instruments stay the
gate for every later claim, and any re-measurement that revives a
superseded projection must land as a new ledger row rather than a
silent edit to the closed numbers above.
