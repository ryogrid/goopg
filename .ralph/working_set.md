(idle — nothing in flight)

Last loop (#24): M0119-0004 **identity-column sequence options round-trip in
pg_dump** — LANDED (DU-002 slice 303). Design
`0119-0004-identity-sequence-options`.

A `GENERATED … AS IDENTITY (sequence_options)` column's backing sequence now
round-trips INCREMENT BY / MINVALUE / MAXVALUE / CACHE / CYCLE, not just START
WITH. Parser captured only START; executor hard-coded increment=1/cache=1/
no-cycle/type-min-max.

- ast.go / catalog.go: ColumnDef + Column new fields
  `Identity{Increment,Min,Max,Cache}*int64` + `IdentityCycle bool`.
- ddl.go: identity arm now parses full sequence-option grammar (mirrors
  parseCreateSequenceTail).
- operators_ddl.go: thread to `RegisterSequence(... increment, min, max, cycle)`
  + `SetSequenceCache`; struct-literal realigned (NOTE: gofmt version-mismatch —
  the `scanRow` comment line at ~7949 is left single-spaced to match HEAD; do
  NOT `gofmt -w` whole file).
- testport/pgdump_connsetup_test.go: slice 303 (`idrich` all opts + CYCLE;
  `idbd` BY DEFAULT + explicit increment + NO MINVALUE/NO MAXVALUE). Pinned
  byte-for-byte vs real pg_dump 18.3.

Gates run: TestPort_PgDumpConnectionSetup PASS; parser+executor+catalog PASS;
build clean; pgbench smoke via pre-commit.

NEXT loop — remaining open under M0119-0004:
- **CREATE SEQUENCE … OWNED BY schema.table.column** — surfaced this loop:
  goopg mis-resolves the 3-part qualified owner (`sequence cannot be owned by
  relation "public"`). Likely in the OWNED BY parse (parseCreateSequenceTail
  ~ddl.go:4136) — it parses owner.String() then optional `.col`, but a
  schema.table.column splits wrong. Good next slice (sequences with OWNED BY
  also dump an `ALTER SEQUENCE … OWNED BY`).
- pg_dump 002–010 catalog-view parity battery (more slices, slice-by-slice via
  TestPort_PgDumpConnectionSetup).
- extended-protocol commit-time deferral (architecturally entangled).
Other M0119: M0119-0002 (CLOG store swap Part B — highest blast radius,
dedicated full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).
