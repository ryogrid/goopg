Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 34 COMPLETE
this loop (commit pending). NOTHING in flight; next loop starts on slice 35
(pg_proc.probin column for getFuncs/dumpFunc).

=== DONE (loop #57) — DU-002 slice 34 ===
Added `pg_proc.proretset` (returns-set boolean flag) to the pg_proc virtual
view. pg_dump's dumpFunc projects proretset to decide whether to emit
`RETURNS SETOF`; without the column `EXECUTE dumpFunc('1654')` aborted
"column proretset does not exist".
Fix is CATALOG-ONLY (internal/initdb/pg_proc_view.go):
- new column {Name:"proretset", Type:bool}.
- built-in stubs (abs variants, RI_FKey_* triggers) are not SRFs → 'f'.
- user routines render 't'/'f' from existing catalog.Routine.ReturnsSet
  (RETURNS SETOF, M0097-0020). proretset is the LAST column (index 15).
Files: internal/initdb/pg_proc_view.go (column + 2 row sites),
internal/initdb/pg_proc_view_test.go (NEW TestPgProcViewProretset guard),
internal/testport/pgdump_connsetup_test.go (header → next blocker),
docs/design/0110-0001-pg-dump-tap-port.md (slice 34 entry + next blocker).
Gates: build clean; gofmt/vet clean (my files); TestPgProcView* PASS;
TestPort_PgDumpConnectionSetup PASS (pg_dump advanced past proretset).
tpch-spotcheck N/A (additive catalog column; no physical/codec/executor-
semantics change).

=== NEXT STEP — DU-002 slice 35 (pg_proc.probin) ===
pg_dump now fails: `column "probin" does not exist` (EXECUTE dumpFunc('1654')).
dumpFunc reads pg_proc.probin (the on-disk binary path for C-language
functions; NULL for every internal/SQL routine goopg has). goopg's pg_proc
virtual view does not expose it.
FIRST: add probin column to internal/initdb/pg_proc_view.go (always NULL ""
for both built-in stubs and user routines — goopg has no C functions). Append
after proretset (index 16). Update pg_proc_view_test.go column-index comments.
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. NOT hit by these slices.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
