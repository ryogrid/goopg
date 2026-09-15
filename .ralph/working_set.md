Task: M0141-S2b-5 — instrumented-trace recon resolving whether
`electOrderedGrouping`'s `anyTranslated` gate declines for the 6 GROUP_AGG
mechanism-B TPC-H queries. DONE and committed this loop. Hypothesis REFUTED
(again) — the real cause is one level further downstream than either S2b-0
or S2b-5 assumed.

Files: `internal/optimizer/upperorderedgrouping.go` (PERMANENT change —
added a `traceGroupDecline` helper + `DPGROUP` trace lines at every decline
point in `groupingEmissionPathkeys`, a success-translation line, and a
loop-decline/election-outcome line in `electOrderedGrouping`; all gated on
the pre-existing `dpTrace`/`GOOPG_PGSHAPED_DP_TRACE`, zero behavior change
when off, unit tests still pass). `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md`
(new "S2b-5 result" section). `docs/design/README.md` (index blurb updated).
`.ralph/fix_plan.md` (S2b-5 closed `[x]` with result; new `M0141-S2b-6`
filed as the direct continuation — now a cost-model investigation, not a
wiring-gap search). `.ralph/deferral_ledger.md` (new row, task-id
`m0141-s2b-5`). Scratch test file `internal/testutil/tpch/zzz_scoping_probe_test.go`
was created to run the trace and deleted before commit (same precedent as
S2b-0) — nothing from it is in the tree or history.

Key symbols/paths: `internal/optimizer/upperorderedgrouping.go` —
`groupingEmissionPathkeys` (now traces every nil-return reason),
`electOrderedGrouping` (now traces `decline(reason)`/`restore(reason)`
call sites and the final elected shape: `Sort-over-Aggregate` vs
`bare-Aggregate`, plus winning `AggStrategy`). `tmp/take4/runs/plansweep/
q04.{pg,goopg}.txt` — uncommitted scratch capture cited as corroborating
evidence (real PG picks `GroupAggregate` fed by a Sort BELOW it for Q4; no
Sort above; goopg's captured plan is the mirror-image Sort-over-Hashed
shape this loop's trace predicts).

Findings this loop: (1) A `go test` CACHE TRAP distinct from S2b-0's
parallelism trap: the scratch probe lives in package `tpch_test`
(`internal/testutil/tpch`), which does NOT import `internal/optimizer` at
Go-compile-time — the server under test is a `go run ./cmd/goopg`
subprocess. Editing `upperorderedgrouping.go` therefore does not change the
probe package's own build inputs, and a first re-run after adding the
instrumentation silently replayed a byte-identical CACHED `go test` result
from before the trace existed (visible as `ok ... (cached)`, floating-point
costs matching to 17 digits). Re-running with `-count=1` (the documented
"one-off probe" carve-out, NOT a gate run) produced the real result.
**Any future probe that changes code reached only through a
`cluster.New`-spawned subprocess must force `-count=1`, or it silently
re-reports stale findings.** Worth a memory note / AGENT.md line if this
bites again. (2) The real trace: for ALL SIX of Q4/Q5/Q8/Q12/Q21/Q22,
`electOrderedGrouping` reaches a real election — `anyTranslated` is `true`
every time (only the trivially-excluded Hashed candidate ever hits a
`DPGROUP decline` line; the Sorted candidate's translation always
succeeds). Q4/Q5/Q12/Q21 elect `Sort`-over-`Hashed`-`Aggregate` (cost
comparison prefers Hashed+explicit-Sort over the translated sort-free
Sorted candidate); Q8/Q22 elect the sort-free Sorted candidate outright (no
Sort node). (3) For Q4 specifically, a stale uncommitted scratch capture
(`tmp/take4/...q04.{pg,goopg}.txt`) shows real PG's actual chosen shape is
the mirror image: `Finalize GroupAggregate` fed by a `Sort` BELOW it (no
Sort above) vs. goopg's Sort-above-HashAggregate. **Conclusion: the
`electOrderedGrouping`/`groupingEmissionPathkeys` mechanism this whole
design doc (S2b, S2b-0, S2b-5) set out to find a wiring gap in was ALREADY
COMPLETE AND CORRECTLY WIRED the entire time for the GROUP_AGG rel** — the
actual, newly-identified root cause for the 4 TPC-H "mechanism (B)"
witnesses is a cost-model discrepancy in the Hashed-vs-Sorted `PathAgg`
comparison (or the Sort/GroupAggregate cost terms feeding it), a
completely different and larger class of bug than anything in the S2b
decomposition. Filed **M0141-S2b-6** to chase it, with an explicit caution
citing M0141-S2a-fix2's precedent (a plausible-looking fix in this same
neighbourhood was tried, measured net-negative, and reverted) — reproduce
and understand the discrepancy numerically before proposing any change.

Next step: pick per the `## Current Priority` banner (re-read it first in
case it changed) — **M0141-S2b-6** is the direct continuation but is sized
as a real cost-model investigation (diff goopg's DPPATH-captured
startup/total for both candidates against PG's `cost_agg`/`cost_sort`
formulas in `postgres/src/backend/optimizer/path/costsize.c`, then get a
FRESH PG capture for Q5/Q12/Q21 — only Q4 was checked against a real
capture this loop, Q5/Q12/Q21 are asserted by code-read symmetry only).
**M0141-S2b-1** (DISTINCT loop-fix) remains available as an independent,
cheaper pick if S2b-6 is deprioritized. Do NOT attempt **M0141-S2b-2**
blind (needs its own scoping pass, per K24). M0142-0003i/-0003k(c) (shared
`:65433` TPC-H cluster reload) remain BLOCKED on a human decision.

Gates run: `go build ./internal/optimizer/...` clean. `go test
./internal/optimizer/...` (full package, not just the touched file) —
PASS, no regressions from the trace instrumentation. `scripts/tpch-spotcheck.sh`
run per the executor/planner practice card — SKIPPED (expected: shared
`:65433` cluster's `tpch` schema is still gone, per M0142-0003k, exits 0
cleanly, not a new failure). `make ralph-state-guard` — pass (see status
block). Pre-commit hook's pgbench smoke will run automatically on commit.

In-flight: none. The scratch probe's throwaway `cluster.New` server shut
down cleanly via its own `defer c.Stop()`; verified via `ps aux | grep
goopg` afterward (only the pre-existing shared `:65432`/`:65433` clusters
remain running, untouched). Debug log `/tmp/goopg_cluster_debug/s2b5-probe.log`
deleted.
