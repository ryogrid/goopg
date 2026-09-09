# R29 results — binder-aware escape guard

Implements `DESIGN.md`. One behaviour change, in
`internal/optimizer/planner.go`: `planHasEscapingOuterRef` became a
structural, binder-counting plan walk; the flat walker survives as
`planHasEscapingOuterRefFlat` for unrecognised nodes.

## 1. Prediction vs. outcome

| | predicted | measured |
|---|---|---|
| `lateral` seam declines (TPC-DS) | 1 -> 0 | **1 -> 0** |
| other decline classes | unchanged | unchanged |
| parity categories | may worsen | scan-type 70 -> **71** |

Both sides re-measured under one protocol (90 s timeout, same clone
:5546, same query set). The "9 declines" figure quoted by earlier
rounds was taken at a 60 s timeout and is not comparable.

```
base (pre-R29)   outer-over-derived 3  outer-link-no-sjinfo 3  outer-spine 2  lateral 1   TOTAL 9
R29              outer-over-derived 3  outer-link-no-sjinfo 3  outer-spine 2  lateral 0   TOTAL 8
```

## 2. Parity

```
base   queries=99 match=0 shapediff=70 missingnode=26 error=3 timeout=0
R29    queries=99 match=0 shapediff=70 missingnode=26 error=3 timeout=0
CATEGORIES base  join-order=95 join-method=74 scan-type=70 parameterisation=41
                 aggregation-strategy=81 sort-strategy=82 parallelism=90
                 qual-placement=12 rendering=32
CATEGORIES R29   ... scan-type=71 ... (every other category identical)
```

Exactly one query's verdict moved, and it is the intended one:

```
- Q68 SHAPE-DIFF [join-order,join-method,aggregation-strategy,sort-strategy,parallelism,qual-placement]
+ Q68 SHAPE-DIFF [join-order,join-method,scan-type,aggregation-strategy,sort-strategy,parallelism,qual-placement]
```

## 3. What the new scan-type divergence is (the round's real finding)

Q68 now reaches the PG-shaped search, and the search prices `customer`
differently from both PG and the legacy fallback:

| | base | R29 | PG 18.3 |
|---|---|---|---|
| `customer` | `Index Scan using customer_pkey` | **`Bitmap Heap Scan` + `Bitmap Index Scan on customer_pkey`** | `Index Scan using customer_pkey` |

This is not a regression introduced by the guard: it is a **cost-model
divergence that the decline was concealing**. While Q68 was declined,
the legacy planner happened to emit PG's scan; now that the query is
eligible, goopg's own costing is what answers, and it prefers a bitmap
path where PG takes the plain index. Per the round's stated criterion
(§5 of the design) this is a real result — the eligibility axis moved
forward and handed a concrete, isolated costing bug to the next round.

Q68's other pre-existing scan divergence is unmoved: PG index-scans
`household_demographics` where goopg seq-scans it, in both binaries.

## 4. Correctness gates

- `go test ./internal/optimizer/ ./internal/executor/ -count=1` — ok.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` —
  44 packages ok, zero FAIL.
- TPC-H values digest (22 queries, :5545) — **byte-identical** to
  `r9-partial-jointype-filter/values-tpch-r9.txt`
  (`values-tpch-r29.txt`).
- TPC-DS SF0.5 sweep —
  `PASS=95 (57 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`.

One test file changed: `exprwalk_inventory_test.go`. The pinned Expr
switch `planner.go:planHasEscapingOuterRef` was renamed, not removed —
its six arms moved to `outerRefEscapes`, now shared by the structural
walk and the flat fallback so the two cannot drift. The inventory guard
demanded the key be updated in the same commit; it was.

## 5. Follow-ups filed

- **K32** — `LATERAL` scoping, oracle-established: it governs sibling
  FROM-item visibility only; an enclosing query level stays visible to
  a CTE body and to a non-lateral derived table. Any future "this node
  is a scope boundary" shortcut is unsound for the same reason K27's
  was.
- **K33** — Q68 `customer`: goopg prices a bitmap path below a pkey
  index scan where PG takes the index. First cleanly isolated
  bitmap-vs-index costing divergence in this workstream.
- 8 seam declines remain: `outer-over-derived` 3, `outer-link-no-sjinfo`
  3, `outer-spine` 2. All are outer-join-shape declines; none is
  `lateral` any more.
