Task: DU-002 slice 285 (loop #53) — COMPLETE, ready to commit + push.

Last landed: a MULTI-ATTRIBUTE generation expression inherited onto a partition leaf
round-trips. Where slices 283/284 referenced a SINGLE column in the generation clause
(`ga * 2`), slice 285's `gb` is `GENERATED ALWAYS AS (ga + gc) STORED` — a binary
expression over two plain columns. SAME render path as slice 283 (attgenerated forces
attrdefs[].separate=false unconditionally, pg_dump.c:9507; ispartition forces
shouldPrintColumn true for every column, slices 281/282) → leaf body prints
`gb integer GENERATED ALWAYS AS (ga + gc) STORED` inline. NEW fact under test: each Var
resolves to the correct inherited column NAME on the leaf (not attnum-shifted/dropped/
swapped). NO production change — goopg already deparses multi-column generation
expressions (slice 59) and inherits parent columns onto partition leaves (281–284).

Fixture: `CREATE TABLE public.pgmc (ga integer, gc integer, gb integer GENERATED ALWAYS
AS (ga + gc) STORED) PARTITION BY LIST (ga)` + `CREATE TABLE public.pgmc_1 PARTITION OF
public.pgmc FOR VALUES IN (1)`.
Asserted: pgmc_1 block has `ga integer` + `gc integer` + inline `gb integer GENERATED
ALWAYS AS (ga + gc) STORED`; `ATTACH PARTITION public.pgmc_1 FOR VALUES IN (1)` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — pgmc fixture (after pvna_1) + assertion
  block (after pvna_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 285 section + Next (286) note.
- .ralph/fix_plan.md — slice 285 progress (loop #53).

Gates: gofmt clean; go build clean; TestPort_PgDumpConnectionSetup PASS (3.78s, vs real
pg_dump 18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 286+): a multi-column / NULL-typed DEFAULT variant on the partition-leaf
ALTER path; OR a generated column whose expression references the partition-key column
itself (Var-to-key deparse through the leaf).
