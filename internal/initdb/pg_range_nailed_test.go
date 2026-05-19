package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestNailedLocalRelsContainsPgRange pins the M0106-0010 step 3bz
// catalog seed for pg_range (OID 3541). PG's standby boot opens
// `RelationBuildDesc(3541)` once Step 3by's pg_publication_rel
// family cleared the previous FATAL; without this entry it FATALs
// with `could not open relation with OID 3541`.
//
// Authoritative source:
//   - postgres/src/include/catalog/pg_range.h
//     (RangeRelationId = 3541, 7 columns, no oid system column).
func TestNailedLocalRelsContainsPgRange(t *testing.T) {
	const rangeOID = 3541

	var got *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == rangeOID {
			got = &nailedLocalRels[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("nailedLocalRels missing OID %d (pg_range) — step 3bz regression", rangeOID)
	}
	if got.RelName != "pg_range" {
		t.Fatalf("OID %d: RelName=%q want %q", rangeOID, got.RelName, "pg_range")
	}
	if got.RelKind != 'r' {
		t.Fatalf("OID %d: RelKind=%q want 'r'", rangeOID, got.RelKind)
	}
	if got.RelNatts != 7 {
		t.Fatalf("OID %d: RelNatts=%d want 7 (PG18 Natts_pg_range)", rangeOID, got.RelNatts)
	}
	if len(got.Attrs) != 7 {
		t.Fatalf("OID %d: len(Attrs)=%d want 7", rangeOID, len(got.Attrs))
	}

	type want struct {
		Name    string
		TypeOID uint32
		Num     int16
		Len     int16
		NotNull bool
	}
	wantAttrs := []want{
		{"rngtypid", 26, 1, 4, true},
		{"rngsubtype", 26, 2, 4, true},
		{"rngmultitypid", 26, 3, 4, true},
		{"rngcollation", 26, 4, 4, true},
		{"rngsubopc", 26, 5, 4, true},
		{"rngcanonical", 24, 6, 4, true},
		{"rngsubdiff", 24, 7, 4, true},
	}
	for i, w := range wantAttrs {
		a := got.Attrs[i]
		if a.Name != w.Name || a.TypeOID != w.TypeOID || a.Num != w.Num || a.Len != w.Len || a.NotNull != w.NotNull {
			t.Errorf("Attrs[%d]=%+v want {%s %d %d %d %v}", i, a, w.Name, w.TypeOID, w.Num, w.Len, w.NotNull)
		}
	}
}

// TestBootstrapPgRangeTuplesSurvivesMappedLocalCatalogPlaceholderPass
// pins that the populated pg_range heap (base/{1,5}/3541) written by
// bootstrapPgRangeTuples is NOT clobbered by the subsequent
// bootstrapMappedLocalCatalogHeaps pass. M0106-0010 batched-52
// surfaced the inverse regression for pg_aggregate (OID 2600) as
// `cache lookup failed for aggregate 2803`; the placeholder list
// must omit pg_range for the same reason.
func TestBootstrapPgRangeTuplesSurvivesMappedLocalCatalogPlaceholderPass(t *testing.T) {
	dir := t.TempDir()
	for _, db := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, db), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if _, err := bootstrapPgRangeTuples(dir); err != nil {
		t.Fatalf("bootstrapPgRangeTuples: %v", err)
	}
	preSize := make(map[string]int64)
	for _, db := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, db, "3541")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("populated %s missing after bootstrapPgRangeTuples: %v", path, err)
		}
		preSize[path] = info.Size()
	}
	if err := bootstrapMappedLocalCatalogHeaps(dir); err != nil {
		t.Fatalf("bootstrapMappedLocalCatalogHeaps: %v", err)
	}
	for path, want := range preSize {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if info.Size() != want {
			t.Errorf("%s clobbered: size now %d, want %d (populated bytes overwritten)", path, info.Size(), want)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(data) < int(storage.BlockSize) {
			t.Fatalf("%s: len=%d, want >= %d", path, len(data), storage.BlockSize)
		}
		if isAllZero(data) {
			t.Fatalf("%s: page is all zero", path)
		}
	}
}

// TestPgRangeRngtypidIndexInitialEntry pins the pgIndexInitialEntries
// row for the UNIQUE PRIMARY index (3542) over pg_range (3541) on
// btree(rngtypid oid_ops). PG's RelationInitIndexAccessInfo requires
// indkey/indclass/indcollation/indisunique/indisprimary to agree
// with the upstream pg_range.h declaration.
func TestPgRangeRngtypidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 3542
		relOID = 3541
		oidOps = uint32(1981)
	)
	var got *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			e := e
			got = &e
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_range_rngtypid_index) — step 3bz regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_range)", got.IndRelid, relOID)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 1 {
		t.Errorf("IndKey=%v want [1] (rngtypid attnum)", got.IndKey)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != oidOps {
		t.Errorf("IndClass=%v want [%d] (oid_ops)", got.IndClass, oidOps)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 0 {
		t.Errorf("IndCollation=%v want [0] (no collation for oid_ops)", got.IndCollation)
	}
	if !got.IsUnique {
		t.Errorf("IsUnique=false want true (DECLARE_UNIQUE_INDEX_PKEY)")
	}
	if !got.IsPrimary {
		t.Errorf("IsPrimary=false want true (DECLARE_UNIQUE_INDEX_PKEY)")
	}
}

// TestPgRangeRngmultitypidIndexInitialEntry pins the pgIndexInitialEntries
// row for the UNIQUE (non-PKEY) single-column index (2228) over pg_range
// (3541) on btree(rngmultitypid oid_ops). attnum 3 = rngmultitypid per
// pg_range_d.h.
func TestPgRangeRngmultitypidIndexInitialEntry(t *testing.T) {
	const (
		idxOID = 2228
		relOID = 3541
		oidOps = uint32(1981)
	)
	var got *pgIndexEntry
	for _, e := range pgIndexInitialEntries() {
		if e.IndexRelid == idxOID {
			e := e
			got = &e
			break
		}
	}
	if got == nil {
		t.Fatalf("pgIndexInitialEntries missing OID %d (pg_range_rngmultitypid_index) — step 3bz regression", idxOID)
	}
	if got.IndRelid != relOID {
		t.Errorf("IndRelid=%d want %d (pg_range)", got.IndRelid, relOID)
	}
	if len(got.IndKey) != 1 || got.IndKey[0] != 3 {
		t.Errorf("IndKey=%v want [3] (rngmultitypid attnum)", got.IndKey)
	}
	if len(got.IndClass) != 1 || got.IndClass[0] != oidOps {
		t.Errorf("IndClass=%v want [%d] (oid_ops)", got.IndClass, oidOps)
	}
	if len(got.IndCollation) != 1 || got.IndCollation[0] != 0 {
		t.Errorf("IndCollation=%v want [0] (no collation for oid_ops)", got.IndCollation)
	}
	if !got.IsUnique {
		t.Errorf("IsUnique=false want true (DECLARE_UNIQUE_INDEX)")
	}
	if got.IsPrimary {
		t.Errorf("IsPrimary=true want false (DECLARE_UNIQUE_INDEX, not the _PKEY variant)")
	}
}

// TestPgRangeInitialEntriesCount pins that pgRangeInitialEntries returns
// exactly 6 rows matching the 6 entries in pg_range.dat.
func TestPgRangeInitialEntriesCount(t *testing.T) {
	entries := pgRangeInitialEntries()
	if len(entries) != 6 {
		t.Fatalf("pgRangeInitialEntries: got %d entries, want 6", len(entries))
	}
}

// TestPgRangeEntriesRngtypidUnique pins that every rngtypid is distinct.
func TestPgRangeEntriesRngtypidUnique(t *testing.T) {
	seen := make(map[uint32]bool)
	for _, e := range pgRangeInitialEntries() {
		if seen[e.RngTypID] {
			t.Errorf("duplicate rngtypid %d", e.RngTypID)
		}
		seen[e.RngTypID] = true
	}
}

// TestPgRangeEntriesRngmultitypidUnique pins that every rngmultitypid
// is distinct (each range type maps to a distinct multirange type).
func TestPgRangeEntriesRngmultitypidUnique(t *testing.T) {
	seen := make(map[uint32]bool)
	for _, e := range pgRangeInitialEntries() {
		if seen[e.RngMultiTypID] {
			t.Errorf("duplicate rngmultitypid %d (rngtypid=%d)", e.RngMultiTypID, e.RngTypID)
		}
		seen[e.RngMultiTypID] = true
	}
}

// TestPgRangeEntriesSpotCheck pins a representative subset of the 6 rows
// against the pg_range.dat source of truth.
func TestPgRangeEntriesSpotCheck(t *testing.T) {
	type want struct {
		rngtypid      uint32
		rngsubtype    uint32
		rngmultitypid uint32
		rngsubopc     uint32
		rngcanonical  uint32
		rngsubdiff    uint32
	}
	// Sourced from pg_range.dat + pg_type.dat + pg_proc.dat + pg_opclass.dat.
	cases := []want{
		{3904, 23, 4451, 1978, 3914, 3922},   // int4range
		{3906, 1700, 4532, 3125, 0, 3924},    // numrange (no canonical)
		{3926, 20, 4536, 3124, 3928, 3923},   // int8range
	}
	entries := pgRangeInitialEntries()
	byTypID := make(map[uint32]pgRangeEntry, len(entries))
	for _, e := range entries {
		byTypID[e.RngTypID] = e
	}
	for _, w := range cases {
		e, ok := byTypID[w.rngtypid]
		if !ok {
			t.Errorf("missing entry for rngtypid %d", w.rngtypid)
			continue
		}
		if e.RngSubtype != w.rngsubtype {
			t.Errorf("rngtypid %d: rngsubtype=%d want %d", w.rngtypid, e.RngSubtype, w.rngsubtype)
		}
		if e.RngMultiTypID != w.rngmultitypid {
			t.Errorf("rngtypid %d: rngmultitypid=%d want %d", w.rngtypid, e.RngMultiTypID, w.rngmultitypid)
		}
		if e.RngSubOpc != w.rngsubopc {
			t.Errorf("rngtypid %d: rngsubopc=%d want %d", w.rngtypid, e.RngSubOpc, w.rngsubopc)
		}
		if e.RngCanonical != w.rngcanonical {
			t.Errorf("rngtypid %d: rngcanonical=%d want %d", w.rngtypid, e.RngCanonical, w.rngcanonical)
		}
		if e.RngSubDiff != w.rngsubdiff {
			t.Errorf("rngtypid %d: rngsubdiff=%d want %d", w.rngtypid, e.RngSubDiff, w.rngsubdiff)
		}
	}
}

// TestBootstrapPgRangeTuplesWritesHeap pins that bootstrapPgRangeTuples
// writes a valid heap file to base/{1,5}/3541 and returns 6 TIDs.
func TestBootstrapPgRangeTuplesWritesHeap(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgRangeTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgRangeTuples: %v", err)
	}
	if len(tids) != 6 {
		t.Errorf("TID map length: got %d, want 6", len(tids))
	}
	for _, e := range pgRangeInitialEntries() {
		if _, ok := tids[e.RngTypID]; !ok {
			t.Errorf("no TID for rngtypid %d", e.RngTypID)
		}
	}
	for _, sub := range []string{"base/1/3541", "base/5/3541"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("heap file %s missing: %v", sub, err)
			continue
		}
		if info.Size()%8192 != 0 {
			t.Errorf("heap file %s size %d is not a multiple of 8192", sub, info.Size())
		}
	}
}

// TestBootstrapPgRangeRngtypidIndexWritesPopulatedBtree pins that
// bootstrapPgRangeRngtypidIndex writes a ≥2-page btree to base/{1,5}/3542.
func TestBootstrapPgRangeRngtypidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgRangeTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgRangeTuples: %v", err)
	}
	if err := bootstrapPgRangeRngtypidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgRangeRngtypidIndex: %v", err)
	}
	for _, sub := range []string{"base/1/3542", "base/5/3542"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("index file %s missing: %v", sub, err)
			continue
		}
		if info.Size() < 2*8192 {
			t.Errorf("index file %s size %d too small (want ≥2 pages)", sub, info.Size())
		}
	}
}

// TestBootstrapPgRangeRngmultitypidIndexWritesPopulatedBtree pins that
// bootstrapPgRangeRngmultitypidIndex writes a ≥2-page btree to
// base/{1,5}/2228.
func TestBootstrapPgRangeRngmultitypidIndexWritesPopulatedBtree(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgRangeTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgRangeTuples: %v", err)
	}
	if err := bootstrapPgRangeRngmultitypidIndex(dir, tids); err != nil {
		t.Fatalf("bootstrapPgRangeRngmultitypidIndex: %v", err)
	}
	for _, sub := range []string{"base/1/2228", "base/5/2228"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("index file %s missing: %v", sub, err)
			continue
		}
		if info.Size() < 2*8192 {
			t.Errorf("index file %s size %d too small (want ≥2 pages)", sub, info.Size())
		}
	}
}
