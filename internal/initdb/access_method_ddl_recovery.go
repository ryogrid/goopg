package initdb

// CREATE/DROP ACCESS METHOD WAL replay (DU-002 restart-persistence
// follow-up to M0119-0004, DU-002 slice 426 ledger resume point).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the access
// method record kinds (70-71) because goopg has no per-access-method file
// namespace — catalog.InMemory's accessMethods map is a pure in-memory
// registry with no page-level state to reconstruct (goopg never invokes a
// user-defined AM's handler; the registry only backs pg_dump round-trip
// fidelity). This recovery pass walks the WAL once after physical replay
// finishes, decodes each access method DDL record, and applies it to the
// catalog so the post-restart server agrees with what the pre-crash server
// told the client. Mirrors replayEventTriggerDDLRecords
// (internal/initdb/event_trigger_ddl_recovery.go); access methods, like
// event triggers, are not schema-scoped (keyed by name only), so this does
// not depend on schema replay having run first.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// replayAccessMethodDDLRecords reads every WAL record under walDir and
// applies CREATE/DROP ACCESS METHOD entries to cat. A missing walDir means
// "freshly initdb'd cluster" and is treated as a no-op. cat may be nil (some
// embedded test setups), in which case the function returns nil without
// doing any I/O.
func replayAccessMethodDDLRecords(walDir string, cat *catalog.InMemory) error {
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
		case wal.RecordKindCreateAccessMethod:
			name, amType, oid, handlerOID, derr := wal.DecodeCreateAccessMethod(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-access-method at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterAccessMethodDuringRecovery(&catalog.AccessMethod{
				Name:       name,
				OID:        oid,
				AMType:     amType,
				HandlerOID: handlerOID,
			})
		case wal.RecordKindDropAccessMethod:
			name, derr := wal.DecodeDropAccessMethod(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-access-method at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.DropAccessMethodDuringRecovery(name)
		}
	}
	return nil
}
