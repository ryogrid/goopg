Task: M0095-0003 (pg_basebackup 011) blocker-diagnosis correction + M0110-0003
amcheck SQL surface still BLOCKED on the orphaned foreign gen-column WIP.

DISPOSITION THIS LOOP (loop #28, 2026-06-14): did NOT rubber-stamp BLOCKED.
Independently investigated whether ANY remaining task has an uncontaminated
opening, then landed the one safe valuable change found.

WHAT LANDED (uncontaminated, test+docs only):
- Corrected the STALE/WRONG skip note in
  `internal/testport/pgbasebackup_port_test.go`
  (`TestPort_PgBasebackup011InPlaceTablespace`) — it blamed BASE_BACKUP, which
  is fully implemented (010 `-X stream`/`-X fetch` PASS). Real 011 blocker is the
  in-place tablespace FEATURE: (1) `allow_in_place_tablespaces` GUC (absent),
  (2) `CREATE TABLESPACE <name> LOCATION ''` DDL (goopg parses only the
  TABLESPACE *clause* and ignores it — no statement / no pg_tablespace insert /
  no `pg_tblspc/<oid>` dir), (3) BASE_BACKUP per-tablespace `<oid>.tar`.
  Items (1)+(3) uncontaminated; (2) edits parser/executor/catalog → blocked.
- Mirrored the correction into fix_plan M0095-0003 note + deferral ledger.

KEY FACTS RE-VERIFIED THIS LOOP:
- Foreign gen-column WIP STILL dirty in catalog/parser/planner/executor (+analyzer,
  mvcc, server/dispatch), frozen mtime 2026-06-13 14:28 (~28h). ORIGINAL holder
  pid 2177381 is now DEAD; no live process owns the edits (the alive
  `claude --resume ec98936f` PID 3999013 started ~3h ago, did not create them).
  Still MUST NOT touch — memory concurrent_ralph_loops_corrupt_tree: a HUMAN clears it.
- pg_tablespace catalog bootstrap exists (initdb); CREATE TABLESPACE DDL does not.
- amcheck engine is logic-complete (heap + B-tree, incl. heapallindexed producer
  pair, xmin/xmax bounds). Only the SQL surface (CREATE EXTENSION amcheck +
  verify_heapam/bt_index_check SRF, docs/design/0110-0008 S1/S2) promotes AC-002,
  and it edits the contaminated tree.

Gates run: go vet ./internal/testport (clean); go build ./... (clean);
deferral-ledger appended; make ralph-state-guard (run before status block).

Next step: HUMAN must clear the orphaned foreign WIP (git status clean on
catalog/parser/planner/executor). THEN, in priority order:
1. amcheck SQL surface S1/S2 (0110-0008) over the finished engine → port
   002_nonesuch.pl → flip CSV AC-002→port (M0110-0003).
2. CREATE TABLESPACE DDL + allow_in_place_tablespaces GUC + per-tablespace
   <oid>.tar → enable TestPort_PgBasebackup011InPlaceTablespace (M0095-0003/011).
3. pg_dump 002+ catalog-view parity (M0110-0001).
Until cleared, every loop here is BLOCKED; do NOT fabricate isolated engine tiers
and do NOT chase BASE_BACKUP for 011 (it is done).
