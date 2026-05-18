# M0106-0010 Step 3de — pg_authid heap row `rolname` byte-layout verification

## Context

Step 3dd (LD_PRELOAD `SIGSEGV` shim) captured the standby's first
client-backend crash on the goopg→PG E2E failover path:

```
btnamecmp+0x52 → namecmp → __strncmp_avx2
  ← _bt_compare ← _bt_first ← btgettuple
  ← systable_getnext ← SearchCatCache(AUTHNAME, …)
  ← get_role_oid("ryo") ← hba_getauthmethod ← ClientAuthentication
```

The working hypothesis recorded with Step 3dd was: *“an `AUTHNAME`
SysCache lookup walks `pg_authid_rolname_index` and the comparator
dereferences a heap tuple with a corrupt/NULL `rolname` pointer.”*

Step 3de’s mandate (from `.ralph/fix_plan.md`) is:

> Seed `pg_authid_rolname_index` (OID 2676) as a populated 2-page
> btree over the existing bootstrap superuser (`postgres`) and the
> test role (`ryo`), and confirm the underlying `pg_authid` heap rows
> carry valid `Form_pg_authid::rolname` payloads.

## Seed already in place

The index seed itself was landed in Step 3cx (commit `06ab6bc`):

- `internal/initdb/btree_index_bootstrap.go::pgBuildIndexTupleNameKey`
  emits a 72-byte single-NAME-column IndexTuple (8-byte header +
  NAMEDATALEN=64 zero-padded NameData, already MAXALIGN’d).
- `internal/initdb/btree_index_bootstrap.go::bootstrapPgAuthidIndexes`
  writes a 16384-byte (metapage + leaf-root) populated btree for both
  pg_authid_oid_index (OID 2677) and pg_authid_rolname_index (OID 2676)
  to `global/<oid>`.
- `internal/initdb/initdb.go::Init` invokes
  `bootstrapPgAuthidIndexes(abs, pgAuthidEntries)` after
  `bootstrapPostgresDatabase` so the index seed overwrites the
  8192-byte placeholder.

Inspection of a freshly initialised data directory confirms both
indexes land at 16384 bytes with `btm_root=1`, `btm_level=0`, and
leaf entries keyed on `"postgres"` (OID 10) and `"ryo"` (OID 16384).
Inspection of `global/1260` confirms two heap tuples with
`t_hoff=24`, `natts=12`, `infomask=0x0000`, rolname at payload offset
4..67 (the 64-byte NameData immediately following the 4-byte oid
column), and zero-padding to NAMEDATALEN.

## What Step 3de actually does

Step 3de records the byte-layout invariants of the pg_authid heap
rows as a regression so future encoder changes (most notably any
relapse that re-routes the `name` type through goopg’s
`encodeValue → encodeVarlen` path) are caught at unit-test time
instead of surfacing as a libc `__strncmp_avx2` SEGV three frames
removed from the buggy encoder.

`internal/initdb/pg_authid_heap_row_test.go` adds
`TestBootstrapPostgresRoleHeapRowRolnameByteLayout`, which:

1. Calls `bootstrapPostgresRole(tmp)` with `USER=ryo`.
2. Reads `global/1260` from the resulting tempdir.
3. Asserts the heap page contains exactly two items.
4. For each item, validates the HeapTupleHeader byte layout:
   `t_hoff == 24`, `Natts_pg_authid == 12`, `HEAP_HASNULL == 0`.
5. Validates the payload layout: oid at offset 0..3, then the
   64-byte rolname NameData. The cstring portion (bytes preceding
   the first NUL) must equal the seeded role name byte-for-byte,
   and the trailing `64 - len(name)` bytes must be zero-padded.

The `TestBootstrapPgAuthidIndexesWritesPopulatedBtrees` regression
already covers the index-leaf side (16384-byte file, btm_root=1,
leaf entries keyed on `"postgres"` and `"ryo"` as byte-exact
NameData). Together the two tests pin every byte
`_bt_compare → btnamecmp → namecmp → strncmp(64)` reads on either
side of the comparison.

## Implications for the open SEGV

With both the index leaf and the heap row byte-layouts verified
correct, Step 3dd’s working hypothesis is **falsified**: the crash
is not a corrupt rolname payload. The remaining suspects are:

- The scan-key Datum that `get_role_oid` constructs (the user
  cstring passed in via `MyProcPort->user_name`).
- The `_bt_compare`-side page-buffer mapping or line-pointer
  decoding (PG reading the populated 2-page btree we shipped via
  pg_basebackup).
- A mismatch between PG’s expected attlen/typalign for NAMEOID
  and what our pg_attribute rows declare — `attcollation=0` for
  rolname (we hard-code zero in `pgAttributeRow`) where PG normally
  records `C_COLLATION_OID=950`. The collation tag is not consulted
  by `btnamecmp` but it does influence `_bt_first`’s scan-key
  setup.

Resolving which pointer (`arg1` = leaf NameData vs `arg2` = scan-key
Name) is the unmapped dereference requires capturing `si_addr` and
saved registers (`RDI`, `RSI`, `RIP`, `RDX`) from the
`ucontext_t` the kernel hands `sa_sigaction`. That is the named
work for Step 3df (Section “Next blocker”).

## Verified

```
go test -count=1 -run 'TestBootstrapPostgresRoleHeapRowRolnameByteLayout|TestBootstrapPgAuthidIndexes' ./internal/initdb/ → PASS
```

Pre-existing baseline failures in `internal/initdb/` and
`internal/wal/` unchanged.

## Files

- `internal/initdb/pg_authid_heap_row_test.go` (new)
- `docs/design/0106-0010-step3de-pg-authid-heap-rolname-byte-layout.md` (this)
- `docs/design/README.md` (index update)
