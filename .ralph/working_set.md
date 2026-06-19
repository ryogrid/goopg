(idle — nothing in flight)

Last landed: DU-002 slice 190 (loop #3) — DEFAULT (catch-all) partition
round-trip pinned in TestPort_PgDumpConnectionSetup.

What happened: goopg already supported DEFAULT partitions end-to-end (parser
PartitionOfClause.Default → executor PartitionBound.IsDefault →
catalog.FormatPartitionBound returns "DEFAULT" → stored in relpartbound), so
pg_dump's `ATTACH PARTITION child DEFAULT` already round-tripped, but no fixture
had pinned it. Added `pdef` LIST parent + `pdef_1 FOR VALUES IN (1)` +
`pdef_def DEFAULT` and asserted the dump. The new fixture's
`ATTACH PARTITION public.pdef_def DEFAULT;\n` line collided with slice-90's broad
empty-DEFAULT domain regression check (`Contains(stdout,"DEFAULT;\n")`) — fixed by
scrubbing that exact attach line before the domain scan (sibling-paths: new
fixture + old assertion updated together in one loop).

Files: internal/testport/pgdump_connsetup_test.go (pdef fixture + asserts +
slice-90 check tightened), docs/design/0110-0001-pg-dump-tap-port.md (Slice 190),
.ralph/fix_plan.md (progress note).
Gates: gofmt OK; go build ./... clean; go vet ./internal/testport clean;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next (slice 191 candidates): (1) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453). (2) per-child
partition storage params `... PARTITION OF parent ... WITH (fillfactor=70)` or
TABLESPACE clause round-trip. (3) MINVALUE/MAXVALUE keyword-AST-node — AVOID
(refactor of working partition-routing code).
