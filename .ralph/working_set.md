(idle — nothing in flight)

Loop #9 closed out the M0110-0001 DU-002 slice-445-follow-up work that was
already implemented (but uncommitted) at loop start: ALTER STATISTICS
RENAME TO/OWNER TO/SET SCHEMA WAL persistence (kinds 97/98/99) + a
physical-replay gap fix for kinds 95-99 in wal.ApplyRecord (standby
streaming-replication path). This loop's own work was verification-only:
ran `scripts/tpch-spotcheck.sh` (PASS, Q12=2/Q13=33), committed
(`9f1f6bc5`), which triggered the pre-commit pgbench smoke hook (PASS, 3/3
workloads, 0 failed txns), pushed to
`origin/align-data-structure-with-pg`, and ran `make ralph-state-guard`
(self-repaired the same benign status/progress marker mismatch seen every
loop; clean after).

Next candidate task (NOT started — pick this up fresh next loop): plain
`ALTER SCHEMA name RENAME TO newname` / `ALTER SCHEMA name OWNER TO role`
— the last open resume point ((3) of the slice-440 deferral_ledger.md row,
line ~434) in the DU-002 ALTER-form audit thread. Unlike ALTER
SEQUENCE/VIEW (slices 439/440, both fixed by reusing `AlterTableStmt`/
execAlterTable), goopg has NO `RenameSchema`/schema-owner-change mechanism
anywhere in `internal/catalog` — needs new catalog support: rekey the
`catalog.Schema` registry entry AND re-point every contained table's
`.Schema` field on rename. Resume point per the ledger: grep
`type.*Schema.*struct` in internal/catalog/catalog.go, add a
`RenameSchema(old, new string) error` + owner field, wire a new
`parseAlter` case in internal/parser/ddl.go (currently swallowed by the
generic schema/view/collation/... catch-all).

Other still-open, smaller resume points from the same thread (lower
priority than ALTER SCHEMA, pick whichever is cheaper if ALTER SCHEMA
proves too large for one loop):
- slice-439 resume (2): execAlterTable's OWNER TO branch (shared by ALTER
  TABLE/SCHEMA/SEQUENCE) never validates the role exists — add a
  role-existence lookup raising PG's `role "..." does not exist`.
- slice-434 (DU-002): COMMENT ON a nonexistent object across ~20 object
  kinds is a silent no-op instead of PG's 42704 (systemic, was explicitly
  scoped OUT of slice 436 which only fixed the object-kind lookup for
  access method).

No implementation started on any of these yet — next loop should pick ONE.
