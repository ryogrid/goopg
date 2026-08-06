(idle — nothing in flight)

Last loop: **M0127-P6.1 COMPLETE — runtime hash-join fusion is deleted.**
First S7 removal; both kill switches defaulted OFF, so it is
behaviour-preserving by construction.

Deleted: `internal/executor/fused_hash_join.go` + `fused_hash_join_test.go`
whole; both `*planner.Join` hook sites (`Build` and `buildRec`);
`GOOPG_RUNTIME_JOIN_FUSION{,_MIN_LEVELS}`; `planner.IsCanonicalKeyEquality`
(unexported `isCanonicalKeyEquality` survives — two prose references
re-pointed at it).

The inventory's one wrong call (`analysis/m0127-p61-fusion-inventory.md` said
"keep `inWorker`, it has other readers"): it has NONE. Every `buildEnv` field
— `root`/`inWorker`/`fusionCfg`/`q0` — had exactly one consumer,
`tryFuseHashCascade`. So the struct went too: `buildWithEnv(plan,inWorker)` →
`buildNode(plan)`, `BuildWorker` now builds byte-for-byte what `Build` builds
(kept as the named worker seam for `gatherOp` + `join_worker_path_test.go`,
whose header carries a P6.1 stamp), and `opTreeSlab.env` went on the schedule
its own comment set.

Closes the FUSED half of the `markJoinPreserveCTID` `FOR UPDATE` row-lock gap
by construction. The `multiHashJoinOp` half stays open until P6.2, so ledger
rows `2026-08-06 M-NIGHTLY (root-0038)` and `(AI-20260806-011323-001)` were
NOT flipped to `resolved`; new row `2026-08-07 M0127-P6.1` records the partial
closure and names P6.2 as the closure event.

Files: `internal/executor/{executor.go,opnode.go,join_worker_path_test.go}`,
`internal/planner/{bushy.go,join_hash_keys.go,join_hash_keys_test.go}`,
`.ralph/{fix_plan.md,deferral_ledger.md}`,
`docs/design/0127-pg-shaped-join-search.md` §6,
`docs/design/leftdeep-joins/IMPLEMENTATION-TODO.md`, `docs/design/README.md`.

Gates run: grep-clean (only historical prose survives), `go build ./...` +
`go vet` clean, UNITS PASS, **SPOT PASS (Q12=2 / Q13=35, 28.8 s query phase,
peak 10,946 MB)**, `make ralph-state-guard` OK (auto-repaired the stale
completed-marker), SMOKE via the commit hook. No DS05 — the deleted operator
was default-OFF and unreachable in every recorded gate run.

**NEXT LOOP — M0127-P6.2 (delete MultiHashJoin).** Much bigger than P6.1:
take a FRESH grep inventory first (2026-08-02 count was ~34 arms / 18 files —
node, packer `rewriteMultiWayChain`/`collectMultiHashTables`,
`mhj_input_rewrite.go`, posmaps, cost/cardinality arms,
`internal/executor/multi_hash_join.go` (696 lines), EXPLAIN arms,
`generateMultiHashJoinPath`, flags `mhjPackingEnabled`/`GOOPG_MHJ_PACKING_OFF`).
Bar is stricter: grep-clean + UNITS + SPOT + **DS05**. When it lands, flip both
`2026-08-06 M-NIGHTLY` ledger rows to `resolved` and confirm
`GOOPG_PGSHAPED_DP=0 go test -run '^TestPort_IsolationEvalPlanQual$'
./internal/testport/` has become unreachable. Never run SPOT while a nightly's
tpch/tpcds stages are live.

In-flight: none.
