Task: DU-002 slice 277 (loop #44) — COMPLETE, ready to commit/push.

Last landed: the ALTER-path counterpart of slice 274's CREATE-inline `nninh3`.
A NAMED NOT NULL added to a LOCAL column via `ALTER TABLE public.nninh6 ADD
CONSTRAINT nninh6_c_not_null NOT NULL c`, whose explicit name EQUALS the auto-name
`nninh6_c_not_null`, must COLLAPSE to bare `c integer NOT NULL` — pg_dump must NOT
leak the `CONSTRAINT nninh6_c_not_null` prefix. NO production change. Slice 274
proved the collapse at table-creation time; this proves the ALTER path stores the
same conname so pg_dump's auto-name suppression (pg_dump.c:17184, fires when conname
== computed default `<table>_<col>_not_null`) also applies.

Fixture: `CREATE TABLE public.nninh6 (c integer, d integer)` then
`ALTER TABLE public.nninh6 ADD CONSTRAINT nninh6_c_not_null NOT NULL c`. Asserted:
nninh6 block has bare `c integer NOT NULL`; does NOT contain `CONSTRAINT
nninh6_c_not_null`; `d integer` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — nninh6 fixture (after nninh5) +
  assertion block (after nninh5 assert).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 277 section + Next (278) note.
- .ralph/fix_plan.md — slice 277 progress (loop #44).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.40s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 278+): an `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col> NO
INHERIT` on a LOCAL column where the name EQUALS the auto-name — the collapse
should drop the `CONSTRAINT` prefix while the `NO INHERIT` suffix survives
(`<col> <type> NOT NULL NO INHERIT`), combining slice 274's collapse with slice
275's suffix threading.
