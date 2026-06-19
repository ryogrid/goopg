Task: DU-002 slice 281 (loop #48) — COMPLETE, committed + pushed.

Last landed: partition-leaf NOT NULL added via `ALTER TABLE ADD CONSTRAINT` routes
to the INLINE column form (ispartition discriminator). Twin of slice 280 but on the
partition INLINE path instead of the legacy-inheritance STANDALONE body path.
`shouldPrintColumn` = `attislocal[j] || ispartition` (pg_dump.c:9970) → partition
columns always print inline, so the standalone-body branch (pg_dump.c:17213) is
never reached; print_notnull renders `CONSTRAINT <name> NOT NULL` inline
(pg_dump.c:17178-17183). NO production change — goopg already exposes conislocal
NOT NULL pg_constraint rows + attnotnull (mninh/idfnd) and reports the leaf as a
partition (pnnl). Verified byte-identical vs real PG 18.3.

Fixture: `CREATE TABLE public.pnna (qa integer, qb integer, qc text) PARTITION BY
LIST (qa)` + `CREATE TABLE public.pnna_1 PARTITION OF public.pnna FOR VALUES IN (1)`
+ `ALTER TABLE pnna_1 ADD CONSTRAINT pnna_named NOT NULL qb` (non-default → inline
`qb integer CONSTRAINT pnna_named NOT NULL`) + `ALTER TABLE pnna_1 ADD CONSTRAINT
pnna_1_qc_not_null NOT NULL qc` (default → collapses to bare `qc text NOT NULL`).
Asserted: pnna_1 block has `qa integer`, `qb integer CONSTRAINT pnna_named NOT
NULL`, `qc text NOT NULL`; NO standalone `CONSTRAINT pnna_named NOT NULL qb`
anywhere; no `CONSTRAINT pnna_1_qc_not_null` leak; ATTACH bound survives.

Files:
- internal/testport/pgdump_connsetup_test.go — pnna fixture (after pnnl_1 ALTER) +
  assertion block (after pnnl_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 281 section + Next (282) note.
- .ralph/fix_plan.md — slice 281 progress (loop #48).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.55s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke (enforced by
.githooks/pre-commit on commit).

Next (slice 282+): a generated-column (`GENERATED ALWAYS AS … STORED`)
inherited/partition body form, OR a child-level DEFAULT added via `ALTER TABLE …
ALTER COLUMN … SET DEFAULT` on an inherited column (the DEFAULT analog of the
NOT NULL ALTER-path slices).
