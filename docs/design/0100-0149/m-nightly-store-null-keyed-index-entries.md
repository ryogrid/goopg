# Storing NULL-keyed index entries: design and slices

Status: S1+S2 landed 2026-09-24 (`83f5635c8`); S3 open. Follows
[the interim guard](m-nightly-index-null-key-guard.md) (`821eda191`). Task:
`.ralph/fix_plan.md` M-NIGHTLY "store NULL-keyed index entries".

## Goal

PostgreSQL stores an index tuple for every heap row, NULL key columns
included (`index_form_tuple` writes a null bitmap; nbtree orders NULLs per
column, NULLS LAST for ASC). goopg's writers skip any row whose key has a
NULL column. The interim guard therefore refuses every scan that would need
those rows, which costs PG's plans wherever a nullable key column is left
unbound (upstream `btree_index`'s composite SAOP probe, `limit`'s
GroupAggregate over an index-only scan).

The goal is to store the entries, as PG does, and then relax the guard for
the indexes that have them.

## What the code does today (audited 2026-09-24)

| area | behaviour | NULL-safe once entries exist? |
|---|---|---|
| probe values (index scan `lookupKey`/`lookupKeys`/range prefix and bounds, SAOP elements, index-only, bitmap, UPDATE-by-index; NLI inners use the same) | a NULL probe value matches nothing; no key is built | yes |
| row-derived probes (`indexRowProbeKey`: unique checks, FK, deferred exclusion) | `indexRowKeyValues` returns ok=false on NULL, so no probe | yes, **provided the probe side keeps that skip** |
| open-ended range, leading column (`a > 5`) | `hiKey` stays nil, the scan runs to the end of the tree; the legacy `tryRangeIndexScan` single-conjunct shape keeps no Filter | **no**: would return the NULL rows at the end |
| equality prefix + open range (`a = 1 AND b > 5`) | upper bound is the prefix pivot, read as +infinity past it | covered by the retained Filter recheck, but the scan reads the NULL entries |
| NULLS FIRST attributes (DESC by default) | the low end would hold the NULLs | **no**, symmetric to the above |
| index-only decode (`pgIndexTupleKeyDatums`) | `DeformPGIndexTuple` isnull → `NullDatum` | yes |
| writers: bulk build (`indexBuildEntryKey` → `collectBTreeEntries`), runtime insert (`indexEntryKey` → `indexRowKey` → `indexRowKeyValues`), upsert arbiter (`arbiterKey` returns nil on NULL; `maintainArbiter` skips) | NULL-keyed rows produce no entry | to change |
| key-change test (`indexKeyColumnsChanged`) | any NULL key means "changed" (conservative; only scopes the unique check) | yes |
| HOT (`hotUpdateEligible`) | column-based: an UPDATE touching any indexed column is non-HOT | yes |
| VACUUM (`vacuumIndexes` → `VacuumIndexPages(deadTIDs)`) and the split-time purge | TID-based | yes |
| amcheck (`operators_bt_index_check.go`) | skips NULL-keyed rows when building the expected entry set | to change |
| format knowledge | only the executor knows (`buildPGIndexKeyDesc`, pgindex_keydesc.go); `catalog.Index` carries no marker and the optimizer cannot import the executor | to change |

## Old indexes: a cluster capability flag

Indexes built before this change lack the entries, and nothing durable
records which indexes have them. `catalog.Index` has no per-index marker
(`indisvalid`/`indisready` are emitted as constants); the only durable
per-index channel is `pg_class.reloptions`, and a goopg-private reloption
would be visible, a new divergence.

So the unit is the cluster. initdb of a NULL-storing binary writes a
goopg-private marker file (`global/pg_goopg_features`, after the
`pg_goopg_catalog_cache.json` precedent) naming the capability
`null_keyed_index_entries`. At open the server reads it into a process-wide
flag.
- **Flag on:** every tuple-format index stores NULL-keyed entries, and the
  planner may relax the guard for tuple-format indexes.
- **Flag off (any older cluster, including the read-only reference
  clusters):** writers keep skipping, amcheck keeps expecting no entries,
  and the guard stays for every index. This is today's behaviour, correct
  and conservative.

A later tool could REINDEX an old cluster and set the flag; that is out of
scope.

## Slices

1. **Readers stop at NULL** (inert until entries exist).
   - An open-ended bound on a tuple-format index stops where the bounded
     column turns NULL. For ASC NULLS LAST the upper bound becomes a
     pivot with that attribute NULL, exclusive. For NULLS FIRST
     attributes the lower bound skips past the NULLs.
   - Applies to index scan, index-only scan and bitmap range probes.
   - Full scans with no bound keep returning every entry; NULLs included
     is PG-correct for an ordered scan.
   - Tests use a hand-built tree holding NULL entries.
2. **Writers store the entries, behind the flag.**
   - The marker file: initdb writes it, open reads it.
   - `indexBuildEntryKey`, `indexEntryKey` and the arbiter entry key
     include NULL-keyed rows in the tuple format.
   - Row-derived probes and arbiter probes keep skipping NULL.
   - NULLS NOT DISTINCT dedup is unchanged.
   - amcheck expects the entries when the flag is on.
   - Gate: new-cluster tests showing the entries exist and every scan
     shape still returns PG's rows. The guard is still on, so no plan
     moves.
3. **Relax the guard.**
   - Move `buildPGIndexKeyDesc`'s eligibility test to a package the
     optimizer can import (catalog, or nbtree if the comparator table
     lives there).
   - `indexUnboundKeysNullSafe` / `indexUnboundKeysNotNull` accept an
     index when the flag is on and the index is tuple-format.
   - Gate: full plan-parity set, regress `btree_index` and `limit`
     recover their index plans, and the NULL-key row tests stay green.

Slices 1 and 2 may land together if slice 1 cannot be exercised without
real entries; slice 3 must not land before both.

## S1+S2 landed (2026-09-24, `83f5635c8`)

As designed, with these specifics:

- **Marker file.** `global/pg_goopg_features` (an `initdb.SampleFiles`
  entry) holds `null_keyed_index_entries`. `initdb.Open` reads it before
  any index is touched and sets `catalog.SetNullKeyedIndexEntries`.
- **Bulk build.** `indexBuildEntryKey` returns the NULL-bearing image in a
  capable cluster. `collectBTreeEntries` files it in `nullEntries`, which
  is appended after the duplicate walk: two NULL images compare equal in
  index order, and `BulkCreate` sorts its input anyway.
- **Runtime writes.** `indexEntryKey` projects through
  `indexRowKeyValuesKeepNull`, while `indexRowProbeKey` keeps
  `indexRowKeyValues` (NULL means no probe).
- **ON CONFLICT.** `arbiterEntryKey` keeps NULLs and `arbiterProbeKey` does
  not. `applyInsert` had gated the arbiter entry on a probe key existing,
  so a NULL conflict key left the arbiter without an entry; it now files
  one.
- **Scan bounds.** `nullStopRangeBound` applies only to a range with
  exactly ONE bound. With none, the scan is a full scan or an
  equality-prefix probe, and both keep the NULL entries. The index scan
  (`nullStopLo`/`nullStopHi` feed `NewScanCursor`) and the index-only scan
  (`RangeScanWithPosLeafFilter`) both use it. Bitmap scans only probe
  equality.
- **Verification.**
  - `bt_index_check(…, heapallindexed)` passes on bulk-built,
    runtime-maintained and arbiter indexes, and passes again after a
    kill -9 restart (WAL replay).
  - Every one-sided shape matches the no-index answer, and each fails with
    the stop disabled.
  - Upstream regress on a capable cluster is identical to HEAD.

Not covered (ledgered): DESC key columns get no NULL stop; a cluster can
only gain the capability at initdb (no upgrade path); removing the marker
from a capable cluster would leave its NULL entries unguarded.
