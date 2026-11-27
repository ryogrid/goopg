package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// RowFilterColumn is one column of the relation a publication row filter
// is evaluated over.
type RowFilterColumn struct {
	Name string
	Type catalog.Type
}

// EvalPublicationRowFilter is pgoutput_row_filter_exec_expr: it evaluates
// the OR of a relation's publication row filters (pg_publication_rel.prqual
// texts) over one row and reports whether the change is published. A NULL
// result is false, as in PG. values carries each column's text form (nil for
// NULL) in cols order — the row is bound through each type's input
// function, the same text round trip a subscriber's apply makes.
//
// A virtual generated column is not stored, so its value is computed from
// its generation expression over the stored columns, as PG expands it
// (expand_generated_columns_in_expr) before evaluating the filter.
//
// relOID, when non-zero, names the relation so the generation expressions
// can be read from the catalog. M0149-0059c.
func EvalPublicationRowFilter(cat catalog.Catalog, dbOid, relOID uint32, cols []RowFilterColumn, quals []string, values []*string) (bool, error) {
	if len(quals) == 0 {
		return true, nil
	}
	virtual := map[string]string{}
	if im, ok := cat.(*catalog.InMemory); ok && relOID != 0 {
		if tbl, ok := im.LookupTableByOID(relOID); ok && tbl != nil {
			for _, c := range tbl.Columns {
				if c.GeneratedVirtual && c.GeneratedExpr != "" {
					virtual[c.Name] = c.GeneratedExpr
				}
			}
		}
	}
	names := make([]string, len(cols))
	vals := make([]string, len(cols))
	outer := make([]string, len(cols))
	for i, c := range cols {
		typeName := c.Type.Name
		if c.Type.IsArray {
			typeName += "[]"
		}
		names[i] = pgQuoteIdent(c.Name)
		if i < len(values) && values[i] != nil {
			vals[i] = pgQuoteLiteral(*values[i]) + "::" + typeName
		} else {
			vals[i] = "NULL::" + typeName
		}
		if g, ok := virtual[c.Name]; ok {
			outer[i] = "(" + g + ")::" + typeName + " AS " + names[i]
		} else {
			outer[i] = names[i]
		}
	}
	ors := make([]string, len(quals))
	for i, q := range quals {
		ors[i] = "(" + q + ")"
	}
	sql := "SELECT (" + strings.Join(ors, " OR ") + ")::bool FROM (SELECT " + strings.Join(outer, ", ") +
		" FROM (VALUES (" + strings.Join(vals, ", ") + ")) AS _rfv(" + strings.Join(names, ", ") + ")) AS _rf"
	stmts, err := parser.Parse(sql)
	if err != nil {
		return false, fmt.Errorf("row filter: %w", err)
	}
	if len(stmts) != 1 {
		return false, fmt.Errorf("row filter: %d statements", len(stmts))
	}
	ctx := NewContext()
	ctx.Catalog = cat
	ctx.CurrentDatabaseOid = dbOid
	plan, err := optimizer.Plan(stmts[0], ctxPlanCatalog(ctx))
	if err != nil {
		return false, fmt.Errorf("row filter: %w", err)
	}
	op, err := Build(plan)
	if err != nil {
		return false, fmt.Errorf("row filter: %w", err)
	}
	if err := op.Open(ctx); err != nil {
		op.Close()
		return false, fmt.Errorf("row filter: %w", err)
	}
	slot, err := op.Next()
	var result Datum
	hasResult := false
	if err == nil && slot != nil {
		if sr := slotRow(slot); len(sr) > 0 {
			result, hasResult = sr[0], true
		}
	}
	op.Close()
	if err != nil {
		return false, fmt.Errorf("row filter: %w", err)
	}
	if !hasResult || result.IsNull() || result.Kind != KindBool {
		return false, nil
	}
	return result.BoolValue(), nil
}
