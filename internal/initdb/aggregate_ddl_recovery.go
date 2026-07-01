package initdb

// CREATE/ALTER/DROP AGGREGATE WAL replay (DU-002 restart-persistence
// follow-up to M0119-0004, slice 405 ledger resume point (c); DROP added in
// loop #56's ledger row resume point; ALTER ... OWNER TO added in loop #57's
// ledger row resume point).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the CREATE/ALTER/
// DROP AGGREGATE record kinds (46, 47, 48, and 49) because goopg has no
// per-aggregate file namespace — there is no page-level state to
// reconstruct. The catalog's user-aggregate registry, however, is the
// in-memory source of truth that backs the pg_aggregate/pg_proc virtual
// views (pg_dump's getAggregates/dumpAgg) AND the planner's
// isUserAggregateFunc lookup used at query execution time. This recovery
// pass walks the WAL once after physical replay finishes, decodes each
// CREATE/ALTER/DROP AGGREGATE record, and applies it to the catalog so the
// post-restart server agrees with what the pre-crash server told the
// client. Mirrors replayCastDDLRecords. Unlike a collation/conversion, a
// user aggregate has no Schema field yet (see the slice 405 ledger row's
// resume point (a)), so this does not depend on replaySchemaDDLRecords
// having run first.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// aggregateRegistryRecovery is the catalog-side surface this recovery pass
// needs. `*catalog.InMemory` satisfies it.
type aggregateRegistryRecovery interface {
	RegisterUserAggregateDuringRecovery(agg *catalog.UserAggregate)
	RenameUserAggregateDuringRecovery(oldName, newName string)
	DropUserAggregateDuringRecovery(name string)
	SetUserAggregateOwnerDuringRecovery(name string, ownerOID uint32)
}

// replayAggregateDDLRecords reads every WAL record under walDir and applies
// CREATE / ALTER AGGREGATE entries to the catalog. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. The catalog argument
// may be nil (some embedded test setups), in which case the function returns
// nil without doing any I/O.
func replayAggregateDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(aggregateRegistryRecovery)
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
		case wal.RecordKindCreateAggregate:
			name, sType, sFunc, finalFunc, combineFunc, initCond, finalFuncModify, argTypes, oid, sFuncStrict, variadic, derr := wal.DecodeCreateAggregate(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-aggregate at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterUserAggregateDuringRecovery(&catalog.UserAggregate{
				OID:             oid,
				Name:            name,
				ArgTypes:        argTypes,
				SType:           sType,
				SFunc:           sFunc,
				FinalFunc:       finalFunc,
				CombineFunc:     combineFunc,
				InitCond:        initCond,
				FinalFuncModify: finalFuncModify,
				SFuncStrict:     sFuncStrict,
				Variadic:        variadic,
			})
		case wal.RecordKindAlterAggregateRename:
			name, newName, derr := wal.DecodeAlterAggregateRename(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-aggregate-rename at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RenameUserAggregateDuringRecovery(name, newName)
		case wal.RecordKindDropAggregate:
			name, derr := wal.DecodeDropAggregate(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-aggregate at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.DropUserAggregateDuringRecovery(name)
		case wal.RecordKindAlterAggregateOwner:
			name, ownerOID, derr := wal.DecodeAlterAggregateOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-aggregate-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.SetUserAggregateOwnerDuringRecovery(name, ownerOID)
		}
	}
	return nil
}
