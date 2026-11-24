package executor

import (
	"bytes"
	"encoding/binary"

	"github.com/goopg/goopg/internal/storage"
)

// cstring-keyed name columns in PG-built catalog btrees. PG's btree name_ops
// opclass has opckeytype = cstring (pg_opclass.dat), so the index's
// attribute is typed cstring and index_form_tuple stores a name key as its
// bytes plus a NUL — not the 64-byte NameData the heap carries — and the
// tuple has INDEX_VAR_MASK set. goopg's own initdb builds these indexes with
// 64-byte NameData keys, and its comparators and tuple builders assume that
// layout. On a goopg node started from a PG base backup (a goopg standby of
// a PG primary, later promoted) the first catalog insert into such an index
// — CREATE TABLE inserting into pg_class_relname_nsp_index — read past the
// short cstring key and panicked. M0149-0016e.
//
// Which layout an index uses is read off the index itself: the leftmost
// leaf's data tuples. New tuples are written in that layout (so a PG node
// that later streams or restarts from these files reads them with its
// cstring tuple descriptor), and every comparison converts both operands
// back to the NameData layout the comparators expect.

// sysKeyConv rewrites the first natts attributes of a cstring-layout key
// payload into the NameData layout. It returns the payload unchanged when it
// cannot be decoded.
type sysKeyConv func(payload []byte, natts int) []byte

// keyAttrLayout is one key attribute of a catalog index: a name, a 4-byte-
// header varlena (oidvector), or a fixed-width value.
type keyAttrLayout struct {
	name    bool
	varlena bool
	length  int
	align   int
}

// sysBtreeAttrLayouts lists indexOID's key attributes in index order.
func sysBtreeAttrLayouts(indexOID uint32) ([]keyAttrLayout, bool) {
	switch indexOID {
	case pgProcPronameArgsNspIndexOID: // (proname, proargtypes oidvector, pronamespace)
		return []keyAttrLayout{{name: true}, {varlena: true, align: 4}, {length: 4, align: 4}}, true
	case pgNamespaceNspnameIndexOID, pgAuthidRolnameIndexOID, pgDatabaseDatnameIndexOID:
		return []keyAttrLayout{{name: true}}, true
	}
	types, ok := sysBtreeKeyTypes(indexOID)
	if !ok {
		return nil, false
	}
	out := make([]keyAttrLayout, len(types))
	for i, t := range types {
		if t == catKeyName {
			out[i] = keyAttrLayout{name: true}
			continue
		}
		l, a := catKeyLenAlign(t)
		out[i] = keyAttrLayout{length: l, align: a}
	}
	return out, true
}

func layoutHasName(lay []keyAttrLayout) bool {
	for _, a := range lay {
		if a.name {
			return true
		}
	}
	return false
}

func layoutHasVarlena(lay []keyAttrLayout) bool {
	for _, a := range lay {
		if a.varlena {
			return true
		}
	}
	return false
}

func alignUp(n, a int) int {
	if a <= 1 {
		return n
	}
	return (n + a - 1) / a * a
}

func padTo(b []byte, a int) []byte {
	for len(b) != alignUp(len(b), a) {
		b = append(b, 0)
	}
	return b
}

// nameDataToCstringPayload converts a NameData-layout key payload into the
// layout PG's cstring tuple descriptor reads.
func nameDataToCstringPayload(lay []keyAttrLayout, payload []byte) ([]byte, bool) {
	out := make([]byte, 0, len(payload))
	in := 0
	for _, a := range lay {
		switch {
		case a.name:
			if in+64 > len(payload) {
				return nil, false
			}
			s := payload[in : in+64]
			if n := bytes.IndexByte(s, 0); n >= 0 {
				s = s[:n]
			}
			out = append(out, s...)
			out = append(out, 0)
			in += 64
		case a.varlena:
			in = alignUp(in, 4)
			if in+4 > len(payload) {
				return nil, false
			}
			l := int(binary.LittleEndian.Uint32(payload[in:]) >> 2)
			if l < 4 || in+l > len(payload) {
				return nil, false
			}
			out = padTo(out, 4)
			out = append(out, payload[in:in+l]...)
			in += l
		default:
			in = alignUp(in, a.align)
			if in+a.length > len(payload) {
				return nil, false
			}
			out = padTo(out, a.align)
			out = append(out, payload[in:in+a.length]...)
			in += a.length
		}
	}
	return out, true
}

// cstringToNameDataPayload converts the first natts attributes of a
// cstring-layout key payload into the NameData layout.
func cstringToNameDataPayload(lay []keyAttrLayout, payload []byte, natts int) ([]byte, bool) {
	if natts > len(lay) {
		natts = len(lay)
	}
	out := make([]byte, 0, len(payload)+64)
	in := 0
	for _, a := range lay[:natts] {
		switch {
		case a.name:
			if in > len(payload) {
				return nil, false
			}
			n := bytes.IndexByte(payload[in:], 0)
			if n < 0 || n > 63 {
				return nil, false
			}
			var nd [64]byte
			copy(nd[:], payload[in:in+n])
			out = append(out, nd[:]...)
			in += n + 1
		case a.varlena:
			in = alignUp(in, 4)
			if in+4 > len(payload) {
				return nil, false
			}
			l := int(binary.LittleEndian.Uint32(payload[in:]) >> 2)
			if l < 4 || in+l > len(payload) {
				return nil, false
			}
			out = padTo(out, 4)
			out = append(out, payload[in:in+l]...)
			in += l
		default:
			in = alignUp(in, a.align)
			if in+a.length > len(payload) {
				return nil, false
			}
			out = padTo(out, a.align)
			out = append(out, payload[in:in+a.length]...)
			in += a.length
		}
	}
	return out, true
}

// nameDataTupleToCstring re-forms a goopg-built index tuple (NameData keys)
// as index_form_tuple would with a cstring tuple descriptor: same TID, keys
// in cstring layout, INDEX_VAR_MASK set, size MAXALIGN'd.
func nameDataTupleToCstring(lay []keyAttrLayout, tup []byte) ([]byte, bool) {
	if len(tup) < sysIndexTupleHoff {
		return nil, false
	}
	payload, ok := nameDataToCstringPayload(lay, tup[sysIndexTupleHoff:])
	if !ok {
		return nil, false
	}
	size := alignUp(sysIndexTupleHoff+len(payload), 8)
	out := make([]byte, size)
	copy(out, tup[:sysIndexTupleHoff])
	copy(out[sysIndexTupleHoff:], payload)
	le := binary.LittleEndian
	tinfo := le.Uint16(out[6:8]) &^ sysIndexSizeMask
	le.PutUint16(out[6:8], tinfo|indexVarMask|uint16(size)&sysIndexSizeMask)
	return out, true
}

// indexVarMask is INDEX_VAR_MASK: the tuple has variable-width attributes.
const indexVarMask uint16 = 0x4000

// cstringKeyConv is the sysKeyConv for an index laid out as lay.
func cstringKeyConv(lay []keyAttrLayout) sysKeyConv {
	return func(payload []byte, natts int) []byte {
		if out, ok := cstringToNameDataPayload(lay, payload, natts); ok {
			return out
		}
		return payload
	}
}

// wrapCstringCmp makes a NameData comparator compare cstring-layout keys.
func wrapCstringCmp(cmp keyCompareFn, conv sysKeyConv, nkeyatts int) keyCompareFn {
	return func(a, b []byte) int {
		return cmp(conv(a, nkeyatts), conv(b, nkeyatts))
	}
}

// sysBtreeUsesCstringKeys reports whether the catalog btree rel stores its
// name keys as cstrings (PG-built), judged from the leftmost leaf's data
// tuples. An index whose leftmost leaf is empty reads as goopg's layout.
func sysBtreeUsesCstringKeys(ctx *Context, rel storage.RelFileNode, lay []keyAttrLayout, rootBlk, level uint32) bool {
	blk := rootBlk
	for lvl := level; lvl > 0; lvl-- {
		img, err := readSysBtreePageImage(ctx, rel, blk)
		if err != nil || len(img.items) == 0 {
			return false
		}
		blk = decodeChildFromTID(img.items[0])
	}
	img, err := readSysBtreePageImage(ctx, rel, blk)
	if err != nil {
		return false
	}
	varlena := layoutHasVarlena(lay)
	for _, it := range img.items {
		if len(it) <= sysIndexTupleHoff {
			continue
		}
		tinfo := binary.LittleEndian.Uint16(it[6:8])
		if !varlena {
			// goopg's NameData layout has only fixed-width attributes, so
			// INDEX_VAR_MASK can only come from a cstring.
			return tinfo&indexVarMask != 0
		}
		if cs, decided := cstringLayoutOfVarlenaKey(lay, it[sysIndexTupleHoff:]); decided {
			return cs
		}
	}
	return false
}

// cstringLayoutOfVarlenaKey decides the layout of a (name, oidvector, …) key
// payload — pg_proc_proname_args_nsp_index, whose tuples are var-width in
// both layouts. In the NameData layout the bytes after the name's NUL up to
// offset 64 are zero, so where a cstring layout would put the oidvector
// header there is a zero word; in the cstring layout it is a real header. A
// name long enough that both layouts put the oidvector at 64 cannot decide.
func cstringLayoutOfVarlenaKey(lay []keyAttrLayout, payload []byte) (cs, decided bool) {
	if len(lay) < 2 || !lay[0].name || !lay[1].varlena || len(payload) < 4 {
		return false, false
	}
	lim := min(64, len(payload))
	n := bytes.IndexByte(payload[:lim], 0)
	if n < 0 {
		return false, false
	}
	p := alignUp(n+1, 4)
	if p >= 64 || p+4 > len(payload) {
		return false, false
	}
	return binary.LittleEndian.Uint32(payload[p:]) != 0, true
}
