Task: DU-002 slice 279 (loop #46) — COMPLETE, committed + pushed.

Last landed: the inherited-child counterpart of slice 277. A DEFAULT-named NOT
NULL added to an INHERITED column via `ALTER TABLE idfnd_child ADD CONSTRAINT
idfnd_child_pid_not_null NOT NULL pid`, whose name EQUALS the auto-name, must
COLLAPSE the `CONSTRAINT` prefix in the STANDALONE body form → bare `NOT NULL
pid`. NO production change. pg_dump's body branch (pg_dump.c:17225-17232) emits
`NOT NULL <col>` when notnull_constrs[j] is empty (conname == computed default),
else `CONSTRAINT <name> NOT NULL <col>`; it reuses the SAME notnull_constrs[]
array as the inline path proven by slice 277, so the collapse fires identically.
Body branch never appends ` NO INHERIT` (inline-only, pg_dump.c:17187) — slice
omits that dimension. Slice 271 proved the named (non-default) body form.

Fixture: `CREATE TABLE public.idfnd_parent (pid integer, pname text)` +
`CREATE TABLE public.idfnd_child (extra integer) INHERITS (public.idfnd_parent)`
+ `ALTER TABLE public.idfnd_child ADD CONSTRAINT idfnd_child_pid_not_null NOT
NULL pid`. Asserted: idfnd_child block has `extra integer` + bare `NOT NULL pid`;
does NOT contain `CONSTRAINT idfnd_child_pid_not_null`; inherited `pid integer`/
`pname text` not re-emitted; `INHERITS (public.idfnd_parent)` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — idfnd fixture (after nninh7) +
  assertion block (after nninh7 assert).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 279 section + Next (280) note.
- .ralph/fix_plan.md — slice 279 progress (loop #46).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.49s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 280+): the partition-leaf counterpart — a conislocal NOT NULL on a
partition leaf column (where tbinfo->ispartition changes the column-omission
decision), OR a multi-column inherited NOT NULL body form proving the attnum
ordering of multiple standalone `NOT NULL <col>` body items.
