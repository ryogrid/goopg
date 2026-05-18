// Package initdb lays out a fresh goopg data directory.
//
// Mirrors upstream's initdb at the directory level (base/, global/,
// pg_wal/, pg_xact/, PG_VERSION) so an operator familiar with
// PostgreSQL can navigate the tree. System catalog heap files
// (pg_class, pg_attribute, pg_type) are created as real relfiles
// during Init so subsequent Open finds the OID-keyed files under
// base/<DBOid>/. The design rationale is in
// docs/design/0017-data-directory.md and
// docs/design/0030-0001-system-catalog-heap-substrate.md.
//
// Spec references:
//
//   - .ralph/specs/GOAL_AND_REQUIREMENTS.md §6.1 ("layout should
//     mirror PostgreSQL's at the directory level")
//   - .ralph/specs/GOAL_AND_REQUIREMENTS.md §10 #2 ("goopg init
//     creates a data directory")
package initdb

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// systemIdentifierFile is the path (relative to the data directory) where
// the 8-byte cluster system identifier is stored. Matches PostgreSQL's
// pg_control convention: a random uint64 generated at initdb time that
// uniquely identifies the cluster. M0101-0001.
const systemIdentifierFile = "global/system_identifier"

// LoadOrCreateSystemID reads the cluster system identifier from
// <dataDir>/global/system_identifier. If the file does not exist (e.g. for
// clusters created by older goopg versions), it generates a new random uint64,
// persists it, and returns it. This value is embedded in every PG-compatible
// WAL page header so pg_waldump can cross-check segment consistency.
func LoadOrCreateSystemID(dataDir string) (uint64, error) {
	path := filepath.Join(dataDir, systemIdentifierFile)
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 8 {
			return 0, fmt.Errorf("goopg: system_identifier: unexpected length %d", len(data))
		}
		return binary.LittleEndian.Uint64(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("goopg: read system_identifier: %w", err)
	}
	// Generate a new random system identifier.
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, fmt.Errorf("goopg: generate system_identifier: %w", err)
	}
	id := binary.LittleEndian.Uint64(buf[:])
	if err := os.WriteFile(path, buf[:], 0o600); err != nil {
		return 0, fmt.Errorf("goopg: write system_identifier: %w", err)
	}
	return id, nil
}

// CatalogVersion is the value written into the data directory's
// `PG_VERSION` file. It must match the major version goopg reports
// in the `server_version` ParameterStatus during the wire-protocol
// handshake (currently "18", aligned with PostgreSQL 18.x).
const CatalogVersion = "18"

// Subdirs is the canonical list of directories goopg init creates
// under the data directory. The list is exported so tests and the
// pg_ctl-shaped administrative tooling can assert against it.
var Subdirs = []string{
	"base",
	"global",
	"pg_wal",
	"pg_wal/archive_status", // PG ValidateXLOGDirectoryStructure creates if missing; safe to pre-create.
	"pg_wal/summaries",
	"pg_xact",
	// SLRU directories PG requires during startup (M0105-0004).
	"pg_subtrans", // CRITICAL: StartupSUBTRANS() needs this for transaction parent lookup.
	"pg_multixact",
	"pg_multixact/members",
	"pg_multixact/offsets",
	// PG-required directories so pg_basebackup clones and pg_ctl start
	// against an imported backup succeed (M0102-0007).
	"pg_commit_ts",
	"pg_dynshmem",
	"pg_logical",
	"pg_logical/snapshots",
	"pg_logical/mappings",
	"pg_notify",
	"pg_replslot",
	"pg_serial",
	"pg_snapshots",
	"pg_stat",
	"pg_stat_tmp",
	"pg_tblspc",
	"pg_twophase",
}

// Files lists the regular files goopg init writes alongside the
// subdirectories. Each entry is a (relative path, builder) pair so
// callers (and tests) can introspect the layout without re-running
// init.
type FileSpec struct {
	Path    string
	Build   func() []byte
	Mode    os.FileMode
}

// SampleFiles returns the file list goopg init writes. The values
// are deterministic so two `goopg init` runs against fresh dirs
// produce byte-identical layouts.
func SampleFiles() []FileSpec {
	return []FileSpec{
		{Path: "PG_VERSION", Build: func() []byte { return []byte(CatalogVersion + "\n") }, Mode: 0o600},
		{Path: "postgresql.conf", Build: defaultPostgresqlConf, Mode: 0o600},
		{Path: "pg_hba.conf", Build: defaultPgHBAConf, Mode: 0o600},
		{Path: "pg_ident.conf", Build: defaultPgIdentConf, Mode: 0o600},
		{Path: "global/pg_filenode.map", Build: defaultRelMapFile, Mode: 0o600},
	}
}

// Options controls goopg init.
type Options struct {
	// DataDir is the absolute or relative path to the directory
	// goopg init should populate. The directory is created if it
	// doesn't exist; if it exists and is non-empty, init refuses
	// (matching upstream initdb's "directory not empty" guard).
	DataDir string
}

// Init lays out the data directory according to opts.
func Init(opts Options) error {
	if opts.DataDir == "" {
		return errors.New("goopg init: -D <data-directory> is required")
	}
	abs, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return fmt.Errorf("goopg init: resolve %q: %w", opts.DataDir, err)
	}
	if err := ensureEmptyDir(abs); err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("goopg init: create %q: %w", abs, err)
	}
	for _, sub := range Subdirs {
		path := filepath.Join(abs, sub)
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("goopg init: mkdir %q: %w", path, err)
		}
	}
	// Default-database directory under base/. DBOid matches
	// catalog.DefaultDBOid so the on-disk layout aligns with what
	// the in-memory catalog hands out. Upstream initdb creates
	// base/1 (template1) plus base/<oid> for each database; v0
	// only needs the one.
	defaultDB := filepath.Join(abs, "base", strconv.FormatUint(uint64(catalog.DefaultDBOid), 10))
	if err := os.Mkdir(defaultDB, 0o700); err != nil {
		return fmt.Errorf("goopg init: mkdir %q: %w", defaultDB, err)
	}
	for _, f := range SampleFiles() {
		path := filepath.Join(abs, f.Path)
		if err := os.WriteFile(path, f.Build(), f.Mode); err != nil {
			return fmt.Errorf("goopg init: write %q: %w", path, err)
		}
	}
	if err := bootstrapSystemCatalogs(abs); err != nil {
		return fmt.Errorf("goopg init: system catalogs: %w", err)
	}
	// M0105: create empty placeholder relfiles for PG shared catalogs.
	// PG backends open these files during startup (e.g. pg_authid,
	// pg_database). Without them, PG FATALs with "could not open file".
	if err := bootstrapSharedCatalogPlaceholders(abs); err != nil {
		return fmt.Errorf("goopg init: shared catalog placeholders: %w", err)
	}
	// Overwrite pg_authid placeholder with a minimal "postgres" superuser row.
	if err := bootstrapPostgresRole(abs); err != nil {
		return fmt.Errorf("goopg init: postgres role: %w", err)
	}
	if err := bootstrapPostgresDatabase(abs); err != nil {
		return fmt.Errorf("goopg init: postgres database: %w", err)
	}
	// M0106-0008: populate pg_class/pg_attribute heap tuples so
	// vanilla PG's RelationBuildDesc → ScanPgRelation finds them.
	pgClassTIDs, err := bootstrapPgClassTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_class tuples: %w", err)
	}
	pgAttrTIDs, err := bootstrapPgAttributeTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_attribute tuples: %w", err)
	}
	// M0106-0010 step 2: write pg_am rows so PG's
	// RelationInitIndexAccessInfo → SearchSysCache1(AMOID, ...) does
	// not return NULL and PANIC when opening a critical index.
	if err := bootstrapPgAmTuples(abs); err != nil {
		return fmt.Errorf("goopg init: pg_am tuples: %w", err)
	}
	// M0106-0010 step 3a: write pg_proc rows for the AM handler
	// functions so PG's RelationInitIndexAccessInfo →
	// OidFunctionCall0(amhandler) finds bthandler /
	// heap_tableam_handler / etc. in the syscache.
	if err := bootstrapPgProcTuples(abs); err != nil {
		return fmt.Errorf("goopg init: pg_proc tuples: %w", err)
	}
	// M0106-0010 step 3b: write pg_opclass rows so PG's
	// RelationInitIndexAccessInfo → SearchSysCache1(CLAOID, ...)
	// resolves every opclass referenced by a nailed index's
	// indclass vector.
	if err := bootstrapPgOpclassTuples(abs); err != nil {
		return fmt.Errorf("goopg init: pg_opclass tuples: %w", err)
	}
	// M0106-0010 step 3c: write pg_amop strategy operator rows
	// (queried at planning time via AMOPSTRATEGY/AMOPOPID) and
	// pg_amproc support function rows (load-bearing — scanned by
	// LookupOpclassInfo during RelationInitIndexAccessInfo).
	if err := bootstrapPgAmopTuples(abs); err != nil {
		return fmt.Errorf("goopg init: pg_amop tuples: %w", err)
	}
	if err := bootstrapPgAmprocTuples(abs); err != nil {
		return fmt.Errorf("goopg init: pg_amproc tuples: %w", err)
	}
	// M0106-0010 step 3f: write an empty pg_index heap page so PG's
	// RelationOpenSmgr → mdopen during nailed-index initialisation
	// finds base/{1,5}/2610 on disk. The previous E2E run failed with
	// "FATAL: could not open file base/5/2610". A heap-initialised
	// page with zero tuples is the minimum that satisfies BasicOpenFile;
	// per-index rows come in the next step.
	pgIndexTIDs, err := bootstrapPgIndexTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_index tuples: %w", err)
	}
	// M0106-0010 step 3p/3r: overwrite the empty btree placeholder at
	// base/{1,5}/2679 + global/2679 with a populated 2-page btree
	// (metapage + leaf-root) carrying one oid-keyed IndexTuple per
	// Form_pg_index heap row so PG's
	// load_critical_index → RelationInitIndexAccessInfo →
	// SearchSysCache1(INDEXRELID, oid) — the call taken once
	// criticalRelcachesBuilt becomes true while loading the
	// SHARED critical indexes — finds the pg_index row via
	// pg_index_indexrelid_index (PG18 OID = 2679, not 2678 as Step
	// 3q originally claimed; Step 3r restores the correct OID).
	// Without this the next FATAL during standby boot is "cache
	// lookup failed for index 2671".
	if err := bootstrapPgIndexIndexrelidIndex(abs, pgIndexTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_index_indexrelid_index: %w", err)
	}
	// M0106-0010 step 3l: overwrite the empty btree placeholder at
	// base/{1,5}/2687 + global/2687 with a populated 2-page btree
	// (metapage + leaf-root) carrying one IndexTuple per pg_opclass
	// row so PG's LookupOpclassInfo(1986) finds the name_ops row
	// via pg_opclass_oid_index. Without this the next FATAL during
	// standby boot is "could not find tuple for opclass 1986".
	if err := bootstrapPgOpclassOidIndex(abs); err != nil {
		return fmt.Errorf("goopg init: pg_opclass_oid_index: %w", err)
	}
	// M0106-0010 step 3m: overwrite the empty btree placeholder at
	// base/{1,5}/2662 + global/2662 with a populated 2-page btree
	// (metapage + leaf-root) carrying one oid-keyed IndexTuple per
	// pg_class heap row so PG's ScanPgRelation(oid, indexOK=true)
	// — invoked once criticalRelcachesBuilt becomes true while
	// loading the SHARED critical indexes — can find pg_class rows
	// via pg_class_oid_index. Without this the next FATAL during
	// standby boot is "could not open critical system index 2671".
	if err := bootstrapPgClassOidIndex(abs, pgClassTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_class_oid_index: %w", err)
	}
	// M0106-0010 step 3o: overwrite the empty btree placeholder at
	// base/{1,5}/2659 + global/2659 with a populated btree (metapage +
	// leaf-root) carrying one (attrelid, attnum)-keyed IndexTuple per
	// pg_attribute heap row so PG's RelationBuildTupleDesc →
	// systable_beginscan(AttributeRelidNumIndexId, {attrelid=X, attnum>0})
	// finds the column tuples via an index scan (the path taken once
	// criticalRelcachesBuilt = true — i.e. for every shared catalog
	// relation loaded after the local critical phase). Without this
	// the next FATAL during standby boot is "pg_attribute catalog is
	// missing N attribute(s) for relation OID …".
	if err := bootstrapPgAttributeRelidAttnumIndex(abs, pgAttrTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_attribute_relid_attnum_index: %w", err)
	}
	// M0106-0010 step 3w: write an empty heap page for every mapped local
	// catalog that lacks a dedicated bootstrapper (pg_aggregate=2600,
	// pg_type=1247, pg_namespace=2615, …). Without these files PG's
	// InitPostgres FATALs with `could not open relation with OID 2600` on
	// the first probe of pg_aggregate after Step 3v cleared the relcache
	// PANIC loop.
	if err := bootstrapMappedLocalCatalogHeaps(abs); err != nil {
		return fmt.Errorf("goopg init: mapped local catalog heaps: %w", err)
	}
	if err := bootstrapCLog(abs); err != nil {
		return fmt.Errorf("goopg init: clog: %w", err)
	}
	// M0105-0007: create zero-filled placeholder pages in SLRU
	// directories so pg_basebackup includes them in the backup.
	// pg_basebackup skips empty directories; PG standby startup
	// requires pg_subtrans/, pg_multixact/members/,
	// pg_multixact/offsets/ to exist.
	if err := bootstrapSLRUPlaceholders(abs); err != nil {
		return fmt.Errorf("goopg init: slru placeholders: %w", err)
	}
	// M0106: generate PG-compatible relcache init files so PG backends
	// can start from a goopg backup without PANIC on critical indexes.
	if err := bootstrapRelcacheInitFiles(abs); err != nil {
		return fmt.Errorf("goopg init: relcache init files: %w", err)
	}
	// Generate and persist the cluster system identifier (M0101-0001).
	// Used as xlp_sysid in PG-compatible WAL page headers.
	sysID, err := LoadOrCreateSystemID(abs)
	if err != nil {
		return fmt.Errorf("goopg init: system_identifier: %w", err)
	}
	// Write the PG-compatible pg_control file so pg_controldata,
	// pg_checksums, and other client tools can inspect the cluster
	// (M0095-0001).
	if err := writePgControl(abs, sysID); err != nil {
		return fmt.Errorf("goopg init: pg_control: %w", err)
	}
	return nil
}

// bootstrapSLRUPlaceholders writes zero-filled 8 KiB placeholder pages
// into pg_subtrans/ and pg_multixact/ sub-directories so pg_basebackup
// includes them in the backup. pg_basebackup skips empty directories;
// PG standby startup requires these directories to exist (StartupSUBTRANS,
// StartupMultiXact).
//
// Mirrors PostgreSQL's SLRU behaviour: a freshly-initialised page is a
// single BLCKSZ-length blob of zero bytes.
func bootstrapSLRUPlaceholders(dataDir string) error {
	zeroPage := make([]byte, storage.BlockSize)
	for _, relPath := range []string{
		"pg_subtrans/0000",
		"pg_multixact/members/0000",
		"pg_multixact/offsets/0000",
	} {
		path := filepath.Join(dataDir, relPath)
		if err := os.WriteFile(path, zeroPage, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// bootstrapSharedCatalogPlaceholders creates empty 8 KiB pages for PG
// shared system catalogs under `global/`. PG backends open these relfiles
// during startup (via the relation map) before the catalogs are fully
// bootstrapped. Without the files, PG FATALs with "could not open file".
func bootstrapSharedCatalogPlaceholders(dataDir string) error {
	heapPage := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(heapPage); err != nil {
		return err
	}

	// Shared catalog heap tables
	heapOIDs := []uint32{
		1260, // pg_authid
		1261, // pg_auth_members
		1262, // pg_database
		1213, // pg_tablespace
		1214, // pg_shdepend
		3592, // pg_shdescription
		6000, // pg_replication_origin
		6100, // pg_subscription
		6243, // pg_parameter_acl
	}
	for _, oid := range heapOIDs {
		path := filepath.Join(dataDir, "global", strconv.FormatUint(uint64(oid), 10))
		if err := os.WriteFile(path, heapPage, 0o600); err != nil {
			return err
		}
	}
	// NOTE: NOT creating critical index pages. If the index files
	// exist, PG uses index scans (returning empty) and never falls
	// back to sequential scan of the heap. Without index files,
	// PG's IndexScanOK() returns false for auth/database lookups
	// (!criticalSharedRelcachesBuilt), forcing a seq scan.
	return nil
}

// bootstrapMappedLocalCatalogHeaps writes a heap-initialised empty 8 KiB
// page for every mapped local system catalog that does NOT receive a
// dedicated PG18-shaped bootstrap (pg_class, pg_attribute, pg_proc, pg_am,
// pg_amop, pg_amproc, pg_opclass, pg_index).
//
// Why: M0106-0010 step 3w. After Step 3v cleared the relcache-init
// assertion PANIC loop, PG standby's backends reach `InitPostgres` and try
// to open `pg_aggregate` (OID 2600) via the standard catcache path. The
// local relfilenode mapper already advertises 2600 → 2600 (and ~30 other
// local catalog OIDs), but no file exists on disk under `base/{1,5}/2600`,
// so `mdopen → BasicOpenFile` FATALs with
// `could not open relation with OID 2600`. The same blocker would surface
// in turn for every other mapped-but-unseeded local catalog, so this
// function seeds them all in one pass instead of fixing one OID per loop.
//
// An empty heap page is sufficient: catcache lookups against an empty
// relation return nothing, which PG's early-startup probes interpret as
// "the catalog has no rows yet" rather than crashing. Rows for catalogs
// PG actually reads during boot (pg_class, pg_attribute, pg_am, …) are
// still produced by the dedicated bootstrappers; this function only fills
// the leftover OIDs so `BasicOpenFile` never returns ENOENT.
func bootstrapMappedLocalCatalogHeaps(dataDir string) error {
	heapPage := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(heapPage); err != nil {
		return err
	}
	// Local catalogs whose heap file is NOT written by a dedicated
	// bootstrapper. Keep in sync with `localRelMap` in
	// `bootstrapPostgresDatabase`; pg_authid (6239) is omitted because it
	// is fundamentally shared and already has a populated `global/1260`
	// heap via `bootstrapPostgresRole`.
	oids := []uint32{
		// 1247 pg_type is bootstrapped by bootstrapSystemCatalogs in
		// goopg's internal row format — do NOT overwrite it.
		826,  // pg_default_acl (M0106-0010 step 3ak)
		2600, // pg_aggregate
		2604, // pg_attrdef
		2605, // pg_cast
		2606, // pg_constraint
		2607, // pg_conversion
		2608, // pg_depend
		2609, // pg_description
		2611, // pg_inherits
		2612, // pg_language
		2613, // pg_largeobject
		2614, // pg_largeobject_metadata
		2615, // pg_namespace
		2617, // pg_operator
		2618, // pg_rewrite
		2619, // pg_statistic
		2620, // pg_trigger
		3381, // pg_statistic_ext
		3501, // pg_enum (M0106-0010 step 3an)
		3596, // pg_seclabel
		3764, // pg_ts_config
		3765, // pg_ts_config_map
		3766, // pg_ts_dict
		3767, // pg_ts_parser
		3768, // pg_ts_template
		3466, // pg_event_trigger (M0106-0010 step 3ar)
		3079, // pg_extension (M0106-0010 step 3aw)
		2328, // pg_foreign_data_wrapper (M0106-0010 step 3bb)
		6003, // pg_publication
		6101, // pg_publication_rel
		6102, // pg_sequence
		6137, // pg_transform
		6245, // pg_statistic_ext_data
		9400, // pg_db_role_setting
	}
	for _, dbOid := range []uint32{1, 5} {
		dbDir := filepath.Join(dataDir, "base", strconv.FormatUint(uint64(dbOid), 10))
		if err := os.MkdirAll(dbDir, 0o700); err != nil {
			return err
		}
		for _, oid := range oids {
			path := filepath.Join(dbDir, strconv.FormatUint(uint64(oid), 10))
			if err := os.WriteFile(path, heapPage, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// makeBtreeRootPage creates an empty btree root page that PG can
// open without crashing. B-tree pages use pd_special for the
// BTPageOpaqueData struct. An empty root/leaf page has btpo_flags =
// BTP_LEAF | BTP_ROOT.
func makeBtreeRootPage() []byte {
	// BTPageOpaqueData layout (16 bytes, end of page):
	//   btpo_prev(4) + btpo_next(4) + btpo_level(4) + btpo_flags(2) +
	//   btpo_cycleid(2).
	// BTMetaPageData layout (sizeof == 48 with trailing pad to 8-byte
	// alignment) mirroring postgres/src/include/access/nbtree.h:
	//   btm_magic        uint32   @ 0
	//   btm_version      uint32   @ 4
	//   btm_root         uint32   @ 8
	//   btm_level        uint32   @ 12
	//   btm_fastroot     uint32   @ 16
	//   btm_fastlevel    uint32   @ 20
	//   btm_last_cleanup_num_delpages uint32 @ 24
	//   (4 bytes padding for float8 alignment)
	//   btm_last_cleanup_num_heap_tuples float8 @ 32  (= -1.0)
	//   btm_allequalimage bool @ 40
	//   (7 bytes trailing pad to sizeof multiple of 8)
	// The on-disk image must satisfy PG's `_bt_getmeta` sanity check:
	// P_ISMETA(opaque) && metad->btm_magic == BTREE_MAGIC. Block 0 of
	// every nailed index file is consumed by this metapage; btm_root =
	// P_NONE (0) declares the index empty so PG's `_bt_getroot` returns
	// no rows on read-only paths and would lazily allocate a real root
	// on the first write. For bootstrap snapshots that matches behavior
	// of an index that has never had a tuple inserted.
	const (
		btreeOpaqueSize      = 16
		sizeofBTMetaPageData = 48
		btpMeta              = 1 << 3   // BTP_META
		btreeMagic           = 0x053162 // BTREE_MAGIC
		btreeVersion         = 4        // BTREE_VERSION
	)

	page := make([]byte, storage.BlockSize)
	h := storage.MustHeader(storage.Page(page))
	// pd_lower points just past the BTMetaPageData payload, matching
	// upstream `_bt_initmetapage` so xlog page-image compression keeps
	// the metadata bytes (see postgres/src/backend/access/nbtree/nbtpage.c:94).
	h.SetLower(uint16(storage.SizeOfPageHeaderData + sizeofBTMetaPageData))
	h.SetUpper(uint16(storage.BlockSize - btreeOpaqueSize))
	h.SetSpecial(uint16(storage.BlockSize - btreeOpaqueSize))
	h.SetPagesizeVersion(storage.BlockSize | 4) // pgPageLayoutVersion = 4

	le := binary.LittleEndian
	base := storage.SizeOfPageHeaderData
	le.PutUint32(page[base+0:base+4], btreeMagic)
	le.PutUint32(page[base+4:base+8], btreeVersion)
	// btm_root, btm_level, btm_fastroot, btm_fastlevel,
	// btm_last_cleanup_num_delpages already zero.
	// btm_last_cleanup_num_heap_tuples = -1.0 (canonical sentinel).
	le.PutUint64(page[base+32:base+40], math.Float64bits(-1.0))
	// btm_allequalimage = false (zero); trailing pad already zero.

	// BTPageOpaqueData at end of page; only btpo_flags is nonzero.
	off := storage.BlockSize - btreeOpaqueSize
	le.PutUint16(page[off+12:off+14], btpMeta)
	return page
}

// bootstrapPostgresRole writes a minimal pg_authid tuple for the
// "postgres" superuser so PG standby accepts connections. The tuple
// uses PG's native heap-tuple encoding (not goopg's internal format).
func bootstrapPostgresRole(dataDir string) error {
	// pg_authid columns (postgres/src/include/catalog/pg_authid.h)
	cols := []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "rolname", Type: catalog.Type{Name: "name"}, Ordinal: 1},
		{Name: "rolsuper", Type: catalog.Type{Name: "bool"}, Ordinal: 2},
		{Name: "rolinherit", Type: catalog.Type{Name: "bool"}, Ordinal: 3},
		{Name: "rolcreaterole", Type: catalog.Type{Name: "bool"}, Ordinal: 4},
		{Name: "rolcreatedb", Type: catalog.Type{Name: "bool"}, Ordinal: 5},
		{Name: "rolcanlogin", Type: catalog.Type{Name: "bool"}, Ordinal: 6},
		{Name: "rolreplication", Type: catalog.Type{Name: "bool"}, Ordinal: 7},
		{Name: "rolbypassrls", Type: catalog.Type{Name: "bool"}, Ordinal: 8},
		{Name: "rolconnlimit", Type: catalog.Type{Name: "int4"}, Ordinal: 9},
		{Name: "rolpassword", Type: catalog.Type{Name: "text"}, Ordinal: 10},
		{Name: "rolvaliduntil", Type: catalog.Type{Name: "timestamptz"}, Ordinal: 11},
	}
	buildRow := func(rolname string) executor.Row {
		return executor.Row{
			executor.NewIntDatum(10),          // oid (bootstrap superuser)
			executor.NewStringDatum(rolname),  // rolname
			executor.NewBoolDatum(true),       // rolsuper
			executor.NewBoolDatum(true),       // rolinherit
			executor.NewBoolDatum(true),       // rolcreaterole
			executor.NewBoolDatum(false),      // rolcreatedb
			executor.NewBoolDatum(true),       // rolcanlogin
			executor.NewBoolDatum(true),       // rolreplication
			executor.NewBoolDatum(true),       // rolbypassrls
			executor.NewIntDatum(-1),          // rolconnlimit
			executor.NewStringDatum(""),       // rolpassword (empty, not null)
			executor.NewTimeDatum(time.Unix(0, 0).UTC()), // rolvaliduntil (epoch, not null)
		}
	}

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return err
	}
	// Add "postgres" role.
	row := buildRow("postgres")
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		return fmt.Errorf("encode postgres role: %w", err)
	}
	tuple := storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
	tuple.Header.SetNatts(len(cols))
	if _, err := storage.PageAddHeapTuple(page, tuple); err != nil {
		return err
	}
	// Add OS user role.
	osUser := os.Getenv("USER")
	if osUser != "" && osUser != "postgres" {
		row2 := buildRow(osUser)
		payload2, err := executor.EncodeRowPG(cols, row2)
		if err != nil {
			return fmt.Errorf("encode %s role: %w", osUser, err)
		}
		tuple2 := storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload2)
		tuple2.Header.SetNatts(len(cols))
		if _, err := storage.PageAddHeapTuple(page, tuple2); err != nil {
			return err
		}
	}
	path := filepath.Join(dataDir, "global", "1260") // pg_authid OID
	return os.WriteFile(path, page, 0o600)
}

// bootstrapPostgresDatabase writes a minimal pg_database tuple for the
// template1 database so PG can look up database names during connection.
func bootstrapPostgresDatabase(dataDir string) error {
	// pg_database columns (postgres/src/include/catalog/pg_database.h):
	// oid(4), datname(64), datdba(4), encoding(4), datlocprovider(1),
	// datistemplate(1), datallowconn(1), datconnlimit(4), datfrozenxid(4),
	// datminmxid(4), dattablespace(4), datcollate(64), datctype(64),
	// daticulocale(text), datcollversion(text), datacl(aclitem[])
	cols := []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "datname", Type: catalog.Type{Name: "name"}, Ordinal: 1},
		{Name: "datdba", Type: catalog.Type{Name: "oid"}, Ordinal: 2},
		{Name: "encoding", Type: catalog.Type{Name: "int4"}, Ordinal: 3},
		{Name: "datlocprovider", Type: catalog.Type{Name: "char"}, Ordinal: 4},
		{Name: "datistemplate", Type: catalog.Type{Name: "bool"}, Ordinal: 5},
		{Name: "datallowconn", Type: catalog.Type{Name: "bool"}, Ordinal: 6},
		{Name: "datconnlimit", Type: catalog.Type{Name: "int4"}, Ordinal: 7},
		{Name: "datfrozenxid", Type: catalog.Type{Name: "xid"}, Ordinal: 8},
		{Name: "datminmxid", Type: catalog.Type{Name: "xid"}, Ordinal: 9},
		{Name: "dattablespace", Type: catalog.Type{Name: "oid"}, Ordinal: 10},
		{Name: "datcollate", Type: catalog.Type{Name: "name"}, Ordinal: 11},
		{Name: "datctype", Type: catalog.Type{Name: "name"}, Ordinal: 12},
		{Name: "daticulocale", Type: catalog.Type{Name: "text"}, Ordinal: 13},
		{Name: "datcollversion", Type: catalog.Type{Name: "text"}, Ordinal: 14},
		{Name: "datacl", Type: catalog.Type{Name: "text"}, Ordinal: 15},
	}
	row := executor.Row{
		executor.NewIntDatum(1),             // oid = template1
		executor.NewStringDatum("template1"), // datname
		executor.NewIntDatum(10),             // datdba = bootstrap superuser
		executor.NewIntDatum(6),              // encoding = PG_UTF8
		executor.NewStringDatum("c"),         // datlocprovider = libc
		executor.NewBoolDatum(false),         // datistemplate
		executor.NewBoolDatum(true),          // datallowconn
		executor.NewIntDatum(-1),             // datconnlimit
		executor.NewIntDatum(3),              // datfrozenxid
		executor.NewIntDatum(1),              // datminmxid
		executor.NewIntDatum(1663),           // dattablespace = pg_default
		executor.NewStringDatum("C"),          // datcollate
		executor.NewStringDatum("C"),          // datctype
		executor.NewStringDatum(""),           // daticulocale (empty, not null)
		executor.NewStringDatum(""),           // datcollversion (empty, not null)
		executor.NewStringDatum(""),           // datacl (empty, not null)
	}
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		return err
	}
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return err
	}
	tuple := storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
	tuple.Header.SetNatts(len(cols))
	if _, err := storage.PageAddHeapTuple(page, tuple); err != nil {
		return err
	}
	// Also add "postgres" database (default connection target).
	row2 := make(executor.Row, len(row))
	copy(row2, row)
	row2[0] = executor.NewIntDatum(5)             // oid = 5
	row2[1] = executor.NewStringDatum("postgres") // datname
	payload2, err := executor.EncodeRowPG(cols, row2)
	if err != nil {
		return err
	}
	tuple2 := storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload2)
	tuple2.Header.SetNatts(len(cols))
	if _, err := storage.PageAddHeapTuple(page, tuple2); err != nil {
		return err
	}
	// Also create index placeholders in base/1/ (goopg's default
	// database). PG's load_critical_index may hardcode dbNode=1
	// for nailed relations.
	base1Dir := filepath.Join(dataDir, "base", "1")
	btreePage := makeBtreeRootPage()
	for _, oid := range []uint32{
		827, // pg_default_acl_role_nsp_obj_index (Step 3al)
		828, // pg_default_acl_oid_index (Step 3am)
		2650, // pg_aggregate_fnoid_index (Step 3x)
		2653, // pg_amop_fam_strat_index (Step 3y)
		2654, 2655, 2658, 2659,
		2660, // pg_cast_oid_index (Step 3ab)
		2661, // pg_cast_source_target_index (Step 3ac)
		2662, 2663, 2667,
		2668, // pg_conversion_default_index (Step 3ah)
		2669, // pg_conversion_name_nsp_index (Step 3aj)
		2670, // pg_conversion_oid_index (Step 3ai)
		2678, 2679, 2680, 2682,
		2684, 2685,
		2686, // pg_opclass_am_name_nsp_index (Step 3ad)
		2687, 2688, 2690, 2691, 2692, 2693, 2701, 2703,
		2704, 3085, 3164,
		3502, // pg_enum_oid_index (Step 3ao)
		3503, // pg_enum_typid_label_index (Step 3ap)
		3534, // pg_enum_typid_sortorder_index (Step 3aq)
		3467, // pg_event_trigger_evtname_index (Step 3as)
		3468, // pg_event_trigger_oid_index (Step 3at)
		3080, // pg_extension_oid_index (Step 3ax)
		3081, // pg_extension_name_index (Step 3ay)
		548,  // pg_foreign_data_wrapper_name_index (Step 3bc)
		112,  // pg_foreign_data_wrapper_oid_index (Step 3bd)
	} {
		if err := os.WriteFile(filepath.Join(base1Dir, strconv.FormatUint(uint64(oid), 10)), btreePage, 0o600); err != nil {
			return err
		}
	}

	// Create the database directory with PG_VERSION and copies of
	// the default database catalog files (from base/1/).
	dbDir := filepath.Join(dataDir, "base", "5")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dbDir, "PG_VERSION"), []byte("18\n"), 0o600); err != nil {
		return err
	}
	entries, _ := os.ReadDir(base1Dir)
	for _, e := range entries {
		src := filepath.Join(base1Dir, e.Name())
		dst := filepath.Join(dbDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return err
		}
	}
	// Local pg_filenode.map with critical local catalog entries.
	// All standard PG local catalogs (OID == filenumber by default).
	localRelMap := makeRelMapFile([][2]uint32{
		{826, 826},   // pg_default_acl (M0106-0010 step 3ak)
		{1247, 1247}, // pg_type
		{1249, 1249}, // pg_attribute
		{1255, 1255}, // pg_proc
		{1259, 1259}, // pg_class
		{2600, 2600}, // pg_aggregate
		{2601, 2601}, // pg_am
		{2602, 2602}, // pg_amop
		{2603, 2603}, // pg_amproc
		{2604, 2604}, // pg_attrdef
		{2605, 2605}, // pg_cast
		{2606, 2606}, // pg_constraint
		{2607, 2607}, // pg_conversion
		{2608, 2608}, // pg_depend
		{2609, 2609}, // pg_description
		{2610, 2610}, // pg_index
		{2611, 2611}, // pg_inherits
		{2612, 2612}, // pg_language
		{2613, 2613}, // pg_largeobject
		{2614, 2614}, // pg_largeobject_metadata
		{2615, 2615}, // pg_namespace
		{2616, 2616}, // pg_opclass
		{2617, 2617}, // pg_operator
		{2618, 2618}, // pg_rewrite
		{2619, 2619}, // pg_statistic
		{2620, 2620}, // pg_trigger
		{3381, 3381}, // pg_statistic_ext
		{3501, 3501}, // pg_enum (M0106-0010 step 3an)
		{3596, 3596}, // pg_seclabel
		{3764, 3764}, // pg_ts_config
		{3765, 3765}, // pg_ts_config_map
		{3766, 3766}, // pg_ts_dict
		{3767, 3767}, // pg_ts_parser
		{3768, 3768}, // pg_ts_template
		{3466, 3466}, // pg_event_trigger (M0106-0010 step 3ar)
		{3079, 3079}, // pg_extension (M0106-0010 step 3aw)
		{2328, 2328}, // pg_foreign_data_wrapper (M0106-0010 step 3bb)
		{6003, 6003}, // pg_publication
		{6101, 6101}, // pg_publication_rel
		{6102, 6102}, // pg_sequence
		{6137, 6137}, // pg_transform
		{6239, 6239}, // pg_authid (shared but also local copy sometimes)
		{6245, 6245}, // pg_statistic_ext_data
		{9400, 9400}, // pg_db_role_setting (shared but may need local mapping)
	})
	if err := os.WriteFile(filepath.Join(dbDir, "pg_filenode.map"), localRelMap, 0o600); err != nil {
		return err
	}
	// Critical index placeholder pages — PG backends PANIC if these
	// files don't exist (load_critical_index in relcache.c).
	btreePage = makeBtreeRootPage()
	for _, oid := range []uint32{
		// Local critical indexes
		827, // pg_default_acl_role_nsp_obj_index (Step 3al)
		828, // pg_default_acl_oid_index (Step 3am)
		2650, // pg_aggregate_fnoid_index (Step 3x)
		2653, // pg_amop_fam_strat_index (Step 3y)
		2654, 2655, 2658, 2659,
		2660, // pg_cast_oid_index (Step 3ab)
		2661, // pg_cast_source_target_index (Step 3ac)
		2662, 2663, 2667,
		2668, // pg_conversion_default_index (Step 3ah)
		2669, // pg_conversion_name_nsp_index (Step 3aj)
		2670, // pg_conversion_oid_index (Step 3ai)
		2678, 2679, 2680, 2682,
		2684, 2685,
		2686, // pg_opclass_am_name_nsp_index (Step 3ad)
		2687, 2688, 2690, 2691, 2692, 2693, 2701, 2703,
		2704, 3085, 3164,
		3502, // pg_enum_oid_index (Step 3ao)
		3503, // pg_enum_typid_label_index (Step 3ap)
		3534, // pg_enum_typid_sortorder_index (Step 3aq)
		3467, // pg_event_trigger_evtname_index (Step 3as)
		3468, // pg_event_trigger_oid_index (Step 3at)
		3080, // pg_extension_oid_index (Step 3ax)
		3081, // pg_extension_name_index (Step 3ay)
		548,  // pg_foreign_data_wrapper_name_index (Step 3bc)
		112,  // pg_foreign_data_wrapper_oid_index (Step 3bd)
	} {
		if err := os.WriteFile(filepath.Join(dbDir, strconv.FormatUint(uint64(oid), 10)), btreePage, 0o600); err != nil {
			return err
		}
	}
	// Shared critical indexes (under global/).
	// Also write local indexes to global/ as fallback — PG's
	// formrdesc may use InvalidOid for dbNode on nailed
	// relations, causing lookups in global/ instead of base/<dboid>/.
	for _, oid := range []uint32{
		2671, 2672, 2676, 2677,
		2694, // pg_auth_members_role_member_index (Step 3z)
		2695, 3593,
		// Also copy all local critical indexes to global/
		827, // pg_default_acl_role_nsp_obj_index (Step 3al)
		828, // pg_default_acl_oid_index (Step 3am)
		2650, // pg_aggregate_fnoid_index (Step 3x)
		2653, // pg_amop_fam_strat_index (Step 3y)
		2654, 2655, 2658, 2659,
		2660, // pg_cast_oid_index (Step 3ab)
		2661, // pg_cast_source_target_index (Step 3ac)
		2662, 2663, 2667,
		2668, // pg_conversion_default_index (Step 3ah)
		2669, // pg_conversion_name_nsp_index (Step 3aj)
		2670, // pg_conversion_oid_index (Step 3ai)
		2678, 2679, 2680, 2682,
		2684, 2685,
		2686, // pg_opclass_am_name_nsp_index (Step 3ad)
		2687, 2688, 2690, 2691, 2692, 2693, 2701, 2703,
		2704, 3085, 3164,
		3502, // pg_enum_oid_index (Step 3ao)
		3503, // pg_enum_typid_label_index (Step 3ap)
		3534, // pg_enum_typid_sortorder_index (Step 3aq)
		3467, // pg_event_trigger_evtname_index (Step 3as)
		3468, // pg_event_trigger_oid_index (Step 3at)
		3080, // pg_extension_oid_index (Step 3ax)
		3081, // pg_extension_name_index (Step 3ay)
		548,  // pg_foreign_data_wrapper_name_index (Step 3bc)
		112,  // pg_foreign_data_wrapper_oid_index (Step 3bd)
	} {
		if err := os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(uint64(oid), 10)), btreePage, 0o600); err != nil {
			return err
		}
	}
	path := filepath.Join(dataDir, "global", "1262") // pg_database OID
	return os.WriteFile(path, page, 0o600)
}

// bootstrapPgClassTuples writes PG-native pg_class heap tuples for every
// nailed relation. Vanilla PG's load_critical_index → RelationBuildDesc →
// ScanPgRelation reads actual pg_class tuples, ignoring the init file.
//
// Returns a map[oid]heapTID so the Step 3m caller can build the matching
// pg_class_oid_index btree over the same heap-row locations.
func bootstrapPgClassTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgClassColDefs()
	allRels := append([]nailedRel{}, nailedSharedRels...)
	allRels = append(allRels, nailedLocalRels...)
	tids, err := writeMultiPageHeap(dataDir, "1259", cols, allRels, func(rel nailedRel) executor.Row {
		return pgClassRow(rel)
	})
	if err != nil {
		return nil, err
	}
	m := make(map[uint32]heapTID, len(allRels))
	for i, rel := range allRels {
		m[rel.OID] = tids[i]
	}
	return m, nil
}

// pgAttrTIDKey identifies a pg_attribute row uniquely by (attrelid, attnum).
// Used by Step 3o's bootstrap of pg_attribute_relid_attnum_index, which
// needs the on-disk heap TID for each (relation, attnum) pair so the
// index leaf's IndexTuple.t_tid points at the correct heap row.
type pgAttrTIDKey struct {
	AttRelID uint32
	AttNum   int16
}

// bootstrapPgAttributeTuples writes PG-native pg_attribute heap tuples for
// every column of every nailed relation so RelationBuildDesc can load column
// metadata from disk. Returns a map from (attrelid, attnum) → heapTID so
// callers can build composite-key btree index tuples that point at each row.
func bootstrapPgAttributeTuples(dataDir string) (map[pgAttrTIDKey]heapTID, error) {
	attrCols := pgAttrColDefs()
	allRels := append([]nailedRel{}, nailedSharedRels...)
	allRels = append(allRels, nailedLocalRels...)
	type rowKey struct {
		AttRelID uint32
		AttNum   int16
	}
	var rows []executor.Row
	keys := make([]rowKey, 0)
	for _, rel := range allRels {
		attrs := pgAttrEntriesForRel(rel)
		for _, a := range attrs {
			rows = append(rows, pgAttributeRow(rel.OID, a))
			keys = append(keys, rowKey{AttRelID: rel.OID, AttNum: a.Num})
		}
	}
	tids, err := writeMultiPageHeapRows(dataDir, "1249", attrCols, rows)
	if err != nil {
		return nil, err
	}
	if len(tids) != len(keys) {
		return nil, fmt.Errorf("pg_attribute: tid/row count mismatch (%d vs %d)", len(tids), len(keys))
	}
	out := make(map[pgAttrTIDKey]heapTID, len(tids))
	for i, k := range keys {
		out[pgAttrTIDKey{AttRelID: k.AttRelID, AttNum: k.AttNum}] = tids[i]
	}
	return out, nil
}

// pgAmColDefs returns pg_am column descriptors matching PG18's
// FormData_pg_am struct byte-for-byte. RelationInitIndexAccessInfo
// calls SearchSysCache1(AMOID, relam) → systable_getnext, which
// returns a HeapTuple whose data is cast as Form_pg_am. The four
// columns are: oid (OID, 4) + amname (NameData, 64) + amhandler
// (regproc=OID, 4) + amtype (char, 1).
func pgAmColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "amname", Type: catalog.Type{Name: "name"}},
		{Name: "amhandler", Type: catalog.Type{Name: "oid"}},
		{Name: "amtype", Type: catalog.Type{Name: "char"}},
	}
}

// pgAmEntry is one row of pg_am.
type pgAmEntry struct {
	OID       uint32
	Name      string
	HandlerID uint32
	AmType    byte // 'i'=index, 't'=table
}

// pgAmInitialEntries mirrors the seven seed rows in PG18's
// src/include/catalog/pg_am.dat. The handler OIDs come from
// pg_proc.dat (heap_tableam_handler=3, bthandler=330, etc.).
func pgAmInitialEntries() []pgAmEntry {
	return []pgAmEntry{
		{2, "heap", 3, 't'},
		{403, "btree", 330, 'i'},
		{405, "hash", 331, 'i'},
		{783, "gist", 332, 'i'},
		{2742, "gin", 333, 'i'},
		{4000, "spgist", 334, 'i'},
		{3580, "brin", 335, 'i'},
	}
}

// pgAmRow builds one pg_am tuple in pgAmColDefs order.
func pgAmRow(e pgAmEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),
		executor.NewStringDatum(e.Name),
		executor.NewIntDatum(int64(e.HandlerID)),
		executor.NewStringDatum(string(rune(e.AmType))),
	}
}

// bootstrapPgAmTuples writes PG-native pg_am heap tuples (one per
// access method) so vanilla PG's SearchSysCache1(AMOID, relam) inside
// RelationInitIndexAccessInfo finds the AM row instead of returning
// InvalidOid and PANICing on a critical index open.
func bootstrapPgAmTuples(dataDir string) error {
	cols := pgAmColDefs()
	entries := pgAmInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgAmRow(e))
	}
	_, err := writeMultiPageHeapRows(dataDir, "2601", cols, rows)
	return err
}


// oidVectorBytes returns the on-disk PG-native serialization of an
// `oidvector` value (1-D Oid array with lbound=0, elemtype=OID(26)).
// Used to seed pg_proc.proargtypes for the AM handler functions —
// PG's RelationInitIndexAccessInfo path looks the handler up via
// fmgr → SearchSysCache1(PROCOID), and the cached tuple's proargtypes
// is dereferenced as ArrayType* through DatumGetPointer. A plain
// varlena text "{2281}" would not satisfy the ARR_ELEMTYPE assertion.
//
// Wire layout (LE):
//
//	[0:4]   varlena 4-byte header: (24 + 4*N) << 2
//	[4:8]   ndim       = 1
//	[8:12]  dataoffset = 0
//	[12:16] elemtype   = 26 (OID)
//	[16:20] dim1       = N
//	[20:24] lbound1    = 0
//	[24:]   N * 4-byte little-endian OIDs
func oidVectorBytes(oids []uint32) []byte {
	const headerSize = 24
	total := headerSize + 4*len(oids)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 26)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(oids)))
	binary.LittleEndian.PutUint32(buf[20:24], 0)
	for i, o := range oids {
		binary.LittleEndian.PutUint32(buf[24+i*4:28+i*4], o)
	}
	return buf
}

// int2VectorBytes builds the on-disk int2vector blob for pg_index.indkey
// and pg_index.indoption. Layout mirrors oidVectorBytes but with
// elemtype=INT2(21) and 2-byte payload elements. Matches upstream
// `buildint2vector` (src/backend/utils/adt/int.c) byte-for-byte: a 24-byte
// 1-D no-null ArrayType header followed by n*int16 values, with vl_len_
// encoded as (total << 2) per SET_VARSIZE_4B.
func int2VectorBytes(values []int16) []byte {
	const headerSize = 24
	total := headerSize + 2*len(values)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 21)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(values)))
	binary.LittleEndian.PutUint32(buf[20:24], 0)
	for i, v := range values {
		binary.LittleEndian.PutUint16(buf[24+i*2:26+i*2], uint16(v))
	}
	return buf
}

// pgProcEntry is a minimal description of a pg_proc row produced
// during initdb. v0 only needs to seed the AM handler functions so
// PG's RelationInitIndexAccessInfo can resolve amhandler via fmgr.
type pgProcEntry struct {
	OID         uint32
	Name        string // proname (NameData, ≤63 bytes)
	RetType     uint32 // prorettype OID
	HandlerName string // prosrc text (e.g. "bthandler") — fmgr internal lookup key
}

// pgProcColDefs returns the 30-column PG18 FormData_pg_proc layout.
// Column order must match `postgres/src/include/catalog/pg_proc.h`
// so PG can dereference GETSTRUCT(tup)→Form_pg_proc directly.
func pgProcColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},                   // 1
		{Name: "proname", Type: catalog.Type{Name: "name"}},              // 2
		{Name: "pronamespace", Type: catalog.Type{Name: "oid"}},          // 3
		{Name: "proowner", Type: catalog.Type{Name: "oid"}},              // 4
		{Name: "prolang", Type: catalog.Type{Name: "oid"}},               // 5
		{Name: "procost", Type: catalog.Type{Name: "float4"}},            // 6
		{Name: "prorows", Type: catalog.Type{Name: "float4"}},            // 7
		{Name: "provariadic", Type: catalog.Type{Name: "oid"}},           // 8
		{Name: "prosupport", Type: catalog.Type{Name: "regproc"}},        // 9
		{Name: "prokind", Type: catalog.Type{Name: "char"}},              // 10
		{Name: "prosecdef", Type: catalog.Type{Name: "bool"}},            // 11
		{Name: "proleakproof", Type: catalog.Type{Name: "bool"}},         // 12
		{Name: "proisstrict", Type: catalog.Type{Name: "bool"}},          // 13
		{Name: "proretset", Type: catalog.Type{Name: "bool"}},            // 14
		{Name: "provolatile", Type: catalog.Type{Name: "char"}},          // 15
		{Name: "proparallel", Type: catalog.Type{Name: "char"}},          // 16
		{Name: "pronargs", Type: catalog.Type{Name: "int2"}},             // 17
		{Name: "pronargdefaults", Type: catalog.Type{Name: "int2"}},      // 18
		{Name: "prorettype", Type: catalog.Type{Name: "oid"}},            // 19
		{Name: "proargtypes", Type: catalog.Type{Name: "oidvector"}},     // 20
		// CATALOG_VARLEN section: nullable in PG but we emit empty
		// binary arrays so the relacl-style "raw bytes as ArrayType*"
		// dereferences in PG do not trip ARR_ELEMTYPE assertions.
		{Name: "proallargtypes", Type: catalog.Type{Name: "oid[]"}},      // 21
		{Name: "proargmodes", Type: catalog.Type{Name: "char[]"}},        // 22
		{Name: "proargnames", Type: catalog.Type{Name: "text[]"}},        // 23
		{Name: "proargdefaults", Type: catalog.Type{Name: "pg_node_tree"}}, // 24
		{Name: "protrftypes", Type: catalog.Type{Name: "oid[]"}},         // 25
		{Name: "prosrc", Type: catalog.Type{Name: "text"}},               // 26 — FORCE_NOT_NULL
		{Name: "probin", Type: catalog.Type{Name: "text"}},               // 27
		{Name: "prosqlbody", Type: catalog.Type{Name: "pg_node_tree"}},   // 28
		{Name: "proconfig", Type: catalog.Type{Name: "text[]"}},          // 29
		{Name: "proacl", Type: catalog.Type{Name: "aclitem[]"}},          // 30
	}
}

// pgProcInitialEntries lists the seven AM handler pg_proc rows that
// vanilla PG18 ships in `pg_proc.dat` and that goopg must mirror so
// `OidFunctionCall0(amhandler)` succeeds during standby startup.
//
// All seven are PROVOLATILE 'v', PROPARALLEL 's', INTERNALlanguageId
// (12), pronamespace = 11 (pg_catalog), proowner = 10 (bootstrap
// superuser), one `internal` argument (OID 2281).
func pgProcInitialEntries() []pgProcEntry {
	return []pgProcEntry{
		// Table AM handler
		{3, "heap_tableam_handler", 269, "heap_tableam_handler"},
		// Index AM handlers
		{330, "bthandler", 325, "bthandler"},
		{331, "hashhandler", 325, "hashhandler"},
		{332, "gisthandler", 325, "gisthandler"},
		{333, "ginhandler", 325, "ginhandler"},
		{334, "spghandler", 325, "spghandler"},
		{335, "brinhandler", 325, "brinhandler"},
	}
}

// pgProcRow materialises one pgProcEntry as the 30-column row that
// EncodeRowPG will pack into the on-disk heap tuple.
func pgProcRow(e pgProcEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),               // 1  oid
		executor.NewStringDatum(e.Name),                  // 2  proname
		executor.NewIntDatum(11),                         // 3  pronamespace = pg_catalog
		executor.NewIntDatum(10),                         // 4  proowner = BOOTSTRAP_SUPERUSERID
		executor.NewIntDatum(12),                         // 5  prolang = INTERNALlanguageId
		executor.NewIntDatum(1),                          // 6  procost = 1 (float4)
		executor.NewIntDatum(0),                          // 7  prorows = 0 (float4)
		executor.NewIntDatum(0),                          // 8  provariadic = 0
		executor.NewIntDatum(0),                          // 9  prosupport = 0
		executor.NewStringDatum("f"),                     // 10 prokind = 'f' (function)
		executor.NewBoolDatum(false),                     // 11 prosecdef
		executor.NewBoolDatum(false),                     // 12 proleakproof
		executor.NewBoolDatum(true),                      // 13 proisstrict
		executor.NewBoolDatum(false),                     // 14 proretset
		executor.NewStringDatum("v"),                     // 15 provolatile = 'v' (volatile)
		executor.NewStringDatum("s"),                     // 16 proparallel = 's' (safe)
		executor.NewIntDatum(1),                          // 17 pronargs = 1 (single `internal` arg)
		executor.NewIntDatum(0),                          // 18 pronargdefaults
		executor.NewIntDatum(int64(e.RetType)),           // 19 prorettype
		executor.NewBytesDatum(oidVectorBytes([]uint32{2281})), // 20 proargtypes = (internal)
		executor.NewStringDatum(""),                      // 21 proallargtypes
		executor.NewStringDatum(""),                      // 22 proargmodes
		executor.NewStringDatum(""),                      // 23 proargnames
		executor.NewStringDatum(""),                      // 24 proargdefaults (pg_node_tree)
		executor.NewStringDatum(""),                      // 25 protrftypes
		executor.NewStringDatum(e.HandlerName),           // 26 prosrc — fmgr internal lookup key
		executor.NewStringDatum(""),                      // 27 probin
		executor.NewStringDatum(""),                      // 28 prosqlbody
		executor.NewStringDatum(""),                      // 29 proconfig
		executor.NewStringDatum(""),                      // 30 proacl
	}
}

// bootstrapPgProcTuples writes the 7 AM handler pg_proc heap tuples
// to base/1/1255 and base/5/1255. M0106-0010 step 3a: required so
// PG standby startup's RelationInitIndexAccessInfo →
// OidFunctionCall0(amhandler) succeeds — fmgr does
// SearchSysCache1(PROCOID, …) on the index AM's handler OID and
// dereferences GETSTRUCT(tup)→Form_pg_proc to read prosrc.
func bootstrapPgProcTuples(dataDir string) error {
	cols := pgProcColDefs()
	entries := pgProcInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgProcRow(e))
	}
	_, err := writeMultiPageHeapRows(dataDir, "1255", cols, rows)
	return err
}

// pgOpclassEntry mirrors one row of PG18's pg_opclass.dat — see
// `postgres/src/include/catalog/pg_opclass.dat` and the
// `FormData_pg_opclass` struct in `pg_opclass.h`.
//
// goopg only needs the btree opclasses the nailed system indexes
// reference via `pg_index.indclass`. Future work (step 3c) extends
// this to pg_amop / pg_amproc.
type pgOpclassEntry struct {
	OID       uint32 // opclass OID
	Method    uint32 // pg_am OID — 403 for btree
	Name      string // opcname (NameData, ≤63 bytes)
	Namespace uint32 // opcnamespace — 11 (pg_catalog)
	Owner     uint32 // opcowner — 10 (POSTGRES bootstrap superuser)
	Family    uint32 // opcfamily — pg_opfamily OID
	IntType   uint32 // opcintype — pg_type OID
	Default   bool   // opcdefault — t/f
	KeyType   uint32 // opckeytype — 0 unless conversion needed
}

// pgOpclassColDefs returns the PG18 9-column FormData_pg_opclass shape.
// The order and types must match `pg_opclass.h` exactly so PG's
// GETSTRUCT(tup) cast yields a valid Form_pg_opclass.
func pgOpclassColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "opcmethod", Type: catalog.Type{Name: "oid"}},
		{Name: "opcname", Type: catalog.Type{Name: "name"}},
		{Name: "opcnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "opcowner", Type: catalog.Type{Name: "oid"}},
		{Name: "opcfamily", Type: catalog.Type{Name: "oid"}},
		{Name: "opcintype", Type: catalog.Type{Name: "oid"}},
		{Name: "opcdefault", Type: catalog.Type{Name: "bool"}},
		{Name: "opckeytype", Type: catalog.Type{Name: "oid"}},
	}
}

// pgOpclassInitialEntries returns the btree opclasses required for
// the nailed system indexes goopg seeds in `nailedLocalRels` /
// `nailedSharedRels`. OIDs match PG18's pg_opclass_d.h where
// available; opfamily OIDs match pg_opfamily_d.h.
//
// Without these rows, PG's `RelationInitIndexAccessInfo` →
// `SearchSysCache1(CLAOID, opcid)` returns NULL for every
// `pg_index.indclass` entry and the standby PANICs the moment it
// opens a critical index past the bthandler stage.
func pgOpclassInitialEntries() []pgOpclassEntry {
	const (
		nsPGCatalog uint32 = 11
		ownerSuper  uint32 = 10
		amBtree     uint32 = 403
		famInteger  uint32 = 1976 // INTEGER_BTREE_FAM_OID
		famOID      uint32 = 1989 // OID_BTREE_FAM_OID
		famText     uint32 = 1994 // TEXT_BTREE_FAM_OID
		famTextPat  uint32 = 2095 // TEXT_PATTERN_BTREE_FAM_OID
		// Canonical opfamily OIDs sourced from pg_opfamily.dat for the
		// three pinned opclasses below. Step 3b mistakenly reused
		// neighbouring families (famText for char_ops, famOID for
		// oidvector_ops, BPCHAR_BTREE for bpchar_pattern_ops) —
		// corrected here so pg_amop lookups under the right family
		// resolve.
		famCharBtree          uint32 = 429  // btree/char_ops
		famOidvectorBtree     uint32 = 1991 // btree/oidvector_ops
		famBpcharPatternBtree uint32 = 2097 // BPCHAR_PATTERN_BTREE_FAM_OID
		famBool               uint32 = 424  // BOOL_BTREE_FAM_OID
	)
	// Synthetic OIDs for opclasses with no hardcoded OID in
	// pg_opclass_d.h. Chosen below FirstGenbkiObjectId (10000) so
	// they don't collide with user-assigned OIDs.
	const (
		nameBtreeOps      uint32 = 1986
		charBtreeOps      uint32 = 1985
		oidvectorBtreeOps uint32 = 1987
		boolBtreeOps      uint32 = 1984
	)
	return []pgOpclassEntry{
		// Hardcoded OIDs from pg_opclass_d.h.
		{1978, amBtree, "int4_ops", nsPGCatalog, ownerSuper, famInteger, 23, true, 0},
		{1979, amBtree, "int2_ops", nsPGCatalog, ownerSuper, famInteger, 21, true, 0},
		{1981, amBtree, "oid_ops", nsPGCatalog, ownerSuper, famOID, 26, true, 0},
		{3124, amBtree, "int8_ops", nsPGCatalog, ownerSuper, famInteger, 20, true, 0},
		{3126, amBtree, "text_ops", nsPGCatalog, ownerSuper, famText, 25, true, 0},
		{4217, amBtree, "text_pattern_ops", nsPGCatalog, ownerSuper, famTextPat, 25, false, 0},
		{4218, amBtree, "varchar_pattern_ops", nsPGCatalog, ownerSuper, famTextPat, 25, false, 0},
		{4219, amBtree, "bpchar_pattern_ops", nsPGCatalog, ownerSuper, famBpcharPatternBtree, 1042, false, 0},
		// Dynamically-assigned OIDs we pin so nailed-index
		// indclass references can point at them.
		// name_ops keys are stored as cstring (2275) for index
		// space — see pg_opclass.dat comment.
		{nameBtreeOps, amBtree, "name_ops", nsPGCatalog, ownerSuper, famText, 19, true, 2275},
		{charBtreeOps, amBtree, "char_ops", nsPGCatalog, ownerSuper, famCharBtree, 18, true, 0},
		{oidvectorBtreeOps, amBtree, "oidvector_ops", nsPGCatalog, ownerSuper, famOidvectorBtree, 30, true, 0},
		{boolBtreeOps, amBtree, "bool_ops", nsPGCatalog, ownerSuper, famBool, 16, true, 0},
	}
}

// pgOpclassRow encodes one pg_opclass row. Field order mirrors
// FormData_pg_opclass so PG's GETSTRUCT cast is byte-for-byte valid.
func pgOpclassRow(e pgOpclassEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),       // 1 oid
		executor.NewIntDatum(int64(e.Method)),    // 2 opcmethod
		executor.NewStringDatum(e.Name),          // 3 opcname (NameData)
		executor.NewIntDatum(int64(e.Namespace)), // 4 opcnamespace
		executor.NewIntDatum(int64(e.Owner)),     // 5 opcowner
		executor.NewIntDatum(int64(e.Family)),    // 6 opcfamily
		executor.NewIntDatum(int64(e.IntType)),   // 7 opcintype
		executor.NewBoolDatum(e.Default),         // 8 opcdefault
		executor.NewIntDatum(int64(e.KeyType)),   // 9 opckeytype
	}
}

// bootstrapPgOpclassTuples writes the pg_opclass heap to
// base/{1,5}/2616 so PG's CLAOID syscache hits resolve for every
// opclass referenced by a nailed index.
func bootstrapPgOpclassTuples(dataDir string) error {
	cols := pgOpclassColDefs()
	entries := pgOpclassInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgOpclassRow(e))
	}
	_, err := writeMultiPageHeapRows(dataDir, "2616", cols, rows)
	return err
}


// pgAmopEntry mirrors one row of PG18's pg_amop.dat — see
// `postgres/src/include/catalog/pg_amop.dat` and the
// `FormData_pg_amop` struct in `pg_amop.h`. goopg only seeds
// the default (lefttype = righttype = opcintype) strategy
// operators for the btree opclasses pinned in
// pgOpclassInitialEntries; cross-type entries are out of scope.
type pgAmopEntry struct {
	OID         uint32 // amop OID
	Family      uint32 // amopfamily — pg_opfamily OID
	LeftType    uint32 // amoplefttype — pg_type OID
	RightType   uint32 // amoprighttype — pg_type OID
	Strategy    int16  // amopstrategy — 1..5 for btree
	Purpose     byte   // amoppurpose — 's' (search) or 'o' (order)
	Operator    uint32 // amopopr — pg_operator OID
	Method      uint32 // amopmethod — pg_am OID (403 = btree)
	SortFamily  uint32 // amopsortfamily — 0 for search ops
}

// pgAmopColDefs returns the PG18 9-column FormData_pg_amop shape.
// Order and types must match `pg_amop.h` exactly so PG's GETSTRUCT
// cast yields a valid Form_pg_amop.
func pgAmopColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},            // 1
		{Name: "amopfamily", Type: catalog.Type{Name: "oid"}},     // 2
		{Name: "amoplefttype", Type: catalog.Type{Name: "oid"}},   // 3
		{Name: "amoprighttype", Type: catalog.Type{Name: "oid"}},  // 4
		{Name: "amopstrategy", Type: catalog.Type{Name: "int2"}},  // 5
		{Name: "amoppurpose", Type: catalog.Type{Name: "char"}},   // 6
		{Name: "amopopr", Type: catalog.Type{Name: "oid"}},        // 7
		{Name: "amopmethod", Type: catalog.Type{Name: "oid"}},     // 8
		{Name: "amopsortfamily", Type: catalog.Type{Name: "oid"}}, // 9
	}
}

// pgAmopInitialEntries returns the canonical btree strategy
// operator rows for each pinned default opclass. OIDs are taken
// from `postgres/src/include/catalog/pg_operator.dat` (canonical
// PG18 builtins). For each (family, lefttype=righttype) key we
// emit five rows — strategy 1..5 → <, <=, =, >=, >.
//
// The amop OIDs are synthetic (below FirstGenbkiObjectId 10000)
// — pg_amop.dat does not pin amop row OIDs upstream either; the
// OID column merely identifies the row in pg_amop_oid_index.
func pgAmopInitialEntries() []pgAmopEntry {
	const (
		amBtree               uint32 = 403
		famInteger            uint32 = 1976
		famOID                uint32 = 1989
		famText               uint32 = 1994
		famTextPattern        uint32 = 2095
		famBool               uint32 = 424
		famCharBtree          uint32 = 429
		famOidvectorBtree     uint32 = 1991
		famBpcharPatternBtree uint32 = 2097
		purposeSearch         byte   = 's'
	)
	// Synthetic OIDs for the amop rows themselves. PG normally
	// assigns these at initdb time; we pin contiguous ranges so
	// the pg_amop_oid_index can later be heap-rebuilt.
	const baseOID uint32 = 7000
	out := make([]pgAmopEntry, 0, 85)
	addPair := func(family, lefttype, righttype uint32, ops [5]uint32) {
		for i := 0; i < 5; i++ {
			out = append(out, pgAmopEntry{
				OID:        baseOID + uint32(len(out)),
				Family:     family,
				LeftType:   lefttype,
				RightType:  righttype,
				Strategy:   int16(i + 1),
				Purpose:    purposeSearch,
				Operator:   ops[i],
				Method:     amBtree,
				SortFamily: 0,
			})
		}
	}
	add := func(family, lefttype uint32, ops [5]uint32) {
		addPair(family, lefttype, lefttype, ops)
	}
	// int4 — pg_operator.dat 97 <, 523 <=, 96 =, 525 >=, 521 >.
	add(famInteger, 23, [5]uint32{97, 523, 96, 525, 521})
	// int2 — 95 <, 522 <=, 94 =, 524 >=, 520 >.
	add(famInteger, 21, [5]uint32{95, 522, 94, 524, 520})
	// int8 — 412 <, 414 <=, 410 =, 415 >=, 413 >.
	add(famInteger, 20, [5]uint32{412, 414, 410, 415, 413})
	// Cross-type integer_ops — pg_amop.dat int24/int28/int42/int48/int82/int84.
	// These are required for index scans that compare across integer widths
	// (e.g. an int4 indexed column compared to an int2 literal). Without the
	// rows, PG's `get_op_btree_interpretation` returns no strategy match and
	// the planner can't push down the qual.
	addPair(famInteger, 21, 23, [5]uint32{534, 540, 532, 542, 536}) // int24
	addPair(famInteger, 21, 20, [5]uint32{1864, 1866, 1862, 1867, 1865}) // int28
	addPair(famInteger, 23, 21, [5]uint32{535, 541, 533, 543, 537}) // int42
	addPair(famInteger, 23, 20, [5]uint32{37, 80, 15, 82, 76})      // int48
	addPair(famInteger, 20, 21, [5]uint32{1870, 1872, 1868, 1873, 1871}) // int82
	addPair(famInteger, 20, 23, [5]uint32{418, 420, 416, 430, 419}) // int84
	// oid  — 609 <, 611 <=, 607 =, 612 >=, 610 >.
	add(famOID, 26, [5]uint32{609, 611, 607, 612, 610})
	// text — 664 <, 665 <=, 98 =, 667 >=, 666 >.
	add(famText, 25, [5]uint32{664, 665, 98, 667, 666})
	// name — 660 <, 661 <=, 93 =, 663 >=, 662 >.
	add(famText, 19, [5]uint32{660, 661, 93, 663, 662})
	// text pattern — 2314 ~<~, 2315 ~<=~, 98 =, 2317 ~>=~, 2318 ~>~.
	add(famTextPattern, 25, [5]uint32{2314, 2315, 98, 2317, 2318})
	// bool — 58 <, 1694 <=, 91 =, 1695 >=, 59 >.
	add(famBool, 16, [5]uint32{58, 1694, 91, 1695, 59})
	// char — pg_operator.dat 631 <, 632 <=, 92 =, 634 >=, 633 >.
	add(famCharBtree, 18, [5]uint32{631, 632, 92, 634, 633})
	// oidvector — 645 <, 647 <=, 649 =, 648 >=, 646 >.
	add(famOidvectorBtree, 30, [5]uint32{645, 647, 649, 648, 646})
	// bpchar pattern — 2326 ~<~, 2327 ~<=~, 1054 =, 2329 ~>=~, 2330 ~>~.
	add(famBpcharPatternBtree, 1042, [5]uint32{2326, 2327, 1054, 2329, 2330})
	return out
}

// pgAmopRow encodes one pg_amop row. Field order mirrors
// FormData_pg_amop so PG's GETSTRUCT cast is byte-for-byte valid.
//
// PG-native alignment puts a 1-byte pad after amoppurpose (offset
// 19) before amopopr (4-byte aligned, offset 20). EncodeRowPG
// inserts the pad automatically based on each column's typalign.
func pgAmopRow(e pgAmopEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),         // 1 oid
		executor.NewIntDatum(int64(e.Family)),      // 2 amopfamily
		executor.NewIntDatum(int64(e.LeftType)),    // 3 amoplefttype
		executor.NewIntDatum(int64(e.RightType)),   // 4 amoprighttype
		executor.NewIntDatum(int64(e.Strategy)),    // 5 amopstrategy (int2)
		executor.NewStringDatum(string(e.Purpose)), // 6 amoppurpose (char)
		executor.NewIntDatum(int64(e.Operator)),    // 7 amopopr
		executor.NewIntDatum(int64(e.Method)),      // 8 amopmethod
		executor.NewIntDatum(int64(e.SortFamily)),  // 9 amopsortfamily
	}
}

// bootstrapPgAmopTuples writes the pg_amop heap to
// base/{1,5}/2602 so PG can resolve strategy operators for the
// pinned btree opclasses without scanning an empty page. PG only
// touches this catalog at query-planning time (operator → strategy
// lookups via the AMOPOPID / AMOPSTRATEGY syscaches), so the
// rows are not load-bearing for hot-standby boot but ARE required
// for any non-trivial SELECT against a system view.
func bootstrapPgAmopTuples(dataDir string) error {
	cols := pgAmopColDefs()
	entries := pgAmopInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgAmopRow(e))
	}
	_, err := writeMultiPageHeapRows(dataDir, "2602", cols, rows)
	return err
}

// pgAmprocEntry mirrors one row of PG18's pg_amproc.dat — see
// `postgres/src/include/catalog/pg_amproc.dat` and the
// `FormData_pg_amproc` struct in `pg_amproc.h`. goopg seeds the
// default cmp (amprocnum=1), sortsupport (amprocnum=2) and
// equalimage (amprocnum=4) support functions for the pinned
// btree opclasses; cross-type rows (lefttype != righttype),
// in_range (amprocnum=3) and skipsupport (amprocnum=6) remain
// out of scope.
type pgAmprocEntry struct {
	OID            uint32 // amproc OID
	Family         uint32 // amprocfamily — pg_opfamily OID
	LeftType       uint32 // amproclefttype — pg_type OID
	RightType      uint32 // amprocrighttype — pg_type OID
	Num            int16  // amprocnum — 1 for cmp
	Proc           uint32 // amproc — pg_proc OID (regproc)
}

// pgAmprocColDefs returns the PG18 6-column FormData_pg_amproc
// shape. Order and types must match `pg_amproc.h` exactly so
// PG's GETSTRUCT cast yields a valid Form_pg_amproc.
func pgAmprocColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},               // 1
		{Name: "amprocfamily", Type: catalog.Type{Name: "oid"}},      // 2
		{Name: "amproclefttype", Type: catalog.Type{Name: "oid"}},    // 3
		{Name: "amprocrighttype", Type: catalog.Type{Name: "oid"}},   // 4
		{Name: "amprocnum", Type: catalog.Type{Name: "int2"}},        // 5
		{Name: "amproc", Type: catalog.Type{Name: "regproc"}},        // 6
	}
}

// pgAmprocInitialEntries returns the canonical btree support
// function rows for each pinned default opclass. Proc OIDs are
// from `postgres/src/include/catalog/pg_proc.dat` and the
// (family,type,num) keys are sourced from `pg_amproc.dat`.
//
// PG's RelationInitIndexAccessInfo → LookupOpclassInfo scans
// pg_amproc for (opcfamily, opcintype, opcintype) and stores the
// support proc OIDs into the relcache opclass entry. Without
// the cmp rows the standby panics the first time an index
// dispatches to its comparison function; sortsupport/equalimage
// are not strictly required at boot but their absence forces PG
// to fall back to the slow cmp-only path and disables btree
// page deduplication respectively, so we seed them too for
// runtime parity with a real PG cluster started from this
// data directory.
//
// Layout per family (lefttype = righttype = opcintype):
//
//   - amprocnum=1 → cmp        (always present)
//   - amprocnum=2 → sortsupport (where PG18 ships one)
//   - amprocnum=4 → equalimage  (always present in PG18)
func pgAmprocInitialEntries() []pgAmprocEntry {
	const (
		famInteger            uint32 = 1976
		famOID                uint32 = 1989
		famText               uint32 = 1994
		famTextPattern        uint32 = 2095
		famBool               uint32 = 424
		famCharBtree          uint32 = 429
		famOidvectorBtree     uint32 = 1991
		famBpcharPatternBtree uint32 = 2097
		// pg_proc OIDs for equalimage support procs (pg_proc.dat).
		btequalimageOID       uint32 = 5051 // generic image-equality
		btvarstrequalimageOID uint32 = 5050 // text / name / varchar
	)
	const baseOID uint32 = 7100
	out := []pgAmprocEntry{
		// integer_ops — cmp + sortsupport + equalimage per type.
		{0, famInteger, 23, 23, 1, 351},                // btint4cmp
		{0, famInteger, 21, 21, 1, 350},                // btint2cmp
		{0, famInteger, 20, 20, 1, 842},                // btint8cmp
		{0, famInteger, 23, 23, 2, 3130},               // btint4sortsupport
		{0, famInteger, 21, 21, 2, 3129},               // btint2sortsupport
		{0, famInteger, 20, 20, 2, 3131},               // btint8sortsupport
		{0, famInteger, 23, 23, 4, btequalimageOID},    // int4
		{0, famInteger, 21, 21, 4, btequalimageOID},    // int2
		{0, famInteger, 20, 20, 4, btequalimageOID},    // int8
		// integer_ops cross-type cmp procs (pg_amproc.dat). Match
		// the cross-type amop strategy rows above so PG's btree
		// LookupOpclassInfo can drive an index scan across integer
		// widths without falling back to lossy cast comparison.
		{0, famInteger, 21, 23, 1, 2190}, // btint24cmp
		{0, famInteger, 21, 20, 1, 2192}, // btint28cmp
		{0, famInteger, 23, 21, 1, 2191}, // btint42cmp
		{0, famInteger, 23, 20, 1, 2188}, // btint48cmp
		{0, famInteger, 20, 21, 1, 2193}, // btint82cmp
		{0, famInteger, 20, 23, 1, 2189}, // btint84cmp
		// oid_ops — cmp + sortsupport + equalimage.
		{0, famOID, 26, 26, 1, 356},                    // btoidcmp
		{0, famOID, 26, 26, 2, 3134},                   // btoidsortsupport
		{0, famOID, 26, 26, 4, btequalimageOID},
		// text_ops — text and name share the family. PG seeds
		// sortsupport + varstr equalimage for both.
		{0, famText, 25, 25, 1, 360},                   // bttextcmp
		{0, famText, 19, 19, 1, 359},                   // btnamecmp
		{0, famText, 25, 25, 2, 3255},                  // bttextsortsupport
		{0, famText, 19, 19, 2, 3135},                  // btnamesortsupport
		{0, famText, 25, 25, 4, btvarstrequalimageOID}, // text
		{0, famText, 19, 19, 4, btvarstrequalimageOID}, // name
		// text_pattern_ops — sortsupport + generic equalimage.
		{0, famTextPattern, 25, 25, 1, 2166},           // bttext_pattern_cmp
		{0, famTextPattern, 25, 25, 2, 3332},           // bttext_pattern_sortsupport
		{0, famTextPattern, 25, 25, 4, btequalimageOID},
		// bool_ops — cmp + equalimage (no sortsupport in PG18).
		{0, famBool, 16, 16, 1, 1693},                  // btboolcmp
		{0, famBool, 16, 16, 4, btequalimageOID},
		// char_ops — cmp + equalimage (no sortsupport in PG18).
		{0, famCharBtree, 18, 18, 1, 358},              // btcharcmp
		{0, famCharBtree, 18, 18, 4, btequalimageOID},
		// oidvector_ops — cmp + equalimage (no sortsupport in PG18).
		{0, famOidvectorBtree, 30, 30, 1, 404},         // btoidvectorcmp
		{0, famOidvectorBtree, 30, 30, 4, btequalimageOID},
		// bpchar_pattern_ops — cmp + sortsupport + equalimage.
		{0, famBpcharPatternBtree, 1042, 1042, 1, 2180},          // btbpchar_pattern_cmp
		{0, famBpcharPatternBtree, 1042, 1042, 2, 3333},          // btbpchar_pattern_sortsupport
		{0, famBpcharPatternBtree, 1042, 1042, 4, btequalimageOID},
	}
	for i := range out {
		out[i].OID = baseOID + uint32(i)
	}
	return out
}

// pgAmprocRow encodes one pg_amproc row. Field order mirrors
// FormData_pg_amproc so PG's GETSTRUCT cast is byte-for-byte
// valid. PG-native alignment puts a 2-byte pad after amprocnum
// (offset 18) before amproc (4-byte aligned, offset 20).
func pgAmprocRow(e pgAmprocEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),       // 1 oid
		executor.NewIntDatum(int64(e.Family)),    // 2 amprocfamily
		executor.NewIntDatum(int64(e.LeftType)),  // 3 amproclefttype
		executor.NewIntDatum(int64(e.RightType)), // 4 amprocrighttype
		executor.NewIntDatum(int64(e.Num)),       // 5 amprocnum (int2)
		executor.NewIntDatum(int64(e.Proc)),      // 6 amproc (regproc)
	}
}

// bootstrapPgAmprocTuples writes the pg_amproc heap to
// base/{1,5}/2603. This is load-bearing for standby boot — PG's
// LookupOpclassInfo unconditionally scans pg_amproc as part of
// RelationInitIndexAccessInfo for every nailed index.
func bootstrapPgAmprocTuples(dataDir string) error {
	cols := pgAmprocColDefs()
	entries := pgAmprocInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgAmprocRow(e))
	}
	_, err := writeMultiPageHeapRows(dataDir, "2603", cols, rows)
	return err
}

// pgIndexColDefs returns the full PG18 FormData_pg_index column shape
// — 21 columns: 2 oids + 2 int2 + 11 bools fixed-part, then int2vector
// indkey, oidvector indcollation / indclass, int2vector indoption, and
// nullable pg_node_tree indexprs / indpred. Byte-aligned offsets match
// `postgres/src/include/catalog/pg_index.h` so the heap-tuple seed and
// PG's `heap_deformtuple → Form_pg_index` cast agree.
func pgIndexColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "indexrelid", Type: catalog.Type{Name: "oid"}},          // 1
		{Name: "indrelid", Type: catalog.Type{Name: "oid"}},            // 2
		{Name: "indnatts", Type: catalog.Type{Name: "int2"}},           // 3
		{Name: "indnkeyatts", Type: catalog.Type{Name: "int2"}},        // 4
		{Name: "indisunique", Type: catalog.Type{Name: "bool"}},        // 5
		{Name: "indnullsnotdistinct", Type: catalog.Type{Name: "bool"}}, // 6
		{Name: "indisprimary", Type: catalog.Type{Name: "bool"}},       // 7
		{Name: "indisexclusion", Type: catalog.Type{Name: "bool"}},     // 8
		{Name: "indimmediate", Type: catalog.Type{Name: "bool"}},       // 9
		{Name: "indisclustered", Type: catalog.Type{Name: "bool"}},     // 10
		{Name: "indisvalid", Type: catalog.Type{Name: "bool"}},         // 11
		{Name: "indcheckxmin", Type: catalog.Type{Name: "bool"}},       // 12
		{Name: "indisready", Type: catalog.Type{Name: "bool"}},         // 13
		{Name: "indislive", Type: catalog.Type{Name: "bool"}},          // 14
		{Name: "indisreplident", Type: catalog.Type{Name: "bool"}},     // 15
		// Variable-length region. int2vector indkey is BKI_FORCE_NOT_NULL.
		{Name: "indkey", Type: catalog.Type{Name: "int2vector"}},       // 16
		{Name: "indcollation", Type: catalog.Type{Name: "oidvector"}},  // 17
		{Name: "indclass", Type: catalog.Type{Name: "oidvector"}},      // 18
		{Name: "indoption", Type: catalog.Type{Name: "int2vector"}},    // 19
		// pg_node_tree fields are nullable; we always encode NULL via
		// the null bitmap (see pgIndexRow).
		{Name: "indexprs", Type: catalog.Type{Name: "pg_node_tree"}},   // 20
		{Name: "indpred", Type: catalog.Type{Name: "pg_node_tree"}},    // 21
	}
}

// pgIndexEntry describes one Form_pg_index row to seed into the heap.
// IndKey, IndCollation, IndClass and IndOption must all have length
// equal to IndNatts (= IndNKeyAtts for goopg's seed, which has no
// INCLUDE columns).
type pgIndexEntry struct {
	IndexRelid   uint32
	IndRelid     uint32
	IndKey       []int16
	IndCollation []uint32
	IndClass     []uint32
	IndOption    []int16
	IsUnique     bool
	IsPrimary    bool
}

// pgIndexInitialEntries returns one Form_pg_index row per nailed index
// (local + shared). Column / opclass selections match upstream
// `pg_<rel>.h::DECLARE_*_INDEX`. Both base/1/2610 and base/5/2610
// receive every entry — pg_index is per-database, but PG's nailed-
// index initialisation walks both critical-local AND critical-shared
// lists against the current database's pg_index, so shared-catalog
// index rows must be present in every per-database pg_index too.
func pgIndexInitialEntries() []pgIndexEntry {
	const (
		oidOps             uint32 = 1981
		int2Ops            uint32 = 1979
		int4Ops            uint32 = 1978
		nameOps            uint32 = 1986
		textOps            uint32 = 3126
		charOps            uint32 = 1985
		oidvectorOps       uint32 = 1987
		float4Ops          uint32 = 10012 // btree float4_ops (postgres.bki: am=403 / btree)
		cCollation         uint32 = 950   // C_COLLATION_OID — name/text use C in catalogs
	)
	// Helper builders.
	entry := func(idxOID, relOID uint32, key []int16, class []uint32, coll []uint32, unique, primary bool) pgIndexEntry {
		n := len(key)
		opt := make([]int16, n)
		return pgIndexEntry{
			IndexRelid:   idxOID,
			IndRelid:     relOID,
			IndKey:       key,
			IndCollation: coll,
			IndClass:     class,
			IndOption:    opt,
			IsUnique:     unique,
			IsPrimary:    primary,
		}
	}
	// Shared-catalog index rows (also written to per-database pg_index).
	shared := []pgIndexEntry{
		entry(2671, 1262, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false),  // pg_database_datname_index
		entry(2672, 1262, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),             // pg_database_oid_index
		entry(2676, 1260, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false),  // pg_authid_rolname_index
		entry(2677, 1260, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),             // pg_authid_oid_index
		entry(2695, 1261, []int16{3, 2, 4}, []uint32{oidOps, oidOps, oidOps}, []uint32{0, 0, 0}, true, false), // pg_auth_members_member_role_index
		// M0106-0010 Step 3z: pg_auth_members_role_member_index (OID 2694).
		// postgres/src/include/catalog/pg_auth_members.h:49 —
		//   DECLARE_UNIQUE_INDEX(pg_auth_members_role_member_index, 2694,
		//     AuthMemRoleMemIndexId, pg_auth_members,
		//     btree(roleid oid_ops, member oid_ops, grantor oid_ops));
		//   MAKE_SYSCACHE(AUTHMEMROLEMEM, pg_auth_members_role_member_index, 8);
		// pg_auth_members attnums (pg_auth_members_d.h): 1=oid,
		// 2=roleid, 3=member, 4=grantor. UNIQUE but NOT primary
		// (DECLARE_UNIQUE_INDEX, not _PKEY which is 6303).
		// Shared catalog like its sibling 2695.
		entry(2694, 1261, []int16{2, 3, 4}, []uint32{oidOps, oidOps, oidOps}, []uint32{0, 0, 0}, true, false), // pg_auth_members_role_member_index
		// pg_shseclabel columns (PG18, pg_shseclabel.h): 1=objoid, 2=classoid,
		// 3=provider, 4=label. Index = btree(objoid, classoid, provider text_ops).
		entry(3593, 3592, []int16{1, 2, 3}, []uint32{oidOps, oidOps, textOps}, []uint32{0, 0, cCollation}, true, true), // pg_shseclabel_object_index
	}
	// Local-catalog index rows mirroring nailedLocalRels.
	local := []pgIndexEntry{
		entry(2703, 1247, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                       // pg_type_oid_index
		entry(2704, 1247, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false),              // pg_type_typname_nsp_index
		entry(2658, 1249, []int16{1, 2}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false),              // pg_attribute_relid_attnam_index
		// pg_attribute columns (PG18, pg_attribute.h): 1=attrelid, 2=attname,
		// 3=atttypid, 4=attlen, 5=attnum, ... Earlier goopg pinned
		// attnum at heap col 6 (legacy PG11/12 layout); PG18 sets
		// Anum_pg_attribute_attnum = 5, so the index must point at col 5.
		entry(2659, 1249, []int16{1, 5}, []uint32{oidOps, int2Ops}, []uint32{0, 0}, true, true),                        // pg_attribute_relid_attnum_index
		entry(2662, 1259, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                       // pg_class_oid_index
		entry(2663, 1259, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false),              // pg_class_relname_nsp_index
		entry(2690, 1255, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                       // pg_proc_oid_index
		entry(2691, 1255, []int16{2, 20, 3}, []uint32{nameOps, oidvectorOps, oidOps}, []uint32{cCollation, 0, 0}, true, false), // pg_proc_proname_args_nsp_index
		// PG18 (postgres/src/include/catalog/indexing.h + pg_index_d.h):
		//   IndexIndrelidIndexId = 2678 = pg_index_indrelid_index
		//     btree(indrelid    oid_ops)  NON-UNIQUE (DECLARE_INDEX)
		//   IndexRelidIndexId    = 2679 = pg_index_indexrelid_index
		//     btree(indexrelid  oid_ops)  UNIQUE PRIMARY KEY
		// Step 3q (2026-05-18) initially split the row with the OIDs
		// reversed (claiming 2678=indexrelid). Step 3r restores the
		// authoritative pg_index_d.h assignment so PG's
		// SearchSysCache1(INDEXRELID, …) — which traverses
		// IndexRelidIndexId = 2679 — finds an index whose indkey={1}
		// (indexrelid) matches the caller's sk_attno. The populated
		// btree built by bootstrapPgIndexIndexrelidIndex therefore
		// writes to base/{1,5}/2679 + global/2679 (Step 3r); 2678's
		// file remains the empty Step-3k placeholder because
		// pg_index_indrelid_index is not used for a syscache lookup
		// during early backend startup.
		entry(2678, 2610, []int16{2}, []uint32{oidOps}, []uint32{0}, false, false),                                     // pg_index_indrelid_index
		entry(2679, 2610, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                       // pg_index_indexrelid_index
		entry(2687, 2616, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                       // pg_opclass_oid_index
		// OID 2655 in upstream is pg_amproc_fam_proc_index (on amprocfamily,
		// amproclefttype, amprocrighttype, amprocnum), not the oid index;
		// the label in nailedLocalRels is historical.
		entry(2655, 2603, []int16{2, 3, 4, 5}, []uint32{oidOps, oidOps, oidOps, int2Ops}, []uint32{0, 0, 0, 0}, true, false), // pg_amproc_fam_proc_index
		// pg_rewrite columns (PG18, pg_rewrite.h): 1=oid, 2=rulename,
		// 3=ev_class. Index = btree(ev_class oid_ops, rulename name_ops).
		entry(2693, 2618, []int16{3, 2}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false),              // pg_rewrite_rel_rulename_index
		// pg_trigger columns (PG18, pg_trigger.h): 1=oid, 2=tgrelid,
		// 3=tgparentid, 4=tgname. Index = btree(tgrelid, tgname).
		entry(2701, 2620, []int16{2, 4}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false),              // pg_trigger_tgrelid_tgname_index
		entry(2667, 2606, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                       // pg_constraint_oid_index
		entry(2688, 2617, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                       // pg_operator_oid_index
		entry(2680, 2611, []int16{1, 3}, []uint32{oidOps, int4Ops}, []uint32{0, 0}, true, true),                        // pg_inherits_relid_seqno_index
		// pg_namespace columns (PG18, pg_namespace.h): 1=oid, 2=nspname,
		// 3=nspowner, 4=nspacl. PG18 indexing.h:
		//   NamespaceNameIndexId = 2684 = pg_namespace_nspname_index
		//     btree(nspname name_ops) UNIQUE
		//   NamespaceOidIndexId  = 2685 = pg_namespace_oid_index
		//     btree(oid oid_ops) UNIQUE PRIMARY KEY
		entry(2684, 2615, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false),                           // pg_namespace_nspname_index
		entry(2685, 2615, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                      // pg_namespace_oid_index
		// OID 2654 = pg_amop_opr_fam_index: btree(amopopr oid_ops,
		// amoppurpose char_ops, amopfamily oid_ops). amoppurpose is
		// pg_amop attnum 6 (char), amopopr is attnum 7, amopfamily attnum 2.
		entry(2654, 2602, []int16{7, 6, 2}, []uint32{oidOps, charOps, oidOps}, []uint32{0, 0, 0}, true, false),         // pg_amop_opr_fam_index
		// M0106-0010 Step 3y: pg_amop_fam_strat_index (OID 2653).
		// postgres/src/include/catalog/pg_amop.h:90 —
		//   DECLARE_UNIQUE_INDEX(pg_amop_fam_strat_index, 2653,
		//     AccessMethodStrategyIndexId, pg_amop,
		//     btree(amopfamily oid_ops, amoplefttype oid_ops,
		//           amoprighttype oid_ops, amopstrategy int2_ops));
		//   MAKE_SYSCACHE(AMOPSTRATEGY, pg_amop_fam_strat_index, 64);
		// pg_amop attnums (pg_amop_d.h): 2=amopfamily, 3=amoplefttype,
		// 4=amoprighttype, 5=amopstrategy. UNIQUE but NOT primary.
		entry(2653, 2602, []int16{2, 3, 4, 5}, []uint32{oidOps, oidOps, oidOps, int2Ops}, []uint32{0, 0, 0, 0}, true, false), // pg_amop_fam_strat_index
		// M0106-0010 Step 3x: pg_aggregate_fnoid_index (OID 2650).
		// postgres/src/include/catalog/pg_aggregate.h:113 —
		//   DECLARE_UNIQUE_INDEX_PKEY(pg_aggregate_fnoid_index, 2650,
		//     AggregateFnoidIndexId, pg_aggregate,
		//     btree(aggfnoid oid_ops));
		//   MAKE_SYSCACHE(AGGFNOID, pg_aggregate_fnoid_index, 16);
		// `aggfnoid` is regproc type but the index uses oid_ops, not
		// regproc_ops. Indexes pg_aggregate (OID 2600) on attnum 1.
		entry(2650, 2600, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_aggregate_fnoid_index
		// M0106-0010 Step 3ab: pg_cast_oid_index (OID 2660).
		// postgres/src/include/catalog/pg_cast.h:59 —
		//   DECLARE_UNIQUE_INDEX_PKEY(pg_cast_oid_index, 2660,
		//     CastOidIndexId, pg_cast, btree(oid oid_ops));
		// Indexes pg_cast (OID 2605) on attnum 1 (oid). UNIQUE PRIMARY KEY.
		entry(2660, 2605, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_cast_oid_index
		// M0106-0010 Step 3ac: pg_cast_source_target_index (OID 2661).
		// postgres/src/include/catalog/pg_cast.h:60 —
		//   DECLARE_UNIQUE_INDEX(pg_cast_source_target_index, 2661,
		//     CastSourceTargetIndexId, pg_cast,
		//     btree(castsource oid_ops, casttarget oid_ops));
		// Indexes pg_cast (OID 2605) on (attnum 2 castsource,
		// attnum 3 casttarget). UNIQUE but NOT PRIMARY KEY
		// (DECLARE_UNIQUE_INDEX, not _PKEY variant).
		entry(2661, 2605, []int16{2, 3}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, false), // pg_cast_source_target_index
		// M0106-0010 Step 3ad: pg_opclass_am_name_nsp_index (OID 2686).
		// postgres/src/include/catalog/pg_opclass.h:85 —
		//   DECLARE_UNIQUE_INDEX(pg_opclass_am_name_nsp_index, 2686,
		//     OpclassAmNameNspIndexId, pg_opclass,
		//     btree(opcmethod oid_ops, opcname name_ops, opcnamespace oid_ops));
		//   MAKE_SYSCACHE(CLAAMNAMENSP, pg_opclass_am_name_nsp_index, 8);
		// pg_opclass attnums (pg_opclass_d.h): 2=opcmethod, 3=opcname,
		// 4=opcnamespace. UNIQUE but NOT primary (DECLARE_UNIQUE_INDEX,
		// not the _PKEY variant — PKEY is 2687 = pg_opclass_oid_index).
		// `opcname` is a `name` type column whose btree opclass uses C
		// collation (C_COLLATION_OID=950) — same convention as
		// pg_database_datname_index (2671) and pg_namespace_nspname_index
		// (2684).
		entry(2686, 2616, []int16{2, 3, 4}, []uint32{oidOps, nameOps, oidOps}, []uint32{0, cCollation, 0}, true, false), // pg_opclass_am_name_nsp_index
		// M0106-0010 Step 3ae: pg_collation_name_enc_nsp_index (OID 3164).
		// postgres/src/include/catalog/pg_collation.h:62 —
		//   DECLARE_UNIQUE_INDEX(pg_collation_name_enc_nsp_index, 3164,
		//     CollationNameEncNspIndexId, pg_collation,
		//     btree(collname name_ops, collencoding int4_ops, collnamespace oid_ops));
		//   MAKE_SYSCACHE(COLLNAMEENCNSP, pg_collation_name_enc_nsp_index, 8);
		// pg_collation attnums (pg_collation_d.h): 2=collname, 7=collencoding,
		// 3=collnamespace. UNIQUE but NOT primary (DECLARE_UNIQUE_INDEX, not
		// the _PKEY variant — PKEY is 3085 = pg_collation_oid_index). `collname`
		// is a `name` type column whose btree opclass uses C collation
		// (C_COLLATION_OID=950) — same convention as pg_database_datname_index
		// (2671), pg_namespace_nspname_index (2684), and
		// pg_opclass_am_name_nsp_index (2686).
		entry(3164, 3456, []int16{2, 7, 3}, []uint32{nameOps, int4Ops, oidOps}, []uint32{cCollation, 0, 0}, true, false), // pg_collation_name_enc_nsp_index
		// M0106-0010 Step 3af: pg_collation_oid_index (OID 3085).
		// postgres/src/include/catalog/pg_collation.h:63 —
		//   DECLARE_UNIQUE_INDEX_PKEY(pg_collation_oid_index, 3085,
		//     CollationOidIndexId, pg_collation, btree(oid oid_ops));
		// pg_collation attnums (pg_collation_d.h): 1=oid. UNIQUE PRIMARY
		// (DECLARE_UNIQUE_INDEX_PKEY) — companion to 3164 (the non-PKEY
		// composite seeded by Step 3ae). Single oid_ops key, no
		// collation. Same single-column oid PKEY pattern as
		// pg_cast_oid_index (2660, Step 3ab) and pg_opclass_oid_index
		// (2687, Step 3l).
		entry(3085, 3456, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_collation_oid_index
		// M0106-0010 Step 3ah: pg_conversion_default_index (OID 2668).
		// postgres/src/include/catalog/pg_conversion.h:63 —
		//   DECLARE_UNIQUE_INDEX(pg_conversion_default_index, 2668,
		//     ConversionDefaultIndexId, pg_conversion,
		//     btree(connamespace oid_ops, conforencoding int4_ops,
		//           contoencoding int4_ops, oid oid_ops));
		//   MAKE_SYSCACHE(CONDEFAULT, pg_conversion_default_index, 8);
		// pg_conversion attnums (pg_conversion_d.h): 3=connamespace,
		// 5=conforencoding, 6=contoencoding, 1=oid. UNIQUE but NOT
		// primary (DECLARE_UNIQUE_INDEX, not the _PKEY variant — the
		// PKEY is 2670 = pg_conversion_oid_index). None of the four
		// keys carry a collation (oid_ops / int4_ops are typeless).
		// Same composite-UNIQUE pattern as pg_amop_fam_strat_index
		// (2754, Step 3y) and pg_collation_name_enc_nsp_index
		// (3164, Step 3ae) — minus the name_ops cCollation slot.
		entry(2668, 2607, []int16{3, 5, 6, 1}, []uint32{oidOps, int4Ops, int4Ops, oidOps}, []uint32{0, 0, 0, 0}, true, false), // pg_conversion_default_index
		// M0106-0010 Step 3ai: pg_conversion_oid_index (OID 2670).
		// postgres/src/include/catalog/pg_conversion.h:65 —
		//   DECLARE_UNIQUE_INDEX_PKEY(pg_conversion_oid_index, 2670,
		//     ConversionOidIndexId, pg_conversion, btree(oid oid_ops));
		// pg_conversion attnums (pg_conversion_d.h): 1=oid. UNIQUE PRIMARY
		// KEY (DECLARE_UNIQUE_INDEX_PKEY) — companion to 2668 (composite
		// UNIQUE non-PKEY seeded by Step 3ah) and 2669 (the conname/nsp
		// composite UNIQUE non-PKEY; to be seeded by Step 3aj). Single
		// oid_ops key, no collation. Same single-column oid PKEY pattern as
		// pg_cast_oid_index (2660, Step 3ab), pg_collation_oid_index (3085,
		// Step 3af), and pg_opclass_oid_index (2687, Step 3l).
		entry(2670, 2607, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_conversion_oid_index
		// M0106-0010 Step 3aj: pg_conversion_name_nsp_index (OID 2669).
		// postgres/src/include/catalog/pg_conversion.h:64 —
		//   DECLARE_UNIQUE_INDEX(pg_conversion_name_nsp_index, 2669,
		//     ConversionNameNspIndexId, pg_conversion,
		//     btree(conname name_ops, connamespace oid_ops));
		//   MAKE_SYSCACHE(CONNAMENSP, pg_conversion_name_nsp_index, 8);
		// pg_conversion attnums (pg_conversion_d.h): 2=conname,
		// 3=connamespace. UNIQUE but NOT primary (DECLARE_UNIQUE_INDEX,
		// not the _PKEY variant — the PKEY is 2670 =
		// pg_conversion_oid_index, seeded by Step 3ai). `conname` is a
		// `name` type column whose btree opclass uses C collation
		// (C_COLLATION_OID=950) — same convention as
		// pg_namespace_nspname_index (2684) and
		// pg_class_relname_nsp_index (2663). Closes the last
		// pg_conversion companion index per pg_conversion.h:63-65;
		// companion to 2668 (composite UNIQUE non-PKEY, Step 3ah) and
		// 2670 (UNIQUE PRIMARY, Step 3ai).
		entry(2669, 2607, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false), // pg_conversion_name_nsp_index
		// M0106-0010 Step 3al: pg_default_acl_role_nsp_obj_index (OID 827).
		// postgres/src/include/catalog/pg_default_acl.h:54 —
		//   DECLARE_UNIQUE_INDEX(pg_default_acl_role_nsp_obj_index, 827,
		//     DefaultAclRoleNspObjIndexId, pg_default_acl,
		//     btree(defaclrole oid_ops, defaclnamespace oid_ops,
		//           defaclobjtype char_ops));
		//   MAKE_SYSCACHE(DEFACLROLENSPOBJ, pg_default_acl_role_nsp_obj_index, 8);
		// pg_default_acl attnums (pg_default_acl_d.h): 2=defaclrole,
		// 3=defaclnamespace, 4=defaclobjtype. UNIQUE but NOT primary
		// (DECLARE_UNIQUE_INDEX, not the _PKEY variant — PKEY is 828 =
		// pg_default_acl_oid_index, to be seeded by Step 3am). None of
		// the three keys carry a collation (oid_ops / char_ops are
		// typeless). Companion to OID 828 (Step 3am UNIQUE PRIMARY on
		// oid). Heap OID 826 (pg_default_acl, Step 3ak nailed rel).
		entry(827, 826, []int16{2, 3, 4}, []uint32{oidOps, oidOps, charOps}, []uint32{0, 0, 0}, true, false), // pg_default_acl_role_nsp_obj_index
		// M0106-0010 Step 3am: pg_default_acl_oid_index (OID 828).
		// postgres/src/include/catalog/pg_default_acl.h:55 —
		//   DECLARE_UNIQUE_INDEX_PKEY(pg_default_acl_oid_index, 828,
		//     DefaultAclOidIndexId, pg_default_acl, btree(oid oid_ops));
		// UNIQUE PRIMARY KEY on attnum 1 (oid). Same single-column oid PKEY
		// pattern as pg_cast_oid_index (2660, Step 3ab),
		// pg_collation_oid_index (3085, Step 3af),
		// pg_conversion_oid_index (2670, Step 3ai), and
		// pg_opclass_oid_index (2687, Step 3l). Heap OID 826 (pg_default_acl,
		// Step 3ak nailed rel). Companion to OID 827 (Step 3al composite
		// UNIQUE non-PKEY backing the DEFACLROLENSPOBJ syscache).
		entry(828, 826, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_default_acl_oid_index
		// M0106-0010 Step 3ao: pg_enum_oid_index (OID 3502).
		// postgres/src/include/catalog/pg_enum.h:47 —
		//   DECLARE_UNIQUE_INDEX_PKEY(pg_enum_oid_index, 3502,
		//     EnumOidIndexId, pg_enum, btree(oid oid_ops));
		//   MAKE_SYSCACHE(ENUMOID, pg_enum_oid_index, 8);
		// pg_enum attnums (pg_enum_d.h): 1=oid. UNIQUE PRIMARY KEY
		// (DECLARE_UNIQUE_INDEX_PKEY) over pg_enum heap OID 3501
		// (Step 3an nailed rel). Single oid_ops key, no collation.
		// Same single-column oid PKEY pattern as pg_cast_oid_index
		// (2660, Step 3ab), pg_collation_oid_index (3085, Step 3af),
		// pg_conversion_oid_index (2670, Step 3ai),
		// pg_default_acl_oid_index (828, Step 3am), and
		// pg_opclass_oid_index (2687, Step 3l). Companion index 3534
		// (pg_enum_typid_sortorder_index UNIQUE composite float4_ops)
		// is seeded by Step 3aq below.
		entry(3502, 3501, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_enum_oid_index
		// M0106-0010 Step 3ap: pg_enum_typid_label_index.
		//   postgres/src/include/catalog/pg_enum.h:48
		//   DECLARE_UNIQUE_INDEX(pg_enum_typid_label_index, 3503,
		//     EnumTypIdLabelIndexId, pg_enum,
		//     btree(enumtypid oid_ops, enumlabel name_ops));
		//   MAKE_SYSCACHE(ENUMTYPOIDNAME, pg_enum_typid_label_index, 8);
		// pg_enum attnums (pg_enum_d.h): 2=enumtypid, 4=enumlabel.
		// UNIQUE non-PRIMARY composite over pg_enum heap OID 3501
		// (Step 3an nailed rel). Same (oid_ops, name_ops) composite
		// shape as pg_type_typname_nsp_index (2704, but with the
		// columns swapped) — leading oid_ops uint OID key plus a
		// name_ops `name` key that carries C_COLLATION_OID = 950 in
		// indcollation. Same convention as pg_conversion_name_nsp_index
		// (2669, Step 3aj) and pg_opclass_am_name_nsp_index (2686,
		// Step 3ad) for the name_ops slot.
		entry(3503, 3501, []int16{2, 4}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false), // pg_enum_typid_label_index
		// M0106-0010 Step 3aq: pg_enum_typid_sortorder_index.
		//   postgres/src/include/catalog/pg_enum.h:48
		//   DECLARE_UNIQUE_INDEX(pg_enum_typid_sortorder_index, 3534,
		//     EnumTypIdSortOrderIndexId, pg_enum,
		//     btree(enumtypid oid_ops, enumsortorder float4_ops));
		// pg_enum attnums (pg_enum_d.h): 2=enumtypid, 3=enumsortorder.
		// UNIQUE non-PRIMARY composite over pg_enum heap OID 3501
		// (Step 3an nailed rel). First nailed index keyed on
		// `float4_ops` btree opclass — OID 10012 from postgres.bki
		// (`insert ( 10012 403 float4_ops 11 10 1970 700 t 0 )`,
		// am=403 / btree). No collation on either key: oid_ops carries
		// no collation; float4_ops is a scalar numeric opclass with no
		// collation slot. Companion to OID 3502 (pg_enum_oid_index
		// UNIQUE PRIMARY, Step 3ao) and OID 3503
		// (pg_enum_typid_label_index UNIQUE composite name_ops, Step 3ap).
		entry(3534, 3501, []int16{2, 3}, []uint32{oidOps, float4Ops}, []uint32{0, 0}, true, false), // pg_enum_typid_sortorder_index
		// M0106-0010 Step 3as: pg_event_trigger_evtname_index. PG18
		//   postgres/src/include/catalog/pg_event_trigger.h:54
		//     DECLARE_UNIQUE_INDEX(pg_event_trigger_evtname_index, 3467,
		//       EventTriggerNameIndexId, pg_event_trigger,
		//       btree(evtname name_ops));
		//     MAKE_SYSCACHE(EVENTTRIGGERNAME, pg_event_trigger_evtname_index, 8);
		// pg_event_trigger attnums (pg_event_trigger_d.h): 2=evtname.
		// UNIQUE non-PRIMARY single-key on a `name` column; like
		// pg_namespace_nspname_index (2684, Step 3t) the name_ops slot
		// carries C_COLLATION_OID = 950. Heap OID 3466 (pg_event_trigger,
		// Step 3ar nailed rel). Companion to OID 3468
		// (pg_event_trigger_oid_index, UNIQUE PRIMARY) — added when a
		// MAKE_SYSCACHE(EVENTTRIGGEROID, …) lookup surfaces.
		entry(3467, 3466, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_event_trigger_evtname_index
		// M0106-0010 Step 3at: pg_event_trigger_oid_index.
		//   postgres/src/include/catalog/pg_event_trigger.h:55
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_event_trigger_oid_index, 3468,
		//       EventTriggerOidIndexId, pg_event_trigger,
		//       btree(oid oid_ops));
		//     MAKE_SYSCACHE(EVENTTRIGGEROID, pg_event_trigger_oid_index, 8);
		// pg_event_trigger attnums (pg_event_trigger_d.h): 1=oid.
		// UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) over pg_event_trigger
		// heap OID 3466 (Step 3ar nailed rel). Single oid_ops key, no
		// collation — same single-column oid PKEY pattern as
		// pg_enum_oid_index (3502, Step 3ao), pg_cast_oid_index (2660,
		// Step 3ab), pg_collation_oid_index (3085, Step 3af),
		// pg_conversion_oid_index (2670, Step 3ai),
		// pg_default_acl_oid_index (828, Step 3am), and
		// pg_opclass_oid_index (2687, Step 3l). Companion to OID 3467
		// (pg_event_trigger_evtname_index, UNIQUE non-PKEY, Step 3as).
		entry(3468, 3466, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_event_trigger_oid_index
		// M0106-0010 Step 3ax: pg_extension_oid_index.
		//   postgres/src/include/catalog/pg_extension.h:56
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_extension_oid_index, 3080,
		//       ExtensionOidIndexId, pg_extension, btree(oid oid_ops));
		//   MAKE_SYSCACHE(EXTENSIONOID, pg_extension_oid_index, 2);
		// pg_extension attnums (pg_extension_d.h): 1=oid.
		// UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) over pg_extension
		// heap OID 3079 (Step 3aw nailed rel). Single oid_ops key, no
		// collation — same single-column oid PKEY pattern as
		// pg_event_trigger_oid_index (3468, Step 3at),
		// pg_enum_oid_index (3502, Step 3ao), pg_cast_oid_index (2660,
		// Step 3ab), pg_collation_oid_index (3085, Step 3af),
		// pg_conversion_oid_index (2670, Step 3ai),
		// pg_default_acl_oid_index (828, Step 3am), and
		// pg_opclass_oid_index (2687, Step 3l). Companion to OID 3081
		// (pg_extension_name_index, UNIQUE non-PKEY) — Step 3ay.
		entry(3080, 3079, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_extension_oid_index
		// M0106-0010 Step 3ay: pg_extension_name_index.
		//   postgres/src/include/catalog/pg_extension.h:57
		//     DECLARE_UNIQUE_INDEX(pg_extension_name_index, 3081,
		//       ExtensionNameIndexId, pg_extension, btree(extname name_ops));
		//   MAKE_SYSCACHE(EXTENSIONNAME, pg_extension_name_index, 2);
		// pg_extension attnums (pg_extension_d.h): 2=extname.
		// UNIQUE non-PRIMARY (DECLARE_UNIQUE_INDEX) over pg_extension
		// heap OID 3079 (Step 3aw nailed rel). Single name_ops key with
		// C_COLLATION_OID = 950 — same single-column name PKEY-less
		// pattern as pg_event_trigger_evtname_index (3467, Step 3as) and
		// pg_namespace_nspname_index (2684, Step 3t). Companion to OID
		// 3080 (pg_extension_oid_index, UNIQUE PKEY) seeded in Step 3ax.
		entry(3081, 3079, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_extension_name_index
		// M0106-0010 Step 3bc: pg_foreign_data_wrapper_name_index.
		//   postgres/src/include/catalog/pg_foreign_data_wrapper.h:56
		//     DECLARE_UNIQUE_INDEX(pg_foreign_data_wrapper_name_index, 548,
		//       ForeignDataWrapperNameIndexId, pg_foreign_data_wrapper,
		//       btree(fdwname name_ops));
		//   MAKE_SYSCACHE(FOREIGNDATAWRAPPERNAME,
		//     pg_foreign_data_wrapper_name_index, 2);
		// pg_foreign_data_wrapper attnums (pg_foreign_data_wrapper_d.h):
		// 2=fdwname. UNIQUE non-PRIMARY (DECLARE_UNIQUE_INDEX) over the
		// pg_foreign_data_wrapper heap OID 2328 (Step 3bb nailed rel).
		// Single name_ops key with C_COLLATION_OID = 950 — same
		// single-column name PKEY-less pattern as pg_extension_name_index
		// (3081, Step 3ay), pg_event_trigger_evtname_index (3467,
		// Step 3as), and pg_namespace_nspname_index (2684, Step 3t).
		// E2E test surfaced this index as the first probe (not the OID
		// companion 112) because process_settings → catcache init opens
		// FOREIGNDATAWRAPPERNAME before FOREIGNDATAWRAPPEROID.
		entry(548, 2328, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_foreign_data_wrapper_name_index
		// M0106-0010 Step 3bd: pg_foreign_data_wrapper_oid_index.
		//   postgres/src/include/catalog/pg_foreign_data_wrapper.h:55
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_data_wrapper_oid_index, 112,
		//       ForeignDataWrapperOidIndexId, pg_foreign_data_wrapper,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(FOREIGNDATAWRAPPEROID,
		//     pg_foreign_data_wrapper_oid_index, 2);
		// pg_foreign_data_wrapper attnums (pg_foreign_data_wrapper_d.h):
		// 1=oid. UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) over the
		// pg_foreign_data_wrapper heap OID 2328 (Step 3bb nailed rel).
		// Single oid_ops key (no collation) — same single-column oid
		// PKEY pattern as pg_extension_oid_index (3080, Step 3ax),
		// pg_event_trigger_oid_index (3468, Step 3at),
		// pg_default_acl_oid_index (828, Step 3am),
		// pg_conversion_oid_index (2670, Step 3ai),
		// pg_cast_oid_index (2660, Step 3ab),
		// pg_collation_oid_index (3085, Step 3af),
		// pg_enum_oid_index (3502, Step 3ao),
		// pg_opclass_oid_index (2687, Step 3l). Companion to OID 548
		// (pg_foreign_data_wrapper_name_index, Step 3bc).
		entry(112, 2328, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_foreign_data_wrapper_oid_index
	}
	out := make([]pgIndexEntry, 0, len(shared)+len(local))
	out = append(out, shared...)
	out = append(out, local...)
	return out
}

// pgIndexNattsByOID returns the per-index attribute count derived from
// pgIndexInitialEntries. It is consumed by relcache_init.go::flattenRels
// to keep pg_class.relnatts aligned with pg_index.indnatts for every
// nailed index — PG's RelationInitIndexAccessInfo FATALs with
// "relnatts disagrees with indnatts for index <oid>" otherwise.
func pgIndexNattsByOID() map[uint32]int16 {
	entries := pgIndexInitialEntries()
	out := make(map[uint32]int16, len(entries))
	for _, e := range entries {
		out[e.IndexRelid] = int16(len(e.IndKey))
	}
	return out
}

// pgIndexRow builds the 21-column Form_pg_index row matching
// pgIndexColDefs order. The two pg_node_tree columns (indexprs,
// indpred) are emitted as SQL NULL via NullDatum — none of the
// nailed system catalog indexes is expression-based or partial.
func pgIndexRow(e pgIndexEntry) executor.Row {
	natts := int16(len(e.IndKey))
	return executor.Row{
		executor.NewIntDatum(int64(e.IndexRelid)),       // 1 indexrelid
		executor.NewIntDatum(int64(e.IndRelid)),         // 2 indrelid
		executor.NewIntDatum(int64(natts)),              // 3 indnatts
		executor.NewIntDatum(int64(natts)),              // 4 indnkeyatts
		executor.NewBoolDatum(e.IsUnique),               // 5 indisunique
		executor.NewBoolDatum(false),                    // 6 indnullsnotdistinct
		executor.NewBoolDatum(e.IsPrimary),              // 7 indisprimary
		executor.NewBoolDatum(false),                    // 8 indisexclusion
		executor.NewBoolDatum(true),                     // 9 indimmediate
		executor.NewBoolDatum(false),                    // 10 indisclustered
		executor.NewBoolDatum(true),                     // 11 indisvalid
		executor.NewBoolDatum(false),                    // 12 indcheckxmin
		executor.NewBoolDatum(true),                     // 13 indisready
		executor.NewBoolDatum(true),                     // 14 indislive
		executor.NewBoolDatum(false),                    // 15 indisreplident
		executor.NewBytesDatum(int2VectorBytes(e.IndKey)),    // 16 indkey
		executor.NewBytesDatum(oidVectorBytes(e.IndCollation)), // 17 indcollation
		executor.NewBytesDatum(oidVectorBytes(e.IndClass)),    // 18 indclass
		executor.NewBytesDatum(int2VectorBytes(e.IndOption)), // 19 indoption
		executor.NullDatum,                              // 20 indexprs (NULL — no expression indexes)
		executor.NullDatum,                              // 21 indpred  (NULL — no partial indexes)
	}
}

// bootstrapPgIndexTuples writes Form_pg_index heap tuples for every
// nailed index (local + shared) to base/{1,5}/2610. M0106-0010 step 3g
// supersedes step 3f's empty-page placeholder. PG's standby boot calls
// `RelationCacheInitializePhase3 → load_critical_index → ScanPgRelation
// → SearchSysCache1(INDEXRELID, ...)`; without an actual row each
// nailed index FATALs with "cache lookup failed for index <oid>".
//
// Returns a map keyed by `indexrelid` so Step 3p's
// bootstrapPgIndexIndexrelidIndex can stamp each leaf IndexTuple's
// t_tid at the (block, offset) where its Form_pg_index row landed.
func bootstrapPgIndexTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgIndexColDefs()
	entries := pgIndexInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgIndexRow(e))
	}
	tids, err := writeMultiPageHeapRows(dataDir, "2610", cols, rows)
	if err != nil {
		return nil, err
	}
	m := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		m[e.IndexRelid] = tids[i]
	}
	return m, nil
}

// pgClassColDefs returns pg_class column descriptors matching PG18's
// FormData_pg_class struct byte-for-byte. RelationBuildDesc casts the
// raw heap tuple as FormData_pg_class*, so every fixed-size field must
// be present at the correct struct offset.
func pgClassColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},               // 0
		{Name: "relname", Type: catalog.Type{Name: "name"}},          // 4 (64 bytes)
		{Name: "relnamespace", Type: catalog.Type{Name: "oid"}},      // 68
		{Name: "reltype", Type: catalog.Type{Name: "oid"}},           // 72
		{Name: "reloftype", Type: catalog.Type{Name: "oid"}},         // 76
		{Name: "relowner", Type: catalog.Type{Name: "oid"}},          // 80
		{Name: "relam", Type: catalog.Type{Name: "oid"}},             // 84
		{Name: "relfilenode", Type: catalog.Type{Name: "oid"}},       // 88
		{Name: "reltablespace", Type: catalog.Type{Name: "oid"}},     // 92
		{Name: "relpages", Type: catalog.Type{Name: "int4"}},         // 96
		{Name: "reltuples", Type: catalog.Type{Name: "float4"}},      // 100
		{Name: "relallvisible", Type: catalog.Type{Name: "int4"}},    // 104
		{Name: "relallfrozen", Type: catalog.Type{Name: "int4"}},     // 108
		{Name: "reltoastrelid", Type: catalog.Type{Name: "oid"}},     // 112
		{Name: "relhasindex", Type: catalog.Type{Name: "bool"}},      // 116
		{Name: "relisshared", Type: catalog.Type{Name: "bool"}},      // 117
		{Name: "relpersistence", Type: catalog.Type{Name: "char"}},   // 118
		{Name: "relkind", Type: catalog.Type{Name: "char"}},          // 119
		{Name: "relnatts", Type: catalog.Type{Name: "int2"}},         // 120
		{Name: "relchecks", Type: catalog.Type{Name: "int2"}},        // 122
		{Name: "relhasrules", Type: catalog.Type{Name: "bool"}},      // 124
		{Name: "relhastriggers", Type: catalog.Type{Name: "bool"}},   // 125
		{Name: "relhassubclass", Type: catalog.Type{Name: "bool"}},   // 126
		{Name: "relrowsecurity", Type: catalog.Type{Name: "bool"}},   // 127
		{Name: "relforcerowsecurity", Type: catalog.Type{Name: "bool"}}, // 128
		{Name: "relispopulated", Type: catalog.Type{Name: "bool"}},   // 129
		{Name: "relreplident", Type: catalog.Type{Name: "char"}},     // 130
		{Name: "relispartition", Type: catalog.Type{Name: "bool"}},   // 131
		{Name: "relrewrite", Type: catalog.Type{Name: "oid"}},        // 132
		{Name: "relfrozenxid", Type: catalog.Type{Name: "xid"}},      // 136
		{Name: "relminmxid", Type: catalog.Type{Name: "xid"}},        // 140
		// Varlena columns. PG's extractRelOptions / aclitem-walking code
		// casts the raw datum as ArrayType*; the empty placeholder MUST
		// therefore be a valid binary ArrayType, not a text "{}" varlena.
		// See encodeValuePG's "aclitem[]" / "text[]" cases and
		// docs/design/0106-0010-pg-class-empty-array-encoding.md.
		{Name: "relacl", Type: catalog.Type{Name: "aclitem[]"}},     // 144 varlena (16-byte empty ArrayType)
		{Name: "reloptions", Type: catalog.Type{Name: "text[]"}},    // varlena
		{Name: "relpartbound", Type: catalog.Type{Name: "pg_node_tree"}}, // varlena text
	}
}

// pgClassRow builds a 14-col pg_class tuple matching pgClassColDefs order.
func pgClassRow(rel nailedRel) executor.Row {
	relType := rel.RelType
	if relType == 0 {
		relType = rel.OID
	}
	relAm := int64(0)
	if rel.RelKind == 'r' {
		relAm = 2
	} else if rel.RelKind == 'i' {
		relAm = 403
	}
	return executor.Row{
		executor.NewIntDatum(int64(rel.OID)),      // 0: oid
		executor.NewStringDatum(rel.RelName),      // 4: relname
		executor.NewIntDatum(11),                  // 68: relnamespace
		executor.NewIntDatum(int64(relType)),      // 72: reltype
		executor.NewIntDatum(0),                   // 76: reloftype
		executor.NewIntDatum(10),                  // 80: relowner
		executor.NewIntDatum(relAm),               // 84: relam
		executor.NewIntDatum(int64(rel.OID)),      // 88: relfilenode
		executor.NewIntDatum(0),                   // 92: reltablespace
		executor.NewIntDatum(0),                   // 96: relpages
		executor.NewIntDatum(0),                   // 100: reltuples
		executor.NewIntDatum(0),                   // 104: relallvisible
		executor.NewIntDatum(0),                   // 108: relallfrozen
		executor.NewIntDatum(0),                   // 112: reltoastrelid
		executor.NewBoolDatum(false),              // 116: relhasindex
		executor.NewBoolDatum(rel.IsShared),       // 117: relisshared
		executor.NewStringDatum("p"),              // 118: relpersistence
		executor.NewStringDatum(string(rune(rel.RelKind))), // 119: relkind
		executor.NewIntDatum(int64(rel.RelNatts)), // 120: relnatts
		executor.NewIntDatum(0),                   // 122: relchecks
		executor.NewBoolDatum(false),              // 124: relhasrules
		executor.NewBoolDatum(false),              // 125: relhastriggers
		executor.NewBoolDatum(false),              // 126: relhassubclass
		executor.NewBoolDatum(false),              // 127: relrowsecurity
		executor.NewBoolDatum(false),              // 128: relforcerowsecurity
		executor.NewBoolDatum(true),               // 129: relispopulated
		executor.NewStringDatum("n"),              // 130: relreplident
		executor.NewBoolDatum(false),              // 131: relispartition
		executor.NewIntDatum(0),                   // 132: relrewrite
		executor.NewIntDatum(3),                   // 136: relfrozenxid
		executor.NewIntDatum(1),                   // 140: relminmxid
		executor.NewStringDatum("{}"),             // relacl (empty aclitem[])
		executor.NewStringDatum("{}"),             // reloptions (empty text[])
		executor.NewStringDatum(""),               // relpartbound (empty pg_node_tree)
	}
}

// pgAttrColDefs returns the 24 pg_attribute column descriptors.
func pgAttrColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "attrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "attname", Type: catalog.Type{Name: "name"}},
		{Name: "atttypid", Type: catalog.Type{Name: "oid"}},
		{Name: "attlen", Type: catalog.Type{Name: "int2"}},
		{Name: "attnum", Type: catalog.Type{Name: "int2"}},
		{Name: "atttypmod", Type: catalog.Type{Name: "int4"}},
		{Name: "attndims", Type: catalog.Type{Name: "int2"}},
		{Name: "attbyval", Type: catalog.Type{Name: "bool"}},
		{Name: "attalign", Type: catalog.Type{Name: "char"}},
		{Name: "attstorage", Type: catalog.Type{Name: "char"}},
		{Name: "attcompression", Type: catalog.Type{Name: "char"}},
		{Name: "attnotnull", Type: catalog.Type{Name: "bool"}},
		{Name: "atthasdef", Type: catalog.Type{Name: "bool"}},
		{Name: "atthasmissing", Type: catalog.Type{Name: "bool"}},
		{Name: "attidentity", Type: catalog.Type{Name: "char"}},
		{Name: "attgenerated", Type: catalog.Type{Name: "char"}},
		{Name: "attisdropped", Type: catalog.Type{Name: "bool"}},
		{Name: "attislocal", Type: catalog.Type{Name: "bool"}},
		{Name: "attinhcount", Type: catalog.Type{Name: "int2"}},
		{Name: "attcollation", Type: catalog.Type{Name: "oid"}},
		{Name: "attacl", Type: catalog.Type{Name: "text"}},
		{Name: "attoptions", Type: catalog.Type{Name: "text"}},
		{Name: "attfdwoptions", Type: catalog.Type{Name: "text"}},
		{Name: "attmissingval", Type: catalog.Type{Name: "text"}},
	}
}

// pgAttrEntriesForRel returns the attribute definitions for a nailed relation.
// For pg_class and pg_attribute themselves we use column lists matching the
// heap encoding; for all others we use the relation's Attrs from the nailed
// registration.
func pgAttrEntriesForRel(rel nailedRel) []nailedAttr {
	if rel.OID == catalog.RelationRelationId {
		// pg_class: derive from pgClassColDefs
		cols := pgClassColDefs()
		attrs := make([]nailedAttr, len(cols))
		for i, c := range cols {
			attrs[i] = nailedAttr{
				Name:    c.Name,
				TypeOID: pgCatalogTypeOID(c.Type.Name),
				Num:     int16(i + 1),
				Len:     int16(pgCatalogTypeLen(c.Type.Name)),
				// Varlena columns (attlen=-1) are nullable; fixed-size
				// catalog columns are NOT NULL. PG's att_addlength_pointer
				// asserts on attlen=0, so we must not produce that value.
				NotNull: pgCatalogTypeLen(c.Type.Name) != -1,
			}
		}
		return attrs
	}
	if rel.OID == catalog.AttributeRelationId {
		cols := pgAttrColDefs()
		attrs := make([]nailedAttr, len(cols))
		for i, c := range cols {
			attrs[i] = nailedAttr{
				Name:    c.Name,
				TypeOID: pgCatalogTypeOID(c.Type.Name),
				Num:     int16(i + 1),
				Len:     int16(pgCatalogTypeLen(c.Type.Name)),
				NotNull: pgCatalogTypeLen(c.Type.Name) != -1,
			}
		}
		return attrs
	}
	return rel.Attrs
}

// pgAttributeRow builds one pg_attribute tuple.
func pgAttributeRow(relOID uint32, a nailedAttr) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(relOID)),
		executor.NewStringDatum(a.Name),
		executor.NewIntDatum(int64(a.TypeOID)),
		executor.NewIntDatum(int64(a.Len)),
		executor.NewIntDatum(int64(a.Num)),
		executor.NewIntDatum(-1),            // atttypmod
		executor.NewIntDatum(0),             // attndims
		executor.NewBoolDatum(pgTypeByVal(a.TypeOID)),
		executor.NewStringDatum(pgTypeAlignChar(a.TypeOID)),
		executor.NewStringDatum(pgTypeStorageChar(a.TypeOID)),
		executor.NewStringDatum(""),         // attcompression
		executor.NewBoolDatum(a.NotNull),
		executor.NewBoolDatum(false),        // atthasdef
		executor.NewBoolDatum(false),        // atthasmissing
		executor.NewStringDatum(""),         // attidentity
		executor.NewStringDatum(""),         // attgenerated
		executor.NewBoolDatum(false),        // attisdropped
		executor.NewBoolDatum(true),         // attislocal
		executor.NewIntDatum(0),             // attinhcount
		executor.NewIntDatum(0),             // attcollation
		// Step 3u: Emit NULL (not empty-text varlena) for the four nullable
		// trailing varlena/array columns. Previously NewStringDatum("") wrote
		// a 1-byte empty varlena which PG's RelationGetIndexAttOptions →
		// index_opclass_options interpreted as "attoptions present" → ereport
		// ERROR → generate_opclass_name → OpclassIsVisible →
		// get_namespace_oid(pg_namespace_nspname_index=2684) → recursive
		// RelationInitIndexAccessInfo on the very index whose error message
		// is being formatted → ERRORDATA_STACK_SIZE PANIC. PG18's default
		// for an unconfigured catalog row is SQL NULL on all four columns.
		executor.NullDatum,                  // attacl
		executor.NullDatum,                  // attoptions
		executor.NullDatum,                  // attfdwoptions
		executor.NullDatum,                  // attmissingval
	}
}

// hasVarWidthCol returns true if any column is varlena ("text" type).
func hasVarWidthCol(cols []catalog.Column) bool {
	for _, c := range cols {
		switch c.Type.Name {
		case "text", "varchar", "bpchar",
			"pg_node_tree",
			"text[]", "_text",
			"aclitem[]", "_aclitem",
			"oid[]", "_oid",
			"int2[]", "_int2",
			"char[]", "_char",
			"oidvector",
			"int2vector",
			"anyarray":
			return true
		}
	}
	return false
}

// heapTID identifies a heap tuple by (block, 1-based offset within block).
// Used by Step 3m's btree index bootstrapping path (pg_class_oid_index)
// where we need to know which TID each just-packed row landed at.
type heapTID struct {
	Block  uint32
	Offset uint16
}

// writeMultiPageHeap writes multiple heap tuples (one per nailed rel) into
// a multi-page heap file. Returns the per-row TID slice (aligned with the
// input rels) so callers that need to build a covering btree index over the
// same rows can stamp the correct ItemPointer into each IndexTuple.
func writeMultiPageHeap(dataDir, relFile string, cols []catalog.Column, rels []nailedRel, rowFn func(nailedRel) executor.Row) ([]heapTID, error) {
	var pages [][]byte
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	tids := make([]heapTID, len(rels))
	for i, rel := range rels {
		row := rowFn(rel)
		payload, err := executor.EncodeRowPG(cols, row)
		if err != nil {
			return nil, fmt.Errorf("encode %s row for %s: %w", relFile, rel.RelName, err)
		}
		tuple := storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
		tuple.Header.SetNatts(len(cols))
		if hasVarWidthCol(cols) {
			tuple.Header.Infomask |= storage.HeapHasVarWidth
		}
		off, err := storage.PageAddHeapTuple(page, tuple)
		if err != nil {
			pages = append(pages, page)
			page = make(storage.Page, storage.BlockSize)
			if err := storage.InitPage(page); err != nil {
				return nil, err
			}
			off, err = storage.PageAddHeapTuple(page, tuple)
			if err != nil {
				return nil, fmt.Errorf("add %s tuple for %s on fresh page: %w", relFile, rel.RelName, err)
			}
		}
		tids[i] = heapTID{Block: uint32(len(pages)), Offset: off}
	}
	pages = append(pages, page)
	raw := make([]byte, 0, storage.BlockSize*len(pages))
	for _, p := range pages {
		raw = append(raw, p...)
	}
	b1 := filepath.Join(dataDir, "base", "1")
	if err := os.WriteFile(filepath.Join(b1, relFile), raw, 0o600); err != nil {
		return nil, err
	}
	b5 := filepath.Join(dataDir, "base", "5")
	if err := os.MkdirAll(b5, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(b5, relFile), raw, 0o600); err != nil {
		return nil, err
	}
	return tids, nil
}

// writeMultiPageHeapRows is like writeMultiPageHeap but takes pre-built rows.
// Returns the per-row heap TIDs in input order — callers that do not need
// them may discard the slice. Step 3o uses the TIDs to build composite-key
// btree index tuples for pg_attribute_relid_attnum_index.
func writeMultiPageHeapRows(dataDir, relFile string, cols []catalog.Column, rows []executor.Row) ([]heapTID, error) {
	var pages [][]byte
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	tids := make([]heapTID, 0, len(rows))
	for _, row := range rows {
		payload, err := executor.EncodeRowPG(cols, row)
		if err != nil {
			return nil, fmt.Errorf("encode %s row: %w", relFile, err)
		}
		bitmap := executor.NullBitmapPG(row)
		var tuple storage.HeapTuple
		if bitmap != nil {
			tuple = storage.NewHeapTupleWithNulls(storage.TransactionID(1), storage.InvalidTransactionID, bitmap, payload)
		} else {
			tuple = storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
		}
		tuple.Header.SetNatts(len(cols))
		if hasVarWidthCol(cols) {
			tuple.Header.Infomask |= storage.HeapHasVarWidth
		}
		off, err := storage.PageAddHeapTuple(page, tuple)
		if err != nil {
			pages = append(pages, page)
			page = make(storage.Page, storage.BlockSize)
			if err := storage.InitPage(page); err != nil {
				return nil, err
			}
			off, err = storage.PageAddHeapTuple(page, tuple)
			if err != nil {
				return nil, fmt.Errorf("add %s tuple on fresh page: %w", relFile, err)
			}
		}
		tids = append(tids, heapTID{Block: uint32(len(pages)), Offset: off})
	}
	pages = append(pages, page)
	raw := make([]byte, 0, storage.BlockSize*len(pages))
	for _, p := range pages {
		raw = append(raw, p...)
	}
	b1 := filepath.Join(dataDir, "base", "1")
	if err := os.WriteFile(filepath.Join(b1, relFile), raw, 0o600); err != nil {
		return nil, err
	}
	b5 := filepath.Join(dataDir, "base", "5")
	if err := os.MkdirAll(b5, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(b5, relFile), raw, 0o600); err != nil {
		return nil, err
	}
	return tids, nil
}

// pgCatalogTypeOID maps a goopg type name → PG type OID.
func pgCatalogTypeOID(t string) uint32 {
	switch t {
	case "bool":
		return 16
	case "char":
		return 18
	case "name":
		return 19
	case "int8":
		return 20
	case "int2":
		return 21
	case "int4", "oid", "xid":
		return 23
	case "text":
		return 25
	case "pg_node_tree":
		return 194
	case "float4":
		return 700
	case "float8":
		return 701
	case "text[]", "_text":
		return 1009
	case "aclitem[]", "_aclitem":
		return 1034
	case "anyarray":
		return 2277
	case "regproc":
		return 24
	case "oidvector":
		return 30
	case "int2vector":
		return 22
	case "char[]", "_char":
		return 1002
	case "oid[]", "_oid":
		return 1028
	case "index_am_handler":
		return 325
	case "table_am_handler":
		return 269
	case "internal":
		return 2281
	}
	return 0
}

// pgCatalogTypeLen returns PG attlen for a goopg type name.
func pgCatalogTypeLen(t string) int {
	switch t {
	case "bool", "char":
		return 1
	case "int2":
		return 2
	case "int4", "oid", "xid", "float4", "regproc", "index_am_handler", "table_am_handler":
		return 4
	case "int8", "float8":
		return 8
	case "name":
		return 64
	case "text", "pg_node_tree", "text[]", "_text", "aclitem[]", "_aclitem", "anyarray", "oidvector", "int2vector", "char[]", "_char", "oid[]", "_oid":
		return -1
	}
	return 4
}

func pgTypeByVal(oid uint32) bool {
	switch oid {
	case 16, 18, 21, 23, 26, 700, 20, 701, 24, 325, 269:
		return true
	}
	return false
}

func pgTypeAlignChar(oid uint32) string {
	switch oid {
	case 16, 18:
		return "c"
	case 21:
		return "s"
	case 23, 26, 700, 194, 1009, 1034, 2277, 24, 325, 269, 30, 22, 1002, 1028:
		return "i"
	case 20, 701:
		return "d"
	}
	return "i"
}

func pgTypeStorageChar(oid uint32) string {
	switch oid {
	case 25, 1043, 1042, 194, 1009, 1034, 2277, 1002, 1028:
		return "x"
	}
	return "p"
}

// bootstrapCLog creates the initial commit log at <dataDir>/global/pg_xact and
// marks the PostgreSQL bootstrap transaction IDs (BootstrapTransactionID=1,
// FrozenTransactionID=2) as committed. These XIDs stamp the system-catalog
// seed rows written by bootstrapSystemCatalogs; without this, a restart would
// find Unknown status for xmin=1 and skip those rows.
func bootstrapCLog(dataDir string) error {
	path := filepath.Join(dataDir, "global", "pg_xact")
	c, err := mvcc.OpenCLog(path)
	if err != nil {
		return err
	}
	if err := c.SetCommitted(mvcc.BootstrapTransactionID); err != nil {
		return fmt.Errorf("mark bootstrap xid: %w", err)
	}
	return c.SetCommitted(mvcc.FrozenTransactionID)
}

// bootstrapSystemCatalogs creates the three core system catalog heap
// relfiles (pg_type, pg_attribute, pg_class) under
// <dataDir>/base/<DefaultDBOid>/ and seeds them with their initial rows.
//
// The storage manager is created and closed inline — no buffer pool is
// needed for a one-shot Extend of a seeded page.
//
// Row encoding matches executor.EncodeRow so a SeqScan on the resulting
// relfile produces the correct values without special-casing.  Transaction
// IDs are set to bootstrapXID (1) so the rows are always visible.
func bootstrapSystemCatalogs(dataDir string) error {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	// pg_type: built-in type entries.
	var pgTypeData [][]byte
	for _, tr := range pgTypeBootstrapRows() {
		pgTypeData = append(pgTypeData, catalog.EncodePGTypeRow(tr))
	}
	if err := extendWithRows(mgr, catalog.TypeRelationId, pgTypeData); err != nil {
		return fmt.Errorf("seed pg_type: %w", err)
	}

	// pg_class: self-referential entries for the three system catalogs.
	var pgClassData [][]byte
	for _, cr := range pgClassBootstrapRows() {
		pgClassData = append(pgClassData, catalog.EncodePGClassRow(cr))
	}
	if err := extendWithRows(mgr, catalog.RelationRelationId, pgClassData); err != nil {
		return fmt.Errorf("seed pg_class: %w", err)
	}

	// pg_attribute: column definitions for the three system catalogs.
	var pgAttrData [][]byte
	for _, ar := range pgAttributeBootstrapRows() {
		pgAttrData = append(pgAttrData, catalog.EncodePGAttributeRow(ar))
	}
	if err := extendWithRows(mgr, catalog.AttributeRelationId, pgAttrData); err != nil {
		return fmt.Errorf("seed pg_attribute: %w", err)
	}

	return nil
}

// extendWithRows initialises a fresh page, writes all rows as heap tuples
// with bootstrapXID, and appends it as block 0 via Manager.Extend.
func extendWithRows(mgr *storage.Manager, relOID uint32, rows [][]byte) error {
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return err
	}
	xid := storage.TransactionID(bootstrapXID)
	for i, data := range rows {
		t := storage.NewHeapTuple(xid, storage.InvalidTransactionID, data)
		if _, err := storage.PageAddHeapTuple(page, t); err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
	}
	rel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: relOID,
		Fork:   storage.MainFork,
	}
	_, err := mgr.Extend(rel, page)
	return err
}

// ensureEmptyDir is upstream's "directory must be empty" guard.
// Returns nil when dir doesn't yet exist (Init will create it) or
// when it exists and contains no entries; otherwise an error.
func ensureEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("goopg init: read %q: %w", dir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("goopg init: directory %q already exists and is not empty", dir)
	}
	return nil
}

func defaultPostgresqlConf() []byte {
	return []byte(`# goopg postgresql.conf — defaults written by 'goopg init'.
#
# This file uses the same key=value syntax as upstream
# PostgreSQL's postgresql.conf. Lines beginning with '#' are
# comments; settings reflect goopg's v0 surface.

#listen_addresses = '127.0.0.1'
#port = 5432

# Encoding is fixed at UTF-8 for the wire protocol.
#server_encoding = 'UTF8'
#client_encoding = 'UTF8'

# DateStyle and TimeZone use the same values goopg advertises in
# the startup ParameterStatus block.
#DateStyle = 'ISO, MDY'
#TimeZone = 'UTC'
`)
}

func makeRelMapFile(mappings [][2]uint32) []byte {
	// RelMapFile layout (PG src/backend/utils/cache/relmapper.c):
	//   int32 magic (4 bytes) = RELMAPPER_FILEMAGIC (0x592717)
	//   int32 num_mappings (4 bytes)
	//   RelMapping mappings[64] (512 bytes, 8 bytes each: Oid + RelFileNumber)
	//   pg_crc32c crc (4 bytes) at offset 520
	const (
		relFileSize   = 524
		relMagic      = 0x592717
		relCRCCOffset = 520
	)
	out := make([]byte, relFileSize)
	binary.LittleEndian.PutUint32(out[0:4], relMagic)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(mappings)))
	for i, m := range mappings {
		off := 8 + i*8
		binary.LittleEndian.PutUint32(out[off:off+4], m[0])
		binary.LittleEndian.PutUint32(out[off+4:off+8], m[1])
	}
	crc := crc32.Checksum(out[:relCRCCOffset], crcCastagnoliTable)
	binary.LittleEndian.PutUint32(out[relCRCCOffset:], crc)
	return out
}

func defaultRelMapFile() []byte {
	return makeRelMapFile([][2]uint32{
		{1262, 1262}, // pg_database
		{1260, 1260}, // pg_authid
		{1261, 1261}, // pg_auth_members
		{1213, 1213}, // pg_tablespace
		{1214, 1214}, // pg_shdepend
		{3592, 3592}, // pg_shdescription
		{6000, 6000}, // pg_replication_origin
		{6100, 6100}, // pg_subscription
		{6243, 6243}, // pg_parameter_acl
	})
}

func defaultPgIdentConf() []byte {
	return []byte(`# PostgreSQL User Name Maps
# This file maps system user names to PostgreSQL user names.
# Format:
#   MAPNAME  SYSTEM-USERNAME  PG-USERNAME
`)
}

func defaultPgHBAConf() []byte {
	return []byte(`# goopg pg_hba.conf — host-based authentication rules.
#
# Same format as upstream PostgreSQL: TYPE  DATABASE  USER  ADDRESS  METHOD
# The first matching rule wins. Default policy: trust loopback,
# reject everything else.

# TYPE   DATABASE  USER   ADDRESS         METHOD
local    all       all                    trust
host     all       all    127.0.0.1/32    trust
host     all       all    ::1/128         trust
host     all       all    0.0.0.0/0       reject
host     all       all    ::/0            reject
`)
}
