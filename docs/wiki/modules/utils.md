# Module: `internal/utils`

The **utility and infrastructure** packages — a collection of leaf packages that
provide foundational services used by every core module. No single package
structure; each sub-package is independent, with no imports from `internal/`
outside `utils/` itself (the **leaf-layer rule**).

## Sub-packages

### `internal/utils/misc` — GUC registry, session state, configuration

The **grand unified configuration** (GUC) engine and per-session state.

| File | LOC | Role |
|---|---|---|
| `guc.go` | 836 | `Registry`/`Variable`: the GUC registry with `Register`, `Get`, `Set`, `ApplyConfigEntries` (conf file), `ApplyReloadEntries` (SIGHUP), `OnChange`, `FormatDisplayValue`, unit conversion, canonicalization. |
| `defaults.go` | 1,603 | `BuildDefaultRegistry`: registers every PG-18 GUC with its default value, context, and unit/hint. The `postgresql.conf.sample` template is kept in sync by `TestSampleConfigCoversRegistry`. |
| `session.go` | 542 | Session state: `SET`/`SET LOCAL`/`RESET`, the undo journal for transaction rollback, `ParameterStatus` tracking, `ReportableVariables`, `ExplainVariables`, `UniqueID`. |
| `parser.go` | 318 | pg_hba.conf- and postgresql.conf-format parser (`ConfigEntry`, `ParseConfigEntries`). |
| `datestyle.go` | 235 | Date/time formatting/parsing helpers (`mergeDateStyle`, datestyle resolution). |
| `timestamptz_out.go` | 281 | `timestamptz`/`timestamp` output formatting (PG-style rendering, `DateStyle`-aware). |
| `encoding_guc.go` | 140 | `client_encoding`/`server_encoding` GUCs + encoding-ID table. |
| `version.go` | 21 | `server_version` / `server_version_num` GUCs. |
| `sample.go` | 20 | `postgresql.conf.sample` generation hook. |

GUC types: `TypeBool/Int/Real/String/Enum`; contexts
`ContextInternal/Postmaster/SigHup/SuBackend/Backend/Suset/Userset`;
sources `SourceDefault/EnvVar/ConfigFile/CommandLine/Override/Session/Local`;
scopes `ScopeServer/Database/Role/Session/Transaction`; units
`UnitNone/Bytes/KB/MB/GB/TB/Ms/S/Min/H/D/Blocks`; flags
`FlagReport/DisallowInFile/NotInSample/Custom/Explain`.

```go
func NewRegistry() *Registry
func (r *Registry) Register(name string, v *Variable) error
func (r *Registry) MustRegister(v *Variable)
func (r *Registry) Get(name string) (*Variable, bool)
func (r *Registry) Set(name, value string, source Source) error
func (r *Registry) ApplyConfigEntries(entries []ConfigEntry) error
func (r *Registry) ApplyReloadEntries(entries []ConfigEntry) ReloadResult
func (r *Registry) OnChange(name string, fn func(value string))
func (r *Registry) All() []*Variable
func NewVariable(spec Variable) *Variable
func (v *Variable) Set(value string, source Source) error
func (v *Variable) Reset()
func (v *Variable) Display() string
func (v *Variable) FormatDisplayValue(raw string) string
func NewSessionRegistry(global *Registry) *SessionRegistry
func (s *SessionRegistry) Get(name) (*Variable, string, bool)
func (s *SessionRegistry) GetDisplay(name) (*Variable, string, bool)
func (s *SessionRegistry) All() []ReportableValue
func (s *SessionRegistry) AllDisplay() []ReportableValue
func (s *SessionRegistry) Set(name, value string, isLocal bool) error
func (s *SessionRegistry) SetStartup(name, value string) error
func (s *SessionRegistry) SetInternal(name, value string) error
func (s *SessionRegistry) Reset(name string) error
func (s *SessionRegistry) ResetAll()
func (s *SessionRegistry) BeginTransaction()
func (s *SessionRegistry) EndTransaction(committed bool)
func (s *SessionRegistry) UniqueID() uint64
func (s *SessionRegistry) SetReportableHook(fn func(name, value string))
func (s *SessionRegistry) ReportableVariables() []ReportableValue
func (s *SessionRegistry) ExplainVariables() []ReportableValue
func IsCustomGUCName(name string) bool
```

**Internal structure** — the `Registry` holds a map of `Variable`s, each
carrying type, context, source, and change hooks; `ApplyConfigEntries` parses
the conf file and rejects unknown parameters; `ApplyReloadEntries` is the
SIGHUP path (warns, never fails; skips Postmaster/Internal contexts).
`SessionRegistry` layers SET (session) and SET LOCAL (transaction) overrides
with lookup precedence `transaction → session → global`. A `txPrior` undo
journal (`map[string]*string`, nil = key absent) records the pre-transaction
session value per key; `EndTransaction(false)` restores it (delete vs write).
`canonicalizeFrom` is `canonicalize` with an explicit baseline — only
`DateStyle` depends on it (`mergeDateStyle` keeps the existing order component
for a partial spec). `FormatDisplayValue` mirrors upstream's
`convert_int_from_base_unit` (negative multiplier for UnitBlocks' kB row).

**GUC details** — `TypeEnum` falls back to the boolean parser only when the
enum offers both `on` and `off` (hidden boolean synonyms, e.g.
`debug_parallel_query`), producing upstream-shaped `ValidationError` Msg + Hint.
`UnitBlocks` stores a count of `blockSize = 8192`-byte pages (the
`min_parallel_*_scan_size` family) and accepts only byte suffixes on input.
Custom GUCs are `FlagCustom` variables created on first `SET` of a
dot-separated name (`IsCustomGUCName`). `NewVariable` panics on an invalid boot
value — boot values are author errors, not user errors. `OnChange` bridges
SQL-level toggles to package-level atomic flags (e.g. `planner.SetNLIEnabled`).

```mermaid
sequenceDiagram
    participant C as SQL SET / conf file / startup packet
    participant SR as SessionRegistry
    participant G as global Registry (Variable)
    participant W as wire (ParameterStatus)

    C->>SR: Set("work_mem", "8MB", local)
    SR->>SR: lowerGUCName + lookupVariable
    SR->>G: canonicalizeFrom(effective, "8MB")
    G-->>SR: "8388608" (canonical, MB→bytes)
    SR->>SR: snapshotPrior(key) + store in session/local layer
    SR->>W: onReportableChange (FlagReport vars only)
    SR->>G: invokeOnChange (process-global bridge)
    C->>SR: EndTransaction(false) — ABORT
    SR->>SR: drop local layer; restore txPrior session values
    SR->>W: onReportableChange for moved FlagReport vars
```

### `internal/utils/adt/datetime` — date/time/interval formatting

The date/time/interval output formatting engine, mirroring PG's `datetime.c`,
`formatting.c`, and `pg_locale.c`.

| File | LOC | Role |
|---|---|---|
| `pg_datetime_format.go` | 223 | `FormatDateTime` (a port of PG's `do_to_timestamp`/`datetime_format`): the `to_char`/`to_timestamp` template engine. |
| `normalize.go` | 528 | `NormalizeTimestamp`/`NormalizeInterval` (time-unit overflow/underflow carry, e.g. 90s → 1min 30s). |
| `timeofday.go` | 424 | `TimeOfDay` (the `now()`/`timeofday()` wall-clock), `TzNames`, timezone name resolution. |
| `interval_format.go` | 110 | `FormatInterval` (ISO 8601, PostgreSQL abbreviated, SQL standard styles). |
| `interval_typmod.go` | 136 | interval typmod decode (precision/fields, e.g. `INTERVAL DAY TO SECOND(3)`). |
| `era.go` | 119 | era name lookup (AD/BC, and CE/BCE for Gregorian). |
| `monthname.go` | 203 | month/day name tables (full and abbreviated, with case handling). |
| `adjust_typmod.go` | 34 | typmod → precision/scale adjustment for timestamps. |

```go
func FormatDateTime(t time.Time, format string) (string, error)
func FormatInterval(months, days, micros int64, style string) string
func NormalizeTimestamp(...) / NormalizeInterval(...)
func TimeOfDay(...)
func ParseIntervalTypmod(typmod int32) (fields int, precision int)
```

**Formatting details** — `FormatDateTime` walks the template one token at a
time, interpreting PG's template patterns (`YYYY`, `MM`, `DD`, `HH24`, `MI`,
`SS`, `MS`/`US` (milliseconds/microseconds), `AM`/`PM`, `TZ`, `OF`, `BC`/`AD`,
day-name and month-name with `FMTM`/`FX` modifiers, quoted literals, and
`"text"` escapes). `normalize.go` carries time-unit overflow/underflow:
`NormalizeInterval` converts e.g. `(0 months, 0 days, 86400_000_000 micros)`
into `(0, 1, 0)` and handles negative carry. `interval_typmod.go` decodes the
packed interval typmod into a field mask (`YEAR`, `MONTH`, `DAY`, `HOUR`,
`MINUTE`, `SECOND`, and `TO`-range combinations) plus seconds precision.
`era.go`/`monthname.go` supply the display-name tables (`eraName`, month and
day full/abbreviated names) used by the template engine.

### `internal/utils/mb` — multi-byte encoding

Character-set encoding support.

| File | LOC | Role |
|---|---|---|
| `conv.go` | 126 | Encoding conversion helpers, byte-order mark handling. |
| `wchar.go` | 157 | Multi-byte character width / validation per encoding. |
| `euc_jp.go` | 159 | EUC-JP (Japanese) converter (JIS X 0208/0212). |
| `euc_kr.go` | 132 | EUC-KR (Korean) converter. |
| `latin1.go` | 107 | LATIN1 converter. |
| `latin2.go` | 136 | LATIN2 converter. |

PG-18 encodings are declared in `misc/encoding_guc.go`; the `mb/` package
provides the actual conversion/width functions. `conv.go` handles the
byte-order mark (BOM) used by UTF-16 client/server encodings and provides the
conversion dispatch; `wchar.go` computes the display width of a character in a
given encoding (needed for correct `length()`-style column-width behaviour and
truncation).

### `internal/utils/activity` — goroutine-level activity tracking

The `ActivityRegistry`: per-backend-goroutine registration, activity kind
(executing query, waiting on lock, idle in transaction), blocking-tag, and
`WaitEvent` tracking. The `pg_stat_activity` view and the lock manager's
deadlock detection both consume this.

| File | LOC | Role |
|---|---|---|
| `registry.go` | 900 | `ActivityRegistry`: per-backend 64 B cache-line-aligned `activitySlot` array (M0107-0005), `Register`/`AcquireConnSlot`, `WaitEventStart`/`WaitEventEnd` (O(1) atomic stores), `Snapshot`, `CountByDatName`, background-worker slots. |
| `activity.go` | 212 | Backward-compat `Registry` alias, `Backend` struct, wait-event type/name constants, goroutine-registration helpers. |
| `stats/counter.go` | 83 | Concurrent-statistics counters (per-counter atomics). |

`activitySlot` layout: `waitInfo` (4 B, packed `(typeCode<<16)|eventCode`),
`stateChange` (8 B unix nanos), `cold` (atomic pointer to `coldActivity`),
padded to exactly 64 B (compile-time asserted). `coldActivity` holds
immutable-after-acquire fields (PID, DatName, …) plus atomically-accessed
mutable fields (XactStart, QueryStart, State, BackendXID/XMin, Query,
`TrackIOTimingOn`, `BackendFlushAfterBlocks`) so `pg_stat_activity` readers
never block query execution. 16 background-worker slots are reserved
(WAL writer, checkpointer, autovacuum, bgwriter, + walsenders/future workers).

```go
func NewRegistry() *Registry                  // 1024 backend slots + 16 bg slots
func NewActivityRegistry(capacity int) *ActivityRegistry
func (r *ActivityRegistry) Register() (*Slot, error)
func (r *ActivityRegistry) SetActivityKind(s *Slot, kind ActivityKind) error
func (r *ActivityRegistry) SetCurrentGoroutine(reg *Registry, procNum int)
func (r *ActivityRegistry) WaitEventStart(s *Slot, typ, event string)
func (r *ActivityRegistry) WaitEventEnd(s *Slot)
func (r *ActivityRegistry) Snapshot() []Backend
func (r *ActivityRegistry) CountByDatName(datName string) int
func (r *ActivityRegistry) UpdateApplicationName(s *Slot, name string)
func PID(pid uint32) string
```

Wait-event constants are upstream-compatible: types `IO/Lock/Client/Timeout/
Activity/IPC/LWLock/BufferPin` and events such as `DataFileRead`, `WALWrite`,
`ClientRead`, `relation`, `WALInsert`, `VacuumDelay`, etc.

**Gotcha** (C3-S5 fix): connection proc slots are RECYCLED on disconnect, so a
re-acquire of a just-freed slot races concurrent registry scans on the `cold`
pointer — hence it is `atomic.Pointer[coldActivity]`, not a plain pointer.
`goroutineID()` uses `runtime.Stack` with a pre-fix bug note (the old loop
searched the whole string for the first space, landing on `s[9]==' '`).

### `internal/utils/errcodes` — SQLSTATE codes

The `codes.go` file (663 LOC) defines every SQLSTATE code as a constant
(`SuccessFulCompletion`, `SerializationFailure`, `SyntaxError`, …) matching
`postgres/src/backend/utils/errcodes.txt`.

### `internal/utils/mmgr` — memory context

The `mctx` (memory context) allocator (517 LOC + 377 test). A PG-style
hierarchical bump-alloc arena:

```
SessionContext  (per-connection)
└── TxnContext  (per-transaction)
    └── StmtContext  (per-statement)
        └── ExprContext  (per-row scratch; optional)
```

Each `Context` owns a set of 64 KiB chunk slabs (`smallChunkSize` 4 KiB for
ExprContext, `largeChunkSize` 256 KiB for sort/hash-join build sides). Chunks
come from per-size `sync.Pool`s; `Reset()` rewinds all chunks to length 0
(retaining backing arrays) and cascades to children; `Release()` returns them
to the pool and removes the context from the registry. Allocation bumps a
pointer inside the current chunk, growing a new one when full
(`growChunk` inserts AFTER the current head, preserving the small-chunk tail).

**Offset encoding** — every allocation is identified by an
`(offset, length uint32)` pair where `offset = chunkIdx*defaultChunkSize +
byteOffsetWithinChunk`, matching the legacy `executor.Arena` encoding so
`KindStringArena`/`KindBytesArena` Datums that store the packed int64 continue
to work. `Bytes(offset, length)` resolves via `chunkIdx = offset / c.cs`.

```go
func NewContext() *Context                       // legacy alias for Acquire(nil, KindSession)
func Acquire(parent *Context, kind Kind) *Context
func (c *Context) ID() ContextID
func (c *Context) Generation() uint32
func (c *Context) Reset() / Release()
func (c *Context) Alloc(n int) []byte
func (c *Context) AllocAligned(n, align int) []byte
func (c *Context) AllocBytes(b []byte) (offset, length uint32)
func (c *Context) AllocString(s string) (offset, length uint32)
func (c *Context) Bytes(offset, length uint32) []byte
func (c *Context) Usage() (allocated, peak int64)
func Lookup(id ContextID) *Context
func Perm() *Context                             // process-global permanent context (slot 1)
func AllocFor[T any](c *Context) *T
func AllocSlice[T any](c *Context, n int) []T
```

The permanent context (slot 1) is the ONE context legitimately shared across
goroutines (big-mantissa numerics allocate from it, never Reset/Released); it is
guarded by a package-level `permMu` RWMutex (`isShared()` is
`c.id == PermContextID`), while every per-session/per-statement/per-expression
context keeps the unsynchronized fast path. `gen` is bumped on `Reset()` for
debug use-after-Reset detection. `lifetimeCounters` (`totalAllocated`,
`currentBytes`, `peakBytes`) back `EXPLAIN (MEMORY)` and survive `Reset()`.

**Gotchas** — `AllocSlice[T]` requires a pointer-free `T` (the backing slab is
allocated as `[]byte`, a GC noscan span; a Go pointer stored in a `T` is
invisible to the mark phase). `putChunk` rejects buffers whose `cap != cs` —
an oversized chunk (from a single allocation exceeding the chunk size) would
otherwise land in the wrong pool and break the offset encoding (an in-chunk
offset ≥ cs aliases to chunkIdx+1, and `Bytes()` resolves into the wrong chunk,
silently returning nil). The `Context` struct is size-constrained to ≤ 96 B
(`TestContextSizeof`), which is why `lifetimeCounters` and the permanent-context
mutex live off-struct.

### `internal/utils/adt/similarto` — SIMILAR TO regex

The `similarto.go` file (179 LOC) translates PG's `SIMILAR TO` pattern syntax
into a Go `regexp` for `LIKE`-style pattern matching (rewriting `%`→`.*`,
`_`→`.`, character classes, and escaping `SIMILAR TO` metacharacters).

### `internal/utils/adt/array` — array utility helpers

The `pgarray.go` file (529 LOC) provides array element type name resolution /
lookup helpers used by the catalog and executor, plus array-output formatting
helpers (`array_elem_output_style`, `bytea_output_escape`).

## Public API

There is no single entry point — each sub-package exposes its own surface (see
the per-package API blocks above).

## Dependencies

- **Used by** — every core package: `internal/executor`, `internal/optimizer`,
  `internal/parser`, `internal/storage`, `internal/access/*`, `internal/postmaster`,
  `internal/catalog`, `internal/replication`, `internal/initdb`, `internal/port`.
- **Uses** — nothing inside `internal/` outside `utils/` itself (leaf-layer
  rule); only the standard library and, for `mb`, the Go `unicode` tables.

## Notable patterns / gotchas

- **GUC registry singleton** — `BuildDefaultRegistry` is called once at startup;
  `Register` is used for custom GUCs. Do not import `internal/executor` or
  `internal/optimizer` from here (layer rule). `guc.go` deliberately keeps
  `blockSize = 8192` local rather than importing `internal/storage`.
- **GUC undo journal** — `session.go` tracks plain `SET` (not `SET LOCAL`)
  values in a `txPrior` map so transaction ABORT reverts them; `SET LOCAL`
  is journalled separately via the local layer, dropped on both COMMIT and
  ROLLBACK. `RESET` restores the session-start (startup) value (PG's
  `reset_val`), NOT the boot value — `SetStartup` is the only writer of the
  startup map, and a user `SET` must never write it (the reciprocal note at
  `Set` prevents drift).
- **`client_min_messages`** — the elevel ceiling (21 = ERROR) is enforced
  once at the single emitter, not at every call site; INFO always sends.
- **Encoding tables** — the encoding-ID table and the `encoding_guc.go` table
  must stay in sync with PG 18's `pg_enc.c`; a stale table means a
  `SET client_encoding` to a valid PG encoding silently fails.
- **`mmgr` is not GC-free** — the memory context is a bump-alloc arena; it does
  not replace Go's GC, it reduces allocation overhead for short-lived parse
  trees. Contexts are cleared after each statement.
- **`Get` fast path** — GUC lookup lowercases once; `Registry.Get` probes the
  map with the raw name first (registration stores the lowercase key) so
  lowercase compile-time constants skip `strings.ToLower` entirely — GUC
  lookups were 4.5% of read-path CPU (perf-optimize-take3/06 candidate F).
- **SessionRegistry is not goroutine-safe** — one per connection-handler
  goroutine; `BeginTransaction`/`EndTransaction` must bracket every explicit
  transaction.
- **`FormatDisplayValue` units** — `SHOW statement_timeout` on the disabled
  value prints a bare "0" (unit conversion restricted to `n > 0`, matching
  real PG), and blocks-valued GUCs smaller than 1 MB use the negative-multiplier
  kB branch to print "512kB" rather than a bare number.
- **`SET x = DEFAULT`** — `SessionRegistry.Set` treats `DEFAULT` (case-
  insensitive) the same as `RESET`, restoring the startup value when one
  exists, else the boot value.
- **Enum HINT shape** — a failed enum `SET` returns `ValidationError` with a
  separate `Hint` (`Available values: …`) rather than baking the list into the
  message, matching PG's `set_config_option` PGC_ENUM branch; callers that can
  surface a HINT should `errors.As` for the type.