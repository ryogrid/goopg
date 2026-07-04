(idle — nothing in flight)

M0110-0001 DU-002 slice 445 CLOSED this loop (2026-07-04): this loop's own
work was already done by a prior background-agent handoff (fix_plan.md,
deferral_ledger.md, and the design doc were already updated with the full
narrative) but left UNCOMMITTED — working_set.md itself was stale (still
described the older root-0024/slice-444 close). Per the
"verify background-agent handoff before commit" memory, re-ran the named
gates from scratch rather than trusting the narrative, all green, then
committed + pushed.

What landed (by the prior handoff, verified not re-implemented this loop):
CREATE/DROP STATISTICS are now WAL-logged (`RecordKindCreateStatistics`/
`RecordKindDropStatistics`, kinds 95/96, internal/wal/statistics_ddl.go) and
replayed on restart (internal/initdb/statistics_ddl_recovery.go's
replayStatisticsDDLRecords, wired into open.go after the access-method DDL
replay call). DROP STATISTICS was entirely unparsed before this slice — added
to parser/ddl.go's DropCompatStmt object-type list + a new
objType=="statistics" case in execDropCompat (operators_ddl.go). Closes
resume point (2) of the slice-441 ledger row.

Files touched (all by the prior handoff, verified this loop):
internal/wal/statistics_ddl.go(+test), internal/initdb/statistics_ddl_recovery.go(+test),
internal/executor/drop_statistics_test.go, internal/catalog/catalog.go
(RegisterStatisticsDuringRecovery/DropStatistics/DropStatisticsDuringRecovery),
internal/executor/operators_ddl.go (execCreateStatistics WAL-append,
execDropCompat "statistics" case), internal/initdb/open.go (replay wiring),
internal/parser/ddl.go ("statistics" ident), docs/design/0110-0001-pg-dump-tap-port.md
(loop #96 section), .ralph/deferral_ledger.md (new slice-445 row, status "-"),
.ralph/fix_plan.md ([x] entry + Current Priority banner).

Gates run (all re-executed fresh this loop, not trusted from the narrative):
go build ./... clean; go test ./internal/wal/... ./internal/catalog/...
./internal/executor/... ./internal/initdb/... ./internal/parser/... all PASS
(initdb ran uncached, 329s, rest cache-hit on unchanged inputs); go test -race
-count=1 on the 10 new/touched statistics test funcs by name, all PASS;
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); make ralph-state-guard
self-repaired the recurring benign status/progress mismatch (same pattern as
prior loops), clean after; pgbench smoke via .githooks/pre-commit PASS (0
failed transactions, 3 workloads). gofmt -l flagged 3 pre-existing-drift files
(catalog.go, operators_ddl.go, statistics_ddl_test.go) — diffed each against
gofmt output and confirmed the drift is in UNRELATED lines far from this
loop's additions (go1.25-vs-go1.26.3 mismatch per standing memory), left
untouched.

Committed a4fa9a4b, pushed to origin/align-data-structure-with-pg.

Still open (documented in the new slice-445 ledger row, status "-", NOT
resolved): ALTER STATISTICS ... RENAME TO/OWNER TO/SET SCHEMA remains
in-memory-only — a restart reverts a renamed/re-owned/moved statistics object
back to its as-created identity (resume point (1) of the original slice-441
row, unchanged). Resume: execAlterStatistics's three mutation call sites in
internal/executor/operators_ddl.go (near RenameStatisticsObject/
SetStatisticsOwner/SetStatisticsSchema) need a
RecordKindAlterStatisticsRename/...Owner/...SetSchema triple + matching
replayStatisticsDDLRecords cases, following this slice's CREATE/DROP template
or the ALTER COLLATION rename/owner/set-schema precedent directly.

Next step: pick ONE of — (a) the ALTER STATISTICS restart-persistence
follow-up above, (b) continue the M0119-0004 pg_dump catalog-view parity
battery (next gap via TestPort_PgDumpConnectionSetup), or (c) one of
root-0024's own two documented residuals (enum/composite-type undo; the
mid-batch-BEGIN throwaway-session handoff) — per fix_plan.md's Current
Priority banner, none is independently urgent; use judgment.
