Task: M0119-0004-ACLHEAP — ACL re-sync from the GRANT path for heap-backed catalogs.
typacl (TYPE/DOMAIN) round-trip is COMPLETE (loop #87). This loop (#88) landed the
`attacl` (column GRANT) STEP-2 catalog building block — the column analogue of loop
#84's typacl renderer. Low blast: nothing on the GRANT path calls it yet, live
behaviour unchanged. The milestone item stays unchecked; the high-blast-radius attacl
half (parser + heap-write + read hook + DU-002 slice) is the remaining follow-up.

LANDED loop #88 (internal/catalog/catalog.go + relacl_test.go):
  - attrACLKey{relOID uint32; attNum int16} — a STRUCT key, NOT a packed
    relOID<<16|attnum uint32 (overflows for real table OIDs >2^16).
  - New composite-keyed stores attrACLs (grantee→priv→grant-option) + attrACLOrder
    (grant order), init'd in NewInMemory.
  - attrACLPrivOrder = {INSERT/a, SELECT/r, UPDATE/w, REFERENCES/x} (column-grantable
    subset, canonical aclitemout bit order).
  - GrantColumnPrivilege / GrantColumnPrivilegeWithGrantOption / RevokeColumnPrivilege
    + AttrACLText(relOID, attNum) renderer. ALL FOUR added to the Catalog interface.
  - KEY DESIGN DIVERGENCE: a column's acldefault('c',owner) is EMPTY (no owner/PUBLIC
    implicit privilege) → attacl is NULL until first GRANT, NO leading owner aclitem,
    returns to NULL on last revoke (table empties to {}, column to NULL). So the
    renderer CANNOT reuse relaclTextLockedFor (always prepends owner) and the
    relACLEmptied/relACLOwnerRevoked machinery does NOT apply. Dedicated renderer.
  - Tests TestAttrACLText / …GrantWithGrantOption / …Revoke / …GranteeNameRendering
    (mixed-case all-alnum stays UNQUOTED per PG putid; only unsafe chars quoted).
  - GATES: full catalog pkg PASS; -race ACL tests PASS; go build ./... clean; go vet
    executor/server/catalog clean; gofmt issues are PRE-EXISTING version-mismatch
    (confirmed via git stash — committed files flag too; NOT mine). pgbench=pre-commit.

NEXT (the attacl high-blast-radius half — a fresh DEDICATED loop, full gate set):
  1. Parser: capture `GRANT priv (col,...) ON TABLE t TO role` into a new
     CompatNoopStmt.AttrACL *AttrACLChange (column-list = ON-detection signal; mirror
     buildTypeACLChange). REVOKE form too.
  2. executor (operators_ddl.go): execCompatNoop `if s.AttrACL != nil` →
     execAttrACLChange: resolve table+col → relOID/attnum, call GrantColumnPrivilege,
     then resyncAttrACLHeapRow — delete-old pg_attribute rows + syncTableToCatalogHeap
     (per pg_attribute_alter_needs_heap_resync memory); set row attacl via
     encodeAclItemArrayText(AttrACLText(relOID,attnum), RoleOID) / NullDatum.
  3. read: pg_attribute seqScanOp attacl decode hook (sibling of the pg_type typacl
     hook in operators_storage.go; decodeAclItemArrayText + RoleNameForOID).
  4. query.go: ensure column GRANT (ON TABLE with column list) routes via
     dispatchSimpleQueryViaExecutor (isHeapACLObject) — column GRANT is heap-backed.
  5. DU-002 slice: `GRANT SELECT (col) ON TABLE t TO role` → assert pg_dump emits it.
  Gates: DU-002 connsetup + TestE2E_PhysicalReplication + -race exec/catalog/storage/
  mvcc + TPC-H Q12=2/Q13=33 + executor/catalog/parser/server units + pgbench.
NOTE: datacl stays permanently deferred (pg_database heap-backed AND --create-only,
untestable under the --no-create connsetup harness).
