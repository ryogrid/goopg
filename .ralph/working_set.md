Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 37 COMPLETE
this loop (commit pending). NOTHING in flight; next loop starts on slice 38
(pg_proc.prorows column for dumpFunc).

=== DONE (loop #60) — DU-002 slice 37 ===
Added `pg_proc.procost` (planner's estimated per-row execution cost, float4) to
the pg_proc virtual view. pg_dump's dumpFunc projects procost; without the
column `EXECUTE dumpFunc('1654')` aborted "column procost does not exist".
Fix is CATALOG-ONLY (internal/initdb/pg_proc_view.go):
- new column {Name:"procost", Type:float4}, LAST column (index 18, after
  proconfig@17).
- built-in stubs (internal lang): "1".
- user routines: language-derived per PG compute_function_attributes —
  "1" if language internal/c, else "100" (DEFAULT_FUNCTION_COST vs interpreted).
- catalog.Routine has NO explicit cost field → derived from r.Language.
Files: internal/initdb/pg_proc_view.go (column + 2 row sites + doc comment),
internal/initdb/pg_proc_view_test.go (NEW TestPgProcViewProcost guard),
internal/testport/pgdump_connsetup_test.go (header → next blocker prorows),
docs/design/0110-0001-pg-dump-tap-port.md (slice 37 entry + next blocker).
Gates: build clean; gofmt/vet clean; initdb suite PASS (incl TestPgProcView*);
TestPort_PgDumpConnectionSetup PASS (pg_dump advanced past procost to prorows).
tpch-spotcheck N/A (additive catalog column; no physical/codec/executor change).

=== NEXT STEP — DU-002 slice 38 (pg_proc.prorows) ===
pg_dump now fails: `column "prorows" does not exist` (EXECUTE dumpFunc('1654')).
dumpFunc reads pg_proc.prorows (estimated result-row count for set-returning
functions, float4; PG default 0 for non-SRFs, 1000 for SRFs).
FIRST: add prorows column to internal/initdb/pg_proc_view.go. Append after
procost (index 19). Type float4. Emit "0" for built-in stubs (none are SRFs);
for user routines, "1000" if r.ReturnsSet else "0" (mirror PG's prorows
default for SRFs). Update column-index comments + add TestPgProcViewProrows.
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. NOT hit by these slices (proconfig always NULL).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
