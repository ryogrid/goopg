Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 35 COMPLETE
this loop (commit pending). NOTHING in flight; next loop starts on slice 36
(pg_proc.proconfig column for dumpFunc).

=== DONE (loop #58) — DU-002 slice 35 ===
Added `pg_proc.probin` (on-disk binary path for C-language functions) to the
pg_proc virtual view. pg_dump's dumpFunc projects probin alongside prosrc;
without the column `EXECUTE dumpFunc('1654')` aborted "column probin does not
exist".
Fix is CATALOG-ONLY (internal/initdb/pg_proc_view.go):
- new column {Name:"probin", Type:text}, LAST column (index 16, after
  proretset@15).
- always NULL "" for BOTH built-in stubs AND user routines — goopg has no
  C-language functions with an on-disk binary path.
Files: internal/initdb/pg_proc_view.go (column + 2 row sites + doc comment),
internal/initdb/pg_proc_view_test.go (NEW TestPgProcViewProbin guard),
internal/testport/pgdump_connsetup_test.go (header → next blocker proconfig),
docs/design/0110-0001-pg-dump-tap-port.md (slice 35 entry + next blocker).
Gates: build clean; gofmt/vet clean; TestPgProcView* PASS;
TestPort_PgDumpConnectionSetup PASS (pg_dump advanced past probin to proconfig).
tpch-spotcheck N/A (additive catalog column; no physical/codec/executor change).

=== NEXT STEP — DU-002 slice 36 (pg_proc.proconfig) ===
pg_dump now fails: `column "proconfig" does not exist` (EXECUTE dumpFunc('1654')).
dumpFunc reads pg_proc.proconfig (per-function GUC SET clauses, text[]; NULL for
every goopg routine — goopg tracks no per-function SET). goopg's pg_proc virtual
view does not expose it.
FIRST: add proconfig column to internal/initdb/pg_proc_view.go (always NULL ""
for both built-in stubs and user routines). Append after probin (index 17).
Update pg_proc_view_test.go column-index comments. Type text[] (or text — NULL
either way; PG declares it text[], mirror that for honesty).
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. NOT hit by these slices.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
