Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 44 COMPLETE
and (to be) pushed. NOTHING in flight; next loop starts on slice 45 (populate
pg_attribute columns in pg_dump's getTableAttrs path).

=== DONE (loop #67) — DU-002 slice 44 ===
Added the `pg_get_function_sqlbody(oid)` executor dispatch case.
Root cause: seed pg_proc already registered it (OID 6197,
internal/initdb/pg_proc_seed_data.go) but the big func switch in
internal/executor/expr.go had NO case → dumpFunc's EXECUTE raised 42883.
Fix: added a case (right before pg_get_function_result) returning NullDatum
unconditionally — sqlbody only deparses LANGUAGE sql ... BEGIN ATOMIC bodies
(PG14+), which goopg never parses, so NULL for every routine is correct and
matches what pg_dump expects for quoted-body SQL functions.
**pg_dump now runs to completion (exit 0)** end-to-end.
Files: internal/executor/expr.go (new case),
internal/executor/pg_get_function_sqlbody_test.go (NEW: TestPgGetFunctionSqlbody
+ ...UnknownOID), internal/testport/pgdump_connsetup_test.go (header slice 44 +
promoted exit-0 branch asserts CREATE TABLE/ALTER OWNER/COPY + logs slice-45
target), docs/design/0110-0001-pg-dump-tap-port.md (slice 44 entry + guard note).
Gates: gofmt/build clean; executor pkg PASS (1.4s);
TestPort_PgDumpConnectionSetup PASS (exit 0, asserts archive entry).
tpch-spotcheck N/A (catalog builtin addition; no executor row-path/codec change).

=== NEXT STEP — DU-002 slice 45 (pg_attribute columns in dump) ===
pg_dump exits 0 but the dump is CONTENT-incomplete: emitted
`CREATE TABLE public.foo (\n)` has an EMPTY column list (no `id integer,
name text`) plus a malformed `WITH (""='')` reloptions clause. Two gaps:
 1. getTableAttrs' per-table pg_attribute query (attname/atttypid/attnotnull/
    atttypmod/attstattarget/...) returns NO rows for user tables → no columns.
 2. reloptions ARRAY subquery yields one empty element → `WITH (""='')`.
Find getTableAttrs' SQL in postgres/src/bin/pg_dump/pg_dump.c (pg_search_symbols
'getTableAttrs') and run it against goopg to see which column/predicate yields
0 rows. Likely pg_attribute virtual view lacks user-table rows, or a join/WHERE
(attnum>0, NOT attisdropped, atttypid resolution) filters them out. Then RUN
`go test -count=1 -v -run TestPort_PgDumpConnectionSetup ./internal/testport/`
— once columns appear, tighten the test to assert `id integer`/`name text`.

ORTHOGONAL PRE-EXISTING (track separately): plpgsql user functions can't be
dumped (plpgsql not in pg_language → prolang=0 → dumpFunc join still 0 rows).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
