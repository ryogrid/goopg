Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 20 (empty
pg_default_acl virtual view) COMPLETE this loop. NOTHING in flight;
next loop starts on slice 21 (pg_conversion virtual view).

=== DONE (loop #43) — DU-002 slice 20 ===
getDefaultACLs runs `SELECT oid, tableoid, defaclrole, defaclnamespace,
defaclobjtype, defaclacl, CASE WHEN defaclnamespace = 0 THEN
acldefault(CASE WHEN defaclobjtype = 'S' THEN 's'::"char" ELSE
defaclobjtype END, defaclrole) ELSE '{}' END AS acldefault FROM
pg_default_acl` → aborted at `relation "pg_default_acl" does not exist`.
Fix (catalog-only, mirrors slice 19):
- internal/catalog/catalog.go: added empty pg_default_acl virtual view,
  OID 826 (pg_default_acl.h), beside pg_foreign_server. Cols: oid,
  defaclrole oid, defaclnamespace oid, defaclobjtype "char", defaclacl
  aclitem[]. VirtualRows = nil (empty by construction; goopg has no
  ALTER DEFAULT PRIVILEGES). CASE/acldefault projection never evaluated
  → NO new expression work, as predicted.
- Design doc 0110-0001 slice-20 block added; pgdump_connsetup_test.go
  header updated (next blocker → pg_conversion); fix_plan loop #43 entry.
Gates: build/gofmt/vet clean; catalog suite PASS;
TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive empty
virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 21 (pg_conversion virtual view) ===
After slice 20, getDefaultACLs passes. pg_dump advances to getConversions
→ `relation "pg_conversion" does not exist` (VERIFIED empirically by
TestPort_PgDumpConnectionSetup). The query is:
  SELECT tableoid, oid, conname, connamespace, conowner FROM pg_conversion
Add the pg_conversion virtual view in internal/catalog/catalog.go (beside
pg_default_acl), OID 2607 (pg_conversion.h CATALOG(pg_conversion,2607,...)).
Cols (pg_conversion.h): oid, conname name, connamespace oid, conowner oid,
conforencoding int4, contoencoding int4, conproc regproc/oid, condefault
bool. NOTE: PG ships ~130 built-in conversions, but pg_dump filters them
as built-ins (oid below FirstNormalObjectId), so an EMPTY view likely
suffices — but VERIFY empirically with TestPort_PgDumpConnectionSetup;
if pg_dump needs the rows present, may need to populate built-ins.

ORTHOGONAL PRE-EXISTING (track separately, irrelevant to empty pg_dump views):
reading a text[] column back from the heap yields the BINARY array encoding
(KindString w/ raw bytes), not the text repr expandArrayDatum parses.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
