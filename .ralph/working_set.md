(idle — nothing in flight)

M0127-P5.6-e-i is DONE and committed: the estimate-audit instrument
(`internal/estimateaudit/audit.go` + `cmd/estimate-audit/main.go`, 12 tests)
and the pre-flip baseline under `analysis/leftdeep-joins/` (see the
`2026-08-04-p56e-README.md` there for run conditions).

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects `M0127-P5.6-e-ii`
— close the two class-(a) causes the baseline isolated (09 §5.2): (1) a
SEMI/ANTI joinrel priced at its outer input VERBATIM (no match fraction —
`calc_joinrel_size_estimate`'s JOIN_SEMI arm, costsize.c; Q18 2.5e7× over,
Q20, Q22), and (2) a joinrel's non-equi restriction contributing ZERO
selectivity (Q19's three-branch OR: est 4.3e7 vs 131 actual; Q3's re-applied
`Filter:`). Bar: UNITS + DS05 + a re-run audit. NOTE this one edits the LIVE
estimator, so DS05 (~1 h) is mandatory, unlike P5.6-a…-e-i. Alternative if
that is judged too wide: `M0127-P5.6-d` is still BLOCKED on P5.7's batch-I/O
term, so P5.7 is the other legitimate pick.**

Carry-over facts a next loop should not re-derive:

- Re-run the audit with: `go build -o /tmp/estimate-audit ./cmd/estimate-audit`
  → `GOGC=100 GOMEMLIMIT=12GiB bench/tpch/setup_goopg.sh` →
  `/tmp/estimate-audit --label <date>-<slug> --timeout 150s` →
  `bench/tpch/stop_goopg.sh`. All 22 queries, 12 min 42 s, nothing timed out.
- `--serial` is NOT optional: goopg loses all instrumentation below a
  `Gather`, and Q9 plans entirely below one. `--warm-stats` is NOT optional:
  ANALYZE stats are per-connection.
- goopg's `actual rows=` is CUMULATIVE across loops; PG prints the per-loop
  average. Do not multiply by `loops`. Ledgered.
- Baseline numbers to beat: Q9 final joinrel 124.7× over (39 447 200 vs
  316 264) — §5's bar is ≤100× and is P5.9's to certify, not P5.6-e's.
- Still open from earlier: P4.1 ledger row #3 (`mergeJoinStream.bufferGroup`
  twin); `pushOneConjunct` not taught the searched tag; `walkPlanExprs` misses
  `Aggregate.Passthrough`/`AggregateCall.Filter`/`WindowFunc`.
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`); `cd`
  persists across Bash calls — use absolute paths.

Gates run this loop: build+vet clean; gofmt clean on both new files; 12 new
tests PASS; UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p56e.log`); the audit run
itself (22/22 measured); pgbench SMOKE via the commit hook. DS05 + SPOT + PLAN
not applicable — new tool + new package, zero planner/executor lines changed.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects, all already
filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
