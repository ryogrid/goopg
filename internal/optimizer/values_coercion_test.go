package optimizer

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func valuesColumn(cells ...Expr) [][]Expr {
	rows := make([][]Expr, len(cells))
	for i, c := range cells {
		rows[i] = []Expr{c}
	}
	return rows
}

// TestCoerceValuesColumn pins transformValuesClause: every cell takes the
// column's common type, whichever row supplied it.
func TestCoerceValuesColumn(t *testing.T) {
	numeric := &CastExpr{Operand: &StringConst{Value: "2.5"}, TargetType: "numeric", SourceType: "unknown"}
	rows := valuesColumn(&StringConst{Value: "4.2e9"}, numeric)
	typ, err := coerceValuesColumn(rows, 0, resolveValuesColumnType(rows, 0))
	if err != nil || typ.Name != "numeric" {
		t.Fatalf("type %q, error %v", typ.Name, err)
	}
	if c, ok := rows[0][0].(*CastExpr); !ok || c.TargetType != "numeric" {
		t.Errorf("literal cell is %T, want a cast to numeric", rows[0][0])
	}

	date := &TypedStringLit{Type: "date", Value: "2020-01-01"}
	stamp := &TypedStringLit{Type: "timestamp", Value: "2020-01-01 10:00:00"}
	rows = valuesColumn(date, stamp)
	typ, err = coerceValuesColumn(rows, 0, resolveValuesColumnType(rows, 0))
	if err != nil || typ.Name != "timestamp" {
		t.Fatalf("type %q, error %v", typ.Name, err)
	}
	if c, ok := rows[0][0].(*CastExpr); !ok || c.TargetType != "timestamp" {
		t.Errorf("date cell is %T, want a cast to timestamp", rows[0][0])
	}

	rows = valuesColumn(&StringConst{Value: "2020-01-01"}, date)
	if _, err = coerceValuesColumn(rows, 0, catalog.Type{Name: "date"}); err != nil {
		t.Fatal(err)
	}
	if lit, ok := rows[0][0].(*TypedStringLit); !ok || lit.Type != "date" {
		t.Errorf("literal cell is %T, want a date literal", rows[0][0])
	}
}

func TestCoerceValuesColumnRejectsMixedCategories(t *testing.T) {
	rows := valuesColumn(&TypedStringLit{Type: "int4", Value: "1"}, &TypedStringLit{Type: "bool", Value: "t"})
	_, err := coerceValuesColumn(rows, 0, resolveValuesColumnType(rows, 0))
	var pe *PlanError
	if !errors.As(err, &pe) || pe.Code != "42804" || pe.Message != "VALUES types integer and boolean cannot be matched" {
		t.Errorf("got %v", err)
	}
}
