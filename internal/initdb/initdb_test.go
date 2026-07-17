package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// TestInitLaysOutDirectoryStructure pins the directory layout
// promised by .ralph/specs/GOAL_AND_REQUIREMENTS.md §6.1: every
// load-bearing subdirectory exists with the expected mode, plus
// PG_VERSION, postgresql.conf, and pg_hba.conf written at the
// root.
func TestInitLaysOutDirectoryStructure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	for _, sub := range Subdirs {
		st, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("missing subdir %q: %v", sub, err)
			continue
		}
		if !st.IsDir() {
			t.Errorf("%q is not a directory", sub)
		}
		if st.Mode().Perm() != 0o700 {
			t.Errorf("%q mode=%o want 0700", sub, st.Mode().Perm())
		}
	}
	// pg_subtrans is critical for PG standby startup (M0105-0004).
	// StartupSUBTRANS() accesses this directory; without it the PG process crashes.
	if _, err := os.Stat(filepath.Join(dir, "pg_subtrans")); err != nil {
		t.Errorf("missing CRITICAL subdir pg_subtrans: %v", err)
	}
	// base/{1,4,5}/PG_VERSION must exist for all seeded databases (M0106-0010 batched-08).
	for _, dbOID := range []string{"1", "4", "5"} {
		pvPath := filepath.Join(dir, "base", dbOID, "PG_VERSION")
		pv, err := os.ReadFile(pvPath)
		if err != nil {
			t.Errorf("missing base/%s/PG_VERSION: %v", dbOID, err)
			continue
		}
		if string(pv) != "18\n" {
			t.Errorf("base/%s/PG_VERSION=%q want %q", dbOID, string(pv), "18\n")
		}
	}
	for _, want := range []string{"PG_VERSION", "postgresql.conf", "pg_hba.conf"} {
		path := filepath.Join(dir, want)
		st, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing %q: %v", want, err)
			continue
		}
		if st.Size() == 0 {
			t.Errorf("%q is empty", want)
		}
	}
	pv, err := os.ReadFile(filepath.Join(dir, "PG_VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pv)) != CatalogVersion {
		t.Errorf("PG_VERSION=%q want %q", strings.TrimSpace(string(pv)), CatalogVersion)
	}
}

// TestInitRefusesNonEmptyDir matches upstream initdb's "directory
// not empty" guard so users can't accidentally clobber a real
// PG installation.
func TestInitRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Init(Options{DataDir: dir, NoSync: true})
	if err == nil {
		t.Fatal("expected error for non-empty dir")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("err=%q, want a 'not empty' message", err.Error())
	}
}

// TestInitAcceptsExistingEmptyDir: an existing-but-empty target
// directory is fine — operators commonly pre-create the mountpoint
// with the right permissions before running goopg init.
func TestInitAcceptsExistingEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("init on empty existing dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err != nil {
		t.Errorf("PG_VERSION missing after init: %v", err)
	}
}

// TestInitRejectsEmptyOption surfaces a clean error rather than
// silently writing to "" / current working directory.
func TestInitRejectsEmptyOption(t *testing.T) {
	if err := Init(Options{}); err == nil {
		t.Fatal("expected error for empty DataDir")
	}
}

// TestInitCreatesSystemCatalogRelfiles verifies that goopg init creates
// one heap relfile for each of the three core system catalogs under
// base/<DefaultDBOid>/. Each file must be exactly BlockSize bytes (one
// initialised empty page), confirming bootstrapSystemCatalogs ran.
func TestInitCreatesSystemCatalogRelfiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	type entry struct {
		name string
		oid  uint32
	}
	sysRels := []entry{
		{"pg_type", catalog.TypeRelationId},
		{"pg_attribute", catalog.AttributeRelationId},
		{"pg_class", catalog.RelationRelationId},
	}
	for _, rel := range sysRels {
		path := filepath.Join(dir, "base",
			fmt.Sprint(catalog.DefaultDBOid),
			fmt.Sprint(rel.oid))
		st, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s (OID %d): expected file %q not found: %v", rel.name, rel.oid, path, err)
			continue
		}
		if st.IsDir() {
			t.Errorf("%s: path is a directory", rel.name)
			continue
		}
		// All three may span multiple blocks (pg_class/pg_attribute due to
		// nailed-relation tuples (M0106-0008); pg_type due to the expanded
		// pg_type.dat seed (batched-15)).
		if st.Size()%int64(storage.BlockSize) != 0 || st.Size() < int64(storage.BlockSize) {
			t.Errorf("%s: size=%d not a multiple of block size %d", rel.name, st.Size(), storage.BlockSize)
		}
	}
}

// TestSystemCatalogRelfilesAreValidHeapPages checks that the relfiles
// written by bootstrapSystemCatalogs contain a valid initialised page
// (not raw zeros) — i.e. InitPage ran successfully.
func TestSystemCatalogRelfilesAreValidHeapPages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	for _, oid := range []uint32{
		catalog.TypeRelationId,
		catalog.AttributeRelationId,
		catalog.RelationRelationId,
	} {
		path := filepath.Join(dir, "base",
			fmt.Sprint(catalog.DefaultDBOid),
			fmt.Sprint(oid))
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("OID %d: open %q: %v", oid, path, err)
		}
		raw := make([]byte, storage.BlockSize)
		n, readErr := f.Read(raw)
		f.Close()
		if n != storage.BlockSize {
			t.Fatalf("OID %d: read %d bytes from first block (want %d): %v", oid, n, storage.BlockSize, readErr)
		}
		// A properly initialised page must NOT be all-zeros: InitPage
		// writes a non-zero pd_pagesize_version field.
		allZero := true
		for _, b := range raw {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			t.Errorf("OID %d: page is all-zeros (InitPage did not run?)", oid)
		}
		// Verify storage.IsNew reports false — an initialised page
		// has pd_upper set to BlockSize, not 0.
		if storage.IsNew(storage.Page(raw)) {
			t.Errorf("OID %d: page reports IsNew=true (header not written?)", oid)
		}
	}
}

// TestPGHBADefaultRules: the sample pg_hba.conf trusts loopback
// and rejects everything else, matching auth.DefaultPolicy() so
// goopg init's defaults align with goopg start's defaults.
func TestPGHBADefaultRules(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "pg_hba.conf"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, needle := range []string{"127.0.0.1/32    trust", "::1/128         trust", "0.0.0.0/0       reject"} {
		if !strings.Contains(got, needle) {
			t.Errorf("pg_hba.conf missing %q", needle)
		}
	}
}

func TestPostgresqlAutoConfHeader(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "postgresql.auto.conf"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, needle := range []string{
		"# Do not edit this file manually!",
		"# It will be overwritten by the ALTER SYSTEM command.",
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("postgresql.auto.conf missing %q", needle)
		}
	}
}

// TestBootstrappedPGTypeRowsReadable verifies that the pg_type relfile
// written during initdb contains decodeable rows for the built-in types.
func TestBootstrappedPGTypeRowsReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	// Scan all blocks of pg_type. The file spans multiple blocks with the
	// expanded 194-entry seed (batched-15); reading only block 0 misses types
	// with large OIDs such as varchar (1043), timestamp (1114), numeric (1700).
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.TypeRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := mgr.NBlocks(rel)
	if err != nil || nBlocks == 0 {
		t.Fatalf("pg_type relfile absent or empty: err=%v nblocks=%d", err, nBlocks)
	}

	typesByOID := map[uint32]catalog.PGTypeRow{}
	page := make(storage.Page, storage.BlockSize)
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		if err := mgr.ReadBlock(rel, blk, page); err != nil {
			t.Fatalf("ReadBlock pg_type blk %d: %v", blk, err)
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			row, err := catalog.DecodePGTypeRow(ht.Data)
			if err != nil {
				var err2 error
				row, err2 = catalog.DecodePGTypePhysicalRow(ht.Data)
				if err2 != nil {
					continue
				}
			}
			typesByOID[row.OID] = row
		}
	}

	// Verify expected types are present.
	required := map[uint32]string{
		catalog.OIDBool:      "bool",
		catalog.OIDInt4:      "int4",
		catalog.OIDInt8:      "int8",
		catalog.OIDText:      "text",
		catalog.OIDVarChar:   "varchar",
		catalog.OIDTimestamp: "timestamp",
		catalog.OIDNumeric:   "numeric",
	}
	for oid, wantName := range required {
		row, ok := typesByOID[oid]
		if !ok {
			t.Errorf("pg_type: OID %d (%s) not found", oid, wantName)
			continue
		}
		if row.TypName != wantName {
			t.Errorf("OID %d: typname=%q want %q", oid, row.TypName, wantName)
		}
		if row.TypNamespace != catalog.PGCatalogNamespaceOID {
			t.Errorf("OID %d: typnamespace=%d want %d", oid, row.TypNamespace, catalog.PGCatalogNamespaceOID)
		}
	}
}

// TestBootstrappedPGClassRowsReadable verifies that pg_class contains the
// three self-referential system catalog entries.
func TestBootstrappedPGClassRowsReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatalf("ReadBlock pg_class: %v", err)
	}

	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatalf("PageLinePointerCount: %v", err)
	}

	classByOID := map[uint32]catalog.PGClassRow{}
	for slot := uint16(1); slot <= uint16(count); slot++ {
		ht, err := storage.PageGetHeapTuple(page, slot)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		row, err := catalog.DecodePGClassRow(ht.Data)
		if err != nil {
			var err2 error
			row, err2 = catalog.DecodePGClassPhysicalRow(ht.Data)
			if err2 != nil {
				continue
			}
		}
		classByOID[row.OID] = row
	}

	required := map[uint32]string{
		catalog.TypeRelationId:      "pg_type",
		catalog.AttributeRelationId: "pg_attribute",
		catalog.RelationRelationId:  "pg_class",
	}
	for oid, wantName := range required {
		row, ok := classByOID[oid]
		if !ok {
			t.Errorf("pg_class: OID %d (%s) not found", oid, wantName)
			continue
		}
		if row.RelName != wantName {
			t.Errorf("OID %d: relname=%q want %q", oid, row.RelName, wantName)
		}
		if row.RelFileNode != oid {
			t.Errorf("OID %d: relfilenode=%d want %d", oid, row.RelFileNode, oid)
		}
	}
}

// TestBootstrappedPGAttributeRowsReadable verifies that pg_attribute
// contains column definitions for all three system catalogs.
func TestBootstrappedPGAttributeRowsReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	// Scan all blocks. pg_attribute spans multiple blocks because it stores
	// column definitions for all nailed relations (M0106-0008, ~264 rows).
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	nBlocks, err := mgr.NBlocks(rel)
	if err != nil || nBlocks == 0 {
		t.Fatalf("pg_attribute relfile absent or empty: err=%v nblocks=%d", err, nBlocks)
	}

	// Gather attnames per relation.
	byRelID := map[uint32][]string{}
	page := make(storage.Page, storage.BlockSize)
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		if err := mgr.ReadBlock(rel, blk, page); err != nil {
			t.Fatalf("ReadBlock pg_attribute blk %d: %v", blk, err)
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			row, err := catalog.DecodePGAttributeRow(ht.Data)
			if err != nil {
				var err2 error
				row, err2 = catalog.DecodePGAttributePhysicalRow(ht.Data)
				if err2 != nil {
					continue
				}
			}
			byRelID[row.AttRelID] = append(byRelID[row.AttRelID], row.AttName)
		}
	}

	// Full PG18 column counts: pg_class=34, pg_attribute=25, pg_type=14.
	// These reflect the PG18 physical tuple layout written by bootstrapPg*Tuples
	// (M0106-0008). The original goopg-only counts (8, 6, 7) are obsolete.
	// pg_attribute grew 24→25 when attstattarget was appended (DU-002 slice 24)
	// so pg_dump's getTableAttrs can read a.attstattarget.
	for relOID, wantCount := range map[uint32]int{
		catalog.RelationRelationId:  34,
		catalog.AttributeRelationId: 25,
		catalog.TypeRelationId:      14,
	} {
		cols := byRelID[relOID]
		if len(cols) != wantCount {
			t.Errorf("pg_attribute: relOID %d has %d cols, want %d (cols: %v)",
				relOID, len(cols), wantCount, cols)
		}
	}

	// Spot-check: pg_class must have an "oid" and "relname" entry.
	pgClassCols := byRelID[catalog.RelationRelationId]
	found := map[string]bool{}
	for _, c := range pgClassCols {
		found[c] = true
	}
	for _, must := range []string{"oid", "relname", "relkind"} {
		if !found[must] {
			t.Errorf("pg_attribute: pg_class missing column %q", must)
		}
	}
	_ = fmt.Sprintf // suppress unused import
}
