(idle — nothing in flight)

Last landed: DU-002 slice 192 (loop #5) — per-leaf-partition TABLESPACE clause
(parser sibling-path gap).

What happened: PG's `CREATE TABLE … PARTITION OF …` grammar admits OptTableSpace
(after OptWith/OnCommitOption). The non-partition CREATE TABLE path already
accepts-and-discards TABLESPACE (ddl.go ~2248; storage manager doesn't honour
tablespaces), but the partition-child arm returned after WITH/ON COMMIT, so a
trailing `TABLESPACE name` left the token unconsumed → syntax error. Fix:
mirrored the main path in the partition-child arm (acceptKeyword(KwTablespace) +
parseIdent, discard). reltablespace stays 0 (default sentinel), so pg_dump emits
no TABLESPACE clause and the child round-trips like an option-less leaf.

Files: internal/parser/ddl.go (partition-child arm, after ON COMMIT block),
internal/parser/gen_override_test.go (2 unit tests:
TestPartitionChildTablespaceClause, TestPartitionChildTablespaceAfterWith),
internal/testport/pgdump_connsetup_test.go (ptbs/ptbs_1 … TABLESPACE pg_default
fixture + no-spurious-TABLESPACE/WITH + ATTACH-bound assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 192), .ralph/fix_plan.md.
Gates: gofmt OK; go build ./... clean; go vet testport clean; parser +
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next (slice 193 candidates): (1) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453). (2) PG18 virtual
generated columns (GENERATED ALWAYS AS (expr) VIRTUAL): parser accepts the
keyword but attGeneratedFor (pg18_user_catalog_rows.go:809) always returns 's',
so a VIRTUAL column dumps as STORED — real divergence but runtime-heavy (needs
attgenerated='v' surfaced + non-stored read semantics). (3) partition-child
`USING method` (table_access_method_clause, before WITH) — another small parser
sibling-path gap if not yet handled.
