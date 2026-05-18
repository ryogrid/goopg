package initdb

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages verifies
// M0106-0010 step 3w: every mapped local catalog OID that lacks a dedicated
// bootstrapper receives a heap-initialised 8 KiB placeholder under both
// base/1/<oid> and base/5/<oid>. Without these files PG's InitPostgres FATALs
// with `could not open relation with OID 2600` (pg_aggregate) and the same
// blocker would resurface in turn for every other unmapped catalog.
//
// Pinning the canonical OID list (rather than just spot-checking 2600) catches
// the case where a future refactor silently drops one of the catalogs — that
// would re-introduce the FATAL on whichever catalog PG happens to probe first.
func TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages(t *testing.T) {
	dir := t.TempDir()
	for _, db := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, db), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	if err := bootstrapMappedLocalCatalogHeaps(dir); err != nil {
		t.Fatalf("bootstrapMappedLocalCatalogHeaps: %v", err)
	}

	// Mirror the canonical list embedded in the bootstrapper. Keep in sync.
	// 1247 pg_type is intentionally absent — bootstrapSystemCatalogs already
	// seeds it in goopg's internal format and overwriting would wipe the
	// rows TestBootstrappedPGTypeRowsReadable depends on.
	wantOIDs := []uint32{
		826, // pg_default_acl (M0106-0010 step 3ak)
		1417, // pg_foreign_server (M0106-0010 step 3be)
		2328, // pg_foreign_data_wrapper (M0106-0010 step 3bb)
		2600, 2604, 2605, 2606, 2607, 2608, 2609, 2611, 2612,
		2613, 2614, 2615, 2617, 2618, 2619, 2620,
		3079, // pg_extension (M0106-0010 step 3aw)
		3381,
		3466, // pg_event_trigger (M0106-0010 step 3ar)
		3501, // pg_enum (M0106-0010 step 3an)
		3596,
		3764, 3765, 3766, 3767, 3768, 6003, 6101, 6102, 6137,
		6245, 9400,
	}

	// pg_aggregate (2600) is the specific blocker Step 3w closes — guard
	// it explicitly so a future re-order can't silently drop it.
	saw2600 := false
	for _, oid := range wantOIDs {
		if oid == 2600 {
			saw2600 = true
			break
		}
	}
	if !saw2600 {
		t.Fatalf("wantOIDs missing 2600 (pg_aggregate) — Step 3w must seed it")
	}

	for _, db := range []string{"base/1", "base/5"} {
		for _, oid := range wantOIDs {
			path := filepath.Join(dir, db, strconv.FormatUint(uint64(oid), 10))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if len(data) != storage.BlockSize {
				t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
			}
			// Reject a tombstone zero-page: InitPage must have stamped
			// the header so PG's mdopen returns a valid PageHeader. A
			// zeroed page would trip PageIsVerified.
			if isAllZero(data) {
				t.Fatalf("%s: page is all zero — InitPage was not applied", path)
			}
		}
	}
}

