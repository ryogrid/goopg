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

// makeBtreeRootPage creates an empty btree root page that PG can
// open without crashing. B-tree pages use pd_special for the
// BTPageOpaqueData struct. An empty root/leaf page has btpo_flags =
// BTP_LEAF | BTP_ROOT.
func makeBtreeRootPage() []byte {
	// BTPageOpaqueData: btpo_prev(4) + btpo_next(4) + btpo_level(4) +
	// btpo_flags(2) + btpo_cycleid(2) = 16 bytes
	const btreeOpaqueSize = 16
	const btpLeaf = 1
	const btpRoot = 2

	page := make([]byte, storage.BlockSize)
	// Standard page header (like InitPage)
	for i := range page {
		page[i] = 0
	}
	h := storage.MustHeader(storage.Page(page))
	h.SetLower(storage.SizeOfPageHeaderData)
	h.SetSpecial(uint16(storage.BlockSize - btreeOpaqueSize))
	h.SetUpper(uint16(storage.BlockSize - btreeOpaqueSize))
	h.SetPagesizeVersion(storage.BlockSize | 4) // pgPageLayoutVersion

	// Initialize BTPageOpaqueData at the end of the page
	le := binary.LittleEndian
	off := storage.BlockSize - btreeOpaqueSize
	le.PutUint32(page[off:off+4], 0)    // btpo_prev = P_NONE
	le.PutUint32(page[off+4:off+8], 0)  // btpo_next = P_NONE
	le.PutUint32(page[off+8:off+12], 0) // btpo_level = 0 (leaf)
	le.PutUint16(page[off+12:off+14], btpLeaf|btpRoot) // flags
	le.PutUint16(page[off+14:off+16], 0) // btpo_cycleid = 0
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
		2654, 2655, 2658, 2659, 2662, 2663, 2667, 2679, 2680, 2682,
		2684, 2685, 2687, 2688, 2690, 2691, 2692, 2693, 2701, 2703,
		2704, 3085, 3164,
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
		{3596, 3596}, // pg_seclabel
		{3764, 3764}, // pg_ts_config
		{3765, 3765}, // pg_ts_config_map
		{3766, 3766}, // pg_ts_dict
		{3767, 3767}, // pg_ts_parser
		{3768, 3768}, // pg_ts_template
		{4044, 4044}, // pg_event_trigger
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
		2654, 2655, 2658, 2659, 2662, 2663, 2667, 2679, 2680, 2682,
		2684, 2685, 2687, 2688, 2690, 2691, 2692, 2693, 2701, 2703,
		2704, 3085, 3164,
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
		2671, 2672, 2676, 2677, 2695, 3593,
		// Also copy all local critical indexes to global/
		2654, 2655, 2658, 2659, 2662, 2663, 2667, 2679, 2680, 2682,
		2684, 2685, 2687, 2688, 2690, 2691, 2692, 2693, 2701, 2703,
		2704, 3085, 3164,
	} {
		if err := os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(uint64(oid), 10)), btreePage, 0o600); err != nil {
			return err
		}
	}
	path := filepath.Join(dataDir, "global", "1262") // pg_database OID
	return os.WriteFile(path, page, 0o600)
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
