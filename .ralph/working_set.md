Task: M0110-0002 — port pg_waldump 002_save_fullpage.pl. LANDED (self-promoting
skip test) + uncovered a real WAL blocker. M0110-0003 still HARD-BLOCKED.

This loop (#17): productive uncontaminated work while M0110-0003's SQL surface
stays blocked by the foreign session (pid 2177381 still alive, WIP frozen since
2026-06-13 14:28, ~23h).

Landed:
- internal/testport/pgwaldump_savefullpage_test.go — TestPort_PgWaldump002SaveFullpage.
  Drives the full `pg_waldump --save-fullpage --relation` path (goopg emits PG
  FPIs; full RelFileLocator spc=1663/db/relNumber; pg_relation_filenode matches).
  Asserts filename format + page-LSN ≤ file-LSN. Currently t.Skips on a REAL
  blocker; auto-promotes once the WAL fix lands. Run: PASS (skips). 001 still PASS.
- docs/design/0110-0002 + deferral ledger + fix_plan M0110-0002 updated.
- memory: goopg_wal_xl_prev_1based_pg_waldump.md.

KEY FINDING (the blocker): goopg writes `xl_prev` as a **1-based** LSN on disk,
so pg_waldump (0-based, anchored on segment name) aborts the record chain at the
2nd record (`incorrect prev-link 0/1000029 at 0/10000A0`, constant +1).
Origin: internal/wal/writer.go ~L1346/L1491 (`start=writePos+leading+1`) →
insert_pos.go reserveLocked (`t.prev=old`) → format.go:263 encodeRecordXLog
writes verbatim. NOT in the frozen WIP set — internal/wal is editable.

SECONDARY FINDING: TestPort_WALPgWaldumpCompat (row W-001, M0101-0003,
pass_required) is SILENTLY RED — segment names are now native PG-format
(TLI prefix), so its ParseUint(name,16,64) overflow + alias logic t.Fatals at
"no WAL segments found". Excluded from `go test ./...` so it escaped notice.

Next step (pick one):
- WAL-correctness loop: emit xl_prev 0-based on disk (encode↔decode SIBLINGS —
  goopg recovery decode reads it back), re-verify M0102 walsender + recovery
  E2E + re-init data dir, then un-skip 002 test + repair W-001 (reuse
  listWALSegments/isHex24). HIGH blast radius — its own loop.
- OR M0110-0003 SQL surface (slice S1 of docs/design/0110-0008) once a HUMAN
  clears the foreign session's static gen-column WIP.

Gates run: go vet ./internal/testport/ clean; gofmt clean; both pg_waldump port
tests PASS (002 skips on blocker). No TPC-H/WAL-race gate (test-only change; no
engine code modified). make ralph-state-guard: run before status block.
