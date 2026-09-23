Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Slices 1, 2a, 3, 4 LANDED; slice 5 re-run DONE with root-cause fix 9e368ade8.
Open: 2b; index_update_stats port; owner call on indexProbeCostMultiplier.

Files: internal/catalog/catalog.go (relAllVisibleKey, RelAllVisibleBlocks),
internal/optimizer/pathindexonly.go (relAllVisibleFraction(cat,...)),
internal/executor/vm_test.go (newVMFixture installs RelAllVisibleFunc).
Design doc docs/design/0100-0149/m0145-0029-one-rel-index-path-coverage.md
§Slice 5 holds the per-test disposition table.

Findings (group I under local flip, per-test PG oracle):
- FIXED 6 subtests (TestIOS_Composite*/3Columns, TestArrayIndexOnly…):
  planner read VM under DefaultDBOid, VACUUM/pg_class under session DB 5.
- STALE (goopg already = PG Seq Scan): TestIndexScan{Varchar,Char,Timestamp}
  EndToEnd — edit in the flip commit (still valid on legacy default).
- MULTIPLIER-DEPENDENT (owner): TestIOS_HeapFallback,
  TestIndexOnlyDeformColdAndVisible.
- REAL GAPS: CREATE INDEX never records heap reltuples/relpages (PG
  index_update_stats) → TestIndexDeformRescanPersistsBound; SAOP + trailing
  range on next column → TestSAOPWithConjunctMoves (slice 2b).
- Suspect sibling (unverified): catalog.TableRealPages/IndexRealPages key
  under DefaultDBOid too.
Also still open: filed wrong-results bug (composite prefix probes skip
trailing-NULL rows), in fix_plan next to the command-tag sweep bugs.

Next step: per banner, M0145-0029 — port index_update_stats (heap stats on
CREATE INDEX over a non-empty heap; keep -1 when empty) or slice 2b; the
multiplier residue waits on the owner.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (25 fires,
introduced=none; knob plans byte-identical) — all PASS.
In-flight: none.
