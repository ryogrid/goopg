package initdb

// ALTER DATABASE ... SET/RESET WAL replay (M0119-0004-ACLHEAP, ALTER
// DATABASE ... SET follow-up).
//
// Mirrors database_ddl_recovery.go's CREATE/DROP DATABASE pattern: physical
// WAL replay ignores these record kinds (they carry only
// catalog.InMemory.dbRoleSettings registry state, no on-disk page data), so
// this recovery pass walks the WAL once after physical replay finishes and
// re-applies each ALTER DATABASE ... SET/RESET/RESET ALL record so
// pg_db_role_setting reflects the pre-crash state after a restart.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// databaseConfigRegistryRecovery is the catalog-side surface this recovery
// pass needs. `*catalog.InMemory` satisfies it.
type databaseConfigRegistryRecovery interface {
	SetDatabaseConfig(dbOid uint32, name, value string)
	ResetDatabaseConfig(dbOid uint32, name string)
	ResetAllDatabaseConfig(dbOid uint32)
}

// replayDatabaseConfigRecords reads every WAL record under walDir and
// applies ALTER DATABASE ... SET/RESET/RESET ALL entries to the catalog. A
// missing walDir means "freshly initdb'd cluster" and is treated as a no-op.
// The catalog argument may be nil (some embedded test setups), in which
// case the function returns nil without doing any I/O.
func replayDatabaseConfigRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(databaseConfigRegistryRecovery)
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
		case wal.RecordKindAlterDatabaseSetConfig:
			dbOid, name, value, derr := wal.DecodeAlterDatabaseSetConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-database-set-config at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetDatabaseConfig(dbOid, name, value)
		case wal.RecordKindAlterDatabaseResetConfig:
			dbOid, name, derr := wal.DecodeAlterDatabaseResetConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-database-reset-config at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.ResetDatabaseConfig(dbOid, name)
		case wal.RecordKindAlterDatabaseResetAllConfig:
			dbOid, derr := wal.DecodeAlterDatabaseResetAllConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-database-reset-all-config at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.ResetAllDatabaseConfig(dbOid)
		}
	}
	return nil
}
