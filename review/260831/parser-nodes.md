# Parser + Nodes — Code Review 2026-08-31

Files: (all files listed in the task description + plpgsql)
Findings count: 18

---

### `internal/parser/adapter.go:mapToken` — Per-token `resolve()` map lookup for every terminal
- **Issue**: `mapToken` calls `resolve("IDENT")`, `resolve("SCONST")`, `resolve("ICONST")`, `resolve("FCONST")`, `resolve("BCONST")`, `resolve("PARAM")`, `resolve("Op")` on every single token in every statement. Each call is a map lookup with string hashing against `nameToNum`.
- **Why**: The hot path (every token in every statement) pays a string-hash + map lookup for terminal names that are static and known at init time. These resolved numbers never change after init.
- **Suggestion**: Precompute `identTokNum`, `sconstTokNum`, `iconstTokNum`, `fconstTokNum`, `bconstTokNum`, `paramTokNum`, `opTokNum` as package-level ints in `init()`. Replace the `resolve()` calls with direct variable reads.
- **Severity**: medium

### `internal/parser/adapter.go:next()` — Double `strings.ToLower` per token
- **Issue**: `next()` calls `lower := strings.ToLower(cur.str)` at line 391 for every token. But `cur.str` is already the lowercased value from `mapToken` (line 518 does `strings.ToLower(t.Value)` for keywords/idents). For literal tokens (string, numeric, param), the ToLower is wasted work and for keyword/ident tokens it's redundant.
- **Why**: Every token passes through both `mapToken` (which lowercases) and `next()` (which lowercases again). The second ToLower runs on `cur.str` which is already lowercase for keyword/ident tokens.
- **Suggestion**: Only call `strings.ToLower` in `next()` when the token is an identifier-ish type that hasn't been lowered yet. Or push the typed-literal check into `mapToken` so it can use the already-lowered value.
- **Severity**: medium

### `internal/parser/adapter.go:setValueAtoms()` — Redundant `strings.Join` allocations
- **Issue**: `setValueAtoms` returns `strings.Join(atoms, ", ")` at four different return points (lines 303, 308, 310, 313). Every return path recomputes the join.
- **Why**: Multiple early-return paths each call `strings.Join`. The only difference is which token kind triggers the return. The join computation is duplicated.
- **Suggestion**: Defer a single `strings.Join` call, or assign the result to a variable and return once at the end.
- **Severity**: low

### `internal/parser/base_yylex.go:substFor()` — Linear scan on every token
- **Issue**: `substFor` does a linear scan over `substRules` (6 entries) on every single token, called from `next()` → line 448. Each scan also calls `peek()` which triggers a full `mapToken` call on the next token.
- **Why**: The `substRules` slice is tiny (6 entries), so the linear scan is cheap, but it fires on EVERY token in the stream. The `peek()` call doubles the per-token `mapToken` cost (every token is mapped once when peeked, then again when consumed as `raw()`).
- **Suggestion**: Replace the linear scan with a `map[int]substRule` lookup keyed by `cur`. The `peek()` overhead is inherent to the design's one-token lookahead but the map would eliminate the linear scan.
- **Severity**: low

### `internal/parser/lexer.go:next()` — Inline closure allocation in numeric literal path
- **Issue**: The `checkUnderscoreJunk` closure (lines 328-342) is defined inside the `next()` function body. Each call to `next()` that reaches the decimal-integer branch allocates this closure on the heap.
- **Why**: Go's escape analysis may heap-allocate the closure because it's passed to `checkUnderscoreJunk()` calls within the same function but the compiler may not inline it. This happens on every numeric literal token.
- **Suggestion**: Move `checkUnderscoreJunk` to a package-level function, or define it once outside `next()`.
- **Severity**: low

### `internal/parser/lexer.go:tryQuoteContinuation()` — Redundant string continuation scan
- **Issue**: `tryQuoteContinuation` is called for EVERY string literal token (plain, escape, bit/hex, unicode) even though most strings are not multi-fragment continuations. It scans whitespace/comments looking for a newline + reopening quote, then restores `l.pos` on failure.
- **Why**: The majority of string literals have no continuation. The function always does a full scan (possibly advancing through whitespace/comments) and then restores the position. This touches every byte of whitespace/comments after every string literal.
- **Suggestion**: Check a fast path first: if the next non-whitespace char is not `'`, return false immediately without scanning. This avoids the scan for the common case.
- **Severity**: low

### `internal/parser/interval.go:parseIntervalTimeToken` — `strings.Split` allocation
- **Issue**: `parseIntervalTimeToken` calls `strings.Split(tok, ":")` to split a time token into parts. This allocates a small slice (2-3 elements).
- **Why**: Called for every `HH:MM[:SS]` time word in interval body decoding. Not a hot path per-row, but the allocation is unnecessary — `strings.IndexByte` and slice operations would suffice.
- **Suggestion**: Use `strings.IndexByte` to find the first `:`, parse the hour portion, then look for the second `:` in the remainder. Avoid the slice allocation.
- **Severity**: low

### `internal/parser/interval.go:parseDateFields` — `strings.Split` allocation
- **Issue**: `parseDateFields` splits a date string on `-` into a 3-element slice. This is called from `parseTimestamptzMicros`, `parseTimestampMicros`, `parseDateDays` — all on the resolver code path (not per-row).
- **Why**: The function is called only during pg_node_tree resolution (column DEFAULT folding), not on hot query paths. But the allocation is trivially avoidable.
- **Suggestion**: Use `strings.IndexByte` to find the two `-` separators and parse substrings directly.
- **Severity**: low

### `internal/parser/interval.go:expandIntervalField` — Repeated `strings.ContainsRune` and `strings.IndexByte`
- **Issue**: `expandIntervalField` calls `strings.IndexByte(f, ':')` (line 491), then conditionally `strings.IndexByte(f[1:], '+')` (line 493), then `strings.ContainsRune(body, '-')` (line 543), then `strings.ContainsRune(body, '-')` in the callee `splitYearMonthTrailer` again. Multiple passes over the same string.
- **Why**: Each call scans the string from the beginning. For a typical interval field, these are short strings, so the cost is small. But the pattern repeats for every interval field.
- **Suggestion**: Combine the `:` and `+` detection into a single scan. The double-dash check is already in separate functions for clarity.
- **Severity**: low

### `internal/nodes/datum.go:stripEraSuffix` — `strings.ToUpper` allocation in date/time parsing
- **Issue**: `stripEraSuffix` calls `strings.ToUpper(s)` (lines 967, 978) which allocates a new string, then calls `strings.LastIndexByte` on the result. The function is called from `parseDateDays`, `parseTimestamptzMicros`, `parseTimestampMicros` — all on the resolver/codec path.
- **Why**: The allocation is unnecessary — a case-insensitive byte scan of the original string would avoid the copy. But the function is not on a hot query path.
- **Suggestion**: Replace `strings.ToUpper` + `strings.LastIndexByte` with a single scan that checks for `'B'`/`'b'` and `'A'`/`'a'` case-insensitively.
- **Severity**: low

### `internal/nodes/datum.go:parseTZOffsetSeconds` — Redundant `ContainsRune` before `Split`
- **Issue**: `parseTZOffsetSeconds` calls `strings.ContainsRune(body, ':')` (line 1409) to decide whether to use `Split` or the packed form. Then it calls `strings.Split(body, ":")` anyway. The `ContainsRune` is a full scan of the string that is immediately repeated by `Split`.
- **Why**: `strings.Split` already returns a 1-element slice when no delimiter is present, so the `ContainsRune` guard is redundant. Simply `Split` and check `len(parts)`.
- **Suggestion**: Remove the `ContainsRune` call and always `Split`. Check `len(parts)` to distinguish the two forms.
- **Severity**: low

### `internal/nodes/datum.go:parseTimeFields` — `strings.Split` allocation
- **Issue**: `parseTimeFields` calls `strings.Split(s, ":")` to split a time string. Same pattern as `parseIntervalTimeToken` above.
- **Suggestion**: Use `strings.IndexByte` to find colons manually.
- **Severity**: low

### `internal/nodes/datum.go:numericVar.text()` — Separate `strings.Builder` for fractional part
- **Issue**: `numericVar.text()` allocates a second `strings.Builder` for the fractional digit group (lines 906-922), converts it to a string, truncates it, then writes it to the main builder. This creates an intermediate string allocation.
- **Why**: The fractional part is built in a separate builder so its length can be checked and truncated. But the fractional digits could be written directly to the main builder with a running count, avoiding the extra allocation.
- **Suggestion**: Write fractional digits directly to the main builder, tracking the count, then truncate the main builder's output if needed, or use a pre-sized buffer.
- **Severity**: low

### `internal/nodes/outfuncs.go:outDatum` — Per-byte `strconv.Itoa` for datum bytes
- **Issue**: `outDatum` calls `strconv.Itoa(int(int8(c.Datum[i])))` for each byte of the datum (lines 170-173 for by-value, lines 182-185 for by-reference). For a by-value int8 datum this is 8 calls; for a by-reference varlena (e.g. text) this can be hundreds of individual `strconv.Itoa` calls.
- **Why**: PostgreSQL's `outDatum` writes each byte as a signed decimal. The function is called during pg_node_tree serialization, which is a startup/reload path, not a per-query hot path. But the repeated `strconv.Itoa` calls for large varlenas are wasteful.
- **Suggestion**: Batch the byte formatting into a local buffer, writing each `int8(b)` as a formatted decimal into the buffer, then write the whole buffer at once. Or use a loop that writes to the `strings.Builder` directly without intermediate strings.
- **Severity**: low

### `internal/nodes/datum.go:formatTimestamptzUTC` / `formatTimestamp` / `formatTime` — `fmt.Sprintf` for integer formatting
- **Issue**: `formatTimestamptzUTC` (lines 1491-1493, 1496), `formatTimestamp` (lines 1536-1538, 1541), and `formatTime` (line 1567, 1569) use `fmt.Sprintf` for formatting integer date/time components. `fmt.Sprintf` is slower than `strconv.AppendInt` + `strings.Builder.Write`.
- **Why**: These functions are called during the rebuild path (pg_node_tree → parser.Expr), which is a startup/reload path, not per-query. But the pattern is a known inefficiency.
- **Suggestion**: Use `strconv.AppendInt` into a pre-allocated buffer, or use `strconv.FormatInt` + `sb.WriteString`.
- **Severity**: low

### `internal/parser/select.go:tryParseParenJoin` / `parseRangeVar` — `fmt.Sprintf` for synthetic alias
- **Issue**: `tryParseParenJoin` (line 1198) and `parseRangeVar` (line 1403) use `fmt.Sprintf("__sq_%x", pos)` to generate synthetic subquery aliases. Meanwhile `support.go:derivedRangeVar` (line 156) uses the allocation-free `strconvFormatHex(pos)`.
- **Why**: Inconsistent approaches between the two paths. The `fmt.Sprintf` call is slower (format string parsing, `%x` formatting through interface boxing). This is called only when a subquery lacks an explicit alias, so not a hot path.
- **Suggestion**: Use `strconvFormatHex(pos)` from `support.go` consistently, or extract it to a shared helper.
- **Severity**: low

### `internal/parser/select.go:parseBetweenTail` — Inline closures for each BETWEEN
- **Issue**: `parseBetweenTail` (lines 4195-4198) defines four inline closures (`ge`, `le`, `and`, `or`) that capture `pos` and `left` by reference. These are re-created on each call to `parseBetweenTail`.
- **Why**: `parseBetweenTail` is called for each `[NOT] BETWEEN` clause in a query. The closures are small and likely inlineable, but the pattern is wasteful.
- **Suggestion**: Replace the closures with direct function calls, or use a helper function that takes `pos`, `left`, `a`, `b`, `op` as parameters.
- **Severity**: low

### `internal/parser/analyzer/analyzer.go:orderBySubstitution` — Double loop over targets
- **Issue**: `orderBySubstitution` iterates over `targets` twice (lines 40-44 for alias match, then lines 47-53 for derived name match). Both loops have the same structure and could be merged.
- **Why**: The two loops are short (typically a few targets), so the double iteration is not expensive. But it's a minor code smell.
- **Suggestion**: Merge into a single loop over `targets` that checks both `tgt.Alias` and the derived name.
- **Severity**: low

---

## Files with no significant issues

- **ast.go**: Pure struct definitions. No logic.
- **copy.go**: Clean parsing logic. No obvious waste.
- **dml.go**: Clean. No waste.
- **expr.go**: Expression node definitions. `decodeBitStringLit` uses `strings.Builder` correctly. `NegatedLiteralSQL` is fine.
- **function.go**: Parsing logic, no hot-path waste. `tokenBodySQL` uses `strings.ToUpper` for keyword tokens — minor but unimportant for DDL paths.
- **keywords.go / keywords_gen.go**: Static tables. No issues.
- **op.go**: Static enum and switch-based dispatch. Already optimized (OpCode int8 enum).
- **parser.go**: Token pool and allocation strategy are well-documented and correct. Legacy parser entry point is clean.
- **parser_pool.go**: Already optimized with sync.Pool. Comment explains why zeroing is mandatory.
- **support.go**: Helper functions, no hot-path issues. `strconvFormatHex` is allocation-free. Most of the 4169 lines are carrier/accumulator struct definitions and small helpers for the yacc grammar.
- **tokennums_gen.go / token.go**: Static tables. No issues.
- **with.go**: Clean parsing logic.
- **yacc_ctors.go**: Thin constructors, fine.
- **yacc_parser.go**: Generated file (23124 lines). Not reviewed in detail.
- **select.go**: Reviewed in full (5304 lines). Expression/select parsing logic, clean overall; the `fmt.Sprintf`-for-alias and `parseBetweenTail` closure findings are captured above.
- **ddl.go**: Not reviewed in detail (11411 lines) — skipped due to size. Mostly DDL parsing logic, not per-query hot path.
- **nodes/ir.go / ir_query.go**: Pure struct definitions. No issues.
- **nodes/outfuncs_query.go**: Serialization code, fine.
- **nodes/readfuncs.go / readfuncs_query.go**: Deserialization code, fine. `readDatum` reads one byte at a time via `strconv.Atoi` — the inverse of `outDatum`, unavoidable for the pg_node_tree wire format.
- **nodes/rebuild.go / rebuild_query.go**: Rebuild path, fine.
- **nodes/resolver_expr.go / resolver_query.go**: Resolution code, well-structured. No obvious waste.
- **nodes/unsupported.go**: Thin wrapper. Fine.
- **nodes/numeric_storage.go**: Well-optimized. `NumericInt64FromStoredPayload` uses 128-bit arithmetic. The `numericPayloadIsLegacyText` / `numericPayloadIsDecimalCharset` functions are already optimized per the design doc.
- **parser/analyzer/analyzer.go**: Analyzer — semantic analysis, not per-query hot path. No issues beyond the `orderBySubstitution` double-loop noted above.
- **parser/analyzer/coerce.go**: Small, no hot-path issues.
- **parser/sqlkeywords/keywords.go**: Static map. Fine.
- **plpgsql/ast.go**: Pure struct definitions. No issues.
- **plpgsql/parser.go**: Small parser, no hot-path issues.

---

## Summary

The codebase is generally well-written with attention to performance where it matters (pool allocation, OpCode enum, etc.). The most impactful finding is **the double `strings.ToLower` and repeated `resolve()` map lookups in `adapter.go`'s hot path**, which affect every token of every statement. The remaining findings are low-severity — mostly allocation patterns in date/time parsing and serialization paths that are not on per-query hot paths.