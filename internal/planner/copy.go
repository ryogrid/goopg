package planner

import (
	"fmt"
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

	tbl, ok := cat.LookupTable(s.Table)
	if !ok {
		return nil, &PlanError{
			Pos:     s.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", s.Table.Name),
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

// validateCopyOptions surfaces the obvious shape errors at plan time
// — duplicate option names, unknown FORMAT values, malformed
// HEADER. Anything semantically deeper (FORCE_QUOTE references
// columns that aren't in the column list, etc.) is the executor's
// problem; pinning those needs the row formatter context.
func validateCopyOptions(s *parser.CopyStmt) error {
	seen := make(map[string]struct{}, len(s.Options))
	for _, o := range s.Options {
		name := strings.ToLower(o.Name)
		if _, dup := seen[name]; dup {
			return &PlanError{
				Pos:     o.Pos(),
				Code:    "42601",
				Message: fmt.Sprintf("option %q specified more than once", name),
			}
		}
		seen[name] = struct{}{}
		switch name {
		case "format":
			switch strings.ToLower(o.Value) {
			case "text", "csv", "binary":
			case "":
				return &PlanError{Pos: o.Pos(), Code: "42601", Message: "FORMAT requires a value"}
			default:
				return &PlanError{Pos: o.Pos(), Code: "0A000", Message: fmt.Sprintf("COPY format %q is not supported", o.Value)}
			}
		case "freeze", "binary", "csv":
			// bare flags — accepted, value-less
		case "header":
			// bare flag, or text true/false/on/off
			if o.Value != "" {
				switch strings.ToLower(o.Value) {
				case "true", "false", "on", "off", "match":
				default:
					return &PlanError{Pos: o.Pos(), Code: "42601", Message: fmt.Sprintf("HEADER value %q is not valid", o.Value)}
				}
			}
		case "delimiter", "null", "quote", "escape", "encoding":
			// must have a value (string)
			if o.Bool {
				return &PlanError{Pos: o.Pos(), Code: "42601", Message: fmt.Sprintf("%s requires a value", strings.ToUpper(name))}
			}
		case "force_quote", "force_not_null", "force_null":
			if !o.Star && len(o.Cols) == 0 {
				return &PlanError{Pos: o.Pos(), Code: "42601", Message: fmt.Sprintf("%s requires '*' or a column list", strings.ToUpper(name))}
			}
		default:
			return &PlanError{Pos: o.Pos(), Code: "0A000", Message: fmt.Sprintf("COPY option %q is not supported", o.Name)}
		}
	}
	return nil
}
