package executor

// operators_pg_get_sequence_data.go — pg_get_sequence_data(regclass) SRF.
//
// Returns (last_value int8, is_called bool) for the given sequence relation.
// pg_dump's getSequences comma-joins it with pg_catalog.pg_sequence
// (`FROM pg_sequence, pg_get_sequence_data(seqrelid)`), an implicit-LATERAL
// join whose seqrelid argument is a correlated reference to the sibling
// pg_sequence row. The operator therefore evaluates its argument against the
// lateral outer slot (bound by the parent joinOp via BindLateralOuter), mirror-
// ing verify_heapam (M0110-0003 gap #6). A constant argument
// (`pg_get_sequence_data('s'::regclass)`) resolves with a nil outer slot.
//
// The argument is a regclass (the sequence's pg_class OID); it is resolved to
// the sequence's virtual catalog table, whose VirtualRows already serve the
// runtime [last_value, log_cnt, is_called] tuple (the same source SELECT *
// FROM <seq> reads). This op projects that to (last_value, is_called).
//
// M0110-0001 (DU-002 slice 115 — sequence-dump downstream links).

import (
	"strconv"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

type pgGetSequenceDataOp struct {
	plan      *optimizer.PgGetSequenceData
	outerSlot SlotView
	row       Row // the single output row, or nil for 0 rows
	emitted   bool
}

func newPgGetSequenceDataOp(p *optimizer.PgGetSequenceData) *pgGetSequenceDataOp {
	return &pgGetSequenceDataOp{plan: p}
}

func (o *pgGetSequenceDataOp) Schema() optimizer.Schema { return o.plan.Output() }

// BindLateralOuter binds the outer row's slot for lateral arg evaluation.
// Called by joinOp before each per-outer-row Open when the SRF sits on the
// right of a Join.Lateral == true. Passing nil clears the binding.
func (o *pgGetSequenceDataOp) BindLateralOuter(slot SlotView) { o.outerSlot = slot }

func (o *pgGetSequenceDataOp) Open(ctx *Context) error {
	o.row = nil
	o.emitted = false
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	if len(o.plan.Args) == 0 {
		return nil
	}
	argVal, err := evalExprSlot(o.plan.Args[0], o.outerSlot, ctx)
	if err != nil {
		return err
	}
	if argVal.IsNull() {
		return nil
	}
	tbl, ok := verifyHeapamResolveTable(argVal, im)
	if !ok || !tbl.IsSequence || tbl.VirtualRows == nil {
		// Not a sequence relation → no rows (mirrors PG's "is not a sequence"
		// being unreachable here because pg_dump only feeds discovered
		// sequences; a non-sequence regclass yields an empty SRF result).
		return nil
	}
	rows := tbl.VirtualRows()
	// VirtualRows yields exactly one [last_value, log_cnt, is_called] tuple.
	if len(rows) == 0 || len(rows[0]) < 3 {
		return nil
	}
	lastVal, perr := strconv.ParseInt(rows[0][0], 10, 64)
	if perr != nil {
		return nil
	}
	isCalled := rows[0][2] == "t"
	o.row = Row{NewIntDatum(lastVal), NewBoolDatum(isCalled)}
	return nil
}

func (o *pgGetSequenceDataOp) Next() (TupleSlot, error) {
	if o.row == nil || o.emitted {
		return nil, EOF
	}
	o.emitted = true
	return SlotFromRow(nil, o.row), nil
}

func (o *pgGetSequenceDataOp) Close() error { return nil }
