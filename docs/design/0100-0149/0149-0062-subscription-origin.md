# 0149\-0062 — Port subscription/t/030\_origin.pl \(origin = none, copy\_data warning\)

Status: done \(TestPort\_Subscription030Origin, three pairings 3/3\).

030\_origin.pl builds bidirectional and three\-node logical replication with `CREATE SUBSCRIPTION ... WITH
(origin = none)`. It checks:

- changes that a node received from another subscription are not sent on;
- update\_origin\_differs and delete\_origin\_differs are detected;
- `copy_data = on` with `origin = none` WARNs when the publisher's table is itself subscribed, directly or through a
  partition ancestor or descendant.

The ported test runs three pairings: all\-goopg; goopg node B with PG 18.3 nodes A and C; PG 18.3 node B with goopg
nodes A and C.

## Landed — M0149\-0062a

### The commit record names its replication origin

PG filters at the commit record. XactLogCommitRecord adds `XACT_XINFO_HAS_ORIGIN` and `xl_xact_origin{origin_lsn,
origin_timestamp}` when `replorigin_session_origin` is set. The record header carries the origin id
\(`XLR_BLOCK_ID_ORIGIN`, XLOG\_INCLUDE\_ORIGIN\). DecodeCommit calls FilterByOrigin and forgets a rejected
transaction.

- The apply worker's transaction already carried its origin id \(`SetTxnOrigin`, 0061j\).
  - `applyCommit` now records the remote commit's end LSN and time \(`SetTxnOriginProgress`, as
    apply\_handle\_commit\_internal sets `replorigin_session_origin_lsn` and `_timestamp`\).
  - The xact\-marker hook reads all three \(`TxnOriginOfXID`\).
- `EncodeXactCommitPGWithOriginAt` writes the xinfo bit and `xl_xact_origin` last.
  - `assembleXLogRecordOriginInto` puts the origin chunk after the block headers and before the main\-data header,
    where XLogRecordAssemble puts it.
- `Classify` passes `RecordOrigin` to `Decoder.ApplyCommitOriginAt`.
  - A decoder origin filter \(`SetOriginFilter`, filter\_by\_origin\_cb\) forgets the transaction.

### pgoutput's origin option and the subscriber's request

- The walsender parses `origin` \('none' / 'any'; anything else is `unrecognized origin value`, 22023, as
  parse\_output\_parameters\). 'none' installs pgoutput\_origin\_filter: skip any origin other than 0.
- `pg_subscription.suborigin` is stored \(`Subscription.Origin`; CREATE, ALTER ... SET, the heap row, the restart
  reload\). Before this it was parsed and dropped.
- The receiver sends `origin '<value>'` in START\_REPLICATION, as libpqrcv\_startstreaming does for a server ≥ 16.
- The launcher restarts a worker whose origin changed.

### check\_publications\_origin

CREATE SUBSCRIPTION \(connect\) and ALTER SUBSCRIPTION ... REFRESH \(and SET / ADD with refresh\) run PG's publisher
query verbatim \(`replication.CheckPublicationsOrigin`\). REFRESH excludes the tables the subscription already has
\(subrel\_local\_oids\). The executor raises PG's WARNING with its DETAIL and HINT \(55000\); `WarningWithHint` gained
a DETAIL field.

The query is also what a PG subscriber sends a goopg publisher, so goopg must answer it:

- `pg_partition_tree` / `pg_partition_ancestors` resolve their argument against the lateral siblings and the outer
  scope, as `pg_options_to_table` does.
  - Their executor op binds a lateral outer slot, or the correlation stack inside a sublink.
  - Before this, `pg_partition_ancestors(PS.srrelid)` inside the JOIN's ON clause failed with `column "srrelid" does
    not exist`.
  - The plan node is registered in the expression walkers \(unnest, foldconst, walk\_export\).
- `FROM P, LATERAL f(P.x) GPT JOIN T ON …` puts the lateral reference in a leaf of an explicit JOIN that is itself the
  comma item.
  - `nodeReferencesOuter` recurses into a Join's children: the right child only when the Join is not itself lateral,
    since a lateral join's right side binds to its own left sibling.
  - `joinOp.BindLateralOuter` forwards the outer row along the same split.
  - Before this, the query died with `column ref pubname/1 on nil slot`.

### Apply UPDATE links the ctid chain

`applyUpdateFound` deleted the old version and inserted the new one without linking them. A concurrent READ COMMITTED
DELETE that waited on the apply transaction found a deleted row with no successor and skipped the new version. 030's
`DELETE FROM tab` on node B left the row the apply worker had just updated, and the next INSERT looped on
insert\_exists.

The old version's t\_ctid now points at the new one, as updateOp's non\-HOT path does \(`stampOldCtid`;
simple\_table\_tuple\_update is a heap\_update\).

Tests: TestCommitOriginIsFilteredByOrigin, TestSubscriptionOriginOption, TestPartitionTreeCorrelatedAndLateralUnderJoin,
TestApplyUpdateLinksCtidChain, TestPort\_Subscription030Origin.

## Deferred \(ledgered\)

- Only the commit record names the origin. PG stamps every record of a replication\-origin session
  \(heap\_insert / heap\_update set XLOG\_INCLUDE\_ORIGIN\).
- Abort and COMMIT PREPARED records carry no origin.
- The origin id is still derived from the subscription OID; there is no pg\_replication\_origin catalog.
- The apply UPDATE is still a delete stamp plus an insert plus a ctid stamp. It is not a single xl\_heap\_update record,
  never HOT, and the new tuple has no HEAP\_UPDATED bit.
