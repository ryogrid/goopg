(idle — nothing in flight)

M0132-S1 landed: the acceptance bar for explicit transactions on the extended
query protocol. Committed and pushed. **Next per banner: M0132-S2 — and S2 lands
ONLY together with S3+S4+S5 in one commit (non-negotiable land-together rule).**

Carry-forward #1 — **how to commit a red test.** One const,
`m0132ExtendedBlocksLanded = false`, in
`internal/server/extended_txn_block_test.go`. Every bar runs its scenario
unconditionally, then asserts either the PG outcome (const true) or today's
divergence (const false). Green at HEAD, 6 of 8 red when flipped (the two
no-divergence guards stay green). The fix cannot land without flipping the const
— the divergence arms fail the moment the divergence disappears — and the const
cannot be flipped without the fix. Reuse this shape for any "test must be red
first" slice; do NOT relax a failing divergence arm, flip the const.

Carry-forward #2 — **the characterisation slice found a bug the milestone had
not attributed to any slice, on the path it was not looking at.** `connTx.Fail()`
has exactly two call sites (`dispatch.go:950`, `:1019`), both on the
executor-error path, so on the SIMPLE path a PLAN-time error (`BEGIN; INSERT INTO
<missing table>;`) leaves the block live and healthy — next statement runs,
status reverts to `T`. The `E` a client sees right after the error comes from
`wireStatus`'s `afterError` argument, not persisted state, which is exactly why
it hides. Second gap: a constant `SELECT 1` bypasses the 25P02 gate that
correctly rejects `SELECT * FROM items`. Both filed as ledger rows and pinned by
tests; **S5 must close both** or it copies the same placement onto the extended
path. Probing (throwaway `zz_probe_test.go`, deleted) is what found them — the
first draft of the aborted-block bar passed for the wrong reason.

Corrections (a)/(b)/(c) all confirmed by measurement, recorded in
`docs/design/0132-0001-…` §7: connTx already threaded (`dispatch_extended.go:30`);
no `execCommit` route (`dispatch.go:2803-2807`, deferred sequence inline at
`:2818-2828`); `Sync` already correct and now guarded by a test that must pass
before AND after the fix.

Files: `internal/server/extended_txn_block_test.go` (new, test-only),
`docs/design/0132-0001-extended-protocol-explicit-txn-state-machine.md` (§7),
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

Gates: `go build ./...` + `go vet ./internal/server/` clean; `go test
./internal/server/` PASS (38 s); both const arms exercised and recorded;
`RALPH_PRECOMMIT_SCOPE=units` PASS; pgbench smoke PASS via the commit hook.
No executor/planner code changed, so tpch-spotcheck was not required.

In-flight: none.
