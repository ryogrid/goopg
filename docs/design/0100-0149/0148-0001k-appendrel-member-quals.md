# M0148\-0001k — appendrel members: per\-member restrictions and `<parent>_N` aliases

**Parent:** M0148\-0001 `aggregates.sql` regress coverage closure · **Status:**
landed 2026\-11\-03; closed (residue in M0148\-0001k1)

## Divergence

goopg expands an inherited or partitioned table into a UNION ALL chain of
member scans (`planScanRangeVar`). Three things differed from PG 18.3:

| | PG | goopg (before) |
|---|---|---|
| member label | `Seq Scan on minmaxtest1 minmaxtest_2` | `Seq Scan on minmaxtest1` |
| restriction | `Filter: (f1 > 1)` under every member | `Append  Filter: (minmaxtest.f1 > 1)` |
| key above the Append | `Group Key: t1.a` | `Group Key: t1_1.a` (first member) once members were aliased |

## PostgreSQL

- `expand_single_inheritance_child` (inherit.c) copies the parent's eref
  alias onto every child RTE, so `set_rtable_names` prints the members
  `<parent>_N` once the parent RTE has claimed the bare name. For an
  inheritance parent that includes the parent's own scan.
- `set_append_rel_size` (allpaths.c) gives every child rel its own copy of
  the parent's `baserestrictinfo`, with the parent's columns translated
  (`adjust_appendrel_attrs`). The Append itself carries no qual.
- A Var of the parent appears above the Append and deparses with the
  parent's refname.

## Change

- **Member aliases** (`planScanRangeVar`):
  - partition leaves carry the parent's refname as their alias when there
    is more than one leaf;
  - inheritance members all carry it, the parent's own scan included;
  - the inheritance Append takes the parent RTID and AppendRelName, and the
    parent's scan gets a fresh RTID, matching the partition shape from
    M0148\\-0001d.
- **Per\-member restriction** (`appendrel_pushdown.go`,
  `pushAppendRelRestrictions`, run from `Plan`'s tail). A Filter directly
  over an inheritance or partition appendrel has each conjunct translated
  through each member's tableoid and remap Projects down to the member's
  SeqScan, where it becomes a leaf\\-local Filter.
  - The conjunct is moved, not copied, so it is still evaluated once per
    row. The emptied Append Filter is left `TRUE`, which EXPLAIN and the
    executor treat as absent.
  - Declined, and left on the Append: conjuncts with an outer reference or
    a sublink; references whose name does not match the appendrel's output
    column; members of an unexpected shape.
- **Locking and modifying plans keep the Append Filter.** In plans with
  FOR UPDATE/SHARE, UPDATE, DELETE or MERGE, the EvalPlanQual recheck
  (`lockRowsOp.findFilterPred` and the DML twins) re\\-tests the latest row
  version against the qual it finds above the scan chain. Moving the qual
  made isolation `eval-plan-qual` return a row the concurrent update had
  removed, so those plans are skipped (ledger).
- **Keys name the parent.** `resolveKeySourceDepth` no longer chases a key
  into an inheritance or partition appendrel's first branch: the Append is
  the parent RTE. `resolvedColumn` deparses such a Var with the parent's
  name too. That walk is right only for a FROM\\-clause UNION ALL
  (M0146\\-0005cb).

## Verification

- `TestExplainAppendRelMembers` checks the inheritance and partition
  labels, per\\-member filters, the parent\\-named Group Key, and the
  filtered values. Mutation\\-checked: without the pass two cases fail, and
  without the key decline one fails.
- `TestInheritanceParentRelSize` now expects the aliased labels.
- Regress runner, HEAD → change (diff lines / hunks):

  | suite | before | after |
  |---|---|---|
  | aggregates | 208 / 9 | 193 / 8 |
  | partition\\_join | 5974 | 5801 |
  | partition\\_aggregate | 1540 | 1473 |
  | inherit | 3186 | 3170 |
  | tablesample | 214 / 5 | 192 / 4 |
  | select\\_parallel | 1528 | 1522 |
  | alter\\_table | 3662 | 3652 |
  | updatable\\_views | 4020 | 4018 |
  | partition\\_prune | 6382 | 6785 |

  partition\\_prune grows because goopg does not prune partitions at plan
  time: every surviving member now prints its own Filter line. rowsecurity
  and union are unchanged; join differs only by hash\\-order noise.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass, eval\\-plan\\-qual included. The TPC gates show no change.

## Remaining (M0148\\-0001k1)

- Push into members in locking and modifying plans once the EvalPlanQual
  recheck reads member quals.
- Plan\\-time partition pruning (partprune.c), which shrinks partition\\_prune
  and partition\\_join's member lists.
- Member order: goopg expands a partitioned table's DEFAULT partition
  first, where PG orders by bound; and inheritance children came back in a
  different order after a restart.
- A single\\-leaf partition, a quals\\-only Append and other plan shapes are
  untouched.
