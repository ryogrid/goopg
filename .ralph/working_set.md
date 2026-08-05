(idle — nothing in flight)

Last loop: **M0127-P5.9-c CLOSED** — the P5.9 blocker is attributed and fixed.
Committed + pushed. Facts the next loop must NOT re-derive:

1. **Run 1's attribution was WRONG.** The `outputLayout` / `boundaryMap` /
   `projectToBindingOrder` producer chain is CORRECT; it builds `[4 5 6 0 1 2 3]`
   for the reproducer. Do not go looking in `createplanjoin.go` again.
2. **Real cause:** `remapTopProjection` (`internal/planner/bushy.go:2204`) finds
   the join tree to derive its posMap from by walking DOWN past `*Project` /
   `*Sort` / `*Limit` / `*LockRows` wrappers — and the search boundary IS a
   `*Project`. It handed `buildBindingsPosMap` a node INSIDE the searched
   subtree (so `collect`'s `isSearchedTree` guard was never asked) and applied
   the search's binding→plan-position permutation to the boundary's own target
   list, which is the map, not a reference into it. Two permutations composed.
   Fix = one `isSearchedTree(root)` guard on that descent.
3. It is the **eighth** member of 08 §3's skip list and the first that neither
   rewrites nor renumbers a join tree — that is why the P5.9-b audit missed it.
   Generalised rule now in 08 §3: skip a searched subtree whenever a pass
   DESCENDS THROUGH a node kind the boundary can be.
4. The proposed `boundaryMap` strengthening was **deliberately not done** and is
   refuted in 09 §3.2 — producer-side check, innocent producer. Replaced by
   `assertSearchedBoundariesIntact` (`createplanroot.go`, tail of `Plan()`,
   flag-gated): a boundary target is a bare `ColumnRef` naming the very column
   it addresses, so a later permutation moves indices and leaves names behind.
5. In-process reproduction beats the SF1 cluster here: `Plan(stmt, cat)` with
   `pgShapedDP = true` set directly (package var) on a 2-table `catalog.NewInMemory`
   with `tbl.Stats` set. `select * from customer, orders where o_custkey =
   c_custkey and o_orderkey = 1` — FROM order must be customer-first or the
   winner is already binding order and the boundary elides its Project.
6. Blind spot (ledgered): the tripwire only checks a MATERIALISED boundary. An
   ELIDED boundary has no target list, so a pass renumbering an elided searched
   subtree's internal joins is still uncaught. Resume point in the ledger row.

Files: `internal/planner/bushy.go` (guard), `createplanroot.go`
(`assertSearchedBoundariesIntact` + `boundaryWalkChildren`), `planner.go`
(call site at `Plan()`'s tail), `internal/planner/joinsearchboundary_test.go` (new).
Docs: 09 §3.1 (attribution corrected) + new §3.2, 03 §10 amendment, 08 §3
amendment, docs/design/README.md, IMPLEMENTATION-TODO P5.9-c, 1 ledger row.

Gates run: UNITS (`RALPH_PRECOMMIT_SCOPE=units`) PASS; SPOT
(`scripts/tpch-spotcheck.sh`) PASS (Q12 rows=2, Q13 rows=35, 27.5 s);
`go test ./internal/planner/` PASS; new test verified to FAIL with the bushy.go
guard stashed; `make ralph-state-guard` (repaired progress marker, then OK);
pgbench smoke via the commit hook.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9-d** — result-digest mode for `cmd/tpch-runner` (per-row
hash in scan order + an order-independent digest for queries without a total
`ORDER BY`), diffed across the two arms. It must land BEFORE P5.9 is re-run.
P5.9-e (Q17) is re-measured only after -d, on top of this fix.

In-flight: none.
