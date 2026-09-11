# R60 SCOPE — partial-nestloop producer: the last missing join arm (2026-09-11)

Follows R59 LANDED (`r59-index-probe-loopcount/REPORT.md`): the probe
re-pricing moved Q3/Q7/Q8/Q9 to serial-NLI shapes and exposed the one
producer goopg has never had — a partial nested loop. Census (R58 SCOPE
§probe-E, re-verified this round against the R59 tree): zero references
to any partial-NL producer in `internal/optimizer/`; the partial family
is hash (`addPartialHashJoinPath`) + merge
(`addPartialMergeJoinPath`/`tryPartialMergeJoinPath`) only.
`cpgather` on the fresh R59 Q7 capture (`/tmp/pp2/r60/`,
`BIN=goopg-r59`, seed 20260905, TRACE+GATHER_TOP, Q7 top
byte-identical to R59 and run-stable run1==run2): every joinrel carries
partials=1 (hash/merge only), the final rel admits its Gather over
cheapest-final-partial 328668.83 → gather 330256.03, dominated by the
serial NL 134639.71. PG's live Q7 (stored pinned-GUC capture) wins
through a partial ladder whose outer IS an index-NL chain; goopg cannot
even offer that shape. This round lands the producer. Read-only probes
so far (oracle source + goopg source + R60 capture); implementation runs
the §5 gates.

Relid map (Q7): 0=supplier, 1=lineitem, 2=orders, 3=customer, 4=n1, 5=n2.
Divisors are per-rel (traced rows ratios): d=2.4 where the outer partial
has 2 workers (`{3,5}`: serial rows 150000 → partial 62500), d=4.0 where
it has 4 (`{2,3,5}`: 1500000 → 375000; `{1,2,3,5}`: 1837006 → 459252).

## 1. Probe G verdicts

### (i) The gap is a producer, not a price

Every number the producer needs already exists in the trace. The serial
NLI arms price the exact pairs a partial-NL would join; the partial
hashes price the exact outers it would ride. No new cost model, no new
sizer, no executor work (see §4): the cut is offer-and-file.

### (ii) `{2,3,5}`: hypothetical partial-NL LOSES (margin 83k, hand-callable)

Serial NLI, PG-order (`outer={3,5} inner={2}`, rows=1500000):

```
producer=nestloop.index relids={2,3,5} total=305492.54 inputtotal=7386.06
```

Serial outer `{3,5}` (hash, rows=150000) = 7386.06, so the orders-probe
rescan+qual = 298106.48 over 150000 outer rows = **1.9873765/probe**.
Partial outer `{3,5}` (hash.partial, rows=62500, d=2.4) = 5307.94.
Hypothetical partial-NL:

```
5307.94 + 62500 × 1.9873765 + startup(~1.94) ≈ 129520.91  (rows 625000, workers 2)
```

vs partial-hash head 46602.31 (`outer={2} inner={3,5}`, rows 375000,
accepted). Ratio 2.78×. The producer fires here and loses honestly.

### (iii) `{1,2,3,5}`: hypothetical partial-NL WINS the partial ladder (margin 151k, hand-callable)

Serial NLI, PG-order (`outer={2,3,5} inner={1}`, rows=1837006):

```
producer=nestloop.index relids={1,2,3,5} total=1407492.91 inputtotal=73321.06
```

Serial outer `{2,3,5}` (hash, rows=1500000) = 73321.06, so the
lineitem-probe rescan+qual = 1334171.85 over 1500000 outer rows. The
probe/qual SPLIT is not needed: whatever fraction is per-probe rescans
with per-worker rows (÷4) and whatever is residual qual rides per-worker
rows (÷4) — the Q terms cancel exactly:

```
46602.31 + 1334171.85/4 + inner-startup(~0.38) ≈ 380145.65  (rows 459252, workers 4)
```

Partial outer `{2,3,5}` (hash.partial, rows=375000, d=4) = 46602.31.
Rows check: 1837006/4 = 459251.5 → 459252, exactly the traced
partial-hash rows at this relid — the one-divisor rule is consistent.
vs partial-hash head 531599.26 (`outer={1,2} inner={3,5}`): **Δ
−151454, the new head.** (Second outer, partial-NL `{2,3,5}`-hyp as
outer: 281601.19-dominated outer + 625000×0.8894 ≈ 837500, dominated —
the loop tries it, addPartialPath drops it.)

### (iv) Q7 top is immobile anyway (margin 195616 > max single-rung shed 151454)

Current final contest (traced):

```
producer=upper.ordered.sort relids=- total=134639.71 verdict=accepted        (serial NL)
producer=gather relids={0,1,2,3,4,5} total=330256.03 inputtotal=328668.83 verdict=dominated
```

Cheapest final partial 328668.83 = partial-hash `outer={1,2,3,4,5}`
`inner={0}` (inputtotal 327900.82). Serial wins by **195616.32**.
The §1.iii head-flip sheds at most 151454 along any chain (a DP chain
visits each joinrel once — it can collect the `{1,2,3,5}` delta at most
once), and full propagation still leaves serial ahead by ~44k. Two
rungs are too close to hand-call and go to the A/B (§3 WATCH): the
`{2,3,4,5}` head 35412.90 (`outer={2}`, challenger ≈ outer-`{2}`-partial
+ customer-probe rescan, margin alleged ~500) and the final-rel rung
`outer={1,2,3,4,5}`-head × `inner={0}`-supplier-probe (≈337.7k vs
328668.83, margin alleged ~9k with Memoize variants in play). Either
outcome is consistent with an immobile top; a moved top FAILS §3 P3 and
triggers the re-audit rule below.

WHY BUILD A PRODUCER THAT LOSES: parity-of-mechanism, not
parity-of-shape. PG's Q7 must win through its partial ladder (its serial
contest resolves differently); goopg's serial NL (134.6k) beats its
gather (330k), so Q7 is decided serially here. After R60 goopg offers
the shape PG offers, prices it with the same arithmetic, and beats it
with a cheaper serial — the honest defeat, priced the same way PG would
price it. If the margin ever flips, §4 names the executor work it would
take; until then the executor is provably out of the contest.

Re-audit rule (carried from R59 §1.iii): any §3 miss triggers re-audit
by DPTRACE A/B (non-partial-NL lines bit-identical, only
`producer=join.nestloop.partial` lines added), not celebration.

## 2. The cut

ONE new function + ONE call site. No cost-model change, no sizer
change, no executor change, no display change.

### Cut site 1: `addPartialNestLoopPaths` in `internal/optimizer/joinpathsnli.go`

The NLI arm's sibling: it shares `nestloopResidualClauses` (same
restrict-drop — movability against `innerAndOuter` is unchanged when
the outer is partial) and the bare+`getMemoizePath` pair expansion
(`getMemoizePath(s, outer, o, i, cp)` already takes the outer *path*,
so a partial outer fits the signature untouched). PG oracle:
`consider_parallel_nestloop` + `try_partial_nestloop_path`
(joinpath.c:2107-2214, :945-1010).

- **V0 (mode)**: `gatherPathsMode == gatherPathsOff` → return. Same
  reader-only justification as the partial-hash producer
  (`joinpathsparallel.go:82-91`): the only readers of a partial path
  are `generateUsefulGatherPaths` and the next level's own partial
  producer.
- **Dispatch** (PG :2022-2031, verbatim set): `joinrel.ConsiderParallel
  && s.parallelModeOK`; jointype ∉ {FULL, RIGHT, RIGHT_ANTI}
  (UNIQUE_OUTER vacuous — goopg has no UNIQUE jointypes at all, verified
  by zero refs); `len(outer.PartialPathlist) > 0`; lateral empty
  (fail-closed skip + trace — C-08's "0 by invariant" is cited, not
  trusted: a future LATERAL producer must trip the trace, not silently
  pass).
- **Outer loop: ALL outer partials**, not the head. PG's `foreach` over
  `partial_pathlist` exists because orderings differ; goopg NL paths
  carry no pathkeys today (§2.iii), but head-only would bake today's
  keylessness into the producer's shape — and rows-per-worker differ by
  outer (d=2.4 vs 4.0 above), so a dearer outer with fewer rows is not
  strictly dominated. Lists are tiny (`partials=1` almost everywhere).
  Skip `o.RequiredOuter != 0` (PG *asserts* the outer unparameterized;
  fail-closed skip + pveto, per `addPartialPath`'s convention,
  `path.go:946`).
- **Inner loop: `inner.CheapestParameterized` as-is.** PG iterates
  `cheapest_parameterized_paths` INCLUDING the prepended cheapest
  unparameterized member — goopg's `setCheapest` prepends identically
  (`path.go:1193`), so the same list drives both the index arm
  (parameterized inners) and the plain arm (unparameterized inner +
  partial outer) with no second loop. Per inner: skip
  `!ParallelSafe` (PG `continue`); the param-subset test
  (`inner_paramrels ⊆ outerrelids`, PG :968-990 — the top_parent
  branch is vacuous in goopg, relids direct); then skip `req != 0`
  (the NLI arm's ledgered second gate, reused: a still-parameterized
  result has no `ppi_rows` until P5.6). UNIQUE_INNER vacuous (no such
  jointype; no `create_unique_path` to call).
- **Per pair: bare + memoize variant** (PG's per-pair `get_memoize_path`,
  same nil-skip shape as the NLI arm). **matpath deliberately OUT**:
  goopg builds no Material path anywhere (`plannersettings.go:72`,
  `joinpathsmemoize.go:449`) — a pre-existing all-join-types gap, not
  an R60 decision; enabling it is its own scope.
- **Costing: the NLI arm's four lines, unchanged**
  (`nestloopCost(cp, o.Cost, in.Cost, o.Rows, in.Rows, 0, matRescan) +
  matBuild + qualEvalCost(len(residual), o.Rows*in.Rows)`).
  `initial_cost_nestloop` performs no worker division, and none is
  wanted: the outer input is already per-worker-divided, its rows
  already per-worker — the partial-ness lives in the inputs, exactly as
  the partial-hash producer's comment documents. SEMI/ANTI
  jointype-math is inherited from the shared helper as-is (the helper
  takes no jointype today — pre-existing NLI-arm approximation,
  ledgered once at the helper, not widened here).
- **Filing**: `Rows = clampRowEst(joinrel.Rows /
  getParallelDivisor(o.ParallelWorkers, cp.parallelLeaderParticipation))`
  (the one-divisor rule, partial-hash twin; `cost_funcs.go:850-861`
  documents the Gather-side undo); `DisabledNodes =
  disabledNodesFor(!cp.enableNestLoop, o, in)` (the R59 rule —
  costsize.c:3282 counts on EVERY nestloop path); `ParallelWorkers =
  o.ParallelWorkers` (PG: "a foolish way to estimate…", kept);
  `ParallelSafe = parallelSafeWith(joinrel, o, in)` (parameterized
  probes arrive parallel-safe via `pathparamindex.go:422`'s
  `rel.ParallelSafeForPath()` — §3 P0 asserts the gate actually passes);
  `ParallelAware = false` (no shared NL build exists);
  `RequiredOuter = 0` (by the req!=0 skip); filed via `addPartialPath`
  (domination built-in). `add_partial_path_precheck` deliberately NOT
  mirrored: it is planner-CPU-only (bail before creating the path) and
  the post-costing domination decides identically — state in comment.
- **Trace**: `tracePVetoCtx(s, "nestloop", <joinrel>, <outer>, <inner>,
  "Vn", …)` sites mirroring the hash producer's V0–V9, filed lines as
  `DPPATH partial producer=join.nestloop.partial …` via `tracePath`.

### (iii) Deliberate non-mirrors (not drift — each cited)

matpath (§2 above); precheck (CPU-only); UNIQUE_* (vacuous);
top_parent relids (no top parents in goopg); Pathkeys nil (BOTH serial
NL arms set none — a shared all-NL gap, not R60's to fix in a partial
producer); SEMI/ANTI jt-math (inherited helper limitation).

### Cut site 2: the call in `addPathsToJoinrel` (`joinpaths.go:377` neighborhood)

Beside `addNLIPaths`, AFTER the `!pathParamByRel(i, outer)` block (the
producer's point is parameterized inners — gating on unparameterized
would refuse exactly its inputs), leaving the already-computed
`paramSrc` unused (partial results must be fully unparameterized, so
there is no star-schema exception to test — the code comment states
this; an earlier draft said "reusing", which was wrong). PG runs it in the post-serial-arms parallel block; goopg's
NLI arm already sits post-block unconditionally for every jointype the
INNER-only pin admits, and the partial-NL arm takes the same seat with
its own dispatch gate. No caller needs its own gate — the sibling audit
(§4) covers why.

## 3. Falsifiable predictions

| # | Claim | Mechanism (§1) | Verdict on miss |
|---|-------|----------------|-----------------|
| P0 | `producer=join.nestloop.partial` lines appear; the `{1,2,3,5}` pair is NOT pvetoed | probes parallel-safe (`pathparamindex.go:422`); param `{2}` ⊆ outer; lateral empty | FAIL → gate the dispatch, no further predictions tested |
| P1 | `{2,3,5}` head stays 46602.31 | hyp ≈129.5k dominated (2.78×) §1.ii | FAIL → re-audit |
| P2 | `{1,2,3,5}` head 531599.26 → ≈380146 (rows 459252, workers 4) | §1.iii, Q-cancellation exact to ~±1 | FAIL outside [370k, 390k] → re-audit |
| P3 | Q7 top byte-identical; values md5 MATCH; gather-vs-serial decision unchanged (serial wins by ~195.6k) | §1.iv: max shed 151454 < margin; threatening rungs all ride ≥380145 outers | FAIL (top moves) → re-audit per §1 rule, implementation round STOPS |
| P4 | DS SF0.5 status set unchanged: PASS=94 (same 57 checksums) + Q72 TIMEOUT | executor gap untouched; producer only ADDS partials, serial contests bit-identical | NEW timeout/CKMISMATCH → explain-or-stop (status channel non-blocking per doctrine, but unexplained delta blocks) |

WATCH (either outcome consistent with P3; A/B adjudicates):
W1 `{2,3,4,5}` head 35412.90 vs newly-offered partial-NL challenger (margin alleged ~500).
W2 final-rel `outer={1,2,3,4,5}`-head × `inner={0}` supplier-probe rung (≈337.7k vs 328668.83, alleged ~9k + Memoize variants).

## 4. Sibling audit + executor inventory (why the cut is planner-only)

- **Callers**: the single `addPathsToJoinrel` site (§2 cut site 2). The
  arm self-gates (V0 + dispatch + per-path skips, all traced); no other
  caller of `btreeIndexAMCost`/`nestloopCost`/`addPartialPath` needs a
  gate — the filing path is shared with the two landed partial
  producers.
- **`createPlan` untouched**: only winners build; P3 proves the winners
  don't change on Q7, §5 gates prove it elsewhere.
- **Executor deliberately OUT**, inventoried so a future margin-flip
  knows its bill: `terminatesPartial` (`parallel.go:501`) returns true
  for `*NestedLoopIndexJoin` AND `*Memoize` — a Gather must sit at or
  below either; `stampParallelScan` stamps only scan kinds
  (Seq/BitmapHeap/Index/IndexOnly); probe-side attach under a partial
  subtree is modelled only behind `hashJoinIsPartialCapable`. A
  partial-NL that ever won would need Gather-below-NLI + per-worker
  parameterized-probe attach (+ Memoize-under-partial) — a separate
  scope, triggered only if P3 ever fails. Until then this is
  planner-offered, planner-beaten, never built.
- **No GUC, no knob, no display change.** EXPLAIN output of every
  winner is byte-identical by P3 (no stamper question arises: the
  inner-probe display question stays ledgered where R59 left it).

## 5. Gates (implementation round)

1. `go test ./internal/optimizer/` green (no `-count=1`); `go vet` clean.
2. Q7 top byte-identical vs R59 capture + run-stable (run1==run2);
   fresh binary name `goopg-r60`, driver `/tmp/pp2/r60/run.sh`
   (already exists — add a `q7top` phase; never reuse r59's OUT).
3. DPTRACE A/B: every non-`join.nestloop.partial` line bit-identical;
   only filed-partial lines added, heads move per P1/P2, W1/W2
   adjudicated with numbers.
4. Values 8/8 md5 MATCH (`q1,q3,q5,q7,q8,q9,q10,q19`); Q19/Q5 tops
   byte-identical to R55/R56.
5. `scripts/pg-plan-parity-diff.py`: match≥5, unparsed=0 (R59: 5/0).
6. DS SF0.5 sweep foreground, fresh build: expect PASS=94 (same set) +
   Q72 TIMEOUT alone. Any delta → P4 rule.
7. REPORT.md with the A/B numbers, then review, then commit + push.

Evidence tmp-only `/tmp/pp2/r60/` (R59-binary Q7 capture + start log
already in place; R60-binary captures to follow).
