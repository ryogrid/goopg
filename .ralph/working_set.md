(idle — nothing in flight)

# Loop #84 — M0145-0004's last residual RE-SCOPED (its description was wrong)

Banner unchanged; 0018 owner-blocked, so the chain stays at **M0145-0004**
(still `[ ]`). Recon: no `internal/`/`cmd/` file touched (C1). Movement: none.
Design: `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`,
final section. Recorded INSIDE M0145-0004 — see the lineage escalation.

## The residual said "CTE-wrapped union leaves (CTEScan hides the carrier —
## Q2/Q14/Q71/Q76)". Measured, that is wrong for THREE of the four.
- **Q71, Q76: no CTE at all** — plain `FROM (… UNION ALL …) alias`, exactly
  the shape the mark already handles. CTEScan visibility cannot be it.
- **Q14: union CTEs referenced 6× each** → upstream does NOT inline them, so
  there is no appendrel to make. Q14 LEAVES the population.
- **Q2: the one genuine case** — `wscs` has a single reference, which is PG's
  inlining criterion.

## The real gap is WHERE THE GATHER SITS
```
Q71 goopg: Append{Gather{Append{Parallel Seq Scan}}}   PG: Gather{Parallel Append{…}}
Q76 goopg: Append{Gather{…}, Gather{…}}                PG: Gather Merge{Parallel Append{…}}
```
goopg gathers INSIDE each member then appends; PG keeps members partial under
ONE Gather above a parallel-aware Append.

## And the appendrel machinery is NOT what decides it
Default arm vs knob arm, same capture run: **Q71/Q76 byte-identical** in
Append/Gather structure; Q2 differs only in cost. So the hoist either does not
fire for them or fires without changing the shape.

## ⚠ NEW BLOCKER — the LINEAGE GUARD vs the owner's re-open
Filing the finding as `M0145-0004a` was REFUSED: root M0145-0001's last five
completed descendants (0006/0014/0015/0016/0017) all read `Movement: none`, so
the budget is exhausted and the guard's remedy is to mark the ROOT `[!]`.
**But the owner re-opened M0145-0001 on 2026-09-22 and sequenced this very
chain (0004 → 0005 → 0007 → 0008) as the answer to that escalation** — marking
it `[!]` again would contradict a live owner decision (R4). The guard cannot
see the re-open because those five rows are still the last five.
So the finding is recorded inside the EXISTING M0145-0004 task instead.
**This recurs on EVERY attempt to file work under this root** — the banner's
own chain is blocked from filing. The owner must clear it (re-pin the lineage
baseline, or exempt the re-opened root). Do NOT mark the root `[!]`.

## Next step (stated in M0145-0004's body, do NOT skip it)
Instrument which of `addBaseRelGatherPaths` / `upperSplitWorkers` / the member
scope's own search root commits the gather BEFORE the Append is built. Do not
assume it is the hoist — guessing here is the exact failure mode the last
several loops kept catching.
Expected movement once known: the `parallelism` category on SF0.25 (84–85 in
both arms today).

## Gates
Recon, zero production diff → no value gates (C1). state guard OK; pgbench
smoke via the commit hook. Reused loop #83's captures
(`tmp/m0145-0004-tlist/`) — no new lane, no new capture.

## Owner escalations — four open (unchanged)
partition_aggregate inventory row; template1 collision (A vs B); M0145-0018
(option re-take AND the criterion-1 waive, loop #82 evidence); M0145-0012
re-sequencing behind M0145-0020a.
