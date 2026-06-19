package parser_test

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPartitionRangeBoundKeywordDistinctFromStringConst pins DU-002 slice 261:
// the bare MINVALUE / MAXVALUE edge keywords in a RANGE partition bound must
// parse to a dedicated *PartitionRangeBoundKeyword node, NOT to
// StringConst{Value:"MINVALUE"}. Before slice 261 both the unbounded keyword and
// the quoted text literal 'MINVALUE' collapsed to the same StringConst, so a
// real text bound value 'MINVALUE' was indistinguishable from -∞ at the routing
// and dump layers. This test guards that the two now parse to distinct nodes.
func TestPartitionRangeBoundKeywordDistinctFromStringConst(t *testing.T) {
	t.Run("bare MINVALUE/MAXVALUE -> PartitionRangeBoundKeyword", func(t *testing.T) {
		stmts, err := parser.Parse(
			"CREATE TABLE c PARTITION OF p FOR VALUES FROM (MINVALUE, 1) TO (10, MAXVALUE)")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		ct, ok := stmts[0].(*parser.CreateTableStmt)
		if !ok || ct.PartitionOf == nil {
			t.Fatalf("expected CreateTableStmt with PartitionOf, got %T", stmts[0])
		}
		from := ct.PartitionOf.FromValues
		to := ct.PartitionOf.ToValues
		if len(from) != 2 || len(to) != 2 {
			t.Fatalf("expected 2 FROM and 2 TO values, got %d/%d", len(from), len(to))
		}

		kw, ok := from[0].(*parser.PartitionRangeBoundKeyword)
		if !ok {
			t.Fatalf("FROM[0] = %T; want *PartitionRangeBoundKeyword", from[0])
		}
		if kw.IsMax {
			t.Errorf("FROM[0] MINVALUE: IsMax = true; want false (-∞)")
		}
		if kw.Keyword() != "MINVALUE" {
			t.Errorf("FROM[0].Keyword() = %q; want MINVALUE", kw.Keyword())
		}
		// FROM[1] is the integer 1 — a concrete value, not a keyword.
		if _, isKw := from[1].(*parser.PartitionRangeBoundKeyword); isKw {
			t.Errorf("FROM[1] (integer 1) parsed as PartitionRangeBoundKeyword")
		}

		kwMax, ok := to[1].(*parser.PartitionRangeBoundKeyword)
		if !ok {
			t.Fatalf("TO[1] = %T; want *PartitionRangeBoundKeyword", to[1])
		}
		if !kwMax.IsMax {
			t.Errorf("TO[1] MAXVALUE: IsMax = false; want true (+∞)")
		}
		if kwMax.Keyword() != "MAXVALUE" {
			t.Errorf("TO[1].Keyword() = %q; want MAXVALUE", kwMax.Keyword())
		}
	})

	t.Run("quoted 'MINVALUE' -> StringConst, not the keyword node", func(t *testing.T) {
		stmts, err := parser.Parse(
			"CREATE TABLE c PARTITION OF p FOR VALUES FROM ('MINVALUE') TO ('MAXVALUE')")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		ct := stmts[0].(*parser.CreateTableStmt)
		from := ct.PartitionOf.FromValues
		if _, isKw := from[0].(*parser.PartitionRangeBoundKeyword); isKw {
			t.Fatalf("quoted 'MINVALUE' parsed as the unbounded keyword node; "+
				"text value collides with -∞ sentinel")
		}
		sc, ok := from[0].(*parser.StringConst)
		if !ok {
			t.Fatalf("FROM[0] = %T; want *StringConst", from[0])
		}
		if sc.Value != "MINVALUE" {
			t.Errorf("FROM[0].Value = %q; want MINVALUE", sc.Value)
		}
	})
}
