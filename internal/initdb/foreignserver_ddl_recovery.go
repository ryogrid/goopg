package initdb

// CREATE/DROP SERVER WAL replay (M0122-0007 foreign-server registry
// restart-durability follow-up).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// SERVER record kinds (126 and 127) because goopg's foreign-server registry
// (`catalog.InMemory.foreignServers`) has no backing heap relation — there is
// no page-level state to reconstruct. Without this pass, a foreign server
// created via `CREATE SERVER myserver FOREIGN DATA WRAPPER my_fdw` vanished
// from `pg_foreign_server` entirely after a restart, orphaning any dependent
// user mapping's `srvid` reference. This recovery pass walks the WAL once
// after physical replay finishes, decodes each CREATE/DROP SERVER record, and
// applies it to the catalog so the post-restart server agrees with what the
// pre-crash server told the client. Mirrors replayTablespaceDDLRecords
// exactly.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// foreignServerRegistryRecovery is the catalog-side surface this recovery
// pass needs. `*catalog.InMemory` satisfies it.
type foreignServerRegistryRecovery interface {
	RegisterForeignServerDuringRecovery(name, fdwName, srvType, srvVersion string, options []string, oid uint32, dbOid ...uint32)
	UnregisterForeignServerDuringRecovery(name string, dbOid ...uint32)
}

// replayForeignServerDDLRecords reads every WAL record under walDir and
// applies CREATE / DROP SERVER entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayForeignServerDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(foreignServerRegistryRecovery)
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
		case wal.RecordKindCreateForeignServer:
			name, fdwName, srvType, srvVersion, options, oid, dbOid, derr := wal.DecodeCreateForeignServer(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-foreign-server at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterForeignServerDuringRecovery(name, fdwName, srvType, srvVersion, options, oid, catalog.NamespaceDBOid(dbOid))
		case wal.RecordKindDropForeignServer:
			name, dbOid, derr := wal.DecodeDropForeignServer(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-foreign-server at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.UnregisterForeignServerDuringRecovery(name, catalog.NamespaceDBOid(dbOid))
		}
	}
	return nil
}
