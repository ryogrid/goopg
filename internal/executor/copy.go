package executor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/utils/mb"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
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
func RunCopyTo(ctx *Context, plan *optimizer.Copy, emit func([]byte) error) (count int64, binary bool, err error) {
	if plan == nil || plan.Direction != optimizer.CopyTo {
		return 0, false, &ExecError{Code: "XX000", Message: "RunCopyTo: plan is nil or not CopyTo"}
	}
	if err := rejectFileEndpoint(plan); err != nil {
		return 0, false, err
	}
	return runCopyToCore(ctx, plan, emit)
}

// RunCopyToFile implements server-side `COPY ... TO 'filepath'`: it opens
// the file for writing and drives the same row-encoding path RunCopyTo
// uses for STDOUT, so the two endpoints stay byte-for-byte identical
// (aside from the wire CopyData framing, which a real file has no need
// of). Mirrors RunCopyFromFile's counterpart on the read side. M0134-0107.
func RunCopyToFile(ctx *Context, plan *optimizer.Copy) (int64, error) {
	if plan == nil || plan.Direction != optimizer.CopyTo {
		return 0, &ExecError{Code: "XX000", Message: "RunCopyToFile: plan is nil or not CopyTo"}
	}
	if plan.Endpoint != optimizer.CopyEndpointFile || plan.Filename == "" {
		return 0, &ExecError{Code: "XX000", Message: "RunCopyToFile: not a file endpoint"}
	}
	f, err := os.Create(plan.Filename)
	if err != nil {
		return 0, &ExecError{Code: "58P01", Pos: plan.Pos(),
			Message: fmt.Sprintf("could not open file \"%s\" for writing: %s", plan.Filename, err)}
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	count, _, err := runCopyToCore(ctx, plan, func(line []byte) error {
		_, werr := w.Write(line)
		return werr
	})
	if err != nil {
		return count, err
	}
	if ferr := w.Flush(); ferr != nil {
		return count, &ExecError{Code: "58030", Pos: plan.Pos(), Message: fmt.Sprintf("could not write to file \"%s\": %s", plan.Filename, ferr)}
	}
	return count, nil
}

func runCopyToCore(ctx *Context, plan *optimizer.Copy, emit func([]byte) error) (count int64, binary bool, err error) {
	binary = IsBinaryFormat(plan.Options)
	var format copyToFormat
	if !binary {
		format = copyToFormatFromOptions(plan.Options)
	}
	dateStyle, dateOrder := "ISO", "MDY"
	// timeZone feeds the timestamptz columns only (config.FormatTimestampTZ);
	// "" means the boot default, UTC.
	timeZone := ""
	// byteaMode feeds the bytea columns (M0134-0001 S12); "hex" is the boot
	// default, matching dispatch.go's appendTypedCellText (SELECT wire) so
	// COPY TO and SELECT agree on the same `bytea_output` GUC.
	byteaMode := "hex"
	if ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("datestyle"); ok {
			dateStyle, dateOrder = misc.ParseDateStyleValue(v)
		}
		if v, ok := ctx.GetSetting("timezone"); ok {
			timeZone = v
		}
		if v, ok := ctx.GetSetting("bytea_output"); ok {
			byteaMode = v
		}
	}
	// M0119-0006 (68th slice): the catalog + search-path-qualify flag the reg*
	// renderer needs. qualify is the negation of RegObjectSchemaVisible — a
	// regtype name is schema-qualified only when "public" is NOT on the
	// session's effective search_path, the same rule the SELECT path applies
	// via internal/server's publicSchemaVisible. argVisible is the regprocedure
	// ARGLIST's per-arg-type visibility predicate (73rd slice, deferral row
	// 1342) — RegObjectSchemaVisible per schema, exactly what the SELECT wire
	// path passes. A nil ctx keeps all nil/false (numeric rendering), matching
	// the pre-68th callers.
	var cat catalog.Catalog
	qualify := false
	var argVisible func(s string) bool
	if ctx != nil {
		cat = ctx.Catalog
		qualify = !RegObjectSchemaVisible(ctx, "public")
		argVisible = func(s string) bool { return RegObjectSchemaVisible(ctx, s) }
	}

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
	} else if format.hasHeader() {
		line := format.appendHeader(nil, cols)
		if err := emit(line); err != nil {
			return 0, false, err
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
		switch {
		case binary:
			buf, encErr = AppendCopyBinaryRow(buf, row, cols)
		case format.csv:
			buf, encErr = EncodeCopyCsvRow(buf, row, cols, format, dateStyle, dateOrder, timeZone, byteaMode, cat, qualify, argVisible)
		default:
			buf, encErr = EncodeCopyTextRow(buf, row, cols, dateStyle, dateOrder, timeZone, byteaMode, cat, qualify, argVisible)
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
	plan   *optimizer.Copy
	cols   []catalog.Column // table's full column list, in declared order
	rowsIn int64
	// lineNo is the 1-based physical line counter PG's CONTEXT message
	// reports ("COPY tbl, line N", copyfromparse.c CopyFromErrorCallback).
	// Incremented once per PushLine call, matching cur_lineno's per-line
	// bump — HEADER's discarded first line counts too, same as upstream.
	lineNo int64
	// format carries the text/CSV knobs (delimiter, quote, escape, NULL
	// string, header) parsed from the option list. The same struct drives
	// COPY TO; input and output MUST read the option list identically or a
	// session cannot COPY back in what it just COPYed out.
	format copyToFormat

	// headerPending is set while the first line of the stream is still the
	// column-name header that HEADER asked us to discard.
	headerPending bool
	// csvPartial holds a CSV record whose quoted field was cut in half by
	// the wire layer's split on '\n'.
	csvPartial []byte

	// binary path state
	binaryBuf        []byte
	binaryHeaderSeen bool

	// srcEnc is the resolved PG encoding ID that incoming raw lines must be
	// converted FROM (into the UTF8 server encoding) before decoding, or -1
	// when no conversion is needed (UTF8 source, or the encoding could not
	// be resolved — matching PG's "no proc needed" fast paths in
	// pg_do_encoding_conversion). Resolved once per statement in
	// newCopyFromExecutor from the COPY ENCODING option, falling back to
	// the session's client_encoding GUC — mirrors PG's ProcessCopyOptions /
	// BeginCopyFrom precedence (options.c: an explicit ENCODING option wins
	// over client_encoding for that one COPY). M0134-0107.
	srcEnc int32

	// missing[i]=true when column i is absent from the COPY column list
	// (PG copyfrom.c defmap: only columns NOT in the list get a default —
	// a column present in the list but holding an explicit NULL input is
	// not "missing"). Computed once per statement since plan.ColumnIndex
	// is immutable across rows. M0134-0005l.
	missing []bool
	// needsConstraints is the fast-path guard: true only when the target
	// table actually has DEFAULTs, NOT NULL columns, CHECK constraints, or
	// domain-typed columns to enforce. COPY is the bulk-load path, so a
	// constraint-free table (the common case for staging loads) skips the
	// whole default/constraint sequence below rather than paying a
	// per-row cost for work that can never fire. M0134-0005l.
	needsConstraints bool
}

// NewCopyFromExecutor binds a CopyFromExecutor to ctx and plan.
// Returns an error when plan is wrong-shape, the endpoint is
// file/PROGRAM, or the storage handles are missing.
func NewCopyFromExecutor(ctx *Context, plan *optimizer.Copy) (*CopyFromExecutor, error) {
	if plan == nil || plan.Direction != optimizer.CopyFrom {
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
	return newCopyFromExecutor(ctx, plan), nil
}

// newCopyFromExecutor builds the executor without the endpoint/handle
// checks, so the file endpoint (RunCopyFromFile) and the STDIN endpoint
// share one option-interpretation site. They previously diverged by
// construction: each hand-built the struct and read only the NULL option.
// resolveCopyFromEncoding returns the PG encoding ID that COPY FROM must
// convert incoming bytes from, or -1 when the source is already UTF8 (the
// only server encoding goopg supports) and no conversion is needed. An
// explicit COPY ... ENCODING 'name' option takes precedence over the
// session's client_encoding GUC, matching PG's BeginCopyFrom (copyfromparse.c
// consults cstate->file_encoding, which ProcessCopyOptions seeds from the
// ENCODING DefElem when present, else pg_get_client_encoding()).
func resolveCopyFromEncoding(opts []parser.CopyOption, getSetting func(string) (string, bool)) int32 {
	name := ""
	for _, o := range opts {
		if strings.EqualFold(o.Name, "encoding") {
			name = o.Value
			break
		}
	}
	if name == "" && getSetting != nil {
		if v, ok := getSetting("client_encoding"); ok {
			name = v
		}
	}
	if name == "" {
		return -1
	}
	upper := strings.ToUpper(name)
	if upper == "UTF8" || upper == "UNICODE" {
		return -1
	}
	id := catalog.EncodingNameToID(upper)
	if id <= 0 { // -1 unknown, 0 SQL_ASCII (any byte string is valid — no conversion)
		return -1
	}
	return id
}

func newCopyFromExecutor(ctx *Context, plan *optimizer.Copy) *CopyFromExecutor {
	format := copyToFormatFromOptions(plan.Options)
	cols := plan.Table.Columns

	srcEnc := int32(-1)
	if ctx != nil {
		srcEnc = resolveCopyFromEncoding(plan.Options, ctx.GetSetting)
	}

	// missing[i]=true for every target column not in the COPY column list
	// (defaults to "all missing" for a bare `COPY tbl FROM ...` with no
	// column list, since plan.ColumnIndex then covers every column and
	// clears every entry). Computed once per statement — M0134-0005l.
	missing := make([]bool, len(cols))
	for i := range missing {
		missing[i] = true
	}
	for _, tgtOrd := range plan.ColumnIndex {
		if tgtOrd >= 0 && tgtOrd < len(missing) {
			missing[tgtOrd] = false
		}
	}

	// needsConstraints: the fast-path guard computed ONCE per COPY
	// statement (not per row) — a table with no DEFAULTs, no NOT NULL
	// columns, no CHECK constraints, and no domain-typed columns skips the
	// whole default/constraint sequence in insertSourceRow.
	needsConstraints := len(plan.Table.CheckConstraints) > 0
	for i, col := range cols {
		if col.NotNull || col.DeclaredTypeName != "" {
			needsConstraints = true
		}
		if missing[i] && col.DefaultExpr != nil {
			needsConstraints = true
		}
	}

	return &CopyFromExecutor{
		ctx:              ctx,
		plan:             plan,
		cols:             cols,
		format:           format,
		headerPending:    format.hasHeader(),
		missing:          missing,
		needsConstraints: needsConstraints,
		srcEnc:           srcEnc,
	}
}

// PushLine decodes one COPY TEXT or COPY CSV row and inserts it. line
// must not include a trailing newline. In CSV format a physical line is
// not necessarily a whole record (a quoted field may contain newlines),
// so PushLine can legitimately insert nothing and buffer instead; call
// Finish at end-of-stream to catch a record left unterminated.
func (c *CopyFromExecutor) PushLine(line []byte) error {
	c.lineNo++
	if c.headerPending {
		// HEADER on input discards the first line (copyfromparse.c
		// NextCopyFrom, cstate->cur_lineno == 1 arm). Applies to TEXT
		// as well as CSV upstream, so it is handled before the split.
		c.headerPending = false
		return nil
	}
	if c.srcEnc >= 0 {
		converted, err := mb.DoEncodingConversion(line, c.srcEnc, mb.PG_UTF8, mb.BuiltinLookup)
		if err != nil {
			return &ExecError{Code: "22021", Pos: c.plan.Pos(), Message: err.Error(), Context: c.copyContext()}
		}
		line = converted
	}
	if c.format.csv {
		return c.pushCsvLine(line)
	}
	src, err := DecodeCopyTextRow(line, c.listedColumns(), c.format.nullStr, timeZoneFromCtx(c.ctx))
	if err != nil {
		return &ExecError{Code: "22P04", Pos: c.plan.Pos(), Message: fmt.Sprintf("COPY: %v", err), Context: c.copyContext()}
	}
	return c.insertSourceRow(src)
}

// copyContext renders PG's CONTEXT line for a COPY FROM error —
// "COPY <table>, line <N>" (copyfromparse.c CopyFromErrorCallback, the
// no-column-name-known case; a column-specific variant exists upstream
// but nothing in this executor identifies which column failed yet).
func (c *CopyFromExecutor) copyContext() string {
	name := ""
	if c.plan != nil && c.plan.Table != nil {
		name = c.plan.Table.Name
	}
	return fmt.Sprintf("COPY %s, line %d", name, c.lineNo)
}

// pushCsvLine feeds one physical line into the CSV reader, re-joining a
// record whose quoted field the wire layer's split on '\n' cut in half.
// The embedded newline is restored as the '\n' that was removed; a '\r'
// that preceded it survives inside the quotes, which is what upstream
// does too (CopyReadLineText only folds CR/LF when NOT in a quoted
// field).
func (c *CopyFromExecutor) pushCsvLine(line []byte) error {
	if len(c.csvPartial) > 0 {
		c.csvPartial = append(c.csvPartial, '\n')
		c.csvPartial = append(c.csvPartial, line...)
		line = c.csvPartial
	}
	src, err := DecodeCopyCsvRow(trimCopyLineCR(line), c.listedColumns(), c.format, timeZoneFromCtx(c.ctx))
	if errors.Is(err, errCsvIncompleteRecord) {
		if len(c.csvPartial) == 0 {
			c.csvPartial = append(c.csvPartial, line...)
		}
		return nil
	}
	c.csvPartial = c.csvPartial[:0]
	if err != nil {
		return &ExecError{Code: "22P04", Pos: c.plan.Pos(), Message: fmt.Sprintf("%v", err)}
	}
	return c.insertSourceRow(src)
}

// Finish reports an end-of-stream condition the per-line path cannot see.
// Today that is only a CSV record still inside a quoted field, which
// upstream raises as `unterminated CSV quoted field`.
func (c *CopyFromExecutor) Finish() error {
	if len(c.csvPartial) > 0 {
		c.csvPartial = c.csvPartial[:0]
		return &ExecError{Code: "22P04", Pos: c.plan.Pos(), Message: "unterminated CSV quoted field"}
	}
	return nil
}

// InCsvQuotedField reports whether the reader is mid-record inside a
// quoted CSV field. The wire layer consults it before honouring the
// deprecated `\.` end-of-data marker: inside quotes that line is DATA,
// as upstream demonstrates by reporting `unterminated CSV quoted field`
// with the `\.` swallowed into the field.
func (c *CopyFromExecutor) InCsvQuotedField() bool { return len(c.csvPartial) > 0 }

// trimCopyLineCR drops the CR of a CRLF record terminator. Only the
// record's own terminator is affected — a CR inside a quoted field is
// followed by the restored '\n', not by end-of-line.
func trimCopyLineCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

// listedColumns returns the columns the user listed, in the order the
// input rows carry them.
func (c *CopyFromExecutor) listedColumns() []catalog.Column {
	listed := make([]catalog.Column, len(c.plan.ColumnIndex))
	for i, ord := range c.plan.ColumnIndex {
		listed[i] = c.cols[ord]
	}
	return listed
}

// insertSourceRow scatters a decoded input row into the table's full
// column slice (unlisted columns stay NULL) and writes it through the
// heap-write path.
func (c *CopyFromExecutor) insertSourceRow(src Row) error {
	row := make(Row, len(c.cols))
	for i := range c.cols {
		row[i] = NullDatum
	}
	for srcIdx, tgtOrd := range c.plan.ColumnIndex {
		row[tgtOrd] = src[srcIdx]
	}
	// M0119-0006 (68th slice): route reg* columns through the SAME
	// coerceRowForConstraintChecks the INSERT path uses, so a name field
	// resolves like regclassin/regrolein ("-" → OID 0, pure-digit → numeric OID,
	// miss → 42P01/42704/42883/42602) instead of encodeValuePG's numeric parse
	// on the KindString copyTextToDatum left behind. Only the reg* family is
	// admitted — every other column is already typed by copyTextToDatum, and
	// re-coercing it would risk drift. The error propagates unwrapped (PushLine
	// returns insertSourceRow's error as-is), so reg*in's own SQLSTATE reaches
	// the wire rather than the 22P04 the decode path wraps.
	if err := coerceRowForConstraintChecks(c.cols, row, func(i int) bool {
		return isRegIdentifierTypeName(c.cols[i].Type.Name)
	}, c.ctx, c.plan.Pos()); err != nil {
		return err
	}

	// M0134-0005l: apply the same default-filling and constraint sequence
	// insertOp.Next runs, so COPY FROM stops silently accepting rows PG
	// rejects and stops storing NULL where PG stores a default
	// (postgres/src/backend/commands/copyfrom.c ExecConstraints runs
	// unconditionally per row; defmap/defexprs fill omitted columns).
	// needsConstraints is computed once per statement (newCopyFromExecutor)
	// so a constraint-free table pays nothing extra per row.
	if c.needsConstraints {
		// DEFAULT filling for columns absent from the COPY column list. A
		// column present in the list but holding an explicit NULL is NOT
		// "missing" — c.missing only marks columns plan.ColumnIndex never
		// targets, matching PG's defmap.
		applyDefaultsForMissing(c.cols, row, c.missing, ctxSeqDBOid(c.ctx))

		// NOT NULL constraint enforcement.
		for i, col := range c.cols {
			if col.NotNull && i < len(row) && row[i].IsNull() {
				return &ExecError{
					Code:    "23502",
					Message: fmt.Sprintf("null value in column %q of relation %q violates not-null constraint", col.Name, c.plan.Table.Name),
					Detail:  formatRowForDetail(c.cols, row),
				}
			}
		}

		// CHECK constraint enforcement.
		if len(c.plan.Table.CheckConstraints) > 0 {
			if err := checkConstraints(c.ctx, c.plan.Table, row); err != nil {
				return err
			}
		}

		// Domain NOT NULL / CHECK constraint enforcement for domain-typed
		// columns.
		if err := checkDomainConstraintsForRow(c.ctx, c.cols, row); err != nil {
			return err
		}
	}

	rel := c.ctx.Catalog.RelFileNode(c.plan.Table)
	ptr, err := writeHeapRowReturning(c.ctx, rel, c.cols, row)
	if err != nil {
		return err
	}
	// M0097-0058: maintain btree indexes for COPY-loaded rows.
	// writeHeapRow only inserts the heap tuple; unique/primary-key
	// indexes must be updated separately, otherwise index scans
	// return zero rows for COPY-loaded data.
	maintainUniqueIndexesForInsert(c.ctx, c.plan.Table, c.cols, row, ptr)
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
		ptr, writeErr := writeHeapRowReturning(c.ctx, rel, c.cols, row)
		if writeErr != nil {
			return false, writeErr
		}
		maintainUniqueIndexesForInsert(c.ctx, c.plan.Table, c.cols, row, ptr)
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
func buildCopySource(plan *optimizer.Copy) (Operator, []catalog.Column, []int, error) {
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
	scan := newSeqScanOp(&optimizer.SeqScan{Table: plan.Table})
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

func rejectFileEndpoint(plan *optimizer.Copy) error {
	switch plan.Endpoint {
	case optimizer.CopyEndpointFile:
		return &ExecError{Code: "0A000", Pos: plan.Pos(), Message: "COPY to/from file is not supported"}
	case optimizer.CopyEndpointProgram:
		return &ExecError{Code: "0A000", Pos: plan.Pos(), Message: "COPY to/from PROGRAM is not supported"}
	}
	return nil
}

// RunCopyFromFile implements server-side COPY table FROM 'filepath'.
// It opens the file, reads tab-delimited text rows, and inserts them
// using the same CopyFromExecutor path as COPY FROM STDIN.
// The file must use PostgreSQL's COPY TEXT format (tab-separated, \N for NULL).
func RunCopyFromFile(ctx *Context, plan *optimizer.Copy) (int64, error) {
	if plan.Endpoint != optimizer.CopyEndpointFile || plan.Filename == "" {
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
	fe := newCopyFromExecutor(ctx, plan)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.Equal(line, []byte(`\.`)) && !fe.InCsvQuotedField() {
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
	if err := fe.Finish(); err != nil {
		return fe.rowsIn, err
	}
	return fe.rowsIn, nil
}
