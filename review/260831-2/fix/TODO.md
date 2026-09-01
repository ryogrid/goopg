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
| EO2-2 | med | `executor/operators_sequence.go:seqState.nextVal` — int64 overflow wraps instead of raising 2200H | ? | [ ] | |
| EO2-3 | med | `executor/operators_project_set.go:openSelectSrfMode` — generate_series int64 overflow spins forever | ? | [ ] | |
| EO2-4 | med | `executor/operators_recursive_cte.go:recursiveUnionOp.Open` — phase state not reset on re-open | ? | [ ] | |
| EO2-5 | med | `executor/opnode.go:limitOpNext` — `FETCH FIRST 0 ROWS WITH TIES` panics on nil tieKeyVals | BUG | [x] | 0e50eb19a |
| ES-6 | med | `executor/plpgsql_runtime.go:executePLpgSQLStmt (ForStmt)` — BY step not validated; zero/negative step infinite-loops | ? | [ ] | |
| ES-7 | med | `executor/plpgsql_runtime.go:lowerPLpgSQLExpr (CastExpr)` — cast dropped in PL/pgSQL expressions | ? | [ ] | |
| IN-2 | med | `initdb/catalog_cache.go:readCatalogCache` — silent partial catalog on TryRegisterUserTable failure | ? | [ ] | |
| ST-3 | med | `storage/bufpool.go:pinLoad/evictVictim` — dirty victim content discarded when the flush fails | ? | [ ] | |
| ST-7 | med | `storage/vm.go:PageAllVisible/PageAllFrozen` — plain XID comparison breaks under wraparound | BUG | [x] | 3f13f584e |
| ST-8 | med | `storage/freeze.go:PageFreezeOldTuples` — plain XID comparison breaks under wraparound | BUG | [x] | 8d5028fb3 |
| NB-2 | med | `pglz/pglz.go:Decompress` — match length clamped instead of erroring on corrupt streams | ? | [ ] | |
| TA-2 | med | `transam/manager.go:AssignXID` — non-atomic read-check-allocate-store allows XID leak/double-assign | ? | [ ] | |
| NP-1 | med | `plpgsql/parser.go:parseFor` — FOR-query scan truncates on a `loop` identifier inside the SQL text | ? | [ ] | |
| NP-5 | med | `plpgsql/parser.go:parseFor` — `isQueryFor` peeks only the first token; parenthesized bound misroutes | ? | [ ] | |
| OP1-2 | med | `optimizer/exprwalk.go:exprChildSlots` — FuncCall child slots omit Filter/Over/OrderBy/WithinGroup/Variadic | ? | [ ] | |
| OP1-3 | med | `optimizer/createplannl.go:createNestLoopBitmapJoinPlan` — `bhs.BitmapQual = nil` drops recheck quals | ? | [ ] | |
| XL-4 | med | `wal/slot_decoder.go:Run` — ConfirmedFlushLSN never advances for PG-format commit records | ? | [ ] | |
| CP-2 | med | `postmaster/dispatch.go:normalizeSQLPreservingLiterals` — plan-cache key collision on quoted identifiers | ? | [ ] | |
| UT-1 | med | `utils/activity/registry.go:coldFromBackend` — `BackendStart` never copied; pg_stat_activity.backend_start empty | BUG | [x] | 306709703 |
| UT-2 | med | `utils/misc/guc.go:canonicalizeFrom` (TypeReal) — unit suffix / scientific notation mis-parsed | BUG | [x] | 00e35fbab |
| UT-4 | med | `utils/mmgr/mctx.go:Context.Release` — mutates `c.children` while ranging over it | BUG | [x] | 370100352 |
| CM-3 | med | `cmd/plan-snapshot/main.go:planEqual` — `rowsRegexp` never matches the real EXPLAIN format | BUG | [x] | this commit |

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
