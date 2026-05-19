# M0106-0010 Step 3q — Split `pg_index` OIDs 2678 and 2679

## Status

Accepted (landed 2026-05-18).

## Context

Step 3p (PARTIAL) populated the `pg_index_indexrelid_index` btree at
`base/{1,5}/2678` + `global/2678` with 23 correctly oid-sorted index
tuples, yet the PG-standby boot FATAL persisted:

```
FATAL:  cache lookup failed for index 2671
```

Investigation in Step 3p documented the root cause:

> BOTH `pgIndexInitialEntries()` (initdb.go) and `nailedLocalRels`
> (relcache_init.go) OMIT OID 2678 (`pg_index_indexrelid_index`)
> ITSELF.

The Step 3p btree therefore had nothing to index for OID 2678 — there
was no `Form_pg_index` heap row for it, no `pg_class` row, no relcache
descriptor, no anything. When PG's
`RelationCacheInitializePhase3` SHARED critical-index pass
(`postgres/src/backend/utils/cache/relcache.c:4214`) called
`load_critical_index(DatabaseNameIndexId=2671)`, the cascading
`SearchSysCache1(INDEXRELID, 2671)` fell back (after the LOCAL pass
flipped `criticalRelcachesBuilt = true`) to a sysscan against 2678 —
Step 3p's btree returned correctly populated rows for all 23 *other*
nailed indexes, but `(2671 → ?)` was absent because the upstream
catalog source for 2671's row was missing from goopg's seeds.

A complicating audit finding: the single `pgIndexInitialEntries` row
labelled `2679 = pg_index_indexrelid_index` carried `IndKey={1}`,
`IsUnique=true`, `IsPrimary=true` — the row content of OID 2678 under
a wrong OID. PG18 actually wires the two indexes as:

```
postgres/src/include/catalog/indexing.h:
  DECLARE_UNIQUE_INDEX_PKEY(pg_index_indexrelid_index, 2678,
      IndexRelidIndexId,    pg_index, btree(indexrelid oid_ops));
  DECLARE_UNIQUE_INDEX     (pg_index_indrelid_index,   2679,
      IndexIndrelidIndexId, pg_index, btree(indrelid    oid_ops));
```

So 2678 keys on `indexrelid` (attnum 1) and is the PRIMARY KEY; 2679
keys on `indrelid` (attnum 2) and is `UNIQUE` only. goopg's single
row had to be split — adding 2678 alone would let PG load
`pg_index_indrelid_index` against the wrong key column, producing a
"column is not in index" FATAL of the Step-3n shape.

## Decision

Two-line change at the data layer; no encoder change, no btree builder
change, no `Init` flow change. Step 3p's existing
`bootstrapPgIndexIndexrelidIndex(dataDir, pgIndexTIDs)` call already
threads heap TIDs from `bootstrapPgIndexTuples` through to the btree
builder. Once OID 2678 lands in `pgIndexInitialEntries`, its heap row
is written, its TID flows into the map, and the btree gets the right
24th leaf entry — no Step 3p code change required.

### Catalog seed changes

`internal/initdb/initdb.go::pgIndexInitialEntries` (replace the
single 2679 row with two rows, fix the comment):

```go
entry(2678, 2610, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),  // pg_index_indexrelid_index
entry(2679, 2610, []int16{2}, []uint32{oidOps}, []uint32{0}, true, false), // pg_index_indrelid_index
```

`internal/initdb/relcache_init.go::nailedLocalRels` idxSpec list (add
2678 just before the existing 2679 entry):

```go
{2678, "pg_index_indexrelid_index"},
{2679, "pg_index_indrelid_index"},
```

### Empty-placeholder file lists

Three `[]uint32` literals in `internal/initdb/initdb.go`
(base/1/ + base/5/ + global/ placeholder writes around
`bootstrapSharedCatalogs` and `bootstrapPostgresDatabase`) all gain
`2678`:

```
2654, 2655, 2658, 2659, 2662, 2663, 2667, 2678, 2679, 2680, 2682, ...
```

Step 3p's `bootstrapPgIndexIndexrelidIndex` runs *after* these
placeholder writes and overwrites the file at all three paths with the
populated 2-block btree; the placeholder write only matters if Step 3p
were skipped, in which case PG's `mdopen` would still find a valid
empty btree file (matching the M0106-0010 invariant that every nailed
index OID must have a heap-tile-readable file on disk before any
backend opens it).

### Why split flag semantics matter

`pg_index_indrelid_index` is `UNIQUE` but not `PRIMARY KEY`. Marking
it primary would lie to PG's planner about the relation's primary
key — once index-only scans or `relhaspkey` lookups are exercised
during catcache replenishment, the wrong-shape row could trigger
`ConditionalPathKeyExists` or constraint-cache mismatches. Splitting
the row is mandatory, not cosmetic.

## Verification

Per-loop pattern continues from Step 3o:

1. Unit pins (new + updated):
   - `TestPgIndex2678And2679AreDistinctWithCorrectFlags` (new) in
     `internal/initdb/pg_index_indexrelid_indrelid_test.go` — both
     entries exist; 2678 is `IndKey={1}, IsUnique=true, IsPrimary=true`;
     2679 is `IndKey={2}, IsUnique=true, IsPrimary=false`.
   - `TestNailedLocalRelsContainsPgIndexIndexrelidIndex` (new) — 2678
     present with `RelKind='i'` and `RelNatts=1`.
   - `TestPgIndexInitialEntriesIndkeyMatchesPG18` (updated) — pin
     extended with 2678:{1} and 2679 corrected to {2}; count rises 23→24.
   - `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree` (Step 3p
     pin, updated) — `mustHave` slice extended with 2678.

2. `go test -count=1 ./internal/initdb/` — the same 14 pre-existing
   baseline failures (M0106-0012 + bootstrap readability + migration /
   recovery suites) as Step 3o reproduce; no new regressions.

3. Cross-package smoke `go test -count=1 ./internal/executor/
   ./internal/server/ ./internal/storage/ ./internal/catalog/
   ./internal/mvcc/` — PASS.

4. `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -count=1 -timeout 120s -run
   'TestE2E_FailoverGoopgToPG/async' ./internal/testport/` — the
   "cache lookup failed for index 2671" FATAL is gone. The next FATAL
   surfaces as `column is not in index` from
   `RelationInitIndexAccessInfo` after the SHARED critical-index pass
   completes — a different indkey-vs-pg_attribute mismatch than Step 3n
   fixed, in Step 3r territory.

## Files

- `internal/initdb/initdb.go` — `pgIndexInitialEntries` split row +
  three placeholder list updates.
- `internal/initdb/relcache_init.go` — `nailedLocalRels` 2678 entry.
- `internal/initdb/pg_index_indkey_test.go` — pinned map updated for
  the split.
- `internal/initdb/btree_index_bootstrap_test.go` — `mustHave` slice
  extended.
- `internal/initdb/pg_index_indexrelid_indrelid_test.go` — new file,
  two pins.

## Next blocker (Step 3r preview)

E2E now FATALs with `column is not in index` after the SHARED
critical-index pass. `nocachegetattr` log preamble shows the failure
fires for a 34-attribute relation (pg_class) at attnum 32 and for a
21-attribute relation (pg_index) at attnum 16-18 (the trailing
indkey/indcollation/indclass/indoption vector columns). The next
investigation should compare an indkey in `pgIndexInitialEntries`
against PG18's authoritative `postgres/src/include/catalog/indexing.h`
for any index whose key column lives beyond attnum 15 — likely a
`pg_*_oid_index` whose stored `indkey[0]` value disagrees with the
column ordering in `pg_*_*Attrs()`.
