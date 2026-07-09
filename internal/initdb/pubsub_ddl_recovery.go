package initdb

// CREATE/DROP/ALTER PUBLICATION/SUBSCRIPTION WAL replay (DU-002
// restart-persistence follow-up to M0119-0004, loop #67 ledger resume
// point).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the
// publication/subscription record kinds (50-55) because goopg has no
// per-publication/subscription file namespace — catalog.PubSub is a pure
// in-memory registry with no page-level state to reconstruct. It is,
// however, the source of truth behind the pg_publication/pg_subscription
// virtual views (pg_dump's getPublications/getSubscriptions) and the
// walsender's publication filter, so losing it on every restart is a
// visible regression. This recovery pass walks the WAL once after physical
// replay finishes, decodes each publication/subscription DDL record, and
// applies it to the PubSub registry so the post-restart server agrees with
// what the pre-crash server told the client. Unlike collation/conversion,
// PubSub is not schema-scoped (keyed by name only, like a cast/transform),
// so this does not depend on schema replay having run first. Mirrors
// replayCollationDDLRecords/replayAggregateDDLRecords, but takes the
// concrete *catalog.PubSub directly (PubSub, unlike catalog.Catalog, has
// only one implementation, so the interface-assertion indirection those
// other recovery drivers use is unnecessary here).

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// replayPubSubDDLRecords reads every WAL record under walDir and applies
// CREATE/DROP/ALTER PUBLICATION/SUBSCRIPTION entries to pubsub. A missing
// walDir means "freshly initdb'd cluster" and is treated as a no-op. pubsub
// may be nil (some embedded test setups), in which case the function
// returns nil without doing any I/O.
func replayPubSubDDLRecords(walDir string, pubsub *catalog.PubSub) error {
	if pubsub == nil {
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
		case wal.RecordKindCreatePublication:
			name, tables, oid, ownerOID, allTables, publishInsert, publishUpdate, publishDelete, derr := wal.DecodeCreatePublication(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-publication at lsn %d: %w", rec.StartLSN, derr)
			}
			pubsub.CreatePublicationDuringRecovery(&catalog.Publication{
				Name:          name,
				OID:           oid,
				Owner:         ownerOID,
				AllTables:     allTables,
				PublishInsert: publishInsert,
				PublishUpdate: publishUpdate,
				PublishDelete: publishDelete,
				Tables:        tables,
			})
		case wal.RecordKindDropPublication:
			name, derr := wal.DecodeDropPublication(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-publication at lsn %d: %w", rec.StartLSN, derr)
			}
			pubsub.DropPublicationDuringRecovery(name)
		case wal.RecordKindAlterPublicationOwner:
			name, ownerOID, derr := wal.DecodeAlterPublicationOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-publication-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			pubsub.SetPublicationOwnerDuringRecovery(name, ownerOID)
		case wal.RecordKindCreateSubscription:
			name, conninfo, slotName, publications, oid, ownerOID, enabled, dbOid, derr := wal.DecodeCreateSubscription(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-subscription at lsn %d: %w", rec.StartLSN, derr)
			}
			pubsub.CreateSubscriptionDuringRecovery(&catalog.Subscription{
				Name:         name,
				OID:          oid,
				Conninfo:     conninfo,
				Owner:        ownerOID,
				Publications: publications,
				Enabled:      enabled,
				SlotName:     slotName,
				DBOid:        dbOid,
			})
		case wal.RecordKindDropSubscription:
			name, derr := wal.DecodeDropSubscription(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-subscription at lsn %d: %w", rec.StartLSN, derr)
			}
			pubsub.DropSubscriptionDuringRecovery(name)
		case wal.RecordKindAlterSubscriptionOwner:
			name, ownerOID, derr := wal.DecodeAlterSubscriptionOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-subscription-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			pubsub.SetSubscriptionOwnerDuringRecovery(name, ownerOID)
		}
	}
	return nil
}
