Task: M0101-0003 / M0110-0002 — WAL xl_prev restart-seeding fix. COMPLETE + committed.

This loop (#18): fixed the real WAL blocker the prior loops diagnosed (and
corrected the diagnosis), while M0110-0003's SQL surface stays HARD-BLOCKED on
the foreign session (pid 2177381 still alive, ~23h frozen gen-column WIP).

Landed (commit on align-data-structure-with-pg):
- internal/wal/writer.go detectWritePos: prevRecPtr = lastRecPtr-1 (0-guarded).
  Root cause was NOT "xl_prev globally 1-based" — the live-append path already
  stores start-1 (0-based) via resetPosition. The restart-seed path
  (detectWritePos:917) assigned scanLastSegmentEnd's 1-based public start-LSN
  verbatim to the 0-based prevRecPtr field, so the FIRST record after boot got
  +1 xl_prev → pg_waldump "incorrect prev-link" at the 2nd record.
- Output-only fix: writePos / client LSNs unchanged; goopg recovery never
  validates xl_prev. Strictly improves goopg→PG (M0102) prev-link validation.
- W-001 (TestPort_WALPgWaldumpCompat) repaired (native-PG segment discovery via
  listWALSegments; old ParseUint(24-hex) overflow skipped all segments) → PASS,
  now guards the fix via the prev-link check.
- 002 (TestPort_PgWaldump002SaveFullpage): prev-link gone; self-skips on the
  SEPARATE remaining blocker (no PG-decodable FPI records — all non-checkpoint
  records route RmgrXLog/0xF0, opaque to PG). Stays under WD-002.
- docs/design/0101-0003-...md + README index + CSV W-001 rationale + regenerated
  markdown + fix_plan M0110-0002 + memory goopg_wal_xl_prev_1based_pg_waldump.md.

Gates run: go test ./internal/wal/ PASS; go test -race ./internal/wal/
./internal/mvcc/ PASS; both pg_waldump oracle tests pass (W-001 PASS, 002 skip);
TestE2E_PhysicalReplication PASS; gofmt+vet clean. (No TPC-H gate — no
planner/executor/codec change.)

Next step (pick one):
- 002 FULL pass: emit PG-decodable heap WAL records with backup blocks
  (XLogRecordBlockHeader + BKPIMAGE) so pg_waldump --save-fullpage extracts FPIs.
  Large, separate feature — its own milestone.
- OR M0110-0003 SQL surface (slice S1 of docs/design/0110-0008) once a HUMAN
  clears the foreign session's static gen-column WIP (pid 2177381).
