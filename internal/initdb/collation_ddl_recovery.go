package initdb

// CREATE/DROP COLLATION WAL replay (DU-002 restart-persistence follow-up to
// M0119-0004).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/DROP
// COLLATION record kinds (42 and 43) because goopg has no per-collation file
// namespace — there is no page-level state to reconstruct. The catalog's
// collation registry, however, is the in-memory source of truth that backs
// the pg_collation virtual view (pg_dump's getCollations/dumpCollation).
// This recovery pass walks the WAL once after physical replay finishes,
// decodes each CREATE/DROP COLLATION record, and applies it to the catalog
// so the post-restart server agrees with what the pre-crash server told the
// client. Mirrors replayConversionDDLRecords, except like a conversion (and
// unlike a cast/transform) a collation is schema-scoped (keyed by (namespace
// OID, name) via catalog.InMemory.CreateCollationDuringRecovery resolving
// `schema` through the live schema map), so the caller must run this after
// replaySchemaDDLRecords.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// collationRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type collationRegistryRecovery interface {
	CreateCollationDuringRecovery(uc *catalog.UserCollation, schema string)
	DropCollationDuringRecovery(name, schema string)
	RenameCollationDuringRecovery(name, schema, newName string)
	SetCollationOwnerDuringRecovery(name, schema string, ownerOID uint32)
	SetCollationSchemaDuringRecovery(name, schema, newSchema string)
}

// replayCollationDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP COLLATION entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayCollationDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(collationRegistryRecovery)
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
		case wal.RecordKindCreateCollation:
			name, schema, collate, ctype, locale, rules, oid, ownerOID, encoding, provider, deterministic, derr := wal.DecodeCreateCollation(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-collation at lsn %d: %w", rec.StartLSN, derr)
			}
			uc := &catalog.UserCollation{
				OID:           oid,
				Name:          name,
				Owner:         ownerOID,
				Provider:      provider,
				Encoding:      int(encoding),
				Collate:       collate,
				Ctype:         ctype,
				Locale:        locale,
				Rules:         rules,
				Deterministic: deterministic,
			}
			reg.CreateCollationDuringRecovery(uc, schema)
		case wal.RecordKindDropCollation:
			name, schema, derr := wal.DecodeDropCollation(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-collation at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropCollationDuringRecovery(name, schema)
		case wal.RecordKindAlterCollationRename:
			name, schema, newName, derr := wal.DecodeAlterCollationRename(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-collation-rename at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RenameCollationDuringRecovery(name, schema, newName)
		case wal.RecordKindAlterCollationOwner:
			name, schema, ownerOID, derr := wal.DecodeAlterCollationOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-collation-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetCollationOwnerDuringRecovery(name, schema, ownerOID)
		case wal.RecordKindAlterCollationSetSchema:
			name, schema, newSchema, derr := wal.DecodeAlterCollationSetSchema(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-collation-set-schema at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetCollationSchemaDuringRecovery(name, schema, newSchema)
		}
	}
	return nil
}
