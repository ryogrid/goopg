# M0148\-0001c3a10 — index paths on appendrel members

Status: slices 1\-3 landed 2026\-11\-15. Parent: M0148\-0001c3a5.

## Problem

- A partitioned table reaches the planner as a pre\-built UNION ALL of
  member scans, so the partitions' indexes never served a WHERE clause.
- `select * from pa where b = 'v12'` with an index on `b`:
  - PG 18.3: `Append` of per\-partition `Bitmap Heap Scan … Index Cond:
    (b = 'v12'::text)`;
  - goopg: `Append` of `Seq Scan … Filter: (b = 'v12'::text)`.
- PG's set\_append\_rel\_pathlist \(allpaths.c\) plans every partition as a
  child rel with its own paths, and add\_paths\_to\_append\_rel builds an
  Append over each child's cheapest path.

## Slice 1 — restricted single\-relation appendrels \(landed\)

- `addAppendRelChildPaths` \(internal/optimizer/partitionwisejoin.go\) runs
  with the base\-rel index producers.
  - It builds each partition's child rel with the M0148\-0001c3a9 machinery:
    the partition's bare scan, the parent's restriction moved onto it, and
    restriction, ordered, parameterised and \(new\) bitmap index paths.
  - It files an Append over each child's cheapest unparameterised path,
    which competes in add\_path with the pre\-built UNION ALL.
- The one\-relation search admits a partitioned appendrel leaf
  \(relfromjoinlist.go\). Before, a single\-table statement reached the
  base\-rel producers only through a gate that required a plain scan leaf,
  so a partitioned table never got index paths at all.
- `PlannerSettings.KeepAppendRelsWhole` replaces reusing
  `EnablePartitionwiseJoin = false` for row\-locking SELECTs and DML. It
  keeps both partitionwise joins and per\-member child paths out of those
  plans, whose EPQ recheck reads the restriction above the appendrel.
- EXPLAIN: `scopeSources` \(explain\_names.go\) registers the parent
  appendrels an Append stands for \(`SetOp.PartitionwiseParents`\), so a key
  inside a sub\-query no longer resolves to another level's relation.
  - Without this, a semi\-join's sort key printed `t1_3.a` for the outer
    `t1.a`.
- Scope gates, each from a measured regression:
  - a one\-relation problem only;
  - the appendrel must carry a restriction \(without one, ordering\-only child
    index paths undercut seq scans: regress aggregates' `p_t1`\);
  - the restriction must not read an outer level \(correlated sub\-plan
    bodies: PG plans those as semi / anti joins\).

## Verification

- `TestAppendRelChildIndexPaths` pins PG 18.3's plans for the two witnesses
  and their values \(incl. `tableoid::regclass`\). It fails without the
  producer.
- Regress vs a same\-suite HEAD baseline: inherit 735→727 \(inventory set:
  720→712\). partition\_join, partition\_aggregate, aggregates and the rest
  are unchanged; join moved by one row\-order line \(known flapper\).

## Slice 2 — index\-only ordering behind an equality prefix \(landed\)

- The per\-partition min / max PG plans \(`Limit → Append → Index Only Scan
  Backward … Index Cond: ((c = 3) AND (a IS NOT NULL))`\) needs, first, the
  same plan on a plain table. goopg already had it for the plain Index Scan
  but not for the index\-only scan.
  - `select * … where c = 3 order by a desc limit 1` planned PG's
    `Index Scan Backward`.
  - `select a …` \(covered, so index\-only\) sorted a bitmap scan instead.
- Cause: `addOneIndexOnlyPath` gave the probe an ordering only for range or
  array clauses \(and the full\-index shape\). PG's build\_index\_paths
  computes one useful\_pathkeys per index whatever the clause kinds, and an
  index whose leading columns are equated to constants delivers its next
  column in order.
- Fix: the index\-only probe carries the ordering whenever there are clauses,
  as `addOneRestrictionIndexPath` already did; its backward twin follows.
- `TestIndexOnlyEqualityPrefixOrdering` pins PG 18.3's two plans and fails
  without the fix. The fixture uses NOT NULL key columns: an in\-process
  cluster without NULL\-keyed index entries declines index paths over
  nullable key columns.
- No regress case moved \(same\-suite A/B identical\); TPC\-H / TPC\-DS plans
  unchanged.

## Slice 3 — index\-only child paths and the ordered Append \(landed\)

- `addChildIndexOnlyPaths` runs the index\-only producer on each partition
  child rel. A child reads the parent's needed columns under the member's
  alias, and a partition carries the parent's column names.
- `addOrderedAppendRelChildPath` is generate\_orderedappend\_paths' plain
  Append arm for partitions\_are\_ordered:
  - it applies to a RANGE partitioned table without a default partition
    whose partition key leads the query's ordering;
  - each child contributes its cheapest path that satisfies the query
    pathkeys, children in bound order, reversed \(and Backward\) for DESC;
  - a child with no such path declines the Append \(PG would need a Merge
    Append or a Sort there\).
- PG's per\-partition min / max rides it: `Result → InitPlan → Limit →
  Append → Index Only Scan Backward … Index Cond: (c = 3)`.
- `TestOrderedAppendRelChildPaths` pins PG 18.3's three plans \(ASC, DESC,
  max\) and their values; all three fail without the ordered Append.
- Regress: unchanged \(inherit's `parted_minmax` witness has one partition
  and already matched\); TPC\-H / TPC\-DS unchanged.

### Not covered by slice 3

- With a nullable ordering column, min/max adds `a IS NOT NULL` and the
  child's index\-only producer declines `(c = 3) AND (a IS NOT NULL)`
  \(`indexOnlyLeafClauses` has no equality\-prefix \+ NullTest shape\). The
  child then uses `Index Scan Backward` where PG uses `Index Only Scan
  Backward`.
- Merge Append \(a default partition, a non\-leading partition key,
  LIST / HASH\).

## Slice 4 — an equality prefix plus a trailing NullTest, index\-only \(landed\)

- `indexOnlyLeafClauses` \(pathindexonly.go\) admits PG's index\-qual
  shape `(c = 3) AND (a IS NOT NULL)` over `(c, a)`. That is an equality
  prefix followed by a NullTest on the next index column, with no range or
  array on that column. The plain producer already filed this shape
  \(`restrictionNullTestOnColumn`\).
  - `IS NOT NULL` counts the column as bound for the NULL\-key rule
    \(`indexUnboundKeysNotNull`\), because it excludes the column's
    NULL\-keyed entries.
  - `IS NULL` leaves the column unbound, so it still needs an index that
    stores NULL\-keyed entries.
- In the IOS lowering \(createplanindex.go\):
  - the prefix becomes `Keys` / `Key`;
  - the NullTest becomes `IndexOnlyScan.Cond`. That is the per\-entry check
    the min/max builder already used, and EXPLAIN prints it after the keys
    in `Index Cond`. Its column is re\-pointed at the covered output column.
- Cost: unchanged arm. `hasRangeClause` sees the NullTest, so
  `rangeIndexSelectivity` prices equality × `nulltestsel`, as the plain
  path does.
- PG's nullable min/max now matches, both per partition under the ordered
  Append and on a plain table: `Result → InitPlan → Limit → [Append →] Index
  Only Scan Backward … Index Cond: ((c = 3) AND (a IS NOT NULL))`.
  - `TestIndexOnlyPrefixNullTest` pins both plans and their values, with
    NULL\-keyed rows present.
  - The live probe \(`tmp/c3a10s2.sql` plus the NULL\-row values,
    including the `IS NULL` count\) matches scratch PG 18.3 byte for byte.
- Regress: same\-suite A/B unchanged apart from `join` 2837→2836. That line
  is the known Hash Right Join unmatched\-row order flap \(the same plan in
  both runs; the standalone `join` gives 2685 twice\). TPC\-H and TPC\-DS
  are unchanged.

## Slice 5 — Merge Append over the partitions' ordered paths \(landed\)

- When the partitions are not ordered on the query's leading key, the child
  rels now give the ordering through a Merge Append. That covers a default
  partition, LIST partitioning and a non\-leading partition key.
  - The new function is `addMergeAppendRelChildPaths` \(partitionwisejoin.go\),
    PG's generate\_orderedappend\_paths Merge Append arm.
  - The ordered RANGE case keeps slice 3's plain Append
    \(`rangePartitionsOrderedOn`\).
- Orderings \(`childOrderingPrefixes`\): each leading part of the query
  ordering that some child's unparameterised path delivers. This is
  all\_child\_pathkeys cut to what a one\-relation query can use. A strict
  prefix leaves the rest of the ORDER BY to an Incremental Sort above the
  merge \(`Presorted Key: pm.a`\).
- Child selection, per child, matching PG:
  - the cheapest\-startup and the cheapest\-total path that deliver the
    ordering;
  - when no path delivers it, the cheapest\-total path under a Sort. The
    Sort is bounded by the LIMIT, because the rel is the whole query
    \(create\_merge\_append\_path's limit\_tuples\);
  - under a LIMIT, also the cheapest fractional path
    \(`cheapestFractionalPathForPathkeys`\).
- Paths filed: the startup Merge Append; the total one when the two differ;
  and the fractional one. All are priced by `mergeAppendCost`
  \(cost\_merge\_append\).
- The path is a `PathPartitionwiseAppend` with `pwMerge`. Its lowering
  \(moved to createplanpartitionwise.go, inside the lowering walk\) puts
  `MergeKeys`, translated against the Append's layout, on every link of the
  UNION ALL chain. That is goopg's existing Merge Append operator.
- Witnesses: `select a from pd where c = 3 order by a limit 2` with a default
  partition, its DESC and min/max forms, the LIST and non\-leading\-key
  tables, a child without an ordered index \(Sort child\), and `order by
  a, b` \(Incremental Sort\).
  - These match PG 18.3 in plan and values; costs match exactly.
  - `TestMergeAppendRelChildPaths` pins four plans and five value checks.
- Regress: inherit 727→707 \(standalone 712→692\). The mclparted /
  mcrparted `Merge Append` plans now match PG, apart from plan\-time
  pruning. join 2836→2837 is the known row\-order flap.

### Not covered by slice 5

- LIST partitions that partitions\_are\_ordered accepts \(no interleaving,
  no default\) still take the Merge Append, where PG uses a plain Append.
- The ordered plain Append still declines when a child has no ordered path,
  where PG appends the child under a Sort.
- A trailing ORDER BY key pinned to a constant \(`order by a desc, b` with
  `b = 'v12'`\) is not dropped from the query pathkeys. PG drops it, so its
  index path delivers the ordering. This also happens on plain tables.

## Slice 6 — unrestricted ordered appendrels \(landed\)

- A partitioned table with no WHERE but a query ordering now gets child rels
  too \(`addAppendRelChildPaths`\). Only the Merge Append arm is filed over
  them. The unordered Append over the children's cheapest paths stays
  restricted\-only, because unrestricted ordering\-only child index paths
  would undercut the seq scans \(regress aggregates' p\_t1\).
  - The partitions\-ordered plain Append stays with
    `addOrderedPartitionAppendPaths` \(orderedpartitionappend.go\). That
    producer already files PG's Index Only Scans, while a child rel's full
    index\-only scan carries no ordering yet \(regress inherit's
    range\_parted\).
  - Members whose scans already carry the WHERE quals
    \(`membersCarryFilters`\) are restricted appendrels without the parent
    Filter. They stay whole, as in slices 1\-5.
- Sizing: an unrestricted child rel is sized by its scan's
  estimate\_rel\_size, with the 10\-page floor \(`childScanRows`\), as
  set\_append\_rel\_size sizes a member. pg\_class's count of 0 for a
  never\-vacuumed partition priced its index paths as one row, which made
  the Merge Append beat PG's HashAggregate on aggregates' p\_t1.
  - Restricted child rels keep the pg\_class count \(`childRelTuples`\).
    Moving them exposes a missing child index\-only path for boolean\-column
    quals \(bool\_rp: `Filter: b` against PG's `Index Cond: (b = true)`\).
- Executor fix found by the gates: an `INSERT … ON CONFLICT` routed to a
  partition wrote the row twice into the leaf's arbiter index.
  - Cause: the non\-arbiter maintenance skipped the parent's arbiter OID,
    while `maintainArbiter` had written the leaf's matching index
    \(`arbiterIndexOIDOn`, operators\_upsert.go\).
  - Effect: every index scan of that leaf returned the row twice. Isolation
    partition\-key\-update\-2 / 3 caught it once `SELECT * FROM foo ORDER BY
    a` became a Merge Append of index scans.
  - Pinned by `TestPartitionedUpsertSingleArbiterEntry`.
- Witnesses \(`tmp/c3a10s6.sql`\): ordered RANGE Append, default\-partition
  Merge Append with and without LIMIT, min/max, a GroupAggregate over the
  merge, and Sort / Append where PG keeps them. All match PG 18.3;
  `TestUnrestrictedMergeAppendRelChildPaths`.
- Regress: inherit 707→705 in the same\-suite run, 692→690 standalone. The
  mclparted / bool\_lp `Merge Append` headers now match; their children are
  Index Scan where PG has Index Only Scan.

### Not covered by slice 6

- A child rel's full index\-only scan has no pathkeys, so unrestricted
  ordered children are Index Scans where PG uses Index Only Scans.
- Restricted child rels are still sized from pg\_class until the boolean
  index\-only clause gap is closed.

## Slice 7 — index\-only children under `select \*` \(landed\)

- A bare `*` makes the statement\-wide needed\-column set untrustworthy
  \(`neededColumnNames`\). The child rels of a `select * … order by a`
  Merge Append therefore offered no index\-only path, and PG's Index Only
  Scans planned as Index Scans \(regress inherit's mclparted\).
- `addBareStarOnlyNeededColumns` \(pathindexonlyneed.go\): when the set is
  unknown, the walk is retried with the bare `*` targets dropped and over
  every pulled\-up body. If that walks, the set is published under
  `neededBareStarOnlyKey`, with `neededColsKnown` still false. Consumers
  that gate on `neededColsKnown` never read it.
- `childNeededColumns` \(partitionwisejoin.go\): with the marker present, a
  child rel needs every column its bare scan emits, plus any system column
  the rest of the statement names.
  - Every scan column must be covered, a superset of what the query reads,
    so the path is offered only where it is sound.
  - `select *, ctid` still declines.
- Executor fix found while testing: the Merge Append \(`setOp.nextMerge`\)
  emitted rows without their input slot's tid. A `ctid` read above it was
  NULL — wrong results since slice 5. The merge now keeps each front row's
  tid \(`mergeTID`\) and stamps it on the emitted slot.
- Witnesses: mclparted and an interleaved LIST `un` table \(`select *`,
  DESC LIMIT, `*, ctid`\) match PG 18.3 in plan and values.
  `TestStarMergeAppendIndexOnlyChildren` pins them, including a ctid check
  that fails without the executor fix.
- Regress: inherit 705→679 in the same\-suite run, 690→664 standalone.

### Not covered by slice 7

- A non\-interleaved LIST table is partitions\_are\_ordered in PG, which
  plans a plain Append, while goopg plans a Merge Append.
- With `enable_indexscan = off`, goopg still prefers a disabled ordered
  index path. This happens on plain tables too \(`select … from ud_1 order
  by a limit 5`\); PG sorts a seq scan.

## Slice 8 — LIST partitions\_are\_ordered \(landed\)

- `listPartitionsOrdered` \(partitionwisejoin.go\) implements
  create\_list\_bounds' interleaved\_parts test as partitions\_are\_ordered
  reads it.
  - With every non\-NULL value sorted by the key type, the partitions'
    positions never step back.
  - The NULL\-accepting partition holds only NULL and is last.
  - There is no default partition.
  - Positions are `ps.parts`, the order the plain Append emits, so the test
    checks exactly the order it licenses. An uncomparable value declines.
- `rangePartitionsOrderedOn` accepts such a LIST table. A restricted ORDER
  BY on its key then plans PG's plain Append in partition order, reversed
  and Backward for DESC, instead of a Merge Append.
  `TestListOrderedAppendRelChildPaths` pins it.
- Scope: unrestricted LIST tables keep slice 6's Merge Append.
  - Letting them take the plain Append regressed regress aggregates' p\_t1
    \(99→103\). The ordered child Append beat the appendrel's HashAggregate
    input because goopg's display cost of the members' Append is about
    twice PG's cost\_append.
  - That cost: the legacy display cost charges `cpu_tuple_cost` per row at
    every UNION ALL link and every tableoid Project, and member seq scans
    without stats have 0 pages instead of estimate\_rel\_size's 10.
  - Fixing the seed path cost alone does not help: the grouping stage
    prices its input from the finished node's display cost
    \(`upperInputPathCost`\).
- Regress: unchanged in the same\-suite A/B \(no regress case has a
  restricted ordered LIST query\). With the unrestricted arm enabled,
  inherit 679→677 but aggregates 99→103, so it was not taken.

## Not covered

- Multi\-relation problems: inside a join the Append's columns still reach
  EXPLAIN key chases and parameterised quals that are not yet taught about
  it \(partition\_join regressed when allowed\).
- Sub\-partitioned and expression\-keyed tables
  \(no partition scheme\); plan\-time pruning \(M0148\-0001k1 \(b\)\).
