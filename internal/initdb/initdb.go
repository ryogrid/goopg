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
	"os"
	"path/filepath"
	"strconv"

	"github.com/goopg/goopg/internal/catalog"
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
	"pg_xact",
	// PG-required directories so pg_basebackup clones and pg_ctl start
	// against an imported backup succeed (M0102-0007).
	"pg_commit_ts",
	"pg_dynshmem",
	"pg_logical",
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
	if err := bootstrapCLog(abs); err != nil {
		return fmt.Errorf("goopg init: clog: %w", err)
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
