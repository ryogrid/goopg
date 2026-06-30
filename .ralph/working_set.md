Task: M0119-0004-ACLHEAP — ACL re-sync from the GRANT path for heap-backed catalogs.
This loop (#84) landed the FIRST self-contained building block (step-2 renderer half),
low blast radius, committed. The milestone continues — the heap-write half remains.

LANDED loop #84 (renderer half of step 2, behaviour-neutral — no GRANT path calls it):
  - `catalog.InMemory.TypeACLText(typeOID)` (internal/catalog/catalog.go) — pg_type
    analogue of `ProcACLText`; delegates to `relaclTextLockedFor` with new
    `typeACLPrivOrder = {USAGE/'U'}` + `ownerTypeACLString = "U"`. Added to `Catalog`
    interface. `acldefault('T',owner) = {=U/owner,owner=U/owner}` == function EXECUTE
    default shape, so machinery reused verbatim.
  - Tests (internal/catalog/relacl_test.go): TestTypeACLText / …GrantWithGrantOption /
    …RevokeFromPublic / …RevokeFromOwner — mirror ProcACL goldens with USAGE.
  - Gates: `go build ./...` clean; full `internal/catalog` suite PASS; pgbench smoke =
    pre-commit hook. Design `0119-0004-acl-grant-heap-vs-virtual-typacl.md` + fix_plan
    updated with a Progress note (box stays UNCHECKED).

NEXT (the high-blast-radius half — a dedicated full-gate loop, see design doc
§"Forward plan" / §"Progress"):
  1. Route GRANT/REVOKE ON TYPE/DOMAIN (and column-level) through
     `dispatchSimpleQueryViaExecutor` (Context in scope) instead of the
     `internal/server/query.go:69-87` server short-circuit. Keep the fast path for
     the virtual classes (table/seq/schema/function).
  2b. Executor GRANT op: update the OID-keyed ACL store (same calls as
     `grant_ddl.go`) THEN re-sync the heap pg_type row — mirror
     `deleteTypeFromCatalogHeap` (operators_ddl.go:10455) delete+reinsert via
     `writeHeapRowCanonical`, filling `typacl` from the new `TypeACLText(oid)`.
     Resolve TYPE/DOMAIN name→OID via LookupEnum/Domain/CompositeType(...ByOID).
  3. `mirrorCatalogRelToPostgresDB(ctx, TypeRelationId)` for standby/basebackup.
  4. Gates (MANDATORY): new DU-002 connsetup TYPE-grant slice (pg_dump stdout vs PG
     18.3) + TestE2E_PhysicalReplication + TPC-H Q12/Q13 + pgbench + executor/catalog/
     parser suites. Re-init data dir if pg_type row layout touched.
`attacl` follows the same template (pg_attribute heap re-sync precedent: memory
`pg_attribute_alter_needs_heap_resync`); `datacl` stays deferred (`--create`-only).
