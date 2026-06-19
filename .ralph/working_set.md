Task: DU-002 slice 274 (loop #41) — COMPLETE, committed.

Last landed: slice 273's boundary twin. A NAMED inline NOT NULL whose name
EQUALS the computed default `<table>_<col>_not_null` COLLAPSES to the bare
`NOT NULL` form (no CONSTRAINT prefix). NO production change — slice 273's code
records the explicit name on AddNotNull so pg_constraint.conname is
`nninh3_c_not_null`; the real pg_dump 18.3 binary compares it against its own
computed default, finds them equal, and drops the prefix (pg_dump.c:17184
ChooseConstraintName match). Pure verification/regression guard confirming goopg
records the explicit name faithfully and does NOT itself force the prefix when an
explicit name is present.

Fixture: `CREATE TABLE public.nninh3 (c integer CONSTRAINT nninh3_c_not_null NOT
NULL, d integer)`. Asserted byte-for-byte: nninh3 block has bare `c integer NOT
NULL` (NOT `CONSTRAINT nninh3_c_not_null`); plain `d integer` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — nninh3 fixture (after nninh2) +
  assertion block (after nninh2 assert): positive `c integer NOT NULL`, negative
  no `CONSTRAINT nninh3_c_not_null`, `d integer` survives.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 274 section + Next (275) note.
- .ralph/fix_plan.md — slice 274 progress (loop #41).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.56s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 275+): the `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col> NO
INHERIT` end-to-end dump on a STANDALONE table (slice 271's ADD-CONSTRAINT path,
rendered inline because the column is local).
