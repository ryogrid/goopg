package executor

import (
	"strings"
	"testing"
)

// TestExplainFunctionScanGenerateSeriesAlias pins M0134-0001 P2 S2: a
// FROM-clause SRF scan renders as PG's `Function Scan on <func> <alias>`
// instead of leaking the planner Go type via the %T fallback.
func TestExplainFunctionScanGenerateSeriesAlias(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM generate_series(1,3) g")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Function Scan on generate_series g") {
		t.Errorf("expected `Function Scan on generate_series g`; got:\n%s", joined)
	}
	if strings.Contains(joined, "*planner.GenerateSeries") {
		t.Errorf("planner type leaked into EXPLAIN; got:\n%s", joined)
	}
}

// TestExplainFunctionScanGenerateSeriesUnaliased pins the PG refname rule:
// with no AS alias the default alias IS the function name, so the label must
// not repeat it (`Function Scan on generate_series`, not `... generate_series
// generate_series`).
func TestExplainFunctionScanGenerateSeriesUnaliased(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM generate_series(1,3)")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Function Scan on generate_series") {
		t.Errorf("expected `Function Scan on generate_series`; got:\n%s", joined)
	}
	if strings.Contains(joined, "Function Scan on generate_series generate_series") {
		t.Errorf("alias equal to funcname must be omitted; got:\n%s", joined)
	}
}

// TestExplainFunctionScanVerbose pins the verbose label (schema-qualified)
// and the verbose-only `Function Call:` detail line, which must appear after
// the label and before any Filter line.
func TestExplainFunctionScanVerbose(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (VERBOSE, COSTS OFF) SELECT * FROM generate_series(1,3) g WHERE g < 3")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Function Scan on pg_catalog.generate_series g") {
		t.Errorf("expected verbose schema-qualified label; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Function Call: generate_series(1, 3)") {
		t.Errorf("expected verbose-only `Function Call: generate_series(1, 3)`; got:\n%s", joined)
	}
	// Function Call precedes the Filter line (PG explain.c 2067-2084).
	fcIdx := strings.Index(joined, "Function Call: generate_series(1, 3)")
	filterIdx := strings.Index(joined, "Filter:")
	if fcIdx < 0 {
		t.Fatalf("Function Call line missing: %s", joined)
	}
	if filterIdx >= 0 && fcIdx > filterIdx {
		t.Errorf("Function Call must precede Filter; got:\n%s", joined)
	}
}

// TestExplainProjectSetBareLabel pins that a SELECT-list SRF renders as a
// bare `ProjectSet` label (no `on <funcname>`, no Function Call) with its
// child visible beneath it.
func TestExplainProjectSetBareLabel(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT generate_series(1,3)")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "*planner.ProjectSet") {
		t.Errorf("planner type leaked for ProjectSet; got:\n%s", joined)
	}
	if !strings.Contains(joined, "ProjectSet") {
		t.Errorf("expected bare `ProjectSet` label; got:\n%s", joined)
	}
	if !strings.Contains(joined, "->  ") {
		t.Errorf("ProjectSet child must render beneath the label; got:\n%s", joined)
	}
	if strings.Contains(joined, "Function Call:") {
		t.Errorf("ProjectSet must not emit a Function Call detail; got:\n%s", joined)
	}
}

// TestExplainFunctionScanUnnestAndSubscripts pins the unnest and
// generate_subscripts Function Scan labels (plain, unaliased).
func TestExplainFunctionScanUnnestAndSubscripts(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	u := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM unnest(ARRAY[1,2,3])"), "\n")
	if !strings.Contains(u, "Function Scan on unnest") {
		t.Errorf("expected `Function Scan on unnest`; got:\n%s", u)
	}
	gs := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM generate_subscripts(ARRAY[1,2,3], 1)"), "\n")
	if !strings.Contains(gs, "Function Scan on generate_subscripts") {
		t.Errorf("expected `Function Scan on generate_subscripts`; got:\n%s", gs)
	}
}

// TestExplainFunctionScanUserSrfPlain pins the plain (non-verbose) label for
// a user-defined SETOF function. PG's get_func_name returns the BARE name in
// both modes; the schema is prepended only under VERBOSE (explain.c:
// 4490-4500), so a routine in the `public` schema must render
// `Function Scan on my_srf f`, not `Function Scan on public.my_srf f`.
// The function is created through the existing runDDL/runExplainRows harness
// (no new function-creation infrastructure).
func TestExplainFunctionScanUserSrfPlain(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, `CREATE FUNCTION my_srf() RETURNS SETOF int4 LANGUAGE sql AS $$ SELECT generate_series(1,3) $$`); err != nil {
		t.Fatalf("CREATE FUNCTION my_srf: %v", err)
	}

	// Aliased: the alias is shown, the schema is not (plain mode).
	aliased := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM my_srf() f"), "\n")
	if !strings.Contains(aliased, "Function Scan on my_srf f") {
		t.Errorf("expected `Function Scan on my_srf f`; got:\n%s", aliased)
	}
	if strings.Contains(aliased, "public.my_srf") {
		t.Errorf("plain mode must not schema-qualify the function; got:\n%s", aliased)
	}

	// Unaliased: the default alias equals the function name, so it is omitted.
	unaliased := strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM my_srf()"), "\n")
	if !strings.Contains(unaliased, "Function Scan on my_srf") {
		t.Errorf("expected `Function Scan on my_srf`; got:\n%s", unaliased)
	}
	if strings.Contains(unaliased, "Function Scan on my_srf my_srf") {
		t.Errorf("alias equal to funcname must be omitted; got:\n%s", unaliased)
	}
}
