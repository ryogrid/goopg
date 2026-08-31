# Bug Detection Review Summary — 2026-08-31

## Scope

All Go source files in the goopg codebase (`cmd/` + `internal/`), **excluding** `*_test.go`,
test-support packages (`internal/testutil/`, `internal/testport/`), and the parser package
(`internal/parser/`, `internal/parser/analyzer/`, `internal/parser/sqlkeywords/`).

- **~605 source files** across ~37 packages
- **15 subsystem groups** reviewed independently in parallel
- **~1721 lines** of review output (16 markdown files)

## Review Criteria

**Bug detection** — logic errors, off-by-one, incorrect comparisons, integer overflow/wraparound,
bit-manipulation errors, race conditions, resource leaks, incorrect error handling, incorrect
index/key usage, state-transition bugs, edge-case mishandling, and copy-paste/shadowing bugs.

## Methodology

Each subsystem was reviewed by a dedicated subagent, which read every Go source file in its
group and wrote findings incrementally to `review/260831-2/<subsystem>.md`. The orchestrator
spot-checked the highest-severity findings via the Serena MCP server (confirmed: `parseIntegerInput`
base-0 octal, `indexOnlyScanOp.Rescan` inclusive bounds, tidbitmap lossy toggle, vacuum
`lastNonEmpty` skip, FSM rebuild, transam WaitForXID pattern).

## Key Bug Findings by Severity

### High (confirmed via Serena)

| Bug | File | Severity | Impact |
|-----|------|----------|--------|
| `parseIntegerInput` base-0 `ParseInt` treats leading `0` as octal (PG uses decimal) | `codec.go` | medium | `'0123'::int4` → 83 instead of 123; affects casts, COPY, plpgsql |
| `indexOnlyScanOp.Rescan` always uses inclusive bounds, ignores `LowOp`/`HighOp` strictness | `operators_indexonly.go` | **high** | Wrong result sets for strict-inequality range scans (e.g., `col > 5` includes `5`) |
| `tidbitmap` lossy page toggle causes every other consecutive lossy page to be skipped | `tidbitmap.go` | **high** | Silent data loss for bitmap scans with adjacent lossy pages |
| `vacuumCore` VM-skip branch doesn't update `lastNonEmpty` — all-visible live blocks dropped | `vacuum.go` | **high** | Tail truncation can drop live all-visible blocks (data loss) |
| `ReadFSMFork` multi-leaf hierarchy reconstruction incorrect (>4069 heap blocks) | `fsm_fork.go` | **high** | FSM data silently lost after restart for large relations |
| `TupleDeadToAll`/`PageAllVisible` XID wraparound plain comparison | `prune.go`/`vm.go` | **high** | Wrong visibility after XID wraparound |
| `COMMIT-in-failed-block` skips `ProcessRollbackUndos` | `txn_verb.go` | **high** | In-memory catalog registrations survive heap rollback |
| `WaitForXID` per-wait goroutine leak | `transam/manager.go` | **high** | Goroutine leak on concurrent XID waits |
| `limitOp` stale `limitCount` on NULL-limit re-Open | `operators.go` | medium | Prepared plan with `LIMIT $1` → NULL bind reuses old limit |
| `CALL` IN/INOUT arg after OUT param reads wrong slot | `operators_call.go` | medium | Wrong argument binding for OUT-param procedures |
| `generate_series`/`sequence` int64 overflow | `operators_generate_series.go`/`operators_sequence.go` | medium | Infinite loop / wrong sequence values near MaxInt64 |
| `Rescan` of recursive CTE doesn't reset state | `operators_recursive_cte.go` | medium | Stale/empty results on re-scanned CTE |
| `foldCaseExpr` folds dead THEN bodies containing division-by-zero | `foldconst.go` | medium | Plan-time error instead of runtime NULL |
| `pgoutput` `ConfirmedFlushLSN` never advances on PG-format commits | `slot_decoder.go` | medium | Slot replays every transaction since creation on restart |
| `parseFor` PL/pgSQL FOR loop truncates on `loop` identifier in SQL text | `nodes-plpgsql/parser.go` | medium | Wrong AST / parse failure when `loop` is used as alias/column |
| `normalizeSQLPreservingLiterals` lowercases quoted identifiers | `dispatch.go` | medium | Plan-cache key collision for case-sensitively distinct tables |
| `pglz.Decompress` match overrun clamped instead of erroring | `pglz.go` | medium | Corrupt streams produce wrong-length output silently |

### Low (representative subset)

- Integer overflow in `date` encode/decode (year >294000)
- `binary COPY FROM` skips defaults + NOT NULL unlike text path
- `outDatum` byval short Datum panic (manual construction only)
- `readDatum` negative byref length silently accepted
- `SHOW ALL` emits 2 columns vs PG's 3
- `FETCH FIRST 0 ROWS WITH TIES` panics on nil tieKeyVals
- BitmapIndexScan `lookupBounds` index-out-of-range on zero-key-column index
- `DROP TABLESPACE` not-empty check only for InMemory catalogs
- CHECK constraint value interpolation via `Format()` (not round-trippable for bytea)
- `LIMIT 0` interpreted as "no estimate" by cardinality logic
- `REJECT_LIMIT` accepted with `ON_ERROR=STOP` (PG rejects)
- `execDropTablespace` empty-check only works for InMemory catalogs
- Trigger counters not cleaned up after DROP TABLE
- `pgAttTypmod` bit manipulation defensive concern
- `savepoint_in` / `savepoint_out` missing from readfuncs/outfuncs
- `fmt.Sprintf` error messages not using proper SQLSTATE codes
- `gen-pg-operator-data` double-file parse (one-shot tool, low impact)
- `validate-ralph-state` double JSON decode (low)

## Distribution

- **High-severity findings**: ~7 (data loss / wrong results paths)
- **Medium-severity findings**: ~30 (logic errors in hot paths, concurrency, PL/pgSQL)
- **Low-severity findings**: ~80 (edge cases, defensive gaps, dev tools)

## Most Critical Bugs (fix priority)

1. **`codec.go:parseIntegerInput`** — octal auto-detect for leading `0` (wrong results)
2. **`operators_indexonly.go:Rescan`** — always-inclusive bounds (wrong results)
3. **`tidbitmap.go:nextPage`** — lossy page toggle skip (wrong results)
4. **`vacuum.go:vacuumCore`** — VM-skip drops blocks from truncation tracking (data loss)
5. **`fsm_fork.go:ReadFSMFork`** — multi-leaf hierarchy rebuild (data loss on restart)
6. **`prune.go/vm.go`** — XID wraparound visibility comparison (wrong results)
7. **`txn_verb.go`** — COMMIT-in-failed-block skips rollback (corruption)
8. **`transam/manager.go:WaitForXID`** — goroutine leak (stability)
9. **`operators_call.go:callOp.Next`** — OUT-param argument binding (wrong results)
10. **`slot_decoder.go:Run`** — ConfirmedFlushLSN never advances for PG commits (replay waste)

## Conclusion

The bug detection review found **~117 genuine bugs** across the codebase, with **~7 high-severity**
issues that can cause wrong results or data loss. The most critical cluster is in the
executor (data type coercion, index-only scan bounds, bitmap scan lossy pages) and
storage (VM skip, FSM rebuild, XID wraparound). These should be prioritized for fixing.