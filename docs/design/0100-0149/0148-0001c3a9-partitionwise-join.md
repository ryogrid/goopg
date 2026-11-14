# M0148\-0001c3a9 — partitionwise join

Status: slices 1\-3 landed 2026\-11\-15 \(member order; partition scheme
and matching; inner partitionwise join of two base appendrels\). Parent: M0148\-0001c3a5.

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

## Slice 2 — partition scheme and matching \(landed\)

- `partScheme` \(internal/optimizer/partscheme.go\) is goopg's view of PG's
  RelOptInfo `part_scheme` / `boundinfo` / `part_rels`:
  - the partitioned parent;
  - its partitions in PartitionDesc order, one per appendrel member;
  - the key columns' positions in the appendrel output.
- The planner stamps it on the appendrel's top SetOp \(`SetOp.partScheme`\)
  when it expands a partitioned table. `relPartScheme` reads it back off a
  search rel's leaf.
  - Scope: one\-level tables keyed by plain columns. A sub\-partitioned or
    expression\-keyed table carries no scheme.
- The partitionwise\-join preconditions, as PG states them:

  | goopg | PG |
  |---|---|
  | `samePartitionScheme` | find\_partition\_scheme identity: strategy, key count, key type, opclass, collation |
  | `partitionBoundsEqual` | partition\_bounds\_equal: same partition count, equal bounds pairwise \(range edges, list value sets with NULL, hash modulus / remainder, default\) |
  | `partkeyEquiJoined` | have\_partkey\_equi\_join: an equi\-join clause per key position, matched by source FROM item and column name |

- Nothing in the search consults them yet. Plans are unchanged
  \(Movement none\).
- Tests: `TestPartSchemeMatching` \(the three predicates\) and
  `TestPartSchemeStampedOnAppendrel` \(the planner stamps the scheme with
  partitions in bound order; fails with the stamp removed\).

## Slice 3 — child joins \(landed\)

- `tryPartitionwiseJoin` \(internal/optimizer/partitionwisejoin.go\) runs in
  `makeJoinRel` after the whole\-rel paths, as try\_partitionwise\_join runs
  in populate\_joinrel\_with\_paths.
  - Conditions: `enable_partitionwise_join`, an inner join \(no
    SpecialJoinInfo\), two base rels whose leaves are one\-level partitioned
    appendrels with equal bounds, and an equi\-join on every key.
- A child rel is the parent rel with its leaf swapped for member i: the same
  relids, `baseOffset` and width, the member's own rows, and a sequential
  scan priced on the member table.
  - This works because a member emits the appendrel's own layout, and the
    search's clauses address a leaf by its pre\-search offset.
  - A restriction above the appendrel \(a Filter during the search\) is
    pushed into a copy of the member chain with the finished\-plan pushdown's
    own helper \(`pushFilterIntoMembers`\).
- Each child join is sized by `sizeJoinRel` over the two child rels and gets
  both directions of `addPaths`. Its cheapest unparameterised path becomes an
  Append child. `PathPartitionwiseAppend` is costed as cost\_append's
  unordered arm and competes in `add_path`.
- Plan: a UNION ALL chain over the child join plans
  \(`createPartitionwiseAppendPlan`).
  - Each child join picks its own inner and outer, so each child is
    projected into binding order \(`reorderToLayout`\). Every child, and the
    Append, then emits one row shape.
  - The chain records its two parent appendrels
    \(`SetOp.PartitionwiseParents`, PG's apprelids):
    - EXPLAIN registers the parents' names before the members', so children
      print `t1_1`, `t2_1`;
    - a column above the Append deparses with its parent's name;
    - `renumberRTIDsFlatRtableOrder` re\-stamps the parents' RTIDs, which no
      plan node carries.
  - SetOp embeds `searchedTree`, because the Append can be the search root.
- Row\-locking SELECTs and UPDATE / DELETE / MERGE switch partitionwise join
  off. Their EPQ recheck reads a restriction kept on the Append
  \(`planTakesRowMarksOrModifies`\).
- Verification:
  - partition\_join's first and third witnesses match PG 18.3 plan for plan;
  - a value probe \(ORDER BY on the inner relation, filtered sides, an
    md5 over all joined rows\) is identical to PG;
  - `TestPartitionwiseJoin` fails with the hook removed;
  - regress mismatch: partition\_join 1990→1882, partition\_aggregate
    587→584.

### Not covered by slice 3

- Child rels carry only a sequential scan. PG's child rels get index,
  bitmap and partial paths, so its second witness merge\-joins an Index Scan
  per partition.
- Child joinrel sizing reads the parent's column statistics. PG reads each
  child's \(52 vs PG 84 rows on prt1 ⋈ prt2's first pair\).
- Member scans are priced at 0 pages where PG's ANALYZE sets them to 1
  \(pre\-existing; it also affects the whole\-rel Append\).
- Outer, semi and anti joins; N\-way joins \(a child join joined again\);
  partition\_bounds\_merge; partitionwise join under row locks and DML.

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
