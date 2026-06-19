Task: DU-002 slice 280 (loop #47) — COMPLETE, ready to commit + push.

Last landed: multi-column inherited NOT NULL body form proving ATTNUM ordering +
PER-COLUMN collapse. Two conislocal NOT NULL constraints on DISTINCT inherited
columns of mninh_child, added in REVERSE attnum order. pg_dump body loop iterates
`j` over columns (pg_dump.c:17175-17233) → standalone `NOT NULL <col>` items sort
by attnum, not creation order. Collapse test `notnull_constrs[j][0]=='\0'`
(pg_dump.c:17226) is per-column. NO production change — composes slices 271/277/279
across multiple columns of one table.

Fixture: `CREATE TABLE public.mninh_parent (ma integer, mb integer, mname text)` +
`CREATE TABLE public.mninh_child (extra integer) INHERITS (public.mninh_parent)` +
`ALTER TABLE public.mninh_child ADD CONSTRAINT mninh_named NOT NULL mb` (attnum 2,
non-default name → keeps `CONSTRAINT mninh_named NOT NULL mb`) +
`ALTER TABLE public.mninh_child ADD CONSTRAINT mninh_child_ma_not_null NOT NULL ma`
(attnum 1, default name → collapses to bare `NOT NULL ma`).
Asserted: block has `extra integer`, bare `NOT NULL ma`, no `CONSTRAINT
mninh_child_ma_not_null`, `CONSTRAINT mninh_named NOT NULL mb`, `NOT NULL ma`
precedes `NOT NULL mb` (attnum order despite mb-first ALTER); inherited `ma
integer`/`mb integer`/`mname text` not re-emitted; `INHERITS (public.mninh_parent)`
survives.

Files:
- internal/testport/pgdump_connsetup_test.go — mninh fixture (after idfnd ALTER) +
  assertion block (after idfnd INHERITS check).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 280 section + Next (281) note.
- .ralph/fix_plan.md — slice 280 progress (loop #47).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.58s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 281+): the partition-leaf counterpart — a conislocal NOT NULL on a
partition leaf column (where tbinfo->ispartition changes the column-omission
decision), OR a generated-column / default-value inherited body form.
