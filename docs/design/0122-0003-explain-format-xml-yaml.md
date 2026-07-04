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
| SETTINGS rendering | open — parsed (`opts.Settings`/`Set.Settings`), never read in `operators_explain.go`; no "Settings:" line ever emitted |
| BUFFERS rendering | open — parsed (`opts.Buffers`), never read; `nodeStats` has no buffer hit/read counters |
| `pg_stat_io` | open — table registered (`internal/catalog/catalog.go:6798`, OID 8061) but `VirtualRows` always returns `nil`; no I/O stat collection exists |
| per-CTE ANALYZE stats | **done** (landed concurrently in the shared tree, folded in this loop — see "Problem"/"Fix" above); `CTEDMLPrefix` nested-node residual still open, ledger row below |
| `track_io_timing` runtime `SET` | open — GUC registered `ContextUserset` but only consulted once at process boot (`cmd/goopg/main.go`'s `boolGUC` → `initdb.OpenOptions.TrackIOTiming`), not re-checked per session/query |

Remaining items recorded in `.ralph/deferral_ledger.md` (2026-07-04,
M0122-0003 row) with resume points. The fix_plan checkbox stays unchecked
until the rest of the cluster lands.
