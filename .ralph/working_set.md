(idle — nothing in flight)

Last loop (#7, 2026-07-31) landed **M0125-0034's set-operation arm** — C1, the
dominant timeout mechanism. `internal/planner/pushdown.go` (+`*SetOp` case in
`collectScanOutputNames`; both M0097-0058 `containsSetOp` bailouts retired),
`internal/planner/nl_index_join.go` (`pickInnerScanForNLI` declines its
left-as-inner flip for a set-operation outer); design
`docs/design/0125-0034-setop-join-promotion.md`; tests
`internal/executor/setop_join_promotion_test.go` (4).

**Acceptance MET: `Q71 PASS 580 rows ck=521a7af7606d10c1`** = the oracle row.
30 `Nested Loop (CROSS)` nodes eliminated; **Q5 Q8 Q14 Q54 Q71 TIMEOUT →
PASS**, SF0.5 timeout class **12 → 7**.

Three findings the next loop should not rediscover:
1. **The blocker was the NAME walk, not the guard.** `collectScanOutputNames`
   enumerates node kinds; with no `*SetOp` case `allColumnRefNamesInScope`
   answered false and `pushOneConjunct` declined *before* reaching its own
   `containsSetOp` bailout. An under-enumerated permissive check fails
   silently — as a missed optimisation with nothing in the plan to say why.
   `*Distinct`, `*Limit`, `*WindowAgg`, `*IndexOnlyScan` … are still absent
   (ledger row).
2. **M0097-0058's premise is refuted.** `SetOp.Output()` IS the narrow schema,
   and the executor pads a `leftWidth+rightWidth` keyRow before evaluating
   either join key, so `index out of range [57] with length 1` cannot arise at
   the join node. The third guard (explicit `JOIN … ON`, planner.go) is still
   in place — no query reaches it (ledger row).
3. **Fixing the promotion made an unreachable NLI path reachable.**
   `pickInnerScanForNLI`'s left-as-inner flip emits `outer ++ inner`; Q71
   planned `Append ++ item` while `sum(ext_price)` stayed bound to
   `item ++ Append` → "aggregate sum requires numeric argument in v0". Now
   declined. Note the flipped shape is **PG's own plan** (ledger row).

Gates: units PASS; `tpch-spotcheck.sh` `RESULT=PASS` (Q12=2 Q13=35); plan-diff
vs `warm-stats-base` 10/22 DIFFER but every changed line is M0125-0039's
qualification → TPC-H-plan-inert; re-pinned
`plan_snapshots/m0125-0034-setop-join-promotion.txt`. All 21 set-operation
TPC-DS queries swept (the complete reachable surface), 15 unchanged.
NOT discharged: the timed TPC-H arm and the full 99-query gate — the nightly
CI batch held the host all loop (load ≈ 9.9), so **no second measured this
loop is a timing**.

Per the banner the **next selection is `M0125-0035`** (C2 qual placement);
M0125-0034 stays unchecked for its CTE/derived-aggregate arm (Q30 Q64 Q65 Q81,
8 crosses) — fold it into -0035 if the diagnosis converges. Re-read the banner
before selecting; it outranks this note.

Host note: nightly run was live throughout; a private binary
`tmp/goopg-m0125-0034-bin` was used everywhere EXCEPT `tpch-spotcheck.sh`,
which rebuilds the shared `tmp/goopg-bench-bin` — that clobber happened at
~01:53 and is worth avoiding next time. Both goopg TPC-DS clusters are DOWN
again, as they were found; :65438 (PG) was already UP.
