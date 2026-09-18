Task: testport/TestE2E_PGColdStartOnGoopgDataDir — DONE (commit
  69b2f7b7b): was the predicted FAIL-WHEN-FIXED flip; re-pinned
  `wantChunks` + comment + ledger row; entry now [x].

Files: internal/testport/e2e_pg_coldstart_on_goopgdata_test.go
  (wantChunks ~L1360-1380); .ralph/fix_plan.md (~L424 [x]);
  .ralph/deferral_ledger.md (new tail row — NOTE: append-only under
  RALPH_LOOP; flipping an existing row's status trips the commit
  guard, record resolutions as new rows).

Key facts: `c11ff797a` (2026-09-01, NB-17) ported upstream's pglz
  hash-chain match search + good_match/good_drop bounds into
  `internal/access/common/pglz` → goopg's pg_toast_2618 now matches
  upstream's own chunk counts (all 17) + stored bytes (≤18 B, 8/17
  exact). Old pin held pre-NB-17 brute-force output (3-4% smaller);
  identical `got` in every nightly since 20260902 → one stale pin,
  six duplicate AIs. Residual deltas are content-level, ledgered.

Hypothesis/Findings: CONFIRMED not a regression. race/internal/
  executor (next open M-NIGHTLY ~L438) already re-confirmed 09-18
  as the known instrumentscope race — new AI-20260919-000526-001
  presumably same signature (check
  ci/logs/20260919-000526/race/go-test.log first).

Environment: `data.HOLD` stands — all internal/ impl blocked; six
  staged S2b-15 optimizer files preserved. Nightly 20260919-000526
  done (fail, 2 AIs — both filed onto existing entries); scheduler
  still running. ci/logs/* auto-staged by nightly — unstage before
  commits; .claude/settings.json, .ralphrc, analysis/, postgres,
  third-party/ have pre-existing unstaged mods — never commit.

Gates run: `go test -v -run '^TestE2E_PGColdStartOnGoopgDataDir$'
  ./internal/testport/` PASS 3.17s (cgroup-capped); vet clean;
  gofmt clean on edit (pre-existing divergence ~L1077, go1.25
  baseline rule — do not fix); pgbench smoke PASS (hook).
In-flight: none.
Next step: item-8 M-NIGHTLY — verify race/internal/executor AI-001
  signature, then isolation items; impl HOLD-blocked; legal =
  recon/test-only or milestones M0119→M0122→M0134→M0095/M0110.
