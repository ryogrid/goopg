package initdb

// CREATE/DROP/ALTER EVENT TRIGGER WAL replay (DU-002 restart-persistence
// follow-up to M0119-0004, loop #70 ledger resume point).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the event
// trigger record kinds (56-60) because goopg has no per-event-trigger file
// namespace — catalog.InMemory's eventTriggers map is a pure in-memory
// registry with no page-level state to reconstruct. It is, however, the
// source of truth behind the pg_event_trigger virtual view (pg_dump's
// getEventTriggers), so losing it on every restart is a visible
// regression. This recovery pass walks the WAL once after physical replay
// finishes, decodes each event trigger DDL record, and applies it to the
// catalog so the post-restart server agrees with what the pre-crash server
// told the client. Mirrors replayPubSubDDLRecords
// (internal/initdb/pubsub_ddl_recovery.go); event triggers, like
// publications/subscriptions, are not schema-scoped (keyed by name only),
// so this does not depend on schema replay having run first.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// replayEventTriggerDDLRecords reads every WAL record under walDir and
// applies CREATE/DROP/ALTER EVENT TRIGGER entries to cat. A missing walDir
// means "freshly initdb'd cluster" and is treated as a no-op. cat may be
// nil (some embedded test setups), in which case the function returns nil
// without doing any I/O.
func replayEventTriggerDDLRecords(walDir string, cat *catalog.InMemory) error {
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
		case wal.RecordKindCreateEventTrigger:
			name, event, tags, oid, ownerOID, funcOID, derr := wal.DecodeCreateEventTrigger(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-event-trigger at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RegisterEventTriggerDuringRecovery(&catalog.EventTrigger{
				Name:    name,
				OID:     oid,
				Event:   event,
				Owner:   ownerOID,
				FuncOID: funcOID,
				Enabled: "O",
				Tags:    tags,
			})
		case wal.RecordKindDropEventTrigger:
			name, derr := wal.DecodeDropEventTrigger(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-event-trigger at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.DropEventTriggerDuringRecovery(name)
		case wal.RecordKindAlterEventTriggerEnabled:
			name, code, derr := wal.DecodeAlterEventTriggerEnabled(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-event-trigger-enabled at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.SetEventTriggerEnabledDuringRecovery(name, string(code))
		case wal.RecordKindAlterEventTriggerRename:
			name, newName, derr := wal.DecodeAlterEventTriggerRename(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-event-trigger-rename at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.RenameEventTriggerDuringRecovery(name, newName)
		case wal.RecordKindAlterEventTriggerOwner:
			name, ownerOID, derr := wal.DecodeAlterEventTriggerOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-event-trigger-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			cat.SetEventTriggerOwnerDuringRecovery(name, ownerOID)
		}
	}
	return nil
}
