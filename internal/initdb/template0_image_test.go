package initdb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestTemplate0ImageMatchesPostgresDatabase is M0131-S15's guard.
//
// Runtime CREATE DATABASE clones base/4 (template0) via
// copyBootstrapCatalogImage, so whatever template0 holds is what every
// runtime-minted database holds. Before S15, bootstrapPostgresDatabase built
// base/4 EARLY — from a base/1 that had not been bulk-loaded yet — and then
// stamped metapage-only placeholders over its index files, so a hosted PG
// PANICked with "could not open critical system index 2662" on the first
// connection to such a database (docs/design/0131-0004 finding F4).
//
// The assertion is deliberately a whole-directory equality against base/5
// rather than a spot-check of 2662: the PANIC named one index, but the set of
// indexes PG nails in RelationCacheInitializePhase3 is larger, and the same
// early-copy ordering left EVERY populated file stale. PG_VERSION is exempt
// (each database directory writes its own).
func TestTemplate0ImageMatchesPostgresDatabase(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := Init(Options{DataDir: dataDir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	srcDir := filepath.Join(dataDir, "base", "5")
	dstDir := filepath.Join(dataDir, "base", "4")
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read base/5: %v", err)
	}
	var missing, differing []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "PG_VERSION" {
			continue
		}
		want, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatalf("read base/5/%s: %v", e.Name(), err)
		}
		got, err := os.ReadFile(filepath.Join(dstDir, e.Name()))
		if os.IsNotExist(err) {
			missing = append(missing, e.Name())
			continue
		}
		if err != nil {
			t.Fatalf("read base/4/%s: %v", e.Name(), err)
		}
		if !bytes.Equal(got, want) {
			differing = append(differing, e.Name())
		}
	}
	if len(missing) > 0 || len(differing) > 0 {
		t.Fatalf("template0's on-disk image diverges from the `postgres` database's — "+
			"every runtime CREATE DATABASE clones it, so a hosted PG will PANIC on a "+
			"critical system index (M0131-S15).\nmissing from base/4: %v\ndiffering: %v",
			missing, differing)
	}

	// The specific file whose emptiness produced the measured PANIC: assert it
	// is a real multi-page btree, not the 1-page metapage-only placeholder, so
	// a future change that made base/4 and base/5 equally EMPTY could not pass
	// the equality above unnoticed.
	fi, err := os.Stat(filepath.Join(dstDir, "2662"))
	if err != nil {
		t.Fatalf("stat base/4/2662 (pg_class_oid_index): %v", err)
	}
	if fi.Size() <= 8192 {
		t.Fatalf("template0's pg_class_oid_index (2662) is %d bytes — that is the "+
			"metapage-only placeholder, i.e. no leaf entries; PG's "+
			"RelationCacheInitializePhase3 PANICs on it", fi.Size())
	}
}
