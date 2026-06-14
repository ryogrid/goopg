Task: M0095-0003 — pg_basebackup `-X fetch` (FETCH_WAL / in-tar WAL).
LANDED loop #12, committed.

What landed: server now honours the BASE_BACKUP `WAL` boolean so a single-
connection `pg_basebackup -X fetch` clones a goopg primary with consistent WAL
included in the data tar (no walsender). Three parts in
internal/server/basebackup.go:
  (a) parse `WAL` option → baseBackupOptions.IncludeWAL (bare flag new-syntax +
      legacy keyword; parseOptionBool honours explicit false).
  (b) pg_wal is no longer walked — shipped as empty dir + archive_status/
      summaries empty subdirs (mirrors basebackup.c sendDir():1385-1407). FIXES
      a prior deviation where goopg shipped full pg_wal contents on EVERY backup.
  (c) appendWALSegments: when IncludeWAL, append in-range
      [XLByteToSeg(startptr) .. XLByteToPrevSeg(endptr)] segments under pg_wal/,
      oldest first, with upstream contiguity sanity check; +history files.

Key finding (corrects the prior loop's plan): NO goopg→PG name conversion
needed — goopg on-disk WAL names are ALREADY PG-format (wal.formatSegmentName →
%08X%08X%08X). The speculatively-added `parseGoopgWalName` (raw %024X parse) was
WRONG and is REMOVED; selection uses wal.ParseXLogFileName.

Files: internal/server/basebackup.go (impl), internal/server/basebackup_test.go
(+3 parser cases), internal/testport/pgbasebackup_port_test.go
(+TestPort_PgBasebackup010FetchWAL + parseBackupLabelStartSegment helper),
docs/design/0095-0003-pg-basebackup-execution.md (+WAL-inclusion section),
docs/test-port/postgres-oracle-port-status.{csv,md} (BB-010), .ralph/fix_plan.md.

Gates run loop #12: gofmt clean; go vet clean; go build ./... OK;
go test ./internal/server/ PASS; go test -race ./internal/server/ PASS;
TestPort_PgBasebackup010{FetchWAL,StreamWAL,BackupExecution,Manifest*} all PASS
(real pg_basebackup + pg_verifybackup oracle); make ralph-state-guard OK.

CONTAMINATION (NOT mine, do NOT git add -A): the 18 foreign-WIP modified files
(catalog.go, operators_*.go, parser/ddl.go, planner/*, analyzer, dispatch,
mvcc/subxact_visibility, …) + untracked gen_override_test.go + .claude/worktrees/*
+ stray ./postgres marker. Commit ONLY the 7 files above.

Next step (M0095-0003 remaining): 011/020 backup-execution branches +
recvlogical — all still blocked on BASE_BACKUP in-place tablespace protocol /
logical replication protocol (same long-standing deps, not this loop's scope).
