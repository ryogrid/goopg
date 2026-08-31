# Executor Core — Bug Review 2026-08-31

Files: advisory.go, amutils.go, applyworker.go, btree_array_key.go, btree_interval_key.go, btree_key_decodable.go, btree_scalar_keys.go, bytea.go, cmdtag_table.go, codec.go, codec_aclitem.go, coltypeinfo.go, command_counter.go, context.go, copy.go, copy_binary.go, copy_csv.go, copy_text.go, datum.go, deferred_exclusion.go, deferred_unique.go, executor.go, explain_cte.go, explain_names.go, expr.go, expr_batch.go, expr_replslot.go, exprnode.go, float_in.go, hash_partition.go, heap_fillfactor.go, heap_insert_select.go, index_mutability.go, instrument.go
Findings count: 8

---

## Findings

### `codec.go:parseIntegerInput` — leading-zero decimal string parsed as octal (base-0 detection bug)
- **Bug**: `strconv.ParseInt(cleaned, 0, bitSize)` with base 0 interprets a leading `0` as octal per Go's rules. A string like `"0123"` (which PostgreSQL's `pg_strtoint32` with base 10 parses as decimal 123) is parsed as octal 83. The comment explicitly states "0 prefix octal not supported by PG — decimal", yet the code uses base-0 detection which does exactly that. This affects `evalCast` (`'0123'::int`), `coerceStringToInt64` (COPY/INSERT of `'0123'` into an int column), `plpgsql_runtime.go`, and `pg_input_error_info`. Also, PG's `int4in` rejects hex like `"0x1F"` (error), but this code accepts it (divergence; the 0x/0b/0o extensions are deliberate but the plain `0` → octal is not).
- **When it triggers**: Any string-to-integer coercion where the input has a leading zero: `SELECT '0123'::int` returns 83 instead of 123; `INSERT INTO t(intcol) VALUES ('0123')` stores 83; `COPY t FROM stdin` with `0123` stores 83. The error is silent (wrong value, no error raised).
- **Fix**: Pass base 10 to `ParseInt` for the general case, and handle `0b`/`0o`/`0x` prefixes explicitly before parsing (or strip the `0o` prefix and pass base 8, `0x` → base 16, `0b` → base 2, and default to base 10 for everything else).
- **Severity**: medium

### `btree_array_key.go:encodeArrayBTreeKey` — quoted text element "NULL" encoded as SQL NULL element
- **Bug**: `parseTextArray` unquotes elements. The encoder then checks `strings.EqualFold(strings.TrimSpace(e), "NULL")` with no memory of whether the element was quoted. In PG, `'{NULL}'` is a SQL NULL element but `'{"NULL"}'` is the 4-character string `"NULL"`. Here both collapse to `arrayKeyElemNull` (0xFF), so a `text[]` column whose element is the literal string `"NULL"` encodes as a NULL element. The key collides with a true NULL-element array `{NULL}` (which the heap codec rejects, so it cannot be stored, but the collision still matters for the literal-string case). An index-only scan decodes 0xFF back to unquoted `NULL`, so `{"NULL"}` round-trips to `{NULL}` — a silent data change.
- **When it triggers**: Indexing/probing a `text[]` (or any array) column where an element value is the literal string "NULL". Both stored and probe keys are consistently wrong, so equality finds the row — but the key is indistinguishable from a NULL element, causing wrong uniqueness enforcement and wrong index-only scan output.
- **Fix**: Track whether the element was quoted from `parseTextArray` (or pass a flag), and emit the element as a non-NULL present element when it was quoted, regardless of spelling.
- **Severity**: medium

### `btree_array_key.go:encodeArrayBTreeKey` — multidimensional guard false-positives on quoted `{`
- **Bug**: The check `strings.ContainsRune(s[1:len(s)-1], '{')` scans the raw array literal text for any `{` byte, ignoring quoting/escaping. A valid 1-D array whose quoted TEXT element contains a literal `{` (e.g. `'{"a{b"}'`) is rejected as `0A000 cannot index multidimensional array column`.
- **When it triggers**: Indexing/probing an array column (esp. `text[]`) whose element text contains `{`. Data is not corrupted (conservative refusal), but valid rows fail to index / probes error out.
- **Fix**: Perform the multidimensionality check on the parsed element list (after `parseTextArray`), scanning for a top-level unquoted `{` outside quotes.
- **Severity**: low

### `copy.go:PushBinaryData` — binary COPY FROM does not apply defaults or constraints for missing columns
- **Bug**: The text COPY path (`insertSourceRow`) calls `applyDefaultsForMissing` and enforces NOT NULL / CHECK constraints when `needsConstraints` is true. The binary COPY path (`PushBinaryData`) skips all of this: it directly calls `writeHeapRowReturning` + `maintainUniqueIndexesForInsert` without applying defaults for missing columns or enforcing NOT NULL/CHECK constraints. In PostgreSQL, binary COPY FROM with a column list fills defaults for omitted columns and enforces constraints, just like text COPY. Under goopg, `COPY t (col_a) FROM ... BINARY` would store NULL for unlisted columns instead of their DEFAULT.
- **When it triggers**: Binary COPY FROM with a column list that omits columns having DEFAULT expressions, NOT NULL, or CHECK constraints. The text path handles these correctly.
- **Fix**: Run the same `applyDefaultsForMissing`/constraint sequence in `PushBinaryData`'s `insertSourceRow` that the text path runs.
- **Severity**: medium

### `codec.go:decodePhysicalPGValueLowered` — date/timestamp decode int64 overflow for dates near PG's extended range
- **Bug**: The date decoder computes `micros := int64(days)*24*3600*1000000 + pgEpochUnixMicros`. `days` can be up to ~2.147e9 (int32 max − 1, PG's max date ≈ year 5.87M), giving ~1.85e20 micros — beyond int64 max (~9.2e18). The encoder (line 667) has the symmetric overflow via `t.UnixMicro() - pgEpochUnixMicros`. Go's `UnixMicro` is undefined when the result exceeds int64. So dates beyond ~year 294000 silently wrap to garbage values rather than raising 22008 like PG.
- **When it triggers**: Storing or reading a `date`/`timestamp` near the upper end of PG's supported date range (~year 294000+). Practical impact is nil for real workloads, but it is silent corruption rather than an error, and PG explicitly supports the full range (4714 BC–5874897 AD).
- **Fix**: Detect the overflow and raise 22008 (date/time field value out of range) in both encode and decode arms, or skip the int64-micros conversion entirely for extremes.
- **Severity**: low

### `copy_binary.go:datumToCopyBinary` — int4 range check missing (unlike int2 arm)
- **Bug**: The `int2` arm in `datumToCopyBinary` validates `d.Int < -32768 || d.Int > 32767` and raises 22003. The `int4` arm (line 188-194) performs no range check: `uint32(int32(d.Int))` silently truncates. An int4 column in goopg should never hold an out-of-range value by invariant, but the encoding path is the last line of defense and the int2 arm has the check; the int4 arm should be symmetric.
- **When it triggers**: Only if a corrupted/invariant-violating Datum reaches the binary COPY encode path, producing a stream PG rejects. Not triggered by normal operation.
- **Fix**: Add the same range check as the int2 arm: `if d.Int < -2147483648 || d.Int > 2147483647 { return 22003 }`.
- **Severity**: low

### `copy_binary.go:copyBinaryToDatum` — date/timestamp/timetz decode missing infinity sentinel handling
- **Bug**: The heap decoders for date, timestamp, timetz, and interval intercept the ±infinity sentinels (MaxInt32/MinInt32 for date, MaxInt64/MinInt64 for timestamp). The binary COPY decoders for date (line 586-592), timestamp (line 536-551), timetz (line 568-585), and interval (line 593-614) do not check for these sentinels. A binary COPY stream containing `infinity`/`-infinity` sentinel values would produce garbage `time.Time` values (e.g., `pgEpoch.AddDate(0,0, MaxInt32)` in the date case wraps to a large year). The encode side (`datumToCopyBinary`) also does not handle infinity sentinels for date/timestamp/timetz.
- **When it triggers**: Binary COPY TO/FROM of `infinity`/`-infinity` date/timestamp values. Edge case.
- **Fix**: Add sentinel checks in both encode and decode binary COPY arms, matching the heap codec behavior.
- **Severity**: low

### `codec_aclitem.go:aclModeFromPrivLetters` — bit index of privilege letter is not validated against aclRightsChars length
- **Bug**: `idx := strings.IndexByte(aclRightsChars, c)` returns -1 for an unrecognised privilege character, which is correctly caught. However, `bit := uint64(1) << uint(idx)` — if `idx` is -1, `uint(-1)` wraps to a large value (2^64-1 on 64-bit), and `1 << uint(-1)` is undefined behavior in Go (`shift count type uint, shift count >= 64` → panic in Go 1.25+). The `if idx < 0` guard catches this before the shift, so this is not a live panic — but it's a fragile pattern relying on the guard order.
- **When it triggers**: Only if the `if idx < 0` guard is removed or refactored in the future. Not a live bug.
- **Fix**: Move the shift after the guard as it already is; no change needed. Noted for maintenance.
- **Severity**: low (not a live bug)

---

## Files with no bugs found

- **advisory.go**: Lock/count bookkeeping, re-entrancy, waiter wakeup, and ctx-cancellation removal are all consistent. `wakeWaitersLocked` deletes the whole list so woken/cancelled waiters cannot leak.
- **amutils.go**: Capability switch mirrors amutils.c. The `can_exclude`→`HasGetTuple` mapping is a documented heuristic.
- **applyworker.go**: `applyDatumEqual` has dead inner code but not a live bug. The int4 range check is downstream.
- **btree_interval_key.go**: int128 span arithmetic verified correct (bits.Mul64, two's-complement negation with carry, sign-extended micros add).
- **btree_key_decodable.go**: Consistent with array/scalar decoders.
- **btree_scalar_keys.go**: timetz GMT/zone arithmetic verified; int2/oid range checks present.
- **bytea.go**: b64 encode wrap logic verified equivalent to PG; hex/escape decoders match PG error semantics.
- **cmdtag_table.go**: Static lookup table; validation logic correct.
- **coltypeinfo.go**: Pure memoization of a pure function; no bug.
- **command_counter.go**: Overflow guard correct (wrapping to 0 → panic).
- **context.go**: Lock acquirer deadlock/timeout/cancel error mapping, tuple lock tags, and temp-file registry ownership all correct.
- **copy_csv.go**: CSV parse/encode logic matches PG CopyReadAttributesCSV/CopyAttributeOutCSV.
- **copy_text.go**: Timestamp/date parsers and timezone handling follow PG DecodeDateTime/EncodeDateTime rules.
- **datum.go**: Bit packing and accessors correct.
- **deferred_exclusion.go**: Deferred recheck logic (≥2 rule, chain-tail resolution at commit) mirrors PG.
- **deferred_unique.go**: Same ≥2 rule, HOT chain tail resolution, stale snapshot refresh.
- **executor.go**: Build dispatch covers all plan types; Run helper correct.
- **explain_cte.go**: CTE hoisting keyed by declaration, not by name or pointer.
- **explain_names.go**: Range-table name qualification with disambiguation.
- **expr.go**: (Scanned for structural issues; expression evaluation and type dispatch are consistent with PG.)
- **expr_batch.go**: Stub/forward-compat; no live code.
- **expr_replslot.go**: SQL function wrappers delegate to wal.Slots; error code mapping correct.
- **exprnode.go**: Fast-path expression evaluator with short-circuit guards and sibling-path parity checks.
- **float_in.go**: PG-faithful float8in/float4in port; underflow detection matches PG's ERANGE+val==0 rule.
- **hash_partition.go**: Jenkins hash implementation matches PG's hash_bytes_extended/hash_combine64.
- **heap_fillfactor.go**: Memo cache with invalidation hook; staleness bounds documented.
- **heap_insert_select.go**: FSM candidate selection and batch-extend logic correct.
- **index_mutability.go**: Volatility gate walks every parser.Expr arm; SQL body inlining with recursion guard.
- **instrument.go**: Counter rollup uses seed-based deltas; save/restore on package global.