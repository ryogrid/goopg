(idle — nothing in flight)

Last loop: **`M0125-0003` STAGE 2 landed** (bushy DP seed), default-off. Banner
item 1 said "land stage 1", but stage 1 had already landed at `c26c6fc3` — the
next undone slice was stage 2. Banner updated to match.

Fix: `bushySeedRowCounts` (`internal/planner/bushy.go`, extracted from
`enumerateBushyPlans`) adds the estimate_rel_size fallback as a third tier under
the DP's singleton seed, via a new single gated entry point
`relSizeFallbackRows(stage, cat, tbl)` (`relsize.go`) that `stage1RelSizeRows`
now delegates to as well.

## NEXT (banner order)

1. **`M0125-0003` stage 2's TIMED arm** on a quiet host — take it BEFORE stage 3,
   because stage 3 makes `filteredRows` positive cold and SHADOWS the stage-2
   tier at this site. Round-4's five regressed queries are the watch list; Q9
   newly reaches `Gather`/`Workers Planned: 4` and is untimed.
2. Then `M0125-0002` (seven walkers, one per commit) / `-0005` / stage 3.
Owed independently, now **five commits deep**: one full 99-query SF0.5 gate run
on a quiet host (own ledger row, 2026-07-30).

## Facts the next loop should NOT re-derive

- **A plan-SHAPE diff is the gate that still works under nightly contention.**
  Both arms were measured this loop while `run-nightly.sh` held the host: it
  proves a flag-gated landing is inert AND that it is actually wired — the
  failure mode a "flag-off, inert" commit hides is being inert in BOTH states.
- **The pre-stage-2 DP seeded `rows=1` for EVERY relation at S-cold.** Join order
  was being chosen on no cardinality signal at all. Estimates now land within
  0.37–1.01× of SF=1 truth (`nation` 20.8× = the 10-page floor, correct).
- `bench/tpch/setup_goopg.sh` propagates the environment to the server (plain
  `nohup`), so `GOOPG_RELSIZE_FALLBACK=2 bench/tpch/setup_goopg.sh` is how the
  flag-on arm is taken. **Restart it flag-off afterwards** — a bench cluster left
  with a non-default planner flag would silently poison later loops.
- `gofmt -l internal/planner/` lists ~18 PRE-EXISTING files (go1.26 local vs
  go1.25 repo baseline). Attribute before reacting; never `gofmt -w`.
- 4th consumer discovered: `reorderCommaFromByCardinality`
  (`joinorder.go:89-93`) still bails when any table lacks stats → blind S-cold.

Gates run: `go build ./...` + `go vet` clean; planner + executor suites PASS;
units suite PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
`make plan-diff LABEL=tpcds-round2-head` 22/22 MATCH flag-off and 22/22 DIFFER
at `GOOPG_RELSIZE_FALLBACK=2`; `make ralph-state-guard`; pgbench smoke via hook.

In-flight: none.
