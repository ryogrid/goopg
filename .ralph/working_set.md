Task: M0142-0008-producer — teach IN-unnesting paths to set Join.SJInfo [DONE, committed]
Files: internal/optimizer/unnest.go (inUnnestSJInfo + 2 call sites),
  internal/optimizer/in_unnest_sjinfo_test.go (new, 5 tests),
  docs/design/0100-0149/m0142-0008-producer-in-unnest-sjinfo.md,
  bench/tpch/baseline-digests.txt (re-captured post-reload)
Key symbols: inUnnestSJInfo, unnestInExpr, unnestNonCorrelatedInExpr,
  existsUnnestSJInfo (template), semiAntiLinksHaveSJInfos (gate at
  joinsearchseam.go:628), extractSearchLeaves semiAnti arm (:1311+)
Hypothesis/Findings:
  - Producer landed and unit-verified (all 4 SJInfo shapes + a real
    extractSearchLeaves pass-the-gate proof).
  - Corpus reachability STILL ZERO by measurement: every IN statement
    declines at `leaf-count` (joinsearchseam.go:325) BEFORE the sjinfo
    gate — Q56 probe: nrels=4 nleaves=2, no link ever extracted.
    Suspected cause: `filter.Child = join` leaves a Filter wrapper above
    the pinned join; the walk treats non-Join nodes as opaque leaves.
    The chain's real unblock is leaf-admission/chain-shape work
    (a-3 increment (ii)); design doc §6 has the resume notes.
  - bench/tpch/baseline-digests.txt was re-captured (was stale vs the
    8-FK reload: 9 ROWS-DIFF + 14 VALUE-DIFF + Q6 colsig drift). The
    acceptance arm is usable again for future executor commits.
  - Untracked stray: internal/optimizer/probe_subq_test.go exists in the
    worktree (not mine; left alone — check before next optimizer work).
Next step: re-read the banner; next M0142-0008 chain items in order are
  0008a-3's increment (ii) / 0008c-1a / 0008c-3d / 0008c-4 — the
  leaf-count decline is the new measured blocker for IN-link reachability.
Gates run: units PASS; tpch-spotcheck PASS (Q12=2/Q13=33);
  tpcds-sf025 PASS 96/0/0/0 plans same=99; tpch-acceptance-arm PASS
  24/24 MATCH (PGSHAPED=1, PER_Q=900, seed 20260905, fresh baseline);
  hook pgbench smoke PASS x2. Commits: 1f76d83d9 (code), f8b80ec80
  (baseline), docs commit follows.
In-flight: none.
