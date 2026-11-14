# M0148\-0001c3a10 — index paths on appendrel members

Status: slices 1\-2 landed 2026\-11\-15. Parent: M0148\-0001c3a5.

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

## Not covered

- Multi\-relation problems: inside a join the Append's columns still reach
  EXPLAIN key chases and parameterised quals that are not yet taught about
  it \(partition\_join regressed when allowed\).
- Ordered children under a Merge Append, and min / max per partition \(PG:
  `Limit → Append → Index Only Scan Backward … Index Cond: ((c = 3) AND (a
  IS NOT NULL))`\).
- Index\-only child paths; sub\-partitioned and expression\-keyed tables
  \(no partition scheme\); plan\-time pruning \(M0148\-0001k1 \(b\)\).
