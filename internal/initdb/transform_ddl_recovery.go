package initdb

// CREATE/DROP TRANSFORM WAL replay (M0119-0004 restart persistence).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// TRANSFORM record kinds (36 and 37) because goopg has no per-transform file
// namespace — there is no page-level state to reconstruct. The catalog's
// transform registry, however, is the in-memory source of truth that backs
// the pg_transform virtual view (pg_dump's getTransforms/dumpTransform).
// This recovery pass walks the WAL once after physical replay finishes,
// decodes each CREATE/DROP TRANSFORM record, and applies it to the catalog
// so the post-restart server agrees with what the pre-crash server told the
// client. Mirrors replaySchemaDDLRecords exactly.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// transformRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type transformRegistryRecovery interface {
	RegisterTransformDuringRecovery(typeName, lang string, oid, fromFuncOID, toFuncOID uint32)
	DropTransformDuringRecovery(typeName, lang string)
}

// replayTransformDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP TRANSFORM entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayTransformDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(transformRegistryRecovery)
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
		case wal.RecordKindCreateTransform:
			typeName, lang, oid, fromFuncOID, toFuncOID, derr := wal.DecodeCreateTransform(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-transform at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterTransformDuringRecovery(typeName, lang, oid, fromFuncOID, toFuncOID)
		case wal.RecordKindDropTransform:
			typeName, lang, derr := wal.DecodeDropTransform(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-transform at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropTransformDuringRecovery(typeName, lang)
		}
	}
	return nil
}
