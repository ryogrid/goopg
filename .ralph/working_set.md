Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Slices 1, 2a, 3, 4 LANDED; slice 5 re-run done (9e368ade8); index_update_stats
heap half ported (292b1af2e). Open: slice 2b; owner call on the multiplier.

Files (this loop): internal/executor/operators_ddl.go (ddlOp.buildHeapTuples,
indexUpdateHeapStats, autoVacuumingActive), index_update_stats_test.go
(TestCreateIndexUpdatesHeapStats, withAutovacuumOff helper), and six tiny-table
executor tests now call withAutovacuumOff. Design doc
docs/design/0100-0149/m0145-0029-one-rel-index-path-coverage.md (§Follow-up).

Findings: PG 18.3 matches exactly (loaded 3/1, empty PK -1, autovacuum_enabled
false -1). w witness now Seq Scan at PG's 1.05. The six tests pinned to
autovacuum=off must be re-checked at the flip against PG's Bitmap Heap Scan
(size unmeasured), not Seq Scan.

Group I status for the flip:
- fixed 6 subtests (IOS allvisfrac), w witness now PG-faithful;
- multiplier-dependent (OWNER): TestIOS_HeapFallback,
  TestIndexOnlyDeformColdAndVisible;
- capability gap: TestSAOPWithConjunctMoves → slice 2b (SAOP + trailing range
  on the next index column; needs an executor probe shape).
Also open: filed wrong-results bug (composite prefix probes skip trailing-NULL
rows) in fix_plan beside the command-tag sweep bugs.

Next step: per banner, M0145-0029 slice 2b — executor probe shape of
equality/SAOP prefix + trailing range bound, then extend the restriction
producer; or, if the owner has answered the multiplier question, apply it.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (25 fires, knob plans
byte-identical), TestPort_RegressSuite — all PASS.
In-flight: none.
