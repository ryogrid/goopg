(idle — nothing in flight)

Last loop: **M0127-P5.9-a** — LANDED, gates green, committed + pushed.
Facts the next loop must NOT re-derive:

1. `internal/planner/relfromjoinlist.go` is new: `planJoinlistSearch` (entry),
   `makeRelFromJoinlist` (allpaths.c:3352), `searchOneProblem` (the ONLY
   written-down form of the protocol), `leafRel`, `validateJoinlistProblem`,
   types `joinlistProblem` / `joinlistRel`, and `joinlist.leafRange`.
2. **The protocol, in order, and it is not interchangeable:**
   `buildInitialRels` → set `s.clauses` → `s.addBaseRelIndexPaths(cat)` →
   `s.joinSearch(s.clauses, newJoinRelBuilder(s, cat))` → `s.finalPath()` →
   `createPlanAtSearchRootRange`. `addParameterizedIndexPaths` READS
   `s.clauses`, which `joinSearch` also sets — publish it before the producers.
3. **Clause placement needs no pass.** Each problem builds its clause list with
   per-ITEM `cumOffsets`; `relidsOfExpr` drops an intra-item clause
   (`relLevel < 2` — already placed below) and declines a clause reaching out
   of a sub-problem's window (`ok=false`), which the parent then places.
4. A sub-joinlist enters its parent as ONE `PathPrebuilt` leaf with
   `binding{offset: base}` and a **table-less** `baseRelInfo` (that nil table is
   what makes every base-rel-only producer decline it). Its pathlist AND its
   searched row estimate are discarded — parent re-derives rows via
   `initialRelRows`'s non-scan arm (`EstimateRows`). Ledgered; the real fix is
   indexing level lists by joinlist ITEM instead of base-relid popcount.
5. `createPlanAtSearchRoot(p, w)` is now `createPlanAtSearchRootRange(p, 0, w)`;
   `boundaryMap(lay, base, width)` and `missingBindingCoords(m, base)` took the
   window. Sub-problems carry GLOBAL binding coordinates and publish only their
   `[base, base+width)` slice — legal because a sub-joinlist covers a
   CONTIGUOUS FROM-item run (`leafRange` checks it; it is not assumed).
6. Still inert: `GOOPG_PGSHAPED_DP` OFF, no `planSelect` call site. DS05/PLAN
   not run and that is not a skip (same structural reason as P5.7-a/-b, P5.8).
7. 2 ledger rows added (pathlist+rows collapse at the boundary; no
   residual-conjunct accounting). P5.8's "no joinlist CONSUMER" row FLIPPED to
   `resolved`.

Gates run: UNITS green (`/tmp/units-p59a.log`, exit 0, zero FAIL lines);
planner package green; gofmt clean on the three touched files; commit-hook
pgbench smoke. No orphaned servers.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY (fix_plan lines ~1097/1203/1215), left unchecked per the banner.

Next step: **M0127-P5.9-b** — the `planSelect` seam. `tryBushyDP`
(bushy.go:85) / `runJoinSearchBelowPinned` (predp.go:46/127) hand
`resolveContext.joinlist` + `preprocessLimit`'s fraction to
`planJoinlistSearch` under `GOOPG_PGSHAPED_DP`, and must decide which
conjuncts the search consumed before rebuilding the residual `Filter`
(`tryBushyDP` returns `(node, residualPredicate)`). Then P5.9 proper: the
09 §3 acceptance run (collapse OFF then ON) + ratchet baseline + estimate
audit, then the flag flip or a documented no-go.

In-flight: none.
