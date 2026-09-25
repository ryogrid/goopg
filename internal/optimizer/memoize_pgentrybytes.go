package optimizer

import "os"

// pgMemoizeEntryBytesCost is M0139-0007b's default-off planner comparison
// switch, in the shape of R108/R113 (GOOPG_PG_HASH_TUPLE_SPILL_COST,
// GOOPG_PG_SORT_RELATION_BYTES_COST). Only the exact value "1" elects PG's
// cost_memoize_rescan tuple-byte currency in costMemoizeRescan; unset and
// every other spelling retain goopg's hashsize.EntryBytes/kvcache currency.
var pgMemoizeEntryBytesCost = pgMemoizeEntryBytesCostFromEnv(os.Getenv("GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST"))

func pgMemoizeEntryBytesCostFromEnv(v string) bool { return v == "1" }

func pgMemoizeEntryBytesCostEnabled() bool { return pgMemoizeEntryBytesCost }

func setPGMemoizeEntryBytesCostForTest(on bool) func() {
	old := pgMemoizeEntryBytesCost
	pgMemoizeEntryBytesCost = on
	return func() { pgMemoizeEntryBytesCost = old }
}

// PG's per-entry struct sizes (nodeMemoize.c:94-123), on the LP64 layout PG is
// built for (8-byte pointers, 8-byte MAXALIGN — the only layout goopg targets):
//
//	MemoizeTuple { MinimalTuple mintuple; struct MemoizeTuple *next; }
//	  MinimalTuple is `typedef struct MinimalTupleData *MinimalTuple` (a
//	  pointer, htup.h) -- so MemoizeTuple is two pointers = 16 bytes, no padding.
//	MemoizeKey { MinimalTuple params; dlist_node lru_node; }
//	  dlist_node is two pointers = 16 bytes (ilist.h:136-141), so MemoizeKey is
//	  8 + 16 = 24 bytes.
//	MemoizeEntry { MemoizeKey *key; MemoizeTuple *tuplehead; uint32 hash;
//	               char status; bool complete; }
//	  8 + 8 + 4 + 1 + 1 = 22, padded to the struct's own 8-byte pointer
//	  alignment = 24 bytes.
const (
	pgMemoizeEntryStructBytes = 24 // sizeof(MemoizeEntry)
	pgMemoizeKeyStructBytes   = 24 // sizeof(MemoizeKey)
	pgMemoizeTupleStructBytes = 16 // sizeof(MemoizeTuple)
)

// pgMemoizeEntryOverheadBytes is ExecEstimateCacheEntryOverheadBytes
// (nodeMemoize.c:1171-1176), transcribed byte-for-byte from the struct sizes
// above: `sizeof(MemoizeEntry) + sizeof(MemoizeKey) + sizeof(MemoizeTuple) *
// ntuples`. It is a fixed PG C-struct cost, not a goopg Go-shape estimate —
// unlike memoizeEntryOverheadBytes (:92-102 above), which prices goopg's own
// kvcache entry and stays the switch-off currency.
func pgMemoizeEntryOverheadBytes(tuples float64) float64 {
	return float64(pgMemoizeEntryStructBytes+pgMemoizeKeyStructBytes) + float64(pgMemoizeTupleStructBytes)*tuples
}
