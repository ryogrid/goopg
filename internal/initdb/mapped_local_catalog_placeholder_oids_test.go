package initdb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestMappedLocalCatalogPlaceholderOIDsOmitsDedicatedBootstrappers guards
// against a regression where bootstrapMappedLocalCatalogHeaps clobbers
// catalogs whose heap is populated by a dedicated bootstrapper. M0106-0010
// batched-52 surfaced this as `cache lookup failed for aggregate 2803` on
// the PG18 failover standby because pg_aggregate (OID 2600) was overwritten
// with an empty 8 KiB heap page after `bootstrapPgAggregateTuples` had
// written all 161 rows.
func TestMappedLocalCatalogPlaceholderOIDsOmitsDedicatedBootstrappers(t *testing.T) {
	oids := mappedLocalCatalogPlaceholderOIDs()
	forbidden := map[uint32]string{
		1247: "pg_type",
		1249: "pg_attribute",
		1255: "pg_proc",
		1259: "pg_class",
		2600: "pg_aggregate",
		2601: "pg_am",
		2602: "pg_amop",
		2603: "pg_amproc",
		2605: "pg_cast",
		2607: "pg_conversion",
		2610: "pg_index",
		2612: "pg_language",
		2615: "pg_namespace",
		2616: "pg_opclass",
		2617: "pg_operator",
		2618: "pg_rewrite",
		2753: "pg_opfamily",
		3456: "pg_collation",
		3541: "pg_range",
	}
	for _, oid := range oids {
		if name, bad := forbidden[oid]; bad {
			t.Errorf("OID %d (%s) has a dedicated bootstrapper but is still in the placeholder list — bootstrapMappedLocalCatalogHeaps will wipe its rows", oid, name)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsPreservesPopulatedFiles is a
// behavioral test: when bootstrapMappedLocalCatalogHeaps runs, it must
// leave pre-existing populated heap files for dedicated catalogs intact.
// We seed base/{1,5}/2600 with a non-empty signature, run the placeholder
// pass, and assert the bytes are unchanged.
func TestBootstrapMappedLocalCatalogHeapsPreservesPopulatedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// Seed a recognisable 16 KiB payload (2 heap pages worth) at each
	// dedicated-bootstrapper OID. If bootstrapMappedLocalCatalogHeaps
	// clobbers them, the size will collapse to one empty page or the
	// content will become an all-zero InitPage.
	signature := bytes.Repeat([]byte{0xAB}, 2*int(storage.BlockSize))
	dedicated := []uint32{2600, 2605, 2607, 2617, 2753, 3541}
	for _, dbOid := range []string{"1", "5"} {
		for _, oid := range dedicated {
			path := filepath.Join(dir, "base", dbOid, fmtUint(oid))
			if err := os.WriteFile(path, signature, 0o600); err != nil {
				t.Fatalf("seed %s: %v", path, err)
			}
		}
	}

	if err := bootstrapMappedLocalCatalogHeaps(dir); err != nil {
		t.Fatalf("bootstrapMappedLocalCatalogHeaps: %v", err)
	}

	for _, dbOid := range []string{"1", "5"} {
		for _, oid := range dedicated {
			path := filepath.Join(dir, "base", dbOid, fmtUint(oid))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read %s: %v", path, err)
				continue
			}
			if !bytes.Equal(got, signature) {
				t.Errorf("OID %d in base/%s was overwritten by bootstrapMappedLocalCatalogHeaps (len=%d, want %d)", oid, dbOid, len(got), len(signature))
			}
		}
	}
}

func fmtUint(v uint32) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var b [10]byte
	n := len(b)
	for v > 0 {
		n--
		b[n] = digits[v%10]
		v /= 10
	}
	return string(b[n:])
}
