(idle — M0102-0009 resolved & committed this loop)

Loop #33 closed **M0102-0009** (PG↔goopg physical failover `/sync_remote_apply`
"did not reach streaming state within 45s"). The failure no longer reproduces —
it was fixed by the `sync_state` wiring (design 0105-0008, real FIRST/ANY rule
in `registerStatReplicationView`). Empirically verified all modes pass:
- `TestE2E_FailoverPGtoGoopg` async / sync_remote_apply / sync_on — PASS (29.25s)
- `TestE2E_FailoverGoopgToPG` async / sync_remote_apply — PASS (5.97s)

Change was test-gating only (no production code): removed the
`GOOPG_RUN_BLOCKED_M0102_E2E` opt-in gate from both failover test files; they now
follow the standard heterogeneous-E2E convention (skip under `-short` or
`GOOPG_SKIP_M0102_E2E=1`), matching e2e_replication_test.go. Also gofmt-fixed a
pre-existing misformat (line ~320) in e2e_failover_pg_to_goopg_test.go.
- Files: internal/testport/e2e_failover_pg_to_goopg_test.go,
  internal/testport/e2e_failover_goopg_to_pg_test.go,
  docs/design/0102-0003-heterogeneous-failover-e2e-harness.md (closure note),
  .ralph/fix_plan.md (M0102-0009 → [x]).
- Gates: gofmt clean; go vet ./internal/testport PASS; both failover E2E suites
  PASS un-gated; make ralph-state-guard OK (had to reset stale
  progress.json "completed"→"running" to match live status.json).

Next candidate tasks (pick one next loop):
- **M0102-0010 gate (b)**: standby-read / physical-replication validation for
  the `--data-checksums` default-ON flip — a checksummed goopg primary streaming
  to a PG standby that verifies pd_checksum. Now UNBLOCKED since physical
  failover/streaming works (M0102-0009 closed). This is the last gate before the
  one-line default flip.
- M0095-0003 WAL-streaming tier (pg_basebackup -X stream) — needs walsender loop
  parity.
- M0110-0001..0004 server-dependent TAP tiers.

⚠️ WORKING-TREE CONTAMINATION (separate session, DO NOT commit): ~18 modified
files + 2 new test files belong to an UNRELATED partition generated-column-
override feature: analyzer/analyzer.go, catalog/catalog.go(+_test),
executor/* (operators.go, operators_ddl.go, operators_join_agg.go,
operators_lockrows.go, opnode.go), mvcc/subxact_visibility.go, parser/ast.go,
parser/ddl.go, planner/* (bushy, nl_index_join, plan, planner, unnest),
server/dispatch.go + parser/gen_override_test.go +
executor/partition_gen_override_test.go. Stage ONLY M0102-0009 files below.
