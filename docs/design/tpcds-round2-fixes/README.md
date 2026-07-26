# TPC-DS SF=1 Round 2 — goopg-only errors, row-count divergences, and timeouts

| field | value |
|-------|-------|
| status | design |
| date | 2026-07-27 |
| branch | `tpcds-fix2` (round 1 landed on `tpcds-error-fix`) |
| parent report | `analysis/tpcds-sf1-goopg-20260726.md` |
| parent design | `docs/design/tpcds-section4.2-fixes/README.md` |
| chapters | single-file bundle; split only if a section outgrows this document |

---

## §0 Summary

The 2026-07-26 re-benchmark left goopg at **75 of 99** TPC-DS SF=1 queries versus
PostgreSQL 18.3's **91**. This document establishes the root cause of every
remaining goopg-only failure and designs the fix for each.

The headline finding is that **three of the four correctness classes are the same
defect class**: goopg's planner contains **fourteen** independent hand-written
expression walkers, each a partial copy of the others, and each silently passing
through the `Expr` node kinds it does not enumerate. Silence is the problem — a
walker that skips `IsNullExpr` does not fail, it just leaves a stale
`ColumnRef.Index` behind, and the query returns the wrong answer.
`internal/planner/plan.go` declares **32** concrete `Expr` types; the walker
responsible for the known wrong answers enumerates 11, and the worst pair
enumerates **4**.

The second finding is independent and equally sharp: `MultiHashJoin.Tables` is
sorted by **table OID**, but the conjuncts in `MultiHashJoin.Filters` still carry
**FROM-clause-cumulative** column indices. One pass consumes the latter as if they
were the former, and a `date_dim` predicate ends up evaluated against `store`.
Worse, once that pass has pushed a conjunct into a table's `Filter`, the later
remap never revisits it — `applyJoinTreePosMap`'s `*MultiHashJoin` arm remaps
`n.Filters` and then **returns without recursing into `n.Tables[i]`**
(`internal/planner/bushy.go:2461-2474`). The corruption is therefore permanent.

Neither bug is a missing feature. Both are silent wrong answers in code that has
been shipping.

**Coverage, stated plainly.** This document establishes a verified root cause and a
landing fix for **6** of the 24 in-scope queries (Q8, Q47, Q50, Q72, Q76, Q83). Q39
and Q49 get a measurement plan, not a fix, because neither has a confirmed
mechanism yet. The 16 timeouts get one hypothesised primary mechanism (§7.1) whose
fix ships **behind a flag that defaults off**, so nothing in that class changes
until a later, separately-measured commit flips it. §9 states which phase does
what.

---

## §1 Scope

### §1.1 In scope

Derived from `/tmp/tpcds-bench-v4.txt` (the raw 2026-07-26 sweep) — every query
where **PostgreSQL 18.3 succeeds and goopg does not**.

| class | queries | count |
|-------|---------|-------|
| goopg-only ERROR | Q8, Q39 | 2 |
| row-count divergence | Q47, Q49, Q50, Q72, Q76, Q83 | 6 |
| goopg TIMEOUT, PG OK | Q5, Q10, Q14, Q30, Q31, Q51, Q54, Q64, Q65, Q67, Q69, Q71, Q78, Q81, Q82, Q88 | 16 |

### §1.2 Explicitly out of scope

| query | reason |
|-------|--------|
| Q36, Q70, Q86 | `dsqgen` subquery-in-FROM generation artifact; **PG also fails**. Recorded as SKIP in `pg_results.txt`. |
| Q4 | **PG also times out** (639 s at a 600 s limit). Not a goopg-only failure. |
| Q11, Q74 | goopg succeeds where PG times out. No gap. |

### §1.3 Resolved during scoping — harness artifacts, not engine bugs

`analysis/tpcds-sf1-goopg-20260726.md` §5 lists Q35, Q45 and Q46 with an unknown
(`?`) row count. Re-running Q45 and Q46 against both servers gives **14** and
**100** rows respectively — identical on both engines. The `?` came from
`scripts/tpcds-bench-compare.sh`, which

1. does not source `bench/tpch/env_goopg.sh`, so `psql` leaves `PATH` partway
   through the sweep — every `*_explain.txt` and `*_result.txt` from Q36 onward is
   the stub `timeout: failed to run command 'psql': No such file or directory`;
2. extracts the row count with `tail -1` on the `(N rows)` marker, which reports
   only the **last** result block of a multi-statement query file (Q14, Q23, Q24,
   Q39 each contain two statements); and
3. runs goopg and PG concurrently under `&` … `wait`, contaminating both timings.

Fixing the harness is a prerequisite for the round-2 report to be trustworthy. Q35
(525 s) still needs its row count resolved.

### §1.4 Reproduction environment

| | goopg | PostgreSQL 18.3 |
|---|---|---|
| endpoint | `127.0.0.1:65433` | `127.0.0.1:65432` |
| database / role | `postgres` / `postgres` | `tpcds` / `ryo` |
| data dir | `bench/tpch/runtime_goopg/data` | `bench/tpch/runtime_goopg/pgdata` |
| queries | `bench/tpch/runtime_goopg/tpcds-data/queries/query{N}.sql` | same files |

Row counts of all 25 tables are identical on both engines
(`store_sales` 2,880,404 · `date_dim` 73,049 · `item` 18,000 · `customer` 100,000).

---

## §2 RC-1a — incomplete expression walkers (Q76, Q72)

### §2.1 Reproduction

```sql
-- goopg 0 rows, PG 64858
select count(*) from store_sales, item, date_dim
 where ss_customer_sk is null
   and ss_sold_date_sk = d_date_sk
   and ss_item_sk = i_item_sk;
```

Replacing the `IS NULL` with an ordinary comparison makes the same query correct:

```sql
-- goopg 26786, PG 26786  ✓
... where ss_quantity = 1 and ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk;
```

`EXPLAIN` on the failing form:

```
Multi-Way Hash Join (3 tables)
      Filter: (<*planner.IsNullExpr>)
  ->  Seq Scan on public.date_dim
  ->  Seq Scan on public.item
  ->  Seq Scan on public.store_sales
```

### §2.2 Root cause

`remapByPosMap` (`internal/planner/bushy.go:2154`) rewrites `ColumnRef.Index`
after the join order has been rearranged. Its type switch enumerates exactly
eleven kinds:

```
ColumnRef, BinaryOp, UnaryOp, FuncCall, ExtractExpr, CastExpr,
InExpr (.Operand only), CaseExpr, ExistsExpr, SubqueryExpr, ArraySubqueryExpr
```

`internal/planner/plan.go` declares 32 concrete `Expr` types. The container kinds
that carry child expressions and are **not** enumerated are:

| kind | child `Expr` slots |
|---|---|
| `IsNullExpr` | `Operand` |
| `IsBoolExpr` | `Operand` |
| `IsDistinctFromExpr` | `Left`, `Right` |
| `CollateExpr` | `Operand` |
| `RowExpr` | `Elems []Expr` |
| `MultiAssignSubqElem` | `Row *MultiAssignSubqRow` |

(`MultiAssignSubqRow` itself holds a `Plan Node`, not an `Expr`, so it is an
opaque-scope **leaf** for traversal purposes, not a container.)

plus four skipped child slots inside kinds that *are* enumerated: `InExpr.List`,
`InExpr.Args`, `ExistsExpr.Args` and `SubqueryExpr.Args` (the last three are
PARAM_EXEC-style argument expressions evaluated against the **current outer row**,
so a stale index there is a second, latent wrong-answer source).

There is no `default:` arm, so an unenumerated node is a silent no-op.

### §2.3 Why it yields exactly zero rows

`rewriteMultiWayChain` (`internal/planner/bushy.go:1719`) sorts `mh.Tables` by
`Table.OID`. For the reproduction the FROM order is `store_sales, item, date_dim`
and the OID order is `date_dim, item, store_sales`.

| relation | FROM-cumulative range | post-sort MHJ range |
|---|---|---|
| `store_sales` (23 cols) | 0–22 | 50–72 |
| `item` (22 cols) | 23–44 | 28–49 |
| `date_dim` (28 cols) | 45–72 | 0–27 |

`ss_customer_sk` sits at FROM index 3 and should be remapped to 50 + 3 = 53.
Because `applyJoinTreePosMap` (`bushy.go:2456`) → `remapByPosMap` never descends
into `IsNullExpr`, index 3 survives. Index 3 in the post-sort schema is a
`date_dim` column, which is never NULL — so the predicate is never true and the
query returns 0 rows. That is the observed behaviour exactly, and it explains why
the `ss_quantity = 1` control (a bare `BinaryOp`, which *is* enumerated) is correct.

**Q72** is the same defect one level deeper. Its target list contains
`sum(case when p_promo_sk is null then 1 else 0 end)`, which reaches
`remapByPosMap` via `remapAggExprsWithBindings` (`bushy.go:2098`). The `CaseExpr`
arm exists; the `IsNullExpr` inside `CaseWhen.When` does not.

### §2.4 The full walker inventory

The same defect class is present across **fourteen** walkers. The three most
complete are `walkColumnRefs` (`pushdown.go:350`, 14 arms covering 17 types — it
handles `CastExpr`/`IsNull`/`IsBool`/`IsDistinctFrom`/`Collate`/`RowExpr` and is
missing only `ArraySubqueryExpr`), `remapColumnRefsToSchema` (`planner.go:11321`,
13 arms) and `shiftColumnRefsBy` (`planner.go:11538`, 13 arms). The eleven below
are stale partial copies of those.

| walker | file:line | missing container kinds | live consequence |
|---|---|---|---|
| `remapByPosMap` | `bushy.go:2154` | IsNull, IsBool, IsDistinctFrom, Collate, Row; `InExpr.List`; `.Args` on In/Exists/Subquery | **wrong answers — Q76, Q72** |
| `visitColumnRefsByName` | `bushy.go:1653` | Cast, IsNull, IsBool, IsDistinctFrom, Collate, Row, `InExpr.List` | `extraInScans` (`bushy.go:1625`) returns a vacuous `true` for an `IS NULL`-only conjunct, capturing out-of-subset conjuncts into `mh.Filters` |
| `visitColumnRefsForTable` | `bushy.go:415` | Cast, IsNull, IsBool, IsDistinctFrom, Collate, Row, ArraySubquery | `tableForCol` (`bushy.go:391`) mis-partitions; becomes a wrong-answer bug the moment §7.3's `shouldAttachBeforeMHJ` gate opens |
| `visitColumnRefs` | `bushy.go:2838` | Cast, IsNull, IsBool, IsDistinctFrom, Collate, Row | `reresolveExprByName` / `reresolveJoinByName` silently skip re-resolution |
| `conjunctIsLocalEligible` | `local_filters.go:89` | Cast, IsNull, IsBool, IsDistinctFrom, Collate, Row, ArraySubquery, MultiAssignSubq* | a `SubqueryExpr` hidden under a `CastExpr` is invisible → conjunct wrongly declared leaf-local |
| `localizeExprToLeaf` | `local_filters.go:268` | Cast, IsNull, IsBool, IsDistinctFrom, Collate, Row | indices not rebased → leaf `Filter` reads the wrong column |
| `shiftColumnRefs` (closure) | `mhj_input_rewrite.go:667` | Cast, Case, In, Extract, IsNull, IsBool, IsDistinctFrom, Collate, Row | §3's shift skips those subtrees |
| `cloneExprForShift` | `mhj_input_rewrite.go:719` | same as above | **must move in lockstep with the line above** — see §3.4 |
| `cloneExprShiftIdx` | `nl_index_join.go:777` | Case, In, Extract, IsNull, IsBool, IsDistinctFrom, Collate, Row | NLI key/residual shifting |
| `exprSide` | `planner.go:5047` | In, IsNull, IsBool, IsDistinctFrom, Collate, Row | join-side classification |
| `formatExprPGReg` | `operators_explain.go:678` | Case, Extract, IsNull, IsBool, IsDistinctFrom, Collate, Row, … | EXPLAIN prints `<*planner.IsNullExpr>` |

### §2.5 Design

Patching `remapByPosMap` alone would fix Q76 and leave ten loaded guns. The
structural fix is one shared traversal primitive plus a test that makes
incompleteness a **build failure** rather than a benchmark diff.

New file `internal/planner/exprwalk.go`:

```go
// exprChildSlots returns addressable pointers to every direct child
// Expr slot of e, together with a classification.
//
//   kindOrdinary    — descend into children normally
//   kindLeaf        — no child Expr slots (constants, ColumnRef, ParamRef, …)
//   kindOpaqueScope — has children, but they belong to an inner query
//                     scope: Subquery/Exists/ArraySubquery/In-with-Plan/
//                     MultiAssignSubq. Callers that today express
//                     "out of scope" by *omission* must express it here
//                     explicitly.
func exprChildSlots(e Expr) (kids []*Expr, kind exprKind)
```

with three thin drivers on top — `walkExprRefs` (read-only),
`rewriteExprRefsInPlace` (mutate), `cloneExprRefs` (structural copy) — and every
walker in §2.4 reimplemented as one driver call plus a per-kind callback.

**Correction from review — one classification is not enough.** The four walkers
that touch subquery-bearing kinds do four *different* things, and collapsing them
into a single `kindOpaqueScope` would silently change behaviour:

| walker | behaviour on `SubqueryExpr` / `ExistsExpr` |
|---|---|
| `walkColumnRefs` (`pushdown.go:357`) | fires the `onOuter` **callback** — a signal the caller acts on |
| `conjunctIsLocalEligible` (`local_filters.go:97`) | sets `eligible = false` — a **veto** |
| `visitColumnRefsForTable` (`bushy.go:422`) | matches and does nothing — **silent ignore** |
| `remapByPosMap` (`bushy.go:2193-2211`) | **descends** into `x.Plan` via `remapOuterRefsInSubplan`, translating `OuterColumnRef.Index` |

So the driver takes a **per-caller scope policy** (`skip` / `signal` / `veto` /
`descend`), and `exprChildSlots` only reports *that a node opens an inner scope*.
`remapByPosMap` is in fact the opposite of opaque — crossing the boundary is its
job.

Two typing constraints the signature must respect:

- `MultiAssignSubqElem.Row` is declared `*MultiAssignSubqRow`, not `Expr`, so
  `&x.Row` is a `**MultiAssignSubqRow` and cannot be returned as a `*Expr`. It
  needs its own arm.
- The inner-scope children (`InExpr.Plan`, `ExistsExpr.Plan`, `SubqueryExpr.Plan`,
  `MultiAssignSubqRow.Plan`) are `Node`, not `Expr`, so a scope-opening node
  reports **zero** `Expr` child slots. The classification, not `len(kids)`, is what
  distinguishes it from a true leaf.

A third hazard the driver must preserve: `remapByPosMap` deliberately **clones** on
rewrite (`cl := *x; cl.Index = newIdx`, `bushy.go:2160-2165`) while
`remapOuterRefsInSubplan` **mutates in place** (`x.Index = posMap(x.Index)`,
`bushy.go:2240`). A generic in-place driver that flattens that asymmetry will
double-remap any shared or twice-visited subplan.

The durable artefact is not the helper but the exhaustiveness gate,
`internal/planner/exprwalk_exhaustive_test.go`: parse `plan.go` with `go/ast`,
enumerate every `func (*X) exprNode()` receiver, and assert each appears in
`exprChildSlots`'s type switch. Adding a 33rd `Expr` type without teaching the
traversal about it then fails `go test`.

### §2.6 Migration order and risk

Convert **one walker per commit**, each with its own gate run. `remapByPosMap`
(§2.2) is converted first because it is the only one causing a known wrong answer.

That conversion *adds* remapping where there was none, so any query containing
`IS NULL` / `IS DISTINCT FROM` / a row constructor / an IN-list with column
elements **inside a predicate that reaches the MHJ rewrite** changes behaviour.
TPC-H's `IS NULL` uses are outer-join-derived rather than raw WHERE conjuncts
under an MHJ, so exposure is believed low — but this is the change most likely to
move a TPC-H number and is gated hardest (§7).

Regression pins go in `internal/planner/bushy_remap_test.go`: assert that the
operands of `IsNullExpr`, `IsDistinctFromExpr`, `RowExpr` and `InExpr.List` are
remapped.

---

## §3 RC-1b — MHJ filter push-down uses two different coordinate spaces (Q50, Q47)

### §3.1 Reproduction

**Q50 shape** — goopg 0 rows, PG 2196:

```sql
select count(*) from store_sales, store_returns, date_dim d2
 where ss_ticket_number = sr_ticket_number
   and ss_item_sk       = sr_item_sk
   and ss_customer_sk   = sr_customer_sk
   and sr_returned_date_sk = d2.d_date_sk
   and d2.d_year = 2001 and d2.d_moy = 8;
```
```
Multi-Way Hash Join (3 tables)
      Filter: ((d_year = 2001) AND (d_moy = 8))
  ->  Seq Scan on public.date_dim d2
        Filter: ((ss_item_sk = sr_item_sk) AND (ss_customer_sk = sr_customer_sk))
  ->  Seq Scan on public.store_returns
  ->  Seq Scan on public.store_sales
```

A `store_sales`↔`store_returns` equijoin has been attached as a **scan-level
filter on `date_dim d2`**, a relation that has neither column.

**Q47 shape** — goopg 0 rows, PG 661185 (the body of Q47's `v1` CTE):

```sql
select count(*) from item, store_sales, date_dim, store
 where ss_item_sk = i_item_sk and ss_sold_date_sk = d_date_sk
   and ss_store_sk = s_store_sk
   and (d_year = 2000 or (d_year = 1999 and d_moy = 12) or (d_year = 2001 and d_moy = 1));
```
```
Multi-Way Hash Join (3 tables)
  ->  Seq Scan on public.date_dim
  ->  Seq Scan on public.store
        Filter: (((d_year = 2000) OR ((d_year = 1999) AND (d_moy = 12))) OR ((d_year = 2001) AND (d_moy = 1)))
  ->  Seq Scan on public.store_sales
```

The `date_dim` OR-predicate is pushed onto the **`store`** scan. Collapsing the OR
to a plain `d_year = 2000` keeps the predicate above the join and returns the
correct 540754.

### §3.2 Root cause

`pushSingleSourceFiltersIntoMHJTables` (`internal/planner/mhj_input_rewrite.go:624`)
computes per-table offsets from the **OID-sorted** `mh.Tables` widths:

```go
offsets := make([]int, len(mh.Tables)+1)
for i, t := range mh.Tables {
        offsets[i+1] = offsets[i] + len(t.Output())
}
```

and then attributes each conjunct to the unique `Tables[i]` whose
`[offsets[i], offsets[i+1])` range covers all of its `ColumnRef.Index` values.

But `mh.Filters` still carries **pre-remap, FROM-cumulative** indices. The
remapping that would reconcile the two happens **later**, in `internal/planner/planner.go`:

```
1003  node = rewriteMultiWayChain(node, cat)                       // OID-sorts mh.Tables
1012  node = rewriteScanInputsWithSingleTablePredicates(node, cat) // → pushSingleSourceFiltersIntoMHJTables
1027  remapWithBindings(node, ctx.bindings)                        // ← reconciliation, too late
```

The comment at `bushy.go:1792` ("Build output schema from all tables (now in FROM
order)") is stale — the sort key immediately above it is `oid`, not FROM position.

**How a single-table predicate reaches `mh.Filters` at all** (the precondition the
first draft of this document left unstated). `pushOneConjunct`
(`internal/planner/pushdown.go:203`) pushes a conjunct onto a `Join` only when
`classifyConjunctSide` returns `sideMixed`, i.e. the conjunct appears to span both
sides. A genuinely single-table predicate reaches that verdict only by **width
misclassification**: its FROM-order indices straddle the join's `leftWidth`
boundary even though every reference belongs to one relation. The name-based guard
`allColumnRefNamesInScope` (`pushdown.go:187`) then passes, because the referenced
relation *is* somewhere in that join's subtree. The conjunct is ANDed onto the
join, `collectMultiHashTables` captures it as an "extra", and it lands in
`mh.Filters`.

That is why TPC-H is not visibly broken today: its predicates mostly do not
straddle, so they stay in the outer `Filter` and are absorbed later by the
**name-based** `absorbConjunctsIntoSubtree` (`mhj_input_rewrite.go:134`), which is
coordinate-space independent. It is luck of index ranges, not a structural
guarantee.

**Why the corruption is permanent.** `applyJoinTreePosMap`'s `*MultiHashJoin` arm
(`bushy.go:2461-2474`) remaps `n.Filters` — its own comment says those refs "are in
pre-rewrite (global FROM-order) coords; remap them to MHJ-output coords here" — and
then `return`s **without recursing into `n.Tables[i]`**. A conjunct already pushed
into a table's `Filter` at `planner.go:1012` is therefore never revisited by the
`:1027` remap. This also means §2's `remapByPosMap` fix does nothing for Q47/Q50,
which is why §9 orders them as separate phases.

### §3.3 Arithmetic confirmation

For the Q50 shape:

| relation | FROM-cumulative | OID-sorted MHJ offsets |
|---|---|---|
| `store_sales` (23 cols) | 0–22 | 48–70 |
| `store_returns` (20 cols) | 23–42 | 28–47 |
| `date_dim` (28 cols) | 43–70 | 0–27 |

`ss_item_sk` = 2 and `sr_item_sk` = 25 both fall inside `[0, 28)` = **date_dim**;
`ss_customer_sk` = 3 and `sr_customer_sk` = 26 likewise. Both conjuncts are
therefore "single-source on date_dim" and get pushed there. The third conjunct,
`sr_returned_date_sk`(23) `= d_date_sk`(43), straddles two ranges, is left in
`mh.Filters`, and is fixed correctly by `remapWithBindings` one step later.

That predicts the observed plan exactly — including which two conjuncts moved and
which one did not.

`rewriteMHJInputsWithSingleTablePredicates` (`mhj_input_rewrite.go:479`) and
`absorbConjunctsIntoSubtree` (`:134`) resolve their targets by **column name**, so
they are coordinate-space independent and unaffected. Only
`pushSingleSourceFiltersIntoMHJTables` uses index ranges.

### §3.4 Design

There is only one coordinate space that is correct here, and the pipeline already
produces it — just too late. **Move `pushSingleSourceFiltersIntoMHJTables` to run
after `remapWithBindings`** (`planner.go:1027`) instead of inside
`rewriteScanInputsWithSingleTablePredicates` (`:1012`). After the remap,
`mh.Filters` is in MHJ-output coordinates, which is exactly the space
`offsets[]` is computed in, so index attribution becomes correct by construction
rather than correct by luck.

`rewriteMHJInputsWithSingleTablePredicates` (the SeqScan→IndexScan promotion) stays
where it is: it resolves by column **name**, is already coordinate-space
independent, and moving it would change the scan identities `buildBindingsPosMap`
keys on.

Defence in depth, in the same commit: before pushing, cross-check the index
attribution against a **name** attribution over `mh.Tables[i].Output()`, and
decline the push when they disagree or when the name is ambiguous (a self-join such
as `date_dim d1, d2, d3` gives every column two or three candidate tables). A
declined conjunct simply stays in `mh.Filters` and evaluates one level higher —
slower, never wrong.

**Risk assessment, corrected from review.** An earlier draft claimed this was
"bit-identical on TPC-H by construction" because FROM order and creation order
coincide there. That is false: of the eight join-heavy TPC-H queries, **seven**
have a FROM order that differs from OID order (only Q3 coincides). TPC-H is
protected by the straddling precondition above, not by order agreement, so this
change must be gated on a full TPC-H run like any other planner change.

**Atomicity requirement.** `shiftColumnRefs` (`mhj_input_rewrite.go:667`) and
`cloneExprForShift` (`:719`) currently enumerate the *same* four node kinds. They
are wrong but mutually consistent: the clone copies exactly what the shift
mutates. Generalising the shift without generalising the clone in the same commit
would let the shift mutate a **shared** subtree still referenced by the enclosing
`Filter.Predicate`. Both move to `cloneExprRefs` from §2.5 together, or neither
moves.

### §3.5 Deferred: the root fix

The structural fix is to run `rewriteScanInputsWithSingleTablePredicates` **after**
`remapWithBindings` (`planner.go:1012` vs `:1027`), so there is only ever one
coordinate space in play. That reorder also moves `IndexScan` promotion after the
remap, which changes the scan identities `buildBindingsPosMap` keys on. It needs
its own design doc and its own benchmark round; it is **not** attempted here.
Recorded in `.ralph/deferral_ledger.md`.

---

## §4 RC-2 — Q8 panic: `buildBindingsPosMap` has no `SetOp` arm

### §4.1 Evidence

`bench/tpch/runtime_goopg/goopg.tpcds.log:3076`:

```
panic="runtime error: index out of range [57] with length 1"
  executor.(*MaterializedSlot).Get        slot.go:79
  executor.evalExprSlot                   expr.go:369
  executor.(*projectOp).Next              operators.go:341
  executor.drainRowsBounded               spill.go:353
  executor.(*joinOp).buildLazyHashTable   operators_join_agg.go:620
  executor.prebuildSharedHashJoins        parallel_hash_build.go:153
  executor.(*gatherOp).prebuildHashJoins  operators_gather.go:111
  executor.(*gatherOp).Open               operators_gather.go:152
```

### §4.2 Root cause

`buildBindingsPosMap`'s `collect` walk (`internal/planner/bushy.go:2320`) advances
its offset for `SeqScan, IndexScan, MultiHashJoin, Values, CTEScan,
MaterializedCTEScan, Project`, descends through `Join, NestedLoopIndexJoin, Filter,
Sort, Aggregate`, and handles one SRF group. It has **no arm for `SetOp`** — so
every scan to the right of a set-op receives an offset that is too low. Q8 puts an
`INTERSECT` inside a FROM subquery, which is exactly that shape.

The same walk also has no arm for `IndexOnlyScan`, `Distinct`, `DistinctOn`,
`Limit`, `WindowAgg`, `RecursiveUnion`, `WorkTableScan`, `Gather`, `GatherMerge`,
`Memoize`, `ProjectSet`, `OrdinalityWrap`, `RowsFrom` or `LockRows`. Each is a
silent-corruption trapdoor of the same class.

The `containsSetOp` guard added by commit `9ddbc679` protects `pushdown.go:241`,
`pushdown.go:264` and `planner.go:2078` — it never protected the remap path, which
is why Q8 kept crashing after that commit.

### §4.3 Why it drops the connection instead of raising an error

Two independent gaps:

1. `evalExprSlot` (`internal/executor/expr.go:353`) bounds-checks `rowSlotView` and
   `*VirtualSlot`, but not `*MaterializedSlot` (`slot.go:79` is a bare
   `s.row[col]`) or `*Slot`.
2. `prebuildSharedHashJoins` runs in the **leader** during `gatherOp.Open`, so it
   is not covered by the parallel-worker `recover` in
   `internal/executor/parallel_runtime.go:141`, which converts a worker panic into
   an `XX000` `ExecError`. The panic instead reaches
   `internal/server/server.go:782`, which logs `backend goroutine panic` and then
   **closes the socket** — no `ErrorResponse`, no `ReadyForQuery`.

PostgreSQL's contract is that an ERROR kills the statement, not the backend
(`postgres/src/backend/tcop/postgres.c`, `sigsetjmp` / `EmitErrorReport` /
`AbortCurrentTransaction`). goopg kills the backend.

### §4.4 Design

**Coverage.** Restructure `collect` into two explicit sets plus a loud default:

- *descend* (node schema is exactly the concatenation of its children):
  `Join, NestedLoopIndexJoin, Filter, Sort, Distinct, DistinctOn, Limit, LockRows,
  Memoize`
- *opaque leaf* (`off += len(n.Output())`, do not descend):
  `Project, Values, CTEScan, MaterializedCTEScan, SetOp, RecursiveUnion,
  WorkTableScan, ProjectSet, OrdinalityWrap, RowsFrom, IndexOnlyScan, WindowAgg`,
  the SRF group
- `default:` → **return nil**, declining the whole remap.

Two placements corrected from review:

- **`WindowAgg` is an opaque leaf, not a descend node.** It *appends* window-function
  columns to its child's output, so descending into it would leave every scan to
  its right short by the number of window columns — the identical defect `SetOp`
  has today.
- **`Aggregate` is deliberately absent from both sets**, so it now hits the
  `default:` decline. Its output is `GroupExprs ++ Aggs`, not its child's
  concatenation; the current code descends into it and is only accidentally correct
  because `collect` is always entered at the plan root or at `aggNode.Child`, never
  at an `Aggregate` sitting as a join input. Declining makes that assumption
  explicit instead of load-bearing-but-unwritten.

`Gather`/`GatherMerge` cannot appear here at all — `MaybeAddGather` runs at
`internal/server/dispatch.go:1201`, after the planner returns.

Declining is the safe direction: an unremapped tree is only wrong when a reorder
actually happened, whereas a mis-advanced offset is wrong unconditionally. **All
three call sites already nil-check the returned posMap** — `remapWithBindings`
(`bushy.go:2019-2022`), `remapTopProjection` (`:2062-2065`) and
`remapAggExprsWithBindings` (`:2115-2118`) — so declining is safe without any
call-site change.

**Diagnostic.** Behind `GOOPG_POSMAP_ASSERT=1`, compare the accumulated offset
against `len(node.Output())` after `collect` and log on mismatch. Not a hard gate —
the Semi/Anti right-side skip in `applyJoinTreePosMap` legitimately breaks the
identity in some shapes.

**Containment.** Add the missing `*MaterializedSlot` / `*Slot` bounds check in
`evalExprSlot` and convert a statement-level panic into an `XX000` `ErrorResponse`
plus `ReadyForQuery`, leaving the connection open. Guard: only convert when no
`DataRow` has been sent for the statement; otherwise fall back to today's
close-the-connection behaviour, since a half-streamed result cannot be retracted.

This containment is worth landing **before** the coverage fix: it turns every
present and future planner index bug from a fatal, undebuggable connection drop
into a normal SQL error.

---

## §5 RC-3 — IN-list cross-kind equality (Q83)

### §5.1 Reproduction

```sql
select count(*) from date_dim where d_date = '2001-07-13';          -- goopg 1  ✓
select count(*) from date_dim where d_date in (date '2001-07-13');  -- goopg 1  ✓
select count(*) from date_dim where d_date in ('2001-07-13');       -- goopg 0  ✗  (PG 1)
```

Q83 gates all three of its CTEs on
`d_date in ('2001-07-13','2001-09-10','2001-11-16')`, so it returns 0 rows where
PG returns 22.

### §5.2 Root cause

This is an **executor** gap, not a parser gap.

`compareEq` (`internal/executor/expr.go:7568`), the equality oracle reached from
`evalInExpr` (`:6438`), has arms for int/int, bool/bool, string/string, time/time,
int↔string, enum↔string, and a delegation to `compareDatum` for `KindNumeric`. It
has **no** `KindTime` ↔ `KindString` arm and falls through to
`return NewBoolDatum(false)`.

The plain `=` form works because the `BinaryOp` path goes through `compareDatum`
(`:2457`) → `promoteCrossKind` (`:2237`) → `tryParseStringAs(KindTime, …)`
(`:2261`), which parses the literal. `compareEq` bypasses all of it.

### §5.3 Design

After the explicit arms, when exactly one side is `KindString`/`KindStringArena`
and the other is not, delegate to `compareDatum(a, b, 0)` and treat an error as
not-equal — precisely the pattern the existing `KindNumeric` arm already uses at
`expr.go:7578-7587`.

`evalInHashProbe` (`internal/executor/subplan_hash.go`) already declines on
cross-family coercion, so it falls through to the linear loop this fix corrects;
verify that decline still fires for `KindTime` vs `KindString`.

### §5.4 Why not fix it in the parser

PostgreSQL resolves an IN list at parse time:
`postgres/src/backend/parser/parse_expr.c`, `transformAExprIn` →
`select_common_type` → `coerce_to_common_type`. goopg deliberately does not:
`docs/design/root-0019-unknown-literal-coercion.md` establishes that the analyzer
types a bare `StringConst` as `unknown` and coercion is resolved at runtime.
Introducing parse-time `select_common_type` would be a larger change that
contradicts the shipped design.

Two consequent divergences from PG, both recorded in the deferral ledger:

- PG raises `22P02` for `int_col IN ('abc')`; goopg will return `false`.
- PG unifies all IN-list elements to a **single** common type; goopg compares
  element-wise, so a heterogeneous list behaves differently.

---

## §5a Q49 (30 vs 34 rows) — no confirmed mechanism

Q49 is three `UNION ALL` branches (web / catalog / store), each ranking a derived
ratio and filtering `return_rank <= 10 or currency_rank <= 10`. goopg returns
exactly **30** where PG returns 34 — suspiciously exactly 3 × 10.

**Ruled out by probe:** `rank()` peer-tie handling. `rank`, `dense_rank` and
`row_number` over a tied ORDER BY are byte-identical to PG (`1,1,3,3,3,6` on both),
so the `<= 10` filter is not silently degrading to `row_number` semantics.

**Remaining candidates**, to be bisected after phase 1.2 and 2.1 land:

1. the `decimal(15,4)` division producing `return_ratio` / `currency_ratio` — a
   precision difference reorders ties and changes which rows sit at rank 10;
2. the mixed `store_sales sts LEFT OUTER JOIN store_returns sr … , date_dim` shape,
   which is exactly the outer-join-plus-comma-join form §2.3 flags as unverified
   for Q72.

No fix is designed here. This is an honest open item, not a deferral.

---

## §6 RC-4 — Q39 "connection lost"

No `backend goroutine panic` line exists for Q39 in either server log, so this is
a `SIGKILL`, not a Go panic — i.e. the process was killed, most plausibly by the
cgroup cap (`scripts/goopg-test-run.sh`, `MemoryMax=24G`). `scripts/tpcds-run.sh`
auto-restarts the server on connection loss, which is why Q40 succeeded afterwards.

Two candidate mechanisms, in order of likelihood:

1. **Wrong hash-join build side.** `collectMultiHashTables` (`bushy.go:1488`)
   picks `probeIdx` as `argmax EstimateRows(scan)`. `EstimateRows(*SeqScan)`
   resolves to `Stats.RowCount` (`cardinality.go:38`, `:89`), which is **0 for
   every table after a restart** (§7.1). All candidates therefore tie at 0 and
   `probeIdx` stays at the first DFS scan, arbitrarily. Q39 joins `inventory`
   (11.7 M rows at SF=1); if it lands on the build side, goopg hashes all of it —
   and the file contains two statements, each self-joining a CTE used twice, so
   the join runs up to four times.
2. **Unbounded aggregate.** `aggregateOp`
   (`internal/executor/operators_join_agg.go:1329`) materialises `rows []Row` with
   no `work_mem` consultation and no spill. `ctx.WorkMem` is read only by
   `subq_cache.go:23`, `operators_memoize.go:106` and the hash-join build side
   (`operators_join_agg.go:530`).

**Measure before building.** Run Q39 alone under the cgroup cap with RSS
monitoring, and check the cgroup `memory.events` `oom_kill` counter and
`dmesg -T | grep -i 'killed process'`. If mechanism (1) holds, §7.1 fixes Q39 for
free and no spill code is needed. Hash-aggregate spill is a subsystem change
requiring its own design doc, and is deferred until measured.

---

## §7 The timeout class

`EXPLAIN` cost and width are hardcoded literals in goopg
(`internal/executor/operators_explain.go:378`, `:925`), so costing cannot be
diagnosed from a plan. The three signals that are real are plan **shape**,
`EXPLAIN ANALYZE` **actual** rows, and the per-SubPlan
`Calls/Rebuilds/Rescans/CacheHits/CacheMisses` counters emitted by
`emitSubPlanSubtrees` (`operators_explain.go:419`).

### §7.1 The primary mechanism — relation sizes are zero after a restart

`loadStatisticsFromHeap` (`internal/initdb/open.go:3433`) restores per-column
statistics from `pg_statistic` but ends with

```go
stats := &catalog.TableStats{Columns: colStats}
cat.SetTableStats(tbl, stats)
```

`RowCount` and `Pages` are left at zero. Consequences, all simultaneous:

- `tableRows` (`cardinality.go:89`) returns 0 → `EstimateRows` is 0 for every scan
  → the MHJ probe-side choice is arbitrary (§6);
- the bushy DP's seed falls back to `rowCounts[i] = 1` for every relation
  (`bushy.go:675`) → join order is effectively unordered;
- `estimateBaseRelInfo.baseRows` is 0 → filtered cardinalities are meaningless;
- `pg_class.reltuples` renders 0.

Confirmed live: the running bench server reports `pg_stats` = 0 rows and
`reltuples` = 0 for every table. **The 2026-07-26 benchmark measured a planner
with no usable size information at all.**

**Design.** Give `tableRows` a fallback: when `Stats == nil || Stats.RowCount <= 0`,
derive rows from the live block count × a type-derived average tuple width — PG's
`estimate_rel_size` (`postgres/src/backend/optimizer/util/plancat.c`). The block
count is already plumbed as `planner.ParallelSettings.BlocksForTable`
(`parallel.go:74-77`, populated from `smgr.NBlocks` at
`internal/server/dispatch.go:1219`); thread the same accessor into the planner's
cardinality context rather than inventing a new one.

This is the route `.ralph/deferral_ledger.md` row `pq-P10` already recommends
(option (b)): it needs no new persistence and "also revives `EstimateRows`
generally after a restart, which is the wider win". Option (a) — persisting
`reltuples`/`relpages` — stays deferred: per `pq-P10`, one analyze-plus-restart
round-trips but a second does not, and the cause is not yet established.

**Risk.** This is the highest-regression-risk change in the document. Direct
precedent: `analysis/tpch-evolution-round4-parallel-query-20260722.md` records that
enabling ANALYZE fixed TPC-H Q5 but regressed Q22 128×, Q4 79×, Q8 53×, Q2 26×.
Mitigation is mandatory and threefold: ship behind `GOOPG_RELSIZE_FALLBACK=1`
defaulting **off** (mirroring `costDrivenJoinOrder` at `bushy.go:563`); run the
full TPC-H power test in both flag states with a per-query table; flip the default
only in a separate commit carrying its own `analysis/` report.

### §7.2 Secondary mechanism — the bushy DP declines outright

`tryBushyDP` bails when `len(tables) > 12` (`bushy.go:93`) or when **any** FROM
leaf is not a `SeqScan`/`IndexScan`/`MultiHashJoin` (`bushy.go:110-117`) — i.e.
any CTE or derived table. Q64 has roughly 20 FROM items, so it receives no join
ordering at all and falls back to SQL-text order.

**In scope:** add a greedy fallback for `n > 12` — repeatedly join the cheapest
connected pair using the existing `estimateJoinCost` (`bushy.go:1197`) — rather
than declining. Gated behind the same flag as §7.1, since it is meaningless
without real cardinalities. No TPC-H query exceeds 8 FROM items, so TPC-H is
unaffected by construction.

**This alone does not fix Q64** (corrected from review). `query64.sql` opens with
`with cs_ui as (…)` and its 18-item FROM references that CTE, so `tryBushyDP`
declines at **both** gates — `len(tables) > 12` *and* the non-scan-leaf check. With
only the first gate lifted, Q64 declines exactly as before. Q64 needs the deferred
second gate too, and is therefore **not** claimed as fixed by this phase.

**Deferred:** admitting derived-table/CTE leaves requires `buildBindingsPosMap` to
key those leaves, which is the exact hazard documented at `bushy.go:101-109` as
having previously caused an index-out-of-bounds panic. Not attempted here.

### §7.3 Mechanisms deferred, with reasons

| id | mechanism | why deferred | reopen criterion |
|---|---|---|---|
| RC-5 gate | `shouldAttachBeforeMHJ` (`local_filters.go:154`) requires ≥5 FROM tables **and** a `SmallDimension` table; `SmallDimension` is hardcoded to `region`/`nation` (`initdb/open.go:2890`, `executor/operators_ddl.go:3376`), so no TPC-DS relation qualifies and base relations are costed unfiltered | the gate exists specifically to prevent a measured TPC-H Q8/Q21 PASS→CANCEL regression, documented in the function's own comment; and `conjunctIsLocalEligible`/`localizeExprToLeaf` are two of the incomplete walkers in §2.4 — opening the gate would make those latent bugs live | after §2's walker conversions land **and** §7.1's flag is defaulted on; needs its own design doc |
| RC-7 | ROLLUP/CUBE/GROUPING SETS become a UNION ALL chain of N independently planned SELECTs sharing the same FROM/WHERE (`planner.go:3176`, `docs/design/0122-0004-grouping-sets-rollup-cube.md`) — Q5 3×, Q14 5×, Q67 9× full re-execution | replacing it needs a real `GroupingSetsAggregate` operator (PG's `AggStrategy` + `GroupingSetsData`, `nodeAgg.c`). Ceiling is only 3–9×, versus 100×+ for a wrong star-join probe side. Wrong first target | if Q5/Q14/Q67 still time out after §7.1 |
| RC-8 | EXISTS/IN under `OR` never decorrelates (`unnest.go:147 subqueryANDReachable`) — Q10/Q69 are `exists(…) and (exists(…) or exists(…))` | PG's `pull_up_sublinks` does not decorrelate under OR either; it uses a hashed SubPlan. The correctness argument in the existing comment is sound | **measure first** with the per-SubPlan counters. If `CacheMisses ≈ Calls`, the fix is hashed-SubPlan caching, not decorrelation — much smaller and safer |
| RC-9 | `terminatesPartial` (`parallel.go:311`) kills parallelism at `SetOp`, so every ROLLUP query is serial by construction | downstream of RC-7: fixing RC-7 deletes the `SetOp`. Doing RC-9 first means parallelising a plan shape we intend to remove | after RC-7 |
| cost model C4 | `costDrivenJoinOrder` default-on (`bushy.go:563`) | stalled on a correctness regression (`docs/design/cost-model/IMPLEMENTATION-TODO.md`: C4-pg-ii "produces incorrect + slow plans", Q8 returned 0 rows). Explicitly off this document's critical path | **re-test after §2.** Under `costDrivenJoinOrder`, `shouldAttachBeforeMHJ` relaxes to `len(bindings)>=2`, exercising the incomplete `localizeExprToLeaf` — some of C4's "incorrect plans" may be RC-1a in disguise |
| EXPLAIN costs | `cost=0.00..0.00 … width=0` literals | a real cost render requires C4 to be trustworthy | with C4 |
| plan cache | `internal/server/plancache.go` is process-wide, keyed on normalized SQL + dbOid, and `Invalidate()` (`:93`) fires only on DDL — **not on ANALYZE** | small, but touching it perturbs every benchmark's warm-up semantics mid-investigation | standalone commit after the §8 protocol stabilises |

---

## §8 Measurement protocol

The two statistics caveats interact badly: `Stats.RowCount` is lost on restart, and
the plan cache is never invalidated by `ANALYZE`. "Same-session ANALYZE" and
"restart to reload" are therefore **not two ways of measuring the same server**.
Every result must be labelled with one of three states:

| state | how to reach it | what the planner sees |
|---|---|---|
| **S-cold** | restart via `scripts/csq-bench-server.sh`, no ANALYZE this session | `Stats.RowCount = 0`; column stats load from `pg_statistic`; `EstimateRows` = 0 everywhere; DP seed `rowCounts[i] = 1`; MHJ probe = first DFS scan. **This is what the 2026-07-26 benchmark measured.** |
| **S-warm** | S-cold, then `ANALYZE` every table, then issue each query for the **first** time | `RowCount` and column stats both correct. See the caveat below — S-warm is only reachable for a query that has not been planned yet in this server process |
| **S-fallback** | S-cold + `GOOPG_RELSIZE_FALLBACK=1` (§7.1) | `RowCount` derived from live block count — the state §7.1 is designed to make equal to S-warm |

Per-query procedure:

1. **Confirm the state before every run.**
   ```bash
   psql -p 65433 -U postgres -d postgres -c \
     "select relname, reltuples::bigint, relpages from pg_class
       where relnamespace='public'::regnamespace order by 2 desc limit 8;"
   psql -p 65433 -U postgres -d postgres -c \
     "select count(*) from pg_stats where schemaname='public';"
   ```
   Non-zero `reltuples` ⇒ S-warm; zero ⇒ S-cold. Both zero **and** `pg_stats`
   empty ⇒ ANALYZE has never run on this data dir.
2. **Never compare a plan across states.** Each query gets a 2×2 of
   {S-cold, S-warm} × {before, after}. If §7.1 works, S-cold-after ≈ S-warm-before.
3. **Read shape and actuals, never cost** (§7).
4. **Bound iteration runs** with `TPCDS_TIMEOUT=120 scripts/tpcds-run.sh <n>`.
   Note that `tpcds-run.sh` auto-restarts the server on connection loss, silently
   returning the run to S-cold mid-batch — check
   `bench/tpch/runtime_goopg/goopg.tpcds.log` for restart boundaries before
   trusting a batch result.
5. **Separate memory from time.** A query that dies rather than times out gets a
   solo run with RSS monitoring and a cgroup `memory.events` `oom_kill` check.
   "Connection lost with no `backend goroutine panic` in the log" means SIGKILL,
   not a Go panic — the two need completely different fixes.
6. **S-warm is single-shot per server process.** `planCache.Invalidate()` fires
   only on a `*planner.DDL` node (`dispatch.go:2975`, `dispatch_extended.go:363`),
   and ANALYZE plans to a `*Utility` node (`planner.go:212-218`), so ANALYZE never
   flushes the cache — and there is no flush command. A query planned before the
   ANALYZE keeps its S-cold plan for the life of the process, while a restart
   returns the whole server to S-cold. So the {S-cold, S-warm} × {before, after}
   matrix in step 2 must be run as: restart → measure S-cold → ANALYZE → measure
   S-warm **on queries not yet issued in that process**. Issue each query exactly
   once per process, or the state label is a lie.
7. **TPC-H regression protocol**, mandatory for §2.6, §3.4 and all of §7: a full
   TPC-H power run from a fresh capped server, in both flag states, with a
   per-query table written to `analysis/`. `scripts/tpch-spotcheck.sh` (Q12/Q13
   row counts) is a **correctness** gate and will not catch a 128× Q22 regression.

---

## §9 Implementation order

| phase | change | fixes | risk |
|---|---|---|---|
| 0.1 | `formatExprPGReg` node coverage (`operators_explain.go:678`) | nothing — makes everything else diagnosable | EXPLAIN goldens; re-capture the plan-gate baseline in the same commit |
| 0.2 | slot bounds check + statement-level panic → `XX000` (§4.4) | Q8 stops taking the server down | low |
| 1.1 | `internal/planner/exprwalk.go` + exhaustiveness test (§2.5) | nothing yet — dead code | none |
| 1.2 | convert `remapByPosMap` (§2.2) | **Q76, Q72** | highest TPC-H exposure in the correctness set |
| 1.3 | `buildBindingsPosMap` coverage + decline-on-unknown (§4.4) | **Q8** | plan-gate + TPC-H |
| 2.1 | move `pushSingleSourceFiltersIntoMHJTables` after `remapWithBindings`, plus the name cross-check, plus `shiftColumnRefs`/`cloneExprForShift` generalised in the same commit (§3.4) | **Q50, Q47** | real TPC-H risk — 7 of 8 join-heavy TPC-H queries have FROM order ≠ OID order; full TPC-H run required |
| 2.2 | convert the remaining walkers, one per commit (§2.4) | latent | **plan-shape change, not just index arithmetic** — `visitColumnRefsByName` feeds `extraInScans` (`bushy.go:1625-1649`), whose vacuous `true` for a ref-less conjunct decides *which conjuncts enter `mh.Filters` at all*. Full TPC-H per commit |
| 3 | `compareEq` cross-kind delegation (§5.3) — **LANDED** `b3493a6e` | **Q83** (0 → 22 rows, exact match) | low |
| 4 | measure Q39 (§6); measure Q49 and Q35 | diagnosis only, no fix designed yet | none |
| 5 | harness fixes (§1.3) — **LANDED** `b3493a6e` | report fidelity; Q45/Q46 confirmed correct | none |
| 6.1 | `tableRows` block-count fallback behind `GOOPG_RELSIZE_FALLBACK` (§7.1) | nothing until the default is flipped in a later commit | highest overall; flag-gated |
| 6.2 | greedy join-order fallback for `n > 12` (§7.2) | **not Q64** — Q64 also needs the deferred non-scan-leaf gate | flag-gated |

Every phase runs `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
before commit; the pre-commit hook adds the pgbench smoke. Planner and executor
phases additionally run `scripts/tpch-spotcheck.sh` and `make plan-gate`.

---

## §10 Deferral ledger rows to append

- parse-time IN-list `select_common_type` (§5.4), with the two divergences named
- reordering `rewriteScanInputsWithSingleTablePredicates` after `remapWithBindings` (§3.5)
- `shouldAttachBeforeMHJ` `SmallDimension` gate (§7.3)
- shared-scan GROUPING SETS operator (§7.3)
- EXISTS-under-OR decorrelation / hashed-SubPlan caching (§7.3)
- parallelising `SetOp` (§7.3)
- `aggregateOp` `work_mem` accounting and spill, if §6 shows it is real
- `plancache` invalidation on ANALYZE (§7.3)
- persisting `reltuples`/`relpages` — the alternative to §7.1 (ledger `pq-P6`, `pq-P10`)

---

## §11 Provenance

- goopg HEAD at analysis time: `ee86594e`, branch `tpcds-fix2`
- benchmark data: `/tmp/tpcds-bench-v4.txt` (2026-07-26), curated in
  `analysis/tpcds-sf1-goopg-20260726.md`
- crash evidence: `bench/tpch/runtime_goopg/goopg.tpcds.log:3076`, timestamped
  **2026-07-24T17:18:18** — it predates the 2026-07-26 sweep. The stack matches
  §4.1 line for line and Q8 still errors at 2 s in the 07-26 run, so the diagnosis
  stands, but this is not a 07-26 artefact.
- the SQL reproductions in §2.1, §3.1, §5.1 and §5a were executed against the live
  servers described in §1.4 on 2026-07-27. **§2.3's Q72 attribution is inference,
  not a reproduction** — Q72 reaches `promotion` through a `LEFT OUTER JOIN`
  (`query72.sql:16`), and it is not established that an outer-joined leaf reaches
  `rewriteMultiWayChain` at all. Q72 must be re-measured after phase 1.2 rather
  than assumed fixed.

---

## §12 Review record (2026-07-27)

This document was put through an adversarial verification pass that re-opened every
cited file:line. Four blocking errors and a number of inaccuracies were found. They
are corrected inline above and recorded here so the corrections are not silently
absorbed.

| id | finding | resolution |
|---|---|---|
| B1 | "FROM order and OID order coincide on TPC-H" is **false** — 7 of the 8 join-heavy TPC-H queries differ (only Q3 coincides). The "no TPC-H risk by construction" argument for the MHJ fix was therefore unsupported. | §3.4 rewritten. The real protection is the straddling precondition, and a full TPC-H run is now mandatory for that phase. |
| B2 | The stated causal model predicted TPC-H Q10 would be silently wrong today, which it is not — so the model of *when the pass fires* was incomplete. | §3.2 now states the precondition: a single-table conjunct reaches `mh.Filters` only via a `sideMixed` **width misclassification** in `pushOneConjunct` that then passes the name guard. Ordinary single-table filters go to the name-based `absorbConjunctsIntoSubtree` instead. Verified against `pushdown.go:203-232` and `mhj_input_rewrite.go:134`. |
| B3 | Lifting only the `n > 12` DP gate cannot fix Q64 — `query64.sql`'s FROM references the `cs_ui` CTE, so the non-scan-leaf gate declines too. | §7.2 and the §9 table now state Q64 is **not** fixed by that phase. |
| B4 | §0 claimed a root cause "for every remaining failure"; in fact 6 of the 24 in-scope queries get a designed landing fix. | §0 now states coverage plainly. |
| D1 | `kindOpaqueScope` collapses four distinct existing behaviours (signal / veto / ignore / descend), and two proposed child slots are not `Expr`-typed. | §2.5 replaced with a per-caller scope **policy**, plus the `MultiAssignSubqElem.Row` and `Plan Node` typing constraints and the clone-vs-mutate asymmetry hazard. |
| D2 | `WindowAgg` was wrongly placed in the descend set; `Aggregate` is only accidentally correct there. `return nil` confirmed safe — all three call sites already nil-check. | §4.4 corrected. |
| D3 | The permanence mechanism was missing: `applyJoinTreePosMap`'s MHJ arm remaps `Filters` and returns without recursing into `Tables[i]`. | Added to §0 and §3.2. It is why phase 1.2 cannot fix Q47/Q50 and the phases must stay separate. |
| D4 | Phase 2.2's risk was labelled "latent"; converting `visitColumnRefsByName` actually changes MHJ *composition* through `extraInScans`. | §9 risk column corrected. |
| G3 | Q49 had no root cause and no hypothesis at all. | New §5a: `rank()` ties ruled out by probe; two remaining candidates named; stated as an open item rather than a deferral. |
| — | Six stale line numbers (`operators_explain.go:849`→`:925`, `planner.go:1024`→`:1027`, `bushy.go:679`→`:675`, `bushy.go:1791`→`:1792`, `bushy.go:104-113`→`:110-117`, `server.go:780`→`:782`); miscounts ("three skipped slots"→four, "eleven walkers"→fourteen, "the worst handles 11"→4); and the `pq-P10` characterisation ("unreproduced"→reproduced but unexplained). | All corrected inline. |
| — | §8 noted that ANALYZE never invalidates the plan cache but gave no way out. | New step 6: S-warm is single-shot per server process — issue each query exactly once per process or the state label is wrong. |

Confirmed correct by the review and left unchanged: the `remapByPosMap` 11-kind
inventory and absent `default:`; the count of 32 `Expr` types; every listed missing
container's child slots; the OID sort in `rewriteMultiWayChain`; the §2.3 and §3.3
offset arithmetic including the TPC-DS column counts (23/22/28/20) and the
prediction of which Q50 conjuncts move and which straddles; the whole
`buildBindingsPosMap` arm inventory and its absent `SetOp` arm; the §4.1 stack
trace line for line; the unchecked `slot.go:79` and `opnode.go:99` gets;
`prebuildSharedHashJoins` running outside the parallel-worker recover; every §5
line cite and the `root-0019` runtime-coercion policy; `probeIdx` staying 0 under
all-zero estimates; `loadStatisticsFromHeap` leaving `RowCount`/`Pages`/`AvgWidth`
zero; and the existence and wording of ledger rows `pq-P6` and `pq-P10`.
