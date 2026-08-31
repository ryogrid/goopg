# 0018-0002 — Static Plan Rendering and Output Contract

**Status:** accepted (Stage A)
**Milestone:** [0018 — EXPLAIN / EXPLAIN ANALYZE Support](../../milestones/0018-explain-and-explain-analyze.md)
**Spans seam:** `Plan.Explain` Options threading, FORMAT JSON renderer,
VERBOSE column qualification, ANALYZE-not-supported gating.
**Cross-links:**
[0018-0001](0018-0001-explain-parser-options-and-ast.md) (parser AST that
this slice consumes), [0003-0007](0003-0007-explain.md) (existing TEXT
renderer baseline).

## Context

M0018-0001 step 1 added the parser AST for upstream-compatible
EXPLAIN options. The parsed options currently flow into
`parser.ExplainStmt.Options` but the planner drops them on the
floor — the executor's `explainOp` always renders a fixed TEXT
shape, ignoring `FORMAT JSON`, `VERBOSE`, and `ANALYZE`.

This slice closes that gap for the **static** options (FORMAT,
VERBOSE) so an operator running `EXPLAIN (FORMAT JSON) <stmt>`
gets machine-readable structured output, and `EXPLAIN VERBOSE`
shows per-node schema columns. ANALYZE is the Stage B contract
and is intentionally rejected here with a clear `0A000` so a user
asking for it gets an actionable error instead of silent
fallthrough.

## What lands

### Plan node carries options

`planner.Explain` grows an `Options parser.ExplainOptions` field.
The Plan dispatcher copies `s.Options` from `parser.ExplainStmt`
verbatim. Pre-M0018 callers that built `*planner.Explain`
directly (none exist in the production paths today; tests use
the dispatcher) keep working — the zero-value `Options` matches
the bare-EXPLAIN form.

### ANALYZE rejection

`Plan(ExplainStmt{Options.Analyze=true, ...})` errors with
SQLSTATE `0A000`:

```
EXPLAIN ANALYZE is not supported in v0 (Stage B — see
docs/milestones/0018-explain-and-explain-analyze.md)
```

The error fires before the inner statement is planned so a user
running `EXPLAIN ANALYZE INSERT ...` doesn't accidentally trigger
the side effects.

### FORMAT JSON

`explainOp.Open` branches on `o.plan.Options.Format`:

- `ExplainFormatText` (default): existing pre-order indented
  walker, byte-for-byte unchanged.
- `ExplainFormatJSON`: emit a single-row result whose one cell is
  the JSON-encoded plan tree.

JSON shape (one object per node):

```json
{
  "Node Type": "Seq Scan on pgbench_accounts",
  "Plan Rows": 1000,
  "Output": ["aid", "abalance"],
  "Plans": [ <child>, ... ]
}
```

- `Node Type` is `describePlan(n)` — the same label TEXT format
  uses, so JSON and TEXT stay in lockstep.
- `Plan Rows` is `planner.EstimateRows(n)`; omitted when zero.
- `Output` is the node's schema column names — only emitted when
  `Options.Verbose` is true (matches upstream's behaviour).
- `Plans` is the array of child nodes. Empty / absent for leaf
  nodes.

The result is wrapped in a single-element array (matching
upstream's `[ {root} ]` shape) so future extensions (multiple
top-level CTEs in the JSON tree) don't require a schema change.

### VERBOSE in TEXT output

When `Options.Verbose` is true, the TEXT walker appends
`Output: (col1, col2, ...)` after each node's label, mirroring
upstream's verbose-mode output line. Shape:

```
Projection
  Output: (aid, abalance)
  ->  Seq Scan on pgbench_accounts
        Output: (aid, abalance)
```

### Other options (forward)

- COSTS / TIMING / SUMMARY / BUFFERS / SETTINGS: parsed but
  unused this slice. Stage B (M0018-0003) honors TIMING and
  SUMMARY; BUFFERS / SETTINGS surface in M0018-0004 once the
  schema is settled. The parser already accepts them — silent
  no-op until then is the agreed Stage A contract per the
  milestone doc.

## Tests

- `TestExplainTextFormatUnchanged`: bare `EXPLAIN SELECT 1`
  produces the same output as before M0018-0002 (regression
  guard for byte-for-byte invariance — every existing
  pg_stat_replication / TPC-H test that runs EXPLAIN keeps
  working).
- `TestExplainFormatJSON`: `EXPLAIN (FORMAT JSON) SELECT 1`
  produces a single row whose value parses as JSON and whose
  root array contains one object with `"Node Type"`.
- `TestExplainVerboseAddsOutput`: `EXPLAIN VERBOSE SELECT
  aid FROM pgbench_accounts` emits at least one line containing
  `Output:` listing the projected columns.
- `TestExplainAnalyzeRejected`: `EXPLAIN ANALYZE SELECT 1`
  errors with SQLSTATE `0A000` and the "Stage B" message.
- `TestExplainFormatJSONHonoursVerbose`: JSON output includes
  the `Output` array only when VERBOSE is set.

## Out of scope

- ANALYZE runtime instrumentation (per-node timing, rows/loops)
  — M0018-0003.
- BUFFERS / SETTINGS counter rendering — M0018-0004.
- Filter predicate / join condition pretty-printing in TEXT
  output — orthogonal renderer improvement, future slice.
- Tuple-level positional output in JSON — current Output array
  is column-name-only, matching upstream's "verbose without
  costs" shape.
