package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExplainAnalyzeActualTimePerLoop pins M0122-0020: EXPLAIN ANALYZE's
// actual startup/total time is the PER-LOOP average, like `rows=` — PG's
// explain.c divides instrument->startup and ->total by nloops, and
// InstrEndLoop sums each cycle's first-tuple time into startup. goopg used
// to print the total over every loop, so an inner probe re-opened once per
// outer row (TPC-DS Q14: `time=0.003..1857.725 rows=42.54 loops=16173`)
// showed a time no single loop ever took.
//
// The inner probe's per-loop total times its loop count is the time spent
// in all its loops, which the nested stopwatch keeps inside the Nested
// Loop's own total. A cumulative print would exceed it ~100-fold.
func TestExplainAnalyzeActualTimePerLoop(t *testing.T) {
	ctx, cleanup := nliJointypeFixture(t)
	defer cleanup()

	rows, err := runQueryWithErr(ctx, "EXPLAIN (ANALYZE, COSTS OFF, FORMAT JSON) "+
		"SELECT count(*) FROM nlij_outer o JOIN nlij_inner i ON i.k = o.k")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(renderRows(rows), "\n")
	var doc []map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, text)
	}
	var nl, probe map[string]any
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		typ, _ := n["Node Type"].(string)
		switch {
		case typ == "Nested Loop":
			nl = n
		case strings.HasPrefix(typ, "Index Scan"), strings.HasPrefix(typ, "Index Only Scan"):
			probe = n
		}
		kids, _ := n["Plans"].([]any)
		for _, k := range kids {
			if m, ok := k.(map[string]any); ok {
				walk(m)
			}
		}
	}
	walk(doc[0]["Plan"].(map[string]any))
	if nl == nil || probe == nil {
		t.Fatalf("plan lacks a Nested Loop over an index probe:\n%s", text)
	}
	loops := probe["Actual Loops"].(float64)
	if loops != 100 {
		t.Fatalf("probe loops = %v, want 100:\n%s", loops, text)
	}
	probeStartup := probe["Actual Startup Time"].(float64)
	probeTotal := probe["Actual Total Time"].(float64)
	nlTotal := nl["Actual Total Time"].(float64)
	if probeStartup > probeTotal {
		t.Fatalf("probe startup %.6f > total %.6f:\n%s", probeStartup, probeTotal, text)
	}
	if probeTotal*loops > nlTotal*1.001+0.001 {
		t.Fatalf("probe per-loop total %.6f ms x %v loops exceeds the Nested Loop's %.6f ms — time printed cumulatively:\n%s",
			probeTotal, loops, nlTotal, text)
	}
}
