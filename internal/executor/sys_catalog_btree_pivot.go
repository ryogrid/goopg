package executor

import "encoding/binary"

// Suffix-truncated pivot tuples. A btree PG built — the catalog indexes of a
// base backup taken from a PG primary — carries internal-page pivots with
// fewer key attributes than the index has (nbtinsert.c _bt_truncate): the
// tuple's t_info has INDEX_ALT_TID_MASK set and its t_tid offset holds the
// attribute count. _bt_compare compares only the attributes present; a
// truncated attribute is minus infinity, so a full key equal on the present
// ones sorts AFTER the pivot. goopg's key comparators read full-width keys,
// and slicing a truncated pivot panicked ("slice bounds out of range") on the
// first catalog insert into such an index — CREATE TABLE / CREATE FUNCTION
// on a goopg node promoted from a PG backup. M0149-0016.

// tupleCmpFn compares a search key with a whole index tuple (header
// included), so it can see whether the tuple is a truncated pivot.
type tupleCmpFn func(newKey, item []byte) int

// pivotAwareCmp wraps a full-key comparator with _bt_compare's handling of
// truncated pivots for indexOID. A non-nil conv says the index stores its
// keys in the cstring layout: both operands are converted to the NameData
// layout cmp expects before anything else (M0149-0016e).
func pivotAwareCmp(indexOID uint32, cmp keyCompareFn, conv sysKeyConv) tupleCmpFn {
	meta, _ := keyMetaForSysBtree(indexOID)
	nkeyatts := int(meta.nkeyatts)
	return func(newKey, item []byte) int {
		if len(item) < sysIndexTupleHoff {
			return 1
		}
		payload := item[sysIndexTupleHoff:]
		natts, truncated := pivotNAtts(item, nkeyatts)
		if truncated && natts == 0 {
			return 1 // all attributes truncated: minus infinity
		}
		if conv != nil {
			newKey = conv(newKey, nkeyatts)
			payload = conv(payload, natts)
		}
		if !truncated {
			return cmp(newKey, payload)
		}
		end, ok := sysBtreeKeyPrefixLen(indexOID, payload, natts)
		if !ok || end > len(payload) {
			return 1
		}
		// The present attributes from the pivot, the truncated ones from the
		// key itself, so the comparator decides on the present ones only.
		padded := make([]byte, 0, max(end, len(newKey)))
		padded = append(padded, payload[:end]...)
		if end < len(newKey) {
			padded = append(padded, newKey[end:]...)
		}
		if c := cmp(newKey, padded); c != 0 {
			return c
		}
		return 1 // equal on the present attributes: key > pivot
	}
}

// pivotNAtts is BTreeTupleGetNAtts: the tuple's key-attribute count, and
// whether it is a suffix-truncated pivot (fewer than nkeyatts).
func pivotNAtts(item []byte, nkeyatts int) (int, bool) {
	tInfo := binary.LittleEndian.Uint16(item[6:8])
	if tInfo&indexAltTIDMask == 0 || nkeyatts == 0 {
		return nkeyatts, false
	}
	natts := int(binary.LittleEndian.Uint16(item[4:6]) & btOffsetMask)
	return natts, natts < nkeyatts
}

// sysBtreeKeyPrefixLen is the byte length, within a key payload, of its
// first natts attributes.
func sysBtreeKeyPrefixLen(indexOID uint32, payload []byte, natts int) (int, bool) {
	if indexOID == pgProcPronameArgsNspIndexOID {
		// (proname name, proargtypes oidvector, pronamespace oid)
		switch natts {
		case 1:
			return 64, true
		case 2:
			if len(payload) < 68 {
				return 0, false
			}
			return 64 + int(binary.LittleEndian.Uint32(payload[64:68])>>2), true
		}
		return 0, false
	}
	types, ok := sysBtreeKeyTypes(indexOID)
	if !ok || natts > len(types) {
		return 0, false
	}
	off := 0
	for i := range natts {
		l, a := catKeyLenAlign(types[i])
		off = (off+a-1)/a*a + l
	}
	return off, true
}

// sysBtreeKeyTypes lists the key attribute types of the multi-attribute
// catalog indexes goopg maintains (the layouts their buildIndexTuple* and
// cmpKey* functions use); single-attribute indexes are never truncated.
func sysBtreeKeyTypes(indexOID uint32) ([]catKeyType, bool) {
	const (
		O = catKeyOid
		I = catKeyInt4
		S = catKeyInt2
		C = catKeyChar
		N = catKeyName
	)
	switch indexOID {
	case pgClassRelnameNspIndexOID, pgTypeTypnameNspIndexOID, pgTSConfigNameNspIndexOID,
		pgTSDictNameNspIndexOID, pgConversionNameNspIndexOID:
		return []catKeyType{N, O}, true
	case pgAttributeRelidAttnumIndexOID, pgAttrdefAdrelidAdnumIndexOID:
		return []catKeyType{O, S}, true
	case pgEnumTypidLabelIndexOID, pgRewriteRelRulenameIndexOID, pgAttributeRelidAttnamIndexOID:
		return []catKeyType{O, N}, true
	case pgEnumTypidSortOrderIndexID: // (oid, float4): float4 lays out as int4
		return []catKeyType{O, I}, true
	case pgCastSourceTargetIndexOID, pgUserMappingUserSrvIdx, pgPublicationRelPrrelidPrpubOID,
		pgTransformTypeLangIndexOID:
		return []catKeyType{O, O}, true
	case pgOperatorOprnameLRNIndexOID:
		return []catKeyType{N, O, O, O}, true
	case pgTSConfigMapIndexOID:
		return []catKeyType{O, I, I}, true
	case pgOpfamilyAmNameNspIndexOID, pgOpclassAmNameNspIndexOID:
		return []catKeyType{O, N, O}, true
	case pgAmopFamStratIndexOID, pgAmprocFamProcIndexOID:
		return []catKeyType{O, O, O, S}, true
	case pgAmopOprFamIndexOID:
		return []catKeyType{O, C, O}, true
	case pgCollationNameEncNspIndexID:
		return []catKeyType{N, I, O}, true
	case pgConversionDefaultIndexOID:
		return []catKeyType{O, I, I, O}, true
	}
	if d, ok := catalogIndexDefByOID[indexOID]; ok {
		out := make([]catKeyType, len(d.keys))
		for i, k := range d.keys {
			out[i] = k.typ
		}
		return out, true
	}
	return nil, false
}
