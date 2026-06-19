(idle — nothing in flight)

Last landed: DU-002 slice 268 (loop #35) — a LOCAL column DEFAULT on a LEGACY
(non-partition) INHERITS child round-trips through pg_dump. The pg_attrdef sibling
of slice 267's table-level CHECK: same column-omission regime (inherited
`pid`/`pname` dropped via attislocal=false; local `extra` printed) but the DEFAULT
must ride INLINE on the local column. Fixture: `idfl_child (extra integer DEFAULT
42) INHERITS (idfl_parent (pid integer, pname text))`. Real pg_dump 18.3 emits body
`extra integer DEFAULT 42` + `INHERITS (public.idfl_parent)` — verified byte-exact
vs PG 18.3 (probed) AND fresh goopg server. NO production change — local-column
attrdef path + legacy-inheritance column omission already exist.

Files (test/docs only):
- internal/testport/pgdump_connsetup_test.go — idfl_parent/idfl_child fixture
  (after ichk_child block, ~line 1413) + block-scoped assert (local
  `extra integer DEFAULT 42`, inherited `pid`/`pname` ABSENT) + INHERITS assert
  (after slice 267's ichk INHERITS assert, ~line 3811).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 268 section + Next note.
- .ralph/fix_plan.md — slice 268 progress entry (loop #35).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.26s); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 269+): a child-level DEFAULT/NOT NULL on an INHERITED column
(`ALTER TABLE child ALTER COLUMN ... SET DEFAULT/SET NOT NULL`), which pg_dump
emits as a separate `ALTER TABLE ... ALTER COLUMN` statement, NOT inline.
