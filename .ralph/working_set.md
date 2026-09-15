Task: M0142-0012a — scoping recon for M0142-0012's blast radius. **DONE and
committed** this loop (`181b2e9a2`). M0142-0012 itself (the actual fix) is
still open and is now the clear next pick in the M0142 milestone.

Files: `docs/design/0100-0149/m0142-0012a-scoping-recon-blast-radius.md`
(new, full writeup), `docs/design/README.md` (indexed), `.ralph/fix_plan.md`
(0012a `[x]`), `.ralph/deferral_ledger.md` (new row). `git diff` on
`internal/optimizer/cardinality.go` is empty — temporary trace added,
measured, fully reverted (same discipline as M0142-0010/0011).

Key symbols (for the NEXT loop implementing M0142-0012 itself):
`EstimateRows`/`estimateJoin`/`joinEquiPairs` (`internal/optimizer/
cardinality.go:93-96,722,1129`) — needs a new `j.Lateral`-aware arm.
`estimateNLIndexJoin` (`cardinality.go:240-266`, already correct incl.
M0142-0006's SEMI/ANTI fix) is the logic to generalize/reuse.
`createNestLoopIndexJoinPlan`/`createNestLoopIndexJoinPlanFused`
(`createplannl.go:107,208,265,355-364`) build the `Join{Algo:NestedLoop,
Lateral:true, Right:*IndexScan}` shape whose equi-key lives on the
`*IndexScan` child's own `Key`/`Keys` (`OuterColumnRef` nestloop param),
invisible to `joinEquiPairs` today.

Findings this loop: measured (not just inspected) the blast radius flagged
by M0142-0011. Env-gated trace (`GOOPG_M0142012_TRACE=1`) on `estimateJoin`,
counting calls where `j.Lateral && j.Right` is a bound `*IndexScan`/
`*IndexOnlyScan` with zero `joinEquiPairs`. Ran on two PRIVATE throwaway
servers (never the shared `:6543x`/`tmp/goopg-bench-bin` lanes — the TPC-H
`:65433` bench cluster had a live peer loop's server on it all loop, never
touched): TPC-H (SF=1 data dir copied to `/tmp/goopg-m0142012-tpch-data`,
private instrumented binary, port 5534, cgroup unit `m0142012-tpch`) and
TPC-DS SF0.25 (private instrumented binary `tmp/goopg-m0142012-bin` against
the idle `:65437` gate cluster's own data dir). **TPC-H 6/21 queries hit it**
(Q2 10, Q7 51, Q8 94, Q9 29, Q11 21, Q21 19 — 224 total call-site hits,
includes flagship Q9 and the M0077-era Q21 NLI witness). **TPC-DS 69/99
queries hit it** (6347 total call-site hits; Q14 highest at 1009, then
Q80/Q49/Q64/Q88/Q23/Q60/Q56/Q33/Q61/Q66/Q38/Q87/Q24/Q72 all >90 hits).
Counts are DP-search call-site hits (candidates costed during planning), not
final-plan node counts — a discarded candidate still increments the total,
so these numbers overstate final-plan impact but are the right signal for
"is this worth fixing" (yes, decisively). Small overlap with M0142-0005's/
M0142-0009's/M0142-0010's own witness populations (corroborating, not
duplicate fixes — confirmed by name-checking their witness lists against
this loop's hit lists).

Next step: **implement M0142-0012** (`.ralph/fix_plan.md`, full resume point
already written by M0142-0011, now backed by real numbers from 0012a). Add a
case ahead of/inside the generic `*Join` dispatch in `EstimateRows`
(`cardinality.go:95-96`) recognizing `j.Lateral && j.Right` is a bound
`*IndexScan`/`*IndexOnlyScan`, routing to a generalized version of
`estimateNLIndexJoin`'s logic (adapt `j.Outer`/`j.Inner` reads to
`j.Left`/`j.Right`/`j.Predicate`; INNER case stays `return l`; SEMI/ANTI
reuses `nliSemiMatchFraction`'s formula sourced from the `*IndexScan`'s own
bound key). Pin with a test mirroring
`TestEstimateRowsNLIndexJoinSemiScalesByMatchFraction` built via the
decomposed `Join{Lateral:true}` shape. Re-verify the Q33 CTE-branch witness
directly (`rows=1` should become `rows≈32`). Given the measured blast
radius (69/99 TPC-DS, 6/21 TPC-H, including Q9/Q21), this task is EXEMPT
from finishing in one loop per AGENT.md's plan-parity harness — land the
code + unit test in one loop if time allows, but the full floor-measurement
suite (TPC-H plan-parity `-serial`, TPC-DS plan-parity, `make ea-ratchet`,
SF0.25 regression sweep) may need to be its own immediately-following task;
name it explicitly in the report if so, per the harness's own rule (do not
substitute a cheaper gate).

Gates run: `go build ./...` clean; `go build ./internal/optimizer/...` and
`go test ./internal/optimizer/...` PASS (both before and after the
trace-add-then-revert — confirms zero production diff); `git status`/
`git diff` on `cardinality.go` confirmed empty pre-commit; `make
ralph-state-guard` ran clean (self-repaired a stale `progress.status` marker
from the previous loop's clean exit, not a project-completion marker — no
action needed, already reconciled). Pre-commit pgbench smoke PASS (ran as
part of `git commit`). Full tpch-spotcheck/ea-ratchet/SF025-sweep NOT run
this loop — correctly skipped per the recon-only precedent (no production
code changed).

In-flight: none. Both private servers (TPC-H `m0142012-tpch` cgroup scope
on port 5534, TPC-DS `sf025` scope under the private binary on port 65437)
stopped via their normal lifecycle commands before this write-up, verified
via `ps aux` — no stray `m0142012`/port-5534 process remains. All scratch
artefacts removed (`/tmp/goopg-m0142012-tpch-data`, `/tmp/m0142012-*`,
`tmp/goopg-m0142012-bin`, `/tmp/estimate-audit-m0142012`).
