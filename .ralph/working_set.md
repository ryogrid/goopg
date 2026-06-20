Task: DU-002 slice 284 (loop #52) — COMPLETE, ready to commit + push.

Last landed: a VIRTUAL GENERATED column inherited onto a partition leaf prints its
generation clause INLINE in the leaf body, rendered BARE without a trailing STORED
(`vb integer GENERATED ALWAYS AS (va * 2)`). The VIRTUAL counterpart of slice 283:
SAME discriminator branch (separate=false via attgenerated, pg_dump.c:9507 — non-empty
attgenerated forces separate=false unconditionally; 'v' is non-empty like 's'), but a
DIFFERENT render branch — pg_dump.c:17171 emits `GENERATED ALWAYS AS (%s)` with NO
trailing keyword (the STORED branch at 17168 is skipped). NO production change — goopg
already round-trips VIRTUAL generated cols on standalone tables (slice 194:
attGeneratedFor='v') and inherits parent columns onto partition leaves (281/282/283);
the two facts compose.

Note: the previous loop (#50/#51) was cut off by API usage limit AFTER adding the
slice 284 FIXTURE but BEFORE the assertion block. This loop added the assertion block,
verified, and is committing.

Fixture: `CREATE TABLE public.pvna (va integer, vb integer GENERATED ALWAYS AS (va * 2)
VIRTUAL) PARTITION BY LIST (va)` + `CREATE TABLE public.pvna_1 PARTITION OF public.pvna
FOR VALUES IN (1)`.
Asserted: pvna_1 block has `va integer` + inline `vb integer GENERATED ALWAYS AS (va * 2)`;
NO trailing STORED on vb; NO standalone `ALTER COLUMN vb SET DEFAULT`; `ATTACH PARTITION
public.pvna_1 FOR VALUES IN (1)` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — pvna fixture (after pgna_1, added by prev
  loop) + assertion block (after pgna_1 ATTACH assertion, added this loop).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 284 section + Next (285) note.
- .ralph/fix_plan.md — slice 284 progress (loop #52).

Gates: gofmt clean; go build clean; TestPort_PgDumpConnectionSetup PASS (3.89s, vs real
pg_dump 18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 285+): a multi-column / NULL-typed DEFAULT variant on the partition-leaf
ALTER path; OR an inherited generated column whose expression references a second
inherited column (multi-attr generation deparse through the leaf).
