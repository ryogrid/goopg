Task: M0140-0006c-2 recon — remaining partial-SetOp branches scoped +
  committed (8ab5816fd); filed M0140-0006c-3 (mixed pa_subpaths arm, Q5).
  Carried-over blocker: M0141-S2b-15 impl remains [!] gated on
  bench/tpch/runtime_goopg/data.HOLD (owner-only recovery).

Files: docs/design/0100-0149/m0140-0006c-2-join-branch-partial-setop-
  admission.md (new "Remaining-branch scoping" section), .ralph/fix_plan.md
  (recon bullet on 0006c-2 + new 0006c-3 entry at ~line 2914) — committed.

Key symbols: setOpBranchDrivingKindIsSupported (gatherpaths.go:594),
  partialPathDrivingKind (:425 — top-level arms for all three kinds exist),
  parallelClaimSet.setOpLeft/Right + newLeafParallelClaimSet +
  attachAll/prebuildBitmap (parallel_scan.go:495-656),
  collectShareableJoins (parallel_hash_build.go:273), collectBitmapScans
  (operators_gather.go:150), parallelChildren (parallel.go:1352).

Hypothesis/Findings: only the NL branch has a corpus witness (Q76, which
  also needs M0142-0005a's Memoize admission — both must land to flip it);
  merge/bitmap branches have ZERO Parallel Append heads in plans-pg.
  Q5 needs the mixed arm (0006c-3); Q2/Q14/Q71 ride landed arms; Q75 has
  no Parallel Append. Executor attach walks already cover merge/NL — the
  only missing machinery is the 3 admission arms + prebuildBitmap's
  per-branch pbm publication. Slice order: NL -> merge -> bitmap.

Next step: per banner, 0006c-2 impl slices stay HOLD-blocked; next
  selectable is the same banner walk (M0142 chain / M0143 / M-NIGHTLY —
  all impl, all HOLD-blocked) or owner recovery.

Gates run: pgbench smoke PASS (pre-commit hook), lineage guard PASS.

In-flight: nightly 20260919-000526 may still be running stage-testport
  (started 00:05); race stage already FAIL = known instrumentscope race
  (M-NIGHTLY-instrumentscope-race-fix). When it finishes, file new
  action-items per the mandatory triage.
