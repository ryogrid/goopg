package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestExplainFormatXMLProducesWellFormedTree: `EXPLAIN (FORMAT XML)
// SELECT 1` returns a single row shaped like upstream's
// <explain>...<Query><Plan>...</Plan></Query></explain> tree.
func TestExplainFormatXMLProducesWellFormedTree(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT XML) SELECT 1")
	if len(lines) != 1 {
		t.Fatalf("FORMAT XML produced %d rows, want 1", len(lines))
	}
	out := lines[0]
	if !strings.HasPrefix(out, `<explain xmlns="http://www.postgresql.org/2009/explain">`) {
		t.Fatalf("missing <explain> root: %s", out)
	}
	if !strings.HasSuffix(out, "</explain>") {
		t.Fatalf("missing </explain> close: %s", out)
	}
	for _, tag := range []string{"<Query>", "<Plan>", "<Node-Type>", "</Plan>", "</Query>"} {
		if !strings.Contains(out, tag) {
			t.Errorf("output missing %q:\n%s", tag, out)
		}
	}
}

// TestExplainFormatXMLNestsJoinChildPlans: a two-table join's child
// nodes render as <Plans><Plan>...</Plan><Plan>...</Plan></Plans>,
// mirroring upstream's array-of-Plan grouping.
func TestExplainFormatXMLNestsJoinChildPlans(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE a (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE b (id int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT XML) SELECT * FROM a JOIN b ON a.id = b.id")
	out := lines[0]
	if !strings.Contains(out, "<Plans>") || !strings.Contains(out, "</Plans>") {
		t.Fatalf("join plan missing <Plans> wrapper:\n%s", out)
	}
	if strings.Count(out, "<Plan>") < 3 { // root Plan + two child Plans
		t.Errorf("expected at least 3 <Plan> tags for a join, got %d:\n%s", strings.Count(out, "<Plan>"), out)
	}
}

// TestExplainFormatXMLVerboseEmitsItemList: VERBOSE's Output column
// list renders as an unlabeled <Item> list (ExplainPropertyList).
func TestExplainFormatXMLVerboseEmitsItemList(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, val int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT XML, VERBOSE) SELECT id FROM t")
	out := lines[0]
	if !strings.Contains(out, "<Output>") || !strings.Contains(out, "<Item>") {
		t.Fatalf("verbose XML missing <Output>/<Item>:\n%s", out)
	}
}

// TestExplainFormatXMLAnalyzeIncludesActualStats: ANALYZE stats
// (Actual Rows / Actual Loops) surface as sibling properties inside
// the <Plan> group, matching the FORMAT JSON equivalent.
func TestExplainFormatXMLAnalyzeIncludesActualStats(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, FORMAT XML) SELECT 1")
	out := lines[0]
	for _, tag := range []string{"<Actual-Rows>", "<Actual-Loops>", "<Planning-Time>", "<Execution-Time>"} {
		if !strings.Contains(out, tag) {
			t.Errorf("ANALYZE XML missing %q:\n%s", tag, out)
		}
	}
}

// TestExplainFormatYAMLProducesExpectedShape: `EXPLAIN (FORMAT YAML)
// SELECT 1` returns the upstream "- Plan:\n    Node Type: ..." shape
// — an unlabeled top-level list item whose first property (Plan)
// stays inline with the dash.
func TestExplainFormatYAMLProducesExpectedShape(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT YAML) SELECT 1")
	if len(lines) != 1 {
		t.Fatalf("FORMAT YAML produced %d rows, want 1", len(lines))
	}
	out := lines[0]
	if !strings.HasPrefix(out, "- Plan:") {
		t.Fatalf("YAML output does not start with '- Plan:':\n%s", out)
	}
	if !strings.Contains(out, `Node Type: "`) {
		t.Errorf("YAML output missing quoted 'Node Type:' property:\n%s", out)
	}
}

// TestExplainFormatYAMLNestsJoinChildPlans mirrors the XML nesting
// test: a join's children render as an indented "Plans:" list with
// "- " markers, each a full nested object.
func TestExplainFormatYAMLNestsJoinChildPlans(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE a (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE b (id int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT YAML) SELECT * FROM a JOIN b ON a.id = b.id")
	out := lines[0]
	if !strings.Contains(out, "Plans:") {
		t.Fatalf("join YAML missing 'Plans:' key:\n%s", out)
	}
	if strings.Count(out, "Node Type:") < 3 {
		t.Errorf("expected at least 3 'Node Type:' entries for a join, got %d:\n%s", strings.Count(out, "Node Type:"), out)
	}
}

// TestExplainFormatYAMLAnalyzeIncludesActualStats pins the ANALYZE +
// FORMAT YAML cross-option interaction (bare numeric tokens, not
// quoted like string properties).
func TestExplainFormatYAMLAnalyzeIncludesActualStats(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, FORMAT YAML) SELECT 1")
	out := lines[0]
	for _, key := range []string{"Actual Rows:", "Actual Loops:", "Planning Time:", "Execution Time:"} {
		if !strings.Contains(out, key) {
			t.Errorf("ANALYZE YAML missing %q:\n%s", key, out)
		}
	}
}

// TestExplainFormatXMLTagNameSanitization pins ExplainXMLTag's
// character-replacement rule directly against the helper.
func TestExplainFormatXMLTagNameSanitization(t *testing.T) {
	cases := map[string]string{
		"Node Type":          "Node-Type",
		"Actual Startup Time": "Actual-Startup-Time",
		"I/O Read Time":      "I-O-Read-Time",
		"Plan_Rows-1.5":      "Plan_Rows-1.5",
	}
	for in, want := range cases {
		if got := xmlTagName(in); got != want {
			t.Errorf("xmlTagName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExplainUnsupportedFormatRejected pins the parser's rejection
// of a bogus FORMAT value now that XML/YAML are accepted alongside
// TEXT/JSON.
func TestExplainUnsupportedFormatRejected(t *testing.T) {
	if _, err := parser.Parse("EXPLAIN (FORMAT BOGUS) SELECT 1"); err == nil {
		t.Fatal("expected an error for FORMAT BOGUS, got nil")
	}
}
