package executor

// M0134-0001 S17 — the "Parallel " EXPLAIN label prefix.
//
// PG sets Plan.parallel_aware per-PATH-CHOICE at path-construction time
// (create_seqscan_path, pathnode.c:996; create_bitmap_heap_path,
// pathnode.c:1115), and ExplainNode prints a bare "Parallel " token before
// the node name whenever it is set (explain.c:1630-1631). goopg mirrors this
// by stamping optimizer.SeqScan.Parallel / optimizer.BitmapHeapScan.Parallel
// once, at Gather-construction time, in internal/optimizer/parallel.go's
// stampParallelScan — see that function's doc comment for why a render-time
// "am I under a Gather" walk was rejected.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// parallelLabelTestTable mirrors internal/optimizer/parallel_test.go's
// bigTable fixture: a plain table with no statistics, big enough (via the
// injected BlocksForTable below) to clear the parallel size gate.
func parallelLabelTestTable(t *testing.T, name string) *catalog.Table {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

// parallelLabelTestSettings mirrors internal/optimizer/parallel_test.go's
// parallelTestSettingsBlocks: inject a relation size directly rather than
// fabricating statistics.
func parallelLabelTestSettings() optimizer.ParallelSettings {
	return optimizer.ParallelSettings{
		MaxWorkersPerGather: 2,
		MinTableScanBlocks:  1024,
		DebugParallelQuery:  "off",
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true }, // 4x the threshold
	}
}

// renderPlanText runs a Node through the same walkPlan driver EXPLAIN TEXT
// uses and returns the joined output lines.
func renderPlanText(n optimizer.Node) string {
	return renderPlanTextOpts(n, parser.ExplainOptions{})
}

// renderPlanTextOpts is renderPlanText with caller-supplied ExplainOptions,
// so a VERBOSE render can be driven through the same walkPlan entry point
// EXPLAIN VERBOSE uses (which routes through describePlanVerbose, the
// sibling of describePlan that S17 also had to touch).
func renderPlanTextOpts(n optimizer.Node, opts parser.ExplainOptions) string {
	var rows []Row
	var b strings.Builder
	walkPlan(&b, n, 0, &rows, opts)
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, r[0].StringValue())
		}
	}
	return strings.Join(lines, "\n")
}

// TestExplainParallelSeqScanLabel is the brief's acceptance criteria 1-3: a
// plan with a Gather over a driving SeqScan renders "Parallel Seq Scan", and
// a serial plan (no Gather) renders a bare "Seq Scan" with no stray prefix
// anywhere. FAILS on unmodified production code (SeqScan had no Parallel
// field, describePlan had no prefix logic) and PASSES after this slice.
func TestExplainParallelSeqScanLabel(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")

	// Serial: MaxWorkersPerGather = 0 disables insertion entirely.
	serialRoot := optimizer.MaybeAddGather(&optimizer.SeqScan{Table: tbl}, optimizer.ParallelSettings{})
	serialText := renderPlanText(serialRoot)
	if !strings.Contains(serialText, "Seq Scan on t") {
		t.Fatalf("serial plan missing bare 'Seq Scan on t':\n%s", serialText)
	}
	if strings.Contains(serialText, "Parallel") {
		t.Errorf("serial plan (no Gather) must not carry a 'Parallel' prefix anywhere:\n%s", serialText)
	}

	// Parallel: large relation clears the size gate, so MaybeAddGather wraps
	// the scan in a real Gather and stamps it via stampParallelScan.
	parallelRoot := optimizer.MaybeAddGather(&optimizer.SeqScan{Table: tbl}, parallelLabelTestSettings())
	if _, ok := parallelRoot.(*optimizer.Gather); !ok {
		t.Fatalf("expected a Gather at the root, got %T", parallelRoot)
	}
	parallelText := renderPlanText(parallelRoot)
	if !strings.Contains(parallelText, "Parallel Seq Scan on t") {
		t.Errorf("parallel plan missing 'Parallel Seq Scan on t':\n%s", parallelText)
	}
}

// TestExplainParallelSeqScanLabelVerbose pins the VERBOSE twin: PG's
// "Parallel " prefix is emitted in ExplainNode before the pname append and is
// entirely independent of es->verbose (explain.c:1630-1631), so EXPLAIN
// VERBOSE over the same parallel plan must show the same prefix. This is the
// case describePlanVerbose's *optimizer.SeqScan return statements have to
// carry themselves — they do not funnel through describePlan.
func TestExplainParallelSeqScanLabelVerbose(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	parallelRoot := optimizer.MaybeAddGather(&optimizer.SeqScan{Table: tbl}, parallelLabelTestSettings())
	if _, ok := parallelRoot.(*optimizer.Gather); !ok {
		t.Fatalf("expected a Gather at the root, got %T", parallelRoot)
	}
	verboseText := renderPlanTextOpts(parallelRoot, parser.ExplainOptions{Verbose: true})
	if !strings.Contains(verboseText, "Parallel Seq Scan on") {
		t.Errorf("EXPLAIN VERBOSE missing 'Parallel Seq Scan on ...':\n%s", verboseText)
	}
}

// TestExplainParallelSeqScanLabelAnalyze is the ANALYZE twin of the test
// above: the "Parallel " prefix must reach walkPlanAnalyzeFiltered too,
// since both text walkers funnel through the same describePlan/
// describePlanVerbose label function.
func TestExplainParallelSeqScanLabelAnalyze(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	parallelRoot := optimizer.MaybeAddGather(&optimizer.SeqScan{Table: tbl}, parallelLabelTestSettings())
	g, ok := parallelRoot.(*optimizer.Gather)
	if !ok {
		t.Fatalf("expected a Gather at the root, got %T", parallelRoot)
	}
	scan, ok := g.Child.(*optimizer.SeqScan)
	if !ok {
		t.Fatalf("expected a SeqScan directly under Gather, got %T", g.Child)
	}
	if !scan.Parallel {
		t.Fatal("scan under Gather should have been stamped Parallel")
	}

	var rows []Row
	walkPlanAnalyzeFiltered(parallelRoot, 0, &rows, parser.ExplainOptions{Analyze: true}, nil, nil, nil, nil, nil, nil, nil, nil, 0, &subPlanReg{rel: newExplainNames(parallelRoot), cte: collectCTEHoist(parallelRoot)})
	var lines []string
	for _, r := range rows {
		if len(r) > 0 && r[0].Kind == KindString {
			lines = append(lines, r[0].StringValue())
		}
	}
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "Parallel Seq Scan on t") {
		t.Errorf("EXPLAIN ANALYZE text missing 'Parallel Seq Scan on t':\n%s", text)
	}
}
