# M0125-0034a — a comma-FROM list with a WITH reference gets ordered for connectivity, not cardinality

Status: landed 2026-07-31 (branch `tpcds-fix2`); Q65 arm (§7) landed the same day, loop #17
Task: `M0125-0034`, class C1's WITH-reference / derived-aggregate arm
Evidence: `analysis/m0125-0034b/`
Related: `docs/design/0125-0034-setop-join-promotion.md` (C1's set-operation arm),
`docs/design/0125-0035a-preserved-side-restriction-descent.md` §6 (which refuted
the qual-placement explanation and pointed here),
`docs/design/0125-0026-timeout-class-plan-comparison.md` (the capture that filed C1)

## 1. The defect

M0125-0026 measured `Nested Loop (CROSS)` 14 times across 8 of the 18 captured
TPC-DS timeout plans, always with the equi-join predicate demoted to a `Filter`
on the CROSS node or above it, and always with at least one join input that was
not a base relation. The set-operation members were closed by
`0125-0034-setop-join-promotion.md`. This document covers what was left:
**Q30, Q64, Q81** (and, not closed, Q65).

Q30's outer query is the shape in miniature:

```sql
from customer_total_return ctr1     -- a WITH reference
    ,customer_address
    ,customer
where ctr1.ctr_customer_sk = c_customer_sk
  and ca_address_sk        = c_current_addr_sk
  and ca_state = 'AR'
```

No predicate connects `ctr1` to `customer_address`. Both equi-predicates reach
`customer`, which sits last. `planFromClause` builds the chain in source order,
so the first join is `ctr1 × customer_address` — about 2×10⁴ CTE rows against
5×10⁴ address rows, 10⁹ pairs, each then probing `customer`. Q30 did not
complete in 300 s, and M0125-0041 measured that it did not complete in 1200 s
either, which is what established this as a shape defect rather than a budget
crossing.

## 2. Why nothing reordered it

goopg has two join-order passes. **Both decline on these lists, for unrelated
reasons, and neither decline is a policy decision** — that is the finding.

**`tryBushyDP` (`internal/planner/bushy.go`)** whitelists its leaves:

```go
switch scan.(type) {
case *SeqScan, *IndexScan, *MultiHashJoin:
default:
    return node, pred
}
```

The comment above it is honest about the cause: `buildBindingsPosMap` remaps
column indices via `(table-pointer, alias)` scan keys, which only exist for
those kinds, so a CTE or subquery leaf would leave ColumnRefs at their pre-DP
coordinates and panic. Separately, `len(tables) > 12` declines outright — Q64's
`cross_sales` has an 18-relation FROM list, so it fails both tests.

**`reorderCommaFromByCardinality` (`internal/planner/joinorder.go`)** declined
because it could not *size* the list:

```go
if !ok || tbl == nil { return s.FromExprs, s.From, false }   // not a base table
if tbl.Stats == nil || tbl.Stats.RowCount <= 0 { ... false } // no ANALYZE
```

A WITH reference is not in the catalog, so the first line fired and the source
order survived.

This is the same mistake in a different costume as M0125-0041's missing
`*CTEScan` arm in `clonePlanReplacingOuter`: **a capability gap that reads as a
cost decision.** The pass's stated precondition was "every FROM item is a base
table with statistics", but the thing it actually needs varies by what it is
trying to decide.

## 3. The fix: separate the two objectives

A missing row count blocks *ranking* connected orders against each other. It
does not block *telling a connected order from a disconnected one*. Those are
different questions, and only the second one is what a Cartesian product is
about.

So the pass now picks its objective from what it knows:

| the FROM list | mode | objective |
|---|---|---|
| all base tables, all ANALYZEd | cardinality (unchanged) | join small relations first |
| all base tables, some un-ANALYZEd | decline (unchanged) | the whole signal is missing |
| contains a WITH reference | **connectivity (new)** | never emit an avoidable cross |

`orderByConnectivity` seeds with the first FROM item and repeatedly takes the
lowest-numbered unplaced item that has an equality edge to something already
placed. It makes no cost claim: with an unsizable relation in the list, ranking
connected orders would mean inventing a number rather than reading one. Ties
therefore break on source order.

Which connected order is *fastest* remains open and belongs to `M0125-0038`
(no cost/cardinality propagation above base scans).

### 3.1 The inertness property

Breaking ties on source order buys a guarantee worth more than the tidy diffs:

> **A source order that is already cross-free is a fixed point of the walk.**

At step *i*, every unplaced item is ≥ *i*; if the source order is cross-free
then item *i* is connected to the prefix, so item *i* is the lowest-numbered
connected unplaced item and is chosen. The walk returns the identity
permutation, and the caller's existing `isIdentityPermutation` check declines
the rewrite.

Equivalently: **the pass rewrites a FROM list if and only if the source order
contains a cross the join graph could have avoided.** Every other query in the
corpus plans exactly as before — which is what bounds the blast radius of a
change to a pass that runs on every comma-FROM SELECT. `TestConnectivityOrder-
InertWhenAlreadyConnected` and `…UnavoidableCrossIsAFixedPoint` pin it.

### 3.2 Why the parser level is the safe seam

This pass runs *before* column resolution and permutes the parser-level FROM
list, so no resolved `ColumnRef.Index` needs remapping — the planner sees the
new order as if the user had written it. That is precisely the machinery whose
absence forced `tryBushyDP`'s leaf whitelist, so fixing the defect here costs
none of that risk. Permuting a pure comma FROM list is semantically neutral:
it is a cross product plus a WHERE, and the SELECT list is resolved by name.

### 3.3 The LATERAL bound — and why Q65 is not closed

The one thing that makes a FROM permutation unsafe is a FROM item that
*references an earlier one*. goopg's parser accepts `LATERAL` and throws it
away (`internal/parser/select.go`: "goopg treats it as a regular derived
table"), so nothing in the AST distinguishes a correlated derived table from an
independent one.

The pass therefore declines the whole list when it sees `rv.Subquery != nil` or
`rv.TableFunc != nil`. A WITH reference is admitted because it cannot correlate
to a sibling FROM item.

**This is why Q65 keeps its crosses**: its two inputs are derived aggregates,
not WITH references. Recording laterality in the AST is the resume point;
deferral ledger, 2026-07-31. **Discharged the same day — §7.**

### 3.4 One implementation trap, recorded because it is easy to repeat

The first cut set `tables[i]` only for relations that had statistics, because
that slice had always been "the sized relations". But `tables` feeds
`buildBareColumnIndex`, which needs the **column list** and never reads
`Stats`. On an un-ANALYZEd cluster this silently emptied the bare-column map,
so `ca_address_sk = c_current_addr_sk` resolved to no edge at all and the walk
saw a disconnected graph. Stats-ness and column-ness are different properties
of the same lookup and must not share a variable.

This matters beyond the bug: the SF0.5 gate runs **S-cold**, and goopg drops
`TableStats.RowCount` on restart, so on that cluster *cardinality mode never
fires at all*. Every reorder measured below is connectivity mode.

## 4. Measurement

Full 99-query SF0.5 gate, one binary, three contiguous chunks
(`analysis/m0125-0034b/gate/`):

**`PASS=92 (55 ck-verified) MISMATCH=1 CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4`**

Diffed cell by cell against the loop #14 baseline
(`analysis/m0125-0043-sf05-20260731/sweep-20260731-121447.txt`, `PASS=89 …
TIMEOUT=6`): **exactly 4 of 99 cells changed**, the other 95 identical in
status, rows *and* value checksum.

| query | before | after | attribution |
|---|---|---|---|
| Q30 | TIMEOUT (also at 1200 s) | **PASS 1 s, 31 rows, ck=f47a48499fd7e070** | this change |
| Q81 | TIMEOUT | **PASS 1 s, 100 rows** | this change |
| Q64 | TIMEOUT (**1848 s**, measured) | MISMATCH 33 s, goopg=0 oracle=2 | this change makes it complete; the wrong answer is pre-existing — §5 |
| Q72 | TIMEOUT | PASS 309 s | **not this change** — Q72 has no `WITH`, so connectivity mode cannot fire on it; it is the cap-straddler the banner already documents (307→308 s) |

The timeout class goes **6 → 2** (`Q65`, `Q78`). Q78 has a WITH but is
unaffected, which is consistent: `-0035`'s remainder is a CTE-*body* problem
(single-reference inlining + EC constant propagation), not a join-order one.

Other gates: `go build ./...`, `go vet ./internal/planner/`,
`go test ./internal/planner/... ./internal/executor/`,
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` all PASS;
`scripts/tpch-spotcheck.sh` `RESULT=PASS` (Q12=2, Q13=35, query-phase 32.2 s).

**TPC-H is inert by construction, not by sampling**: connectivity mode requires
a FROM item the catalog does not know, and the TPC-H query set
(`internal/testutil/tpch`) contains no `WITH … AS (` at all, so the mode is
unreachable on all 22. Cardinality mode's code path is unchanged.

## 5. Q64's MISMATCH is a defect this change exposed, not one it caused

Q64 went from no answer to a wrong answer, so this had to be settled rather
than assumed.

`analysis/m0125-0034b/q64body.sql` runs Q64's `cross_sales` CTE alone, grouped
by `syear`. goopg and PG return the **same 26 rows** — the 18-way join is
right — but goopg spreads them over 9 `syear` values (1994–2002) where PG has
5 (1998–2002). `syear` is `d1.d_year`, and `date_dim` appears **three times**
in that FROM list (`d1` on `ss_sold_date_sk`, `d2` on `c_first_sales_date_sk`,
`d3` on `c_first_shipto_date_sk`). goopg reports first-sales years as sold
years.

`alias_a.sql` / `alias_b.sql` isolate it in six relations and settle
authorship. They differ only in where `customer` sits: in **A** the source
order puts `d2`/`d3` before it, so connectivity mode fires; in **B** the list
is written pre-connected, so the walk is a fixed point and the pass declines.
goopg's output is **byte-identical** in both arms, and wrong the same way —
`y1 = y2 = y3` on every row where PG gives `1998 | 1993 | 1993`.

So multiple aliases of one table collapse to a single alias in projection
resolution. It reproduces with the pass declining, in a query with no
Cartesian product, independent of FROM order. Note the query also emits five
separate `1993|1993|1993` groups under `GROUP BY 1,2,3`: the grouping keys are
distinct while the projected columns are not — the same "right grouping, wrong
projection" signature M0125-0013 found in Q47's CTE body.

Filed as its own item. It is a **silent wrong answer**, which this milestone's
banner ranks above a timeout, and it is now reachable by a 20-second query
instead of being hidden behind a 1848-second one.

## 6. What this does not do

- ~~Q65 keeps its crosses (§3.3, LATERAL bound).~~ Closed by §7.
- `tryBushyDP`'s leaf whitelist and its `> 12` relation limit are untouched.
  Nothing above chose *which* connected order is cheapest; for an 18-relation
  list PG would run GEQO here. `M0125-0038`.
- Connectivity mode does not consult cost, so it cannot trade an avoidable
  cross against anything. On the evidence it never had to: 95 of 99 cells did
  not move.

## 7. The Q65 arm (loop #17): laterality recorded, non-lateral derived tables admitted

§3.3's bound was never about derived tables — it was about *not knowing*. A
non-lateral derived table provably cannot reference a sibling FROM item (PG
rejects the unmarked form with "invalid reference to FROM-clause entry",
`parse_relation.c::errorMissingRTE`), so permuting one is exactly as safe as
permuting a WITH reference. What made the pass decline was that goopg's parser
consumed `LATERAL` and threw it away, leaving nothing in the AST to tell the
two cases apart.

Two changes, one per layer:

- **Parser** (`parser.RangeVar.Lateral`, `select.go`): both `LATERAL` accept
  sites — `parseRangeVar` for comma-FROM items and the `JOIN LATERAL` path,
  which consumes the keyword *before* `parseRangeVar` can see it — now record
  the keyword on the RangeVar. Evaluation is unchanged: goopg still runs a
  lateral subquery as an ordinary derived table (deferral ledger — true
  LATERAL evaluation remains unimplemented).
- **Planner** (`joinorder.go`): the blanket `rv.Subquery != nil` decline is
  split three ways. A table function still declines the whole list — in PG the
  `LATERAL` keyword is *noise* before a function item ("LATERAL can also
  precede a function-call FROM item, but in this case it is a noise word",
  `official_docs_in_md` SELECT), so absence of the keyword proves nothing. A
  `Lateral` derived table declines the whole list, as before in effect. A
  non-lateral derived table is admitted and, having no catalog entry and no
  possible row count, forces connectivity mode — the same standing as a WITH
  reference. Its join edges are found through its alias (`relKeys`); it
  contributes no bare-column entries.

Q65's outer FROM is `store, item, (derived agg) sb, (derived agg) sc` with
every predicate reaching `sc`: the source order crosses `store × item` first.
The walk yields `store, sc, item, sb` — cross-free.
`TestConnectivityOrderQ65Shape` pins the shape;
`…AdmitsNonLateralDerivedTable`, `…DeclinesLateralDerivedTable` (also the
end-to-end pin that the parser records the keyword) and `…DeclinesTableFunc`
pin the boundary.

Measured (full 99-query SF0.5 gate, one binary, three chunks,
`analysis/m0125-0034c/gate/`): **PASS=93 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=2 SKIP=4**. Cell-by-cell against loop #16's capture: **exactly 2 of 99
cells moved** — `Q65 TIMEOUT → PASS 17 s, 100 rows = the oracle` (this change;
14.8 s on the warm probe), and `Q72 PASS 309 s → TIMEOUT 314 s`, which is
**not this change by construction**: Q72 is entirely explicit `JOIN … ON`, so
the pass declines at the `fe.Joins` guard before any of this loop's code runs;
it is the documented cap-straddler oscillating around the 300 s cap
(307/309/314 s across three loops). The timeout class is now **Q78** (-0035's
CTE-body arm) plus the Q72 straddle. TPC-H: `tpch-spotcheck.sh` PASS
(Q12=2 Q13=35), plan-diff vs `m0125-0044-after` **22/22 MATCH** — the TPC-H
query set has no derived table in any ≥3-item comma FROM list, so inertness
held empirically as well as by construction.
