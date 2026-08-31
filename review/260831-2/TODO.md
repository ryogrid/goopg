# Bug Detection Review TODO — 2026-08-31

Review scope: ALL Go source in the goopg codebase (cmd/ + internal/), **excluding** `*_test.go`,
test-support packages (`internal/testutil/`, `internal/testport/`), and the parser package
(`internal/parser/`, `internal/parser/analyzer/`, `internal/parser/sqlkeywords/`).

Review focus: **Bug detection** — logic errors, off-by-one, incorrect comparisons, integer
overflow, bit-manipulation errors, race conditions, resource leaks, incorrect error handling,
incorrect index/key usage, state-transition bugs, edge-case mishandling, and copy-paste/shadowing bugs.

## Subsystem groups — all COMPLETE

| # | Group | Files | Output file | Bug findings | Status |
|---|-------|-------|-------------|-------------|--------|
| 1 | executor: core (expr/operator/datum/slot/codec/join/instrument/etc.) | 36 | `executor-core.md` | 8 | [x] |
| 2 | executor: operators_*.go (analyze → generate_series) | 24 | `executor-operators-1.md` | 10 | [x] |
| 3 | executor: operators_*.go (index → window) + misc | 36 | `executor-operators-2.md` | 8 | [x] |
| 4 | executor: sys_pg_*, sys_catalog_*, pgindex_*, pgstat_*, parallel_*, pg_* | 82 | `executor-sys.md` | 12 | [x] |
| 5 | optimizer (part 1: cardinality → exprwalk) | 20 | `optimizer-1.md` | 6 | [x] |
| 6 | optimizer (part 2: flaglabels → with) | 70 | `optimizer-2.md` | 1 | [x] |
| 7 | access/transam/xlog | 61 | `xlog.md` | 10 | [x] |
| 8 | initdb | 57 | `initdb.md` | 8 | [x] |
| 9 | storage + aio + lmgr + file | 35 | `storage.md` | 10 | [x] |
| 10 | nbtree + amcheck + pglz + backup + commands/vacuum | 34 | `nbtree-amcheck.md` | 6 | [x] |
| 11 | transam (clog/mvcc/snapshot/visibility/subxact/ssi) + multixact + control | 21 | `transam.md` | 6 | [x] |
| 12 | nodes + plpgsql (parser excluded) | 15 | `nodes-plpgsql.md` | 6 | [x] |
| 13 | catalog + postmaster + autovacuum + replication + libpq + auth + port | 61 | `catalog-postmaster.md` | 12 | [x] |
| 14 | utils (misc, adt/datetime, mb, activity, errcodes, mmgr, similarto, array) | 28 | `utils.md` | 7 | [x] |
| 15 | cmd/ (all 25) | 25 | `cmd.md` | 6 | [x] |

**Totals: ~605 source files reviewed, ~117 bug findings (7 high, ~30 medium, ~80 low).**

## High-severity findings (fix priority)

1. `codec.go:parseIntegerInput` — octal auto-detect for leading `0` (wrong results)
2. `operators_indexonly.go:Rescan` — always-inclusive bounds (wrong results)
3. `tidbitmap.go:nextPage` — lossy page toggle skips every other lossy page (wrong results)
4. `vacuum.go:vacuumCore` — VM-skip branch doesn't update `lastNonEmpty` (data loss)
5. `fsm_fork.go:ReadFSMFork` — multi-leaf hierarchy reconstruction (data loss on restart)
6. `prune.go/vm.go` — XID wraparound visibility comparison (wrong results)
7. `txn_verb.go` — COMMIT-in-failed-block skips ProcessRollbackUndos (catalog corruption)
8. `transam/manager.go:WaitForXID` — goroutine leak on concurrent XID waits

## Notes / rule-out

- **Parser package excluded** by user request.
- **Test-support packages excluded** by user request: `internal/testutil/`, `internal/testport/`.
- **Spot-checked via Serena**: `parseIntegerInput` base-0 octal, `indexOnlyScanOp.Rescan`
  inclusive bounds, tidbitmap lossy toggle, vacuum `lastNonEmpty`, FSM rebuild, transam wait
  — all confirmed as genuine bugs.
- `executor-sys.md` and `catalog-postmaster.md` include per-file "no bugs found" notes
  (the count above excludes these "no issue" entries).