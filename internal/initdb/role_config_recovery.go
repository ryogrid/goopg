package initdb

// ALTER ROLE ... SET/RESET WAL replay (M0119-0004-ACLHEAP, ALTER ROLE ...
// SET follow-up).
//
// Mirrors database_config_recovery.go's ALTER DATABASE ... SET/RESET
// pattern: physical WAL replay ignores these record kinds (they carry only
// catalog.InMemory.roleSettings registry state, no on-disk page data), so
// this recovery pass walks the WAL once after physical replay finishes and
// re-applies each ALTER ROLE ... SET/RESET/RESET ALL record so
// pg_db_role_setting reflects the pre-crash state after a restart.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// roleConfigRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type roleConfigRegistryRecovery interface {
	SetRoleConfig(roleOid, dbOid uint32, name, value string)
	ResetRoleConfig(roleOid, dbOid uint32, name string)
	ResetAllRoleConfig(roleOid, dbOid uint32)
}

// replayRoleConfigRecords reads every WAL record under walDir and applies
// ALTER ROLE ... SET/RESET/RESET ALL entries to the catalog. A missing
// walDir means "freshly initdb'd cluster" and is treated as a no-op. The
// catalog argument may be nil (some embedded test setups), in which case
// the function returns nil without doing any I/O.
func replayRoleConfigRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(roleConfigRegistryRecovery)
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
		case wal.RecordKindAlterRoleSetConfig:
			roleOid, dbOid, name, value, derr := wal.DecodeAlterRoleSetConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-role-set-config at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetRoleConfig(roleOid, dbOid, name, value)
		case wal.RecordKindAlterRoleResetConfig:
			roleOid, dbOid, name, derr := wal.DecodeAlterRoleResetConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-role-reset-config at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.ResetRoleConfig(roleOid, dbOid, name)
		case wal.RecordKindAlterRoleResetAllConfig:
			roleOid, dbOid, derr := wal.DecodeAlterRoleResetAllConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-role-reset-all-config at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.ResetAllRoleConfig(roleOid, dbOid)
		}
	}
	return nil
}
