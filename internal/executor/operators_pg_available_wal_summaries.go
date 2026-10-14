package executor

// operators_pg_available_wal_summaries.go — pg_available_wal_summaries() and
// pg_wal_summary_contents() SRFs, ports of backup/walsummaryfuncs.c.
// M0095-0002; real since M0122-0009 S4a.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/access/common/blkreftable"
	"github.com/goopg/goopg/internal/backup/walsummary"
	"github.com/goopg/goopg/internal/optimizer"
)

// pgLSNText formats an LSN the way pg_lsn_out does ("%X/%X").
func pgLSNText(l uint64) string { return fmt.Sprintf("%X/%X", uint32(l>>32), uint32(l)) }

// pgAvailableWalSummariesOp returns (tli, start_lsn, end_lsn) for each summary
// file under pg_wal/summaries (pg_available_wal_summaries ->
// GetWalSummaries(0, InvalidXLogRecPtr, InvalidXLogRecPtr)).
type pgAvailableWalSummariesOp struct {
	plan *optimizer.PgAvailableWalSummaries
	rows []Row
	pos  int
}

func newPgAvailableWalSummariesOp(p *optimizer.PgAvailableWalSummaries) *pgAvailableWalSummariesOp {
	return &pgAvailableWalSummariesOp{plan: p}
}

func (o *pgAvailableWalSummariesOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *pgAvailableWalSummariesOp) Open(ctx *Context) error {
	o.rows, o.pos = nil, 0
	if ctx == nil || ctx.DataDir == "" {
		return nil
	}
	files, err := walsummary.List(ctx.DataDir, 0, 0, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return &ExecError{Code: "58030", Message: err.Error()}
	}
	for _, f := range files {
		o.rows = append(o.rows, Row{NewIntDatum(int64(f.TLI)), NewStringDatum(pgLSNText(f.Start)), NewStringDatum(pgLSNText(f.End))})
	}
	return nil
}

func (o *pgAvailableWalSummariesOp) Next() (TupleSlot, error) {
	if o.pos >= len(o.rows) {
		return nil, EOF
	}
	o.pos++
	return SlotFromRow(nil, o.rows[o.pos-1]), nil
}

func (o *pgAvailableWalSummariesOp) Close() error { return nil }

// pgWalSummaryContentsOp returns the contents of one summary file
// (pg_wal_summary_contents): per relation fork, a row for its limit block when
// it has one (is_limit_block = true), then a row per modified block in the
// file's stored order.
type pgWalSummaryContentsOp struct {
	plan      *optimizer.PgWalSummaryContents
	outerSlot SlotView
	rows      []Row
	pos       int
}

func newPgWalSummaryContentsOp(p *optimizer.PgWalSummaryContents) *pgWalSummaryContentsOp {
	return &pgWalSummaryContentsOp{plan: p}
}

func (o *pgWalSummaryContentsOp) Schema() optimizer.Schema { return o.plan.Output() }

// BindLateralOuter binds the outer row for correlated arguments.
func (o *pgWalSummaryContentsOp) BindLateralOuter(slot SlotView) { o.outerSlot = slot }

func (o *pgWalSummaryContentsOp) Open(ctx *Context) error {
	o.rows, o.pos = nil, 0
	var args [3]Datum
	for i, a := range o.plan.Args {
		d, err := evalExprSlot(a, o.outerSlot, ctx)
		if err != nil {
			return err
		}
		args[i] = d
	}
	// The SRF is strict: any NULL argument returns no rows.
	for _, d := range args {
		if d.IsNull() {
			return nil
		}
	}
	var rawTLI int64
	switch args[0].Kind {
	case KindInt:
		rawTLI = args[0].Int
	default:
		v, err := strconv.ParseInt(strings.TrimSpace(args[0].StringValue()), 10, 64)
		if err != nil {
			return &ExecError{Code: "22P02", Pos: o.plan.Args[0].Pos(), Message: fmt.Sprintf("invalid input syntax for type bigint: \"%s\"", args[0].StringValue())}
		}
		rawTLI = v
	}
	if rawTLI < 1 || rawTLI > 0x7fffffff {
		return &ExecError{Code: "22023", Message: fmt.Sprintf("invalid timeline %d", rawTLI)}
	}
	var lsn [2]uint64
	for i := range lsn {
		l, err := parsePgLSN(args[i+1].StringValue())
		if err != nil {
			return &ExecError{Code: "22P02", Pos: o.plan.Args[i+1].Pos(), Message: fmt.Sprintf("invalid input syntax for type pg_lsn: \"%s\"", args[i+1].StringValue())}
		}
		lsn[i] = l
	}
	f := walsummary.File{TLI: uint32(rawTLI), Start: lsn[0], End: lsn[1]}
	rd, fh, err := walsummary.Read(ctx.DataDir, f)
	if err != nil {
		// OpenWalSummaryFile reports the path relative to the data directory.
		rel := walsummary.Dir + "/" + f.Name()
		if errors.Is(err, os.ErrNotExist) {
			return &ExecError{Code: "58P01", Message: fmt.Sprintf("could not open file \"%s\": No such file or directory", rel)}
		}
		if errors.Is(err, blkreftable.ErrCorrupt) {
			return &ExecError{Code: "XX001", Message: err.Error()}
		}
		return &ExecError{Code: "58030", Message: err.Error()}
	}
	defer fh.Close()
	for {
		key, limit, ok, err := rd.NextRelation()
		if err != nil {
			return &ExecError{Code: "XX001", Message: err.Error()}
		}
		if !ok {
			return nil
		}
		row := func(blk int64, isLimit bool) Row {
			return Row{
				NewIntDatum(int64(key.Locator.RelNumber)),
				NewIntDatum(int64(key.Locator.SpcOid)),
				NewIntDatum(int64(key.Locator.DbOid)),
				NewIntDatum(int64(int16(key.Fork))),
				NewIntDatum(blk),
				NewBoolDatum(isLimit),
			}
		}
		if limit != blkreftable.InvalidBlockNumber {
			o.rows = append(o.rows, row(int64(limit), true))
		}
		var blocks []blkreftable.BlockNumber
		for {
			blocks, err = rd.Blocks(blocks[:0], 256) // MAX_BLOCKS_PER_CALL
			if err != nil {
				return &ExecError{Code: "XX001", Message: err.Error()}
			}
			if len(blocks) == 0 {
				break
			}
			for _, b := range blocks {
				o.rows = append(o.rows, row(int64(b), false))
			}
		}
	}
}

func (o *pgWalSummaryContentsOp) Next() (TupleSlot, error) {
	if o.pos >= len(o.rows) {
		return nil, EOF
	}
	o.pos++
	return SlotFromRow(nil, o.rows[o.pos-1]), nil
}

func (o *pgWalSummaryContentsOp) Close() error { return nil }
