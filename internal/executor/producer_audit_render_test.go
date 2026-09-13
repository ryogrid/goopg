package executor

// R118 (TEMPORARY — removed with the diagnostic before REPORT.md): the
// frozen census must agree with both renderer-visible Join occurrence
// sequences. A real EXPLAIN is planned with the diagnostic on; the TEXT
// Join label lines and the JSON "Node Type" join entries must list the
// same joins in the same pre-order as the frozen report rows.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

func renderAuditTEXT(t *testing.T, ex *optimizer.Explain) []string {
	t.Helper()
	var b strings.Builder
	var rows []Row
	walkPlan(&b, ex.Child, 0, &rows, parser.ExplainOptions{})
	var joins []string
	for _, r := range rows {
		s := ""
		for _, d := range []Datum{slotRowForTest(r)} {
			s += d.StringValue()
		}
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "->"))
		if strings.Contains(trimmed, "Join") || strings.Contains(trimmed, "Nested Loop") {
			joins = append(joins, trimmed)
		}
	}
	return joins
}

func slotRowForTest(r Row) Datum {
	if len(r) == 0 {
		return Datum{}
	}
	return r[0]
}

func renderAuditJSON(t *testing.T, ex *optimizer.Explain) []string {
	t.Helper()
	m := planToJSON(ex.Child, parser.ExplainOptions{})
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var joins []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if nt, ok := x["Node Type"].(string); ok {
				if strings.Contains(nt, "Join") || strings.Contains(nt, "Nested Loop") {
					joins = append(joins, nt)
				}
			}
			for _, key := range []string{"Plans", "Plan"} {
				if kid, ok := x[key]; ok {
					walk(kid)
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(decoded)
	return joins
}

func TestProducerAuditRendererAgreement(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()
	t.Setenv("GOOPG_Q96_PRODUCER_AUDIT", "1")
	stmts, err := parser.Parse("EXPLAIN SELECT f.fid FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, optimizer.DefaultPlannerSettings())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	ex, ok := node.(*optimizer.Explain)
	if !ok {
		t.Fatalf("top is %T", node)
	}
	if ex.ProducerReport == nil {
		t.Fatal("diagnostic-on EXPLAIN must attach a frozen report")
	}
	textJoins := renderAuditTEXT(t, ex)
	jsonJoins := renderAuditJSON(t, ex)
	if len(textJoins) == 0 {
		t.Fatal("TEXT rendered no joins; the comparison is vacuous")
	}
	if len(jsonJoins) != len(textJoins) {
		t.Fatalf("JSON joins %d != TEXT joins %d", len(jsonJoins), len(textJoins))
	}
	// Frozen census rows cover every renderer-visible Join occurrence;
	// lateral/out-of-population rows cannot appear here (fixture has none),
	// and every census row must be one-to-one on this shape.
	n := 0
	for _, r := range ex.ProducerReport.Rows {
		if r.Mapping != "one-to-one" {
			t.Fatalf("row %d maps as %q, want one-to-one", r.Ordinal, r.Mapping)
		}
		n++
	}
	if n != len(textJoins) {
		t.Fatalf("census joins %d != rendered joins %d", n, len(textJoins))
	}
}
