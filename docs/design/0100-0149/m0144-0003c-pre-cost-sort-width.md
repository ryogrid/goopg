# M0144-0003c — price the grouping rel's Sort on the narrowed input width

Status: DECISION RECORDED 2026-09-20 (written BEFORE any parity number was
taken — AGENT.md C3); result in §5
Kind: impl
Parent: M0144-0003
Milestone: M0144

## 1. The filed premise is half wrong, and the half that is right is precise

`M0144-0003` item (d) reads: *"`applyUpperNarrowing` (planner.go:190) narrows
Aggregate/Sort input width on the finished tree — post-tournament, so it can
never move a plan choice."*

That is true of `applyUpperNarrowing`, and it is **not** true of goopg's
narrowing generally. Three pre-cost narrowings already exist, all landed under
banner item 4's M0141-S2a-fix1 family:

| site | narrowed pre-cost by |
|---|---|
| ORDERED upper rel's Sort | `narrowOrderedRelWidths` (`upperordered.go:82`, sweep-a) |
| WINDOW's internal sort | `window_sort_narrow.go` (sweep-b) |
| the aggregate's own entry sizing | `aggInputWidth` (`groupingpaths.go:363`, fix1) |

`applyUpperNarrowing` is the *commit* step — it inserts the narrowing Project
— and `aggInputWidth`'s own comment already says the two are deliberately
separate: *"This is a cost-time PREVIEW ONLY: it does not insert a Project …
deliberately separate from whether `narrowAggregateInput` later actually
COMMITS a narrowing Project."* Item (d) read the commit step and concluded the
costing was post-tournament.

## 2. What IS still un-narrowed, and why it matters

Inside `addGroupingPaths` the aggregate and the Sort beneath it are priced from
**different widths**:

```go
inNcols, inAvgVar := aggInputWidth(child, aggNode)     // NARROWED (line 383)
…
sortedInput := sortPathForBounded(seed, …)             // line 466 — FULL width
Cost: costAgg(…, inNcols, inAvgVar)                    // narrowed again
```

`sortPathForBounded` prices through `pathNCols(sub)`/`pathAvgVarBytes(sub)`
(`joinpathsmerge.go:493`), which fall through to the **input** rel's
`NCols`/`AvgVarBytes` — the full row. So a sorted-aggregate candidate is
charged a full-width sort feeding a narrow-width aggregate. The same holds at
the PLAIN arm's presorted sort (line 397).

PG does not split the two. `grouping_planner` builds the narrowed input target
once (`make_group_input_target`, `postgres/src/backend/optimizer/plan/planner.c:1676-1744`),
`set_pathtarget_cost_width` finalises its width
(`postgres/src/backend/optimizer/path/costsize.c:6367`), and `cost_sort` reads
that same `pathtarget->width` (`costsize.c:2328`). One narrowing, every
candidate priced from it.

**Why this is not cosmetic:** `M0141-S2b-10` established that TPC-H Q4's and
Q12's Hashed-vs-Sorted margins sit *inside* `STD_FUZZ_FACTOR` (1.01) — 899.76
and 2181.96 cost units on totals of ~190k and ~327k. A sort priced on a
narrower row is cheaper, and cheaper on exactly the candidate PG elects.

## 3. The decision

**Price every Sort that `addGroupingPaths` builds on the same narrowed width
the aggregate above it is already priced on.**

Mechanically: build one narrowed shallow copy of the seed path carrying
`NCols = inNcols` / `AvgVarBytes = inAvgVar` — the per-path override
`pathNCols`/`pathAvgVarBytes` already prefer (`path.go:819-842`) — and hand
that to both `sortPathForBounded` call sites.

The quantity is not new and not chosen: `inNcols, inAvgVar` is
`aggInputWidth(child, aggNode)`, already computed at line 383, already
justified as B2 absorption of PG's `pathtarget->width` by M0141-S2a-fix1, and
already the number the aggregate is charged on. This change stops two
consumers of the same input from disagreeing about its width; it introduces no
constant (AGENT.md R6) and substitutes no new quantity (C3).

**Both sort sites change together** — the PLAIN arm's presorted sort and the
SORTED arm's group-key sort. Hard-won Rule #2: a green test on one twin proves
nothing about the other, and these two are the same twin pair.

**What is deliberately NOT changed:** the seed path itself is not mutated. The
hashed and index-driven arms read the seed's cost, not its width, so narrowing
in place would be invisible to them — but it would also silently re-point
anything else holding that pointer. A shallow copy keeps the change to the two
consumers that actually price a width.

## 4. A limit found while writing the test, recorded before measuring

`costSortRunWithWidth` only lets the width reach the PRICE through the **spill
branch**: the byte volumes feed `nruns := inputBytes / work_mem`, and the extra
merge charge applies only when `nruns > mergeorder` (`cost_funcs.go`). An
in-memory sort costs the same at any width — correctly, since nothing is
written.

So this change moves a candidate's price only where the full-width row spills
and the narrowed one spills less (or not at all). The first version of the
unit test used a 100k-row fixture, priced both widths identically, and read as
a wiring failure; it is not — the override reaches `pathNCols` exactly as
intended (the test asserts that separately), the fixture simply never reached
the branch. The test now runs at 5M rows, where the branch is live.

This tempers §5's expectation and is recorded here rather than discovered
afterwards: the effect is real but conditional, so a small or zero category
movement is a plausible honest outcome rather than evidence the wiring failed.

## 5. Expected movement

`sort-strategy` and `aggregation-strategy` on the SF0.25 census clusters
(43 and 44 records respectively), and the TPC-H Q4/Q12 Hashed-vs-Sorted
election M0141-S2b-10 measured as fuzz-tied. Measured by
`pg-plan-parity-diff.py` `CATEGORIES-EXCL-MATCH` on both corpora against a
pinned-epoch capture, plus the SF0.25 sweep's plan channel.

## 6. Result

### 6.1 Inert on both corpora — and §4 predicted why

| corpus | outcome |
|---|---|
| TPC-H SF1 parallel | plans capture **byte-identical** to HEAD's, `sha256 75599dae4efbf31c…`, from a **different binary** (`08d905fa9ed819d4` vs `2326ec51aef8b431`) — G3's proof this is inertness, not a re-measured binary |
| TPC-DS SF0.25 | `queries=99 same=99 changed=0`, values `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |

Parity is unchanged on both:

```
before and after: PLAN-PARITY: queries=22 match=2 shapediff=20 unparsed=0 missingnode=0 error=0 timeout=0
                  CATEGORIES-EXCL-MATCH: join-order=15 join-method=10 scan-type=10 parameterisation=7 aggregation-strategy=6 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2
```

This is the outcome §4 named in advance: the width reaches the price only
through the spill branch, and no corpus grouping sort spills at a width where
narrowing changes the run count. The effect is real and conditional; the
condition is not met here.

Note on evidence: the claim is **not** "this path is unreachable" — every
grouping query prices a sort through it — so C6's trace-count-of-zero is not
the right instrument. The claim is "the price does not change", and
byte-identical plan captures on both corpora from a different binary, plus
green values gates, is the evidence for that.

### 6.2 Why it lands anyway

Two readers of one input no longer disagree about its width. The quantity was
already derived, already charged to the aggregate, and already justified as B2
absorption; the sort was simply not reading it. R3 also applies — a
PG-faithful change is not discarded for lack of number movement.

It is additionally a prerequisite: anything that later makes the spill branch
bite on a grouping sort (a larger scale factor, a lower `work_mem`, or the
`GOOPG_PG_SORT_RELATION_BYTES_COST` currency swap being promoted) now finds
the sort priced on the same row as the aggregate above it.

### 6.3 stats epoch / route / seam census / wall time

Epoch `e4a554b2a4cfb710` on both TPC-H arms (pinned seed); TPC-DS is
consecutive same-day sweeps on the gate cluster. Route: PG-shaped search,
default. Seam-decline census: `N/A — this change prices a path; it declines no
seam`. Wall time: no plan changed on either corpus, so nothing is owed; the
sweep's two runtime moves (Q2 2s→6s, Q28 7s→3s) are both queries whose plans
did not change, on a corpus whose total moved +4.9%, i.e. host noise.

### 6.4 Movement

`Movement: none` — match 2 → 2 and every `CATEGORIES-EXCL-MATCH` count
identical on both corpora.

## 7. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `scripts/tpch-spotcheck.sh` | PASS — Q12 rows=2, Q13 rows=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels MATCH on VALUES |
| floor capture + `pg-plan-parity-diff.py` | PASS — TPC-H match 2, TPC-DS SF0.25 match 2 |
| `make ea-ratchet` | `N/A — no estimate, selectivity or statistics code is touched; this re-points an existing width into an existing cost call` |

## 8. Still open

`M0144-0003c` as filed also named the ORDERED and WINDOW sites. Both were
already narrowed pre-cost before this task (§1's table), so nothing is owed
there. What remains un-narrowed anywhere is the input rel's own
`NCols`/`AvgVarBytes`, which stay full-width by design — the narrowing is a
per-consumer absorption, not a rewrite of the rel's sizing.
