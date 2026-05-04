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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

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
	return nil
}

// bootstrapSystemCatalogs creates the three core system catalog heap
// relfiles (pg_type, pg_attribute, pg_class) under
// <dataDir>/base/<DefaultDBOid>/. Each file gets one initialised empty
// page so subsequent Open finds non-zero-length files and the storage
// manager can determine NBlocks without special-casing.
//
// The storage manager is created and closed inline — no buffer pool is
// needed for a one-shot Extend of a blank page.
func bootstrapSystemCatalogs(dataDir string) error {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	page := make([]byte, storage.BlockSize)
	if err := storage.InitPage(storage.Page(page)); err != nil {
		return err
	}

	// Create one relfile per system catalog in OID order so the file
	// names under base/<DBOid>/ are stable across re-inits.
	sysRelOIDs := []uint32{
		catalog.TypeRelationId,      // 1247  base/1/1247
		catalog.AttributeRelationId, // 1249  base/1/1249
		catalog.RelationRelationId,  // 1259  base/1/1259
	}
	for _, relOID := range sysRelOIDs {
		rel := storage.RelFileNode{
			DBOid:  catalog.DefaultDBOid,
			RelOid: relOID,
			Fork:   storage.MainFork,
		}
		if _, err := mgr.Extend(rel, page); err != nil {
			return fmt.Errorf("extend system catalog OID %d: %w", relOID, err)
		}
	}
	return nil
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
