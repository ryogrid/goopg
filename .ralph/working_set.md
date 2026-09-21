(idle — nothing in flight)

# Loop #85 — M0145-0004 residual NARROWED; last loop's 3 candidates all refuted

Banner unchanged; 0018 owner-blocked → chain stays at **M0145-0004** (`[ ]`).
Recon: no `internal/`/`cmd/` file touched (C1). Movement: none.
Design: `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`,
final section ("Narrowing it further").

## All three candidates from loop #84 are REFUTED
SF0.25 knob-arm DP trace: `baserel.appendrel.partial` **4 paths, every one
`verdict=accepted`**; `upper.setop.append.partial` 11, `.mixed` 3. The mark,
the partial SetOp paths and the hoist ALL work. So it is not
`addBaseRelGatherPaths`, not `upperSplitWorkers`, not the member search root.

## The real shape — the CHAIN STAYS NESTED
Q71's union has THREE members:
```
goopg: Append{ Gather{Append{ws, cs}},  Gather{store_sales} }
PG:    Gather{ Parallel Append{ ws, cs, store_sales } }
```
The INNER link of the right-leaning `SetOp(A, SetOp(B,C))` gets the parallel
Append + hoist; the OUTER link does not, so its inputs are gathered separately
and appended serially. PG's `is_simple_union_all_recurse` walks `larg` AND
`rarg` into ONE appendrel → one partial Append over all members.
**Divergence = chain-flattening DEPTH. Gather placement is the symptom.**

## ⚠ A STALE COMMENT that would have misdirected the fix
`addPartialSetOpPath`'s prose lists "a nested `*SetOp`" as a disqualifying
wrapper. **The code disagrees**: `setOpBranchPartialChainOK` ADMITS a nested
set operation via its carrier check (M0144-0003b-1 added exactly that). Prose
predates code. Do not trust it.

## THE ONE MEASUREMENT STILL MISSING — take it first
Why the OUTER link wins no partial path. `addPartialSetOpPath` returns early
unless `setOpRel.LeftBranchRel` AND `.RightBranchRel` are non-nil and both
`ConsiderParallel`. For the outer link the right input is the inner link's
SETOP rel → **instrument whether that field is populated for a nested-`*SetOp`
input BEFORE changing anything.** Three guessed candidates were already wrong.

## Risk to carry into the fix
Two levels of block claim cooperating across workers. This task was bitten
there once: an unclaimed `PathSetOp` under a partial hash join made every
worker replay the whole union (80/120/200 rows at workers=1/2/4 for a 40-row
join). Needs the same per-worker row-identity pin.

## Gates
Recon, zero production diff → no value gates (C1). state guard OK; pgbench
smoke via the commit hook. Reused existing logs/captures; no new lane.

## ⚠ LINEAGE BLOCKER STILL OPEN (from loop #84)
No new task can be filed under root M0145-0001 — guard refuses, and its remedy
(mark root `[!]`) contradicts the owner's 2026-09-22 re-open. Findings go
inside existing tasks until the owner re-pins the baseline. Do NOT mark `[!]`.

## Owner escalations — four open, unchanged
partition_aggregate inventory row; template1 collision (A vs B); M0145-0018
(option re-take + criterion-1 waive); M0145-0012 behind M0145-0020a.
