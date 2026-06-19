Task: DU-002 slice 276 (loop #43) — COMPLETE, ready to commit/push.

Last landed: the NEGATIVE TWIN of slice 275. A NAMED NOT NULL (no `NO INHERIT`)
added to a LOCAL column via `ALTER TABLE public.nninh5 ADD CONSTRAINT nn5 NOT NULL c`
dumps INLINE as `c integer CONSTRAINT nn5 NOT NULL` and must NOT grow a spurious
` NO INHERIT` suffix. NO production change. Slice 275 proved the ALTER path THREADS
act.NoInherit when present; this proves it does NOT FABRICATE it when absent. Parser
leaves AlterTableAction.NoInherit=false (ddl.go:5483); executor records contype='n'
connoinherit='f' via tbl.AddNotNull(name, col, oid, false, isLocal=true, 0)
(operators_ddl.go:5498). LOCAL + nn5 ≠ auto-name nninh5_c_not_null → pg_dump emits
CONSTRAINT nn5 prefix (pg_dump.c:17184) but NO suffix (17188 gated on connoinherit).

Fixture: `CREATE TABLE public.nninh5 (c integer, d integer)` then
`ALTER TABLE public.nninh5 ADD CONSTRAINT nn5 NOT NULL c`. Asserted: nninh5 block has
`c integer CONSTRAINT nn5 NOT NULL`; does NOT contain `CONSTRAINT nn5 NOT NULL NO
INHERIT`; `d integer` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — nninh5 fixture (after nninh4) +
  assertion block (after nninh4 assert).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 276 section + Next (277) note.
- .ralph/fix_plan.md — slice 276 progress (loop #43).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.36s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 277+): an `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col>` on a
LOCAL column where the name EQUALS the auto-name `<table>_<col>_not_null` — the
ALTER-path counterpart of slice 274, asserting the named constraint collapses to a
bare `<col> <type> NOT NULL` (no `CONSTRAINT` prefix) through pg_dump.
