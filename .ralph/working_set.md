Task: M0119-0004-ACLHEAP — ACL re-sync from the GRANT path for heap-backed catalogs.
Loop #86 landed the THIRD building block (the PG-native aclitem[] binary codec),
behaviour-neutral, committed. The milestone continues — the dispatch reroute +
heap-write half remains (a dedicated full-gate loop).

LANDED loop #86 (aclitem[] codec, behaviour-neutral — NO caller yet):
  - internal/executor/codec_aclitem.go — `encodeAclItemArrayText(aclText, resolveOID)`
    parses canonical aclitemout text `{grantee=privs/grantor,…}` → on-disk _aclitem
    ArrayType varlena (24-byte 1-D no-NULL header, elemtype 1033, one 16-byte AclItem
    per entry: grantee Oid + grantor Oid + privs AclMode uint64, low32=privs / high32=
    grant-option per acl.h `priv<<32`; empty grantee = ACL_ID_PUBLIC 0). Pure: role
    name↔OID via injected callbacks (no catalog dep). `decodeAclItemArrayText(blob,
    resolveName)` inverts. Priv letters `"arwdDxtXUCTcsAm"` (idx i → bit 1<<i); role
    quoting mirrors PG putid.
  - Tests codec_aclitem_test.go: priv-letter↔AclMode, BYTE-EXACT golden for
    `{=U/postgres}` (40 bytes), round-trip (PUBLIC/owner/owner+PUBLIC+grantee/grant-
    option/multi-priv/non-owner-grantor), empty (owner-revoke-all → valid empty
    _aclitem), quoted-role-with-comma. Full executor suite PASS; build clean.
  - WHY this loop: wiring loop #85's parser capture revealed the heap codec could NOT
    encode a non-empty aclitem[] (codec.go case "aclitem[]" only does KindBytes-passthru
    or emptyArrayTypeBytes(1033) → NewStringDatum silently drops the ACL; encodeArrayValuePG
    has no aclitem entry → mis-types as text/elemtype-25). This primitive closes that gap.

PRIOR: loop #84 renderer TypeACLText (catalog.go:8793, typeACLPrivOrder{USAGE/'U'},
ownerTypeACLString "U"); loop #85 parser capture (CompatNoopStmt.TypeACL *TypeACLChange).

NEXT (the high-blast-radius half — a dedicated full-gate loop):
  1. internal/server/query.go:69-87 — exclude " ON TYPE " / " ON DOMAIN " (in `upper`)
     from the GRANT/REVOKE fast path so an autocommit type/domain ACL change FALLS
     THROUGH to dispatchSimpleQueryViaExecutor (line 232). Keep the fast path for
     table/seq/schema/function. The executor already emits the GRANT/REVOKE tag +
     handles txn state (explicit-txn GRANT already routes there).
  2b. execCompatNoop (operators_ddl.go:12342): when `s.TypeACL != nil`, resolve each
     TypeName→OID (catalog LookupEnum/LookupDomain/LookupCompositeType; ByOID variants
     at catalog.go:9861/10283/10153), update OID-keyed ACL store like recordFunctionGrant
     but USAGE (seed owner via MaterializeOwnerACL(oid,"postgres",["USAGE"]) + PUBLIC via
     GrantTablePrivilege(oid,"PUBLIC","USAGE") when TypeACLText(oid)=="", then apply
     grantee changes; REVOKE mirror). Then re-sync the pg_type heap row:
     deleteTypeFromCatalogHeap(ctx,DefaultDBOid,oid,Tx.XID) + rebuild via
     buildUserPGTypeRowFor{Enum,Domain,Composite} + set row[31] (typacl) =
     NewBytesDatum(encodeAclItemArrayText(TypeACLText(oid), roleOID-registry-resolver))
     + writeHeapRowCanonical(ctx,typeRel,pgTypeColumnsPG18(),row). Gate on
     catalogHeapSyncAvailable(ctx). NOTE codec.go:584 case "aclitem[]" passes a KindBytes
     blob through verbatim — so NewBytesDatum is the right datum kind.
  2c. Wire decodeAclItemArrayText into the SELECT … typacl FROM pg_type read path so
     pg_dump/getTypes reads back `{...}` text (find the aclitem[] heap decode site; today
     it text-fallbacks via decodeArrayValuePG which would NOT decode 16-byte AclItems).
     Need a role OID→name resolver from the per-role OID registry.
  3. mirrorCatalogRelToPostgresDB(ctx, catalog.TypeRelationId).
  4. Gates (MANDATORY): new DU-002 connsetup TYPE-grant slice (CREATE TYPE public.gtyp
     AS ENUM(...); CREATE ROLE typg; GRANT USAGE ON TYPE → assert `GRANT ALL ON TYPE
     public.gtyp TO typg;` in pg_dump stdout — USAGE is the only type priv so pg_dump
     renders ALL, like functions/EXECUTE) + TestE2E_PhysicalReplication + TPC-H Q12/Q13
     + pgbench + executor/catalog/parser suites. Re-init data dir (pg_type row now carries
     a populated typacl).
NOTE: newVMFixture has catalogHeapSyncAvailable=false → the heap re-sync can't be unit-
tested in isolation; its real gate is the DU-002 connsetup pg_dump round-trip. Test slice
+ assert pattern: internal/testport/pgdump_connsetup_test.go (CREATE at ~L4043 mood enum;
GRANT-on-function slice 345 at ~L3450; assertions at ~L7700). `attacl` follows same
template; `datacl` deferred (--create only, untestable in --no-create harness).
