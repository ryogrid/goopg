package btree

import (
	"encoding/binary"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-ii-B1 — the item-codec SEAM, behaviour-preserving.
//
// 3b-2c-i routed every ORDERING decision through one per-index object, and
// 3b-2c-ii-A routed every btree open/create through one place that resolves
// that object's descriptor. What is still hard-wired is the third thing the
// descriptor decides: what an `item.key` IS, and therefore how an item is laid
// out on a page.
//
//   - desc == nil (today, every index): `item.key` is goopg's opaque
//     order-preserving payload, and an on-page item is
//     `IndexTupleData(t_tid, t_info) || key` — the header is SYNTHESISED at
//     marshal time from `item.ptr`, and stripped at parse time.
//   - desc != nil (the flip): `item.key` is the WHOLE `FormPGIndexTuple`
//     image, header included, because that is the only operand shape
//     `ComparePGIndexTuples` can order — it needs t_info's null bitmap and
//     t_tid's attribute count and heap TID (see pgkeycmp.go). Marshalling is
//     then very nearly the identity, and parsing keeps the header instead of
//     discarding it.
//
// So the same ~30 call sites that say `it.marshal()` / `parseItem(raw)` /
// `SizeOfIndexTupleData + len(it.key)` today have to say something the index's
// descriptor answers. This file is that answer, and — exactly like 3b-2c-i —
// nothing more: `tupleKeys()` is `desc != nil`, every production descriptor is
// still nil (`pgIndexTupleKeys` is false in internal/executor), so every
// method below takes its blob branch and the on-disk format does not move.
//
// Why the seam is worth its own slice: the flip must be atomic across the
// writers, the comparison layer AND the layout, and "did every layout site
// learn about the format?" is a question about thirty call sites which is far
// better answered against a tree whose bytes have not changed. The tuple
// branches here are therefore written and TESTED (pgitemcodec_test.go) ahead
// of the writers that will produce their input.
//
// NOT converted in B1, deliberately: the package's exported page readers
// (PageItemKeys / PageLeafItems / PageDownlinks / PageHighKey) and the two WAL
// redo entry points (ApplyInsertRecord, ReplayRemoveParentDownlink) had no
// index identity to resolve a descriptor from, so they named `blobFormat`
// explicitly. B2-b (below) turns the format into a parameter of all six; the
// remaining question — can the caller answer it? — is now asked by the
// compiler at each of them.
//
// See docs/design/0130-0011-nbtree-pg-on-disk-format.md.
// ---------------------------------------------------------------------------

// blobFormat is the pre-flip format: opaque order-preserving key payloads,
// bytewise order. It is the zero value, spelled out at the call sites that
// cannot resolve an index descriptor so that "this site assumes the blob
// format" is greppable rather than implied by an omitted argument.
var blobFormat = indexFormat{}

// ---------------------------------------------------------------------------
// M0130-S11.4 slice 3b-2c-ii-B2-b — the format becomes an ARGUMENT of the
// out-of-package page readers.
//
// B1 left six sites naming `blobFormat` because they read a page without
// holding a BTree: the four exported page readers (amcheck's decoders) and the
// two WAL redo entry points. Hard-wiring there is not a missing plumbing
// detail, it is a wrong claim — "every tree on this cluster is blob-formatted"
// — that the flip turns into silent misparsing, because a descriptor-ordered
// tuple's key IS the tuple (header included) and a blob parse strips exactly
// the bytes the comparison needs.
//
// So the readers move onto IndexFormat: the caller must now say which format
// the page it is handing over is in. Today every caller resolves to the blob
// format (the zero value), so nothing on disk or in behaviour moves — but
// "who still cannot resolve a descriptor?" is now a compile-time question
// about a parameter rather than a grep for an identifier.
//
// The two redo entry points keep taking the format the same way, but the WAL
// side genuinely cannot resolve one yet: recovery has a relfilenode and no
// catalog. That is B2-b-ii and has its own ledger row; see ApplyInsertRecord.
// ---------------------------------------------------------------------------

// IndexFormat is one index's on-page key format, as seen from OUTSIDE the
// package: what an item's key bytes mean, and therefore how a page's items
// decode. It is the exported face of indexFormat — out-of-package callers get
// the readers and the redo entry points, not the ordering and layout internals
// the tree itself uses.
//
// The zero value is the blob format (goopg's opaque order-preserving key
// payloads, bytewise order), which is what every index resolves to until
// 3b-2c-ii-B2-c flips the writers. Callers that hold an index's catalog entry
// should build one with IndexFormatFor; callers that hold a live tree should
// ask it with (*BTree).Format so the reader can never disagree with the tree
// that wrote the page.
type IndexFormat struct{ f indexFormat }

// IndexFormatFor returns the format of an index whose key descriptor is
// `desc`. A nil descriptor is the blob format, matching (*BTree).KeyDesc and
// Options.KeyDesc: one nil-means-blob convention across the whole seam.
func IndexFormatFor(desc *PGIndexKeyDesc) IndexFormat {
	return IndexFormat{f: indexFormat{desc: desc}}
}

// Format returns the format this tree reads and writes its pages in, so an
// out-of-package reader of one of its pages decodes it the same way the tree
// does.
func (bt *BTree) Format() IndexFormat { return IndexFormat{f: bt.keyFmt} }

// KeyDesc reports the key descriptor this format decodes by, or nil for the
// blob format.
func (fm IndexFormat) KeyDesc() *PGIndexKeyDesc { return fm.f.desc }

// tupleKeys reports whether `item.key` is a whole nbtree tuple rather than a
// bare key payload. It is exactly "this index has a key descriptor": the
// descriptor is both what makes the tuple shape necessary (nothing else can
// deform the key) and what makes it possible (nothing else knows the
// attributes' layout).
func (f indexFormat) tupleKeys() bool { return f.desc != nil }

// marshal encodes an item as its on-page nbtree tuple.
//
// Blob format: `PGBTItemRaw`/`PGBTPivotRaw` build the IndexTupleData header
// around the payload — see their comments for the field-by-field layout.
//
// Tuple format: the item's key already IS the tuple, so marshalling only has
// to reconcile the header with the fields the in-memory item holds
// authoritatively (`item.ptr`, `item.pivot`), and to leave the caller's slice
// alone while doing it.
func (f indexFormat) marshal(it item) []byte {
	if !f.tupleKeys() {
		return it.marshal()
	}
	raw := append([]byte(nil), it.key...)
	if len(raw) < SizeOfIndexTupleData {
		// Defensive: a tuple-format key shorter than a bare header cannot be
		// a tuple at all. Refusing here would need an error return in
		// sort/insert paths that have nowhere to put one, so pad to the
		// minimum legal tuple — a zero-attribute pivot — which the descent
		// treats as minus infinity and amcheck reports as corrupt.
		raw = append(raw, make([]byte, SizeOfIndexTupleData-len(raw))...)
		pgPutIndexTupleSize(raw, len(raw))
	}
	if it.pivot {
		// A pivot's t_tid is the downlink plus the nbtree status bits, never a
		// heap TID (upstream BTreeTupleSetDownLink + BTreeTupleSetNAtts).
		natts := uint16(f.desc.NKeyAtts())
		if PGIndexTupleIsAltTID(raw) {
			// Already a pivot (a parse → re-marshal round trip, e.g. the split
			// redistribution and the vacuum page rewrites): keep the attribute
			// count it was truncated to instead of un-truncating it.
			natts = BTreeTupleGetNAtts(raw, uint16(f.desc.NKeyAtts()))
		} else if len(raw) == SizeOfIndexTupleData {
			natts = 0 // negative infinity: no key attributes at all
		}
		BTreeTupleSetDownLink(raw, it.ptr.Block)
		if err := BTreeTupleSetNAtts(raw, natts, false); err != nil {
			// Unreachable: natts is bounded by the descriptor's key count,
			// which FormPGIndexTuple already validated against IndexMaxKeys.
			panic(err)
		}
		return raw
	}
	// A plain leaf item's t_tid is the heap TID. FormPGIndexTuple stamped one,
	// but the item may have been rebuilt since (posting-list split, dedup
	// recovery), and `item.ptr` is what every in-package caller reasons about.
	PutPGItemPointer(raw, it.ptr)
	return raw
}

// parse decodes an on-page tuple into an item, copying the key so the result
// outlives the page buffer.
func (f indexFormat) parse(raw []byte) (item, error) {
	it, err := f.parseAliasing(raw)
	if err != nil {
		return item{}, err
	}
	it.key = append([]byte(nil), it.key...)
	return it, nil
}

// parseNoCopy is parse without the key copy: the returned `item.key` ALIASES
// `raw`, so it is only valid while the caller holds the page pinned and
// unmodified (M0091-0002 — the range-scan hot path).
func (f indexFormat) parseNoCopy(raw []byte) (item, error) { return f.parseAliasing(raw) }

// parseAliasing is the shared body. The blob branch delegates to the existing
// decoders so the header validation lives in ONE place (parseItemBody).
func (f indexFormat) parseAliasing(raw []byte) (item, error) {
	if !f.tupleKeys() {
		return parseItemNoCopy(raw)
	}
	ptr, _, pivot, err := parseItemBody(raw)
	if err != nil {
		return item{}, err
	}
	// The difference from the blob branch, and the whole point of this file:
	// the key is the tuple, header included, not the body after it.
	return item{ptr: ptr, key: raw, pivot: pivot}, nil
}

// bodySize is the number of on-page bytes an item body occupies for a key
// operand of len(key) bytes — i.e. what the header adds, or does not.
func (f indexFormat) bodySize(key []byte) int {
	if f.tupleKeys() {
		return len(key)
	}
	return SizeOfIndexTupleData + len(key)
}

// itemEncodedSize returns the bytes `it` occupies on a page INCLUDING its
// 4-byte line pointer. It is the single source of truth for the budget both
// pageHasSpaceFor and PageInsertItemRawAt depend on — they were once computed
// from different expressions and a mismatch at a nearly-full leaf triggered the
// "not enough free space in page" panic (root-0040).
func (f indexFormat) itemEncodedSize(it item) int {
	const itemIDSize = 4 // matches storage.itemIDSize
	return itemIDSize + f.bodySize(it.key)
}

// pageHasSpaceFor reports whether `it` would fit on `p` if appended.
func (f indexFormat) pageHasSpaceFor(p storage.Page, it item) bool {
	h := storage.MustHeader(p)
	free := int(h.Upper()) - int(h.Lower())
	return free >= f.itemEncodedSize(it)
}

// pgPutIndexTupleSize rewrites t_info's size half, preserving its flag bits.
func pgPutIndexTupleSize(raw []byte, size int) {
	if len(raw) < SizeOfIndexTupleData {
		panic(fmt.Sprintf("btree: pgPutIndexTupleSize on a %d-byte tuple", len(raw)))
	}
	binary.LittleEndian.PutUint16(raw[6:8], (pgTInfo(raw)&^uint16(IndexSizeMask))|uint16(size))
}
