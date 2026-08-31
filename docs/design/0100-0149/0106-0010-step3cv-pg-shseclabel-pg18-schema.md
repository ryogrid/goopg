# M0106-0010 Step 3cv — pg_shseclabel PG18 schema alignment

## Context

After Step 3cu seeded `pg_db_role_setting` (OID 2964) as a nailed
shared relation, the next E2E failure surfaced as a persistent
`invalid attalign value:` FATAL on every PG-standby user backend.
The empty `%c` in the message indicated `Form_pg_attribute.attalign`
was the NUL byte (0x00).

A diagnostic LOG with backtrace, added temporarily to PG's
`populate_compact_attribute_internal` (`tupdesc.c:105`) and reverted
after investigation per AGENT.md, traced the call to:

```
populate_compact_attribute
  ← load_relcache_init_file(shared=true)
    ← RelationCacheInitializePhase2
      ← InitPostgres
```

A second diagnostic logging each `Form_pg_attribute` slot as it was
deserialised from `global/pg_internal.init` produced the smoking gun
for OID 3592 (`pg_shseclabel`):

```
relno=19 rel_oid=3592 relnatts=6
  attr[0] attrelid=3592 attname=objoid   atttypid=26 attlen=4    OK
  attr[1] attrelid=3592 attname=classoid atttypid=26 attlen=4    OK
  attr[2] attrelid=3592 attname=provider atttypid=25 attlen=-1   OK
  attr[3] attrelid=3592 attname=label    atttypid=25 attlen=-1   OK
  attr[4] attrelid=126  attname=         atttypid=1  attlen=488  attalign=0x00  ← garbage
```

## Root cause

`postgres/src/include/catalog/pg_shseclabel.h` declares exactly four
columns: `objoid` (oid), `classoid` (oid), `provider` (text), `label`
(text). PG18's baked-in `Schema_pg_shseclabel` and
`Natts_pg_shseclabel = 4` are the source of truth used by
`formrdesc("pg_shseclabel", Natts_pg_shseclabel, Desc_pg_shseclabel)`
in `RelationCacheInitializePhase2`.

goopg had been declaring six columns for the same OID (`oid`,
`classoid`, `objoid`, `objsubid`, `provider`, `label`) and
`relnatts = 6` in `nailedSharedRels`. Column order *and* count
diverged from PG18 — `oid` and `objsubid` are not real columns of
this catalog at all.

Sequence that produces the FATAL:

1. PG standby boots. `RelationCacheInitFileRemove()` at WAL recovery
   start wipes `global/pg_internal.init` and `base/*/pg_internal.init`
   (xlog.c:5633), so the first backend after recovery finds them
   absent.
2. `RelationCacheInitializePhase2` → `load_relcache_init_file(true)`
   returns false → fallback `formrdesc("pg_shseclabel", 4,
   Desc_pg_shseclabel)` allocates a *4-element* `rd_att` array.
3. `RelationCacheInitializePhase3` later overrides `rd_rel` with the
   on-disk `Form_pg_class` row goopg wrote, which stores `relnatts=6`
   (the wrong nailedSharedRels declaration).
4. End of Phase3 calls `write_relcache_init_file(true)`. The writer
   iterates `rel->rd_rel->relnatts == 6` slots and reads
   `rel->rd_att->attrs[i]` for `i ∈ [0,5]`, but the array only has
   four entries. Slots 4 and 5 are uninitialised heap memory and get
   serialised verbatim into `global/pg_internal.init`.
5. Every subsequent backend's
   `RelationCacheInitializePhase2 → load_relcache_init_file(true)`
   parses the garbage slot, hits
   `populate_compact_attribute_internal`, and FATALs because
   `attalign` is 0x00.

The garbage observed (`attrelid=126`, `attlen=488=sizeofRelationData`,
`attstorage=0xa0`) is the standard fingerprint of an OOB read into
the allocator's prior payload bytes.

## Fix

`internal/initdb/relcache_init.go`:

1. `pgShseclabelAttrs()` rewritten to the PG18 4-column schema in
   exactly the order PG18's `Schema_pg_shseclabel` ships:
   - `objoid` (oid, attnum 1)
   - `classoid` (oid, attnum 2)
   - `provider` (text, attnum 3)
   - `label` (text, attnum 4)
   The previous order placed `oid` at attnum 1 and `objoid` at
   attnum 3; both `oid` and `objsubid` are deleted entirely.

2. `nailedSharedRels` entry for OID 3592 changes
   `RelNatts: 6 → 4` so that the on-disk `Form_pg_class.relnatts` PG
   reads in Phase3 matches the 4-slot `rd_att` array `formrdesc`
   allocated in Phase2 — closing the OOB write at
   `write_relcache_init_file`.

The companion index entry — `entry(3593, 3592, []int16{1, 2, 3},
[]uint32{oidOps, oidOps, textOps}, []uint32{0, 0, cCollation}, true,
true)` for `pg_shseclabel_object_index` in
`internal/initdb/initdb.go` — was already keyed on attnums `{1, 2, 3}`
with a comment naming them `objoid, classoid, provider`. After the
attr renumbering, those attnums now resolve to the columns the
comment describes (they had previously been pointing to `oid`,
`classoid`, `objoid` — silently wrong).

## Regression pins

`internal/initdb/pg_shseclabel_pg18_schema_test.go`:

- `TestPgShseclabelAttrsMatchesPG18FormPgShseclabel` — strict fixture
  asserting the 4-column PG18 schema by name / TypeOID / Num / Len /
  NotNull. Future divergence (column reordered, extra column added,
  etc.) trips this test before the E2E FATAL has a chance to surface.
- `TestNailedSharedRelsPgShseclabelRelnattsIsFour` — guards the
  load-bearing `RelNatts == 4` declaration that closes the
  `write_relcache_init_file` OOB read. Also pins
  `OID == SharedSecLabelRelationId (3592)` and
  `RelType == SharedSecLabelRelation_Rowtype_Id (4066)`.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run 'TestPgShseclabel|TestNailedSharedRelsPgShseclabel'
   ./internal/initdb/` PASS (2/2 new tests).
- `go test -count=1 ./internal/initdb/` — same 15 pre-existing
  baseline failures as Step 3cu (no new regressions).
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
  confirms the `invalid attalign value:` FATAL is gone.

## Next blocker

The next FATAL is now `missing support function 1 for attribute 1 of
index "pg_authid_rolname_index"`. That points to a missing
`pg_amproc` row for `name_ops` family — `pg_authid_rolname_index`
keys on `rolname` (NAME), and PG needs a `btnamecmp`-style support
proc registered for `name_ops`. Tracked as Step 3cw.

## References

- `postgres/src/include/catalog/pg_shseclabel.h` — pg_shseclabel
  catalog definition (4 columns).
- `postgres/src/include/catalog/schemapg.h` — `Schema_pg_shseclabel`
  baked-in TupleDesc.
- `postgres/src/backend/utils/cache/relcache.c` —
  `load_relcache_init_file` / `write_relcache_init_file` /
  `RelationCacheInitializePhase2` / `RelationCacheInitializePhase3`.
- `postgres/src/backend/access/common/tupdesc.c:105` —
  `populate_compact_attribute_internal` "invalid attalign value:"
  guard.
- Earlier nailed-shared-rel pattern: Step 3ch (pg_tablespace),
  Step 3cu (pg_db_role_setting).
