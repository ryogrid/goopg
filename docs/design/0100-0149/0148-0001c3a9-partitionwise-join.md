# M0148\-0001c3a9 — partitionwise join

Status: slice 1 landed 2026\-11\-15 \(partition member order\). The join
itself is open. Parent: M0148\-0001c3a5.

## Goal

- PG's `try_partitionwise_join` \(joinrels.c\) joins two partitioned rels
  partition by partition when:
  - `enable_partitionwise_join` is on;
  - both sides have the same partition scheme;
  - the join has an equi\-join clause on every partition key
    \(`have_partkey_equi_join`\);
  - the bounds match \(`partition_bounds_equal`, or `partition_bounds_merge`
    since PG 13\).
- `generate_partitionwise_join_paths` \(allpaths.c\) then adds an Append \(or
  Merge Append\) over the child joins to the parent join rel, and that path
  competes in `add_path` with the whole\-rel joins.
- goopg joins the two appendrels as wholes: `Hash Join` over two `Append`s.
  The partition\_join regress suite is mostly this one gap \(5630\-line diff
  after slice 1\).

## Slice 1 — PartitionDesc member order \(landed\)

- PG expands a partitioned table's partitions in PartitionDesc order, which
  is bound order:
  - `RelationBuildPartitionDesc` \(partdesc.c\) takes each partition's
    canonical index from `partition_bounds_create` \(partbounds.c\);
  - `expand_partitioned_rtentry` \(inherit.c\) walks that order and expands
    each sub\-partitioned child in place \(depth\-first\).
- Member order decides the Append's subplan order, the members' alias
  numbers \(`t1_1`, `t1_2`, …\), and — for partitionwise join — which
  partitions pair up. goopg used creation order, breadth\-first.
- `partitionDescOrder` \(internal/optimizer/partitiondesc.go\) reproduces
  the canonical order:

  | strategy | order |
  |---|---|
  | RANGE | lower bound, MINVALUE first |
  | LIST | smallest non\-NULL value; a NULL\-only partition after those |
  | HASH | \(modulus, remainder\) |
  | any | DEFAULT last |

  - Bounds are stored as raw text. They are compared by the key column's
    type: numeric for integer / numeric / float, byte order for text
    \(the C collation\), ISO date / timestamp text, and bool.
  - Anything else leaves that level in catalog order rather than guess.
  - A sub\-partitioned child also lists its own partitions' bounds, so the
    entry whose `ChildName` names the child is its own bound
    \(`ownPartitionBound`\).
- `collectAllPartitionLeaves` \(planner.go\) walks depth\-first in that order.

### Verification

- A probe against scratch PG 18.3 \(range with MINVALUE and DEFAULT; list
  with NULL, DEFAULT and multi\-value partitions; hash with mixed moduli;
  range over list sub\-partitions\) gives identical plans.
- `TestPartitionDescOrder` \(optimizer\) and
  `TestPartitionMembersInBoundOrder` \(executor\) pin the order. The
  executor test fails both cases with the ordering disabled.
- Regress, against a same\-suite HEAD baseline: partition\_prune 6754→6384,
  partition\_join 5801→5630, inherit 3122→3076. partition\_aggregate
  1473→1479 is hunk realignment: `pagg_tab_ml_p3_s1` now precedes `_s2` as
  in PG, but the alias numbers still differ \(see below\).

### Not covered by slice 1

- Alias numbering: PG gives an intermediate partitioned child an RT index,
  so leaves after it skip a number \(`pagg_tab_ml_p3_s1 pagg_tab_ml_5`\).
  goopg numbers leaves only.
- Bound types other than those listed keep catalog order.
- The executor's DML / tuple\-routing paths enumerate partitions on their
  own and are untouched.

## Remaining slices \(design\)

1. **Partition scheme on the appendrel.** Keep the partitioned parent, its
   key, and the ordered leaf list on the appendrel's binding, so the join
   search can see that a rel is partitioned and how.
   - Today the search sees a pre\-built UNION ALL chain.
   - `paramappend.go` \(`paramAppendPathFor`\) already walks the members of
     such a chain per rel; it is the model for per\-member access.
2. **Child rels.** Give each member a base RelOptInfo built like a search
   leaf, with the parent's restrictions translated to the member
   \(`adjust_appendrel_attrs`\). M0148\-0001c3a10 needs the same rels for
   member index clauses, so build them once for both.
3. **Matching.** At a two\-rel join of two partitioned appendrels:
   - require an equi\-join clause on each partition key;
   - require equal bounds, partition by partition \(`partition_bounds_equal`
     — same strategy, same count, same bound values; merging unequal bounds
     comes later);
   - require the join type to be one PG allows.
4. **Child joins.** For each matched pair, run the ordinary two\-rel join
   path generation over the child rels \(hash, merge and nested loop, each
   costed\). Then build an Append path over the cheapest child joins, and a
   Merge Append when the query wants an ordering.
5. **Consumption.** Honour `enable_partitionwise_join` from
   PlannerSettings. goopg's default is owner\-gated on, so the default plans
   change wherever the path wins — run the census and the fire\-set gate.
   - PG's default is off. goopg's on\-by\-default is an owner decision
     \(GUC defaults\); TPC\-H / TPC\-DS have no partitioned tables, so the
     corpora do not move.
6. **Deeper joins.** A child join joined again with a third partitioned
   rel \(N\-way partitionwise join\), and outer joins with
   default / missing partitions \(`partition_bounds_merge`\).
