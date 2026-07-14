package initdb

// CREATE/DROP TABLESPACE WAL replay (M0122-0007 tablespace-registry
// restart-durability follow-up).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// TABLESPACE record kinds (124 and 125) because goopg's tablespace registry
// (`catalog.InMemory.tablespaces`) has no backing heap relation — there is no
// page-level state to reconstruct. Without this pass, a tablespace created
// via `CREATE TABLESPACE ts1 LOCATION ''` vanished from `pg_tablespace`
// entirely after a restart even though its `pg_tblspc/<oid>/` directory
// stayed on disk, orphaning any table/index whose now-durable
// `reltablespace` OID pointed at it. This recovery pass walks the WAL once
// after physical replay finishes, decodes each CREATE/DROP TABLESPACE
// record, and applies it to the catalog so the post-restart server agrees
// with what the pre-crash server told the client. Mirrors
// replaySchemaDDLRecords exactly.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// tablespaceRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type tablespaceRegistryRecovery interface {
	RegisterTablespaceDuringRecovery(name, owner, location string, oid uint32)
	UnregisterTablespaceDuringRecovery(name string)
}

// replayTablespaceDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP TABLESPACE entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayTablespaceDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(tablespaceRegistryRecovery)
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
		case wal.RecordKindCreateTablespace:
			name, owner, location, oid, derr := wal.DecodeCreateTablespace(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-tablespace at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterTablespaceDuringRecovery(name, owner, location, oid)
		case wal.RecordKindDropTablespace:
			name, derr := wal.DecodeDropTablespace(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-tablespace at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.UnregisterTablespaceDuringRecovery(name)
		}
	}
	return nil
}
