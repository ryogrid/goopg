package initdb

// CREATE/DROP CONVERSION WAL replay (DU-002 restart-persistence follow-up to
// M0119-0004).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// CONVERSION record kinds (40 and 41) because goopg has no per-conversion
// file namespace — there is no page-level state to reconstruct. The
// catalog's conversion registry, however, is the in-memory source of truth
// that backs the pg_conversion virtual view (pg_dump's
// getConversions/dumpConversion). This recovery pass walks the WAL once
// after physical replay finishes, decodes each CREATE/DROP CONVERSION
// record, and applies it to the catalog so the post-restart server agrees
// with what the pre-crash server told the client. Mirrors
// replayCastDDLRecords, except a conversion is schema-scoped (keyed by
// (namespace OID, name) via catalog.InMemory.CreateConversionDuringRecovery
// resolving `schema` through the live schema map), so the caller must run
// this after replaySchemaDDLRecords.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// conversionRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type conversionRegistryRecovery interface {
	CreateConversionDuringRecovery(uc *catalog.UserConversion, schema string)
	DropConversionDuringRecovery(name, schema string)
}

// replayConversionDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP CONVERSION entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayConversionDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(conversionRegistryRecovery)
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
		case wal.RecordKindCreateConversion:
			name, schema, procSchema, procName, oid, ownerOID, funcOID, forEncoding, toEncoding, isDefault, derr := wal.DecodeCreateConversion(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-conversion at lsn %d: %w", rec.StartLSN, derr)
			}
			uc := &catalog.UserConversion{
				OID:         oid,
				Name:        name,
				Owner:       ownerOID,
				ForEncoding: forEncoding,
				ToEncoding:  toEncoding,
				ProcSchema:  procSchema,
				ProcName:    procName,
				FuncOID:     funcOID,
				Default:     isDefault,
			}
			reg.CreateConversionDuringRecovery(uc, schema)
		case wal.RecordKindDropConversion:
			name, schema, derr := wal.DecodeDropConversion(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-conversion at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropConversionDuringRecovery(name, schema)
		}
	}
	return nil
}
