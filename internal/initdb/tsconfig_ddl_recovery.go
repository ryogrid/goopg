package initdb

// CREATE TEXT SEARCH CONFIGURATION / ADD MAPPING / DROP WAL replay (DU-002
// restart-persistence follow-up to slice 446, M0119-0004).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the
// CREATE/ADD-MAPPING/DROP TEXT SEARCH CONFIGURATION record kinds (106, 107,
// 108) because goopg has no per-configuration file namespace — there is no
// page-level state to reconstruct. The catalog's configuration registry,
// however, is the in-memory source of truth that backs the pg_ts_config /
// pg_ts_config_map virtual views (pg_dump's getTSConfigurations/
// dumpTSConfig). This recovery pass walks the WAL once after physical replay
// finishes, decodes each record, and applies it to the catalog so the
// post-restart server agrees with what the pre-crash server told the
// client. Mirrors replayTSDictDDLRecords; a configuration is schema-scoped,
// so the caller must run this after replaySchemaDDLRecords. A configuration's
// ADD MAPPING records are replayed in WAL order after its own CREATE record
// (WAL is scanned in order, so this is guaranteed without extra bookkeeping).

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// tsConfigRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type tsConfigRegistryRecovery interface {
	CreateTSConfigDuringRecovery(uc *catalog.UserTSConfig, schema string)
	AddTSConfigMappingDuringRecovery(name, schema, tokenType string, dictOIDs []uint32)
	DropTSConfigDuringRecovery(name, schema string)
}

// replayTSConfigDDLRecords reads every WAL record under walDir and applies
// CREATE / ADD MAPPING / DROP TEXT SEARCH CONFIGURATION entries to the
// catalog. A missing walDir means "freshly initdb'd cluster" and is treated
// as a no-op. The catalog argument may be nil (some embedded test setups),
// in which case the function returns nil without doing any I/O.
func replayTSConfigDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(tsConfigRegistryRecovery)
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
			// See replayConversionDDLRecords for why non-goopg-native
			// records are skipped before matching against the small custom
			// RecordKind constants (M0106-0011).
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateTSConfig:
			name, schema, oid, ownerOID, parser, derr := wal.DecodeCreateTSConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-tsconfig at lsn %d: %w", rec.StartLSN, derr)
			}
			uc := &catalog.UserTSConfig{
				OID:    oid,
				Name:   name,
				Owner:  ownerOID,
				Parser: parser,
			}
			reg.CreateTSConfigDuringRecovery(uc, schema)
		case wal.RecordKindAddTSConfigMapping:
			name, schema, tokenType, dictOIDs, derr := wal.DecodeAddTSConfigMapping(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode add-tsconfig-mapping at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.AddTSConfigMappingDuringRecovery(name, schema, tokenType, dictOIDs)
		case wal.RecordKindDropTSConfig:
			name, schema, derr := wal.DecodeDropTSConfig(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-tsconfig at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropTSConfigDuringRecovery(name, schema)
		}
	}
	return nil
}
