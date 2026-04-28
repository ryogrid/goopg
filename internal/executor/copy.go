package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// RunCopyTo drives a planner.Copy whose Direction is CopyTo. For
// table-form, it builds a SeqScan over plan.Table; for query-form, it
// builds the inner Query subtree. Each visible row is rendered as a
// COPY TEXT line and passed to emit. RunCopyTo returns the number of
// rows emitted.
//
// emit owns the line bytes (no shared backing array) so callers can
// hand the slice straight to the wire-protocol writer without copying.
//
// The executor opens the source operator on the supplied ctx; the
// caller must have populated Pool/Catalog/TxnMgr/Tx/Snap when the
// source touches storage. Query-form expressions that don't touch
// storage (e.g. `COPY (SELECT 1) TO STDOUT`) work with a bare
// NewContext().
func RunCopyTo(ctx *Context, plan *planner.Copy, emit func([]byte) error) (int64, error) {
	if plan == nil || plan.Direction != planner.CopyTo {
		return 0, &ExecError{Code: "XX000", Message: "RunCopyTo: plan is nil or not CopyTo"}
	}
	if err := rejectFileEndpoint(plan); err != nil {
		return 0, err
	}

	src, cols, projection, err := buildCopySource(plan)
	if err != nil {
		return 0, err
	}
	if err := src.Open(ctx); err != nil {
		_ = src.Close()
		return 0, err
	}
	defer src.Close()

	var (
		count int64
		buf   []byte
	)
	for {
		row, err := src.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return count, err
		}
		if projection != nil {
			projected := make(Row, len(projection))
			for i, idx := range projection {
				projected[i] = row[idx]
			}
			row = projected
		}
		buf = buf[:0]
		buf, err = EncodeCopyTextRow(buf, row, cols)
		if err != nil {
			return count, err
		}
		// Hand a copy to emit so callers can keep references safely.
		line := make([]byte, len(buf))
		copy(line, buf)
		if err := emit(line); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// CopyFromExecutor receives COPY TEXT lines from the wire and writes
// them through the heap-write path. The wire layer is responsible for
// splitting incoming CopyData payloads on `\n` (respecting the
// COPY-TEXT escape rule that backslashed bytes inside a value never
// appear unescaped on the wire — there's no actual backslash-newline
// continuation in the line layer, only inside fields). Each line
// passes through DecodeCopyTextRow → reorder via ColumnIndex →
// writeHeapRow.
type CopyFromExecutor struct {
	ctx     *Context
	plan    *planner.Copy
	cols    []catalog.Column // table's full column list, in declared order
	rowsIn  int64
}

// NewCopyFromExecutor binds a CopyFromExecutor to ctx and plan.
// Returns an error when plan is wrong-shape, the endpoint is
// file/PROGRAM, or the storage handles are missing.
func NewCopyFromExecutor(ctx *Context, plan *planner.Copy) (*CopyFromExecutor, error) {
	if plan == nil || plan.Direction != planner.CopyFrom {
		return nil, &ExecError{Code: "XX000", Message: "NewCopyFromExecutor: plan is nil or not CopyFrom"}
	}
	if plan.Table == nil {
		return nil, &ExecError{Code: "0A000", Pos: plan.Pos(), Message: "COPY FROM requires a target table"}
	}
	if err := rejectFileEndpoint(plan); err != nil {
		return nil, err
	}
	if ctx.Pool == nil || ctx.Catalog == nil || ctx.TxnMgr == nil {
		return nil, &ExecError{Code: "XX000", Pos: plan.Pos(), Message: "COPY FROM requires storage handles in Context"}
	}
	return &CopyFromExecutor{
		ctx:  ctx,
		plan: plan,
		cols: plan.Table.Columns,
	}, nil
}

// PushLine decodes one COPY TEXT row and inserts it. line must not
// include a trailing newline.
func (c *CopyFromExecutor) PushLine(line []byte) error {
	// The decoder is keyed on the columns the user listed — len(plan.ColumnIndex)
	// fields per row, in that order. Then we scatter into the table's full
	// column slice, leaving unlisted columns as NULL.
	listedCols := make([]catalog.Column, len(c.plan.ColumnIndex))
	for i, ord := range c.plan.ColumnIndex {
		listedCols[i] = c.cols[ord]
	}
	src, err := DecodeCopyTextRow(line, listedCols)
	if err != nil {
		return &ExecError{Code: "22P04", Pos: c.plan.Pos(), Message: fmt.Sprintf("COPY: %v", err)}
	}
	row := make(Row, len(c.cols))
	for i := range c.cols {
		row[i] = NullDatum
	}
	for srcIdx, tgtOrd := range c.plan.ColumnIndex {
		row[tgtOrd] = src[srcIdx]
	}
	rel := c.ctx.Catalog.RelFileNode(c.plan.Table)
	if err := writeHeapRow(c.ctx, rel, c.cols, row); err != nil {
		return err
	}
	c.rowsIn++
	return nil
}

// RowsInserted reports how many rows have been successfully appended.
func (c *CopyFromExecutor) RowsInserted() int64 { return c.rowsIn }

// buildCopySource constructs the source Operator for a CopyTo plan,
// the columns the codec should encode against, and an optional
// projection (a slice of indices into the source row to pick + reorder
// when the user-supplied column list differs from the table's
// declared order). projection is nil when no reordering is needed.
func buildCopySource(plan *planner.Copy) (Operator, []catalog.Column, []int, error) {
	if plan.Query != nil {
		op, err := Build(plan.Query)
		if err != nil {
			return nil, nil, nil, err
		}
		schema := plan.Query.Output()
		cols := make([]catalog.Column, len(schema))
		for i, sc := range schema {
			cols[i] = catalog.Column{Name: sc.Name, Type: sc.Type, Ordinal: i}
		}
		return op, cols, nil, nil
	}
	if plan.Table == nil {
		return nil, nil, nil, &ExecError{Code: "XX000", Pos: plan.Pos(), Message: "COPY TO: plan has neither Table nor Query"}
	}
	scan := newSeqScanOp(&planner.SeqScan{Table: plan.Table})
	declared := plan.Table.Columns
	// projection only needed when ColumnIndex is non-default.
	projection := plan.ColumnIndex
	def := true
	if len(projection) != len(declared) {
		def = false
	} else {
		for i, ord := range projection {
			if ord != i {
				def = false
				break
			}
		}
	}
	if def {
		return scan, declared, nil, nil
	}
	cols := make([]catalog.Column, len(projection))
	for i, ord := range projection {
		cols[i] = declared[ord]
	}
	return scan, cols, projection, nil
}

func rejectFileEndpoint(plan *planner.Copy) error {
	switch plan.Endpoint {
	case planner.CopyEndpointFile:
		return &ExecError{Code: "0A000", Pos: plan.Pos(), Message: "COPY to/from file is not supported"}
	case planner.CopyEndpointProgram:
		return &ExecError{Code: "0A000", Pos: plan.Pos(), Message: "COPY to/from PROGRAM is not supported"}
	}
	return nil
}
