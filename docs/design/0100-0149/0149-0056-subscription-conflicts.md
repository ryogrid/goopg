# M0149\-0056 — Port subscription/t/035\_conflicts.pl

Status: done. TestPort\_Subscription035Conflicts passes 3/3 in all three pairings \(all\-goopg; PG 18.3 publisher →
goopg subscriber; goopg publisher → PG 18.3 subscriber\). M0149\-0055 \(034\_temporal\) was skipped as a detour
first: temporal keys need WITHOUT OVERLAPS, exclusion enforcement, range\-typed index keys and GiST identity —
more than 3 links.

035 makes the subscriber hit three local rows at once through the primary key and two unique indexes. It does so
with an INSERT, with an UPDATE, and with an INSERT into a table that is partitioned only on the subscriber. In each
case it waits for PG 18's conflict report:

    ERROR:  conflict detected on relation "public.conf_tab": conflict=multiple_unique_conflicts
    DETAIL:  Key already exists in unique index "conf_tab_pkey", modified in transaction 757.
    	Key (a)=(2); existing local row (2, 2, 2); remote row (2, 3, 4).
    	Key already exists in unique index "conf_tab_b_key", modified in transaction 757.
    	...

The goopg publisher pairing passed as written. The goopg subscriber logged nothing.

## Landed — M0149\-0056a, conflict detection and ReportApplyConflict

PG: InitConflictIndexes gives the apply worker's result relation every immediate unique index. When an INSERT or
UPDATE violates any of them, CheckAndReportConflict finds the local tuple for each violated index
\(FindConflictTuple\). ReportApplyConflict then raises ERRCODE\_UNIQUE\_VIOLATION with the errmsg above. Its
conflict type is insert\_exists or update\_exists, or multiple\_unique\_conflicts when several indexes are violated.
The detail has one sentence and one value line per index \(conflict.c errdetail\_apply\_conflict /
build\_tuple\_value\_details\). Without track\_commit\_timestamp the sentence is "modified in transaction X.". The
error aborts the apply transaction; the worker restarts and retries.

goopg's apply worker maintained unique indexes without checking them, so the conflicting row was inserted as a
duplicate. Now \(internal/executor/applyworker\_conflict.go\):

- `applyConflictIndexes` lists the immediate unique indexes in OID order.
- `applyFindConflicts` runs one lookup per index through M0149\-0054's applyFindTupleSkip. It skips a NULL key and
  an index whose predicate does not admit the row. An UPDATE ignores its own old row. These lookups count no
  idx\_scan: PG detects the conflict while inserting the index entry \(\_bt\_check\_unique\), and 032's
  idx\_scan = 1 check caught the extra count.
- `applyConflictError` builds the 23505 error with PG's errmsg and detail. `pgServerLogText` renders it as PG's
  server log does \(DETAIL:, continuation lines tab\-indented by append\_with\_tabs\); ApplyMessage logs it.

## Landed — M0149\-0056b, INSERT routing into partitions

PG's apply\_handle\_tuple\_routing sends a change for a partitioned local table to its leaf \(ExecFindPartition\).
goopg wrote the row into the partitioned parent's own heap. `applyInsert` now routes through routeToPartition and
remaps the row to the leaf. The leaf's triggers, generated columns, conflict check and indexes then apply, and the
report names the leaf, as in PG.

## Landed — M0149\-0056c, names and order of partition index clones

DefineIndex names a partition's clone of a parent unique index with ChooseIndexName, which joins every key column:
`conf_tab_2_p1_a_b_key`. It clones the indexes in RelationGetIndexList \(OID\) order. goopg used `<child>_key` for
any multi\-column unique index and iterated a map, so the report showed the wrong name in the wrong place.
`partitionCloneIndexName` and `indexesInOIDOrder` fix both the PARTITION OF and the ATTACH PARTITION clone loops.

Tests: TestApplyConflictErrorFormat, TestPartitionCloneIndexName, TestPort\_Subscription035Conflicts.

## Open residuals

- M0149\-0056d \(S2, escalated\): goopg has no replication\-origin progress, so after an apply error the worker
  re\-applies transactions it had already committed. A missing row on UPDATE still installs the new row: no
  update\_missing / delete\_missing report and skip.
- UPDATE / DELETE are not routed into partitions; only INSERT is.
- The "modified locally/by origin … at \<timestamp\>" detail forms \(track\_commit\_timestamp\), NULLS NOT DISTINCT
  indexes, deferrable unique indexes, and pg\_stat\_subscription\_stats conflict counters are not modelled.
- A taken clone name is not uniquified with ChooseRelationName's numeric suffix.
