Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 29 COMPLETE
this loop (commit pending push). NOTHING in flight; next loop starts on
slice 30 (empty pg_amop + pg_amproc virtual views).

=== DONE (loop #52) — DU-002 slice 29 ===
Empty `pg_largeobject_metadata` virtual view (OID 2995) in
internal/catalog/catalog.go beside pg_rewrite. getBlobs probe
`SELECT oid, lomowner, lomacl, acldefault('L', lomowner) AS acldefault FROM
pg_largeobject_metadata ORDER BY lomowner, lomacl::pg_catalog.text, oid` now
resolves (no large objects → 0 rows; acldefault projection never evaluates over
empty set). Schema per pg_largeobject_metadata.h: oid, lomowner oid, lomacl
aclitem[] (Type{Name:"aclitem[]"} — NOTE: Type struct has NO IsArray field; use
the "name[]" string convention). VirtualRows returns nil.
Updated: catalog.go, pgdump_connsetup_test.go (header → next blocker), design
doc 0110-0001 (slice 29 block), fix_plan loop #52.
Gates: build/gofmt/vet clean; catalog suite PASS; TestPort_PgDumpConnectionSetup
PASS. tpch-spotcheck N/A (additive virtual catalog parity only; no physical/
codec/executor change).

=== NEXT STEP — DU-002 slice 30 (pg_amop + pg_amproc empty views) ===
pg_dump now fails: `relation "pg_amproc" does not exist`. Query (getDependencies)
is a pg_depend UNION that joins pg_amop AND pg_amproc for opfamily member deps.
Add BOTH empty virtual views in catalog.go beside pg_largeobject_metadata:
- pg_amop (OID 2602, pg_amop.h): oid, amopfamily oid, amoplefttype oid,
  amoprighttype oid, amopstrategy int2, amoppurpose "char", amopopr oid,
  amopmethod oid, amopsortfamily oid.
- pg_amproc (OID 2603, pg_amproc.h): oid, amprocfamily oid, amproclefttype oid,
  amprocrighttype oid, amprocnum int2, amproc regproc.
goopg has no user-defined opclasses feeding this dump path → 0 rows each.
RUN TestPort_PgDumpConnectionSetup (-count=1) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. Irrelevant to empty pg_dump views.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
