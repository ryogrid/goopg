Task: M0140-0005 — "file the two out-of-reach items as ledger rows" (plan-parity
milestone group, M0140 TPC-DS parallelism). **DONE, committed and pushed** this
loop (branch `plan-parity-with-pg-take2-ralph`, commit `14561db1d`). No
production code changed (filing-only task, matching M0140-0004's precedent).
**M0140 is now fully closed — all five tasks [x].**

Files: `docs/design/0100-0149/m0140-0005-q14-third-category-and-nonplanner-floor-filing.md`
(new design doc), `docs/design/README.md` (+index row), `.ralph/deferral_ledger.md`
(+2 rows: `m0140-0005-q14-parallel-hash-execution-model`,
`m0140-0005-nonplanner-heap-density-floor`), `.ralph/fix_plan.md` (M0140-0005
checked off with nested findings).

What was done: both filed items were already fully investigated in the prior
phase's record (`docs/design/not_ralph/plan_parity_fix_take2/`), so this was
citation + ledger filing, not new investigation. K92 (Q14's `parallelism`
mismatch): confirmed PG's `Parallel Hash` needs workers building a shared hash
from a partial inner path behind a barrier, while goopg's `joinOp` deliberately
drains the build side once on the leader before fan-out (`parallel_scan.go`'s
own comment warns the alternative "would silently drop matches") — not a
labelling fix, needs new executor machinery. K14/K15/K41 (non-planner heap-
density floor): `relpages` is a planner input; K39 already closed the
fact-table direction but K41 (dimension tables diverging the OTHER way —
`customer` 1,979 vs 2,872, `item` 716 vs 1,284) stays open, with `character(N)`
blank-padding (R23) as leading-but-unverified candidate mechanism.

Key symbols: none touched (filing task). Referenced: `parallel_scan.go`
(joinOp build-side-drain comment), `parallel_hash_build.go` (K16's existing
cooperative build), `joinpathsparallel.go`'s `addPartialHashJoinPath` (future
producer site if K92 is ever unblocked).

Gates run: `go build ./...` clean (no code touched). Pre-commit hook (pgbench
smoke) passed on `git commit` (not bypassed). `make ralph-state-guard`: same
recurring stale status="running"/progress="completed" pattern as every prior
loop this session; auto-repaired, confirmed consistent.

In-flight: none.

Next step: M0140 is fully closed. Per the banner order (fix_plan.md
"Selection order"), M0138/M0139/M0140 are all done or independent-and-done —
select **M0141-S0** next: "scoping recon (measurement only, no production
change)" — size the `AGGSPLIT_INITIAL_SERIAL`/`AGGSPLIT_FINAL_DESERIAL`
programme in goopg terms (which executor surfaces change, how many sites, what
a row-borne partial-state representation costs), produce a slice list, file
S1..Sn into the M0141 section with an entry gate, OR record a no-go. Read
`docs/milestones/0141-upper-planner-ordering-contest.md` and
`METHODOLOGY3/02-open-problems.md` §B4 (K96/K97) before starting — a production
diff in S0's commit is a scope violation. If M0141-S0 turns out to need more
recon time than one loop affords, M0143 (gated on nothing, e.g. M0143-0001 "an
in-process test that crosses a DATABASE boundary", named highest-leverage of
its six items) is the fallback per the banner's step 4. Do not re-open
M0140-0001 through -0005.
