(idle — nothing in flight)

Last landed (loop #1, this block): M0118-0002 enabler 0118-0136 — PG-faithful
`float4out`/`float8out` (`executor.PGFloatOut`). NOT a promotion.

What landed:
- New `executor.PGFloatOut(f, bitSize)`: shortest round-trip decimal (Ryu via
  Go's `'e'` verb) + PG's fixed-vs-scientific display-exponent threshold
  (`[-4,15)` float8, `[-4,6)` float4). `2233750::float8` → `2233750` not
  `2.23375e+06`. Handles Infinity/-Infinity/NaN/-0.
- Wired at ALL FOUR sibling sites: codec.go encodeValuePG (float4/float8),
  dispatch.go appendFloatText/appendFloat8Text, dispatch_extended.go float
  result columns, isolation_runner.go scanResultSet (scan OIDs 700/701 as
  NullFloat64 → PGFloatOut).
Files: internal/executor/codec.go + pgfloatout_test.go (new), internal/server/
dispatch.go + dispatch_extended.go, internal/testport/framework/isolation_runner.go.

KEY FINDING (corrects loop #6's claim): predicate-gist is NOT promotable. The
prior "filtered probe → ZERO SSI divergences" was WRONG. With float output fixed,
a full probe shows the first divergence is a GENUINE SSI OVER-DETECTION:
  perm `rxy3 wx3 rxy4 c1 wy4 c2` — goopg raises 40001 on c2 commit; PG commits.
  rxy3 reads p>>point(6000,6000) (X>6000), rxy4 reads p<<point(1000,1000)
  (X<1000); wx3 inserts high-X, wy4 inserts low-X. PG's GiST PAGE-level
  predicate locks see disjoint spatial regions (no dangerous cycle); goopg's
  coarse relation/tuple-grain SIREAD locks the whole relation → false write-skew
  → spurious 40001.

  → NEXT for predicate-gist (Effort-L): GiST spatial page-grain / bounding-box /
    grid-cell predicate locking — the granularity class predicate-hash solved
    with bucket-grain SIREAD (FNV→PageLockTag, design 0118-0099 /
    goopg_hash_index_ssi_bucket_locking). Reuse that pattern for 2D point space.

Other M0118 failed specs: predicate-gin (int4[]-column array typing + GIN AM),
deadlock-parallel (parallel-worker lock groups — no parallel query in goopg).

Gates this loop: TestPGFloatOut PASS; executor+server full pkg tests PASS;
TestPort_PointGeometricRead PASS; full TestPort_RegressSuite TIMED OUT at 30m
(wall-clock only, ZERO subtest failures — infra slowness on WSL2, worsened by
orphaned clusters since cleaned); TPC-H spotcheck SKIPPED (known big-dataset
startup hang, infra not regression — tpch_spotcheck_slru_backfill_startup_hang).
pgbench smoke = pre-commit hook. No-regression argument: PGFloatOut is strictly
MORE PG-faithful, so any previously-matching regress case still matches.
