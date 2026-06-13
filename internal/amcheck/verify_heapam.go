// Package amcheck implements the page-structural core of upstream amcheck's
// verify_heapam() — the heap-page integrity checker
// (postgres/contrib/amcheck/verify_heapam.c).
//
// This is the reusable engine; the SQL surface that exposes it (CREATE
// EXTENSION amcheck + the verify_heapam set-returning function) is wired in a
// later loop. See docs/design/0110-0005-verify-heapam-engine.md for scope and
// the engine-first rationale.
//
// Only the page-structural tier is implemented here: line-pointer bounds and
// alignment, redirect-target validity, and tuple-header offset/t_hoff
// consistency, plus the two infomask-only invariants from check_tuple_header
// (a multixact xmax must not be hint-bit "committed"; a HOT-updated tuple must
// carry a valid xmax). These are deterministic functions of the raw 8 KiB page
// bytes and need neither clog, the relation TupleDesc, nor the toast relation.
// The HOT-chain tier (successor/predecessor consistency) and the
// MVCC/attribute tier (xmin/xmax bounds vs clog, multixact members, TOAST
// pointer validation) are deferred; see the design doc.
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

// maxAlign rounds n up to the nearest multiple of 8 (PG's MAXALIGN on 64-bit
// platforms; postgres/src/include/c.h).
func maxAlign(n int) int { return (n + 7) &^ 7 }

// bitmapLen mirrors upstream BITMAPLEN(natts): bytes needed for a null bitmap
// covering natts attributes (postgres/src/include/access/htup_details.h).
func bitmapLen(natts int) int { return (natts + 7) / 8 }

// VerifyHeapPage runs the page-structural (tier-1) checks of upstream
// verify_heapam against a single heap page. It returns one Report per detected
// structural corruption, in ascending offset order, and nil for a clean (or
// new/empty) page.
//
// A brand-new (all-zero) page carries no line pointers and is reported clean,
// matching upstream's PageGetMaxOffsetNumber == 0 short-circuit. A page whose
// header is too malformed to even count line pointers yields an error (the
// engine cannot safely proceed) rather than a Report — upstream relies on the
// page-header check that runs before this loop.
func VerifyHeapPage(p storage.Page) ([]Report, error) {
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

	for off := firstOffsetNumber; off <= maxoff; off++ {
		offnum := uint16(off)
		item, err := storage.PageGetItemID(p, offnum)
		if err != nil {
			return nil, fmt.Errorf("amcheck: reading line pointer %d: %w", offnum, err)
		}

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
			switch {
			case rditem.Flags == storage.ItemIDUnused:
				report(offnum, fmt.Sprintf(
					"redirected line pointer points to an unused item at offset %d", rd))
			case rditem.Flags == storage.ItemIDDead:
				report(offnum, fmt.Sprintf(
					"redirected line pointer points to a dead item at offset %d", rd))
			case rditem.Flags == storage.ItemIDRedirect:
				report(offnum, fmt.Sprintf(
					"redirected line pointer points to another redirected line pointer at offset %d", rd))
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
		checkTupleHeader(p, lpOff, lpLen, offnum, report)
	}

	return reports, nil
}

// checkTupleHeader mirrors verify_heapam.c:check_tuple_header for the checks
// that are decidable from the page bytes alone: the offset/t_hoff consistency
// checks, the multixact-marked-committed invariant, and the HOT-updated-but-
// xmax-0 invariant. The remaining check_tuple_header invariant (heap-only-but-
// not-updated) and the clog/multixact-dependent checks are deferred; see the
// package doc and the design doc.
//
// On-disk tuple header layout (storage/heap.go MarshalBinary):
//
//	off+4..8 xmax   off+18..20 t_infomask2   off+20..22 t_infomask   off+22 t_hoff
func checkTupleHeader(p storage.Page, lpOff, lpLen int, offnum uint16, report func(uint16, string)) {
	infomask2 := binary.LittleEndian.Uint16(p[lpOff+18 : lpOff+20])
	infomask := binary.LittleEndian.Uint16(p[lpOff+20 : lpOff+22])
	hoff := int(p[lpOff+22])
	natts := int(infomask2 & storage.HeapNattsMask)

	if hoff > lpLen {
		report(offnum, fmt.Sprintf(
			"data begins at offset %d beyond the tuple length %d", hoff, lpLen))
		return
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
	}
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
