(idle — nothing in flight)

Last loop: M-NIGHTLY AI-20260810-011258-003 (`TestE2E_PGStandbyFullCycle`).
Landed and pushed. Also closed AI-20260810-011258-002
(`TestE2E_FailoverGoopgToPG`) as CONFIRMED STALE — PASS at HEAD in 5.77 s, both
subtests, no code change.

The discovery: the M0130-S10 acceptance harness had NEVER run green, and died
at its first statement. goopg could create replication slots only over the
replication protocol (`CREATE_REPLICATION_SLOT`); upstream also exposes
`slotfuncs.c`'s `pg_create_physical_replication_slot` (OID 3779) and
`pg_drop_replication_slot` (3780) as SQL functions. Both OIDs were already
seeded into `pg_proc`, so name resolution SUCCEEDED and the call then fell out
of the executor's builtin switch with 42883 — the catalog advertised a
function the executor could not run. Second blocker: the test created the slot
via SQL *and* passed `pg_basebackup -C`, which upstream rejects.

Fix: `internal/executor/expr_replslot.go` (+ builtin-switch arms in `expr.go`),
`Context.ReplSlots` wired from `s.cfg.Slots` in `internal/server/dispatch.go`
so the SQL and wire paths share ONE registry; helper split into
`runGoopgBasebackupToPGSlot(..., createSlot bool)`. Design: addendum in
`docs/design/0130-0010-pg183-standby-e2e-harness.md` (+ README row). Ledger:
2 rows (record-vs-text return, temporary slots, deferred reservation; plus the
harness's remaining blocker).

AI-...-003 STAYS UNCHECKED. Phase A is now green end to end and Phase B
replays CREATE TABLE / CREATE INDEX / INSERT, but `ALTER TABLE ... ADD COLUMN
extra int DEFAULT 0` then makes every query on that relation ON THE PG STANDBY
raise `could not open relation with OID 2656` — PG's `AttrDefaultFetch`
opening `pg_attrdef` by `AttrDefaultIndexId` 2656, which goopg does not
materialize (pre-existing gap ledgered 2026-07-19). Phases C/D have never
executed. Resume: materialize index 2656 + the relid-2604 tupledesc gap.

Gates run: repro confirmed at HEAD before the fix; new guard
`TestPort_SQLPhysicalReplicationSlotFuncs` PASS (0.98 s);
`TestE2E_FailoverGoopgToPG` PASS after the helper refactor (5.76 s);
`TestE2E_PGStandbyFullCycle` advances from 1.2 s/first-statement to 32 s/Phase
B; units precommit PASS (`internal/initdb` cold, 58 s); pgbench commit hook
PASS; `make ralph-state-guard` OK.

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
M-NIGHTLY still top selectable. AI-...-006 (pgbench/nightly, 79 aborted
clients whose ORIGINATING error is absent from the log while the run prints
`0 failed`) is the highest-value remaining engine item.

In-flight: none.
