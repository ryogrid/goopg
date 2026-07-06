package initdb

// CREATE/DROP TYPE ... AS RANGE WAL replay (DU-002 restart-persistence
// follow-up to M0110-0001, DU-002 slice 429 ledger resume point, sub-item
// (c)).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the range type
// record kinds (81-82) because goopg has no per-range-type file namespace —
// catalog.InMemory's rangeTypes map is a pure in-memory registry with no
// page-level state to reconstruct (the registry only backs pg_dump
// round-trip fidelity). This recovery pass walks the WAL once after physical
// replay finishes, decodes each range type DDL record, and applies it to the
// catalog so the post-restart server agrees with what the pre-crash server
// told the client. Mirrors replayAccessMethodDDLRecords
// (internal/initdb/access_method_ddl_recovery.go); range types, like access
// methods, are not schema-scoped (keyed by name only), so this does not
// depend on schema replay having run first.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// replayRangeTypeDDLRecords reads every WAL record under walDir and applies
// CREATE/DROP TYPE ... AS RANGE entries to cat. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. cat may be nil (some
// embedded test setups), in which case the function returns nil without
// doing any I/O.
func replayRangeTypeDDLRecords(walDir string, cat *catalog.InMemory) error {
	if cat == nil {
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
			// XLogRecord whose MainData is raw PG struct bytes with no
			// goopg kind-byte tag at Payload[0] — scanning it against the
			// small custom RecordKind constants below risks a coincidental
			// false-positive byte match (M0106-0011), so skip these
			// records entirely.
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateRangeType:
			name, subtypeName, multirangeName, oid, arrayOID, multirangeOID, multirangeArrayOID, opclassOID, collationOID, ownerOID, derr := wal.DecodeCreateRangeType(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-range-type at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterRangeTypeDuringRecovery(&catalog.RangeType{
				Name:               name,
				OID:                oid,
				ArrayOID:           arrayOID,
				SubtypeName:        subtypeName,
				OpclassOID:         opclassOID,
				CollationOID:       collationOID,
				MultirangeOID:      multirangeOID,
				MultirangeArrayOID: multirangeArrayOID,
				MultirangeName:     multirangeName,
				Owner:              ownerOID,
			})
		case wal.RecordKindDropRangeType:
			name, derr := wal.DecodeDropRangeType(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-range-type at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.DropRangeTypeDuringRecovery(name)
		case wal.RecordKindAlterRangeTypeRename:
			name, newName, derr := wal.DecodeAlterRangeTypeRename(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-range-type-rename at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RenameRangeTypeDuringRecovery(name, newName)
		case wal.RecordKindAlterRangeTypeOwner:
			name, ownerOID, derr := wal.DecodeAlterRangeTypeOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-range-type-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.SetRangeTypeOwnerDuringRecovery(name, ownerOID)
		}
	}
	return nil
}
