# R127 WITHDRAWN — the premise was refuted by R71 six rounds ago, and I did not read it

This scope proposed fixing Q4's `aggregation-strategy` divergence by
making goopg's semi-join cardinality match PG's. **Review BLOCKed it on
nine findings, three independently fatal.** The scope is withdrawn, not
revised. What replaces it is `FRONTIER.md` in this directory.

## What I did wrong

The standing goal says to read `TODO.md` **および同ディレクトリ配下の
ドキュメント** — and the documents under that directory. I read R120–R126
and the recent commits, and scoped a round on Q4 without opening any of
the **eight prior Q4 rounds**: R71, R72, R73, R74, R77, R78, R79, R81.
There are **125 round directories** here. The scope cited none of them.

Everything below was already on disk before I started.

## The three fatal findings

1. **R127's central experiment had already been run, with a negative
   result.** `r71-q4-semi-selectivity/DIAGNOSIS.md` §1 forced Q4's
   semi-join rows via a temp override at `cardinality.go:239`:

   | forced rows | grouping election |
   |---|---|
   | 57,066 (HEAD) | HashAggregate |
   | 30,000 | HashAggregate |
   | 13,490 (PG's serial figure) | HashAggregate |
   | 3,439 (PG's parallel figure) | HashAggregate |

   > "NO election change at any point — rows reprice only. **Rows theory
   > DEAD** (third exit)."

   R127's P2 was not an open hypothesis. It was a refuted one.

2. **The proposed fix cannot reach PG's number anyway.** goopg already
   has `eqJoinSelectivitySemi` (`joinselectivity.go:764`), and
   `r78-semi-selectivity/PROBE.md` measured what wiring it to Q4 would
   give: `frac=0.7825 … wouldBe=44654` — a **1.28x** reduction, not the
   4.2x my §4 table was built on. Same formula as PG's `eqjoinsel_semi`;
   different inputs (goopg's `l_orderkey` n_distinct is fractional
   `-0.1956`, PG's is absolute `347,537`). R78 closed BLOCKED on exactly
   this.

3. **The divergence is not decided where R127 proposed to instrument
   it.** Per `r81-q4-ordered-remeasure/STEP0.md`, both grouping arms are
   accepted; the verdict happens one rel higher in `electOrderedGrouping`
   (`upperorderedgrouping.go:148`):

   | producer | startup | total |
   |---|---|---|
   | upper.ordered.sort (hashed + Sort) | 491312.45 | 491312.46 |
   | upper.ordered.input (sorted, no Sort) | 495535.31 | 495963.37 |

   Both ratios (1.0095 total, 1.0086 startup) sit **inside**
   `stdFuzzFactor = 1.01` (`path.go:27`), so the M0129-S1 tie-break picks
   actual-cheaper = hashed. **PG's same pair is at 1.0118 on startup —
   outside fuzz — so PG decides on the startup arm and sorted wins.** The
   lever is a startup ratio crossing 1.01. My scope never mentioned
   startup, fuzz, or the tie-break.

## Two more worth keeping

- **The causal story was arithmetically impossible.** `costAgg`
  (`cost_funcs.go:425`) gives sorted and hashed the *same* three tail
  terms; they differ by exactly the Sort's cost, so scaling input rows
  scales both sides and cannot flip the sign of the deficit. It can only
  move the fuzz ratio — the mechanism I failed to model.
- **My M0126 citation was stale.** M0126 closed
  `GOOPG_COST_DRIVEN_JOINORDER` MHJ *fusion*, a default-off flag whose
  node no longer exists (`estimateMultiHashJoin` deleted by M0127-P6.2,
  `cardinality.go:243-247`). It says nothing about Q9's divergence in the
  **default** planner. I used it to rule Q9 out of scope — while opening
  the scope by citing the document that names Q9 as "the real lead".

## What the scope got right, per review

`estimateNLIndexJoin` (`cardinality.go:238-240`) really does return
`EstimateRows(j.Outer)` verbatim for every join type including SEMI, so
57,057 == the Seq Scan's rows **by construction**, not coincidence. The
"one decision" claim holds (`pg-plan-parity-diff.py:679-702`, `:963-965`,
`:701` — electing the no-Sort sorted arm clears both labels). Shape-only
verdicts confirmed (N1, `:44-46`: estimates "never influence the
verdict"). And the corpus ranking is correct: `aggregation-strategy=10`,
`sort-strategy=9`, `match=6`, Q4 the only 2-category query, Q9 the only
1-category one.

## The rule I should have followed, written down

Before scoping round N in this directory: **`ls` the directory and grep
the prior rounds for the target query and the target mechanism.** 125
rounds is far past the point where recent-commit context is sufficient.
Three of my hypotheses in this session were refuted by measurement; this
fourth one had been refuted in writing, on disk, six rounds earlier.
