package executor

// operators_ts_token_type.go — ts_token_type(parser_oid) SRF.
//
// Returns (tokid int4, alias text, description text), one row per token type
// the given parser recognizes. pg_dump's dumpTSConfig calls it with a literal
// argument (`FROM pg_catalog.ts_token_type('%u'::pg_catalog.oid) AS t`, where
// %u is the configuration's cfgparser) to resolve a pg_ts_config_map row's
// maptokentype back to its alias for the ADD MAPPING re-emission. goopg
// models only the one built-in "default" parser
// (catalog.BuiltinTSParserOID); an OID that doesn't match it (a real PG
// would only ever see this for a user-defined parser, which goopg cannot
// create — CREATE TEXT SEARCH PARSER is unimplemented) yields 0 rows rather
// than erroring, since a mismatched OID never occurs in practice here.
//
// M0110-0001 (DU-002 slice 446).

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

type tsTokenTypeOp struct {
	plan      *optimizer.TSTokenType
	outerSlot SlotView
	rows      []catalog.TSTokenType
	idx       int
}

func newTSTokenTypeOp(p *optimizer.TSTokenType) *tsTokenTypeOp {
	return &tsTokenTypeOp{plan: p}
}

func (o *tsTokenTypeOp) Schema() optimizer.Schema { return o.plan.Output() }

// BindLateralOuter binds the outer row's slot for lateral arg evaluation
// (unused by pg_dump's own literal-argument call, but kept for parity with
// the other FROM-clause SRF operators in case a future caller correlates it).
func (o *tsTokenTypeOp) BindLateralOuter(slot SlotView) { o.outerSlot = slot }

func (o *tsTokenTypeOp) Open(ctx *Context) error {
	o.rows = nil
	o.idx = 0
	if o.plan.Arg == nil {
		return nil
	}
	argVal, err := evalExprSlot(o.plan.Arg, o.outerSlot, ctx)
	if err != nil {
		return err
	}
	if argVal.IsNull() || argVal.Kind != KindInt {
		return nil
	}
	if uint32(argVal.Int) == catalog.BuiltinTSParserOID["default"] {
		o.rows = catalog.DefaultParserTokenTypes
	}
	return nil
}

func (o *tsTokenTypeOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	tt := o.rows[o.idx]
	o.idx++
	row := Row{NewIntDatum(int64(tt.TokID)), NewStringDatum(tt.Alias), NewStringDatum(tt.Description)}
	return SlotFromRow(nil, row), nil
}

func (o *tsTokenTypeOp) Close() error { return nil }
