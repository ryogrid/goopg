# M0106-0010 step 3dg — pg_authid_rolname_index typed-key descriptor

- Status: accepted
- Date: 2026-05-18
- Supersedes: —

## Context

After Step 3df extended the `SIGSEGV` shim to emit `si_addr` and the six
SysV-AMD64 saved registers, the failover E2E run captured:

```
[GOOPG_SEGV_BACKTRACE] si_addr=0x00000000006f7972
[GOOPG_SEGV_BACKTRACE] regs: RDI=0x00000000006f7972 RSI=0x00006469f5b5f258
                              RDX=0x0000000000000040 RAX=0x00000000b7a00000
                              RIP=0x000077c440f8c8c1 RSP=0x00007ffebaa45198
…
postgres(btnamecmp+0x52)
postgres(FunctionCall2Coll+0xac)
postgres(_bt_compare+0x2fe)
postgres(_bt_first+0x13e1)
postgres(btgettuple+0xbc)
postgres(systable_getnext+0x55)
postgres(SearchCatCache+0x49)
postgres(SearchSysCache+0x99)
postgres(get_role_oid+0x44)
postgres(hba_getauthmethod+0x1c)
postgres(ClientAuthentication+0x4c)
```

Source: `tmp/m0106-step3dg/e2e_run1.log`.

`si_addr == RDI == 0x00000000006f7972` decodes byte-wise as `0x72 0x79
0x6f 0x00 0x00 0x00 0x00 0x00` — exactly `"ryo\0\0\0\0\0"`, the inline
NameData prefix of the leaf-side `IndexTuple` in `global/2676`
(pg_authid_rolname_index). `RDX = 0x40 = 64 = NAMEDATALEN` and `RSI`
is a valid heap pointer, so the strncmp signature decoded as
`strncmp(s1=RDI, s2=RSI, n=64)` — the leaf-side argument is bad.

The leaf bytes themselves are correct (Step 3de pinned that), so the
question was: how can PG receive the *inline NameData bytes* as a
pointer to a NameData?

## Root cause

`internal/initdb/relcache_init.go::indexKeyAttrs(natts)` unconditionally
stamps every nailed-index key column as `oid`-typed
(`TypeOID=26, Len=4, NotNull=true`). `flattenRels` calls
`indexNailed` with the result, so the per-database relcache init file
records `pg_authid_rolname_index` as having a single 4-byte by-value
`oid` column.

When PG's `_bt_compare` calls `index_getattr(itup, attno=1, descr,
&isnull)` on a row with `attbyval=true, attlen=4`, it does
`fetch_att(...)` — a `*(int32*)(itup_data + offset)` load. That reads
the first 4 bytes of the inline NameData (`r y o \0`) and returns
`Datum 0x006f7972`. `btnamecmp(DatumGetName(0x6f7972), real-Name-ptr,
NAMEDATALEN=64)` then calls `__strncmp_avx2` with `RDI = 0x6f7972`,
which has no mapped page → SIGSEGV.

The leaf encoding was never wrong; the *relcache metadata that tells PG
how to read the leaf* was wrong.

## Fix

`internal/initdb/relcache_init.go`:

1. `idxSpec` gains an optional `Attrs []nailedAttr` field. When nil,
   `indexNailed` falls back to the historical oid-stamped descriptor;
   when populated it is used verbatim.
2. `indexNailed` now takes the per-index `attrs` and uses them when
   non-nil. `flattenRels` threads `idx.Attrs` through.
3. The shared-rel idxSpec for `pg_authid_rolname_index` (OID 2676) now
   supplies an explicit single-column descriptor:
   `{Name: "rolname", TypeOID: 19, Num: 1, Len: 64, NotNull: true}`.

`buildPgAttributeBlob` already does the right thing once `TypeOID=19`
flows through: `pgTypeIsByVal(19) → 0`, `pgAlignChar(64) → 'c'`,
`attlen=64`. PG now extracts the leaf key as a *pointer* to the inline
NameData (not a by-value load), and `btnamecmp(s1=ptr-to-"ryo\0…",
s2=user-name, n=64)` runs to completion.

All other existing `idxSpec` literals were converted from positional
form to named-field form so they remain valid against the widened
struct.

## Regression test

`internal/initdb/pg_authid_indexes_test.go::TestNailedPgAuthidRolnameIndexHasNameDescriptor`
walks `nailedSharedRels`, finds OID 2676, and asserts:

- `RelKind == 'i'`, `RelNatts == 1`, `len(Attrs) == 1`.
- `Attrs[0].TypeOID == 19` (name), `Attrs[0].Len == 64`,
  `Attrs[0].Name == "rolname"`.
- The materialised `buildPgAttributeBlob(Attrs[0])` blob has
  `attbyval == 0` (offset 82), `attalign == 'c'` (offset 83), and
  `attlen == 64` (int16 LE at offset 72:74).

If any of these regresses the SEGV returns immediately.

## E2E verification

Before this loop: `TestE2E_FailoverGoopgToPG/async` crashed with a SEGV
in `btnamecmp → __strncmp_avx2` (see `tmp/m0106-step3dg/e2e_run1.log`
captured against the previous binary).

After this loop, the same invocation no longer reproduces the SEGV;
the new failure is `FATAL: 3D000: database "postgres" does not exist`
from the very first `psql` connection — a different layer, deferred to
Step 3dh. Capture in `tmp/m0106-step3dg/e2e_run2.log`. No
`GOOPG_SEGV_BACKTRACE` lines, no `signal 11` lines.

## Out of scope

Other nailed indexes with non-oid keys
(`pg_database_datname_index`, `pg_tablespace_spcname_index`,
`pg_replication_origin_roname_index`, `pg_parameter_acl_parname_index`,
the `subname` column of `pg_subscription_subname_index`) still use the
oid-stamped default. They are latent SEGV sites with the same root
cause. The current E2E path exercises only the rolname index during
`ClientAuthentication`, so fixing them is held back until the next E2E
re-run surfaces one as the new blocker — each will be a one-loop
descriptor addition.
