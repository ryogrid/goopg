# M0144-0011a-2 — the ORDERED-step candidate minimum, and the seam census that sized it

Status: LANDED 2026-09-20
Kind: impl
Parent: M0144-0011a
Milestone: M0144 (plan-parity harness, measurement-first era)
Predecessors: `m0144-0011a-ordered-step-ordering-claim.md`,
`m0144-0011a-3-identity-project-descent.md`
Census artefact: `analysis/m0144/m0144-0011a-2-ordered-seam-census.md`

## 1. The task had to be sized before it could be worked

M0144-0011a-3 withdrew this task's expected movement: its three named queries
turned out to be blocked by other mechanisms, so AGENT.md S5 left it unworkable
until a trace named a real query. This loop ran that census first and let the
numbers decide what, if anything, to implement.

**Instrument.** Private `:5595` lane (`tmp/c20a/data-sf025`), HEAD `ee38581b1`,
`GOOPG_PGSHAPED_DP_TRACE=1`, one `psql` per query, the server log read by BYTE
OFFSET so each query's trace is exactly delimited. (A first attempt truncated
the log between queries; because the redirect is `O_TRUNC`, not `O_APPEND`,
writes continued at the old offset and the log filled with NUL holes that grep
read as binary. Offsets, not truncation.)

**Rel-level verdicts over the 99-query corpus:**

| `electOrderedGrouping` decline | queries |
|---|---|
| `gate-precondition` (the ordered input is not `agg.node`) | 44 |
| `cands<2(1)` | 37 |
| `parallel-finalize-agg-present` | 4 |
| elected (≥2 candidates) | 14 |

**What the 37 `cands<2(1)` declines then did at the ORDERED step:**

| seed the decline fell through to | queries |
|---|---|
| `keys>0 contained=true` (claim delivered, NO Sort stacked) | 17 |
| `keys>0 contained=false` (partial claim, Sort stacked) | 10 |
| `keys=0` (no claim at all) | 11 |

So for 27 of the 37, `inputNodePathkeys` — with the `*Aggregate` arm
(M0144-0011a) and the identity-`Project` descent (M0144-0011a-3) in place —
already recovers the ordering the loop would have offered.

**The `keys=0` residue is not a walk-top gap.** In the relaxed arm those 11
queries decline at `anyTranslated=false`, and the set is *exactly* the same
eleven: `Q5 Q14 Q16 Q18 Q22 Q27 Q77 Q80 Q92 Q94 Q95`. Every per-candidate
decline in the corpus reads `DPGROUP decline reason=strategy-or-mode
strategy=0` — strategy 0 is `AggStrategyHashed`. Their lone candidate is a
HASHED aggregate, which emits no order, so no ordering-claim work of any kind
can move them; they belong to the `aggregation-strategy` lineage.

That is also a corpus-wide measurement of the sibling-agreement invariant the
code asserts: `groupingEmissionPathkeys` (the `*Path` twin) and
`aggregateEmissionPathkeys` (the Node twin) decline on the identical query set,
query for query, rather than merely on the shapes a unit test enumerates.

**Conclusion for this task's second bullet** — the walk's remaining
`default: nil` tops (`*MergeJoin`, `*GatherMerge`, `*IncrementalSort`) — is
that it has NO query on this corpus. It is retired here rather than carried as
an open guess; the ledger row records the divergence so it is not lost.

## 2. What was implemented

The first bullet is a real divergence and it was closed:

```go
// before
if len(cands) < 2 { return decline(fmt.Sprintf("cands<2(%d)", len(cands))) }
// after
if len(cands) < 1 { return decline(fmt.Sprintf("cands<1(%d)", len(cands))) }
```

PG has no minimum. `create_ordered_paths` iterates the whole input pathlist:

```c
/* postgres/src/backend/optimizer/plan/planner.c:5337 */
foreach(lc, input_rel->pathlist)
```

A lone `AggPath` is offered there exactly like one of many, and goopg's `< 2`
form was the last place this loop disagreed with that. It was removed for
PG-faithfulness, not because a number improved — see §3, where no parity number
does.

Two tests pin it: `TestElectOrderedGroupingOffersALoneCandidate` (a
single-candidate rel elects the no-sort arm) and
`TestElectOrderedGroupingStillDeclinesAnEmptyRel` (lowering the minimum to one
is not lowering it to zero, and the decline stays pre-mutation).

## 3. Measurement (AGENT.md D2)

### 3.1 A methodological correction, stated plainly

The private `:5595` lane reported **zero** plan changes from the relaxation. The
gate-owned SF0.25 cluster (`:65437`) reported **ten**. The lane is a stale clone
with its own statistics, so its candidate sets differ — it is sound for
*mechanism* traces (which decline fires, what the seed carried) and unsound for
*outcome* claims. An earlier draft of the code comment asserted "byte-identical
either way" on the lane's evidence; that is corrected in the landed comment and
here. Lane for mechanism, gate cluster for outcome.

### 3.2 TPC-DS SF0.25 — shape-inert, not cost-inert

```
before: PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
        CATEGORIES-EXCL-MATCH: join-order=91 join-method=70 scan-type=61 parameterisation=55 aggregation-strategy=44 sort-strategy=60 parallelism=85 qual-placement=25 rendering=26

after:  PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
        CATEGORIES-EXCL-MATCH: join-order=91 join-method=70 scan-type=61 parameterisation=55 aggregation-strategy=44 sort-strategy=60 parallelism=85 qual-placement=25 rendering=26
```

Byte-identical parity lines. The ten queries the plan channel flagged — `Q3 Q19
Q42 Q52 Q55 Q71 Q72 Q85 Q91 Q93`, which are exactly the `keys>0
contained=false` rows of the census table — differ **only in the cost printed
on their ORDER BY `Sort`**: with costs stripped, the 99-query capture diffs to
zero lines (header aside).

That cost move is the intended direction rather than a side effect. The ORDERED
upper rel exists because top-level ORDER BY Sorts were priced by
`DeriveLegacyDisplayCost` — the in-memory comparison term alone, no spill charge
(`upperordered.go` file header, DESIGN §0 F1). Those ten Sorts are now priced by
`addOrderedPaths` over the real `PathAgg` candidate instead of by the prebuilt
seed's legacy estimate.

### 3.3 TPC-H SF1, parallel canonical mode

Same pinned stats epoch as the 0011a pair (`e4a554b2a4cfb710`), binary
`42dfcd6069014ad2`, plans `0bf7e4d1540cf0e4`.

```
PLAN-PARITY: queries=22 match=2 shapediff=20 unparsed=0 missingnode=0 error=0 timeout=0
CATEGORIES-EXCL-MATCH: join-order=16 join-method=10 scan-type=10 parameterisation=7 aggregation-strategy=7 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2
```

Identical to HEAD's. Four queries (Q3, Q10, Q18, Q21) change text; with costs
stripped the diff is again zero lines. Same story as SF0.25.

### 3.4 shape-delta

`queries=99 same=99 changed=0` on the confirming sweep of the exact staged code
(the earlier `changed (10)` sweep was against the previous sweep's cost lines).
Cost-stripped: 0 on both corpora.

### 3.5 stats epoch of both arms

TPC-H `e4a554b2a4cfb710` on both (pinned `GOOPG_ANALYZE_SEED=20260905`). TPC-DS:
consecutive same-day sweeps on the gate-owned `:65437` dataset, no reload.

### 3.6 planning route

PG-shaped search (`GOOPG_PGSHAPED_DP=1`, default). The change is in the
upper-rel pipeline above the search seam.

### 3.7 seam-decline census

Delivered in full — §1 IS the census, at the ORDERED seam, over all 99 SF0.25
queries with no timeout (`timeout 120` per query, none hit).

### 3.8 wall time of every query whose plan changed

No shape changed. The sweep's status-delta channel reported `verdict-changes=none
runtime-moves=1 total-delta=-6.0%` on the confirming run; the one flagged move
belongs to a query whose plan did not change and is host noise on a sub-5s
query. No ledger row for a slower plan is owed.

### 3.9 Movement

`Movement: none` — match 2 → 2 on both corpora, every `CATEGORIES-EXCL-MATCH`
count identical, `ea-ratchet` not applicable. The result is a removed
divergence from PG and a better-priced Sort on fourteen queries, neither of
which is movement under S3.

## 4. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `scripts/tpch-spotcheck.sh` | PASS — Q12 rows=2, Q13 rows=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` (PASS=96, SKIP=3) |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels MATCH on VALUES |
| floor capture + `pg-plan-parity-diff.py` | PASS — TPC-H match 2, TPC-DS SF0.25 match 2 |
| `make ea-ratchet` | `N/A — no estimate, selectivity or statistics code is touched` |

All gates were re-run after a late comment-only edit, because the stamp hashes
the staged `internal/` tree and a comment is part of it.

## 5. What this closes and what it leaves

Closed: the candidate-minimum divergence (bullet 1), and — by measurement, not
by implementation — bullet 2, which has no query on this corpus.

Left, with ledger rows: `gate-precondition` is now by far the largest decline
class (44/99) and is entirely unexamined; and the eleven `keys=0` queries are a
hashed-aggregate election question for the `aggregation-strategy` lineage.
Neither is filed as a child of this task, because neither is an ordering-claim
propagation problem — filing them here would repeat the mistake this task's own
predecessor made.
