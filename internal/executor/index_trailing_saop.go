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

// trailingKeyCheck tests one key column of an index entry against a qual
// (built over Row{that column's value}), decoding only the columns up to it
// from the key. It serves IndexScan.TrailingSAOP (column 1) and a skip scan's
// range on a later column (M0148-0001c3a4 (j)).
type trailingKeyCheck struct {
	ctx  *Context
	pred optimizer.Expr // over Row{column pos}
	row  Row
	pos  int

	desc  *nbtree.PGIndexKeyDesc
	cols  []*catalog.Column
	imgs  [][]byte
	nulls []bool
	vals  []Datum
	blob  []catalog.Column // columns 0..pos (blob key format)
	style array.OutputStyle
}

// newTrailingKeyCheck is the check for IndexScan.TrailingSAOP: the array (and
// its column quals) on index column 1.
func newTrailingKeyCheck(ctx *Context, tbl *catalog.Table, idx *catalog.Index, ts *optimizer.TrailingSAOP) (*trailingKeyCheck, error) {
	if len(idx.Columns) < 2 {
		return nil, fmt.Errorf("trailing ScalarArrayOp on single-column index %q", idx.Name)
	}
	return newIndexColumnCheck(ctx, tbl, idx, 1, func(ref *optimizer.ColumnRef) optimizer.Expr { return trailingSAOPPred(ref, ts) })
}

// newIndexColumnCheck builds a check of index column pos against the qual
// build returns over a reference to it. The planner files such quals only
// over decodable key columns (interval is refused), so an undecodable one
// here is a planner bug.
func newIndexColumnCheck(ctx *Context, tbl *catalog.Table, idx *catalog.Index, pos int,
	build func(ref *optimizer.ColumnRef) optimizer.Expr) (*trailingKeyCheck, error) {
	if pos < 0 || pos >= len(idx.Columns) {
		return nil, fmt.Errorf("index column %d out of range on index %q", pos, idx.Name)
	}
	blob := make([]catalog.Column, 0, pos+1)
	for i := 0; i <= pos; i++ {
		col, ok := ctx.Catalog.LookupColumn(tbl, idx.Columns[i])
		if !ok {
			return nil, fmt.Errorf("index column %q not on the table", idx.Columns[i])
		}
		if !indexKeyColumnIsDecodable(*col) {
			return nil, fmt.Errorf("key check over an undecodable key column %q of index %q", col.Name, idx.Name)
		}
		blob = append(blob, *col)
	}
	c := &trailingKeyCheck{ctx: ctx, row: make(Row, 1), pos: pos, style: arrayOutputStyle(ctx)}
	c.pred = build(&optimizer.ColumnRef{Index: 0, Name: blob[pos].Name, Type: blob[pos].Type})
	if desc := ctx.pgIndexKeyDesc(idx); desc != nil {
		c.desc, c.cols = desc, pgIndexKeyColumns(idx)
		n := desc.NKeyAtts()
		c.imgs, c.nulls, c.vals = make([][]byte, n), make([]bool, n), make([]Datum, n)
		return c, nil
	}
	c.blob = blob
	return c, nil
}

// admits reports whether the entry's key column passes the qual.
func (c *trailingKeyCheck) admits(key []byte) (bool, error) {
	if c.desc != nil {
		vals, err := pgIndexTupleKeyDatumsInto(c.desc, c.cols, key, c.imgs, c.nulls, c.vals)
		if err != nil {
			return false, err
		}
		c.row[0] = vals[c.pos]
	} else {
		off := 0
		for i := 0; i < c.pos; i++ {
			_, n, err := decodeIndexKeyColumnStyled(key[off:], c.blob[i], c.style)
			if err != nil {
				return false, err
			}
			off += n
		}
		d, _, err := decodeIndexKeyColumnStyled(key[off:], c.blob[c.pos], c.style)
		if err != nil {
			return false, err
		}
		c.row[0] = d
	}
	return predAdmits(c.ctx, c.pred, c.row)
}

// skipRangePred is `ref op key` for a skip scan's bounds on its range column
// (LowKey/HighKey with their strictness), ANDed.
func skipRangePred(ref *optimizer.ColumnRef, low, high optimizer.Expr, lowOp, highOp parser.OpCode) optimizer.Expr {
	var pred optimizer.Expr
	add := func(e optimizer.Expr) {
		if pred == nil {
			pred = e
		} else {
			pred = &optimizer.BinaryOp{Op: parser.OpAnd, Left: pred, Right: e}
		}
	}
	if low != nil {
		op := parser.OpGe
		if lowOp == parser.OpGt {
			op = parser.OpGt
		}
		add(&optimizer.BinaryOp{Op: op, Left: ref, Right: low})
	}
	if high != nil {
		op := parser.OpLe
		if highOp == parser.OpLt {
			op = parser.OpLt
		}
		add(&optimizer.BinaryOp{Op: op, Left: ref, Right: high})
	}
	return pred
}
