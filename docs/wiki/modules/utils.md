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
func (v *Variable) canonicalize(value string) (string, error)
func (v *Variable) canonicalizeFrom(current, value string) (string, error)
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
func (c *Context) growChunk(n int)
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

### `internal/utils/adt/similarto` — SIMILAR TO regex

The `similarto.go` file (179 LOC) translates PG's `SIMILAR TO` pattern syntax
into a Go `regexp` for `LIKE`-style pattern matching (rewriting `%`→`.*`,
`_`→`.`, character classes, and escaping `SIMILAR TO` metacharacters).

### `internal/utils/adt/array` — array utility helpers

The `pgarray.go` file (529 LOC) provides array element type name resolution /
lookup helpers used by the catalog and executor, plus array-output formatting
helpers (`array_elem_output_style`, `bytea_output_escape`).

## Key flow: memory context reset cascade

```mermaid
sequenceDiagram
    participant E as executor End()
    participant C as Session Context
    participant T as Txn Context
    participant S as Stmt Context
    participant X as Expr Context
    E->>C: Release() (end of connection)
    C->>C: pool chunk slabs, drop child registry
    E->>T: Reset() (end of transaction)
    T->>S: cascade Reset()
    S->>X: cascade Reset()
    X->>X: rewind chunks to len 0 (backing arrays retained)
    X->>X: gen++ (use-after-Reset detection)
    T->>T: lifetimeCounters survive
    T->>T: bump gen, reset alloc pointer
```

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
- **`AllocSlice[T]` pointer-free requirement** — the backing slab is allocated
  as `[]byte` (a GC noscan span); a Go pointer stored in a `T` is invisible to
  the mark phase. Use `AllocFor[T]` for pointer-bearing types.
- **`putChunk` rejects `cap != cs`** — an oversized chunk from a single
  allocation exceeding the chunk size would land in the wrong `sync.Pool` and
  break the offset encoding (an in-chunk offset ≥ cs aliases to chunkIdx+1,
  and `Bytes()` resolves into the wrong chunk, silently returning nil).
- **Context struct is ≤ 96 B** — `TestContextSizeof` pins it; `lifetimeCounters`
  and the permanent-context mutex live off-struct because they would blow the
  size budget.
- **`goroutineID` via `runtime.Stack`** — the old loop searched the whole stack
  string for the first space, landing on `s[9]==' '`; the fixed version walks
  to the "goroutine" prefix. Keep the fix — a bare search silently picks the
  wrong number on unrelated stack layouts.

## GUC variable internals

`Variable` struct (guc.go:136):

```go
type Variable struct {
    Name    string
    Value   string
    BootValue string
    RstVal  string  // reset_val — the session-start value
    Min, Max int64
    Type    Type    // Bool, Int, Real, String, Enum
    Context Context // Internal, Postmaster, SIGHUP, Backend, Suset, Userset
    Source  Source  // Default, EnvVar, ConfigFile, CmdLine, Override, Session, Local
    Scope   Scope   // Server, Database, Role, Session, Transaction
    Unit    Unit    // None, Bytes, KB, MB, GB, TB, Ms, S, Min, H, D, Blocks
    Flags   VarFlag // Report, DisallowInFile, NotInSample, Custom, Explain
    Enum    []string
    OnChange func()
    shortDesc, longDesc string
    // ... internal state
}
```

`canonicalize` converts a user-supplied value to canonical form:
- Ints: `parseIntWithUnit` → unit conversion to native unit.
- Enums: case-insensitive match against `Enum[]`, fallback to bool parser if
  the enum has both "on" and "off" entries.
- Bools: `parseBoolish` (1/yes/on/true/t, 0/no/off/false/f).
- Reals: `strconv.ParseFloat` with locale-independent decimal point.

`canonicalizeFrom` is the same but with an explicit baseline — used only by
`DateStyle`, where a partial spec (e.g. `SET DateStyle = 'SQL'`) must keep the
existing order component.

## `BuildDefaultRegistry` structure (`defaults.go`)

`BuildDefaultRegistry` (1,603 LOC) registers every PG18 GUC. The function
follows the shape of PG's `guc_tables.c`:

```go
func BuildDefaultRegistry() *Registry {
    r := NewRegistry()
    r.MustRegister(vars...) // ~500 Variables
    return r
}
```

Each `Variable` is constructed with `NewVariable`:

```go
r.MustRegister(NewVariable(Variable{
    Name: "work_mem", Type: TypeInt, Context: ContextUserset,
    Value: "4194304", BootValue: "4194304",
    Min: 65536, Max: math.MaxInt64, Unit: UnitKB,
    shortDesc: "Sets the maximum memory used for query workspaces.",
    longDesc:  "...",
}))
```

The `TestSampleConfigCoversRegistry` test ensures every GUC with
`FlagNotInSample` cleared appears in the `postgresql.conf.sample` template.

## SessionRegistry transaction journal (`session.go`)

`BeginTransaction` snapshots the current session values into `txPrior`:
any `SET` (not `SET LOCAL`) during the transaction records the value it
replaces. `EndTransaction(false)` — ABORT — restores every `txPrior` entry:
if the prior value was nil (key absent), the key is deleted; otherwise the
prior value is written back. `EndTransaction(true)` — COMMIT — drops the
journal; `SET` values are now permanent.

`SET LOCAL` values are stored in a separate `localLayer` map (not journalled).
On both COMMIT and ROLLBACK, the local layer is dropped. On ABORT, the
`txPrior` restore runs AFTER the local layer is dropped, so the session
returns to the pre-transaction state.

## `mb` encoding conversion dispatch (`conv.go`)

`conv.go` provides `Convert` (encoding conversion) and `BOMHandling` (UTF-16
byte-order mark detection). The conversion dispatch maps (src encoding,
dst encoding) to a converter function. Known converters are in `euc_jp.go`,
`euc_kr.go`, `latin1.go`, `latin2.go`. Each converter implements:
- `Convert(src []byte) ([]byte, error)` — transcode bytes.
- `Width(r rune) int` — display width for the encoding.

## ActivityRegistry slot layout (`registry.go`)

`activitySlot` is 64-byte cache-line-aligned:

```
offset 0:  waitInfo uint32 (typeCode<<16|eventCode)
offset 4:  _pad0 [4]byte
offset 8:  stateChange int64 (unix nanos)
offset 16: cold atomic.Pointer[coldActivity]  // 8 bytes
offset 24: _pad1 [40]byte  // pad to 64
```

`coldActivity` is a separately allocated struct (not inline) so the atomic
pointer swap is the only write: `cold = new(coldActivity)`, then `Store`.
The old `cold` is garbage-collected.

`Snapshot()` iterates all slots, reads the `cold` pointer atomically, and
copies the fields. It never blocks a backend — if a backend is mid-Reset
(disconnecting), the `cold` pointer may be nil, and the snapshot skips that
slot.

## Annual activity slot count

`capacity` defaults to 1024 (matching PG's `max_connections` default). 16
background-worker slots are reserved for WAL writer, checkpointer, autovacuum,
bgwriter, and walsenders. The `Register()` method returns an error when all
slots are full.

## GUC bridges (`OnChange`)

`OnChange` hooks bridge SQL-level SET to package-level atomic flags:

```go
r.OnChange("enable_nestloop", func(value string) {
    planner.SetNLIEnabled(value == "on")
})
r.OnChange("work_mem", func(value string) {
    executor.SetWorkMem(value)
})
r.OnChange("enable_seqscan", func(value string) {
    planner.SetSeqScanEnabled(value == "on")
})
r.OnChange("enable_hashjoin", func(value string) {
    planner.SetHashJoinEnabled(value == "on")
})
r.OnChange("enable_mergejoin", func(value string) {
    planner.SetMergeJoinEnabled(value == "on")
})
```

These are declared in the `init()` functions of the respective packages and
registered in `BuildDefaultRegistry`'s init chain.

## `internal/utils/errcodes` — SQLSTATE codes

The `codes.go` file defines every SQLSTATE code as an `errcodes.Code` type.
Key groups:

| Range | Category |
|---|---|---:|
| `00000` | Successful completion |
| `01000` | Warning |
| `02000` | No data |
| `03XXX` | SQL statement not yet complete |
| `08XXX` | Connection exception |
| `09XXX` | Triggered action exception |
| `0AXXX` | Feature not supported |
| `0DXXX` | Invalid target type specification |
| `0FXXX` | Locator exception |
| `0LXXX` | Invalid grantor |
| `0PXXX` | Invalid role specification |
| `20XXX` | Case not found for CASE statement |
| `21XXX` | Cardinality violation |
| `22XXX` | Data exception (division by zero, numeric overflow, etc.) |
| `23XXX` | Integrity constraint violation |
| `24XXX` | Invalid cursor state |
| `25XXX` | Invalid transaction state |
| `26XXX` | Invalid SQL statement name |
| `27XXX` | Triggered data change violation |
| `28XXX` | Invalid authorization specification |
| `2BXXX` | Dependent privilege descriptors still exist |
| `2DXXX` | Invalid transaction termination |
| `2FXXX` | SQL routine exception |
| `34XXX` | Invalid cursor name |
| `38XXX` | External routine exception |
| `39XXX` | External routine invocation exception |
| `3BXXX` | Savepoint exception |
| `3DXXX` | Invalid catalog name |
| `3FXXX` | Invalid schema name |
| `40XXX` | Transaction rollback (serialization failure, deadlock, etc.) |
| `42XXX` | Syntax error or access rule violation |
| `44XXX` | WITH CHECK OPTION violation |
| `53XXX` | Insufficient resources |
| `54XXX` | Program limit exceeded |
| `55XXX` | Object not in prerequisite state |
| `57XXX` | Operator intervention |
| `58XXX` | System error (IO error, etc.) |
| `F0000` | Config file error |
| `HVXXX` | FDW error |
| `P0000` | PL/pgSQL error |
| `XX000` | Internal error |

`SerializationFailure` (40001) is used by SSI pre-commit.
`DeadlockDetected` (40P01) is used by the WFG deadlock checker.
`UniqueViolation` (23505) is used by unique constraint checks.
`ForeignKeyViolation` (23503) is used by FK enforcement.

## `internal/utils/adt` overview

Beyond `datetime`, `similarto`, and `array`, the `adt/` tree currently
contains exactly three leaf packages (verified against the source): `array`,
`datetime`, and `similarto`. Everything else that PG puts under
`src/backend/utils/adt/` (numeric, bit, network, tsquery/tsvector, geometric,
etc.) lives in `internal/executor/expr.go` as hand-ported builtins rather
than as `utils/adt` subpackages — the numeric, bit, network, and text-search
type helpers are implemented there. When looking for a type helper, check
`expr.go` first; only datetime/similarto/array live in `utils/adt`.

### `adt/numeric` (in `expr.go`)

Numeric parsing and formatting live in `internal/executor/expr.go`. The
numeric value is represented as `mantissa * 10^-scale` where mantissa can be
an int64 (fast path, `Datum.Int`) or a `big.Int` (big-mantissa path). Key
functions:

```go
func parseNumericOrZero(s string) *big.Int
func newNumericFromFloat(f float64) Datum
func roundNumericToInt(d Datum, pos int) (int64, error)
func roundNumericToScale(d Datum, scale int16) Datum
func int64DivFastToNumeric(val int64, log10 int) Datum
func toCharNumericFormat(val Datum, fmtStr string) string
```

The `big.Int` mantissa path allocates from `mmgr.Perm()` (the permanent
context) so it survives without per-statement Reset.

### `adt/array` (`pgarray.go`)

The `array` package provides:
- `arrayElemOutputStyle` / `byteaOutputEscape` — array output formatting.
- Array element type name resolution / lookup helpers used by the catalog
  and executor.

### `adt/bit`, `adt/network`, `adt/tsquery`/`adt/tsvector`

No such `utils/adt` subpackages exist. Bit/varbit operators, inet/macaddr
parsing, and text-search functions are implemented as builtins in
`internal/executor/expr.go` (`evalPgLSNBinary`, `parseMacaddrLiteral`,
`parseInetLiteral`, etc.). Text search configs/dictionaries are resolved via
the catalog's `UserTSConfig`/`UserTSDict` registries.

## `internal/utils/activity` wait-event constants

The wait-event type and name constants are upstream-compatible so
`pg_stat_activity.wait_event_type`/`wait_event` match a real PG:

```
WaitEventTypeIO      → DataFileRead, DataFileExtend, WALWrite, WALSync, ...
WaitEventTypeLock    → relation, extend, tuple, ...
WaitEventTypeClient  → ClientRead, ClientWrite
WaitEventTypeTimeout → PostmasterMain, BgWorkerStartup, ...
WaitEventTypeActivity → BgWriterMain, CheckpointerMain, AutoVacuumMain, ...
WaitEventTypeIPC     → ...
WaitEventTypeLWLock  → ...
WaitEventTypeBufferPin → ...
```

`WaitEventStart(slot, "IO", "DataFileRead")` sets the `waitInfo` word
`(IO<<16)|DataFileRead`; `WaitEventEnd` clears it. The lock manager and the
buffer pool call these around blocking operations so `pg_stat_activity`
shows what a backend is blocked on.

## `mmgr` chunk geometry

The chunk sizes:

```
defaultChunkSize = 64 KiB   (Session/Txn/Stmt contexts)
smallChunkSize   = 4 KiB    (ExprContext)
largeChunkSize   = 256 KiB  (sort/hash-join build sides)
```

`Acquire(parent, KindExpr)` selects `smallChunkSize`; `Acquire(parent,
KindSort)`/`KindHashJoin` select `largeChunkSize`; everything else defaults
to `defaultChunkSize`.

`growChunk(n)` allocates a new chunk when the current one is full: it inserts
the new chunk AFTER the current head in the chunk list, preserving the
small-chunk tail so the bump pointer keeps pointing into the large chunk for
the sort/hash build side. `AllocAligned(n, align)` bumps to the next aligned
offset within the current chunk, growing if needed.

`Usage()` walks the chunk list summing `cap(chunk)` for allocated and
`len(chunk)` for peak. It excludes chunks returned to the pool.

## `internal/utils/misc` config parser (`parser.go`)

`ParseConfigEntries` reads a `postgresql.conf`-format file into
`[]ConfigEntry`:

```go
type ConfigEntry struct {
    Name  string
    Value string
    Line  int
    File  string
    Comment string
}
```

The parser handles:
- `#` comments (full-line and trailing).
- `key = value` assignments.
- `key = 'quoted value'` (single or double quotes, with backslash escapes).
- Multi-line values via `key = E'...'` (escape strings).
- `include` / `include_dir` / `include_if_exists` directives.
- Blank lines and trailing whitespace.

`ApplyConfigEntries` validates each entry against the registry (unknown
parameter → error; invalid value → `ValidationError`). `ApplyReloadEntries`
is the SIGHUP path: it warns (never fails) on invalid entries and skips
Postmaster/Internal context GUCs that cannot be changed at runtime.

## `datestyle` / `timestamptz_out`

`mergeDateStyle` merges a partial DateStyle spec (e.g. `SET DateStyle = 'SQL'`)
keeping the existing order component. The date styles: `ISO`, `SQL`,
`Postgres`, `German`; orders: `DMY`, `MDY`, `YMD`. The GUC value is stored
as `"ISO, DMY"` (style + order).

`timestamptz_out.go` renders `timestamptz`/`timestamp` values in PG's format
using the current DateStyle. The `TZ` field prints the timezone abbreviation
from the session's `TimeZone` GUC.