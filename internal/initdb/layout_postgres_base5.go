package initdb

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/goopg/goopg/internal/storage"
)

// PostgresCatalogsAtBase5Feature marks a cluster whose `postgres`-namespace
// relation files have been consolidated into base/5 (M0149-0001a1 S1).
//
// Before S1, goopg wrote the postgres database's catalog relations under
// DBOid 1 (catalog.DefaultDBOid, base/1) and copied them block by block into
// base/5 (catalog.PostgresDBOid) after each write. Not every writer took the
// copy: on the shipped TPC-DS corpora base/1's pg_statistic holds the imported
// statistics while base/5's copy is one empty page. Since S1 every storage
// entry point maps DBOid 1 to 5 (storage.StorageDBOid), so base/5 must hold the
// latest copy of every such file before the first relation is opened.
const PostgresCatalogsAtBase5Feature = "postgres_catalogs_at_base5"

// migratePostgresCatalogsToBase5 consolidates base/1 into base/5 once per
// cluster. For every file in base/1 the copy with the higher maximum page
// LSN wins — the one written most recently — and an equal or missing base/1
// copy leaves base/5 untouched. User relations of the postgres database were
// always stored in base/5 only, and catalog files only one side holds data for
// (the other is empty: LSN 0) resolve to the side that has it. Runs before the
// storage Manager opens anything and before WAL redo, which then replays any
// later change on top (pre-S1 records naming DBOid 1 are mapped to base/5 by
// storage.StorageDBOid). base/1 itself is left in place; S3 repurposes it for
// template1.
func migratePostgresCatalogsToBase5(dataDir string) error {
	if readGoopgFeatures(dataDir)[PostgresCatalogsAtBase5Feature] {
		return nil
	}
	src := filepath.Join(dataDir, "base", "1")
	dst := filepath.Join(dataDir, "base", "5")
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return addGoopgFeature(dataDir, PostgresCatalogsAtBase5Feature)
		}
		return fmt.Errorf("migrate base/1 → base/5: %w", err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("migrate base/1 → base/5: %w", err)
	}
	copied := false
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		// Per-database metadata that is not a relation file stays put.
		switch name {
		case "PG_VERSION", "pg_filenode.map", "pg_internal.init":
			continue
		}
		srcPath, dstPath := filepath.Join(src, name), filepath.Join(dst, name)
		srcLSN, err := maxPageLSN(srcPath)
		if err != nil {
			return fmt.Errorf("migrate base/1 → base/5: %w", err)
		}
		dstLSN, err := maxPageLSN(dstPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("migrate base/1 → base/5: %w", err)
		}
		dstMissing := os.IsNotExist(err)
		if !dstMissing && srcLSN <= dstLSN {
			continue
		}
		if dstMissing && srcLSN == 0 {
			// An empty / never-written base/1 file: nothing to carry over.
			if fi, serr := os.Stat(srcPath); serr == nil && fi.Size() == 0 {
				continue
			}
		}
		if err := copyFileSync(srcPath, dstPath); err != nil {
			return fmt.Errorf("migrate base/1 → base/5: %w", err)
		}
		copied = true
	}
	if copied {
		if err := fsyncDir(dst); err != nil {
			return fmt.Errorf("migrate base/1 → base/5: %w", err)
		}
	}
	return addGoopgFeature(dataDir, PostgresCatalogsAtBase5Feature)
}

// maxPageLSN is the largest pd_lsn over a relation file's pages
// (PageHeaderData.pd_lsn = {xlogid, xrecoff}, little-endian at offset 0).
func maxPageLSN(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var max uint64
	hdr := make([]byte, 8)
	for off := int64(0); ; off += storage.BlockSize {
		n, err := f.ReadAt(hdr, off)
		if n < len(hdr) {
			if err == io.EOF || err == nil {
				break
			}
			return 0, err
		}
		lsn := uint64(binary.LittleEndian.Uint32(hdr[0:4]))<<32 | uint64(binary.LittleEndian.Uint32(hdr[4:8]))
		if lsn > max {
			max = lsn
		}
	}
	return max, nil
}

// copyFileSync replaces dst with src's bytes through a temp file, fsynced and
// renamed into place, so a crash leaves either the old or the new copy.
func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".s1tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
