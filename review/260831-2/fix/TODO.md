# Bug-Review Fix Tracker — review/260831-2

Source: the 16 subsystem review reports under `review/260831-2/`.
This file is the **single source of truth** for (a) whether each reported finding
is a real bug and (b) the fix progress.

## How to read the columns

| column | meaning |
|---|---|
| `id` | stable task id (subsystem prefix + number) |
| `sev` | severity as claimed by the review report |
| `finding` | file:symbol + one-line summary |
| `verdict` | `?` = not yet verified / `BUG` = confirmed real bug / `NOTBUG` = report is wrong or already handled / `WONTFIX` = real divergence but deliberately out of scope (must carry a deferral-ledger row) |
| `status` | `[ ]` todo / `[~]` in progress / `[x]` fixed+committed / `[-]` closed without code change |
| `commit` | commit hash of the fix |

Verification rule: every `BUG` verdict must be backed by either a PG 18.3 oracle
observation (`scripts/pg-oracle-diff.sh`) or a reading of the actual goopg code
path that shows the defect — never by the report's claim alone.

Gate rule (CLAUDE.md): commit per fix, never `--no-verify`; executor/planner/codec
changes additionally need `scripts/tpch-spotcheck.sh`.

---

## A. High severity (claimed)

| id | sev | finding | verdict | status | commit |
|---|---|---|---|---|---|
| EO2-1 | high | `executor/operators_indexonly.go:Rescan` — LowOp/HighOp strictness ignored, bounds always inclusive | ? | [ ] | |
| ES-5 | high | `executor/tidbitmap.go:tbmIterator.next/nextPage` — lossy page dropped after exact-page interleaving | ? | [ ] | |
| NB-1 | high | `access/heap/vacuum.go:vacuumCore` — VM-skip branch does not update `lastNonEmpty` (tail truncation drops live blocks) | ? | [ ] | |
| ST-1 | high | `storage/fsm_fork.go:ReadFSMFork` — FSM level reconstruction wrong for >1 leaf page | ? | [ ] | |
| ST-2 | high | `storage/prune.go:TupleDeadToAll` — plain XID comparison wrong under wraparound | ? | [ ] | |
| TA-1 | high | `transam/visibility.go:TupleVisible` — HeapXmaxCommitted hint-bit branch returns false unconditionally | ? | [ ] | |
| CP-1 | high | `postmaster/txn_verb.go:applyTransactionVerb` — DDL in a failed block survives COMMIT-as-ROLLBACK | ? | [ ] | |
| CM-1 | high | `cmd/goopg/standby.go:Promote` — `promoting` atomic never reset; failed promote wedges permanently | ? | [ ] | |

## B. Medium severity (claimed)

| id | sev | finding | verdict | status | commit |
|---|---|---|---|---|---|
| EC-1 | med | `executor/codec.go:parseIntegerInput` — base-0 ParseInt treats leading `0` as octal | ? | [ ] | |
| EC-2 | med | `executor/btree_array_key.go:encodeArrayBTreeKey` — quoted `"NULL"` element encoded as SQL NULL | ? | [ ] | |
| EC-4 | med | `executor/copy.go:PushBinaryData` — binary COPY FROM skips defaults / NOT NULL / CHECK | ? | [ ] | |
| EO1-1 | med | `executor/operators_call.go:callOp.Next` — IN/INOUT arg after an OUT param reads the wrong slot | ? | [ ] | |
| EO1-3 | med | `executor/operators.go:limitOp.Next` — `LIMIT 0 ... WITH TIES` panics on nil tieKeyVals | ? | [ ] | |
| EO2-2 | med | `executor/operators_sequence.go:seqState.nextVal` — int64 overflow wraps instead of raising 2200H | ? | [ ] | |
| EO2-3 | med | `executor/operators_project_set.go:openSelectSrfMode` — generate_series int64 overflow spins forever | ? | [ ] | |
| EO2-4 | med | `executor/operators_recursive_cte.go:recursiveUnionOp.Open` — phase state not reset on re-open | ? | [ ] | |
| EO2-5 | med | `executor/opnode.go:limitOpNext` — `FETCH FIRST 0 ROWS WITH TIES` panics on nil tieKeyVals | ? | [ ] | |
| ES-6 | med | `executor/plpgsql_runtime.go:executePLpgSQLStmt (ForStmt)` — BY step not validated; zero/negative step infinite-loops | ? | [ ] | |
| ES-7 | med | `executor/plpgsql_runtime.go:lowerPLpgSQLExpr (CastExpr)` — cast dropped in PL/pgSQL expressions | ? | [ ] | |
| IN-2 | med | `initdb/catalog_cache.go:readCatalogCache` — silent partial catalog on TryRegisterUserTable failure | ? | [ ] | |
| ST-3 | med | `storage/bufpool.go:pinLoad/evictVictim` — dirty victim content discarded when the flush fails | ? | [ ] | |
| ST-7 | med | `storage/vm.go:PageAllVisible/PageAllFrozen` — plain XID comparison breaks under wraparound | ? | [ ] | |
| ST-8 | med | `storage/freeze.go:PageFreezeOldTuples` — plain XID comparison breaks under wraparound | ? | [ ] | |
| NB-2 | med | `pglz/pglz.go:Decompress` — match length clamped instead of erroring on corrupt streams | ? | [ ] | |
| TA-2 | med | `transam/manager.go:AssignXID` — non-atomic read-check-allocate-store allows XID leak/double-assign | ? | [ ] | |
| NP-1 | med | `plpgsql/parser.go:parseFor` — FOR-query scan truncates on a `loop` identifier inside the SQL text | ? | [ ] | |
| NP-5 | med | `plpgsql/parser.go:parseFor` — `isQueryFor` peeks only the first token; parenthesized bound misroutes | ? | [ ] | |
| OP1-2 | med | `optimizer/exprwalk.go:exprChildSlots` — FuncCall child slots omit Filter/Over/OrderBy/WithinGroup/Variadic | ? | [ ] | |
| OP1-3 | med | `optimizer/createplannl.go:createNestLoopBitmapJoinPlan` — `bhs.BitmapQual = nil` drops recheck quals | ? | [ ] | |
| XL-4 | med | `wal/slot_decoder.go:Run` — ConfirmedFlushLSN never advances for PG-format commit records | ? | [ ] | |
| CP-2 | med | `postmaster/dispatch.go:normalizeSQLPreservingLiterals` — plan-cache key collision on quoted identifiers | ? | [ ] | |
| UT-1 | med | `utils/activity/registry.go:coldFromBackend` — `BackendStart` never copied; pg_stat_activity.backend_start empty | ? | [ ] | |
| UT-2 | med | `utils/misc/guc.go:canonicalizeFrom` (TypeReal) — unit suffix / scientific notation mis-parsed | ? | [ ] | |
| UT-4 | med | `utils/mmgr/mctx.go:Context.Release` — mutates `c.children` while ranging over it | ? | [ ] | |
| CM-3 | med | `cmd/plan-snapshot/main.go:planEqual` — `rowsRegexp` never matches the real EXPLAIN format | ? | [ ] | |

## C. Low severity (claimed)

| id | sev | finding | verdict | status | commit |
|---|---|---|---|---|---|
| EC-3 | low | `executor/btree_array_key.go` — multidimensional guard false-positives on a quoted `{` | ? | [ ] | |
| EC-5 | low | `executor/codec.go:decodePhysicalPGValueLowered` — date/timestamp int64 overflow at PG range extremes | ? | [ ] | |
| EC-6 | low | `executor/copy_binary.go:datumToCopyBinary` — int4 arm missing range check | ? | [ ] | |
| EC-7 | low | `executor/copy_binary.go:copyBinaryToDatum` — infinity sentinels not handled | ? | [ ] | |
| EC-8 | low | `executor/codec_aclitem.go:aclModeFromPrivLetters` — shift-after-guard pattern (report says not a live bug) | ? | [ ] | |
| EO1-2 | low | `executor/operators.go:limitOp.Open` — stale limitCount survives a NULL-limit re-Open | ? | [ ] | |
| EO1-4 | low | `executor/operators_bitmap.go:lookupBounds` — index out of range with a zero-key-column index | ? | [ ] | |
| EO1-5 | low | `executor/operators_generate_series.go:generateSeriesOp.Next` — int64 overflow near MaxInt64 | ? | [ ] | |
| EO1-6 | low | `executor/operators_ddl.go:execDropTablespace` — not-empty check only inspects InMemory catalog | ? | [ ] | |
| EO1-7 | low | `executor/operators_ddl.go:execAlterCollation/Conversion` — rename collision reported as 42704 | ? | [ ] | |
| EO1-8 | low | `executor/operators_ddl.go:execCreateTable` — fallback column path drops serial's implicit NOT NULL | ? | [ ] | |
| EO1-9 | low | `executor/operators_fk.go:checkConstraints` — CHECK VALUES built from `Format()` text | ? | [ ] | |
| EO1-10 | low | `executor/operators_from_unnest.go` and sibling SRFs — `SlotFromRow(nil, …)` nil schema | ? | [ ] | |
| EO2-6 | low | `executor/operators_pg_options_to_table.go:Open` — lateral binding via ctx.OuterRows, no BindLateralOuter | ? | [ ] | |
| EO2-7 | low | `executor/operators_generated.go:evalGenExpr` — ColumnRef bounds check after EqualFold | ? | [ ] | |
| EO2-8 | low | `executor/operators_utility_settings.go:nextShow` — `SHOW ALL` emits 2 columns vs PG's 3 | ? | [ ] | |
| ES-1 | low | `executor/parallel_agg_combine.go:combineNumericSum` — Int-lane contribution dropped when lanes disagree | ? | [ ] | |
| ES-2 | low | `executor/parallel_agg_split.go:aggPartialAccum.merge` — silent break on state-count mismatch | ? | [ ] | |
| ES-3 | low | `executor/pgstat_relations.go:dropTable` — trigger counters not cleaned up | ? | [ ] | |
| ES-4 | low | `executor/pg18_user_catalog_rows.go:pgAttTypmod` — numeric typmod bit manipulation | ? | [ ] | |
| ES-8 | low | `executor/reg_identifier.go:regIdentifierInput` — schema qualifier dropped for user types | ? | [ ] | |
| IN-1 | low | `initdb/pgcontrol.go:BackupControlImage` — uint64 underflow on redoLSN == 0 | ? | [ ] | |
| IN-3 | low | `initdb/xact_recovery.go:replayCLogFromWAL` — latent native-record collision hazard | ? | [ ] | |
| IN-4 | low | `initdb/initdb.go:mappedLocalCatalogPlaceholderOIDs` — duplicate OID 3764 | ? | [ ] | |
| IN-5 | low | `initdb/open.go:Open` (RunningXactsFn) — latent uint32 underflow if Xmax == 0 | ? | [ ] | |
| IN-6 | low | `initdb/information_schema_tables.go` — CRLF / non-`\N` NULL handling | ? | [ ] | |
| ST-4 | low | `storage/bufpool.go:WriteDirtyPages` — bgwriter scan cursor never advances | ? | [ ] | |
| ST-5 | low | `storage/bufmap.go:Lookup` — probe bound off by one | ? | [ ] | |
| ST-6 | low | `storage/bgwriter.go:Stop` — double-close panic / hang on Stop-without-Start | ? | [ ] | |
| ST-9 | low | `aio/method_iouring_linux.go:pokeWake` — NOP written without checking SQ ring fullness | ? | [ ] | |
| ST-10 | low | `storage/writeback.go:accountWrite` — `pendingBlocks.Store(0)` races a concurrent `Add` | ? | [ ] | |
| NB-3 | low | `access/nbtree/btree_vacuum.go:readInternalFirstChildBlock` — wrong downlink block (dead code) | ? | [ ] | |
| NB-4 | low | `access/nbtree/btree.go:descendToLeaf` — wrong sentinel disables the rightmost-leaf cache | ? | [ ] | |
| NB-5 | low | `access/nbtree/btree.go:tryInsertOnCachedRightmost` — no deleted/half-dead page check | ? | [ ] | |
| TA-3 | low | `transam/manager.go:Begin` — auto-assign path skips isolation-level validation | ? | [ ] | |
| TA-4 | low | `transam/manager.go:AcquireConnSlot` — int32 cursor overflow → negative modulo | ? | [ ] | |
| TA-5 | low | `transam/clog.go:GetStatus` — nil `pool` dereference vs the nil-safe contract elsewhere | ? | [ ] | |
| TA-6 | low | `multixact/multixact.go:StatusesConflict` — invalid Status indexes out of bounds | ? | [ ] | |
| NP-2 | low | `plpgsql/parser.go:parseSQLStmt` — `SELECT INTO x FROM t` yields a malformed query | ? | [ ] | |
| NP-3 | low | `nodes/outfuncs.go:outDatum` — by-value Const with short/nil Datum panics | ? | [ ] | |
| NP-4 | low | `nodes/readfuncs.go:readDatum` — negative/zero by-reference length silently accepted | ? | [ ] | |
| NP-6 | low | `plpgsql/parser.go:parseStmt` — `<<label>>` / `label: LOOP` forms mis-parse | ? | [ ] | |
| OP1-1 | low | `optimizer/copy.go:validateCopyOptions` — REJECT_LIMIT accepted with ON_ERROR=STOP | ? | [ ] | |
| OP1-4 | low | `optimizer/cardinality.go:indexScanRows` — unique-index shortcut ignores range bounds | ? | [ ] | |
| OP1-5 | low | `optimizer/costbitmap.go:computeBitmapPagesLooped` — lossy-page adjustment computed then discarded | ? | [ ] | |
| OP1-6 | low | `optimizer/cardinality.go:EstimateRows` — `LIMIT 0` returns 0 read as "no estimate" | ? | [ ] | |
| OP2-1 | low | `optimizer/foldconst.go:foldCaseExpr` — dead THEN under a NULL simple-CASE operand is folded | ? | [ ] | |
| XL-1 | low | `wal/append_xlog_payload.go:appendXLogPayload` — error from emitWithPageHeaders swallowed | ? | [ ] | |
| XL-2 | low | `wal/insert_pos.go:reserveLocked` — `onCrossSegment` padded return discarded | ? | [ ] | |
| XL-3 | low | `wal/pgoutput.go:encodePgoTuplePhysical` — column-offset walk mis-advances on null columns | ? | [ ] | |
| CP-3 | low | `postmaster/autovacuum/launcher.go:freezeCutoff` — dead signedness check / unsigned wrap reliance | ? | [ ] | |
| CP-4 | low | `replication/logicalwalsender.go:walsenderPgoutputAdapter.Write` — LSN underflow on empty write | ? | [ ] | |
| CP-5 | low | `postmaster/server.go:isReplicationStartupParam` — overly broad match | ? | [ ] | |
| UT-3 | low | `utils/misc/guc.go:convertUnit` — int64 overflow on cross-unit conversion | ? | [ ] | |
| UT-5 | low | `utils/activity/stats/counter.go:Add` — index out of range when GOMAXPROCS > 256 | ? | [ ] | |
| UT-6 | low | `utils/adt/datetime/normalize.go:padTimeFields` — run-together time expansion not implemented | ? | [ ] | |
| UT-7 | low | `utils/misc/session.go:EndTransaction` — rollback fires `invokeOnChange` for unchanged values | ? | [ ] | |
| CM-2 | low | `cmd/goopg/standby.go:Promote` — `sc.replayer.ApplyLSN()` without a nil guard | ? | [ ] | |
| CM-4 | low | `cmd/gen-*-coverage/main.go:loadCSV` — `row[statusIdx]` out of bounds on malformed CSV | ? | [ ] | |
| CM-5 | low | `cmd/estimate-audit/main.go:selectQueries` — `Atoi` errors ignored, negative query numbers pass | ? | [ ] | |
| CM-6 | low | `cmd/gen-pg-operator-data/main.go:parseOperatorDat` — right-unary operator kind `'r'` unhandled | ? | [ ] | |

---

## Progress log

(append one line per verified/fixed item)
