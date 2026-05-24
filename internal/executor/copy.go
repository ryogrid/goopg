package executor

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// IsBinaryFormat reports whether the COPY options select binary format.
// Recognises `FORMAT binary` (new-style) and bare `BINARY` keyword (legacy).
func IsBinaryFormat(opts []parser.CopyOption) bool {
	for _, o := range opts {
		if strings.EqualFold(o.Name, "format") && strings.EqualFold(o.Value, "binary") {
			return true
		}
		if strings.EqualFold(o.Name, "binary") && o.Bool {
			return true
		}
	}
	return false
}

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
// RunCopyTo drives a planner.Copy whose Direction is CopyTo.
// When the plan's options select binary format, it emits the 19-byte
// PGCOPY header, binary rows, and the 2-byte trailer; otherwise it
// emits text rows (one per call to emit). Returns the row count and
// whether binary format was selected (so the caller can set the
// wire-protocol format code accordingly).
func RunCopyTo(ctx *Context, plan *planner.Copy, emit func([]byte) error) (count int64, binary bool, err error) {
	if plan == nil || plan.Direction != planner.CopyTo {
		return 0, false, &ExecError{Code: "XX000", Message: "RunCopyTo: plan is nil or not CopyTo"}
	}
	if err := rejectFileEndpoint(plan); err != nil {
		return 0, false, err
	}

	binary = IsBinaryFormat(plan.Options)

	src, cols, projection, buildErr := buildCopySource(plan)
	if buildErr != nil {
		return 0, binary, buildErr
	}
	if openErr := src.Open(ctx); openErr != nil {
		_ = src.Close()
		return 0, binary, openErr
	}
	defer src.Close()

	if binary {
		hdr := CopyBinaryHeader()
		if err := emit(hdr); err != nil {
			return 0, true, err
		}
	}

	var buf []byte
	for {
		slot, nextErr := src.Next()
		if nextErr == EOF {
			break
		}
		if nextErr != nil {
			return count, binary, nextErr
		}
		row := slotRow(slot)
		if projection != nil {
			projected := make(Row, len(projection))
			for i, idx := range projection {
				projected[i] = row[idx]
			}
			row = projected
		}
		buf = buf[:0]
		var encErr error
		if binary {
			buf, encErr = AppendCopyBinaryRow(buf, row, cols)
		} else {
			buf, encErr = EncodeCopyTextRow(buf, row, cols)
		}
		if encErr != nil {
			return count, binary, encErr
		}
		// Hand a copy to emit so callers can keep references safely.
		line := make([]byte, len(buf))
		copy(line, buf)
		if err := emit(line); err != nil {
			return count, binary, err
		}
		count++
	}

	if binary {
		trailer := AppendCopyBinaryTrailer(nil)
		if err := emit(trailer); err != nil {
			return count, true, err
		}
	}
	return count, binary, nil
}

// CopyFromExecutor receives COPY TEXT or COPY BINARY data from the wire
// and writes it through the heap-write path.
//
// Text path: the wire layer splits CopyData payloads on '\n' and calls PushLine.
// Binary path: the wire layer accumulates CopyData payloads and calls PushBinaryData.
type CopyFromExecutor struct {
	ctx    *Context
	plan   *planner.Copy
	cols   []catalog.Column // table's full column list, in declared order
	rowsIn int64

	// binary path state
	binaryBuf        []byte
	binaryHeaderSeen bool
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

// PushBinaryData accepts a chunk of binary COPY data, accumulates it,
// and inserts all complete rows it contains. The header (19 bytes) must
// be present at the start of the first chunk; subsequent chunks continue
// the stream. Returns (done, error) where done=true means the trailer
// was found and no more rows should be expected.
func (c *CopyFromExecutor) PushBinaryData(chunk []byte) (done bool, err error) {
	c.binaryBuf = append(c.binaryBuf, chunk...)

	if !c.binaryHeaderSeen {
		n, parseErr := ParseCopyBinaryHeader(c.binaryBuf)
		if parseErr != nil {
			if len(c.binaryBuf) < 19 {
				return false, nil // need more data
			}
			return false, &ExecError{Code: "22P04", Pos: c.plan.Pos(), Message: fmt.Sprintf("COPY BINARY: %v", parseErr)}
		}
		c.binaryBuf = c.binaryBuf[n:]
		c.binaryHeaderSeen = true
	}

	listedCols := make([]catalog.Column, len(c.plan.ColumnIndex))
	for i, ord := range c.plan.ColumnIndex {
		listedCols[i] = c.cols[ord]
	}

	rows, trailerFound, consumed, parseErr := ParseCopyBinaryRows(c.binaryBuf, listedCols)
	if parseErr != nil {
		return false, &ExecError{Code: "22P04", Pos: c.plan.Pos(), Message: fmt.Sprintf("COPY BINARY: %v", parseErr)}
	}
	c.binaryBuf = c.binaryBuf[consumed:]

	for _, src := range rows {
		row := make(Row, len(c.cols))
		for i := range c.cols {
			row[i] = NullDatum
		}
		for srcIdx, tgtOrd := range c.plan.ColumnIndex {
			row[tgtOrd] = src[srcIdx]
		}
		rel := c.ctx.Catalog.RelFileNode(c.plan.Table)
		if writeErr := writeHeapRow(c.ctx, rel, c.cols, row); writeErr != nil {
			return false, writeErr
		}
		c.rowsIn++
	}
	return trailerFound, nil
}

// IsBinary reports whether this executor is in binary mode.
func (c *CopyFromExecutor) IsBinary() bool {
	return IsBinaryFormat(c.plan.Options)
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
		// Prefer the Copy node's resolved schema: data-modifying inner
		// plans (Insert/Update/Delete) report Output()==nil even with a
		// RETURNING clause, so the planner stashes the RETURNING schema
		// on the Copy node directly. SELECT inner plans carry the same
		// schema, sourced from their own Output().
		schema := plan.Output()
		if schema == nil {
			schema = plan.Query.Output()
		}
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

// RunCopyFromFile implements server-side COPY table FROM 'filepath'.
// It opens the file, reads tab-delimited text rows, and inserts them
// using the same CopyFromExecutor path as COPY FROM STDIN.
// The file must use PostgreSQL's COPY TEXT format (tab-separated, \N for NULL).
func RunCopyFromFile(ctx *Context, plan *planner.Copy) (int64, error) {
	if plan.Endpoint != planner.CopyEndpointFile || plan.Filename == "" {
		return 0, &ExecError{Code: "XX000", Message: "RunCopyFromFile: not a file endpoint"}
	}
	if plan.Table == nil {
		return 0, &ExecError{Code: "0A000", Pos: plan.Pos(), Message: "COPY FROM requires a target table"}
	}
	f, err := os.Open(plan.Filename)
	if err != nil {
		return 0, &ExecError{Code: "58P01", Pos: plan.Pos(),
			Message: fmt.Sprintf("could not open file \"%s\" for reading: %s", plan.Filename, err)}
	}
	defer f.Close()

	// Build a CopyFromExecutor directly (bypassing rejectFileEndpoint).
	fe := &CopyFromExecutor{
		ctx:  ctx,
		plan: plan,
		cols: plan.Table.Columns,
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.Equal(line, []byte(`\.`)) {
			break
		}
		if err := fe.PushLine(line); err != nil {
			return fe.rowsIn, err
		}
	}
	if err := scanner.Err(); err != nil {
		return fe.rowsIn, &ExecError{Code: "58030", Pos: plan.Pos(),
			Message: fmt.Sprintf("error reading COPY file: %v", err)}
	}
	return fe.rowsIn, nil
}
