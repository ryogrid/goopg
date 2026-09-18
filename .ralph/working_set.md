Task: infra/nightly-live-tree-build-race — DONE + committed (021dd66dd).
  Carried-over blocker: M0141-S2b-15 impl remains [!] gated on
  bench/tpch/runtime_goopg/data.HOLD (owner-only recovery).

Files: ci/batch/run-nightly.sh (postgres symlink into worktree),
  stages/stage-{units,race,testport}.sh (cd NIGHTLY_SRC_ROOT),
  lib/common.sh + ci/design/04 (fp comments), .ralph/fix_plan.md — committed.

Key symbols: NIGHTLY_SRC_ROOT, source_fingerprint, clientToolBin/repoRoot
  (testport fixture resolution via postgres/ symlink).

Hypothesis/Findings:
  - Phantom "build broke mid-stage" class closed: builds were already in
    the worktree; go-test stages were the leftover leak. Now ALL Go
    compilation runs on the detached HEAD worktree; untracked scratch
    (bak/, tmp/) can never enter `go list`/`go test`.
  - commit-msg hook enforces TPC-H gates against the INDEX, not the
    pathspec: with internal/ staged it demands fresh gate stamps. Used:
    unstage internal/ → commit scripts/docs → re-stage. S2b-15's six
    optimizer files are STAGED again (same blob hashes).
  - Nightly 20260919-000526 was RUNNING during the loop; edits applied via
    temp-file + os.replace (running bash keeps old inode). Its race FAIL
    is the KNOWN instrumentscope race (open task), not contamination.
  - source_fingerprint is now drift EVIDENCE only; comments updated.

Next step: S2b-15 blocked until owner lifts data.HOLD
  (scripts/tpch-ref-recover.sh --i-am-owner) or adds a gate-exceptions
  row; then re-run tpch-spotcheck + tpch-acceptance-arm on the staged
  tree, triage the 10 ea NEW findings (pg_est=None churn), repin, commit
  internal/. Otherwise next banner item is M0140-0006c-2 partial Append.

Gates run: bash -n ×5; worktree `go build ./...` clean; stage-units.sh
  PASS in worktree; TestPort_PgControldata001 PASS in worktree;
  race-gate dry-run clean; pgbench smoke PASS; ralph-state-guard PASS.

In-flight: nightly 20260919-000526 still running (stage-testport, PID
  2870459, 120m cap from 00:05); remaining stages run new stage scripts
  harmlessly (units/race already done); run-nightly keeps old inode.
