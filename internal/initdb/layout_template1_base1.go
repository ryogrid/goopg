package initdb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// Template1AtBase1Feature marks a cluster whose template1 lives at base/1, the
// directory PostgreSQL reads it from (M0149-0001a1 S3). Before S3, goopg's
// catalog namespace for postgres was the sentinel 1 and template1 was routed
// around it to base/2; S3 makes every namespace OID the PostgreSQL database
// OID (postgres 5, template1 1), so an older cluster's template1 files move
// from base/2 to base/1 (which held only the stale pre-S1 postgres catalog
// copy — S1 consolidated postgres into base/5).
const Template1AtBase1Feature = "template1_at_base1"

// template1LegacyWALEndFile records the end of the WAL that existed when the
// S3 migration started. Startup redo maps the database OIDs of records below
// it the pre-S3 way (xlog.SetLegacyLayoutWALEnd → storage.StorageDBOid), so a
// restart inside the migration still finds each record's relation. Removed
// once the migration is finished.
const template1LegacyWALEndFile = "global/pg_goopg_template1_legacy_wal_end"

// template1CopiedFile marks that template1's base/2 files have been copied
// to base/1 during an unfinished migration. A restart inside the migration
// window must not copy again: base/1 may already hold changes base/2 lacks.
const template1CopiedFile = "global/pg_goopg_template1_copied"

// prepareTemplate1Migration decides, before redo, whether this start must move
// template1 from base/2 to base/1 (M0149-0001a1 S3). pending=true means: redo
// the WAL below legacyEnd with the pre-S3 mapping (template1 records still name
// base/2), then copyTemplate1ToBase1, then finishTemplate1Migration once a
// checkpoint covers the result. The steps are restartable at any point:
//   - the legacy WAL end is persisted before anything moves;
//   - the copy happens after legacy redo and is recorded by
//     template1CopiedFile, so a restart replays legacy records into base/2
//     again (idempotent, unused) and never copies stale data over base/1;
//   - the feature line is written only after the end-of-recovery checkpoint,
//     so a restart before it repeats legacy redo from the same checkpoint.
func prepareTemplate1Migration(dataDir string) (legacyEnd uint64, pending bool, err error) {
	base2 := filepath.Join(dataDir, "base", "2")
	if readGoopgFeatures(dataDir)[Template1AtBase1Feature] {
		// Finished: drop leftovers of an interrupted cleanup.
		if err := os.RemoveAll(base2); err != nil {
			return 0, false, fmt.Errorf("template1 base/2 cleanup: %w", err)
		}
		_ = os.Remove(filepath.Join(dataDir, template1CopiedFile))
		_ = os.Remove(filepath.Join(dataDir, template1LegacyWALEndFile))
		return 0, false, nil
	}
	if _, err := os.Stat(base2); errors.Is(err, fs.ErrNotExist) {
		// No pre-S3 template1 directory (an S3 initdb never makes one).
		return 0, false, addGoopgFeature(dataDir, Template1AtBase1Feature)
	} else if err != nil {
		return 0, false, fmt.Errorf("migrate template1 base/2 → base/1: %w", err)
	}
	legacyEnd, err = readTemplate1LegacyWALEnd(dataDir)
	if err != nil {
		return 0, false, err
	}
	if legacyEnd == 0 {
		legacyEnd, err = walEndLSN(filepath.Join(dataDir, "pg_wal"))
		if err != nil {
			return 0, false, fmt.Errorf("migrate template1 base/2 → base/1: %w", err)
		}
		if legacyEnd == 0 {
			legacyEnd = 1 // no WAL: nothing is legacy, but keep the marker non-zero
		}
		if err := writeFileSync(filepath.Join(dataDir, template1LegacyWALEndFile),
			[]byte(strconv.FormatUint(legacyEnd, 10)+"\n")); err != nil {
			return 0, false, fmt.Errorf("migrate template1 base/2 → base/1: %w", err)
		}
	}
	return legacyEnd, true, nil
}

// copyTemplate1ToBase1 replaces base/1 (the stale pre-S1 postgres catalog
// copy) with template1's base/2 files, once per migration.
func copyTemplate1ToBase1(dataDir string) error {
	marker := filepath.Join(dataDir, template1CopiedFile)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	base1 := filepath.Join(dataDir, "base", "1")
	base2 := filepath.Join(dataDir, "base", "2")
	if err := os.MkdirAll(base1, 0o700); err != nil {
		return fmt.Errorf("copy template1 base/2 → base/1: %w", err)
	}
	src, err := os.ReadDir(base2)
	if err != nil {
		return fmt.Errorf("copy template1 base/2 → base/1: %w", err)
	}
	keep := make(map[string]bool, len(src))
	for _, e := range src {
		if !e.Type().IsRegular() {
			continue
		}
		keep[e.Name()] = true
		if err := copyFileSync(filepath.Join(base2, e.Name()), filepath.Join(base1, e.Name())); err != nil {
			return fmt.Errorf("copy template1 base/2 → base/1: %w", err)
		}
	}
	// A pre-S3 runtime could leave a bootstrap catalog of template1 empty in
	// base/2 (measured: pg_namespace 2615 truncated to 0 bytes after a CREATE
	// TABLE in template1) — goopg never read it, PostgreSQL does. Its rows are
	// lost, so restore the pristine bootstrap image template0 (base/4, never
	// written at runtime) carries for that file.
	if err := restoreEmptyBootstrapCatalogs(base1, filepath.Join(dataDir, "base", "4")); err != nil {
		return fmt.Errorf("copy template1 base/2 → base/1: %w", err)
	}
	old, err := os.ReadDir(base1)
	if err != nil {
		return fmt.Errorf("copy template1 base/2 → base/1: %w", err)
	}
	for _, e := range old {
		if e.Type().IsRegular() && !keep[e.Name()] {
			if err := os.Remove(filepath.Join(base1, e.Name())); err != nil {
				return fmt.Errorf("copy template1 base/2 → base/1: %w", err)
			}
		}
	}
	if err := fsyncDir(base1); err != nil {
		return fmt.Errorf("copy template1 base/2 → base/1: %w", err)
	}
	return writeFileSync(marker, []byte("1\n"))
}

// finishTemplate1Migration records the feature once a checkpoint covers the
// copied template1, then removes base/2 and the copy marker. The legacy WAL
// end marker is retired by the next start (prepareTemplate1Migration).
func finishTemplate1Migration(dataDir string) error {
	if err := addGoopgFeature(dataDir, Template1AtBase1Feature); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dataDir, "base", "2")); err != nil {
		return fmt.Errorf("template1 base/2 cleanup: %w", err)
	}
	_ = os.Remove(filepath.Join(dataDir, template1CopiedFile))
	return nil
}

func readTemplate1LegacyWALEnd(dataDir string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, template1LegacyWALEndFile))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", template1LegacyWALEndFile, err)
	}
	return v, nil
}

// walEndLSN is the end LSN of the last decodable record in walDir (0 when
// there is none).
func walEndLSN(walDir string) (uint64, error) {
	records, err := xlog.ReadAll(walDir, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var end uint64
	for _, r := range records {
		if r.EndLSN > end {
			end = r.EndLSN
		}
	}
	return end, nil
}

func writeFileSync(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

// restoreEmptyBootstrapCatalogs copies template0's file over each zero-length
// bootstrap relation file in dst (relfilenode below FirstNormalObjectId, any
// fork) that template0 holds data for.
func restoreEmptyBootstrapCatalogs(dst, template0 string) error {
	entries, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		num := name
		if i := strings.IndexByte(name, '_'); i >= 0 {
			num = name[:i]
		}
		oid, err := strconv.ParseUint(num, 10, 32)
		if err != nil || oid >= uint64(catalog.FirstUserOID) {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.Size() != 0 {
			continue
		}
		src := filepath.Join(template0, name)
		sfi, err := os.Stat(src)
		if err != nil || sfi.Size() == 0 {
			continue
		}
		if err := copyFileSync(src, filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
}
