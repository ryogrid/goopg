package planner

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// planCopy maps a parser.CopyStmt to a planner.Copy node.
//
// Table-form COPY (`COPY t [(cols)] {FROM|TO} ...`) resolves the
// table and column ordinals against the catalog; the analyzer's
// InsertStmt-style checks aren't reused because COPY's column list
// can also appear on the TO side, where it picks output columns.
//
// Query-form COPY (`COPY (SELECT ...) TO STDOUT`) plans the inner
// SELECT through the regular path so the executor can drive the
// resulting Project node — the wire layer's row formatter takes
// over from there. Query-form is only valid with TO; the parser
// already enforces that.
func planCopy(s *parser.CopyStmt, cat catalog.Catalog) (Node, error) {
	if s.Query != nil {
		// PostgreSQL's grammar accepts `SELECT … INTO …` inside
		// COPY (...) but DoCopy rejects it (copyto.c: CreateTableAsStmt
		// utility) with ERRCODE_FEATURE_NOT_SUPPORTED. Mirror that — the
		// parser flags the form and stops before resolving the target.
		// M0097-0024 (copyselect).
		if s.SelectInto {
			return nil, &PlanError{
				Pos:     s.Pos(),
				Code:    "0A000",
				Message: "COPY (SELECT INTO) is not supported",
			}
		}
		// Plan the inner SELECT just like a top-level one. The
		// resulting Node tree's Output() schema is what the wire
		// layer uses for the CopyOutResponse column list.
		inner, err := planSelect(s.Query, cat)
		if err != nil {
			return nil, err
		}
		return &Copy{
			pos:       s.Pos(),
			Direction: CopyTo,
			Query:     inner,
			Endpoint:  toPlannerEndpoint(s.Endpoint),
			Filename:  s.Filename,
			Options:   s.Options,
			schema:    inner.Output(),
		}, nil
	}

	if s.QueryDML != nil {
		// COPY (INSERT/UPDATE/DELETE … RETURNING) TO. Plan the
		// data-modifying statement through the normal entry point so
		// it runs (and commits, in the wire layer's COPY transaction);
		// the RETURNING rows are what gets streamed out. PostgreSQL
		// requires the RETURNING clause — without it there are no rows
		// to copy.
		inner, err := Plan(s.QueryDML, cat)
		if err != nil {
			return nil, err
		}
		schema := returningSchemaOf(inner)
		if schema == nil {
			// If the target table has rules registered, return a rule-specific
			// error matching PostgreSQL's DoCopy rule checks. M0097-0140.
			if msg := copyDMLRuleError(s.QueryDML, cat); msg != "" {
				return nil, &PlanError{
					Pos:     s.Pos(),
					Code:    "0A000",
					Message: msg,
				}
			}
			return nil, &PlanError{
				Pos:     s.Pos(),
				Code:    "0A000",
				Message: "COPY query must have a RETURNING clause",
			}
		}
		return &Copy{
			pos:       s.Pos(),
			Direction: CopyTo,
			Query:     inner,
			Endpoint:  toPlannerEndpoint(s.Endpoint),
			Filename:  s.Filename,
			Options:   s.Options,
			schema:    schema,
		}, nil
	}

	tbl, ok := cat.LookupTable(s.Table)
	if !ok {
		return nil, &PlanError{
			Pos:     s.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", s.Table.Name),
		}
	}

	// Reject COPY against a plain view. PostgreSQL checks the relation
	// kind before anything else: a view has no heap to stream from or
	// load into, so `COPY v TO` / `COPY v FROM` fail with
	// ERRCODE_WRONG_OBJECT_TYPE (42809). Materialised views are exempt —
	// they store rows in the heap like a table. The `COPY (SELECT ...)`
	// query form (handled above) is the supported way to dump a view.
	// M0097-0009 (copyselect).
	if tbl.View != nil && !tbl.IsMatView {
		if s.Direction == parser.CopyTo {
			return nil, &PlanError{
				Pos:     s.Pos(),
				Code:    "42809",
				Message: fmt.Sprintf("cannot copy from view %q", tbl.Name),
				Hint:    "Try the COPY (SELECT ...) TO variant.",
			}
		}
		return nil, &PlanError{
			Pos:     s.Pos(),
			Code:    "42809",
			Message: fmt.Sprintf("cannot copy to view %q", tbl.Name),
			Hint:    "To enable inserting into the view, provide an INSTEAD OF INSERT trigger.",
		}
	}

	// Resolve the column list. An empty list means "all declared
	// columns in declared order" — same as INSERT.
	var colIndex []int
	if len(s.Columns) == 0 {
		colIndex = make([]int, len(tbl.Columns))
		for i := range tbl.Columns {
			colIndex[i] = i
		}
	} else {
		colIndex = make([]int, 0, len(s.Columns))
		seen := make(map[int]struct{}, len(s.Columns))
		for _, name := range s.Columns {
			col, ok := cat.LookupColumn(tbl, name)
			if !ok {
				return nil, &PlanError{
					Pos:     s.Pos(),
					Code:    "42703",
					Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name),
				}
			}
			if _, dup := seen[col.Ordinal]; dup {
				return nil, &PlanError{
					Pos:     s.Pos(),
					Code:    "42701",
					Message: fmt.Sprintf("column %q specified more than once", name),
				}
			}
			seen[col.Ordinal] = struct{}{}
			colIndex = append(colIndex, col.Ordinal)
		}
	}

	if err := validateCopyOptions(s); err != nil {
		return nil, err
	}

	dir := CopyFrom
	if s.Direction == parser.CopyTo {
		dir = CopyTo
	}

	schema := make(Schema, len(colIndex))
	for i, ord := range colIndex {
		col := tbl.Columns[ord]
		schema[i] = SchemaColumn{Name: col.Name, Type: col.Type}
	}

	return &Copy{
		pos:         s.Pos(),
		Direction:   dir,
		Table:       tbl,
		ColumnIndex: colIndex,
		Endpoint:    toPlannerEndpoint(s.Endpoint),
		Filename:    s.Filename,
		Options:     s.Options,
		schema:      schema,
	}, nil
}

// returningSchemaOf returns the RETURNING output schema of a
// data-modifying plan node, or nil when the statement has no RETURNING
// clause. Used to gate COPY (DML) TO, which streams the RETURNING rows.
func returningSchemaOf(n Node) Schema {
	switch d := n.(type) {
	case *Insert:
		return d.ReturningSchema
	case *Update:
		return d.ReturningSchema
	case *Delete:
		return d.ReturningSchema
	case *Merge:
		return d.ReturningSchema
	}
	return nil
}

// copyDMLRuleError checks whether the DML statement's target table has any
// registered rule and returns the appropriate rule-specific COPY error message.
// Returns "" when no rule is registered, in which case the generic "must have
// a RETURNING clause" error applies. M0097-0140.
func copyDMLRuleError(dml parser.Stmt, cat catalog.Catalog) string {
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		return ""
	}
	// Extract target table name from INSERT/UPDATE/DELETE.
	var tableName string
	switch s := dml.(type) {
	case *parser.InsertStmt:
		tableName = s.Target.Name
	case *parser.UpdateStmt:
		tableName = s.Target.Name
	case *parser.DeleteStmt:
		tableName = s.Target.Name
	}
	if tableName == "" {
		return ""
	}
	kind := im.TableRuleKind(tableName)
	switch kind {
	case "DO ALSO":
		return "DO ALSO rules are not supported for COPY"
	case "DO INSTEAD NOTHING":
		return "DO INSTEAD NOTHING rules are not supported for COPY"
	case "conditional DO INSTEAD":
		return "conditional DO INSTEAD rules are not supported for COPY"
	case "multi-statement DO INSTEAD":
		return "multi-statement DO INSTEAD rules are not supported for COPY"
	case "utility":
		return "COPY query must not be a utility command"
	}
	return ""
}

func toPlannerEndpoint(e parser.CopyEndpoint) CopyEndpoint {
	switch e {
	case parser.CopyEndpointStdin:
		return CopyEndpointStdin
	case parser.CopyEndpointStdout:
		return CopyEndpointStdout
	case parser.CopyEndpointFile:
		return CopyEndpointFile
	case parser.CopyEndpointProgram:
		return CopyEndpointProgram
	}
	return CopyEndpointStdin
}

// validateCopyOptions mirrors PostgreSQL's ProcessCopyOptions
// (src/backend/commands/copy.c): a per-option pass that detects
// redundant options and validates each option's value, followed by a
// pass that rejects incompatible combinations (BINARY vs DELIMITER,
// FORCE_QUOTE outside CSV / on COPY FROM, …). Both passes run at plan
// time so the wire layer rejects the command *before* entering
// copy-in mode — otherwise a bad COPY FROM STDIN would slurp the
// following statements as data and desync the rest of the batch
// (copy2 regress). Messages and check ordering are kept byte-compatible
// with PG; only the SQLSTATE codes are carried for unit tests since
// psql does not print them in the regress diff. M0097-0024.
//
// Anything semantically deeper (FORCE_QUOTE references columns that
// aren't in the column list, encoding-name validity, …) is the
// executor's problem; pinning those needs the row formatter context.
func validateCopyOptions(s *parser.CopyStmt) error {
	isFrom := s.Direction != parser.CopyTo

	var (
		formatSpecified                                        bool
		csvMode, binary                                        bool
		haveDelim, haveNull, haveQuote, haveEscape, haveHeader bool
		haveEncoding, haveConvertSel                           bool
		haveForceQuote, haveForceNotNull, haveForceNull        bool
		freezeSpecified, freezeVal                             bool
		onErrorSpecified, onErrorIsStop                        bool
		logVerbositySpecified                                  bool
		rejectLimitSpecified                                   bool
	)

	conflict := func(o parser.CopyOption) error {
		// PG errorConflictingDefElem — caret on the redundant option.
		return &PlanError{Pos: o.Pos(), Code: "42601", Message: "conflicting or redundant options"}
	}

	for _, o := range s.Options {
		name := strings.ToLower(o.Name)
		switch name {
		case "format":
			if formatSpecified {
				return conflict(o)
			}
			formatSpecified = true
			switch strings.ToLower(o.Value) {
			case "text":
			case "csv":
				csvMode = true
			case "binary":
				binary = true
			case "":
				return &PlanError{Pos: o.Pos(), Code: "42601", Message: "FORMAT requires a value"}
			default:
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: fmt.Sprintf("COPY format \"%s\" not recognized", o.Value)}
			}
		case "freeze":
			if freezeSpecified {
				return conflict(o)
			}
			freezeSpecified = true
			freezeVal = o.Bool || copyOptionBoolValue(o.Value)
		case "delimiter":
			if haveDelim {
				return conflict(o)
			}
			haveDelim = true
		case "null":
			if haveNull {
				return conflict(o)
			}
			haveNull = true
		case "header":
			if haveHeader {
				return conflict(o)
			}
			haveHeader = true
			if o.Value != "" {
				switch strings.ToLower(o.Value) {
				case "true", "false", "on", "off", "0", "1", "match":
				default:
					return &PlanError{Pos: o.Pos(), Code: "22023", Message: "header requires a Boolean value or \"match\""}
				}
			}
		case "quote":
			if haveQuote {
				return conflict(o)
			}
			haveQuote = true
		case "escape":
			if haveEscape {
				return conflict(o)
			}
			haveEscape = true
		case "force_quote":
			if haveForceQuote {
				return conflict(o)
			}
			haveForceQuote = true
			if !o.Star && len(o.Cols) == 0 {
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: fmt.Sprintf("argument to option \"%s\" must be a list of column names", name)}
			}
		case "force_not_null":
			if haveForceNotNull {
				return conflict(o)
			}
			haveForceNotNull = true
			if !o.Star && len(o.Cols) == 0 {
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: fmt.Sprintf("argument to option \"%s\" must be a list of column names", name)}
			}
		case "force_null":
			if haveForceNull {
				return conflict(o)
			}
			haveForceNull = true
			if !o.Star && len(o.Cols) == 0 {
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: fmt.Sprintf("argument to option \"%s\" must be a list of column names", name)}
			}
		case "convert_selectively":
			if haveConvertSel {
				return conflict(o)
			}
			haveConvertSel = true
		case "encoding":
			if haveEncoding {
				return conflict(o)
			}
			haveEncoding = true
		case "on_error":
			if onErrorSpecified {
				return conflict(o)
			}
			onErrorSpecified = true
			// PG defGetCopyOnErrorChoice: direction check precedes value.
			if !isFrom {
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: "COPY ON_ERROR cannot be used with COPY TO"}
			}
			switch strings.ToLower(o.Value) {
			case "stop":
				onErrorIsStop = true
			case "ignore":
			default:
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: fmt.Sprintf("COPY ON_ERROR \"%s\" not recognized", o.Value)}
			}
		case "log_verbosity":
			if logVerbositySpecified {
				return conflict(o)
			}
			logVerbositySpecified = true
			switch strings.ToLower(o.Value) {
			case "silent", "default", "verbose":
			default:
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: fmt.Sprintf("COPY LOG_VERBOSITY \"%s\" not recognized", o.Value)}
			}
		case "reject_limit":
			if rejectLimitSpecified {
				return conflict(o)
			}
			rejectLimitSpecified = true
			n, err := strconv.ParseInt(strings.TrimSpace(o.Value), 10, 64)
			if o.Value == "" || err != nil {
				return &PlanError{Pos: o.Pos(), Code: "42601", Message: fmt.Sprintf("%s requires a numeric value", name)}
			}
			if n <= 0 {
				return &PlanError{Pos: o.Pos(), Code: "22023", Message: fmt.Sprintf("REJECT_LIMIT (%d) must be greater than zero", n)}
			}
		default:
			return &PlanError{Pos: o.Pos(), Code: "42601", Message: fmt.Sprintf("option \"%s\" not recognized", name)}
		}
	}

	// Incompatible-combination pass — PG ProcessCopyOptions ordering.
	if binary && haveDelim {
		return &PlanError{Pos: s.Pos(), Code: "42601", Message: "cannot specify DELIMITER in BINARY mode"}
	}
	if binary && haveNull {
		return &PlanError{Pos: s.Pos(), Code: "42601", Message: "cannot specify NULL in BINARY mode"}
	}
	if binary && haveHeader {
		return &PlanError{Pos: s.Pos(), Code: "0A000", Message: "cannot specify HEADER in BINARY mode"}
	}
	if !csvMode && haveQuote {
		return &PlanError{Pos: s.Pos(), Code: "0A000", Message: "COPY QUOTE requires CSV mode"}
	}
	if !csvMode && haveEscape {
		return &PlanError{Pos: s.Pos(), Code: "0A000", Message: "COPY ESCAPE requires CSV mode"}
	}
	if !csvMode && haveForceQuote {
		return &PlanError{Pos: s.Pos(), Code: "0A000", Message: "COPY FORCE_QUOTE requires CSV mode"}
	}
	if haveForceQuote && isFrom {
		return &PlanError{Pos: s.Pos(), Code: "0A000", Message: "COPY FORCE_QUOTE cannot be used with COPY FROM"}
	}
	if !csvMode && haveForceNotNull {
		return &PlanError{Pos: s.Pos(), Code: "0A000", Message: "COPY FORCE_NOT_NULL requires CSV mode"}
	}
	if haveForceNotNull && !isFrom {
		return &PlanError{Pos: s.Pos(), Code: "22023", Message: "COPY FORCE_NOT_NULL cannot be used with COPY TO"}
	}
	if !csvMode && haveForceNull {
		return &PlanError{Pos: s.Pos(), Code: "0A000", Message: "COPY FORCE_NULL requires CSV mode"}
	}
	if haveForceNull && !isFrom {
		return &PlanError{Pos: s.Pos(), Code: "22023", Message: "COPY FORCE_NULL cannot be used with COPY TO"}
	}
	if freezeVal && !isFrom {
		return &PlanError{Pos: s.Pos(), Code: "22023", Message: "COPY FREEZE cannot be used with COPY TO"}
	}
	if binary && onErrorSpecified && !onErrorIsStop {
		return &PlanError{Pos: s.Pos(), Code: "42601", Message: "only ON_ERROR STOP is allowed in BINARY mode"}
	}
	if rejectLimitSpecified && !onErrorSpecified {
		return &PlanError{Pos: s.Pos(), Code: "22023", Message: "COPY REJECT_LIMIT requires ON_ERROR to be set to IGNORE"}
	}
	return nil
}

// copyOptionBoolValue interprets a COPY flag option's textual value as a
// boolean (PG defGetBoolean). A bare flag is handled by the caller via
// CopyOption.Bool; this covers the explicit `freeze off` / `freeze on`
// forms.
func copyOptionBoolValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true
	default:
		return false
	}
}
