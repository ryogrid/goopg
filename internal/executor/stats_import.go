package executor

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage/lmgr"
)

// PostgreSQL 18 statistics import (M0146-0215; design
// docs/design/0100-0149/m0146-0215-pg-statistics-import.md):
// pg_restore_attribute_stats (statistics/attribute_stats.c) and
// pg_restore_relation_stats (statistics/relation_stats.c), with the argument
// handling of statistics/stat_utils.c. pg_dump --statistics emits these calls.
// They are how a cluster carries another cluster's statistics exactly. PG's
// ANALYZE sample is seeded from a per-process random state, so no port of its
// code can reproduce another cluster's sample.

// statsArg is one entry of a StatsArgInfo table: the argument's name and the
// type stats_check_arg_type requires (a goopg canonical type name; "[]"
// marks an array).
type statsArg struct {
	name string
	typ  string
}

const (
	attRelSchema = iota
	attRelName
	attName
	attNum
	attInherited
	attNullFrac
	attAvgWidth
	attNDistinct
	attMCVals
	attMCFreqs
	attHistogram
	attCorrelation
	attMCElems
	attMCElemFreqs
	attElemCountHist
	attRangeLengthHist
	attRangeEmptyFrac
	attRangeBoundsHist
)

// attStatsArgs is attribute_stats.c's attarginfo.
var attStatsArgs = []statsArg{
	attRelSchema:       {"schemaname", "text"},
	attRelName:         {"relname", "text"},
	attName:            {"attname", "text"},
	attNum:             {"attnum", "int2"},
	attInherited:       {"inherited", "bool"},
	attNullFrac:        {"null_frac", "float4"},
	attAvgWidth:        {"avg_width", "int4"},
	attNDistinct:       {"n_distinct", "float4"},
	attMCVals:          {"most_common_vals", "text"},
	attMCFreqs:         {"most_common_freqs", "float4[]"},
	attHistogram:       {"histogram_bounds", "text"},
	attCorrelation:     {"correlation", "float4"},
	attMCElems:         {"most_common_elems", "text"},
	attMCElemFreqs:     {"most_common_elem_freqs", "float4[]"},
	attElemCountHist:   {"elem_count_histogram", "float4[]"},
	attRangeLengthHist: {"range_length_histogram", "text"},
	attRangeEmptyFrac:  {"range_empty_frac", "float4"},
	attRangeBoundsHist: {"range_bounds_histogram", "text"},
}

const (
	relRelSchema = iota
	relRelName
	relPages
	relTuples
	relAllVisible
	relAllFrozen
)

// relStatsArgs is relation_stats.c's relarginfo.
var relStatsArgs = []statsArg{
	relRelSchema:  {"schemaname", "text"},
	relRelName:    {"relname", "text"},
	relPages:      {"relpages", "int4"},
	relTuples:     {"reltuples", "float4"},
	relAllVisible: {"relallvisible", "int4"},
	relAllFrozen:  {"relallfrozen", "int4"},
}

// statsArgTypeName canonicalises a static type for stats_check_arg_type:
// aliases fold to one spelling, and an array is "<elem>[]".
func statsArgTypeName(t catalog.Type) string {
	n := strings.ToLower(strings.TrimSpace(t.Name))
	array := t.IsArray
	if strings.HasSuffix(n, "[]") {
		n, array = strings.TrimSuffix(n, "[]"), true
	} else if strings.HasPrefix(n, "_") {
		n, array = n[1:], true
	}
	switch n {
	case "real", "float4":
		n = "float4"
	case "integer", "int", "int4":
		n = "int4"
	case "smallint", "int2":
		n = "int2"
	case "boolean", "bool":
		n = "bool"
	case "unknown":
		// An untyped literal resolves to text for a VARIADIC "any" argument.
		n = "text"
	}
	if array {
		n += "[]"
	}
	return n
}

// statsTypeDisplay renders a canonical type name as format_type_be does.
func statsTypeDisplay(n string) string {
	if base, ok := strings.CutSuffix(n, "[]"); ok {
		return statsTypeDisplay(base) + "[]"
	}
	switch n {
	case "float4":
		return "real"
	case "int4":
		return "integer"
	case "int2":
		return "smallint"
	case "bool":
		return "boolean"
	}
	return catalog.ArgTypeDisplayAlias(n)
}

// statsArgs holds the positional arguments stats_fill_fcinfo_from_arg_pairs
// produces: a value per argument, or absent.
type statsArgs struct {
	vals    []Datum
	present []bool
}

func (a statsArgs) has(i int) bool { return a.present[i] }

// fillStatsArgs mirrors stats_fill_fcinfo_from_arg_pairs: it evaluates the
// VARIADIC name/value pairs onto the positional table. It returns ok=false
// when some pair was rejected with a WARNING.
func fillStatsArgs(x *optimizer.FuncCall, slot SlotView, ctx *Context, spec []statsArg) (statsArgs, bool, error) {
	out := statsArgs{vals: make([]Datum, len(spec)), present: make([]bool, len(spec))}
	if len(x.Args)%2 != 0 {
		return out, false, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: "variadic arguments must be name/value pairs",
			Hint:    "Provide an even number of variadic arguments that can be divided into pairs."}
	}
	ok := true
	for i := 0; i < len(x.Args); i += 2 {
		nameD, err := evalExprSlot(x.Args[i], slot, ctx)
		if err != nil {
			return out, false, err
		}
		if nameD.IsNull() {
			return out, false, &ExecError{Code: "XX000", Pos: x.Pos(),
				Message: fmt.Sprintf("name at variadic position %d is null", i+1)}
		}
		if nt, known := optimizer.ExprResultType(x.Args[i]); known && statsArgTypeName(nt) != "text" {
			return out, false, &ExecError{Code: "XX000", Pos: x.Pos(),
				Message: fmt.Sprintf("name at variadic position %d has type %s, expected type %s",
					i+1, statsTypeDisplay(statsArgTypeName(nt)), "text")}
		}
		valD, err := evalExprSlot(x.Args[i+1], slot, ctx)
		if err != nil {
			return out, false, err
		}
		if valD.IsNull() {
			continue
		}
		name := nameD.Format()
		if strings.EqualFold(name, "version") {
			// Accepted and ignored, as PG does for now.
			continue
		}
		argnum := -1
		for j, a := range spec {
			if strings.EqualFold(a.name, name) {
				argnum = j
				break
			}
		}
		if argnum < 0 {
			ctx.AddWarning(fmt.Sprintf("unrecognized argument name: \"%s\"", name))
			ok = false
			continue
		}
		if vt, known := optimizer.ExprResultType(x.Args[i+1]); known {
			if got := statsArgTypeName(vt); got != spec[argnum].typ {
				ctx.AddWarning(fmt.Sprintf("argument \"%s\" has type %s, expected type %s",
					name, statsTypeDisplay(got), statsTypeDisplay(spec[argnum].typ)))
				ok = false
				continue
			}
		}
		out.vals[argnum] = valD
		out.present[argnum] = true
	}
	return out, ok, nil
}

// statsRequireArg mirrors stats_check_required_arg.
func statsRequireArg(a statsArgs, spec []statsArg, i int, pos int) error {
	if !a.has(i) {
		return &ExecError{Code: "22023", Pos: pos,
			Message: fmt.Sprintf("argument \"%s\" must not be null", spec[i].name)}
	}
	return nil
}

// statsFloat4Array parses a real[] argument and applies
// stats_check_arg_array's checks: one dimension and no null elements. A
// failed check is a WARNING and ok=false. Values are float32-rounded, as
// pg_statistic stores them.
func statsFloat4Array(d Datum, argName string, ctx *Context) ([]float64, bool) {
	s := strings.TrimSpace(d.Format())
	if strings.HasPrefix(s, "{{") {
		ctx.AddWarning(fmt.Sprintf("argument \"%s\" must not be a multidimensional array", argName))
		return nil, false
	}
	elems := parseTextArrayElems(s)
	out := make([]float64, 0, len(elems))
	for _, e := range elems {
		if !e.Quoted && strings.EqualFold(e.Text, "NULL") {
			ctx.AddWarning(fmt.Sprintf("argument \"%s\" array must not contain null values", argName))
			return nil, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(e.Text), 32)
		if err != nil {
			ctx.AddWarning(fmt.Sprintf("invalid input syntax for type real: \"%s\"", e.Text))
			return nil, false
		}
		out = append(out, float64(float32(f)))
	}
	return out, true
}

// statsFloat4 reads a real argument, float32-rounded as pg_statistic stores
// it.
func statsFloat4(d Datum) float64 {
	f, ok := datumToFloat64(d)
	if !ok {
		f, _ = strconv.ParseFloat(strings.TrimSpace(d.Format()), 64)
	}
	return float64(float32(f))
}

// statsColumnValues mirrors text_to_stavalues: it reads the text array literal
// through the column type's input (array_in with the column's typmod). The
// values are then rendered as ANALYZE renders them (formatDatumDateStyle), so
// an imported value and an analyzed one compare equal. An input error, or a
// NULL element, is a WARNING and ok=false.
func statsColumnValues(d Datum, staname string, col catalog.Column, ctx *Context) ([]string, bool) {
	elems := parseTextArrayElems(strings.TrimSpace(d.Format()))
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		if !e.Quoted && strings.EqualFold(e.Text, "NULL") {
			ctx.AddWarning(fmt.Sprintf("\"%s\" array must not contain null values", staname))
			return nil, false
		}
		v, err := statsColumnValueText(e.Text, col, ctx)
		if err != nil {
			msg := err.Error()
			if ee, isExec := err.(*ExecError); isExec {
				msg = ee.Message
			}
			ctx.AddWarning(msg)
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// statsColumnValueText runs one element through the column type's input
// function with the column's typmod and renders the result.
func statsColumnValueText(text string, col catalog.Column, ctx *Context) (string, error) {
	t := col.Type
	tn := strings.ToLower(t.Name)
	switch tn {
	case "text", "varchar", "character varying", "char", "bpchar", "character", "name":
		return coerceTextLikeDatum(t, NewStringDatum(text))
	}
	v, err := evalCast(NewStringDatum(text), tn, 0, ctx)
	if err != nil {
		return "", err
	}
	if prec, scale, ok := numericColumnTypmod(t); ok {
		if v, err = applyNumericTypmod(v, prec, scale); err != nil {
			return "", err
		}
	}
	return formatDatumDateStyle(v, ctx), nil
}

// statsTypeHasLessThan reports whether a column type has a default btree
// less-than operator (lookup_type_cache TYPECACHE_LT_OPR): histogram and
// correlation need one.
func statsTypeHasLessThan(t catalog.Type) bool {
	if t.IsArray {
		return true
	}
	switch strings.ToLower(t.Name) {
	case "json", "xml", "point", "line", "lseg", "box", "path", "polygon", "circle", "aclitem", "cid", "xid":
		return false
	}
	return true
}

// statsTypeIsRange reports whether a column is of a range or multirange type.
func statsTypeIsRange(t catalog.Type) bool {
	return strings.HasSuffix(strings.ToLower(t.Name), "range")
}

// statsResolveRelation resolves schemaname.relname, as RangeVarGetRelidExtended
// with RangeVarCallbackForStats does. It returns the table, or isIndex for
// an index name; goopg keeps no statistics for indexes, so callers accept
// those and change nothing.
func statsResolveRelation(ctx *Context, schema, rel string, pos int) (tbl *catalog.Table, isIndex bool, err error) {
	cat := ctxPlanCatalog(ctx)
	name := parser.ObjectName{Schema: schema, Name: rel}
	if t, ok := cat.LookupTable(name); ok {
		if t.Virtual {
			return nil, false, &ExecError{Code: "42809", Pos: pos,
				Message: fmt.Sprintf("cannot modify statistics for relation \"%s\"", t.Name)}
		}
		if !maintenancePermitted(ctx, t) {
			return nil, false, &ExecError{Code: "42501", Pos: pos,
				Message: fmt.Sprintf("permission denied for table %s", t.Name)}
		}
		if err := ctx.acquireRelLockMaybeTransient(ctx.Catalog.RelFileNode(t), lmgr.ShareUpdateExclusiveLock); err != nil {
			return nil, false, err
		}
		return t, false, nil
	}
	if _, ok := cat.LookupIndex(name); ok {
		return nil, true, nil
	}
	return nil, false, &ExecError{Code: "42P01", Pos: pos,
		Message: fmt.Sprintf("relation \"%s.%s\" does not exist", schema, rel)}
}

// evalPgRestoreAttributeStats is pg_restore_attribute_stats(VARIADIC "any")
// → boolean: attribute_statistics_update.
func evalPgRestoreAttributeStats(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	pos := x.Pos()
	a, result, err := fillStatsArgs(x, slot, ctx, attStatsArgs)
	if err != nil {
		return Datum{}, err
	}
	if err := statsRequireArg(a, attStatsArgs, attRelSchema, pos); err != nil {
		return Datum{}, err
	}
	if err := statsRequireArg(a, attStatsArgs, attRelName, pos); err != nil {
		return Datum{}, err
	}
	relname := a.vals[attRelName].Format()
	tbl, isIndex, err := statsResolveRelation(ctx, a.vals[attRelSchema].Format(), relname, pos)
	if err != nil {
		return Datum{}, err
	}

	// The column: attname or attnum, never both.
	if isIndex {
		// Expression-index column statistics are not kept (ledger, M0146-0215).
		return NewBoolDatum(result), nil
	}
	colIdx := -1
	var attname string
	switch {
	case a.has(attName) && a.has(attNum):
		return Datum{}, &ExecError{Code: "22023", Pos: pos,
			Message: "cannot specify both \"attname\" and \"attnum\""}
	case a.has(attName):
		attname = a.vals[attName].Format()
		for i, c := range tbl.Columns {
			if c.Name == attname {
				colIdx = i
				break
			}
		}
		if colIdx < 0 {
			if isSystemColumn(attname) {
				return Datum{}, &ExecError{Code: "0A000", Pos: pos,
					Message: fmt.Sprintf("cannot modify statistics on system column \"%s\"", attname)}
			}
			return Datum{}, &ExecError{Code: "42703", Pos: pos,
				Message: fmt.Sprintf("column \"%s\" of relation \"%s\" does not exist", attname, relname)}
		}
	case a.has(attNum):
		n := int(a.vals[attNum].Int)
		if n < 0 {
			return Datum{}, &ExecError{Code: "0A000", Pos: pos,
				Message: fmt.Sprintf("cannot modify statistics on system column \"%s\"", statsSystemColumnName(n))}
		}
		for i, c := range tbl.Columns {
			if c.Ordinal+1 == n {
				colIdx = i
				break
			}
		}
		if colIdx < 0 {
			return Datum{}, &ExecError{Code: "42703", Pos: pos,
				Message: fmt.Sprintf("column %d of relation \"%s\" does not exist", n, relname)}
		}
		attname = tbl.Columns[colIdx].Name
	default:
		return Datum{}, &ExecError{Code: "22023", Pos: pos,
			Message: "must specify either \"attname\" or \"attnum\""}
	}
	col := tbl.Columns[colIdx]

	if err := statsRequireArg(a, attStatsArgs, attInherited, pos); err != nil {
		return Datum{}, err
	}
	inherited := a.vals[attInherited].BoolValue()

	doMCV := a.has(attMCFreqs) && a.has(attMCVals)
	doHistogram := a.has(attHistogram)
	doCorrelation := a.has(attCorrelation)
	doMCElem := a.has(attMCElems) && a.has(attMCElemFreqs)
	doDecHist := a.has(attElemCountHist)
	doBoundsHist := a.has(attRangeBoundsHist)
	doRangeLength := a.has(attRangeLengthHist) && a.has(attRangeEmptyFrac)

	var mcFreqs []float64
	if a.has(attMCFreqs) {
		var ok bool
		if mcFreqs, ok = statsFloat4Array(a.vals[attMCFreqs], "most_common_freqs", ctx); !ok {
			doMCV, result = false, false
		}
	}
	if a.has(attMCElemFreqs) {
		if _, ok := statsFloat4Array(a.vals[attMCElemFreqs], "most_common_elem_freqs", ctx); !ok {
			doMCElem, result = false, false
		}
	}
	if a.has(attElemCountHist) {
		if _, ok := statsFloat4Array(a.vals[attElemCountHist], "elem_count_histogram", ctx); !ok {
			doDecHist, result = false, false
		}
	}
	// stats_check_arg_pair: the two halves of a pair come together.
	checkPair := func(i, j int) bool {
		if a.has(i) == a.has(j) {
			return true
		}
		nullArg, otherArg := i, j
		if a.has(i) {
			nullArg, otherArg = j, i
		}
		ctx.AddWarning(fmt.Sprintf("argument \"%s\" must be specified when argument \"%s\" is specified",
			attStatsArgs[nullArg].name, attStatsArgs[otherArg].name))
		return false
	}
	if !checkPair(attMCVals, attMCFreqs) {
		doMCV, result = false, false
	}
	if !checkPair(attMCElems, attMCElemFreqs) {
		doMCElem, result = false, false
	}
	if !checkPair(attRangeLengthHist, attRangeEmptyFrac) {
		doRangeLength, result = false, false
	}
	if (doMCElem || doDecHist) && !col.Type.IsArray && strings.ToLower(col.Type.Name) != "tsvector" {
		ctx.AddWarning(fmt.Sprintf("could not determine element type of column \"%s\"", attname))
		doMCElem, doDecHist, result = false, false, false
	}
	if (doHistogram || doCorrelation) && !statsTypeHasLessThan(col.Type) {
		ctx.AddWarning(fmt.Sprintf("could not determine less-than operator for column \"%s\"", attname))
		doHistogram, doCorrelation, result = false, false, false
	}
	if (doRangeLength || doBoundsHist) && !statsTypeIsRange(col.Type) {
		ctx.AddWarning(fmt.Sprintf("column \"%s\" is not a range type", attname))
		doRangeLength, doBoundsHist, result = false, false, false
	}

	if inherited {
		// goopg keeps no inheritance statistics (ledger, M0146-0215).
		return NewBoolDatum(result), nil
	}

	// Start from the column's existing statistics (the existing pg_statistic
	// tuple upstream) and replace only what was given.
	var cs catalog.ColumnStats
	if tbl.Stats != nil && colIdx < len(tbl.Stats.Columns) {
		cs = tbl.Stats.Columns[colIdx]
	}
	if a.has(attNullFrac) {
		cs.NullFrac = statsFloat4(a.vals[attNullFrac])
	}
	if a.has(attAvgWidth) {
		cs.AvgWidth = float64(int32(a.vals[attAvgWidth].Int))
	}
	if a.has(attNDistinct) {
		cs.NDistinct, cs.NDistinctFrac = catalog.ColumnStatsDistinct(statsFloat4(a.vals[attNDistinct]))
	}
	if doMCV {
		if vals, ok := statsColumnValues(a.vals[attMCVals], "most_common_vals", col, ctx); ok {
			// The slot pairs values with frequencies element by element, as
			// the pg_statistic reload reads it back.
			n := min(len(vals), len(mcFreqs))
			cs.MCV = make([]catalog.MCVEntry, n)
			for i := 0; i < n; i++ {
				cs.MCV[i] = catalog.MCVEntry{Value: vals[i], Frequency: mcFreqs[i]}
			}
		} else {
			result = false
		}
	}
	if doHistogram {
		if vals, ok := statsColumnValues(a.vals[attHistogram], "histogram_bounds", col, ctx); ok {
			cs.Histogram = vals
		} else {
			result = false
		}
	}
	if doCorrelation {
		cs.Correlation = statsFloat4(a.vals[attCorrelation])
	}
	// Element and range statistic kinds pass their checks above but are
	// not kept: ColumnStats has no slot for them (ledger, M0146-0215).

	stats := &catalog.TableStats{Analyzed: true, Columns: make([]catalog.ColumnStats, len(tbl.Columns))}
	if tbl.Stats != nil {
		cp := *tbl.Stats
		stats = &cp
		stats.Columns = make([]catalog.ColumnStats, len(tbl.Columns))
		copy(stats.Columns, tbl.Stats.Columns)
		// The presence of a pg_statistic row is what the reload reads as
		// "analyzed".
		stats.Analyzed = true
	}
	stats.Columns[colIdx] = cs
	ctx.Catalog.SetTableStats(tbl, stats)
	if werr := persistColumnStats(ctx, tbl, col, cs); werr != nil {
		// Non-fatal, as for ANALYZE: the statistics are installed in memory.
		_ = werr
	}
	return NewBoolDatum(result), nil
}

// evalPgRestoreRelationStats is pg_restore_relation_stats(VARIADIC "any")
// → boolean: relation_statistics_update.
func evalPgRestoreRelationStats(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	pos := x.Pos()
	a, result, err := fillStatsArgs(x, slot, ctx, relStatsArgs)
	if err != nil {
		return Datum{}, err
	}
	if err := statsRequireArg(a, relStatsArgs, relRelSchema, pos); err != nil {
		return Datum{}, err
	}
	if err := statsRequireArg(a, relStatsArgs, relRelName, pos); err != nil {
		return Datum{}, err
	}
	tbl, isIndex, err := statsResolveRelation(ctx, a.vals[relRelSchema].Format(), a.vals[relRelName].Format(), pos)
	if err != nil {
		return Datum{}, err
	}
	updTuples := false
	var tuples float64
	if a.has(relTuples) {
		tuples = statsFloat4(a.vals[relTuples])
		if tuples < -1.0 {
			ctx.AddWarning("argument \"reltuples\" must not be less than -1.0")
			result = false
		} else {
			updTuples = true
		}
	}
	if isIndex {
		// goopg's planner reads index size physically, as plancat.c does; it
		// keeps no index relation statistics to set (ledger, M0146-0215).
		return NewBoolDatum(result), nil
	}
	// relallvisible is the stored pg_class value the planner reads
	// (M0146-0216). relallfrozen has no consumer in goopg and is not kept
	// (ledger, M0146-0215).
	if !a.has(relPages) && !updTuples && !a.has(relAllVisible) {
		return NewBoolDatum(result), nil
	}
	pages, rows := 0, int64(0)
	analyzed := false
	if tbl.Stats != nil {
		pages, rows, analyzed = tbl.Stats.Pages, tbl.Stats.RowCount, tbl.Stats.Analyzed
	}
	if a.has(relPages) {
		pages = int(uint32(a.vals[relPages].Int))
	}
	if updTuples {
		if tuples < 0 {
			// reltuples = -1: "never vacuumed or analyzed".
			rows, analyzed = 0, false
		} else {
			rows, analyzed = int64(math.Round(tuples)), true
		}
	}
	stats := &catalog.TableStats{}
	if tbl.Stats != nil {
		cp := *tbl.Stats
		stats = &cp
	}
	stats.Pages, stats.RowCount, stats.Analyzed = pages, rows, analyzed
	if a.has(relAllVisible) {
		stats.AllVisible, stats.AllVisibleSet = int32(uint32(a.vals[relAllVisible].Int)), true
	}
	ctx.Catalog.SetTableStats(tbl, stats)
	if werr := persistRelSize(ctx, tbl, rows, pages); werr != nil {
		_ = werr
	}
	return NewBoolDatum(result), nil
}

// statsSystemColumnName names the system column of a negative attnum
// (sysattr.h), for attribute_statistics_update's "cannot modify statistics on
// system column" error.
func statsSystemColumnName(attnum int) string {
	switch attnum {
	case -1:
		return "ctid"
	case -2:
		return "xmin"
	case -3:
		return "cmin"
	case -4:
		return "xmax"
	case -5:
		return "cmax"
	case -6:
		return "tableoid"
	}
	return strconv.Itoa(attnum)
}
