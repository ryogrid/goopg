# C-06 / `c06-collapse-flip-moves-q13` — why the search wins a Merge Left Join on TPC-H Q13

**Measured 2026-09-07 on `cd9218645647626effe63a21d217f10e63be9072`** (branch
`plan-narrowing-and-etc`), against a private clone of the TPC-H SF=1 goopg
cluster (`bench/tpch/runtime_goopg/data` → `/tmp/c06-data`, port 5539,
`GOOPG_ANALYZE_SEED=20260905`, `ANALYZE` in the measuring session,
`max_parallel_workers_per_gather = 0`). PG 18.3 oracle read from the reference
cluster on 65432.

## Verdict

**MIS-GENERATION.** Not mis-costing, and not a discard bug.

`jointypeForDirection` (`internal/optimizer/joinpaths.go:155`) *deliberately*
declines the commuted direction of an outer join rather than emitting PG's
`JOIN_RIGHT`. `makeJoinRel` does call `addPaths` in both directions
(`joinsearchlevel.go:658,661`), so the second call is made and returns nothing.
The consequence on Q13 is that the only hash path the search can price must
build the **1.5 M-row `orders`** side; that build spills, costs 336 448, and
loses to the merge join at 316 090. PG wins the same query with a **`Hash Right
Join`** that hashes the 150 k-row `customer` — the path goopg withheld.

The function's own doc comment states the narrowing and its justification:

> The reversed direction is DECLINED rather than emitted as PG's JOIN_RIGHT /
> JOIN_RIGHT_SEMI / JOIN_RIGHT_ANTI. That is a deliberate narrowing, not an
> oversight … Withholding a path can only lose an optimisation, never produce a
> wrong answer, **and nothing selects these paths today in any case.**

The final clause is what has expired. C-04a/C-04b made LEFT and RIGHT links
collapse-dependent, so with `GOOPG_PGSHAPED_COLLAPSE` at its shipped default
(**on**) a two-table LEFT JOIN now enters the search — and Q13 is exactly the
case where the withheld direction is the winner. The narrowing stopped being
inert.

## R4 first: were both candidates generated?

09 §1 R4 (five wrong Q8 cost hypotheses) says instrument `addPath`, not the cost
functions. `pathtrace.go`'s `DPPATH` channel already does this; it is enabled by
`GOOPG_PGSHAPED_DP_TRACE=1`. The Q13 joinrel `{customer, orders}` on the ON arm
(`evidence/dppath-on.txt`):

| producer | jointype | startup | total | verdict |
|---|---|---:|---:|---|
| `mergejoin` | left | 0.62 | **316 089.88** | **accepted (wins)** |
| `join.hash` | left | 213 116.25 | 336 448.25 | dominated |
| `nestloop.index` | left | 0.62 | 1 492 084.73 | dominated |
| `join.nestloop` | left | 0.25 | 3 375 106 249.25 | dominated |

Both a hash and a merge path *were* generated and both *were* compared. The
merge join is genuinely cheaper **in the model as the search sees it**, so no
cost term is misbehaving between these two candidates. The defect is one level
up: the set of candidates is short by one.

`DPTRACE end top={customer+orders} pairs=1 declined=0 status=ok` — one pair,
`outer={customer} inner={orders}`. The reverse orientation of that pair produced
zero paths.

## Where the hash path's cost goes

`hashJoinCost` (`cost_funcs.go:512`) with `inner = orders`:

```
build   = (cpu_operator*1 + cpu_tuple) * 1 499 850 + inner.Total(97 273) = 116 021
spill   = seq_page_cost * innerPages                                    ≈  97 095   (startup)
        + seq_page_cost * (innerPages + 2*outerPages)                   ≈ 100 000   (run)
```

`hashsize.Choose` returns `NBatch > 1` for a 1.5 M-row, 448-byte-wide build at
`work_mem = 64MB`, so ~197 k of the path's 336 k total — **59 %** — is the spill
of a build side that never had to be the build side.

## The counterfactual, priced by goopg's own cost model

A throwaway probe binary (patch **not committed**; `jointypeForDirection`
returns `(JoinRight, true)` for the commuted containment under
`GOOPG_C06_PROBE=1`) makes the search price the direction it currently withholds
(`evidence/dppath-probe.txt`):

| producer | jointype | startup | total |
|---|---|---:|---:|
| `join.hash` | **right** | 7 101.25 | **124 999.25** ← accepted, wins |
| `mergejoin` | right | 18 122.58 | 298 847.95 |
| `mergejoin` | left | 0.62 | 316 089.88 |
| `join.hash` | left | 213 116.25 | 336 448.25 |

`2.53×` cheaper than the merge join the stock binary wins, `2.69×` cheaper than
the uncommuted hash. Whole-statement cost `154 492` against the stock ON arm's
`338 223`. The emitted plan is

```
Hash Right Join  (cost=7101.25..124999.25)
  Hash Cond: (orders.o_custkey = customer.c_custkey)
  ->  Seq Scan on orders
  ->  Index Only Scan using customer_pk on customer
```

i.e. **PG's plan** (`Hash Right Join`, `Index Only Scan using customer_pk` on
the hashed side, seq scan on `orders`; PG cost 56 163.71, whole statement
67 196.08).

`createPlanNode` already has the arm and the executor runs it: on the same
cluster the commuted plan's 34 result rows are **byte-identical** to the stock
merge plan's. Runtime, one fresh capped server per arm, two runs each, same
session, stats warmed in-session:

| arm | plan | runtime |
|---|---|---:|
| stock, collapse ON | Merge Left Join | 6.06 s / 6.64 s |
| probe, collapse ON | Hash Right Join | 4.36 s / 4.52 s |

So the withheld path is not merely cheaper on paper; it is ~30 % faster.

## Correction to the item's premise: 66 218 and 338 223 are not comparable

The C-06 row and the ledger row both read the flip as "the search wins a Merge
Left Join at 5× the cost of the hash plan it also had available". **The search
never had the 66 218 plan.** On the OFF arm the join does not enter the search at
all (`evidence/dppath-off.txt`):

```
DPTRACE problem nrels=1 rels=customer
DPTRACE seam-spine nspine=1 nrels=2 nprefix=1
```

The LEFT link pins (`joinPinned` returns `!collapseJoins`), the seam peels it
onto the spine, and the join node is built and priced by the **plan-tree
estimator**, not by `hashJoinCost`. That estimator is a different, much cheaper
model, and the two numbers are in different currencies:

| quantity | search (`DPPATH`) | plan-tree estimator (OFF arm EXPLAIN) |
|---|---:|---:|
| scan of `orders` | 97 273.00 (seq) | 29 998.50 — *and the ON arm prints the same 29 998.50 for an **Index Scan**, i.e. the printed figure is method-blind* |
| hash join startup | 213 116.25 | **0.25** (no inner-side cost, no build charge at all) |
| joinrel rows | 1 500 000 | 2 358 304 |

`60 307.79` for the OFF arm's `Hash Left Join` appears in no `DPPATH` line. The
OFF arm's whole 66 218 is legacy-estimator arithmetic. Under one consistent
model — the search's — the shapes rank: commuted hash 124 999 < merge 316 090 <
uncommuted hash 336 448.

This does not weaken the C-06 decision (the flag stays; see below). It changes
what the blocker *is*: not "the search mis-prices a merge join", but "the search
is missing a path, and the OFF arm's apparent bargain is an artefact of a second
cost model".

Two secondary observations, recorded and **not** acted on here:

- goopg's search prices the `orders` seq scan at 97 273 where PG prices it at
  46 564 on essentially the same page count (goopg `relpages` 28 435, PG 27 814).
  The gap is ~53 800 over 1.5 M rows ≈ **0.0359/row for the `NOT LIKE` qual**,
  against PG's single `cpu_operator_cost` of 0.0025 — a ~14× per-row charge for
  a pattern-match qual. Not load-bearing for this diagnosis (it inflates both
  candidates), but it is why goopg's commuted-hash 124 999 is still 2.2× PG's
  56 164.
- The plan-tree estimator printing identical costs for a seq scan and an index
  scan of the same relation is the `c20a-two-estimators-still-two` surface,
  seen from the other side.

## Why this is not landed here

Removing the decline is not a small safe fix:

- it gives the search two spellings of one join (`JOIN_LEFT` on (A,B) and
  `JOIN_RIGHT` on (B,A)), which is the exact coupling C-03b withheld so that
  C-04 would not have to prove both directions at once;
- the probe shows `mergejoin jointype=right` paths appear too, so it is not one
  path but a family;
- SEMI/ANTI must stay declined (`JOIN_RIGHT_SEMI`/`JOIN_RIGHT_ANTI` have no
  goopg executor), so the relaxation has to be LEFT-only and fail-closed;
- it moves plans, so it needs the full bar: units, TPC-H 24/24 by values plus a
  timing pass on every query whose plan changed, TPC-DS SF0.5 sweep, and a
  `plan_snapshots/` re-pin — and re-pinning is explicitly off-limits this
  session (C-19h may be re-pinning).

Filed as a TODO_ALL successor row instead, with this document as its diagnosis.

## What this does and does not unblock

**Unblocks C-06's stated blocker, and only that.** The ledger's resume condition
was "find why the search prices a Merge Left Join over the index-only Hash Left
Join at 5× the cost and still wins it". Answered: it does not have the cheaper
hash, because the commuted direction is withheld by design; and the "5×" is a
comparison across two cost models. C-06 itself **stays blocked and the flag
stays** — retiring `GOOPG_PGSHAPED_COLLAPSE` is still a plan-parity regression
until the commuted direction exists, and the C-20f owner decision (the hatches
stay, an exception becomes a precedent, deletion is irreversible) is unchanged
by anything measured here.

**Does not transfer to C-20c / C-20d / C-20e / C-20f / C-20g.** Each has its own
measured movement and none of them is Q13. This finding is specific to a
statement whose join enters the search as a two-relation OUTER problem, which is
the regime C-04a/C-04b opened and which `GOOPG_PGSHAPED_COLLAPSE` is the switch
for. `GOOPG_INDEXKEY_HARVEST` (C-20c) moves a scan-path producer, not a join
direction; the C-20d/e/f/g flags likewise. Nothing here should be read as
"their movements have the same cause" — an earlier agent already established
that one item's decline did not transfer to its apparent sibling, and this
diagnosis is not evidence against that.

## Reproduction

```
go build -o /tmp/c06-goopg ./cmd/goopg
cp -a bench/tpch/runtime_goopg/data /tmp/c06-data && rm -f /tmp/c06-data/postmaster.pid
GOOPG_CG_UNIT=c06 GOMEMLIMIT=12GiB GOGC=100 GOOPG_ANALYZE_SEED=20260905 \
  GOOPG_PGSHAPED_DP=1 GOOPG_PGSHAPED_COLLAPSE=1 GOOPG_PGSHAPED_DP_TRACE=1 \
  scripts/goopg-test-run.sh /tmp/c06-goopg start -D /tmp/c06-data --listen 127.0.0.1:5539 \
  > /tmp/c06.server.log 2>&1 &
# then, in ONE psql session: ANALYZE customer; ANALYZE orders; ... ; EXPLAIN <Q13>
# the DPPATH/DPTRACE lines are in /tmp/c06.server.log
```

Flip `GOOPG_PGSHAPED_COLLAPSE` to `0` for the other arm. Files under
`evidence/` are the three captures verbatim.
