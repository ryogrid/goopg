Last landed (loop #3 / this session): `stats` rung 8 — FINAL SLRU rung
(M0118-0009, design 0118-0132). `pg_stat_slru` notify `blks_zeroed` + `block_size`
preset GUC. First divergence advanced **L3072 → L3732** — the spec's LAST
permutation. Every SLRU permutation now matches PG 18.3 byte-for-byte.

Files:
- internal/executor/pgstat_slru.go — NEW: slruStatsManager (models notify SLRU
  blks_zeroed via modelled queue head + 8192B page-crossing count);
  RecordNotifyQueueWrite (exported, server calls at notify-commit); snapshotAll;
  fetchSLRURows(ctx) (snapshot-aware rows).
- internal/executor/pgstat_functions.go — funcStatSnapshot gains slruFrozen/
  slruCache; new ensureFullSnapshot freezes func+SLRU cross-kind (snapshot mode).
- internal/executor/operators.go — valuesOp.Open serves pg_stat_slru via
  fetchSLRURows(ctx).
- internal/server/notify.go — publishPendingNotify sums notifyEntryBytes, gated
  on hub.hasAnyListener(), calls executor.RecordNotifyQueueWrite.
- internal/catalog/catalog.go — static pg_stat_slru VirtualRows names fixed to
  PG-17+ (notify/commit_timestamp/.../other).
- internal/config/defaults.go — block_size=8192 PGC_INTERNAL preset.
- pgstat_slru_test.go (executor) + notify_slru_test.go (server) — new units.
- docs/design/0118-0132 + README row + fix_plan note + deferral ledger.

Gates run: go test ./internal/executor/ ./internal/server/ ./internal/config/
./internal/catalog/ PASS; new units + TestFetchFuncStatConsistency/TestStatsGUCs
PASS (-race); TestPort_IsolationStats soft probe L3072→L3732;
TestPort_IsolationAsyncNotify + TestPort_TwoPhaseCommitSameBackend PASS; build
clean. pgbench smoke = pre-commit hook (commit pending).

NEXT (promotes `stats` to pass — the LAST failed M0118-0009 spec): the ONE
remaining blocker at L3732 is NOT a stats subsystem — it is **isolation-runner
connection reuse**. Upstream isolationtester.c opens one connection per session
ONCE (in main) and reuses it for ALL permutations, so session GUCs leak forward;
the last permutation relies on `track_functions='all'` (set by an earlier
permutation) still being in effect, so `pg_stat_get_function_calls` reads 1.
goopg's IsolationRunner.runPermutation (internal/testport/framework/
isolation_runner.go ~L318/L344) opens fresh per-session sql.OpenDB connections
each permutation → track_functions resets to boot `none` → call untracked → NULL.
FIX: hoist per-session connection creation to spec scope (open once per spec,
reuse across permutations; run only each session's `setup` SQL per permutation,
NOT reconnect). Shared infra touching ~117 strict-passing isolation specs — run
the FULL TestPort_Isolation* strict suite after, watch for specs that
accidentally relied on per-permutation GUC reset. After that lands, flip
stats.spec CSV row defer→pass + regen coverage/inventory md.
