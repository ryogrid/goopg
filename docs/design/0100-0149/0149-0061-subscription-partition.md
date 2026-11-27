# M0149\-0061 — Port subscription/t/013\_partition.pl

Status: in progress. TestPort\_Subscription013Partition is written and gated by `GOOPG_TAP_013`. It uses one
publisher and two subscribers in three pairings \(all\-goopg; PG 18.3 publisher → goopg subscribers; goopg
publisher → PG 18.3 subscribers\).

013 replicates partitioned tables:

- from leaves and through roots \(publish\_via\_partition\_root\);
- into differently shaped subscriber partitions, sub\-partitioned with reordered columns;
- with subscriber triggers, TRUNCATE, ATTACH / DETACH and conflict logging.

## Landed — M0149\-0061a \(S2\)

UPDATE and DELETE through a partitioned parent evaluated the parent's WHERE / SET / RETURNING against a leaf's raw
column order. A partition attached with its columns in another order \(`tab1_1 \(b text, a int\)`\) therefore
matched nothing: `UPDATE tab1 SET a = 2 WHERE a = 1` reported UPDATE 0 on a row that exists, and DELETE did the
same. A cross\-partition move also wrote the row in the source partition's layout.

PG maps child tuples to root attributes \(ExecInitPartitionInfo's attribute maps\) and converts a moved tuple
\(ExecCrossPartitionUpdate\). goopg now does the same:

- updateOp and deleteOp give a leaf whose layout differs from the parent's \(`partitionColOrderMatches`\) the
  inheritance child's remap, so expressions are evaluated in parent space and the new row is mapped back to the
  leaf.
- The pending update keeps `childColMap`, so the row is routed on its parent\-ordered form.
- A moved row is written in the destination's own column order.

Same\-layout partitions are unchanged.

The remapped path skips Phase\-1 EPQ, as inheritance children already do \(ledgered\).

Test: TestPartitionDMLMismatchedColumnOrder.

## Landed — M0149\-0061b \(sync LSN, and what it uncovered on the publisher\)

- **Sync LSN.** `RunTableSync` \(CaptureSyncLSN\) copies inside `BEGIN READ ONLY ISOLATION LEVEL REPEATABLE READ`. The
  transaction's first statement reads `pg_current_wal_lsn\(\)`, and that value is recorded as the SYNCDONE statelsn.
  The refresh no longer promotes such a rel to READY. The apply worker's `shouldApplyChangesForRel` is
  should\_apply\_changes\_for\_rel: READY, or SYNCDONE with statelsn <= remote\_final\_lsn \(the Begin's final\_lsn\).
  The commit's end\_lsn \(process\_syncing\_tables\) and idle keepalives \(`PromoteSyncedRels`\) promote the rel.
  PG instead copies in the snapshot of the tablesync worker's own slot \(USE\_SNAPSHOT\); goopg's
  snapshot\-then\-LSN statement leaves a small window for a commit concurrent with the copy \(ledgered\).
- **pgoutput commit LSNs.** Begin final\_lsn and Commit commit\_lsn now carry the commit record's start
  \(decode.c DecodeCommit buf\->origptr\), and end\_lsn its end \(`CommitStartAware`, `Decoder.ApplyCommitAt`\). goopg
  used to send the end in all three fields, which made a PG\-rule sync\-LSN comparison wrong for the transaction
  ending at the sync point. goopg's subscriber now reports end\_lsn as its applied position, as PG's does.
- **FOR ALL TABLES with publish\_via\_partition\_root.**
  - The walsender's filter now publishes a partition's change as its topmost ancestor's \(viaRootAll\).
  - `catalog.AllTablesPublicationRelations` is GetAllTablesPublicationRelations: plain tables and partitions without
    viaroot, plain tables and partitioned roots with it. Partitioned parents had been listed in every mode, so a
    subscriber copied a parent and its leaves.
- **Attribute map.** A leaf whose columns are in another order from the root is published through the root's
  layout \(`tupleConv`, build\_attrmap\_by\_name\). It used to be published as itself, and a subscriber without that
  leaf could not apply it.

Tests: TestApplyWorkerSkipsChangesBeforeSyncLSN; TestApplyWorkerPromotesSyncDoneToReadyOnCommit, updated to PG's
rule \(a commit past the sync LSN applies\).

## Landed — M0149\-0061f / 0061g

- **0061f.** pgoutput\_truncate skips a partition whose publish\-as relation is another relation, i.e. one published
  through its root. The root, listed in the same TRUNCATE, is sent. `PgOutput.truncate` now consults
  `PublishAsAncestor`.
- **0061g \(S2, DDL\).** `ALTER TABLE parent DROP COLUMN / ADD COLUMN` did not recurse into partitions:
  - ADD walked only inheritance children;
  - DROP walked nothing.

  A partitioned table and its partitions therefore disagreed on their columns, and a row routed into a partition
  lost the added column. PG's ATExecDropColumn / ATExecAddColumn recurse through find\_inheritance\_children. A
  partition's columns are always inherited \(attislocal = false, attinhcount = 1\). goopg now matches this:
  - `dropColumnFromChildren` drops the column from every partition, and from an inheritance child whose column is
    inherited, recursively;
  - `addColumnRecursive` walks partitions too, marks the child column inherited, and re\-syncs the child's
    pg\_attribute / pg\_class rows.

  Test: TestAlterTableColumnRecursesIntoPartitions.

## Landed — M0149\-0061d

`applyTruncate` truncated only the local target's own relfilenode, and a partitioned target has none, so every
row stayed. PG's apply\_handle\_truncate:

- skips a relation should\_apply\_changes\_for\_rel rejects;
- adds all partitions of a partitioned target \(find\_all\_inheritors\).

goopg now does both. It reaches the partitions through `underlyingInMemory`, because the worker's catalog is a
SearchPathCatalog wrapper. This also cleared the later sub\_viaroot stall \(0061h\).

Test: TestApplyWorkerTruncatePartitionedTarget.

## Landed — M0149\-0061e / 0061j \(conflict logging, commit timestamps\)

- **0061e.** `logApplyConflict` is ReportApplyConflict at LOG. The message is `conflict detected on relation "s.t":
  conflict=<kind>`. The DETAIL is the conflict's own line, followed by build\_tuple\_value\_details:

  ```
  Existing local row …; remote row …; replica identity (a)=(…).
  ```

  The replica identity uses the replica identity or primary\-key index; without one it shows `replica identity full
  …`. update\_missing and delete\_missing are logged and the change is skipped, in both the plain and the
  partitioned \(leaf\) paths. The plain UPDATE used to install the new row anyway \(M0149\-0044c\).
- **0061j.**
  - `track_commit_timestamp` is now a GUC \(postmaster context\). Before, a postgresql.conf setting it made the
    server fail to start.
  - transam keeps a commit\_ts store: `CommitTsData` returns a committed xid's time and replication origin, recorded
    while tracking is on.
  - The apply worker's transactions carry its subscription's origin \(`SetTxnOrigin`, named `pg_<suboid>`\).
  - `checkOriginDiffers` logs update\_origin\_differs / delete\_origin\_differs when the located row's xmin committed
    under another origin \("Updating the row that was modified locally in transaction N at T."\), and applies the
    change.

  Deviations \(ledgered\): the store is in memory only \(no pg\_commit\_ts SLRU or WAL\), and there is no
  pg\_replication\_origin catalog.

Test: TestApplyWorkerOriginDiffersAndMissingConflicts.

## Landed — M0149\-0061k \(S2: partitioned tables survive a restart\)

A restart lost every partitioned table:

- the startup loader kept only relkind r/m/v/S/f pg\_class rows;
- the partition key was never written to disk;
- a partition's parent link was not restored, so it came back as a standalone table;
- ATTACH PARTITION never set the child's `PartitionParentOID` or re\-journaled it.

**Write side** \(`syncTableToCatalogHeap`\):

- A partitioned table writes its pg\_partitioned\_table heap row \(`writePartitionedTableRow`\). partstrat, partnatts
  and partattrs are PG's; partclass and partcollation are zeros. partexprs holds `PartitionKeyDef` text, the
  pg\_get\_partkeydef body, as goopg stores other pg\_node\_tree columns.
- A partition writes its pg\_inherits parent edge, as PG's StoreSingleInheritance does.
- ATTACH sets `PartitionParentOID` and re\-journals the child on the immediate, DEFAULT and commit\-time paths.
- DETACH clears the link before its re\-sync.

**Reload side:**

- relkind 'p' rows are admitted.
- relpartbound is decoded and re\-parsed through `ParsePartitionBoundText`, which reuses `BuildPartitionBound`, the
  helper factored out of CREATE TABLE … PARTITION OF.
- `loadPartitionKeysFromHeap` \(`ParsePartitionKeyDef`\) runs before the pg\_inherits pass. That pass registers an
  edge whose parent is partitioned as a partition: `PartitionParentOID`, the parent's partition registry, and the
  parent's copy of the bound.

Test: TestPort\_PartitionedTableSurvivesRestart.

## Open

- **0061c.** Subscriber AFTER ROW triggers must fire on the partitions that routed UPDATEs land in.
- **0061d.** A replicated TRUNCATE of a partitioned target must reach its sub\-partitions.
- **0061l.** The origin\_differs check in every pairing, and the sub2 catch\-up with a goopg publisher.
