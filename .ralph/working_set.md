(idle — nothing in flight)

Last landed: DU-002 slice 267 (loop #34) — a LOCAL CHECK constraint on a LEGACY
(non-partition) INHERITS child round-trips through pg_dump. First slice to leave
the partition-leaf regime (slices 264–266) for legacy inheritance, where
`ispartition` is false so `shouldPrintColumn` (pg_dump.c:9970) gates on
`attislocal` ALONE: inherited cols are OMITTED, local col prints. Fixture:
`ichk_child (extra integer, CONSTRAINT ichk_child_pos CHECK (extra > 0)) INHERITS
(ichk_parent (pid integer, pname text))`. Real pg_dump 18.3 emits the body
`extra integer, CONSTRAINT ichk_child_pos CHECK ((extra > 0))` + `INHERITS
(public.ichk_parent)` (NOT an ATTACH). Verified byte-identical vs PG 18.3 (probed)
AND fresh goopg server (test runs pg_dump against goopg). NO production change —
the conislocal CHECK path + legacy-inheritance column omission already existed.

Files (test/docs only):
- internal/testport/pgdump_connsetup_test.go — ichk_parent/ichk_child fixture
  (after pnnl_1 block, ~line 1385) + block-scoped assert (local `extra integer`,
  the `CONSTRAINT ... CHECK`, inherited `pid`/`pname` ABSENT) + INHERITS clause
  assert (after slice 266's pnnl assert, ~line 3750).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 267 section + Next note.
- .ralph/fix_plan.md — slice 267 progress entry (loop #34).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.39s); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 268+): a local column DEFAULT on a legacy INHERITS child (pg_attrdef
sibling of this slice's table-level CHECK); then a child-level DEFAULT/NOT NULL on
an INHERITED column (`ALTER TABLE child ALTER COLUMN ... SET DEFAULT/SET NOT NULL`),
which pg_dump emits as a separate `ALTER TABLE ... ALTER COLUMN` statement, NOT inline.
