package executor

// operators_ddl_partition.go — partition key and bound validation.
// Called from execCreateTable and execCreatePartitionChild BEFORE CreateTable
// to prevent cascade "already exists" errors. M0097-0023.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ──────────────────────────────────────────────────────────────────────────────
// DEFAULT expression validation
// ──────────────────────────────────────────────────────────────────────────────

// validateDefaultExpr checks that a column DEFAULT expression is valid:
// no column references, aggregates, subqueries, or SRFs.
func validateDefaultExpr(e parser.Expr, pos int, ctx *Context) error {
	if e == nil {
		return nil
	}
	switch v := e.(type) {
	case *parser.ColumnRef:
		return &ExecError{Code: "42P17", Pos: pos,
			Message: "cannot use column reference in DEFAULT expression"}
	case *parser.FuncCall:
		name := strings.ToLower(v.Name.Name)
		// Check args first (column refs inside aggregates are still column refs).
		for _, arg := range v.Args {
			if err := validateDefaultExpr(arg, pos, ctx); err != nil {
				return err
			}
		}
		if isKnownAggregate(name) || isUserDefinedAggregate(name, ctx) {
			return &ExecError{Code: "42803", Pos: pos,
				Message: "aggregate functions are not allowed in DEFAULT expressions"}
		}
		if isBuiltinSRF(name) {
			return &ExecError{Code: "0A000", Pos: pos,
				Message: "set-returning functions are not allowed in DEFAULT expressions"}
		}
		// Check catalog for user-defined SRFs.
		if rs := ctx.Catalog.Routines(); rs != nil {
			for _, r := range rs.LookupByName(v.Name) {
				if r.ReturnsSet {
					return &ExecError{Code: "0A000", Pos: pos,
						Message: "set-returning functions are not allowed in DEFAULT expressions"}
				}
			}
		}
	case *parser.SubqueryExpr:
		return &ExecError{Code: "42P17", Pos: pos,
			Message: "cannot use subquery in DEFAULT expression"}
	case *parser.BinaryOp:
		if err := validateDefaultExpr(v.Left, pos, ctx); err != nil {
			return err
		}
		return validateDefaultExpr(v.Right, pos, ctx)
	case *parser.UnaryOp:
		return validateDefaultExpr(v.Operand, pos, ctx)
	case *parser.CastExpr:
		return validateDefaultExpr(v.Operand, pos, ctx)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Partition KEY validation (PARTITION BY clause)
// ──────────────────────────────────────────────────────────────────────────────

// validatePartitionKey validates the PARTITION BY clause of a CREATE TABLE.
// Must be called before CreateTable to prevent cascade "already exists" errors.
func validatePartitionKey(s *parser.CreateTableStmt, cols []catalog.Column, ctx *Context) error {
	pb := s.PartitionBy
	if pb == nil {
		return nil
	}
	pos := s.Pos()

	method := strings.ToLower(pb.Method)
	switch method {
	case "list", "range", "hash":
	default:
		return &ExecError{Code: "42P16", Pos: pos,
			Message: fmt.Sprintf("unrecognized partitioning strategy %q", method)}
	}

	nKeyCols := len(pb.KeyCols)

	// LIST can have only 1 key column.
	if method == "list" && nKeyCols > 1 {
		return &ExecError{Code: "42P16", Pos: pos,
			Message: `cannot use "list" partition strategy with more than one column`}
	}

	// Build column lookup map.
	colsByName := make(map[string]catalog.Column, len(cols))
	for _, c := range cols {
		colsByName[strings.ToLower(c.Name)] = c
	}

	systemCols := map[string]bool{
		"tableoid": true, "xmin": true, "cmin": true,
		"xmax": true, "cmax": true, "ctid": true,
	}

	for i := 0; i < nKeyCols; i++ {
		colName := pb.KeyCols[i]
		opClass := ""
		if i < len(pb.OpClasses) {
			opClass = pb.OpClasses[i]
		}

		if colName == "" {
			// Expression key.
			if i < len(pb.KeyExprs) && pb.KeyExprs[i] != nil {
				if err := validatePartKeyExpr(pb.KeyExprs[i], pos, i+1, ctx); err != nil {
					return err
				}
			}
			continue
		}

		lower := strings.ToLower(colName)

		// Operator class specified — validate it.
		if opClass != "" {
			col, found := colsByName[lower]
			if found && isBtreeUnsupportedType(col.Type.Name) {
				return &ExecError{Code: "42704", Pos: pos,
					Message: fmt.Sprintf("operator class %q does not exist for access method %q", opClass, "btree")}
			}
			// For supported types, accept any opclass (we don't have full opclass catalog).
			continue
		}

		// System column check.
		if systemCols[lower] {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("cannot use system column %q in partition key", lower)}
		}

		// Column existence check.
		col, found := colsByName[lower]
		if !found {
			return &ExecError{Code: "42601", Pos: pos,
				Message: fmt.Sprintf("column %q named in partition key does not exist", colName)}
		}

		// Type must support btree ordering for LIST/RANGE.
		if method == "list" || method == "range" {
			if isBtreeUnsupportedType(col.Type.Name) {
				return &ExecError{Code: "42704", Pos: pos,
					Message: fmt.Sprintf("data type %s has no default operator class for access method %q", col.Type.Name, "btree"),
					Hint:    "You must specify a btree operator class or define a default btree operator class for the data type."}
			}
		}
	}

	return nil
}

// validatePartKeyExpr validates an expression used as a partition key.
// keyColNum is 1-based for error messages.
func validatePartKeyExpr(e parser.Expr, pos int, keyColNum int, ctx *Context) error {
	if e == nil {
		return nil
	}
	switch v := e.(type) {
	case *parser.IntegerConst, *parser.NumericConst, *parser.BooleanConst, *parser.NullConst:
		return &ExecError{Code: "42P16", Pos: pos,
			Message: "cannot use constant expression as partition key"}
	case *parser.StringConst:
		// Bare string literal has pseudo-type unknown.
		return &ExecError{Code: "42P16", Pos: pos,
			Message: fmt.Sprintf("partition key column %d has pseudo-type unknown", keyColNum)}
	case *parser.RowExpr:
		return &ExecError{Code: "42P16", Pos: pos,
			Message: fmt.Sprintf("partition key column %d has pseudo-type record", keyColNum)}
	case *parser.FuncCall:
		if v.Over != nil {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "window functions are not allowed in partition key expressions"}
		}
		name := strings.ToLower(v.Name.Name)
		if isKnownAggregate(name) || isUserDefinedAggregate(name, ctx) {
			return &ExecError{Code: "42803", Pos: pos,
				Message: "aggregate functions are not allowed in partition key expressions"}
		}
		// Check catalog for SRF or non-immutable.
		if rs := ctx.Catalog.Routines(); rs != nil {
			routines := rs.LookupByName(v.Name)
			for _, r := range routines {
				if r.ReturnsSet {
					return &ExecError{Code: "0A000", Pos: pos,
						Message: "set-returning functions are not allowed in partition key expressions"}
				}
				if r.Volatile != "i" {
					return &ExecError{Code: "42P16", Pos: pos,
						Message: "functions in partition key expression must be marked IMMUTABLE"}
				}
				// No-arg IMMUTABLE function is a constant expression.
				if len(v.Args) == 0 && r.Volatile == "i" {
					return &ExecError{Code: "42P16", Pos: pos,
						Message: "cannot use constant expression as partition key"}
				}
			}
		}
		// Recursively validate args.
		for _, arg := range v.Args {
			if err := validatePartKeyExpr(arg, pos, keyColNum, ctx); err != nil {
				return err
			}
		}
	case *parser.SubqueryExpr:
		return &ExecError{Code: "42P16", Pos: pos,
			Message: "cannot use subquery in partition key expression"}
	case *parser.BinaryOp:
		if err := validatePartKeyExpr(v.Left, pos, keyColNum, ctx); err != nil {
			return err
		}
		return validatePartKeyExpr(v.Right, pos, keyColNum, ctx)
	case *parser.UnaryOp:
		return validatePartKeyExpr(v.Operand, pos, keyColNum, ctx)
	case *parser.CastExpr:
		return validatePartKeyExpr(v.Operand, pos, keyColNum, ctx)
	case *parser.ColumnRef:
		// Column references are valid in partition key expressions.
		return nil
	case *parser.CollateExpr:
		return validatePartKeyExpr(v.Operand, pos, keyColNum, ctx)
	}
	return nil
}

// isBtreeUnsupportedType returns true for types without a default btree opclass.
func isBtreeUnsupportedType(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "point", "line", "lseg", "box", "path", "polygon", "circle":
		return true
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────────────
// Partition BOUND validation (FOR VALUES clause)
// ──────────────────────────────────────────────────────────────────────────────

// validatePartitionChildBounds validates the FOR VALUES bounds before creating
// a partition child. Must be called before CreateTable.
func (o *ddlOp) validatePartitionChildBounds(s *parser.CreateTableStmt, parent *catalog.Table) error {
	poc := s.PartitionOf
	pos := s.Pos()
	childName := s.Name.Name

	// Parent must be partitioned.
	if parent.PartitionMethod == "" {
		return &ExecError{Code: "42601", Pos: pos,
			Message: fmt.Sprintf("%q is not partitioned", poc.Parent.String())}
	}

	// Temp/permanent mixing.
	if parent.Temp && !s.Temporary {
		return &ExecError{Code: "0A000", Pos: pos,
			Message: fmt.Sprintf("cannot create a permanent relation as partition of temporary relation %q", poc.Parent.String())}
	}
	if !parent.Temp && s.Temporary {
		return &ExecError{Code: "0A000", Pos: pos,
			Message: fmt.Sprintf("cannot create a temporary relation as partition of permanent relation %q", poc.Parent.String())}
	}

	strategy := strings.ToLower(parent.PartitionMethod)

	// Bound type must match parent strategy.
	if !poc.Default {
		if poc.IsHash && strategy != "hash" {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("invalid bound specification for a %s partition", strategy)}
		}
		if !poc.IsHash && len(poc.InValues) > 0 && strategy != "list" {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("invalid bound specification for a %s partition", strategy)}
		}
		if !poc.IsHash && len(poc.InValues) == 0 && (len(poc.FromValues) > 0 || len(poc.ToValues) > 0) && strategy != "range" {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("invalid bound specification for a %s partition", strategy)}
		}
	}

	// HASH: no DEFAULT partition.
	if poc.Default && strategy == "hash" {
		return &ExecError{Code: "42P16", Pos: pos,
			Message: "a hash-partitioned table may not have a default partition"}
	}

	// Validate bound expressions.
	var boundExprs []parser.Expr
	boundExprs = append(boundExprs, poc.InValues...)
	boundExprs = append(boundExprs, poc.FromValues...)
	boundExprs = append(boundExprs, poc.ToValues...)
	for _, e := range boundExprs {
		if err := validatePartBoundExpr(e, pos, parent); err != nil {
			return err
		}
	}

	// HASH-specific.
	if poc.IsHash {
		if poc.Modulus <= 0 {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "modulus for hash partition must be an integer value greater than zero"}
		}
		if poc.Remainder < 0 || poc.Remainder >= poc.Modulus {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "remainder for hash partition must be less than modulus"}
		}
		if err := validateHashBounds(childName, poc.Modulus, poc.Remainder, parent, pos, o.ctx); err != nil {
			return err
		}
	}

	// RANGE-specific.
	if len(poc.FromValues) > 0 || len(poc.ToValues) > 0 {
		nKeyCols := len(parent.PartitionKey)
		if len(parent.PartitionKeyExprs) > nKeyCols {
			nKeyCols = len(parent.PartitionKeyExprs)
		}
		if nKeyCols == 0 {
			nKeyCols = 1
		}
		if len(poc.FromValues) > 0 && len(poc.FromValues) != nKeyCols {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "FROM must specify exactly one value per partitioning column"}
		}
		if len(poc.ToValues) > 0 && len(poc.ToValues) != nKeyCols {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "TO must specify exactly one value per partitioning column"}
		}
		for _, e := range poc.FromValues {
			if _, isNull := e.(*parser.NullConst); isNull {
				return &ExecError{Code: "42P16", Pos: pos,
					Message: "cannot specify NULL in range bound"}
			}
		}
		for _, e := range poc.ToValues {
			if _, isNull := e.(*parser.NullConst); isNull {
				return &ExecError{Code: "42P16", Pos: pos,
					Message: "cannot specify NULL in range bound"}
			}
		}
		if err := validateRangeBoundOrder(childName, poc.FromValues, poc.ToValues, pos); err != nil {
			return err
		}
		if err := validateRangeOverlap(childName, poc.FromValues, poc.ToValues, parent, pos, o.ctx); err != nil {
			return err
		}
	}

	// LIST-specific overlap.
	if len(poc.InValues) > 0 && strategy == "list" {
		if err := validateListOverlap(childName, poc.InValues, parent, pos, o.ctx); err != nil {
			return err
		}
	}

	// DEFAULT partition conflict.
	if poc.Default && strategy != "hash" {
		if err := validateDefaultPartitionConflict(childName, parent, pos, o.ctx); err != nil {
			return err
		}
	}

	return nil
}

// validatePartBoundExpr checks that a single partition bound expression is valid:
// no column references, aggregates, subqueries, or SRFs.
func validatePartBoundExpr(e parser.Expr, pos int, parent *catalog.Table) error {
	if e == nil {
		return nil
	}
	switch v := e.(type) {
	case *parser.ColumnRef:
		return &ExecError{Code: "42P16", Pos: pos,
			Message: "cannot use column reference in partition bound expression"}
	case *parser.FuncCall:
		name := strings.ToLower(v.Name.Name)
		if isKnownAggregate(name) {
			return &ExecError{Code: "42803", Pos: pos,
				Message: "aggregate functions are not allowed in partition bound"}
		}
		if isBuiltinSRF(name) {
			return &ExecError{Code: "0A000", Pos: pos,
				Message: "set-returning functions are not allowed in partition bound"}
		}
		for _, arg := range v.Args {
			if err := validatePartBoundExpr(arg, pos, parent); err != nil {
				return err
			}
		}
	case *parser.SubqueryExpr:
		return &ExecError{Code: "42P16", Pos: pos,
			Message: "cannot use subquery in partition bound"}
	case *parser.CollateExpr:
		if partKeyHasIntegerType(parent) {
			return &ExecError{Code: "42P18", Pos: pos,
				Message: fmt.Sprintf("collations are not supported by type %s", partKeyFirstTypeName(parent))}
		}
		return validatePartBoundExpr(v.Operand, pos, parent)
	case *parser.BinaryOp:
		if err := validatePartBoundExpr(v.Left, pos, parent); err != nil {
			return err
		}
		return validatePartBoundExpr(v.Right, pos, parent)
	case *parser.UnaryOp:
		return validatePartBoundExpr(v.Operand, pos, parent)
	case *parser.CastExpr:
		return validatePartBoundExpr(v.Operand, pos, parent)
	case *parser.InExpr:
		for _, arg := range v.List {
			if err := validatePartBoundExpr(arg, pos, parent); err != nil {
				return err
			}
		}
	}
	return nil
}

// partKeyHasIntegerType returns true if the parent's first partition key column is integer.
func partKeyHasIntegerType(parent *catalog.Table) bool {
	if len(parent.PartitionKey) == 0 {
		return false
	}
	keyCol := strings.ToLower(parent.PartitionKey[0])
	for _, col := range parent.Columns {
		if strings.ToLower(col.Name) == keyCol {
			switch strings.ToLower(col.Type.Name) {
			case "int", "int2", "int4", "int8", "integer", "bigint", "smallint":
				return true
			}
		}
	}
	return false
}

// partKeyFirstTypeName returns the display type name of the first partition key column.
func partKeyFirstTypeName(parent *catalog.Table) string {
	if len(parent.PartitionKey) == 0 {
		return "unknown"
	}
	keyCol := strings.ToLower(parent.PartitionKey[0])
	for _, col := range parent.Columns {
		if strings.ToLower(col.Name) == keyCol {
			switch strings.ToLower(col.Type.Name) {
			case "int", "int4":
				return "integer"
			case "int2":
				return "smallint"
			case "int8":
				return "bigint"
			case "float4":
				return "real"
			case "float8":
				return "double precision"
			}
			return col.Type.Name
		}
	}
	return "unknown"
}

// ──────────────────────────────────────────────────────────────────────────────
// Range bound order and overlap
// ──────────────────────────────────────────────────────────────────────────────

// validateRangeBoundOrder checks FROM < TO using lexicographic multi-column comparison.
func validateRangeBoundOrder(childName string, fromExprs, toExprs []parser.Expr, pos int) error {
	if len(fromExprs) == 0 || len(toExprs) == 0 {
		return nil
	}
	n := len(fromExprs)
	if len(toExprs) < n {
		n = len(toExprs)
	}
	for i := 0; i < n; i++ {
		fromVal, fromMin, fromMax, fromOk := parseBoundValue(fromExprs[i])
		toVal, toMin, toMax, toOk := parseBoundValue(toExprs[i])
		if !fromOk || !toOk {
			// Cannot determine order for non-numeric/non-minmax bounds; assume valid.
			return nil
		}
		cmp := compareBounds(fromVal, toVal, fromMin, toMin, fromMax, toMax)
		if cmp < 0 {
			// FROM < TO for this column: not empty.
			return nil
		}
		if cmp > 0 {
			// FROM > TO: empty range.
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("empty range bound specified for partition %q\nDETAIL:  Specified lower bound (%s) is greater than or equal to upper bound (%s).",
					childName, boundExprStr(fromExprs[i]), boundExprStr(toExprs[i]))}
		}
		// cmp == 0: equal on this column, check next.
	}
	// All columns compared equal: empty range.
	return &ExecError{Code: "42P16", Pos: pos,
		Message: fmt.Sprintf("empty range bound specified for partition %q\nDETAIL:  Specified lower bound (%s) is greater than or equal to upper bound (%s).",
			childName, boundExprStr(fromExprs[0]), boundExprStr(toExprs[0]))}
}

// boundTuple holds a parsed bound value for multi-column comparison.
type boundTuple struct {
	val   int64
	isMin bool
	isMax bool
	ok    bool
}

func parseTupleFromExprs(exprs []parser.Expr) []boundTuple {
	result := make([]boundTuple, len(exprs))
	for i, e := range exprs {
		v, isMin, isMax, ok := parseBoundValue(e)
		result[i] = boundTuple{val: v, isMin: isMin, isMax: isMax, ok: ok}
	}
	return result
}

func parseTupleFromStrs(strs []string) []boundTuple {
	result := make([]boundTuple, len(strs))
	for i, s := range strs {
		v, isMin, isMax, ok := parseBoundValueFromStr(s)
		result[i] = boundTuple{val: v, isMin: isMin, isMax: isMax, ok: ok}
	}
	return result
}

func lexicoCompareTuples(a, b []boundTuple) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if !a[i].ok || !b[i].ok {
			return 0
		}
		cmp := compareBounds(a[i].val, b[i].val, a[i].isMin, b[i].isMin, a[i].isMax, b[i].isMax)
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

// validateRangeOverlap checks that new range bounds don't overlap existing partitions.
func validateRangeOverlap(childName string, fromExprs, toExprs []parser.Expr, parent *catalog.Table, pos int, ctx *Context) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	children := im.PartitionChildren(parent.OID)
	for _, child := range children {
		for _, pb := range child.PartitionBounds {
			if pb.IsDefault || pb.IsHash || len(pb.FromValues) == 0 {
				continue
			}
			if rangeOverlapsExprStr(fromExprs, toExprs, pb.FromValues, pb.ToValues) {
				return &ExecError{Code: "42P16", Pos: pos,
					Message: fmt.Sprintf("partition %q would overlap partition %q", childName, child.Name)}
			}
		}
	}
	return nil
}

// rangeOverlapsExprStr returns true if [from1, to1) overlaps [from2, to2).
// Uses lexicographic multi-column comparison.
func rangeOverlapsExprStr(from1Exprs, to1Exprs []parser.Expr, from2Strs, to2Strs []string) bool {
	n := len(from1Exprs)
	if len(to1Exprs) < n {
		n = len(to1Exprs)
	}
	if len(from2Strs) < n {
		n = len(from2Strs)
	}
	if len(to2Strs) < n {
		n = len(to2Strs)
	}
	if n == 0 {
		return false
	}
	from1 := parseTupleFromExprs(from1Exprs[:n])
	to1 := parseTupleFromExprs(to1Exprs[:n])
	from2 := parseTupleFromStrs(from2Strs[:n])
	to2 := parseTupleFromStrs(to2Strs[:n])

	// Ranges overlap if F1 < T2 AND F2 < T1.
	return lexicoCompareTuples(from1, to2) < 0 && lexicoCompareTuples(from2, to1) < 0
}

// ──────────────────────────────────────────────────────────────────────────────
// List overlap
// ──────────────────────────────────────────────────────────────────────────────

// validateListOverlap checks that new IN values don't overlap existing list partitions.
func validateListOverlap(childName string, inValues []parser.Expr, parent *catalog.Table, pos int, ctx *Context) error {
	newVals := make(map[string]bool, len(inValues))
	for _, e := range inValues {
		newVals[exprToString(e)] = true
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	children := im.PartitionChildren(parent.OID)
	for _, child := range children {
		for _, pb := range child.PartitionBounds {
			if pb.IsDefault || pb.IsHash {
				continue
			}
			for _, v := range pb.InValues {
				if newVals[v] {
					return &ExecError{Code: "42P16", Pos: pos,
						Message: fmt.Sprintf("partition %q would overlap partition %q", childName, child.Name)}
				}
			}
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Hash partition validation
// ──────────────────────────────────────────────────────────────────────────────

// validateHashBounds checks modulus compatibility and overlap with existing HASH partitions.
func validateHashBounds(childName string, modulus, remainder int64, parent *catalog.Table, pos int, ctx *Context) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	children := im.PartitionChildren(parent.OID)
	for _, child := range children {
		for _, pb := range child.PartitionBounds {
			if !pb.IsHash {
				continue
			}
			existMod := pb.Modulus
			existRem := pb.Remainder

			// Moduli must have a factor relationship.
			large, small := modulus, existMod
			if large < small {
				large, small = small, large
			}
			if large%small != 0 {
				return &ExecError{Code: "42P16", Pos: pos,
					Message: "every hash partition modulus must be a factor of the next larger modulus"}
			}

			// Check for overlapping remainders.
			g := gcdInt64(modulus, existMod)
			if remainder%g == existRem%g {
				existName := hashPartitionChildName(children, existMod, existRem)
				return &ExecError{Code: "42P16", Pos: pos,
					Message: fmt.Sprintf("partition %q would overlap partition %q", childName, existName)}
			}
		}
	}
	return nil
}

// hashPartitionChildName looks up the child name for a HASH partition.
func hashPartitionChildName(children []*catalog.Table, modulus, remainder int64) string {
	for _, child := range children {
		for _, pb := range child.PartitionBounds {
			if pb.IsHash && pb.Modulus == modulus && pb.Remainder == remainder {
				return child.Name
			}
		}
	}
	return fmt.Sprintf("(modulus %d, remainder %d)", modulus, remainder)
}

// gcdInt64 computes the greatest common divisor.
func gcdInt64(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// ──────────────────────────────────────────────────────────────────────────────
// DEFAULT partition conflict
// ──────────────────────────────────────────────────────────────────────────────

// validateDefaultPartitionConflict checks that a new DEFAULT partition doesn't
// conflict with an existing default.
func validateDefaultPartitionConflict(childName string, parent *catalog.Table, pos int, ctx *Context) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	children := im.PartitionChildren(parent.OID)
	for _, child := range children {
		for _, pb := range child.PartitionBounds {
			if pb.IsDefault {
				return &ExecError{Code: "42P16", Pos: pos,
					Message: fmt.Sprintf("partition %q conflicts with existing default partition %q", childName, child.Name)}
			}
		}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ──────────────────────────────────────────────────────────────────────────────

// isKnownAggregate returns true if name is a known SQL aggregate function name.
func isKnownAggregate(name string) bool {
	switch name {
	case "sum", "avg", "count", "min", "max", "array_agg", "string_agg",
		"bool_and", "bool_or", "every", "bit_and", "bit_or", "xmlagg",
		"corr", "covar_pop", "covar_samp", "regr_avgx", "regr_avgy",
		"regr_count", "regr_intercept", "regr_r2", "regr_slope",
		"regr_sxx", "regr_sxy", "regr_syy", "stddev", "stddev_pop",
		"stddev_samp", "variance", "var_pop", "var_samp",
		"json_agg", "json_object_agg", "jsonb_agg", "jsonb_object_agg":
		return true
	}
	return false
}

// isUserDefinedAggregate checks catalog for a user-defined aggregate with this name.
func isUserDefinedAggregate(name string, ctx *Context) bool {
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		if _, found := im.LookupUserAggregateByName(name); found {
			return true
		}
	}
	return false
}

// isBuiltinSRF returns true for well-known built-in set-returning functions.
func isBuiltinSRF(name string) bool {
	switch name {
	case "generate_series", "generate_subscripts", "unnest",
		"json_each", "json_each_text", "json_array_elements",
		"jsonb_each", "jsonb_each_text", "jsonb_array_elements",
		"string_to_table", "regexp_split_to_table":
		return true
	}
	return false
}

// parseBoundValue parses a bound expression to a comparable integer value.
// Returns (value, isMinValue, isMaxValue, ok).
func parseBoundValue(e parser.Expr) (val int64, isMin bool, isMax bool, ok bool) {
	switch v := e.(type) {
	case *parser.IntegerConst:
		return v.Value, false, false, true
	case *parser.ColumnRef:
		switch strings.ToLower(v.Column) {
		case "minvalue":
			return 0, true, false, true
		case "maxvalue":
			return 0, false, true, true
		}
	case *parser.UnaryOp:
		if v.Op == parser.OpUnaryNeg {
			inner, _, _, innerOk := parseBoundValue(v.Operand)
			if innerOk {
				return -inner, false, false, true
			}
		}
	case *parser.StringConst:
		var n int64
		_, err := fmt.Sscanf(v.Value, "%d", &n)
		if err == nil {
			return n, false, false, true
		}
		switch strings.ToLower(v.Value) {
		case "minvalue":
			return 0, true, false, true
		case "maxvalue":
			return 0, false, true, true
		}
	}
	return 0, false, false, false
}

// parseBoundValueFromStr parses a stored bound string value.
func parseBoundValueFromStr(s string) (val int64, isMin bool, isMax bool, ok bool) {
	switch strings.ToLower(s) {
	case "minvalue":
		return 0, true, false, true
	case "maxvalue":
		return 0, false, true, true
	}
	e := &parser.StringConst{Value: s}
	return parseBoundValue(e)
}

// compareBounds compares two partition bound values: -1, 0, or 1.
func compareBounds(a, b int64, aMin, bMin, aMax, bMax bool) int {
	if aMin {
		if bMin {
			return 0
		}
		return -1
	}
	if bMin {
		return 1
	}
	if aMax {
		if bMax {
			return 0
		}
		return 1
	}
	if bMax {
		return -1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// boundExprStr returns a display string for a bound expression.
func boundExprStr(e parser.Expr) string {
	switch v := e.(type) {
	case *parser.IntegerConst:
		return fmt.Sprintf("%d", v.Value)
	case *parser.ColumnRef:
		return strings.ToLower(v.Column)
	case *parser.UnaryOp:
		if v.Op == parser.OpUnaryNeg {
			return "-" + boundExprStr(v.Operand)
		}
	}
	return exprToString(e)
}
