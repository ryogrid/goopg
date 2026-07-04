(idle — nothing in flight)

Last completed (loop #108, 2026-07-04): closed DU-002 slice 439 triage item 1
from loop #107's shortlist — `CREATE`/`ALTER ROLE` attributes beyond
LOGIN/PASSWORD/SUPERUSER (`CREATEDB`/`CREATEROLE`/`REPLICATION`/`BYPASSRLS`/
`CONNECTION LIMIT`/`VALID UNTIL`) were accept-and-ignore. `catalog.RoleAttrs`
gained the six fields; `applyRoleAttrOptions` (`internal/server/role_ddl.go`)
now parses all of them; `wal.RoleStatePayload` carries them through the
crash-tail; `executor.SyncPgAuthidFile`/`ReadPgAuthidRows`/
`initdb.LoadRolesFromAuthidHeap` carry the four bools + ConnLimit through the
pg_authid heap file; the live `pg_authid` `VirtualRows` (what pg_dump/
pg_dumpall actually query) now renders real values instead of the old
hardcoded `f`/`f`/`f`/`f`/`-1`/NULL. New `internal/server/
role_ddl_attrs_test.go`; `internal/wal/role_ddl_test.go` and
`internal/initdb/role_ddl_recovery_test.go`'s `TestPgAuthidSyncLoadRoundTrip`
extended. Deferred: `VALID UNTIL` does not round-trip through the pg_authid
heap file (`buildAuthidUserRow` always writes NULL for `rolvaliduntil`) —
needs a PG-timestamp-literal parser (`PGTimestampIn`-equivalent), out of
scope this loop; ledger row appended (status `-`). Design doc
`root-0021-role-auth-persistence.md` new "Follow-up" section;
`docs/design/README.md`'s root-0021 row updated. Committed and pushed this
loop.

Next step: continue the M0119-0004 pg_dump catalog-view parity battery, OR
pick triage item 2 from loop #107's shortlist: view `WITH CHECK OPTION` is
captured for pg_dump fidelity (`internal/parser/ddl.go`
`parseCreateViewTail`, ~line 2382-2399, sets `CheckOption`) but never
enforced at runtime — no `23514 new row violates check option for view`
anywhere in the executor. That needs INSERT/UPDATE-through-view enforcement
wired into the executor's view-write path. Item 3 (FDW HANDLER/VALIDATOR
function references parsed and discarded, `internal/parser/ddl.go:464`)
ranks last — likely entangled with a general regproc-OID-resolver gap.
Before picking, re-verify still open (deferral ledger triage has repeatedly
found "open" rows already fixed) and prefer smallest blast radius first.

Gates run this loop: go build ./... clean; go vet clean on internal/server,
internal/catalog, internal/initdb, internal/executor, internal/wal;
go test ./internal/server/... ./internal/catalog/... ./internal/initdb/...
PASS; go test ./internal/executor/... PASS; go test -race ./internal/wal/...
PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); pgbench smoke =
pre-commit hook; make ralph-state-guard OK (self-repaired the same recurring
benign progress.json "completed" artifact noted in prior loops' carries).
