package executor

// bt_index_check(regclass, ...) / bt_index_parent_check(regclass, ...) scalar
// functions — slice S4 of docs/design/0110-0008 (the amcheck SQL surface).
//
// Both RETURN void and, exactly like upstream contrib/amcheck/verify_nbtree.c,
// signal corruption by *raising* an error rather than returning rows: the
// engine's []amcheck.BtreeReport findings are joined into the message under
// ERRCODE_INDEX_CORRUPTED ('XX002'). On a clean index they return a void
// (NULL) datum. They are invoked as ordinary scalar functions in a SELECT
// target list (e.g. pg_amcheck's `SELECT bt_index_check(c.oid, false) FROM
// pg_class c, pg_index i WHERE …`), so they live in the evalFuncCall dispatch
// rather than as a FROM-clause SRF plan node like verify_heapam (S3).
//
// This file is the thin wire adapter the design called for: it resolves the
// index regclass argument, fills a PageSource from goopg's buffer manager, and
// drives the already-committed internal/amcheck B-tree engine tiers over the
// live index pages. All corruption-detection logic lives in the engine and is
// unit-tested there (verify_nbtree*.go); this file owns only the
// catalog/storage plumbing the engine deliberately stays decoupled from.
//
// Scope (this slice): the index-only structural tiers, which need only the
// index's own pages —
//   - VerifyBtreePage / VerifyBtreeItemOrder on every block (incl. the metapage),
//   - VerifyBtreeLevelSiblingLinks per level (leftmost-descent right-link walk),
//   - VerifyBtreeParentDownlinks on every internal page (parent-check only).
//
// plus, when the call passes `checkunique := true` on a UNIQUE index,
//   - VerifyBtreeUnique over the leaf level (btIndexCheckUnique below), the
//     heap-visibility-aware duplicate-key tier. M0119-0006.
//
// The heapallindexed and rootdescend arguments are still accepted for call-shape
// compatibility with pg_amcheck without running their tiers: heapallindexed needs
// MVCC-aware heap-tuple → index-key extraction (the engine seam
// VerifyBtreeHeapAllIndexedRelation exists, but forming the heap entry set is the
// missing piece), and the default pg_amcheck B-tree probe passes
// `heapallindexed := false`. This deferral mirrors S3's nil-XidStatusFunc
// clog-tier deferral. M0110-0003.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// evalBtIndexCheck implements bt_index_check (parentCheck=false) and
// bt_index_parent_check (parentCheck=true). Both share the structural
// orchestration; parent-check additionally runs the parent-downlink tier.
func evalBtIndexCheck(x *planner.FuncCall, row Row, ctx *Context, parentCheck bool) (Datum, error) {
	fname := "bt_index_check"
	if parentCheck {
		fname = "bt_index_parent_check"
	}
	if len(x.Args) < 1 {
		return NullDatum, &ExecError{Code: "42883", Pos: x.Pos(),
			Message: fmt.Sprintf("function %s() does not exist", fname)}
	}
	if ctx.Pool == nil || ctx.Catalog == nil {
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: fname + " requires storage handles in Context"}
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(),
			Message: fname + " requires an in-memory catalog"}
	}

	// Resolve the regclass argument to an index relation.
	argVal, err := evalExpr(x.Args[0], row, ctx)
	if err != nil {
		return NullDatum, err
	}
	if argVal.IsNull() {
		// NULL regclass → void (upstream PG_RETURN_NULL on a NULL argument).
		return NullDatum, nil
	}
	idx, ok := btIndexResolve(argVal, im)
	if !ok {
		if argVal.Kind == KindInt {
			if _, isToast := im.ToastParentTable(uint32(argVal.Int)); isToast {
				// Synthetic pg_toast_<oid>_index OID (catalog.go's
				// toastIndexOidOffset scheme): goopg exposes it only as a
				// pg_class/pg_index join target with no real backing index
				// (mirrors the verify_heapam TOAST-heap case above) — no
				// entry is ever actually stored there, so it is vacuously
				// always a healthy, empty index. Report no findings instead
				// of erroring.
				return NullDatum, nil
			}
		}
		return NullDatum, &ExecError{Code: "42P01", Pos: x.Pos(),
			Message: "could not open relation: relation does not exist"}
	}
	// amcheck verifies only B-tree indexes; other access methods raise
	// feature_not_supported upstream. goopg has only btree indexes today, so
	// this is a guard against future AMs (Method is empty for legacy rows).
	if idx.Method != "" && !strings.EqualFold(idx.Method, "btree") {
		return NullDatum, &ExecError{Code: "0A000", Pos: x.Pos(),
			Message: "only B-Tree indexes are supported as targets for verification",
			Detail:  fmt.Sprintf("Relation %q is not a B-Tree index.", idx.Name)}
	}

	rel := ctx.Catalog.IndexRelFileNode(idx)
	// Mirror upstream bt_index_check_callback's smgrexists(MAIN_FORKNUM) guard
	// (verify_nbtree.c:318): an index whose main relation fork is missing (e.g.
	// the backing file was removed on disk, the pg_amcheck file-removal
	// corruption scenario) is reported as ERRCODE_INDEX_CORRUPTED, not silently
	// treated as an empty/clean index. This must run BEFORE NBlocks, which opens
	// the rel with O_CREATE and would otherwise recreate the fork as empty.
	if !ctx.Pool.Exists(rel) {
		return NullDatum, &ExecError{Code: "XX002", Pos: x.Pos(),
			Message: fmt.Sprintf("index \"%s\" lacks a main relation fork", idx.Name)}
	}
	nblocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return NullDatum, err
	}

	// PageSource yields each block's bytes by copy so the engine can walk the
	// whole index without holding a pin across the walk (it only reads).
	src := func(blk storage.BlockNumber) (storage.Page, error) {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			return nil, perr
		}
		page := make(storage.Page, len(s.Page()))
		copy(page, s.Page())
		ctx.Pool.Unpin(s)
		return page, nil
	}

	cmpKeys := btIndexOpClassComparator(idx, im, ctx, x.Pos())
	reports, err := btIndexVerify(src, nblocks, idx.Name, parentCheck, cmpKeys)
	if err == nil && len(reports) == 0 {
		// checkunique runs only on a structurally sound index: upstream reaches
		// bt_entry_unique_check from inside bt_target_page_check, which never
		// runs once an earlier per-page invariant has ereport(ERROR)ed.
		var ureports []amcheck.BtreeReport
		ureports, err = btIndexCheckUnique(x, row, ctx, idx, src, parentCheck, cmpKeys)
		reports = append(reports, ureports...)
	}
	if err != nil {
		// A genuine read error (not a corruption finding) maps to internal error.
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(), Message: err.Error()}
	}
	if len(reports) > 0 {
		return NullDatum, &ExecError{Code: "XX002", Pos: x.Pos(),
			Message: reports[0].Msg,
			Detail:  btIndexReportDetail(reports)}
	}
	// Clean index → void result.
	return NullDatum, nil
}

// btIndexVerify drives the structural engine tiers over the index pages and
// returns every finding (so the SQL surface can report the count, mirroring how
// the standalone realtree test exercises the same tiers). It returns a Go error
// only for a genuine page-read failure, never for a corruption finding.
func btIndexVerify(src amcheck.PageSource, nblocks storage.BlockNumber, name string, parentCheck bool, cmpKeys amcheck.KeyComparator) ([]amcheck.BtreeReport, error) {
	var reports []amcheck.BtreeReport

	// Per-page tiers over every block, including the metapage (block 0).
	for blk := range nblocks {
		p, err := src(blk)
		if err != nil {
			return nil, err
		}
		reports = append(reports, amcheck.VerifyBtreePage(p, blk, name)...)
		reports = append(reports, amcheck.VerifyBtreeItemOrderCmp(p, blk, name, cmpKeys)...)
	}

	// A tree with only the metapage (an empty index, or none) has no key levels
	// to descend; the per-page tiers above are conclusive.
	if nblocks <= btree.MetaBlock+1 {
		return reports, nil
	}

	// Cross-level sibling-link tier: descend root → leftmost child per level,
	// then walk each level's right-links from its leftmost page.
	metaPage, err := src(btree.MetaBlock)
	if err != nil {
		return nil, err
	}
	if meta := btree.ParseMeta(metaPage); meta.Root != 0 {
		for _, lm := range btIndexLeftmostByLevel(src, meta.Root) {
			reports = append(reports, amcheck.VerifyBtreeLevelSiblingLinks(src, lm, name)...)
		}
	}

	// Parent-downlink tier (bt_index_parent_check only): every internal,
	// non-deleted page's downlinks must reach existing children. Leaf and
	// fully-deleted pages carry no downlinks and are exempt.
	if parentCheck {
		for blk := btree.MetaBlock + 1; blk < nblocks; blk++ {
			p, err := src(blk)
			if err != nil {
				return nil, err
			}
			op := btree.ParseOpaque(p)
			if op.IsLeaf() || op.IsDeleted() {
				continue
			}
			reports = append(reports, amcheck.VerifyBtreeParentDownlinks(src, blk, name)...)
		}
	}
	return reports, nil
}

// btIndexCheckUnique runs amcheck's `checkunique` tier when the call requested
// it and the target index actually declares UNIQUE — upstream's two gates,
// `state->checkunique` and `state->indexinfo->ii_Unique` (verify_nbtree.c:1650).
// A non-unique index, or a call that left the argument at its false default
// (which is what pg_amcheck emits unless `--checkunique` is passed), yields no
// findings without touching the heap.
//
// The argument is positional: bt_index_check(index, heapallindexed, checkunique)
// puts it third, bt_index_parent_check(index, heapallindexed, rootdescend,
// checkunique) fourth — the parser strips named-argument labels and keeps the
// written order (M0097-0003), which for both pg_amcheck call shapes is the
// declared order.
//
// Heap visibility is the tier's one real dependency and is supplied here from
// the executor's own MVCC state, mirroring upstream's use of a registered
// transaction snapshot taken once per index check (verify_nbtree.c:471). Without
// a snapshot in Context there is nothing to judge liveness against, so the tier
// is skipped rather than answered wrongly. M0119-0006.
func btIndexCheckUnique(x *planner.FuncCall, row Row, ctx *Context, idx *catalog.Index,
	src amcheck.PageSource, parentCheck bool, cmpKeys amcheck.KeyComparator,
) ([]amcheck.BtreeReport, error) {
	argIdx := 2
	if parentCheck {
		argIdx = 3
	}
	if len(x.Args) <= argIdx {
		return nil, nil
	}
	d, err := evalExpr(x.Args[argIdx], row, ctx)
	if err != nil {
		return nil, err
	}
	if d.IsNull() || !d.BoolValue() {
		return nil, nil
	}
	if !idx.Unique || idx.Table == nil || ctx.Snap.Xmax == 0 {
		// Xmax == 0 is an unseeded snapshot (no transaction snapshot was taken
		// for this statement); there is nothing to judge tuple liveness against.
		return nil, nil
	}

	heapRel := ctx.Catalog.RelFileNode(idx.Table)
	visible := func(tid storage.ItemPointer) bool {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: tid.Block})
		if perr != nil {
			return false
		}
		s.RLock()
		tup, gerr := storage.PageGetHeapTuple(s.Page(), tid.Offset)
		s.RUnlock()
		ctx.Pool.Unpin(s)
		if gerr != nil {
			// An index entry pointing at an unreadable heap slot is heap damage,
			// which verify_heapam reports; it is not a live duplicate.
			return false
		}
		return mvcc.TupleVisible(tup.Header, ctx.Snap, ctx.Tx.XID, ctx.CmdID,
			ctx.comboStore(), ctx.MultiXact)
	}
	return amcheck.VerifyBtreeUnique(src, idx.Name, cmpKeys, visible)
}

// btIndexOpClassComparator returns the operator-class key comparator to verify
// idx under, or nil to use the engine's built-in key-byte order.
//
// Upstream amcheck always compares through the index's support function 1
// (BTORDER_PROC, resolved from pg_amproc for the index's opclass) — see
// _bt_compare / _bt_mkscankey in nbtutils.c, which verify_nbtree.c builds its
// BTScanInsert from. goopg's B-tree is key-encoding based, so a *built-in*
// opclass has no catalog function to call and nil (btree.CompareKeys) is the
// faithful answer: the encoding IS that opclass's order.
//
// A *user-created* class (CREATE OPERATOR CLASS … USING btree … FUNCTION 1 f)
// does name a real comparator, and pg_amcheck's 005_opclass_damage.pl injects
// corruption by repointing exactly that pg_amproc row at a function that sorts
// the other way. Verifying under the catalog-resolved function is what makes the
// physically-unchanged index then report `item order invariant violated`.
//
// Scope: any index whose key columns are all plain (non-expression) columns of a
// type the B-tree key decoder can invert (decodeIndexKeyColumn — int4/int8/
// float8/numeric/date/timestamp(tz)/text-like/enum), with at least one key column
// declaring a user opclass. Composite keys are compared column by column, each
// under its own opclass — the column-wise contract of upstream's _bt_compare,
// which walks scan-key attributes in order and stops at the first non-equal one.
// Key columns that resolve to no user routine keep the engine's own byte order
// for that column (their encoding IS the built-in opclass's order).
//
// INCLUDE (covering) columns need no special handling: goopg encodes a B-tree
// key from the *key* columns alone (encodeCompositeBTreeKey walks idx.Columns;
// no non-key attribute is ever appended), so a covering index's key bytes are
// exactly the column-by-column walk below. That matches upstream, where
// non-key attributes never participate in ordering — _bt_compare stops at
// IndexRelationGetNumberOfKeyAttributes. M0119-0006.
func btIndexOpClassComparator(idx *catalog.Index, im *catalog.InMemory, ctx *Context, pos int) amcheck.KeyComparator {
	if len(idx.Columns) == 0 || idx.Table == nil {
		return nil
	}
	method := idx.Method
	if method == "" {
		method = "btree"
	}
	type keyCol struct {
		col     catalog.Column
		routine *catalog.Routine // nil → this column keeps the engine's byte order
	}
	cols := make([]keyCol, 0, len(idx.Columns))
	anyRoutine := false
	for i, name := range idx.Columns {
		if name == "" {
			// Expression key column. There is no catalog column to read the
			// type off, so resolve the expression's static result type
			// (planner.ExprResultType) and map it onto the surrogate type whose
			// decoder consumes exactly the bytes the expression-key ENCODER
			// wrote (exprKeyDecodeType). M0119-0006.
			surrogate, allowRoutine, ok := exprKeyColumnType(idx, i)
			if !ok {
				return nil
			}
			kc := keyCol{col: catalog.Column{Name: "", Type: surrogate}}
			if allowRoutine && i < len(idx.ColOpClasses) && idx.ColOpClasses[i] != "" {
				if procOID, found := im.LookupOpClassSupportProcOID(idx.ColOpClasses[i],
					catalog.AccessMethodOIDByName(method), 1); found {
					if r := ctx.Catalog.Routines().LookupByOID(procOID); r != nil && len(r.ArgTypes) == 2 {
						kc.routine = r
						anyRoutine = true
					}
				}
			}
			cols = append(cols, kc)
			continue
		}
		var col *catalog.Column
		for j := range idx.Table.Columns {
			if strings.EqualFold(idx.Table.Columns[j].Name, name) {
				col = &idx.Table.Columns[j]
				break
			}
		}
		if col == nil {
			return nil
		}
		kc := keyCol{col: *col}
		if i < len(idx.ColOpClasses) && idx.ColOpClasses[i] != "" {
			if procOID, ok := im.LookupOpClassSupportProcOID(idx.ColOpClasses[i],
				catalog.AccessMethodOIDByName(method), 1); ok {
				if r := ctx.Catalog.Routines().LookupByOID(procOID); r != nil && len(r.ArgTypes) == 2 {
					kc.routine = r
					anyRoutine = true
				}
			}
		}
		cols = append(cols, kc)
	}
	if !anyRoutine {
		// Every key column resolves to a built-in class: the encoding already
		// is that order, so nil (btree.CompareKeys) is the faithful answer and
		// avoids a per-comparison decode.
		return nil
	}
	return func(a, b []byte) int {
		ao, bo := 0, 0
		for _, kc := range cols {
			ad, an, aerr := decodeIndexKeyColumn(a[ao:], kc.col)
			bd, bn, berr := decodeIndexKeyColumn(b[bo:], kc.col)
			if aerr != nil || berr != nil || an <= 0 || bn <= 0 {
				// Not a decodable key at this position: the negative-infinity
				// pivot tuple on an internal page carries an empty key
				// (findChildBlock), and a truncated separator may stop short of
				// this column. Byte order over what remains is correct for
				// those, exactly as it is for the leftmost downlink upstream
				// treats as minus infinity.
				return btree.CompareKeys(a[ao:], b[bo:])
			}
			if cmp := btIndexCompareKeyColumn(kc.routine, a[ao:ao+an], b[bo:bo+bn], ad, bd, ctx, pos); cmp != 0 {
				return cmp
			}
			ao += an
			bo += bn
		}
		// Equal on every key column: any trailing bytes decide, as they do for
		// the engine's own comparator.
		return btree.CompareKeys(a[ao:], b[bo:])
	}
}

// exprKeyColumnType resolves the decode surrogate for expression key column i
// of idx: the column type under which decodeIndexKeyColumn consumes exactly the
// bytes the expression-key encoder wrote for that column, and whether a user
// opclass comparator may be given the decoded Datum.
//
// Two resolutions have to succeed. First the expression's static SQL result
// type (planner.ExprResultType over the same resolved Expr the build path uses,
// so the comparator can never disagree with what was indexed). Then the mapping
// from that SQL type onto the encoding actually produced (exprKeyDecodeType) —
// see its comment for why the two differ. Either failing returns ok=false and
// the caller falls back to whole-key byte order, which is what this arm did
// unconditionally before M0119-0006.
func exprKeyColumnType(idx *catalog.Index, i int) (surrogate catalog.Type, allowRoutine bool, ok bool) {
	if i >= len(idx.ColExprs) || idx.ColExprs[i] == nil || idx.Table == nil {
		return catalog.Type{}, false, false
	}
	planExpr, err := planner.ResolveIndexPredicate(*idx.ColExprs[i], idx.Table)
	if err != nil || planExpr == nil {
		return catalog.Type{}, false, false
	}
	sqlType, resolved := planner.ExprResultType(planExpr)
	if !resolved {
		return catalog.Type{}, false, false
	}
	return exprKeyDecodeType(sqlType)
}

// btIndexCompareKeyColumn orders one key column: through the operator class's
// FUNCTION 1 routine when the column declares a user class, otherwise by the
// column's encoded bytes (which are the built-in class's order by construction).
func btIndexCompareKeyColumn(routine *catalog.Routine, aRaw, bRaw []byte, ad, bd Datum, ctx *Context, pos int) int {
	if routine == nil {
		return btree.CompareKeys(aRaw, bRaw)
	}
	res, err := executeStoredRoutine(routine, []Datum{ad, bd}, ctx, pos)
	if err != nil || res.IsNull() {
		// A comparator that errors or returns NULL cannot decide the ordering;
		// fall back rather than manufacture a bogus finding (upstream would
		// ereport, but amcheck's contract here is report-and-continue over the
		// whole index).
		return btree.CompareKeys(aRaw, bRaw)
	}
	switch {
	case res.Int < 0:
		return -1
	case res.Int > 0:
		return 1
	default:
		return 0
	}
}

// btIndexLeftmostByLevel descends root → leftmost child at each level via the
// negative-infinity (slot-1) downlink, returning the leftmost block of every
// level top-down — the starting point the per-level sibling-link walk needs. A
// read/decode error or an empty internal page stops the descent gracefully (the
// per-page tier already flagged the structural fault); a visited-set guards
// against a downlink cycle so a corrupt tree cannot loop forever.
func btIndexLeftmostByLevel(src amcheck.PageSource, root storage.BlockNumber) []storage.BlockNumber {
	var out []storage.BlockNumber
	seen := make(map[storage.BlockNumber]bool)
	for blk := root; ; {
		if seen[blk] {
			break
		}
		seen[blk] = true
		p, err := src(blk)
		if err != nil {
			return out
		}
		out = append(out, blk)
		op := btree.ParseOpaque(p)
		if op.IsLeaf() {
			return out
		}
		dls, err := btree.PageDownlinks(p)
		if err != nil || len(dls) == 0 {
			return out
		}
		blk = dls[0].Child // leftmost (negative-infinity) downlink
	}
	return out
}

// btIndexResolve converts a KindInt OID or KindString name Datum to the index
// relation it names (mirrors verifyHeapamResolveTable on the heap side).
func btIndexResolve(d Datum, im *catalog.InMemory) (*catalog.Index, bool) {
	switch d.Kind {
	case KindInt:
		return im.LookupIndexByOID(uint32(d.Int))
	case KindString:
		return im.LookupIndex(parser.ObjectName{Name: d.StringValue()})
	default:
		return nil, false
	}
}

// btIndexReportDetail joins every finding's "block N: msg" line into the error
// DETAIL, so the block a corruption was found on is always surfaced even for
// a single-finding raise (upstream amcheck's own ereport calls always include
// an errdetail_internal naming the offending block; dropping it here for the
// single-finding case was a parity gap, not a deliberate simplification).
func btIndexReportDetail(reports []amcheck.BtreeReport) string {
	if len(reports) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range reports {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "block %d: %s", r.Block, r.Msg)
		if r.Detail != "" {
			// The uniqueness tier carries upstream's own errdetail text
			// (bt_report_duplicate); pass it through verbatim.
			fmt.Fprintf(&b, " %s", r.Detail)
		}
	}
	return b.String()
}
