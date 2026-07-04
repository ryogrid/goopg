(idle — nothing in flight)

Loop #10 implemented and landed the last open resume point of the DU-002
slice-440 ledger row: `ALTER SCHEMA name RENAME TO newname` / `ALTER SCHEMA
name OWNER TO role`. Previously "schema" fell into the blanket
schema/view/collation/... compat-stub loop in parseAlter (silent no-op, same
bug class ALTER VIEW had before slice 440), and goopg had no
RenameSchema/schema-owner mechanism in internal/catalog at all.

Landed this loop:
- `catalog.InMemory`: new `schemaOwners map[string]uint32` (default =
  bootstrap superuser 10), `SchemaOwnerOID`/`SetSchemaOwner`/
  `SetSchemaOwnerDuringRecovery`, and `RenameSchema(old, new)
  ([]*Table, error)` — re-keys `schemas`/`schemaOwners` AND cascades every
  `tables`/`indexes` entry whose `Schema` names the old schema (goopg keys
  those maps by schema NAME not OID, unlike real PG's `relnamespace`),
  returning moved sequences so the executor can also cascade the
  executor-side `seqRegistry`. `pg_namespace.nspowner` now reads
  `SchemaOwnerOID` instead of a hardcoded `"10"`.
- `parser`: new `AlterSchemaStmt{Name, Action, NewName, NewOwner}`;
  dedicated `parseAlter` case for `"schema"` (removed from the compat-stub
  catch-all), parsing RENAME TO / OWNER TO (CURRENT_USER/SESSION_USER/
  CURRENT_ROLE → "current_user" sentinel).
- `internal/server/dispatch.go` `ddlTag` → `"ALTER SCHEMA"`;
  `internal/planner/planner.go` routes `*parser.AlterSchemaStmt` to the
  generic `DDL` node (was missing — surfaced as "unsupported statement
  type" during live verification even after parser+executor were wired).
- `internal/executor/operators_ddl.go`'s new `execAlterSchema`: "rename"
  pre-checks both ways for `3F000`/`42P06`, calls `catalog.RenameSchema`,
  WAL-logs, then for each moved sequence mirrors execAlterTable's SET
  SCHEMA-on-single-sequence cascade (RenameSequence + WAL DropSequence +
  WALLogSequenceState + VirtualRows refresh). "owner" validates the role
  via `im.RoleOID` (42704 on miss) then `SetSchemaOwner` + WAL-log.
- New WAL kinds 100/101 (`RecordKindAlterSchemaRename`/`Owner`,
  `internal/wal/schema_alter_ddl.go`), physical-replay no-op case added to
  the existing `RecordKindCreateSchema, RecordKindDropSchema` case in
  `wal.ApplyRecord`, recovery replay wired into
  `internal/initdb/schema_ddl_recovery.go`.
- Tests: `internal/executor/alter_schema_test.go`,
  `internal/wal/schema_alter_ddl_test.go`,
  `internal/initdb/schema_ddl_recovery_test.go`'s new
  `TestSchemaDDLRecoveryReplaysAlterRenameOwner`.
- Docs: `docs/design/0110-0001-pg-dump-tap-port.md` new "Follow-up: ALTER
  SCHEMA..." section + `docs/design/README.md` index sentence appended.
- Deferral ledger row appended: `RenameSchema`'s cascade covers
  tables/indexes/sequences only — `opClassSchemas`/`statisticsObjs` (other
  schema-name-keyed registries) are NOT re-pointed on schema rename yet.

Gates run and PASSING this loop: `go build ./...` clean; targeted package
tests (wal/initdb/catalog/parser/executor/planner/server) all PASS; unit
pre-commit gate (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`)
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); live end-to-end
verification against psql including a full server restart (schema
rename+owner+table data+sequence continuity all survived). `make
ralph-state-guard` clean after self-repair (same benign marker mismatch
every loop). NOT YET committed/pushed at end of this loop response — next
step is `git add` the listed files + commit + push (pre-commit hook will
run the pgbench TPC-B smoke automatically), then re-run
`make ralph-state-guard` once more post-commit.

Next candidate task after this commits: pick the next open item from
.ralph/fix_plan.md / the deferral ledger's oldest unresolved rows (e.g. the
opClassSchemas/statisticsObjs schema-rename-cascade gap just recorded, or
the slice-439 resume-point-(2) generic OWNER-TO role-existence check for
the shared execAlterTable branch, still open for TABLE/SEQUENCE/VIEW).
