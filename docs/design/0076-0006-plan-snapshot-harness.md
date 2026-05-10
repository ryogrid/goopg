# Design 0076-0006 — Plan-snapshot regression harness

**Milestone:** M0076-0006
**Status:** **landed 2026-05-10 (commit pending) with
documented caveat:** when running diff on all 22 queries
in a single batch, ~9 queries show plan divergence due
to connection-pool ordering effects (state accumulates
on the connection across consecutive EXPLAIN
invocations). When running per-query (`--queries=N`)
all queries match deterministically. **Recommended
workflow:** for a single planner commit's verification,
diff per-query against the baseline (`for q in 1 2 ...
22; do plan-snapshot diff --queries=$q; done`); the
batch mode is useful for surveying many queries' plans
quickly but is not a stable regression mechanism. Root
cause investigation (planner-side nondeterminism vs
pool-state) deferred to M0077.
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** none (independent of M0076-0001..0005).
Builds on existing `internal/executor/operators_explain.go`
EXPLAIN renderer.

## Context

M0075's repeated full-sweep cost (~25 min per commit)
was a productivity drag on planner-only iterations
(selectivity, transitivity, rebind). Phase 7 §8
documented the lesson; the user's 2026-05-10 request
prioritised this harness as the FIRST commit in M0076
so subsequent planner work (0004 cost-model, 0001 hook
re-enable, 0005 Q9 fix) can use plan-diff for fast
feedback (≤ 30 s) instead of waiting for the full
sweep.

**Decision tree** (when plan-diff is sufficient vs when
executor sweep is required):

| Change kind | Sufficient | Required |
|-------------|------------|----------|
| Planner: selectivity, transitivity, rebind, cost-model | plan-diff + targeted sweep on diverging queries | — |
| Planner: a planner pass that affects ALL queries (e.g., predicate ordering) | plan-diff (all 22 captured) + spot sweep | 21-q sweep if plan-diff is non-trivial across many queries |
| Executor: Datum, arena, evaluator, slot pipeline | — | 21-q sweep MANDATORY (M0075-0003 lesson) |
| Catalog: persistence, schema, types | — | full sweep + integration tests |
| Wire-protocol: client-facing | — | full sweep + protocol conformance tests |

## Goals

- `cmd/plan-snapshot/main.go` (NEW): a CLI tool with
  two subcommands:
  - `capture --baseline <label> [--queries 1,2,...]`:
    runs each TPC-H query through the planner, serialises
    the plan to JSON, writes
    `plan_snapshots/<label>.json`.
  - `diff --baseline <label> [--mode structural|strict-text|semantic-cost]`:
    re-runs the planner against the same queries, prints
    per-query verdict (`MATCH` / `DIFFER`).
- Three equality levels:
  - **structural** (default): node-type tree + ColumnRef
    indices; ignores cost estimate variance.
  - **strict-text**: byte-for-byte EXPLAIN output diff
    (high false-positive rate; opt-in only).
  - **semantic-cost**: structural + cost estimate ±10 %
    tolerance (for cost-model commits like M0076-0004).
- `Makefile` targets `plan-snapshot-capture LABEL=<name>`
  and `plan-diff LABEL=<name>`.
- Capture in ≤ 30 s wall time (planner-only, no
  execution).
- First baseline captured at HEAD `ffc3429` (M0075 close
  + M0076 carry-forward docs).

## Non-goals

- **Replacing the 21-q sweep for executor commits.**
  Executor changes (Datum / arena / catalog / wire-
  protocol) STILL require the full sweep. The harness
  documents this trade-off explicitly.
- **Cost-model regression detection beyond ±10 %.** If
  cost estimates change wildly, that's intended (the
  cost-model commit is the cause). The harness flags
  divergence; humans decide whether it's acceptable.
- **Auto-running on every commit.** The harness is
  a developer tool; CI integration is M0077 candidate.
- **Plan visualisation / pretty-print.** The harness
  produces machine-readable diffs; visual debugging
  uses `EXPLAIN` directly.

## Proposed implementation

### Tool architecture (`cmd/plan-snapshot/main.go`)

```go
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "os"
    "strings"

    "github.com/goopg/goopg/internal/parser"
    "github.com/goopg/goopg/internal/planner"
    // catalog setup helper from internal/testutil/tpch
)

const tpchQueries = 22 // Q1..Q22; Q15 has 3 sub-queries

// PlanSnapshot is the serialisable JSON form for one
// query's plan.
type PlanSnapshot struct {
    Query int            `json:"query"`
    SQL   string         `json:"sql"`
    Plan  PlanNode       `json:"plan"`
    Error string         `json:"error,omitempty"`
}

// PlanNode mirrors the planner's Node interface but
// in a form that JSON marshals stably (no
// pos/private fields).
type PlanNode struct {
    Type     string     `json:"type"`     // e.g. "Filter", "MultiHashJoin"
    Children []PlanNode `json:"children,omitempty"`
    // Per-node-type fields. Examples:
    Table        string     `json:"table,omitempty"`        // SeqScan, IndexScan
    Index        string     `json:"index,omitempty"`        // IndexScan
    Predicate    string     `json:"predicate,omitempty"`    // Filter (string form)
    JoinType     string     `json:"join_type,omitempty"`    // Join
    JoinAlgo     string     `json:"join_algo,omitempty"`    // Join
    BuildLeft    bool       `json:"build_left,omitempty"`   // Join
    LeftKey      string     `json:"left_key,omitempty"`     // Join
    RightKey     string     `json:"right_key,omitempty"`    // Join
    Tables       []string   `json:"tables,omitempty"`       // MultiHashJoin
    OrderBy      []string   `json:"order_by,omitempty"`     // Sort
    GroupKeys    []string   `json:"group_keys,omitempty"`   // Aggregate
    AggExprs     []string   `json:"agg_exprs,omitempty"`    // Aggregate
    EstimateRows int64      `json:"estimate_rows,omitempty"`
}

func main() {
    if len(os.Args) < 2 {
        usage()
    }
    switch os.Args[1] {
    case "capture":
        runCapture(os.Args[2:])
    case "diff":
        runDiff(os.Args[2:])
    default:
        usage()
    }
}

func runCapture(args []string) {
    var label string
    var queries string
    fs := flag.NewFlagSet("capture", flag.ExitOnError)
    fs.StringVar(&label, "label", "", "baseline label (filename suffix)")
    fs.StringVar(&queries, "queries", "1-22", "comma-or-range list")
    fs.Parse(args)

    qs := expandQueries(queries)
    snapshots := make([]PlanSnapshot, 0, len(qs))
    cat := loadTPCHCatalog() // from internal/testutil/tpch
    for _, q := range qs {
        snap := captureOne(q, cat)
        snapshots = append(snapshots, snap)
    }
    writeJSON(label, snapshots)
}

func captureOne(q int, cat catalog.Catalog) PlanSnapshot {
    sql := loadTPCHQuerySQL(q) // from bench/tpch/queries
    stmt, err := parser.Parse(sql)
    if err != nil {
        return PlanSnapshot{Query: q, SQL: sql, Error: err.Error()}
    }
    node, err := planner.Plan(stmt, cat)
    if err != nil {
        return PlanSnapshot{Query: q, SQL: sql, Error: err.Error()}
    }
    return PlanSnapshot{Query: q, SQL: sql, Plan: nodeToSnapshot(node)}
}

func runDiff(args []string) {
    var label string
    var mode string
    fs := flag.NewFlagSet("diff", flag.ExitOnError)
    fs.StringVar(&label, "label", "", "baseline label to compare against")
    fs.StringVar(&mode, "mode", "structural", "structural | strict-text | semantic-cost")
    fs.Parse(args)

    baseline := readJSON(label)
    cat := loadTPCHCatalog()
    diverging := 0
    for _, base := range baseline {
        current := captureOne(base.Query, cat)
        if !planEqual(base, current, mode) {
            fmt.Printf("Q%d: DIFFER (mode=%s)\n", base.Query, mode)
            diverging++
        } else {
            fmt.Printf("Q%d: MATCH\n", base.Query)
        }
    }
    if diverging > 0 {
        os.Exit(1)
    }
}

func planEqual(a, b PlanSnapshot, mode string) bool {
    switch mode {
    case "structural":
        return structuralEqual(a.Plan, b.Plan)
    case "strict-text":
        ja, _ := json.Marshal(a.Plan)
        jb, _ := json.Marshal(b.Plan)
        return string(ja) == string(jb)
    case "semantic-cost":
        return structuralEqual(a.Plan, b.Plan) &&
            costsWithinTolerance(a.Plan, b.Plan, 0.10)
    }
    return false
}
```

### nodeToSnapshot — the convertor

Walks a `planner.Node` tree and produces a serialisable
`PlanNode`. For each node type (`SeqScan`, `IndexScan`,
`Filter`, `Join`, `MultiHashJoin`, `Sort`, `Aggregate`,
`Project`, `Limit`, `NestedLoopIndexJoin`, `Values`,
`Insert`, `Update`, `Delete`, `Explain`), extract the
relevant fields and skip pos/internal-state.

For ColumnRef: stringify as `"<table>.<col>[idx=<n>,src=<sourceTableIdx>]"`
so disambiguation is preserved in the diff.

For Predicate / Key / Order: stringify the Expr tree
recursively (ColumnRef + BinaryOp + UnaryOp + literals)
into a compact string form.

### structuralEqual

```go
func structuralEqual(a, b PlanNode) bool {
    if a.Type != b.Type { return false }
    if a.Table != b.Table { return false }
    if a.Index != b.Index { return false }
    if a.Predicate != b.Predicate { return false }
    if a.JoinType != b.JoinType { return false }
    if a.JoinAlgo != b.JoinAlgo { return false }
    if a.LeftKey != b.LeftKey { return false }
    if a.RightKey != b.RightKey { return false }
    if !stringSliceEqual(a.Tables, b.Tables) { return false }
    if !stringSliceEqual(a.OrderBy, b.OrderBy) { return false }
    if !stringSliceEqual(a.GroupKeys, b.GroupKeys) { return false }
    if !stringSliceEqual(a.AggExprs, b.AggExprs) { return false }
    if len(a.Children) != len(b.Children) { return false }
    for i := range a.Children {
        if !structuralEqual(a.Children[i], b.Children[i]) { return false }
    }
    return true
}
```

EstimateRows is INTENTIONALLY excluded from structural
mode — cost estimates fluctuate harmlessly.

### Reuse: TPC-H query loader

`bench/tpch/runtime_goopg/queries/qN.sql` (or similar
location — the harness reads from disk).

`internal/testutil/tpch/` already has TPC-H schema +
catalog setup helpers (the cluster-backed test surface).
The harness reuses these for the catalog object instead
of re-implementing.

## Verification

Pre-commit gate:
- `go test ./cmd/plan-snapshot/... ./...` PASS.
- `make plan-snapshot-capture LABEL=test && make plan-diff LABEL=test`
  — zero diff (sanity).
- 21-q row-count sweep: zero change (this commit
  doesn't touch executor at all).
- Capture wall time ≤ 30 s for all 22 queries.

New tests in `cmd/plan-snapshot/main_test.go`:
- `TestCaptureRoundTrip` — capture then diff against
  same data → zero diff.
- `TestStructuralIgnoresCost` — same plan with
  different EstimateRows → MATCH.
- `TestStrictTextDetectsDigitsChange` — same plan with
  different cost → DIFFER under strict-text.
- `TestSemanticCostTolerance` — cost ±9 % → MATCH;
  cost ±11 % → DIFFER.
- `TestPlanError` — query with parser error → snapshot
  records the error string instead of plan.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | nodeToSnapshot misses a node-type → silent diff false negative | Exhaustive switch on Node interface; default arm errors loudly. |
| R2 | Map-iteration nondeterminism in ColumnRef stringification | Sort all keys; M0076-0004 fixes equiv_class.go's classes() the same way. |
| R3 | TPC-H query files moved / renamed → harness breaks | Resolve query path via env-var override + fallback to default; document the path in the design doc. |
| R4 | Plan-diff produces TOO-MANY false positives → noise drowns real signal | Default mode is structural (cost-tolerant); strict-text is opt-in. |

## Migration plan

Single commit (Commit B in M0076):
1. Add `cmd/plan-snapshot/main.go` + helpers.
2. Add `Makefile` targets.
3. Capture the M0076 baseline at HEAD `ffc3429`.
4. Land tests.
5. Verify via self-diff (zero output).
6. Document the decision tree in this design doc's
   §Context.

If capture takes > 30 s: investigate (catalog setup
overhead? per-query parsing cost?). The fast feedback
loop is the value proposition; if it's slow, harness
loses the point.

## References

- `internal/executor/operators_explain.go` — EXPLAIN
  text renderer (used as a verification cross-check
  in tests).
- `internal/planner/planner.go::Plan` — primary
  planning entry point invoked by the harness.
- `internal/testutil/tpch/` — TPC-H catalog + schema
  fixtures.
- `bench/tpch/runtime_goopg/queries/` — TPC-H query
  SQL files (or wherever they live).
