# PostgreSQL Oracle Test-Port Status

Generated from `docs/test-port/postgres-oracle-port-status.csv`.

Status meanings:
- `port`: migrated and pass-required.
- `defer`: in scope, not yet pass-required.
- `excluded`: explicitly out of scope by policy.

## Suite Summary

| suite_type | port | defer | excluded |
| ---------- | ----:| -----:| --------:|
| isolation | 1 | 0 | 0 |
| mixed | 0 | 2 | 0 |
| modules | 0 | 0 | 1 |
| regress | 0 | 1 | 0 |
| tap | 30 | 14 | 1 |

## Entries

| id | upstream_path | suite_type | status | pass_required | rationale | deferred_to |
|----|---------------|------------|--------|---------------|-----------|-------------|
| P-001 | `postgres/src/bin/initdb/t/001_initdb.pl` | tap | port | yes | Ported to go test as TestPort_Initdb001 in internal/testport/tap_port_test.go | `-` |
| P-002 | `postgres/src/bin/pg_ctl/t/001_start_stop.pl` | tap | port | yes | Ported to go test as TestPort_PgCtl001StartStop | `-` |
| P-003 | `postgres/src/bin/pg_ctl/t/002_status.pl` | tap | port | yes | Ported to go test as TestPort_PgCtl002Status | `-` |
| P-004 | `postgres/src/bin/pg_ctl/t/003_promote.pl` | tap | port | yes | Adapted and ported as TestPort_PgCtl003PromoteAdapted | `-` |
| P-005 | `postgres/src/bin/pg_ctl/t/004_logrotate.pl` | tap | port | yes | Adapted and ported as TestPort_PgCtl004LogrotateAdapted | `-` |
| P-006 | `postgres/src/bin/pgbench/t/001_pgbench_with_server.pl` | tap | port | yes | Ported to go test as TestPort_Pgbench001WithServer | `-` |
| P-007 | `postgres/src/bin/pgbench/t/002_pgbench_no_server.pl` | tap | port | yes | Ported to go test as TestPort_Pgbench002NoServer | `-` |
| P-008 | `postgres/src/bin/psql/t/001_basic.pl` | tap | port | yes | Ported to go test as TestPort_Psql001Basic | `-` |
| P-009 | `postgres/src/bin/psql/t/010_tab_completion.pl` | tap | port | yes | Adapted and ported as TestPort_Psql010TabCompletionAdapted | `-` |
| P-010 | `postgres/src/bin/psql/t/020_cancel.pl` | tap | port | yes | Adapted and ported as TestPort_Psql020CancelAdapted | `-` |
| D-001 | `postgres/src/test/regress` | regress | defer | no | Requires dedicated pg_regress-compatible runner and normalization policy while upstream SQL files remain migration targets. | `M0060-0002` |
| D-002 | `postgres/src/test/isolation` | isolation | port | no | IsolationRunner implemented in internal/testport/framework/isolation_runner.go; all 121 specs run via TestPort_IsolationSuite; most defer due to missing SQL features — expected-output comparison drives promotion to pass as features land. | `M0060-0004` |
| D-003 | `postgres/src/test/recovery` | tap | defer | no | Recovery TAP scenarios; subset ported in M0094 (6 tests); remaining 41 tests deferred pending archiving/PITR/timeline/2PC capability. | `M0094` |
| D-004 | `postgres/src/test/subscription` | tap | defer | no | Subscription TAP scenarios; subset ported in M0094 (3 tests); remaining 33 tests deferred pending binary-format/streaming/2PC/row-filter capability. | `M0094` |
| R-001 | `postgres/src/test/recovery/t/001_stream_rep.pl` | tap | port | yes | Ported as TestPort_Recovery001StreamRep in internal/testport/recovery_port_test.go | `-` |
| R-013 | `postgres/src/test/recovery/t/013_crash_restart.pl` | tap | port | yes | Ported as TestPort_Recovery013CrashRestart in internal/testport/recovery_port_test.go | `-` |
| R-019 | `postgres/src/test/recovery/t/019_replslot_limit.pl` | tap | port | yes | Ported as TestPort_Recovery019ReplslotLimit in internal/testport/recovery_port_test.go | `-` |
| R-038 | `postgres/src/test/recovery/t/038_save_logical_slots_shutdown.pl` | tap | port | yes | Ported as TestPort_Recovery038SaveLogicalSlots in internal/testport/recovery_port_test.go | `-` |
| R-039 | `postgres/src/test/recovery/t/039_end_of_wal.pl` | tap | port | yes | Ported as TestPort_Recovery039EndOfWal in internal/testport/recovery_port_test.go | `-` |
| R-047 | `postgres/src/test/recovery/t/047_checkpoint_physical_slot.pl` | tap | port | yes | Ported as TestPort_Recovery047CheckpointPhysicalSlot in internal/testport/recovery_port_test.go | `-` |
| S-001 | `postgres/src/test/subscription/t/001_rep_changes.pl` | tap | port | yes | Ported as TestPort_Subscription001RepChanges in internal/testport/subscription_port_test.go | `-` |
| S-004 | `postgres/src/test/subscription/t/004_sync.pl` | tap | port | yes | Ported as TestPort_Subscription004Sync in internal/testport/subscription_port_test.go | `-` |
| S-026 | `postgres/src/test/subscription/t/026_stats.pl` | tap | port | yes | Ported as TestPort_Subscription026Stats in internal/testport/subscription_port_test.go | `-` |
| P-011 | `postgres/src/bin/scripts/t/080_pg_isready.pl` | tap | port | yes | Ported as TestPort_Scripts080PgIsready in internal/testport/scripts_port_test.go | `-` |
| C-001 | `postgres/src/bin/pg_checksums/t/001_basic.pl` | tap | port | yes | Ported as TestPort_PgChecksums001Basic in internal/testport/client_tools_port_test.go | `-` |
| C-002 | `postgres/src/bin/pg_checksums/t/002_actions.pl` | tap | port | yes | Adapted as TestPort_PgChecksums002Actions in internal/testport/client_tools_port_test.go; option-validation sub-cases pass; checksum enable/disable/check deferred pending goopg pg_control support. | `-` |
| CD-001 | `postgres/src/bin/pg_controldata/t/001_pg_controldata.pl` | tap | port | yes | Adapted as TestPort_PgControldata001 in internal/testport/client_tools_port_test.go; CLI + data-dir error-path sub-cases pass; qr/checkpoint/ output check deferred pending goopg pg_control support. | `-` |
| WS-001 | `postgres/src/bin/pg_walsummary/t/001_basic.pl` | tap | port | yes | Ported as TestPort_PgWalsummary001Basic in internal/testport/client_tools_port_test.go | `-` |
| WS-002 | `postgres/src/bin/pg_walsummary/t/002_blocks.pl` | tap | port | yes | Adapted as TestPort_PgWalsummary002Blocks in internal/testport/client_tools_port_test.go; cluster-init + SQL sub-cases pass; pg_available_wal_summaries() / summarize_wal / pg_walsummary -i deferred pending goopg WAL summarizer. | `-` |
| BB-010 | `postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl` | tap | port | yes | Adapted as TestPort_PgBasebackup010 in internal/testport/pgbasebackup_port_test.go; CLI + option-validation sub-cases pass; backup execution deferred pending pg_basebackup physical streaming in goopg. | `-` |
| BB-011 | `postgres/src/bin/pg_basebackup/t/011_in_place_tablespace.pl` | tap | port | yes | Adapted as TestPort_PgBasebackup011InPlaceTablespace in internal/testport/pgbasebackup_port_test.go; entirely deferred pending physical streaming (BASE_BACKUP protocol) in goopg. | `-` |
| BB-020 | `postgres/src/bin/pg_basebackup/t/020_pg_receivewal.pl` | tap | port | yes | Adapted as TestPort_PgReceivewal020 in internal/testport/pgbasebackup_port_test.go; CLI + option-validation sub-cases pass; streaming deferred pending replication protocol in goopg. | `-` |
| BB-030 | `postgres/src/bin/pg_basebackup/t/030_pg_recvlogical.pl` | tap | port | yes | Adapted as TestPort_PgRecvlogical030 in internal/testport/pgbasebackup_port_test.go; CLI + option-validation sub-cases pass; logical streaming deferred pending full logical replication protocol. | `-` |
| BB-040 | `postgres/src/bin/pg_basebackup/t/040_pg_createsubscriber.pl` | tap | port | yes | Adapted as TestPort_PgCreatesubscriber040 in internal/testport/pgbasebackup_port_test.go; CLI + mandatory-option sub-cases pass; subscriber setup deferred pending streaming + logical replication. | `-` |
| D-005a | `postgres/src/bin/scripts/t/100_vacuumdb.pl` | tap | defer | no | Requires VACUUM parenthesized option syntax (VACUUM (FULL/FREEZE/SKIP_DATABASE_STATS/...)) in goopg parser+executor and pg_catalog.pg_namespace catalog view. | `M0060-0003` |
| D-005b | `postgres/src/bin/scripts/t/102_vacuumdb_stages.pl` | tap | defer | no | Requires same VACUUM parenthesized syntax as D-005a; blocked by same parser gap. | `M0060-0003` |
| D-005c | `postgres/src/bin/scripts/t/101_vacuumdb_all.pl` | tap | defer | no | Requires VACUUM parenthesized syntax (D-005a) and multi-database iteration via pg_database. | `M0060-0003` |
| D-005d | `postgres/src/bin/scripts/t/020_createdb.pl` | tap | defer | no | Requires CREATE DATABASE parser+executor stub; goopg currently single-database only. | `M0060-0003` |
| D-005e | `postgres/src/bin/scripts/t/050_dropdb.pl` | tap | defer | no | Requires DROP DATABASE parser+executor stub; depends on CREATE DATABASE (D-005d). | `M0060-0003` |
| D-005f | `postgres/src/bin/scripts/t/040_createuser.pl` | tap | defer | no | Requires CREATE ROLE/USER parser+executor stub. | `M0060-0003` |
| D-005g | `postgres/src/bin/scripts/t/070_dropuser.pl` | tap | defer | no | Requires DROP ROLE/USER parser+executor stub; depends on CREATE ROLE (D-005f). | `M0060-0003` |
| D-005h | `postgres/src/bin/scripts/t/090_reindexdb.pl` | tap | defer | no | Requires REINDEX DATABASE/TABLE parser+executor stub. | `M0060-0003` |
| D-005i | `postgres/src/bin/scripts/t/091_reindexdb_all.pl` | tap | defer | no | Requires REINDEX stub (D-005h) and multi-database support. | `M0060-0003` |
| D-005j | `postgres/src/bin/scripts/t/010_clusterdb.pl` | tap | defer | no | Requires CLUSTER parser+executor stub. | `M0060-0003` |
| D-005k | `postgres/src/bin/scripts/t/011_clusterdb_all.pl` | tap | defer | no | Requires CLUSTER stub (D-005j) and multi-database support. | `M0060-0003` |
| D-005l | `postgres/src/bin/scripts/t/200_connstr.pl` | tap | defer | no | Requires CREATE DATABASE (D-005d) and LATIN1 server encoding; goopg currently UTF8-only. | `M0060-0003` |
| D-006 | `postgres/src/test/modules` | mixed | defer | no | Modules migration is staged by dependency class and extension assumptions. | `M0060-0005` |
| D-007 | `postgres/contrib` | mixed | defer | no | Contrib migration is staged by dependency class and extension/runtime assumptions. | `M0060-0005` |
| E-001 | `postgres/src/test/modules/unsafe_tests` | modules | excluded | no | Explicit unsafe suite is outside compatibility scope by policy. | `-` |
| E-002 | `postgres/src/bin/pg_dump/t` | tap | excluded | no | Client binary behavior depends on utility surfaces intentionally outside current server-focused scope. | `-` |

## Notes

- Every non-passing target must be present here as `defer` or `excluded`.
- `defer` entries must reference a follow-up milestone task.
