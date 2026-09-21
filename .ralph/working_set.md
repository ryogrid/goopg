(idle — nothing in flight)

# Loop #86 — M0145-0004 residual PINNED to one missing consumer (3rd hypothesis refuted)

Banner unchanged; 0018 owner-blocked → chain stays at **M0145-0004** (`[ ]`).
Recon: no `internal/`/`cmd/` file touched (C1). Movement: none.
Design: `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`,
final section ("Pinned to one query, and a self-correction").

## ⚠ SELF-CORRECTION of loop #85
"The hoist fires and its paths are accepted" is true CORPUS-WIDE (4 filings)
and **false for Q71** — no `baserel.appendrel.partial` line in Q71's trace at
all. A corpus count was generalised to one query. Don't do that again.

## Single-query trace (Q71 alone, private SF0.25 lane) — the facts
```
upper.setop.append.partial relids={0} rows=74  total=18936.41 accepted  inner
upper.setop.append.partial relids={1} rows=168 total=37279.17 accepted  OUTER
gather                     relids={0} rows=229 total=19959.31 accepted  <- inner gathered
(no gather over relids={1}'s partial; no baserel.appendrel.partial at all)
```
So loop #85's hypothesis (outer link wins no partial path / branch rels unset)
is **REFUTED**: the outer link's partial exists and is accepted.
Plan takes the serial outer Append (39336.25) though gathering the partial
would have cost ~38300.

## Also measured, NOT assumed
`subqueryChainIsSimpleUnionAll` ACCEPTS Q71's union (probed on the parse tree:
`from[1]: SetOp!=nil=true isSimpleUnionAll=true`) → the mark predicate is fine.

## THE ONE HYPOTHESIS LEFT — check the TYPE first, do not code
`addAppendRelPartialPaths` requires `rel.baseLeaf` to BE the carrier and
disqualifies any wrapper (Filter/Sort/Limit/Project/LockRows). Q71's subquery
renames every column, so its root is plausibly a **`*Project` over the
`*SetOp`** → carrier lookup fails silently.
**Check `rel.baseLeaf`'s concrete type for this leaf FIRST.** Three hypotheses
refuted in a row; the cost of assuming a fourth is established.
If it IS a `*Project`: do NOT blanket-admit `*Project`. The wrapper rule is
right for a row-CHANGING Project. Implement positional-identity vs computing —
**M0144-0011a-3 already drew that exact distinction**.

## Gates
Recon, zero production diff → no value gates (C1). state guard OK; pgbench
smoke via the commit hook. Lane stopped, `/tmp/q71lane` removed, 5565 free.
Evidence kept: `tmp/m0145-0004-q71-dppath.log`, `…-q71-single-trace.plan`.

## ⚠ LINEAGE BLOCKER STILL OPEN
No new task can be filed under root M0145-0001. Findings go inside existing
tasks until the owner re-pins the baseline. Do NOT mark the root `[!]`.

## Owner escalations — four open, unchanged
partition_aggregate inventory row; template1 collision (A vs B); M0145-0018
(option re-take + criterion-1 waive); M0145-0012 behind M0145-0020a.
