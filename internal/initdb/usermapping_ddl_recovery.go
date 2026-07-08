package initdb

// CREATE/DROP USER MAPPING WAL replay (M0122-0007 user-mapping registry
// restart-durability follow-up — the resume point named in the
// foreign-server registry's own restart-durability ledger row).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// USER MAPPING record kinds (128 and 129) because goopg's user-mapping
// registry (`catalog.InMemory.userMappings`) has no backing heap relation —
// there is no page-level state to reconstruct. Without this pass, a mapping
// created via `CREATE USER MAPPING FOR someuser SERVER myserver` vanished
// from `pg_user_mappings` after a restart even though its referenced server
// (now durable, see foreignserver_ddl_recovery.go) survived. This recovery
// pass walks the WAL once after physical replay finishes, decodes each
// CREATE/DROP USER MAPPING record, and applies it to the catalog. Mirrors
// replayForeignServerDDLRecords exactly.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// userMappingRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type userMappingRegistryRecovery interface {
	RegisterUserMappingDuringRecovery(user, server string, options []string, oid uint32)
	UnregisterUserMappingDuringRecovery(user, server string)
}

// replayUserMappingDDLRecords reads every WAL record under walDir and
// applies CREATE / DROP USER MAPPING entries to the catalog. A missing
// walDir means "freshly initdb'd cluster" and is treated as a no-op. The
// catalog argument may be nil (some embedded test setups), in which case
// the function returns nil without doing any I/O.
func replayUserMappingDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(userMappingRegistryRecovery)
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
		if len(rec.Payload) == 0 || !wal.IsGoopgNativeRecord(rec) {
			// See replaySchemaDDLRecords's identical guard: rules out a
			// PG-native/canonical XLogRecord whose MainData could
			// coincidentally byte-match a small RecordKind constant.
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateUserMapping:
			user, server, options, oid, derr := wal.DecodeCreateUserMapping(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-user-mapping at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterUserMappingDuringRecovery(user, server, options, oid)
		case wal.RecordKindDropUserMapping:
			user, server, derr := wal.DecodeDropUserMapping(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-user-mapping at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.UnregisterUserMappingDuringRecovery(user, server)
		}
	}
	return nil
}
