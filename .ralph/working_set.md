(idle — nothing in flight)

M0127-P5.6-e-ii is DONE and committed. Both class-(a) causes 09 §5.2 named
are closed on the LIVE estimator (`internal/planner/cardinality.go` +
`selectivity.go`): the JOIN_SEMI/JOIN_ANTI arms of
`calc_joinrel_size_estimate` with `eqjoinsel_semi`'s no-MCV match fraction,
and `clauseSelectivity` over the ON-clause conjuncts `HashKeys` does not
answer (both-sided ones only — a single-sided conjunct is a
baserestrictinfo already priced into the component rel).

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next open
M0127 item. Two are ready: `M0127-P5.6-e-iii` (the one this loop spun out —
de-saturate ANALYZE's ndistinct with Haas–Stokes, THEN fix the join-key
merged-coordinate lookup and give `columnNDistinctForChild` its `*Join` arm)
and `M0127-P5.7` (nbatch-aware `hashJoinCost`; also unblocks the still-blocked
P5.6-d). e-iii is the higher-value one — it is the prerequisite for three
open ledger rows and for Q9's ≤10² bar.**

Carry-over facts a next loop should not re-derive:

- The e-iii evidence is already measured and committed: the coordinate
  correction alone was run and REJECTED —
  `analysis/leftdeep-joins/2026-08-04-p56eii-postfix.txt`, Q9 final
  124.7× → 176 424× over, Q8 final 1.9× under → 2 171× over, while Q9's two
  deepest joins became EXACT. Two masking defects: ANALYZE stores the
  SAMPLE ndistinct (1.5M-row unique key reads ≈30 000) and the M0126-0010
  `max(|l|,|r|)` cap fires ONLY on the nd-unavailable path, so supplying nd
  removes the bound. Do not re-run that experiment.
- `LeftKey`/`RightKey`/`Predicate`/`HashKeys` are ALL in the MERGED
  left‖right coordinate space; `Join.Output()` is left-only for SEMI/ANTI.
  `columnStatsForChild` now resolves through a `*Join`; its ndistinct twin
  deliberately does NOT (commented at both ends + a tripwire test).
- Audit re-run recipe: `go build -o /tmp/estimate-audit ./cmd/estimate-audit`
  → `GOGC=100 GOMEMLIMIT=12GiB bench/tpch/setup_goopg.sh` →
  `/tmp/estimate-audit --label <date>-<slug> --timeout 150s` →
  `bench/tpch/stop_goopg.sh`. ~13 min. `--serial` + `--warm-stats` are
  load-bearing; `actual rows=` is CUMULATIVE.
- Still open from earlier: P4.1 ledger row #3 (`mergeJoinStream.bufferGroup`
  twin); `pushOneConjunct` not taught the searched tag; `walkPlanExprs`
  misses `Aggregate.Passthrough`/`AggregateCall.Filter`/`WindowFunc`.
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`).

Gates run this loop: UNITS PASS (exit 0, `/tmp/units_p56eii.log`); DS05 PASS
(PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4 — byte-identical
summary to the two prior sweeps); SPOT PASS (Q12=2, Q13=35); the audit run
(5 violations, one fewer than baseline, none new); pgbench SMOKE via the
commit hook. 10 new tests + 2 existing ones re-pinned.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects, all already
filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
