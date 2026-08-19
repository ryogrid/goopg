# PostgreSQL Oracle Compatibility Report (M0060)

Generated at: 2026-08-19T19:37:16+09:00

Single authority: `docs/test-port/postgres-oracle-target-inventory.csv`.
## Inventory Snapshot

| suite_id | kind | discovered_cases |
| -------- | ---- | ---------------: |
| client-tools-tap | tap | 94 |
| contrib-suites | mixed | 63 |
| isolation-specs | isolation | 121 |
| modules-suites | mixed | 47 |
| recovery-tap | tap | 51 |
| regress-expected | regress | 265 |
| regress-sql | regress | 232 |
| subscription-tap | tap | 40 |

## Status Summary

| suite_id | pass | failed | not-tried | excluded | port | defer |
| -------- | ---: | -----: | --------: | -------: | ---: | ----: |
| client-tools-tap | 0 | 3 | 47 | 3 | 39 | 2 |
| contrib-suites | 0 | 0 | 0 | 0 | 0 | 63 |
| isolation-specs | 119 | 1 | 0 | 0 | 0 | 1 |
| modules-suites | 0 | 0 | 0 | 1 | 0 | 46 |
| recovery-tap | 0 | 43 | 0 | 0 | 8 | 0 |
| regress-expected | 1 | 0 | 262 | 2 | 0 | 0 |
| regress-sql | 42 | 86 | 102 | 2 | 0 | 0 |
| subscription-tap | 0 | 33 | 0 | 0 | 7 | 0 |

## Deferred Blockers

| id | item_path | deferred_to | rationale |
|----|-----------|-------------|-----------|
|  | `postgres/src/test/isolation/specs/prepared-transactions.spec` | `-` | M-NIGHTLY (AI-20260719-094219-001) demoted pass->defer (strict->runIsoSpec). SSI correctness intact -- every permutation's query results/aborts still match PG 18.3 (PrepareCheckForSerializationFailure at PREPARE TRANSACTION time; a PREPARED-but-not-committed peer treated like a committed-first one). Demoted because this uniquely long spec (1500 permutations, ~60s) is the most exposed to the runner's timing-only blocking heuristic (framework/isolation_runner.go blockDetectWait=300ms): goopg has a real intermittent ~300ms server-side 2PC-commit stall on WSL2 (WAL 16MiB segment zero-fill / 2PC state-file I/O) that hits a random PREPARE/COMMIT PREPARED step once per run (3/3 standalone on a quiet host, at a moving permutation), so a non-blocking step is mislabeled <waiting ...>/<... completed>, shifting output by two lines. Re-promote once pg_isolation_test_session_is_blocked (pg_proc OID 3378, registered-but-unimplemented) is implemented and the runner polls it to confirm genuine lock-blocking before annotating slow steps (upstream isolationtester.c behavior). |
| D-005l | `postgres/src/bin/scripts/t/200_connstr.pl` | `M0060-0003` | Requires CREATE DATABASE (D-005d) and LATIN1 server encoding; goopg currently UTF8-only. |
|  | `postgres/src/test/modules/Makefile` | `M0060-0005` |  |
|  | `postgres/src/test/modules/README` | `M0060-0005` |  |
|  | `postgres/src/test/modules/brin` | `M0060-0005` |  |
|  | `postgres/src/test/modules/commit_ts` | `M0060-0005` |  |
|  | `postgres/src/test/modules/delay_execution` | `M0060-0005` |  |
|  | `postgres/src/test/modules/dummy_index_am` | `M0060-0005` |  |
|  | `postgres/src/test/modules/dummy_seclabel` | `M0060-0005` |  |
|  | `postgres/src/test/modules/gin` | `M0060-0005` |  |
|  | `postgres/src/test/modules/injection_points` | `M0060-0005` |  |
|  | `postgres/src/test/modules/ldap_password_func` | `M0060-0005` |  |
|  | `postgres/src/test/modules/libpq_pipeline` | `M0060-0005` |  |
|  | `postgres/src/test/modules/meson.build` | `M0060-0005` |  |
|  | `postgres/src/test/modules/oauth_validator` | `M0060-0005` |  |
|  | `postgres/src/test/modules/plsample` | `M0060-0005` |  |
|  | `postgres/src/test/modules/spgist_name_ops` | `M0060-0005` |  |
|  | `postgres/src/test/modules/ssl_passphrase_callback` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_aio` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_bloomfilter` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_cloexec` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_copy_callbacks` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_custom_rmgrs` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_ddl_deparse` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_dsa` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_dsm_registry` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_escape` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_extensions` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_ginpostinglist` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_integerset` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_json_parser` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_lfind` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_misc` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_oat_hooks` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_parser` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_pg_dump` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_predtest` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_radixtree` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_rbtree` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_regex` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_resowner` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_rls_hooks` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_shm_mq` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_slru` | `M0060-0005` |  |
|  | `postgres/src/test/modules/test_tidstore` | `M0060-0005` |  |
|  | `postgres/src/test/modules/typcache` | `M0060-0005` |  |
|  | `postgres/src/test/modules/worker_spi` | `M0060-0005` |  |
|  | `postgres/src/test/modules/xid_wraparound` | `M0060-0005` |  |
|  | `postgres/contrib/Makefile` | `M0060-0005` |  |
|  | `postgres/contrib/README` | `M0060-0005` |  |
|  | `postgres/contrib/amcheck` | `M0060-0005` |  |
|  | `postgres/contrib/auth_delay` | `M0060-0005` |  |
|  | `postgres/contrib/auto_explain` | `M0060-0005` |  |
|  | `postgres/contrib/basebackup_to_shell` | `M0060-0005` |  |
|  | `postgres/contrib/basic_archive` | `M0060-0005` |  |
|  | `postgres/contrib/bloom` | `M0060-0005` |  |
|  | `postgres/contrib/bool_plperl` | `M0060-0005` |  |
|  | `postgres/contrib/btree_gin` | `M0060-0005` |  |
|  | `postgres/contrib/btree_gist` | `M0060-0005` |  |
|  | `postgres/contrib/citext` | `M0060-0005` |  |
|  | `postgres/contrib/contrib-global.mk` | `M0060-0005` |  |
|  | `postgres/contrib/cube` | `M0060-0005` |  |
|  | `postgres/contrib/dblink` | `M0060-0005` |  |
|  | `postgres/contrib/dict_int` | `M0060-0005` |  |
|  | `postgres/contrib/dict_xsyn` | `M0060-0005` |  |
|  | `postgres/contrib/earthdistance` | `M0060-0005` |  |
|  | `postgres/contrib/file_fdw` | `M0060-0005` |  |
|  | `postgres/contrib/fuzzystrmatch` | `M0060-0005` |  |
|  | `postgres/contrib/hstore` | `M0060-0005` |  |
|  | `postgres/contrib/hstore_plperl` | `M0060-0005` |  |
|  | `postgres/contrib/hstore_plpython` | `M0060-0005` |  |
|  | `postgres/contrib/intagg` | `M0060-0005` |  |
|  | `postgres/contrib/intarray` | `M0060-0005` |  |
|  | `postgres/contrib/isn` | `M0060-0005` |  |
|  | `postgres/contrib/jsonb_plperl` | `M0060-0005` |  |
|  | `postgres/contrib/jsonb_plpython` | `M0060-0005` |  |
|  | `postgres/contrib/lo` | `M0060-0005` |  |
|  | `postgres/contrib/ltree` | `M0060-0005` |  |
|  | `postgres/contrib/ltree_plpython` | `M0060-0005` |  |
|  | `postgres/contrib/meson.build` | `M0060-0005` |  |
|  | `postgres/contrib/oid2name` | `M0060-0005` |  |
|  | `postgres/contrib/pageinspect` | `M0060-0005` |  |
|  | `postgres/contrib/passwordcheck` | `M0060-0005` |  |
|  | `postgres/contrib/pg_buffercache` | `M0060-0005` |  |
|  | `postgres/contrib/pg_freespacemap` | `M0060-0005` |  |
|  | `postgres/contrib/pg_logicalinspect` | `M0060-0005` |  |
|  | `postgres/contrib/pg_overexplain` | `M0060-0005` |  |
|  | `postgres/contrib/pg_prewarm` | `M0060-0005` |  |
|  | `postgres/contrib/pg_stat_statements` | `M0060-0005` |  |
|  | `postgres/contrib/pg_surgery` | `M0060-0005` |  |
|  | `postgres/contrib/pg_trgm` | `M0060-0005` |  |
|  | `postgres/contrib/pg_visibility` | `M0060-0005` |  |
|  | `postgres/contrib/pg_walinspect` | `M0060-0005` |  |
|  | `postgres/contrib/pgcrypto` | `M0060-0005` |  |
|  | `postgres/contrib/pgrowlocks` | `M0060-0005` |  |
|  | `postgres/contrib/pgstattuple` | `M0060-0005` |  |
|  | `postgres/contrib/postgres_fdw` | `M0060-0005` |  |
|  | `postgres/contrib/seg` | `M0060-0005` |  |
|  | `postgres/contrib/sepgsql` | `M0060-0005` |  |
|  | `postgres/contrib/spi` | `M0060-0005` |  |
|  | `postgres/contrib/sslinfo` | `M0060-0005` |  |
|  | `postgres/contrib/start-scripts` | `M0060-0005` |  |
|  | `postgres/contrib/tablefunc` | `M0060-0005` |  |
|  | `postgres/contrib/tcn` | `M0060-0005` |  |
|  | `postgres/contrib/test_decoding` | `M0060-0005` |  |
|  | `postgres/contrib/tsm_system_rows` | `M0060-0005` |  |
|  | `postgres/contrib/tsm_system_time` | `M0060-0005` |  |
|  | `postgres/contrib/unaccent` | `M0060-0005` |  |
|  | `postgres/contrib/uuid-ossp` | `M0060-0005` |  |
|  | `postgres/contrib/vacuumlo` | `M0060-0005` |  |
|  | `postgres/contrib/xml2` | `M0060-0005` |  |
| W-001 | `postgres/src/bin/pg_waldump` | `wal-native-pg-format content rewrite (docs 01/03)` | DEFERRED by canonical-removal (docs/design/wal-native-pg-format/04): goopg now emits real PG (xl_rmid/xl_info) headers over still-native record BODIES — so pg_waldump attempts to decode a goopg body as a PG record and errors — structural parity is intentionally broken until the native->PG content rewrite (docs 01/03). TestPort_WALPgWaldumpCompat (internal/testport/wal_pg_waldump_test.go) now unconditionally t.Skip. Frame parity (page headers 0xD118 / xl_prev 0-based) is unchanged. |
