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
| `pg_stat_io` | open — table registered (`internal/catalog/catalog.go:6798`, OID 8061) but `VirtualRows` always returns `nil`; no I/O stat collection exists |
| per-CTE ANALYZE stats | **done** (landed concurrently in the shared tree, folded in this loop — see "Problem"/"Fix" above); `CTEDMLPrefix` nested-node residual still open, ledger row below |
| `track_io_timing` runtime `SET` | open — GUC registered `ContextUserset` but only consulted once at process boot (`cmd/goopg/main.go`'s `boolGUC` → `initdb.OpenOptions.TrackIOTiming`), not re-checked per session/query |

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
