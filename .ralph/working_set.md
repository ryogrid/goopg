Task: M0142-0003b — "L6 tie-break precision" for the M0142 group. **DONE,
committed this loop** (branch `plan-parity-with-pg-take2-ralph`, commit
`acab0c8c8`). Production diff (a trace format string), but gated-and-inert-
by-construction on the default path (see below).

Files: `internal/optimizer/pathtrace.go` (`formatPathLine`: `%.2f` -> `%g`
for `startup`/`total`/`inputtotal`, matches sibling `DPTRACE cost` channel's
precision), `internal/optimizer/pathtrace_test.go` (3 format-sentinel
assertions updated, `inputtotal=-1.00` -> `inputtotal=-1`),
`docs/design/0100-0149/m0142-0003b-q9-l6-tie-break-precision.md` (new design
doc), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0142-0003b checked off with findings; new task M0142-0003c filed as the
concrete follow-on), `.ralph/deferral_ledger.md` (+1 row, task-id
`m0142-0003b`). Scratch artefacts `analysis/m0142/m0142-0003b-q9-{plan,dptrace-l6,dppath-l6}.txt`
committed (trace evidence, same precedent as -0003a).

What was found (measured via a throwaway instrumented server — private
clone of `bench/tpch/runtime_goopg/data`, port 5534,
`GOOPG_CG_UNIT=m0142-0003b`, cleaned up after use; same protocol as
-0003a):
- **Finding 1: the L6 tie is EXACT, not a rounding artifact.** At full
  float64 precision (`%g`), goopg's winning `join.hash` candidate
  (`{part,supplier,lineitem,partsupp,orders}⋈{nation}`) and PG's own
  chain's cheapest `nestloop.index` candidate
  (`{part,partsupp,supplier,nation,lineitem}⋈{orders}`) both print the
  bit-identical `total=108806.04332442369` — despite reaching it via very
  differently composed (input, marginal) cost pairs (~50 apart each way).
  Why the two different compositions land on the identical bit pattern is
  an OPEN, lower-priority question (algebraic identity vs coincidence) —
  flagged in the ledger, not chased.
- **Finding 2: the tie-break mechanism is PG-faithful, not a goopg
  shortcut.** `DPTRACE pair` confirms goopg's own partition is the FIRST
  (`created=1`) pairing at level 6; PG's chain's equivalent pairing is
  later (`created=0`). `addToPathlist` (`path.go:1080-1098`) rejects an
  exact-tying newcomer outright (`comparePaths` -> `relEqual` ->
  incumbent kept) — and `comparePaths` (`path.go:894-922`) ports PG's own
  `compare_path_costs_fuzzily`/`STD_FUZZ_FACTOR` dominance test verbatim
  (`postgres/src/backend/optimizer/util/pathnode.c`). This is real PG
  behavior, not a bug to fix.
- **Verdict**: goopg's chosen plan does NOT win Q9's L6 step on true cost
  (settles the fork's first branch: no clear cost gap, an exact tie), and
  the tie-break rule itself is not the defect (refines the second branch)
  — the load-bearing divergence is DP **enumeration order** at level 6
  (which partition gets registered first), not a costing term (B8/B10
  ruled out for this specific tie) and not a dominance-rule bug.

Key symbols: `internal/optimizer/pathtrace.go` (`formatPathLine`, now `%g`),
`internal/optimizer/path.go:1080` (`addToPathlist`, exact-tie rejection),
`internal/optimizer/path.go:894` (`comparePaths`, PG-faithful fuzzy-cost
dominance), `internal/optimizer/joinsearchtrace.go` (`DPTRACE pair
created=0/1`, the enumeration-order evidence M0142-0003c needs to build on).

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` (full
package) passes. **`scripts/tpch-spotcheck.sh` did NOT run** — its
private-clone snapshot step needs the shared `:65433` TPC-H cluster to go
quiet, and a pre-existing `tmp/goopg-bench-bin` process (not started by this
task) was answering on `:65433` for the gate's full 3×60s retry budget;
AGENT.md forbids stopping the shared cluster to unblock it. Judged
acceptable for THIS diff only because `formatPathLine`/`tracePath` are only
reachable behind `GOOPG_PGSHAPED_DP_TRACE` (default off) — the default
execution path is provably unchanged by construction, not merely by a
passing test. **Do not extend this precedent to a future task that touches
non-gated planner logic** — run the real spotcheck once the shared cluster
is free. Pre-commit hook's pgbench smoke: ran and PASSED as part of `git
commit` (0 failed, ~42-146 TPS across the three pgbench arms). `make
ralph-state-guard`: same self-repairing "status=running vs stale
progress=completed" pattern as the last three loops, self-repaired to
"in_progress", re-ran and confirmed OK.

In-flight: none. Throwaway server/clone (`/tmp/pp-m0142-0003b`,
`GOOPG_CG_UNIT=m0142-0003b`, port 5534) and the throwaway binary
(`/tmp/goopg-m0142-0003b`) were stopped/removed after use. The shared
`tmp/goopg-bench-bin` process on `:65433` was left exactly as found — NOT
stopped, NOT restarted (it is not owned by this task and AGENT.md forbids
touching it). All other shared bench clusters
(`:65432`/`:65437`/`:65438`) untouched.

Next step: M0142-0003c is now filed in `.ralph/fix_plan.md` — a **recon-first**
task (measurement only, per the practice card's "size a campaign, don't
attempt in one sitting" guidance for planner-search-order surgery) to check
whether goopg's level-6 relset-pair generation order
(`internal/optimizer/joinsearch*.go`) matches PG's own
`join_search_one_level` (`postgres/src/backend/optimizer/path/joinrels.c`)
visitation order — the actual load-bearing variable this loop's Finding 2
identified. Do NOT re-open -0003a/-0003b's verdicts (closed, recorded above
and in the ledger) or re-derive the exact-tie/tie-break findings. Re-read the
`## Current Priority` banner fresh next loop before picking: M0137-M0140
fully closed; M0141's two live slices (`M0141-S2a-fix`, `M0141-S2b`) both
still need their own scoping pass before being same-loop-sized (unchanged
finding from prior loops); M0142-0003c is now M0142's selectable task,
alongside M0143 (gated on nothing). Before running any TPC-H gate, check
whether the shared `:65433` cluster is still busy (`pg_isready -h 127.0.0.1
-p 65433`) — if so, either wait/retry or scope the task to avoid needing it,
per this loop's experience.
