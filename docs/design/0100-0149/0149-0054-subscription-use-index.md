# M0149\-0054 — Port subscription/t/032\_subscribe\_use\_index.pl

Status: done. TestPort\_Subscription032SubscribeUseIndex passes 3/3 in all three pairings \(all\-goopg; PG 18.3
publisher → goopg subscriber; goopg publisher → PG 18.3 subscriber\).

032 checks the subscriber's row lookup under REPLICA IDENTITY FULL through `pg_stat_all_indexes.idx_scan`:

- a btree index on \(x, y\) is used, one scan per change;
- per\-partition indexes of a partitioned table are used;
- an expression\-only index and a partial index are not \(idx\_scan stays 0\);
- an index of two plain columns plus an expression is used;
- after DROP INDEX the lookup falls back to a sequential scan;
- a subscriber\-only column \(NULL\) still allows the index on \(x, y\);
- a unique index is used;
- a hash index is used.

## Landed — M0149\-0054a, the apply worker's row lookup

PG: logical/relation.c FindLogicalRepLocalIndex chooses the index. It takes GetRelationIdentityOrPK first: the
USING INDEX index, or the primary key. Failing that, for a REPLICA IDENTITY FULL remote it takes
FindUsableIndexForReplicaIdentityFull: the first index in OID order that IsIndexUsableForReplicaIdentityFull
accepts. That means btree or hash, not partial, and a leftmost key that is a plain column present in the remote
relation. execReplication.c RelationFindReplTupleByIndex then scans the index, rechecks the tuple, and stops at the
first match, counting one index scan \(pgstat\_count\_index\_scan\). RelationFindReplTupleSeq is the no\-index
fallback. The UPDATE or DELETE applies to that one tuple.

goopg, before this change, scanned the heap and deleted every matching row. An UPDATE of one of several identical
FULL\-identity rows therefore removed all of them and inserted one back. `idx_scan` in pg\_stat\_all\_indexes was a
constant 0.

goopg now \(internal/executor/applyworker\_lookup.go\):

- `applyLocalIndex` is FindLogicalRepLocalIndex. A hash index qualifies: goopg builds hash indexes on the btree
  substrate.
- `applyFindTuple` locates one tuple. Through an index, it does a RangeScan on the leading plain key columns that
  have a value, visits each HOT chain's visible member, rechecks with `rowMatchesKeyCollated`, and returns the first
  match. Otherwise it takes the first visible heap match. An index that does not store NULL\-keyed entries \(the
  blob format of an expression index, or a cluster without the capability\) is used only when the key row has an
  entry in it \(`applyIndexHasEntryFor`, using the maintenance path's own key builders\); otherwise a matching row
  could be missing from the index.
- An index whose key column has a nondeterministic collation is not probed. goopg's index keys compare bytewise
  \(`'FOO'` ≠ `'foo'`\), so the collation\-aware heap scan decides. 012\_collation caught this; PG's index compares
  under the collation.
- `applyDelete` / `applyUpdate` locate the tuple once \(the triggers see the same row\) and delete or replace only
  it \(`applyDeleteTuple`, `applyUpdateFound`\).
- `recordApplyIndexScan` adds the scan to the shared stats store. PG's apply worker reports at each applied commit.
  `catalog.IndexScanStatsFunc` renders it as `idx_scan`.

## Landed — M0149\-0054b, partitions of a FOR TABLE partitioned table

PG publishes a partitioned member as its leaf partitions when publish\_via\_partition\_root is false
\(GetPubPartitionOptionRelations, PUBLICATION\_PART\_LEAF\). pgoutput publishes a partition whose ancestor is a
member \(get\_rel\_sync\_entry's ancestor\_published\).

goopg listed the parent itself and matched the leaf's own name only, so partition changes were never sent. Now:

- `catalog.PublicationLeafTables` expands a member for pg\_get\_publication\_tables and pg\_publication\_tables;
- `RelationDef.AncestorNames` \(`catalog.PartitionAncestorNames`\) feeds the walsender filter's ancestor check.

Tests: TestApplyLocalIndexSelection, TestApplyUpdateReplacesOneOfIdenticalRows, TestPort\_Subscription032SubscribeUseIndex.

## Open residuals

- M0149\-0054c: ordinary queries' index scans count nothing; last\_idx\_scan, idx\_tup\_read and idx\_tup\_fetch are
  not tracked; the view lists a partitioned table's index.
- The recheck uses goopg's key\-row matching, where a NULL key cell matches anything, instead of tuples\_equal's
  NULL = NULL. Only the leading plain key columns are scan keys; PG keys every plain column, with IS NULL for NULLs.
- An UPDATE that finds no row still installs the new row \(PG: update\_missing, skipped — M0149\-0044c\).
- publish\_via\_partition\_root is not modelled. An ancestor's column list is not applied to its partitions.
