# Ledger bulk-triage report

ledger: `.ralph/deferral_ledger.md`  
rows parsed: **2296**  open (`-`/`open`/`[!]`): **2129**  ATTEMPTED, REVERTED (blocker found): 1  CORRECTION + STAGE: 1  FEASIBILITY (investigated, not built): 1  PARTIAL (Stage 1 LANDED): 1  STAGE 2 FLIP LANDED (no silent-failure path; 2 pre-existing bugs found): 1  resolved: 151  resolved (2026-08-10): 1  resolved (2026-08-19, M0134-0005ah): 2  resolved (2026-08-19, M0134-0005ao): 1  resolved (2026-08-19, M0134-0005ar): 2  x: 4  ~~STAGE 2 STARTED (breakage located, flip reverted)~~ SUPERSEDED: 1

## Classification

| class | rows | meaning |
|---|---|---|
| stale-candidate | 28 | every resolvable ref gone at HEAD — resume point dangles |
| partial-stale | 58 | >=1 strong ref gone, >=1 ref live |
| live | 593 | all refs present |
| unverifiable | 1450 | no extractable ref / pg-refs only — survivor by default |

## Stale candidates (auto-flag)

| line | task-id | gone refs (last touch) |
|---|---|---|
| 119 | M0119-0004 | `cast_ddl_recovery.go` ← 64b0891be 2026-07-17 catalog+wal(B2.2a): pg_cast converted to heap journaling — kinds 38/39 retired |
| 121 | M0119-0004 | `collation_ddl_recovery.go` ← 3b5108277 2026-07-17 catalog+wal(B2.2d): pg_collation + pg_conversion converted to heap journaling — kinds 40-45/93/130-132 retired |
| 124 | M0119-0004 | `aggregate_ddl_recovery.go` ← b4743f95f 2026-07-17 catalog+wal(B2.2b): pg_aggregate converted to heap journaling — kinds 46-49 retired |
| 354 | perf-optize3-dash S3a finding | `initdb/native_only_audit_test.go` ← 1f0a3eca9 2026-07-15 wal: remove canonical dual-emit + knob; classify/recover on PG (xl_rmid,xl_info) |
| 373 | M-NIGHTLY (string concat DateStyle) | `join_agg.go` ← no history |
| 374 | M-NIGHTLY (agg element DateStyle) | `join_agg.go` ← no history · pg-ref also gone: `o.c` |
| 389 | WAL native-PG rework §5.1-5.3+§6 removal | `canonical.go` ← 1f0a3eca9 2026-07-15 wal: remove canonical dual-emit + knob; classify/recover on PG (xl_rmid,xl_info) |
| 421 | B4.6 STAGE 3a RM_DBASE — kinds 18/19 RETIRED | `database_ddl_recovery.go` ← 92950fc8e 2026-07-18 catalog+wal(B4.6 Stage 3a): RM_DBASE + pg_database reload — kinds 18/19 retired |
| 494 | tpcds-round2 RC-1b | `TestMHJSingleSourceFilterCoordinateSpace` ← 46446e577 2026-09-20 chore(housekeeping): commit drift — loop state, ci logs, landed-work evidence |
| 546 | M0124-0004 | `scripts/tpcds-sf05-regression.sh` ← e2a50de40 2026-09-11 bench(tpcds): SF0.5→SF0.25 dev-gate migration — reviewed APPROVE, P0/P1/P3 green, golden swapped; `sweep-20260729-033758.txt` ← no history |
| 550 | root-0037 | `server-goroutines.txt` ← no history |
| 570 | M0125-0020 | `sweep-20260729-123114.txt` ← no history; `scripts/tpcds-sf05-regression.sh` ← e2a50de40 2026-09-11 bench(tpcds): SF0.5→SF0.25 dev-gate migration — reviewed APPROVE, P0/P1/P3 green, golden swapped |
| 576 | M0125-0003 | `sweep-20260729-123114.txt` ← no history |
| 616 | M0125-0005 | `internal/planner/bushy.go` ← 049018300 2026-08-07 planner(m0127): P6.3 deletes the old subset-bitmask DP — the PG-shaped search is the only join-order search |
| 640 | M0125-0034 | `joinorder.go+bushy.go` ← no history · pg-ref also gone: `customer.c` |
| 651 | M0125-0042 | `bushy.go` ← 049018300 2026-08-07 planner(m0127): P6.3 deletes the old subset-bitmask DP — the PG-shaped search is the only join-order search |
| 685 | M0125-0013 | `explain.go` ← no history |
| 931 | M0125-0003 | `joinorder.go` ← 362cfa91c 2026-09-03 optimizer: delete joinorder.go — take2 P3-12 cleanup |
| 945 | M0127-P6.1 | `fused_hash_join.go` ← 611061a56 2026-08-07 executor(m0127): P6.1 deletes runtime hash-join fusion — and buildEnv turned out to be fusion and nothing else |
| 950 | M0127-P6.4 GEQO | `joinorder.go` ← 362cfa91c 2026-09-03 optimizer: delete joinorder.go — take2 P3-12 cleanup |
| 952 | M0127-P6.4 semi/anti bushy | `planner/join_is_legal.go` ← no history |
| 954 | M0127-P6.4 join_order_restriction | `planner/join_is_legal.go` ← no history |
| 957 | M0122-0003 EXPLAIN WAL | `operators_explain.go+context.go` ← no history |
| 1392 | M0134-0001 walkExprTree IsNull | `TestExprContainsColumnRef` ← 46446e577 2026-09-20 chore(housekeeping): commit drift — loop state, ci logs, landed-work evidence |
| 1663 | M0134-0026 | `utility_settings.go` ← no history |
| 1871 | M0134-0099 | `collate.icu.sql` ← no history |
| 1921 | M0134-0153 | `float.sql` ← no history |
| 2074 | take3-B-03-declined | `.plans.txt` ← no history |

## Clusters (same-mechanism folds; rows with any survivor)

| cluster | members | stale | partial | live | unverifiable | sample task-ids |
|---|---|---|---|---|---|---|
| family:M0134 | 503 | 4 | 1 | 4 | 494 | M0134-0001, M0134-0001 IOS Cond cosmetics, M0134-0001 dup-equality decline, M0134-0001 const-arg min/max |
| family:M0119 | 177 | 3 | 0 | 6 | 168 | M0119-0004 deferred-RI fresh snapshot, M0119-0004 REPLICA IDENTITY round-trip, M0119-0004 CREATE POLICY round-trip, M0119-0004 CREATE RULE round-trip |
| family:M0127 | 169 | 4 | 1 | 1 | 163 | M0127-P0.2, M0127-P0.3, M0127-P1.1, M0127-P1.2 |
| family:M0131 | 106 | 0 | 0 | 3 | 103 | M0131-S1, M0131-S10, M0131-S3, M0131-S11 |
| family:M0122 | 101 | 1 | 0 | 2 | 98 | M0122-0002, M0122-0003, M0122-0004, M0122-0005 |
| family:M0125 | 96 | 7 | 0 | 10 | 79 | M0125-0011, M0125-0010, M0125-0006, M0125-0017 |
| family:M0130 | 51 | 0 | 0 | 2 | 49 | M0130-S11.2a, M0130-S11.2b, M0130-S11.3, M0130-S11.4 (slice 1/3) |
| family:M0110 | 44 | 0 | 0 | 4 | 40 | M0110-0001, M0110-0002, M0110-0003, M0110-0003 / all-blocked |
| internal/executor/expr.go | 33 | 0 | 1 | 32 | 0 | M0119-0004, M0119-0004 multi-CHECK domain, M0110-0001 slice429, M0122-0007 4e (regclass discovery, collateral) |
| internal/optimizer/planner.go | 32 | 0 | 4 | 28 | 0 | M0125-0039, M0125-0034, M-NIGHTLY (root-0038), M0134-0001 LATERAL agg refs |
| internal/initdb/open.go | 23 | 0 | 2 | 21 | 0 | aio-adapter-lost-checksumfile, M0110-0001 slice429, M0119-0004/M0110-0001, M0119-0004 |
| family:M-NIGHTLY AI | 21 | 0 | 0 | 1 | 20 | M-NIGHTLY AI-20260710-011513-001 / 2b item 1, M-NIGHTLY AI-20260725-004, M-NIGHTLY AI-20260810-011258-005, M-NIGHTLY AI-20260810-011258-003 |
| internal/optimizer/joinsearchseam.go | 18 | 0 | 5 | 13 | 0 | c07-single-rel-never-reaches-ordered-index-producer, m0142-0001, M0142-0008c-3b, M0142-0008a-3i-plumbing-b1 |
| internal/executor/operators_explain.go | 17 | 0 | 1 | 16 | 0 | csq-S6 stage 6b NLI residual + Filter{SeqScan} inner acceptance, M0125-0037, M0125-0042, M0125-0002 |
| internal/executor/operators_ddl.go | 15 | 0 | 1 | 14 | 0 | PRE-EXISTING: ALTER TABLE ADD COLUMN lost on restart, root-0031, M0134-0002 view body freeze, M0134-0002 ONLY-on-partitioned |
| family:M0124 | 15 | 1 | 0 | 0 | 14 | M0124-0001, M0124-0001 (chunk 13 FINAL), M0124-0006, M0124-0003 row-anchor value-blindness |
| internal/executor/operators_storage.go | 13 | 0 | 0 | 13 | 0 | e17-lazy-detoast-divergence, e17-qual-first-error-ordering, M0110-0002 WD-003, 0009-readstream |
| internal/optimizer/cardinality.go | 13 | 0 | 3 | 10 | 0 | tpcds-round2 timeouts, M0125-0003, M0129-S1 Q74 fix, take3-B-04-kept |
| internal/executor/codec.go | 11 | 0 | 1 | 10 | 0 | unimplemented_feat #5(d-iv) (ts − ts over infinity), pq-P10, M0127-P5.9-s, M0119-0006 |
| family:M0123 | 11 | 0 | 0 | 0 | 11 | M0123-S1 canonical pg_node_tree scalar serializer LANDED, M0123-S2 sub-slice 1 pgnodes resolver + rebuild + shape-check, M0123-S2 parts (b)(c) canonical adbin writer + reload wired, M0123-S3 sub-slice 1 query-tree codec (Query/RTE/Var) |
| internal/optimizer/unnest.go | 11 | 0 | 1 | 10 | 0 | csq-S6 stage 6b multi-param decorrelation + conjunct residue, M0125-0001, M0125-0036, M0125-0043 |
| internal/postmaster/dispatch.go | 9 | 0 | 2 | 7 | 0 | M0110-0003, 08 doc 05 BEGIN snapshot reuse, M0125-0021, M0125-0003 |
| internal/parser/ddl.go | 9 | 0 | 0 | 9 | 0 | M0125-0020, M0134-0002 duplicate ENFORCED, M0134-0002 quoted check expr, M0134-0008 |
| internal/access/transam/xlog/recovery.go | 8 | 0 | 0 | 8 | 0 | WAL native-PG rework doc04 §5.4 slice 1, WAL native-PG rework §5.4 slice 2, WAL native-PG rework §5.4 slice 3 (mapping table), M0131-S10.5 |
| internal/parser/select.go | 8 | 0 | 0 | 8 | 0 | M0124-0001, M0125-0006, M0125-0018, M0134-0015 |
| family:take3-EX0 | 8 | 0 | 0 | 1 | 7 | take3-EX0-G-EX1, take3-EX0-G-EX2, take3-EX0-G-EX3, take3-EX0-G-EX4 |
| internal/optimizer/partialaggupper.go | 7 | 0 | 2 | 5 | 0 | take3-B-17b-partial-agg-count, c19g-upper-parallel-family-strategy, take3-D-05-parallel-blindness-narrowed, M0141-S2b-11 |
| internal/utils/misc/defaults.go | 7 | 0 | 0 | 7 | 0 | 0009-readstream, M0131-S1, M0131-S3, M0134-0035 |
| internal/executor/operators_join_agg.go | 7 | 0 | 1 | 6 | 0 | M0125-0026, M0127-P6.4 skew buckets, M0134-0005aj, M0134-0022 |
| family:M-NIGHTLY (AI | 7 | 0 | 0 | 4 | 3 | M-NIGHTLY (AI-20260806-011323-016), M-NIGHTLY (AI-20260806-011323-018), M-NIGHTLY (AI-20260806-011323-001), M-NIGHTLY (AI-20260822-001356-003) |
| scripts/tpch-spotcheck.sh | 7 | 0 | 0 | 7 | 0 | M-NIGHTLY AI-20260811-014635-012, M0143-0003c, M0143-0003e, M0143-0003f |
| internal/catalog/catalog.go | 7 | 0 | 1 | 6 | 0 | M0134-0005ae, M0134-0005am, M0134-0005ap, M0134-0013 |
| family:take3-B | 7 | 1 | 0 | 1 | 5 | take3-B-01b-declined, take3-B-02-interim, take3-B-03-declined, take3-B-08-network-deferred |
| internal/postmaster/copy.go | 6 | 0 | 0 | 6 | 0 | M0117-0007, M0119-0006, M0134-0005j COPY defaults, M0134-0005l COPY error detail |
| internal/optimizer/nl_index_join.go | 6 | 0 | 0 | 6 | 0 | M0125-0002, M0127-P6.3 NLI fan-out, take3-B-17e-blocked, take3-C-20f-blocked |
| family:take3-D | 6 | 0 | 0 | 2 | 4 | take3-D-04-model-wrong, take3-D-04-private-worker-builds, take3-D-05-mapslotbytes-measured, take3-D-05-spacepeak-reporting |
| family:wal-backend | 5 | 0 | 0 | 0 | 5 | wal-backend-flush slice-3, wal-backend-flush slice-4, wal-backend-flush slice-5, wal-backend-flush slice-6 |
| ci/batch/lib/summarize.py | 5 | 0 | 0 | 5 | 0 | M-NIGHTLY AI-20260725-008..026, root-0032 / M-NIGHTLY, M0125-0014 / M0125-0015, M0125-0011 |
| internal/optimizer/cost_funcs.go | 5 | 0 | 1 | 4 | 0 | M0127-P3.1, m0137-0012-b8-indexprobe-multiplier-parity-vs-wallclock, m0142-0003c, M0142-0005 |
| internal/optimizer/costindex.go | 5 | 0 | 2 | 3 | 0 | M0127-P5.9-h, take3-B-15-blocked-2, c19-index-path-qpqual-not-charged, m0137-0012-b10-corr-zero-fallback-max-io-cost |
| family:M0132 | 5 | 0 | 0 | 2 | 3 | M0132-S7, M0132-S8, M0132-S11, M0132-S12 |
| scripts/pg-regress-runner.sh | 5 | 0 | 0 | 5 | 0 | M0134-0008, M0134-0010, M0134-0011, M0134-0009 |
| family:take2-P2 | 5 | 0 | 0 | 0 | 5 | take2-P2-02b, take2-P2-08, take2-P2-10, take2-P2-09-saop |
| family:M0142 | 5 | 0 | 1 | 4 | 0 | m0142-0004, m0142-0012a, m0142-0003f, m0142-0003k |
| internal/storage/bufpool.go | 4 | 0 | 1 | 3 | 0 | take3-E-19-installing-prefetch, perf-optimize3 design handoff, M0129-S5.7 prefetch, take3-E-11-prefetch-discards-buffer |
| family:M0117 | 4 | 0 | 0 | 0 | 4 | M0117-0006, M0117-0007, M0117-0008 |
| family:perf-optimize3 | 4 | 0 | 0 | 0 | 4 | perf-optimize3-dash (design), perf-optimize3-dash S3b, perf-optimize3-dash S4 flip, perf-optimize3-dash S4 (content parity) |
| internal/nodes/datum.go | 4 | 0 | 0 | 4 | 0 | M0123-S4 sub-slice 2 view-query bool/null wiring, S4 sub-slice 29 string folds to text/numeric, M0119-0006 |
| internal/executor/plpgsql_runtime.go | 4 | 0 | 0 | 4 | 0 | root-0034, M0125-0055, M0134-0014, M0134-0044 |
| internal/executor/operators_analyze.go | 4 | 0 | 0 | 4 | 0 | M0125-0003, M0127-P5.6-e-ii, m0138-0002, m0138-0004 |
| internal/executor/instrument.go | 4 | 0 | 2 | 2 | 0 | M0127-P5.6-e-i, M0127-P5.9-o, take3-instrumentscope-datarace, e18-instrumentscope-global-races-coop-producers |
| internal/optimizer/joinpathsmemoize.go | 4 | 0 | 1 | 3 | 0 | M0127-P5.4b-ii-b-2, m0139-0007, m0139-0007b, m0142-0005 |
| family:take3-C | 4 | 0 | 2 | 2 | 0 | take3-C-09-declined, take3-C-04a-Q72-jointype-loss, take3-C-14-dropped, take3-C-20g-blocked |
| scripts/pg-plan-parity-diff.py | 4 | 0 | 0 | 4 | 0 | take3-plan-capture-is-serial-only, m0138-0005, m0142-0012-verify, m0142-0014 |
| internal/optimizer/createplanroot.go | 3 | 0 | 0 | 3 | 0 | take3-C-20h-var-migration, take3-C-20b-var-is-positional, take2-P6-05 |
| family:root-0023 | 3 | 0 | 0 | 0 | 3 | root-0023 |
| family:M-NIGHTLY | 3 | 0 | 1 | 0 | 2 | M-NIGHTLY, M-NIGHTLY-instrumentscope-race-fix |
| internal/access/nbtree/btree.go | 3 | 0 | 0 | 3 | 0 | C3-S2 review (S3 blockers), M0134-0001 backward min/max, M0134-0045 |
| family:csq-R2 | 3 | 0 | 1 | 1 | 1 | csq-R2 |
| family:root-0036 | 3 | 0 | 0 | 0 | 3 | root-0036 |
| internal/optimizer/exprwalk_inventory_test.go | 3 | 0 | 0 | 3 | 0 | M0125-0002, take2-P1-14b-patternsel, take2-R33-sublink-graft-classifiers |
| internal/postmaster/server.go | 3 | 0 | 0 | 3 | 0 | M0125-0002, M0134-0075, M0119-0006 |
| bench/tpch/run_power_test_goopg.sh | 3 | 0 | 0 | 3 | 0 | M0125-0003, M0125-0002 |
| internal/executor/operators_vacuum.go | 3 | 0 | 1 | 2 | 0 | M0125-0028, M0130-S11.6, M0134-0021 |
| internal/optimizer/exists_to_any.go | 3 | 0 | 0 | 3 | 0 | M0125-0036, M0125-0042 |
| family:M0126 | 3 | 0 | 0 | 0 | 3 | M0126-0004, M0126-0013 |
| internal/executor/executor.go | 3 | 0 | 0 | 3 | 0 | M0127-P5.6-e-i, take3-E-08-dropped, take3-E-14-cutA-cutB-deferred |
| internal/optimizer/collapse.go | 3 | 0 | 0 | 3 | 0 | M0127-P5.8, M0127-P5.7, take2-P3-01 |
| ci/batch/run-nightly.sh | 3 | 0 | 1 | 2 | 0 | M0127-P5.9 (run 2), M-NIGHTLY (AI-20260806-011323-002..-015), M-NIGHTLY AI-005117-001..-007 |
| internal/optimizer/joinsearchlevel.go | 3 | 0 | 0 | 3 | 0 | M0127-P5.9, M0127-P5.9-l-i, C-03c FULL-join-search-decline |
| internal/parser/with.go | 3 | 0 | 0 | 3 | 0 | M0125-0049, M0125-0051, take3-C-04a-Q78-firewall-classifier |
| internal/executor/operators_fk.go | 3 | 0 | 0 | 3 | 0 | M0125-0053, M0134-0005g FK twin probe, m0142-0003f |
| internal/optimizer/joinpaths.go | 3 | 0 | 1 | 2 | 0 | M0128-P0.1 Q74 attribution, c06-commuted-leftjoin-direction-withheld, M0142-0008a-3iii |
| cmd/gen-nailed-view-tables/main.go | 3 | 0 | 1 | 2 | 0 | M0131-S7, M0131-S18.4, take3-ea-ratchet-never-ran |
| internal/access/transam/xlog/checkpointer.go | 3 | 0 | 0 | 3 | 0 | M0131-S12, M0131-S18.2, M0131-S18.4 |
| internal/parser/lexer.go | 3 | 0 | 0 | 3 | 0 | M0134-0018, M0134-0030, M0134-0039 |
| internal/executor/operators_merge.go | 3 | 0 | 0 | 3 | 0 | M0134-0029, M0134-0044 |
| family:perf-optimize | 3 | 0 | 0 | 0 | 3 | perf-optimize-take3, perf-optimize-take3 candidate G |
| family:take2-P6 | 3 | 0 | 0 | 0 | 3 | take2-P6-03, take2-P6-04, take2-P6-06 |
| internal/optimizer/selectivity.go | 3 | 0 | 0 | 3 | 0 | take2-P1-16, take3-B-07-deferred, take3-B-19-noted |
| family:M0141 | 3 | 0 | 1 | 2 | 0 | m0141-s2a-fix2, m0141-s2b-0, M0141-S2b-14 |
| internal/optimizer/upperorderedgrouping.go | 3 | 0 | 0 | 3 | 0 | m0141-s2b-5, M0141-S7 (corpus measurement), M0141-S2b-7 |
| family:e10-gathermerge | 2 | 0 | 0 | 2 | 0 | e10-gathermerge-bitmap-untested-e2e, e10-gathermerge-open-error-paths |
| family:M0118 | 2 | 0 | 0 | 0 | 2 | M0118-0129, M0118-0130 |
| family:M0095 | 2 | 0 | 0 | 0 | 2 | M0095-0003 |
| bench/tpch/setup_goopg.sh | 2 | 0 | 0 | 2 | 0 | MAINT, M0125-0026 |
| family:M0097 | 2 | 0 | 0 | 0 | 2 | M0097-0040, M0097-0035 (pg_collation_for column) |
| internal/parser/interval.go | 2 | 0 | 0 | 2 | 0 | unimplemented_feat #5(b) (multi-field literals), M0134-0035 |
| internal/initdb/replication_views.go | 2 | 0 | 0 | 2 | 0 | M0122-0003 (pg_stat_ssl/gssapi), M0131-S7 |
| family:tpcds-round2 stddev | 2 | 0 | 0 | 0 | 2 | tpcds-round2 stddev-crash, tpcds-round2 stddev-precision |
| internal/initdb/relcache_init.go | 2 | 0 | 0 | 2 | 0 | root-0031, M0131-S6 |
| family:root-0031 | 2 | 0 | 0 | 0 | 2 | root-0031 |
| scripts/tpcds-value-diff.py | 2 | 0 | 0 | 2 | 0 | M0124-0001, M0124-0005 |
| internal/optimizer/exprwalk.go | 2 | 0 | 0 | 2 | 0 | M0125-0001, M0125-0024 |
| internal/utils/adt/datetime/normalize.go | 2 | 0 | 0 | 2 | 0 | M0125-0007, M0119-0006 |
| internal/optimizer/plan.go | 2 | 0 | 0 | 2 | 0 | M0125-0008, M0127-P4.3 |
| internal/optimizer/relsize.go | 2 | 0 | 0 | 2 | 0 | M0127-P5.9-r, M0127-P5.6 |
| internal/utils/misc/parser.go | 2 | 0 | 0 | 2 | 0 | M0125-0051, M0134-0021 |
| ci/batch/stages/stage-testport.sh | 2 | 0 | 0 | 2 | 0 | M0127-S7 (wedge attribution), M0127-S7 harness gap |
| ci/batch/stages/stage-pgbench.sh | 2 | 0 | 0 | 2 | 0 | M-NIGHTLY AI-20260810-011258-006 |
| internal/access/nbtree/posting.go | 2 | 0 | 0 | 2 | 0 | M0130-S11.4 (slice 2/3), M0131-S21b |
| internal/utils/misc/timestamptz_out.go | 2 | 0 | 0 | 2 | 0 | M0119-0006, M0134-0026 |
| internal/initdb/initdb.go | 2 | 0 | 0 | 2 | 0 | M0131-S2, M0133-S4 |
| internal/initdb/pgcontrol.go | 2 | 0 | 0 | 2 | 0 | M0131-S2, M0131-S17 |
| family:M0133 | 2 | 0 | 0 | 0 | 2 | M0133-S3, M0133-S4 |
| internal/testport/framework/regress.go | 2 | 0 | 0 | 2 | 0 | M0134-0001 es-indent model, M0134-0005s census correction |
| internal/executor/reg_identifier.go | 2 | 0 | 0 | 2 | 0 | M0134-0005a reg* scalars, testport/TestPort_IsolationIntraGrantInplace |
| internal/executor/session.go | 2 | 0 | 0 | 2 | 0 | M0134-0005f clone attrs forward, M0134-0005g named deferral |
| internal/executor/operators_utility_settings.go | 2 | 0 | 0 | 2 | 0 | M0134-0008 |
| internal/executor/operators_ddl_partition.go | 2 | 0 | 0 | 2 | 0 | M0134-0016 |
| internal/executor/pgstat_relations.go | 2 | 0 | 1 | 1 | 0 | M0134-0020, testport/TestPort_IsolationStats |
| family:take3-EX1 | 2 | 0 | 0 | 2 | 0 | take3-EX1-03-dropped, take3-EX1-04-blocked |
| internal/optimizer/joinsearch.go | 2 | 0 | 0 | 2 | 0 | take3-B-06-deferred, m0142-0003e |
| internal/executor/operators_index.go | 2 | 0 | 0 | 2 | 0 | take3-EX1-03-deferred-toast-indexonly, take3-C-20d-calibrated |
| internal/executor/operators.go | 2 | 0 | 0 | 2 | 0 | take3-B-01c-applying-blocked, M0141-S7-exec-a..d |
| internal/catalog/physical_align.go | 2 | 0 | 0 | 2 | 0 | take3-D-09-noted, M0143-0004 |
| internal/optimizer/joinkeyproof.go | 2 | 0 | 1 | 1 | 0 | take3-tpcds-rowest-3-to-5-orders, take3-C-10b-dropped |
| internal/optimizer/windowsetoppaths.go | 2 | 0 | 0 | 2 | 0 | spill-cut2-scope-corrected, M0141-S2b-3 |
| internal/optimizer/pathtrace.go | 2 | 0 | 0 | 2 | 0 | m0142-0003a, m0142-0003b |
| internal/optimizer/predp.go | 2 | 0 | 0 | 2 | 0 | m0142-0008a-3-iii, M0142-0008a-3i-plumbing-probe1 |
| scripts/estimate-parity/parity.py | 2 | 0 | 2 | 0 | 0 | M0142-0016c, M0142-0016d |
| scripts/tpcds-sf025-regression.sh | 2 | 0 | 1 | 1 | 0 | P0-E7 |
| internal/executor/spill.go | 1 | 0 | 0 | 1 | 0 | take3-D-10-out-of-scope |
| internal/executor/sort_spill_order_test.go | 1 | 0 | 0 | 1 | 0 | take3-E-01-fanin-deferred |
| family:e17-order | 1 | 0 | 1 | 0 | 0 | e17-order-qual-clauses-absent |
| internal/executor/parallel_scan_gather_test.go | 1 | 0 | 0 | 1 | 0 | e10-attachall-precondition-unenforced |
| internal/access/amcheck/bloomfilter.go | 1 | 0 | 0 | 1 | 0 | M0110-0003 |
| internal/access/transam/xlog/format.go | 1 | 0 | 0 | 1 | 0 | M0110-0002 |
| internal/executor/operators_bt_index_check.go | 1 | 0 | 0 | 1 | 0 | M0110-0003 |
| internal/initdb/encoding.go | 1 | 0 | 0 | 1 | 0 | M0119-0004 |
| internal/executor/cmdtag_table.go | 1 | 0 | 0 | 1 | 0 | M0119-0004 |
| internal/postmaster/statement_log.go | 1 | 0 | 0 | 1 | 0 | root-0023 |
| internal/executor/operators_explain_format.go | 1 | 0 | 0 | 1 | 0 | M0122-0003 |
| family:M0005 | 1 | 0 | 0 | 0 | 1 | M0005 |
| family:M0007 | 1 | 0 | 0 | 0 | 1 | M0007 |
| family:pgbench/nightly-reopen | 1 | 0 | 0 | 0 | 1 | pgbench/nightly-reopen-20260709 |
| family:M-NIGHTLY (5th loop) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (5th loop) |
| family:unimplemented_feat #5 (sub-day literals, parser) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (sub-day literals, parser) |
| family:unimplemented_feat #5 (fractional magnitudes) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (fractional magnitudes) |
| family:unimplemented_feat #5(c) (coarse units) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(c) (coarse units) |
| family:unimplemented_feat #5(d-i) (bare | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-i) (bare-number seconds) |
| family:unimplemented_feat m0097-0004 (justify folding) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat m0097-0004 (justify folding) |
| family:unimplemented_feat #5 (year-month hyphen) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (year-month hyphen) |
| family:unimplemented_feat #5(d-ii) (single | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-ii) (single-letter units) |
| family:unimplemented_feat #5(d-iii) (glued + field | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iii) (glued + field-mask) |
| family:unimplemented_feat #5 (ISO 8601 durations) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (ISO 8601 durations) |
| family:unimplemented_feat #5 (year-month quirks) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (year-month quirks) |
| family:unimplemented_feat #5 (glued unit absorption) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (glued unit absorption) |
| family:unimplemented_feat #5 (signed year-month glued) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (signed year-month glued) |
| family:unimplemented_feat #5 (+continuation, sign lexer) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5 (+continuation, sign lexer) |
| family:unimplemented_feat #5(d-iv) (typmod ranges) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (typmod ranges) |
| family:unimplemented_feat #5(d-iv) (trailing bare + range) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (trailing bare + range) |
| family:unimplemented_feat #5(d-iv) (leftward DAY carry) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (leftward DAY carry) |
| family:unimplemented_feat #5(d-iv) (+/ | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (+/-infinity literals) |
| family:unimplemented_feat #5(d-iv) (infinity ADD/SUB) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (infinity ADD/SUB) |
| family:unimplemented_feat #5(d-iv) (EXTRACT interval) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (EXTRACT interval) |
| family:unimplemented_feat #5(d-iv) (unary minus interval) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (unary minus interval) |
| family:unimplemented_feat #5(d-iv) (EXTRACT scale) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (EXTRACT scale) |
| family:unimplemented_feat #5(d-iv) (EPOCH value fix) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (EPOCH value fix) |
| family:unimplemented_feat #5(d-iv) (epoch overflow) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (epoch overflow) |
| family:unimplemented_feat #5(d-iv) (cast | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (cast-form typmod) |
| family:unimplemented_feat #5(d-iv) (precision literal) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (precision literal) |
| family:unimplemented_feat #5(d-iv) (ts±inf) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (ts±inf) |
| family:unimplemented_feat #5(d-iv) (isfinite over sentinels) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (isfinite over sentinels) |
| family:unimplemented_feat #5(d-iv) (date infinity literal) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #5(d-iv) (date infinity literal) |
| family:PGLZ varlena compression parity | 1 | 0 | 0 | 0 | 1 | PGLZ varlena compression parity |
| family:unimplemented_feat #151 (TOAST compress-on | 1 | 0 | 0 | 0 | 1 | unimplemented_feat #151 (TOAST compress-on-write) |
| family:unimplemented_feat (catalog-xmin retention hook) | 1 | 0 | 0 | 0 | 1 | unimplemented_feat (catalog-xmin retention hook) |
| family:M-NIGHTLY (tuplelock iso flake) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (tuplelock iso flake) |
| family:M-NIGHTLY (#89 correction) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (#89 correction) |
| family:perf-optimize2 fix | 1 | 0 | 0 | 0 | 1 | perf-optimize2 fix-03 (safe items) |
| internal/access/transam/subxact_slru.go | 1 | 0 | 0 | 1 | 0 | C2 clog-commit-fsync (S3/S4) |
| family:C3-S3 review (subxact CLOG lanes) | 1 | 0 | 0 | 0 | 1 | C3-S3 review (subxact CLOG lanes) |
| family:C3-S5 soak closure | 1 | 0 | 0 | 0 | 1 | C3-S5 soak closure |
| family:08 bundle incremental implementation | 1 | 0 | 0 | 0 | 1 | 08 bundle incremental implementation |
| family:08 doc 03 C3 kill-migration S1 | 1 | 0 | 0 | 0 | 1 | 08 doc 03 C3 kill-migration S1 |
| family:M-NIGHTLY (regress divergence triage) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (regress divergence triage) |
| family:M-NIGHTLY (baseline refresh) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (baseline refresh) |
| family:M-NIGHTLY (quick | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (quick-suite heuristic retired) |
| family:M-NIGHTLY (startup | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (startup-packet GUC application) |
| family:M-NIGHTLY (DATE style + COPY error) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (DATE style + COPY error) |
| family:M-NIGHTLY (timestamp DateStyle output) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (timestamp DateStyle output) |
| family:M-NIGHTLY (FormatTimestamp fraction trim) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (FormatTimestamp fraction trim) |
| family:M-NIGHTLY (evalCast CAST | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (evalCast CAST-to-text DateStyle) |
| family:M-NIGHTLY (ANALYZE MCV DateStyle) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (ANALYZE MCV DateStyle) |
| family:M-NIGHTLY (plpgsql RAISE DateStyle) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (plpgsql RAISE DateStyle) |
| family:M-NIGHTLY (timeout triage, env) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (timeout triage, env) |
| family:WAL native-PG rework §5.4 slice 4 atomic (R1) | 1 | 0 | 0 | 0 | 1 | WAL native-PG rework §5.4 slice 4 atomic (R1) |
| family:A7-freeze (HeapFreeze→xl_heap_prune) | 1 | 0 | 0 | 0 | 1 | A7-freeze (HeapFreeze→xl_heap_prune) |
| family:A7 heap2 xl_heap_prune flip (prune/freeze kinds) | 1 | 0 | 0 | 0 | 1 | A7 heap2 xl_heap_prune flip (prune/freeze kinds) |
| family:A8 btree structural records→RM_BTREE (split landed) | 1 | 0 | 0 | 0 | 1 | A8 btree structural records→RM_BTREE (split landed) |
| family:A8 xl_btree_unlink_page / mark_page_halfdead | 1 | 0 | 0 | 0 | 1 | A8 xl_btree_unlink_page / mark_page_halfdead |
| family:A8-rest finding: isolation suite false | 1 | 0 | 0 | 1 | 0 | A8-rest finding: isolation suite false-green |
| family:A9 tail: smgr/clog/checkpoint/inval/legacy-frame | 1 | 0 | 0 | 0 | 1 | A9 tail: smgr/clog/checkpoint/inval/legacy-frame |
| family:Phase-A exit: standby replay blocked on FPI unification | 1 | 0 | 0 | 0 | 1 | Phase-A exit: standby replay blocked on FPI unification |
| internal/access/transam/xlog/relmap.go | 1 | 0 | 0 | 1 | 0 | B0.4 relmap writer + RELMAP_UPDATE (deferred by design) |
| family:testport intermittent crawl (bisect exonerates slices) | 1 | 0 | 0 | 0 | 1 | testport intermittent crawl (bisect exonerates slices) |
| family:crawl root cause: leaked orphan server spins at 70% CPU | 1 | 0 | 0 | 1 | 0 | crawl root cause: leaked orphan server spins at 70% CPU |
| family:B3 remaining kinds (69/94/95-99/102 | 1 | 0 | 0 | 0 | 1 | B3 remaining kinds (69/94/95-99/102-103) blocker survey |
| internal/executor/sys_pg_tablespace.go | 1 | 0 | 0 | 1 | 0 | B4.1 pg_tablespace feasibility probe |
| family:B4.1 pg_tablespace LANDED (L3) — kinds 124/125 retired | 1 | 0 | 0 | 0 | 1 | B4.1 pg_tablespace LANDED (L3) — kinds 124/125 retired |
| family:B4.2 pg_db_role_setting (kinds 73-78) — 2nd B4 slice | 1 | 0 | 0 | 0 | 1 | B4.2 pg_db_role_setting (kinds 73-78) — 2nd B4 slice |
| family:parser gap: ALTER DATABASE/ROLE SET not in main parser | 1 | 0 | 0 | 0 | 1 | parser gap: ALTER DATABASE/ROLE SET not in main parser |
| family:B4.3 pg_auth_members (kinds 79/80) — 3rd B4 slice | 1 | 0 | 0 | 0 | 1 | B4.3 pg_auth_members (kinds 79/80) — 3rd B4 slice |
| family:B4.4 pg_subscription (kinds 53-55) — pub/sub closed | 1 | 0 | 0 | 0 | 1 | B4.4 pg_subscription (kinds 53-55) — pub/sub closed |
| family:B4.5 pg_authid + role-state (67/68/72) — boot | 1 | 0 | 0 | 0 | 1 | B4.5 pg_authid + role-state (67/68/72) — boot-critical |
| family:B4.6 STAGE 1 pg_database heap row LANDED | 1 | 0 | 0 | 0 | 1 | B4.6 STAGE 1 pg_database heap row LANDED |
| family:B4.6 STAGE 3b WAL_LOG FPIs — standby-validated, B4 complete | 1 | 0 | 0 | 0 | 1 | B4.6 STAGE 3b WAL_LOG FPIs — standby-validated, B4 complete |
| internal/initdb/index_ddl_recovery_test.go | 1 | 0 | 0 | 1 | 0 | B5 SLICE A index 20/21/94 RETIRED — standby-validated |
| family:B5 SLICE B pg_attrdef 69 RETIRED — corrects reverted row | 1 | 0 | 0 | 0 | 1 | B5 SLICE B pg_attrdef 69 RETIRED — corrects reverted row |
| family:B5 Bstat statistics 95-99 RETIRED (heap | 1 | 0 | 0 | 0 | 1 | B5 Bstat statistics 95-99 RETIRED (heap-backed) |
| family:B5 Bstat follow-ups to 7059ce68 — two root causes fixed | 1 | 0 | 0 | 0 | 1 | B5 Bstat follow-ups to 7059ce68 — two root causes fixed |
| family:B5 Slice C view/matview 102/103 RETIRED — final group | 1 | 0 | 0 | 0 | 1 | B5 Slice C view/matview 102/103 RETIRED — final group |
| family:B5 delete-rmgr cleanup — rmid | 1 | 0 | 0 | 0 | 1 | B5 delete-rmgr cleanup — rmid-128 retired from emitted stream |
| family:02e item A matview IsPopulated across restart RESOLVED | 1 | 0 | 0 | 0 | 1 | 02e item A matview IsPopulated across restart RESOLVED |
| family:02e item B ALTER VIEW/TABLE RENAME across restart RESOLVED | 1 | 0 | 0 | 1 | 0 | 02e item B ALTER VIEW/TABLE RENAME across restart RESOLVED |
| family:M-NIGHTLY race in internal/wal TestDrainSafetyStress RESOLVED | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY race in internal/wal TestDrainSafetyStress RESOLVED |
| family:nightly isolation prepared-txns flake demoted (timing) | 1 | 0 | 0 | 0 | 1 | nightly isolation prepared-txns flake demoted (timing) |
| family:S2 part (a) FuncExpr resolution validated; red test fixed | 1 | 0 | 0 | 0 | 1 | S2 part (a) FuncExpr resolution validated; red test fixed |
| family:S4 sub-slice 4a implicit int→numeric cast FuncExpr | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 4a implicit int→numeric cast FuncExpr |
| family:S4 sub-slice 4b canonical timestamptz Const datums | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 4b canonical timestamptz Const datums |
| family:S4 sub-slice 5 BooleanTest (IS TRUE/FALSE/UNKNOWN) scalar | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 5 BooleanTest (IS TRUE/FALSE/UNKNOWN) scalar |
| family:S4 sub-slice 6 view | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 6 view-query BooleanTest wiring |
| family:S4 sub-slice 7 searched | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 7 searched-form CaseExpr scalar |
| family:S4 sub-slice 9 DistinctExpr scalar node | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 9 DistinctExpr scalar node |
| family:S4 sub-slice 11 IS DISTINCT FROM NULL → NullTest rewrite | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 11 IS DISTINCT FROM NULL → NullTest rewrite |
| family:S4 sub-slice 12 CASE simple form (CaseTestExpr) | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 12 CASE simple form (CaseTestExpr) |
| family:S4 sub-slice 13 CASE cross | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 13 CASE cross-type coercion (select_common_type) |
| family:S4 sub-slice 14 CASE int4→int8 width coercion | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 14 CASE int4→int8 width coercion |
| family:S4 sub-slice 15 CASE float4→float8 coercion | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 15 CASE float4→float8 coercion |
| family:S4 sub-slice 16 unified cross | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 16 unified cross-family → float8 |
| family:S4 sub-slice 17 simple | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 17 simple-CASE numeric-operand coercion |
| family:S4 sub-slice 18 native cross | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 18 native cross-type operator in simple CASE |
| family:S4 sub-slice 19 explicit integer ::type cast | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 19 explicit integer ::type cast |
| family:S4 sub-slice 20 explicit numeric↔integer ::type cast | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 20 explicit numeric↔integer ::type cast |
| family:S4 sub-slice 21 explicit float | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 21 explicit float-family ::type cast |
| family:S4 sub-slice 22 explicit ::numeric(p,s) typmod cast | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 22 explicit ::numeric(p,s) typmod cast |
| family:S4 sub-slice 23 implicit numeric column length coercion | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 23 implicit numeric column length coercion |
| family:S4 sub-slice 24 bare | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 24 bare-numeric-column RelabelType strip |
| family:S4 sub-slice 25 explicit bare | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 25 explicit bare-numeric cast RelabelType |
| family:S4 sub-slice 26 canonical date OID | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 26 canonical date OID-1082 datums |
| family:S4 sub-slice 27 explicit ::date/::timestamptz string cast | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 27 explicit ::date/::timestamptz string cast |
| family:S4 sub-slice 28 string | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 28 string-literal folds to bool/int2/int4/int8 |
| family:S4 sub-slice 29b numeric specials NaN/±Infinity fold | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 29b numeric specials NaN/±Infinity fold |
| family:S4 sub-slice 29c string folds to oid/float4/float8 | 1 | 0 | 0 | 0 | 1 | S4 sub-slice 29c string folds to oid/float4/float8 |
| family:M-NIGHTLY failover zero | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY failover zero-loss flake (sync_remote_apply/on) |
| family:csq-R2 S7 | 1 | 0 | 0 | 0 | 1 | csq-R2 S7 |
| family:0009-readstream | 1 | 0 | 0 | 0 | 1 | 0009-readstream |
| family:tpcds-round2 Q8 | 1 | 0 | 0 | 0 | 1 | tpcds-round2 Q8 |
| analysis/wal-crash-restart-repro.sh | 1 | 0 | 0 | 1 | 0 | root-0032 |
| family:root-0033 | 1 | 0 | 0 | 0 | 1 | root-0033 |
| family:root-0034 | 1 | 0 | 0 | 0 | 1 | root-0034 |
| family:root-0034 / M | 1 | 0 | 0 | 1 | 0 | root-0034 / M-NIGHTLY |
| family:root-0035 | 1 | 0 | 0 | 1 | 0 | root-0035 |
| internal/optimizer/exprkey.go | 1 | 0 | 0 | 1 | 0 | M0125-0009 |
| family:tpcds-round2 in | 1 | 0 | 0 | 0 | 1 | tpcds-round2 in-list-common-type |
| family:tpcds-round2 scaninput | 1 | 0 | 0 | 0 | 1 | tpcds-round2 scaninput-reorder |
| family:tpcds-round2 smalldim | 1 | 0 | 0 | 0 | 1 | tpcds-round2 smalldim-gate |
| family:tpcds-round2 grouping | 1 | 0 | 0 | 0 | 1 | tpcds-round2 grouping-sets-operator |
| family:tpcds-round2 exists | 1 | 0 | 0 | 0 | 1 | tpcds-round2 exists-under-or |
| family:tpcds-round2 setop | 1 | 0 | 0 | 0 | 1 | tpcds-round2 setop-parallel |
| family:tpcds-round2 plancache | 1 | 0 | 0 | 0 | 1 | tpcds-round2 plancache-analyze |
| family:tpcds-round2 posmap | 1 | 0 | 0 | 0 | 1 | tpcds-round2 posmap-assert |
| family:tpcds-round2 panic | 1 | 0 | 0 | 0 | 1 | tpcds-round2 panic-to-xx000 |
| family:tpcds-round2 q47 | 1 | 0 | 0 | 0 | 1 | tpcds-round2 q47-q49-q51 |
| scripts/tpcds-result-checksum.py | 1 | 0 | 0 | 1 | 0 | M0124-0005 |
| internal/parser/token.go | 1 | 0 | 0 | 1 | 0 | M0125-0018 |
| internal/executor/bytea.go | 1 | 0 | 0 | 1 | 0 | M0125-0021 |
| analysis/m0124-0002/run-stream.sh | 1 | 0 | 0 | 1 | 0 | M0124-0002 |
| internal/optimizer/pushdown.go | 1 | 0 | 0 | 1 | 0 | M0125-0012 |
| family:M0125 (BANNER ITEM 4) | 1 | 0 | 0 | 0 | 1 | M0125 (banner item 4) |
| scripts/tpch-relsize-arm.sh | 1 | 0 | 0 | 1 | 0 | M0125-0003 |
| internal/executor/analyze_dbid_routing_test.go | 1 | 0 | 0 | 1 | 0 | M0125-0028 |
| internal/executor/subplan_hash.go | 1 | 0 | 0 | 1 | 0 | M0125-0036 |
| internal/optimizer/small_dimension.go | 1 | 0 | 0 | 1 | 0 | M0125-0043 |
| internal/executor/cte_scalar_sublink_unnest_test.go | 1 | 0 | 0 | 1 | 0 | M0125-0041 |
| internal/optimizer/groupby_alias_collapse_test.go | 1 | 0 | 0 | 1 | 0 | M0125-0044 |
| internal/optimizer/inner_join_qual_pushdown.go | 1 | 0 | 0 | 1 | 0 | M0125-0035 |
| analysis/m0125-0047/capture-plans.sh | 1 | 0 | 0 | 1 | 0 | M0125-0002 |
| analysis/m0125-0047/probe-q85-restarts.sh | 1 | 0 | 0 | 1 | 0 | M0125-0047 |
| internal/executor/join_composite_key.go | 1 | 0 | 0 | 1 | 0 | M0127-P2.2 |
| internal/executor/parallel_worker_ctx.go | 1 | 0 | 0 | 1 | 0 | M0127-P3.5 |
| internal/executor/operator.go | 1 | 0 | 0 | 1 | 0 | M0127-P4.1 |
| internal/optimizer/pathgen.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.5-d |
| internal/optimizer/searchedtree.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.5-f-ii-a |
| internal/optimizer/foldconst.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.7-b |
| internal/testutil/tpch/parity_test.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.9-d |
| internal/optimizer/joinsearchunnestgroupkey_test.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.9-g |
| internal/optimizer/pathkeys.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.9-j |
| internal/optimizer/joinsearchtrace.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.9-l-ii |
| internal/executor/join_batch_explain_test.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.9 |
| internal/optimizer/flaglabels.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.9-q |
| internal/optimizer/collapse_corpus_test.go | 1 | 0 | 0 | 1 | 0 | M0127-P5.9-m |
| internal/executor/join_merge_stream.go | 1 | 0 | 0 | 1 | 0 | M0127-PS6.1 |
| internal/executor/storage_ddl_test.go | 1 | 0 | 0 | 1 | 0 | M-NIGHTLY (AI-20260806-011323-018) |
| internal/testport/regress_suite_test.go | 1 | 0 | 0 | 1 | 0 | M-NIGHTLY wedge casualties |
| family:M-NIGHTLY wedge probe | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY wedge probe |
| family:M-NIGHTLY latch exposure | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY latch exposure |
| internal/optimizer/joinlayout.go | 1 | 0 | 1 | 0 | 0 | M0127-P6.3 |
| family:M0128 | 1 | 0 | 0 | 0 | 1 | M0128-P4.1 reduce_outer_joins |
| internal/parser/ast.go | 1 | 0 | 0 | 1 | 0 | M0129-S9.4 RIGHT/FULL flips |
| family:M0129 | 1 | 0 | 0 | 0 | 1 | M0129-S5.8 getBitmap |
| internal/optimizer/relfromjoinlist.go | 1 | 0 | 0 | 1 | 0 | M0119-0011 transitive equalities |
| internal/executor/sys_pg_ts_config.go | 1 | 0 | 0 | 1 | 0 | M0119-0004 TS config restart |
| family:AI-007 self | 1 | 0 | 0 | 0 | 1 | AI-007 self-join lock |
| internal/access/transam/xlog/archive_restore.go | 1 | 0 | 0 | 1 | 0 | M0130-S9 archive recovery |
| internal/access/amcheck/verify_nbtree_unique.go | 1 | 0 | 0 | 1 | 0 | M0119-0006 checkunique tier |
| family:M-NIGHTLY LINE restore | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY LINE restore |
| family:M-NIGHTLY moved | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY moved-tuple cmax |
| internal/utils/mmgr/mctx_test.go | 1 | 0 | 0 | 1 | 0 | M-NIGHTLY AI-20260810-011258-001 |
| family:M-NIGHTLY (race/internal/mctx) | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY (race/internal/mctx) |
| internal/initdb/pg_proc_seed_defaults.go | 1 | 0 | 0 | 1 | 0 | M-NIGHTLY AI-20260810-011258-003 |
| internal/access/transam/xlog/timeline_history.go | 1 | 0 | 0 | 1 | 0 | M-NIGHTLY AI-20260810-011258-003 |
| internal/access/nbtree/pgformat.go | 1 | 0 | 0 | 1 | 0 | M0130-S11.1 |
| internal/executor/ssi.go | 1 | 0 | 0 | 1 | 0 | M0130-S11.4 (slice 3b-2c-ii-B2-c-iii) |
| internal/access/nbtree/pgsplitleft.go | 1 | 0 | 0 | 1 | 0 | M0130-S11.5b-2 |
| internal/nodes/numeric_storage.go | 1 | 0 | 0 | 1 | 0 | M0119-0006 |
| internal/executor/btree_array_key.go | 1 | 0 | 0 | 1 | 0 | M0119-0006 |
| bench/reindex_cluster.sh | 1 | 0 | 0 | 1 | 0 | M-NIGHTLY AI-20260811-014635-012 |
| internal/utils/adt/datetime/era.go | 1 | 0 | 0 | 1 | 0 | M0119-0006 |
| internal/executor/copy_text.go | 1 | 0 | 0 | 1 | 0 | M0119-0006 |
| internal/utils/adt/datetime/monthname.go | 1 | 0 | 0 | 1 | 0 | M0119-0006 |
| internal/utils/misc/guc.go | 1 | 0 | 0 | 1 | 0 | M0131-S1 |
| internal/initdb/config_seed_test.go | 1 | 0 | 0 | 1 | 0 | M0131-S1 |
| internal/initdb/reverse_path_test.go | 1 | 0 | 0 | 1 | 0 | M0131-S10.5 |
| internal/initdb/system_view_oid_pins.go | 1 | 0 | 0 | 1 | 0 | M0131-S8a |
| scripts/capture-ev-action.sh | 1 | 0 | 0 | 1 | 0 | M0131-S9.0 |
| internal/initdb/btree_index_bootstrap.go | 1 | 0 | 0 | 1 | 0 | M0131-S12 |
| internal/storage/smgr.go | 1 | 0 | 0 | 1 | 0 | M0131-S16 |
| internal/backup/basebackup.go | 1 | 0 | 0 | 1 | 0 | M0131-S20.5 |
| analysis/idxprobe.sh | 1 | 0 | 0 | 1 | 0 | M0131-S30 |
| analysis/crashprobe30.sh | 1 | 0 | 0 | 1 | 0 | M0131-S30 |
| analysis/hotstall.sh | 1 | 0 | 0 | 1 | 0 | M0131-S30.8 / S32 |
| analysis/concurrent-hotrow.sh | 1 | 0 | 0 | 1 | 0 | M0131-S32.1 |
| internal/executor/s321_probe.go | 1 | 0 | 0 | 1 | 0 | M0131-S32.3 |
| internal/storage/vm_redo.go | 1 | 0 | 0 | 1 | 0 | M0131-S21a-2 |
| internal/access/transam/xlog/heap_update_pg_test.go | 1 | 0 | 0 | 1 | 0 | M0131-S21d |
| internal/storage/prune.go | 1 | 0 | 0 | 1 | 0 | M0131-S27 |
| internal/access/transam/visibility.go | 1 | 0 | 0 | 1 | 0 | M0131-S24 |
| internal/access/transam/xlog/pgoutput.go | 1 | 0 | 0 | 1 | 0 | M0119-0006 |
| family:M-NIGHTLY 0811 | 1 | 0 | 0 | 0 | 1 | M-NIGHTLY 0811-001/0812-003/0813-016 |
| internal/testutil/pubsubcluster/cluster.go | 1 | 0 | 0 | 1 | 0 | M-NIGHTLY AI-20260817-011734-005 pubsub wait |
| internal/postmaster/conn_tx.go | 1 | 0 | 0 | 1 | 0 | M0134-0001 SET rollback |
| internal/executor/pg18_user_catalog_rows.go | 1 | 0 | 0 | 1 | 0 | M0134-0004 table owner stamp |
| internal/postmaster/database_ddl.go | 1 | 0 | 0 | 1 | 0 | M0134-0004 owner omissions |
| internal/executor/context.go | 1 | 0 | 0 | 1 | 0 | M0134-0005b extended drain gap |
| internal/executor/deferred_unique.go | 1 | 0 | 0 | 1 | 0 | M0134-0005b snapshot policy |
| internal/testport/ddl_scan_abort_liveness_test.go | 1 | 0 | 0 | 1 | 0 | M0134-0005c prune question |
| internal/testport/deferred_unique_hot_chain_e2e_test.go | 1 | 0 | 0 | 1 | 0 | M0134-0005e multi-hop test |
| internal/executor/applyworker.go | 1 | 0 | 0 | 1 | 0 | M0134-0005j applyworker defaults |
| internal/executor/operators_generated.go | 1 | 0 | 0 | 1 | 0 | M0134-0005m select defaults |
| internal/executor/operators_reindex.go | 1 | 0 | 0 | 1 | 0 | AI-20260819-011823-001 |
| internal/catalog/partition_detach_visibility_test.go | 1 | 0 | 0 | 1 | 0 | M0134-0005aq |
| internal/optimizer/notnull_qual_reduce.go | 1 | 0 | 0 | 1 | 0 | M0134-0010 |
| internal/optimizer/predicate_implication.go | 1 | 0 | 0 | 1 | 0 | M0134-0017 |
| internal/postmaster/query.go | 1 | 0 | 0 | 1 | 0 | M0134-0018 |
| internal/executor/copy_csv.go | 1 | 0 | 0 | 1 | 0 | M0134-0031 |
| internal/executor/unistr.go | 1 | 0 | 0 | 1 | 0 | M0134-0070 |
| scripts/pg-oracle-diff.sh | 1 | 0 | 0 | 1 | 0 | M0134-0070 |
| internal/executor/opnode.go | 1 | 0 | 0 | 1 | 0 | M0134-0073 |
| family:P7.2-testport | 1 | 0 | 0 | 0 | 1 | P7.2-testport-concurrency |
| family:P7.2-per | 1 | 0 | 0 | 0 | 1 | P7.2-per-fragment-routing |
| family:P7.3-regress | 1 | 0 | 0 | 0 | 1 | P7.3-regress-baseline-recheck |
| family:take2-executor | 1 | 0 | 0 | 0 | 1 | take2-executor-residual |
| family:take2-P1 | 1 | 0 | 0 | 0 | 1 | take2-P1-10 |
| family:take3-EX3 | 1 | 0 | 0 | 1 | 0 | take3-EX3-03-step2-blocked |
| family:take3-wrapup | 1 | 0 | 0 | 1 | 0 | take3-wrapup-deferred |
| family:take3-stats | 1 | 0 | 0 | 0 | 1 | take3-stats-persistence-gap |
| family:take3-drift | 1 | 0 | 0 | 0 | 1 | take3-drift-method-fix |
| internal/executor/operators_bitmap.go | 1 | 0 | 0 | 1 | 0 | take3-EX1-03-deferred-toast-bitmap |
| internal/optimizer/cte_inline_pushdown.go | 1 | 0 | 0 | 1 | 0 | take3-C-02c-noted |
| internal/executor/scan_prefilter.go | 1 | 0 | 0 | 1 | 0 | take3-E-04-dropped |
| family:take3-F | 1 | 0 | 0 | 1 | 0 | take3-F-03-dropped |
| internal/optimizer/entrywidth.go | 1 | 0 | 0 | 1 | 0 | take3-D-05-costside-unnarrowed |
| family:take3-bench | 1 | 0 | 0 | 0 | 1 | take3-bench-wal-early-end |
| internal/optimizer/joinpathsmerge.go | 1 | 0 | 0 | 1 | 0 | take3-C-08-noted |
| internal/storage/aio/read_stream.go | 1 | 0 | 1 | 0 | 0 | take3-E-11-readstream-declined |
| family:take3-autovacuum | 1 | 0 | 0 | 1 | 0 | take3-autovacuum-on-does-not-break-the-pin |
| family:tpcds-timing | 1 | 0 | 0 | 0 | 1 | tpcds-timing-concurrent-capture |
| internal/executor/e07_worker_dispatch_bench_test.go | 1 | 0 | 0 | 1 | 0 | take3-E-07-dropped |
| analysis/minimize-datum/tracke-e07-e13-e14-e09c-20260907/census_aggregate.py | 1 | 0 | 0 | 1 | 0 | take3-E-13-dropped |
| internal/executor/scan_deform.go | 1 | 0 | 0 | 1 | 0 | take3-E-14-cutA-dropped-cutB-quantified |
| family:take3-E | 1 | 0 | 1 | 0 | 0 | take3-E-09c-consumer-bound |
| internal/optimizer/joinrestrict.go | 1 | 0 | 0 | 1 | 0 | c04c-nested-outer-refilters-lower-on-qual |
| family:c04c-inner | 1 | 0 | 1 | 0 | 0 | c04c-inner-on-qual-above-outer-declines |
| family:spill-cut3 | 1 | 0 | 0 | 1 | 0 | spill-cut3-deferred |
| family:c19g-debug | 1 | 0 | 0 | 1 | 0 | c19g-debug-parallel-query-unread |
| family:c19g-parallel | 1 | 0 | 0 | 0 | 1 | c19g-parallel-plans-raise-peak-memory |
| family:c20a-two | 1 | 0 | 0 | 1 | 0 | c20a-two-estimators-still-two |
| family:R63-#3 | 1 | 0 | 1 | 0 | 0 | R63-#3-memoize-rightjoin-drops-null-extension |
| family:take2-R81 | 1 | 0 | 0 | 1 | 0 | take2-R81-q4-sort-blocked-on-inputs |
| family:M0138 | 1 | 0 | 0 | 1 | 0 | m0138-0006 |
| internal/optimizer/joinpathsparallel.go | 1 | 0 | 1 | 0 | 0 | m0140-0005-q14-parallel-hash-execution-model |
| family:M0140 | 1 | 0 | 0 | 1 | 0 | m0140-0005-nonplanner-heap-density-floor |
| family:M0139 | 1 | 0 | 0 | 1 | 0 | m0139-0007a |
| family:M0137 | 1 | 0 | 0 | 1 | 0 | m0137-0017-parallel-mode-divergence |
| bench/tpcds/env_tpcds.sh | 1 | 0 | 0 | 1 | 0 | m0137-0020-recapture-headline-unowned |
| internal/testport/mergejoin_all_clauses_test.go | 1 | 0 | 1 | 0 | 0 | m0142-0003j |
| internal/testutil/tpch/tpch_run_test.go | 1 | 0 | 0 | 1 | 0 | m0141-s2b-6 |
| internal/executor/explain_names.go | 1 | 0 | 0 | 1 | 0 | M0142-0008a-3(i) |
| internal/optimizer/createuniquepath.go | 1 | 0 | 0 | 1 | 0 | M0142-0008c-1 |
| internal/executor/subplan.go | 1 | 0 | 0 | 1 | 0 | M0141-S7-exec-b |
| family:P0-0918 | 1 | 0 | 0 | 1 | 0 | P0-0918-audit |
| scripts/tpch-acceptance-arm.sh | 1 | 0 | 0 | 1 | 0 | P0-E7 |
| internal/executor/parallel_scan.go | 1 | 0 | 0 | 1 | 0 | M0140-0006c |
| internal/optimizer/parallel.go | 1 | 0 | 1 | 0 | 0 | M0141-S2b-15 |
| family:testport/TestE2E_PGColdStartOnGoopgDataDir | 1 | 0 | 0 | 1 | 0 | testport/TestE2E_PGColdStartOnGoopgDataDir |
| internal/executor/operators_lockrows.go | 1 | 0 | 0 | 1 | 0 | testport/TestPort_IsolationIntraGrantInplace |
| family:testport/TestPort_IsolationSuite | 1 | 0 | 0 | 1 | 0 | testport/TestPort_IsolationSuite |
| family:AI-007 | 1 | 0 | 0 | 1 | 0 | AI-007-resolution-record |
| internal/catalog/extension_perdb_test.go | 1 | 0 | 0 | 1 | 0 | M0119-0006-resolution-record |
