package btree

// WAL replay helpers for btree records. Exported so the
// `internal/wal` package can apply logical btree records during
// crash recovery without importing btree's internal mutation
// paths.

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// ReplayVacuumPage rebuilds a btree page's kept-items
// projection from a `RecordKindBtreeVacuum` payload. Mirrors
// the per-page critical section in `btree_vacuum.go`'s
// VacuumIndexPages: reset the line-pointer table and data area,
// re-add each kept item via `storage.PageAddItemRaw`, then
// overwrite the opaque flags.
//
// `keptItems` carries each surviving item's raw bytes (the
// `keyLen | ptr.block | ptr.offset | key` blob produced by
// `item.marshal`). The caller is responsible for emitting
// these in the order they should appear post-replay.
//
// `opaqueFlagsAfter` is the post-vacuum BTPageOpaque.Flags
// value. The other opaque fields (Prev, Next, Level, BTreeID)
// are preserved from the on-disk page — VACUUM never changes
// them.
//
// Idempotent at the caller's level via pd_lsn (the wal package
// checks `Header(page).LSN()` against the record's end LSN
// before calling this helper).
//
// (M0079-0002.)
func ReplayVacuumPage(page storage.Page, keptItems [][]byte, opaqueFlagsAfter uint16) error {
	op := readOpaque(page)
	resetPageItems(page)
	for i, raw := range keptItems {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			return fmt.Errorf("btree: replay vacuum item %d: %w", i, err)
		}
	}
	op.Flags = opaqueFlagsAfter
	writeOpaque(page, op)
	return nil
}
