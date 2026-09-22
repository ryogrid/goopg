(idle — nothing in flight)

# Loop #88 — M0145-0004 residual is BLOCKED (5 loops, 5 refuted hypotheses)

Banner unchanged; 0018 owner-blocked → chain stays at **M0145-0004** (`[ ]`).
Recon: no `internal/`/`cmd/` file touched (C1). Movement: none.
Design: `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`,
final section ("Two more eliminations, and a BLOCKED call").

## Eliminated this loop (white-box probe: pipeline on + parallel-sized stats)
```
*SetOp stampedRel=true   rel.ConsiderParallel=true   partialPaths=1
```
- **(2) refuted** — the `*SetOp` the outer binder gets DOES carry the stamp;
  `createSetOpPlan`'s rebuild preserves it.
- **(3) refuted** — the stamped rel's CP is true AND it holds a partial path.
- **(1)** (mark not reaching `baseRelInfo`) is all that's left, and the SOURCE
  does not support it: `seamLeafRelInfo` routes a union leaf through
  `estimateBaseRelInfo` (`b.table` non-nil — `planSubqueryRangeVar` builds a
  synthetic `catalog.Table` for the alias), and that constructor carries
  `appendrel`. The two constructors that omit it are the Semi/Anti synthetic
  arm and a `bindingIdx:-1` default.

## ⚠ WHY BLOCKED — do not start a sixth hypothesis
| loop | hypothesis | verdict |
|---|---|---|
| 84 | CTEScan hides the carrier | refuted (Q71/Q76 have no CTE) |
| 85 | gather placement, 3 producers | refuted (hoist fires, paths accepted) |
| 86 | outer link wins no partial | refuted (it does, accepted) |
| 87 | `*Project` wrapper | refuted (bare `*SetOp`) |
| 88 | lost stamp / stale CP | both refuted |
The remaining candidate is unsupported by the source too → the reasoning is
wrong somewhere static reading and existing traces cannot see.

## THE UNBLOCK (needs an owner nod because of C1)
Add ONE diagnostic line to `addAppendRelPartialPaths` naming which condition
declined (mark / ConsiderParallel / carrier / empty PartialPathlist / tlist),
under the existing `GOOPG_PGSHAPED_DP_TRACE` gate. It declines with a bare
`continue` and NO trace — that observability gap is why four loops each had to
invent a candidate. C1 makes a trace an **impl** task with the full gate set,
so file it as `Kind: impl` and run the gates; do not slip it into a recon.

## Gates
Recon, zero production diff → no value gates (C1). state guard OK; pgbench
smoke via the commit hook. Scratch probe test removed; tree clean.

## ⚠ LINEAGE BLOCKER STILL OPEN
No new task can be filed under root M0145-0001. Do NOT mark the root `[!]`.

## Owner escalations — FIVE open now
1. partition_aggregate inventory row. 2. template1 collision (A vs B).
3. M0145-0018 (option re-take + criterion-1 waive). 4. M0145-0012 behind
M0145-0020a. 5. **NEW**: lineage baseline re-pin + permission for the
one-line hoist trace (C1 impl task).
