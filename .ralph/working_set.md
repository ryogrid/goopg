Task: DU-002 slice 286 (loop #54) — COMPLETE, ready to commit + push.

Last landed: a FORWARD-REFERENCE generation expression inherited onto a partition leaf
round-trips. Where slices 283/285 referenced columns declared BEFORE the generated column
(`gb` attnum 3 over `ga`/`gc` attnum 1/2), slice 286's `gz` is attnum 1 and references
`ya` (attnum 2) + `yc` (attnum 3) — both declared AFTER it. PG puts every table column in
scope for a generation expr regardless of declaration order, so `(ya + yc)` is a legal
forward reference. SAME render path as slice 285 (attgenerated forces attrdefs[].separate=
false unconditionally, pg_dump.c:9507; ispartition forces shouldPrintColumn true for every
column, slices 281/282) → leaf body prints columns in attnum order: inline
`gz integer GENERATED ALWAYS AS (ya + yc) STORED` FIRST, then `ya integer`, `yc integer`.
NEW fact under test: generation deparse resolves each Var by column NAME, not via a
forward-only positional scan (which would see neither operand). NO production change —
goopg resolves generation cols by name (evalGeneratedExpr over catalog.Column) and inherits
parent columns onto partition leaves (281–285); the two compose.

Fixture: `CREATE TABLE public.pgfr (gz integer GENERATED ALWAYS AS (ya + yc) STORED,
ya integer, yc integer) PARTITION BY LIST (ya)` + `CREATE TABLE public.pgfr_1 PARTITION OF
public.pgfr FOR VALUES IN (1)`.
Asserted: pgfr_1 block has inline `gz integer GENERATED ALWAYS AS (ya + yc) STORED` BEFORE
`ya integer` + `yc integer` (ordering check via strings.Index); `ATTACH PARTITION
public.pgfr_1 FOR VALUES IN (1)` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — pgfr fixture (after pgmc_1) + assertion
  block (after pgmc_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 286 section + Next (287) note.
- .ralph/fix_plan.md — slice 286 progress (loop #54).

Gates: gofmt clean; go vet clean; TestPort_PgDumpConnectionSetup PASS (3.75s, vs real
pg_dump 18.3); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 287+): a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER
path; OR a generated column whose expression mixes a forward + backward Var reference plus
a literal (e.g. `gz = ya + 1 + yc`).
