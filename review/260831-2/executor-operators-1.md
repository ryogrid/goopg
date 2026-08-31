# Executor Operators (part 1) — Bug Review 2026-08-31

Files: operators.go, operators_analyze.go, operators_bitmap.go, operators_bt_index_check.go, operators_call.go, operators_checkpoint.go, operators_cluster.go, operators_cte_dml.go, operators_ddl.go, operators_ddl_database_acl.go, operators_ddl_default_privileges.go, operators_ddl_parameter_acl.go, operators_ddl_partition.go, operators_ddl_role_membership.go, operators_distinct.go, operators_explain.go, operators_explain_format.go, operators_fk.go, operators_from_regexp_matches.go, operators_from_regexp_split_to_table.go, operators_from_unnest.go, operators_gather.go, operators_gather_merge.go, operators_generate_series.go
Findings count: 10

### `internal/executor/operators_call.go:callOp.Next` — IN/INOUT args after an OUT param read the wrong slot
- **Bug**: In the PL/pgSQL branch, `argIdx` is advanced only for IN/INOUT (`b`/default) params and skips OUT (`o`) positions, but `o.args` is parameter-position-aligned whenever the caller supplied placeholders for all params (or named args). The OUT-nulling step (`args[i] = NullDatum` for each `"o"` mode) then overwrites the position of an *actual* later IN/INOUT value. Example: `p(IN a, OUT x, IN b)` called `CALL p(1, 2, 3)` → `args = [1, NULL, 3]`; the loop reads IN `b` at `o.args[argIdx]` where `argIdx=1` after `a`, so `b` gets NULL instead of 3.
- **When it triggers**: Any PL/pgSQL procedure with an OUT/INOUT param followed by another IN/INOUT param, called with full placeholder argument list (or with named args). The wrong (NULLed) value is bound, silently producing wrong results.
- **Fix**: When `len(args) == len(ArgTypes)` (param-aligned), index `o.args` by parameter position `i` directly rather than by `argIdx`; only use `argIdx` for the positional-<totalCount case.
- **Severity**: medium

### `internal/executor/operators.go:limitOp.Open` — stale limitCount survives NULL-limit re-Open
- **Bug**: `Open` resets `emitted`/`skipped` but not `limitCount`/`offsetCount`. When the LIMIT expression evaluates to NULL on a re-Open (e.g. `LIMIT $1` with a NULL bind on the second execution of a prepared/cached plan), the `v.IsNull()` branch (no WITH TIES) sets nothing, so the previous positive `limitCount` is reused — the query is silently limited again.
- **When it triggers**: Plan reused across executions where a prior LIMIT was non-NULL and a later bind is NULL (NULL LIMIT means "no limit").
- **Fix**: Set `o.limitCount = -1` and `o.offsetCount = 0` at the top of `Open` (alongside the existing emitted/skipped reset).
- **Severity**: low

### `internal/executor/operators.go:limitOp.Next` — WITH TIES with limitCount==0 panics on nil tieKeyVals
- **Bug**: When `withTies` and `limitCount == 0`, the first `Next()` hits the `o.emitted >= o.limitCount` ties branch with `o.tieKeyVals == nil` (never set, since no row was emitted). `tiesRowMatches` then indexes `o.tieKeyVals[i]` → index-out-of-range panic. (PG returns zero rows for `FETCH FIRST 0 ROWS WITH TIES`.)
- **When it triggers**: `LIMIT 0 ... WITH TIES` (or `FETCH FIRST 0 ROWS WITH TIES`) with a non-empty ORDER BY key list.
- **Fix**: Treat `limitCount == 0` as EOF immediately (before the ties branch), or guard `tiesRowMatches` against nil `tieKeyVals`/`tieKeyExprs`.
- **Severity**: medium

### `internal/executor/operators_bitmap.go:lookupBounds` — index out of range when the index has no key columns
- **Bug**: `lookupBounds` unconditionally reads `o.plan.Index.Columns[0]`. `needsRecheck` guards `len(Columns)==0`, but `buildBitmap` calls `lookupBounds` unconditionally, so a zero-key-column index panics.
- **When it triggers**: A BitmapIndexScan whose index plan node has no key columns (defensive; not reachable through normal planner output today).
- **Fix**: Early-return `nil,nil,nil` when `len(o.plan.Index.Columns) == 0`.
- **Severity**: low

### `internal/executor/operators_generate_series.go:generateSeriesOp.Next` — int64 overflow when start/stop near MaxInt64
- **Bug**: `o.current += o.step` overflows signed int64. `generate_series(9223372036854775807, 9223372036854775807, 1)` emits the one correct row, then `current` wraps to MinInt64; the termination tests (`current > stop`) then never fire, so the operator keeps emitting the entire int64 range (a multi-hour hang / wrong-result stream). PG guards the loop arithmetic against overflow.
- **When it triggers**: Boundary-value start/stop/step combinations where `current+step` overflows int64.
- **Fix**: Emit rows while `current <= stop` computed in a way that cannot overflow (e.g. test `stop-current` against `step`, or detect wrap).
- **Severity**: low

### `internal/executor/operators_ddl.go:execDropTablespace` — not-empty check iterates only in-memory tables/indexes
- **Bug**: The `"tablespace %q is not empty"` guard (`if oid, found := LookupTablespaceOID; found`) only inspects `im.AllTables()`/`im.AllIndexes()` when the catalog is an `*InMemory`. For other catalog wrapper types the guard silently passes and the tablespace is dropped out from under relations that reference it. (The registry drop then succeeds, orphaning the physical directory / catalog references.)
- **When it triggers**: DROP TABLESPACE on a non-InMemory catalog backend (e.g. wrapped catalogs).
- **Fix**: When the catalog is not `*InMemory`, fall back to enumerating relations another way, or refuse the drop.
- **Severity**: low

### `internal/executor/operators_ddl.go:execAlterCollation/execAlterConversion` — rename collision reported as "does not exist"
- **Bug**: `case "rename"` calls `im.RenameCollation(...)`/`im.RenameConversion(...)` and maps ANY error to `notFound()` (42704 "does not exist"). A rename to an already-taken name (which upstream reports as 42710 duplicate_object) is therefore misreported, and a `... RENAME TO` collision is swallowed into the wrong error path.
- **When it triggers**: `ALTER COLLATION ... RENAME TO <existing>` / `ALTER CONVERSION ... RENAME TO <existing>`.
- **Fix**: Distinguish duplicate-name errors (return 42710) from missing-object errors (42704) before calling `notFound()`.
- **Severity**: low

### `internal/executor/operators_ddl.go:execCreateTable` — fallback (non-BodyOrder) column path drops serial's implicit NOT NULL
- **Bug**: The no-BodyOrder fallback column loop (`operators_ddl.go:2590`) sets `NotNull: c.NotNull` only, whereas the BodyOrder `addCol` path sets `NotNull: c.NotNull || c.IdentityColumn || isSerialCol`. A `serial`/identity column created through the fallback path is created nullable (PG makes it NOT NULL). The parser always emits `BodyOrder` today, so this is a latent defensive gap.
- **When it triggers**: `CREATE TABLE ... (a serial, ...)` planned without a `BodyOrder` (parser always emits BodyOrder today, so reachability depends on parser version).
- **Fix**: Apply the same `isSerialCol`/identity NOT NULL logic in the fallback loop, or force every CREATE TABLE through the BodyOrder path.
- **Severity**: low

### `internal/executor/operators_fk.go:checkConstraints` — CHECK-expression VALUES reuse unsafe text rendering of datums
- **Bug**: `colVals[i]` is built from `row[i].Format()` quoted as a string literal and cast `::<type>`. For a text value this is fine (escaping applied), but for a bytea/date/interval value whose `Format()` is not a valid literal for its own type, the constructed SQL either fails to parse (loud, via `unevaluable`) or silently changes the comparison value (e.g. `1.5e-2` vs the type's canonical literal). The failure mode is at minimum a spurious XX000; a wrong-value comparison (if the literal round-trips into a different value) would be a silent CHECK-pass/fail error.
- **When it triggers**: A CHECK constraint on a column whose `Format()` output is not a round-trippable literal of its type (bytea, timestamps with non-ISO session style, booleans when format differs, etc.).
- **Fix**: Pass values as typed parameters / Datum bindings instead of interpolating `Format()` into SQL text (mirrors `evalDomainCheckExpr`'s same hazard).
- **Severity**: low

### `internal/executor/operators_from_unnest.go:fromUnnestOp.Next` — nil schema slot (also regexp + generate_series SRFs)
- **Bug**: `SlotFromRow(nil, row)` and `SlotFromRow(nil, Row{...})` are used in `fromUnnestOp.Next()`, `fromRegexpMatchesOp.Next()`, `fromRegexpSplitToTableOp.Next()`, and `generateSeriesOp.Next()`/`generateSubscriptsOp.Next()`. A nil schema on the returned slot means any consumer that reads the slot's schema (vs. the plan node's) dereferences nil. In the current tree consumers use the plan schema, so this is latent.
- **When it triggers**: If any parent operator starts reading `TupleSlot.Schema()` of an SRF row.
- **Fix**: Pass `o.plan.Output()` instead of nil.
- **Severity**: low

## Files with no functional bugs found

- **operators_analyze.go**: Reservoir sampling logic, ndistinct estimates, and per-column statistics are correct. No arithmetic or logic errors.
- **operators_bt_index_check.go**: B-tree verification drivers are correct. No off-by-one or index issues.
- **operators_checkpoint.go**: Trivial one-shot wrapper, no bugs.
- **operators_cluster.go**: No-op executor with table-existence checks; correct.
- **operators_cte_dml.go**: CTE DML demand-driven execution is correctly synchronized. No racy shared-state access.
- **operators_ddl_database_acl.go**: ACL materialization and heap row resync correct.
- **operators_ddl_default_privileges.go**: ALTER DEFAULT PRIVILEGES validation correct. `current_user→OID 10` matches goopg's documented model.
- **operators_ddl_parameter_acl.go**: Parameter ACL grant/revoke correct.
- **operators_ddl_partition.go**: Partition key and bound validation correct. `validateRangeBoundOrder`/`validateHashBounds`/overlap checks correct.
- **operators_ddl_role_membership.go**: Role membership grant/revoke with authorization checks correct.
- **operators_distinct.go**: Dedup logic correct. `rowKey`-based map and sort-based order correct.
- **operators_explain.go**: EXPLAIN rendering correct. The nil-schema SRF issue noted above is the only concern.
- **operators_explain_format.go**: Pure XML/YAML serializers. No bugs.
- **operators_fk.go**: Referential-integrity enforcement correct. Deferred FK checks, cascading, partition ancestry, and wait-for-XID loops are correct. The `fkRowMatches`/`fkColValues` column-name resolution is correct.
- **operators_from_regexp_matches.go**: Correct.
- **operators_from_regexp_split_to_table.go**: Correct.
- **operators_gather.go**: Parallel worker lifecycle, channel draining, and leader share correct. No deadlock/race.
- **operators_gather_merge.go**: Merge-heap ordering correct. `lessRows` uses `k.NullsFirst` (previously fixed bug).