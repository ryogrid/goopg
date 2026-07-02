package initdb

// CREATE/DROP SCHEMA WAL replay (M0110-0003 schema durability).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// SCHEMA record kinds (34 and 35) because goopg has no per-schema file
// namespace — there is no page-level state to reconstruct. The catalog's
// schema registry, however, is the in-memory source of truth that backs
// pg_namespace lookups and schema-qualified relation resolution (e.g.
// `pg_amcheck --schema s1`). This recovery pass walks the WAL once after
// physical replay finishes, decodes each CREATE/DROP SCHEMA record, and
// applies it to the catalog so the post-restart server agrees with what the
// pre-crash server told the client. Mirrors replayDatabaseDDLRecords exactly.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// schemaRegistryRecovery is the catalog-side surface this recovery pass needs.
// `*catalog.InMemory` satisfies it.
type schemaRegistryRecovery interface {
	RegisterSchemaDuringRecovery(name string, oid uint32)
	UnregisterSchemaDuringRecovery(name string)
}

// replaySchemaDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP SCHEMA entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replaySchemaDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(schemaRegistryRecovery)
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
			// wal.IsGoopgNativeRecord rules out a PG-native/canonical
			// XLogRecord (e.g. a checkpoint written via the real rmgr/info
			// path for standby compatibility) whose MainData is raw PG
			// struct bytes with no goopg kind-byte tag at Payload[0].
			// Scanning it against the small custom RecordKind constants below
			// risks a coincidental false-positive byte match (M0106-0011) —
			// e.g. a checkpoint's redo LSN low byte landing on a real
			// RecordKind value — so skip these records entirely.
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateSchema:
			name, oid, derr := wal.DecodeCreateSchema(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-schema at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterSchemaDuringRecovery(name, oid)
		case wal.RecordKindDropSchema:
			name, derr := wal.DecodeDropSchema(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-schema at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.UnregisterSchemaDuringRecovery(name)
		}
	}
	return nil
}
