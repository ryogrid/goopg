package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// extractHeapFetches walks the EXPLAIN (FORMAT JSON, ANALYZE) tree and
// returns the "Heap Fetches" value of the first node that carries it
// (the IndexOnlyScan). found=false when no node emits the key.
func extractHeapFetches(t *testing.T, jsonRow string) (float64, bool) {
	t.Helper()
	var top []map[string]any
	if err := json.Unmarshal([]byte(jsonRow), &top); err != nil {
		t.Fatalf("invalid EXPLAIN JSON: %v\n%s", err, jsonRow)
	}
	if len(top) == 0 {
		t.Fatalf("EXPLAIN JSON empty: %s", jsonRow)
	}
	var walk func(node map[string]any) (float64, bool)
	walk = func(node map[string]any) (float64, bool) {
		if v, ok := node["Heap Fetches"]; ok {
			if f, ok := v.(float64); ok {
				return f, true
			}
		}
		// PG nests the root plan under "Plan"; children under "Plans".
		if p, ok := node["Plan"].(map[string]any); ok {
			if f, ok := walk(p); ok {
				return f, true
			}
		}
		if kids, ok := node["Plans"].([]any); ok {
			for _, k := range kids {
				if km, ok := k.(map[string]any); ok {
					if f, ok := walk(km); ok {
						return f, true
					}
				}
			}
		}
		return 0, false
	}
	return walk(top[0])
}

// TestExplainHeapFetchesIndexOnlyScan pins design 0118-0102: an
// EXPLAIN (FORMAT JSON, ANALYZE) over an index-only scan emits a
// "Heap Fetches" count, and that count reflects how many entries
// required a heap fetch (page not ALL_VISIBLE). This is the horizons.spec
// enabler — pruner_query reads ...->'Plan'->'Heap Fetches'.
func TestExplainHeapFetchesIndexOnlyScan(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE hft (data int)",
		"CREATE UNIQUE INDEX hft_data_key ON hft (data)",
		"INSERT INTO hft VALUES (1)",
		"INSERT INTO hft VALUES (2)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	const q = "SELECT data FROM hft WHERE data >= 1"

	// Sanity: the planner must promote this to an IndexOnlyScan, else the
	// Heap Fetches assertions below are meaningless.
	if ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog)); ios == nil {
		t.Fatalf("plan for %q has no IndexOnlyScan", q)
	}

	// COSTS OFF text render must carry the upstream IOS label.
	planLines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+q)
	if !strings.Contains(strings.Join(planLines, "\n"), "Index Only Scan using hft_data_key on hft") {
		t.Errorf("COSTS OFF render missing IOS label:\n%s", strings.Join(planLines, "\n"))
	}

	// Before VACUUM the heap page is not ALL_VISIBLE → both entries need a
	// heap fetch.
	rows := runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON, BUFFERS, ANALYZE) "+q)
	if len(rows) != 1 {
		t.Fatalf("EXPLAIN JSON: got %d rows, want 1", len(rows))
	}
	hf, ok := extractHeapFetches(t, rows[0])
	if !ok {
		t.Fatalf("no IndexOnlyScan node emitted \"Heap Fetches\":\n%s", rows[0])
	}
	if hf != 2 {
		t.Errorf("Heap Fetches = %v before VACUUM, want 2\n%s", hf, rows[0])
	}

	// After VACUUM the page is ALL_VISIBLE → the IOS fast path returns rows
	// from the index key with zero heap fetches.
	vacuumThen(t, ctx, "hft")
	rows = runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON, BUFFERS, ANALYZE) "+q)
	hf, ok = extractHeapFetches(t, rows[0])
	if !ok {
		t.Fatalf("no IndexOnlyScan node emitted \"Heap Fetches\" after VACUUM:\n%s", rows[0])
	}
	if hf != 0 {
		t.Errorf("Heap Fetches = %v after VACUUM, want 0\n%s", hf, rows[0])
	}

	// Text ANALYZE render carries the "Heap Fetches: N" detail line.
	textLines := runExplainRows(t, ctx, "EXPLAIN ANALYZE "+q)
	if !strings.Contains(strings.Join(textLines, "\n"), "Heap Fetches:") {
		t.Errorf("text ANALYZE render missing 'Heap Fetches:' line:\n%s", strings.Join(textLines, "\n"))
	}
}
