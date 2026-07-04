Task: M0110-0001 (DU-002 slice 445 follow-up) — ALTER STATISTICS RENAME
TO/OWNER TO/SET SCHEMA WAL/restart persistence, closing resume point (1) of
the slice-441/445 ledger rows (the last open item on the statistics
restart-durability thread).

Files touched (implementation complete, NOT yet committed):
internal/wal/statistics_ddl.go (+test) — new RecordKindAlterStatisticsRename/
Owner/SetSchema (kinds 97/98/99) + Encode/Decode pairs;
internal/wal/recovery.go — added a combined no-op case for
RecordKindCreateStatistics/DropStatistics/AlterStatisticsRename/Owner/
SetSchema in wal.ApplyRecord's physical-replay switch (this was ALSO missing
for the pre-existing 95/96 kinds from last loop — a real gap for standby
streaming replication via stream_replayer.go, found and fixed in this same
loop, not a separate slice);
internal/catalog/catalog.go — RenameStatisticsObjectDuringRecovery/
SetStatisticsOwnerDuringRecovery/SetStatisticsSchemaDuringRecovery;
internal/initdb/statistics_ddl_recovery.go (+test) — statisticsRegistryRecovery
interface + replayStatisticsDDLRecords gain the 3 new cases;
internal/executor/operators_ddl.go — execAlterStatistics's three Action
cases now WAL-append after each in-memory mutation;
docs/design/0110-0001-pg-dump-tap-port.md "loop #97" section;
docs/design/README.md index entry appended (loop #96 was also missing an
index entry — added both #96 and #97 in this loop);
.ralph/deferral_ledger.md — slice-441 row flipped `resolved`, slice-445 row
flipped `resolved`, new `resolved` row appended for this loop's closure;
.ralph/fix_plan.md — new [x] entry + Current Priority banner updated.

Key symbols: execAlterStatistics, replayStatisticsDDLRecords,
wal.ApplyRecord (the collation-case switch block ~line 6419 in recovery.go),
EncodeAlterStatisticsRename/Owner/SetSchema.

Gates run so far: go build ./... clean. Targeted new tests all PASS:
`go test ./internal/wal/... -run Statistics` (10/10),
`go test ./internal/initdb/... -run Statistics` (6/6, incl. new
TestStatisticsDDLRecoveryReplaysAlterRenameOwnerSetSchema),
`go test ./internal/executor/... -run Statistics` (2/2, pre-existing
TestAlterStatisticsRenameOwnerSetSchema still passes unchanged).

Gates run and PASS (this loop, all fresh): `go build ./...` clean; full
`go test ./internal/wal/... ./internal/catalog/... ./internal/executor/...
./internal/initdb/... ./internal/parser/...` (initdb 336s uncached, rest
cached/fast) all PASS; `go test -race ./internal/wal/... ./internal/mvcc/...`
PASS (also re-ran `-race -count=1 -run Statistics` across
wal+mvcc+initdb+executor specifically); `make ralph-state-guard` self-repaired
the recurring benign status/progress mismatch (same pattern every loop),
clean after. `gofmt -l` flags 3 pre-existing-drift files
(statistics_ddl_test.go/catalog.go/operators_ddl.go) — diffed each against
gofmt output and confirmed via `git diff` that every flagged hunk is in
UNRELATED pre-existing lines far from this loop's additions (go1.25-vs-
go1.26.3 mismatch per standing memory); left untouched.

Gates NOT yet run (launched in background, awaiting completion):
`scripts/tpch-spotcheck.sh` (background task bmj4mzgl1). Still needed after
that: pgbench smoke (pre-commit hook path, `.githooks/pre-commit`).

Next step: check the tpch-spotcheck background result (expect Q12=2/Q13=33);
if PASS, run the pgbench smoke, then commit (implementation is complete and
self-contained — do NOT re-implement, only verification gates remain) and
push to origin/align-data-structure-with-pg.
