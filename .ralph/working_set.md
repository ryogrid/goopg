Task COMPLETE and committed (4eecc215, pushed): `ALTER COLLATION RENAME TO /
OWNER TO / REFRESH VERSION` (M0119-0004, design
docs/design/0119-0004-alter-collation.md). Closed the loop #50 ledger row's
"ALTER COLLATION ... still unhandled" item — the `collation` keyword was
entirely absent from `parseAlter()`'s if-chain.

Files: internal/parser/{ast.go,ddl.go,alter_collation_test.go},
internal/catalog/catalog.go (RenameCollation/SetCollationOwner),
internal/executor/{operators_ddl.go,alter_collation_test.go}
(execAlterCollation), internal/planner/planner.go, internal/server/dispatch.go
(ddlTag), docs/design/{0119-0004-alter-collation.md,README.md},
.ralph/{deferral_ledger.md,fix_plan.md}.

Gates run this loop: go build ./... clean; go vet
parser+catalog+executor+planner+server clean; those 5 packages' full suites
PASS; gofmt -l flagged pre-existing unrelated struct-alignment diffs only (the
known go1.25-repo/go1.26.3-local gofmt version mismatch — confirmed zero diff
touching my added code); make ralph-state-guard self-repaired the recurring
stale progress.json marker (same pattern as prior loops); pgbench smoke PASS
via pre-commit hook (177/230/13432 tps, 0 failed).

Deliberately deferred (new ledger row appended, status `-`): ALTER COLLATION
restart persistence — RENAME/OWNER TO are in-memory-only (mirrors the
pre-existing, also-unlogged ALTER TABLE RENAME/OWNER TO). Resume = net-new
RecordKind values (44+) + WAL emission in execAlterCollation + a recovery
replay case in internal/initdb/collation_ddl_recovery.go (no existing ALTER
sibling to copy verbatim — CREATE/DROP COLLATION's kinds don't cover it).

Next step: re-scan .ralph/deferral_ledger.md for the next highest-value open
"| - |" row (90+ open rows before this loop; grep "^| - |" and skim by
task-id cluster). Candidates surfaced by prior loops still open:
- attacl high-blast-radius half (loop #88/89 fix_plan note, M0119-0004-ACLHEAP
  forward plan in the loop #83/0119-0004bg row): parser AttrACLChange capture
  + execAttrACLChange/resyncAttrACLHeapRow + pg_attribute seqscan attacl
  decode hook + DU-002 column-GRANT connsetup slice — this is now DONE
  (loop #89), re-check if datacl (pg_database, --create-only, permanently
  deferred) or typacl variants need anything further before assuming closed.
- Builtin pg_proc rows not SQL-queryable (loop #45/TRANSFORM row) —
  systemic blocker affecting CAST/TRANSFORM/CONVERSION WITH FUNCTION on
  builtin functions; large, needs its own scoped loop.
- M0119-0002 CLOG store swap Part B — flagged as highest blast radius,
  needs a dedicated full-gate session (-race mvcc+wal, xlog_replay,
  heterogeneous PG-standby E2E, fresh-server TPC-H Q12/Q13); do not
  attempt casually in a single generic loop.
