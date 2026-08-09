package mvcc

import (
	"github.com/goopg/goopg/internal/storage"
)

// ComboCIDKey is the hash key for a combo CID: the (cmin, cmax) pair that
// the combo CID represents. Mirrors PostgreSQL's comboCids hash table key
// (combocid.c:52-58).
type ComboCIDKey struct {
	Cmin storage.CommandId
	Cmax storage.CommandId
}

// ComboCIDStore maps (cmin, cmax) pairs to synthetic combo CID numbers and
// back, mirroring PostgreSQL's comboCids hash table and array
// (postgres/src/backend/utils/time/combocid.c). It is owned by the
// transaction (or per-backend) and cleared at transaction end.
//
// When a tuple is both inserted and deleted within the same transaction at
// DIFFERENT command ids, the 4-byte t_field3 union can only hold one
// CommandId, but the tuple must record BOTH the inserting cmin and the
// deleting cmax. PG maps the pair to a surrogate "combo CID" number and
// sets HEAP_COMBOCID in infomask. Later, GetCmin/GetCmax resolve the combo
// CID back to the real cmin or cmax.
//
// The store is nil-safe: all methods on a nil store return sensible
// defaults (GetCmin/GetCmax return the raw t_cid as a plain command id).
type ComboCIDStore struct {
	hash  map[ComboCIDKey]storage.CommandId // (cmin, cmax) → combo CID
	array []ComboCIDKey                     // indexed by combo CID → (cmin, cmax)
}

// GetComboCommandId returns the combo CID for the (cmin, cmax) pair,
// allocating a new one if the pair hasn't been seen before. Mirrors
// PostgreSQL's GetComboCommandId (combocid.c:112-138).
func (cs *ComboCIDStore) GetComboCommandId(cmin, cmax storage.CommandId) storage.CommandId {
	if cs.hash == nil {
		cs.hash = make(map[ComboCIDKey]storage.CommandId)
	}
	key := ComboCIDKey{Cmin: cmin, Cmax: cmax}
	if cid, ok := cs.hash[key]; ok {
		return cid
	}
	// Allocate a new combo CID. Combo CID 0 is unused (reserved as
	// "not a combo"), so the array index starts at 1. PG uses 1-based
	// indexing for comboCids too (combocid.c:74, usedComboCids++).
	cid := storage.CommandId(len(cs.array) + 1)
	cs.hash[key] = cid
	cs.array = append(cs.array, key)
	return cid
}

// GetRealCmin resolves a combo CID back to the real cmin. When the store is
// nil or the combo CID is out of range, returns the raw value unchanged
// (the caller passes the raw t_cid from the tuple header). Mirrors
// PostgreSQL's HeapTupleHeaderGetCmin (combocid.c:104-122).
func (cs *ComboCIDStore) GetRealCmin(comboCID storage.CommandId) storage.CommandId {
	if cs == nil || comboCID == 0 || int(comboCID) > len(cs.array) {
		return comboCID
	}
	return cs.array[comboCID-1].Cmin
}

// GetRealCmax resolves a combo CID back to the real cmax. When the store is
// nil or the combo CID is out of range, returns the raw value unchanged.
// Mirrors PostgreSQL's HeapTupleHeaderGetCmax (combocid.c:126-140).
func (cs *ComboCIDStore) GetRealCmax(comboCID storage.CommandId) storage.CommandId {
	if cs == nil || comboCID == 0 || int(comboCID) > len(cs.array) {
		return comboCID
	}
	return cs.array[comboCID-1].Cmax
}

// GetCmin returns the real cmin for a heap tuple header, resolving combo
// CIDs through the store when needed. When the store is nil or
// HEAP_COMBOCID is clear, the raw t_cid is returned directly (cmin == cmax
// in that case). Mirrors PostgreSQL's HeapTupleHeaderGetCmin.
func GetCmin(h storage.HeapTupleHeader, cs *ComboCIDStore) storage.CommandId {
	raw := h.GetRawCommandId()
	if !storage.IsHeapComboCID(h.Infomask) {
		return raw
	}
	return cs.GetRealCmin(raw)
}

// GetCmax returns the real cmax for a heap tuple header, resolving combo
// CIDs through the store when needed. When the store is nil or
// HEAP_COMBOCID is clear, the raw t_cid is returned directly. Mirrors
// PostgreSQL's HeapTupleHeaderGetCmax.
func GetCmax(h storage.HeapTupleHeader, cs *ComboCIDStore) storage.CommandId {
	raw := h.GetRawCommandId()
	if !storage.IsHeapComboCID(h.Infomask) {
		return raw
	}
	return cs.GetRealCmax(raw)
}

// AdjustCmax ensures the tuple header correctly records both cmin and cmax
// when they differ. Called at every DELETE/UPDATE of a self-inserted tuple.
//
// When the header already has a cmin (stamped at insert) that differs from
// the deleting command id, a combo CID is allocated to represent the pair
// (cmin, deletingCID) and the header is updated with the combo CID +
// HEAP_COMBOCID. When cmin equals deletingCID, no combo is needed — the
// header's existing t_cid serves as both cmin and cmax.
//
// Mirrors PostgreSQL's HeapTupleHeaderAdjustCmax (combocid.c:146-168).
// cs may be nil — in that case no adjustment is done (the raw CommandId in
// the header is used for both cmin and cmax, and the caller stamps cmax
// directly via SetCmax without the combo flag).
func AdjustCmax(h *storage.HeapTupleHeader, deletingCID storage.CommandId, cs *ComboCIDStore) {
	if cs == nil {
		return
	}
	if storage.IsHeapComboCID(h.Infomask) {
		// Already a combo CID — the real cmin is in the store. Check
		// whether the deleting cmd differs from the real cmin.
		raw := h.GetRawCommandId()
		realCmin := cs.GetRealCmin(raw)
		if realCmin != deletingCID {
			newCombo := cs.GetComboCommandId(realCmin, deletingCID)
			h.SetCmax(newCombo, true)
		} else {
			// cmin == cmax: clear the combo, store as plain cid.
			h.SetCmax(deletingCID, false)
		}
		return
	}
	// Plain cid in the header — it's the cmin. If it differs from the
	// deleting cmd, allocate a combo CID.
	cmin := h.GetRawCommandId()
	if cmin != deletingCID {
		comboCID := cs.GetComboCommandId(cmin, deletingCID)
		h.SetCmax(comboCID, true)
	} else {
		h.SetCmax(deletingCID, false)
	}
}

// Reset clears the combo CID store. Called at transaction end.
func (cs *ComboCIDStore) Reset() {
	cs.hash = nil
	cs.array = nil
}
