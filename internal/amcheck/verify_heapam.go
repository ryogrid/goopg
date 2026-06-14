// Package amcheck implements the page-structural core of upstream amcheck's
// verify_heapam() — the heap-page integrity checker
// (postgres/contrib/amcheck/verify_heapam.c).
//
// This is the reusable engine; the SQL surface that exposes it (CREATE
// EXTENSION amcheck + the verify_heapam set-returning function) is wired in a
// later loop. See docs/design/0110-0005-verify-heapam-engine.md for scope and
// the engine-first rationale.
//
// The page-structural tier is implemented here: line-pointer bounds and
// alignment, redirect-target validity, tuple-header offset/t_hoff consistency,
// the two infomask-only invariants from check_tuple_header (a multixact xmax
// must not be hint-bit "committed"; a HOT-updated tuple must carry a valid
// xmax), and the page-structural subset of the HOT-chain (update-chain) tier:
// a redirect must point at a heap-only tuple, HOT chains must not intersect,
// and the HOT-updated/heap-only flags of a chain link must agree. These are
// deterministic functions of the raw 8 KiB page bytes plus the page's own
// block number (to recognise same-page CTID successors) and need neither
// clog, the relation TupleDesc, nor the toast relation.
//
// The clog-dependent HOT-chain tier (verify_heapam.c's second/third
// update-chain loops) is also implemented here, via VerifyHeapPageWithXminStatus
// and an injected XidStatusFunc that resolves a tuple's xmin commit status
// (committed / aborted / in-progress / current). These are the three checks
// that need to know whether each tuple's inserting transaction committed:
// (1) an in-progress xmin updated to a committed xmin, (2) an aborted xmin
// updated to an in-progress or committed xmin, and (3) a heap-only tuple that
// is the root of an update chain (no predecessor) yet has a committed/in-progress
// xmin. The callback keeps the engine decoupled from goopg's clog/proc-array:
// the SQL surface (the verify_heapam SRF) supplies a clog-backed implementation,
// tests supply a map. Bootstrap (xid 1) and frozen (xid 2, or the frozen hint
// bits) xmins resolve to "committed" without consulting the callback, mirroring
// get_xid_status's special-casing. The page-bytes-only entry points pass a nil
// callback, which disables exactly these three checks and leaves their output
// byte-for-byte unchanged.
//
// One relation-dependent check is also available, via VerifyHeapPageWithRel:
// the tuple-natts-vs-table check (verify_heapam.c:check_tuple) — a tuple's
// stored attribute count (from t_infomask2, itself page-structural) must not
// exceed the relation's column count. It needs only that one scalar, supplied
// by the SQL surface (the verify_heapam SRF) in RelDesc.Natts; it needs no
// clog, no per-attribute TupleDesc, and no toast relation, so it is faithful
// to goopg's on-disk layout. The per-attribute walk (check_tuple_attribute)
// is NOT ported: it decodes PG's on-disk varlena/TOAST-pointer format, which
// goopg does not use (goopg's TOAST is a separate chunk relation with a
// goopg-specific in-heap pointer datum — see internal/executor/toast.go), so a
// verbatim port would false-positive on valid goopg pages. The MVCC/attribute
// tier (xmin/xmax numeric bounds vs the cluster's xid range, multixact member
// validation, TOAST pointer validation) remains deferred; see the design doc.
//
// Infomask layout divergence (goopg vs upstream PG): upstream stores
// HEAP_HOT_UPDATED / HEAP_ONLY_TUPLE in t_infomask2, but goopg packs them into
// t_infomask (storage/heap.go HeapHotUpdated/HeapOnlyTuple are read/written
// against HeapTupleHeader.Infomask — see storage/prune.go and the heap_update
// path). Because this engine inspects goopg's own on-disk pages, the
// HOT-updated check below reads the flag from t_infomask (goopg's position),
// not t_infomask2. The corruption messages stay byte-for-byte identical to
// upstream so the later SRF + 004_verify_heapam port can reuse them.
//
// One upstream check_tuple_header invariant is intentionally NOT ported here:
// "tuple is heap only, but not the result of an update" tests
// (t_infomask & HEAP_UPDATED) == 0, but goopg never sets HEAP_UPDATED (0x2000;
// goopg reuses that bit for HeapKeysUpdated in t_infomask2). Porting it
// verbatim would false-positive on every legitimate goopg HOT successor tuple,
// so it is deferred until goopg stamps HEAP_UPDATED. See the design doc.
package amcheck

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// firstOffsetNumber mirrors upstream FirstOffsetNumber: line pointers are
// 1-based (postgres/src/include/storage/off.h).
const firstOffsetNumber = 1

// invalidOffsetNumber mirrors upstream InvalidOffsetNumber (0): the sentinel
// for "no such line pointer" used in the successor/predecessor chain arrays
// (postgres/src/include/storage/off.h).
const invalidOffsetNumber uint16 = 0

// heapXmaxIsMulti mirrors upstream HEAP_XMAX_IS_MULTI
// (postgres/src/include/access/htup_details.h, 0x1000): xmax holds a MultiXactId
// rather than a plain TransactionId. goopg has no multixact on disk and never
// sets this bit, so on a healthy goopg page it is only ever set by corruption —
// exactly what the multixact-marked-committed check below detects. The storage
// package does not export it (goopg consumes no multixact bits), so the engine
// defines it locally at the upstream value.
const heapXmaxIsMulti uint16 = 0x1000

// minTupleHeaderSize is MAXALIGN(SizeofHeapTupleHeader): the smallest a
// LP_NORMAL line pointer's length may legitimately be. Upstream's
// SizeofHeapTupleHeader is 23 (matching storage.SizeOfHeapTupleHeaderData);
// MAXALIGN rounds it to 24.
var minTupleHeaderSize = maxAlign(storage.SizeOfHeapTupleHeaderData)

// Report is one corruption finding. Offset is the 1-based line-pointer offset
// number; Msg is the upstream-matching corruption message (verbatim from
// verify_heapam.c so a later SRF + the 004_verify_heapam port can reuse it).
type Report struct {
	Offset uint16
	Msg    string
}

// RelDesc carries the relation-level metadata the page checker needs for the
// checks that compare a tuple against its table definition (verify_heapam.c's
// check_tuple, which gates these on the relation's TupleDesc). It is supplied
// by the SQL surface — the verify_heapam set-returning function — once that is
// wired; the page-bytes-only entry point VerifyHeapPage leaves it unset, which
// disables the relation-dependent checks. Only the metadata that is faithful to
// goopg's on-disk layout lives here: goopg has neither PG's on-disk varlena/
// TOAST pointer format nor a separately stored relation-wide attribute
// descriptor on the page, so the per-attribute (check_tuple_attribute) walk is
// not represented; see the package doc.
type RelDesc struct {
	// Natts is the relation's column count (RelationGetDescr(rel)->natts). A
	// visible tuple may carry fewer attributes than the table (trailing columns
	// added later), but never more — a stored count above Natts is corruption
	// (verify_heapam.c:1942). Zero (the unset value used by VerifyHeapPage)
	// means "unknown", which skips this check.
	Natts int
}

// XidCommitStatus is the commit status of a tuple's inserting transaction, the
// subset of upstream's XidCommitStatus (verify_heapam.c) that the clog-dependent
// HOT-chain checks branch on. XidStatusUnknown stands in for upstream's
// xmin_commit_status_ok == false (the xid was out of the cluster's valid range,
// invalid, or otherwise undeterminable): the clog-dependent checks skip a tuple
// whose status is unknown, exactly as upstream gates on xmin_commit_status_ok.
// XidStatusCurrent (upstream XID_IS_CURRENT_XID) is kept distinct from
// XidStatusInProgress because the chain checks match the latter strictly — a
// current-transaction xmin must NOT trip the in-progress or root-of-chain
// checks, so collapsing the two would produce false positives.
type XidCommitStatus int

const (
	XidStatusUnknown XidCommitStatus = iota
	XidStatusCommitted
	XidStatusInProgress
	XidStatusAborted
	XidStatusCurrent
)

// XidStatusFunc resolves the commit status of a normal (non-bootstrap,
// non-frozen) transaction id. It is the engine's decoupling seam from goopg's
// clog / proc-array: the verify_heapam SRF supplies a clog-backed implementation
// (committed/aborted from clog, in-progress/current from the proc array), tests
// supply a map. Returning XidStatusUnknown means "could not determine" and makes
// the clog-dependent checks skip that tuple. The engine never calls this for the
// bootstrap (1) or frozen (2 / frozen hint bits) xids — those resolve to
// committed directly, mirroring get_xid_status.
type XidStatusFunc func(xid uint32) XidCommitStatus

// maxAlign rounds n up to the nearest multiple of 8 (PG's MAXALIGN on 64-bit
// platforms; postgres/src/include/c.h).
func maxAlign(n int) int { return (n + 7) &^ 7 }

// bitmapLen mirrors upstream BITMAPLEN(natts): bytes needed for a null bitmap
// covering natts attributes (postgres/src/include/access/htup_details.h).
func bitmapLen(natts int) int { return (natts + 7) / 8 }

// lpEntry caches, per 1-based offset, what the first pass learned about a line
// pointer so the second (HOT-chain) pass can examine links without re-decoding.
// valid mirrors upstream's lp_valid[]: the pointer passed basic sanity (a
// redirect with a valid target, or a normal pointer with sane off/len), so it
// is safe to dereference. successor mirrors upstream's successor[]: the
// same-page offset this pointer points at (a redirect's target, or a normal
// tuple's same-page CTID), or invalidOffsetNumber.
type lpEntry struct {
	flags     storage.ItemIDFlags
	valid     bool
	lpOff     int // tuple-body offset, for LP_NORMAL
	successor uint16
	// xminStatus / xminStatusOK mirror upstream's xmin_commit_status[] /
	// xmin_commit_status_ok[]: the commit status of this tuple's xmin, set in
	// the first pass for valid LP_NORMAL tuples when an XidStatusFunc was
	// supplied. xminStatusOK stays false for redirect/unused/dead pointers, for
	// header-corrupt tuples, and whenever no callback was given, which disables
	// the clog-dependent HOT-chain checks for that tuple — exactly as upstream
	// gates those checks on xmin_commit_status_ok.
	xminStatus   XidCommitStatus
	xminStatusOK bool
}

// VerifyHeapPage runs the page-structural (tier-1) checks of upstream
// verify_heapam against a single heap page. blkno is the page's own block
// number, needed to recognise a tuple's same-page CTID successor (upstream's
// nextblkno == ctx.blkno). It returns one Report per detected structural
// corruption — all first-pass (per-line-pointer) reports in ascending offset
// order, then all HOT-chain-pass reports in ascending offset order, matching
// upstream's two-loop structure — and nil for a clean (or new/empty) page.
//
// A brand-new (all-zero) page carries no line pointers and is reported clean,
// matching upstream's PageGetMaxOffsetNumber == 0 short-circuit. A page whose
// header is too malformed to even count line pointers yields an error (the
// engine cannot safely proceed) rather than a Report — upstream relies on the
// page-header check that runs before this loop.
//
// VerifyHeapPage runs only the checks decidable from the page bytes alone. The
// relation-dependent checks (currently the tuple-natts-vs-table check) are left
// disabled; callers that have the relation's descriptor — the SQL surface —
// should use VerifyHeapPageWithRel.
func VerifyHeapPage(p storage.Page, blkno storage.BlockNumber) ([]Report, error) {
	return verifyHeapPage(p, blkno, RelDesc{}, nil)
}

// VerifyHeapPageWithRel is VerifyHeapPage plus the relation-dependent checks
// driven by rel (verify_heapam.c's check_tuple). A zero-value rel disables
// those checks and makes it identical to VerifyHeapPage.
func VerifyHeapPageWithRel(p storage.Page, blkno storage.BlockNumber, rel RelDesc) ([]Report, error) {
	return verifyHeapPage(p, blkno, rel, nil)
}

// VerifyHeapPageWithXminStatus is VerifyHeapPageWithRel plus the clog-dependent
// HOT-chain checks (verify_heapam.c's second and third update-chain loops),
// driven by xidStatus — a resolver for each tuple's xmin commit status. A nil
// xidStatus disables exactly those three checks and makes this identical to
// VerifyHeapPageWithRel; rel may be zero-value to disable the relation-dependent
// checks independently.
func VerifyHeapPageWithXminStatus(p storage.Page, blkno storage.BlockNumber, rel RelDesc, xidStatus XidStatusFunc) ([]Report, error) {
	return verifyHeapPage(p, blkno, rel, xidStatus)
}

func verifyHeapPage(p storage.Page, blkno storage.BlockNumber, rel RelDesc, xidStatus XidStatusFunc) ([]Report, error) {
	if len(p) != storage.BlockSize {
		return nil, fmt.Errorf("amcheck: page is %d bytes, want %d", len(p), storage.BlockSize)
	}
	if storage.IsNew(p) {
		return nil, nil
	}

	maxoff, err := storage.PageLinePointerCount(p)
	if err != nil {
		return nil, fmt.Errorf("amcheck: cannot determine line pointer count: %w", err)
	}

	var reports []Report
	report := func(off uint16, msg string) {
		reports = append(reports, Report{Offset: off, Msg: msg})
	}

	// entries is 1-indexed by offset number; index 0 is unused.
	entries := make([]lpEntry, maxoff+1)

	// First pass: per-line-pointer sanity, mirroring verify_heapam.c's first
	// loop. Populates entries[].valid and entries[].successor for the second
	// (HOT-chain) pass.
	for off := firstOffsetNumber; off <= maxoff; off++ {
		offnum := uint16(off)
		item, err := storage.PageGetItemID(p, offnum)
		if err != nil {
			return nil, fmt.Errorf("amcheck: reading line pointer %d: %w", offnum, err)
		}
		entries[offnum] = lpEntry{flags: item.Flags, successor: invalidOffsetNumber}

		// Skip unused / dead line pointers: they carry no tuple body.
		if item.Flags == storage.ItemIDUnused || item.Flags == storage.ItemIDDead {
			continue
		}

		// Redirected line pointer: validate the redirect target.
		if item.Flags == storage.ItemIDRedirect {
			rd := item.Offset // ItemIdGetRedirect: target offset number
			if int(rd) < firstOffsetNumber {
				report(offnum, fmt.Sprintf(
					"line pointer redirection to item at offset %d precedes minimum offset %d",
					rd, firstOffsetNumber))
				continue
			}
			if int(rd) > maxoff {
				report(offnum, fmt.Sprintf(
					"line pointer redirection to item at offset %d exceeds maximum offset %d",
					rd, maxoff))
				continue
			}
			rditem, err := storage.PageGetItemID(p, rd)
			if err != nil {
				return nil, fmt.Errorf("amcheck: reading redirect target %d: %w", rd, err)
			}
			switch rditem.Flags {
			case storage.ItemIDUnused:
				report(offnum, fmt.Sprintf(
					"redirected line pointer points to an unused item at offset %d", rd))
			case storage.ItemIDDead:
				report(offnum, fmt.Sprintf(
					"redirected line pointer points to a dead item at offset %d", rd))
			case storage.ItemIDRedirect:
				report(offnum, fmt.Sprintf(
					"redirected line pointer points to another redirected line pointer at offset %d", rd))
			default:
				// Valid redirect to an LP_NORMAL target: record it for the
				// HOT-chain pass.
				entries[offnum].valid = true
				entries[offnum].successor = rd
			}
			continue
		}

		// LP_NORMAL: sanity-check the line pointer's offset and length.
		lpOff := int(item.Offset)
		lpLen := int(item.Length)

		if lpOff != maxAlign(lpOff) {
			report(offnum, fmt.Sprintf(
				"line pointer to page offset %d is not maximally aligned", lpOff))
			continue
		}
		if lpLen < minTupleHeaderSize {
			report(offnum, fmt.Sprintf(
				"line pointer length %d is less than the minimum tuple header size %d",
				lpLen, minTupleHeaderSize))
			continue
		}
		if lpOff+lpLen > storage.BlockSize {
			report(offnum, fmt.Sprintf(
				"line pointer to page offset %d with length %d ends beyond maximum page offset %d",
				lpOff, lpLen, storage.BlockSize))
			continue
		}

		// Tuple header is now safe to examine.
		entries[offnum].valid = true
		entries[offnum].lpOff = lpOff
		headerOK := checkTupleHeader(p, lpOff, lpLen, offnum, report)

		// Resolve this tuple's xmin commit status for the clog-dependent
		// HOT-chain checks (verify_heapam.c populates xmin_commit_status[] here,
		// via check_tuple). Done for every valid LP_NORMAL tuple, gated on a
		// callback having been supplied; the page-bytes-only entry points pass
		// nil so xminStatusOK stays false and those checks are disabled.
		if xidStatus != nil {
			entries[offnum].xminStatus, entries[offnum].xminStatusOK =
				resolveXminStatus(p, lpOff, xidStatus)
		}

		// Relation-dependent check (verify_heapam.c:check_tuple): a tuple may
		// have fewer attributes than the table but never more. Upstream gates
		// this on check_tuple_header succeeding (headerOK) and on the tuple
		// being visible; goopg has no clog for the visibility gate, so we apply
		// it to every header-clean tuple — safe for goopg because a stored
		// natts above the table's column count is structural corruption
		// regardless of visibility, and goopg drops columns logically
		// (attisdropped) rather than shrinking a tuple's natts. Disabled when
		// the relation descriptor is unset (rel.Natts == 0).
		if headerOK && rel.Natts > 0 {
			infomask2 := binary.LittleEndian.Uint16(p[lpOff+18 : lpOff+20])
			natts := int(infomask2 & storage.HeapNattsMask)
			if natts > rel.Natts {
				report(offnum, fmt.Sprintf(
					"number of attributes %d exceeds maximum expected for table %d",
					natts, rel.Natts))
			}
		}

		// If this tuple's CTID points to another tuple on the same page,
		// record that tuple as the successor (verify_heapam.c first loop).
		nextblk := storage.BlockNumber(binary.LittleEndian.Uint32(p[lpOff+12 : lpOff+16]))
		nextoff := binary.LittleEndian.Uint16(p[lpOff+16 : lpOff+18])
		if nextblk == blkno && nextoff != offnum &&
			nextoff >= firstOffsetNumber && int(nextoff) <= maxoff {
			entries[offnum].successor = nextoff
		}
	}

	checkUpdateChains(p, maxoff, entries, report)

	return reports, nil
}

// checkUpdateChains runs the page-structural subset of verify_heapam.c's HOT
// (update) chain validation: a redirect must point at a heap-only tuple, HOT
// chains must not intersect (two pointers reaching the same successor), and a
// chain link's HOT-updated/heap-only flags must agree. The clog-dependent
// checks (xmin commit-status consistency across a link and the "root of chain
// but heap-only" check) are deferred — they require per-tuple commit status
// (XID_COMMITTED / XID_ABORTED / XID_IN_PROGRESS) that the page bytes alone
// cannot supply.
//
// predecessor[off] mirrors upstream's predecessor[]: the offset whose link
// established off as a chain successor; a second pointer reaching off is an
// intersection. HOT-updated is read as the raw t_infomask bit (goopg's
// position — see the package doc), matching upstream's deliberate use of the
// raw bit here rather than HeapTupleHeaderIsHotUpdated.
func checkUpdateChains(p storage.Page, maxoff int, entries []lpEntry, report func(uint16, string)) {
	predecessor := make([]uint16, maxoff+1)

	for off := firstOffsetNumber; off <= maxoff; off++ {
		offnum := uint16(off)
		nextoff := entries[offnum].successor
		// No successor, or the successor isn't a dereferenceable line pointer.
		if nextoff == invalidOffsetNumber || !entries[nextoff].valid {
			continue
		}

		// Current pointer is a redirect: its target must be a heap-only tuple,
		// and chains must not intersect.
		if entries[offnum].flags == storage.ItemIDRedirect {
			// The redirect target was validated LP_NORMAL in the first pass.
			if !isHeapOnly(p, entries[nextoff].lpOff) {
				report(offnum, fmt.Sprintf(
					"redirected line pointer points to a non-heap-only tuple at offset %d", nextoff))
			}
			if predecessor[nextoff] != invalidOffsetNumber {
				report(offnum, fmt.Sprintf(
					"redirect line pointer points to offset %d, but offset %d also points there",
					nextoff, predecessor[nextoff]))
				continue
			}
			predecessor[nextoff] = offnum
			continue
		}

		// Current pointer is LP_NORMAL. A redirect successor cannot be a chain
		// link target here (upstream gives up); only a normal successor whose
		// xmin matches this tuple's update-xmax forms a link.
		if entries[nextoff].flags == storage.ItemIDRedirect {
			continue
		}
		currOff := entries[offnum].lpOff
		nextOff := entries[nextoff].lpOff

		// curr_xmax = HeapTupleHeaderGetUpdateXid: for a non-multi xmax this is
		// the raw xmax. goopg has no on-disk multixact, so a multi xmax can
		// only be injected corruption whose update xid we cannot resolve
		// page-structurally — give up on the link (no false chain).
		currInfomask := readInfomask(p, currOff)
		if currInfomask&heapXmaxIsMulti != 0 {
			continue
		}
		currXmax := rawXmax(p, currOff)
		nextXmin := binary.LittleEndian.Uint32(p[nextOff : nextOff+4])
		if currXmax == 0 || currXmax != nextXmin {
			continue
		}

		// Two tuples linked by xmax==xmin: a HOT/update chain edge.
		if predecessor[nextoff] != invalidOffsetNumber {
			report(offnum, fmt.Sprintf(
				"tuple points to new version at offset %d, but offset %d also points there",
				nextoff, predecessor[nextoff]))
			continue
		}
		predecessor[nextoff] = offnum

		currHotUpdated := currInfomask&storage.HeapHotUpdated != 0
		nextHeapOnly := isHeapOnly(p, nextOff)
		if !currHotUpdated && nextHeapOnly {
			report(offnum, fmt.Sprintf(
				"non-heap-only update produced a heap-only tuple at offset %d", nextoff))
		}
		if currHotUpdated && !nextHeapOnly {
			report(offnum, fmt.Sprintf(
				"heap-only update produced a non-heap only tuple at offset %d", nextoff))
		}

		// Clog-dependent cross-link xmin commit-status checks
		// (verify_heapam.c:759-800). Only run when both tuples' xmin status was
		// determinable (xmin_commit_status_ok for both offsets). The reported
		// offset is the CURRENT tuple's offset and the reported xmins are the
		// frozen-resolved xmins, both verbatim from upstream.
		if entries[offnum].xminStatusOK && entries[nextoff].xminStatusOK {
			currXmin := headerXmin(p, currOff)
			nextXmin := headerXmin(p, nextOff)
			cs := entries[offnum].xminStatus
			ns := entries[nextoff].xminStatus
			switch {
			case cs == XidStatusInProgress && ns == XidStatusCommitted:
				report(offnum, fmt.Sprintf(
					"tuple with in-progress xmin %d was updated to produce a tuple at offset %d with committed xmin %d",
					currXmin, offnum, nextXmin))
			case cs == XidStatusAborted && ns == XidStatusInProgress:
				report(offnum, fmt.Sprintf(
					"tuple with aborted xmin %d was updated to produce a tuple at offset %d with in-progress xmin %d",
					currXmin, offnum, nextXmin))
			case cs == XidStatusAborted && ns == XidStatusCommitted:
				report(offnum, fmt.Sprintf(
					"tuple with aborted xmin %d was updated to produce a tuple at offset %d with committed xmin %d",
					currXmin, offnum, nextXmin))
			}
		}
	}

	// Root-of-chain check (verify_heapam.c:805-833, the third update-chain
	// loop): an update chain can start with a non-heap-only tuple or a redirect
	// line pointer, but never with a heap-only tuple. Run in a separate loop
	// because it needs the fully-populated predecessor[] array. Gated on the
	// tuple's xmin being committed or in-progress and determinable — a redirect/
	// unused/dead pointer never has xminStatusOK, so it is skipped here exactly
	// as upstream's xmin_commit_status_ok gate (the explicit redirect guard
	// mirrors upstream's !ItemIdIsRedirected for clarity).
	for off := firstOffsetNumber; off <= maxoff; off++ {
		offnum := uint16(off)
		if !entries[offnum].xminStatusOK {
			continue
		}
		st := entries[offnum].xminStatus
		if st != XidStatusCommitted && st != XidStatusInProgress {
			continue
		}
		if predecessor[offnum] != invalidOffsetNumber {
			continue
		}
		if entries[offnum].flags == storage.ItemIDRedirect {
			continue
		}
		if isHeapOnly(p, entries[offnum].lpOff) {
			report(offnum, "tuple is root of chain but is marked as heap-only tuple")
		}
	}
}

// resolveXminStatus mirrors get_xid_status (verify_heapam.c) for the subset the
// HOT-chain checks need: it returns the tuple's xmin commit status and whether
// it was determinable (upstream's xmin_commit_status_ok). The bootstrap (1) and
// frozen (2, or the frozen hint bits) xids resolve to committed without
// consulting the callback; the invalid xid (0) is undeterminable; any other xid
// is delegated to fn, and an XidStatusUnknown from fn (out of range /
// undeterminable) maps to ok == false.
func resolveXminStatus(p storage.Page, lpOff int, fn XidStatusFunc) (XidCommitStatus, bool) {
	xmin := headerXmin(p, lpOff)
	switch xmin {
	case 0: // InvalidTransactionId: undeterminable
		return XidStatusUnknown, false
	case 1, uint32(storage.FrozenTransactionID): // bootstrap / frozen: committed
		return XidStatusCommitted, true
	}
	st := fn(xmin)
	if st == XidStatusUnknown {
		return XidStatusUnknown, false
	}
	return st, true
}

// headerXmin returns the tuple's effective xmin (HeapTupleHeaderGetXmin): the
// frozen-transaction id when the frozen hint bits are set (HEAP_XMIN_COMMITTED |
// HEAP_XMIN_INVALID together), otherwise the raw t_xmin field (off+0..4). goopg
// usually freezes by rewriting xmin to FrozenTransactionID(2) directly, but the
// both-hint-bits representation is recognised too so a frozen tuple is never
// mis-resolved through the callback.
func headerXmin(p storage.Page, lpOff int) uint32 {
	infomask := readInfomask(p, lpOff)
	if infomask&(storage.HeapXminCommitted|storage.HeapXminInvalid) ==
		(storage.HeapXminCommitted | storage.HeapXminInvalid) {
		return uint32(storage.FrozenTransactionID)
	}
	return binary.LittleEndian.Uint32(p[lpOff : lpOff+4])
}

// readInfomask returns the tuple's t_infomask (off+20..22).
func readInfomask(p storage.Page, lpOff int) uint16 {
	return binary.LittleEndian.Uint16(p[lpOff+20 : lpOff+22])
}

// isHeapOnly mirrors HeapTupleHeaderIsHeapOnly for goopg's layout: the
// HEAP_ONLY_TUPLE bit lives in t_infomask here (not t_infomask2 — see the
// package doc).
func isHeapOnly(p storage.Page, lpOff int) bool {
	return readInfomask(p, lpOff)&storage.HeapOnlyTuple != 0
}

// checkTupleHeader mirrors verify_heapam.c:check_tuple_header for the checks
// that are decidable from the page bytes alone: the offset/t_hoff consistency
// checks, the multixact-marked-committed invariant, and the HOT-updated-but-
// xmax-0 invariant. The remaining check_tuple_header invariant (heap-only-but-
// not-updated) and the clog/multixact-dependent checks are deferred; see the
// package doc and the design doc.
//
// It returns false when the header is too corrupt to continue with the
// downstream relation-dependent checks (t_hoff beyond the line-pointer length,
// or t_hoff not equal to the expected data offset), mirroring upstream's
// boolean result that gates check_tuple's attribute and natts checks. The
// non-fatal invariants (multixact/HOT) still report but do not flip the result.
//
// On-disk tuple header layout (storage/heap.go MarshalBinary):
//
//	off+0..4  xmin        off+4..8   xmax
//	off+12..16 t_ctid.block (uint32)  off+16..18 t_ctid.offset (uint16)
//	off+18..20 t_infomask2  off+20..22 t_infomask  off+22 t_hoff
//
// Note goopg stores t_ctid.block as a plain uint32 at off+12..16, not as
// upstream's bi_hi/bi_lo BlockIdData split.
func checkTupleHeader(p storage.Page, lpOff, lpLen int, offnum uint16, report func(uint16, string)) bool {
	infomask2 := binary.LittleEndian.Uint16(p[lpOff+18 : lpOff+20])
	infomask := binary.LittleEndian.Uint16(p[lpOff+20 : lpOff+22])
	hoff := int(p[lpOff+22])
	natts := int(infomask2 & storage.HeapNattsMask)

	if hoff > lpLen {
		report(offnum, fmt.Sprintf(
			"data begins at offset %d beyond the tuple length %d", hoff, lpLen))
		return false
	}

	// HEAP_XMAX_COMMITTED with HEAP_XMAX_IS_MULTI is an impossible combination:
	// a multixact xmax is never hint-bit "committed" (verify_heapam.c:1015).
	// Upstream does not skip further checks on this, and neither do we.
	if infomask&storage.HeapXmaxCommitted != 0 && infomask&heapXmaxIsMulti != 0 {
		report(offnum, "multixact should not be marked committed")
	}

	// A HOT-updated tuple must carry a valid xmax pointing at its successor
	// (verify_heapam.c:1029). curr_xmax for a non-multi xmax is the raw xmax
	// field; the multixact case needs a member-table lookup we cannot do
	// page-structurally, so it is skipped. HEAP_HOT_UPDATED is read from
	// t_infomask per goopg's layout (see the package doc).
	if infomask&heapXmaxIsMulti == 0 && isHotUpdated(infomask) && rawXmax(p, lpOff) == 0 {
		report(offnum, "tuple has been HOT updated, but xmax is 0")
	}

	var expectedHoff int
	hasNull := infomask&storage.HeapHasNull != 0
	if hasNull {
		expectedHoff = maxAlign(storage.SizeOfHeapTupleHeaderData + bitmapLen(natts))
	} else {
		expectedHoff = maxAlign(storage.SizeOfHeapTupleHeaderData)
	}
	if hoff != expectedHoff {
		switch {
		case hasNull && natts == 1:
			report(offnum, fmt.Sprintf(
				"tuple data should begin at byte %d, but actually begins at byte %d (1 attribute, has nulls)",
				expectedHoff, hoff))
		case hasNull:
			report(offnum, fmt.Sprintf(
				"tuple data should begin at byte %d, but actually begins at byte %d (%d attributes, has nulls)",
				expectedHoff, hoff, natts))
		case natts == 1:
			report(offnum, fmt.Sprintf(
				"tuple data should begin at byte %d, but actually begins at byte %d (1 attribute, no nulls)",
				expectedHoff, hoff))
		default:
			report(offnum, fmt.Sprintf(
				"tuple data should begin at byte %d, but actually begins at byte %d (%d attributes, no nulls)",
				expectedHoff, hoff, natts))
		}
		return false
	}
	return true
}

// rawXmax returns the tuple's raw t_xmax field (off+4..8). For a non-multi
// xmax this is curr_xmax (HeapTupleHeaderGetUpdateXid's non-multi branch);
// the InvalidTransactionId sentinel is 0.
func rawXmax(p storage.Page, lpOff int) uint32 {
	return binary.LittleEndian.Uint32(p[lpOff+4 : lpOff+8])
}

// isHotUpdated mirrors HeapTupleHeaderIsHotUpdated for goopg's layout: the
// HEAP_HOT_UPDATED bit (in t_infomask here, not t_infomask2 — see the package
// doc) is set, xmax is not marked invalid, and xmin is not marked invalid.
func isHotUpdated(infomask uint16) bool {
	return infomask&storage.HeapHotUpdated != 0 &&
		infomask&storage.HeapXmaxInvalid == 0 &&
		!xminInvalid(infomask)
}

// xminInvalid mirrors HeapTupleHeaderXminInvalid: the xmin hint bits say the
// inserting transaction is known invalid (rolled back).
func xminInvalid(infomask uint16) bool {
	return infomask&(storage.HeapXminCommitted|storage.HeapXminInvalid) == storage.HeapXminInvalid
}
