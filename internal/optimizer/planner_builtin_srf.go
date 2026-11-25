package optimizer

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// builtinSRFSpec is the accepted argument counts of a built-in table
// function planned as a BuiltinSRFScan; its result columns live in
// catalog.LookupBuiltinSRFColumns, shared with the analyzer.
type builtinSRFSpec struct {
	minArgs, maxArgs int
}

var builtinSRFSpecs = map[string]builtinSRFSpec{
	// pg_ls_dir(dirname text [, missing_ok bool, include_dot_dirs bool])
	// RETURNS SETOF text — the 1- and 3-argument pg_proc entries.
	"pg_ls_dir": {minArgs: 1, maxArgs: 3},
	// pg_stat_file(filename text [, missing_ok bool]) RETURNS record.
	"pg_stat_file": {minArgs: 1, maxArgs: 2},
}

// IsBuiltinSRF reports whether name is planned as a BuiltinSRFScan.
func IsBuiltinSRF(name string) bool {
	_, ok := builtinSRFSpecs[strings.ToLower(name)]
	return ok
}

// planBuiltinSRF plans FROM <builtin>(args) [AS alias(cols)] [WITH
// ORDINALITY]. Argument counts follow pg_proc: pg_ls_dir has 1- and
// 3-argument forms only, so a 2-argument call does not resolve, as in PG.
func planBuiltinSRF(rv parser.RangeVar, cat catalog.Catalog, sourceIdx int16, lateralCtx *resolveContext) (Node, rangeBinding, error) {
	tf := rv.TableFunc
	name := strings.ToLower(tf.Name)
	spec := builtinSRFSpecs[name]
	nargs := len(tf.Args)
	if nargs < spec.minArgs || nargs > spec.maxArgs || (name == "pg_ls_dir" && nargs == 2) {
		return nil, rangeBinding{}, &PlanError{Pos: tf.Pos(), Code: "42883",
			Message: fmt.Sprintf("function %s(%s) does not exist", name, builtinSRFArgTypes(nargs))}
	}
	ctx := &resolveContext{cat: cat, parent: planParentOf(cat), settings: DefaultPlannerSettings()}
	if lateralCtx != nil {
		if lateralCtx.parent == nil {
			cp := *lateralCtx
			cp.parent = planParentOf(cat)
			ctx = &cp
		} else {
			ctx = lateralCtx
		}
	}
	args := make([]Expr, nargs)
	for i, a := range tf.Args {
		e, err := resolveExpr(a, ctx)
		if err != nil {
			return nil, rangeBinding{}, err
		}
		args[i] = e
	}
	alias := rv.Alias
	if alias == "" {
		alias = name
	}
	colAliases := rv.Columns
	if tf.WithOrdinality && len(colAliases) > 0 {
		colAliases = colAliases[:len(colAliases)-1]
	}
	outCols, _ := catalog.LookupBuiltinSRFColumns(name)
	cols := outCols.Columns(alias, colAliases)
	schema := make(Schema, len(cols))
	for i, c := range cols {
		schema[i] = SchemaColumn{Name: c.Name, Type: c.Type, SourceTableIdx: sourceIdx}
	}
	node := &BuiltinSRFScan{pos: tf.Pos(), Func: name, Args: args, schema: schema}
	b := rangeBinding{table: &catalog.Table{Name: alias, Columns: cols}, alias: alias, offset: 0, sourceIdx: sourceIdx}
	if tf.WithOrdinality {
		node2, b2 := wrapOrdinality(node, b, rv, sourceIdx)
		return node2, b2, nil
	}
	return node, b, nil
}

func builtinSRFArgTypes(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "unknown"
	}
	return strings.Join(parts, ", ")
}
