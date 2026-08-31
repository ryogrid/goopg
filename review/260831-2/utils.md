# Utils (GUC / datetime / mb / activity / misc) — Bug Review 2026-08-31

Files: internal/utils/misc/{datestyle,defaults,encoding_guc,guc,parser,sample,session,timestamptz_out,version}.go; internal/utils/adt/datetime/{adjust_typmod,era,interval_format,interval_typmod,monthname,normalize,pg_datetime_format,timeofday}.go; internal/utils/mb/{conv,euc_jp,euc_kr,latin1,latin2,wchar}.go; internal/utils/activity/{activity,registry}.go; internal/utils/activity/stats/counter.go; internal/utils/errcodes/codes.go; internal/utils/mmgr/mctx.go; internal/utils/adt/similarto/similarto.go; internal/utils/adt/array/pgarray.go
Findings count: 7

---

### `internal/utils/activity/registry.go:551 (coldFromBackend)` — `BackendStart` never copied from `Backend`; pg_stat_activity.backend_start is always empty
- **Bug**: `coldFromBackend(b *Backend)` constructs a `coldActivity` and copies only `PID`, `DatName`, `UserName`, `ClientAddr`, `ClientPort`, `BackendType`, `ApplicationName`, `State`. It does not copy `b.BackendStart`. `coldActivity.BackendStart` stays at its zero value (0), and `Snapshot()` renders it via `formatNanos(c.BackendStart)` → always `""`.
- **When it triggers**: Every backend/connection that registers with a `Backend.BackendStart` value (e.g. `internal/postmaster/server.go:1089` sets `BackendStart: time.Now().UTC().Format(time.RFC3339Nano)` just before `RegisterAt`) loses the field; `pg_stat_activity.backend_start` is always blank for every connection. Note also the type mismatch — `Backend.BackendStart` is a pre-formatted RFC3339Nano **string**, while `coldActivity.BackendStart` is an **int64 unix-nanos** field, so even a naive copy would need a conversion that does not exist anywhere.
- **Fix**: Convert and copy `b.BackendStart` (parse RFC3339Nano → `UnixNano`, or better, change `Backend.BackendStart` to the raw nanos and format at Snapshot time) inside `coldFromBackend`.
- **Severity**: medium (data loss in a real catalog view column; no crash)

### `internal/utils/misc/guc.go:canonicalizeFrom` (TypeReal arm) — unit suffix / scientific notation silently mis-parsed for unit-less real GUCs
- **Bug**: The `TypeReal` arm scans a numeric prefix and treats everything after the last digit/`+`/`-`/`.` as a `suffix`, but the suffix-conversion block is gated on `v.Unit != UnitNone`. For a unit-less real GUC (`seq_page_cost`, `random_page_cost`, `jit_above_cost`, `parallel_setup_cost`, ...) the suffix is **silently dropped** with no error. Worse, scientific notation is split at the `e`: `SET seq_page_cost = '1.5e3'` yields `numStr = "1.5"`, `suffix = "e3"` which is ignored → the value is stored as **1.5** instead of 1500.
- **When it triggers**: `SET <unit-less real guc> = '1.5e3'` (or any exponent spelling, e.g. `1e-2`, which PG accepts and goopg mis-reads) silently stores a wrong value; `SET random_page_cost = '4.0B'` is accepted as 4.0 instead of rejected. For unit-bearing real GUCs (`vacuum_cost_delay`), the same exponent input is *rejected* ("invalid unit e3"), so behavior is inconsistent and both diverge from PG. (The int path, `parseIntWithUnit`, correctly rejects units when `native == UnitNone`.)
- **Fix**: When `v.Unit == UnitNone` and a suffix remains, return a parse error (mirror upstream `parse_real` which requires the whole string be consumed); and let the numeric-prefix scan recognize the exponent forms (`e`/`E`, optional sign) before splitting suffix.
- **Severity**: medium (silent wrong value / wrong accept-reject behavior; consumed GUCs affected if any unit-less real GUC is load-bearing)

### `internal/utils/misc/guc.go:convertUnit` — int64 overflow on cross-unit conversion
- **Bug**: `return n * fb / tb` multiplies before dividing with no overflow check. `fb` is up to 1024^4 (2^40) for `UnitTB`.
- **When it triggers**: An extreme but syntactically valid input such as `SET work_mem = '1073741824TB'` (native `UnitKB`, MaxVal 1<<40 KB ≈ 2^30 TB) computes `n * fb` = 2^30 * 2^40 = 2^70, wrapping int64; the wrapped (possibly negative) value can then pass the `MinVal/MaxVal` range check and be stored. Upstream PG parses via `pg_strtoint64` and rejects overflow.
- **Fix**: Check `n > (MaxInt64/fb)`-style bounds before multiplying (or divide-then-multiply with remainder correction).
- **Severity**: low (requires absurdly large literal values)

### `internal/utils/mmgr/mctx.go:Context.Release` — mutates `c.children` while ranging over it; skips/double-releases children
- **Bug**: `Release()` does `for _, child := range c.children { child.Release() }`, and `child.Release()` removes itself from the parent's `children` slice via `append(p.children[:i], p.children[i+1:]...)`. The range loop captures the original slice header (fixed length) while the backing array is being shifted underneath it, so with ≥2 children the removal index arithmetic goes wrong: earlier-removed children shift the array so some siblings are **skipped** (never released — their registry slot `releaseID` is never called and their chunks are never returned) while another may be released **twice** (the duplicated trailing element).
- **When it triggers**: Only when a context has 2+ live children at `Release()` time. Typical session/txn/stmt/expr trees have ≤1 child each, so it is latent, but any future fan-out (parallel workers, multiple statement contexts) hits it: leaked contexts stay in `ctxRegistry` (holding their chunks, so memory is not even GC-able) and a child is released twice.
- **Fix**: Iterate a copy of the children (`for _, child := range append([]*Context(nil), c.children...)`) before releasing, or release children without letting them self-remove from the list during the cascade.
- **Severity**: medium (resource/registry-slot leak + double-free of pooled chunks; latent until >1 child)

### `internal/utils/activity/stats/counter.go:Add` — index out of range when GOMAXPROCS > maxShards (256)
- **Bug**: `pid := runtimeshim.PinP(); c.shards[pid].n.Add(delta)` — `PinP()` returns the P index in `[0, GOMAXPROCS)` (pinp_linkname.go), but `Counter.shards` is fixed at `maxShards = 256`.
- **When it triggers**: Any process with `GOMAXPROCS > 256` (a >256-core host, or an explicit `runtime.GOMAXPROCS()` bump, which the code comment itself acknowledges "races with later runtime.GOMAXPROCS() bumps") panics on the first `Add`.
- **Fix**: Clamp/derive the shard count at first use from `GOMAXPROCS` (e.g. `min(GOMAXPROCS(), maxShards)`), or bound the PinP index before indexing.
- **Severity**: low (latent; high-core/misconfigured environments)

### `internal/utils/adt/datetime/normalize.go:padTimeFields` — documented run-together time expansion not implemented ("20200101 040506")
- **Bug**: The package doc for `NormalizeDateTimeInput` promises `"20200101 040506" -> "2020-01-01 04:05:06"`, but `padTimeFields` only rewrites `h:mm[:ss]` colon forms: for a bare run like `"040506"` the leading `digitRun` returns all 6 digits, `len(h) > 2` triggers `return s` unchanged, so the time part stays `"040506"` and the returned string is `"2020-01-01 040506"`. The codebase has no Go layout using a contiguous `150405`-style element (grep for `150405` finds none), so this documented spelling fails to parse where PG accepts it.
- **When it triggers**: `'20200101 040506'::date/timestamp` (run-together 6-digit time after a run-together date) does not get the promised `04:05:06` rewrite; behavior diverges from the documented example (and from PG's DecodeNumberField, which reads a 6-digit run as hhmmss).
- **Fix**: Extend `padTimeFields` (or the date+time composition path) to split a 6-digit run after the date into `HH:MM:SS` (and a 4-digit run into `HH:MM`), mirroring `decodeTimeNumberField` in timeofday.go.
- **Severity**: low (edge-case input; doc/behavior mismatch)

### `internal/utils/misc/session.go:EndTransaction` — rollback fires `invokeOnChange` even when the restored value is unchanged
- **Bug**: In the `!committed` branch, for every key in the `txPrior` journal, `s.global.invokeOnChange(v.Name, after)` is called unconditionally, whereas the `FlagReport`/ParameterStatus side-effect is correctly gated on `after != before`.
- **When it triggers**: A transaction that does `SET x = 'a'` then `SET x = 'a'` (net no change) and rolls back — or any rollback where the pre-transaction session value equals the post-restore value — fires the process-global change callback (`planner.SetNLIEnabled`, `track_io_timing` bridge, etc.) with an unchanged value, causing spurious re-enable/re-toggle work on every rollback.
- **Fix**: Gate the `invokeOnChange` call on `after != before` like the reportable-change branch (only notify on an actual value move).
- **Severity**: low

---

## Files with no bugs found

- `misc/datestyle.go` — `mergeDateStyle` token handling, era math (`eraDisplay`), `fracSecondsSuffix` and the ISO/SQL/Postgres/German renderers verified against PG's `check_datestyle` / `EncodeDateTime` (including the Postgres-style DMY `%a %d %b` vs MDY `%a %b %d` weekday ordering). No defect found.
- `misc/defaults.go`, `misc/version.go`, `misc/sample.go` — registration/data and trivial constants; bounds are PG-faithful. No defect found.
- `misc/encoding_guc.go` — clean-name canonicalization and alias table correct. No defect found.
- `misc/parser.go` — include cycle detection via visited set with `defer delete`, quoted/bareword parsing, multi-token rejoin logic correct. No defect found.
- `misc/timestamptz_out.go` — `resolveLocalWallClock` DST tie-break cases traced (gap/overlap) and correct; `encodeTimezone` widths and `appendZeroPad2` correct.
- `adt/datetime/adjust_typmod.go`, `interval_typmod.go` — rounding (half-away-from-zero) and INTERVAL_MASK range-zeroing switch verified against PG 18.3 for every reachable range; correct.
- `adt/datetime/era.go` — year-zero rejection and BC conversion correct.
- `adt/datetime/interval_format.go` — sign-carry (`isBefore`) logic, pluralization, and 24h+ time formatting match EncodeInterval.
- `adt/datetime/monthname.go` — field assignment (MON-DD-YYYY / DD-MON-YYYY / YYYY-MON-DD) and 2-digit-year windowing correct.
- `adt/datetime/timeofday.go` — meridiem, "10:00.5" MINUTE-TO-SECOND shift, 24:00/23:59:60 handling, `Normalize` day-carry all correct.
- `adt/datetime/pg_datetime_format.go` — `j2date` unsigned port, timetz offset sign handling, fsec trimming correct.
- `mb/conv.go`, `mb/euc_jp.go`, `mb/euc_kr.go`, `mb/latin1.go`, `mb/latin2.go`, `mb/wchar.go` — verifychar ranges and UTF-8 legality checks match PG's wchar.c; latin2 tables/init correct.
- `activity/activity.go` — `goroutineID` parsing bounds-checked. No defect found (aside from the registry BackendStart issue above).
- `errcodes/codes.go` — generated constant tables consistent. No defect found.
- `adt/similarto/similarto.go` — metachar escaping (`\`, `.`, `^`, `$`), bracket tracking (charclass_pos), and the `"..."` part-separator trailer verified byte-for-byte against `similar_escape_internal` (regexp.c). No defect found.
- `adt/array/pgarray.go` — ArrayType layout (HeaderSize 24, elemtype/dataoffset), alignment (`alignPad`), varlena sz decoding, and element widths/aligns all internally consistent and PG-faithful. No defect found.
