Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 36 COMPLETE
this loop (commit pending). NOTHING in flight; next loop starts on slice 37
(pg_proc.procost column for dumpFunc).

=== DONE (loop #59) — DU-002 slice 36 ===
Added `pg_proc.proconfig` (per-function GUC SET clauses, text[]) to the
pg_proc virtual view. pg_dump's dumpFunc projects proconfig; without the
column `EXECUTE dumpFunc('1654')` aborted "column proconfig does not exist".
Fix is CATALOG-ONLY (internal/initdb/pg_proc_view.go):
- new column {Name:"proconfig", Type:text[]}, LAST column (index 17, after
  probin@16).
- always NULL "" for BOTH built-in stubs AND user routines — goopg tracks no
  per-function SET clauses.
Files: internal/initdb/pg_proc_view.go (column + 2 row sites + doc comment),
internal/initdb/pg_proc_view_test.go (NEW TestPgProcViewProconfig guard),
internal/testport/pgdump_connsetup_test.go (header → next blocker procost),
docs/design/0110-0001-pg-dump-tap-port.md (slice 36 entry + next blocker).
Gates: build clean; gofmt/vet clean; initdb suite PASS (incl TestPgProcView*);
TestPort_PgDumpConnectionSetup PASS (pg_dump advanced past proconfig to procost).
tpch-spotcheck N/A (additive catalog column; no physical/codec/executor change).

=== NEXT STEP — DU-002 slice 37 (pg_proc.procost) ===
pg_dump now fails: `column "procost" does not exist` (EXECUTE dumpFunc('1654')).
dumpFunc reads pg_proc.procost (planner's estimated per-row execution cost,
float4; PG default 1 for C/internal-language functions, 100 for others).
goopg's pg_proc virtual view does not expose it.
FIRST: add procost column to internal/initdb/pg_proc_view.go. Append after
proconfig (index 18). Type float4. Emit "1" for built-in stubs; for user
routines, "1" if internal/c language else "100" (mirror PG's
DEFAULT_FUNCTION_COST / clang). Verify catalog.Routine has no explicit cost
field (likely none → use language-based default). Update column-index comments.
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. NOT hit by these slices (proconfig is always NULL).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
