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
// The heapallindexed (heap↔index completeness), rootdescend, and checkunique
// arguments are accepted for call-shape compatibility with pg_amcheck but their
// deeper tiers are deferred to S5: heapallindexed needs MVCC-aware heap-tuple →
// index-key extraction (the engine seam VerifyBtreeHeapAllIndexedRelation
// exists, but forming the heap entry set is the missing piece), and the default
// pg_amcheck B-tree probe passes `heapallindexed := false`. This deferral
// mirrors S3's nil-XidStatusFunc clog-tier deferral. M0110-0003.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/catalog"
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

	reports, err := btIndexVerify(src, nblocks, idx.Name, parentCheck,
		btIndexOpClassComparator(idx, im, ctx, x.Pos()))
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
// Scope (this slice): a single-key-column index on an int4 column whose declared
// opclass resolves to a user routine. That is 005_opclass_damage's shape, and it
// is the only one whose stored key bytes can be inverted back to the SQL datum
// the function expects (btree.DecodeInt4). Wider coverage needs a general
// encoded-key → Datum decoder per type; see the deferral ledger.
// M0119-0006.
func btIndexOpClassComparator(idx *catalog.Index, im *catalog.InMemory, ctx *Context, pos int) amcheck.KeyComparator {
	if len(idx.Columns) != 1 || len(idx.IncludeColumns) > 0 || len(idx.ColOpClasses) < 1 {
		return nil
	}
	className := idx.ColOpClasses[0]
	if className == "" {
		return nil
	}
	method := idx.Method
	if method == "" {
		method = "btree"
	}
	procOID, ok := im.LookupOpClassSupportProcOID(className, catalog.AccessMethodOIDByName(method), 1)
	if !ok {
		return nil
	}
	routine := ctx.Catalog.Routines().LookupByOID(procOID)
	if routine == nil || len(routine.ArgTypes) != 2 {
		return nil
	}
	// Only an int4 key column can be decoded back from its stored key bytes.
	if idx.Table == nil {
		return nil
	}
	isInt4 := false
	for _, c := range idx.Table.Columns {
		if strings.EqualFold(c.Name, idx.Columns[0]) {
			switch strings.ToLower(c.Type.Name) {
			case "int4", "integer", "int":
				isInt4 = !c.Type.IsArray
			}
			break
		}
	}
	if !isInt4 {
		return nil
	}
	return func(a, b []byte) int {
		av, aerr := btree.DecodeInt4(a)
		bv, berr := btree.DecodeInt4(b)
		if aerr != nil || berr != nil {
			// Not a plain int4 key: the negative-infinity pivot tuple on an
			// internal page carries an empty key (findChildBlock), and a
			// truncated separator may be shorter than the full encoding. Byte
			// order is correct for those, exactly as it is for the leftmost
			// downlink upstream treats as minus infinity.
			return btree.CompareKeys(a, b)
		}
		res, err := executeStoredRoutine(routine,
			[]Datum{NewIntDatum(int64(av)), NewIntDatum(int64(bv))}, ctx, pos)
		if err != nil || res.IsNull() {
			// A comparator that errors or returns NULL cannot decide the
			// ordering; fall back rather than manufacture a bogus finding
			// (upstream would ereport, but amcheck's contract here is
			// report-and-continue over the whole index).
			return btree.CompareKeys(a, b)
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
	}
	return b.String()
}
