Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 39 COMPLETE
(not yet committed at time of writing; commit pending). NOTHING in flight
after commit; next loop starts on slice 40 (pg_proc.proparallel for dumpFunc).

=== DONE (loop #62) — DU-002 slice 39 ===
Added `pg_proc.protrftypes` (OID array of argument types whose transforms the
function uses, oidvector) to the pg_proc virtual view. pg_dump's dumpFunc
projects protrftypes; without the column `EXECUTE dumpFunc('1654')` aborted
"column protrftypes does not exist". CATALOG-ONLY
(internal/initdb/pg_proc_view.go):
- new column {Name:"protrftypes", Type:oidvector}, LAST column (index 20,
  after prorows@19).
- ALL rows (built-in stubs + user routines): "" (NULL) — goopg supports no
  transforms, so dumpFunc emits no `TRANSFORM FOR TYPE ...` clause.
Files: internal/initdb/pg_proc_view.go (column + 2 row sites + doc comment),
internal/initdb/pg_proc_view_test.go (NEW TestPgProcViewProtrftypes guard),
internal/testport/pgdump_connsetup_test.go (header → next blocker proparallel),
docs/design/0110-0001-pg-dump-tap-port.md (slice 39 entry + next blocker).
Gates: build clean; gofmt/vet clean; initdb TestPgProcView* PASS;
TestPort_PgDumpConnectionSetup PASS (advanced past protrftypes to proparallel).
tpch-spotcheck N/A (additive catalog column; no physical/codec/executor change).

=== NEXT STEP — DU-002 slice 40 (pg_proc.proparallel) ===
pg_dump now fails: `column "proparallel" does not exist` (EXECUTE dumpFunc('1654')).
dumpFunc reads pg_proc.proparallel (parallel-safety marker, char: 's' safe /
'r' restricted / 'u' unsafe; PG CREATE FUNCTION default is 'u' unsafe — the
case for EVERY goopg routine).
FIRST: add proparallel column to internal/initdb/pg_proc_view.go. Append after
protrftypes (index 21). Type "char" (single-char). Emit "u" (unsafe) for BOTH
built-in stubs and user routines. Update column-index comments + add
TestPgProcViewProparallel (assert "u" for all rows, like the proretset guard).
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. NOT hit by these slices (proconfig always NULL).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
