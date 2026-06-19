Task: DU-002 slice 283 (loop #50) — COMPLETE, committed + pushed.

Last landed: a STORED GENERATED column inherited onto a partition leaf prints its
generation clause INLINE in the leaf body (`gb integer GENERATED ALWAYS AS (ga * 2)
STORED`), NOT as a standalone SET DEFAULT. Generated-column counterpart of slices
281/282 but a DIFFERENT discriminator branch: 281/282 held attrdefs[].separate=false
via shouldPrintColumn (ispartition prints every column); slice 283 holds separate=false
via attgenerated — pg_dump.c:9507 sets separate=false UNCONDITIONALLY when
attgenerated[adnum-1] != '' (a generation expr can never be a standalone SET DEFAULT).
NO production change — goopg already round-trips STORED generated cols on standalone
tables (slice 59: attgenerated='s' + pg_attrdef deparse) and inherits parent columns
onto partition leaves (slices 281/282); the two facts compose. Verified byte-identical
vs real PG 18.3.

Fixture: `CREATE TABLE public.pgna (ga integer, gb integer GENERATED ALWAYS AS (ga * 2)
STORED) PARTITION BY LIST (ga)` + `CREATE TABLE public.pgna_1 PARTITION OF public.pgna
FOR VALUES IN (1)`.
Asserted: pgna_1 block has `ga integer` + inline `gb integer GENERATED ALWAYS AS (ga * 2)
STORED`; NO standalone `ALTER COLUMN gb SET DEFAULT` anywhere; `ATTACH PARTITION
public.pgna_1 FOR VALUES IN (1)` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — pgna fixture (after pdfa_1 ALTER) +
  assertion block (after pdfa_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 283 section + Next (284) note.
- .ralph/fix_plan.md — slice 283 progress (loop #50).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS (3.68s,
byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit
on commit).

Next (slice 284+): a VIRTUAL generated column (GENERATED ALWAYS AS … VIRTUAL, slice 194's
attgenerated='v' form) inherited onto a partition leaf — same separate=false-via-attgenerated
branch but rendered WITHOUT trailing STORED; OR a multi-column / NULL-typed DEFAULT variant
on the partition-leaf ALTER path.
