package executor

// pg_authid runtime sync + startup load — the role/auth persistent store
// (root-0021).
//
// PostgreSQL's durable store for roles is the pg_authid shared catalog heap
// (global/1260); the WAL only carries the crash-recovery tail for those heap
// pages. goopg mirrors that split:
//
//   - DDL time: the server-side CREATE/ALTER/DROP ROLE handler updates the
//     in-memory registries, appends a RecordKindRoleState/DropRole WAL record
//     (the tail), and calls SyncPgAuthidFile to rebuild global/1260 from the
//     catalog role registry — the durable BASE that survives checkpoint-driven
//     WAL segment pruning (the known limitation of pure logical-DDL records).
//   - Startup: LoadRolesFromAuthidHeap reads global/1260 back into the
//     catalog role registry (with pg_authid-shaped attributes + rolpassword
//     verifiers), then replayRoleDDLRecords applies any newer WAL records on
//     top (last-record-wins, idempotent).
//
// The row builders duplicate bootstrapPostgresRoleWithPassword's shapes
// (internal/initdb/initdb.go) — bootstrap-style rows are non-null with
// xmin=1, predefined pg_* rows carry a null bitmap with frozen xmin — so a
// real PG18 standby reading the file sees the same Form_pg_authid layout the
// byte-level regression test pins (pg_authid_heap_row_test.go). Keep the two
// in sync (sibling paths).
//
// Upstream: postgres/src/include/catalog/pg_authid.h (layout),
// postgres/src/backend/commands/user.c (CreateRole/AlterRole writing
// pg_authid), postgres/src/backend/libpq/crypt.c (rolpassword shapes).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgAuthidSyncMu serialises concurrent role-DDL rewrites of global/1260.
var pgAuthidSyncMu sync.Mutex

// pgAuthidSyncCols returns the 12-column pg_authid schema
// (postgres/src/include/catalog/pg_authid.h). Twin of the slice inside
// bootstrapPostgresRoleWithPassword — keep both in sync.
func pgAuthidSyncCols() []catalog.Column {
	return []catalog.Column{
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
}

// pgAuthidPredefined lists the 16 predefined roles from PG18's pg_authid.dat.
// Twin of the list inside bootstrapPostgresRoleWithPassword.
var pgAuthidPredefined = []struct {
	oid  int64
	name string
}{
	{6171, "pg_database_owner"},
	{6181, "pg_read_all_data"},
	{6182, "pg_write_all_data"},
	{3373, "pg_monitor"},
	{3374, "pg_read_all_settings"},
	{3375, "pg_read_all_stats"},
	{3377, "pg_stat_scan_tables"},
	{4569, "pg_read_server_files"},
	{4570, "pg_write_server_files"},
	{4571, "pg_execute_server_program"},
	{4200, "pg_signal_backend"},
	{4544, "pg_checkpoint"},
	{6337, "pg_maintain"},
	{4550, "pg_use_reserved_connections"},
	{6304, "pg_create_subscription"},
	{6392, "pg_signal_autovacuum_worker"},
}

// decodedAuthidRow is one pg_authid heap row read back from global/1260.
type DecodedAuthidRow struct {
	OID       uint32
	Rolname   string
	Super     bool
	CanLogin  bool
	Password  string // "" when NULL/empty
	HasPasswd bool
}

// readPgAuthidRows decodes every live tuple in global/1260. A missing file
// returns (nil, nil) — pre-initdb or non-bootstrap test setups.
func ReadPgAuthidRows(dataDir string) ([]DecodedAuthidRow, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "global", "1260"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	cols := pgAuthidSyncCols()
	var out []DecodedAuthidRow
	for base := 0; base+storage.BlockSize <= len(raw); base += storage.BlockSize {
		page := storage.Page(raw[base : base+storage.BlockSize])
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID ||
				ht.Header.Xmax != storage.InvalidTransactionID {
				continue // dead or deleted tuple
			}
			row := make(Row, len(cols))
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			if derr := DecodeRowIntoMctxPGTuple(row, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				continue
			}
			d := DecodedAuthidRow{
				OID:      uint32(row[0].Int),
				Rolname:  row[1].StringValue(),
				Super:    row[2].BoolValue(),
				CanLogin: row[6].BoolValue(),
			}
			if !row[10].IsNull() {
				d.Password = row[10].StringValue()
				d.HasPasswd = d.Password != ""
			}
			if d.Rolname == "" {
				continue
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// buildAuthidUserRow builds a bootstrap-shaped (non-null, xmin=1) pg_authid
// row for a runtime role. Mirrors buildBootstrapRow in initdb.go.
func buildAuthidUserRow(oid int64, rolname string, super, canLogin bool, rolpassword string) Row {
	return Row{
		NewIntDatum(oid),
		NewStringDatum(rolname),
		NewBoolDatum(super),
		NewBoolDatum(true), // rolinherit — PG default
		NewBoolDatum(false),
		NewBoolDatum(false),
		NewBoolDatum(canLogin),
		NewBoolDatum(false),
		NewBoolDatum(false),
		NewIntDatum(-1),
		NewStringDatum(rolpassword),
		NewTimeDatum(time.Unix(0, 0).UTC()),
	}
}

// SyncPgAuthidFile atomically rebuilds global/1260 from the catalog role
// registry: the bootstrap superuser row (OID 10, verifier preserved from the
// existing file), one row per registered user role (attributes + verifier
// from the RoleAttrs sidecar), and the 16 predefined pg_* roles. Called by
// the server-side role DDL handler after the WAL record is appended, so the
// heap is the durable base and the WAL record only the crash tail — the same
// split PostgreSQL uses for pg_authid.
func SyncPgAuthidFile(dataDir string, cat *catalog.InMemory) error {
	if dataDir == "" || cat == nil {
		return nil
	}
	pgAuthidSyncMu.Lock()
	defer pgAuthidSyncMu.Unlock()

	existing, err := ReadPgAuthidRows(dataDir)
	if err != nil {
		return fmt.Errorf("pg_authid sync: read existing: %w", err)
	}
	if existing == nil {
		return nil // no bootstrap file — nothing to base the rewrite on
	}
	superName, superVerifier := "postgres", ""
	for _, d := range existing {
		if d.OID == 10 {
			superName = d.Rolname
			superVerifier = d.Password
			break
		}
	}

	cols := pgAuthidSyncCols()
	type encTuple struct {
		row    Row
		frozen bool // predefined rows: null bitmap + frozen xmin
	}
	tuples := []encTuple{{row: buildAuthidUserRow(10, superName, true, true, superVerifier)}}
	for _, rs := range cat.AllRoleStates() {
		secret := ""
		if rs.Attrs.CredType != 0 {
			secret = rs.Attrs.Secret
		}
		tuples = append(tuples, encTuple{
			row: buildAuthidUserRow(int64(rs.OID), rs.Name, rs.Attrs.Superuser, rs.Attrs.CanLogin, secret),
		})
	}
	for _, p := range pgAuthidPredefined {
		row := buildAuthidUserRow(p.oid, p.name, false, false, "")
		row[3] = NewBoolDatum(true) // rolinherit
		row[10] = NullDatum         // rolpassword NULL
		row[11] = NullDatum         // rolvaliduntil NULL
		tuples = append(tuples, encTuple{row: row, frozen: true})
	}

	// Assemble pages, spilling to a new block when one fills up.
	var pages []storage.Page
	newPage := func() (storage.Page, error) {
		p := make(storage.Page, storage.BlockSize)
		if err := storage.InitPage(p); err != nil {
			return nil, err
		}
		pages = append(pages, p)
		return p, nil
	}
	page, err := newPage()
	if err != nil {
		return err
	}
	for _, t := range tuples {
		payload, err := EncodeRowPG(cols, t.row)
		if err != nil {
			return fmt.Errorf("pg_authid sync: encode %v: %w", t.row[1].StringValue(), err)
		}
		var tuple storage.HeapTuple
		if t.frozen {
			bitmap := NullBitmapPG(t.row)
			tuple = storage.NewHeapTupleWithNulls(storage.FrozenTransactionID, storage.InvalidTransactionID, bitmap, payload)
		} else {
			tuple = storage.NewHeapTuple(storage.TransactionID(1), storage.InvalidTransactionID, payload)
		}
		tuple.Header.SetNatts(len(cols))
		if _, err := storage.PageAddHeapTuple(page, tuple); err != nil {
			// Page full — spill to a fresh block and retry once.
			page, err = newPage()
			if err != nil {
				return err
			}
			if _, err := storage.PageAddHeapTuple(page, tuple); err != nil {
				return fmt.Errorf("pg_authid sync: add tuple: %w", err)
			}
		}
	}

	// Atomic replace: temp file in the same directory + rename + fsync.
	dir := filepath.Join(dataDir, "global")
	tmp, err := os.CreateTemp(dir, ".1260.tmp*")
	if err != nil {
		return fmt.Errorf("pg_authid sync: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	for _, p := range pages {
		if _, err := tmp.Write(p); err != nil {
			tmp.Close()
			return fmt.Errorf("pg_authid sync: write: %w", err)
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("pg_authid sync: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, "1260")); err != nil {
		return fmt.Errorf("pg_authid sync: rename: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
