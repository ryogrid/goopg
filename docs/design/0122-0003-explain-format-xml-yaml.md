# 0122-0003 — `EXPLAIN (FORMAT XML|YAML)`

Status: accepted, landed (loop #8, 2026-07-04). Source: `.ralph/fix_plan.md`
M0122-0003 ("EXPLAIN output & pg_stat instrumentation", ~7 items — this doc
covers the FORMAT XML/YAML slice only; the item stays unchecked, see
"Cluster status" below).

## Problem

`EXPLAIN`'s FORMAT option only accepted `TEXT`/`JSON`
(`internal/parser/parser.go`'s `parseExplainOneOption`); `FORMAT XML` and
`FORMAT YAML` — both upstream-valid (`postgres/src/backend/commands/
explain_format.c`) — hit the `default:` arm and returned a `SyntaxError`.
`internal/parser/ast.go`'s `ExplainFormat` enum had no XML/YAML members at
all, so there was no representation to render even if the parser accepted
the keyword.

A background survey (this loop) also flagged per-CTE `EXPLAIN ANALYZE` stats
as missing: `Build()`'s `CTEScan`/`CTEDMLPrefix`/`MaterializedCTEScan` cases
returned their operator directly instead of through `maybeInstrument`, so
those nodes never got a `nodeStats` entry and rendered with no
`(actual time=.. rows=.. loops=..)` suffix under ANALYZE. This was a real,
independent gap — it landed in the same shared working tree as this loop's
FORMAT XML/YAML work via a concurrently-running sibling Ralph loop iteration
(both loops on the same tree, no worktree isolation; see
`[[concurrent_ralph_loops_corrupt_tree]]`) before this loop reached the
commit step. Verified non-conflicting (disjoint files/hunks from the
FORMAT XML/YAML change below) and correct (build/vet clean,
`TestExplainCTEScanAnalyzeReportsActualRows` /
`TestExplainCTEDMLPrefixAnalyzeReportsActualRows` pass) before folding into
this loop's commits alongside the FORMAT work, per the root-0026 precedent
for reconciling two loops that land in the same tree.

## Fix

**Per-CTE stats** (landed concurrently, see "Problem" above):
`internal/executor/executor.go`'s `Build()` now routes `CTEScan`,
`CTEDMLPrefix`, and `MaterializedCTEScan` through `maybeInstrument` like
every other node type. Known residual (documented in the sibling loop's own
test comment, carried into the ledger below): `CTEDMLPrefix`'s DML plans and
outer body are Built lazily inside `cteDMLPrefixOp.Open()`, after
`explainOp.Open()`'s `withInstrumentation()` scope around the top-level
`Build()` call has already closed — so nested nodes under `"CTE DML"` (e.g.
`"Insert on t"`) don't yet get their own actual-rows annotation, only the
`"CTE DML"` summary node itself does.

**FORMAT XML/YAML** (this loop's own work):
`internal/parser/ast.go`: `ExplainFormat` gains `ExplainFormatXML` /
`ExplainFormatYAML`. `internal/parser/parser.go`: `parseExplainOneOption`
accepts `xml`/`yaml` alongside `text`/`json`.

`internal/executor/operators_explain_format.go` (new file): both new formats
reuse the *same* generic `map[string]any` tree `planToJSON`/
`planToJSONWithStats` already build for FORMAT JSON — no new tree-walking
logic, just two alternate serializations of it:

- `renderExplainTree(format, root)` — single dispatch point; replaces the
  three previous `if opts.Format == parser.ExplainFormatJSON { ... }` call
  sites in `operators_explain.go`'s `explainOp.Open` (ANALYZE and
  non-ANALYZE branches) with one call each.
- `renderExplainXML` — mirrors `explain_format.c`'s `ExplainOpenGroup`/
  `ExplainProperty`/`ExplainXMLTag` family: root wrapped in
  `<explain xmlns="http://www.postgresql.org/2009/explain">`, each object
  key becomes a sanitized tag (`xmlTagName`: chars outside
  `[A-Za-z0-9-_.]` → `-`, e.g. `"Node Type"` → `Node-Type`, matching
  upstream's `ExplainXMLTag` comment about `"I/O Read Time"` →
  `"I-O-Read-Time"`), a `[]map[string]any` (currently only the `"Plans"`
  key) opens a group whose children use the singularized tag
  (`xmlSingularTag`: `"Plans"` → `"Plan"`, mirroring upstream's
  `ExplainOpenGroup("Plans", "Plans", false, es)` wrapping per-child
  `ExplainOpenGroup("Plan", ...)`), and a `[]string` (VERBOSE's `"Output"`
  column list) renders as unlabeled `<Item>` children
  (`ExplainPropertyList`'s XML branch).
- `renderExplainYAML` — upstream's `escape_yaml` literally delegates to
  `escape_json` (YAML is a JSON superset; the PG comment calls YAML's own
  quoting rules "ridiculously complicated" and opts out), so string leaves
  are rendered via `json.Marshal` and reused verbatim. The one YAML-specific
  piece of state worth preserving is `ExplainYAMLLineStarting`'s inline-vs-
  own-line rule: an **unlabeled** group (a list item introduced by `"- "`)
  puts its *first* property on the same line as the dash and every
  subsequent property on its own indented line; a **labeled** group (a
  `"key:"` line) puts *all* its properties — including the first — on
  their own indented line. `writeYAMLObjectBody`'s `firstInline` parameter
  encodes exactly this rule instead of porting upstream's `grouping_stack`
  int-list machinery.

Field order is alphabetical (`sortedKeys`, `sort.Strings`) rather than
upstream's fixed per-node-type field order — this matches the pre-existing
FORMAT JSON behavior (`json.Marshal` on a `map[string]any` already sorts
keys), so this is not a new divergence; no caller depends on key order.

## Tests

`internal/executor/explain_format_xml_yaml_test.go`: well-formedness of the
`<explain><Query><Plan>...` tree, join child-plan nesting
(`<Plans><Plan>...</Plan><Plan>...</Plan></Plans>`), VERBOSE's `<Item>` list,
ANALYZE's `Actual-Rows`/`Actual-Loops`/`Planning-Time`/`Execution-Time`
properties (XML) and their YAML/bare-numeric-token equivalents, and the
`xmlTagName` sanitization rule directly. `internal/parser/
explain_options_test.go`'s `TestParseExplainRejectsBadFormat` — which had
pinned FORMAT XML as a rejection case — is now `TestParseExplainAcceptsXMLFormat`
(pins acceptance) plus a renamed bad-format case using a truly bogus value.

## Cluster status (M0122-0003, ~7 items)

| item | status |
|---|---|
| FORMAT XML | **done, this loop** |
| FORMAT YAML | **done, this loop** |
| SETTINGS rendering | **done** (later loop, 2026-07-04) — see "SETTINGS rendering" section below |
| BUFFERS rendering | **partial** (later loop, 2026-07-04) — TEXT + JSON/XML/YAML, ANALYZE only, shared hit/read only; see "BUFFERS rendering" section below |
| `pg_stat_io` | **partial** (later loop, 2026-07-05) — row shape (79 rows, upstream valid-combination NULL pattern) + reads/read_bytes/read_time/writes/write_bytes/hits/evictions/extends/extend_bytes/extend_time instrumented for (client backend, relation, normal), fsyncs/fsync_time instrumented for (client backend, wal, normal); reuses/writebacks still open; see "pg_stat_io row shape" + "`fsyncs` / `fsync_time` counters" sections below |
| per-CTE ANALYZE stats | **done** (landed concurrently in the shared tree, folded in this loop — see "Problem"/"Fix" above), including the `CTEDMLPrefix` nested-node residual (later loop, 2026-07-06) — see "`CTEDMLPrefix` nested-node instrumentation" section below |
| `track_io_timing` runtime `SET` | **done** (later loop, 2026-07-05) — see "`track_io_timing` runtime SET" section below |
| real per-wait-event I/O timing | **partial** (later loop, 2026-07-05) — read_time only; see "real per-wait-event I/O timing" section below |

Remaining items recorded in `.ralph/deferral_ledger.md` (2026-07-04,
M0122-0003 rows) with resume points. The fix_plan checkbox stays unchecked
until the rest of the cluster lands.

## SETTINGS rendering (later loop, 2026-07-04)

`EXPLAIN (SETTINGS)` lists GUCs affecting query planning whose value
differs from their built-in default (`explain.sgml`), mirroring upstream's
`get_explain_guc_options` (`guc.c`) + `ExplainPrintSettings` (`explain.c`).

**GUC tagging.** `internal/config/guc.go` gains a `FlagExplain` bit
(mirrors `guc_tables.c`'s `GUC_EXPLAIN`). A full extraction of every
`GUC_EXPLAIN`-flagged struct in `postgres/src/backend/utils/misc/
guc_tables.c` found 62 names; `internal/config/defaults.go` tags the 45
that goopg registers at all (all 24 `enable_*` planner-method toggles —
including the goopg-only `enable_nestloop_index` for consistency — plus
`work_mem`, `random_page_cost`, `effective_cache_size`, the four per-tuple
cost GUCs, `hash_mem_multiplier`, `search_path`, `plan_cache_mode`,
`jit`/`jit_above_cost`, the two collapse-limit GUCs, `parallel_setup_cost`/
`parallel_tuple_cost`/`max_parallel_workers_per_gather`/
`min_parallel_{table,index}_scan_size`/`parallel_leader_participation`,
`debug_parallel_query`). The other 17 (`geqo*`, `temp_buffers`,
`maintenance_io_concurrency`, `constraint_exclusion`, etc.) have no goopg
registry entry at all and are simply unreachable — ledger row below.

**Boot-value comparison bug.** `Variable.BootVal` is the raw author-facing
literal (e.g. `"512MB"`); `Variable.Value`/the effective value is always
canonicalized (e.g. `"524288"`) by `NewVariable`/`Set`. A first-draft
`ExplainVariables()` compared the canonical effective value directly
against the raw `BootVal` string, which made nearly every unit-bearing
GUC (`work_mem`, `effective_cache_size`, `random_page_cost`, ...) appear
"modified" even on a freshly built registry with zero `SET` statements —
caught by `TestExplainVariablesEmptyByDefault`. Fixed by canonicalizing
`BootVal` the same way (`v.canonicalize(v.BootVal)`) before comparing.

**Wiring.** `internal/config/session.go`'s `SessionRegistry.
ExplainVariables()` returns the FlagExplain vars whose session-layered
effective value differs from the canonicalized boot value, sorted by name
(deterministic; upstream's `guc_nondef_list` order instead reflects
modification history, which this codebase doesn't track). A new
`executor.Context.ExplainSettings func() []SettingValue` field is wired in
**both** `internal/server/dispatch.go` (simple protocol) and
`dispatch_extended.go` (extended protocol) to `sess.ExplainVariables()`,
matching the existing `AllSettings`/`AllSettingsDisplay` wiring pattern.

**Rendering.** `internal/executor/operators_explain.go` adds two helpers
called from all four `explainOp.Open` branches (ANALYZE/non-ANALYZE ×
TEXT/structured):
- `appendExplainSettingsRow` — TEXT: one `Settings: k = 'v', k2 = 'v2'`
  row appended after the plan (and, under ANALYZE, before the Planning/
  Execution Time rows — mirrors `ExplainPrintSettings` running inside
  `ExplainPrintPlan`, itself called before the timing summary in
  `ExplainOnePlan`). Omitted entirely when the modified-GUC list is empty,
  matching `ExplainPrintSettings`'s TEXT-branch `if (num <= 0) return`.
- `addExplainSettingsGroup` — JSON/XML/YAML: a `"Settings"` key sibling
  to `"Plan"` at the top level, always present (as `{}` when nothing is
  modified) once SETTINGS is requested — the structured-format branch of
  `ExplainPrintSettings` has no early return, unlike TEXT. No format-
  specific code needed: `writeXMLKeyedValue`/`writeYAMLKeyedValue` already
  handle a `map[string]any` leaf generically (existing FORMAT XML/YAML
  infra from the section above).

**Tests:** `internal/executor/explain_settings_test.go` (TEXT
default-omitted, TEXT with/without modified GUCs, JSON always-present-group,
JSON with modified GUCs, ANALYZE placement before Planning Time).
`internal/config/guc_test.go`: `TestExplainVariablesEmptyByDefault`,
`TestExplainVariablesReportsModifiedPlannerGUC`.

## BUFFERS rendering (later loop, 2026-07-04)

`EXPLAIN (ANALYZE, BUFFERS)` reports, per plan node, how many shared
buffers were served from cache vs. read from disk (`show_buffer_usage` in
`explain.c`, backed by `BufferUsage`/`pgBufferUsage` in `instrument.c`).
goopg had zero buffer-hit/miss counters anywhere before this slice — the
option parsed (`opts.Buffers`) but nothing ever read it.

**Counting at the source.** `internal/storage/bufpool.go`'s `Pool` gains
two `atomic.Int64` counters, `sharedHitCount`/`sharedReadCount`, plus a
`BufferCounters() (hit, read int64)` accessor. They're incremented at the
two `Pin()`/`pinSlow()` decision points that resolve to an
already-cached slot (fast-path CAS success and the slow-path
`tryPinSlot` success), and once per `pinLoad` call (the only place that
issues a real `mgr.ReadBlock` disk read). **Scoped out, deferred:**
`PinNew` (new-block allocation — conceptually closer to PG's
`shared_blks_written`/extend accounting, not a "read") and the rare
race-recovery `tryPinSlot` calls inside `PinNew`/`pinLoad` (another
goroutine already loaded the tag while this one waited for `pinMu`) are
not counted — see ledger row.

**Per-node attribution via the existing instrumentation wrapper.**
`internal/executor/instrument.go`'s `instrumentedOp` already wraps every
node's `Open`/`Next`/`Close` for EXPLAIN ANALYZE's per-node timing/rowcount
(`nodeStats.totalNs`/`rowsOut`), using a nested-stopwatch pattern: a
parent's `Next()` call fully executes any child `Next()` calls before
returning, so timing deltas measured at the parent level are inclusive of
whatever the child subtree did. Buffers reuse the exact same pattern
instead of inventing a second mechanism (sibling-paths rule) — `nodeStats`
gains `bufHit`/`bufRead` (cumulative, inclusive-of-children, matching
upstream's own per-node semantics) and `bufBaseHit`/`bufBaseRead` (the
last-seen `Pool.BufferCounters()` snapshot). `instrumentedOp.Open` captures
`ctx.Pool` and seeds the baseline once; `accountBuffers()` (called from
`Next` and `Close`) diffs the pool's current counters against the baseline
and rolls the delta into `bufHit`/`bufRead`. Because the pool counters are
process-global, not per-node, this diffing-since-last-checkpoint approach
is what makes per-node attribution correct at all — see the doc comment on
`accountBuffers` for the full argument.

**Rendering.** `internal/executor/operators_explain.go`'s
`walkPlanAnalyzeFiltered` (the TEXT + ANALYZE path only) appends a
`Buffers: shared hit=N read=N` detail line per node via
`formatBuffersLine`, mirroring `show_buffer_usage`'s per-term omission
rule: the whole line is omitted when both counters are zero, and each of
`hit=`/`read=` is omitted individually when that counter is zero.

**Deferred (ledger row, same date):**
- `EXPLAIN (BUFFERS)` without `ANALYZE` (PG 17+ shows *planning-time*
  buffer usage in this case) — goopg has no separate planning-phase buffer
  counters; `walkPlan`/`walkPlanFiltered` (the non-ANALYZE path) never
  calls into the buffer accounting at all.
- `dirtied=`/`written=`/local- and temp-buffer terms, and `I/O Timings`
  (gated on `track_io_timing`, itself only read at process boot).
- The two narrow `Pin()` call sites scoped out above (new-block allocation,
  race-recovery re-pin).

**Tests:** `internal/executor/explain_buffers_test.go` —
`TestExplainBuffersAnalyzeTextLine` (line present, at least one of
hit=/read= populated), `TestExplainBuffersOffByDefault` (no BUFFERS ⇒ no
line, even under ANALYZE), `TestExplainBuffersRepeatScanAccumulatesHits`
(a second, warm-cache pass reports hit-only, no read=).

### FORMAT JSON/XML/YAML (later loop, 2026-07-04)

Upstream's `show_buffer_usage` non-TEXT branch prints `"Shared Hit
Blocks"`/`"Shared Read Blocks"`/... as flat sibling properties on the plan
node object — **not** nested under a `"Buffers"` key as an earlier ledger
note had guessed — and, per `peek_buffer_usage`'s comment ("when format is
anything other than text, we print even if the counters are all
zeroes"), unconditionally once BUFFERS is requested, unlike TEXT's
positive-only gating. `planToJSONWithStats` (`internal/executor/
operators_explain.go`) now sets `obj["Shared Hit Blocks"]`/`obj["Shared
Read Blocks"]` from the same per-node `nodeStats.bufHit`/`bufRead` the
TEXT line already reads, whenever `opts.Buffers` is set — scoped to the
two counters goopg actually tracks; the other 8 upstream properties
(`Shared Dirtied/Written Blocks`, all four `Local *`, both `Temp *`) are
not emitted at all (an always-zero stub would misrepresent untracked
dirty/write activity as "confirmed zero"). No XML/YAML-specific code
needed — both formats already render an arbitrary `map[string]any` leaf
generically (`xmlTagName` sanitizes `"Shared Hit Blocks"` to
`Shared-Hit-Blocks`; the YAML renderer keys off the same map).

**Tests:** `TestExplainBuffersJSONAlwaysIncludesSharedBlocks` (present
even when a counter is zero), `TestExplainBuffersJSONOmittedWithoutBuffersOption`
(gated on `opts.Buffers`), `TestExplainBuffersXMLTagSanitized` (tag-name
sanitization).

### `dirtied=`/`written=` counters (later loop, 2026-07-04)

Closes the "Deferred" bullet from the BUFFERS-rendering section above for the
two shared-buffer terms it named: `EXPLAIN (ANALYZE, BUFFERS)` now also
reports `dirtied=`/`written=`, matching `show_buffer_usage`'s exact
`shared_blks_dirtied`/`shared_blks_written` semantics
(`postgres/src/backend/commands/explain.c:4122-4127`, confirmed by direct
source read — the four shared terms share one `has_shared` gate and one
term ordering: `hit= read= dirtied= written=`).

**Counting at the source.** `internal/storage/bufpool.go`'s `Pool` gains
`sharedDirtiedCount`/`sharedWrittenCount` (`atomic.Int64`, siblings of the
existing `sharedHitCount`/`sharedReadCount`); `BufferCounters()` now returns
all four. `sharedDirtiedCount` increments exactly once per clean→dirty
transition — at every one of the 8 CAS-success sites across `MarkDirty`,
`MarkDirtyHintBit`, `markDirtyWithLSNCommon` (shared by `MarkDirtyWithLSN`/
`MarkDirtyWithLSNLocked`), the 3 early-return branches inside
`MarkDirtyForceFPI`, `MarkDirtyChangeRecord`, and `MarkDirtyLogicalChange` —
mirroring `bufmgr.c`'s "if the buffer was not dirty already, do vacuum
accounting" comment at the `MarkBufferDirty`/`MarkBufferDirtyHint` call
sites. `sharedWrittenCount` increments at exactly one site: `evictVictim`'s
post-flush point, when a backend evicts a dirty victim slot to make room for
its own `Pin`/`PinNew`. Deliberately **not** counted: `WriteDirtyPages`
(bgwriter) and `FlushAll`/`FlushAllPaced`/`flushBatch` (checkpointer) — in
upstream, `pgBufferUsage` is a per-backend global, so a checkpointer or
bgwriter process flushing a page increments *its own* counter, never the
querying backend's; since goopg's pool counters are process/pool-global
(the same architectural approximation `sharedHitCount`/`sharedReadCount`
already made), counting bgwriter/checkpointer writes here would misattribute
background IO to whatever query happens to be running concurrently.

**Rendering.** `formatBuffersLine` (TEXT) and `planToJSONWithStats`
(JSON/XML/YAML) both extended with the same per-term positive-only /
unconditional-once-requested rules the hit/read pair already followed;
`nodeStats` gains `bufDirtied`/`bufWritten` + `bufBaseDirtied`/
`bufBaseWritten`, rolled forward by `accountBuffers` identically to
`bufHit`/`bufRead`.

**Verified against a real running server**, not just the Go test suite: a
fresh table's first `UPDATE` shows `dirtied=` withheld once the page is
already dirty from the preceding `INSERT` in the same buffer (no flush in
between — matches upstream: `shared_blks_dirtied` only counts *new*
clean→dirty transitions during the current command); after `CHECKPOINT`
clears the dirty bit, an immediate follow-up `UPDATE` on the same table
correctly reports `Buffers: shared hit=12 read=1 dirtied=1`.

**Tests:** `internal/storage/bufpool_counters_test.go`'s
`TestBufferCountersDirtiedAndWritten` (small pool, forced dirty + forced
eviction, double-`MarkDirty` non-double-count assertion);
`internal/executor/explain_buffers_test.go`'s
`TestFormatBuffersLineDirtiedWritten` (table-driven, all 4 zero/gating
combinations) and `TestExplainBuffersJSONAlwaysIncludesDirtiedWrittenBlocks`.

**Still deferred** (ledger row, same date): `EXPLAIN (BUFFERS)` without
`ANALYZE` (planning-time buffers), local/temp-buffer terms, `I/O Timings`,
and the two narrow `Pin()` call sites (`PinNew`, race-recovery re-pin)
scoped out of the hit/read pair above — none of those needed touching for
this slice.

## pg_stat_io row shape (later loop, 2026-07-04)

`pg_stat_io` (PG 16+) had a table registered (`internal/catalog/catalog.go`,
OID 8061) but a `VirtualRows` stub returning `nil` unconditionally — zero
rows for every query, regardless of `backend_type`/`object`/`context` filter.

**Ground truth over static analysis.** Upstream's row shape is generated by
`pg_stat_get_io` → `pg_stat_io_build_tuples` (`postgres/src/backend/utils/
adt/pgstatfuncs.c`), gated by three boolean predicates in
`postgres/src/backend/utils/activity/pgstat_io.c`:
`pgstat_tracks_io_bktype` (which `BackendType`s participate at all — 14 of
the 18 enum values), `pgstat_tracks_io_object` (which `(IOObject, IOContext)`
combinations a tracked type can emit a row for — a row is omitted entirely
when false), and `pgstat_tracks_io_op` (which of the 8 `IOOp`s get a real
count vs. a NULL cell within an emitted row). Rather than trust a pure
reading of that C logic, this loop stood up a throwaway real PostgreSQL 18.3
cluster (`postgres/local_install/bin/{initdb,pg_ctl,psql}`, a fresh unix-socket
data dir under `/tmp`) and ran `SELECT * FROM pg_stat_io` directly — 79 rows,
confirming the row/NULL shape (and, incidentally, that `pgstat_tracks_io_bktype`
gating is unconditional on whether that process type ever actually ran: a
freshly initdb'd cluster with `summarize_wal` at its default `off` still
reports 2 all-zero `walsummarizer` rows). `internal/testport/
client_tools_port_test.go`'s `TestPort_PgWalsummary002Blocks` had asserted
the opposite (0 walsummarizer rows) before this loop — a plausible-looking
but factually wrong assumption, corrected alongside this change.

**Implementation.** `internal/executor/pgstat_io.go` is a direct Go port of
the three predicate functions (`ioTracksObject`/`ioTracksOp`, `ioTracksBktype`
folded into the `ioBackendType` enum only listing the 14 tracked types) plus
a column-index table (`ioOpColumns`) mirroring `pgstat_get_io_op_index`/
`_byte_index`/`_time_index`. `fetchIOStatRows(ctx *Context) [][]string`
walks backend type × object × context in upstream's own emission order,
skips combinations `ioTracksObject` rejects, and fills every column with
`catalog.VirtualNull` (SQL NULL) except columns `ioTracksOp` says are
tracked, which get `"0"` — a real, honest zero (goopg has performed none of
that IO), not a fabricated stand-in for an untracked cell. The single cell
goopg actually instruments — backend_type='client backend', object=
'relation', context='normal', columns reads/read_bytes/hits — is overridden
from `ctx.Pool.BufferCounters()` (the same pool-wide shared-buffer counters
BUFFERS rendering above uses), converting to bytes via `storage.BlockSize`.
Wired into the SELECT path via `valuesOp.Open` (`internal/executor/
operators.go`) with a `tbl.Name == "pg_stat_io"` case, following the
established `pg_stat_slru`/`pg_prepared_statements` per-connection-live-data
pattern (see [[per_connection_virtual_catalog_scoping]] memory /
`fetchSLRURows`) rather than a static `VirtualRows` closure, since the row
set depends on live pool state.

**Deferred (ledger rows, unchanged from the prior entry):** the other seven
upstream I/O counters (`writes`/`extends`/`evictions`/`reuses`/`writebacks`/
`fsyncs` and all `*_time`/`*_bytes` columns beyond reads/read_bytes) stay a
real `0` rather than tracking actual activity — goopg has no write/extend/
evict/reuse/fsync counters anywhere yet, and `track_io_timing` remains
unwired (open, separate ledger row). Populating those requires the same
storage-layer instrumentation work the BUFFERS `dirtied=`/`written=` gap
above is blocked on; this loop only extended the *existing* single counter
pair to `pg_stat_io`'s exact row shape rather than inventing new collection.

**Tests:** `internal/executor/pgstat_io_test.go` — `TestPgStatIORowCount`
(79 rows), `TestPgStatIOExcludesInvalidCombination` (wal/vacuum never
appears), `TestPgStatIOClientBackendRelationNormalShape` (reads/read_bytes/
hits populated, reuses NULL), `TestPgStatIOWalSummarizerRows` (2 rows,
matching the real-PG finding above), `TestPgStatIOLiveCounters` (end-to-end
through a live query context). `internal/testport/client_tools_port_test.go`'s
`TestPort_PgWalsummary002Blocks` updated to expect 2 walsummarizer rows.

## `track_io_timing` runtime SET (later loop, 2026-07-05)

**Problem.** `track_io_timing` is registered `ContextUserset` (session
`SET`-able), but the value gating the per-I/O wait-event hooks (`Pool.
OnPinWait`/`OnPinDone`, and the AIO/data-file `OnReadWait`/`OnWriteWait`/
`OnExtendWait`/`OnSyncWait` pairs, all in `internal/initdb/open.go`) was
read exactly once at process boot (`cmd/goopg/main.go`'s `boolGUC` →
`initdb.OpenOptions.TrackIOTiming`) and baked into whether those hooks were
installed *at all*. A live `SET track_io_timing = on` updated the GUC
registry but nothing re-read it — the hooks, if never installed at boot,
stayed permanently absent for the life of the process.

**Fix.** Two changes make the setting live per session without
reinstalling hooks or restarting:

1. `internal/activity/registry.go`: `coldActivity` (the per-backend
   mutable-field block) gains `TrackIOTimingOn atomic.Bool`, and
   `ActivityRegistry` gains a process-wide `trackIOTimingFastPath
   atomic.Bool` that latches `true` the first time *any* backend enables
   the setting (via `UpdateTrackIOTiming(procNum, true)` or
   `EnableTrackIOTimingFastPath()`) and is never reset — reverting it
   would require tracking whether every backend has since turned it back
   off, not worth the complexity for a rarely-toggled debug GUC. New
   `LookupTrackedGoroutine()` is a drop-in replacement for
   `LookupCurrentGoroutine()` that additionally requires the calling
   backend's own flag to be on; it short-circuits on the fast-path flag
   first, so the default-off case costs one atomic load, not the
   goroutine-map lookup + mutex — preserving M0092-0005's original
   rationale for gating these hooks in the first place, now enforced
   per-call instead of per-process-boot.
2. `internal/initdb/open.go`: the hooks above are now wired
   **unconditionally** (the `if opts.TrackIOTiming { ... }` wrapper is
   gone) and each closure calls `act.LookupTrackedGoroutine()` instead of
   `activity.LookupCurrentGoroutine()`. `opts.TrackIOTiming` now only
   primes the fast-path flag at boot (`act.EnableTrackIOTimingFastPath()`)
   so a postgresql.conf-configured `on` is live from the very first
   connection.
3. `internal/server/server.go`'s `New()` registers a
   `cfg.Registry.OnChange("track_io_timing", ...)` callback — the same
   established pattern as the pre-existing `application_name` propagation
   hook — that calls `UpdateTrackIOTiming` on the SET-ing backend's own
   procNum (resolved via `activity.LookupCurrentGoroutine()`, since the
   callback runs synchronously on that backend's own goroutine). The
   per-connection setup seeds each new backend's flag immediately after
   `config.NewSessionRegistry` from `sess.Get("track_io_timing")`, so a
   session inherits the correct boot-configured default even before its
   first `SET`.

Background workers (checkpointer/autovacuum) were already unaffected by
these hooks either way — they never call `activity.SetCurrentGoroutine`
for their own goroutine (only real `client_backend` connections do, in
`server.go`), so `LookupCurrentGoroutine`/`LookupTrackedGoroutine` already
returns `ok=false` for them regardless of `track_io_timing`; no behavior
change there.

**Deferred (unaffected by this change, tracked in the rows above):** this
closes only the "not re-checked per session/query" gap. It does not add
any new timing *collection* — there is still no wall-clock instrumentation
at the wait-event sites, so `EXPLAIN`'s `I/O Timings` and `pg_stat_io`'s
six `*_time` columns remain unmeasured even with `track_io_timing=on`.
That is the same storage-instrumentation-layer gap the BUFFERS/`pg_stat_io`
rows above are blocked on; once real per-wait-event timing is added, it
should gate itself on `act.LookupTrackedGoroutine()`/
`ActivityRegistry.TrackIOTiming(procNum)` (now available) rather than
re-deriving a boot-time bool the way the old code did.

**Tests:** `internal/activity/registry_test.go` —
`TestActivityRegistryTrackIOTimingFastPath` (per-backend flag independent
of the latched process-wide fast path),
`TestActivityRegistryTrackIOTimingFastPathBoot` (priming the fast path
alone is not sufficient — a given backend still needs its own flag on).
`internal/server/server_test.go` — `TestTrackIOTimingOnChangePropagatesToActivityRegistry`
(exercises `New()`'s actual `OnChange` wiring end-to-end with a real
`SessionRegistry.Set("track_io_timing", "on", false)`).

## Real per-wait-event I/O timing (later loop, 2026-07-05)

**Problem.** The section above wired `track_io_timing` so a live `SET`
reaches the existing wait-event hooks, but those hooks (`Pool.OnPinWait`/
`OnPinDone`) only ever recorded *that* a wait happened
(`ActivityRegistry.WaitEventStart`/`WaitEventEnd`, which stamp
`pg_stat_activity.wait_event`) — nothing measured *how long* it took.
`pg_stat_io.read_time` and EXPLAIN's `I/O Timings` line therefore had no
real signal to render even with `track_io_timing=on`.

**Fix.** Three small, additive changes turn the existing hook pair into a
real timer, reusing the mono-clock timestamp `WaitEventStart` already
stores rather than adding a second clock read:

1. `internal/activity/registry.go`'s `WaitEventEnd(procNum) time.Duration`
   now returns the elapsed wall-clock time since the matching
   `WaitEventStart` call, computed by reading the per-slot `stateChange`
   mono-clock stamp *before* overwriting it with the current time. All
   pre-existing callers that ignore the return value (most of them, across
   `initdb`/`server`/`executor`) are unaffected — Go permits discarding a
   return value at a statement-level call.
2. `internal/storage/bufpool.go`'s `Pool` gains a `sharedReadTimeNanos`
   atomic accumulator plus `AddReadTimeNanos(n int64)` / `ReadTimeNanos()
   int64`, siblings of the pre-existing `sharedHitCount`/`sharedReadCount`/
   `sharedDirtiedCount`/`sharedWrittenCount` counters that already back
   EXPLAIN (BUFFERS) and `pg_stat_io`.
3. `internal/initdb/open.go`'s pre-existing `pool.OnPinDone` closure — the
   *only* place that currently calls `WaitEventEnd` after a real disk read
   (`pinLoad`'s `mgr.ReadBlock` call, bracketed by `OnPinWait`/`OnPinDone`)
   — now captures the returned duration and calls
   `pool.AddReadTimeNanos(int64(d))`. No new gate was needed: the closure
   body only executes when `act.LookupTrackedGoroutine()` succeeds, which
   already requires the pinning backend's `track_io_timing` flag to be on
   (see the section above) — so real time only ever accumulates exactly
   when upstream's own "these will be zero if track_io_timing is not
   enabled" rule says it should.

`internal/executor/pgstat_io.go`'s `fetchIOStatRows` renders
`ReadTimeNanos()` as the `read_time` column (milliseconds, via the
existing `operators_explain.go` `nsToMs` helper) for the one row goopg
instruments (client backend/relation/normal). While touching this
function, an unrelated pre-existing bug was also fixed: `BufferCounters()`'s
4th return value (`written`, collected since the 2026-07-04 dirtied/written
loop) was being discarded (`_`) instead of feeding the `writes`/
`write_bytes` columns, which had rendered a hardcoded `0` despite a real
counter already existing.

**Deferred (tracked in `.ralph/deferral_ledger.md`, 2026-07-05):**
`write_time` is not measured — `evictVictim`'s dirty-victim flush (the
call site that increments `sharedWrittenCount`) has no `OnWait`/`OnDone`
hook pair to time at all, unlike the read side's pre-existing
`OnPinWait`/`OnPinDone`; adding one is a new hook, not a wiring gap. The
other five `pg_stat_io` op counters (extends/evictions/reuses/writebacks/
fsyncs, plus every one of their own `_bytes`/`_time` columns) still render
upstream's real `0`/NULL shape, since goopg has no instrumentation for
those operations at all. EXPLAIN's `I/O Timings` line itself is not
rendered in any format yet — this loop only wired the underlying counter,
not its EXPLAIN presentation (a separate, BUFFERS-rendering-style
follow-up).

**Tests:** `internal/activity/registry_test.go` —
`TestWaitEventEndReturnsElapsedDuration` (a real `time.Sleep`-backed
duration, not a stub), `TestWaitEventEndOutOfRangeProcNumReturnsZero`.
`internal/storage/bufpool_counters_test.go` —
`TestPoolReadTimeNanosAccumulates` (accumulation + non-positive-duration
guard). `internal/executor/pgstat_io_test.go` —
`TestPgStatIOReadTimeAndWritesRendered` (end-to-end: a real `storage.Pool`
with a forced backend-driven eviction and injected read time, asserting
both the `read_time` and `writes` columns render real, non-placeholder
values).

## `evictions` / `extends` counters (later loop, 2026-07-05)

Two of the five still-`0` `pg_stat_io` op counters named in the section
above now render real values, closing part of that gap (`write_time` and
the remaining three op counters — reuses/writebacks/fsyncs — are still
open, see the updated ledger row).

`storage.Pool` gains `sharedEvictionCount`/`sharedExtendCount`
(`internal/storage/bufpool.go`), following the exact pattern of the
pre-existing `sharedDirtiedCount`/`sharedWrittenCount` pair (own atomic
counters, own `EvictionCount()`/`ExtendCount()` accessors — a new method
pair rather than widening `BufferCounters()`'s 4-value return, to avoid
touching its four existing call sites): `sharedEvictionCount` increments
once in `evictVictim`, immediately after the "slot was free" early return
(i.e. only when a *valid* tag is actually displaced — mirrors
`bufmgr.c`'s `shared_blks_evicted` accounting), regardless of whether the
victim was dirty (the dirty-only `sharedWrittenCount` increments
separately, further down the same function, only on a successful flush).
`sharedExtendCount` increments once in `PinNew`, right after its sole
`p.mgr.Extend` call succeeds — this is the pool's only relation-extension
call site (verified: no other `Extend(` caller exists in the package),
so no sibling site needed updating.

`internal/executor/pgstat_io.go`'s `fetchIOStatRows` wires both into the
`ioOpEvict`/`ioOpExtend` cases of the existing per-op `switch` (same
"client backend/relation/normal" cell the other real counters already
populate) — `extends` also gets `extend_bytes` (`count * 8192`, matching
the `reads`/`writes` byte-column convention); `extend_time` is left at the
existing default `"0"` (no per-extend timing hook exists yet, same
"count wired, time not yet" partial state `read_time` was in before its
own follow-up).

**Tests:** `internal/storage/bufpool_counters_test.go` —
`TestBufferCountersEvictionAndExtend` (a 2-slot pool: fills exactly to
capacity via `PinNew` with zero evictions, then forces N further evictions
via N more `PinNew` calls, asserting both counters independently).
`internal/executor/pgstat_io_test.go` —
`TestPgStatIOEvictionsAndExtendsRendered` (end-to-end: asserts the
rendered `evictions`/`extends`/`extend_bytes` cells match the underlying
counters after a controlled fill-then-evict sequence).

## `write_time` counter (later loop, 2026-07-05)

`write_time` — the last resume point the `evictions`/`extends` section
above left open — now renders a real value, backed by a genuinely new
timing hook (unlike `evictions`/`extends`, which reused the pre-existing
counter-pattern with no new hook needed).

`storage.Pool` gains `sharedWriteTimeNanos` (`internal/storage/bufpool.go`),
`read_time`'s (`sharedReadTimeNanos`) write-side sibling, plus a matching
`OnFlushWait`/`OnFlushDone` hook pair — the write-side analogue of the
pre-existing `OnPinWait`/`OnPinDone` bracket around `pinLoad`'s disk read.
The new pair brackets `evictVictim`'s dirty-victim `flushSlot` call exactly
(same `contentMu`-held span `OnPinWait`/`OnPinDone` brackets on the read
side), so accumulated time reflects only the same foreground,
backend-driven flushes `sharedWrittenCount` already counts — not
bgwriter/checkpointer background flushes (consistent with that counter's
own documented per-backend-attribution rationale). New
`AddWriteTimeNanos`/`WriteTimeNanos` accessor pair mirrors
`AddReadTimeNanos`/`ReadTimeNanos` exactly, including the non-positive-
duration guard.

`internal/initdb/open.go` wires `pool.OnFlushWait`/`pool.OnFlushDone`
immediately after the pre-existing `pool.OnPinWait`/`pool.OnPinDone` block,
using the identical `act.LookupTrackedGoroutine()` → `WaitEventStart(...,
WaitDataFileWrite)` / `WaitEventEnd()` → `AddWriteTimeNanos` pattern (same
`WaitDataFileWrite` wait event `mgr.OnWriteWait`/`OnWriteDone` already use
at the lower `storage.Manager.WriteBlock` layer for `pg_stat_activity`
purposes — this new pair is a separate, `Pool`-level bracket scoped to
exactly the foreground-eviction span, deliberately not reusing `mgr`'s
existing hooks since those fire for every `WriteBlock` call including
background flushes this counter must exclude).

`internal/executor/pgstat_io.go`'s `fetchIOStatRows` reads
`Pool.WriteTimeNanos()` and renders it (via the existing `nsToMs` helper)
as the `write_time` column (col 8) alongside the pre-existing
`writes`/`write_bytes` cells, for the same "client backend/relation/normal"
row.

**Tests:** `internal/storage/bufpool_counters_test.go` —
`TestPoolWriteTimeNanosAccumulates` (accumulation + non-positive-duration
guard, mirrors `TestPoolReadTimeNanosAccumulates`),
`TestPoolOnFlushHooksFireOnDirtyVictimEviction` (installs counting
`OnFlushWait`/`OnFlushDone` closures directly on a real `Pool`, confirms
they fire exactly once per forced dirty-victim eviction and not at all
during a clean fill — mirrors `bm_io_in_progress_test.go`'s `OnPinWait`
hook-invocation pattern). `internal/executor/pgstat_io_test.go` —
`TestPgStatIOWriteTimeRendered` (end-to-end rendered-cell assertion,
mirrors `TestPgStatIOReadTimeAndWritesRendered`).

## `extend_time` counter (later loop, 2026-07-05)

`extend_time` — the resume point the `write_time` section above left open
— now renders a real value, via the same new-hook-pair pattern
`write_time` used (not a reuse of an existing counter).

`storage.Pool` gains `sharedExtendTimeNanos` (`internal/storage/bufpool.go`),
`write_time`'s (`sharedWriteTimeNanos`) relation-extension sibling, plus a
matching `OnExtendWait`/`OnExtendDone` hook pair — the extend-side analogue
of `OnFlushWait`/`OnFlushDone`. The new pair brackets `PinNew`'s
`p.mgr.Extend(rel, s.page)` call exactly — the pool's sole smgr `Extend`
call site, the same span the pre-existing `sharedExtendCount` counter
already attributes to (`extends`/`extend_bytes`). New
`AddExtendTimeNanos`/`ExtendTimeNanos` accessor pair mirrors
`AddWriteTimeNanos`/`WriteTimeNanos` exactly, including the non-positive-
duration guard.

`internal/initdb/open.go` wires `pool.OnExtendWait`/`pool.OnExtendDone`
immediately after the pre-existing `pool.OnFlushWait`/`pool.OnFlushDone`
block, using the identical `act.LookupTrackedGoroutine()` →
`WaitEventStart(..., WaitDataFileExtend)` / `WaitEventEnd()` →
`AddExtendTimeNanos` pattern — deliberately a new `Pool`-level pair, not a
reuse of `storage.Manager`'s existing `mgr.OnExtendWait`/`mgr.OnExtendDone`
(`internal/storage/smgr.go`), for the same per-backend-attribution reason
`write_time` documented for `mgr.OnWriteWait`/`OnWriteDone`: the
`Manager`-level hooks fire for every `Extend`/`ExtendBatch` call regardless
of caller, while this `Pool`-level pair is scoped to exactly the `PinNew`
foreground-extension span pg_stat_io's per-backend-type `extend_time`
column needs.

`internal/executor/pgstat_io.go`'s `fetchIOStatRows` reads
`Pool.ExtendTimeNanos()` and renders it (via the existing `nsToMs` helper)
as the `extend_time` column (col 13) alongside the pre-existing
`extends`/`extend_bytes` cells, for the same "client backend/relation/normal"
row.

**Tests:** `internal/storage/bufpool_counters_test.go` —
`TestPoolExtendTimeNanosAccumulates` (accumulation + non-positive-duration
guard, mirrors `TestPoolWriteTimeNanosAccumulates`),
`TestPoolOnExtendHooksFireOnPinNewExtend` (installs counting
`OnExtendWait`/`OnExtendDone` closures directly on a real `Pool`, confirms
they fire exactly once per `PinNew` call across several calls — mirrors
`TestPoolOnFlushHooksFireOnDirtyVictimEviction`'s hook-invocation pattern).
`internal/executor/pgstat_io_test.go` — `TestPgStatIOExtendTimeRendered`
(end-to-end rendered-cell assertion, mirrors `TestPgStatIOWriteTimeRendered`).

Remaining M0122-0003 sub-items after this loop: `EXPLAIN (BUFFERS)` without
ANALYZE (planning-time buffers), local/temp-buffer terms, the 3 remaining
`pg_stat_io` op counters (reuses/writebacks/fsyncs — each needs a genuinely
new counting mechanism: strategy-ring reuse, bgwriter/checkpointer-scoped
writeback attribution, fsync call-site instrumentation respectively),
EXPLAIN's `I/O Timings` line (now renderable since both `write_time` and
`extend_time` exist), and the `CTEDMLPrefix` nested-node instrumentation
residual.

## Follow-up: EXPLAIN `I/O Timings` line

Closes the "next presentation-layer gap" this doc's `extend_time` section
predicted. `nodeStats` (`internal/executor/instrument.go`) gains
`bufReadTimeNs`/`bufWriteTimeNs` (plus `bufBase*` snapshot pairs), diffed the
same nested-stopwatch way `bufHit`/`bufRead`/etc. already are;
`bufWriteTimeNs` folds `Pool.ExtendTimeNanos()` in alongside
`Pool.WriteTimeNanos()`, mirroring upstream's `pgstat_count_io_op_time`
(`postgres/src/backend/utils/activity/pgstat_io.c`), which buckets
`IOOP_EXTEND` under the same shared-buffer write-time counter — real PG's
"I/O Timings:" line has no separate `extend=` term either.

`formatIOTimingsLine` (`internal/executor/operators_explain.go`) renders
upstream's `show_buffer_usage` `has_shared_timing` branch
(`postgres/src/backend/commands/explain.c`): `"I/O Timings: shared
read=X.XXX write=Y.YYY"`, omitting the whole line when both counters are
zero and each individual `read=`/`write=` term when that counter alone is
zero. Wired into the TEXT walker (`walkPlanAnalyzeFiltered`, right after the
existing `Buffers:` line) and the JSON renderer (`planToJSONWithStats`,
`"Shared I/O Read/Write Time"` keys, right after the existing
`"Shared ... Blocks"` keys).

**Accepted simplification (fixed for non-TEXT, 2026-07-05, this loop):** real
PostgreSQL's non-TEXT (JSON/XML/YAML) branch gates the `ExplainPropertyFloat`
I/O-timing calls on the live `track_io_timing` GUC, not on whether the
accumulated values are nonzero — `planToJSONWithStats` now takes a
`trackIOTiming bool` parameter (the caller's `ctx.Activity.TrackIOTiming(ctx.
ProcNum)` snapshot, threaded unchanged through the recursive `Plans`
re-render from `explainOp.Open`) and emits `"Shared I/O Read Time"`/`"Shared
I/O Write Time"` whenever it's true, even when both accumulators are exactly
zero — matching `explain.c`'s `peek_buffer_usage` comment ("when format is
anything other than text, we print even if the counters are all zeroes")
exactly. The TEXT path (`formatIOTimingsLine`) deliberately keeps its
original nonzero gate: there is no upstream precedent for an explicit
`I/O Timings: shared read=0.000 write=0.000` TEXT line at all, so matching
non-TEXT's unconditional-emit behavior there would invent new TEXT output
upstream never produces.

**Tests:** `internal/executor/explain_buffers_test.go` —
`TestExplainIOTimingsOffByDefault`,
`TestExplainIOTimingsJSONOmittedWithoutAccumulatedTime`,
`TestPlanToJSONWithStatsRendersIOTimingsWhenTrackIOTimingOnEvenAtZero`,
`TestPlanToJSONWithStatsOmitsIOTimingsWhenTrackIOTimingOff`.

**Verification:** `go build ./...` clean; `go test ./internal/executor/...`
PASS (includes the four cases above plus the full pre-existing EXPLAIN
suite); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).

## `fsyncs` / `fsync_time` counters (later loop, 2026-07-05)

Of the three `pg_stat_io` op counters this doc's `extends`/`evictions`
section left open (reuses/writebacks/fsyncs), `fsyncs` is the tractable one:
unlike `reuses` (needs a `BufferAccessStrategy`-style ring buffer goopg does
not implement) or `writebacks` (needs bgwriter/checkpointer async-writeback
issuance goopg does not implement), goopg's `wal.Writer` already performs a
real `fdatasync(2)` per dirty WAL segment in `state.flushUpTo`
(`internal/wal/writer.go`) — an existing, genuinely-real signal that was
simply never counted.

`walBufferCounters` (shared between `Writer` and `state`, the same struct
backing `wal_buffers_*`) gains a `fsyncCount stats.Counter`, incremented
once per segment actually `dataSync`'d inside `flushUpTo`'s dirty-segment
loop — unconditional, matching upstream's "count columns are never gated on
`track_io_timing`" semantics (only the `_time` sibling is). `Writer` gains a
plain `sharedFsyncTimeNanos atomic.Int64` (not sharded — WAL fsyncs are
already serialised through group commit, unlike per-buffer pool ops) plus
`AddFsyncTimeNanos`/`FsyncTimeNanos` accessors mirroring `storage.Pool`'s
`AddExtendTimeNanos`/`ExtendTimeNanos` exactly, and `FsyncCount()`.

Timing gate: the pre-existing `OnWALSync`/`OnWALSyncDone` hook pair
(`internal/initdb/open.go`) already brackets `Writer.FlushUpTo` using a
*fixed* `walProcNum` background slot (for `pg_stat_activity`'s `wait_event`
display, shared by every committing backend) — that slot's own
`track_io_timing` flag is never set by any session, so gating
`AddFsyncTimeNanos` on it would leave `fsync_time` permanently zero.
Instead, `OnWALSyncDone` now separately calls
`act.LookupTrackedGoroutine()`: because `FlushUpTo` runs synchronously on
the *calling backend's own goroutine* (that goroutine's `(registry,
procNum)` was set once at connection setup — `server.go`'s
`activity.SetCurrentGoroutine`), this correctly resolves to the committing
backend's own `track_io_timing` setting — the same gating mechanism
`storage.Pool`'s `OnPinDone`/`OnFlushDone`/`OnExtendDone` already use, just
applied via a second, independent registry lookup rather than reusing
`walProcNum`.

`internal/executor/pgstat_io.go`'s `fetchIOStatRows` gains a second
instrumented cell — `(client backend, wal, normal)` — alongside the
pre-existing `(client backend, relation, normal)` one; only `ioOpFsync` is
wired (`Writer.FsyncCount()`/`FsyncTimeNanos()`, rendered via the existing
`nsToMs` helper), since goopg tracks no other real WAL read/write counters
yet.

**Tests:** `internal/wal/wal_test.go` — `TestWriterFsyncCountRealSignal`
(count increments once per real flush, not per no-op re-flush),
`TestWriterFsyncTimeNanosAccumulates` (accumulator + non-positive-duration
guard, mirrors `TestPoolWriteTimeNanosAccumulates`).
`internal/executor/pgstat_io_test.go` — `TestPgStatIOWalFsyncsRendered`
(end-to-end rendered-cell assertion, mirrors `TestPgStatIOExtendTimeRendered`).

Remaining M0122-0003 sub-items after this loop: `EXPLAIN (BUFFERS)` without
ANALYZE (planning-time buffers), local/temp-buffer terms, the 2 remaining
`pg_stat_io` op counters (reuses/writebacks — each needs a genuinely new
buffering mechanism goopg does not have, see above), and the `CTEDMLPrefix`
nested-node instrumentation residual.

**Verification:** `go build ./...` clean; `go test ./internal/wal/...
./internal/executor/... ./internal/initdb/... ./internal/server/...` PASS.

## `writeback` / `writeback_time` counters (later loop, 2026-07-05)

Of the two `pg_stat_io` op counters the `fsyncs` section above left open
(reuses/writebacks), `writebacks` is now real too. Unlike `reuses` (needs a
`BufferAccessStrategy`-style ring buffer goopg does not implement anywhere
— sequential-scan/VACUUM/COPY buffer reuse is architecturally absent), a
writeback hint only needs (a) something that writes dirty pages — goopg
already has three such call sites (`evictVictim`'s dirty-victim flush,
`WriteDirtyPages` (bgwriter), `flushBatch`/`FlushAllPaced` (checkpointer))
— and (b) a kernel write-behind hint to issue once enough pages accumulate,
which Linux's `sync_file_range(2)` provides directly.

**Design.** New `internal/storage/writeback.go`: `Pool.accountBackendWrite`
/ `accountBgwriterWrite` / `accountCheckpointerWrite`, one call added right
after each of the three write call sites above. Each maintains a running
pending-page counter (`pendingBackendFlushBlocks` etc., `atomic.Int64`)
against a configurable threshold (`backendFlushAfterBlocks` etc.,
`atomic.Int32`, set via `SetBackendFlushAfter`/`SetBgwriterFlushAfter`/
`SetCheckpointFlushAfter`); crossing the threshold resets the pending
counter to 0 and calls the new `Manager.SyncFileRangeHint(rel)`
(`internal/storage/smgr.go`), bracketed by a per-context `On*WritebackWait`/
`On*WritebackDone` hook pair (mirrors `OnFlushWait`/`OnFlushDone`). A
successful hint increments `shared{Backend,Bgwriter,Checkpoint}WritebackCount`;
`ErrWritebackUnsupported` or any other error does not (no real IO happened,
so nothing is counted — matches this doc's running "honest zero, not
fabricated" rule). Three GUCs — `checkpoint_flush_after` (default 32),
`bgwriter_flush_after` (default 64), `backend_flush_after` (default 0,
"never enabled by default") — mirror upstream's exact defaults
(`postgres/src/include/pg_config_manual.h`'s `DEFAULT_CHECKPOINT_FLUSH_AFTER`/
`DEFAULT_BGWRITER_FLUSH_AFTER`/`DEFAULT_BACKEND_FLUSH_AFTER`, 0-256 range
mirroring `WRITEBACK_MAX_PENDING_FLUSHES`=256), threaded through
`initdb.OpenOptions`→`cmd/goopg/main.go`'s `intGUC` the same way
`BgwriterMaxPages` already is (boot-time only, like most of this codebase's
storage-tuning GUCs — no SIGHUP live-reload wiring in this slice).

`Manager.SyncFileRangeHint` (`internal/storage/smgr.go`) delegates to a new
platform-split `syncFileRangeHint(f *os.File)`:
`internal/storage/writeback_linux.go` calls the real
`unix.SyncFileRange(fd, 0, 0, SYNC_FILE_RANGE_WRITE)` (offset/nbytes 0 means
"the whole file to current EOF"; `SYNC_FILE_RANGE_WRITE` alone starts
async writeback without waiting or promising durability — `fsync` still
owns that, exactly matching upstream's `pg_flush_data` contract);
`internal/storage/writeback_other.go` (`!linux`) returns
`ErrWritebackUnsupported` unconditionally, mirroring upstream's behaviour
on platforms without `HAVE_SYNC_FILE_RANGE` (writeback is simply
unavailable, not approximated by a full `fsync`, which would silently
change the durability/blocking contract this hint deliberately lacks).

`internal/executor/pgstat_io.go`'s `fetchIOStatRows` wires the three new
counter pairs into the `writeback`/`writeback_time` cells of three rows:
`(client backend, relation, normal)`, `(background writer, relation,
normal)`, and `(checkpointer, relation, normal)` — the first real
instrumentation for the latter two rows, which previously rendered all
zeros for every column.

**Deliberate simplifications vs. upstream (recorded here + deferral
ledger, not hidden):**
1. Upstream's `WritebackContext` coalesces up to `WRITEBACK_MAX_PENDING_FLUSHES`
   (256) per-relation-segment block ranges before issuing one
   `sync_file_range` per range; goopg tracks one running page count per
   context and, on threshold-crossing, issues a single hint over
   *whichever relation was just written*, not a coalesced multi-relation
   batch. Real kernel behaviour, real GUC-driven cadence, simpler
   bookkeeping.
2. `backend_flush_after` is `PGC_USERSET` upstream (a per-session GUC);
   goopg applies it as one process-wide threshold (`initdb.Open` wires the
   boot-time GUC value only). A `SET backend_flush_after` in one session
   would affect every session's accounting.
3. bgwriter/checkpointer `writeback_time` gating gates on the boot-time
   `TrackIOTiming` value with a plain `time.Now()`/`time.Since` pair
   (`internal/initdb/open.go`), not the `ActivityRegistry` wait-event
   clock the backend path uses — these two singleton background
   goroutines have no registered `activity` background slot yet (unlike
   the WAL writer's `walProcNum`), so their writeback wait never surfaces
   in `pg_stat_activity`, only in `pg_stat_io`.
4. The background writer / checkpointer rows' own `writes`/`write_bytes`/
   `write_time` cells are still an honest 0 — goopg's only real write
   counter (`sharedWrittenCount`/`sharedWriteTimeNanos`) is deliberately
   backend-scoped (see this doc's earlier `dirtied=`/`written=` section);
   attributing bgwriter/checkpointer's own writes to their own rows is a
   smaller, separate residual not required for writeback's own threshold
   accounting.

**Tests:** `internal/storage/writeback_test.go` —
`TestPoolBackendWritebackTriggersAtThreshold` /
`TestPoolBgwriterWritebackTriggersAtThreshold` /
`TestPoolCheckpointerWritebackTriggersAtThreshold` (each context's
threshold-crossing triggers a real writeback via a live temp-file-backed
`Manager`), `TestSyncFileRangeHintOnRealFile` (raw platform-hook smoke
test). `internal/executor/pgstat_io_test.go` —
`TestPgStatIOWritebackRendered` (end-to-end rendered-cell assertion,
mirrors `TestPgStatIOExtendTimeRendered`).

Remaining M0122-0003 sub-items after this loop: `EXPLAIN (BUFFERS)` without
ANALYZE (planning-time buffers), local/temp-buffer terms, `pg_stat_io`'s
`reuses` op counter (needs the `BufferAccessStrategy` ring buffer), and the
four simplifications named above.

**Verification:** `go build ./...` clean; `go test ./internal/storage/...
./internal/executor/... ./internal/config/...` PASS (see also the
initdb/cmd build check run this loop).

## `CTEDMLPrefix` nested-node instrumentation (later loop, 2026-07-06)

The per-CTE ANALYZE stats section above fixed the `CTE DML` summary line
itself (`Build()`'s `*planner.CTEDMLPrefix` case now runs through
`maybeInstrument`), but left the nodes it wraps — the INSERT/UPDATE/DELETE/
MERGE plan(s) and the outer query body — reporting cost-only estimates
under `EXPLAIN ANALYZE`, even though they demonstrably ran and produced
rows.

**Root cause.** `cteDMLPrefixOp.Open()` (`internal/executor/
operators_cte_dml.go`) cannot `Build()` its DML plans and outer body ahead
of time the way every other operator's children are built — CTE write-then-
read ordering requires executing each DML CTE to completion, restoring the
statement-start snapshot, *then* building the outer query so it sees
pre-CTE state. So those `Build()` calls happen lazily inside `Open()`, long
after `explainOp.Open()`'s `withInstrumentation(timing, func() { return
Build(o.plan.Child) })` call has returned. `withInstrumentation`'s `defer`
resets the package-global `instrumentScope` back to its outer value (nil,
at the top level) the moment its `fn` returns — which happens as soon as
the *outermost* `Build()` call constructs `cteDMLPrefixOp` itself, well
before `inner.Open(ctx)` (and therefore `cteDMLPrefixOp.Open`) ever runs.
So the nested `Build(dml)` / `Build(o.plan.Body)` calls always saw
`instrumentScope == nil`, and `maybeInstrument` skipped wrapping — the
renderer's `nodeStatsTable` simply had no entry for those nodes.

**Fix.** Rather than widening `withInstrumentation`'s scope (which would
require holding it open across the entire DML-execution dance, coupling an
EXPLAIN-only concern into core CTE execution ordering), `cteDMLPrefixOp`
now carries forward the specific `*instrumenter` that was active on its
*own* `Build()` call and reinstates it locally around its two lazy
`Build()` sites:

- New `instrumentScopeCarrier` interface (`internal/executor/
  instrument.go`), mirroring the existing `heapFetchCounter` hand-off
  pattern (0118-0102): `maybeInstrument` now also checks
  `op.(instrumentScopeCarrier)` and, if implemented, calls
  `setInstrumentScope(instrumentScope)` — handing the operator the
  instrumenter alive at the moment `maybeInstrument` wrapped it (which is
  non-nil precisely when under `EXPLAIN ANALYZE`).
- `cteDMLPrefixOp` implements it (stores the pointer in a new `scope`
  field) and gained `buildUnderScope(n planner.Node) (Operator, error)`: a
  thin save/restore wrapper — `prev := instrumentScope; instrumentScope =
  o.scope; defer func() { instrumentScope = prev }(); return Build(n)`.
  `Open()`'s DML loop (`Build(dml)`) and outer-body build (`Build(o.plan.
  Body)`) now call `o.buildUnderScope(...)` instead of the bare package
  function. When not under EXPLAIN ANALYZE, `o.scope` is nil, so this is a
  no-op — identical to the pre-fix code path.
- Because `instrumentScope.table` is a single shared map for the whole
  EXPLAIN ANALYZE invocation, reinstating the same `*instrumenter` means
  the nested nodes' stats land in the *same* `nodeStatsTable` the renderer
  already reads — no new plumbing needed on the render side.
  `planChildren`'s existing `*planner.CTEDMLPrefix` case (returns
  `p.DMls` + `p.Body`, `internal/executor/operators_explain.go`) already
  walks into these nodes; it previously found no stats because none had
  ever been recorded, not because the walk was wrong.

**Tests:** `internal/executor/with_explain_test.go`'s
`TestExplainCTEDMLPrefixNestedInsertReportsActualRows` asserts the nested
`Insert on t` line specifically shows `actual time=`/`rows=2.00` (not just
the `CTE DML` summary line, which the pre-existing
`TestExplainCTEDMLPrefixAnalyzeReportsActualRows` already covered — its
stale "deferred, see ledger" doc comment was removed since the gap it
described is now closed).

**Verification:** `go build ./...` clean; `go vet ./internal/executor/...`
clean; `go test -count=1 ./internal/executor/... ./internal/storage/...
./internal/planner/... ./internal/parser/... ./internal/server/...
./internal/config/...` all PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/
Q13=33). Ledger: `.ralph/deferral_ledger.md` (2026-07-06 row, closes rows
467/468).

## `EXPLAIN (BUFFERS)` without `ANALYZE` — planning-time "Planning" group

**Problem (ledger rows 471/472/481/497/498, "gap (2)"):** upstream's
`ExplainOnePlan` (`postgres/src/backend/commands/explain.c`) always calls
`show_buffer_usage(es, &bufusage, true)` with a *planning-time*
`BufferUsage` snapshot whenever `es->buffers` is set, regardless of
whether `ANALYZE` was also requested. `show_buffer_usage`'s own
`peek_buffer_usage` helper returns true for any non-`EXPLAIN_FORMAT_TEXT`
format as soon as buffer tracking was requested, even when every counter
is zero (TEXT instead suppresses the whole block when nothing was
touched — the existing per-node `Buffers: shared ...` TEXT line already
mirrors this positive-only gate correctly). Before this fix, goopg's
`explainOp.Open` never populated a "Planning" group at all for bare
`EXPLAIN (BUFFERS)` (no `ANALYZE`) in FORMAT JSON/XML/YAML — the key was
simply absent, not present-and-zero.

**Fix:** `internal/executor/operators_explain.go` gains
`planningBufferUsageJSON()`, returning a flat
`{"Shared Hit Blocks": 0, "Shared Read Blocks": 0, "Shared Dirtied
Blocks": 0, "Shared Written Blocks": 0}` map. `explainOp.Open`'s two
non-TEXT render sites (the ANALYZE/summary path and the plan-only path)
both set `root["Planning"] = planningBufferUsageJSON()` whenever
`opts.Buffers` is true, independent of `ANALYZE` — matching upstream's
independence of the "Planning" group from the ANALYZE flag exactly. The
generic XML/YAML renderer (already reused for the plan tree and the
existing "Buffers"/"Settings" groups) needs no changes: `xmlTagName`
passes `"Planning"` through unchanged (no whitespace to sanitize) and
nests the four `Shared * Blocks` children the same way it already
sanitizes `"Shared Hit Blocks"` → `Shared-Hit-Blocks` for the per-node
groups.

goopg's planner (`internal/planner`) resolves every relation against the
in-memory `catalog.Catalog` and never calls into `storage.Pool` during
cost estimation — there is no planning-phase code path that could
produce a nonzero counter here, so the all-zero stub is not an
approximation of a real value, it is the actually-correct value given
goopg's architecture. TEXT format is intentionally left unchanged: since
planning buffers are always zero, TEXT's existing positive-only gate
already produces upstream-correct output (no "Planning:" block) without
any new code.

**Tests:** `internal/executor/explain_buffers_test.go` —
`TestExplainBuffersJSONWithoutAnalyzeIncludesPlanningGroup` (bare
`EXPLAIN (BUFFERS, FORMAT JSON)`, no `ANALYZE`, must show `"Planning"`
with hit/read keys), `TestExplainBuffersJSONWithoutBuffersOmitsPlanningGroup`
(plain `EXPLAIN (FORMAT JSON)`, no `BUFFERS`, must NOT show it — pins the
opt-in gate), `TestExplainBuffersAnalyzeJSONIncludesPlanningGroup`
(`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` also shows it, confirming
ANALYZE-independence), `TestExplainBuffersXMLWithoutAnalyzeIncludesPlanningGroup`
(XML sibling, asserts `<Planning>`/`<Shared-Hit-Blocks>0</Shared-Hit-Blocks>`).

**Verification:** `go build ./...` clean; `go test -count=1
./internal/executor/...` (full package) and
`./internal/storage/... ./internal/planner/... ./internal/parser/...
./internal/server/... ./internal/config/...` all PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33). Ledger:
`.ralph/deferral_ledger.md` (2026-07-06 row, closes gap (2) from rows
471/481/497/498).

Local/temp-buffer terms were the only sub-item still open in the
BUFFERS-rendering cluster after this fix — see the next section, which
closes them the same loop-later.

## Local/Temp `* Blocks` terms (later loop, 2026-07-06)

**Problem (ledger row 508's own resume point):** upstream's
`show_buffer_usage`'s non-`EXPLAIN_FORMAT_TEXT` branch always renders
`Local Hit/Read/Dirtied/Written Blocks` and `Temp Read/Written Blocks`
alongside the `Shared *` terms, unconditionally once `BUFFERS` was
requested — the exact same "print even if all zeroes" rule the prior
`Shared *`/`Planning` fixes already implement. goopg had never emitted
these six keys anywhere (neither per-node ANALYZE stats nor the
planning-time group), even though they are always legitimately zero:
goopg has no local-buffer-manager or temp-buffer concept at all — every
relation, including temp tables, is resolved through the one shared
`storage.Pool` — so, exactly like the planning-time `Shared *` terms
before them, "always zero" here is architecturally correct, not a
narrower stub.

**Fix:** both non-TEXT buffer-rendering sites gained the same six
constant-zero keys:
- `planningBufferUsageJSON()` (`internal/executor/operators_explain.go`)
  now returns `Local Hit/Read/Dirtied/Written Blocks` and `Temp
  Read/Written Blocks` alongside the four pre-existing `Shared *` keys.
- `planToJSONWithStats`'s per-node `opts.Buffers` block sets the same six
  keys to `int64(0)` next to the live `s.bufHit`/`s.bufRead`/
  `s.bufDirtied`/`s.bufWritten` shared counters.

TEXT format again needs no change: `formatBuffersLine` only ever emits
the `shared` clause today, and since local/temp are always zero,
upstream's own `has_local`/`has_temp` gates would also suppress those
clauses — there is no TEXT-visible difference to produce. I/O timing
terms (`Local/Temp I/O Read/Write Time`) remain out of scope for this
slice — they are a separate deferred gap (goopg has no per-node local/
temp *or* planning-time I/O timing collection at all yet, tracked
alongside the existing `Shared I/O Read/Write Time` per-node-only gap).

**Tests:** `internal/executor/explain_buffers_test.go` —
`TestExplainBuffersJSONAlwaysIncludesLocalTempBlocks` (per-node,
`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)`, asserts all six keys present),
`TestExplainBuffersPlanningGroupIncludesLocalTempBlocks` (bare `EXPLAIN
(BUFFERS, FORMAT JSON)`, asserts the `"Planning"` group also carries all
six keys).

**Verification:** `go build ./...` clean; `go test -count=1
./internal/executor/... ./internal/storage/... ./internal/planner/...
./internal/parser/... ./internal/server/... ./internal/config/...` all
PASS; `scripts/tpch-spotcheck.sh` PASS.

This closes the local/temp-buffer-terms sub-item named as the only
remaining open item in the BUFFERS-rendering cluster. The cluster's last
open item is now only the `reuses` `pg_stat_io` op counter (needs a
`BufferAccessStrategy`-style ring buffer goopg does not implement) plus
the Local/Temp/Planning-time I/O timing terms just named above.
