Task: M0134-0051 (partition_info.sql) — FIXED and CLOSED this loop, CSV row
flipped `failed`→`pass`/`pass_required=yes`. Next: select M0134-0052
(partition_join.sql).

Files this loop: `internal/executor/operators_ddl.go` (real code fix — gate
`tbl.PartitionMethod != ""` partition-index fan-out on `!s.OnOnly`),
`docs/test-port/postgres-oracle-target-inventory.csv` (partition_info row
flipped to pass), derived docs regenerated via `make regen-testport`,
`.ralph/fix_plan.md` (M0134-0051 entry ticked + rewritten), `.ralph/progress.json`
(state-guard auto-repair, unrelated bookkeeping).

Key symbols / findings: `execCreateIndex` (`internal/executor/operators_ddl.go:
~7403`) never consulted `parser.CreateIndexStmt.OnOnly` (set at
`internal/parser/ddl.go:5342`, field at `internal/parser/ast.go:1646`) before
fanning a `CREATE INDEX` on a partitioned table out to every existing
partition descendant via `createPartitionChildIndexes`. PG's `DefineIndex`
(`postgres/src/backend/commands/indexcmds.c:1230,1303`) gates that same
recursion on `stmt->relation->inh` (false when `ONLY` was specified) — `ON
ONLY` is supposed to require an explicit later `ATTACH PARTITION`. goopg
auto-built+attached child indexes anyway, so `pg_partition_tree()` reported
15 rows where PG expects 7. Fix was a single added `&& !s.OnOnly` condition;
no other bucket existed in this case's diff (79 lines → 0 lines, 1/1 PASS).
No deferral row needed — fully closed, not a partial fix.

Hypothesis/Findings: none outstanding for this case. Not every M0134 `failed`
case is a multi-bucket grind through the numeric-precision/transcendental
gaps — some (like this one and M0134-0048's harness fix) are single
self-contained bugs findable by researcher sizing alone. Keep sizing each
case fresh rather than assuming it collapses into an already-ledgered bucket.

Next step: select **M0134-0052 (partition_join.sql)** per the fix_plan
task-ID-ascending selection rule. Size it via `scripts/pg-regress-runner.sh
--verbose partition_join` (delegate to researcher) before deciding
fix/split/park, same pattern as M0134-0044..0051.

Gates run this loop: `make ralph-state-guard` — ran clean after one
auto-repair (status/progress reconciliation, same recurring pattern, not a
new issue); pgbench smoke will run as part of pre-commit hook (mandatory).
Implementer ran `go build ./...`, `go test ./internal/executor/... -run
TestPartition -v` PASS, `TestPort_IsolationPartitionDropIndexLocking` PASS
(confirms non-ON-ONLY fan-out unaffected), live `pg-regress-runner.sh
--verbose partition_info` PASS (0-line diff, 100% parity), full
`internal/executor` package PASS (~6.7s warm).

Delegation: researcher agent `ae7b3c39d97da2ccc` (1 round, sizing, found
single clean bucket + PG oracle citation) → implementer agent
`a47a11b7b4efac521` (1 round, one-line conditional fix, DONE first try).
Handoff: `tmp/ralph-handoffs/m0134-0051-onoly-index-fanout/` (brief.md
written normally; report.md write blocked by harness policy again this
round — findings relayed as agent output text, folded into this working_set
entry and fix_plan row, nothing lost).

In-flight: none. No server left running (regress runner, package tests, and
isolation test all self-start/stop their own throwaway goopg instances via
the cgroup wrapper). About to commit `internal/executor/operators_ddl.go`,
`docs/test-port/postgres-oracle-target-inventory.csv` (+ any regen-testport
derived-doc diffs), `.ralph/fix_plan.md`, `.ralph/progress.json`,
`.ralph/working_set.md` and push to `regress-renumbering`.
