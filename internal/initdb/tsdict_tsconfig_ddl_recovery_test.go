package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// findUserTSDict looks up a user text search dictionary by (schema, name) in
// the registry snapshot returned by ListUserTSDicts, resolving schema via the
// catalog's schema-name map (mirrors CreateTSDict's own resolution).
func findUserTSDict(cat *catalog.InMemory, name, schema string) *catalog.UserTSDict {
	nsOID := cat.SchemaOID(schema)
	for _, ud := range cat.ListUserTSDicts() {
		if ud.Name == name && ud.NamespaceOID == nsOID {
			return ud
		}
	}
	return nil
}

// findUserTSConfig is the CREATE TEXT SEARCH CONFIGURATION analog of
// findUserTSDict.
func findUserTSConfig(cat *catalog.InMemory, name, schema string) *catalog.UserTSConfig {
	nsOID := cat.SchemaOID(schema)
	for _, uc := range cat.ListUserTSConfigs() {
		if uc.Name == name && uc.NamespaceOID == nsOID {
			return uc
		}
	}
	return nil
}

// TestTSDictDDLRecoveryReplaysCreate confirms the DU-002 restart-persistence
// follow-up to slice 437 (M0119-0004): a CREATE TEXT SEARCH DICTIONARY WAL
// record written by a pre-crash run is replayed into the catalog's
// dictionary registry on the post-crash Open path, so pg_ts_dict (pg_dump's
// getTSDictionaries/dumpTSDictionary) round-trips after restart even though
// the goopg process never wrote a JSON snapshot.
func TestTSDictDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40460)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSDict("simple_dict", "public", `"STOPWORDS" = 'english'`, wantOID, 10, 3727)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tsdict: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	ud := findUserTSDict(cat, "simple_dict", "public")
	if ud == nil {
		t.Fatalf("after WAL replay, dictionary \"simple_dict\" not found; registry = %+v", cat.ListUserTSDicts())
	}
	if ud.OID != wantOID || ud.Template != 3727 || ud.InitOption != `"STOPWORDS" = 'english'` {
		t.Errorf("after WAL replay, dictionary = %+v, want OID=%d Template=3727 InitOption=`\"STOPWORDS\" = 'english'`", ud, wantOID)
	}
}

// TestTSDictDDLRecoveryReplaysDropAfterCreate confirms that a CREATE followed
// by a DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestTSDictDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSDict("simple_dict", "public", "", 40500, 10, 3727)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropTSDict("simple_dict", "public")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat := rt2.Catalog.(*catalog.InMemory)
	if ud := findUserTSDict(cat, "simple_dict", "public"); ud != nil {
		t.Errorf("after CREATE + DROP replay, dictionary \"simple_dict\" = %+v, want nil", ud)
	}
}

// TestReplayTSDictDDLRecordsHandlesMissingWalDir verifies the recovery hook
// is idempotent when invoked against a missing pg_wal directory (brand new
// initdb).
func TestReplayTSDictDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayTSDictDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListUserTSDicts()) != 0 {
		t.Errorf("no-op replay should not register any dictionary, got %+v", cat.ListUserTSDicts())
	}
}

// TestTSConfigDDLRecoveryReplaysCreateAndMapping confirms the DU-002
// restart-persistence follow-up to slice 446 (M0119-0004): a CREATE TEXT
// SEARCH CONFIGURATION record plus its ADD MAPPING records survive a
// restart in WAL order, so pg_ts_config/pg_ts_config_map (pg_dump's
// dumpTSConfig) round-trip after restart.
func TestTSConfigDDLRecoveryReplaysCreateAndMapping(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40460)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSConfig("myconfig", "public", wantOID, 10, 3722)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tsconfig: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("myconfig", "public", "asciiword", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	uc := findUserTSConfig(cat, "myconfig", "public")
	if uc == nil {
		t.Fatalf("after WAL replay, configuration \"myconfig\" not found; registry = %+v", cat.ListUserTSConfigs())
	}
	if uc.OID != wantOID || uc.Parser != 3722 {
		t.Errorf("after WAL replay, configuration = %+v, want OID=%d Parser=3722", uc, wantOID)
	}
	if len(uc.Mappings) != 1 || uc.Mappings[0].TokenType != "asciiword" || len(uc.Mappings[0].DictOIDs) != 1 || uc.Mappings[0].DictOIDs[0] != 3765 {
		t.Errorf("after WAL replay, mappings = %+v, want one asciiword→[3765] entry", uc.Mappings)
	}
}

// TestTSConfigDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// followed by a DROP cancels out.
func TestTSConfigDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSConfig("myconfig", "public", 40500, 10, 3722)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropTSConfig("myconfig", "public")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat := rt2.Catalog.(*catalog.InMemory)
	if uc := findUserTSConfig(cat, "myconfig", "public"); uc != nil {
		t.Errorf("after CREATE + DROP replay, configuration \"myconfig\" = %+v, want nil", uc)
	}
}

// TestReplayTSConfigDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayTSConfigDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayTSConfigDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListUserTSConfigs()) != 0 {
		t.Errorf("no-op replay should not register any configuration, got %+v", cat.ListUserTSConfigs())
	}
}

// TestTSConfigDDLRecoveryReplaysRenameSetSchemaDropMapping guards the
// M0119-0004 slice 446 follow-up: a configuration's RENAME TO / SET SCHEMA /
// DROP MAPPING must survive a restart exactly like its CREATE/ADD MAPPING
// already do (TestTSConfigDDLRecoveryReplaysCreateAndMapping).
func TestTSConfigDDLRecoveryReplaysRenameSetSchemaDropMapping(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40461)
	const wantSchemaOID = uint32(40462)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateSchema("myschema", wantSchemaOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-schema: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSConfig("myconfig2", "public", wantOID, 10, 3722)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tsconfig: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("myconfig2", "public", "asciiword", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("myconfig2", "public", "word", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping (word): %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropTSConfigMapping("myconfig2", "public", "word")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop-mapping: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeRenameTSConfig("myconfig2", "public", "myconfig2_renamed")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append rename-tsconfig: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeSetTSConfigSchema("myconfig2_renamed", "public", "myschema")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append set-tsconfig-schema: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	if uc := findUserTSConfig(cat, "myconfig2", "public"); uc != nil {
		t.Errorf("old name/schema still resolves after RENAME+SET SCHEMA replay: %+v", uc)
	}
	if uc := findUserTSConfig(cat, "myconfig2_renamed", "public"); uc != nil {
		t.Errorf("renamed configuration still in old schema after SET SCHEMA replay: %+v", uc)
	}
	uc := findUserTSConfig(cat, "myconfig2_renamed", "myschema")
	if uc == nil {
		t.Fatalf("after WAL replay, renamed+moved configuration not found; registry = %+v", cat.ListUserTSConfigs())
	}
	if uc.OID != wantOID {
		t.Errorf("after WAL replay, configuration OID = %d, want %d", uc.OID, wantOID)
	}
	if len(uc.Mappings) != 1 || uc.Mappings[0].TokenType != "asciiword" {
		t.Errorf("after WAL replay, mappings = %+v, want only the surviving asciiword entry (word was dropped)", uc.Mappings)
	}
}
