# Utils (GUC / datetime / mb / activity / misc) — Code Review 2026-08-31

Files reviewed (28):
- `internal/utils/misc/`: datestyle.go, defaults.go, encoding_guc.go, guc.go, parser.go, sample.go, session.go, timestamptz_out.go, version.go
- `internal/utils/adt/datetime/`: adjust_typmod.go, era.go, interval_format.go, interval_typmod.go, monthname.go, normalize.go, pg_datetime_format.go, timeofday.go
- `internal/utils/mb/`: conv.go, euc_jp.go, euc_kr.go, latin1.go, latin2.go, wchar.go
- `internal/utils/activity/`: activity.go, registry.go; `internal/utils/activity/stats/counter.go`
- `internal/utils/errcodes/codes.go`
- `internal/utils/mmgr/mctx.go`
- `internal/utils/adt/similarto/similarto.go`
- `internal/utils/adt/array/pgarray.go`

Findings count: 15

## Misc (GUC)

### `internal/utils/misc/encoding_guc.go:encodingNameToCanonical` — re-cleans the constant encoding table on every call
- **Issue**: Each call iterates the 42-entry constant `pgEncNames` array and calls `cleanEncName(n)` on every entry, doing a fresh allocation + full byte scan per entry, per call. `cleanEncName`'s results are loop-invariant: the table is a compile-time constant.
- **Why**: `encodingNameToCanonical` runs on every `SET client_encoding` / startup-packet encoding validation; the cleaned set never changes yet is rebuilt from scratch each time.
- **Suggestion**: Precompute a `map[string]string` (cleaned name → canonical name) once (a package-level literal or lazily via `sync.Once`) and do a single map lookup. Same pattern exists in the duplicated tables in `internal/catalog/encoding.go` / `internal/initdb/encoding.go`.
- **Severity**: medium

### `internal/utils/misc/guc.go:canonicalizeFrom` — redundant identical branches in the `nativeStr` closure
- **Issue**: The TypeReal range-error path has `if x == float64(int64(x)) { return fmt.Sprintf("%g ms", x) } return fmt.Sprintf("%g ms", x)` — both branches produce the identical string.
- **Why**: Dead duplicate; the conditional is meaningless as written.
- **Suggestion**: Collapse to `return fmt.Sprintf("%g ms", x)` (or `strconv.FormatFloat(x, 'g', -1, 64) + " ms"`).
- **Severity**: low

### `internal/utils/misc/guc.go:parseBoolish` — allocates two strings per call
- **Issue**: `strings.ToLower(strings.TrimSpace(s))` allocates the trimmed copy and then the lowercased copy on every bool-GUC SET and every enum-with-bool-pair fallback.
- **Why**: Bool GUCs are among the most SET, and this also fires for `SET x = on/off` on on/off enums.
- **Suggestion**: Trim once and compare with `strings.EqualFold` per spelling, or hand-roll an ASCII case-insensitive compare over the trimmed span with no allocation.
- **Severity**: low

### `internal/utils/misc/session.go:Set/SetStartup/SetInternal` — repeated lowercasing of the same name
- **Issue**: Each method lowercases the same input up to 3 times: `s.lookupVariable(name)` (via `global.Get` / `custom[...ToLower]`), then `s.Get(name)` (via `lowerGUCName`), then `key := strings.ToLower(name)`.
- **Why**: Not the statement hot path, but every session-layer SET/startup still pays redundant scans that the `Get` method already optimised away.
- **Suggestion**: Lowercase once at method top (`key := lowerGUCName(name)`), then use `lookupVariableLower(key)` and reuse `key` for the session/local map writes (the pattern `Get` already uses).
- **Severity**: low

### `internal/utils/misc/timestamptz_out.go:TimestampToTimestampTZ` — redundant re-copy of the UTC wall clock
- **Issue**: `u := t.UTC()` is already a UTC-tagged `time.Time`; the subsequent `time.Date(u.Year(), …, u.Nanosecond(), time.UTC)` reconstructs an identical value.
- **Why**: `resolveLocalWallClock` only reads the wall-clock fields, so the `time.Date` + field extraction is pure waste per conversion.
- **Suggestion**: `return resolveLocalWallClock(u, sessionLocation(zone))`.
- **Severity**: low

### `internal/utils/misc/datestyle.go` — no issues found
- `fracSecondsSuffix` uses a stack `[6]byte`; `eraDisplay` rebuilds only for BC years; `+` concatenation is on small fixed strings. Nothing worth flagging.

### `internal/utils/misc/defaults.go`, `sample.go`, `version.go` — no issues found
- defaults.go is data-only; sample.go is an embed accessor; version.go is constants.

### `internal/utils/misc/parser.go` — no significant issues
- The multi-token bareword re-join uses `strings.Builder` with sensible growth; `readSingleQuoted`/`readDoubleQuoted` are single-pass. Minor (not worth a finding): `parseConfigLine` returns `line[i]` in one error path after `i == start` was already rejected for `i >= len(line)`, but that is a latent panic edge, not a perf issue.

## Datetime

### `internal/utils/adt/datetime/pg_datetime_format.go` — `fmt.Sprintf` on the per-cell output path
- **Issue**: `formatISODateNoEra` (`fmt.Sprintf("%04d-%02d-%02d", …)`), `formatTimeOfDay` (`fmt.Sprintf("%02d:%02d:%02d", …)` + `fmt.Sprintf("%06d", frac)`), `FormatTimestamp` (`fmt.Sprintf("%s %s", …)`), and `FormatTimeTZ` (`fmt.Fprintf` ×3) each allocate via Sprintf on every cell rendered.
- **Why**: These run once per date/time/timestamp value per row in result-set scans and COPY; a byte buffer with `strconv.AppendInt` (or fixed-width `append` of two digits) avoids the intermediate strings.
- **Suggestion**: Render into a small stack `[]byte` with `strconv.AppendInt` (as `timestamptz_out.go:appendZeroPad2` already does), reusing a scratch buffer across rows where feasible.
- **Severity**: medium

### `internal/utils/adt/datetime/interval_format.go:FormatInterval` — Sprintf-based time-of-day assembly
- **Issue**: `fmt.Fprintf(&sb, "%02d:%02d:%02d", …)` and `strings.TrimRight(fmt.Sprintf("%06d", …), "0")` go through Sprintf's reflection path even though the destination is already a `strings.Builder`.
- **Why**: Per-interval-cell output; the fixed-width two/three-digit fields are trivial to append directly.
- **Suggestion**: Use `strconv.AppendInt` / hand-rolled zero-padding into `sb` (small, format-identical rewrite).
- **Severity**: low

### `internal/utils/adt/datetime/timeofday.go:ParseTimeOfDay` — lowercase allocation just to test for "allballs"
- **Issue**: `lower := strings.ToLower(s)` allocates the whole string solely to compare against the literal `"allballs"`.
- **Why**: Parsing path; minor, but the compare can be done case-insensitively without a copy.
- **Suggestion**: `strings.EqualFold(s, "allballs")`. Same for `splitMeridiem`'s `strings.ToUpper(s[len(s)-2:])`.
- **Severity**: low

### `internal/utils/adt/datetime/timeofday.go:CanonicalizeTimeToken` — zero-pad via `strconv.Itoa(1_000_000_000 + nsec)[1:]`
- **Issue**: The fractional-seconds renderer allocates an `itoa` of `1e9+nsec` purely to slice off the leading `1` as a poor-man's zero-pad, then `TrimRight`s it.
- **Why**: Same per-cell output path as above.
- **Suggestion**: Use `writePadded(&b, strconv.Itoa(tod.Nsec), 9)` semantics (pad to 9 digits) instead of the arithmetic trick.
- **Severity**: low

### `internal/utils/adt/datetime/adjust_typmod.go`, `interval_typmod.go`, `era.go`, `monthname.go`, `normalize.go` — no issues found
- `normalize.go` is well done: canonical inputs short-circuit (`padDateFields`/`padTimeFields` return the input without allocation), `strings.Builder.Grow` is sized, and `digitRun` uses slicing. `monthname.go`'s `strings.ToLower` for the month lookup is on the parse path only and acceptable.

## MB (encoding conversion)

### `internal/utils/mb/conv.go:DoEncodingConversion` — dead `destBuf` allocation
- **Issue**: `destBuf := make([]byte, len(src)*MAX_CONVERSION_GROWTH+1)` (up to 4× the input length, the largest allocation in the function) is allocated and then never used — the procs each allocate and return their own destination, and `_ = destBuf` just silences the compiler.
- **Why**: Wasteful memory traffic (allocation + zeroing up to 4× input size) on every real conversion.
- **Suggestion**: Delete the allocation (and the `consumed`/`destBuf` discards) entirely.
- **Severity**: medium

### `internal/utils/mb/euc_kr.go:euc_kr_to_utf8` — per-character `dec.Bytes` allocations
- **Issue**: The pre-allocated `dest` (sized `len(src)*3`) is only ever used for ASCII bytes; every multi-byte character goes through `dec.Bytes(src[i:i+2])`, which allocates a fresh output slice per character. Similarly `enc.Bytes` in the reverse direction.
- **Why**: EUC_KR text with many double-byte characters triggers one allocation per character during conversion.
- **Suggestion**: Size `dest` to worst case and feed it directly to `dec.Transform` (x/text's in-place API) instead of allocating per call; also stop double-reporting `RuneError` via a second `string()` conversion.
- **Severity**: low

### `internal/utils/mb/latin1.go`, `latin2.go`, `wchar.go`, `euc_jp.go` — no significant issues
- Table-driven with pre-sized destinations and ASCII fast paths. `latin2.go`'s reverse map is built once in `init()`. `euc_jp.go` is intentionally a stub for non-ASCII.

## Activity

### `internal/utils/activity/activity.go:goroutineID` — `runtime.Stack` + string conversion per call
- **Issue**: `goroutineID` allocates a 64-byte buffer and converts the stack dump to a `string` on every call; it is used by `SetCurrentGoroutine`/`ClearCurrentGoroutine` and the legacy lookup fallback.
- **Why**: Connection start/end only, and the gls fast path (registry.go) bypasses it; acceptable. Flagging for awareness since the underlying map + RWMutex (`goroutineActivityMap`) is a second full registry alongside the slot array.
- **Suggestion**: Not worth changing unless the legacy fallback becomes hot; note only.
- **Severity**: low

### `internal/utils/activity/registry.go`, `stats/counter.go` — no issues found
- Wait-event hot path is already allocation-free atomic stores; `Snapshot` is off-path; `formatNanos`/`monoToWall` conversions are fine. `Counter` walks all 256 shards on `Sum`/`Reset` but those are documented non-hot-path consumers, and `maxShards` is bounded.

## Err codes

### `internal/utils/errcodes/codes.go` — no issues found
- Generated constants; `Lookup` is a single map hit on error paths; `Class`'s `string(code[:2])` allocates but is trivial and off-path.

## Memory contexts

### `internal/utils/mmgr/mctx.go:Lookup` — global mutex on the per-datum read path
- **Issue**: `Lookup(id)` takes `ctxMu.Lock()` for a single array index read, and is called from `Datum.StringValue`/`BytesValue` (datum.go:210/234/354/453/661/670) — the hot seqScan/indexScan decode path — for every arena-backed datum.
- **Why**: `ctxMu` is a single process-wide mutex; per-row arena datum reads from all concurrent backends serialize on it, in a design that otherwise went to great lengths to make per-backend state lock-free (activity registry, per-backend contexts). The registry is a fixed `[65536]*Context` array whose pointer writes could be atomic.
- **Suggestion**: Change `ctxRegistry` to an array of `atomic.Pointer[Context]` (or `atomic.UnsafePointer`), making `Lookup` a lock-free atomic load; `releaseID` stores nil atomically. Mutating `ctxRegistryNext`/`ctxFreeList` keeps the mutex, so only the read path becomes lock-free.
- **Severity**: medium

### `internal/utils/mmgr/mctx.go:growChunk` — O(n) chunk-tail copy on every chunk growth
- **Issue**: `copy(c.chunks[newIdx+1:], c.chunks[newIdx:])` memmoves the chunk tail each time a new chunk is inserted after `head`.
- **Why**: Chunk lists are short (a few per statement context), so cost is bounded; note only.
- **Suggestion**: Acceptable; a doubly- or singly-linked chunk list would avoid the shift if contexts ever grow deep.
- **Severity**: low

## SIMILAR TO

### `internal/utils/adt/similarto/similarto.go` — no issues found
- Single-pass state machine into a pre-grown `strings.Builder`; `ValidateEscape` uses `RuneCountInString` (correct for multi-byte escapes). No redundant work.

## Arrays

### `internal/utils/adt/array/pgarray.go:DecodeElemStyled` — `strings.ToLower(elemName)` recomputed per element
- **Issue**: `DecodeElemStyled` calls `strings.ToLower(elemName)` on every invocation, and `RenderTextStyled` calls it once per element in the `for i := 0; i < n; i++` loop (`pgarray.go:301`). `ElemTypeInfo` separately lowercases the same name once. For an n-element array this is n redundant allocations+scans of a loop-invariant string, plus the per-element switch.
- **Why**: Array rendering runs per column per row (heap codec, pgoutput decode); element counts can be large.
- **Suggestion**: Lowercase once in `RenderTextStyled` and pass the lowered name into the loop, or lower once inside `DecodeElemStyled`'s fixed/numeric branches only where a comparison actually needs it (the varlena path only needs it for "numeric"/"bytea").
- **Severity**: medium

### `internal/utils/adt/array/pgarray.go` — otherwise no issues found
- `ByteaOutHex`/`ByteaOutEscape`/`uuidCanonical` are hand-rolled byte builders; `QuoteTextElem` is single-pass. Good.
