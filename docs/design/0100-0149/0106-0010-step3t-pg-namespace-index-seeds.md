# M0106-0010 Step 3t — pg_namespace nspname/oid index seeds

## Status
Accepted (2026-05-18).

## Context
After Step 3s fixed the BlockIdData encoding in index tuples,
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
advanced past the `previous segment is only 6 blocks` FATAL to a new
blocker:

```
FATAL: could not open relation with OID 2684
```

The fix-plan hypothesis labelled OID 2684 as
`pg_amop_fam_strat_index`, but the authoritative PG18 sources tell a
different story (`postgres/src/include/catalog/pg_namespace.h:56-57`
and `pg_namespace_d.h:24-25`):

```c
DECLARE_UNIQUE_INDEX(pg_namespace_nspname_index, 2684,
    NamespaceNameIndexId, pg_namespace,
    btree(nspname name_ops));
DECLARE_UNIQUE_INDEX_PKEY(pg_namespace_oid_index, 2685,
    NamespaceOidIndexId, pg_namespace,
    btree(oid oid_ops));
```

`pg_amop_fam_strat_index` is OID **2653** in PG18, not 2684.

Both `pg_namespace_nspname_index` (OID 2684) and `pg_namespace_oid_index`
(OID 2685) had empty-placeholder relfiles seeded by Step 3k (the three
empty-placeholder OID lists at `internal/initdb/initdb.go:592 / 674 / 689`
include both OIDs), but neither was registered in:

- `pgIndexInitialEntries()` — so no `Form_pg_index` heap row, no
  `pg_index` btree leaf tuple, and no entry in the per-index TID map.
- `nailedLocalRels` — so the relcache init file's TupleDesc carried no
  entry, and `RelationCacheInitializePhase3` never tried to load the
  index in the first place… until a `SearchSysCache1(NAMESPACEOID, …)`
  hit later in startup tried to dereference an OID-keyed
  `pg_namespace` lookup, which routed through
  `RelationIdGetRelation(2684)`.

The result: PG FATAL'd with `could not open relation with OID 2684`
because `RelationBuildDesc(2684)` ran a sysscan against
`pg_class_oid_index` (OID 2662) — which now correctly returns the
heap TID for nailed relations — but no pg_class row exists for OID
2684 because Step 3m's `bootstrapPgClassTuples` iterates
`nailedLocalRels`, and the two indexes are absent from that list.

## Decision

Seed both indexes into `pgIndexInitialEntries()` and
`nailedLocalRels`. No new builder, encoder, or `Init` flow change is
required — the populated btree at file 2679
(`bootstrapPgIndexIndexrelidIndex`) already walks every
`pgIndexInitialEntries` row, so adding entries here flows their TIDs
through the existing plumbing; `bootstrapPgClassTuples` already walks
every `nailedLocalRels` entry to emit pg_class rows, so adding the
two nailed-index labels flows their pg_class rows through too.

### Concrete changes

`internal/initdb/initdb.go::pgIndexInitialEntries` (after the
`pg_inherits_relid_seqno_index` entry):

```go
entry(2684, 2615, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_namespace_nspname_index
entry(2685, 2615, []int16{1}, []uint32{oidOps},  []uint32{0},          true, true),  // pg_namespace_oid_index
```

`internal/initdb/relcache_init.go::nailedLocalRels` (in the `idxSpec`
list, after `{2680, "pg_inherits_relid_seqno_index"}`):

```go
{2684, "pg_namespace_nspname_index"},
{2685, "pg_namespace_oid_index"},
```

`indkey` rationale (`postgres/src/include/catalog/pg_namespace.h`):
heap col 1 = `oid`, col 2 = `nspname` (name), col 3 = `nspowner`,
col 4 = `nspacl`. `pg_namespace_nspname_index` therefore keys on
attnum 2 with `name_ops` opclass and the `C` collation (OID 950,
canonical for catalog text-like columns). `pg_namespace_oid_index`
keys on attnum 1 with `oid_ops` (no collation).

Empty placeholder coverage for the two OIDs already exists at the
three relfile-list sites — no change needed.

## Regression pins

`internal/initdb/pg_namespace_index_test.go` (new):

- `TestPgNamespaceIndexesSeededFromInitialEntries` asserts each entry's
  `(IndRelid, IndKey, IsUnique, IsPrimary)` quadruple against the
  PG18-canonical values.
- `TestNailedLocalRelsContainsPgNamespaceIndexes` asserts both OIDs
  carry `RelKind='i'`, `RelNatts=1`, and the PG18 label.

`TestPgIndexInitialEntriesIndkeyMatchesPG18` extended with the two
new entries; its `len(got) != len(want)` guard forces future
additions to update the pinned map.

`TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
extended with 2684 and 2685 to keep the populated btree from
silently dropping their TIDs.

## Verification

- `go test -count=1 -run 'TestPgNamespaceIndexes|TestNailedLocalRelsContainsPgNamespaceIndexes|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 baseline failures
  as Step 3s; no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` —
  expected: `could not open relation with OID 2684` FATAL gone;
  Step 3u territory is whatever the next syscache lookup surfaces.

## Files touched

- `internal/initdb/initdb.go` — two new `entry(…)` calls.
- `internal/initdb/relcache_init.go` — two new `idxSpec` rows.
- `internal/initdb/pg_namespace_index_test.go` — new regression pins.
- `internal/initdb/pg_index_indkey_test.go` — extended pinned map.
- `internal/initdb/btree_index_bootstrap_test.go` — extended
  `mustHave` slice.
- `docs/design/0106-0010-step3t-pg-namespace-index-seeds.md` — this
  document.
- `docs/design/README.md` — index entry for this design doc.
