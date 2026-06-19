Task: DU-002 slice 275 (loop #42) — COMPLETE, ready to commit/push.

Last landed: a NAMED `NO INHERIT` NOT NULL added to a LOCAL column via
`ALTER TABLE ... ADD CONSTRAINT nn4 NOT NULL c NO INHERIT` dumps INLINE as
`c integer CONSTRAINT nn4 NOT NULL NO INHERIT`. NO production change — the path
already exists end-to-end: parser captures `NO INHERIT` into
AlterTableAction.NoInherit (ddl.go:5483); executor records contype='n' with
connoinherit='t' via tbl.AddNotNull(name, col, oid, act.NoInherit=true,
isLocal=true, 0) (operators_ddl.go:5498) + delete-old-rows/syncTableToCatalogHeap.
Sibling-paths regression guard: ALTER path must thread NoInherit + conname
identically to the CREATE-inline path (nninh2, slice 273). Because column is
LOCAL and nn4 ≠ auto-name nninh4_c_not_null, real pg_dump re-emits CONSTRAINT
prefix + NO INHERIT suffix.

Fixture: `CREATE TABLE public.nninh4 (c integer, d integer)` then
`ALTER TABLE public.nninh4 ADD CONSTRAINT nn4 NOT NULL c NO INHERIT`. Asserted:
nninh4 block has `c integer CONSTRAINT nn4 NOT NULL NO INHERIT`; `d integer` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — nninh4 fixture (after nninh3) +
  assertion block (after nninh3 assert).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 275 section + Next (276) note.
- .ralph/fix_plan.md — slice 275 progress (loop #42).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.56s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 276+): an `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col>`
(named, NO `NO INHERIT`) on a LOCAL column — the negative twin asserting the
inline `CONSTRAINT <name> NOT NULL` form WITHOUT a spurious ` NO INHERIT` suffix.
