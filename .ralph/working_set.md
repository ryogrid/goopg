(idle — nothing in flight)

# Loop #87 — the declining condition is NAMED: the union leaf's ConsiderParallel=0

Banner unchanged; 0018 owner-blocked → chain stays at **M0145-0004** (`[ ]`).
Recon: no `internal/`/`cmd/` file touched (C1). Movement: none.
Design: `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`,
final section ("The declining condition, named at last").

## 4th hypothesis REFUTED, then the answer
- **Refuted**: a union subquery in FROM lowers to a **bare `*SetOp`** as the
  join input, with or without column renames (white-box probe). No `*Project`
  wrapper; `rel.baseLeaf` IS a carrier type.
- **THE ANSWER** — from a trace channel that already existed:
  ```
  DPTRACE cpadmit src=base rel={tmp} cp=0 leaf=other     <- Q71's union alias
  DPTRACE cpadmit src=base rel={item} cp=1 leaf=seq      <- every other leaf
  ```
  `considerParallel` emits this under the SAME `GOOPG_PGSHAPED_DP_TRACE=1`
  gate as DPPATH (`joinsearchtrace.go` `baseCP`). So
  `addAppendRelPartialPaths`'s `if !rel.ConsiderParallel { continue }`
  is what declines the hoist.

## NEXT STEP — narrow to ONE of three (each a one-line check)
`considerparallel.go`'s appendrel arm sets
`rel.ConsiderParallel = setOpRel.ConsiderParallel` when the mark is set and the
carrier resolves. `cp=0` means it did not take, so:
1. `s.relInfos[i].appendrel` false at the search (propagation gap — the binding
   DOES set `b.appendrel` at planner.go:5864);
2. **`carrier.setOpBranchRel()` nil** — the node the outer binder gets is not
   the instance `createSetOpPaths` stamped, since `createSetOpPlan` rebuilds it
   via `createPlanNode`. ← most likely on the evidence;
3. `setOpRel.ConsiderParallel` false at that moment (ordering —
   `addPartialSetOpPath` is what sets it).
Check, then fix. Four hypotheses already refuted here; do not code against a
fifth guess.

## ⚠ METHOD NOTE — keep this
`DPTRACE cpadmit` runs under the gate this milestone uses everywhere, and FOUR
loops asked "which condition declined?" without looking at it. When a silent
`continue` is the suspect, check whether an existing trace channel already
covers the predicate before proposing to add one.

## Gates
Recon, zero production diff → no value gates (C1). state guard OK; pgbench
smoke via the commit hook. Reused loop #86's kept trace
(`tmp/m0145-0004-q71-dppath.log`) — no lane started this loop.

## ⚠ LINEAGE BLOCKER STILL OPEN
No new task can be filed under root M0145-0001. Findings go inside existing
tasks until the owner re-pins the baseline. Do NOT mark the root `[!]`.

## Owner escalations — four open, unchanged
partition_aggregate inventory row; template1 collision (A vs B); M0145-0018
(option re-take + criterion-1 waive); M0145-0012 behind M0145-0020a.
