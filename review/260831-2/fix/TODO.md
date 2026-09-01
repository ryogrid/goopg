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
| EO2-1 | high | `executor/operators_indexonly.go:Rescan` — LowOp/HighOp strictness ignored, bounds always inclusive | BUG | [x] | `5360b7e79` |
| ES-5 | high | `executor/tidbitmap.go:tbmIterator.next/nextPage` — lossy page dropped after exact-page interleaving | BUG | [x] | 3767bb7f4 |
| NB-1 | high | `access/heap/vacuum.go:vacuumCore` — VM-skip branch does not update `lastNonEmpty` (tail truncation drops live blocks) | BUG | [x] | `dafe96692` |
| ST-1 | high | `storage/fsm_fork.go:ReadFSMFork` — FSM level reconstruction wrong for >1 leaf page | BUG | [x] | `e4b3b7ff3` |
| ST-2 | high | `storage/prune.go:TupleDeadToAll` — plain XID comparison wrong under wraparound | BUG | [x] | `8ca6a5a8d` |
| TA-1 | high | `transam/visibility.go:TupleVisible` — HeapXmaxCommitted hint-bit branch returns false unconditionally | BUG | [x] | `98a9a11a6` |
| CP-1 | high | `postmaster/txn_verb.go:applyTransactionVerb` — DDL in a failed block survives COMMIT-as-ROLLBACK | BUG | [x] | `574dd707b` |
| CM-1 | high | `cmd/goopg/standby.go:Promote` — `promoting` atomic never reset; failed promote wedges permanently | BUG | [x] | 2723c742a |

## B. Medium severity (claimed)

| id | sev | finding | verdict | status | commit |
|---|---|---|---|---|---|
| EC-1 | med | `executor/codec.go:parseIntegerInput` — base-0 ParseInt treats leading `0` as octal | BUG | [x] | ed634f2c9 |
| EC-2 | med | `executor/btree_array_key.go:encodeArrayBTreeKey` — quoted `"NULL"` element encoded as SQL NULL | BUG | [x] | 9000565e7 |
| EC-4 | med | `executor/copy.go:PushBinaryData` — binary COPY FROM skips defaults / NOT NULL / CHECK | BUG | [x] | 7f9f16784 |
| EO1-1 | med | `executor/operators_call.go:callOp.Next` — IN/INOUT arg after an OUT param reads the wrong slot | BUG | [x] | cf2eae0e2 |
| EO1-3 | med | `executor/operators.go:limitOp.Next` — `LIMIT 0 ... WITH TIES` panics on nil tieKeyVals | BUG | [x] | 0e50eb19a |
| EO2-2 | med | `executor/operators_sequence.go:seqState.nextVal` — int64 overflow wraps instead of raising 2200H | BUG | [x] | this commit |
| EO2-3 | med | `executor/operators_project_set.go:openSelectSrfMode` — generate_series int64 overflow spins forever | BUG | [x] | this commit |
| EO2-4 | med | `executor/operators_recursive_cte.go:recursiveUnionOp.Open` — phase state not reset on re-open | NOT A BUG | [x] | — |
| EO2-5 | med | `executor/opnode.go:limitOpNext` — `FETCH FIRST 0 ROWS WITH TIES` panics on nil tieKeyVals | BUG | [x] | 0e50eb19a |
| X-5 | med | *(not in the review — found while scoping ES-7)* `executor/plpgsql_runtime.go` — float8 arithmetic inside PL/pgSQL is evaluated as numeric (`(5::float8/2)::text` = `2.5000000000000000`, PG: `2.5`) | BUG | [ ] | found while scoping ES-7 |
| X-6 | med | *(not in the review — found while scoping ES-7)* `executor/plpgsql_runtime.go` — `RETURN <expr>::text` fails 42804 | BUG | [ ] | found while scoping ES-7 |
| ES-6 | med | `executor/plpgsql_runtime.go:executePLpgSQLStmt (ForStmt)` — BY step not validated; zero/negative step infinite-loops | BUG | [x] | db8e39358 |
| ES-7 | med | `executor/plpgsql_runtime.go:lowerPLpgSQLExpr (CastExpr)` — cast dropped in PL/pgSQL expressions | BUG | [x] | f41be5f8e |
| IN-2 | med | `initdb/catalog_cache.go:readCatalogCache` — silent partial catalog on TryRegisterUserTable failure | NOT A BUG | [x] | — |
| ST-3 | med | `storage/bufpool.go:pinLoad/evictVictim` — dirty victim content discarded when the flush fails | BUG (deferred) | [ ] | see log |
| ST-7 | med | `storage/vm.go:PageAllVisible/PageAllFrozen` — plain XID comparison breaks under wraparound | BUG | [x] | 3f13f584e |
| ST-8 | med | `storage/freeze.go:PageFreezeOldTuples` — plain XID comparison breaks under wraparound | BUG | [x] | 8d5028fb3 |
| NB-2 | med | `pglz/pglz.go:Decompress` — match length clamped instead of erroring on corrupt streams | NOT A BUG | [x] | — |
| TA-2 | med | `transam/manager.go:AssignXID` — non-atomic read-check-allocate-store allows XID leak/double-assign | NOT A BUG | [x] | — |
| NP-1 | med | `plpgsql/parser.go:parseFor` — FOR-query scan truncates on a `loop` identifier inside the SQL text | NOT A BUG | [x] | — |
| NP-5 | med | `plpgsql/parser.go:parseFor` — `isQueryFor` peeks only the first token; parenthesized bound misroutes | BUG | [x] | this commit |
| OP1-2 | med | `optimizer/exprwalk.go:exprChildSlots` — FuncCall child slots omit Filter/Over/OrderBy/WithinGroup/Variadic | NOT A BUG | [x] | — |
| OP1-3 | med | `optimizer/createplannl.go:createNestLoopBitmapJoinPlan` — `bhs.BitmapQual = nil` drops recheck quals | BUG | [x] fixed + guarded | this commit |
| XL-4 | med | `wal/slot_decoder.go:Run` — ConfirmedFlushLSN never advances for PG-format commit records | BUG | [x] fixed + guarded | this commit |
| CP-2 | med | `postmaster/dispatch.go:normalizeSQLPreservingLiterals` — plan-cache key collision on quoted identifiers | BUG | [x] | this commit |
| UT-1 | med | `utils/activity/registry.go:coldFromBackend` — `BackendStart` never copied; pg_stat_activity.backend_start empty | BUG | [x] | 306709703 |
| UT-2 | med | `utils/misc/guc.go:canonicalizeFrom` (TypeReal) — unit suffix / scientific notation mis-parsed | BUG | [x] | 00e35fbab |
| UT-4 | med | `utils/mmgr/mctx.go:Context.Release` — mutates `c.children` while ranging over it | BUG | [x] | 370100352 |
| CM-3 | med | `cmd/plan-snapshot/main.go:planEqual` — `rowsRegexp` never matches the real EXPLAIN format | BUG | [x] | edd6195b9 |

## C. Low severity (claimed)

| id | sev | finding | verdict | status | commit |
|---|---|---|---|---|---|
| EC-3 | low | `executor/btree_array_key.go` — multidimensional guard false-positives on a quoted `{` | BUG | [x] fixed + guarded | this commit |
| EC-5 | low | `executor/codec.go:decodePhysicalPGValueLowered` — date/timestamp int64 overflow at PG range extremes | BUG | [x] fixed + guarded | this commit |
| EC-6 | low | `executor/copy_binary.go:datumToCopyBinary` — int4 arm missing range check | NOT A BUG | [x] refuted (unreachable) | - |
| EC-7 | low | `executor/copy_binary.go:copyBinaryToDatum` — infinity sentinels not handled | BUG | [x] fixed + guarded | this commit |
| EC-8 | low | `executor/codec_aclitem.go:aclModeFromPrivLetters` — shift-after-guard pattern (report says not a live bug) | NOT A BUG | [x] refuted (self-declared) | - |
| EO1-2 | low | `executor/operators.go:limitOp.Open` — stale limitCount survives a NULL-limit re-Open | BUG | [x] fixed + guarded | this commit |
| EO1-4 | low | `executor/operators_bitmap.go:lookupBounds` — index out of range with a zero-key-column index | NOT A BUG | [x] | — |
| EO1-5 | low | `executor/operators_generate_series.go:generateSeriesOp.Next` — int64 overflow near MaxInt64 | BUG (dup EO2-3) | [x] | 5c60e8551 |
| EO1-6 | low | `executor/operators_ddl.go:execDropTablespace` — not-empty check only inspects InMemory catalog | NOT A BUG | [x] | — |
| EO1-7 | low | `executor/operators_ddl.go:execAlterCollation/Conversion` — rename collision reported as 42704 | BUG | [x] fixed + guarded | this commit |
| EO1-8 | low | `executor/operators_ddl.go:execCreateTable` — fallback column path drops serial's implicit NOT NULL | NOT A BUG | [x] | — |
| EO1-9 | low | `executor/operators_fk.go:checkConstraints` — CHECK VALUES built from `Format()` text | BUG | [x] fixed + guarded | this commit |
| EO1-10 | low | `executor/operators_from_unnest.go` and sibling SRFs — `SlotFromRow(nil, …)` nil schema | NOT A BUG | [x] refuted | - |
| EO2-6 | low | `executor/operators_pg_options_to_table.go:Open` — lateral binding via ctx.OuterRows, no BindLateralOuter | BUG | [x] fixed + guarded | this commit |
| EO2-7 | low | `executor/operators_generated.go:evalGenExpr` — ColumnRef bounds check after EqualFold | NOT A BUG | [x] refuted | - |
| EO2-8 | low | `executor/operators_utility_settings.go:nextShow` — `SHOW ALL` emits 2 columns vs PG's 3 | BUG | [x] fixed + guarded | this commit |
| ES-1 | low | `executor/parallel_agg_combine.go:combineNumericSum` — Int-lane contribution dropped when lanes disagree | BUG | [x] fixed + guarded | this commit |
| ES-2 | low | `executor/parallel_agg_split.go:aggPartialAccum.merge` — silent break on state-count mismatch | NOT A BUG | [x] verified, no change | — |
| ES-3 | low | `executor/pgstat_relations.go:dropTable` — trigger counters not cleaned up | BUG | [x] fixed + guarded | this commit |
| ES-4 | low | `executor/pg18_user_catalog_rows.go:pgAttTypmod` — numeric typmod bit manipulation | NOT A BUG | [x] verified; adjacent gap fixed (X-4) | — |
| ES-8 | low | `executor/reg_identifier.go:regIdentifierInput` — schema qualifier dropped for user types | BUG | [x] fixed + guarded | this commit |
| IN-1 | low | `initdb/pgcontrol.go:BackupControlImage` — uint64 underflow on redoLSN == 0 | NOT A BUG | [x] verified, no change | — |
| IN-3 | low | `initdb/xact_recovery.go:replayCLogFromWAL` — latent native-record collision hazard | BUG (latent) | [x] fixed + guarded | this commit |
| IN-4 | low | `initdb/initdb.go:mappedLocalCatalogPlaceholderOIDs` — duplicate OID 3764 | NOT A BUG | [x] verified, no change | — |
| IN-5 | low | `initdb/open.go:Open` (RunningXactsFn) — latent uint32 underflow if Xmax == 0 | NOT A BUG | [x] verified, no change | — |
| IN-6 | low | `initdb/information_schema_tables.go` — CRLF / non-`\N` NULL handling | NOT A BUG | [x] verified, no change | — |
| ST-4 | low | `storage/bufpool.go:WriteDirtyPages` — bgwriter scan cursor never advances | BUG | [x] fixed + guarded | this commit |
| ST-5 | low | `storage/bufmap.go:Lookup` — probe bound off by one | NOT A BUG | [x] | — |
| ST-6 | low | `storage/bgwriter.go:Stop` — double-close panic / hang on Stop-without-Start | NOT A BUG | [x] | — |
| ST-9 | low | `aio/method_iouring_linux.go:pokeWake` — NOP written without checking SQ ring fullness | BUG | [x] fixed + guarded | this commit |
| ST-10 | low | `storage/writeback.go:accountWrite` — `pendingBlocks.Store(0)` races a concurrent `Add` | NOT A BUG | [x] verified, no change | — |
| NB-3 | low | `access/nbtree/btree_vacuum.go:readInternalFirstChildBlock` — wrong downlink block (dead code) | BUG | [x] fixed + guarded | this commit |
| NB-4 | low | `access/nbtree/btree.go:descendToLeaf` — wrong sentinel disables the rightmost-leaf cache | BUG | [x] fixed + guarded | this commit |
| NB-5 | low | `access/nbtree/btree.go:tryInsertOnCachedRightmost` — no deleted/half-dead page check | BUG | [x] fixed + guarded | this commit |
| TA-3 | low | `transam/manager.go:Begin` — auto-assign path skips isolation-level validation | BUG | [x] fixed + guarded | this commit |
| TA-4 | low | `transam/manager.go:AcquireConnSlot` — int32 cursor overflow → negative modulo | BUG | [x] fixed + guarded | this commit |
| TA-5 | low | `transam/clog.go:GetStatus` — nil `pool` dereference vs the nil-safe contract elsewhere | NOT A BUG | [x] | — |
| TA-6 | low | `multixact/multixact.go:StatusesConflict` — invalid Status indexes out of bounds | NOT A BUG | [x] | — |
| NP-2 | low | `plpgsql/parser.go:parseSQLStmt` — `SELECT INTO x FROM t` yields a malformed query | NOT A BUG | [x] refuted (PG does the same) | - |
| NP-3 | low | `nodes/outfuncs.go:outDatum` — by-value Const with short/nil Datum panics | NOT A BUG | [x] verified, no change | — |
| NP-4 | low | `nodes/readfuncs.go:readDatum` — negative/zero by-reference length silently accepted | NOT A BUG | [x] verified, no change | — |
| NP-6 | low | `plpgsql/parser.go:parseStmt` — `<<label>>` / `label: LOOP` forms mis-parse | NOT A BUG | [x] unimplemented feature, not a regression | - |
| OP1-1 | low | `optimizer/copy.go:validateCopyOptions` — REJECT_LIMIT accepted with ON_ERROR=STOP | BUG | [x] fixed + guarded | this commit |
| OP1-4 | low | `optimizer/cardinality.go:indexScanRows` — unique-index shortcut ignores range bounds | NOT A BUG | [x] verified, no change | — |
| OP1-5 | low | `optimizer/costbitmap.go:computeBitmapPagesLooped` — lossy-page adjustment computed then discarded | BUG | [x] fixed + guarded | this commit |
| OP1-6 | low | `optimizer/cardinality.go:EstimateRows` — `LIMIT 0` returns 0 read as "no estimate" | NOT A BUG | [x] verified, no change | — |
| OP2-1 | low | `optimizer/foldconst.go:foldCaseExpr` — dead THEN under a NULL simple-CASE operand is folded | BUG | [x] fixed + guarded | this commit |
| XL-1 | low | `wal/append_xlog_payload.go:appendXLogPayload` — error from emitWithPageHeaders swallowed | NOT A BUG | [x] refuted (no error is returned) | - |
| XL-2 | low | `wal/insert_pos.go:reserveLocked` — `onCrossSegment` padded return discarded | NOT A BUG | [x] refuted (unmounted legacy path) | - |
| XL-3 | low | `wal/pgoutput.go:encodePgoTuplePhysical` — column-offset walk mis-advances on null columns | NOT A BUG | [x] | — |
| XL-5 | med | `wal/pgoutput.go:pgoPhysicalAlign` — alignment table drifted from the executor decoder's (found while checking XL-3) | BUG | [x] fixed + guarded | this commit |
| CP-3 | low | `postmaster/autovacuum/launcher.go:freezeCutoff` — dead signedness check / unsigned wrap reliance | BUG | [x] fixed + guarded | this commit |
| CP-4 | low | `replication/logicalwalsender.go:walsenderPgoutputAdapter.Write` — LSN underflow on empty write | BUG | [x] fixed + guarded | this commit |
| CP-5 | low | `postmaster/server.go:isReplicationStartupParam` — overly broad match | BUG | [x] fixed + guarded | this commit |
| UT-3 | low | `utils/misc/guc.go:convertUnit` — int64 overflow on cross-unit conversion | BUG | [x] fixed + guarded | this commit |
| UT-5 | low | `utils/activity/stats/counter.go:Add` — index out of range when GOMAXPROCS > 256 | BUG | [x] fixed + guarded | this commit |
| UT-6 | low | `utils/adt/datetime/normalize.go:padTimeFields` — run-together time expansion not implemented | NOT A BUG | [x] verified, no change | — |
| UT-7 | low | `utils/misc/session.go:EndTransaction` — rollback fires `invokeOnChange` for unchanged values | NOT A BUG | [x] refuted (callbacks idempotent) | - |
| CM-2 | low | `cmd/goopg/standby.go:Promote` — `sc.replayer.ApplyLSN()` without a nil guard | NOT A BUG | [x] verified, no change | — |
| CM-4 | low | `cmd/gen-*-coverage/main.go:loadCSV` — `row[statusIdx]` out of bounds on malformed CSV | NOT A BUG | [x] refuted (csv enforces field count) | - |
| CM-5 | low | `cmd/estimate-audit/main.go:selectQueries` — `Atoi` errors ignored, negative query numbers pass | BUG | [x] fixed + guarded | this commit |
| CM-6 | low | `cmd/gen-pg-operator-data/main.go:parseOperatorDat` — right-unary operator kind `'r'` unhandled | NOT A BUG | [x] refuted (PG has no postfix operators) | - |
| X-1 | low | *(not in the review — found while verifying OP2-1)* `optimizer/foldconst.go` — `COALESCE(1, 1/0)` raises at plan time; PG returns 1 | BUG | [ ] not fixed | |
| X-2 | low | *(not in the review — found while verifying NP-2)* `'x' \|\| true` yields `xt`; PG yields `xtrue` (bool→text in concat takes the 1-char form) | BUG | [ ] not fixed | |
| X-3 | low | *(not in the review — found while verifying NP-2)* plpgsql `FOUND` stays false after a successful `SELECT … INTO`; PG sets it true | BUG | [ ] not fixed | |
| X-4 | med | *(not in the review — found while verifying ES-4)* `executor/pg18_user_catalog_rows.go:pgAttTypmod` — no arm for time/timetz/timestamp/timestamptz, so `time(3)` reports atttypmod -1 (PG: 3) | BUG | [x] fixed + guarded | this commit |
| X-7 | med | *(not in the review — found while verifying ES-8)* a literal `'int4'::regtype` in a SELECT list is never resolved: goopg prints `int4` (PG: `integer`, via format_type) and `'int4'::regtype::oid` errors `invalid input syntax for type oid`. The regtype COLUMN path is correct, so the literal-cast path bypasses `regIdentifierInput` entirely. `'pg_class'::regclass::oid` works, so this is regtype-only | BUG | [ ] not fixed | |
| X-8 | med | *(not in the review — found while probing)* `enable_indexscan` / `enable_bitmapscan` / `enable_indexonlyscan` are accepted but ignored: with all three `off` in a fresh session, `SELECT * FROM t WHERE a = 42` still plans `Index Only Scan` at cost 0.00..0.00 (PG switches to `Seq Scan`). The zero cost suggests a planner fast path that never consults the GUCs | BUG | [ ] not fixed | |

---

## Progress log

(append one line per verified/fixed item)

- 2026-09-01 **EO2-1 = BUG (fixed).** `indexOnlyScanOp.Rescan` passed `false, false`
  to `RangeScanWithPosLeafFilter` and padded only the composite HI bound, so a
  strict bound scanned INCLUSIVELY — the exact asymmetry `indexScanOp` avoids.
  Not reachable at HEAD only because `tryPromoteIndexOnlyScan` refused to promote
  strict-bound scans (M0134-0001 S4 Option B, deferral-ledger row 2026-08-15),
  which cost the PG-matching `Index Only Scan` plan shape. Fixed with Option A:
  `IndexOnlyScan` carries `LowOp`/`HighOp` (optimizer/plan.go), the promotion
  copies them and the refusal is gone (optimizer/planner.go), the operator
  mirrors `indexScanOp`'s strictness-aware composite padding and passes the real
  exclusive flags, and the EXPLAIN IOS branch gained the missing `Index Cond:`
  (shared `formatIndexCondParts`) and `Filter:` lines. Verified on a live capped
  server: `a > 5 AND a < 10` under an Index Only Scan returns 6..9, and a
  composite `(a,b)` index agrees with the inclusive rewrite. The two
  `range_exclusive_index_scan_test.go` cases that pinned Option B were updated to
  the Option-A shape; the composite-keeps-Filter case now passes only because the
  render gap is closed. Deferral-ledger row flipped to `resolved`.
  Gates: UNITS PASS, tpch-spotcheck PASS (Q12=2/Q13=34), TPC-DS SF0.5 PASS.

Note on the `commit` column: a commit cannot contain its own hash, so a row's
hash is filled in by the NEXT commit; rows reading `see progress log` are already
landed.

- 2026-09-01 **NB-1 = BUG (fixed).** `vacuumCore`'s visibility-map skip branch
  `continue`d without touching `lastNonEmpty`, and the tail-truncation step at
  the end of the pass truncates the relation to `lastNonEmpty+1`. A trailing run
  of all-visible (hence live, hence skippable) blocks therefore looked EMPTY and
  was truncated away — silent data loss on exactly the pages VACUUM is proudest
  of skipping. Upstream advances `vacrel->nonempty_pages` on this very path
  (`heap_vac_scan_next_block`, vacuumlazy.c). Fix: set `lastNonEmpty = blk`
  before the `continue`. Guard: `TestVacuumTailTruncationKeepsVMSkippedBlocks`
  (3 all-visible live blocks + 1 genuinely empty tail block; nblocks must be 3,
  was 0 before the fix).

- 2026-09-01 **ST-1 = BUG (fixed).** `ReadFSMFork` reconstructed the FSM tree by
  walking BACKWARDS from the root with `need = ceil(levelCount/fsmSlotsPerPage)`,
  which evaluates to 1 at every step — so it always concluded the file had
  exactly one leaf page. `buildFSMTree` writes level 0 first and the root last,
  so for any relation over `fsmSlotsPerPage` (4069) heap blocks every free-space
  entry from block 4069 onward was silently dropped on load, and the leaf offset
  it computed was wrong as well. Fix: leaves start at offset 0, and the leaf
  COUNT is recovered forward (total pages is strictly increasing in the leaf
  count, so scan for the count whose tree is exactly this file); a file matching
  no leaf count is now a hard error rather than a silent partial read. Guard:
  `TestFSMForkRoundTripBeyondOneLeafPage` (fsmSlotsPerPage+100 blocks).

- 2026-09-01 **ST-2 = BUG (fixed).** `TupleDeadToAll` compared the effective
  xmax against the horizon with a plain `effXmax >= oldestXmin`. XIDs are
  modular: once the counter wraps past 2^31 the plain compare inverts, so a
  tuple deleted by a transaction NEWER than the horizon is reported dead-to-all
  and pruning reclaims a row that concurrent snapshots can still see. PG uses
  `TransactionIdPrecedes` here (`HeapTupleSatisfiesVacuum`). Fix:
  `!XIDPrecedes(effXmax, oldestXmin)`. Guard: `TestTupleDeadToAllWraparound`
  (horizon 0xFFFFFF00, deleter 50 — i.e. just after a wrap).

- 2026-09-01 **TA-1 = BUG (fixed).** `TupleVisible`'s `HeapXmaxCommitted` arm
  returned `false` unconditionally. The hint bit only records that xmax
  committed at SOME point — it says nothing about whether the reading snapshot
  predates that commit — so a REPEATABLE READ / serializable snapshot lost rows
  the moment any other backend's scan happened to set the bit, and the same
  snapshot disagreed with the `TupleVisibleSubxact` twin two arms down, which
  does consult the snapshot. PG re-checks in this exact branch
  (`HeapTupleSatisfiesMVCC`: `if (XidInMVCCSnapshot(xmax, snapshot)) return
  true;`). Fix: `return !snap.SeesCommittedXID(effXmax)`. Guard:
  `TestTupleVisibleXmaxCommittedHintRespectsSnapshot` — a snapshot with the
  deleter still in progress keeps seeing the row, the subxact twin agrees, and a
  deleter below Xmin stays invisible.

- 2026-09-01 **CP-1 = BUG (fixed).** `applyTransactionVerb`'s COMMIT-in-a-failed-
  block arm (which is a ROLLBACK per PG semantics) went straight to
  `TxnMgr.Rollback` without first running `executor.ProcessRollbackUndos` — the
  step the explicit `TxRollback` arm right below it does run. In-memory catalog
  registrations are not transactional, so `BEGIN; CREATE TABLE t; <error>;
  COMMIT;` left `t` alive and writable in the catalog while its
  pg_class/pg_attribute rows had been rolled back. Verified on a live capped
  server before the fix (`failblk` survived and accepted an INSERT). Fix: run
  the undos on this path too. Guard:
  `internal/testport/failed_block_commit_ddl_undo_test.go` — one session, four
  messages; after the failed-block COMMIT `pg_class` must have no `failblk` row
  and the INSERT must fail with "does not exist" (red before the fix on both
  assertions).

- 2026-09-01 **CM-1 = BUG (fixed).** `standbyController.Promote` set the
  `promoting` atomic and never cleared it, and wrapped the work in a
  `sync.Once`. A promote that FAILED (drain timeout, cancelled context) left the
  node a standby, yet every later PROMOTE — control socket and `promote.signal`
  alike — returned "promotion already in progress" for the life of the process.
  That directly contradicts `promoteSignalWatcher`'s own contract ("removed
  first so a partial Promote can be retried by re-creating the file"). Fix:
  `defer sc.promoting.Store(false)` so the in-flight guard is released on every
  exit, drop the now-redundant `sync.Once` (the `promoted` flag already provides
  success idempotency, and steps 1-2 of runPromote are idempotent), and re-check
  `promoted` after winning the CAS. Guard:
  `TestStandbyControllerPromoteRetryableAfterFailure` — hand-built controller
  whose receiverDone stays open so the first attempt fails deterministically on
  a cancelled context; the retry after closing it must succeed (it returned
  "promotion already in progress" before the fix).

- 2026-09-01 **ES-5 = BUG (fixed).** `tbmIterator` carried a `lossyVisited`
  flag whose intent was "do not return the same lossy page twice", but the
  flag was set on every lossy page yielded and only cleared on the *next*
  lossy page — so with a run of lossy pages every second one was skipped
  entirely, silently losing all its tuples from a bitmap heap scan. There is
  no such de-duplication state in PG's `tbm_iterate`: the chunk cursor
  already advances monotonically, so a page can never repeat. Fix: drop the
  field and both guard blocks in `nextPage`/`next`. Guard:
  `TestTIDBitmapIteratorEveryLossyPageYielded` (red before the fix: it
  observed only the odd-numbered lossy pages). Gates: units green;
  TPC-DS SF0.5 sweep on this build PASS=95 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0, plan shapes 99/99 identical.

- 2026-09-01 **EC-1 = BUG (fixed).** `parseIntegerInput` handed the whole
  string to `strconv.ParseInt(…, 0, bitSize)`, whose base-0 mode reads a
  leading `0` as an OCTAL prefix: `'010'::int` returned 8 and `'09'::int`
  failed with a parse error, where PG returns 10 and 9. PG's `pg_strtoint32`
  only honours the explicit `0x`/`0o`/`0b` radix prefixes it added in v16 —
  a bare leading zero is decimal. Fix: detect the explicit prefix
  (after an optional sign) and pass base 0 only then, base 10 otherwise.
  Guard: `TestParseIntegerInputLeadingZeroIsDecimal`. Gates: units green;
  TPC-DS SF0.5 sweep on this build PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0
  TIMEOUT=0, plan shapes 99/99 identical; TPC-H spotcheck Q12/Q13 PASS.

- 2026-09-01 **EC-2 = BUG (fixed).** `encodeArrayBTreeKey` split the array
  literal with `parseTextArray`, which returns only the unquoted element
  TEXT, then tagged any element equal to `NULL` as a SQL NULL. That erases
  the one distinction the array syntax exists to carry: `{NULL}` is a null
  element but `{"NULL"}` is the four-character string. Two rows differing
  only in that quoting hashed to the same b-tree key, so an index lookup
  for `'{"NULL"}'` could return the `{NULL}` row (PG's `ReadArrayStr`
  treats the quoted form as a plain value). Fix: `parseTextArrayElems`
  returns each element with a `Quoted` flag and the NULL tag is applied
  only to unquoted `NULL`; `parseTextArray` stays a thin wrapper so the
  other callers are unchanged. Guard:
  `TestEncodeArrayBTreeKeyQuotedNullIsNotNull`. Gates: units green;
  TPC-DS SF0.5 sweep PASS=95 MISMATCH=0, plan shapes 99/99 identical.

- 2026-09-01 **EC-4 = BUG (fixed).** The text/CSV COPY path routed every row
  through `insertSourceRow`, which scatters the parsed columns into the
  table's row shape AND applies column defaults, NOT NULL and CHECK
  constraints before storing. `PushBinaryData` (BINARY COPY FROM) called
  only the scatter half and stored the row directly, so a binary load could
  write NULLs into NOT NULL columns, skip DEFAULT/serial filling for
  omitted columns and violate CHECK constraints — a durable corruption of
  the table's declared invariants. PG runs one `CopyFrom` loop for all
  formats and calls `ExecConstraints` per tuple regardless (copyfrom.c).
  Fix: split `insertSourceRow` into `scatterSourceRow` + `storeCopyRow`
  and have the binary loop call both, so the two siblings cannot drift
  again. Guard: `internal/executor/copy_binary_constraints_test.go`
  (NOT NULL, CHECK and DEFAULT cases). Gates: units green; TPC-DS SF0.5
  sweep PASS=95 MISMATCH=0; TPC-H spotcheck Q12/Q13 PASS.

- 2026-09-01 **EO1-1 = BUG (fixed).** `callOp` bound the CALL argument list
  with a moving `argIdx` cursor that advanced once per NON-OUT parameter,
  while the values themselves had already been scattered onto the routine's
  full parameter list. For a procedure whose OUT parameter is not last —
  `p(IN a, OUT b, IN c)` — the cursor read slot 1 for the third parameter,
  so `c` silently received `b`'s (unset) value and every error message
  named the wrong argument position. Fix: bind positionally from the
  scattered list (`o.args[i]`, `argument %d, i+1`) and scatter a short
  positional list onto the non-OUT positions before that. Guard:
  `internal/executor/call_out_param_slot_test.go`. Gates: units green;
  TPC-DS SF0.5 sweep PASS=95 MISMATCH=0; TPC-H spotcheck Q12/Q13 PASS.

- 2026-09-01 **EO1-3 / EO2-5 = BUG (fixed, one change).** Both LIMIT
  implementations — `limitOp.Next` (operators.go, the Build+Run path) and
  `limitOpNext` (opnode.go, the BuildFast path the live server uses) —
  entered their WITH TIES comparison as soon as `emitted >= limitCount`.
  For a count of ZERO that is true before any row has been emitted, so
  `tieKeyVals` was still empty and the comparison indexed an empty Row:
  `FETCH FIRST 0 ROWS WITH TIES` panicked the backend with "index out of
  range [0] with length 0". PG returns no rows (nodeLimit.c never enters
  the tie window with an empty boundary row). Fix: in both siblings, bail
  out with EOF when the tie-key snapshot is empty. Guards:
  `TestLimitZeroWithTiesReturnsNoRows` (Build+Run) and
  `TestLimitZeroWithTiesReturnsNoRowsFast` (BuildFast) — each was verified
  red by stashing only its own sibling's file. Gates: units green; TPC-DS
  SF0.5 sweep PASS=95 MISMATCH=0; TPC-H spotcheck Q12/Q13 PASS.

- 2026-09-01 **ST-7 = BUG (fixed).** `PageAllVisible` and `PageAllFrozen`
  compared each tuple's xmin against the horizon with a plain unsigned
  `>=`. XIDs are a circular 32-bit space: once the counter wraps, every
  pre-wraparound xmin (numerically huge) reads as NEWER than the small
  post-wrap horizon, so VACUUM could never set ALL_VISIBLE / ALL_FROZEN on
  an old page again — index-only scans lose their visibility fast path and
  freezing stalls exactly when wraparound pressure is highest. PG compares
  with `TransactionIdPrecedes` (heap_page_is_all_visible). Fix: use the
  repo's existing circular primitive `XIDPrecedes` (same primitive as the
  ST-2 fix). Guard: `TestPageAllVisibleAcrossWraparound`. Gates: units
  green; pgbench smoke green (commit hook).

- 2026-09-01 **ST-8 = BUG (fixed).** Same class as ST-7, on the freeze path:
  `PageFreezeOldTuples` decided eligibility with `xmin >= freezeBelow` and
  tracked `MinUnfrozenXID` with `<`. After the XID counter wraps, every
  ancient (numerically huge) xmin looks newer than the freeze cutoff, so
  VACUUM freezes NOTHING at the moment freezing is what prevents
  wraparound, and relfrozenxid advances off the wrong minimum. PG uses
  `TransactionIdPrecedes` (heap_prepare_freeze_tuple). Fix: `XIDPrecedes`
  for both comparisons. Guard: `TestPageFreezeAcrossWraparound` (a
  pre-wrap tuple must be frozen and the post-wrap one reported as
  MinUnfrozenXID). Gates: units green; pgbench smoke green.

- 2026-09-01 **UT-1 = BUG (fixed).** `coldFromBackend` copied every
  immutable slot field except `BackendStart`, so although
  postmaster/server.go stamps the connection time at registration, the
  activity slot kept 0 and `formatNanos(0)` returns "" — every row of
  pg_stat_activity reported an EMPTY backend_start (PG's is never null;
  monitoring queries date sessions from it). The field also changes
  representation at the boundary (RFC3339Nano string in, unix nanos in the
  slot), which is how the omission stayed invisible. Fix: parse it in
  `coldFromBackend` (new `parseWallString`) and fall back to registration
  time when the caller left it unset. Guard:
  `TestRegisterKeepsBackendStart`. Gates: units green; pgbench smoke green.

- 2026-09-01 **UT-2 = BUG (fixed).** `canonicalizeFrom`'s TypeReal arm split
  the value into number and unit suffix with a hand-rolled scan that
  accepted only sign, digits and `.`, so an exponent landed in the
  "suffix": `1e-2` became number `1` + suffix `e-2`, and since a unitless
  GUC ignores its suffix the stored value was 1 — a silent 100x error on a
  planner cost knob. Measured on PG 18.3 (127.0.0.1:65438):
  `set seq_page_cost = 1e-2; show seq_page_cost;` → `0.01`; `1E2` → `100`;
  `2.5e1` → `25`; `set cursor_tuple_fraction = 5e-1` → `0.5` (goopg
  rejected the last one as out of range, having read it as 5). PG parses
  reals with strtod (`parse_real`). Fix: `realNumericPrefixLen` takes the
  longest float-parseable prefix — exactly strtod's rule — and the rest is
  the suffix. Guard: `TestSetRealAcceptsScientificNotation`. Gates: units
  green; pgbench smoke green.

- 2026-09-01 **UT-4 = BUG (fixed).** `Context.Release` ranged over
  `c.children` while each child's own `Release` spliced itself out of that
  same slice. The range keeps the original length but the splice shifts the
  not-yet-visited elements down, so with N children every second one was
  never released — its chunks never returned to the pool and its own
  subtree stayed alive — while another child was released twice. Measured
  by the guard: with 5 children, children 1 and 3 kept their id, their
  chunk and their parent pointer. Fix: detach the slice first
  (`children := c.children; c.children = nil`) and clear each child's
  parent before releasing it, so the child's removal scan is a no-op.
  Guard: `TestReleaseCascadesToEveryChild`. Gates: units green; pgbench
  smoke green.

- 2026-09-01 **CM-3 = BUG (fixed).** `plan-snapshot`'s `rowsRegexp` was
  `\s*\(rows=(\d+)\)`, a shape no EXPLAIN ever prints: PG 18.3 emits
  `Seq Scan on lineitem  (cost=0.00..35.50 rows=2550 width=4)` and goopg
  emits the same annotation with zeroed costs
  (operators_explain.go). Nothing was ever stripped or extracted, so the
  `structural` mode silently behaved exactly like `strict-text` — a cost
  drift reported a plan DIFFER — and `semantic-cost` compared two empty
  cost lists, i.e. its ±10% tolerance never ran. The unit tests missed it
  because they were written against the same invented format; they now use
  real annotations. Fix: match the whole `(cost=A..B rows=N width=W)`
  annotation and capture N. Guard:
  `TestRowsRegexpMatchesRealExplainOutput` (one PG line, one goopg line).
  Gates: units green; pgbench smoke green.

- 2026-09-01 **NB-2 = NOT A BUG.** `pglz.Decompress` clamping a match length
  to the remaining output (`if remaining := rawSize - len(dst); length >
  remaining { length = remaining }`) is exactly what PG does:
  `postgres/src/common/pg_lzcompress.c`, pglz_decompress → `/* Don't emit
  more data than requested. */ len = Min(len, destend - dp);`. Corruption
  is caught the same way in both: goopg's trailing
  `len(dst) != rawSize` check is PG's `check_complete` test. (One real
  difference, noted and NOT a live bug: PG's check_complete also requires
  the SOURCE to be fully consumed (`sp != srcend`), so goopg accepts a
  stream with trailing garbage after a complete output. No caller can
  produce that; recorded here rather than fixed.)

- 2026-09-01 **TA-2 = NOT A BUG (no concurrent caller).** `AssignXID`'s
  read-check-allocate-store on `s.xid` is indeed not atomic, but a slot is
  a per-connection procNum and the only caller is
  `Context.MaterializeWriterXID`, reached from the owning backend's own
  goroutine on the write path; goopg's parallel workers are read-only and
  never materialise an XID. So no two goroutines can race on one slot
  today — the same structural argument PG relies on (a backend assigns its
  own XID under XidGenLock, never another backend's). Left as-is rather
  than adding a CAS that would still leak the losing XID; re-open this row
  if a parallel or background writer ever shares a procNum.

- 2026-09-01 **EO2-4 = NOT A BUG (re-open unreachable).** `recursiveUnionOp.Open`
  really does leave `initDone`/`done`/`outIdx`/`depth`/`output` intact, so a
  second `Open` on the same operator would replay a finished fixpoint. But no
  caller can do that. The only re-open path in the executor is the SubPlan
  rescan machinery (`executor/subplan.go`), and `classifySubPlan` has no arm
  for `*optimizer.RecursiveUnion`: it falls into `default: kind =
  rescanRebuild`, i.e. any plan containing a recursive CTE is rebuilt from the
  plan tree for every rescan, never re-Opened. The other per-outer-row replay
  path is the `Rescan(outerSlot, outerWidth)` interface (nested-loop-index
  join / memoize), which `recursiveUnionOp` does not implement, so it can
  never be selected as a rescannable inner. Left as-is; if a
  `*RecursiveUnion` arm is ever added to `classifySubPlan`, the reset must be
  added at the same time.

- 2026-09-01 **NP-1 = NOT A BUG (PG truncates identically).** The FOR-query
  scan really does stop at the first depth-0 `loop` token even when it is
  meant as an identifier — but that is exactly PG's own lexical rule, since
  plpgsql scans the SQL text for the closing LOOP keyword rather than parsing
  it. Measured on PG 18.3 at 127.0.0.1:65438:
  `for r in select loop from np1_t loop …` → `ERROR: syntax error at or near
  "from"` (PG cut the statement at `loop` too), while the quoted form
  `for r in select "loop" from np1_t loop …` returns 3. goopg matches on both
  counts: its lexer only produces the `loop` KEYWORD token for the unquoted
  spelling, so a quoted `"loop"` column runs through the FOR-query path fine
  (verified with a throwaway probe: `np1_f()` = 3). No divergence to fix.

- 2026-09-01 **NP-5 = BUG, FIXED.** `parseFor` chose between an integer-range
  FOR and a query FOR by peeking at a single token, and treated ANY leading
  `(` as a query FOR. So a parenthesised lower bound went to the SQL parser:
  `FOR i IN (1+1)..4 LOOP` failed with `42601: FOR query parse error: syntax
  error at or near "1"`. PG resolves the same ambiguity by which terminator
  the bound expression stops at (`pl_gram.y` for_control reads it with
  `read_sql_expression2(K_DOTDOT, K_LOOP)`), i.e. a top-level `..` ahead of
  LOOP means integer FOR; measured on PG 18.3, that loop sums to 9. Fix: new
  `dotDotBeforeLoop()` pure lookahead (paren-depth aware, stops at the depth-0
  LOOP) decides the `(` case. Guard
  `internal/executor/plpgsql_for_paren_bound_test.go` covers both the range
  form and the control `FOR r IN (SELECT …) LOOP`, which must stay a query
  FOR; proven red by stashing only `internal/pl/plpgsql/parser.go`.

- 2026-09-01 **CP-2 = BUG, FIXED (live wrong results).**
  `normalizeSQLPreservingLiterals` lowercased everything outside SINGLE quotes,
  so a quoted identifier was folded too and `SELECT * FROM "Foo"` /
  `SELECT * FROM "foo"` produced the same `planCacheKey`. PG downcases only
  UNquoted identifiers (`scan.l` {identifier} → `downcase_truncate_identifier`;
  the `<xd>` delimited form is taken as-is), so those are two different tables.
  Reproduced on a throwaway goopg server (port 5539, `tmp/cp2data`): with 111
  in `"Foo"` and 222 in `"foo"`, three `select * from "Foo"` followed by
  `select * from "foo"` returned **111** — the cached "Foo" plan. Fix: a
  `inDoubleQuote` span in the same shape as the single-quote one (doubled `""`
  is an escaped quote, not the end). Re-verified on the same server after the
  fix: `Foo=111`, `foo=222`. Guard
  `internal/postmaster/plancache_quoted_ident_test.go`, proven red by stashing
  only `internal/postmaster/dispatch.go`; it also pins the unquoted folding,
  the whitespace collapse and the single-quote preservation.
  (Noted, NOT fixed: a dollar-quoted body is still case-folded in the key, so
  two `CREATE FUNCTION`s whose bodies differ only in case share a key. DDL
  invalidates the cache on every statement, so no reproducer today.)

- 2026-09-01 **ES-6 = BUG, FIXED (hang).** The `FOR … BY <step>` arm read the
  step and went straight into the loop, so `BY 0` (and any negative step in a
  forward loop) never advanced past the bound and the backend spun inside the
  function forever — no error, no cancellation point. PG validates the step up
  front: null → `22004 "BY value of FOR loop cannot be null"`, non-positive →
  `22023 "BY value of FOR loop must be greater than zero"` (pl_exec.c), both
  reproduced on PG 18.3 at 127.0.0.1:65438. Fix: the same two checks before
  the loop. Guard `internal/executor/plpgsql_for_step_test.go` runs the
  function on a side goroutine with a 10 s deadline, so the pre-fix build
  fails by TIMING OUT rather than by hanging the suite.

- 2026-09-01 **ES-7 = BUG, FIXED (wrong results).** `lowerPLpgSQLExpr`'s
  `*parser.CastExpr` arm returned the operand unchanged, so every cast written
  inside a PL/pgSQL expression was silently DISCARDED and the expression
  evaluated at the operand's own type: `7 / 2::numeric` did integer division
  and produced 3. Fix: lower the operand and rebuild the cast through the new
  `optimizer.NewCastExprFromParser`, which is the planner's own construction
  (target type lower-cased, source type from `exprType`, typmod encoded) so
  the two lowering paths cannot disagree. Guard
  `internal/executor/plpgsql_cast_test.go` pins the PG 18.3 output
  `3.5000000000000000|2|2.5000000000000000`, the middle term being the control
  that uncast integer division must STAY integer division.
  Two gaps found while scoping this and NOT introduced by it (both verified to
  fail on the pre-fix build too, so they are separate findings, added to the
  table as X-5/X-6): float8 arithmetic inside PL/pgSQL is evaluated as
  numeric, and `RETURN <expr>::text` fails 42804.

- 2026-09-01 **IN-2 = NOT A BUG (the error cannot occur).** `readCatalogCache`
  logs and continues when `TryRegisterUserTable` fails, and still returns
  `(true, nil)` — so a partial registration would be reported as a cache HIT
  and `open.go:1475` would skip `loadUserTablesFromHeap`, leaving tables
  invisible. But `InMemory.TryRegisterUserTable`
  (`internal/catalog/catalog.go:12432`) has exactly one error return, `nil
  table`, and the loop constructs the `*catalog.Table` itself two lines above
  the call; a duplicate key is an explicit idempotent `return nil`. So the
  warn-and-continue branch is unreachable. Left as-is, with the standing
  requirement recorded here: if `TryRegisterUserTable` ever grows a real
  error return, `readCatalogCache` must return `(false, err)` so the heap scan
  still runs.

- 2026-09-01 **ST-3 = BUG, NOT FIXED (deferred, needs an eviction-path
  redesign).** `evictVictim` deletes the victim's bufmap entry and releases
  the slot's waiters BEFORE it looks at `flushErr`, and `pinLoad`/`pinNewXID`
  then call `releaseVictimSlot`, so the slot is handed to the next requester.
  When `flushSlot` fails (`WriteBlock` on ENOSPC/EIO, or `flushWALWithRetry`),
  the only copy of that dirty page is therefore discarded: the caller gets an
  error, but a later successful write of the same block never happens and the
  block silently reverts to its pre-flush on-disk content. PG keeps the buffer
  valid AND dirty on a write error (the `elog(ERROR)` in `FlushBuffer` unwinds
  with the buffer still in the mapping) so the next checkpoint retries it.
  Matching that means keeping the slot occupied, dirty and un-reusable on a
  failed flush and retrying later — which touches the very ordering the
  current code documents as the M-NIGHTLY loop-13 root-cause fix (bmDelete
  must happen after the flush, before releasing waiters). Deliberately NOT
  attempted blind: `Pool.mgr` is a concrete `*Manager`, so there is no seam to
  inject a write failure and the change would land unverified in the pool's
  most concurrency-sensitive path. Recorded here as a real defect awaiting a
  fault-injection seam.

- 2026-09-01 **OP1-2 = NOT A BUG (wrong type).** The finding lists
  `Filter`/`Over`/`OrderBy`/`WithinGroup` as fields of the `*FuncCall` the
  `exprChildSlots` arm handles, but `exprwalk.go` walks the OPTIMIZER expr
  tree, and `optimizer.FuncCall` (`plan.go:596`) has exactly four fields
  besides `pos`: `Name`, `Args`, `Star`, `Variadic`, `ReturnType`, `ArgWidth`
  — no clause fields at all. `Variadic` is a bool, and `exprSelfKey` already
  keys it (`"fn:%s/%t/%t"`). Those clauses live on the SEPARATE optimizer
  types `AggregateExpr` (Filter, OrderBy, WithinGroup — plan.go:1154) and
  `WindowFunc` (Filter — plan.go:1310), and `exprChildSlots` has no arm for
  either: it returns `(nil, false)`, which every one of the four drivers
  treats as "never taught" — `walkExprRefs`, `rewriteExprRefsInPlace` and
  `cloneExprRefs` call `OnUnknown` and ABORT with false, and
  `appendExprIdentityKey` refuses to build a key. So an aggregate's FILTER
  expression is never silently skipped; the traversal declines instead. That
  is the design the file documents (a `default:` sweep is deliberately absent
  so "known childless" and "never taught" stay distinguishable).

- 2026-09-01 **XL-4 = BUG, FIXED.** `SlotDecoder.Run` advanced the slot's
  `ConfirmedFlushLSN` only when the record's NATIVE payload began with
  `RecordKindXactCommit`. But every commit the server writes is PG-format —
  `initdb/open.go`'s xact-marker hook calls `EncodeXactCommitPG` for BOTH the
  plain and the HAS_INVALS case — and those carry no native payload, so
  `Classify` routes them through `classifyDecodedXLog` → `Decoder.ApplyCommit`
  and the payload test never matched. The plugin saw the commit; the slot's
  restart anchor stayed at creation, so a restart replayed every transaction
  since the slot was created and re-delivered already-acked commits. Fix: a
  `recordIsXactCommit` helper that accepts either form (native payload kind,
  or `RmgrXact` + `xlogXactCommit` in the decoded header — the exact test
  `classifyDecodedXLog` dispatches on), so the two paths cannot drift again.
  Guard `internal/access/transam/xlog/slot_decoder_pgcommit_test.go` appends a
  real `EncodeXactCommitPG` record and asserts the anchor moved; pre-fix it
  read `ConfirmedFlushLSN=0`. The existing native-format test stays as the
  other half of the pair.

### OP1-3 — BUG (fixed, guard added)

`createNestLoopBitmapJoinPlan` cleared `bhs.BitmapQual` while
`probeEnforcedClauses` had already removed the same clause from the join
residual, so on the NLI-bitmap arm the join key was enforced *nowhere*. That is
sound for the sibling index arm (the probe enforces its keys exactly) but not
for a bitmap heap scan: once a per-probe bitmap exceeds `work_mem`,
`tbmLossify` degrades pages to lossy and the heap scan yields every tuple on
such a page, relying on the recheck qual to filter them — PG keeps
`bitmapqualorig` for exactly this. `BitmapQual` still cannot be set here (it
would need leaf-local coordinates for a per-outer-row probe), so the recheck is
folded back in as key pairs on the join predicate, which is evaluated on the
merged outer++inner row where the layout translation is well defined. The plan
shape could not be forced on a live server (`enable_indexscan` /
`enable_bitmapscan` are declared-but-unconsumed GUCs, and the plan cache serves
the earlier plan for the same SQL text anyway), so the guard is a planner unit
test: `TestCreateNestLoopBitmapJoinRechecksProbeClause`
(`internal/optimizer/createplannl_bitmap_recheck_test.go`), proven red against
the pre-fix code — `Predicate is nil`.

### OP1-1 — BUG (fixed, guard added)

Confirmed against PG 18.3: `COPY t FROM STDIN (ON_ERROR stop, REJECT_LIMIT 5)`
raises `COPY REJECT_LIMIT requires ON_ERROR to be set to IGNORE` there, while
goopg accepted it and silently ignored the limit. Upstream's test is
`opts_out->reject_limit && !opts_out->on_error`
(postgres/src/backend/commands/copy.c:960) and `COPY_ON_ERROR_STOP` is the enum
zero value, so an explicit STOP reads exactly like an omitted ON_ERROR — only
IGNORE licenses REJECT_LIMIT. goopg tested `!onErrorSpecified`, which the
explicit form passes. Fixed to `(!onErrorSpecified || onErrorIsStop)`.

Guard: a new row in `TestPlanCopyIncorrectOptions`
(`internal/optimizer/copy_test.go`), red before the fix ("expected error").

### OP2-1 — BUG (fixed, guard added)

Confirmed: `SELECT CASE NULL::int WHEN 1 THEN 1/0 ELSE 42 END` returns 42 on
PG 18.3 and raised `ERROR: division by zero` on goopg. `toLiteralValue` does not
cover `*NullConst`, so the simple-CASE constant shortcut was skipped and every
dead THEN was folded as "potentially reachable". A NULL operand makes every
branch dead (`NULL = x` is NULL for any x, `WHEN NULL` included), so the fix
skips those branches outright — the same outcome PG reaches from the other
side, by only taking its constant-CASE path for a non-null `Const` arg. The
NULL test sees through the casts a typed NULL literal leaves behind
(`isNullConstExpr`).

Guard: `TestFoldSimpleCaseNullOperandSuppressesDivisionByZero`
(`internal/optimizer/foldconst_test.go`), covering both the typed and untyped
operand; red before the fix (panic → plan error).

Side finding, filed as row **X-1** and NOT fixed here: the same plan-time
eagerness hits `SELECT COALESCE(1, 1/0)` — PG returns 1, goopg raises division
by zero. It is a different folder (not `foldCaseExpr`) and out of this row's
scope.

### NP-2 — NOT A BUG

PG's own plpgsql performs the identical reconstruction. Asking both engines for
`SELECT INTO x FROM no_such_tbl` inside a function, PG 18.3 reports
`QUERY: SELECT        FROM no_such_tbl_np2` — i.e. it also cuts the INTO
targets out of the text and leaves an empty select list — and `SELECT FROM t`
has been legal SQL since 9.4, so nothing is malformed. goopg parses the same
text and raises the same 42P01. Value-wise the two agree as well:
`SELECT INTO x FROM np2` leaves `x` NULL on both.

Two unrelated divergences surfaced while probing this and are filed as rows
**X-2** and **X-3** (both unfixed, outside this review's scope): `'x' || true`
returns `xt` on goopg vs `xtrue` on PG, and plpgsql `FOUND` stays false after a
successful `SELECT … INTO` (`7/found=false` vs PG's `7/found=true`).

### NP-6 — NOT A BUG (unimplemented feature)

Confirmed missing, and the report says so itself ("out of scope per the AST
comments … a documented limitation rather than a regression"). `<<top>> LOOP …
EXIT top WHEN … END LOOP` returns 3 on PG 18.3 and fails on goopg at CREATE
FUNCTION time with `plpgsql: syntax error at byte 27: unsupported PL/pgSQL
statement`. It fails loudly at definition time — there is no silent
misbehaviour to fix — and statement labels (`<<lbl>>`, `lbl:`, `EXIT lbl`,
`CONTINUE lbl`) are a parser+executor feature, not a defect in existing code.

### CM-4 — NOT A BUG

`encoding/csv` cannot hand these loaders a short row. `Reader.FieldsPerRecord`
defaults to 0, which locks the count to the header's on the first `Read`, and
every later record with a different count fails with `wrong number of fields`
(verified with a standalone probe: header `a,b,c` + row `1,2` → `rec=["1" "2"]
err=record on line 2: wrong number of fields`). All three loaders return that
error from the `read csv row` wrap, so the `row[statusIdx]` access is reached
only for rows at least as long as the header, and `statusIdx` comes from the
header. The adjacent `itemPathIdx >= len(row)` checks the report contrasts with
are themselves dead for the same reason.

### CM-5 — BUG (fixed, guard added)

Real, and the worse case is the one the report mentions second: with both Atoi
errors dropped, `--queries 5-` parsed `hi` as 0, the `for q := lo; q <= hi`
loop ran zero times, and the tool audited **no queries at all** while exiting
successfully — a silent no-op, not just a stray negative index. Fixed by
requiring both bounds to parse, rejecting `lo < 1` and `hi < lo`, and failing on
an unparsable bare number instead of skipping it.

Guard: `cmd/estimate-audit/main_test.go` — `TestSelectQueriesParsesValidSpecs`
pins the accepted forms and `TestSelectQueriesRejectsMalformedSpecs` re-execs
the test binary for each bad spec (`5-`, `-5`, `3-x`, `abc`, `0`, `9-4`) and
requires a non-zero exit, since `selectQueries` reports through `fatal`.

### CM-6 — NOT A BUG

PostgreSQL has no right-unary (postfix) operators any more: `CREATE OPERATOR`
answers a missing right argument with `operator right argument type must be
specified` / `DETAIL: Postfix operators are not supported.`
(postgres/src/backend/commands/operatorcmds.c:184), and the oracle's
`pg_operator.dat` carries `oprkind => 'l'` 41 times and no `'r'` at all (the
rest default to `'b'`). The generator reads that file, so the `'r'` branch
cannot be reached from any PG 18 tree; adding it would be dead code.

### CP-5 — BUG (fixed, guard added)

Real, and confirmed at the wire against PG 18.3 by connecting with a
`replication=` startup parameter: PG answers `bogus`, `truex`, `databas` and
`2` with `FATAL: invalid value for parameter "replication": "…"` plus
`HINT: Valid values are: "false", 0, "true", 1, "database".`, while goopg
completed the handshake and ran `select 1` for all of them. `off` / `no` / `of`
/ `n` connect normally on PG (parse_bool takes any non-empty prefix of
true/false/yes/no/on/off, case-insensitively, plus 1/0) but were routed to the
walsender path by goopg's "anything not in {0,false,FALSE,False} is
replication" rule.

Fixed by mirroring `backend_startup.c:751`: `database` first, then
`parse_bool_with_len`'s prefix rule (`parseStartupBool`), and a third result —
"unparsable" — which the startup path answers with the same FATAL 22023 and
HINT (new `writeFatalHint`). Out of scope and left alone: goopg's replication
connections still fall back to the SQL dispatcher, where PG says `cannot
execute SQL commands in WAL sender for physical replication`; the report notes
that fallback as intended.

Guard: `internal/postmaster/replication_startup_param_test.go` — a table of 22
spellings, every one checked against the oracle, plus a wire test asserting the
FATAL/22023/HINT for `replication=bogus`. Red before the fix (the wire half read
message type 'R', i.e. the handshake proceeded).

### UT-7 — NOT A BUG

The redundant call is real: on rollback `invokeOnChange` fires for every
journalled key even when the restored value equals the pre-restore one, while
the `onReportableChange` sibling two lines above is gated on `after != before`.
It has no observable effect, though. Every registered callback is an idempotent
per-value setter — `enable_nestloop_index` / `enable_memoize` /
`enable_presorted_aggregate` / `enable_hashagg` (cmd/goopg/main.go) assign a
planner flag, and `application_name` / `track_io_timing` /
`backend_flush_after` (internal/postmaster/server.go) write one field of the
calling backend's activity slot. Re-assigning the value a variable already
holds changes nothing and costs a map lookup per rolled-back SET. Left as-is;
gating it would be cosmetic.

### XL-1 — NOT A BUG (misread signature)

`emitWithPageHeaders` returns `(out []byte, leading int)`
(`internal/access/transam/xlog/xlog_emit.go:62`) — there is no error to
propagate. The discarded second value is the leading page-header byte count,
which this composer does not need: `AppendBuiltEmitted` hands `leading` to the
build closure as an *input*, computed by `predictEmittedSize` before the record
is built. Nothing is swallowed.

### XL-2 — NOT A BUG (unmounted legacy path, and the live path already does this)

The observation describes `reserveLocked`, which serves `reserve` /
`reserveAndPublish` → `stripeAppend` / `stripeAppendBuild` — none of which any
production caller reaches. The live PG-compat write path is
`state.append → core.AppendXLogPayload → AppendBuiltEmitted →
reserveEmittedAndPublish`, and that path already implements exactly the
requested rule: it takes the hook's `padded` result and advances `t.prev` only
when a real pad record was written, with a long comment explaining that a gap
below `xlogMinimumRecordSize` is zero-filled (PG's AdvanceXLInsertBuffer
behaviour, M0131-S30.6) and must not be linked to. `reserveLocked`'s own
comment already records the divergence as deliberate for the legacy path.
Nothing to fix; if the legacy path is ever mounted it should be deleted rather
than patched.

### CP-3 — BUG, worse than reported (fixed, guard added)

The report calls the observable result "currently correct"; it is not. With
`fb := nextXID - storage.TransactionID(eff)` in uint32, every cluster younger
than `vacuum_freeze_min_age` (the default 50M — i.e. essentially every goopg
cluster that has ever run) wrapped to a value near 2^32, and the very next line,
`if fb > oldestXmin { fb = oldestXmin }`, rewrote that to oldestXmin. So goopg
froze EVERY dead-to-all tuple on every autovacuum pass, where PG freezes none:
upstream keeps the wrapped value and compares it with `TransactionIdPrecedes`
(vacuum.c:1204-1209), whose modular ordering places it 50M transactions in the
past, so no tuple qualifies. The `if fb < 0` line is dead as reported.

Fixed by computing in signed space and returning 0 — goopg's `FreezeBelow`
consumer (`internal/commands/vacuum`) compares plainly (`xmin < FreezeBelow`)
and treats 0 as "freezing off", which is how the wrapped-limit outcome has to be
expressed here. Reproducing PG's wrapped value literally would require
wraparound-aware comparisons throughout the freeze path; that is a separate
piece of work.

Guard: `internal/postmaster/autovacuum/freeze_cutoff_test.go` — six points
across the young/mature/clamped/reloption space; red before the fix (fresh
cluster returned 90 instead of 0).

### CP-4 — BUG (fixed, guard added)

Real, and the damage outlives the frame: `endLSN := nextLSN + len(p) - 1`
underflows on an empty write, so the adapter emitted a 30-byte `'w'` frame with
EndLSN ≈ 2^64 AND set `nextLSN = endLSN + 1 = 0`, after which the next real
message was advertised as `StartLSN=100 EndLSN=99` — the LSN stream is
corrupted from then on, not just for the empty frame. Fixed by returning
`(0, nil)` for an empty payload before any LSN arithmetic.

Guard: `TestWalsenderPgoutputAdapterEmptyWriteIsDropped`
(`internal/replication/logicalwalsender_test.go`) — nothing on the wire, nextLSN
unmoved, and the following write still framed 100..102. Red before the fix on
all three.

### IN-1 — NOT A BUG

`BackupControlImage` has exactly one caller (`internal/backup/basebackup.go:334`)
and it sits inside `if redoLSN > 0`. The companion `ckptRecLSN - 1` on the next
line is guarded too — lines 292-293 replace a zero `ckptRecLSN` with `redoLSN`
before either subtraction. The underflow is unreachable.

### IN-4 — NOT A BUG

3764 really is listed twice, but the list is consumed only by
`bootstrapMappedLocalCatalogHeaps`, which does `os.WriteFile(path, heapPage)`
per OID: writing the same empty 8 KiB page twice is idempotent. What is wrong is
the COMMENT on the first of the two entries ("pg_ts_config (stale — true
pg_ts_config OID is 3602)"): 3764 is pg_ts_template, and 3602 is already listed
on its own line. Cosmetic; no behaviour to fix.

### IN-5 — NOT A BUG

`snap.Xmax` is `m.xidgen.Peek()` (`captureSnapshot`, manager.go:1355) — the next
XID to hand out, which starts at `FirstNormalTransactionID` and only ever moves
forward. `Xmax == 0` cannot occur, so `uint32(snap.Xmax - 1)` cannot wrap.

### ST-10 — NOT A BUG

The race is real but the counter is a writeback HINT scheduler, not an
accounting record: an increment lost between `Add` and `Store(0)` delays the
next `sync_file_range` hint by at most one threshold's worth of blocks, and the
kernel writes the pages out regardless. Upstream's equivalent
(`ScheduleBufferTagForWriteback` / `IssuePendingWritebacks`, bufmgr.c) resets
`nr_pending = 0` without any atomicity either — it is per-backend there, which
is exactly why the pattern is not written to be race-free. No correctness
consequence; not worth a lock on this path.

### CM-2 — NOT A BUG

`sc.replayer` cannot be nil: `startStandbyReplayer` (cmd/goopg/main.go:958) has
no nil return path, and `standbyController` is only ever constructed by
`startStandby`, which assigns its result. The nil checks at lines 219/238/298
are defensive, not evidence of a reachable nil, so the un-guarded
`sc.replayer.ApplyLSN()` in the "replay drained" log line is safe. Left as is
rather than adding a fourth guard for a state that cannot exist.

### NP-3 / NP-4 — NOT A BUG (both)

Both ends of the Const datum round-trip are already closed against the shapes
the report worries about.

NP-3: `outDatum`'s by-value arm indexes `c.Datum[0..7]` unconditionally, so a
by-value Const whose Datum is shorter than 8 bytes would panic — but no such
Const can be built. Every by-value constructor in `internal/nodes/datum.go`
fills the datum through `byvalWord`, which always returns 8 bytes, and there is
no assignment to `ConstByval` anywhere outside those literals. The byte count is
also PG-correct: upstream prints `datumGetSize` (== typlen for by-value) as the
length and then `sizeof(Datum)` == 8 bytes of body (outfuncs.c:355-362), which
is what the Go code does with `ConstLen` + 8 bytes.

NP-4: `readDatum` already forces `n = 8` for by-value and `n = 0` for
`length <= 0` (readfuncs.go:219-224), so a negative or zero declared length on a
by-reference datum reads an empty body rather than looping negatively. The
by-reference nil case also matches upstream's literal `0 [ ]` rendering
(outfuncs.c:366-367) because `len(nil) == 0`.

### OP1-4 — NOT A BUG

Returning 1 when a unique index has every key column pinned by equality is
exactly PG's `btcostestimate` shortcut, and additional range bounds cannot make
the answer smaller in a model that clamps at one row (`clamp_row_est`). The
extra `lowKey`/`highKey` are redundant restrictions on a set already known to
hold at most one row.

### OP1-6 — NOT A BUG

`LIMIT 0` does return 0 from `EstimateRows`, but no consumer turns that into a
wrong answer: PG itself floors the Limit node at one row, and goopg prints the
same. Oracle vs goopg, both `Limit … rows=1`:

```
explain select * from t limit 0;
explain select * from (select * from t limit 0) s join t on s.a = t.a;
```

Since the observable estimate already agrees with PG, changing the internal
sentinel would be churn.

### UT-6 — NOT A BUG

Run-together times ARE expanded — just not in `padTimeFields`. Every form the
report names round-trips identically on goopg and the PG 18.3 oracle:

| input | PG | goopg |
|---|---|---|
| `time '040506'` | 04:05:06 | 04:05:06 |
| `time '0405'` | 04:05:00 | 04:05:00 |
| `time '040506.25'` | 04:05:06.25 | 04:05:06.25 |
| `time '0405 PM'` | 16:05:00 | 16:05:00 |
| `timetz '040506+05'` | 04:05:06+05 | 04:05:06+05 |
| `timestamp '20010203 040506'` | 2001-02-03 04:05:06 | 2001-02-03 04:05:06 |
| `time '04'` | error 22007 | error 22007, same text |

### ES-1 — BUG, and wider than reported (fixed, guard added)

The report frames this as a parallel-combine concern; it is a wrong-results bug
in the SERIAL path too. `aggRuntime` keeps two accumulators for sum/avg — `sum`
for KindInt arguments, `numericSum` for KindNumeric ones — and
`finishBuiltinAgg` returns the numeric lane whenever it is live, so everything
the int lane held is discarded. Any group whose argument Kind varies per row
mixes the lanes:

```
create table es1(a int); insert into es1 select g from generate_series(1,4) g;
select sum(case when a = 1 then 1 else 1.5 end),
       avg(case when a = 1 then 1 else 1.5 end) from es1;
PG 18.3 : 5.5 | 1.3750000000000000
goopg   : 4.5 | 1.12500000000000000000
```

`combineNumericSum` is the second way into the same state (it adds the int
lanes, then takes src's numeric lane wholesale), which is what the report saw.

Fixed at the finalizer instead of at either producer: `foldIntLaneIntoNumeric`
merges the int lane into the numeric one before the sum/avg arms read it, so
both the serial path and every combine order land on the same total. The
divergent avg scale in the trace above was a symptom of the same defect — with
the lanes folded, goopg's avg matches PG's rendering exactly.

Guard: `internal/executor/agg_mixed_lane_test.go` — the mixed-lane group at 1,
2 and 4 partials, plus the two single-lane shapes so the fold cannot disturb
them. Red before the fix on every mixed row (4.5 / 1.125…).

### ES-2 — NOT A BUG

The `break` guards a length mismatch that cannot arise: every group's state
slice is `make([]aggRuntime, len(o.plan.Aggs))` (operators_join_agg.go:2022,
2077, 2494) and the `aggs` argument merge is called with is that same
`o.plan.Aggs`, from the same plan node, in every worker. There is no path that
reaches merge with differently-sized slices.

### ES-3 — BUG (fixed, guard added)

`dropTable` cleared `shared`, `pending`, `staging` and `prepared` but left the
`triggers` map — the autovacuum inputs (n_dead_tup, n_ins_since_vacuum,
n_mod_since_analyze) — untouched. Upstream drops the whole
`PgStat_StatTabEntry` in `pgstat_drop_relation`, trigger inputs included. The
leftover entry both leaks and, on an OID a later relation inherits, hands that
relation a head start toward an autovacuum it never earned. One `delete` added
alongside the other four.

Guard: `TestRelStatsDropTableClearsAutovacuumTriggers` — red before the fix with
`dead=40 ins=100 mod=140` surviving the drop.

### OP1-5 — BUG (fixed, guard added)

Confirmed and worth fixing: `compute_bitmap_pages`' lossiness correction raises
`tuples_fetched` (costsize.c:6564-6588) because a lossy page forces the heap
node to recheck every tuple on it, and goopg computed the correction into
`lossyTuples`/`exactTuples` and then discarded both with `_ =`. The caller
charges `cpu_tuple_cost * tuplesFetched`, so every work_mem-bound bitmap scan
was under-priced in exactly the regime where it should start losing to a seq
scan.

The discarded formula was also not PG's — it keyed off the loop-prorated
`pages` instead of the single-scan `heap_pages`, compared against `maxEntries`
rather than `maxEntries / 2`, and approximated tuples-per-lossy-page as
`T/pages`. The port now follows upstream line for line, and
`computeBitmapPages`/`computeBitmapPagesLooped` return the adjusted tuple count
(taking `relTuples`) so all six call sites cost the heap side with it.

Guard: `TestComputeBitmapPages_LossyAdjustment` — the arithmetic worked through
in the test body (400 pages, 15125 tuples), plus the two non-lossy shapes
(budget >= heap pages, and the unlimited budget) that must leave the estimate
alone. The test previously asserted nothing at all: it ended with `_ = pages //
at minimum, it should not panic`.

### ES-4 — NOT A BUG (but the function had a real hole next door: X-4)

The numeric arithmetic is right. Checked against the PG 18.3 oracle,
`atttypmod` for `numeric(10,2)` is 655366 on both engines, `numeric(10)` 655364,
bare `numeric` -1, and `varchar(5)`/`char(5)`/`bit(3)`/`interval hour to minute`
all agree too.

What the same function was missing is the whole time/timestamp family — see X-4
below, fixed in the same commit as it was found in this function's body.

### X-4 — BUG found while checking ES-4 (fixed, guard added)

`pgAttTypmod` had no arm for `time`/`timetz`/`timestamp`/`timestamptz`
(1083/1266/1114/1184), so every one of them reported `atttypmod = -1`:

| column | PG 18.3 | goopg (before) |
|---|---|---|
| `time(3)` | 3 | -1 |
| `timetz(4)` | 4 | -1 |
| `timestamp(2)` | 2 | -1 |
| `timestamptz(5)` | 5 | -1 |

Their typmod is the fractional-seconds precision stored raw (`anytime_typmodin`
/ `anytimestamp_typmodin` return `p` with no VARHDRSZ offset). Any client that
rebuilds a column's type from the catalog — pg_dump first among them — silently
dropped the precision.

Guard: `TestPgAttTypmodEveryTypmodCarryingType` — all twelve typmod-carrying
spellings, oracle-pinned. Red before the fix on exactly the four rows above.

- 2026-09-01 **ES-8 = BUG (fixed, guard added).** `regIdentifierInput`'s
  `regtype` arm split `schema.name` only so the bare name would reach
  `TypeNameToOID`/`userTypeOIDForName` — the schema half was then discarded.
  PG's `regtypein` hands the parsed name to `parseTypeString` ->
  `LookupTypeName`, which resolves the namespace FIRST (3F000 if it does not
  exist) and then searches that namespace only. Oracle (PG 18.3, values pushed
  through a `regtype` column so the literal-cast path is not in the way) vs the
  pre-fix build: `pg_catalog.int4` 23 / 23, `public.int4` error / 23,
  `nosuchschema.int4` error / 23, `nosuchschema.ct` error / the OID of
  `public.ct`. The sibling `regclass` arm was checked and already gets this
  right (`nosuchschema.e9` -> 42704, `public.pg_class` -> error), so the defect
  is confined to `regtype`. Fix: raise 3F000 for an unknown schema, let a
  built-in answer only to `pg_catalog` (or no qualifier), and fall back to the
  user-type lookup for any other existing schema. That last step stays a
  bare-name lookup on purpose: goopg's catalog has no per-schema type
  namespace at all (a second `CREATE TYPE s2.ct` is rejected as a duplicate),
  so per-schema user-type resolution is a separate, larger gap and not this
  row. Guard `internal/executor/reg_identifier_regtype_schema_test.go` pins the
  eight spellings; reverting the hunk turns four of them red.

- 2026-09-01 **TA-3 = BUG (fixed, guard added).** Confirmed by reading
  `internal/access/transam/manager.go:262`: the variadic auto-assign branch
  initialises the slot and `return`s at line 302, and the isolation switch sat
  at line 304 — reachable only when the caller passed an explicit `procNum`.
  So `Begin(IsolationLevel(99))` returned `{Handle:2 Isolation:unknown(99)}`
  while `Begin(IsolationLevel(99), 3)` errored. The second half is worse than
  the asymmetry: the auto-assign branch CASes `inTxn` to 1 before it returns,
  so any error return after that point abandons a proc slot (the guard
  measures this — the pre-fix run shows free slots 1024 -> 1023). Fix: hoist
  the switch to the top of `Begin`, ahead of both branches and ahead of the
  CAS. No production caller is affected (all pass a parsed level), which is
  why this stayed latent. Guard
  `internal/access/transam/begin_isolation_validation_test.go` pins both
  branches rejecting, no slot leak, and the three valid levels still passing.

- 2026-09-01 **IN-3 = BUG, latent (fixed, guarded).** Both startup WAL
  scanners in `internal/initdb/xact_recovery.go` — `replayCLogFromWAL` and
  `walHasXactRecords` — switched on `r.Payload[0]` as a goopg RecordKind
  without asking `xlog.IsGoopgNativeRecord` first. That function's own
  documentation names these scanners as callers that must ask: a structurally
  real PG record carries raw struct bytes at that offset (a checkpoint's
  redo-LSN low byte, say) which can equal `RecordKindXactCommit`, and the
  scanners would then stamp an unrelated XID committed / force the
  crash-recovery branch on a cluster with no transaction history. It is latent
  today only because PG-format records reach these loops with a nil `Payload`.
  Fix: one shared `nativeKindByte(r, minLen)` predicate used by both loops.
  Guard `internal/initdb/xact_recovery_native_guard_test.go` feeds it a
  PG-format record whose payload byte collides; it is accepted without the
  fix.

- 2026-09-01 **IN-6 = NOT A BUG.** The claim is about CRLF line endings and
  non-`\N` NULLs in the embedded TSV captures. All seven git-tracked TSVs
  under `internal/initdb/` are LF-clean (`grep -c $'\r'` = 0 on each), and
  they are `go:embed`ed from the repo, so no runtime input path exists — a
  Windows-edited capture is a re-capture hygiene question, not a defect in
  `infoSchemaTableRows`. A CRLF file would also not corrupt silently: the
  trailing `\r` lands in the last column's string and the field-count check
  fires on any row whose column count shifts. No change.

- 2026-09-01 **ST-9 = BUG (fixed, guard added).** `pokeWake` writes the
  close-time wake NOP straight at `tail & sqMask` with no fullness check.
  Every other submission goes through `Submit`, which first takes a token
  from `m.slots` — sized to `sqEntries` (`method_iouring_linux.go:269`) — so
  the ring can be exactly full of unconsumed SQEs when `Close` pokes. The NOP
  then overwrites a live read/write SQE, and that operation's completion
  never arrives. Fix: `sqRingFull(tail)` (unsigned `tail-head >= sqEntries`,
  which is wrap-safe because both counters are free-running uint32s) and an
  early return — a full ring has ops in flight whose CQEs wake the reaper
  anyway, so nothing is lost. Guard
  `internal/storage/aio/iouring_sq_full_test.go` pins the arithmetic across
  the wrap and pins `pokeWake` leaving a full ring's SQE untouched.

- 2026-09-01 **NB-3 = BUG (fixed, guard added).** `readInternalFirstChildBlock`
  decoded the downlink as `binary.LittleEndian.Uint32(raw[2:6])`. A pivot
  tuple's downlink is a `t_tid`: `bi_hi` at [0:2], `bi_lo` at [2:4], with the
  offset/status half at [4:6]. The expression therefore dropped the high 16
  bits of the block number and folded the status half in. The review calls it
  dead code and it is — the function had no callers, which is why nothing
  noticed. Fix: call the shared `BTreeTupleGetDownLink`. Guard
  `internal/access/nbtree/internal_first_child_test.go` is now its caller; it
  stamps a downlink that uses both halves plus a non-zero status half, where
  the old expression returns 0x72345 for 0x12345. (Note the weaker first
  draft of this test passed against the bug: a small tree only produces block
  numbers under 65536 with a zero status half, where the two decoders agree.)

- 2026-09-01 **NB-4 + NB-5 = BUG (fixed together, guards added).** The
  rightmost-leaf insert cache was dead. `descendToLeaf` stored the block only
  when `op.Next == 0`, but `readOpaque` maps the on-disk `P_NONE` sibling to
  `storage.InvalidBlockNumber` (`legacySibling`, pgpage.go:297), so the
  condition was never true on a rightmost page; `tryInsertOnCachedRightmost`'s
  mirror test `op.Next != 0` would have called every entry stale anyway. Every
  append-shaped insert paid a full descent. Fixing the sentinel is NB-4; doing
  only that would arm a path that has never executed, which is exactly NB-5:
  the entry point never checked for a deleted / half-dead page, so an insert
  could land on a page `unlinkEmptyLeaf` is about to unlink and
  `pinNewOrRecycled` re-initialise under a different key range. The check also
  covers an incomplete split, which upstream's `_bt_findinsertloc` resolves via
  `_bt_finish_split` before inserting and which only the descent path here
  knows how to handle. Both land in one commit because the first without the
  second is the corruption. Guard
  `internal/access/nbtree/rightmost_cache_test.go` pins the cache being
  populated and actually taken (the cached leaf gains the next ascending key
  without a descent) and the three unlinkable page states missing to the
  descent with the cache cleared.

### UT-5 — BUG (fixed, guard added)

`Counter.Add` indexed the 256-entry shard table with `runtimeshim.PinP()`,
whose result is bounded by `GOMAXPROCS`, not by `maxShards`. On a host running
with `GOMAXPROCS > 256` the very first `Add` panics with `index out of range
[256] with length 256` — and every `pg_stat_*` counter in the server goes
through this path. The index is now folded with `pid & (maxShards-1)` (maxShards
is a power of two) via a new `shardFor` helper: two Ps then share a cache line,
which costs a little contention but never a wrong or missing count. Guard:
`TestCounterShardForFoldsHighPIndexes`
(`internal/utils/activity/stats/counter_shard_bound_test.go`), proven red — it
panics against the pre-fix indexing.

### ST-4 — BUG (fixed, guard added)

`Pool.WriteDirtyPages` advanced the bgwriter's independent sweep cursor with
`p.bgwriterHand = (start + n) % n`, which is just `start` — a no-op. Every tick
restarted the sweep at the same origin, so the bgwriter kept re-cleaning the
lowest-indexed dirty buffers and the buffers past its first `maxPages` victims
were only ever written by the checkpointer or by a foreground eviction. Upstream
`BgBufferSync` advances `next_to_clean` by the number of buffers scanned; the
cursor now does the same (`start + scanned`), set after the scan so it reflects
what the tick actually examined. Correctness was never at risk — this is a
fairness/latency divergence. Guard: `TestWriteDirtyPagesAdvancesScanCursor`
(`internal/storage/bgwriter_cursor_test.go`), proven red — `bgwriterHand stayed
at 0`.

### TA-6 — NOT A BUG

`StatusesConflict` does index `statusHWLock[held]` unguarded, but no `Status`
value in the tree originates outside the package's own constructors:
`lockOnlyMemberStatus` / `updaterMemberStatus` (`operators_lockrows.go`) map
infomask bits onto the six defined constants, and `store.CreateFromMembers`
rejects anything failing `Status.Valid()`. There is no SLRU/on-disk member
decoder yet, so no untrusted byte can reach the table. Upstream indexes
`MultiXactStatusLock` exactly as unguardedly in `DoLockModesConflict`, so
adding a bounds check here would be a divergence, not a fix. Revisit if and
when the on-disk member decode lands.

### ST-6 — NOT A BUG

`Bgwriter.Stop` would indeed panic on a second call (`close` of a closed
channel) and hang forever if called without `Start` (`<-b.done` never fires).
Neither is reachable: the sole production owner (`initdb/open.go`) constructs
the bgwriter and calls `Start()` immediately in the same branch, and its
shutdown path is `if r.bgwriter != nil { r.bgwriter.Stop(); r.bgwriter = nil }`.
Left as an API-robustness note rather than a fix.

### ST-5 — NOT A BUG

`bufmap.Lookup`'s safety bound is `dist <= size` where the sibling `Insert` /
`Delete` / `compact` loops use `i < size`, so Lookup probes one bucket more
than the table holds. The extra probe revisits the home bucket, which was
already examined and cannot have become the answer (a matching key would have
returned on the first visit, and an empty bucket would have terminated), so the
result is identical either way — the inconsistency is cosmetic, not a
correctness or termination defect.

### XL-3 — NOT A BUG (but see XL-5)

The claim is that the column-offset walk mis-advances on NULL columns. It does
not: a NULL column occupies no bytes in a PG physical tuple, so skipping it
without advancing `off` is correct, and the alignment for the next stored
column is applied when that column is decoded. `encodePgoTuplePhysical` is
structurally identical to its executor sibling
(`decodeRowIntoMctxPGTupleLowered`) here — same null-bitmap test, same
`i >= storedNatts` short-circuit, same align-then-decode order. Checking that
equivalence did surface a real divergence in the alignment table itself,
filed as XL-5.

### XL-5 — BUG (fixed, guard added) — new finding

`pgoPhysicalAlign` carried a hand-copied subset of the executor decoder's
alignment table, and it had drifted: `pg_lsn`, `xid8`, `smallserial`/`serial2`,
`serial8` and `anyarray` all fell through to the default 4 instead of their
`pg_type` typalign (8, 8, 2, 8, 8). A logical-replication tuple containing any
of those columns was therefore read at the wrong offset, corrupting that column
*and every column after it* on the replication wire — the classic
sibling-paths-must-agree failure. Rather than re-copying the table, it is lifted
into `catalog.PhysicalTypeAlign` / `PhysicalTypeAlignName` (catalog is already
imported by both sides, and neither import direction creates a cycle);
`executor.physicalPGTypeAlignLowered` and `pgoPhysicalAlign` are now thin
callers, so the two paths cannot drift again. Guard:
`TestPgoPhysicalAlignMatchesPGTypalign`
(`internal/access/transam/xlog/pgoutput_align_test.go`), whose expectations are
the typalign values in `postgres/src/include/catalog/pg_type.dat`; proven red
against the pre-fix table — six of the twelve cases failed.

### EO1-2 — BUG (fixed, guard added)

`limitOp.Open` resets the per-execution counters but not the bounds themselves.
A correlated `LIMIT`/`OFFSET` — `SELECT (SELECT … LIMIT o.n) FROM o`, legal SQL
and re-evaluated on every rescan — that evaluates to NULL for a later outer row
means "no limit"/"no offset", but the NULL arms only skip the assignment, so
the previous outer row's bound stayed in `o.limitCount` / `o.offsetCount` and
the unlimited execution silently returned the earlier row count. `Open` now
resets them to `-1` / `0` before evaluating. Guard:
`TestLimitOpReOpenWithNullLimitDropsPreviousBound`
(`internal/executor/limit_reopen_null_test.go`), proven red — the second
execution returned 2 rows instead of 4.

### EO1-4 — NOT A BUG

`lookupBounds` does dereference `o.plan.Index.Columns[0]` unguarded, but a
zero-key-column index cannot exist in goopg's catalog: every `CREATE INDEX`
path records at least one column name, and expression indexes — the one shape
that could plausibly produce an empty column list — are not represented in the
catalog at all (see the comment in `optimizer/pathparamindex.go`, "an
expression index is not a thing goopg's catalog has"). The `len(idx.Columns) ==
0` guards in the sibling readers (`pgindex_keydesc.go`, `pgindex_tuplekey.go`,
`operators_bt_index_check.go`) are defensive, not evidence of a reachable case.
Revisit if expression indexes ever land.

### TA-5 — NOT A BUG

`CLog.GetStatus` does dereference `c.pool.Load()` without a nil check while
`Flush` / `SetFlushWALHook` / `SetFsyncDisabled` guard theirs, but that
asymmetry is the documented contract, not an oversight: `OpenCLog` returns a
pool-less shell and `EnablePGSLRUMirror` — which creates the pool — "MUST run
before any" read or write (clog.go:83), and both production callers
(`initdb.Open`, `initdb.initdb`) do exactly that at startup, before the server
accepts connections. The nil-safe siblings are the *setters*, which are legal
to call before the enable step precisely so that call order does not matter. A
nil pool inside `GetStatus` would be a startup-ordering bug, for which a panic
is a better signal than silently reporting every transaction in-progress.

### TA-4 — BUG (fixed, guard added)

`AcquireConnSlot`'s rotating scan cursor is a free-running `atomic.Int32`.
After 2^31 cumulative connections it wraps negative, and `1 + (start+off) %
int32(sz-1)` then evaluates to zero or a negative index — the process panics
with `index out of range [-701]` (verified), i.e. every session on the server
dies. 2^31 connections is reachable for a long-lived server (~24 days at 1000
connections/s). The rotation is now reduced through `uint32`, which keeps it in
`[0, sz-1)` for every cursor value. Guard:
`TestAcquireConnSlotSurvivesCursorWraparound`
(`internal/access/transam/conn_slot_wrap_test.go`), which parks the cursor at
`MaxInt32-2` and acquires across the wrap; proven red — it panics against the
pre-fix arithmetic.

### UT-3 — BUG (fixed, guard added)

`convertUnit` computed `n * mul / div` unchecked, so a unit-suffixed GUC value
that overflows int64 wrapped silently: `work_mem = '9000000TB'` became
-8350722093481984 and `statement_timeout = '9223372036854775d'` became
-69811200. Whether that is rejected downstream then depends on the wrapped
value landing outside the GUC's min/max — a wrapped value inside the range is
simply *accepted*, setting a parameter the user never asked for. PG 18.3
answers both with `ERROR: invalid value for parameter … HINT: Value exceeds
integer range.` (verified against the oracle on port 65438), which
`convert_to_base_unit` produces via `pg_mul_s64_overflow`. The multiply is now
checked in a new `scaleUnit` helper. Guard:
`TestParseIntWithUnitRejectsOverflow`
(`internal/utils/misc/guc_unit_overflow_test.go`), which also pins the
still-valid `8MB`/`1500ms` conversions; proven red — all three overflow cases
returned wrapped negatives.

### EO1-5 — BUG, duplicate of EO2-3 (fixed in 5c60e8551)

Same defect, same line: `generateSeriesOp.Next` advanced with a bare
`o.current += o.step`, which wraps at the int64 ceiling back inside the bounds
so the series never terminates. Fixed together with the zero-step check under
EO2-3; the guard is `internal/executor/generate_series_overflow_test.go`.

### EO1-6 — NOT A BUG

`execDropTablespace`'s "not empty" guard is indeed inside an
`o.ctx.Catalog.(*catalog.InMemory)` type assertion, but that assertion is the
repo-wide idiom — around 300 sites in `internal/executor` alone do the same —
because `*catalog.InMemory` is the only Catalog implementation the server ever
installs (`postmaster/server.go` passes `cfg.Catalog` straight through). The
guard therefore always runs in production. If a second implementation ever
lands, this is one of ~300 places to revisit, not a defect of this function.

### EO1-8 — NOT A BUG (dead path)

The second column-building loop in `execCreateTable` — the one labelled
"Fallback: no BodyOrder (e.g. empty column list or old path)" — does build
`NotNull: c.NotNull` without the `|| c.IdentityColumn || isSerialCol` term the
live `addCol` path uses (and drops `IsArray` and the identity fields too). It
is unreachable: both parser front ends append to `BodyOrder` for every column
they append to `Columns` (`parser/ddl.go:3815,3896`,
`parser/yacc_parser.go:16415`), and no other code in the tree constructs a
`parser.CreateTableStmt`, so a statement with columns always has a non-empty
`BodyOrder`. Verified behaviourally as well: `CREATE TABLE s (a serial)` /
`bigserial` / `smallserial` all land `NotNull=true` through the live path.

### EO2-8 — BUG (fixed)

Confirmed against the live PG 18.3 oracle: `SHOW ALL` there is
`name | setting | description` (guc.c `ShowAllGUCConfig`), while goopg emitted
two columns — the third was deliberately deferred ("leave description for
milestone 5 (catalog) work" in `postmaster/query.go`). A client reading the
third field by index got a short row. Fixed in all four places that shape the
result: the plan schema (`optimizer/plan.go` `Utility.Output`), the executor
operator (`executor/operators_utility_settings.go` `nextShow`), and the simple
and extended protocol paths (`postmaster/query.go` `handleShowAll` +
`showAllFields`, `postmaster/extended.go` — 3 sites). The description text is
read from the `pg_catalog.pg_settings` virtual view's `short_desc` column, so
it stays byte-identical to PG for every GUC that view already carries; GUCs it
does not carry yet get an empty description rather than invented text.

Guard: `postmaster/show_all_description_test.go` —
`TestShowAllEmitsDescriptionColumn` (RowDescription is exactly
name/setting/description and every DataRow carries 3 columns; red before the
fix with "has 2 columns ([name setting])") and
`TestShowAllDescriptionIsPopulated` (a server wired with a catalog returns
PG's exact `short_desc` for `enable_seqscan`).

### EO1-7 — BUG (fixed)

Real. Both `execAlterCollation` and `execAlterConversion` mapped *every*
error out of `im.RenameCollation` / `im.RenameConversion` to `notFound()`, and
those helpers return two distinct failures: source missing, and target name
already taken. A collision therefore reported 42704 "does not exist" naming
the SOURCE object — and with `IF EXISTS` it was downgraded to a NOTICE and the
statement reported success while nothing had been renamed. PG 18.3 oracle:

    ALTER COLLATION zzc1 RENAME TO zzc2;
    ERROR:  collation "zzc2" for encoding "UTF8" already exists in schema "public"
    ALTER CONVERSION zzcv1 RENAME TO zzcv2;
    ERROR:  conversion "zzcv2" already exists in schema "public"

both 42710 duplicate_object (`report_namespace_conflict`, alter.c). Fix:
`catalog.ErrRenameNameConflict`, wrapped by the two rename helpers on the
target-taken path, and an `errors.Is` arm in each executor rename case that
raises 42710 with PG's wording (the encoding is spelled "UTF8" exactly as the
COMMENT ON COLLATION path already assumes).

Guard: `executor/alter_rename_conflict_test.go` — collation and conversion
cases assert code 42710 and PG's exact message, that `IF EXISTS` does not
swallow the collision, and that the source object survives the failed rename.
Red before the fix: `42704: collation "cc1" does not exist`.

### EO1-9 — BUG (fixed) — worse than reported

Real, and it bites two functions, one of them silently. `checkConstraints`
rebuilds every column value with `Datum.Format()` and interpolates it back
into a synthetic `SELECT (expr) FROM (VALUES ('…'::type)) AS _chk(…)`.
`Format()` is a *display* rendering: for a `date` column it produces
"05-06-2020", which does not re-parse as a date. Observed on goopg before the
fix, for an INSERT PG 18.3 accepts:

    CREATE TABLE ck_date (d date CHECK (d > '2000-01-01'));
    INSERT INTO ck_date VALUES ('2020-05-06');
    ERROR: XX000 internal error: could not evaluate check constraint
           "ck_date_d_check" ... 22007 invalid input syntax for type date: "05-06-2020"

`evalDomainCheckExpr` has the identical construction *and* treats every
evaluation failure as a PASS, so the same type mismatch made a domain CHECK
silently unenforced — a wrong-results bug, not just a spurious error:

    CREATE DOMAIN dd AS date CHECK (VALUE > '2000-01-01');
    INSERT INTO dt VALUES ('1999-01-01');   -- goopg: accepted
    -- PG 18.3: ERROR value for domain zzdd violates check constraint "zzdd_check"

Fix: both sites bind the row values as parameters (`$1::type`, with
`synthCtx.Params` carrying the Datums) instead of rendering and re-parsing
them, so no round trip through text happens. The cast keeps the column's
declared type, with `[]` re-appended for array columns (`Type.Name` holds the
element name).

Guard: `executor/check_constraint_param_binding_test.go` —
`TestCheckConstraintValuesBindAsParams` (date/bytea/timestamp/interval/float8/
array/quoted-text all insert cleanly, and a violating row still raises 23514)
and `TestDomainCheckDateIsEnforced` (violating domain value now raises 23514).
Both red before the fix.

### EO1-10 — NOT A BUG (latent, and not a nil deref)

`SlotFromRow(nil, row)` in the SRF operators passes a nil `optimizer.Schema`,
which is a nil *slice*, not a nil pointer — reading it yields length 0, never
a panic. Both places in the tree that read a producer slot's own schema guard
for it: `plpgsql_runtime.go:1985` takes it only `if len(s) > 0` (otherwise
keeping `op.Schema()`), and `:2094` only `if slot.Schema() != nil`. Verified
behaviourally: `FOR r IN SELECT * FROM unnest(ARRAY[10,20]) AS t(x) LOOP acc
:= acc || r.x` returns "10 20 ", i.e. field-by-name binding through an SRF
works today. Worth tidying if the SRFs are touched again, but nothing is
broken.

### EO2-6 — BUG (fixed) — root cause is in the planner, not the operator

Real, and the missing `BindLateralOuter` was only half of it.
`pg_options_to_table` was absent from *both* lateral mechanisms: the operator
read `ctx.OuterRows` and implemented no `BindLateralOuter`, and — the actual
blocker — `optimizer.nodeReferencesOuter` had no `*PgOptionsToTable` case, so
the wrapping Join was never marked `Lateral` and `openLateral` (the only code
that binds an outer row per iteration) never ran at all. Result, on goopg
before the fix:

    SELECT z.id, t.option_name, t.option_value
      FROM zopts z, LATERAL pg_options_to_table(z.opts) t;
    ERROR: XX000 column ref opts/1 on nil slot

PG 18.3 returns `(1,a,1) (1,b,2) (2,c,3)` for the same fixture (verified on
the oracle). The correlated-subquery spelling pg_dump itself uses
(`(SELECT count(*) FROM pg_options_to_table(z.opts))`) worked, which is why
this stayed hidden.

Fix: a `*PgOptionsToTable` arm in `nodeReferencesOuter` returning
`exprContainsColumnRef(x.Arg)`, plus `BindLateralOuter` on the operator with
`evalExprSlot` against the bound slot, falling back to the `ctx.OuterRows` top
when no slot is bound (keeps the pg_dump path intact).

Guard: `executor/pg_options_to_table_lateral_test.go` — both spellings, exact
PG row set. Red before the fix with the "nil slot" error above.

### EO2-7 — NOT A BUG

Two claims, neither holds. (a) "`i < len(row)` is checked after EqualFold, so
a matching name past the row's end silently returns NULL": both orderings
behave identically — when the bound check fails the loop just continues, and
since a table cannot have two columns of the same name, the continue always
runs out and returns `NullDatum` either way. The proposed reordering changes
no observable behaviour. (b) "`row[i] = val` panics on a short row": every
caller sizes the row to the column list it passes — `computeGeneratedColumns`
is always called with the table (or leaf partition) the row was just built or
remapped for (`remapRowForPartition` re-sizes to `part.Columns` before the
partition-level call), and `applyDefaultsForMissing`'s `missing` slice is
built alongside the row from the same column list at all five call sites
(upsert, COPY, MERGE, apply worker, insert). No reachable short-row path.

### EC-3 — BUG (fixed)

Real. `encodeArrayBTreeKey`'s multidimensional refusal scanned the raw literal
text for any `{` byte, with no regard for quoting, so a plain 1-D `text[]`
value whose element merely contains a brace was rejected. On goopg before the
fix, with an index on the column:

    SELECT count(*) FROM zbrace WHERE a = '{"a{b"}';
    ERROR: 0A000 btree v0 cannot index multidimensional array column "a"

PG 18.3 (oracle) indexes the same value and returns 1. Fix: a new
`arrayLiteralHasNestedBrace` scan that tracks quotes and backslash escapes the
way `parseTextArrayElems` does, so only a top-level `{` counts as nesting.

Guard: `executor/btree_array_key_brace_test.go` — the quoted-brace element now
probes to 1 row, and a genuinely nested literal (`{{1,2},{3,4}}`) still raises
0A000. Red before the fix with the error above.

### EC-5 — BUG, narrowed (fixed on the decode side)

Partly real. The SQL-facing half is already covered: goopg's date/timestamp
*input* refuses anything outside Go's time range with an explicit error —
`INSERT … '5874897-12-31'` → 22007, `'4714-11-24 BC'` → `22008 date/time
value out of range for date … (goopg stores date between 1677-09-21 and
2262-04-11)` — so no wrap can be reached from SQL, and the ±infinity
sentinels are intercepted before the arithmetic in both the timestamp and
date arms.

What is genuinely unguarded is the decode of a heap real PG wrote: PG's date
range runs to 5874897 AD, and `int64(days)*86400*1e6` overflows around
294247 AD, turning a far-future date into a plausible near-past one with no
error at all. Fixed by bounding `days` against the representable micros range
and returning an error instead. The encoder needs no counterpart: a Datum
outside Go's range cannot be constructed in the first place.

Guard: `executor/codec_date_range_test.go` — out-of-range day counts (±1.43e9,
PG's own extreme) now error, both infinity sentinels still decode, and an
ordinary date still round-trips. Red before the fix (both extremes decoded
"successfully").

### EC-6 — NOT A BUG (unreachable)

The observation is factually right — the `int2` arm range-checks and the `int4`
arm does not — but no out-of-range int4 Datum can reach it. Every ingress into
an `int4` column rejects the value first: `INSERT INTO t(i int4) VALUES
(2147483648)` → `integer out of range`, `… VALUES (2147483647::int8 + 1)` →
same, and text `COPY … FROM STDIN` with `9999999999` → `value is out of range
for type integer`. The one case that *does* produce an oversized value,
`SELECT 2147483647::int4 + 1`, is typed `bigint` by goopg (PG errors instead —
a separate, unrelated divergence) and so takes the int8 arm and ships 8 bytes.
The report itself notes "not triggered by normal operation". Left as-is rather
than adding an unreachable branch.

### EC-7 — BUG, narrowed (fixed, both directions)

Real for `date` / `timestamp` / `timestamptz`, and worse than "decode only":
the *encode* side was wrong too. `datumToCopyBinary` passed the ±infinity
KindTime carrier (Int = INT64 extreme *nanoseconds*) through `TimeValue()`, so
`COPY … TO STDOUT (FORMAT binary)` shipped `'infinity'::date` as `00017632`
(= 2262-04-11) and `'infinity'::timestamp` as `001d679a6aab73f7`, where PG 18.3
ships `7fffffff` / `7fffffffffffffff` (verified against the oracle on 65438).
`copyBinaryToDatum` then reconstructed those as ordinary finite values, so a
binary dump/restore silently rewrote infinity to a year-2262 timestamp. The
heap codec (`codec.go`) had handled the sentinels since #5(d-iv); this is the
sibling-paths-must-agree pattern again. Fixed by intercepting
DT_NOEND/DT_NOBEGIN and DATEVAL_NOEND/DATEVAL_NOBEGIN in all four arms.

Narrowed on two of the reported types: `interval` already round-trips
correctly (its sentinel *is* the wire value — `7fffffffffffffff7fffffff
7fffffff` — so the plain encoder is accidentally right), and `timetz` has no
infinity in PG at all, so nothing is missing there.

Guard: `executor/copy_binary_infinity_test.go` — the six wire forms are pinned
to the bytes captured from PG 18.3 and the round trip must preserve the
sentinel and its sign. Red before the fix on all six.

### EC-8 — NOT A BUG

The report says so itself ("the `if idx < 0` guard catches this before the
shift, so this is not a live panic … no change needed"). Recorded as reviewed;
nothing to fix.
