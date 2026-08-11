(idle — nothing in flight)

Last loop (#108): **M0131-S10 — retire `copyInitFiles`, correct the record** —
DONE, ticked, committed, pushed to `make-db-cluster-compat`.

M-NIGHTLY duty: `ci/logs/action-items.md` still run `20260811-014635` (12 items,
unchanged since loop #100). All filed; the 11 open ones stay PARKED per banner.

What landed:
- `internal/testport/`: `copyInitFiles` + its 3 call sites deleted
  (`e2e_failover_goopg_to_pg_test.go`, `e2e_pg183_standby_full_cycle_test.go`,
  `e2e_checksum_replication_test.go`). Three *tombstone* comments remain by
  design (S10.2 is "correct the record", not "erase it") — design guard 1 says
  "grep returns nothing"; the accurate claim is "no callable reference".
- Both mis-stating comments re-attributed: the blocker is
  `RelationBuildRuleLock` scanning `pg_rewrite` via index **2693** with
  `indexOK=true` and no seq-scan fallback → `rd_rules=NULL` → 42809 in
  `plancat.c`. 2620 is `pg_trigger`, so row 428's "index 2620 exists" was
  never evidence about the right index.
- `internal/initdb/pg_type_bootstrap.go:324` stale `copyInitFiles()` mention fixed.
- `docs/design/0130-0002-*`: guard #2 de-staled; "Remaining" items 2 and 3
  corrected. **Guard #1 left untouched on purpose — M0131-S4 owns it.**
- 6 ledger rows (record correction; 3 retroactive for `0130-0002`'s
  never-ledgered Remaining items; 2 scope-limit rows: `pg_filenode.map`
  write-only, and 10 unreplayed rmids of which 6/18/19 are NOT index AMs).
- Design `0131-0010` draft → accepted; README index row given its Summary column.

Next loop: per banner — M-NIGHTLY filing, then M0131 top-to-bottom. Next
unchecked is **M0131-S3** (`TestE2E_GoopgColdStartOnPGDataDir`, design
`0131-0003`); S1+S2 are landed and S10 has now cleared the dead pattern out of
the harness, so S3 must NOT reintroduce an init-file copy. Carry into S3: a
real-world PG conf still exits 1 on the `CUSTOMIZED OPTIONS` tail
(`unix_socket_directories`/`maintenance_work_mem`/`wal_compression`) — a
pristine initdb dir will not hit it (loop #106 ledger row).

Gates: `go build ./...` + `go vet ./internal/testport/ ./internal/initdb/` clean;
testport test binary compiles; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (`internal/initdb` 63 s cold, rest cached);
pgbench smoke PASS via the commit hook. The design's E2E guards 2/3 (three real
PG 18.3 standby cycles) NOT run — out of units scope, and PG unlinks every init
file in `StartupXLOG` before a backend exists, so the deletion cannot change
their outcome; the next nightly run is the confirmation.

In-flight: none
