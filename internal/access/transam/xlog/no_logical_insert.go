package xlog

// no_logical_insert.go — HEAP_INSERT_NO_LOGICAL for goopg's in-place table
// rewrites. M0149-0061l.
//
// PG rewrites a table (ALTER TABLE ... ALTER COLUMN TYPE, a rewriting ADD
// COLUMN, VACUUM FULL) into a NEW transient relfilenode whose pg_class row
// carries relrewrite, and logical decoding never publishes those inserts
// (ReorderBufferProcessTXN skips a relrewrite relation; raw_heap_insert's
// TOAST rows are HEAP_INSERT_NO_LOGICAL, which leaves
// XLH_INSERT_CONTAINS_NEW_TUPLE clear so DecodeInsert ignores them).
//
// goopg rewrites in place: it truncates the table's own relfilenode and
// re-inserts every row through the ordinary insert path, so without a mark
// each re-inserted row reached subscribers as a fresh INSERT
// (013_partition.pl: the subscriber's insert_exists loop after the
// publisher's ALTER TABLE tab5 DROP COLUMN b). The rewrite registers its
// (relation, xid) here for the duration of the re-insert; the heap-insert
// WAL hook then leaves XLH_INSERT_CONTAINS_NEW_TUPLE clear, exactly as
// heap_insert does under HEAP_INSERT_NO_LOGICAL, and Classify skips the
// record as DecodeInsert does.

import (
	"sync"

	"github.com/goopg/goopg/internal/storage"
)

type noLogicalKey struct {
	rel storage.RelFileNode
	xid storage.TransactionID
}

var (
	noLogicalMu sync.RWMutex
	noLogical   = map[noLogicalKey]int{}
)

// BeginNoLogicalInserts marks rel's inserts by xid as not logically
// decoded until the returned function is called.
func BeginNoLogicalInserts(rel storage.RelFileNode, xid storage.TransactionID) (end func()) {
	k := noLogicalKey{rel, xid}
	noLogicalMu.Lock()
	noLogical[k]++
	noLogicalMu.Unlock()
	return func() {
		noLogicalMu.Lock()
		if noLogical[k]--; noLogical[k] <= 0 {
			delete(noLogical, k)
		}
		noLogicalMu.Unlock()
	}
}

// InsertIsNoLogical reports whether an insert into rel by xid is inside a
// BeginNoLogicalInserts window.
func InsertIsNoLogical(rel storage.RelFileNode, xid storage.TransactionID) bool {
	noLogicalMu.RLock()
	defer noLogicalMu.RUnlock()
	if len(noLogical) == 0 {
		return false
	}
	return noLogical[noLogicalKey{rel, xid}] > 0
}
