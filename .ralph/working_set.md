Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 19 (empty
pg_foreign_server virtual view) COMPLETE this loop. NOTHING in flight;
next loop starts on slice 20 (empty pg_default_acl virtual view).

=== DONE (loop #42) — DU-002 slice 19 ===
getForeignServers runs `SELECT tableoid, oid, srvname, srvowner, srvfdw,
srvtype, srvversion, srvacl, acldefault('S', srvowner) AS acldefault,
array_to_string(ARRAY(SELECT … FROM pg_options_to_table(srvoptions) …), …)
AS srvoptions FROM pg_foreign_server` → aborted at `relation
"pg_foreign_server" does not exist`. Fix (catalog-only, mirrors slice 16):
- internal/catalog/catalog.go: added empty pg_foreign_server virtual view,
  OID 1417 (pg_foreign_server.h), beside pg_foreign_data_wrapper. Cols:
  oid, srvname name, srvowner oid, srvfdw oid, srvtype text, srvversion text,
  srvacl aclitem[], srvoptions text[]. VirtualRows = nil (empty by
  construction; goopg has no CREATE SERVER). The correlated
  pg_options_to_table(srvoptions) ARRAY subquery (slice 18, already working)
  is never evaluated → NO new SRF work, as predicted.
- Design doc 0110-0001 slice-19 block added; pgdump_connsetup_test.go header
  updated; fix_plan loop #42 entry added.
Gates: build/gofmt/vet clean; catalog suite PASS; TestPort_PgDumpConnectionSetup
PASS. tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count
risk).

=== NEXT STEP — DU-002 slice 20 (pg_default_acl virtual view) ===
After slice 19, getForeignServers passes. getUserMappings short-circuits
(goopg has no foreign servers → no catalog query), so pg_dump advances
straight to getDefaultACLs → `relation "pg_default_acl" does not exist`
(VERIFIED empirically by TestPort_PgDumpConnectionSetup, NOT the predicted
pg_user_mappings). The query is:
  SELECT oid, tableoid, defaclrole, defaclnamespace, defaclobjtype, defaclacl,
  CASE WHEN defaclnamespace = 0 THEN acldefault(CASE WHEN defaclobjtype = 'S'
  THEN 's'::"char" ELSE defaclobjtype END, defaclrole) ELSE '{}' END AS
  acldefault FROM pg_default_acl
Add the empty pg_default_acl virtual view in internal/catalog/catalog.go
(beside pg_foreign_server), OID 826 (VERIFIED via pg_default_acl.h
CATALOG(pg_default_acl,826,...)). Cols (pg_default_acl.h): oid,
defaclrole oid, defaclnamespace oid, defaclobjtype "char", defaclacl
aclitem[]. Empty by construction (goopg defines no default-ACL entries).
No new SRF/expr work expected. RUN TestPort_PgDumpConnectionSetup after to
find the REAL next blocker.

ORTHOGONAL PRE-EXISTING (track separately, irrelevant to empty pg_dump views):
reading a text[] column back from the heap yields the BINARY array encoding
(KindString w/ raw bytes), not the text repr expandArrayDatum parses.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
