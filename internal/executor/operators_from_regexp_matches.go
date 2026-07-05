package executor

// operators_from_regexp_matches.go — FROM regexp_matches(string, pattern[, flags]).
//
// The FROM-clause form of regexp_matches; unlike the SELECT-list position
// (see operators_project_set.go's regexpMatchesResults branch), this SRF
// produces its own range-table row: one row per match when flags contains
// 'g', at most one row otherwise, zero rows on no match. Reuses the same
// evalRegexpMatchesSRF/regexpAllMatchesArrays machinery landed for the
// SELECT-list form. M0122-0002 follow-up.

import "github.com/goopg/goopg/internal/planner"

type fromRegexpMatchesOp struct {
	plan *planner.FromRegexpMatches
	rows []Row
	idx  int
}

func newFromRegexpMatchesOp(p *planner.FromRegexpMatches) *fromRegexpMatchesOp {
	return &fromRegexpMatchesOp{plan: p}
}

func (o *fromRegexpMatchesOp) Schema() planner.Schema { return o.plan.Output() }

func (o *fromRegexpMatchesOp) Open(ctx *Context) error {
	o.rows = nil
	o.idx = 0

	// Resolve args against the current outer row when this SRF is driven
	// laterally (a correlated pattern/string argument), mirrors
	// pgOptionsToTableOp.Open.
	var outerRow Row
	if len(ctx.OuterRows) > 0 {
		outerRow = ctx.OuterRows[len(ctx.OuterRows)-1]
	}
	sD, err := evalExpr(o.plan.StringExpr, outerRow, ctx)
	if err != nil {
		return err
	}
	patD, err := evalExpr(o.plan.PatternExpr, outerRow, ctx)
	if err != nil {
		return err
	}
	flagsD := NullDatum
	if o.plan.FlagsExpr != nil {
		flagsD, err = evalExpr(o.plan.FlagsExpr, outerRow, ctx)
		if err != nil {
			return err
		}
	}
	for _, d := range evalRegexpMatchesSRF(sD, patD, flagsD) {
		o.rows = append(o.rows, Row{d})
	}
	return nil
}

func (o *fromRegexpMatchesOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

func (o *fromRegexpMatchesOp) Close() error { return nil }
