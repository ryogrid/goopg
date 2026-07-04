package initdb

// CREATE/DROP STATISTICS WAL replay (DU-002 restart-persistence follow-up to
// slice 441). Mirrors replayCollationDDLRecords / replayAccessMethodDDLRecords:
// goopg has no per-statistics-object page-level state, so physical WAL replay
// ignores record kinds 95/96 and this logical pass reapplies them to the
// catalog after physical replay finishes. Statistics objects reference a
// table (stxrelid) purely for informational/lookup purposes — the catalog
// stores the recorded TableOID verbatim rather than re-resolving it by name,
// so this can run any time after schema replay; it is placed after
// loadUserTablesFromHeap in open.go purely to group it with the other
// DU-002 name-keyed DDL replay passes (access method, function, range type),
// not because of a real ordering dependency.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// statisticsRegistryRecovery is the catalog-side surface this recovery pass
// needs. *catalog.InMemory satisfies it.
type statisticsRegistryRecovery interface {
	RegisterStatisticsDuringRecovery(schema, name string, oid, tableOID, ownerOID uint32, kinds, columns, exprs []string, hasExpr bool)
	DropStatisticsDuringRecovery(name, schema string)
	RenameStatisticsObjectDuringRecovery(name, newName string)
	SetStatisticsOwnerDuringRecovery(name string, ownerOID uint32)
	SetStatisticsSchemaDuringRecovery(name, newSchema string)
}

// replayStatisticsDDLRecords reads every WAL record under walDir and applies
// CREATE / DROP STATISTICS entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayStatisticsDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(statisticsRegistryRecovery)
	if !ok {
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
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateStatistics:
			name, schema, oid, tableOID, ownerOID, kinds, columns, exprs, hasExpr, derr := wal.DecodeCreateStatistics(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-statistics at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterStatisticsDuringRecovery(schema, name, oid, tableOID, ownerOID, kinds, columns, exprs, hasExpr)
		case wal.RecordKindDropStatistics:
			name, schema, derr := wal.DecodeDropStatistics(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-statistics at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropStatisticsDuringRecovery(name, schema)
		case wal.RecordKindAlterStatisticsRename:
			name, newName, derr := wal.DecodeAlterStatisticsRename(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-statistics-rename at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RenameStatisticsObjectDuringRecovery(name, newName)
		case wal.RecordKindAlterStatisticsOwner:
			name, ownerOID, derr := wal.DecodeAlterStatisticsOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-statistics-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetStatisticsOwnerDuringRecovery(name, ownerOID)
		case wal.RecordKindAlterStatisticsSetSchema:
			name, newSchema, derr := wal.DecodeAlterStatisticsSetSchema(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-statistics-set-schema at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetStatisticsSchemaDuringRecovery(name, newSchema)
		}
	}
	return nil
}
