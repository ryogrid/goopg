# 09 — Verification and Acceptance

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| inherits | the M0126 acceptance discipline (symmetric timeouts, per-class attribution, "a documented no-go is a successful completion; an unmeasured outcome is the only failure") and the standing repo gates (units, pgbench-smoke hook, spotcheck, plan-gate, SF0.5, race-gate for concurrency-adjacent stages) |

## 1. Correctness floor (every stage, non-negotiable)

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` green; the
  pre-commit pgbench smoke on every commit (never `--no-verify`).
- `scripts/tpch-spotcheck.sh` (Q12/Q13 canonical row counts, fresh capped
  server) on every planner/executor/codec commit.
- TPC-DS SF0.5 gate (`scripts/tpcds-sf05-regression.sh sweep`): **zero**
  row-count deltas and **zero** checksum deltas vs the git-tracked oracle.
  This is the gate that caught fusion (Q14 100 vs 200) — it is the primary
  correctness instrument for every executor stage, especially E1 (the seam
  rewrite) and S3 (spill), and runs per stage, not just at the end.
- Full regress-port suite after E1, E4, S3, S4 (codec/format-adjacent
  changes; the M0106 six-silent-regressions precedent).
- Race gate (`make race-gate`) for stages touching shared state (E3's
  build-path changes interact with `parallel_hash_build.go`; S3's temp-file
  registry is per-query but Close paths run under cancellation).
- Sibling-path audits, explicitly listed per stage in code review: E4
  (planner keys ↔ executor key encode), §2.1 of
  [06](06-hash-spill-and-memory.md) (planner nbatch ↔ executor nbatch),
  E5 (compiled ↔ interpreted evaluators).

## 2. Stage gates (advancement criteria)

Recorded per stage in [08](08-migration-and-removal.md) §2's table; the
binding numeric ones:

| gate | bar | evidence file convention |
|---|---|---|
| S1 exit | Q3, Q10, Q18, Q7 each ≤ 1.2× their R0 times (8.46 / 6.04 / 27.58 / 25.13 s; R0 = integer+MHJ pinned baseline, total 493.31 s); no other query > 1.2× vs pre-S1 HEAD | `analysis/leftdeep-joins/<date>-s1-ab.txt` |
| S3 exit | Q21 completes at SF1 under the standard cgroup cap with `work_mem` at default; a forced-spill run (`work_mem` lowered until nbatch ≥ 4 on Q3) returns byte-identical results to the no-spill run | `…-s3-spill.txt` |
| S5 exit | the full acceptance bar, §3 | `…-s5-acceptance.txt` |

## 3. The S5 acceptance bar (successor to M0126-0012's)

TPC-H SF1, fresh capped server per arm, symmetric 600 s timeouts, server age
held constant across arms (sweep-tail discipline):

1. **22/22 complete** — zero hang / OOM / timeout / row-count mismatch. **As
   amended by §3.1 and made executable by §3.3: plus VALUE-level equality
   against the flag-OFF arm, evidenced by `tpch-runner -diff` reporting
   `VERDICT: PASS`. Row counts alone do not discharge this clause.**
2. **Total wall time ≤ 1.2×** the better of pinned R0 (493.31 s) and a
   contemporaneous integer-arm run at the same HEAD.
3. **No single query > 2× its R0 time** — Q9 explicitly: ≤ 170.9 s
   (2 × R0's 85.46 s; the integer default arm's 58.83 s from
   `stage3-order-ab.txt` is the aspirational target beyond the bar).
4. TPC-DS SF0.5: zero deltas (as §1).
5. **No `MultiHashJoin` in any emitted plan; fusion never triggers**
   (assert via EXPLAIN sweep over both suites).
6. **Bushy-plan capability (PG-identical search):** on every searched query
   where PG 18.3's EXPLAIN shows a bushy join spine (composite ⋈ composite),
   goopg must be able to produce the same bushy tree shape — the same
   composite⋈composite pairing over the same relset partition — verified
   through the §4 parity gate's spine diff. Alternative shapes chosen on
   cost-constant or stats-fidelity grounds stay admitted under the ratchet;
   a bushy shape PG can produce that the goopg search *cannot express* is a
   hard failure (the [02](02-plan-shape-contract.md) contract is
   PG-identical shape, not a trade).

A documented no-go with attribution (§6) is an acceptable S5 outcome — the
flag then stays OFF and the bundle's planner half returns to design. The
executor stages S0–S4 stand on their own gates regardless.

### 3.1 The run, and why clause 1 could not have caught it (P5.9, 2026-08-05)

**Run 1 of this bar is a documented NO-GO.** `GOOPG_PGSHAPED_DP` stays OFF.
Evidence: `analysis/leftdeep-joins/2026-08-05-p59-s5-acceptance.txt`
(HEAD `1bb24984`, TPC-H SF1, one binary, two arms differing only in the flag).
Clause 1 fails on four counts — Q2 returns 0 rows against 455, Q7/Q8/Q9 raise
`42883`, Q17 hangs past 3300 s where flag-OFF takes 20.93 s — and clause 3 on
Q5 (4.15×) and Q10 (3.83×). Clause 5 is the one clause that passed: zero
`MultiHashJoin`, zero fusion, in both arms across the EXPLAIN sweep. The
flag-OFF arm completed 22/22 in 380.1 s, inside clause 2's allowance, so the
bar is not failing for want of a contemporaneous baseline.

All of clause 1's failures are **one defect**, attribution class (c): the search
boundary publishes a coordinate map that is a **rotation** of the correct
permutation. The discriminator across the sweep is `boundaryMapIsIdentity`: a
winner that is already left-deep in binding order takes the early return and is
correct; a winner that reorders the leaves gets the rotation.

*(Run 1's write-up attributed the rotation to the `outputLayout` that
`createPlanNode` returns for the winning join arm. That attribution is
**refuted** — see §3.2. The layout and the map built from it are correct; the
map is rewritten afterwards.)*

Two things this bar has to learn from it, both binding on the re-run:

- **A rotation satisfies `boundaryMap`'s tripwire.** §10 of
  [03](03-join-search-pg-dp.md) installed that check against the M0097-0058
  class, and it tests exactly three things — HOLE, OUT OF RANGE, DUPLICATE —
  each of which is a way of *not* being a permutation of `[0,width)`. A
  rotation is a permutation, so the check passes on the one instance of the
  class the search actually produced. A permutation test is not a correctness
  test for a map whose contract is *which* permutation. §3.2 adds the reason
  that observation, though true, is not where the fix goes.
- **Clause 1's row-count comparison cannot see this defect.** The reproducer
  returns the right number of rows with every column value shifted one position
  from its name. Five queries in the ON arm "matched" on count and were not
  compared on value. Clause 1 is therefore amended: **22/22 complete plus
  VALUE-level equality against the flag-OFF arm**, not row counts. The three
  `42883` errors are not three bugs — `extract(year from …)` is the only TPC-H
  construct that type-checks its argument at run time, so Q7/Q8/Q9 are simply
  the only three places the silent corruption becomes loud. A bar that relies
  on a query being loud is measuring the query, not the engine.

Clauses 4 and 6, the §4 parity gate and the §5 estimate audit all score plan
QUALITY, and were deliberately not run: they compare plans from a build whose
plans compute the wrong answer. They are the first instruments to re-run once
the rotation is fixed, and they remain part of the bar P5.9 must clear.

### 3.2 The rotation, attributed (P5.9-c, 2026-08-05)

The producer was innocent. Reproduced in-process against a two-relation
catalog — `select * from customer, orders where o_custkey = c_custkey and
o_orderkey = 1`, which puts the cheap side (`orders`, restricted to one row)
OUTER while the bindings are `customer ++ orders`, so the boundary must emit
its Project — the tree that leaves `createPlanAtSearchRoot` is **correct**:
the join publishes `orders ++ customer`, the layout says so, `boundaryMap`
inverts it, and `projectToBindingOrder` emits `[4 5 6 0 1 2 3]` with each
target naming the column it addresses.

The rotation is applied **after** the boundary, by `remapTopProjection`
(bushy.go). That function finds the join tree to derive its posMap from by
walking DOWN past `*Project` / `*Sort` / `*Limit` / `*LockRows` wrappers — and
the search boundary IS a `*Project`. So the descent stepped over the search
root, handed `buildBindingsPosMap` a node *inside* the searched subtree, and
`collect`'s searched-subtree guard (P5.5-f-ii-a) never fired, because it was
never asked about the root. The map that came back was the search's own
binding→plan-position permutation, and it was then applied to every wrapper
above — including the boundary Project's own target list, which is not a
reference into the map but the map itself. Two permutations composed:
`[4 5 6 0 1 2 3]` became `[1 2 3 4 5 6 0]`, i.e. every column's value one
relation-block from its name. Exactly the reported symptom.

This is the fifth member of 08 §3's skip list, missed at P5.9-b because the
four that were added (`pushPredicatesIntoCrossJoins`, `rewriteJoinsToNLI`,
`rewriteMultiWayChain`, `rewriteScanInputsWithSingleTablePredicates`) all
REWRITE a join tree, while this one only renumbers wrappers — and the
`collect`-side guard made it look already covered. It also latently covers a
second shape: an elided boundary whose root is a `*Sort` was stepped over the
same way.

**What the bar takes from this, and it is not "strengthen `boundaryMap`".**
Run 1's write-up proposed promoting `boundaryMap` from a permutation test to a
per-leaf identity test against each leaf's recorded `baseOffset`/width. That
check would have been silent here: it runs at the producer, and the producer
was right. The invariant that catches a *post hoc* permutation is a
consumer-side one, and `projectToBindingOrder` already makes it hold by
construction —

> target `i` is a bare `ColumnRef` naming the very column it addresses
> (`child.Output()[target[i].Index]`), and the node's own `schema[i]` is that
> same column.

— because a permutation moves the indices and leaves the names behind.
`assertSearchedBoundariesIntact` (createplanroot.go) checks it over the
FINISHED tree at the tail of `Plan()`, after every rewriter, gated on
`GOOPG_PGSHAPED_DP` so the default arm pays one boolean. Verified
non-vacuous both ways: it fires on the unfixed `remapTopProjection`, and
`TestAssertBoundaryProjectionIntactCatchesARotation` rotates a real boundary
node by one and requires the panic.

Regression cover: `internal/planner/joinsearchboundary_test.go`. It asserts on
the column each `select *` target RESOLVES TO rather than on indices (an index
expectation would encode the search's chosen order and then fail for cost-model
changes), and a companion test asserts the fixture still produces a
non-binding-order winner — otherwise the boundary would elide its Project and
the regression test would be green for the wrong reason.

Still open before the bar can be re-run: P5.9-d (the harness compares row
counts, not values) and P5.9-e (Q17, to be re-measured on top of this fix).

### 3.3 Clause 1's instrument, built and calibrated (P5.9-d, 2026-08-05)

§3.1 amended clause 1 to demand VALUE-level equality and left the harness
unable to supply it. `cmd/tpch-runner` now computes three digests per result
set and `-diff` compares two arms on them. Evidence:
`analysis/leftdeep-joins/2026-08-05-p59d-digest-selfdiff.txt`.

| digest | what it is | what it answers |
|---|---|---|
| `colsig` | FNV-1a/64 over the column NAMES in order | did the output header move? |
| `ordered` | FNV-1a/64 chained over the rows in scan order | same tuples, same sequence? |
| `unordered` | the wrapping SUM of the per-row hashes | same MULTISET of tuples? |

Three choices in there are load-bearing, and each is pinned by a test in
`cmd/tpch-runner/digest_test.go`:

- **Sum, not XOR, for the order-independent digest.** XOR cancels an identical
  pair, so a query that emitted a row twice would digest like a query that
  emitted it zero times. Sum is commutative *and* duplicate-sensitive, which is
  what "multiset" requires.
- **Length-prefixed field encoding, not delimiters.** A TPC-H text column may
  contain any byte, so a delimiter is forgeable: `("a","b")` and `("ab","")`
  would collide. NULL gets its own marker byte so it cannot hash as `''`.
- **`rows=N` stays the LAST token on the line.** `scripts/tpch-spotcheck.sh`,
  `ci/batch/stages/stage-tpch.sh` and `scripts/tpch-relsize-arm.sh` all extract
  the row count with an end-of-line-anchored regex. Appending digests after the
  count would have made all three silently extract the empty string the first
  time anyone ran a gate with `-digest` — a new instrument that disarms three
  existing ones. The digests go before the count instead, so `-digest` composes.

The verdicts are deliberately unequal in strength. `VALUE-DIFF` is decisive.
`ORDER-DIFF` (multisets agree, scan order does not) is a **question**: for a
query whose `ORDER BY` is a total order it is a defect, and for one with ties —
Q3, Q10, Q18 — two correct arms may legitimately differ. The differ has no
model of which query is which, so it reports the distinction rather than
absolving it. `NO-DIGEST` **fails**: it is how a run made without `-digest` is
stopped from reading as "everything matched", which is precisely how run 1's
five silently-corrupt queries passed. `BOTH-ERROR` fails too — a query neither
arm answered was not compared.

**Calibration — the flag-OFF arm against itself: 24/24 MATCH.** Two arms,
identical by construction, differing only in the server process and the wall
clock. All four large tie-prone results (Q3 11521 rows, Q10 20501, Q16 18213,
Q15a 10000) matched on the *ordered* digest too, so at a fixed plan scan order
is reproducible and a clean run yields no spurious `ORDER-DIFF`. Repeated across
four server processes and two independently built engine images. Cost: 389 s vs
run 1's digest-less 380.1 s, ~2 % over ~61k scanned rows — inside arm-to-arm
noise. `-digest` still defaults OFF so an R0-comparable timing needs no
argument.

**The bar itself takes two amendments from this.**

1. Clause 1 is now *executable*: the re-run must produce `VERDICT: PASS` from
   `tpch-runner -diff <off-arm> <on-arm>`, with every `ORDER-DIFF` — if any —
   individually adjudicated against that query's `ORDER BY` in the write-up.
   A row-count table is no longer sufficient evidence for clause 1.
2. Both arms of the re-run must be driven by **`scripts/tpch-acceptance-arm.sh`**,
   promoted into the repo this task. Run 1 was driven by an untracked `tmp/`
   script, so the protocol §3.1 documents could not be re-executed from a clean
   checkout — and §3.1 ends by requiring exactly that re-execution.

What this does NOT establish, and the re-run must not read into it: both arms
are goopg. The diff certifies that two goopg arms AGREE, not that either is
right. A value wrong in both arms is invisible to it; only the PG oracle can
see that, and wiring one in is a ledger row (2026-08-05, M0127-P5.9-d), not
part of this instrument.

### 3.4 Run 2, and the arm that turned out not to be an oracle (P5.9, 2026-08-05)

**Run 2 of the bar is a second documented NO-GO.** `GOOPG_PGSHAPED_DP` stays
OFF. Evidence: `analysis/leftdeep-joins/2026-08-05-p59run2-s5-acceptance.txt`
(HEAD `c00db762`, one binary, two arms, both driven by
`scripts/tpch-acceptance-arm.sh` as §3.3 clause 2 requires — the first
execution of this bar a clean checkout can reproduce).

Clause 1 went from four failures to two: **Q7/Q8/Q9 and Q17 all MATCH on
values now**, and they match on the full digest, not merely on the columns
that happened to type-check. Clause 5 passed for the second time (zero
`MultiHashJoin`, zero fusion, both arms). Clauses 2 (1.36×) and 3
(Q7 2.14× / Q9 3.23× / Q10 3.78× / Q18 2.42×) fail; clauses 4 and 6 were
again not reached, for §3.1's unchanged reason — Q2 still computes the wrong
answer under the flag, and plan QUALITY cannot be scored on a build whose
plans are wrong. The one absolute target the bundle set, Q9 ≤ 170.9 s, is
**met** at 53.56 s.

The two surviving clause-1 cells are not two of a kind, and the difference is
what this run adds to the bar:

- **Q2 (`ROWS-DIFF A=455 B=0`) is the flag's.** The decorrelated aggregate is
  spliced in as a foreign coordinate scope joined on `p_partkey =
  ps_partkey` — the shape [P5.9-f](#) fixed for Q17 at `outerWidth`, here
  with a 4-relation inner under a 5-relation outer. Filed M0127-P5.9-g.
- **Q5 (`VALUE-DIFF`, 5 rows both arms) is the BASELINE's.** Q5's WHERE puts
  `{c_nationkey, s_nationkey, n_nationkey}` in one equivalence class over
  three relations, so a correct plan emits two clauses from it. The flag-OFF
  plan emits one (`c_nationkey = n_nationkey`) and never nation-constrains
  `supplier` at all; since `l_suppkey = s_suppkey` is 1:1 this does not
  multiply rows, it admits the ~24-in-25 lineitems whose supplier nation
  differs from the customer's, and revenue inflates ~24×. Measured: PG 18.3
  `5.59e7`, goopg flag-OFF `1.34e9`, goopg flag-ON `5.73e7`. Re-stating the
  dropped equality **redundantly in the SQL does not bring it back** — the
  class is formed and then under-emitted — which rules out both written-order
  sensitivity and "the user must spell out the transitive closure". Filed
  **M0119-0011**, independent of this bundle: it is a wrong answer on goopg's
  *default* planner.

**The amendment this forces on clause 1.** §3.3 closed by warning that the
diff "certifies that two goopg arms AGREE, not that either is right", and
named the PG oracle as the missing instrument. Run 2 hit that limit on its
second run — but from the unexpected side. The instrument was not blind here;
it was *directionally misread by its own specification*, which scores a
non-MATCH as a flag-ON failure. Clause 1 is therefore amended a second time:

> **22/22 complete plus value-level equality with the flag-OFF arm; and every
> non-MATCH cell is ADJUDICATED AGAINST POSTGRESQL before it is attributed.**
> A cell where flag-ON agrees with PG and flag-OFF does not is a baseline
> defect, filed outside this bundle, and does not count against the flip.

Q5 remains a *bookkeeping* failure of run 2 (the arms disagree, and the bar
is specified on agreement) while being a correctness *win* for the flag. It is
also the only finding the value-level amendment has produced so far that run
1's row-count table could never have produced at all — Q5 returns five rows in
both arms.

Two consequences for the timing clauses, both binding on run 3:

- **Q5's 4.09× is struck from clause 3.** The arms compute different result
  sets; flag-OFF admits ~25× more joined rows and is still 4× faster because
  it keeps a 4-worker `Gather` over a hash pipeline while flag-ON picks a
  serial `Merge Join` over an `orders` index scan. Re-base only once
  M0119-0011 lands and both arms compute the same answer.
- **Clause 2's "contemporaneous integer-arm run" is only a valid basis where
  that arm is correct.** Run 2's OFF total (372.50 s) includes a Q5 that is
  fast because it is wrong. The basis is kept as measured and the failure
  recorded honestly, but run 3 recomputes it post-M0119-0011.

### 3.5 Run 3 — correctness discharged, and the timing gap named (P5.9, 2026-08-05)

**Run 3 is a third documented NO-GO, and the first one that fails on
performance alone.** `GOOPG_PGSHAPED_DP` stays OFF. Evidence:
`analysis/leftdeep-joins/2026-08-05-p59run3-s5-acceptance.txt` (HEAD
`1964333a`, one binary, two arms, `scripts/tpch-acceptance-arm.sh`).

**Clause 1 PASSES.** `tpch-runner -diff` reports 23 MATCH and one VALUE-DIFF,
and that one cell is Q5 — whose digests are byte-identical to run 2's on both
arms, so run 2's PG adjudication carries verbatim: flag-ON agrees with PG 18.3,
flag-OFF does not, and §3.4's second amendment excludes it. Three runs:
4 flag-owned failures → 2 → **0**.

Because clause 1 passed, the instruments runs 1 and 2 deliberately withheld
finally ran. What they found is the run's contribution:

| clause | run 3 | note |
|---|---|---|
| 1 value equality | **PASS** | 23 MATCH; Q5 adjudicated to the baseline (M0119-0011) |
| 2 total ≤ 1.2× | FAIL 1.362× | OFF 378.21 s, ON 515.06 s, allowance 453.85 s |
| 3 no query > 2× | FAIL ×5 | Q10 3.91, Q9 3.13, Q18 2.47, Q7 2.07, Q12 2.07; **Q9's absolute bar ≤ 170.9 s PASSES at 54.95 s** |
| 4 TPC-DS SF0.5 | FAIL | **MISMATCH=0, CKMISMATCH=0**; 7 ERROR (one assertion) + 5 TIMEOUT |
| 5 no MultiHashJoin/fusion | PASS | third consecutive, both arms |
| 6 bushy capability | PARTIAL | first evidence: shape divergences 67 → 46, matched joinrels 21 → 32 under the flag |

**The §4/§5 finding, which supersedes P5.9-h's plan.** Both arms audited at the
same HEAD against the same committed PG reference:

| | flag OFF | flag ON |
|---|---|---|
| absolute violations (§5) | 1 | 13 |
| `parity_violations` (§4) | **0** | **6** |
| `shape_mismatches` (§4) | 67 | 46 |

Every one of the six parity violations is a joinrel the PG-shaped search sizes
at **`rows=1`** against actuals of 5 869 – 1 999 080 (Q9 316 264×, Q10 114 106×
twice, Q12 31 354×, Q5 7 411×, Q7 5 869×); the same joinrels on the flag-OFF
arm estimate within 1.4–6.3× of actual. **The five queries carrying parity
violations are the five queries that fail clause 3.** The timing gap is not a
cost-constant gap — it is an estimate collapse, and the cost model cannot rank
anything once every joinrel above the collapse is 1 row.

Q12 is the reproducer, because it joins exactly two relations and so has no
search-order confound. Under the flag its outer input is
`Index Scan using orders_pk on orders` with **no index condition** — a full
ordered scan the search adds for merge-join sortedness — carrying `rows=1`,
the row estimate of a parameterized single-row lookup. Under the flag-OFF arm
the same relation is a `Seq Scan` at `rows=1500000` and the join estimates
21 154. Whether the 1 is created when the path is built or is a correct path
size mis-consumed by `makeJoinRel`/`sizeJoinRel`
(`internal/planner/joinsearchlevel.go:324-330` clamps `rows < 1` to 1, which
would mask a zero as a one) is P5.9-h's first bisect. Q18 is *not* part of this
class — its final joinrel is ~23 400× over in **both** arms — and is tracked
separately.

**Clause 4 found a defect TPC-H cannot reach.** Zero MISMATCH and zero
CKMISMATCH across 99 queries under the flag — but 7 queries (Q11, Q31, Q47,
Q57, Q58, Q74, Q83) abort at plan time on
`assertSearchedTreeNeedsNoReconcile` (`searchedtree.go:205`, the P5.5-f-ii-a
cross-check) with a layout disagreement — `ca_county 0→8`,
`customer_id 0→12`, `customer_id 0→20`, `i_category 0→16`, `i_category 0→18`,
`item_id 0→4`, `item_id 2→0` — and 5 more (Q7, Q26, Q27, Q53, Q63) time out at
320 s in the §5 estimate-collapse pattern. The assertion is doing exactly what
it was built for: it converts a wrong-column plan into a dead connection
instead of a wrong answer. Filed **M0127-P5.9-i**. The lesson for this bar is
that TPC-H — the corpus every P5.9-x defect so far was found on — does not
exercise the repeated-alias CTE/UNION-ALL family at all, so clause 4 must run
on **every** future acceptance run, not only after clause 1 is clean.

Harness changes forced by run 3, both for the P5.9-d reason (an evidence file
whose driver is ephemeral, or which cannot name its own arm, is not evidence):
`scripts/tpch-estimate-audit-arm.sh` (new — the §4/§5 instruments had no
in-repo server bring-up, so the ratchet was produced by an ad-hoc command line
each time) and `sf05_planner_flags_line` in
`scripts/tpcds-sf05-regression.sh`, which did not print the PGSHAPED flags and
so made a flag-ON sweep byte-indistinguishable from a flag-OFF one.

### 3.6 The estimate collapse, closed — and what it was not (P5.9-h, 2026-08-05)

§3.5 ended on a bisect question: is the `rows=1` minted when the ordered index
path is BUILT, or is a correct size mis-consumed by `makeJoinRel` /
`sizeJoinRel`? **The answer is neither.** The search's own numbers were right
all along — `addOneOrderedIndexPath` sets `Path.Rows = rel.Rows` (the
relation's post-restriction estimate, 1 500 000 for `orders`) and `makeJoinRel`
sizes the joinrel off that. The 1 is minted AFTER the search, when the finished
plan tree is re-estimated: `EstimateRows` (`internal/planner/cardinality.go`)
answered **1 for every `*IndexScan` and `*IndexOnlyScan`**, on the convention
that such a node is an equality probe — one row per call site.

That convention was true of every index scan goopg emitted before
P5.4c-ii-b. Each one carries an Index Cond binding the index columns, and the
executor probes it once per outer row. P5.4c-ii-b introduced the second shape
PG has always had — an index path with an EMPTY `indexclauses` list, which
"implies a full index scan" (`pathnodes.h:1817`), generated so a merge join
above it can skip a sort — and for that node the answer is the relation's
cardinality. The fix is one arm: a node with no `Key`, no `Keys` and no
`LowKey`/`HighKey` estimates `tableRows(Table)`; anything that binds the index
keeps the old answer verbatim.

It was never a display defect. `EstimateRows` is what EXPLAIN prints, but it is
also what sizes a hash table (`operators_join_agg.go:629`), picks a join
algorithm (`planner.go:2360`) and decides a Memoize (`memoize.go:114`) — and a
leaf that under-reports by the size of the table under-reports every join above
it, the same propagation shape M0125-0038 fixed for the pass-through wrappers.

Measured on the five queries that carried all six violations
(`scripts/tpch-estimate-audit-arm.sh`, `--queries 5,7,9,10,12`, same cluster,
`analysis/leftdeep-joins/2026-08-05-p59h-audit-{on,off}.txt`):

| | run 3 (ON) | P5.9-h (ON) |
|---|---|---|
| `parity_violations` (§4) | **6** | **0** |
| Q12 `{lineitem,orders}` | est 1 / actual 31 354 | est 46 001 / actual 31 354 (1.5×) |
| Q5 final joinrel | est 1 / actual 7 411 | 7.0× |
| Q7 final joinrel | est 1 / actual 5 869 | 2.1× |
| Q9 final joinrel | est 1 / actual 316 264 | 6.3× |
| Q10 final joinrel | est 1 / actual 114 106 | 1.4× |
| Q12's `orders` leaf | `rows=1` | `rows=1500000` (exactly actual) |

**What it did NOT fix, and this is the part that re-opens P5.9-h rather than
closing it: the plan shapes are byte-identical before and after, and so are the
timings** (Q12 20.83 s → 20.21 s, Q7 27.47 s → 26.85 s under `EXPLAIN
ANALYZE`). §3.5 asserted "the timing gap is an estimate collapse"; that
assertion is **refuted**. The collapse was real and is now gone, and the five
queries still plan a Merge Join over a full ordered index scan of `orders`
where the flag-OFF arm plans a Hash Join over a Seq Scan. Choosing to read
1.5 M rows through a primary-key index to save a sort is a COST question — the
ordered path's `costIndexScan` at `selectivity = 1.0` against `costSeqscan`
plus the sort the merge arm avoids — not a cardinality one. That is the
remaining half of P5.9-h.

Blast radius, measured rather than argued: the flag-OFF TPC-H arm emits **no**
bound-less index scan at all (the audit's `.plans.txt` for the same five
queries contains zero `Index Scan` lines), and the TPC-DS SF0.5 plan-shape
capture reads `queries=99 same=99 changed=0`. The ≤0.6 % drift in the OFF
arm's `est=` column between run 3 and this run is the audit's own `--warm-stats`
re-ANALYZE, which samples: same ratios to one decimal, identical shapes, and
Q9 — whose estimates are driven by relation sizes rather than NDistinct —
byte-identical.

### 3.7 The seven TPC-DS aborts were the CHECKER's disagreement (P5.9-i, 2026-08-05)

§3.5 filed seven TPC-DS queries — Q11, Q31, Q47, Q57, Q58, Q74, Q83 — that
abort at plan time under the flag inside
`assertSearchedTreeNeedsNoReconcile` (`internal/planner/searchedtree.go:205`),
each reporting a distinct column moving: `ca_county 0→8`, `customer_id 0→12`,
`customer_id 0→20`, `i_category 0→16`, `i_category 0→18`, `item_id 0→4`,
`item_id 2→0`. The assertion's contract is that two unrelated mechanisms — the
arms' coordinate arithmetic over `outputLayout` and `reconcileNLILayout`'s name
resolution — reach the same index, so a disagreement is a bug in one of them.
**It was in the second one**, and it was not new: the same defect had been a
silent wrong-column join on the cost path since M0071-0009.

The reproducer is Q83's outer query, which joins three CTE scans —
`sr_items`, `cr_items`, `wr_items` — each publishing a column named `item_id`.
`reresolveJoinByName`'s `predRebind` resolves a predicate operand against the
side its current index suggests and **falls back to the other side when the
name is not found there**, a fallback written for `pushOneConjunct`'s residuals
(whose side classification an earlier pass may have invalidated). `resolveSide`
returned -1 for two different situations, and the fallback could not tell them
apart:

- **miss** — the name is not on this side. Real evidence the classification
  was wrong; crossing over is correct.
- **ambiguous** — the name is on this side more than once. Evidence of nothing
  except that the resolver cannot finish; crossing over is a guess, and on
  these seven queries it was the wrong one.

`SourceTableIdx` did not rescue it, and the reason is the discovery worth
keeping. M0071-0009 added the (Name, SourceTableIdx) lookup for Q21's three
`lineitem` aliases — three range-table entries of **one** scope, hence three
distinct source indices — and its comment called a duplicate "shouldn't happen
in well-formed schemas". That is true within a scope and false across them.
Every `item_id` above descends from `item.i_item_id` inside a **separate** WITH
arm, each arm numbers its own range table, so all three columns carry the same
source identity. Both lookups are therefore ambiguous, the correct side answers
-1, the other side answers with its single match, and a reference correctly
bound to column 0 is rebound to column 4 — a predicate comparing a column to
itself, i.e. a cross product. This is the CTE/UNION-ALL family that TPC-H's 22
queries never produce, which is why three acceptance runs and the whole TPC-H
bar could not reach it.

The fix is in the resolver, not the arms: `lookupColumnIndexByName` and
`lookupColumnIndexByNameAndSource` (`bushy.go`) report the duplicate case
separately, `predRebind` abstains on it, and the miss fallback is untouched —
`TestReresolveStillCrossesSidesOnAPlainMiss` pins that half so the fix cannot
silently widen. An ambiguous (Name, SourceTableIdx) does not retry Name-only:
dropping a disambiguator can only match the same columns or more.
`findUniqueColumnIndex` / `findColumnIndexByNameAndSource` survive as
one-line wrappers, so the NLI and join-key rebind sites keep their exact prior
behaviour (they force a side, and an ambiguous name there was already left
alone).

Measured, SF0.5 subset sweep under `GOOPG_PGSHAPED_DP=1`
(`sweep-20260805-222627.txt`, oracle = PG 18.3):

| | run 3 (ON) | P5.9-i (ON) |
|---|---|---|
| Q11 | ERROR 0s | PASS 20s, 8 rows, ck matches |
| Q31 | ERROR 0s | PASS 14s, 19 rows, ck matches |
| Q57 | ERROR 0s | PASS 81s, 100 rows (ck=n/a) |
| Q58 | ERROR 0s | PASS 22s, 0 rows, ck matches |
| Q74 | ERROR 0s | PASS 88s, 7 rows, ck matches |
| Q83 | ERROR 0s | PASS 3s, 3 rows, ck matches |
| Q47 | ERROR 0s | **TIMEOUT** at the 300s gate |
| summary | `ERROR=7` | `PASS=6 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1` |

Five of the six carry a value checksum identical to PG's, so this is an answer
match and not a row-count coincidence.

**Q47 is a new, separate defect and must not be booked as this one's
remainder.** It now plans and returns the correct 100 rows — timed alone on a
freshly restarted server, **8 m 40 s** — where the flag-OFF arm passes in
11–13 s (`sweep-20260805-1{74711,92044}.txt`). A ~40× cost regression on one
query is the same class as §3.6's leftover: a plan the search prefers and
should not. It was invisible until now only because the query used to abort
before it could run, which is the honest form of "P5.9-i uncovered it". Filed
as **P5.9-j**, ledgered, and it keeps DS05 clause 4 red for run 4.

### 3.8 Q47's 40× was one term charged on the wrong tuple count (P5.9-j, 2026-08-05)

§3.7 left Q47 as the flag-ON arm's only TIMEOUT: correct 100 rows in **8 m
40 s** against 11–13 s flag-OFF. It is not a search-order defect and not an
estimate defect — **the estimate is PG-faithful and PG makes the same one.**
It is a single cost term charged on the wrong number of tuples.

The subject is Q47's `v2`, which self-joins the `v1` CTE three ways on four
equalities plus an offset (`v1.rn = v1_lead.rn - 1`). Reduced to its skeleton
the defect is reproducible on SF0.5 in one EXPLAIN, and the reduction is the
useful part: with **three** join keys the top join is a Hash Join, with
**four** it becomes a Nested Loop, and *which* columns they are does not
matter. That is a threshold on a count, which pointed at cost rather than at
binding — the P5.9-i family this query came from would have been indifferent to
arity.

Instrumenting `addPathsToJoinrel` at the top pair (`{v1,v1_lag} ⋈ v1_lead`)
gave the whole answer in two lines:

| | startup | total |
|---|---|---|
| hash (build the 1-row side) | 669.68 | **968.55** |
| plain nested loop | 370.79 | **968.53** |

The loop wins by **0.02** on total and by 300 on startup, so `addPath` drops
the hash as strictly dominated — correctly, on those numbers. Neither input is
misjudged: both sides carry 7 193 rows, all four clauses are found and all four
are keyable (`clauses=4 keys=4 residual=0`).

What makes the loop look free is that its outer is the already-collapsed
joinrel. Four independent equalities over CTE scans that carry no statistics
multiply four `DEFAULT_EQ_SEL`s: 7 193² × 0.005⁴ = 0.03, clamped to **1 row**
(at three keys the same arithmetic gives 6, which is why three keys still hash).
A one-row outer rescans the inner exactly once, so the loop's rescan term
vanishes. **PG estimates `rows=1` here too** — verified directly against the
oracle on the same data — so the collapse is not the bug. PG escapes it by a
different route: its CTE scan publishes the WindowAgg's ordering as pathkeys, so
the merge join it picks costs 375.55 with **no sort at all**. goopg's paths
carry no pathkeys out of a CTE scan (the `generate_mergejoin_paths` gap already
recorded in [03](03-join-search-pg-dp.md) §5), so its merge arm must pay for two
sorts and lands at 1393.36 — out of contention, leaving the hash and the loop to
decide it between them at a margin of 0.02.

The defect is in that margin. `final_cost_nestloop` (`costsize.c`) charges the
per-tuple CPU on the pairs the loop *walks*, and PG comments the distinction at
the assignment because it is exactly the one that gets lost:

```c
/* Compute number of tuples processed (not number emitted!) */
ntuples = outer_path_rows * inner_path_rows;
...
cpu_per_tuple = cpu_tuple_cost + restrict_qual_cost.per_tuple;
run_cost += cpu_per_tuple * ntuples;
```

goopg splits that sum across two sites — the qual half is the caller's
`qualEvalCost(cp, len(quals), o.Rows*i.Rows)`, already on the cross product —
and the `cpu_tuple_cost` half was landing on the join's **output** rows inside
`nestloopCost` (`cost_funcs.go`). The error is invisible almost everywhere,
because a nested loop is preferred precisely when its output is small: the term
is smallest on exactly the plans it exists to deter. Here it charged 0.01 × 1
where PG charges 0.01 × 7 193, and 71.92 of missing cost decided a 0.02 race.

The fix is the one line, plus PG's clamp of either side to one tuple, and
`innerRows` threaded to the three call sites — `addNestLoopPath` and
`addNLIPaths` pass the inner PATH's own count (`ppi_rows` for a parameterised
inner, the same number their `qualEvalCost` already uses), and the legacy
bushy NLI-delegation site passes 1, the per-probe count its `indexProbeCost`
rescan term already assumes. **The hash and merge siblings are deliberately
untouched**: PG charges those on `hashjointuples` / `mergejointuples`, which
really are output counts, so this is not a symmetric slip.

Measured, SF0.5 subset sweep, both arms, `/tmp` scratch results:

| | before | after |
|---|---|---|
| Q47, flag ON | TIMEOUT (>300 s); 8 m 40 s solo | **PASS 13 s**, 100 rows |
| ON subset (Q6/30/47/54/58/83/84) | `PASS=6 TIMEOUT=1` | **`PASS=7 TIMEOUT=0`** |
| OFF subset, same seven | `PASS=7` | `PASS=7`, checksums identical |

The top join of Q47 is now a five-key Hash Join on both arms, and the loop
costs 1040.45 against the hash's 968.55 — the ordering PG's own formula gives.

Two things this did **not** fix, both already on the books. The CTE-scan
pathkey gap is the real reason goopg cannot reach PG's free merge here, and it
stays P5.4c-ii's. And the 1-row collapse on stats-less CTE columns is faithful
to PG but is still a 7 193× error against actuals on both arms; it is the
§4.1 parity ratchet's subject, not this one's.

### 3.9 The sort was the only spill nobody charged for (P5.9-k, 2026-08-05)

§3.6 closed P5.9-h's cardinality half and, in doing so, refuted its own
headline: the five queries' estimates became right and their plans and timings
did not move at all. What was left was named there as a cost question — reading
1.5 M rows of `orders` through the primary-key index to save a sort — and the
suspect was named as `costIndexScan` at `selectivity = 1.0`. **The suspect was
innocent.** The index scan's price is roughly what PG would charge for it; the
defect is on the other side of the comparison, and it is not a mispricing so
much as a MISSING price.

`costSortRun` implemented only the comparison term of `cost_sort`. Its own
comment said so and gave the reason — "width participates in the external-merge
arm (not modelled at the milestone; TPC-H sorts are small dimension outputs)".
That premise was true when it was written and is exactly what this phase
invalidates: a merge join sorts a **join input**, and Q12's is 5 997 241
`lineitem` rows. So a sort that will certainly write ~4.7 GB of runs was priced
as though it fit in memory.

The reason that is a defect and not an approximation is the rival it competes
against. Since P5.7-a the hash join HAS been charged its spill in full
(`hashJoinCost`'s `NBatch > 1` term). Both operators spill the same rows
through the same `work_mem` budget in the same executor; only one of them was
billed. Design [04](04-cost-and-cardinality.md) §1 forbids exactly this — two
independently calibrated models competing inside one `addPath` comparison — and
here the asymmetry is not a calibration constant but an entire term. Q12's two
candidates, at the default 512 MB budget:

| | bytes spilled | charged before P5.9-k |
|---|---|---|
| Hash Join (`orders` build, `lineitem` probe) | 0.68 GB + 4.75 GB | **1 326 616** |
| Merge Join (sort of `lineitem`) | 4.75 GB | **0** |

With 4.75 GB priced at zero the merge arm wins on a rounding difference, which
is precisely what the five queries did.

The fix is `cost_tuplesort`'s disk branch (costsize.c:2144), reproduced term for
term: `npages = ceil(input_bytes / BLCKSZ)`, `nruns = input_bytes /
sort_mem_bytes`, `log_runs = ceil(log(nruns) / log(mergeorder))` with
`mergeorder` from `tuplesort_merge_order` (`MINORDER` 6, `MAXORDER` 500), and
`2 * npages * log_runs` page accesses at PG's stated ¾-sequential/¼-random mix.
Two details are goopg's rather than PG's, and both are chosen for symmetry with
the rival rather than for fidelity to upstream in isolation:

- the row width comes from `hashsize.EntryBytes`, the same model `spillPages`
  uses for the hash side's batch files, so the two charges are denominated in
  one currency (this is the point of the change; a byte model that disagreed
  with the hash side's would have re-created the defect with different
  numbers);
- `ncols == 0` means "width unknown" and suppresses the disk term, which is the
  same reading `hashJoinCost` already gives a zero `innerCols`. An unknown
  width must not invent an I/O charge for one candidate and excuse the other.

Upstream's middle branch — the bounded heap-sort for a useful `limit_tuples` —
is unreachable from here and is not written: goopg has no LIMIT-aware sort
path, so `output_bytes == input_bytes` identically and that branch's guard is
false whenever the disk branch did not already fire. Ledgered with the LIMIT
push-down. PG's `tuples < 2 ⇒ 2` clamp IS adopted, replacing a `return Cost{}`:
a sort of a collapsed 1-row estimate must not be free, which is the same
failure mode §3.8 closed on the nested-loop side.

Measured, five queries, one binary, both arms in one session
(`scripts/tpch-acceptance-arm.sh`,
`analysis/leftdeep-joins/2026-08-05-p59k-{on,off}.txt`):

| query | run 3 ON | run 3 OFF | P5.9-k ON | P5.9-k OFF |
|---|---|---|---|---|
| Q7 | 26.71 s | 12.92 s | **16.29 s** | 16.50 s |
| Q9 | 54.95 s | 17.57 s | **15.86 s** | 16.11 s |
| Q10 | 22.93 s | 5.86 s | **5.65 s** | 5.70 s |
| Q12 | 20.79 s | 10.02 s | **9.82 s** | 9.56 s |
| Q18 | 74.71 s | 30.19 s | **29.79 s** | 29.03 s |
| total | 200.09 s | 76.56 s | **77.41 s** | 76.90 s |
| ON/OFF | **2.61×** | | **1.007×** | |

Every digest (`colsig`/`ordered`/`unordered`) is identical to run 3's on both
arms, so nothing about the result set moved. Q12's plan is now `Hash Join` over
two `Seq Scan`s — the OFF arm's shape — and the full ordered index scan of
`orders` is gone from all five. The §5 audit's ON arm
(`analysis/leftdeep-joins/2026-08-05-p59k-audit-on.txt`) now reports the SAME
single violation as the OFF arm, Q18's `Hash Join (SEMI)` at 22 285× — the
one §3.6 already excluded from this class because it is present in both arms —
and the §4 ratchet holds at `parity_violations=0`.

Two things this did not settle. The five queries no longer choose an ordered
index scan, but nothing has yet shown that goopg would ever be right to: its
merge operator sorts BOTH inputs unconditionally (`newMergeSortedSource`,
`join_merge_stream.go`), so the sort a pre-ordered input is credited with
skipping (`tryMergeJoinPath`'s `pathkeysContainedIn` branch) is not actually
skipped at run time. That credit is now the only remaining fiction in the merge
arm's cost, and it is filed rather than fixed here because closing it means
teaching the executor to stream a pre-sorted side, not adjusting a constant.
And the fix reaches exactly one producer: `sortPathFor` is `costSortRun`'s only
caller, because the query's own ORDER BY never enters the path model at all
(§10 of [03](03-join-search-pg-dp.md) defers query pathkeys past the search
boundary). A final `Sort` that spills is therefore still unpriced — it just has
no rival to be unfair to yet.

### 3.10 Run 4 — five clauses pass, and the sixth has no instrument (P5.9, 2026-08-06)

Evidence: `analysis/leftdeep-joins/2026-08-05-p59run4-s5-acceptance.txt`
(HEAD `9e0cfe67`, one binary, both arms, protocol per §3.4's resume point).

| clause | run 3 | run 4 |
|---|---|---|
| 1 — 22/22 + value equality | PASS | **PASS** (23 MATCH; Q5 excluded per §3.4) |
| 2 — total ≤ 1.2× | FAIL 1.362× | **PASS 0.982×** (ON 355.14 s, OFF 361.59 s) |
| 3 — no query > 2× | FAIL (5 cells) | **PASS**, worst 1.36× (Q2); Q9 15.83 s ≤ 170.9 s |
| 4 — TPC-DS SF0.5 | FAIL (7 ERROR, 5 TIMEOUT) | **PASS** `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |
| 5 — no `MultiHashJoin`/fusion | PASS | **PASS** (fourth consecutive) |
| 6 — bushy-plan capability | PARTIAL | **PASS** — §3.13 measured both remaining candidates `OFFERED` (was UNDISCHARGED at §3.11) |
| §4 ratchet `parity_violations` | 0 OFF / **6** ON | **0 OFF / 0 ON** |
| §5 absolute tripwire | 1 violation per arm (Q18) | 1 violation per arm (Q18), ON the smaller |

Every clause runs 1–3 failed on is green, and no defect anywhere in the run is
attributed to `GOOPG_PGSHAPED_DP`. **The flip is still held, by clause 6 —
and clause 6 is a gate on the harness before it is a gate on the planner.**
(Discharged two channels later: §3.11 built the spine diff, §3.12 the
enumeration trace, and §3.13 measured clause 6 green on 2026-08-06. What
follows is the state at run 4, kept as written.)

§4 specifies the check as "verified through the §4 parity gate's spine diff".
That spine diff does not exist: `cmd/estimate-audit` contains zero occurrences
of "bushy" or "spine". Its parity channel compares per-joinrel *estimates* and
labels one-sided relsets `SHAPE (PG-only joinrel)` / `SHAPE (goopg-only
joinrel)`. A relset name says which base relations are underneath a node; it
does not say how they were **paired**, and clause 6 is a question about
pairing. Run 3's "PARTIAL" rested on the shape-divergence *count* (67 → 46),
which is a proxy for the check, not the check.

Measured directly for run 4 — a join node is bushy iff both children, after
unwrapping `Hash`/`Materialize`/`Sort`/`Gather`/`Memoize`/aggregation nodes,
are themselves joins:

- PG 18.3 chooses a bushy spine on exactly **three** of the 22: Q7
  (`Hash Join ← [Nested Loop, Hash Join]`), Q8 and Q20 (both
  `← [Nested Loop, Nested Loop]`).
- goopg produces **no** bushy spine on any of the 22, in **either** arm.

PG's Q7 partition is `{customer+lineitem+n2+orders} ⋈ {n1+supplier}`; goopg's
ON arm builds `{lineitem+n1+n2+orders+supplier} ⋈ customer`. That divergence is
not itself a failure — §4 admits shapes chosen on cost-constant or
stats-fidelity grounds and reserves hard failure for a bushy shape the search
*cannot express*. The structural evidence says it can: `joinSearchOneLevel`
phase 2 (`internal/planner/joinsearchlevel.go:171-222`) is `joinrels.c:141-198`
term for term — k-loop halfway break, mirror-image offset, clause-only pair
gate — and is unit-tested by `TestJoinSearchFourRelChainOffersBushyPair`,
`TestJoinSearchBushyIsClauseOnly` and
`TestJoinSearchPairCountMatchesClosedForm`.

**But those tests prove the mechanism on a synthetic 4-relation chain.** For
Q7's, Q8's and Q20's actual relset partitions, "enumerated and lost on cost"
and "never enumerated" predict the identical observable — a left-deep winner —
so the run cannot distinguish them. Hence undischarged rather than failed, and
hence no flip: passing five clauses while the sixth is unmeasured is precisely
what four runs of this bar exist to prevent.

→ **P5.9-l**: build the channel §4 named. The search records, per query, the
joinrel pairings the DP actually built; a comparator asks whether PG's chosen
partition is among them. Clause 6 then either passes as a cost/stats divergence
admitted under the ratchet, or names a concrete gap in the bushy phase. Both
are results; the present state is neither.

**↳ The manual measurement quoted above is superseded by §3.11's instrumented
one.** "goopg produces no bushy spine on any of the 22, in either arm" is
false: the flag-ON arm goes bushy on six queries, and on Q20 it chooses PG's
bushy partition exactly. The three-query PG count and the Q7 partition survive.

### 3.11 The spine channel, built — and goopg was already bushy (P5.9-l, 2026-08-06)

Clause 6's instrument now exists: `internal/estimateaudit/spine.go`, rendered
into every `cmd/estimate-audit` run that has a PG reference, so §4's ratchet,
§5's tripwire and the spine diff come out of one arm with no change to
`scripts/tpch-estimate-audit-arm.sh`.

**Why the parity channel could not have answered this.** `Parity` keys a
joinrel by the SET of base relations underneath it — upstream's
`RelOptInfo.relids` — which is the right key for an *estimate* comparison and
the wrong one for clause 6. On Q7 both engines build the six-relation top
joinrel, so the parity channel reports it `matched`; what differs is how that
relset was **partitioned**, and a relset name cannot carry a partition. The
spine channel computes, for every join node, the relsets of its immediate
children (`SpineJoin.Inputs`), and classes the node bushy iff both children —
after descending through every single-child pipeline node between them, which is
how PG's inner side reaches a join through a `Hash` — are themselves joins.

Applied offline to run 4's committed plans (`--from-plans`, no re-run) against
the pinned PG 18.3 reference:

| | flag OFF | flag ON |
|---|---|---|
| pairings matched | 13 | **24** |
| PG-only pairings | 44 | **33** |
| goopg-only pairings | 45 | **32** |
| bushy spine chosen by goopg | 2 (Q5, Q20) | **6** (Q2, Q7, Q8, Q9, Q10, Q20) |
| bushy spine chosen by PG | 3 (Q7, Q8, Q20) | 3 (Q7, Q8, Q20) |
| clause-6 candidates | 2 (Q7, Q8) | **2** (Q7, Q8) |

Three results, in order of how much they move clause 6:

1. **goopg's search expresses AND WINS a real bushy TPC-H partition.** Q20's
   top pairing is `{nation+supplier} ⋈ {lineitem+part+partsupp}` on *both*
   engines — the diff prints it `both`. That is the evidence the synthetic
   4-relation chain tests could not supply: phase 2 built a bushy pair over a
   five-relation TPC-H relset and `add_path` kept it. Q7's own ON-arm plan is
   bushy one level under a left-deep top (`{lineitem+orders} ⋈
   {n1+n2+supplier}`), so the mechanism is live on that query too.
2. **The flag moves every spine number toward PG.** Matched pairings nearly
   double, both one-sided counts fall by ~25 %, and the bushy count goes 2 → 6.
   Run 3's shape-divergence proxy (67 → 46) pointed the same way; this is the
   same movement measured on the unit clause 6 is stated on.
3. **Two candidates remain**, and only two: PG's bushy top on Q7
   (`{customer+lineitem+n2+orders} ⋈ {n1+supplier}`) and on Q8
   (`{lineitem+orders+part} ⋈ {customer+n1+region}`). Q20's is no longer a
   candidate because goopg chose it.

Clause 6 is therefore no longer "unmeasured", but it is not yet discharged.
§4's hard-failure condition is a bushy shape the search *cannot express*, and
for Q7/Q8 "enumerated and lost on cost" and "never enumerated" still predict the
same observable — a chosen plan without that pairing. Result 1 is strong
circumstantial evidence for the first (the same phase-2 code produced a bushy
winner on Q20 in the same arm), but circumstantial is what §3.10 refused to
flip on. → **P5.9-l-ii**: the search's own provenance — record every pairing
`makeJoinRel` was offered, with its phase, and ask directly whether Q7's and
Q8's partitions are in that set.

Two limits of the channel, both printed rather than silent:

- **Ambiguous pairings are excluded from the candidate list.** A plan that
  scans one relation name twice without an alias (Q2, Q8, Q17, Q18, Q22 on the
  ON arm) collapses two range-table entries into one relset member — §4.1's
  "`shape_mismatches` is an upper bound" note, same cause. The pairing is still
  printed; it just cannot be adjudicated. Q8's candidate comes from the
  *reference* side, which does dedupe (`select_rtable_names`, ruleutils.c).
- **The diff is over CHOSEN spines**, on both sides. It says nothing about what
  either search enumerated — which is precisely the gap P5.9-l-ii closes.

Evidence: `analysis/leftdeep-joins/2026-08-06-p59l-spine-{on,off}.txt` and
`-README.md`.

### 3.12 The enumeration-provenance channel (P5.9-l-ii, built 2026-08-06)

§3.11 ends on a question a chosen plan cannot answer, so this channel reads the
other end: the join search's own record of what it was OFFERED.

**Writer** — `internal/planner/joinsearchtrace.go`, gated on
`GOOPG_PGSHAPED_DP_TRACE=1` (read once at process start, like every other
planner gate) and nil when off, so production is untouched. `joinSearch` emits
one block per join problem to stderr — the server log — in a single write:

```
DPTRACE problem nrels=4 rels=n1,supplier,customer,orders
DPTRACE pair phase=2 lev=4 created=0 pair={customer+orders} | {n1+supplier} outer={n1+supplier} inner={customer+orders}
DPTRACE decline phase=1 lev=2 reason=no-join-clause pair={customer} | {supplier}
DPTRACE end top={customer+n1+orders+supplier} pairs=10 declined=5 status=ok
```

Three decisions carry the channel:

1. **The pair key is `SpineJoin.PairKey`'s string, byte for byte** — members
   sorted by NAME, sides sorted among themselves. The plan side sorts, so a key
   equal only up to a permutation is not a key, and a drift here would turn
   every candidate into a false NOT-ENUMERATED.
2. **Names are the FROM item's alias when it has one**, else its unqualified
   catalog name — `estimateaudit.leafRel`'s rule (parity.go). Without it Q7's
   `nation n1` / `nation n2` collapse into one member on the search side while
   staying distinct on the plan side.
3. **Refusals are recorded, not just acceptances.** The connectivity gate
   (`hasRelevantJoinClause`, joinsearchlevel.go) is the one place a partition PG
   chose can be silently withheld; logging only the accepted pairs would report
   "declined for want of a join clause" and "never reached" as the same silence.

**Reader** — `internal/estimateaudit/enumtrace.go`, driven by
`estimate-audit --enum-trace <server log>` (the arm script passes it whenever
`DP_TRACE=1`). It derives the partitions to adjudicate from the §3.11 spine
diff and answers each one:

| verdict | meaning | clause 6 |
|---|---|---|
| `OFFERED` | the search produced this pairing | divergence is cost/stats — §4 admits it, **passes** |
| `DECLINED` | reached and refused by the connectivity gate | a named gap, **fails** |
| `SIDE-NOT-BUILT` | one side was never built as a joinrel — a gap one level below | a named gap, **fails** |
| `NOT-ENUMERATED` | both sides exist, the pair was neither offered nor declined | a named gap, **fails** |
| `NO-TRACE` | no DPTRACE block was harvested at all — a statement about the HARVEST, not the search | inadmissible |
| `CROSS-QUERY-LEVEL` | the trace was harvested, and the pairing's two sides were planned at different query levels (§3.13) | control: out of scope · candidate: **fails** |

**Controls are part of the instrument, not of the report.** Every bushy pairing
goopg itself chose was by construction offered to `makeJoinRel`, so it must come
back `OFFERED`; §3.11 names Q20's matched pairing as exactly this control. The
reader derives the control set from the diff rather than hard-coding Q20, and a
failing control prints `VERDICT: HARNESS FAULT` and voids every candidate
verdict in that run — the guard that stops an unharvested log (wrong path, flag
off, wrong arm) from being reported as a planner defect.

Ratchet line:
`RATCHET enum_controls= enum_candidates_offered= enum_problems= enum_malformed=`.

**Live smoke, 2026-08-06** (`analysis/leftdeep-joins/2026-08-06-p59lii-dptrace-*`):
on a throwaway 4-relation cluster the chosen plan is bushy, the trace records
that partition at `phase=2` with `created=0` (a phase-1 pair reached the top
relset first — which is itself the proof that "relset built" and "partition
offered" are different questions), the alias `n1` survives, and an unconnected
partition adjudicates to `SIDE-NOT-BUILT` with the side named.

Measured on TPC-H SF=1 in §3.13.

### 3.13 Clause 6, measured — both candidates were OFFERED (P5.9-l-ii, 2026-08-06)

Arm: `PLAN_ONLY=1 DP_TRACE=1 PGSHAPED=1 PER_Q=180s
scripts/tpch-estimate-audit-arm.sh 2026-08-06-p59lii-enum-on --queries 7,8,20`.
Evidence: `analysis/leftdeep-joins/2026-08-06-p59lii-enum-on.{txt,plans.txt,dptrace.txt}`
plus its README; the verdict is re-derivable offline from the two committed
inputs with `--from-plans … --enum-trace …`, no cluster required.

```
controls (goopg's OWN bushy pairings, must all be OFFERED): 2/2
controls set aside as CROSS-QUERY-LEVEL (a SubPlan boundary, not a partition): 1
candidates (PG-only bushy pairings): 2/2 offered by the goopg search
RATCHET enum_controls=2/2 enum_controls_oos=1 enum_candidates_offered=2/2
        enum_candidates_crosslevel=0 enum_problems=3 enum_malformed=0
```

Both partitions §3.11 left open were **offered to `makeJoinRel`** — Q7's
`{customer+lineitem+n2+orders} ⋈ {n1+supplier}` and Q8's
`{lineitem+orders+part} ⋈ {customer+n1+region}`, each at `phase=2` (the bushy
pass), each with `created=false` because another pairing reached that relset
first. goopg's search **can express both shapes and chose otherwise on cost**.
That is the reading §4 admits: clause 6 is discharged as a cost/stats question
and routes to the §4 parity ratchet, not to a search defect. Clause 6 **passes**.

**Two instrument changes this measurement forced.**

*A plan-only mode.* The first arm needed the host to itself for a full power
run, and the host was held by the nightly batch for two loops running. But
every question clause 6 asks — which pairings a plan contains, which the search
was offered — is decided before a tuple moves. `--plan-only` (arm: `PLAN_ONLY=1`)
runs plain `EXPLAIN`, drops §5 and the §4 parity column *by omission rather than
by printing them empty* (a §5 table of `actual=? (no ANALYZE)` rows ends in a
clean verdict, which is the one way the artifact could lie), and finishes in
about four minutes. Because it produces and consumes no timing, it is exempt
from the arm script's nightly-batch refusal — that refusal protects timings, and
this run has none to protect or to spoil.

*Cross-query-level pairings are not partitions.* The first run came back
`VERDICT: HARNESS FAULT` on a control: goopg's own Q20 plan prints
`{nation+supplier} ⋈ {lineitem+part+partsupp}`, but Q20's only traced join
problem is `{nation,supplier}` — the other three relations live under SubPlans,
separate planning contexts. `Spine` reads a pairing at that node because a
printed plan does not mark query levels; no join search of either engine ever
partitioned that relset. Adjudicating it as a control voided a run whose two real
candidates had both come back `OFFERED`.

The fix is not to relax the control guard — a failing in-scope control still
voids the run — but to classify the case, and the classification is
*asymmetric*: as a **control** it is out of scope (goopg's search legitimately
never saw it, and the count is printed, never silently dropped); as a
**candidate** it is a clause-6 **failure**, and a sharper one than
`NOT-ENUMERATED` — a partition PG reached inside one join problem is one goopg
cannot reach *at all* when it did not flatten the sublink into the same problem,
so the shape is unreachable rather than merely unchosen. The discriminator is
positive evidence, not absence: a relation that entered **no** traced problem was
planned somewhere else, whereas a log with no blocks at all is still `NO-TRACE`
and still voids the run.

## 4. The PG plan-shape parity gate (new instrument)

Once the P-PG shape contract holds ([02](02-plan-shape-contract.md) §1),
goopg's join spines are structurally comparable to PG's for the first time.
Add `scripts/pg-plan-shape-diff.sh --strict` (the existing
script is report-only): normalise both EXPLAIN outputs to a join-spine
skeleton — node type, join type, build/probe side (PG: which child is under
`Hash`; goopg: `BuildLeft`), base-rel leaf names — and diff.

- Scope: TPC-H 22 + TPC-DS 99 against PG 18.3 with matched stats
  (both sides ANALYZEd; goopg per-session ANALYZE caveat applies).
- Bar at S5: **report-mode with a pinned mismatch budget** — the count of
  mismatching spines is recorded in the evidence file and must not grow
  across subsequent commits (ratchet). A hard match-all bar is wrong while
  cost constants and stats fidelity still differ; the ratchet makes drift
  visible without blocking on estimator parity.
- **There is no `expected-bushy` category.** goopg implements PG's full
  three-phase search, bushy phase included
  ([03](03-join-search-pg-dp.md) §4.3), so a bushy spine PG chose and goopg
  cannot produce is a genuine divergence, not an accepted trade — it is
  classed per §6 (usually (b) plan shape) and fixed. Spine mismatches driven
  by cost-constant or stats fidelity stay under the ratchet as usual, and
  are re-reviewed at each ratchet update.

**Where the spine diff actually landed (P5.9-l, 2026-08-06):** inside
`cmd/estimate-audit` (`internal/estimateaudit/spine.go`), not in a separate
`scripts/pg-plan-shape-diff.sh`. One arm, one artifact, one PG reference — the
§4 ratchet, the §5 tripwire and the spine diff are three questions about the
same pair of captured plans, and splitting them across two drivers is how the
ratchet and the spine budget would drift apart. Its ratchet line is
`RATCHET spine_pg_only= spine_goopg_only= bushy_pg= bushy_goopg=
clause6_candidates=`; the pinned values are in §3.11.

### 4.1 The ratchet is per-joinrel PARITY, not an absolute factor (P5.6-g-iii, landed 2026-08-05)

§5's absolute tripwire answers "is this estimate good?". The question this
milestone has to answer is "**is this estimate worse than PG's?**". On TPC-H
the two questions disagree on every joinrel the absolute bar flags — measured,
`analysis/leftdeep-joins/2026-08-05-p56giii-parity-README.md` §1:

| joinrel | goopg | PG 18.3 | absolute bar | parity bar |
|---|---|---|---|---|
| Q18 final | 42 837× over | **5 387× over** | violation | PG trips the 10³ tripwire too |
| Q21 final | 4 003× under | **4 178× under** | violation | excess **1.0×** — parity |
| Q19 final | 131× under | 1.0× | *silent* (<10³) | **violation, 126.5× worse** |

The absolute bar flagged the one joinrel where goopg matches PG exactly and
stayed silent on the one where goopg is two orders of magnitude worse. So the
bar P5.9 certifies is:

- **Unit of comparison: the joinrel, identified by its base-relation SET**
  (upstream's `RelOptInfo.relids`), reconstructed from the printed plan. Two
  engines that reach `{customer,orders}` by different join orders still built
  the same joinrel, and its ACTUAL row count is a property of the query and the
  data, not of the plan — so the misestimate factors are directly comparable.
- **Two conditions, both required** (`estimateaudit.ParityBar`): goopg's factor
  exceeds the reference's by more than `Slack` (default 10×, because this §
  already declines a match-all bar while cost constants and stats fidelity
  differ) **and** goopg's own factor exceeds `Floor` (default 100×, so a
  joinrel PG nails and goopg gets within 20× does not enter the ratchet).
- **A joinrel only one engine built is a SHAPE divergence**, counted separately
  and classed per §6 — there is nothing to compare it against. This is the
  spine-mismatch budget above, now countable per joinrel rather than per query.
- The absolute tripwire of §5 **stays**, as a coarse tripwire and as the home
  of the per-query bars (Q9's ≤10², Q21's PG-parity 5 000×).

**Baseline pinned 2026-08-05** (TPC-H 22, LEGACY planner, goopg plans replayed
from the committed P5.6-g capture, PG 18.3 reference captured live on 65432):
`parity_violations=1 shape_mismatches=67`, 21 joinrels matched, 3 ambiguous.
The single violation is Q19 `{lineitem,part}`. **`shape_mismatches` is an upper
bound**: goopg's EXPLAIN does not deduplicate repeated relation names the way
`select_rtable_names` (ruleutils.c) does (`lineitem_1`, `n1`/`n2`), so Q8, Q17
and Q18 lose their final-joinrel comparison to a rendering gap rather than a
planning difference (deferral-ledger row, 2026-08-05).

**Re-pinned 2026-08-05 at P5.9 run 3, now as a two-arm ratchet** (HEAD
`1964333a`, one binary, both arms measured LIVE via the new
`scripts/tpch-estimate-audit-arm.sh`, same committed PG reference):

| arm | `parity_violations` | `shape_mismatches` | joinrels matched |
|---|---|---|---|
| `GOOPG_PGSHAPED_DP=0` | **0** | 67 | 21 |
| `GOOPG_PGSHAPED_DP=1` | **6** | 46 | 32 |

The live flag-OFF arm reproduces the baseline above exactly on shape (67
divergences, 21 matched) and drops its one parity violation: Q19
`{lineitem,part}` now reads `est=286 actual=131` (2.2× over, excess 2.1×)
where the replayed P5.6-g capture read `est=1 actual=131`. The 1-row clamp on
Q19 is gone, so the LEGACY arm carries **zero** parity violations, and that is
what future commits ratchet against on the OFF arm. The ON arm's 6 are §3.5's
estimate collapse and are P5.9-h's to clear; the ON arm's 46 is the first
measurement of the shape claim [02](02-plan-shape-contract.md) makes.

**Amended 2026-08-05 (P5.9-h, §3.6): the ON arm's 6 are CLEARED.** Re-measured
on the five queries that carried them, `parity_violations=0` on both arms, so
the ratchet the ON arm carries forward is **0**, the same number as the OFF
arm, and any future commit that reintroduces one is a regression on either arm.
`shape_mismatches` is unchanged (24 on the five-query subset, both arms) — the
fix moved estimates, not shapes.

## 5. Estimate audit (class-(a) regression tripwire)

Automate the order-attribution methodology: for each TPC-H query, EXPLAIN
(without ANALYZE) at each join level vs actuals from a one-off instrumented
run; flag any joinrel whose estimate is > 10³× off. Q9's chain must show the
[04](04-cost-and-cardinality.md) §3 improvement (≤ 10²× at the final
joinrel). Runs at S5 and on any later selectivity change; output committed
under `analysis/leftdeep-joins/`.

### 5.1 The instrument (P5.6-e-i, landed 2026-08-04)

`cmd/estimate-audit` + `internal/estimateaudit`. One `EXPLAIN ANALYZE` per
query supplies BOTH sides of the comparison (the `rows=` of the cost
parenthetical and the `actual rows=` of the instrumentation), so the "one-off
instrumented run" above is not a separate build:

```
go build -o /tmp/estimate-audit ./cmd/estimate-audit
bench/tpch/setup_goopg.sh                 # cluster on 65433
/tmp/estimate-audit --label <YYYY-MM-DD>-<slug>   # all 22, ~13 min
bench/tpch/stop_goopg.sh
```

It writes `analysis/leftdeep-joins/<label>.txt` (the audit) and
`<label>.plans.txt` (the raw plans, so a later reader can re-derive any row),
and exits 1 when a joinrel is over threshold — instrument and tripwire in one
binary. The unit of audit is the JOINREL: a badly misestimated *scan* is not
a §5 violation, because §5's criterion is stated at join levels.

Three measurement conditions are forced by goopg-specific behaviour, and each
would silently corrupt the audit if left alone:

- **`--serial` (default).** goopg does not propagate worker instrumentation
  out of a `Gather` (upstream merges it in `execParallel.c`
  `ExecParallelRetrieveInstrumentation`), so in a parallel plan every node
  *below* the Gather reports estimates only. Q9 — the query this section
  states its acceptance criterion on — plans entirely below a Gather, so it
  is **unmeasurable in parallel**. The first run of the instrument recorded
  exactly that and is kept as evidence:
  `analysis/leftdeep-joins/2026-08-04-p56e-parallel-uninstrumented.txt`.
  The flag sets `max_parallel_workers_per_gather = 0`; the join tree under
  audit is the same one, minus the Gather.
- **`--warm-stats` (default).** goopg's ANALYZE statistics are
  per-connection and bare `ANALYZE;` is a no-op, so the run holds one
  stats-warmed session (explicit `ANALYZE <table>` per table) for every
  query. Without it the audit measures the no-stats planner.
- **Cumulative, not per-loop, actuals.** goopg's `actual rows=` is a raw
  cumulative counter (`instrumentedOp.stats.rowsOut`), where upstream prints
  the per-loop average. The tool consumes the printed value as-is; a reader
  who assumes PG semantics and multiplies by `loops` inflates every
  nested-loop inner node by exactly the loop count. Ledgered.

Two modes were added by P5.6-g-iii (2026-08-05) and are what make §4.1
runnable:

```
# capture the reference in the same run (PG 18.3 on 65432, bench/tpch/setup_pg.sh)
/tmp/estimate-audit --label <label> --ref-port 65432

# or apply a NEW instrument to OLD committed evidence — no 13-minute rerun
/tmp/estimate-audit --label <label> \
    --from-plans analysis/leftdeep-joins/<earlier>.plans.txt \
    --reference  analysis/leftdeep-joins/<earlier>.pg.plans.txt
```

`--from-plans` replays a committed `.plans.txt` instead of connecting: the
estimator is not consulted, so the replayed audit is bit-identical to the
original run's. A freshly captured reference is written to
`<label>.pg.plans.txt` so the comparison stays re-derivable. The reference is
captured through the same code path as goopg (same queries, same `--serial`,
ANALYZE first) — a reference measured under a different protocol would compare
two protocols rather than two planners.

### 5.2 Baseline, 2026-08-04 (pre-flip, `GOOPG_PGSHAPED_DP` OFF)

`analysis/leftdeep-joins/2026-08-04-p56e-baseline.txt`, all 22 queries, TPC-H
SF=1. Five joinrels over the 10³ tripwire, all but one an OVER-estimate:

| query | joinrel | est | actual | factor |
|---|---|---|---|---|
| Q18 | final (SEMI) | 1 756 987 324 | 70 | 2.5 × 10⁷ over |
| Q19 | final | 43 060 427 | 131 | 3.3 × 10⁵ over |
| Q3 | final | 91 875 163 | 30 401 | 3.0 × 10³ over |
| Q20 | inner (SEMI) | 6 772 315 | 2 568 | 2.6 × 10³ over |
| Q7 | inner (build=left) | 126 | 150 000 | 1.2 × 10³ **under** |

**Q9's final joinrel is 124.7× over (est 39 447 200 vs actual 316 264)** —
just outside this section's ≤ 10² bar, and the number P5.9 re-measures once
[04](04-cost-and-cardinality.md) §3's sizing is on the live path. The shape
of the miss is the compounding §3 exists to end: Q9's three outermost
joinrels all carry the SAME estimate (39 447 200) while the actual collapses
from 5 997 241 to 316 264 across them — two joins that cost nothing in the
estimate.

The violations split into two class-(a) causes, both filed as P5.6-e-ii and
neither fixed here — §6 forbids a constant moving without its class
diagnosis, and these need the diagnosis first:

- **A SEMI/ANTI joinrel is priced at its outer input verbatim.** Q18's final
  SEMI carries the identical estimate to the join beneath it
  (1 756 987 324), against 70 actual rows: the match fraction is not applied
  at all, where `calc_joinrel_size_estimate`'s JOIN_SEMI arm (costsize.c)
  scales the outer's rows by the semi-join selectivity. Q20's inner SEMI
  (2.6 × 10³ over) and Q22's ANTI final (643×, under the tripwire) share the
  shape. Note that Q18's outer is *itself* 293× over — the
  `lineitem ⋈ orders` FK equality priced at 1.76 × 10⁹ against 5 997 241
  actual, which is the eqjoinsel/FK-superkey miss P5.6-a…-c reproduce
  upstream's answer for; the SEMI defect stacks on top of it rather than
  causing it.
- **A joinrel's non-equi restriction contributes no selectivity.** Q19's
  final joinrel is a plain INNER over two *unfiltered* scans (5 997 241 ×
  200 000) whose entire WHERE is one three-branch OR over `part` and
  `lineitem` columns; the plan shows only the `Hash Cond`, and the estimate
  (4.3 × 10⁷) credits the OR nothing, against 131 actual rows. Q3's final
  (3.0 × 10³ over) is the same omission over the three-conjunct `Filter:`
  the plan re-applies at the join.

Two instrumentation gaps the run surfaced, both ledgered and neither fixed
here: the Gather gap above, and Q11's `InitPlan`/`SubPlan` joins, which
report no `actual rows=` even in serial mode.

### 5.3 Both causes closed, 2026-08-04 (P5.6-e-ii)

`analysis/leftdeep-joins/2026-08-04-p56eii.txt`, same instrument and same run
conditions, LEGACY planner still (`GOOPG_PGSHAPED_DP` OFF) — only
`estimateJoin` changed. Provenance and the full before/after table:
`2026-08-04-p56eii-README.md`.

What landed, both in `internal/planner/cardinality.go`:

- **SEMI/ANTI are sized from the OUTER.** `estimateJoin` gained the arms
  `calc_joinrel_size_estimate` has: `outer_rows · jselec` for JOIN_SEMI and
  `outer_rows · (1 − jselec)` for JOIN_ANTI, with the match fraction from
  `eqjoinsel_semi`'s no-MCV branch — `nd1 ≤ nd2 → 1.0`, else `nd2/nd1`, and
  0.5 when either side is a default. `nd2` carries upstream's asymmetric
  clamp to the inner relation's row count (the only pathway by which an
  inner-side restriction reaches a semi/anti estimate; clamping `nd1` too
  would double-count the outer's own restrictions).
- **The non-equi restriction is priced.** The conjuncts of `Predicate` that
  `HashKeys` does not already answer are run through `clauseSelectivity`,
  which required `columnStatsForChild` to resolve a column THROUGH a join
  (`Predicate` is written in the merged left‖right space) and to remap
  through a `Project`'s target list, as its ndistinct twin already did.
  Only conjuncts referencing BOTH sides count: a single-sided conjunct is a
  baserestrictinfo upstream and is already priced into the component rel's
  size, even though goopg also leaves a copy on the join for the executor.

| query | joinrel | §5.2 baseline | now |
|---|---|---|---|
| Q19 | final | 328 705× over | 13.1× under |
| Q20 | final (SEMI) | 891× over | 9.5× under |
| Q21 | final (ANTI) | 499× over | 9.7× under |
| Q22 | final (ANTI) | 643× over | 1.8× over |
| Q4 | final (SEMI) | 485× over | 7.3× over |
| Q18 | final (SEMI) | 2.5 × 10⁷ over | 1.26 × 10⁷ over |
| Q9 | final | 124.7× over | 124.7× over |

Five joinrels remain over threshold, one fewer than the baseline and with no
new ones. Q18, Q3 and Q20's inner SEMI all still fail for a cause §5.2 named
but did not own: their OUTER input is 293× / 5.8× / 86× over on its own.

**Why the third cause is NOT fixed here.** The first cut also corrected the
join-key ndistinct lookup — `RightKey.Index` is a MERGED index and was being
resolved against the right child's own schema, so the right side of an
equi-join never entered `max(nd)` — and let both column lookups resolve
through a join, which is what `examine_variable` does. Measured
(`2026-08-04-p56eii-postfix.txt`), every joinrel it touched got more accurate
and the queries above them got far worse: **Q9's final 124.7× → 176 424×
over, Q8's final 1.9× under → 2 171× over**, with Q9's two deepest joins
landing exactly on their actuals. The missing `nd` was cancelling two
pre-existing defects — ANALYZE storing a SAMPLE distinct count with no
Haas–Stokes scale-up (a 1.5 M-row unique key reads as ≈ 30 000), and the
M0126-0010 `max(|l|,|r|)` cap firing only on the nd-unavailable path, so
supplying `nd` also removes the bound. Per §6 that is a class-(a) diagnosis
with its own mechanism, filed as **P5.6-e-iii**: de-saturate ANALYZE, then
land the coordinate correction with it. The rejected run is committed
because the conclusion is only defensible with its numbers present.

### 5.4 The third cause closed, 2026-08-04 (P5.6-e-iii)

`analysis/leftdeep-joins/2026-08-04-p56eiii.txt`, same instrument and same run
conditions, LEGACY planner still (`GOOPG_PGSHAPED_DP` OFF).

What landed:

- **ANALYZE de-saturated** (`internal/executor/operators_analyze.go`,
  `ndistinctEstimate`). goopg stored the SAMPLE's distinct count as the
  table's ndistinct, so with the default statistics target a 1.5 M-row unique
  key read as ≈ 30 000. `ndistinctEstimate` mirrors `compute_scalar_stats`'s
  ndistinct block (analyze.c:2588-2648) branch for branch: the
  `nmultiple == 0` unique-column arm, the `nmultiple == ndistinct`
  whole-value-set arm, and Haas–Stokes Duj1 `n·d/(n − f1 + f1·n/N)` clamped to
  `[d, N]`. `ColumnStats.NDistinct` and `NDistinctFrac` are now two renderings
  of that one estimate, and `StaDistinct()` picks between them with upstream's
  own 10 %-of-rows rule instead of always preferring the fraction.
- **The join keys resolve in the merged coordinate space**
  (`internal/planner/cardinality.go`). `estimateJoin`'s equi arm reads the
  right key through `rightKeyNDistinct`, the same left-width shift the
  SEMI/ANTI path has used since §5.3, and `columnNDistinctForChild` gained the
  `*Join` arm its `columnStatsForChild` twin already had. The two column
  lookups no longer diverge; the divergence tripwire test is retired.
- **The M0126-0010 cap was re-examined and deliberately left alone** — it
  still fires only on the nd-unavailable fallback path. It is a non-PG
  heuristic standing in for upstream's FK-driven `fkselec`
  (`get_foreign_key_join_selectivity`), and a genuine many-to-many join
  legitimately exceeds `max(|l|,|r|)`. What made it look load-bearing in §5.3
  was the saturated `nd` it was compensating for.

Violations: **5 → 2.** Q3, Q7 and Q20's inner SEMI are closed outright; Q18's
final SEMI improved by 293× and still violates.

| query | joinrel | §5.3 | now |
|---|---|---|---|
| Q3 | final | 2 967× over | 10.4× over |
| Q5 | d4 | 447.7× over | 1.5× over |
| Q7 | d4 | 1 190× under | 1.4× over |
| Q8 | d3 | 20.7× over | 1.3× over |
| Q17 | final | 7.5× over | 1.0× under |
| Q18 | final (SEMI) | 1.26 × 10⁷ over | 42 837× over |
| Q20 | inner SEMI | 1 311× over | 129× over |
| Q16 | final (ANTI) | 16.0× under | 85.1× under |
| Q19 | final | 13.1× under | 131× under |
| Q21 | final (ANTI) | 9.7× under | 4 003× under |

Two regressions came out of it, both filed rather than papered over:

- **SEMI/ANTI collapse to `est=1`** (Q21, Q19, Q16, and Q21's inner SEMI). A
  truthful `nd` makes `eqjoinsel_semi`'s `nd1 ≤ nd2` test succeed, the match
  fraction becomes exactly 1.0, and JOIN_ANTI's `outer · (1 − jselec)` floors
  at `clamp_row_est`'s 1. Upstream reaches 1.0 far less often because it takes
  the MCV branch of `eqjoinsel_semi` first, which goopg has no join-level MCV
  list for.
- **Q9 is UNMEASURED** — it exceeded the audit's 150 s timeout where §5.3
  measured it at 93.9 s. Attributed per §6 before anything landed: the
  ANALYZE half is the whole cause (reverting only the planner half reproduces
  the identical plan shape, `rows=160406045` vs `159924827`). The mechanism is
  a class-(a) defect this change UNMASKED rather than introduced — Q9's
  `l_suppkey = ps_suppkey AND l_partkey = ps_partkey` is a TWO-pair equi-join
  that `estimateJoin` prices on ONE pair while excluding BOTH from the
  residual, so it reads `6 M · 800 k / 10 000 = 481 M` and the DP puts it
  under the `part` filter instead of above it. Pricing every pair the way
  `clauselist_selectivity` does swings it the other way (≈ 2 rows) without
  upstream's FK selectivity to bound it, so the fix is
  `get_foreign_key_join_selectivity`, not a constant. **P5.9 cannot certify
  Q9's ≤ 10² bar until this lands.**

Q9's deepest joins are now exact (`5 997 241` on both), which is the evidence
that the ndistinct itself is right and the remaining error is the multi-key
pricing above it.

### 5.5 The multi-key cause closed, 2026-08-04 (P5.6-f)

`analysis/leftdeep-joins/2026-08-04-p56f.txt`, LEGACY planner
(`GOOPG_PGSHAPED_DP` OFF) as before — but **on a different schema from every
audit before it**, so it is diffed against a re-baseline rather than against
§5.4.

**Step 0: the baseline moved.** M0127-P5.6-f-pre proved goopg's `tpch` database
carried 0 user indexes against the PG 18.3 reference's 16, and its fix is
forward-only. Since half 2 of P5.6-f reads a UNIQUE index, the eight UNIQUE
indexes the reference declares were re-created before anything was measured
(`partsupp_pk (ps_partkey, ps_suppkey)` is the one Q9 turns on), and confirmed
to survive a restart — the first end-to-end validation of the P5.6-f-pre fix on
a real cluster. The eight NON-unique FK indexes were deliberately left out:
they carry no uniqueness evidence and would have moved plan SHAPE inside a
cardinality measurement, which §6 forbids.
`2026-08-04-p56f-baseline-idx.txt` is that cluster with the OLD planner, and it
reports the identical two violations and the identical UNMEASURED Q9 as §5.4 —
so the index creation contributed nothing to the delta below.

What landed (`internal/planner/joinkeyproof.go`, `cardinality.go`):

- **Every equi-pair is priced.** `estimateJoin` charged ONE pair while
  `Join.Residual()` excluded them all, so Q9's second equated column vanished
  from the estimate entirely. The pair list is `joinEquiPairs`, and the same
  list is now what `joinResidualSelectivity` excludes — the two can no longer
  disagree. It is derived from `Predicate` when `Join.HashKeys` is empty, which
  is the state EVERY estimate taken during join-order search sees
  (`fillJoinHashKeys` is one late pass at the tail of `Plan()`).
- **`get_foreign_key_join_selectivity` (costsize.c:5651) for the legacy
  estimator.** The same algorithm as `superkeyJoinSelectivity`
  (joinrelsize.go), arm for arm, over `*Join` nodes instead of `RelOptInfo`s:
  the covered pairs are removed and ONE `1/raw_ntuples` substituted for the key
  as a whole, largest divisor first, whole key or nothing. Half 1 alone prices
  Q9's composite key as `1/(200 000 · 10 000)` — 2 400 rows against 5 997 241
  actual, a bigger error than the defect and in the other direction. This is
  why the item always said the halves must land together.
- **The evidence reaches a catalog-free estimator by being stamped.** A table's
  indexes live only in the catalog, and `estimateJoin` takes a bare `Node`
  (EXPLAIN in the executor calls it too). `SeqScan.UniqueKeys` /
  `IndexScan.UniqueKeys` are stamped at the sites that already stamp
  `SmallDim`, through the planner's own `cat` — which also settles the dbOid
  hazard of cost-model/14 §2 that a bare `InMemory` lookup would reintroduce.
- **The proof resolves each end independently.** Requiring BOTH ends of a pair
  to reach a base relation was the mechanism's first shape and was measured
  wrong: Q20's `partsupp ⋈ (SELECT … GROUP BY …)` has a HashAggregate on one
  side that no resolver sees through, the proof went unmade, and the joinrel
  read 283 against 236 624. The uniqueness argument only ever concerns the KEY
  side. Only the declared-FK arm needs the far end, because it has to name the
  referenced parent.

Violations: **2 → 2** — Q18's final SEMI (42 837× over) and Q21's final ANTI
(4 003× under), both owned by P5.6-g and both untouched. **No joinrel got
worse.**

| query | joinrel | re-baseline | now |
|---|---|---|---|
| Q9 | `lineitem ⋈ partsupp` | 479 779 280 (80× over) | **5 997 241 — exact** |
| Q20 | d3 INNER (`partsupp ⋈ agg`) | 12.2× over | 3.1× over |
| Q20 | d2 SEMI | 125.0× over | 31.7× over |
| Q5 | d6 INNER | 5 996 041 | 5 997 241 — exact |

Everything else moved by under 2 %, which is the residual-accounting half.

**Q9 is measurable again — at 291.8 s, not within the audit's 150 s.** The
sequence is 93.9 s (§5.3) → unmeasured (§5.4) → 291.8 s. Its cardinality defect
is closed and the remaining error is class (b), plan shape: all three hash
joins carry the full 5 997 241 rows because the `part` filter (5.3 % selective)
is applied ABOVE them, where PG filters `part` first and index-scans lineitem
through `lineitem_part_supp_fkidx`.

**Why an exact estimate did not move the shape, and what owns that.** The
legacy planner does not size its join-order search with `estimateJoin` at all.
`estimateJoinCost` (bushy.go:1257) has its own cardinality arm, and its
PRODUCTION branch — the integer DP, `costDrivenJoinOrder` OFF — computes `ndv`
as the maximum NDistinct over *every column of the edge's two tables*, ignoring
the join key. The multi-edge enumeration and superkey probe that do exist there
(`crossEdgesBetween` + `uniqueNoFanoutRawCount`, whose FK arm additionally
divides by the CHILD's count where upstream divides by the parent's) sit in the
`costDrivenJoinOrder` branch that M0126 closed as a no-go and left OFF. So
P5.6-f reaches every printed estimate and every post-search decision, and the
search itself not at all. Filed as **P5.6-f-ii**; P5.9 still cannot certify
Q9's ≤ 10² runtime bar, but it can now certify its cardinality.

### 5.6 The search reached, 2026-08-05 (P5.6-f-ii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56fii{,-halfway}.txt`,
`-README.md`. Same cluster and schema as §5.5, so the diff carries no schema
variable.

§5.5 named one cause — the integer DP's `ndv` being a table-wide maximum. It was
real and it was not sufficient. Instrumenting the DP (rather than reading it)
surfaced two more, both of which had been latent for as long as the accurate
path existed and neither of which any unit test could have caught, because both
concern how a coordinate is *interpreted* rather than what is computed from it:

1. **A `joinEdge`'s key expressions are in GLOBAL FROM-list coordinates.**
   `accurateKeyDistinct` indexed `Stats.Columns` with `ColumnRef.Index`, so on
   Q5 it read out of range for *every* join key (`c_nationkey` is `Index: 16`
   against 8 columns) and returned 0 — silently handing the search back to the
   table-wide maximum it was supposed to replace. On `nation`, `Index: 3` was in
   range and answered with `n_comment`'s distinct count for `n_nationkey`.
   `edgeColName`'s `cr.Name` fallback masked the whole thing by still returning
   the right *name*. Third instance of the P5.6-e-ii `RightKey` class.
2. **`accurateKeyDistinct` bypassed `StaDistinct()`**, multiplying
   `NDistinctFrac × RowCount` unconditionally — the branch P5.6-e-iii created
   `StaDistinct()` to arbitrate (`get_variable_numdistinct`, selfuncs.c).

**The half-fix is recorded because it is the argument for the whole one.**
Landing only the superkey proof made Q5's `lineitem ⋈ supplier` truthful
(39 981 → 5 997 241) while its rival `customer ⋈ supplier` kept reading 10 000
against a real 60 000 000; the DP took the cartesian product and Q5 went 65.9 s →
over the 150 s timeout. A search selects on *comparisons*, so a partially
truthful estimator is not a partial fix — it is a new class-(b) defect. This is
the §6 protocol's own logic applied to the estimator: the class was (b) both
before and after, and only a uniform divisor closes it.

Landed together: one divisor for both search modes (`graphJoinKeyDivisor`, the
graph-space twin of P5.6-f's `superkeyJoinEstimate`, plus the §4 per-clause
product for the unproven remainder); name-based column resolution;
`StaDistinct()` rendering. `uniqueNoFanoutRawCount` is deleted — its FK arm
divided by the child's raw count where costsize.c:5847 divides by the parent's.

Result: violations **2 → 2** (Q18, Q21 — both P5.6-g's), **no joinrel worse**,
and **Q9 measured for the first time**: UNMEASURED (>150 s) → 6.3× over, inside
its `≤100×` override. Runtime, which is what class (b) is judged on: **zero
regressions across 22 queries**, Q5 65.9 s → 17.1 s, Q7 38.9 → 27.2, Q21 125.1 →
90.5, common-measured total 546.8 s → 445.1 s (**0.81×**), and Q9 291.8 s →
16.6 s off-instrument. Plan-gate diverged 19/22 against the old baseline — the
intended outcome — and is 22/22 MATCH against the re-pinned
`plan_snapshots/m0127-p56fii.txt`.

**P5.9 can now certify Q9's ≤ 10² runtime bar as well as its cardinality.**

### 5.7 The semi-join arms completed, and what the two violations actually are, 2026-08-05 (P5.6-g)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56g.txt`, `-README.md`. Same
cluster and schema as §5.5/§5.6.

Landed in `internal/planner/cardinality.go` (+ `joinkeyproof.go` publishing the
whole statistics row): `eqjoinsel_semi`'s **MCV arm** — match the two MCV lists,
take the matched frequency mass as exact, and run the nd heuristic only on the
uncertain remainder with both distinct counts discounted by the match count —
and the **`(1 - nullfrac1)` factor** on every branch including the 0.5 punt.
`CLAMP_PROBABILITY` came with them. 13 tests.

**Result: a measured no-op on TPC-H.** Violations 2 → 2, both bit-identical;
every other joinrel moved under 5 %, in both directions, on INNER joins this
change cannot reach — ANALYZE's sampling noise between runs, which is now the
documented noise floor for reading this instrument.

**A no-op is indistinguishable from a broken wire until you separate them.**
Both halves were probed on a throwaway cluster with the same binary, varying
only the data: a semi-join whose inner has an MCV list estimates **5 010 against
an actual 5 010**, and the identical join whose inner ANALYZE gave no MCV list
(uniform data — upstream discards a list whose values are not more common than
average) estimates **20**. A 25 %-NULL outer key estimates **750 against an
actual 750** where it previously said 1 000. The mechanism works; TPC-H's
near-uniform surrogate keys and NOT NULL join columns cannot exercise it.

**The finding that reframes the milestone: neither violation belongs to this
item, and one of them is not a defect.** Both were re-measured against the PG
18.3 reference (port 65432, same dataset), which nobody had done:

- **Q21's ANTI — PG 18.3 estimates `rows=1` too**, against the same actual of
  4 003. `neqjoinsel` (selfuncs.c) does not price a `<>` clause through
  `eqjoinsel` for `JOIN_SEMI`/`JOIN_ANTI` at all; it returns `1 - nullfrac` by
  documented design. The eq clause is a self-join on `lineitem.l_orderkey`, so
  `nd1 = nd2` and every branch — including the new MCV one, whose
  `uncertainfrac` is then exactly 1.0 — returns 1.0, and `outer · (1 - jselec)`
  floors at one row in both engines. **Closing this would mean diverging from
  PG.** It is an audit-override, not an estimator task.
- **Q18's SEMI is a real divergence of a different mechanism.** PG never plans
  it as a semi-join: `GROUP BY l_orderkey` makes the subquery unique on the
  join key, so PG dedups to a plain inner join and estimates 117 159 (1 674×
  over). goopg keeps the SEMI and lands on the **0.5 punt** — `5 997 241 × 0.5`
  exactly — because `resolveBaseColumn` has no `*HashAggregate` arm and the
  inner's 1 210 559-row estimate is far above `defaultNumDistinct`, so the
  clamp never rescues `nd2`. Neither new arm participates.

**Consequence for §4 and P5.9: at 1 674× PG itself trips this audit's 1 000×
tripwire on Q18.** An absolute factor is the wrong bar for a PG-parity
milestone; the ratchet P5.9 certifies should be per-joinrel parity against the
reference, with the absolute tripwire kept only as a coarse tripwire. Filed as
**P5.6-g-ii** (the `*HashAggregate` arm and Q18's dedup-to-inner shape) and
**P5.6-g-iii** (the Q21 override + the parity bar).

DS05 could not run: the gate self-refuses while the nightly CI batch holds the
host (`FATAL: the nightly CI batch is running`), and the batch was mid-run with
a wedged testport stage for this loop's whole duration. Carried, with the exact
command, in `.ralph/working_set.md`.

### 5.8 The instrument corrected, 2026-08-05 (P5.6-g-iii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giii-parity.txt` (+
`.pg.plans.txt`, `-README.md`). **No estimator code changed**: the goopg side is
the committed P5.6-g capture replayed with `--from-plans`, so every goopg number
is bit-identical to §5.7's. The only new measurement is the PG 18.3 reference.

Landed: Q21's per-query bar beside Q9's (`estimateaudit.Q21AntiJoinMax`, 5 000×,
with its justification rendered into the artifact rather than left as a bare
number), and §4.1's per-joinrel parity gate (`internal/estimateaudit/parity.go`).
Absolute violations on TPC-H **2 → 1**: Q18 stays, Q21 is measured parity
(excess 1.0× against PG's own 4 178×).

Two findings the parity column produced on its first run:

- **Q19 `{lineitem,part}` is the only estimator defect TPC-H can prove**:
  goopg est 1 vs actual 131, PG est 116 vs actual 112 — 126.5× worse than the
  reference, and *invisible* to the absolute tripwire at 131× < 10³. Neither
  scan carries a filter, so Q19's three OR'd conjunction groups all ride as the
  join's residual and the whole predicate is priced at the join level, landing
  on the 1-row clamp. Filed as **P5.6-g-iv**.
- **goopg's EXPLAIN cannot name a repeated relation.** Upstream deduplicates
  printed relation names (`select_rtable_names`, ruleutils.c): a subquery's
  second scan of `lineitem` prints as `lineitem_1`, Q8's two `nation` RTEs as
  `n1`/`n2`. goopg prints the bare name, so two range-table entries are
  indistinguishable in the text and Q8/Q17/Q18 lose their final-joinrel
  comparison to a rendering gap. The gate reports the collision (`~` marker,
  `N ambiguous`) instead of silently picking one; the fix is in the renderer.
  Ledgered 2026-08-05.

Watch list (>10× the reference but under the 100× floor, so not yet ratcheted):
Q16 `{part,partsupp,supplier}` 84.9× vs 2.0×, Q20 `{lineitem,part,partsupp}`
32.1× vs 1.1×, Q14 `{lineitem,part}` 12.4× vs 1.0×.

### 5.9 The Q19 defect closed — a missing preprocessing pass, 2026-08-05 (P5.6-g-iv)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giv.txt` (+ `.plans.txt`,
`-README.md`). Q12 and Q19 measured; see "why only two queries" below.

§5.8 predicted the defect was in how the residual was priced. It was one level
earlier than that: **goopg never ran PG's `canonicalize_qual`**
(`process_duplicate_ors`, prepqual.c), so the OR was never reduced before the
qual was distributed.

Q19's whole WHERE is `(A ∧ …) ∨ (A ∧ …) ∨ (A ∧ …)` where `A` is the join clause
`p_partkey = l_partkey`, repeated verbatim in every arm. Upstream hoists `A` —
along with `l_shipmode IN (…)`, `l_shipinstruct = '…'` and `p_size >= 1`, which
are also in all three arms — leaving `A ∧ (rest₁ ∨ rest₂ ∨ rest₃)`. goopg did
not, with three consequences that compounded:

1. **The join clause was priced twice.** Once as the equi-join key
   (`l·r/nd` = 1/200 000), and again inside each OR arm, where
   `eqOpSelectivity` sees two columns and no constant and returns
   DEFAULT_EQ_SEL. Three arms at ~5·10⁻⁹ apiece drove the product to ~0.1 rows,
   i.e. the 1-row clamp.
2. **Three real restrictions were priced nowhere.** The single-relation
   conjuncts common to all arms stayed trapped inside the OR, so neither scan
   could be filtered and `joinResidualSelectivity` — which correctly skips
   single-sided conjuncts as "already priced at the scan" — had nothing to skip
   and nothing had been priced.
3. **M0058-0004 had already computed the intersection and thrown it away.**
   `commonEquijoinsAcrossOr` (joinorder.go) extracts exactly `A` so the join
   EDGE exists, which is why goopg emitted a Hash Join at all; the qual itself
   stayed opaque. That workaround is the same computation as
   `process_duplicate_ors`, applied to one consumer instead of to the qual.

Landed: `internal/planner/qual_canonical.go` (`canonicalizeQual`, upstream's
`find_duplicate_ors` over goopg's binary AND/OR tree), applied in `planSelect`
at upstream's own placement — after parse analysis, before the qual is
distributed. The parse tree is **not** mutated; it is shared with the view/rule
deparsers, which must keep rendering the query as written.

The equality test is `strictParserExprKey` (exprkey.go), not `parserExprKey`.
That distinction is load-bearing: `parserExprKey` deliberately drops a
ColumnRef's table qualifier (M0097-0003), under which `a.x = 1` and `b.x = 1`
compare equal, and hoisting one of them out of an OR rewrites a qual that admits
rows from either table into one that demands both. Pinned by
`TestCanonicalizeQualDoesNotHoistAcrossTableQualifiers`.

Result — Q19 `{lineitem, part}`:

| | est | actual | ratio | PG 18.3 | excess |
|---|---|---|---|---|---|
| before (§5.8) | 1 | 131 | 131.0× under | 1.0× | **126.5×** |
| after | 309 | 131 | 2.4× over | 1.0× | **2.3×** |

`RATCHET parity_violations=0 shape_mismatches=0`. The plan now shows PG's own
Q19 shape: `Filter: (l_shipmode = ANY …) AND (l_shipinstruct = …)` on the
lineitem scan, `Filter: (p_size >= 1)` on part, the reduced OR at the join.

**Why only Q12 and Q19 were measured.** This pass can only change a query whose
WHERE contains an OR; on every other input `canonicalizeQual` returns its
argument unchanged. Exactly three of the 22 TPC-H texts contain `or`, and Q15's
is `CREATE OR REPLACE VIEW`. Q12 is therefore the control, and it is
bit-identical to §5.7's baseline (1.5× / excess 1.3×; est 45 793 → 46 222 is
ANALYZE sampling noise between sessions) — its OR is a two-arm disjunction of
bare equalities with no common conjunct, so it correctly finds no winners. The
19 OR-free queries were not re-run and are not claimed to have moved; the claim
is that the pass is a structural no-op on them.

**What is deliberately not reproduced.** `find_duplicate_ors` also drops
constant TRUE/FALSE/NULL inputs as it recurses, with different rules for WHERE
quals and CHECK constraints. goopg folds constants in `FoldConstants`, and
duplicating that logic here would give two passes an opportunity to disagree
about three-valued logic. The pass is also applied to SELECT only, not to
UPDATE/DELETE quals (planner.go:9167ff). Both ledgered 2026-08-05.

**Not yet discharged:** the TPC-DS SF0.5 gate. TPC-DS has far more OR-bearing
queries than TPC-H, so it — not TPC-H — is where this pass's plan-shape blast
radius actually gets measured. It self-refuses while the nightly CI batch holds
the host, and is carried on `M0127-P5.6-g-i` together with P5.6-g's own
undischarged sweep. **Discharged 2026-08-05 — see §5.10.**

### 5.10 The DS05 gate for three commits, and which one actually moved the corpus, 2026-08-05 (P5.6-g-i)

Evidence `analysis/leftdeep-joins/2026-08-05-p56gi-*` (README, the two sweep
reports, four whole-corpus plan captures, the capture script).

`scripts/tpcds-sf05-regression.sh sweep` had last run at `ce027cee` (P5.6-f).
Between that report and HEAD sit **three** estimator commits, not the two this
item was filed for — `4b820ab8` (P5.6-f-ii) landed after the baseline sweep and
was never gated either. Arms: **A** `ce027cee` (P5.6-f) → **B** `4b820ab8`
(+f-ii) → **C** `8ce056ff` (+g; g-iii is instrument-only) → **D** `f8338a09`
(+g-iv).

**The gate.** At D the summary is `PASS=94 (57 ck-verified, 37 ck=n/a)
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4` — identical to the A baseline
line for line, including the 57 value checksums and the single TIMEOUT (Q47).
Not one query changed its row count or its checksum. Total sweep seconds
1828 → 1788, but that comparison is **not** claimed: the A sweep ran
01:23→01:59 and the nightly batch fired at 01:43, so its back half carries load
this run did not.

**The blast radius.** `EXPLAIN` (no ANALYZE) over all 99 queries at each arm,
S-cold, one binary at a time. Noise floor measured first — the same binary
captured twice gives byte-identical plans for all 99, so M0125-0047's
plan-snapshot nondeterminism does not contaminate the attribution.

| step | plans changed |
|---|---|
| A→B **P5.6-f-ii** | **74** of 99 |
| B→C **P5.6-g** | **1** (Q83) |
| C→D **P5.6-g-iv** | **4** (Q13, Q41, Q48, Q85) |
| A→D net | 75 (nothing changed and changed back) |

**§5.7's hypothesis is measured false.** The reason this sweep was raised in
priority — TPC-DS has nullable join keys, so `(1 - nullfrac1)` and the MCV arm
should move plan shape where TPC-H showed nothing — does not survive
measurement: P5.6-g moves **one** plan in the corpus. Its estimates move; the
search's *choice* almost never does. The 74-query churn is **P5.6-f-ii**, the
commit that taught the join-order search to read the join key at all (§5.6):
before it the integer DP priced an edge by the maximum NDistinct over every
column of the two tables, so nearly every multi-way join in the corpus was
ordered on a number unrelated to the key being joined on. 74 plans moved, zero
rows moved. That is the strongest available evidence that §5.6's change is a
re-ordering and not a semantic one — and it is evidence P5.6-f-ii shipped
without.

P5.6-g-iv's four are `canonicalizeQual` behaving as specified. Q41 is the pure
case: `(A AND X) OR (A AND Y)` → `A AND (X OR Y)` inside one scan's filter,
nothing else moves. Q13 is the load-bearing one — the three join clauses
repeated in all three OR arms plus `ca_country = 'United States'` are hoisted
out, so the planner sees join clauses where it previously saw an opaque OR and
builds hash joins in place of a nested loop carrying the whole disjunction as a
filter, which is PG's own Q13 shape. 27 of the 99 texts contain an OR (against
2 in all of TPC-H); the pass fires on the other 23 without changing what the
search picks.

**What this leaves open.** The gate has no plan-shape channel: the four captures
above were built by hand for this loop, and a 74-query plan change passed the
gate in silence because it compares row counts and checksums only. That is the
correct *primary* bar — but a search change of this size being invisible to it
is filed as a follow-up under M0127, not fixed here. Q13's new plan also hashes
all 1 920 800 `customer_demographics` rows: free at SF0.5 (20 s → 21 s), and no
SF=1 sweep has run since.

### 5.11 The DS05 gate gets a plan-shape channel, 2026-08-05 (P5.6-g-i-b)

Evidence `analysis/leftdeep-joins/2026-08-05-p56gib-README.md`. Closes §5.10's
"what this leaves open": the 74-query plan change that passed the gate in
silence is now something the gate itself reports.

**The primary bar is untouched.** Row counts and value checksums still decide
pass/fail, and nothing in this channel can change the exit status — verified by
running a sweep with `PLAN_DIFF` pointed at a nonexistent file: the gate still
exits 0 and the report says so out loud. A plan that moves is *information about
a planner change*, not a failure; only rows and checksums are correctness.

**The channel.** `scripts/tpcds-sf05-regression.sh` gained a `plans`
subcommand and a tail stage on `sweep`:

- one `EXPLAIN`-only pass over all 99 queries on a freshly started server,
  written to `plans-<stamp>.txt` beside `sweep-<stamp>.txt` (same stamp — the
  two artefacts of one run pair by name);
- every statement in a file is `EXPLAIN`-prefixed, so Q14/23/24/39's second
  statement is never executed and no query runs for real;
- `scripts/tpcds-plan-diff.py OLD NEW` diffs it per query against the previous
  capture and appends `=== PLAN-SHAPE: queries=99 same=N changed=N … ===` plus
  the changed query list to the report. `--verbose` prints the unified diff.

Three properties make the output readable as signal:

1. **Noise floor zero**, re-confirmed here — three consecutive captures at the
   same commit, `changed=0` each time. `EXPLAIN` without `ANALYZE` emits no
   timings and no actual rows, which is what makes the file byte-stable.
2. **The capture is always the full corpus**, even under `QUERIES=` (which turns
   the *sweep* into a subset probe). A plan file exists to be diffed against
   every other plan file; a subset would report the other 98 as `removed`. The
   full pass costs **14 s**, so there is nothing to save by narrowing it.
3. **The flags line is stamped into the capture**, not only the sweep report. A
   plan diff between two arms run under different planner flags is meaningless,
   and the file has to be able to say which arm it is on its own face.

**Validation — the instrument reproduces §5.10's table without re-running
anything.** The file format is deliberately identical to the hand-rolled
predecessor (`2026-08-05-p56gi-capture.sh`), so the four committed corpus
captures are valid baselines. A capture taken through the new gate path at
`b2d82285` (whose Go code is `f8338a09` verbatim — the two commits since are
docs and CI logs) diffs against them as:

| baseline | changed | expected |
|---|---|---|
| D `f8338a09` | **0** | 0 — same engine code, different harness and directory |
| C `8ce056ff` | **4** — Q13, Q41, Q48, Q85 | §5.10's P5.6-g-iv set, exactly |
| B `4b820ab8` | **5** — + Q83 | + §5.10's P5.6-g set |
| A `ce027cee` | **75** | §5.10's A→D net |

The D row is the load-bearing one: it is the cross-harness compatibility proof,
and it only passes because of one normalisation. psql stamps errors with the
*path* of the script it was reading (`psql:/tmp/xyz.sql:29: ERROR: …`), and
TPC-DS Q36/Q70/Q86 are dsqgen artefacts whose block is an error message rather
than a plan. Before the fix every capture written to a different directory
reported all three as changed — three permanent false positives in a channel
whose entire value rests on a zero noise floor. `tpcds-plan-diff.py`
canonicalises that prefix to `psql:<script>:<line>:` and keeps the line number,
which moves only when the query file itself does.

**Scope.** This channel is goopg-against-goopg over time: it answers "did this
commit move a plan?", not "is the plan PG's". The second question is §4's
per-joinrel parity instrument, and the two are deliberately separate — one runs
on the SF0.5 cluster in 14 s with no PG instance, the other needs the oracle.

### 5.12 What crosses a grouping node, 2026-08-05 (P5.6-g-ii)

Evidence: `analysis/leftdeep-joins/2026-08-05-p56gii{.txt,.plans.txt,-README.md}`,
`-ds05-sweep.txt`, `-plans-{before,after}.txt`.

**The item was filed as the wrong half of itself, and the oracle is why.**
P5.6-g-ii asked for a `*HashAggregate` arm on `resolveBaseColumn`, and §5.7 had
already measured that the arm alone reads *worse* (Q18 2.99 M → 4.84 M). It
reads worse because upstream does not have it. `examine_simple_variable`
(selfuncs.c), inside a subquery RTE, hits `if (subquery->groupClause)`, sets
`vardata->isunique` when the referenced output is the sole grouping column, and
returns — "cannot go further" — *without* a statistics tuple. What crosses a
grouping node upstream is **uniqueness, never a distribution**: grouping mashes
the underlying column's MCV list and histogram beyond recognition, but it
cannot destroy the fact that one row survives per distinct group value. The
consumer is `get_variable_numdistinct`'s `if (vardata->isunique) stadistinct =
-1.0 * (1.0 - stanullfrac)` — a negative `stadistinct` is a fraction of the
relation's rows, and `stanullfrac` is 0 because there is no statistics tuple,
so the answer is the grouped relation's own row count.

Landed accordingly: `resolvesToGroupUniqueColumn` / `groupUniqueNDistinct`
(joinkeyproof.go), consumed **only** by `columnNDistinctForChild`.
`resolveBaseColumn` still has no grouping arm and `columnStatsForChildBase`
still answers nil through one; a test pins that, because handing the base
column's MCV list up would make `eqjoinsel_semi` take its MCV arm on the wrong
relation's frequencies — the P5.6-e-ii defect class in a new place. Upstream's
`list_length(...) == 1` restriction is kept and is load-bearing: with two
grouping columns the pair is unique but neither column is (Q20's
`GROUP BY ps_partkey, ps_suppkey`). The DISTINCT / DISTINCT ON halves of the
same test are the `*Distinct` / `*DistinctOn` arms.

**Q18: 42 837× → 24 242×** (est 2 998 620 → 1 696 939 against an actual 70).
The old number was `5 997 241 × 0.5` exactly — `eqjoinsel_semi`'s punt, taken
because `defaultNumDistinct` sat far below the inner's rows so the nd2 clamp
never fired. Parity excess against PG's own 5 387× drops 8.0× → 4.5×. It
remains this corpus's one absolute violation, and the residual is now
attributable rather than mysterious: goopg's `l_orderkey` ndistinct (~1 210 559)
is *more* accurate than PG's (~339 000, against a truth of 1 500 000), which is
what makes goopg's post-HAVING inner ~3.6× larger than PG's 113 141. Closing
the rest is a HAVING-selectivity problem, not a join-selectivity one.

**`reduce_unique_semijoins` was measured inert, not skipped.** PG's Q18 plan
confirms the SEMI→INNER conversion fires. At goopg's join order it changes no
number: for an inner unique on the join key, `inner_rows` equals nd2, so
`outer · inner / max(nd1, nd2)` and `outer · min(1, nd2/nd1)` agree term for
term. What it buys upstream is join-order freedom — PG joins `orders ⋈ agg`
first (113 141) where goopg joins `orders ⋈ lineitem` first (5 997 241). Ledger
row; deferred rather than guessed, because a goopg SEMI `*Join`'s `Output()` is
left-only and a node-type swap changes the output width of everything above it.

**The defect the arm exposed: `estimateJoin` had no outer-join arm at all.**
LEFT / RIGHT / FULL took the INNER product — upstream's first line for each of
them — and stopped before the second, "the output must be at least as large as
the non-nullable input" (`calc_joinrel_size_estimate`, costsize.c). It was
unreachable while a LEFT join's key resolved to nothing, because the
`defaultEqSelectivity` fallback caps at `max(l, r)`. With the keys resolvable,
TPC-DS Q77's `store LEFT JOIN (… GROUP BY s_store_sk)` estimated 885 rows for a
join whose outer alone is 8 885. `outerJoinRowFloor` is that clamp, RIGHT
included (goopg keeps JOIN_RIGHT where upstream has already commuted it, so its
non-nullable input is the inner).

**DS05: 12 of 99 plans moved, zero rows moved.** `PASS=94 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4`, identical to the `ce027cee` baseline
line for line. Five plans from the grouping arm (joins between grouped CTEs on
the group key, whose estimate goes from `l` to `min(l, r)`), seven from the
floor; Q77 moved under the arm and moved *back* once the floor landed. Stream
2 116 s → 2 074 s with three real wins, all from the floor: **Q80 41 s → 14 s,
Q40 16 s → 2 s, Q78 29 s → 17 s**. This is the first commit whose plan-shape
channel (§5.11) was read *before* the sweep rather than after — the 20 s
capture scoped the blast radius, and caught the Q77 impossibility early enough
that the floor shipped in the same change.

### 5.13 Q18's residual is not a defect, and the instrument was off by one selectivity, 2026-08-05 (P5.6-g-v)

The item asked one question — is Q18's residual the group estimate or the
HAVING selectivity? — and required it be answered by measurement before
anything was touched. One `EXPLAIN` of the bare subquery on each engine
answers it:

| | group estimate | after `HAVING sum(l_quantity) > 313` | factor |
|---|---|---|---|
| PG 18.3 | 339 423 | 113 141 | ÷3 |
| goopg | 1 150 720 | 383 573 | ÷3 |

**Both engines apply exactly the same HAVING selectivity.** `113 141 =
339 423 / 3` and `383 573 = 1 150 720 / 3` are both DEFAULT_INEQ_SEL over an
aggregate for which neither engine has statistics — upstream `cost_agg`
(costsize.c) scales `output_tuples` by `clauselist_selectivity(quals)` and
goopg's `*Filter` wrapper over the `*Aggregate` does the identical thing.
There is no HAVING defect.

The entire 3.39× gap is the **group estimate**, and it is the direction the
item warned about: goopg's `l_orderkey` ndistinct (1 150 720) is *more*
accurate than PG's (339 423) against a truth of 1 500 000 — PG is 4.4× LOW.
Q18's inner is larger than PG's *because goopg's statistics are better*.
Closing the parity gap here would mean deliberately degrading ndistinct
accuracy, so **P5.6-g-v closes with no estimator change**: Q18's standing
audit violation is inherent to pricing an aggregate with no statistics, a
defect goopg shares with upstream and is only visibly worse at because
upstream's compensating ndistinct error happens to point the other way.

**What the measurement did find.** Reading the two EXPLAINs side by side
exposed a defect in the instrument itself. goopg keeps a qual and the rows it
filters on two different plan nodes: the predicate lives on a `*Filter`
wrapper which `walkPlanFiltered` (operators_explain.go) collapses onto the
child below it, so the rendered node set matches PG's. The collapsed line
kept printing `EstimateRows(child)` — the **PRE-qual** count — beside a
`Filter:` detail the estimator had already applied. Upstream has no
equivalent gap because the two live on one struct:
`set_baserel_size_estimates` stores `rel->rows` already scaled by
`clauselist_selectivity(baserestrictinfo)`, and `cost_agg` sets `path->rows`
only after the HAVING scaling.

The estimator was always right — a *parent* node reads
`EstimateRows(*Filter)` and sees the filtered count, which is why a `Gather`
above a filtered scan reported the correct number while the scan under it did
not. Only the collapsed line lied, by exactly the filter's selectivity:

| | before | after | PG 18.3 |
|---|---|---|---|
| `lineitem WHERE l_shipdate <= '1994-01-01'` | 5 997 241 | 1 689 312 | 1 673 754 |
| `nation WHERE n_regionkey = 1` | 25 | 4 | 5 |
| TPC-DS `date_dim WHERE d_year = 2000` | 73 049 | 365 | 365 |

This is a **P5.6-g-iii-class instrument defect, not a cosmetic one**. Both
acceptance instruments read that field: `internal/estimateaudit` parses it
with `nodeLineRe` (audit.go), and §5.11's DS05 plan-shape channel captures
it. Every filtered base relation in every capture taken before this commit
reports its unfiltered size. Conclusions drawn from plan *text* about how
large goopg believes a filtered relation to be — the M0125-0026 ledger row's
"`date_dim` is costed at 73 049 rows" among them — were reading the renderer,
not the estimator; C2's qual *placement* finding is unaffected (the predicate
genuinely renders above the scan), but the row-count half of that evidence
was an artifact. **↳ NARROWED by §5.14 — that last clause is too broad: the
artifact reading only applies to a line that carries a collapsed `Filter:`,
and C2's cited `date_dim` scans do not. Read §5.14 before re-using this
paragraph.**

**Gates.** UNITS green. Audit: 1 violation (Q18), unchanged from the
`p56gii` baseline — every joinrel diff is sub-1 % ANALYZE sampling noise
(316 634 → 311 456, ratios 10.4×→10.2×, 6.2×→6.4×) and no joinrel moved to a
worse class; join nodes carry no collapsed `*Filter` in TPC-H, which is why
the fix leaves them untouched. DS05 `plans`: **95 of 99 captures changed, and
with `rows=` normalised the diff is 6 lines — a psql column-width header, and
nothing else.** Zero structural movement across the corpus, which is the
proof the change is confined to rendering: it cannot reach plan selection.
3 regression tests (`explain_collapsed_filter_rows_test.go`), each verified
to fail without the fix (1000→100, 10→3).

### 5.14 Which pre-fix plan-text conclusions survive, 2026-08-05 (P5.6-g-vi)

§5.13 corrupted the evidence base retroactively: every capture under
`analysis/` taken before `20e17fa5` reports some node lines at their pre-qual
row count. This section is the re-read — how wide the damage is, and which
closed findings quoted the damaged field. Full working:
`analysis/leftdeep-joins/2026-08-05-p56gvi-README.md`. **No code changed.**

**The blast radius, measured.** The two DS05 corpus captures that bracket the
fix are line-aligned (5 962 lines each), so a positional diff isolates exactly
what moved. Of **3 283** node lines carrying `rows=`, **836 changed (25.5 %)**,
across **96 of 99 queries** — and the split is clean:

| population | count | changed |
|---|---|---|
| node lines carrying a `Filter:` detail | 966 | **836** |
| node lines with **no** `Filter:` detail | 2 317 | **0** |

**So the rule for reading any pre-`20e17fa5` capture is exact: a `rows=` is
trustworthy iff its node line has no `Filter:` detail beneath it.** Nothing
without a `Filter:` moved anywhere in the corpus. (The 130 `Filter:`-carrying
lines that did not move are details that never came from a collapsed
wrapper — a scan's own qual field, `Filter: (true)` on a CTE scan, an index
recheck.) Where the field is wrong it is badly wrong: overstatement **median
9×, p90 18 000×, max 1 920 800×**. And the rule covers **join nodes**, not
only scans — Q1's `Hash Join (INNER) … Filter: (date_dim.d_year = 2000)` went
`rows=716` → `rows=3`. §5.13's "join nodes carry no collapsed `*Filter`" holds
for TPC-H only; TPC-DS is where the join-level qual placement of C2 lives.

**Verdicts.** Every closed finding whose reasoning quotes a scan or aggregate
row count from plan text was checked against the capture it cites:
**M0125-0026 C2** (pervasive form and the Q5 form), **M0125-0038 (C5)**,
**M0125-0040 (C6)**, **M0125-0031**, and the `estimate-audit` joinrel
conclusions of §5.3–§5.12 — **all survive on their own evidence; none needs
re-deriving.** The audit conclusions survive by direct re-measurement (§5.13's
run: 1 violation, unchanged); the rest survive because the lines they quote
are bare. That is not luck: C2, C5 and C6 are *about* relations goopg failed
to filter, so the numbers they quote are the ones the renderer had no filter
to mis-scale.

**One correction owed, and it runs the other way.** §5.13 called the row-count
half of M0125-0026's "`date_dim` is costed at 73 049" an artifact. It is not.
C2's own measurement is that **66 of 68** qualifying `Filter:` lines sit on a
join node, so the `date_dim` scans carry no filter and 73 049 is what the
estimator genuinely used — the row-count claim is faithful for exactly the
reason the placement claim is true. The two corrupted captures are C2's two
named exceptions (the scalar-SubPlan `date_dim` scans in Q14/Q54, printing
`rows=73049` beside a scan-level `Filter:`), and they are cited only as
placement exceptions; no conclusion rests on their row counts.

**C5 corroborates the fix rather than falling to it.** C5 read
`359 432 (web_sales) × 365.25 (date_dim after d_year) = 131 280 738` against a
rendered `rows=131280740`. But 365.25 appears nowhere in that plan text — the
`date_dim` line reads 73 049. It is `73 049 × 0.005` (`DEFAULT_EQ_SEL`), which
C5 recovered by *dividing* the join estimate. C5 therefore observed, without
naming it, precisely what §5.13 later proved: the estimator was carrying the
post-qual number all along and only the rendered line disagreed.

Pre-fix captures are **not** being re-captured — they stay as the historical
record. Apply the rule above when reading one.

### 5.15 The DS05 TIMEOUT did not hop, it was moved, 2026-08-05 (P5.6-f-iii)

Working notes + full evidence: `analysis/m0127-p56fiii/README.md`.

P5.6-f-iii was filed on the reading that the SF0.5 gate's single TIMEOUT
"hopped from Q72 to Q47 (2026-08-04), unattributed", provisionally blamed on
the documented sweep-tail confound. **That reading is refuted.** The move is a
real re-pricing introduced by **`ce027cee` (P5.6-f)**.

**The gate's summary line cannot distinguish the two.** `TIMEOUT=1` is
invariant to *which* query timed out, so a re-pricing that trades one timeout
for another is indistinguishable from noise at the summary level. Across the
boundary the summary was byte-identical (`PASS=94 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=1 SKIP=4`) while four queries moved by 4–17×.

**Evidence, in the order it decides the question:**

1. *Not noise — a step function.* Eight consecutive sweeps hold the old regime
   (Q47 ≈30 s, Q72 timeout), four hold the new (Q47 timeout, Q72 ≈166 s),
   within-regime spread ±3 s. A GC/state confound is unrepeatable; this is not.
2. *The confound is structurally inapplicable to Q47.* Q47 runs at position 47,
   **before** Q72: in the old regime no timeout had yet occurred, and in the new
   regime Q47 is itself the first. Nothing preceded it to thrash the heap. And
   a *fresher* post-restart server cannot explain Q57 getting 5× slower.
3. *Solo runs reproduce the new regime.* Quiet host, fresh S-cold server,
   `TIMEOUT_SEC=900` to recover the true runtime instead of the clipped cap:
   **Q47 523 s**, **Q57 81 s**, both with correct row counts. The confound
   hypothesis predicted Q47 ≈ 31 s.
4. *Bisect on a copy of the cluster*, so the live SF0.5 dir was never at risk
   and code was the only variable: `30293f78` → 31 s, `29daeb72` → 30 s with a
   **byte-identical plan**, HEAD → 523 s. Old binary on *today's* data is fast,
   which exonerates the cluster data as well as `29daeb72`.
5. `29daeb72..ce027cee` is **one commit**, P5.6-f. P5.6-g-i's four corpus
   captures corroborate for free: Q47's degraded top join is already present in
   A=`ce027cee`.

**Why the boundary sweep is mislabelled `29daeb72`:** its header carries
`build: rebuilt from tree [tree DIRTY in Go sources — the binary is not this
commit alone]` and `diff=129e691bd41a`. That binary was `29daeb72` **plus
uncommitted P5.6-f WIP**. The harness printed the warning and a content hash;
the warning was correct and was not read. **When attributing any sweep, read
the `diff=` field before the commit subject.**

**Mechanism.** Q47's outermost join carries five equi-pairs
(`i_category`, `i_brand`, `s_store_name`, `s_company_name`, `rn = rn-1`).
P5.6-f moved pricing from one pair to every pair
(`internal/planner/cardinality.go:457-483`), folding them under
**independence** — `sel /= pairNDistinct(...)` multiplied across all pairs.
Two of the five are strongly correlated (`i_category`↔`i_brand`,
`s_store_name`↔`s_company_name`), so the joinrel is under-estimated by orders
of magnitude, a tiny inner estimate makes a nested loop look free, and the plan
degrades from a 5-pair **Hash Join** to a **Nested Loop with no join
condition** — quadratic on real CTE volume. The same fold pointed the right way
is why Q9/Q72/Q53 improved. This is the **inverse** of the single-key
degeneracy trap: the fix for under-pricing over-corrected into
under-estimation. Only structural facts (join method, `Hash Cond` arity) are
cited here — per §5.14 the `rows=` on `Filter:`-carrying lines is not evidence.

**P5.6-f stays.** It is a net win (+Q72, +Q53, +Q9's exact joinrel) and
correctness never moved.

> **⚠ Corrected by §5.17 (2026-08-05).** This section originally closed by
> attributing the regression to a missing **functional-dependency arm**, on the
> reading that `clauselist_selectivity` (`clausesel.c`) consults
> `dependencies_clauselist_selectivity` / `statext_clauselist_selectivity`
> before multiplying. **That is false for join clauses**:
> `clauselist_selectivity_ext` gates the whole extended-statistics branch on
> `find_single_rel_for_clauses`, which returns `NULL` as soon as any clause has
> two relids. PG multiplies multi-pair join clauses blind, exactly as goopg
> does — and, measured, PG estimates Q47's two correlated 5-pair joins at
> `rows=1` itself. The successor **M0127-P5.6-f-iv** filed from this paragraph
> is refuted in §5.17, which names the divergence that *is* real. Everything
> above this box (the attribution to `ce027cee`, the bisect, the plan
> degradation) stands unchanged.

**Gate lesson (actionable):** the plan-shape channel (§5.11) catches plan
drift, but a *named-victim* timeout comparison would have caught this on the
night it landed. The sweep report should diff the TIMEOUT **set**, not its
cardinality. Landed as §5.16.

### 5.16 The DS05 gate diffs the TIMEOUT set, 2026-08-05 (P5.6-f-v)

Discharges §5.15's gate lesson. The counts in `=== SUMMARY: … TIMEOUT=1 … ===`
cannot say *which* query owns them, which is exactly how a 17× re-pricing of
Q47 hid behind four byte-identical summary lines. The sweep now also diffs its
own **per-query status/runtime vector** against the previous report and prints
what moved, by name:

```
TIMEOUT    +Q47  -Q72
SLOWER     Q57 15s->81s (5.4x)  …
```

**The channel.** `scripts/tpcds-sweep-diff.py OLD NEW`, wired into `sweep` as a
tail stage directly under the SUMMARY (before the slower plan pass), plus a
`delta [OLD [NEW]]` subcommand that runs nothing and costs nothing. Like §5.11
it is **non-blocking** — performance never fails this gate, only rows and
checksums do — and like §5.11 its baseline is chosen *before* the new report
exists so it cannot diff a run against itself.

Four deliberate limits, printed in the channel's own header every run so a
quiet delta is never read as a stronger claim than it is:

1. **The input is the report itself**, in the format `cmd_sweep` already
   printf()s. No new artefact, no new format — and therefore every one of the
   ~90 reports already archived under `tpcds-results-sf05/` is a valid
   baseline, retroactively.
2. **Both arms compare the intersection.** A query absent from one side (subset
   probe, or a corpus that grew) has no verdict there; counting it as "left the
   PASS set" made every full-vs-probe pair scream. What is missing is named once
   as `ONLY-OLD` / `ONLY-NEW`.
3. **TIMEOUT readings are excluded from the runtime arm** — a clipped query
   reports the cap, not its runtime (§5.15's `TIMEOUT_SEC=900` probe is the
   whole reason that distinction matters). Verdict changes already name it.
4. **A runtime move needs ≥2× *and* ≥5 s on the larger side.** Reports carry
   integer seconds, so 1 s → 3 s is "3×" and is noise. Both thresholds are CLI
   flags and both appear in the summary line.

The default baseline additionally **skips SUBSET PROBES**: a probe is stamped
"NOT a gate result" and covers a handful of queries, so diffing a full sweep
against one would compare 3 queries and stay silent about the other 96. The
newest *full* report is the last comparable gate run.

**Validation — replay, not a new measurement.** The tool was run over all 87
adjacent pairs of the archived corpus: zero crashes, zero parse failures, and
**17 pairs report a verdict change**. On the pair that motivated it —
`sweep-20260804-214607` → `-232914`, the two reports whose SUMMARY lines are
byte-identical — it prints `TIMEOUT +Q47 -Q72`, `PASS +Q72 -Q47`, and
`SLOWER Q57 15s->81s (5.4x)`: both §5.15 victims, named, from artefacts that
already existed on the night it landed. A full gate sweep at HEAD then ran the
channel live (`sweep-20260805-090258`, PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=1 SKIP=4): `verdict-changes=none`, one runtime move (Q83 7 s → 3 s) —
the correct reading for a harness-only commit.

**What this does not do.** It compares *adjacent* runs, so a regression that
creeps in below 2× per run stays unnamed, and it still cannot *fail* the gate on
a traded timeout — it only makes one impossible to miss in the report. Making a
named-victim regression fatal needs a curated per-query budget file, which is
not filed: the timeout set is still moving under active planner work.

### 5.17 PG has no functional-dependency arm for join clauses, 2026-08-05 (P5.6-f-iv)

Working notes + full evidence: `analysis/m0127-p56fiv/README.md`.

§5.15 attributed Q47's 31 s → 523 s regression correctly to `ce027cee`, then
filed the wrong repair: **M0127-P5.6-f-iv**, "damp correlated equi-pairs the way
`dependencies_clauselist_selectivity` does". Read against the oracle, that
resume point is refuted on two independent grounds.

**1. The upstream citation does not apply to join clauses.**
`clauselist_selectivity_ext` (clausesel.c) gates extended statistics on
`find_single_rel_for_clauses`, and that helper returns `NULL` the moment a
clause carries two relids:

```c
	if (!bms_get_singleton_member(rinfo->clause_relids, &relid))
		return NULL;		/* multiple relations in this clause */
```

A join clause has two relids by construction, so
`statext_clauselist_selectivity` — and `dependencies_clauselist_selectivity`
behind it — **never runs on a join clause list**. Extended statistics are a
*restriction-clause* mechanism upstream. What PG has left for a multi-pair join
is per-pair `eqjoinsel` multiplied blind, minus whatever
`get_foreign_key_join_selectivity` removed — which is exactly the shape P5.6-f
landed. Implementing the filed item would have moved goopg **away** from PG
while citing PG for it.

**2. Measured: PG collapses the same join.** Plain `EXPLAIN` of `query47.sql`
verbatim against the PG 18.3 SF0.5 oracle (:65438, `tpcds05`) gives `rows=1` on
*both* correlated 5-pair joins — `Merge Join … rows=1` over an inner
`Merge Join … rows=1`. The collapse is not the divergence.

**What differs is the size of the join's INPUTS.** Same query, same scale:

| node | PG 18.3 | goopg `096d3949` |
|---|---|---|
| `CTE Scan on v1` | **7 643** | **18** |
| top join estimate | rows=1 | rows=1 |
| top join method | **Merge Join** | **Nested Loop** |

PG refuses the nested loop from an estimate of 1 because rescanning a
7 643-row CTE per outer row is expensive. goopg accepts it because its inner is
18 rows. The plan flip is downstream of a **425× under-estimate of the `v1`
subtree**, and that under-estimate predates P5.6-f (`30293f78` carries the same
18 and still picks the Hash Join) — P5.6-f only tipped an already-mispriced
comparison.

**The 425×, isolated.** goopg's `HashAggregate rows=18` is `child/2` over a
`Hash Join rows=36`, so the error is in that join. Holding the four-table join
fixed on the live SF0.5 cluster and varying only the `date_dim` restriction:

| restriction | `date_dim` scan | join below `store` | join **above** `store` | extra factor |
|---|---|---|---|---|
| *(none)* | 73 049 | 1 439 608 | 1 439 608 | **1.0** |
| `d_year = 2000` | 365 | 7 193 | 35 | **≈ 1/205** |
| `d_dom = 15` | 365 | 7 193 | 35 | **≈ 1/205** |
| Q47's three-branch OR | 368 | 7 252 | 36 | **≈ 1/201** |
| `d_year > 1999` | 36 889 | 726 987 | 367 128 | **≈ 0.505** |

`store.s_store_sk` is unique over a 12-row relation, so that join is
row-preserving by construction and must return its left input unchanged. The
extra factor instead reproduces, per row, **the selectivity the `date_dim` scan
had already applied** (0.505 for the inequality; 365/73 049 = 0.005 for each
equality). With no restriction present it is exactly 1.0. That is a
double-count of a pushed-down baserestrictinfo — the trap
`joinResidualSelectivity`'s own header says it prevents — and PG does not do it
(its `store` join is 2 583 → 2 465).

**Ruled out, so the next loop does not re-walk it.** `joinResidualSelectivity`'s
guard (`exprSide(c, leftWidth) != sideMixed → continue`) is *correct in
isolation*: a throwaway probe gives `sideLeft` for `col = const` and `sideMixed`
for `col = col` across the width, so a one-sided conjunct is skipped there. The
leak is elsewhere in `estimateJoin`'s pair list — most likely
`splitAllEqualitiesForHash` admitting a `col = const` restriction as an
equi-*pair*, though the `d_year > 1999` row (factor 0.505, which is neither
`1/nd` nor `defaultEqSelectivity`) shows that arm cannot explain every row.

> **⚠ That last paragraph is wrong; corrected by §5.18 (2026-08-05).** The leak
> is not in `estimateJoin` at all — instrumenting the live server shows
> `estimateJoin` returning the *correct* 726 987 for the `store` join while
> EXPLAIN printed 367 128. The measurement in the table above stands; only its
> attribution to the pair loop was wrong. §5.18 names the real node.

**Successors.** **M0127-P5.6-f-vi** (the double-charge, with a unit test as its
discriminating instrument — build a join whose left is a `*Filter` over a scan
with the same conjunct still in `Predicate`, assert the estimate equals the
unfiltered one scaled once) and **M0127-P5.6-f-vii** (`estimateAggregate`'s
`child/2` vs upstream `estimate_num_groups`, a second and independent gap on the
same path, *not* load-bearing for Q47 and therefore not to be folded in).
Correctness never moved in any regime: Q47 returns its 100 oracle rows
throughout. 2 ledger rows.

### 5.18 The double-charge is a duplicated qual, not a mispriced join, 2026-08-05 (P5.6-f-vi)

§5.17 measured the defect correctly and located it wrongly. Instrumenting the
running SF0.5 server settles it in one line: for the `store` join of §5.17's
probe under `d_year > 1999`,

```
ESTJOIN l=726987 r=12 sel=0.0833 resid=1 pairs=1 rows=726987 bound=726987
```

`estimateJoin` returns **726 987 — the correct, row-preserving answer** — and
EXPLAIN prints 367 128. Its pair loop, its residual guard and
`splitAllEqualitiesForHash` are all exonerated; the two candidates §5.17 left
open are both dead. (A synthetic unit-scale rebuild of the same three-node tree
also returns the correct number, which is why the prescribed unit test had to be
written against the *plan the planner actually builds*, not against a
hand-assembled join.)

The factor is applied one node higher. The same probe traced through the
`*Filter` arm of `EstimateRows`:

```
ESTFILTER childType=*planner.SeqScan child=73049 sel=0.505 leafLocal=true  pred=&{pos:173 Op:> …}
ESTFILTER childType=*planner.Join    child=726987 sel=0.505 leafLocal=false pred=&{pos:173 Op:> …}
```

**The same source conjunct — same `pos` — is priced by two different `*Filter`
nodes.** `pushSingleSideQualsIntoInnerJoinInputs`
(`internal/planner/inner_join_qual_pushdown.go`, M0125-0004) copies a
single-relation restriction from the residual Filter down onto the relation it
references and **deliberately leaves `f.Predicate` untouched** — its documented
"property 2", which is what keeps the join's own residual evaluation correct.
The estimator then charged both copies, so the restriction's selectivity was
squared and the surplus landed on the join.

Upstream has no such node pair to reconcile: `distribute_restrictinfo_to_rels`
(`optimizer/plan/initsplan.c`) **moves** a single-relation clause into that
baserel's `baserestrictinfo`. `set_baserel_size_estimates` prices it once, and
the joinrel above never sees it — which is exactly the invariant
`calc_joinrel_size_estimate`'s opening comment asserts ("we are not
double-counting them because they were not considered in estimating the sizes of
the component rels").

**The fix.** goopg cannot move the clause, so it records the duplication:
`Filter.PushedBelow` lists the conjuncts a placement pass copied downward, and
`filterSelectivity` (`cardinality.go`) skips them. Splitting the predicate to do
so is not a second formula — `clauseSelectivity`'s `OpAnd` arm is itself
`left × right` under the independence assumption — so an empty `PushedBelow`
reproduces the former number bit for bit. Both duplicating passes are stamped,
per the sibling-paths rule: the binary-join arm (`pushInnerJoinInputQuals`) and
the MHJ arm (`pushResidualQualsIntoMHJTables`), whose header states the identical
"property 2". The two *moving* siblings — `pushOuterQualsIntoLaterals`
(pushdown.go, rewrites `f.Predicate`) and `pushSingleSourceFiltersIntoMHJTables`
(mhj_input_rewrite.go, consumes `mh.Filters`, which no estimator reads) — were
checked and need no stamp.

**Measured after (same probe, same cluster):**

| restriction | `date_dim` scan | join below `store` | join **above** `store` | extra factor |
|---|---|---|---|---|
| *(none)* | 73 049 | 1 439 608 | 1 439 608 | 1.0 |
| `d_year = 2000` | 365 | 7 193 | **7 193** | **1.0** |
| `d_year > 1999` | 36 889 | 726 987 | **726 987** | **1.0** |

Q47's `v1` subtree moves **18 → 3 626 rows** against PG's 7 643 — the same order
at last, and the residual gap is §5.17's separately-filed `estimateAggregate`
`child/2` (P5.6-f-vii), not this.

**Scope of the change, and what it did *not* buy.** This under-sized *every*
join above *every* pushed-down restriction, so it is broad, not Q47-local: the
SF0.5 gate reports **50 of 99 plan shapes changed** where the previous sweep
reported 0. It bought no verdict change — the named TIMEOUT set is still exactly
`{Q47}`, PASS=94 / MISMATCH=0 / CKMISMATCH=0 / ERROR=0, byte-identical
per-query verdicts to the `f05b5329` baseline. Q47's own plan still takes the
nested loop. Stated plainly because §5.17's chain predicted the estimate fix
would be *necessary*, not that it would be *sufficient*, and only the first half
is now evidence.

**The estimate audit is the load-bearing check here, not a formality.** The fix
*removes* a downward correction, so every affected joinrel estimate moves UP —
and §5's TPC-H corpus is dominated by OVER-estimates (Q3's `d2` sat at 10.2×
over). A broad upward shift could therefore have pushed a joinrel through the
10³ tripwire. It did not:
`analysis/leftdeep-joins/2026-08-05-p56fvi-postfix.txt` vs the `p56gv-postfix`
baseline shows per-joinrel moves of **±1–4 %** and no new violation. The corpus
keeps its single standing violation, Q18's final SEMI, which *improved*
25 182× → 23 433× (still the aggregate-result-statistics gap ledgered under
P5.6-g-v, not a joinrel-sizing defect). TPC-H barely moves because its
single-relation restrictions mostly reach their leaves through Slice A
(`attachRelationLocalFilters`), which partitions them out pre-DP and never
leaves a duplicate; `pushInnerJoinInputQuals` is the TPC-DS-shaped path.

### 5.19 `estimate_num_groups`, and the end of the DS05 TIMEOUT set, 2026-08-05 (P5.6-f-vii, closing -f-viii)

`estimateAggregate` answered `child / 2` for any GROUP BY that was not a single
bare ColumnRef, and for the one that was, the column's whole-table NDistinct
with **no clamp at all**. Both halves are now `estimateNumGroups`
(`internal/planner/cardinality.go`), which is `estimate_num_groups`
(`utils/adt/selfuncs.c:3449`): unique variables per grouping expression, the
per-relation product of their distinct counts clamped to that relation's
`tuples` (÷ 10 when more than one variable, never below the largest single
ndistinct), the Yao/Dell'Era correction for a relation the plan RESTRICTED, the
product across relations, and the closing clamp to `input_rows`.

**The item was filed as explicitly NOT load-bearing for Q47.** It is what
closed it. Q47's `v1` body is a 6-key GROUP BY over 7 252 rows; the two numbers
that moved are one line apart in the plan:

| node | before (`child/2`) | after (`estimate_num_groups`) | PG 18.3 |
|---|---|---|---|
| `HashAggregate (6 keys)` (the `v1` body) | 3 626 | **7 252** | 7 643 |
| `CTE Scan on v1` (post `d_year = 2000 AND avg_monthly_sales > 0`) | 6 | **12** | — |
| top block | `Nested Loop rows=1958` over `Hash Join rows=108` | **`Hash Join (INNER, build=left) rows=7252`** | Merge Join |

There is no new formula behind the 7 252: six grouping keys over 7 252 input
rows cannot produce more than 7 252 groups, so the answer is the `input_rows`
clamp, and halving it was arbitrary. Halving it twice — the CTE body is scanned
three times (`v1`, `v1_lag`, `v1_lead`) — is what made the 6-row outer look
like a free rescan driver, which is precisely the resume point -f-viii had
written down ("the `CTE Scan on v1 rows=6` outer is what makes rescanning 3 626
rows look free"). The nested loop is gone and **Q47 completes in 12 s against
its 300 s timeout, 100 rows, matching the PG oracle.**

**The DS05 named TIMEOUT set is now empty** — `TIMEOUT=0`, PASS 94 → **95**,
`MISMATCH=0 CKMISMATCH=0 ERROR=0`, the first sweep since §5.15 with no timing-out
query at all. Per §5.16 the delta is stated by NAME: `PASS +Q47 / TIMEOUT −Q47`,
59 of 99 plan shapes changed, no other verdict move.

**Estimate audit** (`analysis/leftdeep-joins/2026-08-05-p56fvii.txt`, vs the
`p56fvi-postfix` baseline): no new violation. Every TPC-H joinrel moves under
1 % except Q20, which *improves* — its `d2` SEMI 77 462 → 63 875 (30.2× → 24.9×
over) and its `d3` INNER 715 931 → 561 004 (3.0× → 2.4× over), because Q20's
inner aggregate no longer feeds a halved row count into the joins above it.
Q18's standing final-SEMI violation improved again, 23 433× → 23 015×.

**Four upstream refinements are deliberately absent**, each ledgered rather than
faked, because each needs machinery goopg's planner does not have at estimate
time: the equivalence-class de-duplication of step 3 (no EC structure),
`estimate_multivariate_ndistinct` (no extended statistics), the boolean
short-circuit (`exprType` is not available in this package — a boolean *column*
still answers 2 through its own ndistinct, only boolean *expressions* fall to
the default), and the volatile-grouping-expression arm that returns
`input_rows`. `estimateSetOp`'s non-ALL `/2` is left alone for the same reason
§5.18's change was measured alone: the set-op's output columns are not carried
as grouping expressions over each input, and wiring it would put a second
variable into this sweep.

### 5.20 Q17 never hung — a Gather swallowed its error, 2026-08-05 (P5.9-e)

Q17 at flag ON was carried for two loops as ">1200 s at 3.8 % CPU vs 20.93 s
flag-OFF, cause unidentified", with a standing hypothesis that the boundary
rotation fed the hash join a rotated key column and degenerated it to one
bucket. **The hypothesis is refuted and the runtime figure was never a runtime
figure.** Re-measured on the P5.9-c engine, profiled rather than re-EXPLAINed
per the item's own instruction (`analysis/leftdeep-joins/2026-08-05-p59e-q17-hang.txt`):

| t | RSS | CPU since previous sample | goroutines | statement goroutine |
|---|---|---|---|---|
| 60 s | 8.8 GB | 10 % | 19 | `gatherOp.Close` → chan receive |
| 180 s | 8.8 GB | **0.8 %** | 19 | `gatherOp.Close` → chan receive |

Not one worker goroutine was alive, RSS did not move between the samples, and
the statement had already left the executor loop. A degenerate hash join is a
*spin*; this was a *park*. The backend was deadlocked in `gatherOp.Close`
(`internal/executor/operators_gather.go:374`, `for range o.ch`) — the drain that
lets a worker blocked mid-send observe cancellation.

**The defect is in `Open`, not in `Close`, and it is not new and not
flag-specific.** `Open` created `o.ch`, launched the workers, built the leader's
own child, and started the goroutine that closes `o.ch` **last**. Every error
return before that line — `prebuildHashJoins`, the leader's `buildChild`, the
leader's `child.Open` — therefore returned an error while leaving a live channel
with nobody to close it. The workers exited; the closer never existed; `Close`
drained forever. The statement's real error never reached the client, so the
symptom presented as an unbounded runtime with nothing in EXPLAIN to explain it.
The invariant is now explicit and pinned: **once `o.ch` exists, a closer for it
exists on every path out of `Open`** (`startChannelCloser`, idempotent, called
after the last `group.Go` — earlier would close the channel under a worker that
has not sent yet, and a send on a closed channel panics a goroutine that
`serveConn`'s recover does not cover).
`TestGatherCloseTerminatesAfterOpenError` fails on all three arms without the
fix (30 s watchdog each) and passes with it. The sibling `gatherMergeOp` is
**structurally immune** and was left alone: its channels are closed by each
worker's own `defer close(o.chans[idx])`, so the closer is created atomically
with the goroutine that owns it — the very property plain Gather lacked.

With the error no longer swallowed, Q17 at flag ON says what was actually wrong,
in *less* wall-clock than the flag-OFF arm takes to succeed:

| arm (same engine `c3bb4efa88fd4982`) | result |
|---|---|
| `GOOPG_PGSHAPED_DP=1` | **`ERROR after 28.73s — column ref l_quantity/30 out of VirtualSlot range 27 (chained-NLI?)` (XX000)** |
| `GOOPG_PGSHAPED_DP=0` | `OK elapsed=33.17s rows=1` |

So P5.9-e's bar is met by its second clause (an attributed finding), and the
"157×" line in the S5 write-up is withdrawn: there is no Q17 *timing* regression
to explain. What remains is a plain correctness defect — a column reference
resolved to index 30 against a 27-wide `VirtualSlot`
(`internal/executor/expr.go:366`), i.e. an expression whose var indexes do not
match the tuple layout at the node the PG-shaped search built. That is the same
family as P5.9-c's rotated coordinate map, one level further out, and it is
filed as **P5.9-f**; the full bar re-run stays blocked on it. Note what the
sequence cost: the acceptance bar's per-query timeout could not fire either,
because the hang is *after* the row stream, in the server, where a client-side
cancel has nothing to interrupt — a bounded arm is not a bounded server.

### 5.21 The decorrelated aggregate is a foreign scope, and the join key relied on a repair pass, 2026-08-05 (P5.9-f)

P5.9-e handed this item one symptom — `column ref l_quantity/30 out of
VirtualSlot range 27` at flag ON — and one instruction: dump the failing node's
schema width against the Var indexes its residual carries, and do **not** start
from EXPLAIN. Both were followed, and the item turned out to contain **two
independent defects on the same seam**. Only the first is the reported symptom;
the second was uncovered by fixing the first, and would have shipped a silently
wrong answer.

**Reproducer, ~1 s.** The 28.7 s SF1 arm is not the instrument. Q17's shape on a
3000-row, 200-part fixture in a throwaway cluster reproduces the error verbatim
(`l_quantity/29`, the same 25-offset one plan-shape apart) and turns each
iteration from half a minute into a second. Evidence:
`analysis/leftdeep-joins/p59f/`.

**The shape.** Q17's WHERE carries a correlated scalar aggregate, so
`whereEligibleForPreDPUnnest` (predp.go) declines the pre-DP position and the
legacy order runs: **search first, decorrelate second**. `unnestSubquery`
(unnest.go) then splices a hash join on top of the finished searched tree whose
inner side is a `HashAggregate` over a **clone** of `lineitem`:

```
Filter{ l_quantity/4 < 0.2 * avg/26 }
  Join{INNER hash, width 27}          <- NOT tagged searchedTree
    Left : <searched tree, 25 cols>   <- tagged; lineitem(0..15) ++ part(16..24)
    Right: Aggregate{ group l_partkey, avg }   <- 2 cols, a SEPARATE scope
             Filter -> SeqScan lineitem        <- a CLONE
```

**Defect 1 — build and apply disagreed about `*Aggregate`.**
`applyJoinTreePosMap` (bushy.go) has always returned at an `*Aggregate`
("aggregate expressions are a different scope"). Its build-side twin,
`buildBindingsPosMap`'s `collect`, **descended** into one — an arm that has been
there since M0041 with no recorded motivation. With the flag on, the outer side
is a searched subtree and so records *no* scan entries (P5.5-f-ii-a's opacity),
which left the aggregate's clone as the **first and only** `lineitem` entry, at
offset 25. "First occurrence wins" then made the map read `lineitem[i] → 25 + i`,
and the residual's `l_quantity/4` became `l_quantity/29` against a 27-wide
composed slot. Flag OFF hid it by **accident**, not by design: the untagged outer
join recorded `lineitem` at offset 0 first, and the clone's entry was discarded.

The descent was also wrong on its own arithmetic. An `*Aggregate`'s output is
group keys + agg results, so descending advanced `off` by the *child's* width
(16) instead of the aggregate's (2), leaving anything to its right short by 14 —
the identical defect `*WindowAgg` was moved out of the descend set for in RC-2.
The fix is one line of behaviour: `off += len(x.Output())`, record nothing. This
is the **third** instance of the same rule (`*Project` M0125-0012, `*SetOp` /
`*WindowAgg` RC-2), and the rule is now worth stating without a node list:
**`collect` and `applyJoinTreePosMap` must stop at the same nodes; a node whose
output is not the ordered concatenation of its children's outputs is an opaque
leaf to both.**

On Q17 the corrected walk collects *nothing* — the outer side is opaque and the
inner side is a foreign scope — so the remap declines. That is the truth, not a
loss: the search boundary already republishes binding order, so there was never
anything to correct.

**Defect 2 — the join key was being repaired by a pass nobody declared.**
Declining the remap also stops `applyJoinTreePosMap`, and with it
`reresolveJoinByName`. Flag-ON Q17 then stopped erroring and started returning
**zero rows** against the flag-OFF control's five. The cause was in the splice
itself (unnest.go): its `Predicate` and `LeftKey` used merged coordinates
(`p_partkey/16 = l_partkey/25`) while its `RightKey` was built with the
inner-relative index **`0`**. The executor evaluates both keys against a *merged*
slot (`mergedKeySlot`, operators_join_agg.go), so `0` addressed the outer side's
first column: Q17 hashed `part.p_partkey` against `lineitem.l_orderkey`. The
plan dump shows the wreckage precisely — `LeftKey=p_partkey/16
RightKey=l_partkey/0` with `fillJoinHashKeys` deriving a **second**, correct pair
from the Predicate, which EXPLAIN rendered as a two-condition `Hash Cond`.

This had been latent since the splice was written, invisible because
`reresolveJoinByName` rebinds join keys **by name** and silently repaired it on
every path that reached it. That is the transferable finding: *a construction
site that emits coordinates in one space and depends on a later name-rebind to
translate them has no contract, only a habit* — and the habit broke the first
time a legitimate change made the later pass decline. `RightKey` is now built at
`outerWidth`, the same coordinate its own `Predicate` uses.

**Result.** Both arms, one binary (`69b9f548e04161c8`), TPC-H SF1:

| arm | result |
|---|---|
| `GOOPG_PGSHAPED_DP=1` | `OK elapsed=33.46s ordered=acb1af46ffdeef81 rows=1` |
| `GOOPG_PGSHAPED_DP=0` | `OK elapsed=32.98s ordered=acb1af46ffdeef81 rows=1` |
| `tpch-runner -diff` | `Q17 MATCH rows=1` — **VERDICT: PASS** |

Compared on values, not row counts (P5.9-d), which is what makes the second
defect detectable at all: a zero-row Q17 and a one-row Q17 differ in row count,
but a *wrongly keyed* Q17 that still returns one row would not. The 28.73 s
"error" and the 33 s success were never a timing story — the arms are within 1.5 %
of each other, closing the last thread of the withdrawn "157×" figure.

Blast radius is wider than the flag: both fixes change flag-OFF planning for
every correlated-aggregate decorrelation. Gates run: UNITS; SPOT (Q12 rows=2,
Q13 rows=35, 28.9 s); **DS05 sweep PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0, plan shapes 99/99 identical, no verdict changes and no runtime moves**;
the two arms above. **P5.9's full bar re-run is now unblocked.**

### 5.22 The decorrelated GROUP BY key was recorded in the scope it was found in, 2026-08-05 (P5.9-g)

Run 2's last clause-1 failure that was actually the flag's: TPC-H **Q2 returned
0 rows under `GOOPG_PGSHAPED_DP=1` against the control's 455**. Unchanged across
runs 1 and 2, so it was never the P5.9-c rotation it had been provisionally
attributed to.

**What the splice needs.** `unnestSubquery` (unnest.go) rewrites Q2's correlated
scalar aggregate into a hash join whose inner side is a `HashAggregate` over a
clone of the subquery body, GROUPED BY the correlation column. That group key is
evaluated against `agg.Child`'s **output**.

**Where the key's coordinate came from.** Somewhere else. `SubCol` is recorded at
whichever site the correlation was *collected* from, and there are two:

- the Filter walk in `collectUnnestParamsAndResiduals` — the conjunct's own
  space, which for a top-level Filter *is* the aggregate's input; and
- `harvestIndexKeyParams` — the correlation the inner planner folded into an
  `*IndexScan` probe key. That index is **leaf-relative**: the walk descends
  through joins and projects recording `is.Output()` positions and never
  accumulates an offset.

The two spaces agree only when the column's relation happens to sit at the same
offset in both. For Q2 they did, for years: left-deep and unprojected, partsupp
is the first relation of the subquery body, so `ps_partkey` was 0 either way.
Under the flag the search boundary publishes a rotated coordinate map (P5.9-c)
and a reordering `Project` lands partsupp at offset 14, behind
region/nation/supplier. `ps_partkey/0` then reads **`r_regionkey`**: every
European row groups under the single key 3, `part.p_partkey = 3` matches
nothing, and Q2 returns nothing.

Note what the aggregate's *argument* did — `min(ps_supplycost/17)`, correctly
resolved, because it is cloned from the original aggregate and was always
expressed against `agg.Child`. **Key and argument were in different scopes
inside the same node.** That is the fourth instance of this family after
`*Project` (M0125-0012), `*SetOp`/`*WindowAgg` (RC-2) and `*Aggregate`
(P5.9-f), and the first where the two disagreeing coordinates live in one plan
node rather than across a build/apply pair.

**Fix.** `resolveSubColInSchema` re-expresses `SubCol` in the schema the
consumer will actually index — identity when the recorded index already names
the right column (so no working path's ColumnRef changes), otherwise by name
with `SourceTableIdx` disambiguating a self-join, and **nil** when it cannot be
pinned to exactly one column. A nil is a bail, not an error: the caller leaves
the correlated SubPlan in place, which is slower and right. This is the R3-4
rule the EXISTS path already applies to `SubCol.Index` ("an in-range index
pointing at the wrong column is the silent case"), generalised and moved to
where the coordinate is consumed. `replace[p.OuterRef]` deliberately keeps the
UNRESOLVED ref — it is substituted where the `OuterColumnRef` stood, which for
an index probe is the leaf space it was harvested in.

The sibling `unnestScalarWithResiduals` (the aggregate-above-join form) indexes
`SubCol` into its inner schema twice, at `leftWidth + idx`, after
`clonePlanReplacingOuter`, `unnestSubqueriesInPlan` and `unwrapTrivialWrappers`
have each reshaped it — strictly further from the harvest space than the
GROUP-BY case. It is routed through the same resolver rather than left as the
next instance to be found by an acceptance run.

**Why the indexes matter to the reproducer.** Without the TPC-H primary keys the
correlation stays in a Filter, the two spaces coincide, and both arms agree —
the fixture cannot fail. The defect is only reachable once `partsupp_pk` exists
and the inner planner folds `ps_partkey = p_partkey` into an index probe. A
first 5-table fixture without indexes returned 18 rows on both arms and nearly
retired the hypothesis; adding the PKs reproduced 0-vs-18 immediately.

**Result.** Both arms, one binary (`c8fe0d352d75b67e`), TPC-H SF1:

| arm | result |
|---|---|
| `GOOPG_PGSHAPED_DP=0` | `OK elapsed=2.43s ordered=1c0f630719e8c7bf rows=455` |
| `GOOPG_PGSHAPED_DP=1` | `OK elapsed=3.36s ordered=1c0f630719e8c7bf rows=455` |
| `tpch-runner -diff` | `Q2 MATCH rows=455` — **VERDICT: PASS** |

Adjudicated against PostgreSQL per §3.4: on the bench-free 5-table fixture PG
18.3 returns the same 18 tuples in the same order as both goopg arms (differing
only in `char(N)` blank padding and numeric scale, two pre-existing formatting
gaps unrelated to this seam).

Blast radius is wider than the flag — the resolver and both bails run on
flag-OFF planning too. Gates run: UNITS; SPOT (Q12 rows=2, Q13 rows=35, 28.3 s);
DS05; the two arms above. **Clause 1 of the acceptance bar now has no known
flag-owned failure; run 3 is unblocked, with the clause 2/3 timing gap (P5.9-h)
the remaining work.**

## 6. Attribution protocol for regressions (inherited, binding)

Any per-query regression during S1–S5 gets classed before any fix lands:

- **(a) cardinality** — estimate wrong → fix in [04](04-cost-and-cardinality.md)
  §3 mechanisms only;
- **(b) plan shape** — estimate right, order/method wrong → enumerator or
  cost-function bug, cite the PG analogue;
- **(c) cost-model realism** — plan matches intent, runtime disagrees →
  missing cost term (nbatch, seam) with `costsize.c` citation;
- **(d) executor** — same plan, slower run → seam/allocation regression;
  pprof before patch.

No constant may change without its class diagnosis in the commit message —
the "no unfalsifiable tuning" rule made procedural.

## 7. Microbenchmarks (executor stages)

`go test -bench` fixtures under `internal/executor/`:

- seam benchmark: 3-level cascade, 1M synthetic probe rows — allocs/op must
  be 0 in steady state after E1+E2 (the assertion that kills the pool
  round-trip class);
- build benchmark: single-pass vs two-pass (guards E3 against regression);
- key benchmark: composite int64 pair vs string keys (E4);
- spill benchmark: nbatch ∈ {1, 4, 16} at fixed input size (S3 overhead
  curve; nbatch=1 must be within noise of pre-S3).

Benchmarks are tripwires with recorded baselines in the evidence directory,
not CI gates (WSL2 noise); regressions > 20 % require investigation before
the stage advances.

### 3.14 The flip, and the 24 tests that were measuring the fixture (P5.9, 2026-08-06)

`GOOPG_PGSHAPED_DP` is **ON by default** as of this section. The knob survives
as a kill-switch — only the exact string `0` turns the search off — because §2
of [08](08-migration-and-removal.md) makes "flip it OFF, get `tryBushyDP` back
in one restart, no rebuild" S5's entire rollback story until S7 deletes the old
DP. `GOOPG_COST_DRIVEN_JOINORDER` is retired in the same commit, as §2 says it
would be: the env hook is gone, while `costDrivenJoinOrder` and its setter stay
with the enumerator they belong to.

The evidence for the flip is run 4 (§3.10) plus §3.13's clause-6 measurement,
and nothing in this section revises either. What this section records is what
the flip cost inside the tree, because the acceptance bar could not have found
it: **24 standing unit tests failed the moment the default changed** — 17 in
`internal/planner`, 7 in `internal/executor` — and every one of them had been
green through all four acceptance runs. Both bars run on ANALYZEd TPC-H and
TPC-DS data; the unit suites do not, and that difference is the whole story.

**The single mechanism.** The old enumerator promotes join OPERATORS by rule:
`rewriteJoinsToNLI` turns any equi-join on an indexed inner into a
`NestedLoopIndexJoin`, `rewriteMultiWayChain` packs a hash cascade into a
`MultiHashJoin`, `IsSmallDimensionSide` pins a build side. None of those rules
consults a row count, so they fire identically on a fixture that has no
statistics at all. The PG-shaped search has no such rules — P5.9-b's eight
skips keep every one of them off a searched tree — and picks the operator by
cost, like `add_path`. On a relation the planner believes holds zero rows, the
cheapest join is a bare nested loop, and the search plans one. Correctly.

**Which of those beliefs are real.** Two are not, and separating them is the
work this section did:

- *`catalog.NewInMemory()` fixtures with no `TableStats`* (the 17 planner
  tests). There is no relation, no file and no block count; nothing is wrong
  with a planner that sizes them at zero. These tests assert the legacy
  REWRITE RULES, which still ship and still run behind the kill-switch, so they
  are pinned to that arm by `useLegacyEnumerator` — not relaxed to accept
  either operator, which would leave them unable to fail. Their production-arm
  counterpart is new: `TestPGShapedSearchPicksNLIOnCost` and
  `TestPGShapedSearchPicksHashJoinOnCost` show the searched arm reaching the
  same two operators by cost once the fixture carries numbers that justify it
  (50 rows against a 200 000-row indexed inner; 500 000 against 400 000
  unindexed).
- *`newDDLFixture` relations with rows on disk and no sizer* (the 7 executor
  tests). This one was a real gap in the harness. `initdb.Open` installs a
  block-count reader on the catalog (`SetRelationSizer`), which is what lets
  the relation-size fallback — goopg's `estimate_rel_size`, M0125-0003 — size a
  never-ANALYZEd relation from its live file. The executor fixture builds a
  `Context` directly and installed none, so `RelationBlocks` answered "no
  estimate" and 4 000 rows on disk were planned as one. Installing the same
  reader in the fixture fixed 4 of the 7 outright and is the change that makes
  those tests plan like a server rather than like nothing.

**The production case was measured, not assumed.** The failure mode that would
actually matter — a populated relation nobody ever ANALYZEd, planned blind, and
the flip turning that into nested loops — does not occur. On a live throwaway
server (200 000 rows joined against 2 000, no `ANALYZE` anywhere) both arms
produce the same plan and the same estimates from block counts alone:

```
flag ON   Hash Join (INNER)  rows=272760   Hash Cond: (b.k = s.k)
            Seq Scan on big b   rows=272760
            Seq Scan on small s rows=2260
flag OFF  identical, modulo `public.`-qualified scan labels
```

The seam is what prevents it, deliberately and with the reasoning written down
at the call site (`joinsearchseam.go`): when `estimateBaseRelInfo` comes back
with no rows it re-asks `relSizeFallbackRows`, "so the seam would [not] hand
the search a blind problem where the DP it replaces gets a live block-count
estimate."

**Three residues, all filed rather than fixed.** (i) goopg prints no
`Join Filter:` line for a hash join's residual qual, on either arm — which is
why `TestExplainQualifiesUpperFilter` has no searched-arm shape to assert
against and stays pinned: single-relation quals push down to the scan, where
`show_scan_qual` (explain.c:2540) correctly prints them UNQUALIFIED, and
cross-relation quals are not printed at all. (ii) The hash-join batch-growth
path needs a plan that under-estimates its build side; the searched arm no
longer does on that fixture, so growth coverage now runs on a deliberately
blinded legacy-arm fixture and the searched arm has none. (iii) The
`MultiHashJoin` tests need the old enumerator to produce an MHJ at all, which
is clause 5 working as specified. All three are deferral-ledger rows dated
2026-08-06; (iii) resolves by deletion at S7.

**What is still not measured.** The collapse sub-flag `GOOPG_PGSHAPED_COLLAPSE`
stays OFF and its own acceptance pass — §2's "then with collapse ON" — has
never been run; no arm to date has set it. That is not a gap in this flip: §2
gives the sub-flag a separate soak precisely so the enumerator swap and the
population change do not land as one unattributable diff, which makes the
collapse-ON pass the gate on the COLLAPSE flip rather than on this one. It is
filed as its own item.

### 3.15 The post-flip DS05 arm: zero correctness movement, 86 of 99 plans re-planned (P5.9-n, 2026-08-06)

The one gate the flip commit could not pay. `b92582fb` landed with the DS05
clause unrun because `scripts/tpcds-sf05-regression.sh sweep` refused —
`FATAL: the nightly CI batch is running (ci/batch)` — and `FORCE=1` would have
bought a TIMEOUT column contaminated by a CPU-saturated host, which is the
confound the refusal exists to prevent. It was run here on a quiet host (load
1.27 falling, nightly `20260806-011323` exited at 02:25, no orphaned bench
servers), 02:28:14 → 02:56:15, **28 minutes** rather than the ~1 h the README
budgets — the timeout class being empty is most of that difference.

**The verdict is green and byte-identical to run 4's:**

```
=== SUMMARY: PASS=95 (57 ck-verified, 38 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4 ===
=== STATUS-DELTA: compared=99 verdict-changes=none runtime-moves=0 (>=2.0x, floor 5s) ===
```

against `sweep-20260806-002849.txt` (`9e0cfe67`). Report:
`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260806-022814.txt`. So
P5.9 does not reopen: no MISMATCH, no CKMISMATCH, no query lost or gained a
verdict, and nothing moved 2× in either direction.

**The plan channel's "changed (33)" is not the flip, and the reason is a stale
label.** The channel diffed against `plans-20260805-222627.txt`, and that
capture is stamped `GOOPG_PGSHAPED_DP=1` — it is an ON-arm capture taken during
P5.9-h's acceptance work. The 33 therefore measure ON@`2f13e13e` → ON@`13009c0c`,
i.e. the P5.9-j and P5.9-k cost terms, with the enumerator held constant. The
flip's own effect was not in that number at all.

Worse, the new capture was stamped `GOOPG_PGSHAPED_DP=unset(off)` while running
ON. `sf05_planner_flags_line` had kept the pre-flip label through the flip —
**the exact defect its own comment block documents happening to
`GOOPG_RELSIZE_FALLBACK` at M0125-0005**, one flag generation later. Two
artefacts state the opposite of the regime they measured:
`sweep-20260806-022814.txt:8` and `plans-20260806-022814.txt:5`. Both are
untracked run output, so they are annotated here rather than rewritten. The
label is now `unset(on)`, and `GOOPG_COST_DRIVEN_JOINORDER` — which `b92582fb`
retired, no code reads it (`internal/planner/bushy.go:13`) — is stamped
`retired(M0127-P5.9)` rather than dropped, so an old artefact that carries a
real value for it stays distinguishable from one captured by this version.

**The flip's real plan blast radius, measured at a fixed binary.** Same
`13009c0c` build, same cost model, same stats, same S-cold server; only
`GOOPG_PGSHAPED_DP` differs (default vs `=0`):

| | queries |
|---|---|
| identical on both enumerators | **13** — Q9 Q28 Q36 Q40 Q41 Q44 Q49 Q70 Q72 Q78 Q80 Q86 Q93 |
| different plan text | **86** |
| └ of those, a different join-operator multiset | 22 |
| └ of those, same operator inventory, different order / qual placement / row estimates | 64 |

Three of the 13 (Q36, Q70, Q86) are the dsqgen artefacts whose block is a parse
error on PG too, so **10 real queries plan identically and 86 do not**. At a
fixed binary the two captures can only differ by the enumerator's choice, so
this is signal at the channel's measured zero noise floor (§5.11).

Q1 is representative of the 22: the searched arm (shipped default) picks
`Gather Merge` over `Sort` over three hash joins with `s_state = 'TN'` pushed
down to the `store` scan and `rows=238` at the top; the legacy arm picks two
nested loops driven by `store_pkey` and `customer_pkey` index scans, no Gather,
`s_state = 'TN'` pulled up into the top join's `Filter:`, and `rows=1`. Both
return the same rows and the same checksum.

**What this establishes that run 4's cells could not.** The acceptance bar
reported "nothing changed", and that was true of every result and false of 87%
of the plans. The flip is not a marginal re-ranking that happens to tie: it
re-plans nearly the whole corpus, and the corpus is exactly as correct
afterwards. That is the strongest available statement about the flip's risk
profile on this benchmark, and it took the fixed-binary A/B to make it — the
cross-commit diff the gate runs by default cannot separate an enumerator change
from a cost-term change, which is how the 33 came to look like the answer.

### 3.16 The provenance label is now generated from the default it names (P5.9-q, 2026-08-06)

§3.15 fixed a mis-stamped label by hand for the **second** time. That is the
finding: `sf05_planner_flags_line` had documented, in its own comment block, the
M0125-0005 flip that outlived its `unset(off)` label — and then outlived the
M0127-P5.9 flip the same way, mis-stamping the acceptance run of the flip
itself (`sweep-20260806-022814.txt:8`, `plans-20260806-022814.txt:5`). A defect
whose repair consists of a comment predicting its own recurrence is not
repaired.

The reason is structural, not attentional: the label for an UNSET variable is a
claim about a **Go default**, and it lived in a **bash printf**. Nothing that
compiles, runs, or diffs could relate the two. So the two halves are joined:

```
internal/planner/flaglabels.go     flagResolvedState[env]("")  -> "on" | "off" | "2" | "current"
  → cmd/gen-planner-flag-labels    renders the shell fragment
  → scripts/planner-flags.env      GENERATED, checked in (a gate host needs no Go)
  → scripts/planner-flags.sh       planner_flags_body(), sourced by both gates
```

Each label is computed by the *same function that resolves the default at
process start* — `pgShapedDPFromEnv`, `parseRelSizeFallbackStage`,
`memoizeFromEnv`, … — several of which were factored out of their `init()` in
this commit for exactly that purpose. Nothing in the chain restates a default.

**Four guards, and what each one catches** (`internal/planner/flaglabels_test.go`):

| test | catches |
|---|---|
| `TestFlagProvenanceEnvIsGenerated` | the P5.9/M0125-0005 defect itself: a default flipped, the label not regenerated. Verified by probe — flipping `pgShapedCollapseFromEnv` to default-on fails it with the two labels side by side. |
| `TestFlagLabelsRoundTrip` | a label that reads well but is not runnable. The token inside `unset(…)` must resolve, through the flag's own parser, back to the same state — so `GOOPG_PGSHAPED_DP=unset(on)` is an instruction an operator can paste. |
| `TestFlagProvenanceTableCoversPlannerEnv` | the silent half: a plan-shaping flag the artefacts never name. Every `os.Getenv("GOOPG_*")` in the package must be stamped or explicitly exempt with a reason. Verified by probe. |
| `TestGateScriptsUseGeneratedFlagLabels` | the labels creeping back into the shell — no non-comment `unset(` in either gate. |

The coverage guard immediately produced its first finding. The stamp named
**six** flags; the planner reads **twelve**. `GOOPG_EXISTS_TO_ANY`,
`GOOPG_UNNEST_PREDP`, `GOOPG_INDEXKEY_HARVEST`, `GOOPG_NLI_COSTGATE`,
`GOOPG_HASH_OUTER_JOIN` and `GOOPG_MHJ_PACKING_OFF` all change plan shape and
appeared in no artefact goopg has ever captured — an A/B across any of them
would have produced two byte-indistinguishable reports, which is the precise
failure `sf05_planner_flags_line` was written to prevent. They are stamped now,
at no shell cost, because the gates iterate the table.

`scripts/tpch-spotcheck.sh` — the gate every planner commit pays — is on the
same table. Its line previously hedged `GOOPG_RELSIZE_FALLBACK=unset(build
default)` (honest, but not diffable), named `GOOPG_COST_DRIVEN_JOINORDER`,
which no code reads, and **did not name `GOOPG_PGSHAPED_DP` at all** — so since
`b92582fb` its timings could not say which enumerator produced them.

The six pre-existing labels are byte-identical before and after
(`unset(2)`, `retired(M0127-P5.9)`, `unset(on)` ×3, `unset(off)`), so captures
from this version stay comparable with the corpus taken since §3.15's fix; the
line grows to the right.

One boundary is deliberate and ledgered: the guard scans `internal/planner`
only. Executor-side kill switches (`GOOPG_HASHED_SUBPLAN` and its siblings)
also move measured runtime and remain unstamped.

**Verification of the change itself.** Units pass; `scripts/tpch-spotcheck.sh`
is green with canonical rows (Q12=2, Q13=35, 26.1 s query phase) and its new
line names the enumerator for the first time. The SF0.5 **plan channel** was
run as the planner-neutrality proxy and reports `queries=99 same=99 changed=0`
against `plans-20260806-022814.txt` — the true ON-arm capture — so nothing in
this commit moves a plan, and the row-count sweep §3.15 ran 40 minutes earlier
still describes this plan set.

The channel's *default* baseline selection walked straight into the hazard this
task is about: it auto-picked `plans-20260806-025726.txt`, the fixed-binary
**OFF** arm, and reported `same=13 changed=86` — reproducing §3.15's 86/13 split
exactly, which is a clean independent replication of the flip's blast radius and
would have been a terrifying false regression for anyone who read the number
without opening the header. That is the whole argument for this task in one
command: the header is what makes a diff attributable, so it has to be true.

### 3.17 `Join Filter:` — the conjunct that was in no line (P5.9-o, 2026-08-06)

§3.14 filed three residues rather than fixing them. This closes the first:
goopg printed **no `Join Filter:` line at all**, so for

```sql
SELECT jl.v FROM jl JOIN jr ON jl.a = jr.a AND jl.v < jr.w
```

the second conjunct appeared **nowhere in the plan text**. The key printed as
`Hash Cond:` (P2.1) and the conjunct the executor re-checks once per candidate
match was invisible on both arms. The rows were always right; what was missing
was the ability to read the plan against PostgreSQL's own output for the same
query — the exact reading P2.1 built `Hash Cond:` to restore, stopping one
conjunct short.

**Upstream's rule, and where it lives.** `ExplainNode` prints
`show_upper_qual(join.joinqual, "Join Filter", …)` immediately after the Cond
line, identically for `T_HashJoin`, `T_MergeJoin` and `T_NestLoop`
(`postgres/src/backend/commands/explain.c`). `joinqual` is not a separate
planner concept: `create_hashjoin_plan` builds it as
`list_difference(joinclauses, hashclauses)` (`createplan.c`) — the join's quals
**minus** whatever the key list already enforces. A nested loop has no key
list, so its whole qual set is the residual.

**Why the split is asked of the planner rather than recomputed.**
`formatJoinFilter` (`internal/executor/operators_explain.go`) calls
`ExecHashKeyPlan` / `ExecMergeKeyPlan` — *the same methods the executor uses*
to decide what it re-checks per match (`join_exec_keys.go`, P2.2/P2.3). An
EXPLAIN that derived the residual independently could disagree with the one
actually evaluated, which is precisely the invisibility this line exists to
remove. The consequence is the property worth stating: **every conjunct prints
exactly once** — inside the Cond line when a key enforces it, as `Join Filter`
when it does not.

**Verified against PostgreSQL 18.3, not against a reading of explain.c.** A
throwaway 18.3 cluster (`initdb` → :5533, same DDL, `enable_mergejoin`/
`enable_nestloop` off to pin the operator) produced these, and goopg's text is
byte-identical on all four:

| shape | PG 18.3 / goopg |
|---|---|
| one residual conjunct | `Hash Cond: (jl.a = jr.a)` + `Join Filter: (jl.v < jr.w)` |
| two residual conjuncts | `Join Filter: ((jl.v < jr.w) AND (jl.b <> jr.b))` |
| all-equijoin two-key | `Hash Cond: ((jl.a = jr.a) AND (jl.b = jr.b))`, **no** Join Filter line |
| merge join | `Merge Cond: (a.id = b.id)` + `Join Filter: (a.st < b.st)` |

(PG's nested-loop arm prints the whole conjunction —
`Join Filter: ((a.st < b.st) AND (a.id = b.id))` — which is what goopg's
no-key-list branch emits too.)

**What it unpins.** `TestExplainQualifiesUpperFilter` was pinned to the legacy
enumerator by §3.14 and is now back on the **default** arm. The pin was never
"the new plan is wrong": its fixture's `a.st = 'x'` names one relation, the
searched arm pushes it to the scan as upstream does, and `show_scan_qual`
deparses a scan qual **unqualified** — so asserting a prefix there would have
asserted the opposite of PostgreSQL. The repair (a conjunct spanning both
relations, which no scan can absorb) was unavailable only because the line did
not exist. It exists now, it is emitted through the same `es->rtable_size > 1`
rule as `Hash Cond`, and that rule is what the test asserts.

Two new tests hold the pair: `TestExplainRendersJoinFilterResidual` (the line,
its text, its two-conjunct AND chain, and its slot *after* the Cond line) and
`TestExplainNoJoinFilterWhenKeysCoverThePredicate` (the all-equijoin case
prints nothing — the half that keeps "exactly once" honest, since a residual
that printed but was not evaluated is the same defect mirrored).

**Blast radius, stated so the next plan diff is attributable.** Every captured
plan whose join carries a non-key conjunct grows one line. The SF0.5 **plan
channel** compares goopg text to a previous goopg capture, so its next run will
report those queries as `changed` with no planner change behind it; the
`make plan-diff` channel compares against PG and should move the other way.
Neither is a regression, and this paragraph is the header to read it by — the
lesson §3.16 was filed for.

**Still deferred** (ledger rows, 2026-08-06): the ANALYZE counters
`Rows Removed by Join Filter` / `by Filter` (goopg emits no `Rows Removed by …`
line at all), the structured formats (`FORMAT JSON`/`XML`/`YAML` carry no qual
properties whatsoever — not `Join Filter`, not `Hash Cond`, not `Filter`), and
`NestedLoopIndexJoin`'s residual, which keeps its `Filter:` label because its
`Predicate` mixes hoisted inner scan filters with join residuals and would be
*less* faithful relabelled wholesale.

### 3.18 The collapse-ON acceptance pass: green everywhere, and the flag cannot move a plan (P5.9-m, 2026-08-06)

08 §2's S5 row gates on running this bar "once with collapse OFF, then with
collapse ON", and no arm to date had ever set `GOOPG_PGSHAPED_COLLAPSE=1`. It has
now been run end to end. **Every clause is green and the verdict is a NO-GO**,
because the same run also measured why the green is uninformative: on the whole
measured corpus the flag cannot change a plan, and the reason is not in the
collapse pass.

**The arms.** One binary (`5ed79a1b78bab6a8`, HEAD `d867ae03`), `GOOPG_PGSHAPED_DP=1`
explicit on both, `COLLAPSE` the only variable.

| channel | collapse OFF | collapse ON |
|---|---|---|
| TPC-H SF1, 24 labels | 370.29 s | 364.26 s (**0.984×**), **24/24 MATCH on values** |
| TPC-DS SF0.5 sweep | `PASS=95 (57 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4` | **identical**, `STATUS-DELTA verdict-changes=none runtime-moves=0` |
| DS05 plan capture, fixed binary | — | **`queries=99 same=99 changed=0`** |

Artefacts: `analysis/leftdeep-joins/2026-08-06-p59m-coll-{off,on}.txt`,
`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260806-035500.txt`,
`plans-20260806-{035500,042316}.txt`.

Read the sweep's own plan channel with §3.17's header: it reports
`changed (36)` against `plans-20260806-031851.txt`, a baseline captured before
the `Join Filter:` commit. That count is §3.17's line, not collapse. The
one-variable number is the third row — ON vs OFF at a FIXED binary, both
post-`d867ae03` — and it is **zero**.

**Why the TPC-H arm is a control, not a test.** `GOOPG_PGSHAPED_COLLAPSE` acts
only on an explicit INNER/CROSS JOIN; `joinPinned` pins outer joins in both
regimes. The TPC-H corpus contains exactly one explicit join — Q13's
`LEFT OUTER JOIN` — so **0 of 22** queries pose a different search problem under
the flag, and the 0.984× above is host noise measured to three digits.
`TestCollapseIsAControlOnTheTPCHCorpus` pins that, running the production
`deconstructJointree` over the production parse at both flag values; if the
corpus ever gains an inner-JOIN spelling the test fails and says the arm has
become a real test.

**The TPC-DS corpus is eligible — twice.** Same instrument over the 99 dsqgen
queries (3 unparseable: Q36, Q70, Q86): **Q72 and Q75** change search problem,
nothing else. A `grep -c ' join '` says three, adding Q78 — and is wrong. Q78's
chains are `A LEFT JOIN B … JOIN date_dim …`: the pinned outer join has already
folded its two sides into ONE joinlist item, so the inner join that follows
offers a two-member problem either way. The planner's count is the one the arm
runs, which is why the measurement goes through `deconstructJointree` and not
through a regex. (Same reason a two-way `a JOIN b` is not eligible: `[[[0] [1]]]`
and `[0 1]` differ only by the inert nesting of initsplan.c:1417, and
`canonJoinlist` strips it before comparing.)

**So why did Q72 and Q75 plan identically?** Two facts predict that observable
and they have opposite consequences — the search ran and chose the same plan, or
the search never ran. The printed plan cannot separate them; the enumeration
trace (§3.12) can, because "no trace" IS the evidence for the second. Measured
with `GOOPG_PGSHAPED_DP_TRACE=1` on the SF0.5 cluster
(`analysis/leftdeep-joins/p59m-collapse-probe.sh`):

- Q72's eleven-way explicit-JOIN level: **no trace at all**, in either regime.
- Q75: one traced problem, `nrels=2 rels=curr_yr,prev_yr` — its top-level
  two-way CTE join. The six-way inner-JOIN level the flag collapses: no trace.
- Synthetic control, same three relations written both ways:
  `store_sales JOIN date_dim ON … JOIN item ON …` produces **no** trace with
  `COLLAPSE=1`, while `FROM store_sales, date_dim, item WHERE …` produces the
  full four-pair enumeration. The two plans differ (the explicit-JOIN one carries
  a leftover `Filter: (date_dim.d_year = 2000)` above the join), which is the
  legacy syntactic path's signature.

**The cause is the seam, not the collapse pass.** `ctx.joinlist` is read only
after `tryPGShapedJoinSearch`'s preconditions pass, and one of them is that the
pre-search node's leaves enumerate to the binding count. `extractScans`
(`internal/planner/bushy.go:261`) descends `JoinTypeCross` and nothing else, so
an explicit JOIN arrives as ONE node for N bindings and the seam declines before
the joinlist is consulted. `TestPGShapedSeamDeclines/leaf count disagrees with
binding count` has pinned that gate from the other side since P5.9; what this
pass adds is that it is also the collapse flag's blocker — the flag flattens a
joinlist that the statement never gets far enough to use.
`TestCollapseDoesNotReachTheSearch` pins the pair, and fails the day the seam
learns to walk an INNER chain, which is exactly when this no-go should be
re-measured.

**Verdict: `pgShapedCollapse` stays default OFF.** 08 §2's S5 collapse gate is
**not discharged** — it was run, and what it measured is that the flag is
currently inert end to end. Flipping it would advance the row with a change that
cannot move a plan, which is the §3.15/§3.16 defect one flag generation later:
a gate reporting a number about a variable it did not vary. The unblock is a
seam that admits an INNER-join chain (ledger row, 2026-08-06); collapse becomes
measurable the moment it lands, and this section is the protocol to re-run.

### 3.19 The seam learns to walk an INNER chain, and the corpus turns out to have none (P5.9-r, 2026-08-06)

§3.18 named the collapse flip's blocker — the seam declined every FROM clause
written `JOIN … ON` — and filed the unblock as P5.9-r. It has landed, the
protocol above has been re-run, and the verdict is unchanged with a **different
and more precise cause**: the door is open and the corpus has nobody to walk
through it.

**What landed.** `extractSearchLeaves` (`internal/planner/joinsearchseam.go`)
replaces `extractScans` at the seam. It descends INNER links as well as CROSS
ones and returns the `ON` quals of every link it flattened, which the seam then
appends to the `WHERE` conjuncts and partitions with the same rule — upstream's
shape, where `distribute_qual_to_rels` places a qual by the relids it reads and
an inner join's qual has no other property to distinguish it. Routing the quals
is not optional: the seam DISCARDS the pre-search chain and carries only its
leaves, so an `ON` qual the walk failed to hand over would not be demoted to a
slower plan, it would vanish and the statement would return the cross product.
`TestPGShapedSeamSearchesAnExplicitInnerChain` asserts all three destinations at
once (both equalities enforced in the searched tree, the WHERE restriction on
its leaf, empty residual), and `TestCollapseDoesNotReachTheSearch` is inverted
into `TestCollapseReachesTheSearch`.

An `ON` qual may be moved at all only because the link is INNER — an inner
join's qual is semantically a `WHERE` qual, so a conjunct the search does not
place is still correct in the residual `Filter` above the searched tree. That
equivalence is the whole licence, and it is why the walk stops at every other
join type.

**Three shapes stay declined, each for a correctness reason.** An OUTER (or
semi/anti) link stops the walk; an `ON` qual on a non-first comma FROM item is
written in that item's own coordinates and re-basing it needs a rewriter that
answers "unchanged" for an expression kind it does not know (`shiftColumnRefsBy`,
planner.go), so the seam admits the shift-free case and declines the rest; a
LATERAL marker on an INNER link is now read before the link is flattened, which
it had to be — checking only CROSS links would have let the one shape the search
must never reorder in through the new door.

**The re-run.** DS05 plan capture, one binary, `COLLAPSE` the only variable:

| arm | result |
|---|---|
| collapse OFF vs the `d867ae03` baseline | `queries=99 same=99 changed=0` |
| collapse ON vs collapse OFF, fixed binary | `queries=99 same=99 changed=0` |
| DS05 SF0.5 row/value sweep (default regime) | `PASS=95 (57 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4` |
| ↳ vs §3.18's own sweep | `STATUS-DELTA compared=99 verdict-changes=none runtime-moves=0` |

Artefacts: `bench/tpcds/runtime_goopg/tpcds-results-sf05/plans-20260806-{051002,051102,051234,051536}.txt`,
`sweep-20260806-051536.txt`.

The sweep is the correctness clause, and it is the one that would have caught a
mis-routed `ON` qual: a qual dropped on the way into the clause list produces a
cross product, which is a row-count MISMATCH, not a plan-shape difference. It
reports zero — against a git-tracked PG oracle, with 57 of the 95 passes
verified on a value checksum and not merely on a count.

**Why it is still zero, measured rather than inferred.** This pass adds the
instrument §3.18 lacked: `DPTRACE seam-decline reason=… nrels=… nleaves=…`
(`internal/planner/joinsearchtrace.go`), which distinguishes "the seam declined
the statement" from "the search ran and enumerated nothing" — two facts that
produced the identical silence in §3.18's log and cost that pass a synthetic
control to separate. Q72 answers immediately:

```
DPTRACE seam-decline reason=leaf-count nrels=11 nleaves=1
```

One leaf for eleven bindings, because Q72's chain ENDS in
`left outer join promotion … left outer join catalog_returns …` and the walk
stops at the first outer link it meets from the top. Q75 is the same at
`nrels=4 nleaves=1`, Q78 at `nrels=3 nleaves=2`. Generalised at the parse level
by `TestNoCorpusQueryHasAnInnerOnlyJoinChain`: of 99 TPC-DS queries, **twelve**
spell an explicit JOIN and **all twelve** contain an outer join, so **zero** are
INNER-only. TPC-H is the same with one query — Q13's only explicit join is a
LEFT OUTER. The corpus contains no statement this walk can act on, and the plan
A/B could not have moved.

**The blocker, one level deeper.** `joinlistItem` (`internal/planner/collapse.go`)
carries no join TYPE. `joinPinned` correctly wraps an outer join into its own
two-member subproblem, but nothing downstream could rebuild it AS an outer join
— `makeRelFromJoinlist` joins its items and the type is simply not in the data —
so admitting an outer link would silently plan a LEFT JOIN as an INNER JOIN.
**The leaf-count decline is what stands between that latent shape and a wrong
answer today**, which is why P5.9-r kept it rather than widening the walk
further. Making an outer join reorderable is 03 §4.4's `SpecialJoinInfo` work;
making it merely *representable* below a pinned spine is the smaller successor,
and it is the one the corpus needs — `runJoinSearchBelowPinned` already does
exactly that for the semi/anti spine.

**Verdict: `pgShapedCollapse` stays default OFF, and 08 §2's S5 collapse gate is
still run-but-NOT-discharged.** §3.18's no-go stands; its stated cause does not.
The protocol to re-run is this section, and the signal to re-run it is
`TestNoCorpusQueryHasAnInnerOnlyJoinChain` going red — the day a measurable
query exists — or the outer-link successor landing, whichever comes first.

### 3.20 The outer spine is peeled, the corpus moves for the first time, and the query that moved found a spill bug (P5.9-s, 2026-08-06)

§3.19 ended with the seam able to walk an INNER chain and a corpus containing
none: all twelve TPC-DS queries that spell an explicit JOIN are topped by an
outer link, so the walk stopped at the first one and the statement fell back to
the syntactic shape. This section is the successor it filed, and it is the first
entry in this chapter where **a corpus plan actually changed with every result
held identical**.

**What landed, in two halves.** The joinlist half: `joinlistItem` carries a
`jointype`, set by the new `pinnedItem` constructor, and `makeRelFromJoinlist`
now REFUSES a pinned outer subproblem outright
(`TestSearchRefusesToPlanAPinnedOuterJoin`). That converts §3.19's blocker from
an accident into an invariant — the leaf-count decline was the only thing
standing between a `LEFT JOIN` and a plan that dropped its unmatched left rows,
and a decline is not a guard, it is a shape that happened not to arrive. The
seam half: `splitOuterSpine` (`internal/planner/joinsearchseam.go`) peels the
pinned outer links off the TOP of the chain, the search plans the INNER PREFIX
below them, and the links are spliced back above the searched subtree
unchanged — the same division `runJoinSearchBelowPinned` (`predp.go`) already
makes for the semi/anti spine, for the same reason (goopg cannot yet infer 03
§4.4's `SpecialJoinInfo` ordering constraints, so the choice is "search what is
below it" or "search nothing", and until now it was nothing).

**Only LEFT may be peeled, and that bound is the whole correctness argument.**
The prefix is the peeled link's LEFT side, and the seam does not merely reorder
it: single-relation conjuncts are attached to prefix leaves and spanning ones are
placed by the search INSIDE the prefix — i.e. BELOW the outer join. For a LEFT
JOIN the left side is the non-nullable one and that is exactly what upstream
does (`check_outerjoin_delay`, `initsplan.c`, delays a qual only when its relids
reach the NULLABLE side). For RIGHT the nullable side IS the left one, so the
same push would turn `WHERE a.x IS NULL` from a test on null-extended rows into
a test on `a`'s own rows; FULL nullifies both sides. Both are declined
(`TestPGShapedSeamDeclinesANonLeftSpine`), as is an outer link buried BELOW an
inner one (Q78's `A LEFT JOIN B … JOIN date_dim …` shape) and a one-relation
prefix. The splice itself preserves nothing but the left side's column layout,
which is why a LATERAL spine link is declined rather than reasoned about.

**New instrument: `DPTRACE seam-spine nspine=… nrels=… nprefix=…`.** A search
block covering nine of a statement's eleven relations otherwise reads, in a log,
as an enumerator that gave up at nine — the same ambiguity `seam-decline` was
added to remove one step earlier (§3.19).

**The measurement.** `TestCorpusQueriesWithASearchableInnerPrefix` runs the
production producer over the corpus and pins the population at **{72, 75}** — the
same two queries the COLLAPSE arm is eligible on, because `deconstructJointree`
decides both. Zero before this task, so the corpus population went **0 → 2**. The
DS05 SF0.5 gate then measured it end to end:

| channel | reading |
|---|---|
| results | `PASS=95 (57 ck-verified, 38 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4` — cells identical to the baseline |
| status-delta | `verdict-changes=none runtime-moves=1` — **Q72 163s → 70s (2.3× faster)** |
| plan-shape | `queries=99 same=97 changed=2` — exactly Q72 and Q75, the predicted set |
| TPC-H | `tpch-spotcheck` Q12=2 Q13=35 PASS |

Report: `sweep-20260806-062915.txt`. Q72's searched prefix is nine relations
under two `left outer join`s, visible in its EXPLAIN as a hash-join cascade below
two `Nested Loop (LEFT)` nodes.

**The bug the moved plan found, and why nothing else could have found it.**
Q72 first came back `ERROR: operator + requires integer operands` from
`d3.d_date > d1.d_date + 5`. It was NOT the peel: the same query spelled without
its outer links — a shape §3.19's walk already searched — failed identically,
and `GOOPG_PGSHAPED_DP=0` answered correctly. `work_mem = '2GB'` also answered
correctly, which named the cause: **`encodeDatum` (`internal/executor/spill.go`)
wrote a `KindTime` datum's value and never its `Flags` byte**, so every DATE that
went through a hash-join batch spill came back a bare timestamp. `flagDate` is
what `date + integer` (upstream `date_pli`), `Format()`'s MDY rendering and the
`date`-typed cast arms dispatch on, and a spilled row is read back as a bare
`Row` with no column types in reach — nothing downstream could re-derive it.

The reason it survived this long is the shape of its symptom: a COMPARISON of two
spilled dates still works, because `Int` survives intact and only the TYPE is
forgotten. `TestSpillRoundTrip` compared values and reported success on a datum
that had lost half its meaning. `TestSpillPreservesTheDateDiscriminator` asserts
the flag, not just the value, on an ordinary date and on both `±infinity`
sentinels (whose carrier IS `KindTime + flagDate`); its sibling
`TestSpillDoesNotForgeANumericRepresentation` pins the other direction, since
`flagBigNumeric` describes a representation the decoder re-establishes for itself
and must NOT be carried back. Both directions of the encode/decode pair changed
together, and there is exactly one such pair — every spill consumer (hash-join
batches, external sort, `drainRowsBounded`) goes through it.

So the join-search work paid for an executor defect that predates it: reaching a
new plan shape is also a probe of the operators that shape reaches.

**Verdict: `pgShapedCollapse` stays default OFF.** The peel is orthogonal to it —
it fires with collapse ON or OFF, because an outer pin is unconditional
(`joinPinned`) — and the flip's own gate still has a corpus of two eligible
queries. What has changed is that the collapse arm is no longer a control over an
empty population: {72, 75} now reach the search whichever way the flag is set, so
the next re-run of §3.18's protocol is measuring a real difference for the first
time. The remaining reorderability gap is 03 §4.4's `SpecialJoinInfo` inference —
an outer join that must be *reordered* rather than *pinned above a searched
prefix* — which is where Q78's buried outer link lives.

### 3.21 The spill frame is a serialization contract, and it was broken three ways (P5.9-u, 2026-08-06)

§3.20 closed a wrong-answer bug by adding one byte to the spill frame: TPC-DS
Q72's `d3.d_date > d1.d_date + 5` raised `operator + requires integer operands`
only at the `work_mem` where the join under it spilled, because `encodeDatum`
(`internal/executor/spill.go`) wrote a `KindTime` datum's value and never its
`flagDate`. This item audited the rest of that frame, and the fix turned out to
be the smaller half of the finding.

**Why goopg can have this bug and PG cannot.** A PG Datum never travels alone —
it moves with the `TupleDesc` that names its type OID, and `date`, `timestamp`
and `timetz` are separate types with separate `typoutput` functions
(`postgres/src/backend/utils/adt/date.c`, `timestamp.c`). goopg instead carries
five SQL types on the one `KindTime` carrier and distinguishes them with
per-value state, so **every** serializer has to remember that state. The spill
codec is the only Datum-level serializer in the tree (the storage and wire
codecs both read a declared column type alongside the bytes and can re-derive
what a value is), and it is exactly the one that had forgotten.

**What the audit found.** Three breaks, not one:

| # | lost | shape | how it showed |
|---|---|---|---|
| 1 | the DATE discriminator | **silent** | §3.20, found in production by Q72 |
| 2 | a `timetz`'s UTC offset (`Datum.Scale`, minutes — `NewTimeTZDatum`) | **silent** | found by this audit |
| 3 | `KindEnum` and `KindToastPointer` had no arm at all | loud | found by this audit |

Breaks 1 and 2 share the dangerous shape: `Int` survives, so the value still
*compares* against itself correctly and only its type is gone. That is why
`TestSpillRoundTrip` stayed green for as long as it did — it compared values.
Break 2 is a genuine wrong answer, not a latent one: `compareDatum` normalises a
timetz to UTC through `Scale` (matching upstream `timetz_cmp`), so a spilled
timetz sorted by *local* time against unspilled peers and rendered in the wrong
zone. Measured, not argued — with the `Scale` write removed, the guard's
comparison of `12:00-07` against `13:00+00` flips from `+1` to `-1`.

Break 3 is loud (`decodeDatum` rejects the frame with "unknown datum kind"), so
it is not a wrong answer — but a query that had to spill an enum column simply
could not run.

**The fix is the contract, not the three patches.** `flagDate` is retired;
`Datum.TimeSub` (a `TimeSubtype`, carved out of the existing alignment pad so
the 48-byte layout of `docs/design/perf-optimize/02-datum-pointer-free.md` is
unchanged) replaces it. The point is not the rename — it is that a *field* with
a declared value space can be enumerated, and a flag bit cannot. So the guards
walk the space rather than a hand-picked sample:

- `TestSpillDatumRoundTripCoversEveryKind` iterates `0..datumKindCount` and
  **fails on a kind with no case**, so a new `DatumKind` cannot reach the tree
  without an arm in both halves of the codec.
- `TestSpillDatumRoundTripCoversEveryTimeSubtype` does the same over
  `0..timeSubtypeCount`.
- `decodeDatum` **rejects** an out-of-range subtype instead of clamping it.
  Quietly widening an unknown subtype to "bare timestamp" is precisely the
  failure mode the contract exists to close, so the reader fails loudly in the
  one direction that would otherwise produce a wrong answer.

`Datum.Flags` is deliberately still not serialized. Its one remaining bit,
`flagBigNumeric`, describes a *representation* the decoder re-establishes for
itself (`newNumeric` picks the fast/big mantissa path from what it decodes);
carrying it across would let a decoded numeric claim an arena mantissa it does
not have. That distinction — type state must travel, representation state must
not — is the rule the codec now states in comments at both halves.

**Verification.** Units (0 FAIL) plus the regress-port suite run
baseline-relative in a worktree off clean HEAD: 56 tests, **identical verdicts
and identical diff line counts** on both arms, which is the only meaningful
reading of a runner sitting at 1/52 absolute parity. `tpch-spotcheck` Q12=2
Q13=35 PASS. Both new guards were proven to bite by reverting each fix in turn.

**Still open** (ledger row P5.9-u): `TimeSubTime` and `TimeSubTimestampTZ` are
declared but not populated by their producers, and `compareDatum` still infers
"is a timetz" from `Scale != 0` — which mis-reads a genuine `+00` timetz as a
plain `time`. Wiring the producers and switching that test to `TimeSub` is the
follow-up; it is a behaviour change and needs its own bar.

### 3.22 A RIGHT JOIN's nullable arm may be REORDERED but not PUSHED INTO — and the flip that was supposed to enable it cannot be represented (P5.9-t, 2026-08-06)

P5.9-s peeled the pinned outer spine off the top of a FROM chain and searched
the prefix below it, but `spineLinkSearchable` admitted `JoinTypeLeft` alone.
The follow-up was filed as "port `reduce_outer_joins`
(postgres/src/backend/optimizer/prep/prepjointree.c:3360) so a RIGHT arrives as
a LEFT with swapped sides, then widen the check". **Both halves of that filing
turned out to be wrong, and the second one is the interesting half.**

**The flip cannot be represented.** Upstream's flip swaps a `JoinExpr`'s `larg`
and `rarg`. goopg's `parser.FromExpr` is a `Base RangeVar` plus a **flat**
`[]JoinExpr` whose every right side is a single range var — a strictly left-deep
chain with no node for a nested join. The flipped shape of
`a ⋈ b ⋈ c RIGHT JOIN d` is `d LEFT JOIN (a ⋈ b ⋈ c)`, which that AST cannot
spell. Performing the swap inside the planner's own tree instead is not a
workaround but a different change: a `Join`'s schema is the positional
concatenation of its inputs, so swapping arms renumbers every binding offset and
reorders `SELECT *`. Upstream is free of that only because its Vars are
varno-addressed and `SELECT *` was expanded against the range table at parse
analysis, before the planner ever sees the jointree.

**The flip was never what the seam needed.** Because the chain is left-deep, a
RIGHT JOIN's multi-relation side is on the **left** of the pin — exactly where a
LEFT JOIN's is. `splitOuterSpine` already walks `j.Left`; nothing structural
distinguishes the two cases. What distinguishes them is **nullability**, and
therefore what may be pushed *into* the prefix:

- the ORDER of the prefix is searchable either way. Upstream builds a
  sub-joinlist for an outer join's nullable arm (`deconstruct_recurse`) and
  `make_rel_from_joinlist` recurses into it — the relations inside a nullable arm
  may be joined in any order among themselves;
- the `WHERE` is not pushable. `check_outerjoin_delay` (initsplan.c) delays a
  qual coming from *above* an outer join whenever its relids reach the nullable
  side. Under a RIGHT link the prefix **is** the nullable side, so the whole
  `WHERE` is delayed.

So the landed change is: `spineLinkSearchable` admits a matched LEFT/LEFT or
RIGHT/RIGHT pair (the plan node and the joinlist must **agree** — which member of
the pin is the left side is the entire question the splice answers), and
`prefixNullable` scans the whole spine so that one RIGHT link anywhere holds the
entire `WHERE` in the residual `Filter` above the spine. The prefix's own `ON`
quals are unaffected: they originate *below* the outer join, upstream distributes
them normally, and suppressing them would cost a cross product rather than a
wrong answer.

`prefixNullable` is written as "anything that is not LEFT" rather than "is
RIGHT", so a join type added to `spineLinkSearchable` later is nullable until
someone argues otherwise.

FULL stays declined. Both of its inputs are null-extended, and its USING
coalescing (`UsingLeftCols`/`UsingRightCols`, planner.go) names merged-var
positions a re-associated input would have to be re-checked against.

**Verification.** The wrong answer is not hypothetical and is not subtle. The
executor-level guard runs

```sql
SELECT rj_c.id, rj_a.id, rj_b.id
  FROM rj_a JOIN rj_b ON rj_a.id = rj_b.aid
  RIGHT JOIN rj_c ON rj_b.cid = rj_c.id
 WHERE rj_a.id IS NULL
```

which must answer the single null-extended row. With `prefixNullable` disabled
the seam attaches `rj_a.id IS NULL` to the `rj_a` leaf, that leaf yields nothing,
and **all three** `rj_c` rows come back null-extended — measured, by reverting
the guard. `TestRightJoinSpineAgreesAcrossEnumerators` additionally pins the
unrestricted join's rows against both enumerators, so a lost prefix `ON` qual (a
cross product, which the restricted form cannot see) fails too.

Planner-side: `TestPGShapedSeamSearchesTheNullablePrefixBelowARightJoinSpine`
asserts the spine node's identity, type and `ON` qual survive the splice, that
the prefix enforces its own two `ON` quals and not the spine's, and that zero
leaf-local filters were installed; `TestPGShapedSeamHoldsTheWholeWhereBelowAMixedSpine`
covers `… RIGHT JOIN c LEFT JOIN d`, where reading only the topmost link would
conclude "push freely". Both were proven to bite.

Gates: full units (0 FAIL); `tpch-spotcheck` Q12=2 Q13=35 PASS; the TPC-DS SF0.5
sweep. The corpus contains **no** RIGHT JOIN — `grep` over the TPC-DS and TPC-H
query sets finds none — so the sweep is a no-regression reading rather than a
demonstration, and the demonstration is the two guards above.

**Still open** (ledger row P5.9-t): `reduce_outer_joins`' actual *reductions* are
still unported. PG turns `a ⋈ b RIGHT JOIN c WHERE a.x = 5` into an INNER join
(the strict qual on the nullable side proves no null-extended row survives) and
then pushes `a.x = 5` down freely; goopg keeps the RIGHT and holds the qual
above it. That is a pessimization, never a wrong answer, and it is the reduction
half — not the flip half — that is worth porting, since the flip is unrepresentable
and unnecessary.
