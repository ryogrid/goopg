(idle — nothing in flight)

Last loop: **M0127-P5.9-j CLOSED** — Q47's 40× was ONE cost term charged on the
wrong tuple count. Do NOT re-derive:

1. **The estimate is not the bug.** `{v1,v1_lag}` sizes to 1 row (four
   `DEFAULT_EQ_SEL`s over stats-less CTE columns: 7193² × 0.005⁴ → clamp 1).
   **PG estimates `rows=1` too** — verified on the 65438 oracle, db `tpcds05`,
   same data. Do not "fix" it; both engines are ~7193× under actuals here and
   beating PG would be a divergence. Ledgered to §4.1's ratchet.
2. PG escapes via a mechanism goopg lacks: its CTE scan publishes the
   WindowAgg's ordering as pathkeys ⇒ merge at 375.55 with **no Sort**.
   goopg sorts twice (1393.36), so hash (968.55) vs plain NL (**968.53**)
   decided it at a margin of **0.02**. CTE pathkey propagation = P5.4c-ii,
   ledgered.
3. Root cause: `final_cost_nestloop` charges `cpu_per_tuple` on
   `ntuples = outer_path_rows * inner_path_rows` — PG comments it in place,
   "number of tuples processed (not number emitted!)". goopg splits that sum
   (qual half = caller's `qualEvalCost`, already on the cross product) and the
   `cpu_tuple_cost` half landed on OUTPUT rows — smallest exactly on the plans
   the term deters. Fix in `nestloopCost` + `innerRows` threaded to 3 sites.
   Hash/merge siblings UNTOUCHED (PG charges those on `hashjointuples` /
   `mergejointuples`, which really are output counts).
4. **Reduction technique that cracked it**: the threshold is on ARITY, not
   columns — 3 join keys hash, 4 fall to NL, whichever columns. That ruled out
   the P5.9-i binding family in one EXPLAIN.
5. **Measured**: Q47 flag-ON 8m40s → **13 s**; DS05 subset (Q6/30/47/54/58/
   83/84) ON `PASS=6 TIMEOUT=1` → **`PASS=7 TIMEOUT=0`**; OFF subset unchanged,
   checksums identical.

Files: `internal/planner/cost_funcs.go`, `pathgen.go`, `joinpathsnli.go`,
`bushy.go`, `nestloop_ntuples_test.go` (new, 5 tests). Docs: 09 §3.8, bundle
README, docs/design/README.md, IMPLEMENTATION-TODO P5.9-j [x], fix_plan P5.9-j
[x], 2 ledger rows, `analysis/leftdeep-joins/2026-08-05-p59j-ds05-{on,off}.txt`.

Gates run: `go test ./internal/planner/` (green), UNITS precommit (green),
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35), DS05 subset sweep
BOTH arms (7 queries each, all PASS), pgbench smoke via the commit hook, `make
ralph-state-guard` (repaired a stale progress marker). **NOT run: full DS05
`sweep` (~1 h) and `make plan-diff`** — still ledgered from P5.9-h, discharge
at run 4.

Nightly triage 20260805-014309: unchanged run, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9-h's cost half** — the last named S5 defect: why a
bound-less ordered index scan of `orders` + merge beats Seq Scan + hash under
the flag (`costIndexScan` at selectivity 1.0, `pathindexordered.go`). Then run 4
of the bar, with the DS05 clause no longer optional.

In-flight: none.
