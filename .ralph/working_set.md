(idle — nothing in flight)

Last landed: DU-002 slice 193 (loop #6) — per-leaf-partition `USING <access_method>`
clause (parser sibling-path gap).

What happened: PG's `CREATE TABLE … PARTITION OF …` grammar is
`OptPartitionSpec table_access_method_clause OptWith OnCommitOption OptTableSpace`,
so `USING method` sits between `PARTITION BY` and `WITH`. The partition-child arm
jumped from the optional PARTITION BY block straight to WITH, so `USING heap` left
the token unconsumed → syntax error (same sibling-path gap as slices 191/192). Fix:
inserted a USING trailer at the grammar position (after PARTITION BY, before WITH):
acceptKeyword(KwUsing)/acceptIdentKeyword("using") + parseIdent(), discard. relam
stays default (single heap AM), so pg_dump emits no USING clause; child round-trips
like an access-method-less leaf.

Files: internal/parser/ddl.go (partition-child arm, new USING block before WITH,
~line 1723), internal/parser/gen_override_test.go (TestPartitionChildUsingClause,
TestPartitionChildUsingBeforeWith), internal/testport/pgdump_connsetup_test.go
(puse/puse_1 … USING heap fixture + no-spurious-USING/WITH + ATTACH assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 193), .ralph/fix_plan.md.
Gates: gofmt OK; go build ./... clean; go vet testport clean; parser +
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next (slice 194 candidates): (1) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453). (2) PG18 virtual
generated columns (GENERATED ALWAYS AS (expr) VIRTUAL): attGeneratedFor always
returns 's' so a VIRTUAL column dumps as STORED — real divergence but
runtime-heavy. (3) inspect remaining partition-child trailers for any other
sibling-path gaps (none obvious left after USING/WITH/ON COMMIT/TABLESPACE).
