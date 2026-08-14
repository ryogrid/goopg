# PostgreSQL Oracle Compatibility Report (M0060)

Generated at: 2026-08-14T18:54:55+09:00

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
| client-tools-tap | 0 | 0 | 0 | 50 | 39 | 5 |
| contrib-suites | 0 | 0 | 0 | 0 | 0 | 63 |
| isolation-specs | 119 | 1 | 0 | 0 | 0 | 1 |
| modules-suites | 0 | 0 | 0 | 1 | 0 | 46 |
| recovery-tap | 0 | 0 | 0 | 0 | 8 | 43 |
| regress-expected | 1 | 0 | 161 | 103 | 0 | 0 |
| regress-sql | 41 | 87 | 0 | 104 | 0 | 0 |
| subscription-tap | 0 | 0 | 0 | 0 | 7 | 33 |

## Deferred Blockers

| id | item_path | deferred_to | rationale |
|----|-----------|-------------|-----------|
|  | `postgres/src/test/isolation/specs/prepared-transactions.spec` | `-` | M-NIGHTLY (AI-20260719-094219-001) demoted pass->defer (strict->runIsoSpec). SSI correctness intact -- every permutation's query results/aborts still match PG 18.3 (PrepareCheckForSerializationFailure at PREPARE TRANSACTION time; a PREPARED-but-not-committed peer treated like a committed-first one). Demoted because this uniquely long spec (1500 permutations, ~60s) is the most exposed to the runner's timing-only blocking heuristic (framework/isolation_runner.go blockDetectWait=300ms): goopg has a real intermittent ~300ms server-side 2PC-commit stall on WSL2 (WAL 16MiB segment zero-fill / 2PC state-file I/O) that hits a random PREPARE/COMMIT PREPARED step once per run (3/3 standalone on a quiet host, at a moving permutation), so a non-blocking step is mislabeled <waiting ...>/<... completed>, shifting output by two lines. Re-promote once pg_isolation_test_session_is_blocked (pg_proc OID 3378, registered-but-unimplemented) is implemented and the runner polls it to confirm genuine lock-blocking before annotating slow steps (upstream isolationtester.c behavior). |
|  | `postgres/src/test/recovery/t/002_archiving.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/003_recovery_targets.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/004_timeline_switch.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/005_replay_delay.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/006_logical_decoding.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/007_sync_rep.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/008_fsm_truncation.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/009_twophase.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/010_logical_decoding_timelines.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/012_subtransactions.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/014_unlogged_reinit.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/015_promotion_pages.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/016_min_consistency.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/017_shm.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/018_wal_optimize.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/020_archive_status.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/021_row_visibility.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/022_crash_temp_files.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/023_pitr_prepared_xact.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/024_archive_recovery.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/025_stuck_on_old_timeline.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/026_overwrite_contrecord.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/027_stream_regress.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/028_pitr_timelines.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/029_stats_restart.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/030_stats_cleanup_replica.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/031_recovery_conflict.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/032_relfilenode_reuse.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/033_replay_tsp_drops.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/034_create_database.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/035_standby_logical_decoding.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/036_truncated_dropped.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/037_invalid_database.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/040_standby_failover_slots_sync.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/041_checkpoint_at_promote.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/042_low_level_backup.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/043_no_contrecord_switch.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/044_invalidate_inactive_slots.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/045_archive_restartpoint.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/046_checkpoint_logical_slot.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/recovery/t/048_vacuum_horizon_floor.pl` | `M0094` | Requires replication/failover/recovery semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/002_types.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/003_constraints.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/005_encoding.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/006_rewrite.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/007_ddl.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/008_diff_schema.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/009_matviews.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/010_truncate.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/011_generated.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/012_collation.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/013_partition.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/014_binary.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/015_stream.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/016_stream_subxact.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/017_stream_ddl.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/018_stream_subxact_abort.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/019_stream_subxact_ddl_abort.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/020_messages.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/021_twophase.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/022_twophase_cascade.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/023_twophase_stream.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/024_add_drop_pub.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/025_rep_changes_for_schema.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/027_nosuperuser.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/028_row_filter.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/029_on_error.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/030_origin.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/031_column_list.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/032_subscribe_use_index.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/033_run_as_table_owner.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/034_temporal.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/035_conflicts.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
|  | `postgres/src/test/subscription/t/100_bugs.pl` | `M0094` | Requires logical replication/subscription semantics not yet fully implemented in goopg v0. |
| WD-003 | `postgres/src/bin/pg_waldump/t/002_save_fullpage.pl` | `perf-optimize3-dash resume (GOOPG_WAL_CANONICAL=on + perf-optimize3/05-improvement-designs/01 C1)` | DEFERRED by perf-optimize3-dash S4 (native-only WAL default; canonical emission off): assert-skips unless GOOPG_WAL_CANONICAL=on. Ported as TestPort_PgWaldump002SaveFullpage in internal/testport/pgwaldump_savefullpage_test.go (M0119-0005). Was blocked on two issues (xl_prev 1-based on-disk LSN + HOT updates never emitting a PG-canonical FPI); both fixed. Drives --save-fullpage --relation over a CHECKPOINT+UPDATE workload and asserts the upstream filename format + page-LSN ordering. |
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
| WD-004 | `postgres/src/bin/pg_waldump` | `perf-optimize3-dash resume (GOOPG_WAL_CANONICAL=on + perf-optimize3/05-improvement-designs/01 C1)` | DEFERRED by perf-optimize3-dash S4 (native-only WAL default; canonical emission off): assert-skips unless GOOPG_WAL_CANONICAL=on. Ported as TestPort_PgWaldumpVacuumPruneRoundtrip in internal/testport/pgwaldump_vacuum_prune_test.go (M0119-0005). Closes the live pg_waldump --rmgr=Heap2 round-trip gap left open by the prune/VACUUM canonical-WAL fix: DELETE+VACUUM a table and assert the upstream binary decodes the resulting XLOG_HEAP2_PRUNE_VACUUM_SCAN record for the correct relation locator with no structural error. |
| AC-003 | `postgres/src/bin/pg_amcheck/t` | `M0110-0003` | Deferred remainder of the pg_amcheck TAP suite: 003_check.pl 004_verify_heapam.pl 005_opclass_damage.pl. These run actual heap/btree corruption checks against a live server and need verify_heapam()/bt_index_check() with real on-disk corruption injection plus operator-class catalog parity (005). 002_nonesuch.pl is promoted under AC-002. PARTIAL: the page-structural heap tier of 004_verify_heapam.pl is now ported as TestPort_PgAmcheck004VerifyHeapam (internal/testport/pgamcheck004_port_test.go) — it inits a live goopg cluster with --no-data-checksums (mirroring upstream no_data_checksums=>1) runs CREATE EXTENSION amcheck inserts rows stops cleanly overwrites the first line pointer length on block 0 of the heap file (the upstream stop->seek/overwrite->restart mechanism) restarts and asserts pg_amcheck exits 2 and reports the upstream-verbatim line-pointer-ends-beyond-maximum-page-offset report. The MVCC/attribute and TOAST tiers of 004 corrupt PG on-disk varatt_external layout which goopg diverges from (chunk-relation TOAST) so they are not faithfully portable. Blocker #2 (bt_index_check schema-qualified dispatch) is now FIXED: pg_amcheck calls the amcheck builtins qualified by the install schema (public.bt_index_check) not pg_catalog; evalFuncCall now strips a user-schema qualifier for the amcheck scalar builtins so a scoped run checks a relation's dependent btree indexes — proven end-to-end by TestPort_PgAmcheckBtreeIndexCheck (a healthy indexed user table checks clean through the real binary) and unit-gated by TestBtIndexCheck_SchemaQualifiedDispatch. The whole-database relation-enumeration + per-relation dispatch tier (003_check.pl clean-db path) is now covered by TestPort_PgAmcheckAllTables (internal/testport/pgamcheck_alltables_port_test.go): a database mixing the goopg-supported relkinds 003_check builds (heap table several btree indexes incl. UNIQUE a sequence a view and a materialized view) checks clean through both a --schema-scoped run AND the unscoped whole-database run (exit 0 empty output). This empirically REFUTES the prior blocker #3 hypothesis (system-catalog heap resolution): the default whole-db run that would reach pg_catalog.* is itself clean because goopg never feeds its system catalogs to pg_amcheck heap-check dispatch — there is no verify_heapam-on-catalog gap to close. The index file-removal corruption tier of 003_check.pl is now ported as TestPort_PgAmcheck003MissingIndexFork (internal/testport/pgamcheck003_missingfork_test.go): it builds a goopg-supported heap+btree fixture stops cleanly removes the index main fork file on disk (the upstream plan_to_remove_relation_file stop->unlink->restart mechanism) restarts and asserts pg_amcheck exits 2 and reports the upstream-verbatim index-lacks-a-main-relation-fork message. goopg now mirrors bt_index_check_callback's smgrexists(MAIN_FORKNUM) guard (verify_nbtree.c:318) via a stat-only Pool.Exists check in evalBtIndexCheck run before NBlocks (whose O_CREATE open would otherwise silently recreate the removed fork as an empty clean index) — unit-gated by TestBtIndexCheck_DetectsMissingRelationFork. The heap-table file-removal corruption tier of 003_check.pl is now ported as TestPort_PgAmcheck003MissingHeapFile (internal/testport/pgamcheck003_missingheap_test.go): it builds a plain goopg heap table stops cleanly removes the heap main fork file on disk (the upstream plan_to_remove_relation_file on an ordinary table stop->unlink->restart mechanism) restarts and asserts pg_amcheck exits 2 and reports the upstream-verbatim could-not-open-file No-such-file-or-directory message. goopg now mirrors verify_heapam opening the relation's main fork (RelationGetNumberOfBlocks -> mdnblocks fails for a removed file) via a stat-only Pool.Exists check in verifyHeapamOp.Open run before NBlocks (whose O_CREATE open would otherwise silently recreate the removed fork as an empty clean heap) building the data-dir-relative relpath through Pool.RelPath and raising 58030 (ERRCODE_IO_ERROR what errcode_for_file_access yields for ENOENT) — unit-gated by TestVerifyHeapam_DetectsMissingRelationFile. The CENTRAL combined-corruption assertion of 003_check.pl (its main check :347-365) is now ported as TestPort_PgAmcheck003CombinedCorruption (internal/testport/pgamcheck003_combined_test.go): it injects THREE distinct corruption classes — a removed btree index fork a removed heap file and an overwritten heap line pointer — across three relations in a SINGLE stop->corrupt->restart cycle (mirroring perform_all_corruptions) then asserts one scoped pg_amcheck run reports ALL THREE upstream-verbatim regexes together (index-lacks-a-main-relation-fork + line-pointer + could-not-open-file) with exit 2 and empty stderr. This is the integration property no isolated surrogate proves: pg_amcheck's per-relation dispatch does not abort on the first corrupt relation (the removed-file case raises 58030) but continues enumerating and reports every distinct corruption class in one invocation. Scoped with one --table per relation in the public schema (its value is the multi-relation single-pass property orthogonal to schema scoping). The genuinely schema-scoped tier of 003_check.pl (upstream builds its fixture in user schemas s1/s2 and scopes runs with --schema) is now ported as TestPort_PgAmcheck003SchemaScoped (internal/testport/pgamcheck003_schemascoped_test.go): it creates s1.t003sc removes its heap main fork across a stop->corrupt->restart cycle and asserts a --schema s1 scoped pg_amcheck run reports the missing file (exit 2) — proving the user schema AND the table reloaded in it both survived the restart with the correct schema association end-to-end through the real binary's schema resolution. This closes the historical public-only workaround: CREATE SCHEMA durability landed via WAL replay (loop #20) and user-schema TABLE durability via the pg_class.relnamespace OID round-trip (loop #21 — namespaceOIDForSchema now stamps the real schema OID and loadUserTablesFromHeap/loadUserIndexesFromHeap reverse-map it via SchemaNameForOID after replaySchemaDDLRecords restores the registry). Remaining 003_check blockers are purely feature/corruption: the hash/gist/gin/brin/spgist index AMs goopg lacks the box/int4range/int4[] column types STORAGE EXTERNAL TOAST corruption multi-database orchestration and the page-overwrite mechanics for those unsupported relkinds. 005_opclass_damage.pl is now FULLY ported as TestPort_PgAmcheck005OpclassDamage (internal/testport/pgamcheck005_opclass_test.go): it builds upstream's fixture verbatim in shape (two user operator classes over int4 each naming its own FUNCTION 1 comparator an ordinary index under one and a UNIQUE index WITH (deduplicate_items = off) under the other over generate_series(1 to 1000)) then runs the real pg_amcheck binary through all four upstream phases against ONE unchanging set of index pages: clean -> repoint int4_fickle_ops FUNCTION 1 at a descending comparator via UPDATE pg_catalog.pg_amproc and assert exit 2 with the upstream-verbatim item order invariant violated for index fickleidx -> repair the amproc row and assert a --checkunique run is clean again -> repoint int4_unique_ops FUNCTION 1 at a comparator declaring 768 and 769 equal and assert exit 2 with index uniqueness is violated for index bttest_unique_idx. No byte on disk is ever corrupted: every verdict is decided by what pg_amproc currently says the operator class comparator is which is the property 005 exists to prove. Engine support is the M0119-0006 operator-class comparator dispatch (executor.btIndexOpClassComparator resolving FUNCTION 1 live via catalog.InMemory.LookupOpClassSupportProcOID and amcheck.VerifyBtreeItemOrderCmp comparing under it) plus the checkunique tier amcheck.VerifyBtreeUnique (upstream bt_entry_unique_check) which pg_amcheck reaches only because goopg reports amcheck extension version 1.4 (pg_amcheck.c:607-631 silently drops --checkunique below that). Runs are scoped with --table public.int4tbl (same whole-db scoping adaptation as the 003/004 ports). Remaining deferred in this row: only the 003_check.pl / 004_verify_heapam.pl tiers named above (unsupported index AMs box/int4range/int4[] column types STORAGE EXTERNAL TOAST corruption and multi-database orchestration). |
| e2e-failover-goopg-to-pg-async | `postgres/src/test/recovery` | `perf-optimize3-dash resume (GOOPG_WAL_CANONICAL=on + perf-optimize3/05-improvement-designs/01 C1)` | DEFERRED by perf-optimize3-dash S4 (native-only WAL default; canonical emission off): assert-skips unless GOOPG_WAL_CANONICAL=on. Scenario B async: pinned by TestE2E_FailoverGoopgToPG/async in internal/testport/e2e_failover_goopg_to_pg_test.go (M0102-0007; design 0102-0003) |
| e2e-failover-goopg-to-pg-sync | `postgres/src/test/recovery` | `perf-optimize3-dash resume (GOOPG_WAL_CANONICAL=on + perf-optimize3/05-improvement-designs/01 C1)` | DEFERRED by perf-optimize3-dash S4 (native-only WAL default; canonical emission off): assert-skips unless GOOPG_WAL_CANONICAL=on. Scenario B sync_remote_apply: pinned by TestE2E_FailoverGoopgToPG/sync_remote_apply in internal/testport/e2e_failover_goopg_to_pg_test.go (M0102-0007; design 0102-0003 + 0102-0005) |
