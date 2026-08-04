Task: M0127-P5.6-g-iii — the acceptance instrument, not the estimator. DONE,
documented, committed. Nothing in flight.

Files: internal/estimateaudit/parity.go (NEW), audit.go (`FinalBar`,
`Node.Rels`/`RelKey`, upstream join labels), parity_test.go (NEW, 11 tests),
cmd/estimate-audit/main.go (`--from-plans`/`--reference`/`--ref-port`).

Four things the next loop should NOT re-derive:

1. **Q19 is the only estimator defect TPC-H can prove** — goopg est 1 vs
   actual 131, PG 116 vs 112 (126.5× excess). Filed as M0127-P5.6-g-iv. The
   absolute tripwire never saw it (131× < 1 000×).
2. **Q21 is measured PG parity**, excess 1.0× (goopg 4 003× / PG 4 178×), and
   Q18 is a shape mismatch, not a parity row, because of #3.
3. **goopg's EXPLAIN prints no `lineitem_1`/`n1`/`n2` disambiguation** (PG's
   `select_rtable_names`, ruleutils.c), so two RTEs of one relation are
   indistinguishable in the text. Costs Q8/Q17/Q18 their final-joinrel
   comparison; `shape_mismatches=67` is therefore an UPPER bound. Ledgered
   2026-08-05; fix belongs in internal/executor/operators_explain.go.
4. **PG's TPC-H load ≠ goopg's** (lineitem 5 998 835 vs 5 997 241, two
   independent HammerDB runs), so actuals differ ~0.03 %. Compare FACTORS, not
   row counts.

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giii-parity.txt`,
`.pg.plans.txt`, `-README.md`. Docs: 09 §4.1 (the restated ratchet) + §5.8.

Next step: per the banner, the next M0127 item. **M0127-P5.6-g-i (the carried
DS05 sweep) still goes first** — still blocked this loop: the nightly CI batch
was mid-run (`ci/batch/run-nightly.sh`, testport stage, ~2 h elapsed) and
`scripts/tpcds-sf05-regression.sh` self-refuses while it holds the host. Then
P5.6-g-ii (HashAggregate arm + Q18 dedup shape) or the new P5.6-g-iv (Q19).

Gates run: build + vet + gofmt-clean; estimateaudit `go test` PASS (11 new,
verbose-verified after the last edit); UNITS PASS (`/tmp/units_p56giii.log`);
audit run WITH the parity column (the item's stated bar) — absolute violations
2 → 1, `parity_violations=1 shape_mismatches=67`; pgbench smoke via the commit
hook. No SPOT/DS05: no planner or executor code changed (goopg plans were
replayed from the committed P5.6-g capture).

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
under M-NIGHTLY. Nothing new to file.

In-flight: none.
