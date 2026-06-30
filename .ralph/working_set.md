(idle — nothing in flight)

Last landed: M0119-0004 **architectural finding** (loop #83, DOCUMENTATION — no code change).
Closed the DU-002 ACL **GRANT round-trip** slice thread (330–356): it is COMPLETE for every
object class goopg serves VIRTUALLY (table/sequence `relacl`, schema `nspacl`, function
`proacl`). The three still-open cases (`typacl`, `attacl`, `datacl`) share ONE root cause,
now documented in design `0119-0004-acl-grant-heap-vs-virtual-typacl.md`:

  GRANT is recorded SERVER-SIDE (`internal/server/query.go:69-87`) with only the in-memory
  ACL store in scope — NO executor `*Context`, so NO heap write. That works for the virtual
  catalogs (pg_class/pg_namespace virtual builders + `pg_proc_view.go:388` project the ACL
  live; `execCreateFunction` writes NO pg_proc heap row) but NOT for `pg_type` — the ONLY
  user catalog written to REAL heap rows (`writeHeapRowCanonical` at CREATE TYPE/DOMAIN bakes
  `typacl=NULL`) for M0097-0022 PG-standby compat. No virtual pg_type overlay → `getTypes`
  reads NULL → `GRANT USAGE ON TYPE` is a silent no-op (`grant_ddl.go:137` bails). `attacl`
  (pg_attribute heap) same; `datacl` same AND `--create`-only (untestable under `--no-create`).

Files this loop: docs/design/0119-0004-acl-grant-heap-vs-virtual-typacl.md (new),
docs/design/README.md (row 0119-0004bg), .ralph/fix_plan.md (ledger + new task
M0119-0004-ACLHEAP), .ralph/deferral_ledger.md. Gates: `go build ./...` clean;
`make ralph-state-guard` OK; pgbench smoke = pre-commit hook.

NEXT (M0119-0004-ACLHEAP — the next REAL task, milestone-sized not a slice): route GRANT on a
heap-backed object through `dispatchSimpleQueryViaExecutor` (Context in scope) + executor
GRANT op that updates the ACL store AND re-syncs the pg_type heap `typacl` (mirror
`deleteTypeFromCatalogHeap` delete+reinsert; new `TypeACLText` = `relaclTextLockedFor` over
`{USAGE}`/`acldefault('T',owner)`); gates MUST include TestE2E_PhysicalReplication (standby
reads pg_type) + TPC-H Q12/Q13 + pgbench. See the design doc §"Forward plan" for the full
step list and the de-risking catalog accessors (LookupEnum/Domain/CompositeType ...ByOID).
Alternatively pick a different open milestone (M0119-0005 pg_waldump server tier, etc.).
