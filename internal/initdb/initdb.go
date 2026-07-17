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
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
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
const CatalogVersion = config.MajorVersion

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
	Path  string
	Build func() []byte
	Mode  os.FileMode
}

// SampleFiles returns the file list goopg init writes. The values
// are deterministic so two `goopg init` runs against fresh dirs
// produce byte-identical layouts.
func SampleFiles() []FileSpec {
	return []FileSpec{
		{Path: "PG_VERSION", Build: func() []byte { return []byte(CatalogVersion + "\n") }, Mode: 0o600},
		{Path: "postgresql.conf", Build: func() []byte { return config.SampleConfig() }, Mode: 0o600},
		{Path: "postgresql.auto.conf", Build: defaultPostgresqlAutoConf, Mode: 0o600},
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

	// SuperuserName is the name of the bootstrap superuser role
	// (pg_authid OID 10), matching upstream initdb's -U/--username
	// option. When empty, it defaults to "postgres". Names beginning
	// with "pg_" are rejected (reserved namespace), mirroring
	// initdb.c:3479.
	SuperuserName string

	// WALDir, when non-empty, relocates the write-ahead log directory
	// outside the data directory, mirroring upstream initdb's
	// -X/--waldir (initdb.c create_xlog_or_symlink). It must be an
	// absolute path — relative paths are rejected before any filesystem
	// layout, like initdb.c's "WAL directory location must be an
	// absolute path" — and must be empty or non-existent (a non-empty
	// directory is rejected). On success <DataDir>/pg_wal is created as
	// a symlink to WALDir, and pg_wal/archive_status and
	// pg_wal/summaries are created inside WALDir via that symlink. When
	// empty, pg_wal is a plain subdirectory of DataDir as before.
	WALDir string

	// NoSync, when true, skips the final recursive fsync of the data
	// directory that Init otherwise performs before returning, mirroring
	// upstream initdb's -N/--no-sync. Faster, but the cluster is not
	// guaranteed durable on disk if the host crashes immediately after
	// init (initdb.c: do_sync gated on nosync).
	NoSync bool

	// SyncOnly, when true, makes Init fsync an already-initialized data
	// directory to disk and return without creating a new cluster,
	// mirroring upstream initdb's -S/--sync-only (initdb.c:3439). The
	// DataDir must already exist and be accessible; a missing directory
	// is rejected (mirrors initdb.c's "could not access directory" via
	// pg_check_dir <= 0). NoSync is ignored when SyncOnly is set, since
	// syncing is the entire purpose of the operation.
	SyncOnly bool

	// SyncMethod selects how the data directory is flushed to disk:
	// "" or "fsync" (default, a recursive fsync of every file/dir) or
	// "syncfs" (one syncfs(2) per filesystem). Mirrors upstream initdb's
	// --sync-method (parse_sync_method, src/fe_utils/option_utils.c:90).
	// An unrecognized value is rejected before any work; "syncfs" is
	// rejected on builds without syncfs support (non-Linux), matching
	// upstream's HAVE_SYNCFS gate.
	SyncMethod string

	// NoSyncDataFiles, when true, excludes the per-database data files in
	// base/ from the fsync pass, mirroring upstream initdb's
	// --no-sync-data-files (sync_data_files=false, initdb.c:3396). Under
	// the syncfs method it has no effect, because syncfs flushes the whole
	// filesystem and goopg creates no tablespace symlinks (the only place
	// upstream's sync_data_files gate applies under syncfs).
	NoSyncDataFiles bool

	// TextSearchConfig, when non-empty, seeds
	// default_text_search_config in postgresql.conf as
	// 'pg_catalog.<value>', mirroring upstream initdb's
	// -T/--text-search-config (initdb.c:1343-1346, 3347). When empty the
	// template's commented-out default is left untouched.
	TextSearchConfig string

	// AllowGroupAccess, when true, relaxes the data directory so the
	// owner's group may read (and traverse) it, mirroring upstream
	// initdb's -g/--allow-group-access (initdb.c:3360
	// SetDataDirectoryCreatePerm(PG_DIR_MODE_GROUP)). Every directory is
	// created/left at 0o750 and every file at 0o640 (vs the default
	// 0o700/0o600), and postgresql.conf's log_file_mode is seeded to 0640
	// (initdb.c:1421-1425). When false the cluster is owner-only.
	AllowGroupAccess bool

	// ExtraGUC are name=value GUC overrides seeded into postgresql.conf,
	// mirroring upstream initdb's -c/--set (initdb.c:3266-3281,
	// 1430-1436). They are applied after TextSearchConfig so a -c switch
	// can override an earlier assignment (including
	// default_text_search_config). Each entry rewrites an existing
	// (possibly commented) assignment in place, or appends a new line.
	ExtraGUC []GUCSetting

	// AuthMethodHost and AuthMethodLocal select the authentication
	// methods written into pg_hba.conf for host and local connections,
	// mirroring upstream initdb's -A/--auth (sets both), --auth-host, and
	// --auth-local (initdb.c:3248-3264). Empty defaults to "trust" (with a
	// warning, like initdb's check_authmethod_unspecified). A single
	// --auth=ident maps the local side to "peer" and --auth=peer maps the
	// host side to "ident" (the ident↔peer cross-map). An invalid method,
	// or a password method (md5/password/scram-sha-256) on both sides
	// without PwFile, is rejected before any filesystem work.
	AuthMethodHost  string
	AuthMethodLocal string

	// PwFile, when non-empty, is a path whose first line is the cleartext
	// password for the bootstrap superuser, mirroring upstream initdb's
	// --pwfile (initdb.c get_su_pwd file branch). The password is encoded
	// into pg_authid.rolpassword as a SCRAM-SHA-256 verifier (or md5 when an
	// md5 auth method was chosen), and password_encryption is seeded to md5
	// in that case. An unreadable or empty file is rejected. The
	// interactive -W/--pwprompt form is not supported (goopg init is
	// non-interactive).
	PwFile string

	// Encoding selects the default character-set encoding for the new
	// cluster's databases, mirroring upstream initdb's -E/--encoding. The
	// name is matched case-insensitively and punctuation-insensitively
	// (so "UTF-8", "utf8", and "unicode" all select UTF8) and must name a
	// valid server-side encoding; an unknown name or a client-only encoding
	// (SJIS, BIG5, GBK, UHC, GB18030, JOHAB, SHIFT_JIS_2004) is rejected
	// before any filesystem work, with initdb's exact "is not a valid server
	// encoding name" wording. When empty, the default is UTF8 (goopg's locale
	// is fixed at C/UTF8 pending the --locale option family). The chosen
	// encoding ID is written into pg_database.encoding.
	Encoding string

	// LocaleProvider selects the default collation provider for the new
	// cluster, mirroring upstream initdb's --locale-provider (initdb.c:3367).
	// "" or "libc" use the operating-system C library (the default);
	// "builtin" uses PG's built-in C/C.UTF-8/PG_UNICODE_FAST locales; "icu"
	// is recognized but rejected ("ICU is not supported in this build")
	// because goopg is built without ICU. An unrecognized value is rejected
	// before any filesystem work. The chosen provider is written to
	// pg_database.datlocprovider.
	LocaleProvider string

	// Locale and the LC* fields mirror initdb's --locale and the per-category
	// --lc-collate/--lc-ctype/--lc-messages/--lc-monetary/--lc-numeric/--lc-time
	// (initdb.c setlocales). Locale supplies the default for any unset
	// category; each unset category ultimately falls back to "C" (goopg's
	// fixed locale — the running engine does not vary collation, so these are
	// recorded on disk for PG-compat only). For a non-libc provider, --locale
	// also supplies the per-database collation (datlocale) when no
	// provider-specific locale is given.
	Locale     string
	LCCollate  string
	LCCtype    string
	LCMessages string
	LCMonetary string
	LCNumeric  string
	LCTime     string

	// BuiltinLocale, ICULocale, and ICURules mirror initdb's --builtin-locale,
	// --icu-locale, and --icu-rules. Each is only legal with its matching
	// provider (initdb.c:3424-3434): --builtin-locale needs --locale-provider
	// builtin (valid values C / C.UTF-8 / PG_UNICODE_FAST, recorded in
	// pg_database.datlocale), and --icu-locale/--icu-rules need the icu
	// provider (always rejected here, as goopg has no ICU). A mismatched
	// combination is rejected before any filesystem work.
	BuiltinLocale string
	ICULocale     string
	ICURules      string

	// DataChecksums requests upstream initdb's -k/--data-checksums: a cluster
	// whose pg_control data_checksum_version is 1 and whose every data page
	// carries a valid pd_checksum. When true, Init stamps checksums into every
	// relation page in one offline pass after the bootstrap completes
	// (stampClusterChecksums, mirroring pg_checksums --enable); the storage
	// engine then verifies them on read (ManagerConfig.ChecksumsEnabled, set
	// from pg_control at Open). When false (the goopg default) the cluster is
	// checksum-less and the bootstrap + I/O path is byte-identical to before.
	// PG 18 defaults this ON; goopg keeps it off until recovery/replication
	// validation precedes flipping the default (M0102-0010). See
	// docs/design/0102-0019-initdb-data-checksums.md.
	DataChecksums bool

	// Registry, when non-nil, is the server's live GUC registry.
	// Its values for max_connections, max_worker_processes,
	// max_wal_senders, max_prepared_transactions,
	// max_locks_per_transaction, and wal_level are echoed into
	// global/pg_control so a standby's CheckRequiredParameterValues
	// sees the primary's resource sizing. When nil, hard-coded
	// upstream defaults are used.
	Registry *config.Registry
}

// CreatePerDatabaseScaffolding creates base/<dbOID>/ and writes
// base/<dbOID>/PG_VERSION so upstream PG ValidatePgVersion passes.
// Called for every database OID seeded in pg_database at Init time, and
// again by CREATE DATABASE (internal/server) and its WAL-replay recovery
// path (M0122-0007 physical-storage-isolation slice 2) for a newly
// allocated dboid. Idempotent — os.MkdirAll/os.WriteFile both tolerate an
// already-existing directory/file, so replaying the same CREATE DATABASE
// record twice (or re-running after a crash between mkdir and the WAL
// append that makes it durable) is always safe.
func CreatePerDatabaseScaffolding(dataDir string, dbOID uint32) error {
	dbDir := filepath.Join(dataDir, "base", strconv.FormatUint(uint64(dbOID), 10))
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return fmt.Errorf("create base/%d: %w", dbOID, err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "PG_VERSION"), []byte(CatalogVersion+"\n"), 0o600); err != nil {
		return fmt.Errorf("write base/%d/PG_VERSION: %w", dbOID, err)
	}
	// B0.3 (doc 02a §4): a CREATE DATABASE database gets the full pristine
	// bootstrap catalog image — every catalog heap + btree + pg_filenode.map
	// initdb built — instead of empty lazily-created files. Copied from
	// template0's directory (base/4), which never receives runtime writes,
	// NOT from the named template: goopg clones template user tables under
	// fresh OIDs (copyTemplateTables), so the named template's pg_class heap
	// would carry rows for relations that don't exist in the new database.
	// (PG copies everything from the named template because its clones keep
	// their OIDs — a documented goopg deviation.) The three system databases
	// are populated by initdb itself and skip this.
	switch dbOID {
	case catalog.DefaultDBOid, template0DbOid, catalog.PostgresDBOid:
		return nil
	}
	return copyBootstrapCatalogImage(dataDir, dbOID)
}

// template0DbOid is template0's pg_database OID (initdb seeds 1=template1,
// 4=template0, 5=postgres).
const template0DbOid uint32 = 4

// copyBootstrapCatalogImage copies every regular file from base/4
// (template0's pristine bootstrap catalog image) into base/<dbOID>/,
// skipping files that already exist — which makes it idempotent for the
// two callers that share it via CreatePerDatabaseScaffolding: the CREATE
// DATABASE server path and its WAL-replay recovery (a replay over a live
// database directory must never clobber post-create catalog writes).
// Each file is written via write-temp + fsync + rename so a crash cannot
// leave a torn catalog file that a later replay would skip as "exists".
// A missing base/4 (a pre-B0.3 data dir) is a silent no-op — such
// clusters keep the historical lazy-file behavior.
func copyBootstrapCatalogImage(dataDir string, dbOID uint32) error {
	srcDir := filepath.Join(dataDir, "base", strconv.FormatUint(uint64(template0DbOid), 10))
	dstDir := filepath.Join(dataDir, "base", strconv.FormatUint(uint64(dbOID), 10))
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read template0 image: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "PG_VERSION" {
			continue
		}
		dst := filepath.Join(dstDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // already present — replay over a live dir
		}
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return fmt.Errorf("read template0 %s: %w", e.Name(), err)
		}
		tmp := dst + ".tmp"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("create %s: %w", tmp, err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("fsync %s: %w", tmp, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return fmt.Errorf("rename %s: %w", tmp, err)
		}
	}
	if d, err := os.Open(dstDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// RemovePerDatabaseScaffolding removes base/<dbOID>/ (the symmetric
// counterpart to CreatePerDatabaseScaffolding), called by DROP DATABASE
// (internal/server) and its WAL-replay recovery path once the drop is
// durable (M0122-0007 physical-storage-isolation slice 3). A missing
// directory is not an error — os.RemoveAll is a no-op in that case, which
// matters for replay: a crash between the removal and the DROP DATABASE
// WAL record becoming durable must not turn a replay into a hard failure.
func RemovePerDatabaseScaffolding(dataDir string, dbOID uint32) error {
	dbDir := filepath.Join(dataDir, "base", strconv.FormatUint(uint64(dbOID), 10))
	if err := os.RemoveAll(dbDir); err != nil {
		return fmt.Errorf("remove base/%d: %w", dbOID, err)
	}
	return nil
}

// setupWALDir relocates pg_wal outside the data directory, mirroring
// upstream initdb's -X/--waldir (initdb.c create_xlog_or_symlink). walDir
// must already be validated as an absolute path. Mirroring upstream's
// pg_check_dir switch: a non-existent directory is created, an existing
// empty one is reused (permissions fixed), and a non-empty one is
// rejected. On success <abs>/pg_wal is created as a symlink to walDir.
func setupWALDir(abs, walDir string) error {
	entries, err := os.ReadDir(walDir)
	switch {
	case err == nil:
		// Present: reuse only if empty (upstream pg_check_dir case 1).
		// Any entry — including a lost+found mount-point marker — makes
		// it non-empty and is rejected (cases 2/3/4 → exit 1).
		if len(entries) > 0 {
			return fmt.Errorf("WAL directory %q exists but is not empty", walDir)
		}
		if err := os.Chmod(walDir, 0o700); err != nil {
			return fmt.Errorf("change permissions of WAL directory %q: %w", walDir, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Not there, must create it (upstream pg_check_dir case 0).
		if err := os.MkdirAll(walDir, 0o700); err != nil {
			return fmt.Errorf("create WAL directory %q: %w", walDir, err)
		}
	default:
		return fmt.Errorf("access WAL directory %q: %w", walDir, err)
	}
	link := filepath.Join(abs, "pg_wal")
	if err := os.Symlink(walDir, link); err != nil {
		return fmt.Errorf("create symbolic link %q: %w", link, err)
	}
	return nil
}

// dataDirSyncMethod selects how syncDataDir flushes the cluster to disk,
// mirroring upstream's DataDirSyncMethod (src/include/common/file_utils.h).
type dataDirSyncMethod int

const (
	syncMethodFsync  dataDirSyncMethod = iota // recursive fsync (default)
	syncMethodSyncfs                          // one syncfs(2) per filesystem
)

// resolveSyncMethod ports parse_sync_method (src/fe_utils/option_utils.c:90):
// "" or "fsync" → FSYNC; "syncfs" → SYNCFS, but only where syncfs is
// supported (HAVE_SYNCFS / Linux), else rejected; anything else is an
// "unrecognized sync method" error.
func resolveSyncMethod(s string) (dataDirSyncMethod, error) {
	switch s {
	case "", "fsync":
		return syncMethodFsync, nil
	case "syncfs":
		if !syncfsSupported {
			return 0, fmt.Errorf("goopg init: this build does not support sync method \"syncfs\"")
		}
		return syncMethodSyncfs, nil
	default:
		return 0, fmt.Errorf("goopg init: unrecognized sync method: %s", s)
	}
}

// syncDataDir flushes dataDir to disk so the cluster is durable, mirroring
// upstream initdb's sync_pgdata (src/common/file_utils.c).
//
// FSYNC method: recursively fsync every regular file and directory under
// dataDir. When syncDataFiles is false, the base/ subtree (the per-database
// data files) is excluded, exactly as upstream sets exclude_dir =
// "<pg_data>/base" for --no-sync-data-files. The top-level walk ignores
// symlinks, so an external WAL directory linked at <dataDir>/pg_wal is
// fsynced separately by recursing through that symlink. goopg init creates
// no tablespace symlinks under pg_tblspc, so (unlike upstream) there is no
// second process_symlinks pass for pg_tblspc.
//
// SYNCFS method: a single syncfs(2) on dataDir flushes its whole filesystem,
// plus a syncfs of a relocated pg_wal symlink target. syncDataFiles has no
// effect here — upstream only uses it to gate per-tablespace syncfs calls,
// and goopg has no tablespaces.
func syncDataDir(dataDir string, method dataDirSyncMethod, syncDataFiles bool) error {
	pgWal := filepath.Join(dataDir, "pg_wal")
	walIsSymlink := false
	if info, err := os.Lstat(pgWal); err == nil && info.Mode()&os.ModeSymlink != 0 {
		walIsSymlink = true
	}

	if method == syncMethodSyncfs {
		if err := syncfsPath(dataDir); err != nil {
			return err
		}
		if walIsSymlink {
			if err := syncfsPath(pgWal); err != nil {
				return err
			}
		}
		return nil
	}

	// FSYNC method.
	excludeDir := ""
	if !syncDataFiles {
		excludeDir = filepath.Join(dataDir, "base")
	}
	if err := walkAndFsync(dataDir, false, excludeDir); err != nil {
		return err
	}
	if walIsSymlink {
		if err := walkAndFsync(pgWal, true, ""); err != nil {
			return err
		}
	}
	return nil
}

// walkAndFsync recursively applies fsync to path and everything beneath it,
// mirroring upstream initdb's walkdir (src/common/file_utils.c): each regular
// file and directory is fsynced, and the directory itself is fsynced after
// its children. Symlinks are ignored except at the top level when
// followTopSymlink is true (used to descend into a relocated pg_wal). Symlinks
// encountered in subdirectories are always ignored, matching upstream's
// intentional choice not to propagate process_symlinks into recursive calls.
//
// excludeDir, when non-empty, is a directory path that is skipped entirely
// (neither descended nor fsynced), porting walkdir's
// `if (exclude_dir && strcmp(exclude_dir, path) == 0) return;`. It is used
// for --no-sync-data-files (excludeDir = <dataDir>/base) and is propagated
// into recursive calls exactly as upstream does, though it only ever matches
// the top-level base/ directory.
func walkAndFsync(path string, followTopSymlink bool, excludeDir string) error {
	if excludeDir != "" && path == excludeDir {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if !followTopSymlink {
			return nil
		}
		// Resolve the symlink target so we recurse into the real dir/file.
		if info, err = os.Stat(path); err != nil {
			return err
		}
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			child := filepath.Join(path, e.Name())
			// Subdirectory recursion never follows symlinks (upstream
			// passes process_symlinks=false to nested walkdir calls).
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			if e.IsDir() {
				if err := walkAndFsync(child, false, excludeDir); err != nil {
					return err
				}
			} else if e.Type().IsRegular() {
				if err := fsyncPath(child, false); err != nil {
					return err
				}
			}
		}
	}
	return fsyncPath(path, info.IsDir())
}

// fsyncPath opens path read-only and fsyncs it, mirroring upstream initdb's
// fsync_fname_ext (src/common/file_utils.c). Benign errors are tolerated the
// same way upstream does: EACCES on open (and EISDIR for directories), and
// EBADF/EINVAL on the fsync itself for directories — some filesystems reject
// fsync of a directory opened O_RDONLY, and that is not a durability failure.
func fsyncPath(path string, isDir bool) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || (isDir && errors.Is(err, syscall.EISDIR)) {
			return nil
		}
		return fmt.Errorf("open %q for fsync: %w", path, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		if isDir && (errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.EINVAL)) {
			return nil
		}
		return fmt.Errorf("fsync %q: %w", path, err)
	}
	return nil
}

// relaxToGroupAccess relaxes every directory under dataDir to 0o750
// (PG_DIR_MODE_GROUP) and every regular file to 0o640 (PG_FILE_MODE_GROUP),
// mirroring the net on-disk effect of upstream initdb's
// SetDataDirectoryCreatePerm(PG_DIR_MODE_GROUP) (src/common/file_perm.c,
// initdb.c:3360). Upstream creates each file/dir at the group mode from the
// start via the pg_dir_create_mode / pg_file_create_mode globals; goopg lays
// the tree out at owner mode (0o700/0o600) and relaxes it here in one pass,
// producing an identical final tree — the invariant that 001_initdb.pl's
// check_mode_recursive($datadir, 0750, 0640) validates.
//
// Like fsyncDataDir, the top-level walk ignores symlinks, so a relocated
// pg_wal (-X/--waldir) is descended through separately to relax its external
// target and contents too (check_mode_recursive follows symlinks with
// follow_fast, so the WAL directory must satisfy the same modes).
func relaxToGroupAccess(dataDir string) error {
	if err := chmodTreeGroup(dataDir, false); err != nil {
		return err
	}
	pgWal := filepath.Join(dataDir, "pg_wal")
	if info, err := os.Lstat(pgWal); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := chmodTreeGroup(pgWal, true); err != nil {
			return err
		}
	}
	return nil
}

// chmodTreeGroup recursively chmods path and everything beneath it: every
// directory to 0o750 and every regular file to 0o640. Symlinks are ignored
// except at the top level when followTopSymlink is true (used to descend into
// a relocated pg_wal), exactly mirroring walkAndFsync's traversal so the two
// passes agree on which entries they touch.
func chmodTreeGroup(path string, followTopSymlink bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if !followTopSymlink {
			return nil
		}
		if info, err = os.Stat(path); err != nil {
			return err
		}
	}
	if info.IsDir() {
		if err := os.Chmod(path, 0o750); err != nil {
			return fmt.Errorf("chmod dir %q: %w", path, err)
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			// Nested symlinks are never followed (matches walkAndFsync).
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			if err := chmodTreeGroup(filepath.Join(path, e.Name()), false); err != nil {
				return err
			}
		}
		return nil
	}
	if info.Mode().IsRegular() {
		if err := os.Chmod(path, 0o640); err != nil {
			return fmt.Errorf("chmod file %q: %w", path, err)
		}
	}
	return nil
}

func Init(opts Options) error {
	if opts.DataDir == "" {
		return errors.New("goopg init: -D <data-directory> is required")
	}
	abs, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return fmt.Errorf("goopg init: resolve %q: %w", opts.DataDir, err)
	}
	// Validate the sync method up front (mirrors parse_sync_method, which
	// initdb runs during option parsing) so both the sync-only and full-init
	// paths reject a bad value before touching the filesystem.
	syncMethod, err := resolveSyncMethod(opts.SyncMethod)
	if err != nil {
		return err
	}
	// Sync-only mode (upstream initdb -S/--sync-only, initdb.c:3439):
	// fsync an already-initialized data directory to disk and exit
	// without creating a new cluster. The directory must already exist
	// and be accessible; a missing directory is rejected, mirroring
	// initdb.c's pg_check_dir(pg_data) <= 0 → "could not access
	// directory". This branch runs before any layout/validation so it
	// never mutates the tree.
	if opts.SyncOnly {
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("goopg init: could not access directory %q", abs)
		}
		if err := syncDataDir(abs, syncMethod, !opts.NoSyncDataFiles); err != nil {
			return fmt.Errorf("goopg init: sync data to disk: %w", err)
		}
		return nil
	}
	// Resolve the bootstrap superuser name (upstream initdb -U/--username).
	// Default "postgres"; reject the reserved "pg_" prefix exactly as
	// initdb.c:3479 does, before touching the filesystem.
	superuser := opts.SuperuserName
	if superuser == "" {
		superuser = "postgres"
	}
	if strings.HasPrefix(superuser, "pg_") {
		return fmt.Errorf("goopg init: superuser name %q is disallowed; role names cannot begin with \"pg_\"", superuser)
	}
	// Resolve + validate the default database encoding up front (upstream
	// initdb -E/--encoding via get_encoding_id, initdb.c:846) so an unknown
	// or client-only encoding name aborts before any filesystem work — and
	// before the trust-default auth warning is printed.
	encodingID, err := resolveEncoding(opts.Encoding)
	if err != nil {
		return err
	}
	// Resolve + validate the locale provider and lc_* settings up front
	// (upstream initdb --locale-provider / --locale / --lc-* / --builtin-locale
	// / --icu-* via setlocales + setup_encoding). A bad provider, an illegal
	// option combination, or an encoding/locale mismatch aborts before any
	// filesystem work. The result feeds pg_database (datlocprovider /
	// datcollate / datctype / datlocale) and the lc_* GUC seeding below.
	locale, err := resolveLocale(opts, encodingID)
	if err != nil {
		return err
	}
	// Resolve + validate the authentication methods up front (upstream
	// initdb -A/--auth, --auth-host, --auth-local) before any filesystem
	// work, so a bad method or a password method without --pwfile aborts
	// before the tree is created. Then read --pwfile (if any) and encode the
	// superuser's rolpassword verifier; both are needed below when writing
	// pg_hba.conf, postgresql.conf, and pg_authid.
	authHost, authLocal, authWarn, err := resolveAuthMethods(opts.AuthMethodHost, opts.AuthMethodLocal, opts.PwFile != "")
	if err != nil {
		return err
	}
	var rolpassword, passwordEncryption string
	if opts.PwFile != "" {
		pwd, perr := readSuperuserPasswordFile(opts.PwFile)
		if perr != nil {
			return perr
		}
		rolpassword, passwordEncryption, perr = encodeSuperuserPassword(pwd, authHost, authLocal, superuser)
		if perr != nil {
			return perr
		}
	}
	if authWarn {
		// Mirrors initdb's trust-default warning (initdb.c authwarning,
		// 3518). Non-fatal; the cluster is created with trust auth.
		log.Printf("goopg init: enabling \"trust\" authentication for local connections; "+
			"you can change this by editing pg_hba.conf or using -A, --auth-local, or --auth-host (data dir %q)", abs)
	}
	// Resolve the optional external WAL directory (upstream initdb
	// -X/--waldir). It must be an absolute path; reject a relative path
	// before any filesystem layout happens, mirroring initdb.c's "WAL
	// directory location must be an absolute path" (initdb.c:2961).
	walDir := opts.WALDir
	if walDir != "" && !filepath.IsAbs(walDir) {
		return fmt.Errorf("goopg init: WAL directory location must be an absolute path: %q", walDir)
	}
	if err := ensureEmptyDir(abs); err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("goopg init: create %q: %w", abs, err)
	}
	// When an external WAL directory is requested, set up <abs>/pg_wal as
	// a symlink to it before the subdir loop. The loop then skips the
	// literal "pg_wal" entry but still creates pg_wal/archive_status and
	// pg_wal/summaries, which land inside walDir through the symlink.
	if walDir != "" {
		if err := setupWALDir(abs, walDir); err != nil {
			return fmt.Errorf("goopg init: %w", err)
		}
	}
	for _, sub := range Subdirs {
		if sub == "pg_wal" && walDir != "" {
			continue // already created as a symlink to the external WAL dir
		}
		path := filepath.Join(abs, sub)
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("goopg init: mkdir %q: %w", path, err)
		}
	}
	// Per-database directories for the three system databases seeded in
	// pg_database (OID 1 = template1, OID 4 = template0, OID 5 = postgres).
	// Each needs base/<dboid>/ and base/<dboid>/PG_VERSION so PG's
	// ValidatePgVersion passes at standby startup.
	for _, dbOID := range []uint32{1, 4, 5} {
		if err := CreatePerDatabaseScaffolding(abs, dbOID); err != nil {
			return fmt.Errorf("goopg init: %w", err)
		}
	}
	for _, f := range SampleFiles() {
		path := filepath.Join(abs, f.Path)
		if err := os.WriteFile(path, f.Build(), f.Mode); err != nil {
			return fmt.Errorf("goopg init: write %q: %w", path, err)
		}
	}
	// Overwrite pg_hba.conf with the resolved auth methods (upstream
	// initdb's @authmethodhost@/@authmethodlocal@ token replacement in
	// setup_config). SampleFiles wrote the trust default above; this
	// substitutes the requested methods (a no-op when both are trust).
	hbaPath := filepath.Join(abs, "pg_hba.conf")
	if err := os.WriteFile(hbaPath, buildPgHBAConf(authHost, authLocal), 0o600); err != nil {
		return fmt.Errorf("goopg init: write %q: %w", hbaPath, err)
	}
	// Seed any --text-search-config / --set GUC overrides into the
	// just-written postgresql.conf, mirroring upstream initdb's
	// setup_config (initdb.c). password_encryption is seeded to md5 when an
	// md5 auth method was chosen (initdb.c:1402-1413). No-op when no
	// relevant option was given.
	if err := seedPostgresqlConf(abs, opts.TextSearchConfig, passwordEncryption, opts.AllowGroupAccess, locale.localeGUCSettings(), opts.ExtraGUC); err != nil {
		return fmt.Errorf("goopg init: seed postgresql.conf: %w", err)
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
	// Overwrite pg_authid placeholder with a minimal superuser row for
	// `superuser` (default "postgres") plus (if distinct) an OS-user role
	// at OID 16384.
	pgAuthidEntries, err := bootstrapPostgresRoleWithPassword(abs, superuser, rolpassword)
	if err != nil {
		return fmt.Errorf("goopg init: postgres role: %w", err)
	}
	if err := bootstrapPostgresDatabase(abs, encodingID, locale); err != nil {
		return fmt.Errorf("goopg init: postgres database: %w", err)
	}
	// M0106-0010 step 3cs: overwrite the empty btree placeholder at
	// global/2672 with a populated 2-page btree carrying oid-keyed
	// IndexTuples for the two pg_database heap rows just written
	// (template1 OID 1, postgres OID 5). Without populated entries
	// PG's CheckMyDatabase (postinit.c:335) FATALs with
	// `cache lookup failed for database 5` because the syscache
	// DATABASEOID lookup via pg_database_oid_index returns NULL.
	// This is the next blocker after step 3cr made reltablespace=1664
	// route shared catalogs to global/<relfilenode>.
	if err := bootstrapPgDatabaseOidIndex(abs); err != nil {
		return fmt.Errorf("goopg init: pg_database_oid_index: %w", err)
	}
	// M0106-0010 step 3dh: overwrite the empty btree placeholder at
	// global/2671 with a populated 2-page btree carrying name-keyed
	// IndexTuples for the same two pg_database heap rows. Without
	// populated entries PG's InitPostgres → get_db_info() FATALs with
	// `3D000: database "postgres" does not exist` because the syscache
	// DATABASENAME lookup via pg_database_datname_index returns NULL.
	// Surfaced by TestE2E_FailoverGoopgToPG/async after step 3dg fixed
	// the SEGV in pg_authid_rolname_index.
	if err := bootstrapPgDatabaseDatnameIndex(abs); err != nil {
		return fmt.Errorf("goopg init: pg_database_datname_index: %w", err)
	}
	// M0106-0010 step 3cx: overwrite the empty btree placeholders at
	// global/2676 (pg_authid_rolname_index) and global/2677
	// (pg_authid_oid_index) with populated 2-page btrees so PG's
	// InitializeSessionUserId → SearchSysCache1(AUTHNAME, "<os-user>") and
	// SearchSysCache1(AUTHOID, oid) lookups succeed against the heap rows
	// just written by bootstrapPostgresRole. Without these, the next FATAL
	// during standby boot is `28000: role "<os-user>" does not exist`.
	if err := bootstrapPgAuthidIndexes(abs, pgAuthidEntries); err != nil {
		return fmt.Errorf("goopg init: pg_authid indexes: %w", err)
	}
	// M0106-0010 batched-12: overwrite the empty placeholder at global/1213
	// with two pg_tablespace rows (pg_default OID 1663, pg_global OID 1664)
	// and populate the oid-index (global/2697) and spcname-index (global/2698)
	// so PG's TABLESPACEOID syscache lookup during InitPostgres finds them.
	pgTablespaceEntries, err := bootstrapPgTablespaceTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_tablespace tuples: %w", err)
	}
	if err := bootstrapPgTablespaceOidIndex(abs, pgTablespaceEntries); err != nil {
		return fmt.Errorf("goopg init: pg_tablespace_oid_index: %w", err)
	}
	if err := bootstrapPgTablespaceSpcnameIndex(abs, pgTablespaceEntries); err != nil {
		return fmt.Errorf("goopg init: pg_tablespace_spcname_index: %w", err)
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
	// M0106-0010 step 3cq: overwrite base/{1,5}/1247 with PG-canonical
	// FormData_pg_type heap tuples for every TypeOID a nailedRel attr
	// references. bootstrapSystemCatalogs above writes v0-encoded
	// pg_type rows for the goopg planner; PG18's TupleDescInitEntry
	// reads typalign at struct offset 128 after Form_pg_type cast and
	// FATALs on `\0` if the heap is v0-encoded. Must run before any
	// pg_type-index bootstrap (none currently exist) so the index TIDs
	// would point at the canonical heap rows.
	pgTypeTIDs, err := bootstrapPgTypeTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_type tuples: %w", err)
	}
	// M0106-0010 step 3cz: overwrite the empty btree placeholder at
	// base/{1,5}/2703 with a populated 2-page btree (metapage +
	// leaf-root) carrying one oid-keyed IndexTuple per pg_type heap
	// row so PG's TupleDescInitEntry → typeidType →
	// SearchSysCache1(TYPEOID, ObjectIdGetDatum(oid)) finds the
	// type row (e.g. int4 OID=23). Without this the first client
	// backend FATALs with `XX000: cache lookup failed for type 23`
	// at tupdesc.c:896 on the standby's first query.
	if err := bootstrapPgTypeOidIndex(abs, pgTypeTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_type_oid_index: %w", err)
	}
	// B2.1a: populate pg_type_typname_nsp_index (2704) so PG's
	// LookupTypeName → SearchSysCache2(TYPENAMENSP, ...) resolves builtin
	// AND runtime types by name. Left as an empty placeholder before, every
	// named cast on a real PG standby (`SELECT 1::int4`, `::text`) failed
	// with `type "..." does not exist`.
	if err := bootstrapPgTypeTypnameNspIndex(abs, pgTypeTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_type_typname_nsp_index: %w", err)
	}
	// M0106-0010 step 2: write pg_am rows so PG's
	// RelationInitIndexAccessInfo → SearchSysCache1(AMOID, ...) does
	// not return NULL and PANIC when opening a critical index.
	if err := bootstrapPgAmTuples(abs); err != nil {
		return fmt.Errorf("goopg init: pg_am tuples: %w", err)
	}
	// Seed pg_namespace (OIDs 11/99/2200) + indexes so PG's NAMESPACENAME
	// and NAMESPACEOID syscache lookups find pg_catalog. Without these rows,
	// schema-qualified relation lookups (SELECT … FROM pg_catalog.X) fail.
	pgNamespaceTIDs, err := bootstrapPgNamespaceTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_namespace tuples: %w", err)
	}
	if err := bootstrapPgNamespaceNspnameIndex(abs, pgNamespaceTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_namespace_nspname_index: %w", err)
	}
	if err := bootstrapPgNamespaceOidIndex(abs, pgNamespaceTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_namespace_oid_index: %w", err)
	}
	// M0106-0010 step 3a: write pg_proc rows for the AM handler
	// functions so PG's RelationInitIndexAccessInfo →
	// OidFunctionCall0(amhandler) finds bthandler /
	// heap_tableam_handler / etc. in the syscache.
	pgProcTIDs, err := bootstrapPgProcTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_proc tuples: %w", err)
	}
	// M0106-0010 step 3db: overwrite the empty btree placeholder at
	// base/{1,5}/2690 with a populated 2-page btree (metapage +
	// leaf-root) carrying one oid-keyed IndexTuple per pg_proc heap
	// row so PG's SearchSysCache1(PROCOID, ObjectIdGetDatum(oid))
	// resolves the AM handler functions (bthandler=330, etc.) via
	// the indexed path. Without populated leaf entries the lookup
	// returns NULL and an InitPostgres-stage client backend SIGSEGVs
	// when downstream code dereferences GETSTRUCT on the NULL tuple.
	if err := bootstrapPgProcOidIndex(abs, pgProcTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_proc_oid_index: %w", err)
	}
	// M0106-0010 batched-50: overwrite the empty btree placeholder at
	// base/{1,5}/2691 with a populated multi-leaf btree carrying one
	// (proname, proargtypes, pronamespace) IndexTuple per pg_proc heap
	// row. Without this, the PG-standby parse-analyse path returns no
	// candidates for any built-in function lookup (FuncnameGetCandidates
	// → SearchSysCacheList1(PROCNAMEARGSNSP, ...)) and a query like
	// `SELECT count(*) FROM <user table>` fails with 42883 "function
	// count() does not exist".
	if err := bootstrapPgProcPronameArgsNspIndex(abs, pgProcTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_proc_proname_args_nsp_index: %w", err)
	}
	// M0106-0010 batched-16: overwrite base/{1,5}/2617 with all 799
	// pg_operator rows so PG's OPEROID/OPERNAMENSP syscaches resolve
	// operator lookups during planning and expression compilation.
	pgOperatorTIDs, err := bootstrapPgOperatorTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_operator tuples: %w", err)
	}
	if err := bootstrapPgOperatorOidIndex(abs, pgOperatorTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_operator_oid_index: %w", err)
	}
	if err := bootstrapPgOperatorOprnameIndex(abs, pgOperatorTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_operator_oprname_l_r_n_index: %w", err)
	}
	// M0106-0010 batched-19: write all 177 pg_opfamily rows so PG's
	// OPFAMILYOID / OPFAMILYAMNAMENSP syscaches resolve family lookups.
	pgOpfamilyTIDs, err := bootstrapPgOpfamilyTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_opfamily tuples: %w", err)
	}
	// M0106-0010 step 3b / batched-19: write all 177 pg_opclass rows so PG's
	// RelationInitIndexAccessInfo → SearchSysCache1(CLAOID, ...)
	// resolves every opclass referenced by a nailed index's
	// indclass vector.
	pgOpclassTIDs, err := bootstrapPgOpclassTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_opclass tuples: %w", err)
	}
	// M0106-0010 step 3c: write pg_amop strategy operator rows
	// (queried at planning time via AMOPSTRATEGY/AMOPOPID) and
	// pg_amproc support function rows (load-bearing — scanned by
	// LookupOpclassInfo during RelationInitIndexAccessInfo).
	if err := bootstrapPgAmopTuples(abs); err != nil {
		return fmt.Errorf("goopg init: pg_amop tuples: %w", err)
	}
	pgAmprocTIDs, err := bootstrapPgAmprocTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_amproc tuples: %w", err)
	}
	// M0106-0010 step 3cw: overwrite the empty btree placeholder at
	// base/{1,5}/2655 + global/2655 with a populated 2-page btree
	// (metapage + leaf-root) carrying one (family, lefttype, righttype,
	// num)-keyed IndexTuple per pg_amproc heap row so PG's
	// IndexSupportInitialize → sysscan(pg_amproc_fam_proc_index) finds
	// the cmp/sortsupport/equalimage rows. Without this the next FATAL
	// during standby boot is "missing support function 1 for attribute
	// 1 of index pg_authid_rolname_index".
	if err := bootstrapPgAmprocFamProcIndex(abs, pgAmprocTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_amproc_fam_proc_index: %w", err)
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
	// M0106-0010 batched-19: overwrite base/{1,5}/2687 + global/2687 with
	// a populated btree carrying one oid-keyed IndexTuple per pg_opclass row
	// (177 rows, potentially multi-page) so PG's LookupOpclassInfo finds
	// every opclass via pg_opclass_oid_index.
	if err := bootstrapPgOpclassOidIndex(abs, pgOpclassTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_opclass_oid_index: %w", err)
	}
	// M0106-0010 batched-19: seed pg_opfamily_oid_index (2755) and
	// pg_opfamily_am_name_nsp_index (2754) so PG's OPFAMILYOID and
	// OPFAMILYAMNAMENSP syscache lookups resolve family entries.
	if err := bootstrapPgOpfamilyOidIndex(abs, pgOpfamilyTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_opfamily_oid_index: %w", err)
	}
	if err := bootstrapPgOpfamilyAmNameNspIndex(abs, pgOpfamilyTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_opfamily_am_name_nsp_index: %w", err)
	}
	// M0106-0010 batched-20: write all 235 pg_cast rows so PG's
	// CASTSOURCETARGET syscache resolves cast lookups during expression
	// compilation (implicit/assignment casts in parser/optimizer).
	pgCastTIDs, err := bootstrapPgCastTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_cast tuples: %w", err)
	}
	if err := bootstrapPgCastOidIndex(abs, pgCastTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_cast_oid_index: %w", err)
	}
	if err := bootstrapPgCastSourceTargetIndex(abs, pgCastTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_cast_source_target_index: %w", err)
	}
	// M0106-0010 batched-21: write all 7 pg_collation rows so PG's
	// COLLNAMEENCNSP and COLLOID syscaches resolve collation lookups
	// during expression compilation and text comparison.
	pgCollationTIDs, err := bootstrapPgCollationTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_collation tuples: %w", err)
	}
	if err := bootstrapPgCollationOidIndex(abs, pgCollationTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_collation_oid_index: %w", err)
	}
	if err := bootstrapPgCollationNameEncNspIndex(abs, pgCollationTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_collation_name_enc_nsp_index: %w", err)
	}
	// M0106-0010 batched-22: write all 128 pg_conversion rows so PG's
	// CONDEFAULT, CONNAMENSP, and CONVOID syscaches resolve conversion
	// lookups (FindDefaultConversion / FindConversion paths).
	pgConversionTIDs, err := bootstrapPgConversionTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_conversion tuples: %w", err)
	}
	if err := bootstrapPgConversionOidIndex(abs, pgConversionTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_conversion_oid_index: %w", err)
	}
	if err := bootstrapPgConversionNameNspIndex(abs, pgConversionTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_conversion_name_nsp_index: %w", err)
	}
	if err := bootstrapPgConversionDefaultIndex(abs, pgConversionTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_conversion_default_index: %w", err)
	}
	// M0106-0010 batched-23: write all 161 pg_aggregate rows and the
	// populated pg_aggregate_fnoid_index (OID 2650) so PG's AGGFNOID
	// syscache resolves aggregate lookups (e.g. during function parsing
	// of calls like SUM, AVG, COUNT). The aggfnoid column (regproc) is
	// the primary key — no separate oid column exists in pg_aggregate.
	pgAggregateTIDs, err := bootstrapPgAggregateTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_aggregate tuples: %w", err)
	}
	if err := bootstrapPgAggregateFnoidIndex(abs, pgAggregateTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_aggregate_fnoid_index: %w", err)
	}
	// M0106-0010 batched-24: write all 6 pg_range rows to base/{1,5}/3541
	// and populate pg_range_rngtypid_index (3542, PKEY) and
	// pg_range_rngmultitypid_index (2228) so PG's RANGETYPE /
	// RANGEMULTIRANGE syscaches resolve range-type lookups. pg_range has
	// no 'oid' column; rngtypid is the natural key.
	pgRangeTIDs, err := bootstrapPgRangeTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_range tuples: %w", err)
	}
	if err := bootstrapPgRangeRngtypidIndex(abs, pgRangeTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_range_rngtypid_index: %w", err)
	}
	if err := bootstrapPgRangeRngmultitypidIndex(abs, pgRangeTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_range_rngmultitypid_index: %w", err)
	}
	// M0106-0010 batched-25: write the 3 pg_language rows (internal/c/sql)
	// to base/{1,5}/2612 so PG's LANGNAME and LANGOID syscaches resolve
	// language OID lookups during function compilation and handler dispatch.
	pgLanguageTIDs, err := bootstrapPgLanguageTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_language tuples: %w", err)
	}
	if err := bootstrapPgLanguageOidIndex(abs, pgLanguageTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_language_oid_index: %w", err)
	}
	if err := bootstrapPgLanguageNameIndex(abs, pgLanguageTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_language_name_index: %w", err)
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
	if err := bootstrapPgClassRelnameNspIndex(abs, pgClassTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_class_relname_nsp_index: %w", err)
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
	// M0106-0010 step 3dm phase B: seed pg_rewrite heap + leaf indices.
	// Step 3dl seeded pg_stat_wal_receiver into pg_class with
	// relhasrules=true, which tells PG's relcache to fetch the
	// ON-SELECT rule via RewriteRelRulenameIndexId. Without a real
	// Form_pg_rewrite row and the two leaf btrees the next FATAL is
	// "cache lookup failed for rule" on first open of the view.
	pgRewriteTIDs, err := bootstrapPgRewriteTuples(abs)
	if err != nil {
		return fmt.Errorf("goopg init: pg_rewrite tuples: %w", err)
	}
	if err := bootstrapPgRewriteOidIndex(abs, pgRewriteTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_rewrite_oid_index: %w", err)
	}
	if err := bootstrapPgRewriteRelRulenameIndex(abs, pgRewriteTIDs); err != nil {
		return fmt.Errorf("goopg init: pg_rewrite_rel_rulename_index: %w", err)
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
	// Write the bootstrap WAL segment (pg_wal/000000010000000000000001)
	// BEFORE pg_control so the ordering invariant matches BootStrapXLOG
	// (xlog.c:5175-5219): WAL flushed → pg_control written.
	// M0106-0010 batched-04.
	if err := WriteBootstrapWAL(abs, sysID, time.Now()); err != nil {
		return fmt.Errorf("goopg init: bootstrap wal: %w", err)
	}
	// Write the PG-compatible pg_control file so pg_controldata,
	// pg_checksums, and other client tools can inspect the cluster
	// (M0095-0001).
	if err := writePgControl(abs, sysID, opts.Registry, opts.DataChecksums); err != nil {
		return fmt.Errorf("goopg init: pg_control: %w", err)
	}
	// Data-page checksums (upstream initdb -k/--data-checksums): pg_control's
	// data_checksum_version=1 (written just above) is a promise that every
	// data page on disk carries a valid pd_checksum. The bootstrap wrote all
	// relation files without checksums; stamp them now in one offline pass
	// over base/ and global/ (mirroring pg_checksums --enable), after every
	// relation file — including the base/1 → base/5 / template0 copies and
	// the mapped-local-catalog heaps — is final, and before the trailing fsync
	// so the checksummed bytes are flushed durably. A version-0 cluster (the
	// default) skips this entirely and is byte-identical to before.
	if opts.DataChecksums {
		if err := stampClusterChecksums(abs); err != nil {
			return fmt.Errorf("goopg init: data-page checksums: %w", err)
		}
	}
	// Relax the whole tree to group-readable mode when -g/--allow-group-access
	// was given (upstream SetDataDirectoryCreatePerm(PG_DIR_MODE_GROUP)). Done
	// after the full layout but before the fsync below, so the relaxed modes
	// are flushed durably (upstream creates at the group mode from the start;
	// the net on-disk result is identical — every dir 0o750, every file 0o640).
	if opts.AllowGroupAccess {
		if err := relaxToGroupAccess(abs); err != nil {
			return fmt.Errorf("goopg init: allow group access: %w", err)
		}
	}
	// Flush the freshly written cluster to disk so it survives a host
	// crash, mirroring upstream initdb's default trailing
	// sync_pgdata (initdb.c:3512). Skipped when NoSync is set
	// (-N/--no-sync), which trades durability for speed.
	if !opts.NoSync {
		if err := syncDataDir(abs, syncMethod, !opts.NoSyncDataFiles); err != nil {
			return fmt.Errorf("goopg init: sync data to disk: %w", err)
		}
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
		2964, // pg_db_role_setting (M0106-0010 Step 3cu)
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
// mappedLocalCatalogPlaceholderOIDs returns the local-catalog relfilenodes
// whose heap files are written as 8 KiB empty placeholders by
// bootstrapMappedLocalCatalogHeaps. The list intentionally OMITS catalogs
// that have a dedicated populating bootstrapper — overwriting their heap
// with an empty page would wipe out the seeded rows.
//
// Dedicated bootstrappers exist for the following local-catalog OIDs and
// MUST NOT appear here (regression: M0106-0010 batched-52 surfaced
// `cache lookup failed for aggregate 2803` after 2600 was clobbered to
// an empty page):
//
//	1247 pg_type            (pg_type_bootstrap.go)
//	1249 pg_attribute       (bootstrapPgAttributeTuples)
//	1255 pg_proc            (bootstrapPgProcTuples)
//	1259 pg_class           (bootstrapPgClassTuples)
//	2600 pg_aggregate       (bootstrapPgAggregateTuples)
//	2601 pg_am              (bootstrapPgAmTuples)
//	2602 pg_amop            (bootstrapPgAmopTuples)
//	2603 pg_amproc          (bootstrapPgAmprocTuples)
//	2605 pg_cast            (bootstrapPgCastTuples)
//	2610 pg_index           (bootstrapPgIndexTuples)
//	2612 pg_language        (bootstrapPgLanguageTuples)
//	2615 pg_namespace       (bootstrapPgNamespaceTuples)
//	2616 pg_opclass         (bootstrapPgOpclassTuples)
//	2617 pg_operator        (bootstrapPgOperatorTuples)
//	2618 pg_rewrite         (bootstrapPgRewriteTuples)
//	2607 pg_conversion      (bootstrapPgConversionTuples)
//	2753 pg_opfamily        (bootstrapPgOpfamilyTuples)
//	3456 pg_collation       (bootstrapPgCollationTuples)
//	3541 pg_range           (bootstrapPgRangeTuples)
func mappedLocalCatalogPlaceholderOIDs() []uint32 {
	return []uint32{
		826,  // pg_default_acl (M0106-0010 step 3ak)
		2604, // pg_attrdef
		2606, // pg_constraint
		2608, // pg_depend
		2609, // pg_description
		2611, // pg_inherits
		2613, // pg_largeobject
		2614, // pg_largeobject_metadata
		2619, // pg_statistic
		2620, // pg_trigger
		3381, // pg_statistic_ext
		3501, // pg_enum (M0106-0010 step 3an)
		3596, // pg_seclabel
		3602, // pg_ts_config (M0106-0010 step 3ck)
		3764, // pg_ts_config (stale — true pg_ts_config OID is 3602)
		3603, // pg_ts_config_map (M0106-0010 step 3cj)
		3765, // pg_ts_config_map (stale — true pg_ts_config_map OID is 3603)
		3600, // pg_ts_dict (M0106-0010 step 3cm)
		3766, // pg_ts_template_tmplname_index — overwritten with btree root below
		3601, // pg_ts_parser (M0106-0010 step 3cn)
		3767, // pg_ts_template_oid_index — overwritten with btree root below
		3764, // pg_ts_template (M0106-0010 step 3co)
		3768, // pg_ts_template (stale — true pg_ts_template OID is 3764)
		3466, // pg_event_trigger (M0106-0010 step 3ar)
		3079, // pg_extension (M0106-0010 step 3aw)
		2328, // pg_foreign_data_wrapper (M0106-0010 step 3bb)
		1417, // pg_foreign_server (M0106-0010 step 3be)
		1418, // pg_user_mapping (M0106-0010 step 3cp)
		3118, // pg_foreign_table (M0106-0010 step 3bh)
		3350, // pg_partitioned_table (M0106-0010 step 3bs)
		6104, // pg_publication (M0106-0010 step 3bu)
		6237, // pg_publication_namespace (M0106-0010 step 3bx)
		6106, // pg_publication_rel (M0106-0010 step 3by)
		2224, // pg_sequence (M0106-0010 step 3cb)
		3429, // pg_statistic_ext_data (M0106-0010 step 3cc)
		3576, // pg_transform (M0106-0010 step 3ci)
		6003, // pg_publication (stale — no upstream catalog at OID 6003)
		6101, // pg_publication_rel
		6102, // pg_subscription_rel
		6137, // pg_transform (stale — true pg_transform OID is 3576)
		6245, // pg_statistic_ext_data
		9400, // pg_db_role_setting
	}
}

func bootstrapMappedLocalCatalogHeaps(dataDir string) error {
	heapPage := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(heapPage); err != nil {
		return err
	}
	oids := mappedLocalCatalogPlaceholderOIDs()
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
func bootstrapPostgresRole(dataDir, superuser string) ([]pgAuthidEntry, error) {
	return bootstrapPostgresRoleWithPassword(dataDir, superuser, "")
}

// bootstrapPostgresRoleWithPassword is bootstrapPostgresRole with an optional
// pre-encoded rolpassword verifier for the bootstrap superuser (OID 10),
// supplied by --pwfile (upstream initdb's setup_auth ALTER USER … PASSWORD).
// An empty rolpassword leaves the row's password empty (the no-password
// default). The verifier is a non-NULL text value, so the bootstrap row keeps
// HEAP_HASNULL clear and its t_hoff is unchanged.
func bootstrapPostgresRoleWithPassword(dataDir, superuser, rolpassword string) ([]pgAuthidEntry, error) {
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
	// Bootstrap superuser row (OID 10) and optional OS-user row (OID 16384).
	// rolpassword/rolvaliduntil are written as empty-string/epoch for the
	// bootstrap rows to keep HEAP_HASNULL clear (no null bitmap, t_hoff=24).
	// The OID-10 superuser carries the --pwfile verifier (when supplied);
	// it remains a non-NULL text value, so HEAP_HASNULL stays clear.
	buildBootstrapRow := func(oid int64, rolname string) executor.Row {
		pw := ""
		if oid == 10 {
			pw = rolpassword
		}
		return executor.Row{
			executor.NewIntDatum(oid),                    // oid
			executor.NewStringDatum(rolname),             // rolname
			executor.NewBoolDatum(true),                  // rolsuper
			executor.NewBoolDatum(true),                  // rolinherit
			executor.NewBoolDatum(true),                  // rolcreaterole
			executor.NewBoolDatum(false),                 // rolcreatedb
			executor.NewBoolDatum(true),                  // rolcanlogin
			executor.NewBoolDatum(true),                  // rolreplication
			executor.NewBoolDatum(true),                  // rolbypassrls
			executor.NewIntDatum(-1),                     // rolconnlimit
			executor.NewStringDatum(pw),                  // rolpassword (verifier or empty, not null)
			executor.NewTimeDatum(time.Unix(0, 0).UTC()), // rolvaliduntil (epoch, not null)
		}
	}
	// Predefined-role rows: rolsuper=false, rolinherit=true, all other
	// privilege flags false, rolconnlimit=-1, rolpassword/rolvaliduntil NULL.
	// xmin is stamped FrozenTransactionID (= 2) so the rows are permanently
	// visible without a VACUUM FREEZE pass.
	buildPredefinedRow := func(oid int64, rolname string) executor.Row {
		return executor.Row{
			executor.NewIntDatum(oid),        // oid
			executor.NewStringDatum(rolname), // rolname
			executor.NewBoolDatum(false),     // rolsuper
			executor.NewBoolDatum(true),      // rolinherit
			executor.NewBoolDatum(false),     // rolcreaterole
			executor.NewBoolDatum(false),     // rolcreatedb
			executor.NewBoolDatum(false),     // rolcanlogin
			executor.NewBoolDatum(false),     // rolreplication
			executor.NewBoolDatum(false),     // rolbypassrls
			executor.NewIntDatum(-1),         // rolconnlimit
			executor.NullDatum,               // rolpassword (NULL)
			executor.NullDatum,               // rolvaliduntil (NULL)
		}
	}

	// Roles to seed. OID 10 = BOOTSTRAP_SUPERUSERID (pinned by PG18 for the
	// canonical bootstrap "postgres" role). The OS user (if different) gets
	// a distinct OID at FirstNormalObjectId=16384 so PG's syscache lookups
	// by name and by OID find separate rows and PG's
	// `InitializeSessionUserId → SearchSysCache1(AUTHNAME, $USER)` succeeds.
	type roleSeed struct {
		oid     int64
		rolname string
	}
	seeds := []roleSeed{{oid: 10, rolname: superuser}}
	osUser := os.Getenv("USER")
	if osUser != "" && osUser != superuser {
		seeds = append(seeds, roleSeed{oid: 16384, rolname: osUser})
	}

	// 16 predefined roles from pg_authid.dat (PG18).
	predefined := []roleSeed{
		{oid: 6171, rolname: "pg_database_owner"},
		{oid: 6181, rolname: "pg_read_all_data"},
		{oid: 6182, rolname: "pg_write_all_data"},
		{oid: 3373, rolname: "pg_monitor"},
		{oid: 3374, rolname: "pg_read_all_settings"},
		{oid: 3375, rolname: "pg_read_all_stats"},
		{oid: 3377, rolname: "pg_stat_scan_tables"},
		{oid: 4569, rolname: "pg_read_server_files"},
		{oid: 4570, rolname: "pg_write_server_files"},
		{oid: 4571, rolname: "pg_execute_server_program"},
		{oid: 4200, rolname: "pg_signal_backend"},
		{oid: 4544, rolname: "pg_checkpoint"},
		{oid: 6337, rolname: "pg_maintain"},
		{oid: 4550, rolname: "pg_use_reserved_connections"},
		{oid: 6304, rolname: "pg_create_subscription"},
		{oid: 6392, rolname: "pg_signal_autovacuum_worker"},
	}

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return nil, err
	}
	entries := make([]pgAuthidEntry, 0, len(seeds)+len(predefined))

	// Bootstrap rows (no null bitmap, xmin=1).
	for _, s := range seeds {
		payload, err := executor.EncodeRowPG(cols, buildBootstrapRow(s.oid, s.rolname))
		if err != nil {
			return nil, fmt.Errorf("encode %s role: %w", s.rolname, err)
		}
		tuple := storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
		tuple.Header.SetNatts(len(cols))
		slot, err := storage.PageAddHeapTuple(page, tuple)
		if err != nil {
			return nil, err
		}
		entries = append(entries, pgAuthidEntry{
			OID:     uint32(s.oid),
			Rolname: s.rolname,
			TID:     heapTID{Block: 0, Offset: slot},
		})
	}

	// Predefined-role rows (null bitmap for rolpassword/rolvaliduntil, xmin=FrozenTransactionID).
	for _, s := range predefined {
		row := buildPredefinedRow(s.oid, s.rolname)
		payload, err := executor.EncodeRowPG(cols, row)
		if err != nil {
			return nil, fmt.Errorf("encode predefined role %s: %w", s.rolname, err)
		}
		bitmap := executor.NullBitmapPG(row)
		tuple := storage.NewHeapTupleWithNulls(storage.FrozenTransactionID, storage.InvalidTransactionID, bitmap, payload)
		tuple.Header.SetNatts(len(cols))
		slot, err := storage.PageAddHeapTuple(page, tuple)
		if err != nil {
			return nil, err
		}
		entries = append(entries, pgAuthidEntry{
			OID:     uint32(s.oid),
			Rolname: s.rolname,
			TID:     heapTID{Block: 0, Offset: slot},
		})
	}

	path := filepath.Join(dataDir, "global", "1260") // pg_authid OID
	if err := os.WriteFile(path, page, 0o600); err != nil {
		return nil, err
	}
	return entries, nil
}

// bootstrapPostgresDatabase writes a minimal pg_database tuple for the
// template1 database so PG can look up database names during connection.
//
// The row layout MUST match PG18's Form_pg_database exactly because
// pg_database is one of the five formrdesc'd shared critical catalogs:
// PG's RelationCacheInitializePhase2 hardcodes the TupleDesc from
// `postgres/src/include/catalog/pg_database.h` and Phase3 reads our
// heap bytes through that TupleDesc. Schema mismatch → either GETSTRUCT
// reads garbage out of the fixed prefix or SysCacheGetAttr trips
// `Assert("j > attnum")` in nocachegetattr because HEAP_HASVARWIDTH is
// missing while the TupleDesc believes there are var-width attrs
// before the target attnum. M0106-0010 Step 3ct.
func bootstrapPostgresDatabase(dataDir string, encodingID int32, locale localeSettings) error {
	// PG18 pg_database schema (18 cols) per postgres/src/include/catalog/pg_database.h:
	//   1  oid              Oid
	//   2  datname          NameData (NAMEDATALEN=64)
	//   3  datdba           Oid
	//   4  encoding         int4
	//   5  datlocprovider   char (1 byte)
	//   6  datistemplate    bool
	//   7  datallowconn     bool
	//   8  dathasloginevt   bool          (PG18 ADDITION)
	//   9  datconnlimit     int4
	//  10  datfrozenxid     TransactionId
	//  11  datminmxid       TransactionId
	//  12  dattablespace    Oid
	//  --- CATALOG_VARLEN below this line ---
	//  13  datcollate       text          (was NameData in pre-PG15)
	//  14  datctype         text          (was NameData in pre-PG15)
	//  15  datlocale        text          (renamed from daticulocale)
	//  16  daticurules      text          (PG18 ADDITION)
	//  17  datcollversion   text          (BKI_DEFAULT(_null_))
	//  18  datacl           aclitem[]
	// Column list lives in catalog.PgDatabaseColumnsPG18 (shared with the
	// runtime datfrozenxid persistence path, M0117-0008 Part B) so the two
	// encode/decode call sites can never drift out of sync.
	cols := catalog.PgDatabaseColumnsPG18()
	// Locale columns come from the resolved --locale-provider / --locale /
	// --lc-* options (resolveLocale). The default (no options) reproduces a
	// fresh `initdb --locale=C` under the libc provider: datlocprovider='c',
	// datcollate/datctype="C", datlocale NULL. daticurules, datcollversion,
	// datacl stay NULL per BKI defaults (no ICU rules; no recorded collation
	// version; no ACL means default-public access). For the builtin provider
	// datlocale carries the per-database collation (C / C.UTF-8 /
	// PG_UNICODE_FAST); libc records no datlocale.
	datlocaleDatum := executor.NullDatum
	if locale.datlocale != "" {
		datlocaleDatum = executor.NewStringDatum(locale.datlocale)
	}
	buildRow := func(oid uint32, name string, isTemplate bool, allowConn bool) executor.Row {
		return executor.Row{
			executor.NewIntDatum(int64(oid)),                 // oid
			executor.NewStringDatum(name),                    // datname
			executor.NewIntDatum(10),                         // datdba = bootstrap superuser
			executor.NewIntDatum(int64(encodingID)),          // encoding (default PG_UTF8=6; -E/--encoding)
			executor.NewStringDatum(string(locale.provider)), // datlocprovider (--locale-provider)
			executor.NewBoolDatum(isTemplate),                // datistemplate
			executor.NewBoolDatum(allowConn),                 // datallowconn
			executor.NewBoolDatum(false),                     // dathasloginevt
			executor.NewIntDatum(-1),                         // datconnlimit
			executor.NewIntDatum(3),                          // datfrozenxid
			executor.NewIntDatum(1),                          // datminmxid
			executor.NewIntDatum(1663),                       // dattablespace = pg_default
			executor.NewStringDatum(locale.collate),          // datcollate (text, NOT NULL)
			executor.NewStringDatum(locale.ctype),            // datctype   (text, NOT NULL)
			datlocaleDatum,                                   // datlocale (NULL for libc)
			executor.NullDatum,                               // daticurules
			executor.NullDatum,                               // datcollversion
			executor.NullDatum,                               // datacl
		}
	}
	writeRow := func(page storage.Page, row executor.Row) error {
		payload, err := executor.EncodeRowPG(cols, row)
		if err != nil {
			return err
		}
		bitmap := executor.NullBitmapPG(row)
		var tuple storage.HeapTuple
		if bitmap != nil {
			tuple = storage.NewHeapTupleWithNulls(storage.TransactionID(1), storage.InvalidTransactionID, bitmap, payload)
		} else {
			tuple = storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
		}
		tuple.Header.SetNatts(len(cols))
		// Any text column ⇒ HEAP_HASVARWIDTH must be set. Without this
		// PG18 nocachegetattr skips its var-width early-exit guard
		// (`if (HeapTupleHasVarWidth(tup))`), enters the fast path, walks
		// the TupleDesc forward, breaks at the first attlen<=0 attribute,
		// and trips `Assert(j > attnum)` if that var-width attr sits at
		// position ≤ the target. CheckMyDatabase →
		// SysCacheGetAttr(DATABASEOID, tup, Anum_pg_database_datcollversion)
		// hits this exact path during early backend startup.
		tuple.Header.Infomask |= storage.HeapHasVarWidth
		_, err = storage.PageAddHeapTuple(page, tuple)
		return err
	}

	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		return err
	}
	if err := writeRow(page, buildRow(1, "template1", true, true)); err != nil {
		return err
	}
	if err := writeRow(page, buildRow(5, "postgres", false, true)); err != nil {
		return err
	}
	if err := writeRow(page, buildRow(4, "template0", true, false)); err != nil {
		return err
	}
	// Also create index placeholders in base/1/ (goopg's default
	// database). PG's load_critical_index may hardcode dbNode=1
	// for nailed relations.
	base1Dir := filepath.Join(dataDir, "base", "1")
	btreePage := makeBtreeRootPage()
	for _, oid := range []uint32{
		827,  // pg_default_acl_role_nsp_obj_index (Step 3al)
		828,  // pg_default_acl_oid_index (Step 3am)
		2650, // pg_aggregate_fnoid_index (Step 3x)
		2653, // pg_amop_fam_strat_index (Step 3y)
		2654, 2655, 2658, 2659,
		2660, // pg_cast_oid_index (Step 3ab)
		2661, // pg_cast_source_target_index (Step 3ac)
		2662, 2663,
		2665, // pg_constraint_conrelid_contypid_conname_index (batched-48)
		2666, // pg_constraint_contypid_index (B2.1b — domain-constraint typcache scans)
		2667,
		2668, // pg_conversion_default_index (Step 3ah)
		2669, // pg_conversion_name_nsp_index (Step 3aj)
		2670, // pg_conversion_oid_index (Step 3ai)
		2678, 2679, 2680, 2682,
		// 2684, 2685: dedicated bootstrappers (bootstrapPgNamespaceNspnameIndex/OidIndex)
		2686, // pg_opclass_am_name_nsp_index (Step 3ad)
		2687, 2688,
		2689,       // pg_operator_oprname_l_r_n_index (Step 3bl)
		2690, 2691, // pg_proc_oid_index, pg_proc_proname_args_nsp_index
		// 2692, 2693: dedicated bootstrappers (pg_rewrite_oid_index/pg_rewrite_rel_rulename_index)
		2701, 2703,
		5002, // pg_sequence_seqrelid_index (B1.3b: runtime inserts route to base/1; without this placeholder the file auto-created 0-byte and every insert silently skipped)
		2754, // pg_opfamily_am_name_nsp_index (Step 3bn)
		2755, // pg_opfamily_oid_index (Step 3bo)
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
		549,  // pg_foreign_server_name_index (Step 3bf)
		113,  // pg_foreign_server_oid_index (Step 3bg)
		3119, // pg_foreign_table_relid_index (Step 3bi)
		2681, // pg_language_name_index (Step 3bj)
		3351, // pg_partitioned_table_partrelid_index (Step 3bt)
		6111, // pg_publication_pubname_index (Step 3bv)
		6110, // pg_publication_oid_index (Step 3bw)
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
		// {2615, 2615}: dedicated bootstrapper (bootstrapPgNamespaceTuples)
		{2616, 2616}, // pg_opclass
		{2617, 2617}, // pg_operator
		// {2618, 2618}: dedicated bootstrapper (bootstrapPgRewriteTuples)
		{2619, 2619}, // pg_statistic
		{2620, 2620}, // pg_trigger
		{3381, 3381}, // pg_statistic_ext
		{3501, 3501}, // pg_enum (M0106-0010 step 3an)
		{3596, 3596}, // pg_seclabel
		{3602, 3602}, // pg_ts_config (M0106-0010 step 3ck) — authoritative OID per pg_ts_config.h:30
		{3764, 3764}, // pg_ts_config (stale — true pg_ts_config OID is 3602, mapped above)
		{3603, 3603}, // pg_ts_config_map (M0106-0010 step 3cj) — authoritative OID per pg_ts_config_map.h:30
		{3765, 3765}, // pg_ts_config_map (stale — true pg_ts_config_map OID is 3603, mapped above)
		{3600, 3600}, // pg_ts_dict (M0106-0010 step 3cm) — authoritative OID per pg_ts_dict.h:29
		{3766, 3766}, // pg_ts_template_tmplname_index (M0106-0010 step 3co) — authoritative OID per pg_ts_template.h:48
		{3601, 3601}, // pg_ts_parser (M0106-0010 step 3cn) — authoritative OID per pg_ts_parser.h:29
		{3767, 3767}, // pg_ts_template_oid_index (M0106-0010 step 3co) — authoritative OID per pg_ts_template.h:49
		{3764, 3764}, // pg_ts_template (M0106-0010 step 3co) — authoritative OID per pg_ts_template.h:29
		{3768, 3768}, // pg_ts_template (stale — true pg_ts_template OID is 3764, mapped above; 3768 has no upstream catalog assignment)
		{3466, 3466}, // pg_event_trigger (M0106-0010 step 3ar)
		{3079, 3079}, // pg_extension (M0106-0010 step 3aw)
		{2328, 2328}, // pg_foreign_data_wrapper (M0106-0010 step 3bb)
		{1417, 1417}, // pg_foreign_server (M0106-0010 step 3be)
		{1418, 1418}, // pg_user_mapping (M0106-0010 step 3cp) — authoritative OID per pg_user_mapping.h:28
		{3118, 3118}, // pg_foreign_table (M0106-0010 step 3bh)
		{2753, 2753}, // pg_opfamily (M0106-0010 step 3bm)
		{3350, 3350}, // pg_partitioned_table (M0106-0010 step 3bs)
		{6104, 6104}, // pg_publication (M0106-0010 step 3bu)
		{6237, 6237}, // pg_publication_namespace (M0106-0010 step 3bx)
		{6106, 6106}, // pg_publication_rel (M0106-0010 step 3by)
		{3541, 3541}, // pg_range (M0106-0010 step 3bz)
		{2224, 2224}, // pg_sequence (M0106-0010 step 3cb)
		{3429, 3429}, // pg_statistic_ext_data (M0106-0010 step 3cc)
		{3576, 3576}, // pg_transform (M0106-0010 step 3ci) — authoritative OID per pg_transform.h
		{6003, 6003}, // pg_publication (stale comment — OID 6003 has no upstream catalog assignment)
		{6101, 6101}, // pg_publication_rel
		{6102, 6102}, // pg_subscription_rel (stale comment was "pg_sequence" — true pg_sequence OID is 2224, mapped above)
		{6137, 6137}, // pg_transform (stale — true pg_transform OID is 3576, mapped above)
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
	perDBIndexOIDs := []uint32{
		// Local critical indexes
		827,  // pg_default_acl_role_nsp_obj_index (Step 3al)
		828,  // pg_default_acl_oid_index (Step 3am)
		2650, // pg_aggregate_fnoid_index (Step 3x)
		2653, // pg_amop_fam_strat_index (Step 3y)
		2654, 2655, 2658, 2659,
		2660, // pg_cast_oid_index (Step 3ab)
		2661, // pg_cast_source_target_index (Step 3ac)
		2662, 2663,
		2665, // pg_constraint_conrelid_contypid_conname_index (batched-48)
		2666, // pg_constraint_contypid_index (B2.1b — domain-constraint typcache scans)
		2667,
		2668, // pg_conversion_default_index (Step 3ah)
		2669, // pg_conversion_name_nsp_index (Step 3aj)
		2670, // pg_conversion_oid_index (Step 3ai)
		2678, 2679, 2680, 2682,
		// 2684, 2685: dedicated bootstrappers (bootstrapPgNamespaceNspnameIndex/OidIndex)
		2686, // pg_opclass_am_name_nsp_index (Step 3ad)
		2687, 2688,
		2689,       // pg_operator_oprname_l_r_n_index (Step 3bl)
		2690, 2691, // pg_proc_oid_index, pg_proc_proname_args_nsp_index
		// 2692, 2693: dedicated bootstrappers (pg_rewrite_oid_index/pg_rewrite_rel_rulename_index)
		2701, 2703,
		2754, // pg_opfamily_am_name_nsp_index (Step 3bn)
		2755, // pg_opfamily_oid_index (Step 3bo)
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
		549,  // pg_foreign_server_name_index (Step 3bf)
		113,  // pg_foreign_server_oid_index (Step 3bg)
		3119, // pg_foreign_table_relid_index (Step 3bi)
		2681, // pg_language_name_index (Step 3bj)
		3351, // pg_partitioned_table_partrelid_index (Step 3bt)
		6111, // pg_publication_pubname_index (Step 3bv)
		6110, // pg_publication_oid_index (Step 3bw)
		6238, // pg_publication_namespace_oid_index (Step 3bx)
		6239, // pg_publication_namespace_pnnspid_pnpubid_index (Step 3bx)
		6112, // pg_publication_rel_oid_index (Step 3by)
		6113, // pg_publication_rel_prrelid_prpubid_index (Step 3by)
		6116, // pg_publication_rel_prpubid_index (Step 3by)
		3542, // pg_range_rngtypid_index (Step 3bz)
		2228, // pg_range_rngmultitypid_index (Step 3bz)
		5002, // pg_sequence_seqrelid_index (Step 3cb)
		3433, // pg_statistic_ext_data_stxoid_inh_index (Step 3cc)
		3380, // pg_statistic_ext_oid_index (Step 3cd)
		3997, // pg_statistic_ext_name_index (Step 3cd)
		3379, // pg_statistic_ext_relid_index (Step 3cd)
		2696, // pg_statistic_relid_att_inh_index (Step 3ce)
		6114, // pg_subscription_oid_index (Step 3cf)
		6115, // pg_subscription_subname_index (Step 3cf)
		6117, // pg_subscription_rel_srrelid_srsubid_index (Step 3cg)
		2697, // pg_tablespace_oid_index (Step 3ch)
		2698, // pg_tablespace_spcname_index (Step 3ch)
		3574, // pg_transform_oid_index (Step 3ci)
		3575, // pg_transform_type_lang_index (Step 3ci)
		3609, // pg_ts_config_map_index (Step 3cj)
		3608, // pg_ts_config_cfgname_index (Step 3ck)
		3712, // pg_ts_config_oid_index (Step 3ck)
		3604, // pg_ts_dict_dictname_index (Step 3cm)
		3605, // pg_ts_dict_oid_index (Step 3cm)
		3606, // pg_ts_parser_prsname_index (Step 3cn)
		3607, // pg_ts_parser_oid_index (Step 3cn)
		3766, // pg_ts_template_tmplname_index (Step 3co)
		3767, // pg_ts_template_oid_index (Step 3co)
		174,  // pg_user_mapping_oid_index (Step 3cp)
		175,  // pg_user_mapping_user_server_index (Step 3cp)
	}
	for _, oid := range perDBIndexOIDs {
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
		6303, // pg_auth_members_oid_index (batched-13)
		6302, // pg_auth_members_grantor_index (batched-13)
		2695, 3593,
		6246, // pg_parameter_acl_parname_index (Step 3bq)
		6247, // pg_parameter_acl_oid_index (Step 3br)
		6001, // pg_replication_origin_roiident_index (Step 3ca)
		6002, // pg_replication_origin_roname_index (Step 3ca)
		2965, // pg_db_role_setting_databaseid_rol_index (Step 3cu)
		// Also copy all local critical indexes to global/
		827,  // pg_default_acl_role_nsp_obj_index (Step 3al)
		828,  // pg_default_acl_oid_index (Step 3am)
		2650, // pg_aggregate_fnoid_index (Step 3x)
		2653, // pg_amop_fam_strat_index (Step 3y)
		2654, 2655, 2658, 2659,
		2660, // pg_cast_oid_index (Step 3ab)
		2661, // pg_cast_source_target_index (Step 3ac)
		2662, 2663,
		2665, // pg_constraint_conrelid_contypid_conname_index (batched-48)
		2666, // pg_constraint_contypid_index (B2.1b — domain-constraint typcache scans)
		2667,
		2668, // pg_conversion_default_index (Step 3ah)
		2669, // pg_conversion_name_nsp_index (Step 3aj)
		2670, // pg_conversion_oid_index (Step 3ai)
		2678, 2679, 2680, 2682,
		// 2684, 2685: dedicated bootstrappers (bootstrapPgNamespaceNspnameIndex/OidIndex)
		2686, // pg_opclass_am_name_nsp_index (Step 3ad)
		2687, 2688,
		2689,       // pg_operator_oprname_l_r_n_index (Step 3bl)
		2690, 2691, // pg_proc_oid_index, pg_proc_proname_args_nsp_index
		// 2692, 2693: dedicated bootstrappers (pg_rewrite_oid_index/pg_rewrite_rel_rulename_index)
		2701, 2703,
		2754, // pg_opfamily_am_name_nsp_index (Step 3bn)
		2755, // pg_opfamily_oid_index (Step 3bo)
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
		549,  // pg_foreign_server_name_index (Step 3bf)
		113,  // pg_foreign_server_oid_index (Step 3bg)
		3119, // pg_foreign_table_relid_index (Step 3bi)
		2681, // pg_language_name_index (Step 3bj)
		3351, // pg_partitioned_table_partrelid_index (Step 3bt)
		6111, // pg_publication_pubname_index (Step 3bv)
		6110, // pg_publication_oid_index (Step 3bw)
		6238, // pg_publication_namespace_oid_index (Step 3bx)
		6239, // pg_publication_namespace_pnnspid_pnpubid_index (Step 3bx)
		6112, // pg_publication_rel_oid_index (Step 3by)
		6113, // pg_publication_rel_prrelid_prpubid_index (Step 3by)
		6116, // pg_publication_rel_prpubid_index (Step 3by)
		3542, // pg_range_rngtypid_index (Step 3bz)
		2228, // pg_range_rngmultitypid_index (Step 3bz)
		5002, // pg_sequence_seqrelid_index (Step 3cb)
		3433, // pg_statistic_ext_data_stxoid_inh_index (Step 3cc)
		3380, // pg_statistic_ext_oid_index (Step 3cd)
		3997, // pg_statistic_ext_name_index (Step 3cd)
		3379, // pg_statistic_ext_relid_index (Step 3cd)
		2696, // pg_statistic_relid_att_inh_index (Step 3ce)
		6114, // pg_subscription_oid_index (Step 3cf)
		6115, // pg_subscription_subname_index (Step 3cf)
		6117, // pg_subscription_rel_srrelid_srsubid_index (Step 3cg)
		2697, // pg_tablespace_oid_index (Step 3ch)
		2698, // pg_tablespace_spcname_index (Step 3ch)
		3574, // pg_transform_oid_index (Step 3ci)
		3575, // pg_transform_type_lang_index (Step 3ci)
		3609, // pg_ts_config_map_index (Step 3cj)
		3608, // pg_ts_config_cfgname_index (Step 3ck)
		3712, // pg_ts_config_oid_index (Step 3ck)
		3604, // pg_ts_dict_dictname_index (Step 3cm)
		3605, // pg_ts_dict_oid_index (Step 3cm)
		3606, // pg_ts_parser_prsname_index (Step 3cn)
		3607, // pg_ts_parser_oid_index (Step 3cn)
		3766, // pg_ts_template_tmplname_index (Step 3co)
		3767, // pg_ts_template_oid_index (Step 3co)
		174,  // pg_user_mapping_oid_index (Step 3cp)
		175,  // pg_user_mapping_user_server_index (Step 3cp)
	} {
		if err := os.WriteFile(filepath.Join(dataDir, "global", strconv.FormatUint(uint64(oid), 10)), btreePage, 0o600); err != nil {
			return err
		}
	}
	// Create the database directory for template0 (OID 4) with the same
	// structure as postgres (OID 5): PG_VERSION, catalog copies, relmap, indexes.
	tmpl0Dir := filepath.Join(dataDir, "base", "4")
	if err := os.MkdirAll(tmpl0Dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmpl0Dir, "PG_VERSION"), []byte("18\n"), 0o600); err != nil {
		return err
	}
	entries4, _ := os.ReadDir(base1Dir)
	for _, e := range entries4 {
		src := filepath.Join(base1Dir, e.Name())
		dst := filepath.Join(tmpl0Dir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(tmpl0Dir, "pg_filenode.map"), localRelMap, 0o600); err != nil {
		return err
	}
	for _, oid := range perDBIndexOIDs {
		if err := os.WriteFile(filepath.Join(tmpl0Dir, strconv.FormatUint(uint64(oid), 10)), btreePage, 0o600); err != nil {
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

// pgNamespaceColDefs returns PG18's 4-column FormData_pg_namespace layout.
// pg_namespace.h: oid, nspname, nspowner, nspacl.
func pgNamespaceColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "nspname", Type: catalog.Type{Name: "name"}},
		{Name: "nspowner", Type: catalog.Type{Name: "oid"}},
		{Name: "nspacl", Type: catalog.Type{Name: "aclitem[]"}},
	}
}

type pgNamespaceEntry struct {
	OID      uint32
	NspName  string
	NspOwner uint32
}

// pgNamespaceInitialEntries returns the three namespaces PG18 initdb creates:
// pg_catalog (11), pg_toast (99), and public (2200).
func pgNamespaceInitialEntries() []pgNamespaceEntry {
	return []pgNamespaceEntry{
		{OID: 11, NspName: "pg_catalog", NspOwner: 10},
		{OID: 99, NspName: "pg_toast", NspOwner: 10},
		{OID: 2200, NspName: "public", NspOwner: 10},
	}
}

func pgNamespaceRow(e pgNamespaceEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),      // oid
		executor.NewStringDatum(e.NspName),      // nspname
		executor.NewIntDatum(int64(e.NspOwner)), // nspowner
		executor.NewStringDatum("{}"),           // nspacl (empty aclitem[])
	}
}

// bootstrapPgNamespaceTuples writes pg_catalog(11)/pg_toast(99)/public(2200)
// rows to base/{1,5}/2615 so PG's NAMESPACENAME and NAMESPACEOID syscache
// lookups find the namespaces via pg_namespace_nspname_index (2684) and
// pg_namespace_oid_index (2685). Without these rows, any schema-qualified
// relation lookup (e.g. SELECT … FROM pg_catalog.pg_stat_wal_receiver) fails
// with "schema does not exist" before the relation lookup is even attempted.
func bootstrapPgNamespaceTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgNamespaceColDefs()
	entries := pgNamespaceInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgNamespaceRow(e))
	}
	tids, err := writeMultiPageHeapRows(dataDir, "2615", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("pg_namespace heap: %w", err)
	}
	m := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		m[e.OID] = tids[i]
	}
	return m, nil
}

// bootstrapPgNamespaceNspnameIndex writes base/{1,5}/2684 with one
// 72-byte name-keyed IndexTuple per pg_namespace row.
func bootstrapPgNamespaceNspnameIndex(dataDir string, tids map[uint32]heapTID) error {
	type entry struct {
		name string
		blk  uint32
		off  uint16
	}
	entries := make([]entry, 0, len(tids))
	for _, e := range pgNamespaceInitialEntries() {
		t := tids[e.OID]
		entries = append(entries, entry{name: e.NspName, blk: t.Block, off: t.Offset})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleNameKey(e.blk, e.off, e.name)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_namespace_nspname_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1, 0)
	file := append(meta, leaf...)
	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		if err := os.WriteFile(filepath.Join(dir, "2684"), file, 0o600); err != nil {
			return fmt.Errorf("write pg_namespace_nspname_index in %s: %w", dir, err)
		}
	}
	return nil
}

// bootstrapPgNamespaceOidIndex writes base/{1,5}/2685 with one
// 16-byte oid-keyed IndexTuple per pg_namespace row.
func bootstrapPgNamespaceOidIndex(dataDir string, tids map[uint32]heapTID) error {
	type entry struct {
		oid uint32
		blk uint32
		off uint16
	}
	entries := make([]entry, 0, len(tids))
	for oid, t := range tids {
		entries = append(entries, entry{oid: oid, blk: t.Block, off: t.Offset})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].oid < entries[j].oid })

	tuples := make([][]byte, len(entries))
	for i, e := range entries {
		tuples[i] = pgBuildIndexTupleOidKey(e.blk, e.off, e.oid)
	}
	leaf, err := pgBuildBtreeLeafRootPage(tuples)
	if err != nil {
		return fmt.Errorf("pg_namespace_oid_index leaf: %w", err)
	}
	meta := pgBuildBtreeMetapageWithRoot(1, 0)
	file := append(meta, leaf...)
	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		if err := os.WriteFile(filepath.Join(dir, "2685"), file, 0o600); err != nil {
			return fmt.Errorf("write pg_namespace_oid_index in %s: %w", dir, err)
		}
	}
	return nil
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

// oidArrayBytes builds a binary `oid[]` ArrayType for pg_proc.proallargtypes
// (and any other oid[] column that needs a non-empty value). Layout matches
// PG's construct_array output for typcategory='A' / typelem=26 / typalign='i':
// 24-byte header (vl_len_, ndim, dataoffset, elemtype, dim[0], lbound[0]) then
// N×4-byte little-endian OIDs. lbound=1 mirrors construct_array's default
// (unlike pg_proc.proargtypes which uses oidvector with lbound=0).
//
// Step 3dk: feeds executor.NewBytesDatum so encodeValuePG's KindBytes
// passthrough path emits the blob unchanged. NULL pg_proc rows still use
// emptyArrayTypeBytes(26).
func oidArrayBytes(oids []uint32) []byte {
	const headerSize = 24
	total := headerSize + 4*len(oids)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 26)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(oids)))
	binary.LittleEndian.PutUint32(buf[20:24], 1)
	for i, o := range oids {
		binary.LittleEndian.PutUint32(buf[24+i*4:28+i*4], o)
	}
	return buf
}

// charArrayBytes builds a binary `char[]` ArrayType for pg_proc.proargmodes
// (and any other char[] column needing a non-empty value). PG's CHAR (OID 18)
// is single-byte with typalign='c'; elements are packed without padding.
// Layout: 24-byte header + N×1 byte payload.
//
// Step 3dk: feeds executor.NewBytesDatum / KindBytes passthrough.
func charArrayBytes(chars []byte) []byte {
	const headerSize = 24
	total := headerSize + len(chars)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 18)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(chars)))
	binary.LittleEndian.PutUint32(buf[20:24], 1)
	copy(buf[24:], chars)
	return buf
}

// textArrayBytes builds a binary `text[]` ArrayType for pg_proc.proargnames
// (and any other text[] column needing a non-empty value). text is a 4-byte
// aligned varlena (typalign='i'); PG's array_seek walks elements by
// `att_align_nominal(off, 'i') == (off+3) &^ 3` before reading each element's
// 4-byte varlena header.
//
// Layout: 24-byte header (vl_len_, ndim=1, dataoffset=0, elemtype=25, dim[0]=N,
// lbound[0]=1) then a packed-with-4-byte-alignment sequence of long-form
// varlenas (4-byte length + payload). Each element's varlena header encodes
// total size via SET_VARSIZE_4B: (totalSize << 2) with low 2 bits = 00.
//
// Step 3dk: feeds executor.NewBytesDatum / KindBytes passthrough.
func textArrayBytes(strs []string) []byte {
	const headerSize = 24
	align4 := func(n int) int { return (n + 3) &^ 3 }

	// Compute element offsets and total array size up-front.
	elemOffsets := make([]int, len(strs))
	off := headerSize
	for i, s := range strs {
		off = align4(off)
		elemOffsets[i] = off
		off += 4 + len(s)
	}
	total := off

	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 25)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(strs)))
	binary.LittleEndian.PutUint32(buf[20:24], 1)
	for i, s := range strs {
		eoff := elemOffsets[i]
		binary.LittleEndian.PutUint32(buf[eoff:eoff+4], uint32(4+len(s))<<2)
		copy(buf[eoff+4:], []byte(s))
	}
	return buf
}

// pgProcEntry is a minimal description of a pg_proc row produced
// during initdb. v0 only needs to seed the AM handler functions so
// PG's RelationInitIndexAccessInfo can resolve amhandler via fmgr.
//
// M0106-0010 Step 3dj extends the shape with two switches —
// `RetSet` and `NotStrict` — so a single SRF entry (OID 3317,
// pg_stat_get_wal_receiver) can opt into proretset=true and
// proisstrict=false without disturbing the AM-handler / type-IO
// rows pinned by TestPgProcRowBtreeHandlerMatchesFormPgProc et al.
// The CATALOG_VARLEN array columns (proallargtypes, proargmodes,
// proargnames) remain empty here; populating them is Step 3dk
// (view-side seeding) work because the view rewrite rule must
// resolve `s.<col>` references through proargnames anyway.
type pgProcEntry struct {
	OID         uint32
	Name        string   // proname (NameData, ≤63 bytes)
	RetType     uint32   // prorettype OID
	ArgTypes    []uint32 // proargtypes vector. nil/empty → defaults to [2281] (internal)
	Volatile    byte     // provolatile char. 0 → defaults to 'v' (volatile)
	Parallel    byte     // proparallel char. 0 → defaults to 's' (safe)
	RetSet      bool     // proretset. defaults to false
	NotStrict   bool     // proisstrict inverse. defaults to strict (true)
	Lang        uint32   // prolang OID. 0 → use 12 (INTERNALlanguageId). 13 = C, 14 = SQL.
	HandlerName string   // prosrc text (e.g. "bthandler") — fmgr internal lookup key
	// Step 3dk: OUT-arg metadata for SRFs whose result columns are described
	// via proallargtypes / proargmodes / proargnames rather than prorettype.
	// nil leaves the corresponding column as the legacy empty-ArrayType shell;
	// non-nil triggers pgProcRow to emit a binary ArrayType blob via
	// executor.NewBytesDatum + codec.go's KindBytes passthrough.
	AllArgTypes []uint32 // proallargtypes (oid[])
	ArgModes    []byte   // proargmodes (char[]; 'i'/'o'/'b'/'v'/'t')
	ArgNames    []string // proargnames (text[])
	// batched-51: prokind override. 0 → derive from HandlerName
	// (aggregate_dummy → 'a', window_* → 'w', otherwise 'f'). PG18's
	// ParseFuncOrColumn → check_agg_arguments rejects the agg(*) call
	// shape unless prokind='a'. Explicit non-zero values take precedence
	// for entries whose handler does not encode the kind in its name.
	Kind byte
}

// pgProcColDefs returns the 30-column PG18 FormData_pg_proc layout.
// Column order must match `postgres/src/include/catalog/pg_proc.h`
// so PG can dereference GETSTRUCT(tup)→Form_pg_proc directly.
func pgProcColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},               // 1
		{Name: "proname", Type: catalog.Type{Name: "name"}},          // 2
		{Name: "pronamespace", Type: catalog.Type{Name: "oid"}},      // 3
		{Name: "proowner", Type: catalog.Type{Name: "oid"}},          // 4
		{Name: "prolang", Type: catalog.Type{Name: "oid"}},           // 5
		{Name: "procost", Type: catalog.Type{Name: "float4"}},        // 6
		{Name: "prorows", Type: catalog.Type{Name: "float4"}},        // 7
		{Name: "provariadic", Type: catalog.Type{Name: "oid"}},       // 8
		{Name: "prosupport", Type: catalog.Type{Name: "regproc"}},    // 9
		{Name: "prokind", Type: catalog.Type{Name: "char"}},          // 10
		{Name: "prosecdef", Type: catalog.Type{Name: "bool"}},        // 11
		{Name: "proleakproof", Type: catalog.Type{Name: "bool"}},     // 12
		{Name: "proisstrict", Type: catalog.Type{Name: "bool"}},      // 13
		{Name: "proretset", Type: catalog.Type{Name: "bool"}},        // 14
		{Name: "provolatile", Type: catalog.Type{Name: "char"}},      // 15
		{Name: "proparallel", Type: catalog.Type{Name: "char"}},      // 16
		{Name: "pronargs", Type: catalog.Type{Name: "int2"}},         // 17
		{Name: "pronargdefaults", Type: catalog.Type{Name: "int2"}},  // 18
		{Name: "prorettype", Type: catalog.Type{Name: "oid"}},        // 19
		{Name: "proargtypes", Type: catalog.Type{Name: "oidvector"}}, // 20
		// CATALOG_VARLEN section: nullable in PG but we emit empty
		// binary arrays so the relacl-style "raw bytes as ArrayType*"
		// dereferences in PG do not trip ARR_ELEMTYPE assertions.
		{Name: "proallargtypes", Type: catalog.Type{Name: "oid[]"}},        // 21
		{Name: "proargmodes", Type: catalog.Type{Name: "char[]"}},          // 22
		{Name: "proargnames", Type: catalog.Type{Name: "text[]"}},          // 23
		{Name: "proargdefaults", Type: catalog.Type{Name: "pg_node_tree"}}, // 24
		{Name: "protrftypes", Type: catalog.Type{Name: "oid[]"}},           // 25
		{Name: "prosrc", Type: catalog.Type{Name: "text"}},                 // 26 — FORCE_NOT_NULL
		{Name: "probin", Type: catalog.Type{Name: "text"}},                 // 27
		{Name: "prosqlbody", Type: catalog.Type{Name: "pg_node_tree"}},     // 28
		{Name: "proconfig", Type: catalog.Type{Name: "text[]"}},            // 29
		{Name: "proacl", Type: catalog.Type{Name: "aclitem[]"}},            // 30
	}
}

// pgProcInitialEntries returns all pg_proc.dat entries for PG18.
// This is the full set of 3397 entries generated from
// postgres/src/include/catalog/pg_proc.dat by cmd/gen-pg-proc-data/main.go.
// It supersedes the former hand-crafted list of 32 entries (7 AM handlers +
// 24 I/O regprocs + 1 SRF) that was maintained prior to M0106-0010
// batched-14.
func pgProcInitialEntries() []pgProcEntry {
	return pgProcAllEntries()
}

// pgProcRow materialises one pgProcEntry as the 30-column row that
// EncodeRowPG will pack into the on-disk heap tuple. A nil/empty
// ArgTypes defaults to `(internal)`; a zero Volatile defaults to 'v'.
// These defaults preserve the bthandler-style AM-handler row shape
// pinned by TestPgProcRowBtreeHandlerMatchesFormPgProc.
func pgProcRow(e pgProcEntry) executor.Row {
	// nil ArgTypes preserves the legacy AM-handler default (one
	// `internal` argument, OID 2281). An explicitly empty non-nil
	// slice (e.g. `[]uint32{}`) is the unambiguous spelling for a
	// zero-argument function — required by Step 3dj's
	// pg_stat_get_wal_receiver entry (proargtypes => '' upstream).
	argTypes := e.ArgTypes
	if argTypes == nil {
		argTypes = []uint32{2281}
	}
	vol := e.Volatile
	if vol == 0 {
		vol = 'v'
	}
	parallel := e.Parallel
	if parallel == 0 {
		parallel = 's'
	}
	kind := e.Kind
	if kind == 0 {
		kind = derivePgProcKind(e.HandlerName)
	}
	// Step 3dk: emit OUT-arg metadata as binary ArrayType blobs when
	// supplied. NewStringDatum("") falls through to encodeValuePG's
	// emptyArrayTypeBytes path; NewBytesDatum lands a KindBytes datum
	// that the new codec.go passthrough emits verbatim. Behaviour for
	// all pre-Step-3dk entries is unchanged because their fields are nil.
	allArgs := executor.NewStringDatum("")
	if e.AllArgTypes != nil {
		allArgs = executor.NewBytesDatum(oidArrayBytes(e.AllArgTypes))
	}
	argModes := executor.NewStringDatum("")
	if e.ArgModes != nil {
		argModes = executor.NewBytesDatum(charArrayBytes(e.ArgModes))
	}
	argNames := executor.NewStringDatum("")
	if e.ArgNames != nil {
		argNames = executor.NewBytesDatum(textArrayBytes(e.ArgNames))
	}
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)), // 1  oid
		executor.NewStringDatum(e.Name),    // 2  proname
		executor.NewIntDatum(11),           // 3  pronamespace = pg_catalog
		executor.NewIntDatum(10),           // 4  proowner = BOOTSTRAP_SUPERUSERID
		executor.NewIntDatum(func() int64 { // 5  prolang
			if e.Lang != 0 {
				return int64(e.Lang)
			}
			return 12 // INTERNALlanguageId
		}()),
		executor.NewIntDatum(1),                          // 6  procost = 1 (float4)
		executor.NewIntDatum(0),                          // 7  prorows = 0 (float4)
		executor.NewIntDatum(0),                          // 8  provariadic = 0
		executor.NewIntDatum(0),                          // 9  prosupport = 0
		executor.NewStringDatum(string(kind)),            // 10 prokind
		executor.NewBoolDatum(false),                     // 11 prosecdef
		executor.NewBoolDatum(false),                     // 12 proleakproof
		executor.NewBoolDatum(!e.NotStrict),              // 13 proisstrict
		executor.NewBoolDatum(e.RetSet),                  // 14 proretset
		executor.NewStringDatum(string(vol)),             // 15 provolatile
		executor.NewStringDatum(string(parallel)),        // 16 proparallel
		executor.NewIntDatum(int64(len(argTypes))),       // 17 pronargs
		executor.NewIntDatum(0),                          // 18 pronargdefaults
		executor.NewIntDatum(int64(e.RetType)),           // 19 prorettype
		executor.NewBytesDatum(oidVectorBytes(argTypes)), // 20 proargtypes
		allArgs,                                // 21 proallargtypes
		argModes,                               // 22 proargmodes
		argNames,                               // 23 proargnames
		executor.NewStringDatum(""),            // 24 proargdefaults (pg_node_tree)
		executor.NewStringDatum(""),            // 25 protrftypes
		executor.NewStringDatum(e.HandlerName), // 26 prosrc — fmgr internal lookup key
		executor.NewStringDatum(""),            // 27 probin
		executor.NewStringDatum(""),            // 28 prosqlbody
		executor.NewStringDatum(""),            // 29 proconfig
		executor.NewStringDatum(""),            // 30 proacl
	}
}

// derivePgProcKind picks the prokind char for a pg_proc.dat entry
// when the entry does not set Kind explicitly. Mirrors PG18's
// PROKIND_* constants in `postgres/src/include/catalog/pg_proc.h`:
// 'f' regular function, 'a' aggregate, 'w' window, 'p' procedure.
// goopg's seed data uses the upstream pg_proc.dat handler-name
// convention — `aggregate_dummy` for aggregates and `window_*` for
// window functions — so a single handler-name probe recovers the
// canonical kind without auditing all 3397 seed rows. batched-51:
// without this, PG18 standby's `ParseFuncOrColumn` rejects every
// `agg(*)` call shape with 42809 because every prokind reads 'f'.
func derivePgProcKind(handlerName string) byte {
	switch {
	case handlerName == "aggregate_dummy":
		return 'a'
	case len(handlerName) > 7 && handlerName[:7] == "window_":
		return 'w'
	default:
		return 'f'
	}
}

// bootstrapPgProcTuples writes the 7 AM handler pg_proc heap tuples
// to base/1/1255 and base/5/1255. M0106-0010 step 3a: required so
// PG standby startup's RelationInitIndexAccessInfo →
// OidFunctionCall0(amhandler) succeeds — fmgr does
// SearchSysCache1(PROCOID, …) on the index AM's handler OID and
// dereferences GETSTRUCT(tup)→Form_pg_proc to read prosrc.
func bootstrapPgProcTuples(dataDir string) ([]heapTID, error) {
	cols := pgProcColDefs()
	entries := pgProcInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgProcRow(e))
	}
	return writeMultiPageHeapRows(dataDir, "1255", cols, rows)
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
		amHash      uint32 = 405
		amGist      uint32 = 783
		amGin       uint32 = 2742
		amBrin      uint32 = 4000
		amSpgist    uint32 = 3580
	)
	// Entries with explicit OIDs from pg_opclass_d.h / pg_opclass.dat.
	// Entries without explicit OIDs in pg_opclass.dat use synthetic OIDs
	// starting at 9000, assigned in dat-file order.
	return []pgOpclassEntry{
		// btree/array_ops
		{OID: 9000, Method: amBtree, Name: "array_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 397, IntType: 2277, Default: true, KeyType: 0},
		// hash/array_ops
		{OID: 9001, Method: amHash, Name: "array_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 627, IntType: 2277, Default: true, KeyType: 0},
		// btree/bit_ops
		{OID: 9002, Method: amBtree, Name: "bit_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 423, IntType: 1560, Default: true, KeyType: 0},
		// btree/bool_ops — synthetic OID 1984 (no explicit OID in dat; pinned for nailed-index indclass)
		{OID: 1984, Method: amBtree, Name: "bool_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 424, IntType: 16, Default: true, KeyType: 0},
		// btree/bpchar_ops
		{OID: 9003, Method: amBtree, Name: "bpchar_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 426, IntType: 1042, Default: true, KeyType: 0},
		// hash/bpchar_ops
		{OID: 9004, Method: amHash, Name: "bpchar_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 427, IntType: 1042, Default: true, KeyType: 0},
		// btree/bytea_ops
		{OID: 9005, Method: amBtree, Name: "bytea_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 428, IntType: 17, Default: true, KeyType: 0},
		// btree/char_ops — synthetic OID 1985 (no explicit OID in dat; pinned for nailed-index indclass)
		{OID: 1985, Method: amBtree, Name: "char_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 429, IntType: 18, Default: true, KeyType: 0},
		// hash/char_ops
		{OID: 9006, Method: amHash, Name: "char_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 431, IntType: 18, Default: true, KeyType: 0},
		// btree/cidr_ops (default=false: inet_ops is the default for inet)
		{OID: 9007, Method: amBtree, Name: "cidr_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1974, IntType: 869, Default: false, KeyType: 0},
		// hash/cidr_ops
		{OID: 9008, Method: amHash, Name: "cidr_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1975, IntType: 869, Default: false, KeyType: 0},
		// btree/date_ops — OID 3122 = DATE_BTREE_OPS_OID
		{OID: 3122, Method: amBtree, Name: "date_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 434, IntType: 1082, Default: true, KeyType: 0},
		// hash/date_ops
		{OID: 9009, Method: amHash, Name: "date_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 435, IntType: 1082, Default: true, KeyType: 0},
		// btree/float4_ops
		{OID: 9010, Method: amBtree, Name: "float4_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1970, IntType: 700, Default: true, KeyType: 0},
		// hash/float4_ops
		{OID: 9011, Method: amHash, Name: "float4_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1971, IntType: 700, Default: true, KeyType: 0},
		// btree/float8_ops — OID 3123 = FLOAT8_BTREE_OPS_OID
		{OID: 3123, Method: amBtree, Name: "float8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1970, IntType: 701, Default: true, KeyType: 0},
		// hash/float8_ops
		{OID: 9012, Method: amHash, Name: "float8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1971, IntType: 701, Default: true, KeyType: 0},
		// btree/inet_ops
		{OID: 9013, Method: amBtree, Name: "inet_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1974, IntType: 869, Default: true, KeyType: 0},
		// hash/inet_ops
		{OID: 9014, Method: amHash, Name: "inet_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1975, IntType: 869, Default: true, KeyType: 0},
		// gist/inet_ops (default=false: spgist is the default for inet gist)
		{OID: 9015, Method: amGist, Name: "inet_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3550, IntType: 869, Default: false, KeyType: 0},
		// spgist/inet_ops
		{OID: 9016, Method: amSpgist, Name: "inet_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3794, IntType: 869, Default: true, KeyType: 0},
		// btree/int2_ops — OID 1979 = INT2_BTREE_OPS_OID
		{OID: 1979, Method: amBtree, Name: "int2_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1976, IntType: 21, Default: true, KeyType: 0},
		// hash/int2_ops
		{OID: 9017, Method: amHash, Name: "int2_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1977, IntType: 21, Default: true, KeyType: 0},
		// btree/int4_ops — OID 1978 = INT4_BTREE_OPS_OID
		{OID: 1978, Method: amBtree, Name: "int4_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1976, IntType: 23, Default: true, KeyType: 0},
		// hash/int4_ops
		{OID: 9018, Method: amHash, Name: "int4_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1977, IntType: 23, Default: true, KeyType: 0},
		// btree/int8_ops — OID 3124 = INT8_BTREE_OPS_OID
		{OID: 3124, Method: amBtree, Name: "int8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1976, IntType: 20, Default: true, KeyType: 0},
		// hash/int8_ops
		{OID: 9019, Method: amHash, Name: "int8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1977, IntType: 20, Default: true, KeyType: 0},
		// btree/interval_ops
		{OID: 9020, Method: amBtree, Name: "interval_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1982, IntType: 1186, Default: true, KeyType: 0},
		// hash/interval_ops
		{OID: 9021, Method: amHash, Name: "interval_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1983, IntType: 1186, Default: true, KeyType: 0},
		// btree/macaddr_ops
		{OID: 9022, Method: amBtree, Name: "macaddr_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1984, IntType: 829, Default: true, KeyType: 0},
		// hash/macaddr_ops
		{OID: 9023, Method: amHash, Name: "macaddr_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1985, IntType: 829, Default: true, KeyType: 0},
		// btree/macaddr8_ops
		{OID: 9024, Method: amBtree, Name: "macaddr8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3371, IntType: 774, Default: true, KeyType: 0},
		// hash/macaddr8_ops
		{OID: 9025, Method: amHash, Name: "macaddr8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3372, IntType: 774, Default: true, KeyType: 0},
		// btree/name_ops — synthetic OID 1986 (no explicit OID; pinned for nailed-index indclass)
		// name_ops keys are stored as cstring (2275) for index space — see pg_opclass.dat comment.
		{OID: 1986, Method: amBtree, Name: "name_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1994, IntType: 19, Default: true, KeyType: 2275},
		// hash/name_ops
		{OID: 9026, Method: amHash, Name: "name_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1995, IntType: 19, Default: true, KeyType: 0},
		// btree/numeric_ops — OID 3125 = NUMERIC_BTREE_OPS_OID
		{OID: 3125, Method: amBtree, Name: "numeric_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1988, IntType: 1700, Default: true, KeyType: 0},
		// hash/numeric_ops
		{OID: 9027, Method: amHash, Name: "numeric_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1998, IntType: 1700, Default: true, KeyType: 0},
		// btree/oid_ops — OID 1981 = OID_BTREE_OPS_OID
		{OID: 1981, Method: amBtree, Name: "oid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1989, IntType: 26, Default: true, KeyType: 0},
		// hash/oid_ops
		{OID: 9028, Method: amHash, Name: "oid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1990, IntType: 26, Default: true, KeyType: 0},
		// btree/oidvector_ops — synthetic OID 1987 (pinned for nailed-index indclass)
		{OID: 1987, Method: amBtree, Name: "oidvector_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1991, IntType: 30, Default: true, KeyType: 0},
		// hash/oidvector_ops
		{OID: 9029, Method: amHash, Name: "oidvector_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1992, IntType: 30, Default: true, KeyType: 0},
		// btree/record_ops
		{OID: 9030, Method: amBtree, Name: "record_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2994, IntType: 2249, Default: true, KeyType: 0},
		// hash/record_ops
		{OID: 9031, Method: amHash, Name: "record_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 6194, IntType: 2249, Default: true, KeyType: 0},
		// btree/record_image_ops (default=false: record_ops is the default)
		{OID: 9032, Method: amBtree, Name: "record_image_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3194, IntType: 2249, Default: false, KeyType: 0},
		// btree/text_ops — OID 3126 = TEXT_BTREE_OPS_OID
		{OID: 3126, Method: amBtree, Name: "text_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1994, IntType: 25, Default: true, KeyType: 0},
		// hash/text_ops
		{OID: 9033, Method: amHash, Name: "text_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1995, IntType: 25, Default: true, KeyType: 0},
		// btree/time_ops
		{OID: 9034, Method: amBtree, Name: "time_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1996, IntType: 1083, Default: true, KeyType: 0},
		// hash/time_ops
		{OID: 9035, Method: amHash, Name: "time_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1997, IntType: 1083, Default: true, KeyType: 0},
		// btree/timestamptz_ops — OID 3127 = TIMESTAMPTZ_BTREE_OPS_OID
		{OID: 3127, Method: amBtree, Name: "timestamptz_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 434, IntType: 1184, Default: true, KeyType: 0},
		// hash/timestamptz_ops
		{OID: 9036, Method: amHash, Name: "timestamptz_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1999, IntType: 1184, Default: true, KeyType: 0},
		// btree/timetz_ops
		{OID: 9037, Method: amBtree, Name: "timetz_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2000, IntType: 1266, Default: true, KeyType: 0},
		// hash/timetz_ops
		{OID: 9038, Method: amHash, Name: "timetz_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2001, IntType: 1266, Default: true, KeyType: 0},
		// btree/varbit_ops
		{OID: 9039, Method: amBtree, Name: "varbit_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2002, IntType: 1562, Default: true, KeyType: 0},
		// btree/varchar_ops (default=false: text_ops is the default for varchar=25)
		{OID: 9040, Method: amBtree, Name: "varchar_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1994, IntType: 25, Default: false, KeyType: 0},
		// hash/varchar_ops
		{OID: 9041, Method: amHash, Name: "varchar_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1995, IntType: 25, Default: false, KeyType: 0},
		// btree/timestamp_ops — OID 3128 = TIMESTAMP_BTREE_OPS_OID
		{OID: 3128, Method: amBtree, Name: "timestamp_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 434, IntType: 1114, Default: true, KeyType: 0},
		// hash/timestamp_ops
		{OID: 9042, Method: amHash, Name: "timestamp_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2040, IntType: 1114, Default: true, KeyType: 0},
		// btree/text_pattern_ops — OID 4217 = TEXT_BTREE_PATTERN_OPS_OID
		{OID: 4217, Method: amBtree, Name: "text_pattern_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2095, IntType: 25, Default: false, KeyType: 0},
		// btree/varchar_pattern_ops — OID 4218 = VARCHAR_BTREE_PATTERN_OPS_OID
		{OID: 4218, Method: amBtree, Name: "varchar_pattern_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2095, IntType: 25, Default: false, KeyType: 0},
		// btree/bpchar_pattern_ops — OID 4219 = BPCHAR_BTREE_PATTERN_OPS_OID
		{OID: 4219, Method: amBtree, Name: "bpchar_pattern_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2097, IntType: 1042, Default: false, KeyType: 0},
		// btree/money_ops
		{OID: 9043, Method: amBtree, Name: "money_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2099, IntType: 790, Default: true, KeyType: 0},
		// hash/bool_ops
		{OID: 9044, Method: amHash, Name: "bool_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2222, IntType: 16, Default: true, KeyType: 0},
		// hash/bytea_ops
		{OID: 9045, Method: amHash, Name: "bytea_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2223, IntType: 17, Default: true, KeyType: 0},
		// btree/tid_ops
		{OID: 9046, Method: amBtree, Name: "tid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2789, IntType: 27, Default: true, KeyType: 0},
		// hash/xid_ops
		{OID: 9047, Method: amHash, Name: "xid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2225, IntType: 28, Default: true, KeyType: 0},
		// hash/xid8_ops
		{OID: 9048, Method: amHash, Name: "xid8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 5032, IntType: 5069, Default: true, KeyType: 0},
		// btree/xid8_ops
		{OID: 9049, Method: amBtree, Name: "xid8_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 5067, IntType: 5069, Default: true, KeyType: 0},
		// hash/cid_ops
		{OID: 9050, Method: amHash, Name: "cid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2226, IntType: 29, Default: true, KeyType: 0},
		// hash/tid_ops
		{OID: 9051, Method: amHash, Name: "tid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2227, IntType: 27, Default: true, KeyType: 0},
		// hash/text_pattern_ops
		{OID: 9052, Method: amHash, Name: "text_pattern_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2229, IntType: 25, Default: false, KeyType: 0},
		// hash/varchar_pattern_ops
		{OID: 9053, Method: amHash, Name: "varchar_pattern_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2229, IntType: 25, Default: false, KeyType: 0},
		// hash/bpchar_pattern_ops
		{OID: 9054, Method: amHash, Name: "bpchar_pattern_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2231, IntType: 1042, Default: false, KeyType: 0},
		// hash/aclitem_ops
		{OID: 9055, Method: amHash, Name: "aclitem_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2235, IntType: 1033, Default: true, KeyType: 0},
		// gist/box_ops
		{OID: 9056, Method: amGist, Name: "box_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2593, IntType: 603, Default: true, KeyType: 0},
		// gist/point_ops (opckeytype=box 603)
		{OID: 9057, Method: amGist, Name: "point_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 1029, IntType: 600, Default: true, KeyType: 603},
		// gist/poly_ops (opckeytype=box 603)
		{OID: 9058, Method: amGist, Name: "poly_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2594, IntType: 604, Default: true, KeyType: 603},
		// gist/circle_ops (opckeytype=box 603)
		{OID: 9059, Method: amGist, Name: "circle_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2595, IntType: 718, Default: true, KeyType: 603},
		// gin/array_ops (opckeytype=anyelement 2283)
		{OID: 9060, Method: amGin, Name: "array_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2745, IntType: 2277, Default: true, KeyType: 2283},
		// btree/uuid_ops
		{OID: 9061, Method: amBtree, Name: "uuid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2968, IntType: 2950, Default: true, KeyType: 0},
		// hash/uuid_ops
		{OID: 9062, Method: amHash, Name: "uuid_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 2969, IntType: 2950, Default: true, KeyType: 0},
		// btree/pg_lsn_ops
		{OID: 9063, Method: amBtree, Name: "pg_lsn_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3253, IntType: 3220, Default: true, KeyType: 0},
		// hash/pg_lsn_ops
		{OID: 9064, Method: amHash, Name: "pg_lsn_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3254, IntType: 3220, Default: true, KeyType: 0},
		// btree/enum_ops (opcintype=anyenum 3500)
		{OID: 9065, Method: amBtree, Name: "enum_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3522, IntType: 3500, Default: true, KeyType: 0},
		// hash/enum_ops
		{OID: 9066, Method: amHash, Name: "enum_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3523, IntType: 3500, Default: true, KeyType: 0},
		// btree/tsvector_ops
		{OID: 9067, Method: amBtree, Name: "tsvector_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3626, IntType: 3614, Default: true, KeyType: 0},
		// gist/tsvector_ops (opckeytype=gtsvector 3642)
		{OID: 9068, Method: amGist, Name: "tsvector_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3655, IntType: 3614, Default: true, KeyType: 3642},
		// gin/tsvector_ops (opckeytype=text 25)
		{OID: 9069, Method: amGin, Name: "tsvector_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3659, IntType: 3614, Default: true, KeyType: 25},
		// btree/tsquery_ops (opcintype=tsquery 3615)
		{OID: 9070, Method: amBtree, Name: "tsquery_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3683, IntType: 3615, Default: true, KeyType: 0},
		// gist/tsquery_ops (opckeytype=int8 20)
		{OID: 9071, Method: amGist, Name: "tsquery_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3702, IntType: 3615, Default: true, KeyType: 20},
		// btree/range_ops (opcintype=anyrange 3831)
		{OID: 9072, Method: amBtree, Name: "range_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3901, IntType: 3831, Default: true, KeyType: 0},
		// hash/range_ops
		{OID: 9073, Method: amHash, Name: "range_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3903, IntType: 3831, Default: true, KeyType: 0},
		// gist/range_ops
		{OID: 9074, Method: amGist, Name: "range_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3919, IntType: 3831, Default: true, KeyType: 0},
		// spgist/range_ops
		{OID: 9075, Method: amSpgist, Name: "range_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 3474, IntType: 3831, Default: true, KeyType: 0},
		// btree/multirange_ops (opcintype=anymultirange 4537)
		{OID: 9076, Method: amBtree, Name: "multirange_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4199, IntType: 4537, Default: true, KeyType: 0},
		// hash/multirange_ops
		{OID: 9077, Method: amHash, Name: "multirange_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4225, IntType: 4537, Default: true, KeyType: 0},
		// gist/multirange_ops (opckeytype=anyrange 3831)
		{OID: 9078, Method: amGist, Name: "multirange_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 6158, IntType: 4537, Default: true, KeyType: 3831},
		// spgist/box_ops
		{OID: 9079, Method: amSpgist, Name: "box_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 5000, IntType: 603, Default: true, KeyType: 0},
		// spgist/quad_point_ops
		{OID: 9080, Method: amSpgist, Name: "quad_point_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4015, IntType: 600, Default: true, KeyType: 0},
		// spgist/kd_point_ops (default=false: quad_point_ops is default)
		{OID: 9081, Method: amSpgist, Name: "kd_point_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4016, IntType: 600, Default: false, KeyType: 0},
		// spgist/text_ops
		{OID: 9082, Method: amSpgist, Name: "text_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4017, IntType: 25, Default: true, KeyType: 0},
		// spgist/poly_ops (opckeytype=box 603)
		{OID: 9083, Method: amSpgist, Name: "poly_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 5008, IntType: 604, Default: true, KeyType: 603},
		// btree/jsonb_ops (opcintype=jsonb 3802)
		{OID: 9084, Method: amBtree, Name: "jsonb_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4033, IntType: 3802, Default: true, KeyType: 0},
		// hash/jsonb_ops
		{OID: 9085, Method: amHash, Name: "jsonb_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4034, IntType: 3802, Default: true, KeyType: 0},
		// gin/jsonb_ops (opckeytype=text 25)
		{OID: 9086, Method: amGin, Name: "jsonb_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4036, IntType: 3802, Default: true, KeyType: 25},
		// gin/jsonb_path_ops (default=false; opckeytype=int4 23)
		{OID: 9087, Method: amGin, Name: "jsonb_path_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4037, IntType: 3802, Default: false, KeyType: 23},
		// brin/bytea_minmax_ops
		{OID: 9088, Method: amBrin, Name: "bytea_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4064, IntType: 17, Default: true, KeyType: 17},
		// brin/bytea_bloom_ops
		{OID: 9089, Method: amBrin, Name: "bytea_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4578, IntType: 17, Default: false, KeyType: 17},
		// brin/char_minmax_ops
		{OID: 9090, Method: amBrin, Name: "char_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4062, IntType: 18, Default: true, KeyType: 18},
		// brin/char_bloom_ops
		{OID: 9091, Method: amBrin, Name: "char_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4577, IntType: 18, Default: false, KeyType: 18},
		// brin/name_minmax_ops
		{OID: 9092, Method: amBrin, Name: "name_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4065, IntType: 19, Default: true, KeyType: 19},
		// brin/name_bloom_ops
		{OID: 9093, Method: amBrin, Name: "name_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4579, IntType: 19, Default: false, KeyType: 19},
		// brin/int8_minmax_ops
		{OID: 9094, Method: amBrin, Name: "int8_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4054, IntType: 20, Default: true, KeyType: 20},
		// brin/int8_minmax_multi_ops
		{OID: 9095, Method: amBrin, Name: "int8_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4602, IntType: 20, Default: false, KeyType: 20},
		// brin/int8_bloom_ops
		{OID: 9096, Method: amBrin, Name: "int8_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4572, IntType: 20, Default: false, KeyType: 20},
		// brin/int2_minmax_ops
		{OID: 9097, Method: amBrin, Name: "int2_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4054, IntType: 21, Default: true, KeyType: 21},
		// brin/int2_minmax_multi_ops
		{OID: 9098, Method: amBrin, Name: "int2_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4602, IntType: 21, Default: false, KeyType: 21},
		// brin/int2_bloom_ops
		{OID: 9099, Method: amBrin, Name: "int2_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4572, IntType: 21, Default: false, KeyType: 21},
		// brin/int4_minmax_ops
		{OID: 9100, Method: amBrin, Name: "int4_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4054, IntType: 23, Default: true, KeyType: 23},
		// brin/int4_minmax_multi_ops
		{OID: 9101, Method: amBrin, Name: "int4_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4602, IntType: 23, Default: false, KeyType: 23},
		// brin/int4_bloom_ops
		{OID: 9102, Method: amBrin, Name: "int4_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4572, IntType: 23, Default: false, KeyType: 23},
		// brin/text_minmax_ops
		{OID: 9103, Method: amBrin, Name: "text_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4056, IntType: 25, Default: true, KeyType: 25},
		// brin/text_bloom_ops
		{OID: 9104, Method: amBrin, Name: "text_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4573, IntType: 25, Default: false, KeyType: 25},
		// brin/oid_minmax_ops
		{OID: 9105, Method: amBrin, Name: "oid_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4068, IntType: 26, Default: true, KeyType: 26},
		// brin/oid_minmax_multi_ops
		{OID: 9106, Method: amBrin, Name: "oid_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4606, IntType: 26, Default: false, KeyType: 26},
		// brin/oid_bloom_ops
		{OID: 9107, Method: amBrin, Name: "oid_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4580, IntType: 26, Default: false, KeyType: 26},
		// brin/tid_minmax_ops
		{OID: 9108, Method: amBrin, Name: "tid_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4069, IntType: 27, Default: true, KeyType: 27},
		// brin/tid_bloom_ops
		{OID: 9109, Method: amBrin, Name: "tid_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4581, IntType: 27, Default: false, KeyType: 27},
		// brin/tid_minmax_multi_ops
		{OID: 9110, Method: amBrin, Name: "tid_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4607, IntType: 27, Default: false, KeyType: 27},
		// brin/float4_minmax_ops
		{OID: 9111, Method: amBrin, Name: "float4_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4070, IntType: 700, Default: true, KeyType: 700},
		// brin/float4_minmax_multi_ops
		{OID: 9112, Method: amBrin, Name: "float4_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4608, IntType: 700, Default: false, KeyType: 700},
		// brin/float4_bloom_ops
		{OID: 9113, Method: amBrin, Name: "float4_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4582, IntType: 700, Default: false, KeyType: 700},
		// brin/float8_minmax_ops
		{OID: 9114, Method: amBrin, Name: "float8_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4070, IntType: 701, Default: true, KeyType: 701},
		// brin/float8_minmax_multi_ops
		{OID: 9115, Method: amBrin, Name: "float8_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4608, IntType: 701, Default: false, KeyType: 701},
		// brin/float8_bloom_ops
		{OID: 9116, Method: amBrin, Name: "float8_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4582, IntType: 701, Default: false, KeyType: 701},
		// brin/macaddr_minmax_ops
		{OID: 9117, Method: amBrin, Name: "macaddr_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4074, IntType: 829, Default: true, KeyType: 829},
		// brin/macaddr_minmax_multi_ops
		{OID: 9118, Method: amBrin, Name: "macaddr_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4609, IntType: 829, Default: false, KeyType: 829},
		// brin/macaddr_bloom_ops
		{OID: 9119, Method: amBrin, Name: "macaddr_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4583, IntType: 829, Default: false, KeyType: 829},
		// brin/macaddr8_minmax_ops
		{OID: 9120, Method: amBrin, Name: "macaddr8_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4109, IntType: 774, Default: true, KeyType: 774},
		// brin/macaddr8_minmax_multi_ops
		{OID: 9121, Method: amBrin, Name: "macaddr8_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4610, IntType: 774, Default: false, KeyType: 774},
		// brin/macaddr8_bloom_ops
		{OID: 9122, Method: amBrin, Name: "macaddr8_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4584, IntType: 774, Default: false, KeyType: 774},
		// brin/inet_minmax_ops (default=false: inet_inclusion_ops is default for inet brin)
		{OID: 9123, Method: amBrin, Name: "inet_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4075, IntType: 869, Default: false, KeyType: 869},
		// brin/inet_minmax_multi_ops
		{OID: 9124, Method: amBrin, Name: "inet_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4611, IntType: 869, Default: false, KeyType: 869},
		// brin/inet_bloom_ops
		{OID: 9125, Method: amBrin, Name: "inet_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4585, IntType: 869, Default: false, KeyType: 869},
		// brin/inet_inclusion_ops
		{OID: 9126, Method: amBrin, Name: "inet_inclusion_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4102, IntType: 869, Default: true, KeyType: 869},
		// brin/bpchar_minmax_ops
		{OID: 9127, Method: amBrin, Name: "bpchar_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4076, IntType: 1042, Default: true, KeyType: 1042},
		// brin/bpchar_bloom_ops
		{OID: 9128, Method: amBrin, Name: "bpchar_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4586, IntType: 1042, Default: false, KeyType: 1042},
		// brin/time_minmax_ops
		{OID: 9129, Method: amBrin, Name: "time_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4077, IntType: 1083, Default: true, KeyType: 1083},
		// brin/time_minmax_multi_ops
		{OID: 9130, Method: amBrin, Name: "time_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4612, IntType: 1083, Default: false, KeyType: 1083},
		// brin/time_bloom_ops
		{OID: 9131, Method: amBrin, Name: "time_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4587, IntType: 1083, Default: false, KeyType: 1083},
		// brin/date_minmax_ops
		{OID: 9132, Method: amBrin, Name: "date_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4059, IntType: 1082, Default: true, KeyType: 1082},
		// brin/date_minmax_multi_ops
		{OID: 9133, Method: amBrin, Name: "date_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4605, IntType: 1082, Default: false, KeyType: 1082},
		// brin/date_bloom_ops
		{OID: 9134, Method: amBrin, Name: "date_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4576, IntType: 1082, Default: false, KeyType: 1082},
		// brin/timestamp_minmax_ops
		{OID: 9135, Method: amBrin, Name: "timestamp_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4059, IntType: 1114, Default: true, KeyType: 1114},
		// brin/timestamp_minmax_multi_ops
		{OID: 9136, Method: amBrin, Name: "timestamp_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4605, IntType: 1114, Default: false, KeyType: 1114},
		// brin/timestamp_bloom_ops
		{OID: 9137, Method: amBrin, Name: "timestamp_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4576, IntType: 1114, Default: false, KeyType: 1114},
		// brin/timestamptz_minmax_ops
		{OID: 9138, Method: amBrin, Name: "timestamptz_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4059, IntType: 1184, Default: true, KeyType: 1184},
		// brin/timestamptz_minmax_multi_ops
		{OID: 9139, Method: amBrin, Name: "timestamptz_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4605, IntType: 1184, Default: false, KeyType: 1184},
		// brin/timestamptz_bloom_ops
		{OID: 9140, Method: amBrin, Name: "timestamptz_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4576, IntType: 1184, Default: false, KeyType: 1184},
		// brin/interval_minmax_ops
		{OID: 9141, Method: amBrin, Name: "interval_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4078, IntType: 1186, Default: true, KeyType: 1186},
		// brin/interval_minmax_multi_ops
		{OID: 9142, Method: amBrin, Name: "interval_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4613, IntType: 1186, Default: false, KeyType: 1186},
		// brin/interval_bloom_ops
		{OID: 9143, Method: amBrin, Name: "interval_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4588, IntType: 1186, Default: false, KeyType: 1186},
		// brin/timetz_minmax_ops
		{OID: 9144, Method: amBrin, Name: "timetz_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4058, IntType: 1266, Default: true, KeyType: 1266},
		// brin/timetz_minmax_multi_ops
		{OID: 9145, Method: amBrin, Name: "timetz_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4604, IntType: 1266, Default: false, KeyType: 1266},
		// brin/timetz_bloom_ops
		{OID: 9146, Method: amBrin, Name: "timetz_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4575, IntType: 1266, Default: false, KeyType: 1266},
		// brin/bit_minmax_ops
		{OID: 9147, Method: amBrin, Name: "bit_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4079, IntType: 1560, Default: true, KeyType: 1560},
		// brin/varbit_minmax_ops
		{OID: 9148, Method: amBrin, Name: "varbit_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4080, IntType: 1562, Default: true, KeyType: 1562},
		// brin/numeric_minmax_ops
		{OID: 9149, Method: amBrin, Name: "numeric_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4055, IntType: 1700, Default: true, KeyType: 1700},
		// brin/numeric_minmax_multi_ops
		{OID: 9150, Method: amBrin, Name: "numeric_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4603, IntType: 1700, Default: false, KeyType: 1700},
		// brin/numeric_bloom_ops
		{OID: 9151, Method: amBrin, Name: "numeric_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4574, IntType: 1700, Default: false, KeyType: 1700},
		// brin/uuid_minmax_ops
		{OID: 9152, Method: amBrin, Name: "uuid_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4081, IntType: 2950, Default: true, KeyType: 2950},
		// brin/uuid_minmax_multi_ops
		{OID: 9153, Method: amBrin, Name: "uuid_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4614, IntType: 2950, Default: false, KeyType: 2950},
		// brin/uuid_bloom_ops
		{OID: 9154, Method: amBrin, Name: "uuid_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4589, IntType: 2950, Default: false, KeyType: 2950},
		// brin/range_inclusion_ops (opcintype=anyrange 3831)
		{OID: 9155, Method: amBrin, Name: "range_inclusion_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4103, IntType: 3831, Default: true, KeyType: 3831},
		// brin/pg_lsn_minmax_ops
		{OID: 9156, Method: amBrin, Name: "pg_lsn_minmax_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4082, IntType: 3220, Default: true, KeyType: 3220},
		// brin/pg_lsn_minmax_multi_ops
		{OID: 9157, Method: amBrin, Name: "pg_lsn_minmax_multi_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4615, IntType: 3220, Default: false, KeyType: 3220},
		// brin/pg_lsn_bloom_ops
		{OID: 9158, Method: amBrin, Name: "pg_lsn_bloom_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4590, IntType: 3220, Default: false, KeyType: 3220},
		// brin/box_inclusion_ops
		{OID: 9159, Method: amBrin, Name: "box_inclusion_ops", Namespace: nsPGCatalog, Owner: ownerSuper, Family: 4104, IntType: 603, Default: true, KeyType: 603},
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
func bootstrapPgOpclassTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgOpclassColDefs()
	entries := pgOpclassInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgOpclassRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "2616", cols, rows)
	if err != nil {
		return nil, err
	}
	tidMap := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		tidMap[e.OID] = rawTIDs[i]
	}
	return tidMap, nil
}

// pgAmopEntry mirrors one row of PG18's pg_amop.dat — see
// `postgres/src/include/catalog/pg_amop.dat` and the
// `FormData_pg_amop` struct in `pg_amop.h`. goopg only seeds
// the default (lefttype = righttype = opcintype) strategy
// operators for the btree opclasses pinned in
// pgOpclassInitialEntries; cross-type entries are out of scope.
type pgAmopEntry struct {
	OID        uint32 // amop OID
	Family     uint32 // amopfamily — pg_opfamily OID
	LeftType   uint32 // amoplefttype — pg_type OID
	RightType  uint32 // amoprighttype — pg_type OID
	Strategy   int16  // amopstrategy — 1..5 for btree
	Purpose    byte   // amoppurpose — 's' (search) or 'o' (order)
	Operator   uint32 // amopopr — pg_operator OID
	Method     uint32 // amopmethod — pg_am OID (403 = btree)
	SortFamily uint32 // amopsortfamily — 0 for search ops
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
		amBtree  uint32 = 403
		amHash   uint32 = 405
		amGist   uint32 = 783
		amGin    uint32 = 2742
		amSpgist uint32 = 4000
		amBrin   uint32 = 3580
		// purposeSearch is the default amoppurpose for index strategy operators.
		// purposeOrder marks distance (ordering) operators such as <->.
		purposeSearch byte = 's'
		purposeOrder  byte = 'o'
	)
	// Synthetic OIDs: PG assigns these at initdb time; we pin contiguous
	// ranges starting from baseOID so pg_amop_oid_index can be heap-rebuilt.
	const baseOID uint32 = 7000
	out := make([]pgAmopEntry, 0, 945)
	// addPair emits 5 strategy rows (1..5) for one (family, left, right, am) triple.
	addPair := func(family, lefttype, righttype, method uint32, ops [5]uint32) {
		for i := 0; i < 5; i++ {
			out = append(out, pgAmopEntry{
				OID:        baseOID + uint32(len(out)),
				Family:     family,
				LeftType:   lefttype,
				RightType:  righttype,
				Strategy:   int16(i + 1),
				Purpose:    purposeSearch,
				Operator:   ops[i],
				Method:     method,
				SortFamily: 0,
			})
		}
	}
	// amOp emits one search-purpose row for non-btree or non-5-strategy entries.
	amOp := func(family, lefttype, righttype uint32, strategy int16, operator, method uint32) {
		out = append(out, pgAmopEntry{
			OID:        baseOID + uint32(len(out)),
			Family:     family,
			LeftType:   lefttype,
			RightType:  righttype,
			Strategy:   strategy,
			Purpose:    purposeSearch,
			Operator:   operator,
			Method:     method,
			SortFamily: 0,
		})
	}
	// amOpOrder emits one ordering-purpose row (amoppurpose='o') used by
	// KNN index scans; amopsortfamily is the btree family for the sort key.
	amOpOrder := func(family, lefttype, righttype uint32, strategy int16, operator, method, sortFamily uint32) {
		out = append(out, pgAmopEntry{
			OID:        baseOID + uint32(len(out)),
			Family:     family,
			LeftType:   lefttype,
			RightType:  righttype,
			Strategy:   strategy,
			Purpose:    purposeOrder,
			Operator:   operator,
			Method:     method,
			SortFamily: sortFamily,
		})
	}

	// family=1976 (integer_ops) btree
	addPair(1976, 23, 23, amBtree, [5]uint32{97, 523, 96, 525, 521})        // int4 x int4
	addPair(1976, 23, 21, amBtree, [5]uint32{535, 541, 533, 543, 537})      // int4 x int2
	addPair(1976, 23, 20, amBtree, [5]uint32{37, 80, 15, 82, 76})           // int4 x int8
	addPair(1976, 21, 21, amBtree, [5]uint32{95, 522, 94, 524, 520})        // int2 x int2
	addPair(1976, 21, 23, amBtree, [5]uint32{534, 540, 532, 542, 536})      // int2 x int4
	addPair(1976, 21, 20, amBtree, [5]uint32{1864, 1866, 1862, 1867, 1865}) // int2 x int8
	addPair(1976, 20, 20, amBtree, [5]uint32{412, 414, 410, 415, 413})      // int8 x int8
	addPair(1976, 20, 21, amBtree, [5]uint32{1870, 1872, 1868, 1873, 1871}) // int8 x int2
	addPair(1976, 20, 23, amBtree, [5]uint32{418, 420, 416, 430, 419})      // int8 x int4

	// family=1989 (oid_ops) btree
	addPair(1989, 26, 26, amBtree, [5]uint32{609, 611, 607, 612, 610}) // oid x oid

	// family=5067 (xid8_ops) btree
	addPair(5067, 5069, 5069, amBtree, [5]uint32{5073, 5075, 5068, 5076, 5074}) // xid8 x xid8

	// family=2789 (tid_ops) btree
	addPair(2789, 27, 27, amBtree, [5]uint32{2799, 2801, 387, 2802, 2800}) // tid x tid

	// family=1991 (oidvector_ops) btree
	addPair(1991, 30, 30, amBtree, [5]uint32{645, 647, 649, 648, 646}) // oidvector x oidvector

	// family=1970 (float_ops) btree
	addPair(1970, 700, 700, amBtree, [5]uint32{622, 624, 620, 625, 623})      // float4 x float4
	addPair(1970, 700, 701, amBtree, [5]uint32{1122, 1124, 1120, 1125, 1123}) // float4 x float8
	addPair(1970, 701, 701, amBtree, [5]uint32{672, 673, 670, 675, 674})      // float8 x float8
	addPair(1970, 701, 700, amBtree, [5]uint32{1132, 1134, 1130, 1135, 1133}) // float8 x float4

	// family=429 (char_ops) btree
	addPair(429, 18, 18, amBtree, [5]uint32{631, 632, 92, 634, 633}) // char x char

	// family=1994 (text_ops) btree
	addPair(1994, 25, 25, amBtree, [5]uint32{664, 665, 98, 667, 666})  // text x text
	addPair(1994, 19, 19, amBtree, [5]uint32{660, 661, 93, 663, 662})  // name x name
	addPair(1994, 19, 25, amBtree, [5]uint32{255, 256, 254, 257, 258}) // name x text
	addPair(1994, 25, 19, amBtree, [5]uint32{261, 262, 260, 263, 264}) // text x name

	// family=426 (bpchar_ops) btree
	addPair(426, 1042, 1042, amBtree, [5]uint32{1058, 1059, 1054, 1061, 1060}) // bpchar x bpchar

	// family=428 (bytea_ops) btree
	addPair(428, 17, 17, amBtree, [5]uint32{1957, 1958, 1955, 1960, 1959}) // bytea x bytea

	// family=434 (datetime_ops) btree
	addPair(434, 1082, 1082, amBtree, [5]uint32{1095, 1096, 1093, 1098, 1097}) // date x date
	addPair(434, 1082, 1114, amBtree, [5]uint32{2345, 2346, 2347, 2348, 2349}) // date x timestamp
	addPair(434, 1082, 1184, amBtree, [5]uint32{2358, 2359, 2360, 2361, 2362}) // date x timestamptz
	addPair(434, 1114, 1114, amBtree, [5]uint32{2062, 2063, 2060, 2065, 2064}) // timestamp x timestamp
	addPair(434, 1114, 1082, amBtree, [5]uint32{2371, 2372, 2373, 2374, 2375}) // timestamp x date
	addPair(434, 1114, 1184, amBtree, [5]uint32{2534, 2535, 2536, 2537, 2538}) // timestamp x timestamptz
	addPair(434, 1184, 1184, amBtree, [5]uint32{1322, 1323, 1320, 1325, 1324}) // timestamptz x timestamptz
	addPair(434, 1184, 1082, amBtree, [5]uint32{2384, 2385, 2386, 2387, 2388}) // timestamptz x date
	addPair(434, 1184, 1114, amBtree, [5]uint32{2540, 2541, 2542, 2543, 2544}) // timestamptz x timestamp

	// family=1996 (time_ops) btree
	addPair(1996, 1083, 1083, amBtree, [5]uint32{1110, 1111, 1108, 1113, 1112}) // time x time

	// family=2000 (timetz_ops) btree
	addPair(2000, 1266, 1266, amBtree, [5]uint32{1552, 1553, 1550, 1555, 1554}) // timetz x timetz

	// family=1982 (interval_ops) btree
	addPair(1982, 1186, 1186, amBtree, [5]uint32{1332, 1333, 1330, 1335, 1334}) // interval x interval

	// family=1984 (macaddr_ops) btree
	addPair(1984, 829, 829, amBtree, [5]uint32{1222, 1223, 1220, 1225, 1224}) // macaddr x macaddr

	// family=3371 (macaddr8_ops) btree
	addPair(3371, 774, 774, amBtree, [5]uint32{3364, 3365, 3362, 3367, 3366}) // macaddr8 x macaddr8

	// family=1974 (network_ops) btree
	addPair(1974, 869, 869, amBtree, [5]uint32{1203, 1204, 1201, 1206, 1205}) // inet x inet

	// family=1988 (numeric_ops) btree
	addPair(1988, 1700, 1700, amBtree, [5]uint32{1754, 1755, 1752, 1757, 1756}) // numeric x numeric

	// family=424 (bool_ops) btree
	addPair(424, 16, 16, amBtree, [5]uint32{58, 1694, 91, 1695, 59}) // bool x bool

	// family=423 (bit_ops) btree
	addPair(423, 1560, 1560, amBtree, [5]uint32{1786, 1788, 1784, 1789, 1787}) // bit x bit

	// family=2002 (varbit_ops) btree
	addPair(2002, 1562, 1562, amBtree, [5]uint32{1806, 1808, 1804, 1809, 1807}) // varbit x varbit

	// family=2095 (text_pattern_ops) btree
	addPair(2095, 25, 25, amBtree, [5]uint32{2314, 2315, 98, 2317, 2318}) // text x text

	// family=2097 (bpchar_pattern_ops) btree
	addPair(2097, 1042, 1042, amBtree, [5]uint32{2326, 2327, 1054, 2329, 2330}) // bpchar x bpchar

	// family=2099 (money_ops) btree
	addPair(2099, 790, 790, amBtree, [5]uint32{902, 904, 900, 905, 903}) // money x money

	// family=397 (array_ops) btree
	addPair(397, 2277, 2277, amBtree, [5]uint32{1072, 1074, 1070, 1075, 1073}) // anyarray x anyarray

	// family=2994 (record_ops) btree
	addPair(2994, 2249, 2249, amBtree, [5]uint32{2990, 2992, 2988, 2993, 2991}) // record x record

	// family=3194 (record_image_ops) btree
	addPair(3194, 2249, 2249, amBtree, [5]uint32{3190, 3192, 3188, 3193, 3191}) // record x record

	// family=2968 (uuid_ops) btree
	addPair(2968, 2950, 2950, amBtree, [5]uint32{2974, 2976, 2972, 2977, 2975}) // uuid x uuid

	// family=3253 (pg_lsn_ops) btree
	addPair(3253, 3220, 3220, amBtree, [5]uint32{3224, 3226, 3222, 3227, 3225}) // pg_lsn x pg_lsn

	// family=3522 (enum_ops) btree
	addPair(3522, 3500, 3500, amBtree, [5]uint32{3518, 3520, 3516, 3521, 3519}) // anyenum x anyenum

	// family=3626 (tsvector_ops) btree
	addPair(3626, 3614, 3614, amBtree, [5]uint32{3627, 3628, 3629, 3631, 3632}) // tsvector x tsvector

	// family=3683 (tsquery_ops) btree
	addPair(3683, 3615, 3615, amBtree, [5]uint32{3674, 3675, 3676, 3678, 3679}) // tsquery x tsquery

	// family=3901 (range_ops) btree
	addPair(3901, 3831, 3831, amBtree, [5]uint32{3884, 3885, 3882, 3886, 3887}) // anyrange x anyrange

	// family=4199 (multirange_ops) btree
	addPair(4199, 4537, 4537, amBtree, [5]uint32{2862, 2863, 2860, 2864, 2865}) // anymultirange x anymultirange

	// family=4033 (jsonb_ops) btree
	addPair(4033, 3802, 3802, amBtree, [5]uint32{3242, 3244, 3240, 3245, 3243}) // jsonb x jsonb

	// family=427 (bpchar_ops) hash
	amOp(427, 1042, 1042, 1, 1054, amHash) // strat=1 =(bpchar,bpchar) (bpchar x bpchar)

	// family=431 (char_ops) hash
	amOp(431, 18, 18, 1, 92, amHash) // strat=1 =(char,char) (char x char)

	// family=435 (date_ops) hash
	amOp(435, 1082, 1082, 1, 1093, amHash) // strat=1 =(date,date) (date x date)

	// family=1971 (float_ops) hash
	amOp(1971, 700, 700, 1, 620, amHash)  // strat=1 =(float4,float4) (float4 x float4)
	amOp(1971, 701, 701, 1, 670, amHash)  // strat=1 =(float8,float8) (float8 x float8)
	amOp(1971, 700, 701, 1, 1120, amHash) // strat=1 =(float4,float8) (float4 x float8)
	amOp(1971, 701, 700, 1, 1130, amHash) // strat=1 =(float8,float4) (float8 x float4)

	// family=1975 (network_ops) hash
	amOp(1975, 869, 869, 1, 1201, amHash) // strat=1 =(inet,inet) (inet x inet)

	// family=1977 (integer_ops) hash
	amOp(1977, 21, 21, 1, 94, amHash)   // strat=1 =(int2,int2) (int2 x int2)
	amOp(1977, 23, 23, 1, 96, amHash)   // strat=1 =(int4,int4) (int4 x int4)
	amOp(1977, 20, 20, 1, 410, amHash)  // strat=1 =(int8,int8) (int8 x int8)
	amOp(1977, 21, 23, 1, 532, amHash)  // strat=1 =(int2,int4) (int2 x int4)
	amOp(1977, 21, 20, 1, 1862, amHash) // strat=1 =(int2,int8) (int2 x int8)
	amOp(1977, 23, 21, 1, 533, amHash)  // strat=1 =(int4,int2) (int4 x int2)
	amOp(1977, 23, 20, 1, 15, amHash)   // strat=1 =(int4,int8) (int4 x int8)
	amOp(1977, 20, 21, 1, 1868, amHash) // strat=1 =(int8,int2) (int8 x int2)
	amOp(1977, 20, 23, 1, 416, amHash)  // strat=1 =(int8,int4) (int8 x int4)

	// family=1983 (interval_ops) hash
	amOp(1983, 1186, 1186, 1, 1330, amHash) // strat=1 =(interval,interval) (interval x interval)

	// family=1985 (macaddr_ops) hash
	amOp(1985, 829, 829, 1, 1220, amHash) // strat=1 =(macaddr,macaddr) (macaddr x macaddr)

	// family=3372 (macaddr8_ops) hash
	amOp(3372, 774, 774, 1, 3362, amHash) // strat=1 =(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=1990 (oid_ops) hash
	amOp(1990, 26, 26, 1, 607, amHash) // strat=1 =(oid,oid) (oid x oid)

	// family=1992 (oidvector_ops) hash
	amOp(1992, 30, 30, 1, 649, amHash) // strat=1 =(oidvector,oidvector) (oidvector x oidvector)

	// family=6194 (record_ops) hash
	amOp(6194, 2249, 2249, 1, 2988, amHash) // strat=1 =(record,record) (record x record)

	// family=1995 (text_ops) hash
	amOp(1995, 25, 25, 1, 98, amHash)  // strat=1 =(text,text) (text x text)
	amOp(1995, 19, 19, 1, 93, amHash)  // strat=1 =(name,name) (name x name)
	amOp(1995, 19, 25, 1, 254, amHash) // strat=1 =(name,text) (name x text)
	amOp(1995, 25, 19, 1, 260, amHash) // strat=1 =(text,name) (text x name)

	// family=1997 (time_ops) hash
	amOp(1997, 1083, 1083, 1, 1108, amHash) // strat=1 =(time,time) (time x time)

	// family=1999 (timestamptz_ops) hash
	amOp(1999, 1184, 1184, 1, 1320, amHash) // strat=1 =(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=2001 (timetz_ops) hash
	amOp(2001, 1266, 1266, 1, 1550, amHash) // strat=1 =(timetz,timetz) (timetz x timetz)

	// family=2040 (timestamp_ops) hash
	amOp(2040, 1114, 1114, 1, 2060, amHash) // strat=1 =(timestamp,timestamp) (timestamp x timestamp)

	// family=2222 (bool_ops) hash
	amOp(2222, 16, 16, 1, 91, amHash) // strat=1 =(bool,bool) (bool x bool)

	// family=2223 (bytea_ops) hash
	amOp(2223, 17, 17, 1, 1955, amHash) // strat=1 =(bytea,bytea) (bytea x bytea)

	// family=2225 (xid_ops) hash
	amOp(2225, 28, 28, 1, 352, amHash) // strat=1 =(xid,xid) (xid x xid)

	// family=5032 (xid8_ops) hash
	amOp(5032, 5069, 5069, 1, 5068, amHash) // strat=1 =(xid8,xid8) (xid8 x xid8)

	// family=2226 (cid_ops) hash
	amOp(2226, 29, 29, 1, 385, amHash) // strat=1 =(cid,cid) (cid x cid)

	// family=2227 (tid_ops) hash
	amOp(2227, 27, 27, 1, 387, amHash) // strat=1 =(tid,tid) (tid x tid)

	// family=2229 (text_pattern_ops) hash
	amOp(2229, 25, 25, 1, 98, amHash) // strat=1 =(text,text) (text x text)

	// family=2231 (bpchar_pattern_ops) hash
	amOp(2231, 1042, 1042, 1, 1054, amHash) // strat=1 =(bpchar,bpchar) (bpchar x bpchar)

	// family=2235 (aclitem_ops) hash
	amOp(2235, 1033, 1033, 1, 974, amHash) // strat=1 =(aclitem,aclitem) (aclitem x aclitem)

	// family=2969 (uuid_ops) hash
	amOp(2969, 2950, 2950, 1, 2972, amHash) // strat=1 =(uuid,uuid) (uuid x uuid)

	// family=3254 (pg_lsn_ops) hash
	amOp(3254, 3220, 3220, 1, 3222, amHash) // strat=1 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=1998 (numeric_ops) hash
	amOp(1998, 1700, 1700, 1, 1752, amHash) // strat=1 =(numeric,numeric) (numeric x numeric)

	// family=627 (array_ops) hash
	amOp(627, 2277, 2277, 1, 1070, amHash) // strat=1 =(anyarray,anyarray) (anyarray x anyarray)

	// family=2593 (box_ops) gist
	amOp(2593, 603, 603, 1, 493, amGist)             // strat=1 <<(box,box) (box x box)
	amOp(2593, 603, 603, 2, 494, amGist)             // strat=2 &<(box,box) (box x box)
	amOp(2593, 603, 603, 3, 500, amGist)             // strat=3 &&(box,box) (box x box)
	amOp(2593, 603, 603, 4, 495, amGist)             // strat=4 &>(box,box) (box x box)
	amOp(2593, 603, 603, 5, 496, amGist)             // strat=5 >>(box,box) (box x box)
	amOp(2593, 603, 603, 6, 499, amGist)             // strat=6 ~=(box,box) (box x box)
	amOp(2593, 603, 603, 7, 498, amGist)             // strat=7 @>(box,box) (box x box)
	amOp(2593, 603, 603, 8, 497, amGist)             // strat=8 <@(box,box) (box x box)
	amOp(2593, 603, 603, 9, 2571, amGist)            // strat=9 &<|(box,box) (box x box)
	amOp(2593, 603, 603, 10, 2570, amGist)           // strat=10 <<|(box,box) (box x box)
	amOp(2593, 603, 603, 11, 2573, amGist)           // strat=11 |>>(box,box) (box x box)
	amOp(2593, 603, 603, 12, 2572, amGist)           // strat=12 |&>(box,box) (box x box)
	amOpOrder(2593, 603, 600, 15, 606, amGist, 1970) // strat=15 <->(box,point) (box x point)

	// family=1029 (point_ops) gist
	amOp(1029, 600, 600, 11, 4161, amGist)           // strat=11 |>>(point,point) (point x point)
	amOp(1029, 600, 600, 30, 506, amGist)            // strat=30 >^(point,point) (point x point)
	amOp(1029, 600, 600, 1, 507, amGist)             // strat=1 <<(point,point) (point x point)
	amOp(1029, 600, 600, 5, 508, amGist)             // strat=5 >>(point,point) (point x point)
	amOp(1029, 600, 600, 10, 4162, amGist)           // strat=10 <<|(point,point) (point x point)
	amOp(1029, 600, 600, 29, 509, amGist)            // strat=29 <^(point,point) (point x point)
	amOp(1029, 600, 600, 6, 510, amGist)             // strat=6 ~=(point,point) (point x point)
	amOpOrder(1029, 600, 600, 15, 517, amGist, 1970) // strat=15 <->(point,point) (point x point)
	amOp(1029, 600, 603, 28, 511, amGist)            // strat=28 <@(point,box) (point x box)
	amOp(1029, 600, 604, 48, 756, amGist)            // strat=48 <@(point,polygon) (point x polygon)
	amOp(1029, 600, 718, 68, 758, amGist)            // strat=68 <@(point,circle) (point x circle)

	// family=2594 (poly_ops) gist
	amOp(2594, 604, 604, 1, 485, amGist)              // strat=1 <<(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 2, 486, amGist)              // strat=2 &<(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 3, 492, amGist)              // strat=3 &&(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 4, 487, amGist)              // strat=4 &>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 5, 488, amGist)              // strat=5 >>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 6, 491, amGist)              // strat=6 ~=(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 7, 490, amGist)              // strat=7 @>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 8, 489, amGist)              // strat=8 <@(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 9, 2575, amGist)             // strat=9 &<|(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 10, 2574, amGist)            // strat=10 <<|(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 11, 2577, amGist)            // strat=11 |>>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 12, 2576, amGist)            // strat=12 |&>(polygon,polygon) (polygon x polygon)
	amOpOrder(2594, 604, 600, 15, 3289, amGist, 1970) // strat=15 <->(polygon,point) (polygon x point)

	// family=2595 (circle_ops) gist
	amOp(2595, 718, 718, 1, 1506, amGist)             // strat=1 <<(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 2, 1507, amGist)             // strat=2 &<(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 3, 1513, amGist)             // strat=3 &&(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 4, 1508, amGist)             // strat=4 &>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 5, 1509, amGist)             // strat=5 >>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 6, 1512, amGist)             // strat=6 ~=(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 7, 1511, amGist)             // strat=7 @>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 8, 1510, amGist)             // strat=8 <@(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 9, 2589, amGist)             // strat=9 &<|(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 10, 1515, amGist)            // strat=10 <<|(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 11, 1514, amGist)            // strat=11 |>>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 12, 2590, amGist)            // strat=12 |&>(circle,circle) (circle x circle)
	amOpOrder(2595, 718, 600, 15, 3291, amGist, 1970) // strat=15 <->(circle,point) (circle x point)

	// family=2745 (array_ops) gin
	amOp(2745, 2277, 2277, 1, 2750, amGin) // strat=1 &&(anyarray,anyarray) (anyarray x anyarray)
	amOp(2745, 2277, 2277, 2, 2751, amGin) // strat=2 @>(anyarray,anyarray) (anyarray x anyarray)
	amOp(2745, 2277, 2277, 3, 2752, amGin) // strat=3 <@(anyarray,anyarray) (anyarray x anyarray)
	amOp(2745, 2277, 2277, 4, 1070, amGin) // strat=4 =(anyarray,anyarray) (anyarray x anyarray)

	// family=3523 (enum_ops) hash
	amOp(3523, 3500, 3500, 1, 3516, amHash) // strat=1 =(anyenum,anyenum) (anyenum x anyenum)

	// family=3655 (tsvector_ops) gist
	amOp(3655, 3614, 3615, 1, 3636, amGist) // strat=1 @@(tsvector,tsquery) (tsvector x tsquery)

	// family=3659 (tsvector_ops) gin
	amOp(3659, 3614, 3615, 1, 3636, amGin) // strat=1 @@(tsvector,tsquery) (tsvector x tsquery)
	amOp(3659, 3614, 3615, 2, 3660, amGin) // strat=2 @@@(tsvector,tsquery) (tsvector x tsquery)

	// family=3702 (tsquery_ops) gist
	amOp(3702, 3615, 3615, 7, 3693, amGist) // strat=7 @>(tsquery,tsquery) (tsquery x tsquery)
	amOp(3702, 3615, 3615, 8, 3694, amGist) // strat=8 <@(tsquery,tsquery) (tsquery x tsquery)

	// family=3903 (range_ops) hash
	amOp(3903, 3831, 3831, 1, 3882, amHash) // strat=1 =(anyrange,anyrange) (anyrange x anyrange)

	// family=3919 (range_ops) gist
	amOp(3919, 3831, 3831, 1, 3893, amGist)  // strat=1 <<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 1, 4395, amGist)  // strat=1 <<(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 2, 3895, amGist)  // strat=2 &<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 2, 2875, amGist)  // strat=2 &<(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 3, 3888, amGist)  // strat=3 &&(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 3, 2866, amGist)  // strat=3 &&(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 4, 3896, amGist)  // strat=4 &>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 4, 3585, amGist)  // strat=4 &>(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 5, 3894, amGist)  // strat=5 >>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 5, 4398, amGist)  // strat=5 >>(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 6, 3897, amGist)  // strat=6 -|-(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 6, 4179, amGist)  // strat=6 -|-(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 7, 3890, amGist)  // strat=7 @>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 7, 4539, amGist)  // strat=7 @>(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 8, 3892, amGist)  // strat=8 <@(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 8, 2873, amGist)  // strat=8 <@(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 2283, 16, 3889, amGist) // strat=16 @>(anyrange,anyelement) (anyrange x anyelement)
	amOp(3919, 3831, 3831, 18, 3882, amGist) // strat=18 =(anyrange,anyrange) (anyrange x anyrange)

	// family=6158 (multirange_ops) gist
	amOp(6158, 4537, 4537, 1, 4397, amGist)  // strat=1 <<(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 1, 4396, amGist)  // strat=1 <<(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 2, 2877, amGist)  // strat=2 &<(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 2, 2876, amGist)  // strat=2 &<(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 3, 2868, amGist)  // strat=3 &&(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 3, 2867, amGist)  // strat=3 &&(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 4, 4142, amGist)  // strat=4 &>(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 4, 4035, amGist)  // strat=4 &>(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 5, 4400, amGist)  // strat=5 >>(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 5, 4399, amGist)  // strat=5 >>(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 6, 4198, amGist)  // strat=6 -|-(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 6, 4180, amGist)  // strat=6 -|-(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 7, 2871, amGist)  // strat=7 @>(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 7, 2870, amGist)  // strat=7 @>(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 8, 2874, amGist)  // strat=8 <@(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 8, 4540, amGist)  // strat=8 <@(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 2283, 16, 2869, amGist) // strat=16 @>(anymultirange,anyelement) (anymultirange x anyelement)
	amOp(6158, 4537, 4537, 18, 2860, amGist) // strat=18 =(anymultirange,anymultirange) (anymultirange x anymultirange)

	// family=4225 (multirange_ops) hash
	amOp(4225, 4537, 4537, 1, 2860, amHash) // strat=1 =(anymultirange,anymultirange) (anymultirange x anymultirange)

	// family=4015 (quad_point_ops) spgist
	amOp(4015, 600, 600, 11, 4161, amSpgist)           // strat=11 |>>(point,point) (point x point)
	amOp(4015, 600, 600, 30, 506, amSpgist)            // strat=30 >^(point,point) (point x point)
	amOp(4015, 600, 600, 1, 507, amSpgist)             // strat=1 <<(point,point) (point x point)
	amOp(4015, 600, 600, 5, 508, amSpgist)             // strat=5 >>(point,point) (point x point)
	amOp(4015, 600, 600, 10, 4162, amSpgist)           // strat=10 <<|(point,point) (point x point)
	amOp(4015, 600, 600, 29, 509, amSpgist)            // strat=29 <^(point,point) (point x point)
	amOp(4015, 600, 600, 6, 510, amSpgist)             // strat=6 ~=(point,point) (point x point)
	amOp(4015, 600, 603, 8, 511, amSpgist)             // strat=8 <@(point,box) (point x box)
	amOpOrder(4015, 600, 600, 15, 517, amSpgist, 1970) // strat=15 <->(point,point) (point x point)

	// family=4016 (kd_point_ops) spgist
	amOp(4016, 600, 600, 11, 4161, amSpgist)           // strat=11 |>>(point,point) (point x point)
	amOp(4016, 600, 600, 30, 506, amSpgist)            // strat=30 >^(point,point) (point x point)
	amOp(4016, 600, 600, 1, 507, amSpgist)             // strat=1 <<(point,point) (point x point)
	amOp(4016, 600, 600, 5, 508, amSpgist)             // strat=5 >>(point,point) (point x point)
	amOp(4016, 600, 600, 10, 4162, amSpgist)           // strat=10 <<|(point,point) (point x point)
	amOp(4016, 600, 600, 29, 509, amSpgist)            // strat=29 <^(point,point) (point x point)
	amOp(4016, 600, 600, 6, 510, amSpgist)             // strat=6 ~=(point,point) (point x point)
	amOp(4016, 600, 603, 8, 511, amSpgist)             // strat=8 <@(point,box) (point x box)
	amOpOrder(4016, 600, 600, 15, 517, amSpgist, 1970) // strat=15 <->(point,point) (point x point)

	// family=4017 (text_ops) spgist
	amOp(4017, 25, 25, 1, 2314, amSpgist)  // strat=1 ~<~(text,text) (text x text)
	amOp(4017, 25, 25, 2, 2315, amSpgist)  // strat=2 ~<=~(text,text) (text x text)
	amOp(4017, 25, 25, 3, 98, amSpgist)    // strat=3 =(text,text) (text x text)
	amOp(4017, 25, 25, 4, 2317, amSpgist)  // strat=4 ~>=~(text,text) (text x text)
	amOp(4017, 25, 25, 5, 2318, amSpgist)  // strat=5 ~>~(text,text) (text x text)
	amOp(4017, 25, 25, 11, 664, amSpgist)  // strat=11 <(text,text) (text x text)
	amOp(4017, 25, 25, 12, 665, amSpgist)  // strat=12 <=(text,text) (text x text)
	amOp(4017, 25, 25, 14, 667, amSpgist)  // strat=14 >=(text,text) (text x text)
	amOp(4017, 25, 25, 15, 666, amSpgist)  // strat=15 >(text,text) (text x text)
	amOp(4017, 25, 25, 28, 3877, amSpgist) // strat=28 ^@(text,text) (text x text)

	// family=4034 (jsonb_ops) hash
	amOp(4034, 3802, 3802, 1, 3240, amHash) // strat=1 =(jsonb,jsonb) (jsonb x jsonb)

	// family=4036 (jsonb_ops) gin
	amOp(4036, 3802, 3802, 7, 3246, amGin)  // strat=7 @>(jsonb,jsonb) (jsonb x jsonb)
	amOp(4036, 3802, 25, 9, 3247, amGin)    // strat=9 ?(jsonb,text) (jsonb x text)
	amOp(4036, 3802, 1009, 10, 3248, amGin) // strat=10 ?|(jsonb,_text) (jsonb x _text)
	amOp(4036, 3802, 1009, 11, 3249, amGin) // strat=11 ?&(jsonb,_text) (jsonb x _text)
	amOp(4036, 3802, 4072, 15, 4012, amGin) // strat=15 @?(jsonb,jsonpath) (jsonb x jsonpath)
	amOp(4036, 3802, 4072, 16, 4013, amGin) // strat=16 @@(jsonb,jsonpath) (jsonb x jsonpath)

	// family=4037 (jsonb_path_ops) gin
	amOp(4037, 3802, 3802, 7, 3246, amGin)  // strat=7 @>(jsonb,jsonb) (jsonb x jsonb)
	amOp(4037, 3802, 4072, 15, 4012, amGin) // strat=15 @?(jsonb,jsonpath) (jsonb x jsonpath)
	amOp(4037, 3802, 4072, 16, 4013, amGin) // strat=16 @@(jsonb,jsonpath) (jsonb x jsonpath)

	// family=3474 (range_ops) spgist
	amOp(3474, 3831, 3831, 1, 3893, amSpgist)  // strat=1 <<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 2, 3895, amSpgist)  // strat=2 &<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 3, 3888, amSpgist)  // strat=3 &&(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 4, 3896, amSpgist)  // strat=4 &>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 5, 3894, amSpgist)  // strat=5 >>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 6, 3897, amSpgist)  // strat=6 -|-(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 7, 3890, amSpgist)  // strat=7 @>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 8, 3892, amSpgist)  // strat=8 <@(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 2283, 16, 3889, amSpgist) // strat=16 @>(anyrange,anyelement) (anyrange x anyelement)
	amOp(3474, 3831, 3831, 18, 3882, amSpgist) // strat=18 =(anyrange,anyrange) (anyrange x anyrange)

	// family=5000 (box_ops) spgist
	amOp(5000, 603, 603, 1, 493, amSpgist)             // strat=1 <<(box,box) (box x box)
	amOp(5000, 603, 603, 2, 494, amSpgist)             // strat=2 &<(box,box) (box x box)
	amOp(5000, 603, 603, 3, 500, amSpgist)             // strat=3 &&(box,box) (box x box)
	amOp(5000, 603, 603, 4, 495, amSpgist)             // strat=4 &>(box,box) (box x box)
	amOp(5000, 603, 603, 5, 496, amSpgist)             // strat=5 >>(box,box) (box x box)
	amOp(5000, 603, 603, 6, 499, amSpgist)             // strat=6 ~=(box,box) (box x box)
	amOp(5000, 603, 603, 7, 498, amSpgist)             // strat=7 @>(box,box) (box x box)
	amOp(5000, 603, 603, 8, 497, amSpgist)             // strat=8 <@(box,box) (box x box)
	amOp(5000, 603, 603, 9, 2571, amSpgist)            // strat=9 &<|(box,box) (box x box)
	amOp(5000, 603, 603, 10, 2570, amSpgist)           // strat=10 <<|(box,box) (box x box)
	amOp(5000, 603, 603, 11, 2573, amSpgist)           // strat=11 |>>(box,box) (box x box)
	amOp(5000, 603, 603, 12, 2572, amSpgist)           // strat=12 |&>(box,box) (box x box)
	amOpOrder(5000, 603, 600, 15, 606, amSpgist, 1970) // strat=15 <->(box,point) (box x point)

	// family=5008 (poly_ops) spgist
	amOp(5008, 604, 604, 1, 485, amSpgist)              // strat=1 <<(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 2, 486, amSpgist)              // strat=2 &<(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 3, 492, amSpgist)              // strat=3 &&(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 4, 487, amSpgist)              // strat=4 &>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 5, 488, amSpgist)              // strat=5 >>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 6, 491, amSpgist)              // strat=6 ~=(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 7, 490, amSpgist)              // strat=7 @>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 8, 489, amSpgist)              // strat=8 <@(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 9, 2575, amSpgist)             // strat=9 &<|(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 10, 2574, amSpgist)            // strat=10 <<|(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 11, 2577, amSpgist)            // strat=11 |>>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 12, 2576, amSpgist)            // strat=12 |&>(polygon,polygon) (polygon x polygon)
	amOpOrder(5008, 604, 600, 15, 3289, amSpgist, 1970) // strat=15 <->(polygon,point) (polygon x point)

	// family=3550 (network_ops) gist
	amOp(3550, 869, 869, 3, 3552, amGist)  // strat=3 &&(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 18, 1201, amGist) // strat=18 =(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 19, 1202, amGist) // strat=19 <>(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 20, 1203, amGist) // strat=20 <(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 21, 1204, amGist) // strat=21 <=(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 22, 1205, amGist) // strat=22 >(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 23, 1206, amGist) // strat=23 >=(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 24, 931, amGist)  // strat=24 <<(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 25, 932, amGist)  // strat=25 <<=(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 26, 933, amGist)  // strat=26 >>(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 27, 934, amGist)  // strat=27 >>=(inet,inet) (inet x inet)

	// family=3794 (network_ops) spgist
	amOp(3794, 869, 869, 3, 3552, amSpgist)  // strat=3 &&(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 18, 1201, amSpgist) // strat=18 =(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 19, 1202, amSpgist) // strat=19 <>(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 20, 1203, amSpgist) // strat=20 <(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 21, 1204, amSpgist) // strat=21 <=(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 22, 1205, amSpgist) // strat=22 >(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 23, 1206, amSpgist) // strat=23 >=(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 24, 931, amSpgist)  // strat=24 <<(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 25, 932, amSpgist)  // strat=25 <<=(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 26, 933, amSpgist)  // strat=26 >>(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 27, 934, amSpgist)  // strat=27 >>=(inet,inet) (inet x inet)

	// family=4064 (bytea_minmax_ops) brin
	amOp(4064, 17, 17, 1, 1957, amBrin) // strat=1 <(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 2, 1958, amBrin) // strat=2 <=(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 3, 1955, amBrin) // strat=3 =(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 4, 1960, amBrin) // strat=4 >=(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 5, 1959, amBrin) // strat=5 >(bytea,bytea) (bytea x bytea)

	// family=4578 (bytea_bloom_ops) brin
	amOp(4578, 17, 17, 1, 1955, amBrin) // strat=1 =(bytea,bytea) (bytea x bytea)

	// family=4062 (char_minmax_ops) brin
	amOp(4062, 18, 18, 1, 631, amBrin) // strat=1 <(char,char) (char x char)
	amOp(4062, 18, 18, 2, 632, amBrin) // strat=2 <=(char,char) (char x char)
	amOp(4062, 18, 18, 3, 92, amBrin)  // strat=3 =(char,char) (char x char)
	amOp(4062, 18, 18, 4, 634, amBrin) // strat=4 >=(char,char) (char x char)
	amOp(4062, 18, 18, 5, 633, amBrin) // strat=5 >(char,char) (char x char)

	// family=4577 (char_bloom_ops) brin
	amOp(4577, 18, 18, 1, 92, amBrin) // strat=1 =(char,char) (char x char)

	// family=4065 (name_minmax_ops) brin
	amOp(4065, 19, 19, 1, 660, amBrin) // strat=1 <(name,name) (name x name)
	amOp(4065, 19, 19, 2, 661, amBrin) // strat=2 <=(name,name) (name x name)
	amOp(4065, 19, 19, 3, 93, amBrin)  // strat=3 =(name,name) (name x name)
	amOp(4065, 19, 19, 4, 663, amBrin) // strat=4 >=(name,name) (name x name)
	amOp(4065, 19, 19, 5, 662, amBrin) // strat=5 >(name,name) (name x name)

	// family=4579 (name_bloom_ops) brin
	amOp(4579, 19, 19, 1, 93, amBrin) // strat=1 =(name,name) (name x name)

	// family=4054 (integer_minmax_ops) brin
	amOp(4054, 20, 20, 1, 412, amBrin)  // strat=1 <(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 2, 414, amBrin)  // strat=2 <=(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 3, 410, amBrin)  // strat=3 =(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 4, 415, amBrin)  // strat=4 >=(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 5, 413, amBrin)  // strat=5 >(int8,int8) (int8 x int8)
	amOp(4054, 20, 21, 1, 1870, amBrin) // strat=1 <(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 2, 1872, amBrin) // strat=2 <=(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 3, 1868, amBrin) // strat=3 =(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 4, 1873, amBrin) // strat=4 >=(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 5, 1871, amBrin) // strat=5 >(int8,int2) (int8 x int2)
	amOp(4054, 20, 23, 1, 418, amBrin)  // strat=1 <(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 2, 420, amBrin)  // strat=2 <=(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 3, 416, amBrin)  // strat=3 =(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 4, 430, amBrin)  // strat=4 >=(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 5, 419, amBrin)  // strat=5 >(int8,int4) (int8 x int4)
	amOp(4054, 21, 21, 1, 95, amBrin)   // strat=1 <(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 2, 522, amBrin)  // strat=2 <=(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 3, 94, amBrin)   // strat=3 =(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 4, 524, amBrin)  // strat=4 >=(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 5, 520, amBrin)  // strat=5 >(int2,int2) (int2 x int2)
	amOp(4054, 21, 20, 1, 1864, amBrin) // strat=1 <(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 2, 1866, amBrin) // strat=2 <=(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 3, 1862, amBrin) // strat=3 =(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 4, 1867, amBrin) // strat=4 >=(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 5, 1865, amBrin) // strat=5 >(int2,int8) (int2 x int8)
	amOp(4054, 21, 23, 1, 534, amBrin)  // strat=1 <(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 2, 540, amBrin)  // strat=2 <=(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 3, 532, amBrin)  // strat=3 =(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 4, 542, amBrin)  // strat=4 >=(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 5, 536, amBrin)  // strat=5 >(int2,int4) (int2 x int4)
	amOp(4054, 23, 23, 1, 97, amBrin)   // strat=1 <(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 2, 523, amBrin)  // strat=2 <=(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 3, 96, amBrin)   // strat=3 =(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 4, 525, amBrin)  // strat=4 >=(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 5, 521, amBrin)  // strat=5 >(int4,int4) (int4 x int4)
	amOp(4054, 23, 21, 1, 535, amBrin)  // strat=1 <(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 2, 541, amBrin)  // strat=2 <=(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 3, 533, amBrin)  // strat=3 =(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 4, 543, amBrin)  // strat=4 >=(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 5, 537, amBrin)  // strat=5 >(int4,int2) (int4 x int2)
	amOp(4054, 23, 20, 1, 37, amBrin)   // strat=1 <(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 2, 80, amBrin)   // strat=2 <=(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 3, 15, amBrin)   // strat=3 =(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 4, 82, amBrin)   // strat=4 >=(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 5, 76, amBrin)   // strat=5 >(int4,int8) (int4 x int8)

	// family=4602 (integer_minmax_multi_ops) brin
	amOp(4602, 20, 20, 1, 412, amBrin)  // strat=1 <(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 2, 414, amBrin)  // strat=2 <=(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 3, 410, amBrin)  // strat=3 =(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 4, 415, amBrin)  // strat=4 >=(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 5, 413, amBrin)  // strat=5 >(int8,int8) (int8 x int8)
	amOp(4602, 20, 21, 1, 1870, amBrin) // strat=1 <(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 2, 1872, amBrin) // strat=2 <=(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 3, 1868, amBrin) // strat=3 =(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 4, 1873, amBrin) // strat=4 >=(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 5, 1871, amBrin) // strat=5 >(int8,int2) (int8 x int2)
	amOp(4602, 20, 23, 1, 418, amBrin)  // strat=1 <(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 2, 420, amBrin)  // strat=2 <=(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 3, 416, amBrin)  // strat=3 =(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 4, 430, amBrin)  // strat=4 >=(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 5, 419, amBrin)  // strat=5 >(int8,int4) (int8 x int4)
	amOp(4602, 21, 21, 1, 95, amBrin)   // strat=1 <(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 2, 522, amBrin)  // strat=2 <=(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 3, 94, amBrin)   // strat=3 =(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 4, 524, amBrin)  // strat=4 >=(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 5, 520, amBrin)  // strat=5 >(int2,int2) (int2 x int2)
	amOp(4602, 21, 20, 1, 1864, amBrin) // strat=1 <(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 2, 1866, amBrin) // strat=2 <=(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 3, 1862, amBrin) // strat=3 =(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 4, 1867, amBrin) // strat=4 >=(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 5, 1865, amBrin) // strat=5 >(int2,int8) (int2 x int8)
	amOp(4602, 21, 23, 1, 534, amBrin)  // strat=1 <(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 2, 540, amBrin)  // strat=2 <=(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 3, 532, amBrin)  // strat=3 =(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 4, 542, amBrin)  // strat=4 >=(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 5, 536, amBrin)  // strat=5 >(int2,int4) (int2 x int4)
	amOp(4602, 23, 23, 1, 97, amBrin)   // strat=1 <(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 2, 523, amBrin)  // strat=2 <=(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 3, 96, amBrin)   // strat=3 =(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 4, 525, amBrin)  // strat=4 >=(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 5, 521, amBrin)  // strat=5 >(int4,int4) (int4 x int4)
	amOp(4602, 23, 21, 1, 535, amBrin)  // strat=1 <(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 2, 541, amBrin)  // strat=2 <=(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 3, 533, amBrin)  // strat=3 =(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 4, 543, amBrin)  // strat=4 >=(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 5, 537, amBrin)  // strat=5 >(int4,int2) (int4 x int2)
	amOp(4602, 23, 20, 1, 37, amBrin)   // strat=1 <(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 2, 80, amBrin)   // strat=2 <=(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 3, 15, amBrin)   // strat=3 =(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 4, 82, amBrin)   // strat=4 >=(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 5, 76, amBrin)   // strat=5 >(int4,int8) (int4 x int8)

	// family=4572 (integer_bloom_ops) brin
	amOp(4572, 20, 20, 1, 410, amBrin) // strat=1 =(int8,int8) (int8 x int8)
	amOp(4572, 21, 21, 1, 94, amBrin)  // strat=1 =(int2,int2) (int2 x int2)
	amOp(4572, 23, 23, 1, 96, amBrin)  // strat=1 =(int4,int4) (int4 x int4)

	// family=4056 (text_minmax_ops) brin
	amOp(4056, 25, 25, 1, 664, amBrin) // strat=1 <(text,text) (text x text)
	amOp(4056, 25, 25, 2, 665, amBrin) // strat=2 <=(text,text) (text x text)
	amOp(4056, 25, 25, 3, 98, amBrin)  // strat=3 =(text,text) (text x text)
	amOp(4056, 25, 25, 4, 667, amBrin) // strat=4 >=(text,text) (text x text)
	amOp(4056, 25, 25, 5, 666, amBrin) // strat=5 >(text,text) (text x text)

	// family=4573 (text_bloom_ops) brin
	amOp(4573, 25, 25, 1, 98, amBrin) // strat=1 =(text,text) (text x text)

	// family=4068 (oid_minmax_ops) brin
	amOp(4068, 26, 26, 1, 609, amBrin) // strat=1 <(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 2, 611, amBrin) // strat=2 <=(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 3, 607, amBrin) // strat=3 =(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 4, 612, amBrin) // strat=4 >=(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 5, 610, amBrin) // strat=5 >(oid,oid) (oid x oid)

	// family=4606 (oid_minmax_multi_ops) brin
	amOp(4606, 26, 26, 1, 609, amBrin) // strat=1 <(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 2, 611, amBrin) // strat=2 <=(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 3, 607, amBrin) // strat=3 =(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 4, 612, amBrin) // strat=4 >=(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 5, 610, amBrin) // strat=5 >(oid,oid) (oid x oid)

	// family=4580 (oid_bloom_ops) brin
	amOp(4580, 26, 26, 1, 607, amBrin) // strat=1 =(oid,oid) (oid x oid)

	// family=4069 (tid_minmax_ops) brin
	amOp(4069, 27, 27, 1, 2799, amBrin) // strat=1 <(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 2, 2801, amBrin) // strat=2 <=(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 3, 387, amBrin)  // strat=3 =(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 4, 2802, amBrin) // strat=4 >=(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 5, 2800, amBrin) // strat=5 >(tid,tid) (tid x tid)

	// family=4581 (tid_bloom_ops) brin
	amOp(4581, 27, 27, 1, 387, amBrin) // strat=1 =(tid,tid) (tid x tid)

	// family=4607 (tid_minmax_multi_ops) brin
	amOp(4607, 27, 27, 1, 2799, amBrin) // strat=1 <(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 2, 2801, amBrin) // strat=2 <=(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 3, 387, amBrin)  // strat=3 =(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 4, 2802, amBrin) // strat=4 >=(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 5, 2800, amBrin) // strat=5 >(tid,tid) (tid x tid)

	// family=4070 (float_minmax_ops) brin
	amOp(4070, 700, 700, 1, 622, amBrin)  // strat=1 <(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 2, 624, amBrin)  // strat=2 <=(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 3, 620, amBrin)  // strat=3 =(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 4, 625, amBrin)  // strat=4 >=(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 5, 623, amBrin)  // strat=5 >(float4,float4) (float4 x float4)
	amOp(4070, 700, 701, 1, 1122, amBrin) // strat=1 <(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 2, 1124, amBrin) // strat=2 <=(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 3, 1120, amBrin) // strat=3 =(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 4, 1125, amBrin) // strat=4 >=(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 5, 1123, amBrin) // strat=5 >(float4,float8) (float4 x float8)
	amOp(4070, 701, 700, 1, 1132, amBrin) // strat=1 <(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 2, 1134, amBrin) // strat=2 <=(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 3, 1130, amBrin) // strat=3 =(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 4, 1135, amBrin) // strat=4 >=(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 5, 1133, amBrin) // strat=5 >(float8,float4) (float8 x float4)
	amOp(4070, 701, 701, 1, 672, amBrin)  // strat=1 <(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 2, 673, amBrin)  // strat=2 <=(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 3, 670, amBrin)  // strat=3 =(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 4, 675, amBrin)  // strat=4 >=(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 5, 674, amBrin)  // strat=5 >(float8,float8) (float8 x float8)

	// family=4608 (float_minmax_multi_ops) brin
	amOp(4608, 700, 700, 1, 622, amBrin)  // strat=1 <(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 2, 624, amBrin)  // strat=2 <=(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 3, 620, amBrin)  // strat=3 =(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 4, 625, amBrin)  // strat=4 >=(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 5, 623, amBrin)  // strat=5 >(float4,float4) (float4 x float4)
	amOp(4608, 700, 701, 1, 1122, amBrin) // strat=1 <(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 2, 1124, amBrin) // strat=2 <=(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 3, 1120, amBrin) // strat=3 =(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 4, 1125, amBrin) // strat=4 >=(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 5, 1123, amBrin) // strat=5 >(float4,float8) (float4 x float8)
	amOp(4608, 701, 700, 1, 1132, amBrin) // strat=1 <(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 2, 1134, amBrin) // strat=2 <=(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 3, 1130, amBrin) // strat=3 =(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 4, 1135, amBrin) // strat=4 >=(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 5, 1133, amBrin) // strat=5 >(float8,float4) (float8 x float4)
	amOp(4608, 701, 701, 1, 672, amBrin)  // strat=1 <(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 2, 673, amBrin)  // strat=2 <=(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 3, 670, amBrin)  // strat=3 =(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 4, 675, amBrin)  // strat=4 >=(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 5, 674, amBrin)  // strat=5 >(float8,float8) (float8 x float8)

	// family=4582 (float_bloom_ops) brin
	amOp(4582, 700, 700, 1, 620, amBrin) // strat=1 =(float4,float4) (float4 x float4)
	amOp(4582, 701, 701, 1, 670, amBrin) // strat=1 =(float8,float8) (float8 x float8)

	// family=4074 (macaddr_minmax_ops) brin
	amOp(4074, 829, 829, 1, 1222, amBrin) // strat=1 <(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 2, 1223, amBrin) // strat=2 <=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 3, 1220, amBrin) // strat=3 =(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 4, 1225, amBrin) // strat=4 >=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 5, 1224, amBrin) // strat=5 >(macaddr,macaddr) (macaddr x macaddr)

	// family=4609 (macaddr_minmax_multi_ops) brin
	amOp(4609, 829, 829, 1, 1222, amBrin) // strat=1 <(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 2, 1223, amBrin) // strat=2 <=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 3, 1220, amBrin) // strat=3 =(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 4, 1225, amBrin) // strat=4 >=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 5, 1224, amBrin) // strat=5 >(macaddr,macaddr) (macaddr x macaddr)

	// family=4583 (macaddr_bloom_ops) brin
	amOp(4583, 829, 829, 1, 1220, amBrin) // strat=1 =(macaddr,macaddr) (macaddr x macaddr)

	// family=4109 (macaddr8_minmax_ops) brin
	amOp(4109, 774, 774, 1, 3364, amBrin) // strat=1 <(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 2, 3365, amBrin) // strat=2 <=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 3, 3362, amBrin) // strat=3 =(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 4, 3367, amBrin) // strat=4 >=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 5, 3366, amBrin) // strat=5 >(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=4610 (macaddr8_minmax_multi_ops) brin
	amOp(4610, 774, 774, 1, 3364, amBrin) // strat=1 <(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 2, 3365, amBrin) // strat=2 <=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 3, 3362, amBrin) // strat=3 =(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 4, 3367, amBrin) // strat=4 >=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 5, 3366, amBrin) // strat=5 >(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=4584 (macaddr8_bloom_ops) brin
	amOp(4584, 774, 774, 1, 3362, amBrin) // strat=1 =(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=4075 (network_minmax_ops) brin
	amOp(4075, 869, 869, 1, 1203, amBrin) // strat=1 <(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 2, 1204, amBrin) // strat=2 <=(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 3, 1201, amBrin) // strat=3 =(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 4, 1206, amBrin) // strat=4 >=(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 5, 1205, amBrin) // strat=5 >(inet,inet) (inet x inet)

	// family=4611 (network_minmax_multi_ops) brin
	amOp(4611, 869, 869, 1, 1203, amBrin) // strat=1 <(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 2, 1204, amBrin) // strat=2 <=(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 3, 1201, amBrin) // strat=3 =(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 4, 1206, amBrin) // strat=4 >=(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 5, 1205, amBrin) // strat=5 >(inet,inet) (inet x inet)

	// family=4585 (network_bloom_ops) brin
	amOp(4585, 869, 869, 1, 1201, amBrin) // strat=1 =(inet,inet) (inet x inet)

	// family=4102 (network_inclusion_ops) brin
	amOp(4102, 869, 869, 3, 3552, amBrin)  // strat=3 &&(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 7, 934, amBrin)   // strat=7 >>=(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 8, 932, amBrin)   // strat=8 <<=(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 18, 1201, amBrin) // strat=18 =(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 24, 933, amBrin)  // strat=24 >>(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 26, 931, amBrin)  // strat=26 <<(inet,inet) (inet x inet)

	// family=4076 (bpchar_minmax_ops) brin
	amOp(4076, 1042, 1042, 1, 1058, amBrin) // strat=1 <(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 2, 1059, amBrin) // strat=2 <=(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 3, 1054, amBrin) // strat=3 =(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 4, 1061, amBrin) // strat=4 >=(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 5, 1060, amBrin) // strat=5 >(bpchar,bpchar) (bpchar x bpchar)

	// family=4586 (bpchar_bloom_ops) brin
	amOp(4586, 1042, 1042, 1, 1054, amBrin) // strat=1 =(bpchar,bpchar) (bpchar x bpchar)

	// family=4077 (time_minmax_ops) brin
	amOp(4077, 1083, 1083, 1, 1110, amBrin) // strat=1 <(time,time) (time x time)
	amOp(4077, 1083, 1083, 2, 1111, amBrin) // strat=2 <=(time,time) (time x time)
	amOp(4077, 1083, 1083, 3, 1108, amBrin) // strat=3 =(time,time) (time x time)
	amOp(4077, 1083, 1083, 4, 1113, amBrin) // strat=4 >=(time,time) (time x time)
	amOp(4077, 1083, 1083, 5, 1112, amBrin) // strat=5 >(time,time) (time x time)

	// family=4612 (time_minmax_multi_ops) brin
	amOp(4612, 1083, 1083, 1, 1110, amBrin) // strat=1 <(time,time) (time x time)
	amOp(4612, 1083, 1083, 2, 1111, amBrin) // strat=2 <=(time,time) (time x time)
	amOp(4612, 1083, 1083, 3, 1108, amBrin) // strat=3 =(time,time) (time x time)
	amOp(4612, 1083, 1083, 4, 1113, amBrin) // strat=4 >=(time,time) (time x time)
	amOp(4612, 1083, 1083, 5, 1112, amBrin) // strat=5 >(time,time) (time x time)

	// family=4587 (time_bloom_ops) brin
	amOp(4587, 1083, 1083, 1, 1108, amBrin) // strat=1 =(time,time) (time x time)

	// family=4059 (datetime_minmax_ops) brin
	amOp(4059, 1114, 1114, 1, 2062, amBrin) // strat=1 <(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 2, 2063, amBrin) // strat=2 <=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 3, 2060, amBrin) // strat=3 =(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 4, 2065, amBrin) // strat=4 >=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 5, 2064, amBrin) // strat=5 >(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1082, 1, 2371, amBrin) // strat=1 <(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 2, 2372, amBrin) // strat=2 <=(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 3, 2373, amBrin) // strat=3 =(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 4, 2374, amBrin) // strat=4 >=(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 5, 2375, amBrin) // strat=5 >(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1184, 1, 2534, amBrin) // strat=1 <(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 2, 2535, amBrin) // strat=2 <=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 3, 2536, amBrin) // strat=3 =(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 4, 2537, amBrin) // strat=4 >=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 5, 2538, amBrin) // strat=5 >(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1082, 1082, 1, 1095, amBrin) // strat=1 <(date,date) (date x date)
	amOp(4059, 1082, 1082, 2, 1096, amBrin) // strat=2 <=(date,date) (date x date)
	amOp(4059, 1082, 1082, 3, 1093, amBrin) // strat=3 =(date,date) (date x date)
	amOp(4059, 1082, 1082, 4, 1098, amBrin) // strat=4 >=(date,date) (date x date)
	amOp(4059, 1082, 1082, 5, 1097, amBrin) // strat=5 >(date,date) (date x date)
	amOp(4059, 1082, 1114, 1, 2345, amBrin) // strat=1 <(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 2, 2346, amBrin) // strat=2 <=(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 3, 2347, amBrin) // strat=3 =(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 4, 2348, amBrin) // strat=4 >=(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 5, 2349, amBrin) // strat=5 >(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1184, 1, 2358, amBrin) // strat=1 <(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 2, 2359, amBrin) // strat=2 <=(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 3, 2360, amBrin) // strat=3 =(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 4, 2361, amBrin) // strat=4 >=(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 5, 2362, amBrin) // strat=5 >(date,timestamptz) (date x timestamptz)
	amOp(4059, 1184, 1082, 1, 2384, amBrin) // strat=1 <(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 2, 2385, amBrin) // strat=2 <=(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 3, 2386, amBrin) // strat=3 =(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 4, 2387, amBrin) // strat=4 >=(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 5, 2388, amBrin) // strat=5 >(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1114, 1, 2540, amBrin) // strat=1 <(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 2, 2541, amBrin) // strat=2 <=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 3, 2542, amBrin) // strat=3 =(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 4, 2543, amBrin) // strat=4 >=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 5, 2544, amBrin) // strat=5 >(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1184, 1, 1322, amBrin) // strat=1 <(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 2, 1323, amBrin) // strat=2 <=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 3, 1320, amBrin) // strat=3 =(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 4, 1325, amBrin) // strat=4 >=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 5, 1324, amBrin) // strat=5 >(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=4605 (datetime_minmax_multi_ops) brin
	amOp(4605, 1114, 1114, 1, 2062, amBrin) // strat=1 <(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 2, 2063, amBrin) // strat=2 <=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 3, 2060, amBrin) // strat=3 =(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 4, 2065, amBrin) // strat=4 >=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 5, 2064, amBrin) // strat=5 >(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1082, 1, 2371, amBrin) // strat=1 <(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 2, 2372, amBrin) // strat=2 <=(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 3, 2373, amBrin) // strat=3 =(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 4, 2374, amBrin) // strat=4 >=(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 5, 2375, amBrin) // strat=5 >(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1184, 1, 2534, amBrin) // strat=1 <(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 2, 2535, amBrin) // strat=2 <=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 3, 2536, amBrin) // strat=3 =(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 4, 2537, amBrin) // strat=4 >=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 5, 2538, amBrin) // strat=5 >(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1082, 1082, 1, 1095, amBrin) // strat=1 <(date,date) (date x date)
	amOp(4605, 1082, 1082, 2, 1096, amBrin) // strat=2 <=(date,date) (date x date)
	amOp(4605, 1082, 1082, 3, 1093, amBrin) // strat=3 =(date,date) (date x date)
	amOp(4605, 1082, 1082, 4, 1098, amBrin) // strat=4 >=(date,date) (date x date)
	amOp(4605, 1082, 1082, 5, 1097, amBrin) // strat=5 >(date,date) (date x date)
	amOp(4605, 1082, 1114, 1, 2345, amBrin) // strat=1 <(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 2, 2346, amBrin) // strat=2 <=(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 3, 2347, amBrin) // strat=3 =(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 4, 2348, amBrin) // strat=4 >=(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 5, 2349, amBrin) // strat=5 >(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1184, 1, 2358, amBrin) // strat=1 <(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 2, 2359, amBrin) // strat=2 <=(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 3, 2360, amBrin) // strat=3 =(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 4, 2361, amBrin) // strat=4 >=(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 5, 2362, amBrin) // strat=5 >(date,timestamptz) (date x timestamptz)
	amOp(4605, 1184, 1082, 1, 2384, amBrin) // strat=1 <(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 2, 2385, amBrin) // strat=2 <=(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 3, 2386, amBrin) // strat=3 =(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 4, 2387, amBrin) // strat=4 >=(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 5, 2388, amBrin) // strat=5 >(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1114, 1, 2540, amBrin) // strat=1 <(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 2, 2541, amBrin) // strat=2 <=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 3, 2542, amBrin) // strat=3 =(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 4, 2543, amBrin) // strat=4 >=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 5, 2544, amBrin) // strat=5 >(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1184, 1, 1322, amBrin) // strat=1 <(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 2, 1323, amBrin) // strat=2 <=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 3, 1320, amBrin) // strat=3 =(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 4, 1325, amBrin) // strat=4 >=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 5, 1324, amBrin) // strat=5 >(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=4576 (datetime_bloom_ops) brin
	amOp(4576, 1114, 1114, 1, 2060, amBrin) // strat=1 =(timestamp,timestamp) (timestamp x timestamp)
	amOp(4576, 1082, 1082, 1, 1093, amBrin) // strat=1 =(date,date) (date x date)
	amOp(4576, 1184, 1184, 1, 1320, amBrin) // strat=1 =(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=4078 (interval_minmax_ops) brin
	amOp(4078, 1186, 1186, 1, 1332, amBrin) // strat=1 <(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 2, 1333, amBrin) // strat=2 <=(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 3, 1330, amBrin) // strat=3 =(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 4, 1335, amBrin) // strat=4 >=(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 5, 1334, amBrin) // strat=5 >(interval,interval) (interval x interval)

	// family=4613 (interval_minmax_multi_ops) brin
	amOp(4613, 1186, 1186, 1, 1332, amBrin) // strat=1 <(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 2, 1333, amBrin) // strat=2 <=(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 3, 1330, amBrin) // strat=3 =(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 4, 1335, amBrin) // strat=4 >=(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 5, 1334, amBrin) // strat=5 >(interval,interval) (interval x interval)

	// family=4588 (interval_bloom_ops) brin
	amOp(4588, 1186, 1186, 1, 1330, amBrin) // strat=1 =(interval,interval) (interval x interval)

	// family=4058 (timetz_minmax_ops) brin
	amOp(4058, 1266, 1266, 1, 1552, amBrin) // strat=1 <(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 2, 1553, amBrin) // strat=2 <=(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 3, 1550, amBrin) // strat=3 =(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 4, 1555, amBrin) // strat=4 >=(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 5, 1554, amBrin) // strat=5 >(timetz,timetz) (timetz x timetz)

	// family=4604 (timetz_minmax_multi_ops) brin
	amOp(4604, 1266, 1266, 1, 1552, amBrin) // strat=1 <(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 2, 1553, amBrin) // strat=2 <=(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 3, 1550, amBrin) // strat=3 =(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 4, 1555, amBrin) // strat=4 >=(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 5, 1554, amBrin) // strat=5 >(timetz,timetz) (timetz x timetz)

	// family=4575 (timetz_bloom_ops) brin
	amOp(4575, 1266, 1266, 1, 1550, amBrin) // strat=1 =(timetz,timetz) (timetz x timetz)

	// family=4079 (bit_minmax_ops) brin
	amOp(4079, 1560, 1560, 1, 1786, amBrin) // strat=1 <(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 2, 1788, amBrin) // strat=2 <=(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 3, 1784, amBrin) // strat=3 =(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 4, 1789, amBrin) // strat=4 >=(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 5, 1787, amBrin) // strat=5 >(bit,bit) (bit x bit)

	// family=4080 (varbit_minmax_ops) brin
	amOp(4080, 1562, 1562, 1, 1806, amBrin) // strat=1 <(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 2, 1808, amBrin) // strat=2 <=(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 3, 1804, amBrin) // strat=3 =(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 4, 1809, amBrin) // strat=4 >=(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 5, 1807, amBrin) // strat=5 >(varbit,varbit) (varbit x varbit)

	// family=4055 (numeric_minmax_ops) brin
	amOp(4055, 1700, 1700, 1, 1754, amBrin) // strat=1 <(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 2, 1755, amBrin) // strat=2 <=(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 3, 1752, amBrin) // strat=3 =(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 4, 1757, amBrin) // strat=4 >=(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 5, 1756, amBrin) // strat=5 >(numeric,numeric) (numeric x numeric)

	// family=4603 (numeric_minmax_multi_ops) brin
	amOp(4603, 1700, 1700, 1, 1754, amBrin) // strat=1 <(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 2, 1755, amBrin) // strat=2 <=(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 3, 1752, amBrin) // strat=3 =(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 4, 1757, amBrin) // strat=4 >=(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 5, 1756, amBrin) // strat=5 >(numeric,numeric) (numeric x numeric)

	// family=4574 (numeric_bloom_ops) brin
	amOp(4574, 1700, 1700, 1, 1752, amBrin) // strat=1 =(numeric,numeric) (numeric x numeric)

	// family=4081 (uuid_minmax_ops) brin
	amOp(4081, 2950, 2950, 1, 2974, amBrin) // strat=1 <(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 2, 2976, amBrin) // strat=2 <=(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 3, 2972, amBrin) // strat=3 =(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 4, 2977, amBrin) // strat=4 >=(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 5, 2975, amBrin) // strat=5 >(uuid,uuid) (uuid x uuid)

	// family=4614 (uuid_minmax_multi_ops) brin
	amOp(4614, 2950, 2950, 1, 2974, amBrin) // strat=1 <(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 2, 2976, amBrin) // strat=2 <=(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 3, 2972, amBrin) // strat=3 =(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 4, 2977, amBrin) // strat=4 >=(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 5, 2975, amBrin) // strat=5 >(uuid,uuid) (uuid x uuid)

	// family=4589 (uuid_bloom_ops) brin
	amOp(4589, 2950, 2950, 1, 2972, amBrin) // strat=1 =(uuid,uuid) (uuid x uuid)

	// family=4103 (range_inclusion_ops) brin
	amOp(4103, 3831, 3831, 1, 3893, amBrin)  // strat=1 <<(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 2, 3895, amBrin)  // strat=2 &<(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 3, 3888, amBrin)  // strat=3 &&(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 4, 3896, amBrin)  // strat=4 &>(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 5, 3894, amBrin)  // strat=5 >>(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 7, 3890, amBrin)  // strat=7 @>(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 8, 3892, amBrin)  // strat=8 <@(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 2283, 16, 3889, amBrin) // strat=16 @>(anyrange,anyelement) (anyrange x anyelement)
	amOp(4103, 3831, 3831, 17, 3897, amBrin) // strat=17 -|-(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 18, 3882, amBrin) // strat=18 =(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 20, 3884, amBrin) // strat=20 <(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 21, 3885, amBrin) // strat=21 <=(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 22, 3887, amBrin) // strat=22 >(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 23, 3886, amBrin) // strat=23 >=(anyrange,anyrange) (anyrange x anyrange)

	// family=4082 (pg_lsn_minmax_ops) brin
	amOp(4082, 3220, 3220, 1, 3224, amBrin) // strat=1 <(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 2, 3226, amBrin) // strat=2 <=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 3, 3222, amBrin) // strat=3 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 4, 3227, amBrin) // strat=4 >=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 5, 3225, amBrin) // strat=5 >(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=4615 (pg_lsn_minmax_multi_ops) brin
	amOp(4615, 3220, 3220, 1, 3224, amBrin) // strat=1 <(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 2, 3226, amBrin) // strat=2 <=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 3, 3222, amBrin) // strat=3 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 4, 3227, amBrin) // strat=4 >=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 5, 3225, amBrin) // strat=5 >(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=4590 (pg_lsn_bloom_ops) brin
	amOp(4590, 3220, 3220, 1, 3222, amBrin) // strat=1 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=4104 (box_inclusion_ops) brin
	amOp(4104, 603, 603, 1, 493, amBrin)   // strat=1 <<(box,box) (box x box)
	amOp(4104, 603, 603, 2, 494, amBrin)   // strat=2 &<(box,box) (box x box)
	amOp(4104, 603, 603, 3, 500, amBrin)   // strat=3 &&(box,box) (box x box)
	amOp(4104, 603, 603, 4, 495, amBrin)   // strat=4 &>(box,box) (box x box)
	amOp(4104, 603, 603, 5, 496, amBrin)   // strat=5 >>(box,box) (box x box)
	amOp(4104, 603, 603, 6, 499, amBrin)   // strat=6 ~=(box,box) (box x box)
	amOp(4104, 603, 603, 7, 498, amBrin)   // strat=7 @>(box,box) (box x box)
	amOp(4104, 603, 603, 8, 497, amBrin)   // strat=8 <@(box,box) (box x box)
	amOp(4104, 603, 603, 9, 2571, amBrin)  // strat=9 &<|(box,box) (box x box)
	amOp(4104, 603, 603, 10, 2570, amBrin) // strat=10 <<|(box,box) (box x box)
	amOp(4104, 603, 603, 11, 2573, amBrin) // strat=11 |>>(box,box) (box x box)
	amOp(4104, 603, 603, 12, 2572, amBrin) // strat=12 |&>(box,box) (box x box)
	amOp(4104, 603, 600, 7, 433, amBrin)   // strat=7 @>(box,point) (box x point)

	// Total non-btree amOp calls: 660
	// Grand total: 945 rows (= 945 pg_amop entries)

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
	OID       uint32 // amproc OID
	Family    uint32 // amprocfamily — pg_opfamily OID
	LeftType  uint32 // amproclefttype — pg_type OID
	RightType uint32 // amprocrighttype — pg_type OID
	Num       int16  // amprocnum — 1 for cmp
	Proc      uint32 // amproc — pg_proc OID (regproc)
}

// pgAmprocColDefs returns the PG18 6-column FormData_pg_amproc
// shape. Order and types must match `pg_amproc.h` exactly so
// PG's GETSTRUCT cast yields a valid Form_pg_amproc.
func pgAmprocColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},             // 1
		{Name: "amprocfamily", Type: catalog.Type{Name: "oid"}},    // 2
		{Name: "amproclefttype", Type: catalog.Type{Name: "oid"}},  // 3
		{Name: "amprocrighttype", Type: catalog.Type{Name: "oid"}}, // 4
		{Name: "amprocnum", Type: catalog.Type{Name: "int2"}},      // 5
		{Name: "amproc", Type: catalog.Type{Name: "regproc"}},      // 6
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
//
// Returns the per-row heapTIDs in pgAmprocInitialEntries order so
// bootstrapPgAmprocFamProcIndex (Step 3cw) can build composite-key
// IndexTuples pointing at the heap rows.
func bootstrapPgAmprocTuples(dataDir string) ([]heapTID, error) {
	cols := pgAmprocColDefs()
	entries := pgAmprocInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgAmprocRow(e))
	}
	return writeMultiPageHeapRows(dataDir, "2603", cols, rows)
}

// pgIndexColDefs returns the full PG18 FormData_pg_index column shape
// — 21 columns: 2 oids + 2 int2 + 11 bools fixed-part, then int2vector
// indkey, oidvector indcollation / indclass, int2vector indoption, and
// nullable pg_node_tree indexprs / indpred. Byte-aligned offsets match
// `postgres/src/include/catalog/pg_index.h` so the heap-tuple seed and
// PG's `heap_deformtuple → Form_pg_index` cast agree.
func pgIndexColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "indexrelid", Type: catalog.Type{Name: "oid"}},           // 1
		{Name: "indrelid", Type: catalog.Type{Name: "oid"}},             // 2
		{Name: "indnatts", Type: catalog.Type{Name: "int2"}},            // 3
		{Name: "indnkeyatts", Type: catalog.Type{Name: "int2"}},         // 4
		{Name: "indisunique", Type: catalog.Type{Name: "bool"}},         // 5
		{Name: "indnullsnotdistinct", Type: catalog.Type{Name: "bool"}}, // 6
		{Name: "indisprimary", Type: catalog.Type{Name: "bool"}},        // 7
		{Name: "indisexclusion", Type: catalog.Type{Name: "bool"}},      // 8
		{Name: "indimmediate", Type: catalog.Type{Name: "bool"}},        // 9
		{Name: "indisclustered", Type: catalog.Type{Name: "bool"}},      // 10
		{Name: "indisvalid", Type: catalog.Type{Name: "bool"}},          // 11
		{Name: "indcheckxmin", Type: catalog.Type{Name: "bool"}},        // 12
		{Name: "indisready", Type: catalog.Type{Name: "bool"}},          // 13
		{Name: "indislive", Type: catalog.Type{Name: "bool"}},           // 14
		{Name: "indisreplident", Type: catalog.Type{Name: "bool"}},      // 15
		// Variable-length region. int2vector indkey is BKI_FORCE_NOT_NULL.
		{Name: "indkey", Type: catalog.Type{Name: "int2vector"}},      // 16
		{Name: "indcollation", Type: catalog.Type{Name: "oidvector"}}, // 17
		{Name: "indclass", Type: catalog.Type{Name: "oidvector"}},     // 18
		{Name: "indoption", Type: catalog.Type{Name: "int2vector"}},   // 19
		// pg_node_tree fields are nullable; we always encode NULL via
		// the null bitmap (see pgIndexRow).
		{Name: "indexprs", Type: catalog.Type{Name: "pg_node_tree"}}, // 20
		{Name: "indpred", Type: catalog.Type{Name: "pg_node_tree"}},  // 21
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
		oidOps       uint32 = 1981
		int2Ops      uint32 = 1979
		int4Ops      uint32 = 1978
		nameOps      uint32 = 1986
		textOps      uint32 = 3126
		charOps      uint32 = 1985
		oidvectorOps uint32 = 1987
		boolOps      uint32 = 1984
		float4Ops    uint32 = 10012 // btree float4_ops (postgres.bki: am=403 / btree)
		cCollation   uint32 = 950   // C_COLLATION_OID — name/text use C in catalogs
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
		entry(2671, 1262, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false),                   // pg_database_datname_index
		entry(2672, 1262, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                              // pg_database_oid_index
		entry(2676, 1260, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false),                   // pg_authid_rolname_index
		entry(2677, 1260, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                              // pg_authid_oid_index
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
		// M0106-0010 batched-13: pg_auth_members_oid_index (OID 6303).
		// postgres/src/include/catalog/pg_auth_members.h:48 —
		//   DECLARE_UNIQUE_INDEX_PKEY(pg_auth_members_oid_index, 6303,
		//     AuthMemOidIndexId, pg_auth_members, btree(oid oid_ops));
		// pg_auth_members attnums: 1=oid. PRIMARY KEY over pg_auth_members.
		entry(6303, 1261, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_auth_members_oid_index
		// M0106-0010 batched-13: pg_auth_members_grantor_index (OID 6302).
		// postgres/src/include/catalog/pg_auth_members.h:51 —
		//   DECLARE_INDEX(pg_auth_members_grantor_index, 6302,
		//     AuthMemGrantorIndexId, pg_auth_members, btree(grantor oid_ops));
		// pg_auth_members attnums: 4=grantor. Non-unique, non-primary.
		entry(6302, 1261, []int16{4}, []uint32{oidOps}, []uint32{0}, false, false), // pg_auth_members_grantor_index
		// pg_shseclabel columns (PG18, pg_shseclabel.h): 1=objoid, 2=classoid,
		// 3=provider, 4=label. Index = btree(objoid, classoid, provider text_ops).
		entry(3593, 3592, []int16{1, 2, 3}, []uint32{oidOps, oidOps, textOps}, []uint32{0, 0, cCollation}, true, true), // pg_shseclabel_object_index
		// M0106-0010 Step 3bq: pg_parameter_acl_parname_index.
		//   postgres/src/include/catalog/pg_parameter_acl.h:53
		//     DECLARE_UNIQUE_INDEX(pg_parameter_acl_parname_index, 6246,
		//       ParameterAclParnameIndexId, pg_parameter_acl,
		//       btree(parname text_ops));
		//   MAKE_SYSCACHE(PARAMETERACLNAME, pg_parameter_acl_parname_index, 4);
		// pg_parameter_acl attnums (pg_parameter_acl_d.h):
		// 1=oid, 2=parname, 3=paracl. UNIQUE but NOT primary —
		// DECLARE_UNIQUE_INDEX is not the _PKEY variant; pg_parameter_acl's
		// PKEY is OID 6247 (pg_parameter_acl_oid_index). Single text_ops key
		// with C_COLLATION_OID = 950 — same convention as the text_ops
		// `provider` slot of pg_shseclabel_object_index (3593) and any other
		// text-typed nailed index key. Shared catalog (BKI_SHARED_RELATION)
		// over pg_parameter_acl heap OID 6243 (Step 3bp nailed shared rel).
		// E2E test (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) surfaced OID 6246 as the next
		// FATAL after Step 3bp seeded pg_parameter_acl.
		entry(6246, 6243, []int16{2}, []uint32{textOps}, []uint32{cCollation}, true, false), // pg_parameter_acl_parname_index
		// M0106-0010 Step 3br: pg_parameter_acl_oid_index.
		//   postgres/src/include/catalog/pg_parameter_acl.h:54
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_parameter_acl_oid_index, 6247,
		//       ParameterAclOidIndexId, pg_parameter_acl,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(PARAMETERACLOID, pg_parameter_acl_oid_index, 4);
		// pg_parameter_acl attnums (pg_parameter_acl_d.h):
		// 1=oid, 2=parname, 3=paracl. UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY)
		// single oid_ops key (no collation) over pg_parameter_acl heap
		// OID 6243 (Step 3bp nailed shared rel). Companion to OID 6246
		// (parname text_ops UNIQUE non-PKEY) seeded in Step 3bq.
		// E2E test (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) surfaced OID 6247 as the next
		// FATAL after Step 3bq seeded pg_parameter_acl_parname_index.
		entry(6247, 6243, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_parameter_acl_oid_index
		// M0106-0010 Step 3ca: pg_replication_origin_roiident_index.
		//   postgres/src/include/catalog/pg_replication_origin.h:57
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_replication_origin_roiident_index, 6001,
		//       ReplicationOriginIdentIndex, pg_replication_origin,
		//       btree(roident oid_ops));
		//   MAKE_SYSCACHE(REPLORIGIDENT, pg_replication_origin_roiident_index, 16);
		// pg_replication_origin attnums (pg_replication_origin_d.h):
		// 1=roident, 2=roname. UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY)
		// single oid_ops key (no collation) over pg_replication_origin heap
		// OID 6000 (Step 3ca nailed shared rel). Same single-column oid_ops
		// UNIQUE PRIMARY pattern as pg_parameter_acl_oid_index (6247,
		// Step 3br). Without this entry RelationIdGetRelation(6001) FATALs.
		// E2E test (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) surfaced OID 6000 as the next FATAL
		// after Step 3bz seeded pg_range; seeding the heap + both indexes
		// in one step matches the family-complete pattern.
		entry(6001, 6000, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_replication_origin_roiident_index
		// M0106-0010 Step 3ca: pg_replication_origin_roname_index.
		//   postgres/src/include/catalog/pg_replication_origin.h:58
		//     DECLARE_UNIQUE_INDEX(pg_replication_origin_roname_index, 6002,
		//       ReplicationOriginNameIndex, pg_replication_origin,
		//       btree(roname text_ops));
		//   MAKE_SYSCACHE(REPLORIGNAME, pg_replication_origin_roname_index, 16);
		// UNIQUE (NOT primary; DECLARE_UNIQUE_INDEX, not the _PKEY variant —
		// PKEY is 6001 = pg_replication_origin_roiident_index) single text_ops
		// key with C_COLLATION_OID = 950 — same convention as
		// pg_parameter_acl_parname_index (6246, Step 3bq) and the text_ops
		// `provider` slot of pg_shseclabel_object_index (3593). Shared catalog
		// (BKI_SHARED_RELATION) over pg_replication_origin heap OID 6000.
		// Without this entry RelationIdGetRelation(6002) FATALs.
		entry(6002, 6000, []int16{2}, []uint32{textOps}, []uint32{cCollation}, true, false), // pg_replication_origin_roname_index
		// M0106-0010 Step 3cf: pg_subscription_oid_index.
		//   postgres/src/include/catalog/pg_subscription.h:103
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_subscription_oid_index, 6114,
		//       SubscriptionObjectIndexId, pg_subscription,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(SUBSCRIPTIONOID, pg_subscription_oid_index, 4);
		// pg_subscription attnums (pg_subscription_d.h): 1=oid, 2=subdbid,
		// 3=subskiplsn, 4=subname, ... UNIQUE PRIMARY single oid_ops key
		// (no collation) over pg_subscription heap OID 6100 (already
		// nailed shared rel). Same single-column oid_ops UNIQUE PRIMARY
		// pattern as pg_replication_origin_roiident_index (6001).
		entry(6114, 6100, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_subscription_oid_index
		// M0106-0010 Step 3cf: pg_subscription_subname_index.
		//   postgres/src/include/catalog/pg_subscription.h:104
		//     DECLARE_UNIQUE_INDEX(pg_subscription_subname_index, 6115,
		//       SubscriptionNameIndexId, pg_subscription,
		//       btree(subdbid oid_ops, subname name_ops));
		//   MAKE_SYSCACHE(SUBSCRIPTIONNAME, pg_subscription_subname_index, 4);
		// pg_subscription attnums: subdbid = col 2 (oid_ops, no
		// collation), subname = col 4 (name_ops, C_COLLATION_OID = 950).
		// UNIQUE (NOT primary; DECLARE_UNIQUE_INDEX, not the _PKEY
		// variant — PKEY is 6114). Composite shared-catalog btree.
		// E2E test (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) surfaced OID 6115 as the next
		// FATAL after Step 3ce seeded pg_statistic.
		entry(6115, 6100, []int16{2, 4}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false), // pg_subscription_subname_index
		// M0106-0010 Step 3ch: pg_tablespace_oid_index.
		//   postgres/src/include/catalog/pg_tablespace.h:52
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_tablespace_oid_index, 2697,
		//       TablespaceOidIndexId, pg_tablespace,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(TABLESPACEOID, pg_tablespace_oid_index, 4);
		// pg_tablespace attnums (pg_tablespace_d.h): 1=oid, 2=spcname,
		// 3=spcowner, 4=spcacl, 5=spcoptions. UNIQUE PRIMARY single
		// oid_ops key (no collation) over pg_tablespace heap OID 1213
		// (Step 3ch nailed shared rel). Same single-column oid_ops UNIQUE
		// PRIMARY pattern as pg_replication_origin_roiident_index (6001)
		// and pg_subscription_oid_index (6114).
		entry(2697, 1213, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_tablespace_oid_index
		// M0106-0010 Step 3ch: pg_tablespace_spcname_index.
		//   postgres/src/include/catalog/pg_tablespace.h:53
		//     DECLARE_UNIQUE_INDEX(pg_tablespace_spcname_index, 2698,
		//       TablespaceNameIndexId, pg_tablespace,
		//       btree(spcname name_ops));
		// UNIQUE (NOT primary; DECLARE_UNIQUE_INDEX, not the _PKEY variant —
		// PKEY is 2697 = pg_tablespace_oid_index). Single name_ops key
		// with C_COLLATION_OID = 950 — same convention as
		// pg_database_datname_index (2671), pg_authid_rolname_index (2676)
		// and other shared-catalog name-keyed indexes.
		entry(2698, 1213, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_tablespace_spcname_index
		// M0106-0010 Step 3cu: pg_db_role_setting_databaseid_rol_index.
		//   postgres/src/include/catalog/pg_db_role_setting.h:51
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_db_role_setting_databaseid_rol_index,
		//       2965, DbRoleSettingDatidRolidIndexId, pg_db_role_setting,
		//       btree(setdatabase oid_ops, setrole oid_ops));
		// pg_db_role_setting attnums (pg_db_role_setting_d.h): 1=setdatabase,
		// 2=setrole, 3=setconfig. UNIQUE PRIMARY composite over pg_db_role_setting
		// heap OID 2964 (Step 3cu nailed shared rel). No MAKE_SYSCACHE —
		// `process_settings` looks up rows via direct index scan (sysscan on the
		// composite key), not syscache.
		entry(2965, 2964, []int16{1, 2}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, true), // pg_db_role_setting_databaseid_rol_index
	}
	// Local-catalog index rows mirroring nailedLocalRels.
	local := []pgIndexEntry{
		entry(2703, 1247, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                          // pg_type_oid_index
		entry(2704, 1247, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false), // pg_type_typname_nsp_index
		entry(2658, 1249, []int16{1, 2}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false), // pg_attribute_relid_attnam_index
		// pg_attribute columns (PG18, pg_attribute.h): 1=attrelid, 2=attname,
		// 3=atttypid, 4=attlen, 5=attnum, ... Earlier goopg pinned
		// attnum at heap col 6 (legacy PG11/12 layout); PG18 sets
		// Anum_pg_attribute_attnum = 5, so the index must point at col 5.
		entry(2659, 1249, []int16{1, 5}, []uint32{oidOps, int2Ops}, []uint32{0, 0}, true, true),                                // pg_attribute_relid_attnum_index
		entry(2662, 1259, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                               // pg_class_oid_index
		entry(2663, 1259, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false),                      // pg_class_relname_nsp_index
		entry(2690, 1255, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                               // pg_proc_oid_index
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
		entry(2678, 2610, []int16{2}, []uint32{oidOps}, []uint32{0}, false, false), // pg_index_indrelid_index
		entry(2679, 2610, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),   // pg_index_indexrelid_index
		entry(2687, 2616, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),   // pg_opclass_oid_index
		// OID 2655 in upstream is pg_amproc_fam_proc_index (on amprocfamily,
		// amproclefttype, amprocrighttype, amprocnum), not the oid index;
		// the label in nailedLocalRels is historical.
		entry(2655, 2603, []int16{2, 3, 4, 5}, []uint32{oidOps, oidOps, oidOps, int2Ops}, []uint32{0, 0, 0, 0}, true, false), // pg_amproc_fam_proc_index
		// pg_rewrite columns (PG18, pg_rewrite.h): 1=oid, 2=rulename,
		// 3=ev_class. Index = btree(ev_class oid_ops, rulename name_ops).
		entry(2693, 2618, []int16{3, 2}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false), // pg_rewrite_rel_rulename_index
		// M0106-0010 Step 3dm phase B: pg_rewrite_oid_index.
		//   postgres/src/include/catalog/pg_rewrite.h:46
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_rewrite_oid_index, 2692,
		//       RewriteOidIndexId, pg_rewrite,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(RULEOID, pg_rewrite_oid_index, 4);
		// Single-column oid_ops UNIQUE PRIMARY over pg_rewrite heap OID
		// 2618. Companion to OID 2693 (ev_class/rulename UNIQUE non-PKEY).
		entry(2692, 2618, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_rewrite_oid_index
		// pg_trigger columns (PG18, pg_trigger.h): 1=oid, 2=tgrelid,
		// 3=tgparentid, 4=tgname. Index = btree(tgrelid, tgname).
		entry(2701, 2620, []int16{2, 4}, []uint32{oidOps, nameOps}, []uint32{0, cCollation}, true, false), // pg_trigger_tgrelid_tgname_index
		entry(2667, 2606, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                          // pg_constraint_oid_index
		// B2.1b: PG's domain typcache (GetDomainConstraints) scans this index;
		// without the pg_index row + file placeholder a standby raises
		// "could not open relation with OID 2666" on ANY domain-typed cast.
		entry(2666, 2606, []int16{10}, []uint32{oidOps}, []uint32{0}, false, false), // pg_constraint_contypid_index
		// M0106-0010 batched-48: pg_constraint_conrelid_contypid_conname_index
		// (OID 2665, ConstraintRelidTypidNameIndexId). pg_constraint attnums
		// (pg_constraint.h): 2=conname (name), 9=conrelid (oid),
		// 10=contypid (oid). PG declares UNIQUE not PKEY.
		entry(2665, 2606, []int16{9, 10, 2}, []uint32{oidOps, oidOps, nameOps}, []uint32{0, 0, cCollation}, true, false), // pg_constraint_conrelid_contypid_conname_index
		entry(2688, 2617, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                                         // pg_operator_oid_index
		entry(2680, 2611, []int16{1, 3}, []uint32{oidOps, int4Ops}, []uint32{0, 0}, true, true),                          // pg_inherits_relid_seqno_index
		// pg_namespace columns (PG18, pg_namespace.h): 1=oid, 2=nspname,
		// 3=nspowner, 4=nspacl. PG18 indexing.h:
		//   NamespaceNameIndexId = 2684 = pg_namespace_nspname_index
		//     btree(nspname name_ops) UNIQUE
		//   NamespaceOidIndexId  = 2685 = pg_namespace_oid_index
		//     btree(oid oid_ops) UNIQUE PRIMARY KEY
		entry(2684, 2615, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_namespace_nspname_index
		entry(2685, 2615, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),            // pg_namespace_oid_index
		// OID 2654 = pg_amop_opr_fam_index: btree(amopopr oid_ops,
		// amoppurpose char_ops, amopfamily oid_ops). amoppurpose is
		// pg_amop attnum 6 (char), amopopr is attnum 7, amopfamily attnum 2.
		entry(2654, 2602, []int16{7, 6, 2}, []uint32{oidOps, charOps, oidOps}, []uint32{0, 0, 0}, true, false), // pg_amop_opr_fam_index
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
		// M0106-0010 Step 3bf: pg_foreign_server_name_index.
		//   postgres/src/include/catalog/pg_foreign_server.h:55
		//     DECLARE_UNIQUE_INDEX(pg_foreign_server_name_index, 549,
		//       ForeignServerNameIndexId, pg_foreign_server,
		//       btree(srvname name_ops));
		//   MAKE_SYSCACHE(FOREIGNSERVERNAME,
		//     pg_foreign_server_name_index, 2);
		// pg_foreign_server attnums (pg_foreign_server_d.h):
		// 2=srvname. UNIQUE non-PRIMARY (DECLARE_UNIQUE_INDEX) over the
		// pg_foreign_server heap OID 1417 (Step 3be nailed rel). Single
		// name_ops key with C_COLLATION_OID = 950 — same single-column
		// name PKEY-less pattern as pg_foreign_data_wrapper_name_index
		// (548, Step 3bc), pg_extension_name_index (3081, Step 3ay),
		// pg_event_trigger_evtname_index (3467, Step 3as), and
		// pg_namespace_nspname_index (2684, Step 3t). E2E test surfaced
		// this index (not the OID companion 113) as the first FATAL
		// after Step 3be — process_settings → catcache init opens
		// FOREIGNSERVERNAME before FOREIGNSERVEROID.
		entry(549, 1417, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_foreign_server_name_index
		// M0106-0010 Step 3bg: pg_foreign_server_oid_index.
		//   postgres/src/include/catalog/pg_foreign_server.h:58
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_server_oid_index, 113,
		//       ForeignServerOidIndexId, pg_foreign_server,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(FOREIGNSERVEROID,
		//     pg_foreign_server_oid_index, 2);
		// pg_foreign_server attnums (pg_foreign_server_d.h):
		// 1=oid. UNIQUE PRIMARY KEY over the pg_foreign_server heap OID
		// 1417 (Step 3be nailed rel). Single oid_ops key with collation
		// 0 — same single-column oid PKEY pattern as
		// pg_foreign_data_wrapper_oid_index (112, Step 3bd) and
		// pg_extension_oid_index. Companion to OID 549 (Step 3bf);
		// surfaced as the next E2E FATAL after Step 3bf landed.
		entry(113, 1417, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_foreign_server_oid_index
		// M0106-0010 Step 3bi: pg_foreign_table_relid_index.
		//   postgres/src/include/catalog/pg_foreign_table.h:47
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_table_relid_index, 3119,
		//       ForeignTableRelidIndexId, pg_foreign_table,
		//       btree(ftrelid oid_ops));
		//   MAKE_SYSCACHE(FOREIGNTABLEREL,
		//     pg_foreign_table_relid_index, 4);
		// pg_foreign_table attnums (pg_foreign_table_d.h):
		// 1=ftrelid. UNIQUE PRIMARY KEY over the pg_foreign_table heap
		// OID 3118 (Step 3bh nailed rel). Single oid_ops key with
		// collation 0 — same single-column oid PKEY pattern as
		// pg_foreign_data_wrapper_oid_index (112, Step 3bd) and
		// pg_foreign_server_oid_index (113, Step 3bg). Unlike most
		// catalogs, pg_foreign_table has no system `oid` column —
		// ftrelid is the primary key, but indkey points at attnum 1
		// (ftrelid) all the same. E2E test surfaced OID 3119 as the
		// next FATAL after Step 3bh seeded the heap nailed rel.
		entry(3119, 3118, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_foreign_table_relid_index
		// M0106-0010 Step 3bj: pg_language_name_index.
		//   postgres/src/include/catalog/pg_language.h:69
		//     DECLARE_UNIQUE_INDEX(pg_language_name_index, 2681,
		//       LanguageNameIndexId, pg_language,
		//       btree(lanname name_ops));
		//   MAKE_SYSCACHE(LANGNAME, pg_language_name_index, 4);
		// pg_language attnums (pg_language_d.h):
		// 1=oid, 2=lanname. UNIQUE but NOT primary —
		// DECLARE_UNIQUE_INDEX is not the _PKEY variant; pg_language's
		// PKEY is OID 2682 (pg_language_oid_index). Single name_ops key
		// with C collation — same single-column name_ops UNIQUE pattern
		// as pg_database_datname_index (2671), pg_authid_rolname_index
		// (2676), pg_namespace_nspname_index (2684). pg_language heap
		// OID 2612 is already a nailed local rel (relcache_init.go).
		// E2E test surfaced OID 2681 as the next FATAL after Step 3bi
		// seeded pg_foreign_table_relid_index.
		entry(2681, 2612, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_language_name_index
		// M0106-0010 Step 3bk: pg_language_oid_index.
		//   postgres/src/include/catalog/pg_language.h:70
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_language_oid_index, 2682,
		//       LanguageOidIndexId, pg_language, btree(oid oid_ops));
		//   MAKE_SYSCACHE(LANGOID, pg_language_oid_index, 4);
		// UNIQUE PRIMARY KEY single oid_ops key (no collation) over
		// pg_language heap OID 2612 (already a nailed local rel). Same
		// single-column oid PKEY pattern as pg_cast_oid_index (2660),
		// pg_foreign_server_oid_index (113), pg_extension_oid_index
		// (3080), pg_event_trigger_oid_index (3468). Companion to
		// pg_language_name_index (2681, Step 3bj). E2E test surfaced
		// OID 2682 as the next FATAL after Step 3bj seeded
		// pg_language_name_index.
		entry(2682, 2612, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_language_oid_index
		// M0106-0010 Step 3bl: pg_operator_oprname_l_r_n_index.
		//   postgres/src/include/catalog/pg_operator.h:86
		//     DECLARE_UNIQUE_INDEX(pg_operator_oprname_l_r_n_index, 2689,
		//       OperatorNameNspIndexId, pg_operator,
		//       btree(oprname name_ops, oprleft oid_ops,
		//             oprright oid_ops, oprnamespace oid_ops));
		//   MAKE_SYSCACHE(OPERNAMENSP, pg_operator_oprname_l_r_n_index, 256);
		// pg_operator attnums (pg_operator.h struct order):
		// 1=oid, 2=oprname, 3=oprnamespace, 4=oprowner, 5=oprkind,
		// 6=oprcanmerge, 7=oprcanhash, 8=oprleft, 9=oprright, 10=oprresult,
		// 11=oprcom, 12=oprnegate, 13=oprcode, 14=oprrest, 15=oprjoin.
		// UNIQUE but NOT primary — DECLARE_UNIQUE_INDEX is not the _PKEY
		// variant; pg_operator's PKEY is OID 2688 (pg_operator_oid_index).
		// `oprname` is a `name` column whose btree opclass uses C collation
		// (C_COLLATION_OID=950) — same convention as pg_proc_proname_args_nsp_index
		// (2691), pg_opclass_am_name_nsp_index (2686). The three oid_ops
		// keys carry no collation. pg_operator heap OID 2617 is already a
		// nailed local rel (relcache_init.go:122). E2E test surfaced OID
		// 2689 as the next FATAL after Step 3bk seeded pg_language_oid_index.
		entry(2689, 2617, []int16{2, 8, 9, 3}, []uint32{nameOps, oidOps, oidOps, oidOps}, []uint32{cCollation, 0, 0, 0}, true, false), // pg_operator_oprname_l_r_n_index
		// M0106-0010 Step 3bn: pg_opfamily_am_name_nsp_index.
		//   postgres/src/include/catalog/pg_opfamily.h:47
		//     DECLARE_UNIQUE_INDEX(pg_opfamily_am_name_nsp_index, 2754,
		//       OpfamilyAmNameNspIndexId, pg_opfamily,
		//       btree(opfmethod oid_ops, opfname name_ops,
		//             opfnamespace oid_ops));
		//   MAKE_SYSCACHE(OPFAMILYAMNAMENSP,
		//     pg_opfamily_am_name_nsp_index, 8);
		// pg_opfamily attnums (pg_opfamily.h struct order):
		// 1=oid, 2=opfmethod, 3=opfname, 4=opfnamespace, 5=opfowner.
		// UNIQUE but NOT primary — DECLARE_UNIQUE_INDEX is not the
		// _PKEY variant; pg_opfamily's PKEY is OID 2755
		// (pg_opfamily_oid_index, deferred to Step 3bo). `opfname` is a
		// `name` column whose btree opclass uses C collation
		// (C_COLLATION_OID=950) — same convention as
		// pg_opclass_am_name_nsp_index (2686, Step 3ad),
		// pg_conversion_name_nsp_index (2669, Step 3aj),
		// pg_collation_name_enc_nsp_index (3164, Step 3ae). pg_opfamily
		// heap OID 2753 is already a nailed local rel (Step 3bm,
		// relcache_init.go). E2E test (TestE2E_FailoverGoopgToPG/async
		// with GOOPG_RUN_BLOCKED_M0102_E2E=1) confirmed OID 2754 as the
		// next FATAL after Step 3bm seeded pg_opfamily heap.
		entry(2754, 2753, []int16{2, 3, 4}, []uint32{oidOps, nameOps, oidOps}, []uint32{0, cCollation, 0}, true, false), // pg_opfamily_am_name_nsp_index
		// M0106-0010 Step 3bo: pg_opfamily_oid_index.
		//   postgres/src/include/catalog/pg_opfamily.h:54
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_opfamily_oid_index, 2755,
		//       OpfamilyOidIndexId, pg_opfamily, btree(oid oid_ops));
		//   MAKE_SYSCACHE(OPFAMILYOID, pg_opfamily_oid_index, 8);
		// UNIQUE PRIMARY single oid_ops key (no collation) on pg_opfamily
		// heap OID 2753 (already a nailed local rel since Step 3bm). E2E
		// test (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) is expected to surface OID 2755
		// as the next FATAL after Step 3bn seeded
		// pg_opfamily_am_name_nsp_index. Mirrors the single-column oid_ops
		// UNIQUE PKEY pattern of pg_language_oid_index (2682, Step 3bk),
		// pg_opclass_oid_index (2687, Step 3l),
		// pg_extension_oid_index (3080, Step 3ax),
		// pg_event_trigger_oid_index (3468, Step 3at),
		// pg_foreign_data_wrapper_oid_index (112, Step 3bd),
		// pg_foreign_server_oid_index (113, Step 3bg).
		entry(2755, 2753, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_opfamily_oid_index
		// M0106-0010 Step 3bt: pg_partitioned_table_partrelid_index.
		//   postgres/src/include/catalog/pg_partitioned_table.h:69
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_partitioned_table_partrelid_index, 3351,
		//       PartitionedRelidIndexId, pg_partitioned_table,
		//       btree(partrelid oid_ops));
		//   MAKE_SYSCACHE(PARTRELID, pg_partitioned_table_partrelid_index, 32);
		// pg_partitioned_table attnums (pg_partitioned_table_d.h):
		// 1=partrelid (oid), 2=partstrat, 3=partnatts, 4=partdefid,
		// 5=partattrs, 6=partclass, 7=partcollation, 8=partexprs.
		// UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) single oid_ops key
		// (no collation) over pg_partitioned_table heap OID 3350 (Step 3bs
		// nailed local rel). pg_partitioned_table has NO `oid` system
		// column — `partrelid` (attnum 1) IS the primary key, mirroring
		// pg_foreign_table's ftrelid (Step 3bi). Without this entry
		// RelationIdGetRelation(3351) FATALs even though the Form_pg_index
		// row exists in the upstream init data, because no pg_class row
		// gets seeded. E2E test (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) is expected to surface OID 3351
		// as the next FATAL after Step 3bs seeded pg_partitioned_table.
		entry(3351, 3350, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_partitioned_table_partrelid_index
		// M0106-0010 Step 3bv: pg_publication_pubname_index.
		//   postgres/src/include/catalog/pg_publication.h:73
		//     DECLARE_UNIQUE_INDEX(pg_publication_pubname_index, 6111,
		//       PublicationNameIndexId, pg_publication,
		//       btree(pubname name_ops));
		//   MAKE_SYSCACHE(PUBLICATIONNAME, pg_publication_pubname_index, 8);
		// pg_publication attnums (pg_publication_d.h / pg_publication.h):
		// 1=oid, 2=pubname (name), 3=pubowner, 4=puballtables,
		// 5=pubinsert, 6=pubupdate, 7=pubdelete, 8=pubtruncate,
		// 9=pubviaroot, 10=pubgencols.
		// UNIQUE (NOT primary; DECLARE_UNIQUE_INDEX, not DECLARE_UNIQUE_INDEX_PKEY)
		// single name_ops key with C collation over pg_publication heap
		// OID 6104 (Step 3bu nailed local rel). Mirrors the single name_ops
		// UNIQUE pattern of pg_namespace_nspname_index (2684, Step 3t),
		// pg_event_trigger_evtname_index (3467, Step 3as),
		// pg_extension_name_index (3081, Step 3ay),
		// pg_foreign_data_wrapper_name_index (548, Step 3bc),
		// pg_foreign_server_name_index (549, Step 3bf),
		// pg_language_name_index (2681, Step 3bj). Without this entry
		// RelationIdGetRelation(6111) FATALs. E2E test
		// (TestE2E_FailoverGoopgToPG/async with GOOPG_RUN_BLOCKED_M0102_E2E=1)
		// surfaced OID 6111 as the next FATAL after Step 3bu seeded
		// pg_publication.
		entry(6111, 6104, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false), // pg_publication_pubname_index
		// M0106-0010 Step 3bw: pg_publication_oid_index.
		//   postgres/src/include/catalog/pg_publication.h:72
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_publication_oid_index, 6110,
		//       PublicationObjectIndexId, pg_publication,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(PUBLICATIONOID, pg_publication_oid_index, 8);
		// UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) single oid_ops key
		// (no collation) over pg_publication heap OID 6104 (Step 3bu nailed
		// local rel). pg_publication's oid system column is attnum 1.
		// Mirrors the single oid_ops UNIQUE PRIMARY pattern of
		// pg_language_oid_index (2682, Step 3bk),
		// pg_extension_oid_index (3080, Step 3ax),
		// pg_event_trigger_oid_index (3468, Step 3at),
		// pg_foreign_data_wrapper_oid_index (112, Step 3bd),
		// pg_foreign_server_oid_index (113, Step 3bg),
		// pg_opfamily_oid_index (2755, Step 3bo),
		// pg_parameter_acl_oid_index (6247, Step 3br). Without this entry
		// RelationIdGetRelation(6110) FATALs. E2E test
		// (TestE2E_FailoverGoopgToPG/async with GOOPG_RUN_BLOCKED_M0102_E2E=1)
		// is expected to surface OID 6110 as the next FATAL after Step 3bv
		// seeded pg_publication_pubname_index (6111).
		entry(6110, 6104, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_publication_oid_index
		// M0106-0010 Step 3bx: pg_publication_namespace_oid_index.
		//   postgres/src/include/catalog/pg_publication_namespace.h:44
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_publication_namespace_oid_index, 6238,
		//       PublicationNamespaceObjectIndexId, pg_publication_namespace,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(PUBLICATIONNAMESPACE,
		//     pg_publication_namespace_oid_index, 64);
		// pg_publication_namespace attnums (pg_publication_namespace_d.h /
		// pg_publication_namespace.h): 1=oid, 2=pnpubid, 3=pnnspid.
		// UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) single oid_ops key
		// (no collation) over pg_publication_namespace heap OID 6237
		// (Step 3bx nailed local rel). Same single-column oid_ops UNIQUE
		// PRIMARY pattern as pg_publication_oid_index (6110, Step 3bw),
		// pg_opfamily_oid_index (2755, Step 3bo),
		// pg_language_oid_index (2682, Step 3bk),
		// pg_extension_oid_index (3080, Step 3ax),
		// pg_event_trigger_oid_index (3468, Step 3at),
		// pg_foreign_data_wrapper_oid_index (112, Step 3bd),
		// pg_foreign_server_oid_index (113, Step 3bg). Without this
		// entry RelationIdGetRelation(6238) FATALs. E2E test
		// (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) is expected to surface OID
		// 6238 alongside the heap and the composite UNIQUE companion
		// 6239 after Step 3bw seeded pg_publication_oid_index.
		entry(6238, 6237, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_publication_namespace_oid_index
		// M0106-0010 Step 3bx: pg_publication_namespace_pnnspid_pnpubid_index.
		//   postgres/src/include/catalog/pg_publication_namespace.h:45
		//     DECLARE_UNIQUE_INDEX(pg_publication_namespace_pnnspid_pnpubid_index, 6239,
		//       PublicationNamespacePnnspidPnpubidIndexId, pg_publication_namespace,
		//       btree(pnnspid oid_ops, pnpubid oid_ops));
		//   MAKE_SYSCACHE(PUBLICATIONNAMESPACEMAP,
		//     pg_publication_namespace_pnnspid_pnpubid_index, 64);
		// pg_publication_namespace attnums (pg_publication_namespace_d.h /
		// pg_publication_namespace.h): 1=oid, 2=pnpubid, 3=pnnspid.
		// UNIQUE (NOT primary; DECLARE_UNIQUE_INDEX, not the _PKEY
		// variant — PKEY is 6238 = pg_publication_namespace_oid_index)
		// composite (pnnspid, pnpubid) oid_ops over
		// pg_publication_namespace heap OID 6237 (Step 3bx nailed
		// local rel). Neither key carries a collation (oid_ops is
		// typeless). Same all-oid_ops composite UNIQUE non-PKEY pattern
		// — though no other catalog has the exact (oid,oid) shape;
		// closest analogue is pg_amop_fam_strat_index (2653) which
		// adds an int2_ops tail. Without this entry
		// RelationIdGetRelation(6239) FATALs. E2E test
		// (TestE2E_FailoverGoopgToPG/async with
		// GOOPG_RUN_BLOCKED_M0102_E2E=1) is expected to surface OID
		// 6239 alongside the heap and the oid PKEY companion 6238
		// after Step 3bw seeded pg_publication_oid_index.
		entry(6239, 6237, []int16{3, 2}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, false), // pg_publication_namespace_pnnspid_pnpubid_index
		// M0106-0010 Step 3by: pg_publication_rel_oid_index.
		//   postgres/src/include/catalog/pg_publication_rel.h:50
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_publication_rel_oid_index, 6112,
		//       PublicationRelObjectIndexId, pg_publication_rel,
		//       btree(oid oid_ops));
		//   MAKE_SYSCACHE(PUBLICATIONREL, pg_publication_rel_oid_index, 64);
		// pg_publication_rel attnums (pg_publication_rel_d.h /
		// pg_publication_rel.h): 1=oid, 2=prpubid, 3=prrelid, 4=prqual,
		// 5=prattrs. UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) single
		// oid_ops key (no collation) over pg_publication_rel heap OID 6106
		// (Step 3by nailed local rel). Same single-column oid_ops UNIQUE
		// PRIMARY pattern as pg_publication_oid_index (6110, Step 3bw),
		// pg_publication_namespace_oid_index (6238, Step 3bx),
		// pg_opfamily_oid_index (2755, Step 3bo). Without this entry
		// RelationIdGetRelation(6112) FATALs. E2E test
		// (TestE2E_FailoverGoopgToPG/async with GOOPG_RUN_BLOCKED_M0102_E2E=1)
		// is expected to surface OID 6112 alongside the heap and the
		// composite + non-unique companions 6113 / 6116 after Step 3bx
		// seeded the pg_publication_namespace family.
		entry(6112, 6106, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_publication_rel_oid_index
		// M0106-0010 Step 3by: pg_publication_rel_prrelid_prpubid_index.
		//   postgres/src/include/catalog/pg_publication_rel.h:51
		//     DECLARE_UNIQUE_INDEX(pg_publication_rel_prrelid_prpubid_index, 6113,
		//       PublicationRelPrrelidPrpubidIndexId, pg_publication_rel,
		//       btree(prrelid oid_ops, prpubid oid_ops));
		//   MAKE_SYSCACHE(PUBLICATIONRELMAP,
		//     pg_publication_rel_prrelid_prpubid_index, 64);
		// UNIQUE (NOT primary; DECLARE_UNIQUE_INDEX, not the _PKEY
		// variant — PKEY is 6112 = pg_publication_rel_oid_index)
		// composite (prrelid, prpubid) oid_ops over pg_publication_rel
		// heap OID 6106 (Step 3by nailed local rel). Neither key carries
		// a collation (oid_ops is typeless). Same all-oid_ops composite
		// UNIQUE non-PKEY pattern as pg_publication_namespace_pnnspid_pnpubid_index
		// (6239, Step 3bx). Without this entry RelationIdGetRelation(6113)
		// FATALs.
		entry(6113, 6106, []int16{3, 2}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, false), // pg_publication_rel_prrelid_prpubid_index
		// M0106-0010 Step 3by: pg_publication_rel_prpubid_index.
		//   postgres/src/include/catalog/pg_publication_rel.h:52
		//     DECLARE_INDEX(pg_publication_rel_prpubid_index, 6116,
		//       PublicationRelPrpubidIndexId, pg_publication_rel,
		//       btree(prpubid oid_ops));
		// Non-UNIQUE single-column index over prpubid (attnum 2). No
		// MAKE_SYSCACHE — used by GetPublicationRelations() to enumerate
		// relations belonging to a given publication via systable_beginscan.
		// First non-UNIQUE entry pinned in pgIndexInitialEntries for the
		// pg_publication_rel family. Without this entry
		// RelationIdGetRelation(6116) FATALs.
		entry(6116, 6106, []int16{2}, []uint32{oidOps}, []uint32{0}, false, false), // pg_publication_rel_prpubid_index
		// M0106-0010 Step 3bz: pg_range_rngtypid_index.
		//   postgres/src/include/catalog/pg_range.h:60
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_range_rngtypid_index, 3542,
		//       RangeTypidIndexId, pg_range, btree(rngtypid oid_ops));
		//   MAKE_SYSCACHE(RANGETYPE, pg_range_rngtypid_index, 4);
		// pg_range attnums (pg_range_d.h): 1=rngtypid, 2=rngsubtype,
		// 3=rngmultitypid, 4=rngcollation, 5=rngsubopc, 6=rngcanonical,
		// 7=rngsubdiff. UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) single
		// oid_ops key (no collation) over pg_range heap OID 3541 (Step 3bz
		// nailed local rel). Without this entry RelationIdGetRelation(3542)
		// FATALs.
		entry(3542, 3541, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_range_rngtypid_index
		// M0106-0010 Step 3bz: pg_range_rngmultitypid_index.
		//   postgres/src/include/catalog/pg_range.h:61
		//     DECLARE_UNIQUE_INDEX(pg_range_rngmultitypid_index, 2228,
		//       RangeMultirangeTypidIndexId, pg_range,
		//       btree(rngmultitypid oid_ops));
		//   MAKE_SYSCACHE(RANGEMULTIRANGE,
		//     pg_range_rngmultitypid_index, 4);
		// UNIQUE (NOT primary; DECLARE_UNIQUE_INDEX, not the _PKEY variant
		// — PKEY is 3542 = pg_range_rngtypid_index) single oid_ops key over
		// pg_range heap OID 3541. attnum 3 = rngmultitypid. Without this
		// entry RelationIdGetRelation(2228) FATALs.
		entry(2228, 3541, []int16{3}, []uint32{oidOps}, []uint32{0}, true, false), // pg_range_rngmultitypid_index
		// M0106-0010 Step 3cb: pg_sequence_seqrelid_index.
		//   postgres/src/include/catalog/pg_sequence.h:42
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_sequence_seqrelid_index, 5002,
		//       SequenceRelidIndexId, pg_sequence, btree(seqrelid oid_ops));
		//   MAKE_SYSCACHE(SEQRELID, pg_sequence_seqrelid_index, 32);
		// pg_sequence attnums (pg_sequence_d.h): 1=seqrelid, 2=seqtypid,
		// 3=seqstart, 4=seqincrement, 5=seqmax, 6=seqmin, 7=seqcache,
		// 8=seqcycle. UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY) single
		// oid_ops key (no collation) over pg_sequence heap OID 2224 (Step
		// 3cb nailed local rel). Without this entry RelationIdGetRelation(5002)
		// FATALs.
		entry(5002, 2224, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_sequence_seqrelid_index
		// M0106-0010 Step 3cc: pg_statistic_ext_data_stxoid_inh_index.
		//   postgres/src/include/catalog/pg_statistic_ext_data.h:57
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_ext_data_stxoid_inh_index, 3433,
		//       StatisticExtDataStxoidInhIndexId, pg_statistic_ext_data,
		//       btree(stxoid oid_ops, stxdinherit bool_ops));
		//   MAKE_SYSCACHE(STATEXTDATASTXOID,
		//     pg_statistic_ext_data_stxoid_inh_index, 4);
		// pg_statistic_ext_data attnums (pg_statistic_ext_data_d.h):
		// 1=stxoid, 2=stxdinherit, 3=stxdndistinct, 4=stxddependencies,
		// 5=stxdmcv, 6=stxdexpr. UNIQUE PRIMARY (DECLARE_UNIQUE_INDEX_PKEY)
		// composite (2-column) key: stxoid oid_ops + stxdinherit bool_ops
		// (no collation on either column) over pg_statistic_ext_data heap
		// OID 3429 (Step 3cc nailed local rel). First non-single-column
		// nailed index seeded in M0106-0010 — exercises the multi-column
		// IndKey/IndClass slot. Without this entry RelationIdGetRelation(3433)
		// FATALs.
		entry(3433, 3429, []int16{1, 2}, []uint32{oidOps, boolOps}, []uint32{0, 0}, true, true), // pg_statistic_ext_data_stxoid_inh_index
		// M0106-0010 Step 3cd: pg_statistic_ext indexes (3 total).
		//   postgres/src/include/catalog/pg_statistic_ext.h:73..75
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_ext_oid_index, 3380,
		//       StatisticExtOidIndexId, pg_statistic_ext, btree(oid oid_ops));
		//     DECLARE_UNIQUE_INDEX(pg_statistic_ext_name_index, 3997,
		//       StatisticExtNameIndexId, pg_statistic_ext,
		//       btree(stxname name_ops, stxnamespace oid_ops));
		//     DECLARE_INDEX(pg_statistic_ext_relid_index, 3379,
		//       StatisticExtRelidIndexId, pg_statistic_ext,
		//       btree(stxrelid oid_ops));
		//   MAKE_SYSCACHE(STATEXTOID, pg_statistic_ext_oid_index, 4);
		//   MAKE_SYSCACHE(STATEXTNAMENSP, pg_statistic_ext_name_index, 4);
		// pg_statistic_ext attnums (pg_statistic_ext_d.h): 1=oid, 2=stxrelid,
		// 3=stxname, 4=stxnamespace, 5=stxowner, 6=stxkeys, 7=stxstattarget,
		// 8=stxkind, 9=stxexprs. Heap OID 3381 is the nailed local rel
		// added in Step 3cd. Three indexes:
		//  - 3380 UNIQUE PRIMARY single oid_ops over attnum 1 (oid)
		//  - 3997 UNIQUE composite name_ops (cCollation) + oid_ops over
		//         attnums 3,4 (stxname, stxnamespace)
		//  - 3379 NON-UNIQUE single oid_ops over attnum 2 (stxrelid)
		entry(3380, 3381, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true),                          // pg_statistic_ext_oid_index
		entry(3997, 3381, []int16{3, 4}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false), // pg_statistic_ext_name_index
		entry(3379, 3381, []int16{2}, []uint32{oidOps}, []uint32{0}, false, false),                        // pg_statistic_ext_relid_index
		// M0106-0010 Step 3ce: pg_statistic_relid_att_inh_index. PG18
		//   postgres/src/include/catalog/pg_statistic.h:139
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_relid_att_inh_index, 2696,
		//       StatisticRelidAttnumInhIndexId, pg_statistic,
		//       btree(starelid oid_ops, staattnum int2_ops, stainherit bool_ops));
		//   MAKE_SYSCACHE(STATRELATTINH, pg_statistic_relid_att_inh_index, 128);
		// pg_statistic attnums (pg_statistic_d.h): 1=starelid, 2=staattnum,
		// 3=stainherit. Heap OID 2619 is the nailed local rel added in Step
		// 3ce. UNIQUE PRIMARY 3-column composite key with three different
		// opclasses (oid_ops, int2_ops, bool_ops) and no collations.
		entry(2696, 2619, []int16{1, 2, 3}, []uint32{oidOps, int2Ops, boolOps}, []uint32{0, 0, 0}, true, true), // pg_statistic_relid_att_inh_index
		// M0106-0010 Step 3cg: pg_subscription_rel_srrelid_srsubid_index. PG18
		//   postgres/src/include/catalog/pg_subscription_rel.h:52
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_subscription_rel_srrelid_srsubid_index, 6117,
		//       SubscriptionRelSrrelidSrsubidIndexId, pg_subscription_rel,
		//       btree(srrelid oid_ops, srsubid oid_ops));
		//   MAKE_SYSCACHE(SUBSCRIPTIONRELMAP, pg_subscription_rel_srrelid_srsubid_index, 64);
		// pg_subscription_rel attnums: 1=srsubid, 2=srrelid. Index leads on
		// srrelid → IndKey = {2, 1}. Heap OID 6102 is the nailed local rel
		// added in Step 3cg. UNIQUE PRIMARY 2-column composite key, both
		// columns oid_ops with no collation.
		entry(6117, 6102, []int16{2, 1}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, true), // pg_subscription_rel_srrelid_srsubid_index
		// M0106-0010 Step 3ci: pg_transform_oid_index. PG18
		//   postgres/src/include/catalog/pg_transform.h:43
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_transform_oid_index, 3574,
		//       TransformOidIndexId, pg_transform, btree(oid oid_ops));
		//   MAKE_SYSCACHE(TRFOID, pg_transform_oid_index, 16);
		// pg_transform attnums (pg_transform_d.h): 1=oid. Heap OID 3576 is
		// the nailed local rel added in Step 3ci. UNIQUE PRIMARY single-column
		// key, oid_ops with no collation.
		entry(3574, 3576, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_transform_oid_index
		// M0106-0010 Step 3ci: pg_transform_type_lang_index. PG18
		//   postgres/src/include/catalog/pg_transform.h:44
		//     DECLARE_UNIQUE_INDEX(pg_transform_type_lang_index, 3575,
		//       TransformTypeLangIndexId, pg_transform,
		//       btree(trftype oid_ops, trflang oid_ops));
		//   MAKE_SYSCACHE(TRFTYPELANG, pg_transform_type_lang_index, 16);
		// pg_transform attnums: 1=oid, 2=trftype, 3=trflang. IndKey = {2, 3}.
		// UNIQUE (NOT PRIMARY) 2-column composite key, both oid_ops with no
		// collation.
		entry(3575, 3576, []int16{2, 3}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, false), // pg_transform_type_lang_index
		// M0106-0010 Step 3cj: pg_ts_config_map_index. PG18
		//   postgres/src/include/catalog/pg_ts_config_map.h:48
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_ts_config_map_index, 3609,
		//       TSConfigMapIndexId, pg_ts_config_map,
		//       btree(mapcfg oid_ops, maptokentype int4_ops, mapseqno int4_ops));
		//   MAKE_SYSCACHE(TSCONFIGMAP, pg_ts_config_map_index, 2);
		// pg_ts_config_map attnums (pg_ts_config_map_d.h): 1=mapcfg,
		// 2=maptokentype, 3=mapseqno, 4=mapdict. IndKey = {1, 2, 3}. Heap OID
		// 3603 is the nailed local rel added in Step 3cj. UNIQUE PRIMARY
		// 3-column composite key, no collations (oid_ops + int4_ops only).
		entry(3609, 3603, []int16{1, 2, 3}, []uint32{oidOps, int4Ops, int4Ops}, []uint32{0, 0, 0}, true, true), // pg_ts_config_map_index
		// M0106-0010 Step 3ck: pg_ts_config_cfgname_index. PG18
		//   postgres/src/include/catalog/pg_ts_config.h:50
		//     DECLARE_UNIQUE_INDEX(pg_ts_config_cfgname_index, 3608,
		//       TSConfigNameNspIndexId, pg_ts_config,
		//       btree(cfgname name_ops, cfgnamespace oid_ops));
		//   MAKE_SYSCACHE(TSCONFIGNAMENSP, pg_ts_config_cfgname_index, 2);
		// pg_ts_config attnums (pg_ts_config_d.h): 1=oid, 2=cfgname,
		// 3=cfgnamespace, 4=cfgowner, 5=cfgparser. IndKey = {2, 3}. Heap OID
		// 3602 is the nailed local rel added in Step 3ck. UNIQUE (NOT
		// PRIMARY) 2-column composite key; cfgname uses C_COLLATION_OID
		// (name catalog columns use C collation), cfgnamespace has no
		// collation.
		entry(3608, 3602, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false), // pg_ts_config_cfgname_index
		// M0106-0010 Step 3ck: pg_ts_config_oid_index. PG18
		//   postgres/src/include/catalog/pg_ts_config.h:51
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_ts_config_oid_index, 3712,
		//       TSConfigOidIndexId, pg_ts_config, btree(oid oid_ops));
		//   MAKE_SYSCACHE(TSCONFIGOID, pg_ts_config_oid_index, 2);
		// pg_ts_config attnums: 1=oid. Heap OID 3602 is the nailed local
		// rel added in Step 3ck. UNIQUE PRIMARY single-column key, oid_ops
		// with no collation.
		entry(3712, 3602, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_ts_config_oid_index
		// M0106-0010 Step 3cm: pg_ts_dict_dictname_index. PG18
		//   postgres/src/include/catalog/pg_ts_dict.h:56
		//     DECLARE_UNIQUE_INDEX(pg_ts_dict_dictname_index, 3604,
		//       TSDictionaryNameNspIndexId, pg_ts_dict,
		//       btree(dictname name_ops, dictnamespace oid_ops));
		//   MAKE_SYSCACHE(TSDICTNAMENSP, pg_ts_dict_dictname_index, 2);
		// pg_ts_dict attnums (pg_ts_dict_d.h): 1=oid, 2=dictname,
		// 3=dictnamespace, 4=dictowner, 5=dicttemplate, 6=dictinitoption.
		// IndKey = {2, 3}. Heap OID 3600 is the nailed local rel added in
		// Step 3cm. UNIQUE (NOT PRIMARY) 2-column composite key; dictname
		// uses C_COLLATION_OID (name catalog columns use C collation),
		// dictnamespace has no collation.
		entry(3604, 3600, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false), // pg_ts_dict_dictname_index
		// M0106-0010 Step 3cm: pg_ts_dict_oid_index. PG18
		//   postgres/src/include/catalog/pg_ts_dict.h:57
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_ts_dict_oid_index, 3605,
		//       TSDictionaryOidIndexId, pg_ts_dict, btree(oid oid_ops));
		//   MAKE_SYSCACHE(TSDICTOID, pg_ts_dict_oid_index, 2);
		// pg_ts_dict attnums: 1=oid. Heap OID 3600 is the nailed local rel
		// added in Step 3cm. UNIQUE PRIMARY single-column key, oid_ops with
		// no collation.
		entry(3605, 3600, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_ts_dict_oid_index
		// M0106-0010 Step 3cn: pg_ts_parser_prsname_index. PG18
		//   postgres/src/include/catalog/pg_ts_parser.h:56
		//     DECLARE_UNIQUE_INDEX(pg_ts_parser_prsname_index, 3606,
		//       TSParserNameNspIndexId, pg_ts_parser,
		//       btree(prsname name_ops, prsnamespace oid_ops));
		//   MAKE_SYSCACHE(TSPARSERNAMENSP, pg_ts_parser_prsname_index, 2);
		// pg_ts_parser attnums (pg_ts_parser_d.h): 1=oid, 2=prsname,
		// 3=prsnamespace, 4=prsstart, 5=prstoken, 6=prsend, 7=prsheadline,
		// 8=prslextype. IndKey = {2, 3}. Heap OID 3601 is the nailed local
		// rel added in Step 3cn. UNIQUE (NOT PRIMARY) 2-column composite
		// key; prsname uses C_COLLATION_OID (name catalog columns use C
		// collation), prsnamespace has no collation.
		entry(3606, 3601, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false), // pg_ts_parser_prsname_index
		// M0106-0010 Step 3cn: pg_ts_parser_oid_index. PG18
		//   postgres/src/include/catalog/pg_ts_parser.h:57
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_ts_parser_oid_index, 3607,
		//       TSParserOidIndexId, pg_ts_parser, btree(oid oid_ops));
		//   MAKE_SYSCACHE(TSPARSEROID, pg_ts_parser_oid_index, 2);
		// pg_ts_parser attnums: 1=oid. Heap OID 3601 is the nailed local
		// rel added in Step 3cn. UNIQUE PRIMARY single-column key, oid_ops
		// with no collation.
		entry(3607, 3601, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_ts_parser_oid_index
		// M0106-0010 Step 3co: pg_ts_template_tmplname_index. PG18
		//   postgres/src/include/catalog/pg_ts_template.h:48
		//     DECLARE_UNIQUE_INDEX(pg_ts_template_tmplname_index, 3766,
		//       TSTemplateNameNspIndexId, pg_ts_template,
		//       btree(tmplname name_ops, tmplnamespace oid_ops));
		//   MAKE_SYSCACHE(TSTEMPLATENAMENSP, pg_ts_template_tmplname_index, 2);
		// pg_ts_template attnums (pg_ts_template_d.h): 1=oid, 2=tmplname,
		// 3=tmplnamespace, 4=tmplinit, 5=tmpllexize. IndKey = {2, 3}. Heap
		// OID 3764 is the nailed local rel added in Step 3co. UNIQUE (NOT
		// PRIMARY) 2-column composite key; tmplname uses C_COLLATION_OID
		// (name catalog columns use C collation), tmplnamespace has no
		// collation.
		entry(3766, 3764, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false), // pg_ts_template_tmplname_index
		// M0106-0010 Step 3co: pg_ts_template_oid_index. PG18
		//   postgres/src/include/catalog/pg_ts_template.h:49
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_ts_template_oid_index, 3767,
		//       TSTemplateOidIndexId, pg_ts_template, btree(oid oid_ops));
		//   MAKE_SYSCACHE(TSTEMPLATEOID, pg_ts_template_oid_index, 2);
		// pg_ts_template attnums: 1=oid. Heap OID 3764 is the nailed local
		// rel added in Step 3co. UNIQUE PRIMARY single-column key, oid_ops
		// with no collation.
		entry(3767, 3764, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_ts_template_oid_index
		// M0106-0010 Step 3cp: pg_user_mapping_oid_index. PG18
		//   postgres/src/include/catalog/pg_user_mapping.h:52
		//     DECLARE_UNIQUE_INDEX_PKEY(pg_user_mapping_oid_index, 174,
		//       UserMappingOidIndexId, pg_user_mapping, btree(oid oid_ops));
		//   MAKE_SYSCACHE(USERMAPPINGOID, pg_user_mapping_oid_index, 2);
		// pg_user_mapping attnums (pg_user_mapping_d.h): 1=oid, 2=umuser,
		// 3=umserver, 4=umoptions. Heap OID 1418 is the nailed local rel
		// added in Step 3cp. UNIQUE PRIMARY single-column key, oid_ops
		// with no collation.
		entry(174, 1418, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_user_mapping_oid_index
		// M0106-0010 Step 3cp: pg_user_mapping_user_server_index. PG18
		//   postgres/src/include/catalog/pg_user_mapping.h:53
		//     DECLARE_UNIQUE_INDEX(pg_user_mapping_user_server_index, 175,
		//       UserMappingUserServerIndexId, pg_user_mapping,
		//       btree(umuser oid_ops, umserver oid_ops));
		//   MAKE_SYSCACHE(USERMAPPINGUSERSERVER, pg_user_mapping_user_server_index, 2);
		// IndKey = {2, 3}. UNIQUE (NOT PRIMARY) 2-column composite key;
		// both oid_ops with no collation.
		entry(175, 1418, []int16{2, 3}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, false), // pg_user_mapping_user_server_index
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
		executor.NewIntDatum(int64(e.IndexRelid)),              // 1 indexrelid
		executor.NewIntDatum(int64(e.IndRelid)),                // 2 indrelid
		executor.NewIntDatum(int64(natts)),                     // 3 indnatts
		executor.NewIntDatum(int64(natts)),                     // 4 indnkeyatts
		executor.NewBoolDatum(e.IsUnique),                      // 5 indisunique
		executor.NewBoolDatum(false),                           // 6 indnullsnotdistinct
		executor.NewBoolDatum(e.IsPrimary),                     // 7 indisprimary
		executor.NewBoolDatum(false),                           // 8 indisexclusion
		executor.NewBoolDatum(true),                            // 9 indimmediate
		executor.NewBoolDatum(false),                           // 10 indisclustered
		executor.NewBoolDatum(true),                            // 11 indisvalid
		executor.NewBoolDatum(false),                           // 12 indcheckxmin
		executor.NewBoolDatum(true),                            // 13 indisready
		executor.NewBoolDatum(true),                            // 14 indislive
		executor.NewBoolDatum(false),                           // 15 indisreplident
		executor.NewBytesDatum(int2VectorBytes(e.IndKey)),      // 16 indkey
		executor.NewBytesDatum(oidVectorBytes(e.IndCollation)), // 17 indcollation
		executor.NewBytesDatum(oidVectorBytes(e.IndClass)),     // 18 indclass
		executor.NewBytesDatum(int2VectorBytes(e.IndOption)),   // 19 indoption
		executor.NullDatum,                                     // 20 indexprs (NULL — no expression indexes)
		executor.NullDatum,                                     // 21 indpred  (NULL — no partial indexes)
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
		{Name: "oid", Type: catalog.Type{Name: "oid"}},                  // 0
		{Name: "relname", Type: catalog.Type{Name: "name"}},             // 4 (64 bytes)
		{Name: "relnamespace", Type: catalog.Type{Name: "oid"}},         // 68
		{Name: "reltype", Type: catalog.Type{Name: "oid"}},              // 72
		{Name: "reloftype", Type: catalog.Type{Name: "oid"}},            // 76
		{Name: "relowner", Type: catalog.Type{Name: "oid"}},             // 80
		{Name: "relam", Type: catalog.Type{Name: "oid"}},                // 84
		{Name: "relfilenode", Type: catalog.Type{Name: "oid"}},          // 88
		{Name: "reltablespace", Type: catalog.Type{Name: "oid"}},        // 92
		{Name: "relpages", Type: catalog.Type{Name: "int4"}},            // 96
		{Name: "reltuples", Type: catalog.Type{Name: "float4"}},         // 100
		{Name: "relallvisible", Type: catalog.Type{Name: "int4"}},       // 104
		{Name: "relallfrozen", Type: catalog.Type{Name: "int4"}},        // 108
		{Name: "reltoastrelid", Type: catalog.Type{Name: "oid"}},        // 112
		{Name: "relhasindex", Type: catalog.Type{Name: "bool"}},         // 116
		{Name: "relisshared", Type: catalog.Type{Name: "bool"}},         // 117
		{Name: "relpersistence", Type: catalog.Type{Name: "char"}},      // 118
		{Name: "relkind", Type: catalog.Type{Name: "char"}},             // 119
		{Name: "relnatts", Type: catalog.Type{Name: "int2"}},            // 120
		{Name: "relchecks", Type: catalog.Type{Name: "int2"}},           // 122
		{Name: "relhasrules", Type: catalog.Type{Name: "bool"}},         // 124
		{Name: "relhastriggers", Type: catalog.Type{Name: "bool"}},      // 125
		{Name: "relhassubclass", Type: catalog.Type{Name: "bool"}},      // 126
		{Name: "relrowsecurity", Type: catalog.Type{Name: "bool"}},      // 127
		{Name: "relforcerowsecurity", Type: catalog.Type{Name: "bool"}}, // 128
		{Name: "relispopulated", Type: catalog.Type{Name: "bool"}},      // 129
		{Name: "relreplident", Type: catalog.Type{Name: "char"}},        // 130
		{Name: "relispartition", Type: catalog.Type{Name: "bool"}},      // 131
		{Name: "relrewrite", Type: catalog.Type{Name: "oid"}},           // 132
		{Name: "relfrozenxid", Type: catalog.Type{Name: "xid"}},         // 136
		{Name: "relminmxid", Type: catalog.Type{Name: "xid"}},           // 140
		// Varlena columns. PG's extractRelOptions / aclitem-walking code
		// casts the raw datum as ArrayType*; the empty placeholder MUST
		// therefore be a valid binary ArrayType, not a text "{}" varlena.
		// See encodeValuePG's "aclitem[]" / "text[]" cases and
		// docs/design/0106-0010-pg-class-empty-array-encoding.md.
		{Name: "relacl", Type: catalog.Type{Name: "aclitem[]"}},          // 144 varlena (16-byte empty ArrayType)
		{Name: "reloptions", Type: catalog.Type{Name: "text[]"}},         // varlena
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
	// M0106-0010 Step 3dl: views have no physical storage. PG's
	// `RELKIND_HAS_STORAGE` macro (pg_class.h:200) explicitly excludes
	// RELKIND_VIEW ('v'); pg_class.relfilenode is therefore 0 and relam
	// is 0 (no table access method). pg_class.relhasrules is true so
	// that PG's relcache fetches the view's ON-SELECT rewrite rule from
	// pg_rewrite when the view is opened.
	relFilenode := int64(rel.OID)
	relHasRules := false
	if rel.RelKind == 'v' {
		relFilenode = 0
		// Keep relHasRules=false: the view is found in pg_class (name lookup works)
		// and PG won't try to load the rewrite rule. Querying the view will return
		// an error (no storage) but won't crash. Needed until the ev_action format
		// is fully compatible with the running PG18 version.
		// relHasRules = true
	}
	return executor.Row{
		executor.NewIntDatum(int64(rel.OID)), // 0: oid
		executor.NewStringDatum(rel.RelName), // 4: relname
		executor.NewIntDatum(11),             // 68: relnamespace
		executor.NewIntDatum(int64(relType)), // 72: reltype
		executor.NewIntDatum(0),              // 76: reloftype
		executor.NewIntDatum(10),             // 80: relowner
		executor.NewIntDatum(relAm),          // 84: relam
		executor.NewIntDatum(relFilenode),    // 88: relfilenode
		// M0106-0010 Step 3cr: shared catalogs must store reltablespace
		// = GLOBALTABLESPACE_OID (1664). PG's RelationInitPhysicalAddr
		// (postgres/src/backend/utils/cache/relcache.c:1347-1354)
		// resolves the spcOid purely from pg_class.reltablespace:
		//   if (reltablespace) spcOid = reltablespace;
		//   else                spcOid = MyDatabaseTableSpace;
		//   if (spcOid == GLOBALTABLESPACE_OID) dbOid = InvalidOid;
		//   else                                dbOid = MyDatabaseId;
		// The comment at line 1335-1336 explicitly states "we do not
		// look at relisshared here" — so the only way a shared catalog
		// file path resolves to `global/` is reltablespace == 1664.
		// formrdesc sets this in memory at Phase 2 (relcache.c:1948),
		// but Phase 3 then overrides rd_rel with the on-disk pg_class
		// row, so the on-disk value must match.
		pgClassReltablespaceFor(rel.IsShared),              // 92: reltablespace
		executor.NewIntDatum(0),                            // 96: relpages
		executor.NewIntDatum(0),                            // 100: reltuples
		executor.NewIntDatum(0),                            // 104: relallvisible
		executor.NewIntDatum(0),                            // 108: relallfrozen
		executor.NewIntDatum(0),                            // 112: reltoastrelid
		executor.NewBoolDatum(false),                       // 116: relhasindex
		executor.NewBoolDatum(rel.IsShared),                // 117: relisshared
		executor.NewStringDatum("p"),                       // 118: relpersistence
		executor.NewStringDatum(string(rune(rel.RelKind))), // 119: relkind
		executor.NewIntDatum(int64(rel.RelNatts)),          // 120: relnatts
		executor.NewIntDatum(0),                            // 122: relchecks
		executor.NewBoolDatum(relHasRules),                 // 124: relhasrules
		executor.NewBoolDatum(false),                       // 125: relhastriggers
		executor.NewBoolDatum(false),                       // 126: relhassubclass
		executor.NewBoolDatum(false),                       // 127: relrowsecurity
		executor.NewBoolDatum(false),                       // 128: relforcerowsecurity
		executor.NewBoolDatum(true),                        // 129: relispopulated
		executor.NewStringDatum("n"),                       // 130: relreplident
		executor.NewBoolDatum(false),                       // 131: relispartition
		executor.NewIntDatum(0),                            // 132: relrewrite
		executor.NewIntDatum(3),                            // 136: relfrozenxid
		executor.NewIntDatum(1),                            // 140: relminmxid
		executor.NewStringDatum("{}"),                      // relacl (empty aclitem[])
		executor.NewStringDatum("{}"),                      // reloptions (empty text[])
		executor.NewStringDatum(""),                        // relpartbound (empty pg_node_tree)
	}
}

// pgClassReltablespaceFor returns the pg_class.reltablespace value for a
// nailed relation. Shared catalogs must store GLOBALTABLESPACE_OID (1664)
// so the file path resolves to `global/<relfilenode>`; local catalogs
// store 0 (the default, which routes to the database's tablespace).
// See pgClassRow callsite for the relcache.c:1347-1354 derivation.
func pgClassReltablespaceFor(isShared bool) executor.Datum {
	if isShared {
		return executor.NewIntDatum(1664) // GLOBALTABLESPACE_OID
	}
	return executor.NewIntDatum(0)
}

// pgAttrColDefs returns the 25 pg_attribute column descriptors. attstattarget
// is appended last (not at its PG18-canonical position #4); see
// catalog.PGAttributeColumns for the rationale (preserves the fixed-offset
// physical decoder and keeps t_hoff stable). Always emitted NULL.
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
		// attacl is a PG-native _aclitem array (OID 1034), not text: a column
		// GRANT stores it as a binary ArrayType blob and the seqscan/index-scan ACL
		// hook decodes it to canonical aclitemout text on read. Declaring it text
		// here made the decoder hand back the raw blob as a KindString (pg_dump then
		// failed to parse the ACL). Mirrors pg_type.typacl. M0119-0004-ACLHEAP.
		{Name: "attacl", Type: catalog.Type{Name: "aclitem[]"}},
		{Name: "attoptions", Type: catalog.Type{Name: "text"}},
		{Name: "attfdwoptions", Type: catalog.Type{Name: "text"}},
		{Name: "attmissingval", Type: catalog.Type{Name: "text"}},
		{Name: "attstattarget", Type: catalog.Type{Name: "int2"}},
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
		executor.NewIntDatum(-1), // atttypmod
		executor.NewIntDatum(0),  // attndims
		executor.NewBoolDatum(pgTypeByVal(a.TypeOID)),
		executor.NewStringDatum(pgTypeAlignChar(a.TypeOID)),
		executor.NewStringDatum(pgTypeStorageChar(a.TypeOID)),
		executor.NewStringDatum(""), // attcompression
		executor.NewBoolDatum(a.NotNull),
		executor.NewBoolDatum(false), // atthasdef
		executor.NewBoolDatum(false), // atthasmissing
		executor.NewStringDatum(""),  // attidentity
		executor.NewStringDatum(""),  // attgenerated
		executor.NewBoolDatum(false), // attisdropped
		executor.NewBoolDatum(true),  // attislocal
		executor.NewIntDatum(0),      // attinhcount
		executor.NewIntDatum(0),      // attcollation
		// Step 3u: Emit NULL (not empty-text varlena) for the four nullable
		// trailing varlena/array columns. Previously NewStringDatum("") wrote
		// a 1-byte empty varlena which PG's RelationGetIndexAttOptions →
		// index_opclass_options interpreted as "attoptions present" → ereport
		// ERROR → generate_opclass_name → OpclassIsVisible →
		// get_namespace_oid(pg_namespace_nspname_index=2684) → recursive
		// RelationInitIndexAccessInfo on the very index whose error message
		// is being formatted → ERRORDATA_STACK_SIZE PANIC. PG18's default
		// for an unconfigured catalog row is SQL NULL on all four columns.
		executor.NullDatum, // attacl
		executor.NullDatum, // attoptions
		executor.NullDatum, // attfdwoptions
		executor.NullDatum, // attmissingval
		executor.NullDatum, // attstattarget (PG18 BKI_FORCE_NULL default)
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

// pgAuthidEntry describes a bootstrapped pg_authid row and the heap-TID
// where it was written, so the rolname / oid index bootstrap (Step 3cx)
// can build IndexTuples pointing at each row.
type pgAuthidEntry struct {
	OID     uint32
	Rolname string
	TID     heapTID
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
	case 3220:
		// M0106-0010 Step 3cg: pg_lsn typbyval = FLOAT8PASSBYVAL (true on
		// 64-bit). pg_subscription_rel.srsublsn and pg_subscription.subskiplsn
		// both depend on this.
		return true
	}
	return false
}

func pgTypeAlignChar(oid uint32) string {
	switch oid {
	case 16, 18, 19:
		// M0106-0010 batched-36 loop 5: NAMEOID (19) has typalign='c'
		// per PG18 `postgres/src/include/catalog/pg_type.dat` (`name`
		// entry: `typalign => 'c'`). Without this, pgAttributeRow
		// writes attalign='i' (4-byte) for every name column. The
		// immediate consumer that surfaced this was the test pinning
		// pg_namespace_nspname_index's pg_attribute row alignment; a
		// latent multi-column-name-typed offset miscount would have
		// followed.
		return "c"
	case 21:
		return "s"
	case 23, 26, 700, 194, 1009, 1034, 2277, 24, 325, 269, 30, 22, 1002, 1028, 3361, 3402, 5017:
		return "i"
	case 20, 701, 10028, 3220:
		// PG18 runtime pg_type lookup: _pg_statistic (10028) has typalign='d'
		// because its element rowtype pg_statistic carries int8/float8-aligned
		// columns (stanullfrac, stadistinct float4 padded to 8-byte; stavalues
		// anyarray). M0106-0010 Step 3cc.
		// pg_lsn (3220) is 8-byte XLogRecPtr with typalign='d' per
		// `postgres/src/include/catalog/pg_type.dat:413`. M0106-0010 Step 3cg.
		return "d"
	}
	return "i"
}

func pgTypeStorageChar(oid uint32) string {
	switch oid {
	case 25, 1043, 1042, 194, 1009, 1034, 2277, 1002, 1028, 3361, 3402, 5017, 10028:
		// M0106-0010 Step 3cc: pg_ndistinct (3361) / pg_dependencies (3402) /
		// pg_mcv_list (5017) / _pg_statistic (10028) all carry typstorage='x'
		// (EXTENDED) per PG18 runtime pg_type lookup. Without this entry the
		// nailed pg_attribute row for stxdndistinct/stxddependencies/stxdmcv/
		// stxdexpr would emit attstorage='p' (PLAIN), confusing PG-standby's
		// TOAST machinery the moment any pg_statistic_ext_data row is written.
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
	// M0106-0010 batched-44: bootstrap a PG-canonical pg_xact/ SLRU directory
	// (since M0117-0006 Part C the sole CLOG store — the legacy goopg flat
	// file is retired). A PG18 standby attached via basebackup reads commit
	// status through SimpleLruReadPage_ReadOnly, which requires segment files
	// named %04X with at least one BLCKSZ-aligned page.
	// BootstrapTransactionID (1) and FrozenTransactionID (2) are NOT normal
	// XIDs — TransactionLogFetch short-circuits them to COMMITTED without
	// consulting the SLRU — so we don't need to stamp their lanes; we just
	// need the segment file to exist so the first runtime SetCommitted
	// (xid >= 3) can extend it.
	if err := c.EnablePGSLRUMirror(filepath.Join(dataDir, "pg_xact")); err != nil {
		return fmt.Errorf("enable pg_xact slru mirror: %w", err)
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

func defaultPostgresqlAutoConf() []byte {
	return []byte("# Do not edit this file manually!\n" +
		"# It will be overwritten by the ALTER SYSTEM command.\n")
}

func makeRelMapFile(mappings [][2]uint32) []byte {
	// B0.4: one encoder for bootstrap AND WAL paths — wal.EncodeRelMapFile
	// is the normative RelMapFile renderer (relmapper.c layout); this
	// wrapper only adapts the historical [][2]uint32 call shape.
	ms := make([]wal.RelMapping, len(mappings))
	for i, m := range mappings {
		ms[i] = wal.RelMapping{Oid: m[0], FileNumber: m[1]}
	}
	return wal.EncodeRelMapFile(ms)
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

// defaultPgHBAConf is the trust-everywhere default file used by SampleFiles
// and the no-auth-option path. Init overwrites pg_hba.conf with
// buildPgHBAConf(host, local) when an explicit auth method is requested.
func defaultPgHBAConf() []byte {
	return buildPgHBAConf("trust", "trust")
}
