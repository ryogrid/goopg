# M0106-0010 Step 3da — pg_type I/O regproc OIDs populated

## Problem

After Step 3cz seeded a populated `pg_type_oid_index` (OID 2703), the
PG-standby's `TypeOIDtoLookupByCatcache(TYPEOID, 23)` returned a valid
tuple — but the very first `SELECT 1` probe still failed with:

```
ERROR:  42883: no output function available for type integer
LOCATION:  getTypeOutputInfo, lsyscache.c:3063
```

`getTypeOutputInfo` raises this when the SysCache-resolved Form_pg_type's
`typoutput` field is zero. The full `[m0102-pg-standby-log]` capture also
showed a derivative SIGSEGV every ~1.5s on a follow-up backend, leading
to the `HandleChildCrash` → `reinitialising` → loop that consumed the
test's 300s budget on every cycle.

## Root cause

`internal/initdb/pg_type_bootstrap.go::pgTypeRow` emitted **zero** for
`typinput` (col 16), `typoutput` (col 17), `typreceive` (col 18), and
`typsend` (col 19). The bootstrap deliberately left them at 0 when only
the typalign / typstorage / typlen / typbyval fixed-part fields were
needed for the earlier `populate_compact_attribute_internal` crash
(Steps 3cq–3cz). PG18's runtime path past the SysCache lookup reads
those four regproc fields, and a zero value is an immediate fail for
the I/O-function dispatch.

## Fix

`pgTypeEntry` gains four `uint32` fields: `Input`, `Output`, `Receive`,
`Send`. Every case in `pgTypeCanonical` fills them with the PG18
canonical regproc OID sourced from
`postgres/src/include/catalog/pg_proc.dat`:

| OID | typname | typinput | typoutput | typreceive | typsend |
|----:|---------|---------:|----------:|-----------:|--------:|
|  16 | bool    |     1242 |      1243 |       2436 |    2437 |
|  17 | bytea   |     1244 |        31 |       2412 |    2413 |
|  18 | char    |     1245 |        33 |       2434 |    2435 |
|  19 | name    |       34 |        35 |       2422 |    2423 |
|  20 | int8    |      460 |       461 |       2408 |    2409 |
|  21 | int2    |       38 |        39 |       2404 |    2405 |
|  22 | int2vector |    40 |        41 |       2410 |    2411 |
|  23 | int4    |       42 |    **43** |       2406 |    2407 |
|  24 | regproc |       44 |        45 |       2444 |    2445 |
|  25 | text    |       46 |        47 |       2414 |    2415 |
|  26 | oid     |     1798 |      1799 |       2418 |    2419 |
|  27 | tid     |       48 |        49 |       2438 |    2439 |
|  28 | xid     |       50 |        51 |       2440 |    2441 |
|  29 | cid     |       52 |        53 |       2442 |    2443 |
|  30 | oidvector |     54 |        55 |       2420 |    2421 |
| 194 | pg_node_tree |  195 |     196 |        197 |     198 |
| 269 | table_am_handler |267|     268 |          0 |       0 |
| 325 | index_am_handler |326|     327 |          0 |       0 |
| 700 | float4  |      200 |       201 |       2424 |    2425 |
| 701 | float8  |      214 |       215 |       2426 |    2427 |
| 1033 | aclitem |    1031 |      1032 |          0 |       0 |
| 1042 | bpchar  |    1044 |      1045 |       2430 |    2431 |
| 1043 | varchar |    1046 |      1047 |       2432 |    2433 |
| 1184 | timestamptz |1150 |      1151 |       2476 |    2477 |
| 2277 | anyarray |   2296 |      2297 |       2502 |    2503 |
| 2281 | internal |   2304 |      2305 |          0 |       0 |
| 3220 | pg_lsn   |   3229 |      3230 |       3238 |    3239 |
| 3361 | pg_ndistinct |3355|      3356 |       3357 |    3358 |
| 3402 | pg_dependencies |3404|   3405 |       3406 |    3407 |
| 5017 | pg_mcv_list |5018 |      5019 |       5020 |    5021 |

All array types (1002 `_char`, 1009 `_text`, 1021 `_float4`, 1028
`_oid`, 1034 `_aclitem`, 1185 `_timestamptz`, 10028 `_pg_statistic`)
share the generic `array_in/out/recv/send` quad (750 / 751 / 2400 /
2401). aclitem and the three pseudo types (table_am_handler,
index_am_handler, internal) intentionally carry 0 in typreceive/typsend
— they have no binary I/O in upstream PG18.

`pgTypeRow` emits the four regproc OIDs at columns 16–19 instead of
zero. The on-disk fixed-part layout is unchanged; only the four
previously-zero 4-byte slots at offsets 100/104/108/112 now carry the
canonical OIDs.

## Regression pin

`internal/initdb/pg_type_bootstrap_test.go::
TestPgTypeRowEmbedsCanonicalIORegprocOIDs` covers five cases (int4,
bool, text, oid, name) at two layers:

1. The `pgTypeEntry` returned by `pgTypeCanonical` carries the exact
   I/O OIDs from upstream `pg_type.dat`.
2. The PG-encoded row payload from `EncodeRowPG(pgTypeRow(e))` places
   those OIDs at FormData_pg_type byte offsets 100 / 104 / 108 / 112.

The mandatory `(23, 42, 43, 2406, 2407)` case is the exact precondition
whose absence triggered the FATAL.

## Verification

`go build ./...` — PASS.
Targeted: `go test -count=1 -run 'TestPgType|TestBootstrapPgType' ./internal/initdb/` — 7/7 PASS.
Cross-package smoke: `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — all PASS.
`go test -count=1 ./internal/initdb/` — same 15 pre-existing baseline failures as Step 3cz (no new regressions).

End-to-end:
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
captured via the Step 3cy `[m0102-pg-standby-log]` cleanup tag.

| metric | before Step 3da (Step 3cz log) | after Step 3da |
|--------|------------------------------:|---------------:|
| `cache lookup failed for type 23` lines | 0 | 0 |
| `no output function available for type integer` lines | 41 | **0** |
| `terminated by signal 11` lines | 56 | **1** |
| standby quiet running window | 0s | **8+ minutes** |

After the fix, the standby boots past the `SELECT 1` probe, the
derivative crash loop is broken, and the postmaster runs the
checkpointer idle for the rest of the test budget. The single
remaining SIGSEGV is a separate, non-looping crash on a different
follow-up backend path — it becomes Step 3db's scope.

## Next blocker (Step 3db)

The lone surviving SIGSEGV happens during `InitPostgres
(postinit.c:723)` on a follow-up backend (PID 1182375 in the run2
capture). No FATAL or ERROR precedes it; the backend was inside an
`aio_shared_buffer_readv_cb` cycle when it died. Working hypothesis:
the SysCache lookup chain now reaches a previously-unexercised
`pg_proc` row that is still missing or malformed — int4out (OID 43)
is the obvious next dependency. Step 3db should grep the standby's
`base/{1,5}/1255` (pg_proc) for OID 43 / int4out and apply the same
pattern (canonical heap row + populated `pg_proc_oid_index`) used by
Steps 3cw / 3cx / 3cz if it's missing.

## Files touched

- `internal/initdb/pg_type_bootstrap.go` — `pgTypeEntry` gains four
  regproc OID fields; every `pgTypeCanonical` case fills them;
  `pgTypeRow` emits them at columns 16–19.
- `internal/initdb/pg_type_bootstrap_test.go` —
  `TestPgTypeRowEmbedsCanonicalIORegprocOIDs` regression pin.
- `docs/design/0106-0010-step3da-pg-type-io-regproc-oids.md` (this
  file) + index update in `docs/design/README.md`.
