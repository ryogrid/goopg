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
| tap | 53 | 6 | 1 |
| utility | 1 | 0 | 0 |

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
| D-002 | `postgres/src/test/isolation` | isolation | port | no | IsolationRunner implemented in internal/testport/framework/isolation_runner.go; all 121 specs run via TestPort_IsolationSuite; most defer due to missing SQL features — expected-output comparison drives promotion to pass as features land. M0104-0008 (2026-05-14): canonical simple-write-skew permutations promoted to pass-required as focused Go tests in internal/testport/ssi_write_skew_test.go (4 tests evidence the SSI 40001 anomaly-prevention DoD); auto-permutation generation of upstream spec files still pending in the runner before D-002 can advance to pass_required=yes. | `M0060-0004` |
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
| C-002 | `postgres/src/bin/pg_checksums/t/002_actions.pl` | tap | port | yes | Adapted as TestPort_PgChecksums002Actions in internal/testport/client_tools_port_test.go; option-validation sub-cases pass; checksum enable/disable/check still deferred (goopg writes pg_control via M0095-0001 but page-level checksums are not yet implemented). | `-` |
| CD-001 | `postgres/src/bin/pg_controldata/t/001_pg_controldata.pl` | tap | port | yes | Adapted as TestPort_PgControldata001 in internal/testport/client_tools_port_test.go; full data-dir check asserts pg_controldata exits 0 with 'checkpoint' output (M0095-0001 wrote global/pg_control); CRC-corruption sub-case deferred (pg_controldata only warns on bad CRC). | `-` |
| WS-001 | `postgres/src/bin/pg_walsummary/t/001_basic.pl` | tap | port | yes | Ported as TestPort_PgWalsummary001Basic in internal/testport/client_tools_port_test.go | `-` |
| WS-002 | `postgres/src/bin/pg_walsummary/t/002_blocks.pl` | tap | port | yes | Adapted as TestPort_PgWalsummary002Blocks in internal/testport/client_tools_port_test.go; cluster-init + SQL sub-cases pass; pg_available_wal_summaries() / summarize_wal / pg_walsummary -i deferred pending goopg WAL summarizer. | `-` |
| BB-010 | `postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl` | tap | port | yes | Adapted as TestPort_PgBasebackup010 + TestPort_PgBasebackup010BackupExecution in internal/testport/pgbasebackup_port_test.go; CLI + option-validation sub-cases pass; backup execution (-X none --no-manifest) drives a live cluster end-to-end through BASE_BACKUP and verifies extracted backup_label + global/pg_control + PG_VERSION. | `-` |
| BB-011 | `postgres/src/bin/pg_basebackup/t/011_in_place_tablespace.pl` | tap | port | yes | Adapted as TestPort_PgBasebackup011InPlaceTablespace in internal/testport/pgbasebackup_port_test.go; entirely deferred pending physical streaming (BASE_BACKUP protocol) in goopg. | `-` |
| BB-020 | `postgres/src/bin/pg_basebackup/t/020_pg_receivewal.pl` | tap | port | yes | Adapted as TestPort_PgReceivewal020 in internal/testport/pgbasebackup_port_test.go; CLI + option-validation sub-cases pass; streaming deferred pending replication protocol in goopg. | `-` |
| BB-030 | `postgres/src/bin/pg_basebackup/t/030_pg_recvlogical.pl` | tap | port | yes | Adapted as TestPort_PgRecvlogical030 in internal/testport/pgbasebackup_port_test.go; CLI + option-validation sub-cases pass; logical streaming deferred pending full logical replication protocol. | `-` |
| BB-040 | `postgres/src/bin/pg_basebackup/t/040_pg_createsubscriber.pl` | tap | port | yes | Adapted as TestPort_PgCreatesubscriber040 in internal/testport/pgbasebackup_port_test.go; CLI + mandatory-option sub-cases pass; subscriber setup deferred pending streaming + logical replication. | `-` |
| D-005a | `postgres/src/bin/scripts/t/100_vacuumdb.pl` | tap | port | yes | Ported as TestPort_Scripts100Vacuumdb in internal/testport/scripts_port_test.go; VACUUM paren syntax + pg_namespace + OPERATOR()/ANY()/LATERAL support (M0095-0004). | `-` |
| D-005b | `postgres/src/bin/scripts/t/102_vacuumdb_stages.pl` | tap | port | yes | Ported as TestPort_Scripts102VacuumdbStages in internal/testport/scripts_port_test.go; --analyze-in-stages uses ANALYZE paren syntax (M0095-0004). | `-` |
| D-005c | `postgres/src/bin/scripts/t/101_vacuumdb_all.pl` | tap | port | yes | Ported as TestPort_Scripts101VacuumdbAll in internal/testport/scripts_port_test.go; --all queries pg_database+pg_namespace with LATERAL/OPERATOR/ANY support (M0095-0004). | `-` |
| D-005d | `postgres/src/bin/scripts/t/020_createdb.pl` | tap | port | yes | Ported as TestPort_Scripts020Createdb in internal/testport/scripts_port_test.go; CREATE DATABASE via tryHandleDatabaseDDL + catalog.CreateDatabase (M0095-0007). | `-` |
| D-005e | `postgres/src/bin/scripts/t/050_dropdb.pl` | tap | port | yes | Ported as TestPort_Scripts050Dropdb in internal/testport/scripts_port_test.go; DROP DATABASE checks ErrDatabaseNotFound (M0095-0007). | `-` |
| D-005f | `postgres/src/bin/scripts/t/040_createuser.pl` | tap | port | yes | Ported as TestPort_Scripts040Createuser in internal/testport/scripts_port_test.go; CREATE ROLE via compat+role-tracking handler (M0095-0006). | `-` |
| D-005g | `postgres/src/bin/scripts/t/070_dropuser.pl` | tap | port | yes | Ported as TestPort_Scripts070Dropuser in internal/testport/scripts_port_test.go; DROP ROLE checks in-memory role set (M0095-0006). | `-` |
| D-005h | `postgres/src/bin/scripts/t/090_reindexdb.pl` | tap | port | yes | Ported as TestPort_Scripts090Reindexdb in internal/testport/scripts_port_test.go; REINDEX parser+executor no-op stub (M0095-0005). | `-` |
| D-005i | `postgres/src/bin/scripts/t/091_reindexdb_all.pl` | tap | port | yes | Ported as TestPort_Scripts091ReindexdbAll in internal/testport/scripts_port_test.go; uses pg_database query from M0095-0004 (M0095-0005). | `-` |
| D-005j | `postgres/src/bin/scripts/t/010_clusterdb.pl` | tap | port | yes | Ported as TestPort_Scripts010Clusterdb in internal/testport/scripts_port_test.go; CLUSTER parser+executor stub + pg_class relnamespace OID fix (M0095-0008). | `-` |
| D-005k | `postgres/src/bin/scripts/t/011_clusterdb_all.pl` | tap | port | yes | Ported as TestPort_Scripts011ClusterdbAll in internal/testport/scripts_port_test.go; uses pg_database query (M0095-0008). | `-` |
| D-005l | `postgres/src/bin/scripts/t/200_connstr.pl` | tap | defer | no | Requires CREATE DATABASE (D-005d) and LATIN1 server encoding; goopg currently UTF8-only. | `M0060-0003` |
| D-006 | `postgres/src/test/modules` | mixed | defer | no | Modules migration is staged by dependency class and extension assumptions. | `M0060-0005` |
| D-007 | `postgres/contrib` | mixed | defer | no | Contrib migration is staged by dependency class and extension/runtime assumptions. | `M0060-0005` |
| E-001 | `postgres/src/test/modules/unsafe_tests` | modules | excluded | no | Explicit unsafe suite is outside compatibility scope by policy. | `-` |
| DU-001 | `postgres/src/bin/pg_dump/t/001_basic.pl` | tap | port | yes | Ported as TestPort_PgDump001Basic in internal/testport/pgdump_port_test.go (M0110-0001); pure CLI option-handling test (help/version/options + invalid-option and disallowed-combination cases for pg_dump/pg_restore/pg_dumpall) — requires no server connection. | `-` |
| E-002 | `postgres/src/bin/pg_dump/t` | tap | excluded | no | Directory umbrella for the pg_dump TAP suite. 001_basic.pl ported separately as DU-001; 002-010 stay excluded pending broad catalog-view parity (pg_class/pg_attribute/pg_type/pg_proc/pg_depend/pg_extension) and a dump+restore round-trip against a live goopg server (M0110-0001). | `-` |
| W-001 | `postgres/src/bin/pg_waldump` | utility | port | yes | Ported as TestPort_WALPgWaldumpCompat in internal/testport/wal_pg_waldump_test.go; verifies PG-compatible WAL format (M0101-0001) | `-` |
| WD-001 | `postgres/src/bin/pg_waldump/t/001_basic.pl` | tap | port | yes | Ported as TestPort_PgWaldump001Basic in internal/testport/pgwaldump_port_test.go (M0110-0002); the pure CLI option-handling tier (help/version/options + no-args/too-many-args + invalid --block/--fork/--limit/--relation/--rmgr/--start/--end + --rmgr=list) — needs no server. The server-dependent tier of 001_basic.pl is deferred under WD-002. | `-` |
| WD-002 | `postgres/src/bin/pg_waldump/t` | tap | defer | no | Deferred remainder of the pg_waldump TAP suite: the server-dependent tier of 001_basic.pl (per-rmgr/per-relation/per-block filtering) and 002_save_fullpage.pl. Blocked because the workload exercises the hash/gin/gist/spgist/brin access methods goopg does not implement; goopg WAL-format readability for supported record types is already covered by W-001 (TestPort_WALPgWaldumpCompat). | `M0110-0002` |
| AC-001 | `postgres/src/bin/pg_amcheck/t/001_basic.pl` | tap | port | yes | Ported as TestPort_PgAmcheck001Basic in internal/testport/pgamcheck_port_test.go (M0110-0003); the pure CLI option-handling tier (program_help_ok/program_version_ok/program_options_handling_ok) — decided by the upstream binary's arg parser before any server connection. Runs the bundled pg_amcheck with LD_LIBRARY_PATH at postgres/local_install/lib (it links PQcancelBlocking which the host libpq may lack). The server-dependent tests are deferred under AC-002. | `-` |
| AC-002 | `postgres/src/bin/pg_amcheck/t` | tap | defer | no | Deferred remainder of the pg_amcheck TAP suite: 002_nonesuch.pl 003_check.pl 004_verify_heapam.pl 005_opclass_damage.pl. Blocked because they connect to a live server and run heap/btree corruption checks requiring the verify_heapam() set-returning function and operator-class system-catalog parity goopg does not yet implement. | `M0110-0003` |
| RW-001 | `postgres/src/bin/pg_resetwal/t/001_basic.pl` | tap | port | yes | Ported as TestPort_PgResetwal001Basic in internal/testport/pgresetwal_port_test.go (M0110-0004); the CLI-only tier (program_help_ok/program_version_ok/program_options_handling_ok + too-many-args/no-data-directory/nonexistent-directory + the option-argument validation cases for -c/-e/-l/-m/-o/-O/-u/-x/--wal-segsize/--char-signedness) — all decided in getopt before pg_resetwal opens the data directory so it needs no server. The server-dependent tier (init/start/--force reset + SLRU-derived control-override options) is deferred under RW-002. | `-` |
| RW-002 | `postgres/src/bin/pg_resetwal/t` | tap | defer | no | Deferred remainder of the pg_resetwal TAP suite: the server-dependent tier of 001_basic.pl (init/start/--pgdata/-n/--force reset round-trips + the --commit-timestamp-ids/--multixact-ids/--multixact-offset/--oldest-transaction-id/--next-transaction-id overrides computed from real pg_commit_ts/pg_multixact/pg_xact SLRU segments) and 002_corrupted.pl. Blocked on pg_control byte-level read/write round-trip compatibility (M0106) plus on-disk SLRU-segment-layout parity. | `M0110-0004` |
| e2e-logical-failover-pg-to-goopg-async | `postgres/src/test/subscription` | tap | port | yes | Scenario A async: pinned by TestPort_PgoutputInteropPGToGoopgPgbenchKillAsync in internal/testport/pgoutput_interop_test.go (M0103-0007 rung 23; design 0103-0046) | `-` |
| e2e-logical-failover-pg-to-goopg-sync | `postgres/src/test/subscription` | tap | port | yes | Scenario A sync_remote_apply: pinned by TestPort_PgoutputInteropPGToGoopgPgbenchKillSyncRemoteApply in internal/testport/pgoutput_interop_test.go (M0103-0007 rung 26; design 0103-0049) | `-` |
| e2e-logical-failover-goopg-to-pg-async | `postgres/src/test/subscription` | tap | port | yes | Scenario B async: pinned by TestPort_PgoutputInteropGoopgToPG live wrapper in internal/testport/pgoutput_interop_test.go (M0103-0008 loop 19 closure; design 0103-0023) | `-` |
| e2e-logical-failover-goopg-to-pg-sync | `postgres/src/test/subscription` | tap | port | yes | Scenario B sync_remote_apply: pinned by TestPort_PgoutputInteropGoopgToPG live wrapper in internal/testport/pgoutput_interop_test.go (M0103-0008 loop 19 closure; design 0103-0023) | `-` |
| e2e-failover-pg-to-goopg-async | `postgres/src/test/recovery` | tap | port | yes | Scenario A async: pinned by TestE2E_FailoverPGtoGoopg/async in internal/testport/e2e_failover_pg_to_goopg_test.go (M0102-0006; design 0102-0003) | `-` |
| e2e-failover-pg-to-goopg-sync | `postgres/src/test/recovery` | tap | port | yes | Scenario A sync_remote_apply: pinned by TestE2E_FailoverPGtoGoopg/sync_remote_apply in internal/testport/e2e_failover_pg_to_goopg_test.go (M0102-0006; design 0102-0003 + 0102-0005) | `-` |
| e2e-failover-goopg-to-pg-async | `postgres/src/test/recovery` | tap | port | yes | Scenario B async: pinned by TestE2E_FailoverGoopgToPG/async in internal/testport/e2e_failover_goopg_to_pg_test.go (M0102-0007; design 0102-0003) | `-` |
| e2e-failover-goopg-to-pg-sync | `postgres/src/test/recovery` | tap | port | yes | Scenario B sync_remote_apply: pinned by TestE2E_FailoverGoopgToPG/sync_remote_apply in internal/testport/e2e_failover_goopg_to_pg_test.go (M0102-0007; design 0102-0003 + 0102-0005) | `-` |

## Notes

- Every non-passing target must be present here as `defer` or `excluded`.
- `defer` entries must reference a follow-up milestone task.
