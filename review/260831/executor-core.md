# Executor Core — Code Review 2026-08-31

Files: advisory.go, amutils.go, applyworker.go, btree_array_key.go, btree_interval_key.go, btree_key_decodable.go, btree_scalar_keys.go, bytea.go, cmdtag_table.go, codec.go, codec_aclitem.go, codec_array.go, coltypeinfo.go, command_counter.go, context.go, copy.go, copy_binary.go, copy_csv.go, copy_text.go, datum.go, deferred_exclusion.go, deferred_unique.go, executor.go, explain_cte.go, explain_names.go, expr.go, expr_batch.go, expr_replslot.go, exprnode.go, float_in.go, hash_partition.go, heap_fillfactor.go, heap_insert_select.go, index_mutability.go, instrument.go
Findings count: 23

### `advisory.go:PgLockRows` — fmt.Sprintf for uint32 in row builder
- **Issue**: `classid`/`objid` are `uint32` but formatted with `fmt.Sprintf("%d", key.hi)`, which routes through reflection and allocations.
- **Why**: This runs for every row of `pg_locks` output; `strconv.FormatUint` is cheaper and allocation-free for small ints.
- **Suggestion**: Use `strconv.FormatUint(uint64(key.hi), 10)`.
- **Severity**: low

### `advisory.go:acquire` — recursion re-registers waiter and re-probes activity per wake
- **Issue**: On a wake, `acquire` recurses into itself, re-running `activity.LookupCurrentGoroutine` and appending a *new* waiter. Under contention, many waiters wake at once (thundering herd), each recursing and re-blocking; the call stack grows with retry count.
- **Why**: Each recursion holds no state, so the work (lookup + wait-event registration + waiter append) repeats from scratch on every retry.
- **Suggestion**: Convert to a loop (`for { ... }`) with a single activity probe; only append the waiter once per iteration.
- **Severity**: low

### `applyworker.go:decodePgoutputTupleAsRow` — column-name map rebuilt on every row
- **Issue**: The remote→local column ordinal mapping is recomputed with an O(remote×local) nested loop (plus two `[]bool` allocations) for *every* INSERT/DELETE/UPDATE tuple.
- **Why**: The mapping depends only on the relation's column lists, which are constant for the relation; it is loop-invariant per row and is recomputed per-row even in high-volume apply streams.
- **Suggestion**: Compute the `localIdx`/`claimed` mapping once per relation (cache on `applyRel`, keyed by `remote.Columns` identity) and reuse it; only `row`/`unchanged`/`missing` need per-row allocation.
- **Severity**: medium

### `applyworker.go:applyContext` — fresh Context + XID materialization per row
- **Issue**: `applyInsert`/`applyDelete`/`applyUpdate`/`applyTruncate` each call `applyContext()`, which allocates a new `Context` via `NewContext()` and re-materializes the writer XID and snapshot on every event.
- **Why**: The context fields are stable for the worker's lifetime; only `currentTx`/`Snap` change per transaction. Per-row `NewContext()` is avoidable allocation + repeated snapshot lookup.
- **Suggestion**: Cache one context on the worker, updating `Tx`/`Snap` only when the transaction changes (at `applyBegin`/`applyCommit`).
- **Severity**: low

### `applyworker.go:primaryKeyOnlyRow` / `replicaIdentityKeyRow` — per-row nested column lookups
- **Issue**: Both helpers find key columns via nested loops over column lists (`cat.IndexesOnTable(tbl)` + name match loops; remote×local loops) on every no-OldTuple UPDATE.
- **Why**: The identity/PK column positions are constant per relation; recomputing them per row is loop-invariant work.
- **Suggestion**: Cache the key-column positions (set of local ordinals) on `applyRel` when it is resolved, and index the row directly.
- **Severity**: low

### `applyworker.go:parsePgoutputText` — `string(data)` allocated before type dispatch
- **Issue**: `s := string(data)` copies the byte slice to a string on every column even when only int parsing (which needs a string anyway) is done; the allocation is unconditional.
- **Why**: Minor per-column allocation; `strconv.ParseInt` needs a string so only the fallback path could avoid it, but the conversion still happens before the switch.
- **Suggestion**: Acceptable as-is; alternatively dispatch on type name first and convert only where needed. Low impact.
- **Severity**: low

### `hash_partition.go:computeHashPartitionRowHash` — per-row invariant work on the INSERT partition-routing hot path
- **Issue**: For every hashed key column, the default-hash arm re-scans `tbl.Columns` with a name match + `strings.ToLower` per column, and the custom-opclass arm re-runs `LookupOpClassHashFunc` + `Routines().LookupByName` + a scan for the 2-arg routine. All of this depends only on the table's partition key, not on the row.
- **Why**: `routeToPartitionDepth` calls this per inserted row, so O(keys×cols) name scans and repeated catalog routine lookups run once per row on a hot write path.
- **Suggestion**: Resolve per-key column types / hash routines once per relation (cache on the table or context) and reuse per row.
- **Severity**: medium

### `context.go:waitForRelationLockers` — `time.After` allocates a fresh timer per poll iteration
- **Issue**: The polling loop calls `time.After(pollInterval)` on every iteration, allocating a new timer each time (and leaking it until it fires).
- **Why**: A `time.Ticker` (or a single reusable timer) avoids per-iteration allocation; the loop already has a `waiting` flag, so a ticker lifecycle is easy to bound.
- **Suggestion**: `ticker := time.NewTicker(pollInterval); defer ticker.Stop()` and `<-ticker.C`.
- **Severity**: low

### `explain_names.go:nodePtr` — `fmt.Sprintf("%p", n)` as map key
- **Issue**: Node identity for the `nodeLabels` map is a string built with `fmt.Sprintf("%p", n)` (allocates + reflection) at collect/render time.
- **Why**: A `uintptr(unsafe.Pointer(n))` (or `*optimizer.Node` keyed map directly) is cheaper and GC-safe here; the string round-trip is pure overhead.
- **Suggestion**: Key `nodeLabels` by the node pointer value rather than a formatted string.
- **Severity**: low

### `deferred_exclusion.go:runAllDeferredExclusionChecks` / `deferred_unique.go:runAllDeferredUniqueChecks` — per-check catalog re-resolution
- **Issue**: Each queued check re-runs `im.LookupTable(...)` plus a linear `indexByNameOnTable` scan over the table's indexes, per check, at COMMIT.
- **Why**: All checks for the same (table, index) could resolve the (tbl, idx) pair once; in a bulk-load transaction with many queued rows this is repeated O(tables × indexes) work.
- **Suggestion**: Group the checks by table+index and resolve each pair once; or cache the resolved pair on the session alongside the queue.
- **Severity**: low

### `copy_csv.go:parseCopyCsvFields` — per-field `string(field)` conversion and byte-at-a-time field building
- **Issue**: Each field is accumulated byte-by-byte with `field = append(field, c)` (repeated growth) and then `string(field) == f.nullStr` allocates a string per field for the NULL check.
- **Why**: Per-field allocation and potential slice re-growth on every row of a COPY CSV FROM; a `strings.Builder`-style or `[]byte` with tracking avoids the conversion for the common non-null case.
- **Suggestion**: Compare `bytes.Equal(field, []byte(f.nullStr))` (or keep a precomputed null []byte) to avoid the string alloc; pre-size the field buffer.
- **Severity**: low

### `codec.go:pgFloatFromDatum` — `StringValue()` computed then discarded for KindNumeric
- **Issue**: `raw := d.StringValue()` runs unconditionally, then `if d.Kind == KindNumeric { raw = numericText(d) }` recomputes from the Int/Scale fields, throwing away the first result.
- **Why**: For KindNumeric (the common float8-decode shape) `StringValue()` is pure waste — a call that dereferences Buf/arena for no value.
- **Suggestion**: Branch on Kind first: `if d.Kind == KindNumeric { raw = numericText(d) } else { raw = d.StringValue() }`.
- **Severity**: low

### `codec.go:parseIntegerInput` — `strings.ReplaceAll` allocation per integer coercion
- **Issue**: Every string→int coercion (per value stored into an integer column) does `strings.ReplaceAll(s, "_", "")`, allocating a fresh string even when the input contains no underscores.
- **Why**: Runs once per inserted/coerced value; a scan that skips the replace when no `_` is present avoids the allocation in the common case.
- **Suggestion**: First `strings.IndexByte(s, '_')`; only build the cleaned copy when present (the underscore rules are validated separately anyway).
- **Severity**: low

### `copy.go:insertSourceRow` — per-row `make(Row, …)` instead of the row pool
- **Issue**: Every COPY FROM row allocates a fresh `Row` with `make(Row, len(c.cols))` (and `PushBinaryData` does the same per row), bypassing the `rowPool` (`acquireRow`) the scan/join paths use.
- **Why**: COPY FROM is the bulk-load hot path; pooled row slices would avoid one allocation + GC per row.
- **Suggestion**: `dst := acquireRow(len(c.cols))` (the existing pool in this package) and reset before use.
- **Severity**: medium

### `copy.go:PushBinaryData` / `listedColumns` — per-call rebuild of `listedCols` / `listedColumns()`
- **Issue**: `PushBinaryData` rebuilds `listedCols` (an allocation + loop over `plan.ColumnIndex`) on every chunk, and the text path's `listedColumns()` allocates on every `PushLine`.
- **Why**: Both depend only on the immutable `plan.ColumnIndex`/`cols`, so they are loop invariants.
- **Suggestion**: Compute the listed-columns slice once in `newCopyFromExecutor` and cache it on the struct.
- **Severity**: low

### `copy_binary.go:decodeNumericBinary` — dead `fullMantissa` computation before unconditional fallback
- **Issue**: Lines ~800-814 compute `fullMantissa` with power-of-10000 loops, but line ~817 then unconditionally returns `decodeNumericBinaryViaBig` — the computed value is never used.
- **Why**: Pure wasted arithmetic + multiply-loop per numeric field per binary-COPY row.
- **Suggestion**: Delete the dead `fullMantissa` accumulation (and the `overflow` int-path scaffolding if the big path is the only survivor), keeping only the validation and the big-path call.
- **Severity**: medium

### `copy_binary.go:decodeNumericBinaryViaBig` — big.Int reconstruction built then discarded
- **Issue**: Builds `val` (per-digit `big.NewInt`, `Exp` pow) and `scale` loops, then discards both and re-derives the value from a string (`numericBinaryToString` + `parseNumeric`).
- **Why**: Several big.Int allocations and exponentiations per numeric field that contribute nothing to the result.
- **Suggestion**: Keep only the string-based reconstruction (or, better, implement a direct base-10000 → mantissa/scale conversion); drop the dead big.Int accumulation.
- **Severity**: low

### `copy_binary.go:datumToCopyBinary` — per-field allocation then re-copy into dst
- **Issue**: Each field allocates its own `make([]byte, N)` payload and `AppendCopyBinaryRow` then appends it into `dst`, so fixed-width fields are allocated and copied once each.
- **Why**: For the fixed-width arms (int2/int4/int8/float/oid/uuid/date/time/…) the bytes could be written straight into `dst`, avoiding the intermediate allocation.
- **Suggestion**: Have `datumToCopyBinary` take `dst` and append into it (returns the extended slice) instead of returning a fresh slice.
- **Severity**: low

### `copy_text.go:copyTextToDatum` — `string(raw)` conversion per field per row
- **Issue**: Every COPY TEXT field is converted to a string (`string(raw)`) for the parse calls (`strconv.ParseInt(string(raw), 10, 64)`, `parseCopyTimestampZoneSession(string(raw), …)`, etc.), plus another allocation at the default `NewStringDatum(string(raw))`.
- **Why**: Per-field allocation on the COPY FROM bulk-load path; the raw `[]byte` is already owned, so most paths only need a string view.
- **Suggestion**: Where the type is known to be a text-literal pass-through, construct the string once; for the int arms use a single conversion (they need a string anyway). Lowering is bounded, but the double conversion on the timestamp path (`string(raw)` inside a wider parse) can be reduced.
- **Severity**: low

### `expr.go:evalCast` — `strings.ToLower(targetType)` on every cast evaluation
- **Issue**: `evalCast` opens with `switch strings.ToLower(targetType) {…}`, so every per-row `::int4`/`::text`/`::numeric` cast lowercases the target-type string on every evaluation.
- **Why**: The target type is loop-invariant per expression; this runs once per row through the CastExpr arm of `evalExprSlot` (casts are not compiled into the fast `exprTreeSlab` path — they fall to `ExprAdapter`).
- **Suggestion**: Hoist the lowering at plan/build time (cache the lowered target type on the CastExpr node, or route casts through the compiled path), and dispatch on the lowered value.
- **Severity**: medium

### `expr.go:compareDatum` — per-comparison UUID / pg_lsn detection on the string-compare hot path
- **Issue**: Every `KindString` comparison runs `looksLikePgLSN(as) && looksLikePgLSN(bs)` (which allocates via `s[:slash] + hexLow` when a slash is found) and `isValidUUIDStr(as) || isValidUUIDStr(bs)`.
- **Why**: Text equality/ordering is the most common predicate; the length guards make these cheap for typical values, but `isValidUUIDStr` still scans the whole string when it is 32/36/38 chars, and `looksLikePgLSN` concatenates on every slash-containing value.
- **Suggestion**: Replace the `looksLikePgLSN` concatenation with two loops over the two sub-slices (no allocation); gate the UUID normalization behind a cheap length check before `isValidUUIDStr` (it already length-gates, so the main win is the LSN alloc).
- **Severity**: low

### `expr.go:evalBinary` — ILIKE lowercases both operands per row
- **Issue**: `OpILike`/`OpNotILike` do `strings.ToLower(ls), strings.ToLower(rs)` per evaluation, allocating two new strings on every row the predicate tests.
- **Why**: Runs in the scan filter per tuple; the pattern side is loop-invariant and could be lowered once at plan time.
- **Suggestion**: Pre-lower the pattern at build time (or cache it on the plan node); only the searched string needs per-row lowering.
- **Severity**: low

### `expr.go:looksLikePgLSN` — string concatenation for validation loop
- **Issue**: `for _, c := range s[:slash] + hexLow` allocates a new string just to iterate the two parts.
- **Why**: Runs on every slash-containing string comparison; the concatenation is unnecessary — two loops (or byte indexing over the two ranges) avoid it.
- **Suggestion**: Iterate `s[:slash]` then `hexLow` in separate loops.
- **Severity**: low
