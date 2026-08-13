package initdb

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages verifies that
// every mapped local-catalog OID lacking a dedicated bootstrapper receives
// a heap-initialised 8 KiB placeholder under both base/1/<oid> and
// base/5/<oid>. Without these files PG's InitPostgres FATALs with
// `could not open relation with OID NNNN`.
//
// M0106-0010 batched-52: catalogs that NOW have dedicated populating
// bootstrappers (pg_aggregate 2600, pg_cast 2605, pg_conversion 2607,
// pg_operator 2617, pg_opfamily 2753, pg_range 3541) are intentionally
// omitted — overwriting their seeded heaps with empty pages would wipe
// real rows and reintroduce blockers like
// `cache lookup failed for aggregate 2803`.
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

	// Catalogs with no dedicated bootstrapper — keep in sync with
	// mappedLocalCatalogPlaceholderOIDs.
	wantOIDs := []uint32{
		826,  // pg_default_acl (M0106-0010 step 3ak)
		1417, // pg_foreign_server (M0106-0010 step 3be)
		1418, // pg_user_mapping (M0106-0010 step 3cp)
		2224, // pg_sequence (M0106-0010 step 3cb)
		2328, // pg_foreign_data_wrapper (M0106-0010 step 3bb)
		2604, 2608, 2609, 2611,
		2613, 2614,
		2619, 2620,
		3079, // pg_extension (M0106-0010 step 3aw)
		3118, // pg_foreign_table (M0106-0010 step 3bh)
		3350, // pg_partitioned_table (M0106-0010 step 3bs)
		3381,
		3429, // pg_statistic_ext_data (M0106-0010 step 3cc)
		3466, // pg_event_trigger (M0106-0010 step 3ar)
		3501, // pg_enum (M0106-0010 step 3an)
		3576, // pg_transform (M0106-0010 step 3ci)
		3596,
		3600, // pg_ts_dict (M0106-0010 step 3cm)
		3601, // pg_ts_parser (M0106-0010 step 3cn)
		3602, // pg_ts_config (M0106-0010 step 3ck)
		3603, // pg_ts_config_map (M0106-0010 step 3cj)
		3764, 3765, 3766, 3767, 3768,
		6003, 6101, 6102, 6104, 6106, 6137, 6237,
		6245, 9400,
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

	// Catalogs that now have dedicated bootstrappers MUST be skipped
	// by bootstrapMappedLocalCatalogHeaps; their files should not be
	// created by this call. The dedicated bootstrapper is responsible.
	for _, db := range []string{"base/1", "base/5"} {
		for _, oid := range []uint32{2600, 2605, 2606, 2607, 2617, 2753, 3541} {
			path := filepath.Join(dir, db, strconv.FormatUint(uint64(oid), 10))
			if _, err := os.Stat(path); err == nil {
				t.Errorf("OID %d in %s exists after bootstrapMappedLocalCatalogHeaps — must be omitted (clobbers dedicated bootstrapper)", oid, db)
			}
		}
	}
}

