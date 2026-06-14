package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/goopg/goopg/internal/storage"
)

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

// relFileNamePattern matches the names of relation data files: a numeric
// relfilenode, an optional fork suffix (_fsm / _vm / _init), and an optional
// segment number. It is goopg's equivalent of pg_checksums'
// parse_filename_for_nontemp_relation
// (postgres/src/bin/pg_checksums/pg_checksums.c) — the filter that decides
// which files under base/<db>/ and global/ carry data pages that must be
// checksummed. Cluster metadata that is NOT a relation page file (PG_VERSION,
// pg_filenode.map, pg_internal.init, pg_control, …) is named non-numerically
// and therefore never matches, so it is left untouched.
var relFileNamePattern = regexp.MustCompile(`^[0-9]+(_(fsm|vm|init))?(\.[0-9]+)?$`)

// stampClusterChecksums walks a freshly bootstrapped cluster and stamps a
// data-page checksum into every block of every relation file under base/ and
// global/, mirroring `pg_checksums --enable`
// (postgres/src/bin/pg_checksums/pg_checksums.c: scan_directory).
//
// It is the single enablement point for initdb --data-checksums. A bootable
// checksummed cluster needs a valid pd_checksum on EVERY data page the
// bootstrap wrote, across ~40 scattered direct os.WriteFile sites plus the
// Manager-written system catalogs — missing even one yields an unbootable
// cluster (the smgr verify on first read fails). Rather than thread the
// cluster's checksum flag through every one of those sites (any one of which,
// if missed, is a silent unbootable-cluster bug), goopg runs ONE offline pass
// after all bootstrap writes complete. The on-disk result is identical to
// upstream initdb -k — every data page carries a valid pd_checksum — and the
// implementation is centralized and impossible to leave a page un-checksummed.
//
// Each matched file is read, every BlockSize block is checksummed via
// checksumRelationData (new/all-zero pages are skipped, exactly as upstream
// PageIsNew), and the file is rewritten in place. os.WriteFile preserves the
// existing file's mode (the perm argument applies only on creation), so a
// later -g/--allow-group-access relax pass still owns the final mode. The
// block number folded into each checksum is the block's offset within the file
// (off/BlockSize), matching how the runtime smgr verifies a block on read.
//
// Called only when checksums are requested; a version-0 cluster (the goopg
// default) never enters here and remains byte-identical to before.
func stampClusterChecksums(dataDir string) error {
	dirs := []string{filepath.Join(dataDir, "global")}
	baseDir := filepath.Join(dataDir, "base")
	dbDirs, err := os.ReadDir(baseDir)
	if err != nil {
		return fmt.Errorf("read base dir: %w", err)
	}
	for _, d := range dbDirs {
		if d.IsDir() {
			dirs = append(dirs, filepath.Join(baseDir, d.Name()))
		}
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read %q: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !relFileNamePattern.MatchString(e.Name()) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %q: %w", path, err)
			}
			if len(raw) == 0 {
				continue // empty placeholder relfile — no pages to checksum
			}
			if err := os.WriteFile(path, checksumRelationData(raw, true), 0o600); err != nil {
				return fmt.Errorf("write %q: %w", path, err)
			}
		}
	}
	return nil
}
