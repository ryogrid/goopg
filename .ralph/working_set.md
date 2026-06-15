Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 32 COMPLETE
this loop (commit pending). NOTHING in flight; next loop starts on slice 33
(current_schemas() name[] array-literal rendering).

=== DONE (loop #55) — DU-002 slice 32 ===
Empty `pg_sequence` virtual view (OID 2224, 0 rows) in catalog.go +
`pg_get_sequence_data(regclass)` FROM-clause SRF (last_value int8,
is_called bool). getSequences' `FROM pg_catalog.pg_sequence,
pg_get_sequence_data(seqrelid)` implicit-LATERAL comma join now resolves.
KEY LESSON: the analyzer (internal/analyzer/analyzer.go `tableFuncColumns`)
runs BEFORE the planner and was the actual gate — column resolution
("column last_value does not exist") fired in Analyze(), not planSelect.
A FROM-clause SRF needs FOUR sites: tableFuncColumns (analyzer),
planTableFuncRangeVar dispatch + planXxx (planner), foldconst.go +
unnest.go walk cases, PgXxx node (plan.go) + nodeReferencesOuter case,
executor op + executor.go dispatch.
Sequences ARE supported but skipped from pg_class virtual view (Virtual &&
View==nil, catalog.go ~line 1757) → pg_dump getTables never sees relkind='S'
→ empty pg_sequence is consistent; SRF never invoked over empty LHS.
Files: catalog.go (pg_sequence view), plan.go (PgGetSequenceData node),
planner.go (dispatch + planPgGetSequenceData + nodeReferencesOuter case),
foldconst.go, unnest.go, executor.go (dispatch),
operators_pg_get_sequence_data.go (+_test.go), analyzer.go (tableFuncColumns),
pgdump_connsetup_test.go (header→next blocker), design 0110-0001 (slice 32),
fix_plan loop #55.
Gates: build/gofmt(my files)/vet clean; catalog+analyzer+planner+executor
suites PASS; new SRF tests PASS; TestPort_PgDumpConnectionSetup PASS.
tpch-spotcheck N/A (additive virtual catalog + FROM-SRF parity; no
physical/codec/executor-semantics change).

=== NEXT STEP — DU-002 slice 33 (current_schemas array literal) ===
pg_dump now fails: `pg_dump: error: could not parse result of
current_schemas()`. pg_dump's dumpable-object setup calls
current_schemas(true) and parses the returned name[] with parsePGArray,
which expects the `{a,b}` text array-literal form. goopg likely renders the
binary/KindString array encoding instead (cf. orthogonal note below).
FIRST: grep executor/expr.go for current_schemas; check how the name[] result
Datum is encoded over the wire (text vs binary array). Make it emit `{a,b}`.
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately, may be same root as slice 33):
reading a text[] column back from the heap yields the BINARY array encoding
(KindString raw bytes), not the text repr expandArrayDatum parses.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
