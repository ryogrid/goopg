(idle — nothing in flight)

Last loop: **M0127-P6.3 COMPLETE — the old subset-bitmask DP is deleted**
(30 files, +~600/-~3900; `bushy.go` 2880 lines gone whole). Third S7
removal. The PG-shaped search is now the ONLY join-order search.

Gone: `enumerateBushyPlans`/`enumerateSubsets`/`enumerateSplits`, the
`dp map[uint16]dpEntry`, `estimateJoinCost` + integer weights,
`attachUnusedCrossEdges`, `bushySeedRowCounts`, the 12-table cap,
`buildJoinFromDP`, the per-subset layout/remap family, `joinGraph`/
`joinEdge`, the graph-space key-proof sibling in `joinkeyproof.go`, and
both flags (`costDrivenJoinOrder`/`SetCostDrivenJoinOrder` — default-OFF
since P5.9, so behaviour-preserving by construction; `SetPGShapedJoinSearch`
— `GOOPG_PGSHAPED_DP=0` now means NO search, the syntactic order).
`tryBushyDP` → `tryJoinSearch`; `joinorder.go` demoted to over-limit
sequencer. Six old-DP-only test files deleted; three truncated at
tombstones naming the surviving coverage.

**Survivors split out, not deleted** — the P6.2 trap ("the name lies")
applied again: new `joinlayout.go` (1348 lines) holds the HELD-BACK
`buildBindingsPosMap`/`applyJoinTreePosMap` (08 §4: until the 03 §10
boundary map is proven), `reconcileNLILayout` (production call site died
with the flag; STAYS per 08 §3 as `assertSearchedTreeNeedsNoReconcile`'s
oracle — ledgered), and the remap walkers. `joinrestrict.go` took three
coordinate/edge helpers with live consumers. `shouldAttachLocalFilters-
BeforeSearch`/`attachRelationLocalFilters` had NO caller but bushy.go →
deleted. NLI INNER stats-aware fan-out test died with the flag — its Q5
hazard (730k→~290M) is real; resurrection is a cost-model decision
(ledgered).

Gates: grep-clean, `go build`/`go vet` clean, UNITS PASS, SPOT PASS
(Q12=2/Q13=35, 29.4 s, peak 12,115 MB), **DS05 PASS**
(`sweep-20260807-122645`: 95/0/0/0/0/4, `verdict-changes=none
runtime-moves=0`, **PLAN-SHAPE 99/99 same, 0 changed**),
`make ralph-state-guard` OK. `TestExprSwitchInventoryIsPinned` caught the
file split → re-keyed in-commit (`bushy.go`→`joinlayout.go` ×4), not
suppressed.

**NEXT LOOP — M0127-P6.4 (supersession stamps + ledger rows), the LAST
M0127 task.** 0034-0001, 0038-0001, cost-model/09 §3 MHJ allowance,
0043/0063/0125/0126 MHJ chapters get `superseded by: leftdeep-joins/`
headers (never deleted); README index status flips; ledger rows for every
deliberately-skipped PG behaviour (GEQO, skew buckets, SpecialJoinInfo
in-DP — `join_is_legal`-inference-dependent marker —, shared spilling
builds, full join_order_restriction inference). IMPLEMENTATION-TODO P6.4;
08 §5. Bar: doc review. Still open after P6.4, NOT M0127 scope: the 03 §10
boundary-map proof (frees `buildBindingsPosMap`/`applyJoinTreePosMap`) and
the reconcileNLILayout retirement test (08 §3) — both ledgered.

In-flight: none.
