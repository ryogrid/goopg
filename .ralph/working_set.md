(idle — nothing in flight)

M0127-P5.6-e-iii is DONE and committed. All three parts landed and were
measured together: Haas–Stokes in ANALYZE (`executor.ndistinctEstimate`),
the merged-coordinate right-key lookup + `columnNDistinctForChild`'s `*Join`
arm, and the M0126-0010 cap re-examined (left fallback-only, deliberately).
Estimate-audit violations **5 → 2**.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next open
M0127 item. Three are ready, and `M0127-P5.6-f` is the highest-value one —
it OWNS the Q9 regression this loop attributed and is a hard blocker on
P5.9's Q9 ≤10² certification. The others: `M0127-P5.6-g` (eqjoinsel_semi's
MCV arm; owns the SEMI/ANTI `est=1` collapse, incl. Q21's new audit
violation) and `M0127-P5.7` (nbatch-aware `hashJoinCost`, also unblocks the
still-blocked P5.6-d).**

Carry-over facts a next loop should not re-derive:

- **Q9 is UNMEASURED** in the new audit (93.9 s → >150 s). Attribution was
  taken per 09 §6 BEFORE landing: reverting ONLY the planner half reproduces
  the identical plan shape (481 222 948 vs 479 779 280), so the de-saturated
  ANALYZE is the whole cause. Mechanism: `l_suppkey = ps_suppkey AND
  l_partkey = ps_partkey` is a TWO-pair equi-join priced on ONE pair while
  `Join.Residual()` excludes BOTH → 481 M, so the DP puts it UNDER the
  `part` filter. Folding `max(nd)` over all pairs alone gives ≈2 rows —
  P5.6-f must land the `get_foreign_key_join_selectivity` analogue in the
  SAME change. Do not re-derive this; it is in 09 §5.4 + two ledger rows +
  `2026-08-04-p56eiii-README.md`.
- Q9's two deepest joins are now EXACT (5 997 241 both) — the ndistinct is
  right; the error is entirely the multi-key pricing above it.
- `NDistinct` and `NDistinctFrac` are now two renderings of ONE estimate;
  `StaDistinct()` picks with upstream's 10%-of-rows rule. `parallel_agg.go`
  still needs frac populated for every column — do not switch it to PG's
  "absolute below 10%, fraction above" storage or the split gate refuses.
- Still open from earlier: P4.1 ledger row #3 (`mergeJoinStream.bufferGroup`
  twin); `pushOneConjunct` not taught the searched tag; `walkPlanExprs`
  misses `Aggregate.Passthrough`/`AggregateCall.Filter`/`WindowFunc`.
- Audit re-run recipe: `go build -o /tmp/estimate-audit ./cmd/estimate-audit`
  → `GOGC=100 GOMEMLIMIT=12GiB bench/tpch/setup_goopg.sh` →
  `/tmp/estimate-audit --label <date>-<slug> --timeout 150s` →
  `bench/tpch/stop_goopg.sh`. ~13 min. DB/user are `tpch`/`tpch` (NOT
  postgres); `actual rows=` is CUMULATIVE.
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`).

Gates run this loop: UNITS PASS (exit 0, `/tmp/units_p56eiii.log`); SPOT PASS
(Q12=2, Q13=35); DS05 PASS (PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=1 SKIP=4 — identical summary to the three prior sweeps); the audit
run (2 violations, three fewer than baseline, one new: Q21). pgbench SMOKE
via the commit hook.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects, all
already filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
