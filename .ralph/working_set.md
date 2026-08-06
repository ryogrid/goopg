(idle — nothing in flight)

Last loop: **M0127-P6.2 COMPLETE — MultiHashJoin is deleted** (`4e08d4b7`,
75 files, +849/-4616). Second S7 removal.

Inventory was 42 non-test files, above 08 §4's ~34-arm/18-file estimate.
Gone: `multi_hash_join.go` (696 lines) + 8 MHJ-only test files, the node,
`PathMultiHash`, packer (`rewriteMultiWayChain`/`collectMultiHashTables`),
the whole posmap family (`mhjPosMapOf` was ALREADY a permanent `return nil`
— its 4 call sites were dead branches; `binaryTreePosMapOf` dead outright),
`estimateMultiHashJoin`, `multiHashJoinCost`, `generateMultiHashJoinPath`
(**never had a production caller** — settles 0126-0011 §3 as *deleted*),
both EXPLAIN arms, flag trio (`GOOPG_MHJ_PACKING_OFF` → retired row).

**Two inventory calls were wrong, both toward over-deletion** — the trap for
P6.3: `mhj_input_rewrite.go` is NOT an MHJ file (its first half is the
generic single-table-predicate→IndexScan promotion `planSelect` calls on
every query) — split + renamed `scan_input_rewrite.go`. And
`shouldAttachBeforeMHJ` is a live Slice-A gate → renamed
`shouldAttachLocalFiltersBeforeSearch`. **Grep the callers before deleting
any MHJ-NAMED symbol; the name lies.**

**The row-lock closure P6.2 was named for did NOT happen — premise measured
false.** With BOTH nodes deleted, `GOOPG_PGSHAPED_DP=0 go test -count=1 -run
'^TestPort_IsolationEvalPlanQual$' ./internal/testport/` still fails
byte-identically to a HEAD-baseline worktree run (`L1001 expected
" <waiting ...>" / actual ""`). So the three ledger rows (P6.1's,
`2026-08-06 M-NIGHTLY` root-0038 and AI-20260806-011323-001) are **NOT**
flipped to `resolved`. Real cause located: `markJoinPreserveCTID`
(`operators_lockrows.go:~404`) has only 4 arms (project/filter/sort/joinOp)
and the legacy plan for `lockwithvalues` reaches a node outside that set.
Default S5-ON PASSES the spec — production unaffected. Ledger row 2026-08-07
carries the resume point.

Gates: grep-clean, `go build`/`go vet ./...` clean, UNITS PASS, **SPOT PASS**
(Q12=2/Q13=35, 28.3 s vs P6.1's 28.8 s, peak 10,658 MB), **DS05 PASS**
(95 PASS / 0 MISMATCH / 0 CKMISMATCH / 0 ERROR / 0 TIMEOUT / 4 SKIP;
**PLAN-SHAPE 99/99 same, 0 changed**), SMOKE via hook (13,106 TPS),
`make ralph-state-guard` OK (auto-repaired the stale completed-marker).
Two guards caught real staleness and were repaired, not suppressed:
`TestExprSwitchInventoryIsPinned` + `TestFlagProvenanceEnvIsGenerated`
(regen via `go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env`).

**NEXT LOOP — M0127-P6.3 (delete the old subset-bitmask DP + layout/remap
family).** `enumerateBushyPlans`/`enumerateSubsets`/`enumerateSplits`/
`dp map[uint16]dpEntry`, `estimateJoinCost` + integer weights,
`attachUnusedCrossEdges`, `bushySeedRowCounts`, the 12-table cap,
`IsSmallDimensionSide` pinning, `chooseInnerJoinAlgo` (searched); demote
`joinorder.go` to over-limit sequencer. **HELD BACK per 08 §4:
`buildBindingsPosMap`/`applyJoinTreePosMap` stay** until the 03 §10 boundary
map is proven — 08 §4 calls this the S7 change most likely to regress.
Bar: grep-clean + UNITS + SPOT + DS05. Also freed by P6.3:
`SetPGShapedJoinSearch`, `costDrivenJoinOrder`/`SetCostDrivenJoinOrder`
(bushy.go:13 says they live until the old DP goes).

Housekeeping: two orphaned `TestPort_RegressSuite` servers from earlier loops
were live during this loop's gates (PIDs 3360059 / 3384704, ~4% CPU, ~380 MB
each, /tmp/TestPort_RegressSuite*/001/data) — low spin, did not affect row
counts. Not owned by this loop, so not killed. Never run SPOT while a
nightly's tpch/tpcds stages are live.

In-flight: none.
