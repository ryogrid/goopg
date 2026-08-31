# 0125-0034 — A join over a set operation stops being a Cartesian product

**Task:** `M0125-0034` (C1 of `M0125-0026`'s timeout-class taxonomy)
**Branch:** `tpcds-fix2`
**Evidence:** `analysis/m0125-0026-timeout-plans/README.md` §"The dominant
mechanism"; this loop's artefacts under `analysis/m0125-0034/` and the plan
capture `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0034/`.

## 1. The defect

`M0125-0026` classified the TPC-DS timeout class and found one dominant
mechanism: `Nested Loop (CROSS)` with the equi-join predicate demoted to a
`Filter`, at every site where a join input was a set operation, a CTE
reference or a derived aggregate. This task takes the **set-operation arm**.

Q71 is the acceptance query and shows the shape exactly:

```
goopg (before):  Nested Loop (CROSS)              PG 18.3:  Nested Loop
                   -> Seq Scan on item                        -> Parallel Append
                   -> Append (3 UNION ALL branches)           -> Index Scan using item_pkey
                 Filter: sold_item_sk = i_item_sk                   Index Cond: i_item_sk = sold_item_sk
```

`item` is 18,000 rows and the union of the three sales channels is ~2.5 M at
SF=0.5, so goopg was materialising a ~4.5×10^10 row-pair product where PG does
one index probe per outer row.

## 2. Why the predicate never reached the Join

Two independent planner defects, stacked. Either one alone reproduces the
`CROSS`, which is why only fixing both moves the queries.

### 2.1 `collectScanOutputNames` cannot see a `*SetOp` (the primary cause)

`pushOneConjunct` (`internal/planner/pushdown.go`) promotes a `CROSS` to an
`INNER` join when a WHERE conjunct spans both inputs. Before committing it
validates the conjunct **by name** against the scans in the join's subtree —
`allColumnRefNamesInScope`, added for TPC-H Q9 because width-based
classification can produce a `sideMixed` verdict for a conjunct that actually
references a relation *outside* this join.

That name walk, `collectScanOutputNames`, enumerated node kinds explicitly and
had **no `*SetOp` case**. For `item, (… UNION ALL …) tmp` it therefore
collected only `item`'s column names, `sold_item_sk` was "not in scope", and
the conjunct was declined before the promotion logic ran at all.

The failure mode of an under-enumerated permissive check is silent: a missing
case is not a wrong answer, it is a missed optimisation, and nothing in the
plan says why.

### 2.2 M0097-0058's `containsSetOp` bailout (the secondary cause)

Commit `9ddbc679` added a blanket decline: `pushOneConjunct` refused the
`CROSS → INNER` promotion, and `planner.go` refused the hash algorithm,
whenever either side contained a `SetOp`/`RecursiveUnion`. The stated premise
was that the predicate's `ColumnRef` indices "refer to the global FROM-clause
schema rather than the subquery's own output", producing
`index out of range [57] with length 1` when the executor drains the build
side.

The premise does not survive inspection:

* `SetOp.Output()` returns `n.Left.Output()` — the **narrow projected
  schema**. So the planner's `leftWidth`/`totalWidth` and the executor's
  `len(o.left.Schema())` agree; there is no global-vs-narrow discrepancy at the
  join node.
* The executor never evaluates a join key against a bare build row. Both
  `buildHashRight` and `buildHashRightWithCTID`
  (`internal/executor/operators_join_agg.go`) construct a `keyRow` of
  `leftWidth + rightWidth` and copy the build row into its tail, so a key
  carrying a global index reads the slot it was resolved against.

In the shape the guard was written for (the set operation one level down,
behind a `*Project`) `collectScanOutputNames` *did* find the names — via its
`*Project` case — so the guard fired on its own. Lifting it there yields a hash
join with byte-identical answers, verified both in unit tests and on the SF0.5
cluster.

Both bailouts are therefore retired. The guard on the explicit `JOIN … ON`
path (`internal/planner/planner.go`, the `jn.Algo` assignment) is **left in
place** — this task's evidence is entirely comma-FROM — and is carried as a
deferral-ledger row.

### 2.3 The NLI schema flip, uncovered by the fix

With the promotion restored, Q71 stopped timing out and started **erroring**:
`aggregate sum requires numeric argument in v0`.

`pickInnerScanForNLI` (`internal/planner/nl_index_join.go`) may choose the
**left** child as the index-probed inner, which makes the right child the
loop driver and emits `outer ++ inner` — the flip of `Left ++ Right`. The
function already declines that flip when the would-be outer is an `*Aggregate`
or a `*Values`, both because their columns are not tracked in
`remapWithBindings`' scan map and the downstream refs cannot be rebound.

A `*SetOp` outer has exactly the same property, but the case had been
**unreachable**: this branch only accepts `JoinTypeInner`, and §2.2's bailout
guaranteed a set-operation join was never promoted out of `CROSS`. Restoring
the promotion made it reachable for the first time, and Q71 planned
`Append ++ item` while the outer query's `sum(ext_price)` and its four group
keys stayed bound to `item ++ Append` — `sum()` was handed `i_brand`, a text
column. Declining the flip for a set-operation outer routes the join back to
the hash path, which keeps the canonical schema.

Note that the flipped plan is structurally PG's own (`Append` driving,
`item` probed by `item_pkey`). Making the flip *correct* rather than declined
is a strictly better outcome and is carried as a ledger row; it needs
`remapWithBindings` to track set-operation output columns.

## 3. The change

| file | change |
|---|---|
| `internal/planner/pushdown.go` | `collectScanOutputNames`: add the `*SetOp, *RecursiveUnion` case (names from `n.Output()`, no recursion into branches — a branch's internal names are not in the outer query's scope) |
| `internal/planner/pushdown.go` | `pushOneConjunct`: retire both M0097-0058 `containsSetOp` bailouts (promotion + hash algorithm) |
| `internal/planner/nl_index_join.go` | `pickInnerScanForNLI`: decline the left-as-inner flip when the would-be outer contains a set operation, alongside the existing `*Aggregate` / `*Values` declines |
| `internal/executor/setop_join_promotion_test.go` | new — four tests pinning the promotion, the answer, the NLI-flip decline, and the `INTERSECT`-behind-`Project` arm |

## 4. Measurement

Host caveat: the nightly CI batch held the host for this whole loop
(`load average ≈ 9.9`), so **every second below is inflated and none of them is
a timing result**. Row counts and value checksums are unaffected and are what
the acceptance is stated in; the sweep reports carry the harness's own
`FORCE=1 — … PER-QUERY SECONDS ARE NOT [VALID]` stamp.

### 4.1 Acceptance — met

```
Q71  PASS  580 rows  ck=521a7af7606d10c1
```

identical to the git-tracked PG 18.3 oracle row `71|OK|580|521a7af7606d10c1`
(`analysis/m0125-0034/sweep-q71/`).

### 4.2 The class

Plain-EXPLAIN `Nested Loop (CROSS)` counts, newest prior capture vs this one
(`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0034/`):

| Q | before | after |
|---|---:|---:|
| Q5 | 9 | **0** |
| Q8 | 1 | **0** |
| Q14 | 17 | **0** |
| Q54 | 2 | **0** |
| Q71 | 1 | **0** |
| Q30 | 1 | 1 |
| Q64 | 4 | 4 |
| Q65 | 2 | 2 |
| Q81 | 1 | 1 |

**30 Cartesian products eliminated.** The four survivors are C1's *other*
arm — a CTE reference or a derived aggregate on the join input, not a set
operation — and remain open under this task.

Answers (`analysis/m0125-0034/sweep-c1recheck/`, `sweep-c1setop/`):

| Q | before | after |
|---|---|---|
| Q5 | TIMEOUT | **PASS** 100 rows |
| Q8 | TIMEOUT | **PASS** 0 rows `ck=1f18d650d205d71d` |
| Q14 | TIMEOUT | **PASS** 200 rows |
| Q54 | TIMEOUT | **PASS** 0 rows `ck=1f18d650d205d71d` |
| Q71 | TIMEOUT | **PASS** 580 rows `ck=521a7af7606d10c1` |

The SF0.5 timeout class goes **12 → 7**.

### 4.3 Regression surface — swept exhaustively, not sampled

The change can only affect a query containing a set operation, so all 21 such
TPC-DS queries were run rather than a sample
(`analysis/m0125-0034/sweep-setop-all/`). The 15 that were not part of the
rescue are unchanged: `PASS=15 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
every row count and every checksum equal to the 2026-07-30 full-gate baseline.
Q4 stays `SKIP` (its PG oracle itself times out).

### 4.4 Other gates

* `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS.
* `scripts/tpch-spotcheck.sh` — `RESULT=PASS`, `Q12=2 / Q13=35`.
* `make plan-diff LABEL=warm-stats-base` (warm TPC-H server, :65433) — 10 of 22
  DIFFER, and **every changed line in all ten is a `Filter:` / `Sort Key:`
  column qualification**, i.e. `M0125-0039`'s rendering change, which the
  `warm-stats-base` snapshot predates. Zero structural change: **this task is
  TPC-H-plan-inert**. Re-pinned as
  `plan_snapshots/m0125-0034-setop-join-promotion.txt` so the next loop does
  not re-diagnose the qualification noise;
  `analysis/m0125-0034/plan-diff-warm-stats-base.txt` keeps the reading.
  (Diffing against the S-cold label `m0125-0005-relsize-default-stage2` on a
  warm server reports 22/22 for stats reasons and is the wrong comparison —
  noted here because it cost time.)

## 5. What is still owed

Carried as deferral-ledger rows, dated 2026-07-31:

1. **C1's non-set-operation arm** — Q30 Q64 Q65 Q81 still emit 8 crosses over
   a CTE reference or a derived aggregate. `M0125-0034` stays open for them.
2. **The `JOIN … ON` `containsSetOp` guard** in `internal/planner/planner.go`
   is untouched; the same reasoning applies but no evidence in this task's
   capture exercises it.
3. **The NLI flip is declined, not fixed.** PG's own Q71 plan is the flipped
   shape; making `remapWithBindings` track set-operation output columns would
   let goopg take it.
4. **`collectScanOutputNames` is still an explicit enumeration.** `*Distinct`,
   `*DistinctOn`, `*Limit`, `*WindowAgg`, `*IndexOnlyScan` and others remain
   absent, and each absence is a silent `CROSS` for a FROM subquery of that
   shape. Only the set-operation case had measured evidence behind it.
