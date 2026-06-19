Task: DU-002 slice 278 (loop #45) — COMPLETE, ready to commit/push.

Last landed: combines slice 277's auto-name collapse with slice 275's NO
INHERIT suffix on the ALTER path. A NAMED `NO INHERIT` NOT NULL added to a LOCAL
column via `ALTER TABLE public.nninh7 ADD CONSTRAINT nninh7_c_not_null NOT NULL c
NO INHERIT`, whose explicit name EQUALS the auto-name `nninh7_c_not_null`, must
COLLAPSE the `CONSTRAINT` prefix while the `NO INHERIT` suffix SURVIVES — bare
`c integer NOT NULL NO INHERIT`. NO production change. pg_dump renders the column
NOT NULL in two independent steps (pg_dump.c:17179-17188): name-vs-default picks
bare ` NOT NULL` (conname == computed default → suppressed at 17184), then
notnull_noinh[j] appends ` NO INHERIT` (17187). Proves the ALTER path threads
BOTH the collapsible conname (slice 277) AND the noinh bit (slice 275) together.

Fixture: `CREATE TABLE public.nninh7 (c integer, d integer)` then
`ALTER TABLE public.nninh7 ADD CONSTRAINT nninh7_c_not_null NOT NULL c NO INHERIT`.
Asserted: nninh7 block has bare `c integer NOT NULL NO INHERIT`; does NOT contain
`CONSTRAINT nninh7_c_not_null`; `d integer` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — nninh7 fixture (after nninh6) +
  assertion block (after nninh6 assert).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 278 section + Next (279) note.
- .ralph/fix_plan.md — slice 278 progress (loop #45).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.55s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 279+): the `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col>`
counterpart on an INHERITED (child) column — pg_dump attaches the NOT NULL to the
child only when it is locally declared (conislocal), exercising the inheritance
interaction the prior local-only slices deliberately avoided.
