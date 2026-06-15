Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 31 COMPLETE
this loop (commit pending). NOTHING in flight; next loop starts on slice 32
(pg_sequence catalog + pg_get_sequence_data SRF).

=== DONE (loop #54) — DU-002 slice 31 ===
Empty `pg_seclabels` virtual view (unused OID 3597) in
internal/catalog/catalog.go beside pg_amproc. getSecLabels' query
`SELECT label, provider, classoid, objoid, objsubid FROM pg_catalog.pg_seclabels
ORDER BY classoid, objoid, objsubid` now resolves (no SECURITY LABEL → 0 rows).
pg_seclabels is a VIEW (no oid col). Cols: objoid oid, classoid oid, objsubid
int4, objtype text, objnamespace oid, objname text, provider text, label text.
VirtualRows returns nil.
Updated: catalog.go, pgdump_connsetup_test.go (header → next blocker), design
doc 0110-0001 (slice 31 block + slice 32 prediction), fix_plan loop #54.
Gates: build/gofmt/vet clean; catalog suite PASS; TestPort_PgDumpConnectionSetup
PASS. tpch-spotcheck N/A (additive virtual catalog parity only; no physical/
codec/executor change).

=== NEXT STEP — DU-002 slice 32 (pg_sequence + pg_get_sequence_data) ===
pg_dump now fails: `relation "pg_sequence" does not exist`. Query (getSequences):
`SELECT seqrelid, format_type(seqtypid, NULL), seqstart, seqincrement, seqmax,
seqmin, seqcache, seqcycle, last_value, is_called FROM pg_catalog.pg_sequence,
pg_get_sequence_data(seqrelid) ORDER BY seqrelid`.
HARDER than prior slices — TWO things must resolve:
  1. pg_sequence: a REAL catalog (one row per sequence relation), NOT a view.
     Cols (pg_sequence.h): seqrelid oid, seqtypid oid, seqstart int8,
     seqincrement int8, seqmax int8, seqmin int8, seqcache int8, seqcycle bool.
  2. pg_get_sequence_data(seqrelid): a set-returning FUNCTION (last_value int8,
     is_called bool) — NOT a view. The comma-join is an implicit LATERAL.
FIRST verify whether goopg supports CREATE SEQUENCE (grep parser/executor for
SEQUENCE). If NO sequences exist, an empty pg_sequence view (0 rows) suffices
AND the function over an empty set is never called — but the function must still
be REGISTERED or the query parse/plan may fail. Check how other SRFs are
registered (e.g. pg_get_keywords, generate_series) before adding.
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. Irrelevant to empty pg_dump views.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
