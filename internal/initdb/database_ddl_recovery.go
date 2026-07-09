package initdb

// CREATE/DROP DATABASE WAL replay (M0054-0001).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the
// CREATE/DROP DATABASE record kinds (18 and 19) because v0 has no
// per-database file namespace — there is no page-level state to
// reconstruct. The catalog's `databases` registry, however, is the
// in-memory source of truth that backs `pg_database` and the
// connection-startup database-existence check. This recovery pass
// walks the WAL once after physical replay finishes, decodes each
// CREATE/DROP DATABASE record, and applies it to the catalog so the
// post-restart server agrees with what the pre-crash server told the
// client.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// databaseRegistryRecovery is the catalog-side surface this recovery
// pass needs. `*catalog.InMemory` satisfies it.
type databaseRegistryRecovery interface {
	RegisterDatabaseDuringRecovery(name string, owner, oid uint32)
	UnregisterDatabaseDuringRecovery(name string)
}

// replayDatabaseDDLRecords reads every WAL record under walDir and
// applies CREATE / DROP DATABASE entries to the catalog. A missing
// walDir means "freshly initdb'd cluster" and is treated as a no-op.
// The catalog argument may be nil (some embedded test setups), in
// which case the function returns nil without doing any I/O.
//
// dataDir, when non-empty, re-creates base/<oid>/ (+ PG_VERSION) for every
// replayed CREATE DATABASE record (M0122-0007 physical-storage-isolation
// slice 2) — CreatePerDatabaseScaffolding is idempotent, so this is safe
// whether or not the directory already survived the crash. DROP DATABASE
// replay does not remove the directory (physical directory removal on
// DROP is a separate, not-yet-implemented slice — see the deferral
// ledger); an empty orphaned base/<oid> from a dropped database is
// harmless since nothing routes relation I/O through it yet.
func replayDatabaseDDLRecords(walDir string, cat catalog.Catalog, dataDir string) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(databaseRegistryRecovery)
	if !ok {
		// Catalog implementation does not expose the recovery hooks;
		// nothing to do. (catalog.InMemory does expose them.)
		return nil
	}
	if _, err := os.Stat(walDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat wal dir: %w", err)
	}

	records, err := wal.ReadAll(walDir, 0)
	if err != nil {
		return fmt.Errorf("read wal: %w", err)
	}

	for _, rec := range records {
		if len(rec.Payload) == 0 {
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateDatabase:
			name, owner, oid, derr := wal.DecodeCreateDatabase(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-database at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterDatabaseDuringRecovery(name, owner, oid)
			if dataDir != "" && oid != 0 {
				if err := CreatePerDatabaseScaffolding(dataDir, oid); err != nil {
					return fmt.Errorf("recreate base/%d for %q at lsn %d: %w", oid, name, rec.StartLSN, err)
				}
			}
		case wal.RecordKindDropDatabase:
			name, derr := wal.DecodeDropDatabase(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-database at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.UnregisterDatabaseDuringRecovery(name)
		}
	}
	return nil
}
