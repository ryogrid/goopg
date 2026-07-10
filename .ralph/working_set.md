(idle — nothing in flight)

## Loop summary (2026-07-10, loop #5)

**Outcome: M0122-0007 4e follow-up 41 — extended `CREATE DATABASE ...
TEMPLATE`'s relation-copy mechanism (follow-up 40) to also cover
sequences — implemented, independently verified, committed.**

- Nightly triage: sole action item AI-20260710-011513-001 (build failure)
  already had a closed M-NIGHTLY task (follow-up 14); `go build ./...`
  clean at loop start — no new M-NIGHTLY task needed.
- Picked the next M0122-0007 4e resume point per follow-up 40's own
  deferral row: extend TEMPLATE copy to sequences (smaller/independent
  slice vs. the index/view/matview/typed-table cases, since sequences need
  no relation file and no per-database sys-btree bootstrap).
- Researched sequence subsystem (own reads + a background research agent,
  cross-checked): sequence durable state is a process-global registry
  (`internal/executor` `seqRegistry`), not heap-backed — clone mechanism
  = `RestoreSequenceFromWAL` (already exists, used by WAL replay).
- **Real bug caught by the E2E test, not the unit tests:** first attempt
  detected sequences via the same `tmpl.AllTables(oid)` walk plain tables
  use, but `AllTables` always skips `Virtual` rows and every real
  sequence's pg_class relation IS `Virtual` — unit tests passed (hand-built
  fixtures didn't set `Virtual`) but real `CREATE SEQUENCE` + wire-protocol
  E2E failed with `relation "s_copy" does not exist`. Fixed via
  `executor.AllSequenceInfos(oid)` (reads the registry directly) instead.
  Full detail in the follow-up-41 deferral ledger row and design doc
  section — read those before touching this area again.
- Landed: `executor.SnapshotSequenceState` (new); `resolveCreateDatabaseTemplate`
  3rd return `sequences []executor.SeqInfo`; new `s.copyTemplateSequences`;
  `rollbackTemplateCopy` gained a sequence-registry sweep.
- Verified independently: `go build`/`go vet ./...` clean; `go test
  ./internal/server/... ./internal/executor/... ./internal/catalog/...
  ./internal/wal/... ./internal/initdb/...` PASS; `-race` on
  server+executor PASS except `TestConnectExceedsPositiveDatconnlimitRejected`
  (re-confirmed via `git stash` + `-count=3` against unmodified HEAD —
  pre-existing `internal/activity/registry.go` race, not a regression);
  `go test -short` full repo (excl. testport) PASS; `tpch-spotcheck.sh`
  PASS (Q12=2/Q13=33); pgbench smoke PASS (0 failed, all 3 workloads).
- Updated fix_plan.md (follow-up-41 entry), deferral_ledger.md (new row),
  design doc `0122-0018-...md` (new subsection + status line), and
  `docs/design/README.md` index row.

**Next natural M0122-0007 work:** index/view/matview/typed-table TEMPLATE
copying (index-file cloning + per-database sys-btree catalog bootstrap;
view/matview AST/ViewDef cloning; composite-type OID resolution) — see
follow-up 41's deferral ledger row for the resume point; OR the
independent per-database index/type catalog-row + sys-btree bootstrap gap
itself; OR move to a different M0122-00xx milestone for variety.

Gates run: go build, go vet, go test (server/executor/catalog/wal/initdb,
fresh), go test -race (server/executor, 1 pre-existing unrelated failure
re-confirmed via stash), go test -short full repo (excl. testport),
tpch-spotcheck, pgbench smoke, ralph-state-guard (self-repaired a stale
running/completed mismatch) — all PASS.
In-flight: none
