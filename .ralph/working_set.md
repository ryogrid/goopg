Task: M0119-0004-ACLHEAP — ACL re-sync from the GRANT path for heap-backed catalogs.
Loop #87 LANDED the high-blast-radius half: the `pg_type.typacl` TYPE/DOMAIN GRANT
round-trip is COMPLETE end-to-end and committed. The milestone item stays unchecked
because `attacl` (column GRANT) remains; `datacl` is permanently deferred.

LANDED loop #87 (typacl heap-write + read — FULL gate set PASS):
  - internal/server/query.go — `isHeapACLObject` (`" ON TYPE "`/`" ON DOMAIN "`) excludes
    those from the autocommit GRANT/REVOKE server fast path → falls through to
    dispatchSimpleQueryViaExecutor (line ~232). Virtual classes (table/seq/schema/func) keep
    the server recorder.
  - internal/executor/operators_ddl.go — execCompatNoop gained `if s.TypeACL != nil` →
    execTypeACLChange (resolveUserTypeOID over LookupEnum/Domain/CompositeType; ACL store
    update mirrors recordFunctionGrant/Revoke with USAGE; typeACLAllPrivs={USAGE}) →
    resyncTypeACLHeapRow (rebuild base pg_type row via buildUserPGTypeRowFor{Enum,Domain,
    Composite}, typacl=NewBytesDatum(encodeAclItemArrayText(TypeACLText(oid),RoleOID)) or
    NullDatum; deleteTypeFromCatalogHeap+writeHeapRowCanonical+mirrorCatalogRelToPostgresDB;
    gated on catalogHeapSyncAvailable).
  - internal/executor/codec.go — new `case "aclitem[]","_aclitem"` in
    decodePhysicalPGValueMctx returns the full _aclitem varlena as KindBytes (was a
    meaningless default-branch string).
  - internal/executor/operators_storage.go — seqScanOp.{typeACLCat,typeACLColIdx,
    typeACLOidIdx} armed in Open only for pg_type; Next() hook (after enum injector / post
    RUnlock) renders the KindBytes typacl to aclitemout text via decodeAclItemArrayText +
    catalog.InMemory.RoleNameForOID (NEW reverse resolver: 0→PUBLIC, 10→postgres, role→
    case-preserved name, else numeric).
  - Tests: internal/testport/pgdump_connsetup_test.go slice 357 (CREATE TYPE public.gtype +
    CREATE ROLE typg_grantee + GRANT USAGE ON TYPE → assert `GRANT ALL ON TYPE public.gtype
    TO typg_grantee;`); catalog TestRoleNameForOID; executor TestAclItemHeapDecodeCase.
  - GATES (all PASS): DU-002 connsetup; TestE2E_PhysicalReplication; -race
    executor/catalog/storage/mvcc; executor/catalog/parser/server units; TPC-H Q12=2/Q13=33;
    pgbench (pre-commit). gofmt: only pre-existing version-mismatch alignment diffs (NOT mine).

NEXT (the `attacl` follow-up — a fresh slice, NOT this loop):
  Reuse the SAME template against heap-backed pg_attribute. Steps:
  1. Parser: capture column-level GRANT `GRANT priv (col,...) ON TABLE t TO role` into a new
     CompatNoopStmt.AttrACL *AttrACLChange (mirror TypeACLChange; ON-column detection).
  2. catalog: AttrACLText renderer (relaclTextLockedFor over the column-priv order) + the
     OID-keyed ACL store keyed by (attrelid<<16 | attnum) or a dedicated map.
  3. executor: execAttrACLChange + resyncAttrACLHeapRow (delete-old pg_attribute rows +
     syncTableToCatalogHeap, per pg_attribute_alter_needs_heap_resync memory); set
     row attacl via encodeAclItemArrayText.
  4. read: pg_attribute seqscan attacl decode hook (sibling of the pg_type typacl hook).
  5. DU-002 slice: `GRANT SELECT (col) ON TABLE t TO role` → assert pg_dump emits it.
NOTE: `datacl` stays permanently deferred (pg_database heap-backed AND pg_dump --create-only,
untestable under the --no-create connsetup harness). Ledger row recorded 2026-06-30.
