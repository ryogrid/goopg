package executor

// M0148-0001c3a4 (i): the `= ANY` array an index scan carries on the second
// index column behind a leading-column range (IndexScan / IndexOnlyScan
// .TrailingSAOP) — PG's `((thousand < 2) AND (tenthous = ANY (…)))`. nbtree
// applies such a non-leading array key to every entry it reads, before the
// heap fetch; both scans check it the same way here.

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// trailingSAOPPred is `ref = ANY (Elems)` AND each column qual, over ref.
func trailingSAOPPred(ref *optimizer.ColumnRef, ts *optimizer.TrailingSAOP) optimizer.Expr {
	var pred optimizer.Expr = &optimizer.InExpr{Operand: ref, List: append([]optimizer.Expr(nil), ts.Elems...)}
	for _, q := range ts.ColQuals {
		var qe optimizer.Expr
		if q.Array != nil {
			qe = &optimizer.InExpr{Operand: ref, List: append([]optimizer.Expr(nil), q.Array...)}
		} else {
			qe = &optimizer.BinaryOp{Op: q.Op, Left: ref, Right: q.Key}
		}
		pred = &optimizer.BinaryOp{Op: parser.OpAnd, Left: pred, Right: qe}
	}
	return pred
}

// predAdmits evaluates a qual over row: true only for a non-NULL true.
func predAdmits(ctx *Context, pred optimizer.Expr, row Row) (bool, error) {
	v, err := evalExpr(pred, row, ctx)
	if err != nil {
		return false, err
	}
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue(), nil
}

// indexKeyDecodable reports whether every key column of idx decodes back to
// its value from an index entry (indexOnlyScanOp.indexKeyIsDecodable's rule:
// interval's lossy key span cannot, and the blob format renders only some
// types as their heap text).
func indexKeyDecodable(ctx *Context, tbl *catalog.Table, idx *catalog.Index) bool {
	if idx == nil || ctx == nil || ctx.Catalog == nil {
		return true
	}
	blobKey := ctx.pgIndexKeyDesc(idx) == nil
	for _, colName := range idx.Columns {
		col, ok := ctx.Catalog.LookupColumn(tbl, colName)
		if !ok {
			continue // the decode path reports this one itself
		}
		if !indexKeyColumnIsDecodable(*col) {
			return false
		}
		if blobKey && !indexKeyColumnRendersHeapText(*col) {
			return false
		}
	}
	return true
}

// trailingKeyCheck tests an index entry's second key column against the
// trailing array, decoding only that column's value from the key.
type trailingKeyCheck struct {
	ctx  *Context
	pred optimizer.Expr // over Row{second column}
	row  Row

	desc               *nbtree.PGIndexKeyDesc
	cols               []*catalog.Column
	imgs               [][]byte
	nulls              []bool
	vals               []Datum
	blobCol0, blobCol1 catalog.Column
	style              array.OutputStyle
}

// newTrailingKeyCheck builds the key-level check. The planner files a
// trailing array only over two decodable leading key columns
// (trailingSAOPOnRange refuses interval), so an undecodable one here is a
// planner bug.
func newTrailingKeyCheck(ctx *Context, tbl *catalog.Table, idx *catalog.Index, ts *optimizer.TrailingSAOP) (*trailingKeyCheck, error) {
	if len(idx.Columns) < 2 {
		return nil, fmt.Errorf("trailing ScalarArrayOp on single-column index %q", idx.Name)
	}
	col1, ok := ctx.Catalog.LookupColumn(tbl, idx.Columns[1])
	if !ok {
		return nil, fmt.Errorf("trailing ScalarArrayOp column %q not on the table", idx.Columns[1])
	}
	col0, ok := ctx.Catalog.LookupColumn(tbl, idx.Columns[0])
	if !ok {
		return nil, fmt.Errorf("index column %q not on the table", idx.Columns[0])
	}
	if !indexKeyColumnIsDecodable(*col0) || !indexKeyColumnIsDecodable(*col1) {
		return nil, fmt.Errorf("trailing ScalarArrayOp over undecodable key columns of index %q", idx.Name)
	}
	c := &trailingKeyCheck{ctx: ctx, row: make(Row, 1), style: arrayOutputStyle(ctx)}
	c.pred = trailingSAOPPred(&optimizer.ColumnRef{Index: 0, Name: col1.Name, Type: col1.Type}, ts)
	if desc := ctx.pgIndexKeyDesc(idx); desc != nil {
		c.desc, c.cols = desc, pgIndexKeyColumns(idx)
		n := desc.NKeyAtts()
		c.imgs, c.nulls, c.vals = make([][]byte, n), make([]bool, n), make([]Datum, n)
		return c, nil
	}
	c.blobCol0, c.blobCol1 = *col0, *col1
	return c, nil
}

// admits reports whether the entry's second key column passes the array.
func (c *trailingKeyCheck) admits(key []byte) (bool, error) {
	if c.desc != nil {
		vals, err := pgIndexTupleKeyDatumsInto(c.desc, c.cols, key, c.imgs, c.nulls, c.vals)
		if err != nil {
			return false, err
		}
		c.row[0] = vals[1]
	} else {
		_, n, err := decodeIndexKeyColumnStyled(key, c.blobCol0, c.style)
		if err != nil {
			return false, err
		}
		d, _, err := decodeIndexKeyColumnStyled(key[n:], c.blobCol1, c.style)
		if err != nil {
			return false, err
		}
		c.row[0] = d
	}
	return predAdmits(c.ctx, c.pred, c.row)
}
