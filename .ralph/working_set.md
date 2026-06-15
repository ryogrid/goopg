Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 30 COMPLETE
this loop (commit pending push). NOTHING in flight; next loop starts on
slice 31 (empty pg_seclabels virtual view).

=== DONE (loop #53) — DU-002 slice 30 ===
Empty `pg_amop` (OID 2602) + `pg_amproc` (OID 2603) virtual views in
internal/catalog/catalog.go beside pg_largeobject_metadata. getDependencies'
pg_depend UNION joining both for opfamily member deps now resolves (no user
opclasses → 0 rows each). Schemas:
- pg_amop (pg_amop.h): oid, amopfamily oid, amoplefttype oid, amoprighttype oid,
  amopstrategy int2, amoppurpose "char", amopopr oid, amopmethod oid,
  amopsortfamily oid.
- pg_amproc (pg_amproc.h): oid, amprocfamily oid, amproclefttype oid,
  amprocrighttype oid, amprocnum int2, amproc regproc.
VirtualRows returns nil. NOTE: Type struct has NO IsArray field; use "name[]"
string convention for array types (not needed here — all scalar).
Updated: catalog.go, pgdump_connsetup_test.go (header → next blocker), design
doc 0110-0001 (slice 30 block + slice 31 prediction), fix_plan loop #53.
Gates: build/gofmt/vet clean; catalog suite PASS; TestPort_PgDumpConnectionSetup
PASS. tpch-spotcheck N/A (additive virtual catalog parity only; no physical/
codec/executor change).

=== NEXT STEP — DU-002 slice 31 (pg_seclabels empty view) ===
pg_dump now fails: `relation "pg_seclabels" does not exist`. Query (getSecLabels):
`SELECT label, provider, classoid, objoid, objsubid FROM pg_catalog.pg_seclabels
ORDER BY classoid, objoid, objsubid`.
Add empty virtual view in catalog.go beside pg_amproc:
- pg_seclabels: in stock PG a system view over pg_seclabel + pg_shseclabel.
  Cols the query needs: label text, provider text, classoid oid, objoid oid,
  objsubid int4. Full view schema also: objtype text, objnamespace oid,
  objname text. goopg has no SECURITY LABEL → 0 rows.
  pg_seclabels has NO oid column (it's a view). Pick an unused OID for the
  virtual table registration.
RUN TestPort_PgDumpConnectionSetup (-count=1) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. Irrelevant to empty pg_dump views.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
