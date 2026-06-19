Task: DU-002 slice 282 (loop #49) — COMPLETE, ready to commit + push.

Last landed: a DEFAULT added to a partition leaf's INHERITED column via
`ALTER TABLE <leaf> ALTER COLUMN <col> SET DEFAULT <expr>` rides INLINE on the
printed column (`kb integer DEFAULT 7`), NOT as a standalone ALTER. DEFAULT analog
of slice 281; partition-INLINE twin of slice 269. Discriminator = the
`attrdefs[].separate` flag (pg_dump.c:9507-9535): pg_dump marks a default `separate`
(→ standalone ALTER) only on the `!shouldPrintColumn` branch; shouldPrintColumn
returns `attislocal[j] || ispartition` (pg_dump.c:9964) → every partition column
prints inline, separate stays false, DEFAULT joins the CREATE TABLE body. NO
production change — goopg already records the ALTER-path DEFAULT (AlterTableSetDefault,
added for slice 269: Column.DefaultExpr → pg_attrdef + atthasdef) and reports the leaf
as partition (slices 266/281). Verified byte-identical vs real PG 18.3.

Fixture: `CREATE TABLE public.pdfa (ka integer, kb integer) PARTITION BY LIST (ka)`
+ `CREATE TABLE public.pdfa_1 PARTITION OF public.pdfa FOR VALUES IN (1)`
+ `ALTER TABLE public.pdfa_1 ALTER COLUMN kb SET DEFAULT 7`.
Asserted: pdfa_1 block has `ka integer` + inline `kb integer DEFAULT 7`; NO standalone
`ALTER COLUMN kb SET DEFAULT 7` anywhere; `ATTACH PARTITION public.pdfa_1 FOR VALUES
IN (1)` survives.

Files:
- internal/testport/pgdump_connsetup_test.go — pdfa fixture (after pnna_1 ALTERs) +
  assertion block (after pnna_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 282 section + Next (283) note.
- .ralph/fix_plan.md — slice 282 progress (loop #49).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.59s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 283+): a generated-column (`GENERATED ALWAYS AS … STORED`)
inherited/partition body form (attgenerated forces separate=false, pg_dump.c:9507),
OR a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER path.
