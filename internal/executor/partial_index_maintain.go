package executor

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0148-0001c3 — a partial index holds only the rows its predicate admits.
//
// ExecInsertIndexTuples (execIndexing.c) evaluates each partial index's
// predicate against the new tuple and skips the index when it is not
// satisfied (`if (!ExecQual(predicate, econtext)) continue;`); uniqueness and
// ON CONFLICT arbitration therefore apply only among admitted rows. goopg
// filtered rows only at CREATE INDEX time, so every later INSERT / UPDATE /
// COPY / MERGE filed an entry for every row: a partial UNIQUE index rejected
// `(1,-1),(1,-2)` under `ON up (a) WHERE b > 0`, and a scan that relied on
// the predicate returned rows outside it.

// indexPredicateAdmits reports whether row, laid out as cols, belongs in idx.
// A plain index admits everything. A predicate that cannot be resolved admits
// too — the index then holds a superset, which every reader that cannot prove
// the predicate already treats as unusable — and so does one that errors.
func indexPredicateAdmits(ctx *Context, idx *catalog.Index, tbl *catalog.Table, cols []catalog.Column, row Row) bool {
	if idx == nil || !idx.HasPredicate || tbl == nil {
		return true
	}
	pred, err := optimizer.ResolveIndexPredicateOf(idx, tbl)
	if err != nil || pred == nil {
		return true
	}
	pv, err := evalExpr(pred, tableLayoutRow(tbl, cols, row), ctx)
	if err != nil {
		return true
	}
	return !pv.IsNull() && pv.Kind == KindBool && pv.BoolValue()
}

// tableLayoutRow re-lays row (one value per cols entry) out in tbl.Columns
// order, which is the layout ResolveIndexPredicate's column references index.
// A column cols does not carry is NULL.
func tableLayoutRow(tbl *catalog.Table, cols []catalog.Column, row Row) Row {
	same := len(cols) == len(tbl.Columns)
	for i := 0; same && i < len(cols); i++ {
		same = cols[i].Name == tbl.Columns[i].Name
	}
	if same && len(row) >= len(tbl.Columns) {
		return row
	}
	out := make(Row, len(tbl.Columns))
	for i, tc := range tbl.Columns {
		out[i] = NullDatum
		for j, c := range cols {
			if j < len(row) && c.Name == tc.Name {
				out[i] = row[j]
				break
			}
		}
	}
	return out
}

// indexPredicateColumns lists the columns idx's predicate reads — part of
// the index's attributes for HOT purposes (RelationGetIndexAttrBitmap adds
// the predicate's Vars), since changing one can move the row into or out of
// the index.
func indexPredicateColumns(idx *catalog.Index, tbl *catalog.Table) []string {
	if idx == nil || !idx.HasPredicate || tbl == nil {
		return nil
	}
	pred, err := optimizer.ResolveIndexPredicateOf(idx, tbl)
	if err != nil || pred == nil {
		return nil
	}
	var out []string
	optimizer.WalkExprTree(pred, func(e optimizer.Expr) {
		if cr, ok := e.(*optimizer.ColumnRef); ok {
			out = append(out, cr.Name)
		}
	})
	return out
}
