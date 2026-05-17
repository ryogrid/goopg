package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgEventTrigger pins the M0106-0010 step 3ar
// catalog seed for pg_event_trigger (OID 3466). PG's standby boot opens
// `RelationBuildDesc(3466)`; without this entry it FATALs with
// `could not open relation with OID 3466`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_event_trigger.h
//   - postgres/src/include/catalog/pg_event_trigger_d.h
//     (EventTriggerRelationId = 3466, Natts_pg_event_trigger = 7).
func TestNailedLocalRelsContainsPgEventTrigger(t *testing.T) {
	const eventTriggerOID = 3466

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == eventTriggerOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_event_trigger) — step 3ar regression", eventTriggerOID)
	}
	if got.RelName != "pg_event_trigger" {
		t.Fatalf("OID %d: RelName=%q want %q", eventTriggerOID, got.RelName, "pg_event_trigger")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", eventTriggerOID, got.RelKind)
	}
	if got.RelNatts != 7 {
		t.Fatalf("OID %d: RelNatts=%d want 7 (PG18 Natts_pg_event_trigger)", eventTriggerOID, got.RelNatts)
	}
	if len(got.Attrs) != 7 {
		t.Fatalf("OID %d: len(Attrs)=%d want 7", eventTriggerOID, len(got.Attrs))
	}

	// Per-attribute pins against PG18 pg_event_trigger_d.h / pg_event_trigger.h.
	// evttags is in the CATALOG_VARLEN block, no BKI_FORCE_NOT_NULL, so it is
	// the only nullable column (text[] = OID 1009).
	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"oid", 26, 1, 4, true},
		{"evtname", 19, 2, 64, true},
		{"evtevent", 19, 3, 64, true},
		{"evtowner", 26, 4, 4, true},
		{"evtfoid", 26, 5, 4, true},
		{"evtenabled", 18, 6, 1, true},
		{"evttags", 1009, 7, -1, false},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapMappedLocalCatalogHeapsIncludesPgEventTrigger pins the
// step 3ar fix that corrects the empty-heap placeholder list to use the
// canonical EventTriggerRelationId (3466), replacing the prior incorrect
// 4044 placeholder. Without the on-disk file at base/{1,5}/3466, PG's
// mdopen FATALs after the pg_class row resolves the relation.
func TestBootstrapMappedLocalCatalogHeapsIncludesPgEventTrigger(t *testing.T) {
	dir := t.TempDir()
	for _, db := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, db), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := bootstrapMappedLocalCatalogHeaps(dir); err != nil {
		t.Fatalf("bootstrapMappedLocalCatalogHeaps: %v", err)
	}
	for _, db := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, db, "3466")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v (step 3ar regression — 4044 → 3466 not applied)", path, err)
		}
		if len(data) != storage.BlockSize {
			t.Fatalf("%s: len=%d, want %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero — InitPage was not applied", path)
		}
		// Guard against the regressed file still being written under OID 4044.
		if _, err := os.Stat(filepath.Join(dir, db, "4044")); err == nil {
			t.Fatalf("%s/4044 should not exist: 4044 is not a valid catalog OID; step 3ar requires 3466 (EventTriggerRelationId)", db)
		}
	}
}
