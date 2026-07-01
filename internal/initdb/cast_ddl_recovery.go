package initdb

// CREATE/DROP CAST WAL replay (DU-002 restart-persistence follow-up to
// M0119-0004).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// CAST record kinds (38 and 39) because goopg has no per-cast file
// namespace — there is no page-level state to reconstruct. The catalog's
// cast registry, however, is the in-memory source of truth that backs the
// pg_cast virtual view (pg_dump's getCasts/dumpCast). This recovery pass
// walks the WAL once after physical replay finishes, decodes each
// CREATE/DROP CAST record, and applies it to the catalog so the
// post-restart server agrees with what the pre-crash server told the
// client. Mirrors replayTransformDDLRecords exactly.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// castRegistryRecovery is the catalog-side surface this recovery pass needs.
// `*catalog.InMemory` satisfies it.
type castRegistryRecovery interface {
	RegisterCastDuringRecovery(source, target, context, method string, oid, funcOID uint32)
	DropCastDuringRecovery(source, target string)
}

// replayCastDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP CAST entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayCastDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(castRegistryRecovery)
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
		case wal.RecordKindCreateCast:
			source, target, context, method, oid, funcOID, derr := wal.DecodeCreateCast(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-cast at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterCastDuringRecovery(source, target, context, method, oid, funcOID)
		case wal.RecordKindDropCast:
			source, target, derr := wal.DecodeDropCast(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-cast at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropCastDuringRecovery(source, target)
		}
	}
	return nil
}
