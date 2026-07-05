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

// TestTSDictDDLRecoveryReplaysRenameSetSchemaOptions guards the DU-002 ALTER
// TEXT SEARCH DICTIONARY follow-up (M0119-0004): RENAME TO/SET SCHEMA/the
// ( key [= value], ... ) options-merge form must survive a restart, mirroring
// TestTSConfigDDLRecoveryReplaysRenameSetSchemaDropMapping.
func TestTSDictDDLRecoveryReplaysRenameSetSchemaOptions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40470)
	const wantSchemaOID = uint32(40471)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateSchema("mydictschema", wantSchemaOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-schema: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSDict("simple_dict2", "public", "stopwords = 'english'", wantOID, 10, 3727)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tsdict: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterTSDictOptions("simple_dict2", "public", "stopwords = 'english', accept = 'false'")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append alter-tsdict-options: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeRenameTSDict("simple_dict2", "public", "simple_dict2_renamed")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append rename-tsdict: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeSetTSDictSchema("simple_dict2_renamed", "public", "mydictschema")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append set-tsdict-schema: %v", werr)
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
	if ud := findUserTSDict(cat, "simple_dict2", "public"); ud != nil {
		t.Errorf("old name/schema still resolves after RENAME+SET SCHEMA replay: %+v", ud)
	}
	if ud := findUserTSDict(cat, "simple_dict2_renamed", "public"); ud != nil {
		t.Errorf("renamed dictionary still in old schema after SET SCHEMA replay: %+v", ud)
	}
	ud := findUserTSDict(cat, "simple_dict2_renamed", "mydictschema")
	if ud == nil {
		t.Fatalf("after WAL replay, renamed+moved dictionary not found; registry = %+v", cat.ListUserTSDicts())
	}
	if ud.OID != wantOID {
		t.Errorf("after WAL replay, dictionary OID = %d, want %d", ud.OID, wantOID)
	}
	if want := "stopwords = 'english', accept = 'false'"; ud.InitOption != want {
		t.Errorf("after WAL replay, InitOption = %q, want %q", ud.InitOption, want)
	}
}

// TestTSConfigDDLRecoveryReplaysReplaceMappingDict guards the ALTER MAPPING
// REPLACE follow-up to M0119-0004 slice 446: the dictionary substitution
// must survive a restart exactly like RENAME/SET SCHEMA/DROP MAPPING
// already do (TestTSConfigDDLRecoveryReplaysRenameSetSchemaDropMapping).
// Covers both the token-type-scoped and bare replace forms across two
// separate configurations in the same WAL stream.
func TestTSConfigDDLRecoveryReplaysReplaceMappingDict(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID1 = uint32(40463)
	const wantOID2 = uint32(40464)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSConfig("scopedcfg", "public", wantOID1, 10, 3722)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tsconfig scopedcfg: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("scopedcfg", "public", "asciiword", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping asciiword: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("scopedcfg", "public", "word", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping word: %v", werr)
	}
	// Scoped REPLACE: only asciiword's dictionary is substituted.
	if _, _, werr := rt1.WAL.Append(wal.EncodeReplaceTSConfigMappingDict("scopedcfg", "public", []string{"asciiword"}, 3765, 16400)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append replace-mapping-dict (scoped): %v", werr)
	}

	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSConfig("barecfg", "public", wantOID2, 10, 3722)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tsconfig barecfg: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("barecfg", "public", "asciiword", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping barecfg asciiword: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("barecfg", "public", "word", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping barecfg word: %v", werr)
	}
	// Bare REPLACE (no token-type list): every mapped token type is
	// substituted.
	if _, _, werr := rt1.WAL.Append(wal.EncodeReplaceTSConfigMappingDict("barecfg", "public", nil, 3765, 16401)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append replace-mapping-dict (bare): %v", werr)
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

	scoped := findUserTSConfig(cat, "scopedcfg", "public")
	if scoped == nil {
		t.Fatalf("after WAL replay, configuration \"scopedcfg\" not found; registry = %+v", cat.ListUserTSConfigs())
	}
	for _, m := range scoped.Mappings {
		switch m.TokenType {
		case "asciiword":
			if len(m.DictOIDs) != 1 || m.DictOIDs[0] != 16400 {
				t.Errorf("scopedcfg asciiword DictOIDs = %v, want [16400] (replaced)", m.DictOIDs)
			}
		case "word":
			if len(m.DictOIDs) != 1 || m.DictOIDs[0] != 3765 {
				t.Errorf("scopedcfg word DictOIDs = %v, want [3765] (untouched by scoped REPLACE)", m.DictOIDs)
			}
		}
	}

	bare := findUserTSConfig(cat, "barecfg", "public")
	if bare == nil {
		t.Fatalf("after WAL replay, configuration \"barecfg\" not found; registry = %+v", cat.ListUserTSConfigs())
	}
	for _, m := range bare.Mappings {
		if len(m.DictOIDs) != 1 || m.DictOIDs[0] != 16401 {
			t.Errorf("barecfg %s DictOIDs = %v, want [16401] (bare REPLACE touches every mapped token type)", m.TokenType, m.DictOIDs)
		}
	}
}

// TestTSConfigDDLRecoveryReplaysAlterMapping guards the ALTER MAPPING FOR tok
// WITH dict [, ...] override follow-up to M0119-0004 slice 446: the
// wholesale dictionary-list replacement for an already-mapped token type
// (and the plain insert for a not-yet-mapped one) must survive a restart
// exactly like the sibling ADD/DROP/RENAME/SET SCHEMA/REPLACE forms already
// do.
func TestTSConfigDDLRecoveryReplaysAlterMapping(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40465)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateTSConfig("altercfg", "public", wantOID, 10, 3722)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-tsconfig: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("altercfg", "public", "asciiword", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping asciiword: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAddTSConfigMapping("altercfg", "public", "word", []uint32{3765})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append add-mapping word: %v", werr)
	}
	// Override asciiword's entire dictionary list wholesale.
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterTSConfigMapping("altercfg", "public", "asciiword", []uint32{16410, 16411})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append alter-mapping asciiword: %v", werr)
	}
	// Override a not-yet-mapped token type — same effect as ADD MAPPING.
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterTSConfigMapping("altercfg", "public", "numword", []uint32{16412})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append alter-mapping numword: %v", werr)
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

	cfg := findUserTSConfig(cat, "altercfg", "public")
	if cfg == nil {
		t.Fatalf("after WAL replay, configuration \"altercfg\" not found; registry = %+v", cat.ListUserTSConfigs())
	}
	seen := map[string][]uint32{}
	for _, m := range cfg.Mappings {
		seen[m.TokenType] = m.DictOIDs
	}
	if got := seen["asciiword"]; len(got) != 2 || got[0] != 16410 || got[1] != 16411 {
		t.Errorf("altercfg asciiword DictOIDs = %v, want [16410 16411] (overridden)", got)
	}
	if got := seen["word"]; len(got) != 1 || got[0] != 3765 {
		t.Errorf("altercfg word DictOIDs = %v, want [3765] (untouched)", got)
	}
	if got := seen["numword"]; len(got) != 1 || got[0] != 16412 {
		t.Errorf("altercfg numword DictOIDs = %v, want [16412] (newly created via override)", got)
	}
}
