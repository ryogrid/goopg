package initdb

// CREATE/DROP/RENAME/SET SCHEMA/options TEXT SEARCH DICTIONARY WAL replay
// (DU-002 restart-persistence follow-up to slice 437, extended by the ALTER
// TEXT SEARCH DICTIONARY follow-up, M0119-0004).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores all of this
// file's TEXT SEARCH DICTIONARY record kinds (104/105 CREATE/DROP, 114/115/
// 116 RENAME/SET SCHEMA/options) because goopg has no per-dictionary file
// namespace — there is no page-level state to reconstruct. The catalog's
// dictionary registry, however, is the in-memory source of truth that backs
// the pg_ts_dict virtual view (pg_dump's getTSDictionaries/
// dumpTSDictionary). This recovery pass walks the WAL once after physical
// replay finishes, decodes each record, and applies it to the catalog so
// the post-restart server agrees with what the pre-crash server told the
// client. Mirrors replayConversionDDLRecords, except a dictionary is
// schema-scoped (keyed by (namespace OID, name) via
// catalog.InMemory.CreateTSDictDuringRecovery resolving `schema` through the
// live schema map), so the caller must run this after
// replaySchemaDDLRecords.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// tsDictRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type tsDictRegistryRecovery interface {
	CreateTSDictDuringRecovery(ud *catalog.UserTSDict, schema string)
	DropTSDictDuringRecovery(name, schema string)
	RenameTSDictDuringRecovery(name, schema, newName string)
	SetTSDictSchemaDuringRecovery(name, schema, newSchema string)
	AlterTSDictOptionsDuringRecovery(name, schema, initOption string)
}

// replayTSDictDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP TEXT SEARCH DICTIONARY entries to the catalog. A missing
// walDir means "freshly initdb'd cluster" and is treated as a no-op. The
// catalog argument may be nil (some embedded test setups), in which case the
// function returns nil without doing any I/O.
func replayTSDictDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(tsDictRegistryRecovery)
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
			// records (e.g. a real PG-canonical XLogRecord) are skipped
			// before matching against the small custom RecordKind
			// constants (M0106-0011).
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateTSDict:
			name, schema, initOption, oid, ownerOID, template, derr := wal.DecodeCreateTSDict(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-tsdict at lsn %d: %w", rec.StartLSN, derr)
			}
			ud := &catalog.UserTSDict{
				OID:        oid,
				Name:       name,
				Owner:      ownerOID,
				Template:   template,
				InitOption: initOption,
			}
			reg.CreateTSDictDuringRecovery(ud, schema)
		case wal.RecordKindDropTSDict:
			name, schema, derr := wal.DecodeDropTSDict(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-tsdict at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropTSDictDuringRecovery(name, schema)
		case wal.RecordKindRenameTSDict:
			name, schema, newName, derr := wal.DecodeRenameTSDict(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode rename-tsdict at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RenameTSDictDuringRecovery(name, schema, newName)
		case wal.RecordKindSetTSDictSchema:
			name, schema, newSchema, derr := wal.DecodeSetTSDictSchema(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode set-tsdict-schema at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetTSDictSchemaDuringRecovery(name, schema, newSchema)
		case wal.RecordKindAlterTSDictOptions:
			name, schema, initOption, derr := wal.DecodeAlterTSDictOptions(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-tsdict-options at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.AlterTSDictOptionsDuringRecovery(name, schema, initOption)
		}
	}
	return nil
}
