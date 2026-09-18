Task: M0140-0006c-3 — mixed partial/claimed-whole SetOp Append arm (Q5).
  SCOPING RECON DONE this loop (commit 584545492): design doc
  `docs/design/0100-0149/m0140-0006c-3-mixed-partial-setop-append.md` +
  README index + fix_plan progress bullet. Impl remains HOLD-blocked.

Files: doc above; `.ralph/fix_plan.md` (0006c-3 bullet ~L2932);
  `docs/design/README.md` (index row ~L1333). No `internal/` changes.

Key symbols: `addPartialSetOpPath` (windowsetoppaths.go:525 — pure-only
  guard at :550); `parallelClaimSet`/`newLeafParallelClaimSet`/`attachAll`
  (parallel_scan.go:495-605); `*setOp.nextStreaming`
  (operators_setop.go:101); `stampParallelScan`/`drivingScan`/
  `parallelChildren` `*SetOp` arms (parallel.go:625+,:1389).
  PG refs: allpaths.c:1412-1453 (per-child pick), :1594-1627 (mixed arm),
  pathnode.c:1343-1365 (first_partial_path), costsize.c:2168-2243 (LPT),
  :2342-2403 (mixed cost_append), nodeAppend.c (pa_finished claim).

Hypothesis/Findings: mixed arm needs (a) per-branch pick partial-vs-
  cheapest-parallel-safe-total, (b) Path->*SetOp partialness marker,
  (c) claimedWhole atomic.Bool per leaf claim set + CAS-claim in
  nextStreaming, (d) rows override to pure arm's Rows when both fire.
  Only open design Q: prebuildBitmap exclusion for claimed-whole
  subtrees. Q5 needs ONLY this arm (partial side = bare seq scan).

Environment note: `data.HOLD` was RE-IMPOSED 2026-09-18T22:52 (unclean
  :65433 shutdown, stale postmaster.pid pid 1808130 — owner inspect).
  ALL internal/ impl commits blocked again; S2b-15's six staged files
  stay staged (unstage->commit->re-stage dance held).
  Nightly 20260919-000526 testport still running — file its AI next loop.

Gates run: pgbench smoke PASS (commit hook); no unit/spotcheck needed
  (docs-only). ralph-state-guard pending this write.

In-flight: none. Staged S2b-15 set preserved (see above).
Next step: banner order — item-1 P0-H11 owner-gated; items 2-4 done;
  item-5 chain impl all HOLD-blocked. Legal work = recon/docs/test-only.
  Triage nightly action-items when run 20260919-000526 finishes.
