package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// M0122-0012 C32 — lazy probe-side deform (design
// docs/design/0100-0149/0122-0012-c32-lazy-probe-deform.md).
//
// PG deforms a scan tuple only as far as each consumer reads it
// (slot_getsomeattrs): ExecHashJoin's HJ_NEED_NEW_OUTER state evaluates the
// outer hash keys through ExecHashGetHashValue, whose FETCHSOME step reaches
// the key attributes only, and the join qual and projection fetch the rest in
// HJ_SCAN_BUCKET for a tuple whose hash matched. goopg's seq scan decoded its
// whole read set before the join saw the row: TPC-H Q17 decoded l_quantity and
// l_extendedprice for all 6M lineitem rows although 0.09 % match the hash
// table. Armed here, the scan decodes the probe-key columns first and the join
// finishes a row (probeFinish) only where it uses it beyond its keys.

// probeDeferArmed is a test hook (probe_defer_test.go counts arms through
// it); a no-op in production.
var probeDeferArmed = func() {}

// probeDeferDisabled turns C32 off. Tests set it to compare deferred and
// eager probes.
var probeDeferDisabled = false

// probeDeferDisarmRows is how many deferred rows a scan returns before it
// checks whether deferring pays: when the join finished at least half of
// them, every row pays for two passes and few are saved, so the scan stops
// deferring (a join that uses most of its probe rows — TPC-DS Q59's
// store_sales probe of date_dim — would otherwise run slower).
const probeDeferDisarmRows = 4096

// armProbeDefer makes the scan's first pass decode only up to its last key
// column, skipping the other read columns before it, and reports whether any
// read column was deferred. It declines whenever a post-decode step reads or
// rewrites the row the join has not finished: an absorbed qual, the GiST/GIN
// SSI hooks, enum labels, the aclitem renders, ctid string columns beyond the
// table's, and a scan that clones its rows (only a borrowed row is the one the
// join holds).
func (o *seqScanOp) armProbeDefer(keyCols []int) bool {
	if probeDeferDisabled || seqScanDeformPoison || !o.borrowRows || o.qualSet || o.prefilterSet ||
		o.gistSSIIdxOID != 0 || o.ginSSIIdxOID != 0 ||
		o.typeACLColIdx >= 0 || o.attrACLColIdx >= 0 || o.dbACLColIdx >= 0 ||
		len(o.schema) > len(o.cols) || len(o.colInfo) != len(o.cols) {
		return false
	}
	for _, et := range o.enumTypes {
		if et != nil {
			return false
		}
	}
	isKey := make([]bool, len(o.cols))
	last := -1
	for _, c := range keyCols {
		if c < 0 || c >= len(o.cols) {
			return false
		}
		isKey[c] = true
		last = max(last, c)
	}
	from := last + 1
	var early []colTypeInfo
	for i := 0; i < from; i++ {
		if !o.colInfo[i].skip && !isKey[i] {
			if early == nil {
				early = append([]colTypeInfo(nil), o.colInfo...)
			}
			early[i].skip = true
		}
	}
	late := false
	for i := from; i < len(o.colInfo); i++ {
		if !o.colInfo[i].skip {
			late = true
			break
		}
	}
	if early == nil && !late {
		return false
	}
	o.probeDeferFrom, o.probeDeferInfo = from, early
	o.deferPending, o.deferRows, o.deferFinished = false, 0, 0
	probeDeferArmed()
	return true
}

// finishDeferred completes the row the scan last returned, under the page's
// content lock as the first pass ran: it resumes where the first pass stopped
// (column probeDeferFrom at byte deferOff) or, when the first pass skipped
// read columns before its stop, decodes the row again from column 0. It then
// resolves out-of-line values exactly as the eager path does. The page stays
// pinned until the scan advances, so the tuple bytes are still there. A no-op
// when the row is already complete.
func (o *seqScanOp) finishDeferred() error {
	if !o.deferPending {
		return nil
	}
	o.deferPending = false
	o.deferFinished++
	from, off := o.probeDeferFrom, o.deferOff
	if o.probeDeferInfo != nil || from == 0 {
		from, off = 0, 0
	}
	if o.pinned != nil {
		o.pinned.RLock()
	}
	_, err := o.decodeScanRowRangeInfo(o.colInfo, o.deferData, o.deferBitmap, o.deferNatts, from, o.deferBound, off)
	if err == nil && needsDetoastPrefix(o.scanRow, o.deferBound) {
		var detoasted Row
		if detoasted, err = DetoastRowBound(o.ctx, o.rel, o.cols, o.scanRow, o.deferBound); err == nil {
			copy(o.scanRow[:o.deferBound], detoasted[:o.deferBound])
		}
	}
	if o.pinned != nil {
		o.pinned.RUnlock()
	}
	return err
}

// unwrapInstrumented returns the operator an EXPLAIN ANALYZE wrapper times.
func unwrapInstrumented(op Operator) Operator {
	for {
		io, ok := op.(*instrumentedOp)
		if !ok {
			return op
		}
		op = io.inner
	}
}

// armProbeDefer is the join half of C32, run once the probe side is open: it
// finds a seq scan under the probe — directly, or under one projection whose
// targets are all bare column references — maps the probe key columns down to
// it, and arms it. probeFinish then completes the current probe row: the scan
// decodes its deferred columns and the projection re-copies its cells. Not
// armed for a join already split into batches (nbatch > 1), whose probe rows
// are saved to batch files, or a Parallel Hash join; and every routeProbeRow
// site finishes the row first, so a batch file never receives a pending row.
func (o *joinOp) armProbeDefer() {
	o.probeFinish = nil
	if probeDeferDisabled || o.lazyProbe == nil || (o.batches != nil && o.batches.nbatch > 1) || o.parallelFill != nil || o.parallelSkipProbe ||
		o.plan == nil || o.plan.Algo != optimizer.JoinAlgoHash {
		return
	}
	offset := 0
	if o.plan.BuildLeft {
		offset = o.lazyLW
	}
	keyCols := make([]int, 0, len(o.probeKeyExprs))
	for _, e := range o.probeKeyExprs {
		cr, ok := e.(*optimizer.ColumnRef)
		if !ok || cr.Index-offset < 0 {
			return
		}
		keyCols = append(keyCols, cr.Index-offset)
	}
	if len(keyCols) == 0 {
		return
	}
	op := unwrapInstrumented(o.lazyProbe)
	var proj *projectOp
	if p, ok := op.(*projectOp); ok {
		if p.live != nil {
			return
		}
		refs := projectColRefs(p.targets)
		for _, c := range refs {
			if c < 0 {
				return
			}
		}
		for i, k := range keyCols {
			if k >= len(refs) {
				return
			}
			keyCols[i] = refs[k]
		}
		proj = p
		op = unwrapInstrumented(p.child)
	}
	scan, ok := op.(*seqScanOp)
	if !ok || !scan.armProbeDefer(keyCols) {
		return
	}
	if proj == nil {
		o.probeFinish = scan.finishDeferred
		return
	}
	refs := projectColRefs(proj.targets)
	o.probeFinish = func() error {
		if !scan.deferPending {
			return nil
		}
		if err := scan.finishDeferred(); err != nil {
			return err
		}
		for i, c := range refs {
			if i < len(proj.out) && c < len(scan.scanRow) {
				proj.out[i] = scan.scanRow[c]
			}
		}
		return nil
	}
}

// finishProbe completes the bound probe row before the join reads it beyond
// its keys (C32). A no-op unless armProbeDefer armed the probe.
func (o *joinOp) finishProbe() error {
	if o.probeFinish == nil {
		return nil
	}
	return o.probeFinish()
}
