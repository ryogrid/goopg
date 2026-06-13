package initdb

import "github.com/goopg/goopg/internal/storage"

// checksumRelationData returns the on-disk bytes for a freshly bootstrapped
// relation file, with a data-page checksum stamped into every block when
// checksums are enabled for the cluster.
//
// This is the single primitive that the ~50 direct os.WriteFile bootstrap
// page-write sites (initdb.go, btree_index_bootstrap.go,
// pg_tablespace_bootstrap.go, pg_proc_proname_args_nsp_index_bootstrap.go)
// will route through to enable a bootable --data-checksums cluster. Routing
// every site through one helper is the safe way to satisfy the invariant that
// a checksummed cluster needs pd_checksum on EVERY bootstrap page — missing a
// single block yields an unbootable cluster (the checksum verify on first read
// fails). See docs/design/0102-0019-initdb-data-checksums.md.
//
// The block number folded into each checksum is derived from the block's byte
// offset within the file (block N lives at offset N*BlockSize), exactly
// matching how the runtime smgr verifies a block on read (smgr.go: off/BlockSize).
// This makes the helper uniform across single-page heaps and multi-page heap /
// btree files without any per-site block-number bookkeeping at the call site.
//
// When enabled is false (the default, and currently the only reachable mode
// while Init rejects --data-checksums) the input slice is returned verbatim —
// no copy, no allocation, byte-identical to a plain os.WriteFile. Mirrors
// upstream PageSetChecksumCopy, which is skipped entirely when
// DataChecksumsEnabled() is false (postgres/src/backend/storage/page/bufpage.c).
//
// raw is expected to be a whole number of BlockSize pages. Any trailing bytes
// that do not form a complete final block are copied through verbatim rather
// than corrupted; a partial tail is itself a bootstrap bug, so the helper does
// not attempt to checksum it.
func checksumRelationData(raw []byte, enabled bool) []byte {
	if !enabled {
		return raw
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	nfull := len(out) / storage.BlockSize
	for i := range nfull {
		start := i * storage.BlockSize
		block := out[start : start+storage.BlockSize]
		// PageSetChecksumCopy never mutates its input and skips new/all-zero
		// pages (they carry no checksum, exactly as upstream PageIsNew does).
		copy(block, storage.PageSetChecksumCopy(block, storage.BlockNumber(i)))
	}
	return out
}
